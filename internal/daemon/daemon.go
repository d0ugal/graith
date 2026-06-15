package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/detector"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/sandbox"
)

const (
	gitFetchTimeout    = 2 * time.Minute
	gitUsernameTimeout = 15 * time.Second
)

type attachedClient struct {
	conn        net.Conn
	kick        func()
	sendControl func(msgType string, payload any)
}

// hookReport tracks the latest status report from an agent hook.
// This is runtime-only and NOT persisted to state.json.
type hookReport struct {
	Status             string
	Event              string
	ToolName           string
	Model              string
	CostUSD            *float64
	ContextPercent     *float64
	ReportedAt         time.Time
	AuthoritativeUntil time.Time
}

// SessionManager orchestrates PTY sessions, state persistence, and git worktrees.
type SessionManager struct {
	mu               sync.RWMutex
	state            *State
	sessions         map[string]*grpty.Session
	attachedClients  map[string]*attachedClient
	hookReports      map[string]hookReport
	pendingApprovals map[string]*pendingApproval
	cfg              *config.Config
	paths            config.Paths
	log              *slog.Logger
	configFile       string
	upgradeCh        chan string
	messages         *MsgStore
	mcpManager       *MCPManager
	startedAt        time.Time
}

// NewSessionManager creates a SessionManager with the given config and paths.
func NewSessionManager(cfg *config.Config, paths config.Paths, log *slog.Logger) *SessionManager {
	return &SessionManager{
		state:            NewState(),
		sessions:         make(map[string]*grpty.Session),
		attachedClients:  make(map[string]*attachedClient),
		hookReports:      make(map[string]hookReport),
		pendingApprovals: make(map[string]*pendingApproval),
		cfg:              cfg,
		paths:            paths,
		log:              log,
		startedAt:        time.Now(),
	}
}

func (sm *SessionManager) SetMsgStore(ms *MsgStore) {
	sm.messages = ms
}

func (sm *SessionManager) SetMCPManager(mm *MCPManager) {
	sm.mcpManager = mm
}

// HandleHookReport processes a status report from an agent hook, updating the
// in-memory hookReports map and the session's AgentStatus. This is the
// authoritative source of agent status when hooks are active.
func (sm *SessionManager) HandleHookReport(sr protocol.StatusReportMsg) {
	var status string
	var staleness time.Duration

	switch sr.Event {
	case "SessionStart":
		status = "active"
		staleness = 5 * time.Second
	case "UserPromptSubmit", "PreToolUse", "PostToolUse":
		status = "active"
		staleness = 30 * time.Second
	case "Notification", "PermissionRequest":
		status = "approval"
		staleness = 30 * time.Minute
	case "Stop":
		status = "ready"
		staleness = 30 * time.Minute
	default:
		sm.log.Info("ignoring unknown hook event", "event", sr.Event, "session_id", sr.SessionID)
		return
	}

	now := time.Now()
	report := hookReport{
		Status:             status,
		Event:              sr.Event,
		ToolName:           sr.ToolName,
		Model:              sr.Model,
		ReportedAt:         now,
		AuthoritativeUntil: now.Add(staleness),
	}

	var oldStatus string
	var name string
	var changed bool

	sm.mu.Lock()
	sess, ok := sm.state.Sessions[sr.SessionID]
	if !ok {
		sm.mu.Unlock()
		sm.log.Info("hook report for unknown session", "session_id", sr.SessionID)
		return
	}

	// Accumulate usage data — keep the latest non-nil values from previous reports.
	if sr.Usage != nil && sr.Usage.CostUSD != nil {
		report.CostUSD = sr.Usage.CostUSD
	} else if prev, ok := sm.hookReports[sr.SessionID]; ok && prev.CostUSD != nil {
		report.CostUSD = prev.CostUSD
	}
	if sr.Context != nil && sr.Context.Percent != nil {
		report.ContextPercent = sr.Context.Percent
	} else if prev, ok := sm.hookReports[sr.SessionID]; ok && prev.ContextPercent != nil {
		report.ContextPercent = prev.ContextPercent
	}
	if sr.Model != "" {
		report.Model = sr.Model
	} else if prev, ok := sm.hookReports[sr.SessionID]; ok && prev.Model != "" {
		report.Model = prev.Model
	}

	oldStatus = sess.AgentStatus
	name = sess.Name
	sm.hookReports[sr.SessionID] = report
	changed = oldStatus != status
	sess.AgentStatus = status
	if changed {
		sess.StatusChangedAt = time.Now()
	}
	sess.HookToolName = report.ToolName
	sess.HookModel = report.Model
	sess.HookCostUSD = report.CostUSD
	sess.HookContextPercent = report.ContextPercent
	sm.mu.Unlock()

	sm.log.Info("hook report processed",
		"session_id", sr.SessionID, "event", sr.Event,
		"status", status, "tool_name", sr.ToolName)

	if changed {
		sm.onAgentStatusChange(sr.SessionID, name, oldStatus, status)
	}
}

func (sm *SessionManager) KickAttachedClient(sessionID string) {
	sm.mu.Lock()
	ac, ok := sm.attachedClients[sessionID]
	if ok {
		delete(sm.attachedClients, sessionID)
	}
	sm.mu.Unlock()
	if ok {
		ac.kick()
	}
}

func (sm *SessionManager) SetAttachedClient(sessionID string, conn net.Conn, kick func(), sendCtrl func(string, any)) {
	sm.mu.Lock()
	sm.attachedClients[sessionID] = &attachedClient{conn: conn, kick: kick, sendControl: sendCtrl}
	sm.mu.Unlock()
}

func (sm *SessionManager) ClearAttachedClient(sessionID string, conn net.Conn) {
	sm.mu.Lock()
	if ac, ok := sm.attachedClients[sessionID]; ok && ac.conn == conn {
		delete(sm.attachedClients, sessionID)
	}
	sm.mu.Unlock()
}

func (sm *SessionManager) IsAttachedClient(sessionID string, conn net.Conn) bool {
	sm.mu.RLock()
	ac, ok := sm.attachedClients[sessionID]
	sm.mu.RUnlock()
	return ok && ac.conn == conn
}

// LoadState reads persisted state from disk and reconciles dead processes.
func (sm *SessionManager) LoadState() error {
	state, err := LoadState(sm.paths.StateFile)
	if err != nil {
		return err
	}
	state.Reconcile()
	sm.state = state
	return sm.saveState()
}

func (sm *SessionManager) AdoptSessions(manifest *UpgradeManifest) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for _, us := range manifest.Sessions {
		sessState, ok := sm.state.Sessions[us.ID]
		if !ok {
			sm.log.Warn("manifest references unknown session", "id", us.ID)
			continue
		}

		logPath := filepath.Join(sm.paths.LogDir, us.ID+".log")
		ptySess, err := grpty.AdoptSession(grpty.AdoptOpts{
			ID:         us.ID,
			Fd:         uintptr(us.Fd),
			PID:        us.PID,
			LogPath:    logPath,
			MaxLogSize: 100 * 1024 * 1024,
		})
		if err != nil {
			sm.log.Warn("failed to adopt session", "id", us.ID, "err", err)
			sessState.Status = StatusStopped
			sessState.StatusChangedAt = time.Now()
			continue
		}

		sm.sessions[us.ID] = ptySess
		go sm.watchSession(us.ID, ptySess)
		sm.log.Info("adopted session", "id", us.ID, "pid", us.PID)
	}

	return sm.saveState()
}

func (sm *SessionManager) saveState() error {
	return SaveState(sm.paths.StateFile, sm.state)
}

func generateID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func repoHash(repoPath string) string {
	h := uint64(0)
	for _, c := range repoPath {
		h = h*31 + uint64(c)
	}
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(h >> (i * 8))
	}
	return hex.EncodeToString(b)[:12]
}

// Create starts a new agent session, either in a git worktree, in-place
// in an existing repo, or as a standalone scratch session (when noRepo is true).
//
// The method uses three-phase locking to avoid holding the daemon mutex during
// potentially blocking git/network operations (fetch, GitHub API calls, PTY spawn):
//  1. Lock: validate, reserve session as StatusCreating, unlock
//  2. Git setup and PTY spawn (no lock held)
//  3. Lock: commit to StatusRunning, unlock
func (sm *SessionManager) Create(name, agentName, repoPath, baseBranch, prompt, model, parentID string, noRepo bool, shareWorktree string, agentHooks bool, inPlace, allowConcurrent bool, rows, cols uint16) (SessionState, error) {
	if err := ValidateSessionName(name); err != nil {
		return SessionState{}, err
	}

	agent, ok := sm.cfg.Agents[agentName]
	if !ok {
		return SessionState{}, fmt.Errorf("unknown agent %q", agentName)
	}

	if err := validateModel(agent, model); err != nil {
		return SessionState{}, err
	}

	// Early validation that doesn't require the lock.
	if inPlace && noRepo {
		return SessionState{}, fmt.Errorf("--in-place and --no-repo are mutually exclusive")
	}
	if inPlace && shareWorktree != "" {
		return SessionState{}, fmt.Errorf("--in-place and --share-worktree are mutually exclusive")
	}
	if inPlace && baseBranch != "" {
		return SessionState{}, fmt.Errorf("--in-place and --base are mutually exclusive (in-place sessions don't create branches)")
	}

	// --- Pre-lock: resolve repo root and discover GitHub username ---
	// These can involve network calls (gh api) and must not hold the mutex.
	var preRepoRoot string
	if !noRepo && shareWorktree == "" && repoPath != "" {
		if !git.IsInsideGitRepo(repoPath) {
			if inPlace {
				return SessionState{}, fmt.Errorf("not inside a git repository: %s", repoPath)
			}
			return SessionState{}, fmt.Errorf("not inside a git repository: %s (use --no-repo for sessions without a repo)", repoPath)
		}
		var err error
		preRepoRoot, err = git.RepoRootPath(repoPath)
		if err != nil {
			return SessionState{}, fmt.Errorf("find repo root: %w", err)
		}
	}

	preUsername := sm.cfg.GitHubUsername
	if preUsername == "" && preRepoRoot != "" && !inPlace {
		ctx, cancel := context.WithTimeout(context.Background(), gitUsernameTimeout)
		preUsername, _ = git.DiscoverGitHubUsername(ctx, preRepoRoot)
		cancel()
	}
	if preUsername == "" {
		preUsername = "user"
	}

	// --- Phase 1: Lock, validate state, reserve session ---
	sm.mu.Lock()

	id := generateID()

	var repoRoot, repoName, worktreePath, branchName string
	var sharedWorktree bool
	var sharedWorktreeSourceID string
	var fetchOnCreate bool
	var rcIncludes []string
	var sourceIncludes []IncludedRepoState

	switch {
	case shareWorktree != "":
		var source *SessionState
		for _, s := range sm.state.Sessions {
			if s.Name == shareWorktree || s.ID == shareWorktree {
				source = s
				break
			}
		}
		if source == nil {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("session %q not found for --share-worktree", shareWorktree)
		}
		if source.WorktreePath == "" {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("session %q has no worktree to share", shareWorktree)
		}
		worktreePath = source.WorktreePath
		repoRoot = source.RepoPath
		repoName = source.RepoName
		baseBranch = source.BaseBranch
		sharedWorktree = true
		sharedWorktreeSourceID = source.ID
		sourceIncludes = make([]IncludedRepoState, len(source.Includes))
		copy(sourceIncludes, source.Includes)
	case noRepo:
		worktreePath = filepath.Join(sm.paths.DataDir, "scratch", id)
		if err := os.MkdirAll(worktreePath, 0o700); err != nil {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("create scratch dir: %w", err)
		}
	case inPlace:
		repoRoot = preRepoRoot

		rc, ok := sm.cfg.FindRepo(repoRoot)
		if !ok {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("repo root %q is not configured in [[repos]] — add it to config to use --in-place", repoRoot)
		}

		if len(rc.Includes) > 0 {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("repo %q has includes configured — drop --in-place to create an includes session with worktrees", repoRoot)
		}

		if !allowConcurrent && !rc.AllowConcurrent {
			canonicalRoot := config.ResolvePath(repoRoot)
			for _, s := range sm.state.Sessions {
				if s.InPlace && config.ResolvePath(s.WorktreePath) == canonicalRoot && (s.Status == StatusRunning || s.Status == StatusCreating) {
					sm.mu.Unlock()
					return SessionState{}, fmt.Errorf("an in-place session %q is already running in %q — use --allow-concurrent to override", s.Name, repoRoot)
				}
			}
		}

		repoName = filepath.Base(repoRoot)
		worktreePath = repoRoot
	default:
		repoRoot = preRepoRoot
		if !sm.cfg.RepoPathAllowed(repoPath) {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("repo path %q is not under any allowed_repo_paths", repoPath)
		}

		rc, _ := sm.cfg.FindRepo(repoRoot)

		if rc.Singleton {
			canonicalRoot := config.ResolvePath(repoRoot)
			for _, s := range sm.state.Sessions {
				if config.ResolvePath(s.RepoPath) == canonicalRoot && (s.Status == StatusRunning || s.Status == StatusCreating) {
					sm.mu.Unlock()
					return SessionState{}, fmt.Errorf("repo %q has singleton = true and session %q is already running — stop it first", repoRoot, s.Name)
				}
			}
		}

		if baseBranch == "" {
			var err error
			baseBranch, err = git.DiscoverDefaultBranch(repoRoot)
			if err != nil {
				sm.mu.Unlock()
				return SessionState{}, err
			}
		}

		repoName = filepath.Base(repoRoot)

		branchPrefix, _ := config.Expand(sm.cfg.BranchPrefix, config.TemplateVars{Username: preUsername})
		branchName = fmt.Sprintf("%s/%s-%s", branchPrefix, name, id)

		sessionDir := filepath.Join(sm.paths.DataDir, "worktrees", repoName, repoHash(repoRoot), id)

		if len(rc.Includes) > 0 {
			worktreePath = filepath.Join(sessionDir, repoName)
			rcIncludes = make([]string, len(rc.Includes))
			copy(rcIncludes, rc.Includes)
		} else {
			worktreePath = sessionDir
		}

		fetchOnCreate = sm.cfg.FetchOnCreate
	}

	agentSessionID := ""
	if agentName == "claude" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		agentSessionID = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	}

	// Resolve sandbox under the lock (reads config, fast).
	sandboxed, err := sm.resolveSandbox(agentName)
	if err != nil {
		if noRepo {
			os.RemoveAll(worktreePath)
		}
		sm.mu.Unlock()
		return SessionState{}, err
	}
	if sharedWorktree && !sandboxed {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("--share-worktree requires sandbox to be enabled so the shared worktree can be mounted read-only; set sandbox.enabled = true in config and ensure safehouse is installed (gr doctor)")
	}

	// Resolve MCP servers under the lock (reads config).
	var mcpServers []config.MCPServerConfig
	if agentHooks {
		mcpServers = sm.resolveMCPServers(agentName)
	}

	// Snapshot config values needed for Phase 2.
	cfgSnapshot := sm.cfg
	sandboxMerged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[agentName].Sandbox)

	// Reserve the session with StatusCreating so concurrent operations
	// (list, singleton checks) see it exists.
	placeholder := &SessionState{
		ID:                     id,
		ParentID:               parentID,
		Name:                   name,
		RepoPath:               repoRoot,
		RepoName:               repoName,
		WorktreePath:           worktreePath,
		Branch:                 branchName,
		BaseBranch:             baseBranch,
		Agent:                  agentName,
		AgentSessionID:         agentSessionID,
		Model:                  model,
		SharedWorktree:         sharedWorktree,
		SharedWorktreeSourceID: sharedWorktreeSourceID,
		InPlace:                inPlace,
		AgentHooks:             agentHooks,
		Status:                 StatusCreating,
		CreatedAt:              time.Now().UTC(),
		StatusChangedAt:        time.Now().UTC(),
	}
	sm.state.Sessions[id] = placeholder
	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		if noRepo {
			os.RemoveAll(worktreePath)
		}
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	sm.mu.Unlock()

	// --- Phase 2: External work (no lock held) ---
	// Git setup, hook injection, sandbox wrapping, PTY spawn.

	var includes []IncludedRepoState
	cleanupOnError := func() {
		sm.cleanupHooks(id, agentName, worktreePath)
		if sharedWorktree || inPlace {
			return
		}
		switch {
		case noRepo:
			os.RemoveAll(worktreePath)
		case len(includes) > 0:
			sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
		case repoRoot != "":
			_ = git.TeardownSession(repoRoot, worktreePath, branchName)
		}
	}
	rollbackState := func() {
		sm.mu.Lock()
		delete(sm.state.Sessions, id)
		_ = sm.saveState()
		sm.mu.Unlock()
	}

	// Git worktree setup (default path only — includes fetch which can block).
	if repoRoot != "" && !sharedWorktree && !inPlace {
		gitCtx, gitCancel := context.WithTimeout(context.Background(), gitFetchTimeout)
		defer gitCancel()

		branchPrefix, _ := config.Expand(cfgSnapshot.BranchPrefix, config.TemplateVars{Username: preUsername})

		if len(rcIncludes) > 0 {
			if err := git.SetupSession(gitCtx, repoRoot, worktreePath, branchName, baseBranch, fetchOnCreate); err != nil {
				rollbackState()
				return SessionState{}, fmt.Errorf("setup main repo git session: %w", err)
			}

			for _, incPath := range rcIncludes {
				resolved := config.ResolvePath(incPath)
				if !cfgSnapshot.RepoPathAllowed(resolved) {
					sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
					rollbackState()
					return SessionState{}, fmt.Errorf("included repo %q is not under any allowed_repo_paths", incPath)
				}
				if !git.IsInsideGitRepo(resolved) {
					sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
					rollbackState()
					return SessionState{}, fmt.Errorf("included repo %q is not a git repository", incPath)
				}
				incRoot, err := git.RepoRootPath(resolved)
				if err != nil {
					sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
					rollbackState()
					return SessionState{}, fmt.Errorf("find included repo root for %q: %w", incPath, err)
				}
				incName := filepath.Base(incRoot)
				incBaseBranch, err := git.DiscoverDefaultBranchOrHEAD(incRoot)
				if err != nil {
					sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
					rollbackState()
					return SessionState{}, fmt.Errorf("discover default branch for included repo %q: %w", incPath, err)
				}
				incBranch := fmt.Sprintf("%s/%s-%s/%s", branchPrefix, name, id, incName)
				sessionDir := filepath.Dir(worktreePath)
				incWorktreePath := filepath.Join(sessionDir, incName)

				if err := git.SetupSession(gitCtx, incRoot, incWorktreePath, incBranch, incBaseBranch, fetchOnCreate); err != nil {
					sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
					rollbackState()
					return SessionState{}, fmt.Errorf("setup included repo %q: %w", incName, err)
				}

				includes = append(includes, IncludedRepoState{
					RepoPath:     incRoot,
					RepoName:     incName,
					WorktreePath: incWorktreePath,
					Branch:       incBranch,
					BaseBranch:   incBaseBranch,
				})
			}
		} else {
			if err := git.SetupSession(gitCtx, repoRoot, worktreePath, branchName, baseBranch, fetchOnCreate); err != nil {
				rollbackState()
				return SessionState{}, fmt.Errorf("setup git session: %w", err)
			}
		}
	}

	// Build template vars, env, args, hooks, sandbox — all fast, no lock needed.
	vars := config.TemplateVars{
		Username:       preUsername,
		AgentSessionID: agentSessionID,
		SessionName:    name,
		SessionID:      id,
		WorktreePath:   worktreePath,
		Model:          model,
	}

	expandedArgs, err := config.ExpandSlice(agent.Args, vars)
	if err != nil {
		cleanupOnError()
		rollbackState()
		return SessionState{}, fmt.Errorf("expand agent args: %w", err)
	}
	if prompt != "" {
		expandedArgs = append(expandedArgs, prompt)
	}

	logPath := filepath.Join(sm.paths.LogDir, id+".log")

	env := make(map[string]string, len(agent.Env)+5)
	for k, v := range agent.Env {
		env[k] = v
	}
	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = name
	env["GRAITH_AGENT_TYPE"] = agentName
	env["GRAITH_WORKTREE_PATH"] = worktreePath
	if sm.paths.Profile != "" {
		env["GRAITH_PROFILE"] = sm.paths.Profile
	}
	if inPlace {
		env["GRAITH_IN_PLACE"] = "true"
	}
	for _, inc := range includes {
		env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
	}
	if sharedWorktree {
		for _, inc := range sourceIncludes {
			env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
		}
	}

	if agentHooks {
		hookArgs, hookEnv, err := sm.injectHooks(agentName, id, worktreePath, mcpServers)
		if err != nil {
			cleanupOnError()
			rollbackState()
			return SessionState{}, fmt.Errorf("inject agent hooks: %w", err)
		}
		expandedArgs = append(expandedArgs, hookArgs...)
		for k, v := range hookEnv {
			env[k] = v
		}
	}

	command := agent.Command
	finalArgs := expandedArgs
	var scratchDir string
	var mergedSandbox *config.SandboxConfig
	if sandboxed {
		merged := sandboxMerged
		merged.ReadDirs = expandPaths(merged.ReadDirs)
		merged.WriteDirs = expandPaths(merged.WriteDirs)
		mergedSandbox = &merged
		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_AGENT_TYPE", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}
		for k := range env {
			envKeys = append(envKeys, k)
		}
		opts := sm.sandboxOptsFromConfig(merged, id, worktreePath, envKeys, agentHooks)
		if len(includes) > 0 {
			opts.WriteDirs = append(opts.WriteDirs, sm.deriveSandboxIncludesWriteDirs(includes)...)
		}
		if sharedWorktree {
			scratchDir = filepath.Join(sm.paths.DataDir, "scratch", id)
			if err := os.MkdirAll(scratchDir, 0o700); err != nil {
				cleanupOnError()
				rollbackState()
				return SessionState{}, fmt.Errorf("create scratch dir: %w", err)
			}
			opts.ReadDirs = append(opts.ReadDirs, worktreePath)
			for _, inc := range sourceIncludes {
				opts.ReadDirs = append(opts.ReadDirs, inc.WorktreePath)
			}
			opts.WorktreeDir = scratchDir
		}
		command, finalArgs = sandbox.Wrap(agent.Command, expandedArgs, opts)
		sm.log.Info("sandboxing session", "id", id, "agent", agentName,
			"command", command, "read_dirs", opts.ReadDirs, "write_dirs", opts.WriteDirs,
			"features", opts.Features, "workdir", opts.WorktreeDir)
	}

	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    command,
		Args:       finalArgs,
		Dir:        worktreePath,
		Env:        env,
		Rows:       rows,
		Cols:       cols,
		LogPath:    logPath,
		MaxLogSize: 100 * 1024 * 1024,
	})
	if err != nil {
		cleanupOnError()
		if scratchDir != "" {
			os.RemoveAll(scratchDir)
		}
		rollbackState()
		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	// --- Phase 3: Lock, commit to running ---
	sm.mu.Lock()

	// Check the session wasn't deleted while we were setting up.
	if _, ok := sm.state.Sessions[id]; !ok {
		sm.mu.Unlock()
		_ = ptySess.Kill()
		ptySess.Close()
		cleanupOnError()
		if scratchDir != "" {
			os.RemoveAll(scratchDir)
		}
		os.Remove(logPath)
		return SessionState{}, fmt.Errorf("session was deleted during creation")
	}

	sessState := sm.state.Sessions[id]
	sessState.Sandboxed = sandboxed
	sessState.SandboxConfig = mergedSandbox
	sessState.Includes = includes
	sessState.Status = StatusRunning
	sessState.StatusChangedAt = time.Now()
	sessState.PID = ptySess.Cmd.Process.Pid
	sessState.CreationCfg = &CreationConfig{
		Agent:         agent,
		SandboxConfig: cfgSnapshot.Sandbox.Merge(agent.Sandbox),
	}

	sm.sessions[id] = ptySess

	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		delete(sm.sessions, id)
		sm.mu.Unlock()
		_ = ptySess.Kill()
		ptySess.Close()
		cleanupOnError()
		if scratchDir != "" {
			os.RemoveAll(scratchDir)
		}
		sm.cleanupHooks(id, agentName, worktreePath)
		os.Remove(logPath)
		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	result := cloneSessionState(sessState)
	sm.mu.Unlock()

	go sm.watchSession(id, ptySess)

	return result, nil
}

// Fork creates a new session that branches from an existing session's git state
// and uses the agent's fork_args to carry over the conversation history.
//
// Uses three-phase locking like Create to avoid holding the mutex during
// git fetch and PTY spawn.
func (sm *SessionManager) Fork(name, sourceSessionID string, rows, cols uint16) (SessionState, error) {
	if err := ValidateSessionName(name); err != nil {
		return SessionState{}, err
	}

	// --- Pre-lock: discover GitHub username ---
	sm.mu.RLock()
	cfgSnapshot := sm.cfg
	source, sourceOk := sm.state.Sessions[sourceSessionID]
	var sourceRepoPath string
	if sourceOk {
		sourceRepoPath = source.RepoPath
	}
	sm.mu.RUnlock()

	preUsername := cfgSnapshot.GitHubUsername
	if preUsername == "" && sourceRepoPath != "" {
		ctx, cancel := context.WithTimeout(context.Background(), gitUsernameTimeout)
		preUsername, _ = git.DiscoverGitHubUsername(ctx, sourceRepoPath)
		cancel()
	}
	if preUsername == "" {
		preUsername = "user"
	}

	// --- Phase 1: Lock, validate, reserve ---
	sm.mu.Lock()

	source, ok := sm.state.Sessions[sourceSessionID]
	if !ok {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("source session %q not found", sourceSessionID)
	}

	if source.RepoPath == "" {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("cannot fork session %q: source has no repo (fork requires a git repository)", source.Name)
	}

	if source.InPlace {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("cannot fork session %q: in-place sessions cannot be forked", source.Name)
	}

	if rc, ok := sm.cfg.FindRepo(source.RepoPath); ok && rc.Singleton {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("cannot fork session %q: repo %q has singleton = true — stop the source session first or remove the singleton constraint", source.Name, source.RepoPath)
	}

	agentName := source.Agent
	agent, ok := sm.cfg.Agents[agentName]
	if !ok {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("unknown agent %q", agentName)
	}

	id := generateID()

	repoRoot := source.RepoPath
	repoName := source.RepoName
	baseBranch := source.BaseBranch
	sourceModel := source.Model
	sourceAgentSessionID := source.AgentSessionID
	sourceAgentHooks := source.AgentHooks
	sourceForkIncludes := make([]IncludedRepoState, len(source.Includes))
	copy(sourceForkIncludes, source.Includes)

	branchPrefix, _ := config.Expand(sm.cfg.BranchPrefix, config.TemplateVars{Username: preUsername})
	branchName := fmt.Sprintf("%s/%s-%s", branchPrefix, name, id)

	sessionDir := filepath.Join(sm.paths.DataDir, "worktrees", repoName, repoHash(repoRoot), id)

	var worktreePath string
	if len(sourceForkIncludes) > 0 {
		worktreePath = filepath.Join(sessionDir, repoName)
	} else {
		worktreePath = sessionDir
	}

	agentSessionID := ""
	if agentName == "claude" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		agentSessionID = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	}

	sandboxed, err := sm.resolveSandbox(agentName)
	if err != nil {
		sm.mu.Unlock()
		return SessionState{}, err
	}

	var mcpServers []config.MCPServerConfig
	if sourceAgentHooks {
		mcpServers = sm.resolveMCPServers(agentName)
	}

	sandboxMerged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[agentName].Sandbox)
	fetchOnCreate := sm.cfg.FetchOnCreate

	placeholder := &SessionState{
		ID:              id,
		ParentID:        sourceSessionID,
		Name:            name,
		RepoPath:        repoRoot,
		RepoName:        repoName,
		WorktreePath:    worktreePath,
		Branch:          branchName,
		BaseBranch:      baseBranch,
		Agent:           agentName,
		AgentSessionID:  agentSessionID,
		Model:           sourceModel,
		AgentHooks:      sourceAgentHooks,
		Status:          StatusCreating,
		CreatedAt:       time.Now().UTC(),
		StatusChangedAt: time.Now().UTC(),
	}
	sm.state.Sessions[id] = placeholder
	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	sm.mu.Unlock()

	// --- Phase 2: Git setup and PTY spawn (no lock) ---
	var forkIncludes []IncludedRepoState

	forkCleanup := func() {
		sm.cleanupHooks(id, agentName, worktreePath)
		if len(forkIncludes) > 0 {
			sm.teardownIncludes(repoRoot, worktreePath, branchName, forkIncludes)
		} else {
			_ = git.TeardownSession(repoRoot, worktreePath, branchName)
		}
	}
	rollbackState := func() {
		sm.mu.Lock()
		delete(sm.state.Sessions, id)
		_ = sm.saveState()
		sm.mu.Unlock()
	}

	gitCtx, gitCancel := context.WithTimeout(context.Background(), gitFetchTimeout)
	defer gitCancel()

	if len(sourceForkIncludes) > 0 {
		if err := git.SetupSession(gitCtx, repoRoot, worktreePath, branchName, baseBranch, fetchOnCreate); err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("setup main repo git session for fork: %w", err)
		}
		for _, srcInc := range sourceForkIncludes {
			incBranch := fmt.Sprintf("%s/%s-%s/%s", branchPrefix, name, id, srcInc.RepoName)
			incWorktreePath := filepath.Join(sessionDir, srcInc.RepoName)
			if err := git.SetupSession(gitCtx, srcInc.RepoPath, incWorktreePath, incBranch, srcInc.Branch, fetchOnCreate); err != nil {
				sm.teardownIncludes(repoRoot, worktreePath, branchName, forkIncludes)
				rollbackState()
				return SessionState{}, fmt.Errorf("setup included repo %q for fork: %w", srcInc.RepoName, err)
			}
			forkIncludes = append(forkIncludes, IncludedRepoState{
				RepoPath:     srcInc.RepoPath,
				RepoName:     srcInc.RepoName,
				WorktreePath: incWorktreePath,
				Branch:       incBranch,
				BaseBranch:   srcInc.Branch,
			})
		}
	} else {
		if err := git.SetupSession(gitCtx, repoRoot, worktreePath, branchName, baseBranch, fetchOnCreate); err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("setup git session: %w", err)
		}
	}

	vars := config.TemplateVars{
		Username:                 preUsername,
		AgentSessionID:           agentSessionID,
		SessionName:              name,
		SessionID:                id,
		WorktreePath:             worktreePath,
		ForkSourceAgentSessionID: sourceAgentSessionID,
		Model:                    sourceModel,
	}

	args := agent.ForkArgs
	if len(args) == 0 {
		args = agent.Args
	}

	expandedArgs, err := config.ExpandSlice(args, vars)
	if err != nil {
		forkCleanup()
		rollbackState()
		return SessionState{}, fmt.Errorf("expand fork args: %w", err)
	}

	logPath := filepath.Join(sm.paths.LogDir, id+".log")

	env := make(map[string]string, len(agent.Env)+5)
	for k, v := range agent.Env {
		env[k] = v
	}
	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = name
	env["GRAITH_AGENT_TYPE"] = agentName
	env["GRAITH_WORKTREE_PATH"] = worktreePath
	if sm.paths.Profile != "" {
		env["GRAITH_PROFILE"] = sm.paths.Profile
	}
	for _, inc := range forkIncludes {
		env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
	}

	if sourceAgentHooks {
		hookArgs, hookEnv, err := sm.injectHooks(agentName, id, worktreePath, mcpServers)
		if err != nil {
			forkCleanup()
			rollbackState()
			return SessionState{}, fmt.Errorf("inject agent hooks: %w", err)
		}
		expandedArgs = append(expandedArgs, hookArgs...)
		for k, v := range hookEnv {
			env[k] = v
		}
	}

	command := agent.Command
	finalArgs := expandedArgs
	var mergedSandbox *config.SandboxConfig
	if sandboxed {
		merged := sandboxMerged
		merged.ReadDirs = expandPaths(merged.ReadDirs)
		merged.WriteDirs = expandPaths(merged.WriteDirs)
		mergedSandbox = &merged
		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_AGENT_TYPE", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}
		for k := range env {
			envKeys = append(envKeys, k)
		}
		opts := sm.sandboxOptsFromConfig(merged, id, worktreePath, envKeys, sourceAgentHooks)
		if len(forkIncludes) > 0 {
			opts.WriteDirs = append(opts.WriteDirs, sm.deriveSandboxIncludesWriteDirs(forkIncludes)...)
		}
		command, finalArgs = sandbox.Wrap(agent.Command, expandedArgs, opts)
		sm.log.Info("sandboxing forked session", "id", id,
			"command", command, "read_dirs", opts.ReadDirs, "write_dirs", opts.WriteDirs,
			"features", opts.Features, "workdir", opts.WorktreeDir)
	}

	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    command,
		Args:       finalArgs,
		Dir:        worktreePath,
		Env:        env,
		Rows:       rows,
		Cols:       cols,
		LogPath:    logPath,
		MaxLogSize: 100 * 1024 * 1024,
	})
	if err != nil {
		forkCleanup()
		rollbackState()
		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	// --- Phase 3: Lock, commit ---
	sm.mu.Lock()

	if _, ok := sm.state.Sessions[id]; !ok {
		sm.mu.Unlock()
		_ = ptySess.Kill()
		ptySess.Close()
		forkCleanup()
		os.Remove(logPath)
		return SessionState{}, fmt.Errorf("session was deleted during creation")
	}

	sessState := sm.state.Sessions[id]
	sessState.Sandboxed = sandboxed
	sessState.SandboxConfig = mergedSandbox
	sessState.Includes = forkIncludes
	sessState.Status = StatusRunning
	sessState.StatusChangedAt = time.Now()
	sessState.PID = ptySess.Cmd.Process.Pid
	sessState.CreationCfg = &CreationConfig{
		Agent:         agent,
		SandboxConfig: cfgSnapshot.Sandbox.Merge(agent.Sandbox),
	}

	sm.sessions[id] = ptySess

	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		delete(sm.sessions, id)
		sm.mu.Unlock()
		_ = ptySess.Kill()
		ptySess.Close()
		forkCleanup()
		os.Remove(logPath)
		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	result := cloneSessionState(sessState)
	sm.mu.Unlock()

	go sm.watchSession(id, ptySess)

	return result, nil
}

// watchSession waits for a PTY session to exit and updates state accordingly.
// If the session has been replaced (e.g. by Resume) or removed (e.g. by Delete),
// the watcher is stale and skips the state update and status event.
func (sm *SessionManager) watchSession(id string, sess *grpty.Session) {
	<-sess.Done()

	sm.mu.Lock()
	stale := sm.sessions[id] != sess
	var name string
	var deleted bool
	if !stale {
		if s, ok := sm.state.Sessions[id]; ok {
			name = s.Name
			exitCode := sess.ExitCode()
			s.Status = StatusStopped
			s.StatusChangedAt = time.Now()
			s.ExitCode = &exitCode
			s.PID = 0
			if lastOut := sess.LastOutputAt(); !lastOut.IsZero() {
				s.LastOutputAt = &lastOut
			}
			if err := sm.saveState(); err != nil {
				sm.log.Error("failed to save state after session exit", "id", id, "err", err)
			}
		} else {
			deleted = true
		}
	}
	sm.mu.Unlock()

	if stale {
		sm.log.Info("ignoring stale session exit", "id", id, "exit_code", sess.ExitCode())
		return
	}
	if deleted {
		sm.log.Info("ignoring exit for deleted session", "id", id, "exit_code", sess.ExitCode())
		return
	}

	sm.log.Info("session exited", "id", id, "exit_code", sess.ExitCode())
	sm.onAgentStatusChange(id, name, "running", "stopped")
}

// Resume restarts a stopped session using the agent's resume_args.
//
// Uses two-phase locking: the GitHub username discovery happens before the lock,
// and the PTY spawn happens after releasing the lock to avoid blocking the daemon.
func (sm *SessionManager) Resume(id string, rows, cols uint16) (SessionState, error) {
	// --- Pre-lock: discover GitHub username ---
	sm.mu.RLock()
	sessSnap, snapOk := sm.state.Sessions[id]
	var snapRepoPath string
	var snapAgent string
	if snapOk {
		snapRepoPath = sessSnap.RepoPath
		snapAgent = sessSnap.Agent
	}
	cfgUsername := sm.cfg.GitHubUsername
	sm.mu.RUnlock()

	preUsername := cfgUsername
	if preUsername == "" && snapRepoPath != "" {
		ctx, cancel := context.WithTimeout(context.Background(), gitUsernameTimeout)
		preUsername, _ = git.DiscoverGitHubUsername(ctx, snapRepoPath)
		cancel()
	}
	if preUsername == "" {
		preUsername = "user"
	}
	_ = snapAgent // used only to decide whether to discover username

	// --- Phase 1: Lock, validate, prepare, mark creating ---
	sm.mu.Lock()

	sessState, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q not found", id)
	}
	if sessState.Status == StatusDeleting {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is being deleted", id)
	}
	if sessState.Status == StatusRunning {
		result := *sessState
		sm.mu.Unlock()
		return result, nil
	}
	if sessState.Status == StatusCreating {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is being created", id)
	}

	if err := ValidateSessionName(sessState.Name); err != nil {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session has unsafe name %q (created before validation was added): %w — use 'gr rename' to fix", sessState.Name, err)
	}

	agent, ok := sm.cfg.Agents[sessState.Agent]
	if !ok {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("unknown agent %q", sessState.Agent)
	}

	sandboxed, err := sm.resolveSandbox(sessState.Agent)
	if err != nil {
		sm.mu.Unlock()
		return SessionState{}, err
	}

	if sessState.SharedWorktree && !sandboxed {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("shared-worktree session %q requires sandbox but sandbox is not enabled in current config; enable sandbox to resume", id)
	}

	if sessState.RepoPath != "" {
		if rc, ok := sm.cfg.FindRepo(sessState.RepoPath); ok && rc.Singleton {
			canonicalRoot := config.ResolvePath(sessState.RepoPath)
			for _, s := range sm.state.Sessions {
				if s.ID != id && config.ResolvePath(s.RepoPath) == canonicalRoot && (s.Status == StatusRunning || s.Status == StatusCreating) {
					sm.mu.Unlock()
					return SessionState{}, fmt.Errorf("repo %q has singleton = true and session %q is already running — stop it first", sessState.RepoPath, s.Name)
				}
			}
		}
	}

	if sessState.InPlace {
		if !git.IsInsideGitRepo(sessState.WorktreePath) {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("in-place repo path %q is no longer a git repository", sessState.WorktreePath)
		}
		currentRoot, err := git.RepoRootPath(sessState.WorktreePath)
		if err != nil {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("resolve in-place repo root: %w", err)
		}
		if config.ResolvePath(currentRoot) != config.ResolvePath(sessState.WorktreePath) {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("in-place repo root changed: saved %q, current %q", sessState.WorktreePath, currentRoot)
		}
		rc, ok := sm.cfg.FindRepo(sessState.WorktreePath)
		if !ok {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("repo path %q is no longer configured in [[repos]] — add it back to config to resume this in-place session", sessState.WorktreePath)
		}
		if !rc.AllowConcurrent {
			for _, s := range sm.state.Sessions {
				if s.ID != id && s.InPlace && config.ResolvePath(s.WorktreePath) == config.ResolvePath(sessState.WorktreePath) && (s.Status == StatusRunning || s.Status == StatusCreating) {
					sm.mu.Unlock()
					return SessionState{}, fmt.Errorf("another in-place session %q is already running in %q — stop it first or use allow_concurrent in config", s.Name, sessState.WorktreePath)
				}
			}
		}
	}

	// Resolve MCP servers under the lock.
	var mcpServers []config.MCPServerConfig
	if sessState.AgentHooks {
		mcpServers = sm.resolveMCPServers(sessState.Agent)
	}

	// Snapshot shared worktree source includes under lock.
	var sharedSourceIncludes []IncludedRepoState
	if sessState.SharedWorktree {
		if source, ok := sm.state.Sessions[sessState.SharedWorktreeSourceID]; ok {
			sharedSourceIncludes = make([]IncludedRepoState, len(source.Includes))
			copy(sharedSourceIncludes, source.Includes)
		}
	}

	sandboxMerged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[sessState.Agent].Sandbox)
	cfgSnapshot := sm.cfg

	// Save previous state for rollback.
	prevStatus := sessState.Status
	prevExitCode := sessState.ExitCode
	prevPID := sessState.PID
	prevAgentStatus := sessState.AgentStatus
	prevSandboxed := sessState.Sandboxed
	prevSandboxConfig := sessState.SandboxConfig
	prevCreationCfg := sessState.CreationCfg
	prevIdleSince := sessState.IdleSince

	// Mark as creating so concurrent operations see it's busy.
	prevStatusChangedAt := sessState.StatusChangedAt
	sessState.Status = StatusCreating
	sessState.StatusChangedAt = time.Now().UTC()
	if err := sm.saveState(); err != nil {
		sessState.Status = prevStatus
		sessState.StatusChangedAt = prevStatusChangedAt
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	// Snapshot session fields for use outside the lock.
	sessName := sessState.Name
	sessAgent := sessState.Agent
	sessWorktreePath := sessState.WorktreePath
	sessAgentSessionID := sessState.AgentSessionID
	sessModel := sessState.Model
	sessAgentHooks := sessState.AgentHooks
	sessIncludes := make([]IncludedRepoState, len(sessState.Includes))
	copy(sessIncludes, sessState.Includes)
	sessInPlace := sessState.InPlace
	sessSharedWorktree := sessState.SharedWorktree

	sm.mu.Unlock()

	// --- Phase 2: Build command and spawn PTY (no lock) ---
	rollbackState := func() {
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.Status = prevStatus
			s.ExitCode = prevExitCode
			s.PID = prevPID
			s.AgentStatus = prevAgentStatus
			s.Sandboxed = prevSandboxed
			s.SandboxConfig = prevSandboxConfig
			s.CreationCfg = prevCreationCfg
			s.IdleSince = prevIdleSince
			_ = sm.saveState()
		}
		sm.mu.Unlock()
	}

	resumeArgs := agent.ResumeArgs
	if len(resumeArgs) == 0 {
		resumeArgs = agent.Args
	}

	vars := config.TemplateVars{
		Username:       preUsername,
		AgentSessionID: sessAgentSessionID,
		SessionName:    sessName,
		SessionID:      id,
		WorktreePath:   sessWorktreePath,
		Model:          sessModel,
	}
	expandedArgs, err := config.ExpandSlice(resumeArgs, vars)
	if err != nil {
		rollbackState()
		return SessionState{}, fmt.Errorf("expand resume args: %w", err)
	}

	logPath := filepath.Join(sm.paths.LogDir, id+".log")

	env := make(map[string]string, len(agent.Env)+5)
	for k, v := range agent.Env {
		env[k] = v
	}
	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = sessName
	env["GRAITH_AGENT_TYPE"] = sessAgent
	env["GRAITH_WORKTREE_PATH"] = sessWorktreePath
	if sm.paths.Profile != "" {
		env["GRAITH_PROFILE"] = sm.paths.Profile
	}
	if sessInPlace {
		env["GRAITH_IN_PLACE"] = "true"
	}

	for _, inc := range sessIncludes {
		if !git.IsInsideGitRepo(inc.WorktreePath) {
			rollbackState()
			return SessionState{}, fmt.Errorf("included worktree %q (%s) is no longer a valid git repo — delete and recreate the session", inc.WorktreePath, inc.RepoName)
		}
		env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
	}

	if sessAgentHooks {
		hookArgs, hookEnv, err := sm.injectHooks(sessAgent, id, sessWorktreePath, mcpServers)
		if err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("inject agent hooks: %w", err)
		}
		expandedArgs = append(expandedArgs, hookArgs...)
		for k, v := range hookEnv {
			env[k] = v
		}
	}

	command := agent.Command
	finalArgs := expandedArgs
	var mergedSandbox *config.SandboxConfig
	if sandboxed {
		merged := sandboxMerged
		merged.ReadDirs = expandPaths(merged.ReadDirs)
		merged.WriteDirs = expandPaths(merged.WriteDirs)
		mergedSandbox = &merged
		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_AGENT_TYPE", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}
		for k := range env {
			envKeys = append(envKeys, k)
		}
		opts := sm.sandboxOptsFromConfig(merged, id, sessWorktreePath, envKeys, sessAgentHooks)
		if len(sessIncludes) > 0 {
			opts.WriteDirs = append(opts.WriteDirs, sm.deriveSandboxIncludesWriteDirs(sessIncludes)...)
		}
		if sessSharedWorktree {
			scratchDir := filepath.Join(sm.paths.DataDir, "scratch", id)
			if err := os.MkdirAll(scratchDir, 0o700); err != nil {
				rollbackState()
				return SessionState{}, fmt.Errorf("create scratch dir for shared worktree resume: %w", err)
			}
			opts.ReadDirs = append(opts.ReadDirs, sessWorktreePath)
			for _, inc := range sharedSourceIncludes {
				opts.ReadDirs = append(opts.ReadDirs, inc.WorktreePath)
			}
			opts.WorktreeDir = scratchDir
		}
		command, finalArgs = sandbox.Wrap(agent.Command, expandedArgs, opts)
		sm.log.Info("sandboxing resumed session", "id", id,
			"command", command, "read_dirs", opts.ReadDirs, "write_dirs", opts.WriteDirs,
			"features", opts.Features, "workdir", opts.WorktreeDir)
	}

	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    command,
		Args:       finalArgs,
		Dir:        sessWorktreePath,
		Env:        env,
		Rows:       rows,
		Cols:       cols,
		LogPath:    logPath,
		MaxLogSize: 100 * 1024 * 1024,
	})
	if err != nil {
		rollbackState()
		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	// --- Phase 3: Lock, commit to running ---
	sm.mu.Lock()

	if _, ok := sm.state.Sessions[id]; !ok {
		sm.mu.Unlock()
		_ = ptySess.Kill()
		ptySess.Close()
		return SessionState{}, fmt.Errorf("session was deleted during resume")
	}

	sessState = sm.state.Sessions[id]
	sessState.Status = StatusRunning
	sessState.StatusChangedAt = time.Now()
	sessState.ExitCode = nil
	sessState.PID = ptySess.Cmd.Process.Pid
	sessState.AgentStatus = ""
	sessState.IdleSince = nil
	sessState.Sandboxed = sandboxed
	sessState.SandboxConfig = mergedSandbox
	sessState.CreationCfg = &CreationConfig{
		Agent:         agent,
		SandboxConfig: cfgSnapshot.Sandbox.Merge(agent.Sandbox),
	}

	sm.sessions[id] = ptySess

	if err := sm.saveState(); err != nil {
		sessState.Status = prevStatus
		sessState.StatusChangedAt = prevStatusChangedAt
		sessState.ExitCode = prevExitCode
		sessState.PID = prevPID
		sessState.AgentStatus = prevAgentStatus
		sessState.Sandboxed = prevSandboxed
		sessState.SandboxConfig = prevSandboxConfig
		sessState.CreationCfg = prevCreationCfg
		sessState.IdleSince = prevIdleSince
		delete(sm.sessions, id)
		sm.mu.Unlock()
		_ = ptySess.Kill()
		ptySess.Close()
		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	delete(sm.hookReports, id)
	result := cloneSessionState(sessState)
	sm.mu.Unlock()

	go sm.watchSession(id, ptySess)

	return result, nil
}

// Delete stops a session, removes its worktree/branch, and deletes state.
// Git teardown is attempted before removing the session from state; if teardown
// fails the session is kept for retry and the error is returned.
func (sm *SessionManager) Delete(id string) error {
	sm.mu.Lock()

	sessState, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("session %q not found", id)
	}
	if sessState.Starred {
		sm.mu.Unlock()
		return fmt.Errorf("session %q is starred; unstar it first to delete", id)
	}
	if sessState.Status == StatusDeleting {
		sm.mu.Unlock()
		return fmt.Errorf("session %q is already being deleted", id)
	}

	ac, hasClient := sm.attachedClients[id]
	if hasClient {
		delete(sm.attachedClients, id)
	}

	// Snapshot PTY session and remove from map under the lock so no concurrent
	// access is possible, then release the lock before blocking waits.
	ptySess, hasPTY := sm.sessions[id]
	if hasPTY {
		delete(sm.sessions, id)
	}

	repoPath := sessState.RepoPath
	worktreePath := sessState.WorktreePath
	branch := sessState.Branch
	shared := sessState.SharedWorktree
	inPlace := sessState.InPlace
	agentName := sessState.Agent
	prevStatus := sessState.Status
	sessionIncludes := make([]IncludedRepoState, len(sessState.Includes))
	copy(sessionIncludes, sessState.Includes)

	if sessState.Status == StatusCreating {
		// Session is mid-creation (Phase 2). Remove from state so Phase 3 detects
		// the deletion and handles cleanup (worktree, PTY).
		delete(sm.state.Sessions, id)
		delete(sm.hookReports, id)
		_ = sm.saveState()
		sm.mu.Unlock()
		if hasClient {
			ac.kick()
		}
		return nil
	}

	sessState.Status = StatusDeleting
	sessState.StatusChangedAt = time.Now()
	sessState.PID = 0
	_ = sm.saveState()
	sm.mu.Unlock()

	// Blocking operations outside the lock.
	if hasPTY {
		ptySess.Detach()
		if !ptySess.Exited() {
			_ = ptySess.Kill()
			select {
			case <-ptySess.Done():
			case <-time.After(5 * time.Second):
				_ = ptySess.ForceKill()
			}
		}
		ptySess.Close()
	}

	// Attempt git teardown before removing the session from state.
	var teardownErr error
	switch {
	case shared:
		scratchDir := filepath.Join(sm.paths.DataDir, "scratch", id)
		teardownErr = os.RemoveAll(scratchDir)
	case inPlace:
		// In-place sessions: leave the repo completely untouched
	case repoPath != "" && len(sessionIncludes) > 0:
		teardownErr = sm.teardownIncludes(repoPath, worktreePath, branch, sessionIncludes)
	case repoPath != "":
		teardownErr = git.TeardownSession(repoPath, worktreePath, branch)
	case worktreePath != "":
		teardownErr = os.RemoveAll(worktreePath)
	}

	if teardownErr != nil {
		sm.log.Error("git teardown failed, session kept for retry",
			"session_id", id, "err", teardownErr)
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			if prevStatus == StatusRunning {
				s.Status = StatusStopped
			} else {
				s.Status = prevStatus
			}
			_ = sm.saveState()
		}
		sm.mu.Unlock()
		if hasClient {
			ac.kick()
		}
		return fmt.Errorf("git teardown failed (session kept for retry): %w", teardownErr)
	}

	sm.mu.Lock()
	delete(sm.state.Sessions, id)
	delete(sm.hookReports, id)
	err := sm.saveState()
	sm.mu.Unlock()

	_ = os.Remove(filepath.Join(sm.paths.LogDir, id+".log"))
	sm.cleanupHooks(id, agentName, worktreePath)

	if hasClient {
		ac.kick()
	}

	return err
}

// DeleteWithChildren deletes a session and all its transitive descendants.
// Git teardown is attempted before removing each session from state; sessions
// whose teardown fails are kept for retry. Returns the list of deleted session
// IDs and an error if any teardowns failed.
func (sm *SessionManager) DeleteWithChildren(id string, excludeRoot bool) ([]string, error) {
	sm.mu.Lock()

	sess, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q not found", id)
	}
	if !excludeRoot && sess.Starred {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q is starred; unstar it first to delete", id)
	}

	toDelete := sm.collectDescendants(id)
	if excludeRoot {
		toDelete = filterExcludeRoot(toDelete, id)
	}

	type snapshot struct {
		id           string
		agent        string
		repoPath     string
		worktreePath string
		branch       string
		shared       bool
		inPlace      bool
		prevStatus   SessionStatus
		includes     []IncludedRepoState
		ptySess      *grpty.Session
		client       *attachedClient
	}

	snaps := make([]snapshot, 0, len(toDelete))
	var creatingIDs []string
	for _, did := range toDelete {
		sess := sm.state.Sessions[did]
		if sess.Starred {
			sm.log.Info("skipping starred session in bulk delete", "session_id", did, "name", sess.Name)
			continue
		}
		if sess.Status == StatusDeleting {
			continue
		}

		if sess.Status == StatusCreating {
			// Mid-creation: remove from state so Phase 3 detects the deletion.
			delete(sm.state.Sessions, did)
			delete(sm.hookReports, did)
			if ac, ok := sm.attachedClients[did]; ok {
				delete(sm.attachedClients, did)
				creatingIDs = append(creatingIDs, did)
				_ = ac // kick after unlock
			} else {
				creatingIDs = append(creatingIDs, did)
			}
			continue
		}

		s := snapshot{
			id:           did,
			agent:        sess.Agent,
			repoPath:     sess.RepoPath,
			worktreePath: sess.WorktreePath,
			branch:       sess.Branch,
			shared:       sess.SharedWorktree,
			inPlace:      sess.InPlace,
			prevStatus:   sess.Status,
			includes:     make([]IncludedRepoState, len(sess.Includes)),
		}
		copy(s.includes, sess.Includes)
		if pty, ok := sm.sessions[did]; ok {
			s.ptySess = pty
			delete(sm.sessions, did)
		}
		if ac, ok := sm.attachedClients[did]; ok {
			s.client = ac
			delete(sm.attachedClients, did)
		}
		snaps = append(snaps, s)
		sess.Status = StatusDeleting
		sess.StatusChangedAt = time.Now()
		sess.PID = 0
	}
	_ = sm.saveState()
	sm.mu.Unlock()

	// Kill all PTY processes outside the lock.
	for _, s := range snaps {
		if s.ptySess != nil {
			s.ptySess.Detach()
			if !s.ptySess.Exited() {
				_ = s.ptySess.Kill()
				select {
				case <-s.ptySess.Done():
				case <-time.After(5 * time.Second):
					_ = s.ptySess.ForceKill()
				}
			}
			s.ptySess.Close()
		}
	}

	// Attempt teardowns, tracking which succeed.
	var teardownErrs []error
	succeeded := make(map[string]bool, len(snaps))
	for _, s := range snaps {
		var err error
		switch {
		case s.shared:
			err = os.RemoveAll(filepath.Join(sm.paths.DataDir, "scratch", s.id))
		case s.inPlace:
		case s.repoPath != "" && len(s.includes) > 0:
			err = sm.teardownIncludes(s.repoPath, s.worktreePath, s.branch, s.includes)
		case s.repoPath != "":
			err = git.TeardownSession(s.repoPath, s.worktreePath, s.branch)
		case s.worktreePath != "":
			err = os.RemoveAll(s.worktreePath)
		}
		if err != nil {
			sm.log.Error("git teardown failed, session kept for retry",
				"session_id", s.id, "err", err)
			teardownErrs = append(teardownErrs, fmt.Errorf("session %s: %w", s.id, err))
		} else {
			succeeded[s.id] = true
		}
	}

	// Remove successfully torn-down sessions; revert failed ones to their prior status.
	sm.mu.Lock()
	deletedIDs := append([]string{}, creatingIDs...)
	for _, s := range snaps {
		if succeeded[s.id] {
			delete(sm.state.Sessions, s.id)
			delete(sm.hookReports, s.id)
			deletedIDs = append(deletedIDs, s.id)
		} else if sess, ok := sm.state.Sessions[s.id]; ok {
			if s.prevStatus == StatusRunning {
				sess.Status = StatusStopped
			} else {
				sess.Status = s.prevStatus
			}
		}
	}
	stateErr := sm.saveState()
	sm.mu.Unlock()

	for _, s := range snaps {
		if succeeded[s.id] {
			_ = os.Remove(filepath.Join(sm.paths.LogDir, s.id+".log"))
			sm.cleanupHooks(s.id, s.agent, s.worktreePath)
		}
		if s.client != nil {
			s.client.kick()
		}
	}

	if stateErr != nil {
		return deletedIDs, stateErr
	}
	if len(teardownErrs) > 0 {
		return deletedIDs, fmt.Errorf("git teardown failed for %d session(s) (kept for retry): %w",
			len(teardownErrs), errors.Join(teardownErrs...))
	}
	return deletedIDs, nil
}

// collectDescendants returns the target ID plus all transitive children, leaves first.
func (sm *SessionManager) collectDescendants(rootID string) []string {
	children := make(map[string][]string)
	for id, sess := range sm.state.Sessions {
		if sess.ParentID != "" {
			children[sess.ParentID] = append(children[sess.ParentID], id)
		}
	}

	var result []string
	seen := make(map[string]bool)
	var walk func(string)
	walk = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		for _, child := range children[id] {
			walk(child)
		}
		result = append(result, id)
	}
	walk(rootID)
	return result
}

// Stop sends SIGTERM to a session's process without removing the session or worktree.
func (sm *SessionManager) Stop(id string) error {
	sm.mu.Lock()
	sessState, ok := sm.state.Sessions[id]
	sm.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	if sessState.Status != StatusRunning {
		return fmt.Errorf("session %q is not running", id)
	}

	ptySess, ok := sm.GetPTY(id)
	if !ok {
		return fmt.Errorf("session %q has no PTY", id)
	}

	if err := ptySess.Kill(); err != nil {
		return fmt.Errorf("send SIGTERM: %w", err)
	}
	return nil
}

func filterExcludeRoot(ids []string, rootID string) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != rootID {
			result = append(result, id)
		}
	}
	return result
}

// StopWithChildren stops all descendants of rootID. If excludeRoot is true,
// the root session itself is not stopped. Already-stopped sessions are skipped.
// Returns the list of session IDs that were actually stopped.
func (sm *SessionManager) StopWithChildren(rootID string, excludeRoot bool) ([]string, error) {
	sm.mu.Lock()

	if _, ok := sm.state.Sessions[rootID]; !ok {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q not found", rootID)
	}

	toStop := sm.collectDescendants(rootID)
	if excludeRoot {
		toStop = filterExcludeRoot(toStop, rootID)
	}

	sm.mu.Unlock()

	var stopped []string
	for _, id := range toStop {
		sm.mu.Lock()
		sess, ok := sm.state.Sessions[id]
		sm.mu.Unlock()
		if !ok {
			continue
		}
		if sess.Starred {
			sm.log.Info("skipping starred session in bulk stop", "session_id", id, "name", sess.Name)
			continue
		}
		if sess.Status != StatusRunning {
			continue
		}
		ptySess, ok := sm.GetPTY(id)
		if !ok {
			continue
		}
		if err := ptySess.Kill(); err != nil {
			sm.log.Warn("stop child failed", "session_id", id, "error", err)
			continue
		}
		stopped = append(stopped, id)
	}

	return stopped, nil
}

// Restart stops a running session (or no-ops if already stopped) and resumes it,
// picking up the current agent and sandbox configuration.
func (sm *SessionManager) Restart(id string, rows, cols uint16) (SessionState, error) {
	ptySess, hasPTY := sm.GetPTY(id)
	if hasPTY && !ptySess.Exited() {
		if err := ptySess.Kill(); err != nil {
			return SessionState{}, fmt.Errorf("stop session: %w", err)
		}
		<-ptySess.Done()

		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok && s.Status == StatusRunning {
			exitCode := ptySess.ExitCode()
			s.Status = StatusStopped
			s.StatusChangedAt = time.Now()
			s.ExitCode = &exitCode
			s.PID = 0
			sm.saveState()
		}
		sm.mu.Unlock()
	}

	return sm.Resume(id, rows, cols)
}

// Rename changes the display name of a session.
func (sm *SessionManager) Star(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	if s.Status == StatusDeleting {
		return fmt.Errorf("session %q is being deleted", id)
	}
	s.Starred = true
	return sm.saveState()
}

func (sm *SessionManager) Unstar(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	if s.Status == StatusDeleting {
		return fmt.Errorf("session %q is being deleted", id)
	}
	s.Starred = false
	return sm.saveState()
}

func sanitizeSummaryText(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r >= 32 && r != 127 {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func (sm *SessionManager) SetSummary(sessionID, text string, ttlSeconds int) error {
	text = sanitizeSummaryText(text)
	if len(text) > 100 {
		return fmt.Errorf("summary text exceeds 100 bytes")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}

	now := time.Now()
	s.SummaryText = text
	s.SummarySetAt = &now
	s.SummaryTTL = ttlSeconds

	if text == "" {
		s.SummaryText = ""
		s.SummarySetAt = nil
		s.SummaryTTL = 0
	}

	return sm.saveState()
}

func (sm *SessionManager) ClearSummary(sessionID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}

	s.SummaryText = ""
	s.SummarySetAt = nil
	s.SummaryTTL = 0

	return sm.saveState()
}

func (sm *SessionManager) Rename(id, newName string) error {
	if err := ValidateSessionName(newName); err != nil {
		return err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	s.Name = newName
	return sm.saveState()
}

// List returns copies of all known session states.
func (sm *SessionManager) List() []SessionState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	list := make([]SessionState, 0, len(sm.state.Sessions))
	for _, s := range sm.state.Sessions {
		list = append(list, cloneSessionState(s))
	}
	return list
}

func (sm *SessionManager) fleetSummary() protocol.FleetSummary {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var f protocol.FleetSummary
	for _, s := range sm.state.Sessions {
		f.Total++
		switch s.Status {
		case StatusRunning:
			switch s.AgentStatus {
			case "approval":
				f.Approval++
			case "ready":
				f.Ready++
			default:
				f.Active++
			}
		case StatusCreating:
			f.Active++
		case StatusStopped:
			f.Stopped++
		case StatusErrored:
			f.Errored++
		}
	}
	return f
}

// Diagnostics collects runtime health data for gr doctor.
func (sm *SessionManager) Diagnostics() protocol.DiagnosticsMsg {
	sm.mu.RLock()
	cfg := sm.cfg
	now := time.Now()

	var sessions []protocol.SessionDiagnostic
	var sbDiag protocol.ScrollbackDiagnostic
	var fleet protocol.FleetSummary

	for id, s := range sm.state.Sessions {
		sd := protocol.SessionDiagnostic{
			ID:          id,
			Name:        s.Name,
			Status:      string(s.Status),
			AgentStatus: s.AgentStatus,
			PID:         s.PID,
		}

		// Tally fleet summary from the same snapshot as the session list.
		fleet.Total++
		switch s.Status {
		case StatusRunning:
			switch s.AgentStatus {
			case "approval":
				fleet.Approval++
			case "ready":
				fleet.Ready++
			default:
				fleet.Active++
			}
		case StatusCreating:
			fleet.Active++
		case StatusStopped:
			fleet.Stopped++
		case StatusErrored:
			fleet.Errored++
		}

		if s.Status == StatusRunning && s.PID > 0 {
			sd.PIDAlive = isProcessAlive(s.PID)
		}

		if s.WorktreePath != "" {
			sd.WorktreePath = s.WorktreePath
			if _, err := os.Stat(s.WorktreePath); err == nil {
				sd.WorktreeExists = true
			}
		}

		sd.ConfigStale = isConfigStale(*s, cfg)

		if hr, ok := sm.hookReports[id]; ok && s.Status == StatusRunning {
			sd.HookStale = now.After(hr.AuthoritativeUntil)
		}

		if ptySess, ok := sm.sessions[id]; ok {
			written, maxSize, saturated := ptySess.Scrollback.Stats()
			sd.ScrollbackBytes = written
			sd.ScrollbackMax = maxSize
			sd.Saturated = saturated

			sbDiag.TotalFiles++
			sbDiag.TotalBytes += written
			if saturated {
				sbDiag.SaturatedCount++
			}
		}

		sessions = append(sessions, sd)
	}
	sm.mu.RUnlock()

	var msgDiag protocol.MessagesDiagnostic
	if sm.messages != nil {
		if streams, err := sm.messages.ListStreams("", true); err == nil {
			msgDiag.TotalStreams = len(streams)
			for _, s := range streams {
				msgDiag.TotalMessages += s.Total
			}
		}
	}

	return protocol.DiagnosticsMsg{
		DaemonPID:    os.Getpid(),
		DaemonUptime: now.Sub(sm.startedAt).Truncate(time.Second).String(),
		Fleet:        fleet,
		Sessions:     sessions,
		Scrollback:   sbDiag,
		Messages:     msgDiag,
	}
}

// Get returns a copy of a session state by ID.
func (sm *SessionManager) Get(id string) (SessionState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.state.Sessions[id]
	if !ok {
		return SessionState{}, false
	}
	return cloneSessionState(s), ok
}

// GetPTY returns the live PTY session by ID.
func (sm *SessionManager) GetPTY(id string) (*grpty.Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[id]
	return s, ok
}

// StopAll gracefully terminates all running sessions.
func (sm *SessionManager) StopAll(ctx context.Context) {
	sm.mu.Lock()
	type snapshot struct {
		id   string
		sess *grpty.Session
	}
	sessions := make([]snapshot, 0, len(sm.sessions))
	for id, sess := range sm.sessions {
		sessions = append(sessions, snapshot{id, sess})
	}
	sm.mu.Unlock()

	for _, s := range sessions {
		if !s.sess.Exited() {
			sm.log.Info("stopping session", "id", s.id)
			_ = s.sess.Kill()
		}
	}

	for _, s := range sessions {
		select {
		case <-s.sess.Done():
		case <-time.After(5 * time.Second):
			sm.log.Warn("force killing session", "id", s.id)
			_ = s.sess.ForceKill()
		}
	}
}

func (sm *SessionManager) RunMessageCleanupLoop(ctx context.Context) {
	if sm.messages == nil {
		return
	}

	sm.runMessageCleanupFromConfig()

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sm.runMessageCleanupFromConfig()
		}
	}
}

func (sm *SessionManager) runMessageCleanupFromConfig() {
	sm.mu.RLock()
	maxAge := sm.cfg.Messages.MaxAgeDuration()
	maxPerStream := sm.cfg.Messages.MaxPerStream
	sm.mu.RUnlock()
	if maxAge == 0 && maxPerStream == 0 {
		return
	}
	sm.runMessageCleanup(maxAge, maxPerStream)
}

func (sm *SessionManager) runMessageCleanup(maxAge time.Duration, maxPerStream int) {
	deleted, err := sm.messages.Cleanup(maxAge, maxPerStream)
	if err != nil {
		sm.log.Error("message cleanup failed", "err", err)
		return
	}
	if deleted > 0 {
		sm.log.Info("message cleanup", "deleted", deleted)
	}
}

// RunDetectionLoop periodically scans PTY scrollback to detect agent status
// (active, needs approval, ready) for all running sessions.
func (sm *SessionManager) RunDetectionLoop(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sm.detectAgentStatuses()
		}
	}
}

func (sm *SessionManager) detectAgentStatuses() {
	sm.mu.RLock()
	type target struct {
		id           string
		name         string
		agent        string
		prevStatus   string
		pty          *grpty.Session
		worktreePath string
		baseBranch   string
		repoPath     string
		includes     []IncludedRepoState
	}
	var targets []target
	for id, s := range sm.state.Sessions {
		if s.Status != StatusRunning {
			continue
		}
		if ptySess, ok := sm.sessions[id]; ok {
			inc := make([]IncludedRepoState, len(s.Includes))
			copy(inc, s.Includes)
			targets = append(targets, target{
				id: id, name: s.Name, agent: s.Agent, prevStatus: s.AgentStatus, pty: ptySess,
				worktreePath: s.WorktreePath, baseBranch: s.BaseBranch, repoPath: s.RepoPath,
				includes: inc,
			})
		}
	}
	sm.mu.RUnlock()

	var toAutoStop []string

	for _, t := range targets {
		var status string

		// Check if we have an authoritative hook report for this session
		sm.mu.RLock()
		hr, hasHook := sm.hookReports[t.id]
		sm.mu.RUnlock()

		if hasHook && time.Now().Before(hr.AuthoritativeUntil) {
			status = hr.Status
		} else {
			// Fall back to PTY scraping
			content := t.pty.ScreenPreview()
			if content == "" {
				continue
			}

			outputAge := detector.OutputAgeUnknown
			if lastOut := t.pty.LastOutputAt(); !lastOut.IsZero() {
				outputAge = time.Since(lastOut)
			}

			d := detector.New(t.agent)
			status = string(d.Detect(content, outputAge))

			if status == string(detector.StatusUnknown) && t.prevStatus != "" && t.pty.RecentlyAdopted(60*time.Second) {
				status = t.prevStatus
			}
		}

		var dirty bool
		var unpushed int
		if t.worktreePath != "" && t.repoPath != "" {
			if d, err := git.HasUncommittedChanges(t.worktreePath); err == nil {
				dirty = d
			}
			if t.baseBranch != "" {
				if n, err := git.UnpushedCommitCount(t.worktreePath, t.baseBranch); err == nil {
					unpushed = n
				}
			}
		}
		for i := range t.includes {
			inc := &t.includes[i]
			if d, err := git.HasUncommittedChanges(inc.WorktreePath); err == nil {
				inc.dirty = d
				dirty = dirty || d
			}
			if inc.BaseBranch != "" {
				if n, err := git.UnpushedCommitCount(inc.WorktreePath, inc.BaseBranch); err == nil {
					inc.unpushed = n
					unpushed += n
				}
			}
		}

		if status != t.prevStatus {
			sm.onAgentStatusChange(t.id, t.name, t.prevStatus, status)
		}

		sm.mu.Lock()
		if s, ok := sm.state.Sessions[t.id]; ok {
			if status != s.AgentStatus {
				s.StatusChangedAt = time.Now()
			}
			s.AgentStatus = status
			s.GitDirty = dirty
			s.GitUnpushed = unpushed
			for i := range s.Includes {
				if i < len(t.includes) {
					s.Includes[i].dirty = t.includes[i].dirty
					s.Includes[i].unpushed = t.includes[i].unpushed
				}
			}

			if sm.checkIdleSession(s) {
				toAutoStop = append(toAutoStop, t.id)
			}
		}
		sm.mu.Unlock()
	}

	for _, id := range toAutoStop {
		sm.mu.RLock()
		s, ok := sm.state.Sessions[id]
		if !ok {
			sm.mu.RUnlock()
			continue
		}
		_, hasClient := sm.attachedClients[id]
		stillIdle := !hasClient && s.AgentStatus == "ready"
		name := s.Name
		idleSince := s.IdleSince
		sm.mu.RUnlock()

		if !stillIdle {
			continue
		}

		var idleDur time.Duration
		if idleSince != nil {
			idleDur = time.Since(*idleSince)
		}
		sm.log.Info("auto-stopping idle session", "session", name, "id", id, "idle_duration", idleDur.Round(time.Second))
		if err := sm.Stop(id); err != nil {
			sm.log.Error("failed to auto-stop session", "id", id, "err", err)
		}
	}
}

// checkIdleSession updates the idle tracking for a session and returns true if it
// should be auto-stopped. Caller must hold sm.mu.
func (sm *SessionManager) checkIdleSession(s *SessionState) bool {
	_, hasClient := sm.attachedClients[s.ID]
	isIdle := !hasClient && s.AgentStatus == "ready"

	if isIdle {
		if s.IdleSince == nil {
			now := time.Now()
			s.IdleSince = &now
		}
	} else {
		s.IdleSince = nil
	}

	if s.IdleSince != nil {
		agentCfg := sm.cfg.Agents[s.Agent]
		timeout := agentCfg.IdleTimeoutDuration()
		if timeout > 0 && time.Since(*s.IdleSince) >= timeout {
			return true
		}
	}
	return false
}

// ReloadConfig loads the config from disk and swaps it in, logging what changed.
func (sm *SessionManager) ReloadConfig() error {
	cfg, err := config.LoadOrDefault(sm.configFile)
	if err != nil {
		return err
	}
	sm.mu.RLock()
	oldDataDir := sm.cfg.DataDir
	sm.mu.RUnlock()
	if cfg.DataDir != oldDataDir {
		return fmt.Errorf("data_dir changed from %q to %q: run 'gr daemon restart' to apply", oldDataDir, cfg.DataDir)
	}
	sm.applyConfig(cfg)
	return nil
}

func (sm *SessionManager) applyConfig(newCfg *config.Config) {
	sm.mu.Lock()
	old := sm.cfg
	sm.cfg = newCfg
	sm.mu.Unlock()

	if old.DefaultAgent != newCfg.DefaultAgent {
		sm.log.Info("config changed", "key", "default_agent", "old", old.DefaultAgent, "new", newCfg.DefaultAgent)
	}
	if old.BranchPrefix != newCfg.BranchPrefix {
		sm.log.Info("config changed", "key", "branch_prefix", "old", old.BranchPrefix, "new", newCfg.BranchPrefix)
	}
	if old.FetchOnCreate != newCfg.FetchOnCreate {
		sm.log.Info("config changed", "key", "fetch_on_create", "old", old.FetchOnCreate, "new", newCfg.FetchOnCreate)
	}
	if old.GitHubUsername != newCfg.GitHubUsername {
		sm.log.Info("config changed", "key", "github_username", "old", old.GitHubUsername, "new", newCfg.GitHubUsername)
	}
	if old.Keybindings != newCfg.Keybindings {
		sm.log.Info("config changed", "key", "keybindings")
	}
	if old.Notifications != newCfg.Notifications {
		sm.log.Info("config changed", "key", "notifications")
	}
	for name, agent := range newCfg.Agents {
		if oldAgent, ok := old.Agents[name]; !ok {
			sm.log.Info("config changed", "key", "agents", "action", "added", "agent", name)
		} else if oldAgent.Command != agent.Command || oldAgent.IdleTimeout != agent.IdleTimeout {
			sm.log.Info("config changed", "key", "agents", "action", "modified", "agent", name)
		}
	}
	for name := range old.Agents {
		if _, ok := newCfg.Agents[name]; !ok {
			sm.log.Info("config changed", "key", "agents", "action", "removed", "agent", name)
		}
	}
	if old.Sandbox.Enabled != newCfg.Sandbox.Enabled {
		sm.log.Info("config changed", "key", "sandbox.enabled", "old", old.Sandbox.Enabled, "new", newCfg.Sandbox.Enabled)
	}
	if fmt.Sprint(old.Sandbox.ReadDirs) != fmt.Sprint(newCfg.Sandbox.ReadDirs) {
		sm.log.Info("config changed", "key", "sandbox.read_dirs", "old", old.Sandbox.ReadDirs, "new", newCfg.Sandbox.ReadDirs)
	}
	if fmt.Sprint(old.Sandbox.WriteDirs) != fmt.Sprint(newCfg.Sandbox.WriteDirs) {
		sm.log.Info("config changed", "key", "sandbox.write_dirs", "old", old.Sandbox.WriteDirs, "new", newCfg.Sandbox.WriteDirs)
	}
	if fmt.Sprint(old.Sandbox.Features) != fmt.Sprint(newCfg.Sandbox.Features) {
		sm.log.Info("config changed", "key", "sandbox.features", "old", old.Sandbox.Features, "new", newCfg.Sandbox.Features)
	}
	if sm.mcpManager != nil {
		sm.mcpManager.Reload(newCfg)
		sm.log.Info("MCP manager config reloaded")
	}
}

func (sm *SessionManager) teardownIncludes(mainRepoPath, mainWorktreePath, mainBranch string, includes []IncludedRepoState) error {
	var errs []error
	for i := len(includes) - 1; i >= 0; i-- {
		inc := includes[i]
		if err := git.TeardownSession(inc.RepoPath, inc.WorktreePath, inc.Branch); err != nil {
			sm.log.Warn("failed to teardown included worktree", "repo", inc.RepoName, "path", inc.WorktreePath, "err", err)
			errs = append(errs, err)
		}
	}
	if err := git.TeardownSession(mainRepoPath, mainWorktreePath, mainBranch); err != nil {
		sm.log.Warn("failed to teardown main worktree", "path", mainWorktreePath, "err", err)
		errs = append(errs, err)
	}
	if len(includes) > 0 {
		if err := os.RemoveAll(filepath.Dir(mainWorktreePath)); err != nil {
			sm.log.Warn("failed to remove session directory", "path", filepath.Dir(mainWorktreePath), "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (sm *SessionManager) deriveSandboxIncludesWriteDirs(includes []IncludedRepoState) []string {
	var dirs []string
	for _, inc := range includes {
		dirs = append(dirs, inc.WorktreePath)
		gitDir, commonDir, err := git.WorktreeGitDirs(inc.WorktreePath)
		if err != nil {
			sm.log.Warn("failed to resolve git dirs for included repo", "repo", inc.RepoName, "err", err)
			continue
		}
		dirs = append(dirs, gitDir, commonDir)
	}
	return dirs
}

func (sm *SessionManager) resolveSandbox(agentName string) (bool, error) {
	merged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[agentName].Sandbox)
	if !merged.Enabled {
		return false, nil
	}
	cmd := merged.Command
	if cmd == "" {
		cmd = "safehouse"
	}
	if !sandbox.AvailableCommand(cmd) {
		return false, fmt.Errorf("sandbox enabled for agent %q but %q is not available — install safehouse or disable sandbox in config", agentName, cmd)
	}
	return true, nil
}

func (sm *SessionManager) sandboxOptsFromConfig(merged config.SandboxConfig, sessionID, worktreePath string, envKeys []string, agentHooks bool) sandbox.WrapOpts {
	readDirs := expandPaths(merged.ReadDirs)
	writeDirs := expandPaths(merged.WriteDirs)

	if agentHooks {
		hd := sm.hookDir(sessionID)
		if _, err := os.Stat(hd); err == nil {
			readDirs = append(readDirs, hd)
		}
	}
	readDirs = append(readDirs, filepath.Dir(sm.paths.ConfigFile))
	if grBin := resolveGrBin(); grBin != "gr" {
		readDirs = append(readDirs, filepath.Dir(grBin))
	}
	readDirs = append(readDirs, sm.paths.RuntimeDir)

	return sandbox.WrapOpts{
		WorktreeDir:      worktreePath,
		ReadDirs:         readDirs,
		WriteDirs:        writeDirs,
		Features:         merged.Features,
		EnvKeys:          envKeys,
		SafehouseCommand: merged.Command,
	}
}

func expandPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	var out []string
	for _, p := range paths {
		expanded := config.ExpandPath(p)
		if strings.ContainsAny(expanded, "*?[") {
			if matches, err := filepath.Glob(expanded); err == nil && len(matches) > 0 {
				out = append(out, matches...)
				continue
			}
		}
		out = append(out, expanded)
	}
	return out
}

// cleanupLegacyDaemon stops an old daemon that may be listening on the
// pre-v0.11 socket path ($TMPDIR or /tmp). Without this, upgrading would
// leave an orphaned daemon since the new CLI can't reach the old socket.
func cleanupLegacyDaemon(log *slog.Logger) {
	for _, dir := range config.LegacyRuntimeDirs() {
		sock := filepath.Join(dir, "graith.sock")
		pid := filepath.Join(dir, "graith.pid")

		if _, err := os.Stat(sock); err != nil {
			continue
		}

		conn, err := net.DialTimeout("unix", sock, 500*time.Millisecond)
		if err != nil {
			os.Remove(sock)
			os.Remove(pid)
			log.Info("removed stale legacy socket", "path", sock)
			continue
		}
		conn.Close()

		data, err := os.ReadFile(pid)
		if err == nil {
			var legacyPID int
			if _, err := fmt.Sscanf(string(data), "%d", &legacyPID); err == nil && IsGraithDaemon(legacyPID) {
				log.Info("stopping legacy daemon", "pid", legacyPID, "socket", sock)
				_ = syscall.Kill(legacyPID, syscall.SIGTERM)
			}
		}

		os.Remove(sock)
		os.Remove(pid)
	}
}

// Run starts the daemon: acquires PID file, listens on the Unix socket,
// serves connections, and blocks until SIGTERM/SIGINT or an upgrade signal.
func Run(cfg *config.Config, paths config.Paths, configFile, adoptFrom string) error {
	if err := paths.EnsureDirs(); err != nil {
		return err
	}

	logFile, err := os.OpenFile(paths.DaemonLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	log := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))

	sm := NewSessionManager(cfg, paths, log)
	sm.configFile = configFile
	sm.upgradeCh = make(chan string, 1)

	msgStore, err := NewMsgStore(paths.MessagesDB)
	if err != nil {
		return fmt.Errorf("open message store: %w", err)
	}
	defer func() { _ = msgStore.Close() }()
	sm.messages = msgStore

	mcpMgr := NewMCPManager(cfg, []config.MCPServerConfig{graithMCPServer()}, paths.LogDir, log)
	sm.mcpManager = mcpMgr
	defer mcpMgr.Shutdown()

	var l net.Listener

	if adoptFrom != "" {
		manifest, err := ReadManifest(adoptFrom)
		if err != nil {
			return fmt.Errorf("read upgrade manifest: %w", err)
		}
		os.Remove(adoptFrom)

		if manifest.Profile != paths.Profile {
			return fmt.Errorf("profile mismatch: manifest has %q but daemon is %q", manifest.Profile, paths.Profile)
		}

		f := os.NewFile(uintptr(manifest.ListenerFd), "listener")
		l, err = net.FileListener(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("adopt listener from fd %d: %w", manifest.ListenerFd, err)
		}

		if err := sm.LoadState(); err != nil {
			log.Warn("failed to load state", "err", err)
		}
		if err := sm.AdoptSessions(manifest); err != nil {
			log.Warn("failed to adopt sessions", "err", err)
		}

		log.Info("daemon upgraded", "adopted_sessions", len(manifest.Sessions), "pid", os.Getpid())
	} else {
		if paths.Profile == "" {
			cleanupLegacyDaemon(log)
		}

		if err := AcquirePIDFile(paths.PIDFile); err != nil {
			return err
		}

		var listenErr error
		l, listenErr = Listen(paths.SocketPath)
		if listenErr != nil {
			ReleasePIDFile(paths.PIDFile)
			return listenErr
		}

		if err := sm.LoadState(); err != nil {
			log.Warn("failed to load state", "err", err)
		}

		logAttrs := []any{"socket", paths.SocketPath, "pid", os.Getpid()}
		if paths.Profile != "" {
			logAttrs = append(logAttrs, "profile", paths.Profile)
		}
		log.Info("daemon started", logAttrs...)
	}
	defer l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(l, func(ctx context.Context, conn net.Conn) {
		HandleConnection(ctx, conn, sm, log)
	}, log)

	go func() { _ = srv.Serve(ctx) }()
	go sm.RunDetectionLoop(ctx)
	go sm.RunMessageCleanupLoop(ctx)

	if configFile == "" {
		configFile = paths.ConfigFile
	}
	w := config.NewWatcher(configFile, sm.applyConfig, log)
	go func() {
		if err := w.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("config watcher stopped", "err", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	for {
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGHUP {
				log.Info("received SIGHUP, reloading config")
				if err := sm.ReloadConfig(); err != nil {
					log.Error("config reload failed", "err", err)
				}
				continue
			}
			log.Info("shutting down")
			cancel()
			sm.StopAll(ctx)
			srv.Shutdown()
			os.Remove(paths.SocketPath)
			ReleasePIDFile(paths.PIDFile)
			return nil

		case clientExecPath := <-sm.upgradeCh:
			log.Info("preparing upgrade", "client_exec_path", clientExecPath)

			// Kill MCP server processes before exec — defers don't
			// run when syscall.Exec replaces the process image.
			mcpMgr.Shutdown()

			unixL, ok := l.(*net.UnixListener)
			if !ok {
				log.Error("listener is not a unix listener, cannot upgrade")
				return fmt.Errorf("upgrade failed: listener type mismatch")
			}
			listenerFile, err := unixL.File()
			if err != nil {
				log.Error("get listener fd", "err", err)
				return fmt.Errorf("upgrade failed: %w", err)
			}
			listenerFd := listenerFile.Fd()

			manifest, err := sm.PrepareUpgrade(listenerFd, configFile)
			if err != nil {
				listenerFile.Close()
				log.Error("prepare upgrade", "err", err)
				return fmt.Errorf("upgrade failed: %w", err)
			}

			manifestPath, err := WriteManifest(paths.RuntimeDir, manifest)
			if err != nil {
				listenerFile.Close()
				log.Error("write manifest", "err", err)
				return fmt.Errorf("upgrade failed: %w", err)
			}

			log.Info("exec-ing new binary", "manifest", manifestPath, "sessions", len(manifest.Sessions))

			if err := ExecUpgrade(manifestPath, configFile, clientExecPath); err != nil {
				listenerFile.Close()
				os.Remove(manifestPath)
				log.Error("exec failed", "err", err)
				return fmt.Errorf("upgrade exec failed: %w", err)
			}
			return nil
		}
	}
}
