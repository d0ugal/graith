package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

	"github.com/d0ugal/graith/internal/chrome"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/detector"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/sandbox"
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
	chromeInstances  map[string]*chrome.Instance
	attachedClients  map[string]*attachedClient
	hookReports      map[string]hookReport
	pendingApprovals map[string]*pendingApproval
	cfg              *config.Config
	paths            config.Paths
	log              *slog.Logger
	configFile       string
	upgradeCh        chan string
	messages         *MsgStore
}

// NewSessionManager creates a SessionManager with the given config and paths.
func NewSessionManager(cfg *config.Config, paths config.Paths, log *slog.Logger) *SessionManager {
	return &SessionManager{
		state:            NewState(),
		sessions:         make(map[string]*grpty.Session),
		chromeInstances:  make(map[string]*chrome.Instance),
		attachedClients:  make(map[string]*attachedClient),
		hookReports:      make(map[string]hookReport),
		pendingApprovals: make(map[string]*pendingApproval),
		cfg:              cfg,
		paths:            paths,
		log:              log,
	}
}

func (sm *SessionManager) SetMsgStore(ms *MsgStore) {
	sm.messages = ms
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
func (sm *SessionManager) Create(name, agentName, repoPath, baseBranch, prompt, parentID string, noRepo bool, shareWorktree string, agentHooks bool, inPlace, allowConcurrent bool, rows, cols uint16) (SessionState, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if inPlace && noRepo {
		return SessionState{}, fmt.Errorf("--in-place and --no-repo are mutually exclusive")
	}
	if inPlace && shareWorktree != "" {
		return SessionState{}, fmt.Errorf("--in-place and --share-worktree are mutually exclusive")
	}
	if inPlace && baseBranch != "" {
		return SessionState{}, fmt.Errorf("--in-place and --base are mutually exclusive (in-place sessions don't create branches)")
	}

	agent, ok := sm.cfg.Agents[agentName]
	if !ok {
		return SessionState{}, fmt.Errorf("unknown agent %q", agentName)
	}

	id := generateID()

	var repoRoot, repoName, worktreePath, branchName string
	var sharedWorktree bool
	var sharedWorktreeSourceID string
	var includes []IncludedRepoState

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
			return SessionState{}, fmt.Errorf("session %q not found for --share-worktree", shareWorktree)
		}
		if source.WorktreePath == "" {
			return SessionState{}, fmt.Errorf("session %q has no worktree to share", shareWorktree)
		}
		worktreePath = source.WorktreePath
		repoRoot = source.RepoPath
		repoName = source.RepoName
		baseBranch = source.BaseBranch
		sharedWorktree = true
		sharedWorktreeSourceID = source.ID
	case noRepo:
		worktreePath = filepath.Join(sm.paths.DataDir, "scratch", id)
		if err := os.MkdirAll(worktreePath, 0o700); err != nil {
			return SessionState{}, fmt.Errorf("create scratch dir: %w", err)
		}
	case inPlace:
		if !git.IsInsideGitRepo(repoPath) {
			return SessionState{}, fmt.Errorf("not inside a git repository: %s", repoPath)
		}

		var err error
		repoRoot, err = git.RepoRootPath(repoPath)
		if err != nil {
			return SessionState{}, fmt.Errorf("find repo root: %w", err)
		}

		rc, ok := sm.cfg.FindRepo(repoRoot)
		if !ok {
			return SessionState{}, fmt.Errorf("repo root %q is not configured in [[repos]] — add it to config to use --in-place", repoRoot)
		}

		if len(rc.Includes) > 0 {
			return SessionState{}, fmt.Errorf("repo %q has includes configured — drop --in-place to create an includes session with worktrees", repoRoot)
		}

		if !allowConcurrent && !rc.AllowConcurrent {
			canonicalRoot := config.ResolvePath(repoRoot)
			for _, s := range sm.state.Sessions {
				if s.InPlace && config.ResolvePath(s.WorktreePath) == canonicalRoot && s.Status == StatusRunning {
					return SessionState{}, fmt.Errorf("an in-place session %q is already running in %q — use --allow-concurrent to override", s.Name, repoRoot)
				}
			}
		}

		repoName = filepath.Base(repoRoot)
		worktreePath = repoRoot
	default:
		if !sm.cfg.RepoPathAllowed(repoPath) {
			return SessionState{}, fmt.Errorf("repo path %q is not under any allowed_repo_paths", repoPath)
		}
		if !git.IsInsideGitRepo(repoPath) {
			return SessionState{}, fmt.Errorf("not inside a git repository: %s (use --no-repo for sessions without a repo)", repoPath)
		}

		var err error
		repoRoot, err = git.RepoRootPath(repoPath)
		if err != nil {
			return SessionState{}, fmt.Errorf("find repo root: %w", err)
		}

		rc, _ := sm.cfg.FindRepo(repoRoot)

		if rc.Singleton {
			canonicalRoot := config.ResolvePath(repoRoot)
			for _, s := range sm.state.Sessions {
				if config.ResolvePath(s.RepoPath) == canonicalRoot && s.Status == StatusRunning {
					return SessionState{}, fmt.Errorf("repo %q has singleton = true and session %q is already running — stop it first", repoRoot, s.Name)
				}
			}
		}

		if baseBranch == "" {
			baseBranch, err = git.DiscoverDefaultBranch(repoRoot)
			if err != nil {
				return SessionState{}, err
			}
		}

		repoName = filepath.Base(repoRoot)

		username := sm.cfg.GitHubUsername
		if username == "" {
			username, _ = git.DiscoverGitHubUsername(repoRoot)
		}
		if username == "" {
			username = "user"
		}

		branchPrefix, _ := config.Expand(sm.cfg.BranchPrefix, config.TemplateVars{Username: username})
		branchName = fmt.Sprintf("%s/%s-%s", branchPrefix, name, id)

		sessionDir := filepath.Join(sm.paths.DataDir, "worktrees", repoName, repoHash(repoRoot), id)

		if len(rc.Includes) > 0 {
			worktreePath = filepath.Join(sessionDir, repoName)
			if err := git.SetupSession(repoRoot, worktreePath, branchName, baseBranch, sm.cfg.FetchOnCreate); err != nil {
				return SessionState{}, fmt.Errorf("setup main repo git session: %w", err)
			}

			for _, incPath := range rc.Includes {
				resolved := config.ResolvePath(incPath)
				if !sm.cfg.RepoPathAllowed(resolved) {
					sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
					return SessionState{}, fmt.Errorf("included repo %q is not under any allowed_repo_paths", incPath)
				}
				if !git.IsInsideGitRepo(resolved) {
					sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
					return SessionState{}, fmt.Errorf("included repo %q is not a git repository", incPath)
				}
				incRoot, err := git.RepoRootPath(resolved)
				if err != nil {
					sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
					return SessionState{}, fmt.Errorf("find included repo root for %q: %w", incPath, err)
				}
				incName := filepath.Base(incRoot)
				incBaseBranch, err := git.DiscoverDefaultBranchOrHEAD(incRoot)
				if err != nil {
					sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
					return SessionState{}, fmt.Errorf("discover default branch for included repo %q: %w", incPath, err)
				}
				incBranch := fmt.Sprintf("%s/%s-%s/%s", branchPrefix, name, id, incName)
				incWorktreePath := filepath.Join(sessionDir, incName)

				if err := git.SetupSession(incRoot, incWorktreePath, incBranch, incBaseBranch, sm.cfg.FetchOnCreate); err != nil {
					sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
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
			worktreePath = sessionDir
			if err := git.SetupSession(repoRoot, worktreePath, branchName, baseBranch, sm.cfg.FetchOnCreate); err != nil {
				return SessionState{}, fmt.Errorf("setup git session: %w", err)
			}
		}
	}

	agentSessionID := ""
	if agentName == "claude" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		agentSessionID = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	}

	username := sm.cfg.GitHubUsername
	if username == "" && repoRoot != "" {
		username, _ = git.DiscoverGitHubUsername(repoRoot)
	}
	if username == "" {
		username = "user"
	}

	vars := config.TemplateVars{
		Username:       username,
		AgentSessionID: agentSessionID,
		SessionName:    name,
		SessionID:      id,
		WorktreePath:   worktreePath,
	}
	cleanupOnError := func() {
		sm.cleanupHooks(id)
		if sharedWorktree || inPlace {
			return
		}
		switch {
		case noRepo:
			os.RemoveAll(worktreePath)
		case len(includes) > 0:
			sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
		default:
			_ = git.TeardownSession(repoRoot, worktreePath, branchName)
		}
	}

	expandedArgs, err := config.ExpandSlice(agent.Args, vars)
	if err != nil {
		cleanupOnError()
		return SessionState{}, fmt.Errorf("expand agent args: %w", err)
	}
	if prompt != "" {
		expandedArgs = append(expandedArgs, prompt)
	}

	logPath := filepath.Join(sm.paths.LogDir, id+".log")

	env := make(map[string]string, len(agent.Env)+3)
	for k, v := range agent.Env {
		env[k] = v
	}
	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = name
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
		if source, ok := sm.state.Sessions[sharedWorktreeSourceID]; ok {
			for _, inc := range source.Includes {
				env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
			}
		}
	}

	sandboxed, err := sm.resolveSandbox(agentName)
	if err != nil {
		cleanupOnError()
		return SessionState{}, err
	}
	if sharedWorktree && !sandboxed {
		cleanupOnError()
		return SessionState{}, fmt.Errorf("--share-worktree requires sandbox to be enabled so the shared worktree can be mounted read-only; set sandbox.enabled = true in config and ensure safehouse is installed (gr doctor)")
	}

	if agentHooks {
		hookArgs, hookEnv, err := sm.injectHooks(agentName, id)
		if err != nil {
			cleanupOnError()
			return SessionState{}, fmt.Errorf("inject agent hooks: %w", err)
		}
		expandedArgs = append(expandedArgs, hookArgs...)
		for k, v := range hookEnv {
			env[k] = v
		}
	}
	chromeInst, err := sm.startChromeForSession(id, agentName)
	if err != nil {
		cleanupOnError()
		return SessionState{}, err
	}
	if chromeInst != nil {
		env["CHROME_REMOTE_DEBUGGING_URL"] = chromeInst.URL()
	}

	command := agent.Command
	finalArgs := expandedArgs
	var scratchDir string
	var mergedSandbox *config.SandboxConfig
	if sandboxed {
		merged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[agentName].Sandbox)
		merged.ReadDirs = expandPaths(merged.ReadDirs)
		merged.WriteDirs = expandPaths(merged.WriteDirs)
		mergedSandbox = &merged
		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_WORKTREE_PATH", "TERM"}
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
				if chromeInst != nil {
					chromeInst.Stop()
				}
				return SessionState{}, fmt.Errorf("create scratch dir: %w", err)
			}
			opts.ReadDirs = append(opts.ReadDirs, worktreePath)
			if source, ok := sm.state.Sessions[sharedWorktreeSourceID]; ok {
				for _, inc := range source.Includes {
					opts.ReadDirs = append(opts.ReadDirs, inc.WorktreePath)
				}
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
		if chromeInst != nil {
			chromeInst.Stop()
		}
		if scratchDir != "" {
			os.RemoveAll(scratchDir)
		}
		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	var chromePID, chromePort int
	var chromeDir string
	if chromeInst != nil {
		chromePID = chromeInst.PID()
		chromePort = chromeInst.Port()
		chromeDir = chromeInst.Dir()
		sm.chromeInstances[id] = chromeInst
	}

	sessState := &SessionState{
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
		Sandboxed:              sandboxed,
		SandboxConfig:          mergedSandbox,
		SharedWorktree:         sharedWorktree,
		SharedWorktreeSourceID: sharedWorktreeSourceID,
		InPlace:                inPlace,
		Includes:               includes,
		AgentHooks:             agentHooks,
		ChromePID:              chromePID,
		ChromePort:             chromePort,
		ChromeDir:              chromeDir,
		Status:                 StatusRunning,
		PID:                    ptySess.Cmd.Process.Pid,
		CreatedAt:              time.Now().UTC(),
	}

	sm.state.Sessions[id] = sessState
	sm.sessions[id] = ptySess

	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		delete(sm.sessions, id)
		sm.stopChromeForSession(id)
		_ = ptySess.Kill()
		ptySess.Close()
		cleanupOnError()
		if scratchDir != "" {
			os.RemoveAll(scratchDir)
		}
		sm.cleanupHooks(id)
		os.Remove(logPath)
		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	go sm.watchSession(id, ptySess)

	return *sessState, nil
}

// Fork creates a new session that branches from an existing session's git state
// and uses the agent's fork_args to carry over the conversation history.
func (sm *SessionManager) Fork(name, sourceSessionID string, rows, cols uint16) (SessionState, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	source, ok := sm.state.Sessions[sourceSessionID]
	if !ok {
		return SessionState{}, fmt.Errorf("source session %q not found", sourceSessionID)
	}

	if source.RepoPath == "" {
		return SessionState{}, fmt.Errorf("cannot fork session %q: source has no repo (fork requires a git repository)", source.Name)
	}

	if source.InPlace {
		return SessionState{}, fmt.Errorf("cannot fork session %q: in-place sessions cannot be forked", source.Name)
	}

	if rc, ok := sm.cfg.FindRepo(source.RepoPath); ok && rc.Singleton {
		return SessionState{}, fmt.Errorf("cannot fork session %q: repo %q has singleton = true — stop the source session first or remove the singleton constraint", source.Name, source.RepoPath)
	}

	agentName := source.Agent
	agent, ok := sm.cfg.Agents[agentName]
	if !ok {
		return SessionState{}, fmt.Errorf("unknown agent %q", agentName)
	}

	id := generateID()

	repoRoot := source.RepoPath
	repoName := source.RepoName
	baseBranch := source.BaseBranch

	username := sm.cfg.GitHubUsername
	if username == "" && repoRoot != "" {
		username, _ = git.DiscoverGitHubUsername(repoRoot)
	}
	if username == "" {
		username = "user"
	}

	branchPrefix, _ := config.Expand(sm.cfg.BranchPrefix, config.TemplateVars{Username: username})
	branchName := fmt.Sprintf("%s/%s-%s", branchPrefix, name, id)

	sessionDir := filepath.Join(sm.paths.DataDir, "worktrees", repoName, repoHash(repoRoot), id)

	var worktreePath string
	var forkIncludes []IncludedRepoState

	if len(source.Includes) > 0 {
		worktreePath = filepath.Join(sessionDir, repoName)
		if err := git.SetupSession(repoRoot, worktreePath, branchName, baseBranch, sm.cfg.FetchOnCreate); err != nil {
			return SessionState{}, fmt.Errorf("setup main repo git session for fork: %w", err)
		}
		for _, srcInc := range source.Includes {
			incBranch := fmt.Sprintf("%s/%s-%s/%s", branchPrefix, name, id, srcInc.RepoName)
			incWorktreePath := filepath.Join(sessionDir, srcInc.RepoName)
			if err := git.SetupSession(srcInc.RepoPath, incWorktreePath, incBranch, srcInc.Branch, sm.cfg.FetchOnCreate); err != nil {
				sm.teardownIncludes(repoRoot, worktreePath, branchName, forkIncludes)
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
		worktreePath = sessionDir
		if err := git.SetupSession(repoRoot, worktreePath, branchName, baseBranch, sm.cfg.FetchOnCreate); err != nil {
			return SessionState{}, fmt.Errorf("setup git session: %w", err)
		}
	}

	agentSessionID := ""
	if agentName == "claude" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		agentSessionID = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	}

	vars := config.TemplateVars{
		Username:                 username,
		AgentSessionID:           agentSessionID,
		SessionName:              name,
		SessionID:                id,
		WorktreePath:             worktreePath,
		ForkSourceAgentSessionID: source.AgentSessionID,
	}

	args := agent.ForkArgs
	if len(args) == 0 {
		args = agent.Args
	}
	forkCleanup := func() {
		sm.cleanupHooks(id)
		if len(forkIncludes) > 0 {
			sm.teardownIncludes(repoRoot, worktreePath, branchName, forkIncludes)
		} else {
			_ = git.TeardownSession(repoRoot, worktreePath, branchName)
		}
	}

	expandedArgs, err := config.ExpandSlice(args, vars)
	if err != nil {
		forkCleanup()
		return SessionState{}, fmt.Errorf("expand fork args: %w", err)
	}

	logPath := filepath.Join(sm.paths.LogDir, id+".log")

	env := make(map[string]string, len(agent.Env)+3)
	for k, v := range agent.Env {
		env[k] = v
	}
	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = name
	env["GRAITH_WORKTREE_PATH"] = worktreePath
	if sm.paths.Profile != "" {
		env["GRAITH_PROFILE"] = sm.paths.Profile
	}
	for _, inc := range forkIncludes {
		env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
	}

	if source.AgentHooks {
		hookArgs, hookEnv, err := sm.injectHooks(agentName, id)
		if err != nil {
			forkCleanup()
			return SessionState{}, fmt.Errorf("inject agent hooks: %w", err)
		}
		expandedArgs = append(expandedArgs, hookArgs...)
		for k, v := range hookEnv {
			env[k] = v
		}
	}

	chromeInst, err := sm.startChromeForSession(id, agentName)
	if err != nil {
		forkCleanup()
		return SessionState{}, err
	}
	if chromeInst != nil {
		env["CHROME_REMOTE_DEBUGGING_URL"] = chromeInst.URL()
	}

	sandboxed := source.Sandboxed
	command := agent.Command
	finalArgs := expandedArgs
	var mergedSandbox *config.SandboxConfig
	if sandboxed {
		merged := sm.resolveStoredSandboxConfig(source)
		mergedSandbox = &merged
		cmd := merged.Command
		if cmd == "" {
			cmd = "safehouse"
		}
		if !sandbox.AvailableCommand(cmd) {
			forkCleanup()
			if chromeInst != nil {
				chromeInst.Stop()
			}
			return SessionState{}, fmt.Errorf("source session was sandboxed but %q is no longer available", cmd)
		}
		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}
		for k := range env {
			envKeys = append(envKeys, k)
		}
		opts := sm.sandboxOptsFromConfig(merged, id, worktreePath, envKeys, source.AgentHooks)
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
		if chromeInst != nil {
			chromeInst.Stop()
		}
		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	var chromePID, chromePort int
	var chromeDir string
	if chromeInst != nil {
		chromePID = chromeInst.PID()
		chromePort = chromeInst.Port()
		chromeDir = chromeInst.Dir()
		sm.chromeInstances[id] = chromeInst
	}

	sessState := &SessionState{
		ID:             id,
		ParentID:       sourceSessionID,
		Name:           name,
		RepoPath:       repoRoot,
		RepoName:       repoName,
		WorktreePath:   worktreePath,
		Branch:         branchName,
		BaseBranch:     baseBranch,
		Agent:          agentName,
		AgentSessionID: agentSessionID,
		AgentHooks:     source.AgentHooks,
		Sandboxed:      sandboxed,
		SandboxConfig:  mergedSandbox,
		Includes:       forkIncludes,
		ChromePID:      chromePID,
		ChromePort:     chromePort,
		ChromeDir:      chromeDir,
		Status:         StatusRunning,
		PID:            ptySess.Cmd.Process.Pid,
		CreatedAt:      time.Now().UTC(),
	}

	sm.state.Sessions[id] = sessState
	sm.sessions[id] = ptySess

	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		delete(sm.sessions, id)
		sm.stopChromeForSession(id)
		_ = ptySess.Kill()
		ptySess.Close()
		forkCleanup()
		os.Remove(logPath)
		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	go sm.watchSession(id, ptySess)

	return *sessState, nil
}

// watchSession waits for a PTY session to exit and updates state accordingly.
// If the session has been replaced (e.g. by Resume) or removed (e.g. by Delete),
// the watcher is stale and skips the state update.
func (sm *SessionManager) watchSession(id string, sess *grpty.Session) {
	<-sess.Done()

	sm.mu.Lock()
	stale := sm.sessions[id] != sess
	var name string
	if !stale {
		sm.stopChromeForSession(id)
		if s, ok := sm.state.Sessions[id]; ok {
			name = s.Name
			exitCode := sess.ExitCode()
			s.Status = StatusStopped
			s.ExitCode = &exitCode
			s.PID = 0
			s.ChromePID = 0
			s.ChromePort = 0
			s.ChromeDir = ""
			if err := sm.saveState(); err != nil {
				sm.log.Error("failed to save state after session exit", "id", id, "err", err)
			}
		}
	}
	sm.mu.Unlock()

	if stale {
		sm.log.Info("ignoring stale session exit", "id", id, "exit_code", sess.ExitCode())
		return
	}

	sm.log.Info("session exited", "id", id, "exit_code", sess.ExitCode())
	sm.onAgentStatusChange(id, name, "running", "stopped")
}

// Resume restarts a stopped session using the agent's resume_args.
func (sm *SessionManager) Resume(id string, rows, cols uint16) (SessionState, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sessState, ok := sm.state.Sessions[id]
	if !ok {
		return SessionState{}, fmt.Errorf("session %q not found", id)
	}
	if sessState.Status == StatusRunning {
		return *sessState, nil
	}

	agent, ok := sm.cfg.Agents[sessState.Agent]
	if !ok {
		return SessionState{}, fmt.Errorf("unknown agent %q", sessState.Agent)
	}

	args := agent.ResumeArgs
	if len(args) == 0 {
		args = agent.Args
	}

	username := sm.cfg.GitHubUsername
	if username == "" {
		username, _ = git.DiscoverGitHubUsername(sessState.RepoPath)
	}
	if username == "" {
		username = "user"
	}

	vars := config.TemplateVars{
		Username:       username,
		AgentSessionID: sessState.AgentSessionID,
		SessionName:    sessState.Name,
		SessionID:      sessState.ID,
		WorktreePath:   sessState.WorktreePath,
	}
	expandedArgs, err := config.ExpandSlice(args, vars)
	if err != nil {
		return SessionState{}, fmt.Errorf("expand resume args: %w", err)
	}

	logPath := filepath.Join(sm.paths.LogDir, id+".log")

	env := make(map[string]string, len(agent.Env)+3)
	for k, v := range agent.Env {
		env[k] = v
	}
	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = sessState.Name
	env["GRAITH_WORKTREE_PATH"] = sessState.WorktreePath
	if sm.paths.Profile != "" {
		env["GRAITH_PROFILE"] = sm.paths.Profile
	}

	if sessState.SharedWorktree && !sessState.Sandboxed {
		return SessionState{}, fmt.Errorf("shared-worktree session %q was created without sandbox and cannot be resumed safely; delete and recreate with sandbox enabled", id)
	}

	if sessState.RepoPath != "" {
		if rc, ok := sm.cfg.FindRepo(sessState.RepoPath); ok && rc.Singleton {
			canonicalRoot := config.ResolvePath(sessState.RepoPath)
			for _, s := range sm.state.Sessions {
				if s.ID != id && config.ResolvePath(s.RepoPath) == canonicalRoot && s.Status == StatusRunning {
					return SessionState{}, fmt.Errorf("repo %q has singleton = true and session %q is already running — stop it first", sessState.RepoPath, s.Name)
				}
			}
		}
	}

	if sessState.InPlace {
		if !git.IsInsideGitRepo(sessState.WorktreePath) {
			return SessionState{}, fmt.Errorf("in-place repo path %q is no longer a git repository", sessState.WorktreePath)
		}
		currentRoot, err := git.RepoRootPath(sessState.WorktreePath)
		if err != nil {
			return SessionState{}, fmt.Errorf("resolve in-place repo root: %w", err)
		}
		if config.ResolvePath(currentRoot) != config.ResolvePath(sessState.WorktreePath) {
			return SessionState{}, fmt.Errorf("in-place repo root changed: saved %q, current %q", sessState.WorktreePath, currentRoot)
		}
		rc, ok := sm.cfg.FindRepo(sessState.WorktreePath)
		if !ok {
			return SessionState{}, fmt.Errorf("repo path %q is no longer configured in [[repos]] — add it back to config to resume this in-place session", sessState.WorktreePath)
		}
		if !rc.AllowConcurrent {
			for _, s := range sm.state.Sessions {
				if s.ID != id && s.InPlace && config.ResolvePath(s.WorktreePath) == config.ResolvePath(sessState.WorktreePath) && s.Status == StatusRunning {
					return SessionState{}, fmt.Errorf("another in-place session %q is already running in %q — stop it first or use allow_concurrent in config", s.Name, sessState.WorktreePath)
				}
			}
		}
		env["GRAITH_IN_PLACE"] = "true"
	}

	for _, inc := range sessState.Includes {
		if !git.IsInsideGitRepo(inc.WorktreePath) {
			return SessionState{}, fmt.Errorf("included worktree %q (%s) is no longer a valid git repo — delete and recreate the session", inc.WorktreePath, inc.RepoName)
		}
		env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
	}

	if sessState.AgentHooks {
		hookArgs, hookEnv, err := sm.injectHooks(sessState.Agent, id)
		if err != nil {
			return SessionState{}, fmt.Errorf("inject agent hooks: %w", err)
		}
		expandedArgs = append(expandedArgs, hookArgs...)
		for k, v := range hookEnv {
			env[k] = v
		}
	}

	chromeInst, err := sm.startChromeForSession(id, sessState.Agent)
	if err != nil {
		return SessionState{}, err
	}
	if chromeInst != nil {
		env["CHROME_REMOTE_DEBUGGING_URL"] = chromeInst.URL()
	}
	stopChrome := func() {
		if chromeInst != nil {
			chromeInst.Stop()
		}
	}

	command := agent.Command
	finalArgs := expandedArgs
	if sessState.Sandboxed {
		merged := sm.resolveStoredSandboxConfig(sessState)
		cmd := merged.Command
		if cmd == "" {
			cmd = "safehouse"
		}
		if !sandbox.AvailableCommand(cmd) {
			stopChrome()
			return SessionState{}, fmt.Errorf("session was sandboxed but %q is no longer available — install safehouse or delete and recreate the session", cmd)
		}
		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}
		for k := range env {
			envKeys = append(envKeys, k)
		}
		opts := sm.sandboxOptsFromConfig(merged, id, sessState.WorktreePath, envKeys, sessState.AgentHooks)
		if len(sessState.Includes) > 0 {
			opts.WriteDirs = append(opts.WriteDirs, sm.deriveSandboxIncludesWriteDirs(sessState.Includes)...)
		}
		if sessState.SharedWorktree {
			scratchDir := filepath.Join(sm.paths.DataDir, "scratch", id)
			if err := os.MkdirAll(scratchDir, 0o700); err != nil {
				stopChrome()
				return SessionState{}, fmt.Errorf("create scratch dir for shared worktree resume: %w", err)
			}
			opts.ReadDirs = append(opts.ReadDirs, sessState.WorktreePath)
			if source, ok := sm.state.Sessions[sessState.SharedWorktreeSourceID]; ok {
				for _, inc := range source.Includes {
					opts.ReadDirs = append(opts.ReadDirs, inc.WorktreePath)
				}
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
		Dir:        sessState.WorktreePath,
		Env:        env,
		Rows:       rows,
		Cols:       cols,
		LogPath:    logPath,
		MaxLogSize: 100 * 1024 * 1024,
	})
	if err != nil {
		stopChrome()
		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	prevStatus := sessState.Status
	prevExitCode := sessState.ExitCode
	prevPID := sessState.PID
	prevAgentStatus := sessState.AgentStatus
	prevChromePID := sessState.ChromePID
	prevChromePort := sessState.ChromePort
	prevChromeDir := sessState.ChromeDir

	sessState.Status = StatusRunning
	sessState.ExitCode = nil
	sessState.PID = ptySess.Cmd.Process.Pid
	sessState.AgentStatus = ""
	sessState.IdleSince = nil

	if chromeInst != nil {
		sessState.ChromePID = chromeInst.PID()
		sessState.ChromePort = chromeInst.Port()
		sessState.ChromeDir = chromeInst.Dir()
		sm.chromeInstances[id] = chromeInst
	}

	sm.sessions[id] = ptySess

	if err := sm.saveState(); err != nil {
		sessState.Status = prevStatus
		sessState.ExitCode = prevExitCode
		sessState.PID = prevPID
		sessState.AgentStatus = prevAgentStatus
		sessState.ChromePID = prevChromePID
		sessState.ChromePort = prevChromePort
		sessState.ChromeDir = prevChromeDir
		sm.stopChromeForSession(id)
		delete(sm.sessions, id)
		_ = ptySess.Kill()
		ptySess.Close()
		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	delete(sm.hookReports, id)
	go sm.watchSession(id, ptySess)

	return *sessState, nil
}

// Delete stops a session, removes its worktree/branch, and deletes state.
func (sm *SessionManager) Delete(id string) error {
	sm.mu.Lock()

	sessState, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("session %q not found", id)
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

	sm.stopChromeForSession(id)

	repoPath := sessState.RepoPath
	worktreePath := sessState.WorktreePath
	branch := sessState.Branch
	shared := sessState.SharedWorktree
	inPlace := sessState.InPlace
	sessionIncludes := make([]IncludedRepoState, len(sessState.Includes))
	copy(sessionIncludes, sessState.Includes)

	delete(sm.state.Sessions, id)
	delete(sm.hookReports, id)
	err := sm.saveState()
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

	switch {
	case shared:
		scratchDir := filepath.Join(sm.paths.DataDir, "scratch", id)
		_ = os.RemoveAll(scratchDir)
	case inPlace:
		// In-place sessions: leave the repo completely untouched
	case repoPath != "" && len(sessionIncludes) > 0:
		sm.teardownIncludes(repoPath, worktreePath, branch, sessionIncludes)
	case repoPath != "":
		_ = git.TeardownSession(repoPath, worktreePath, branch)
	case worktreePath != "":
		_ = os.RemoveAll(worktreePath)
	}
	_ = os.Remove(filepath.Join(sm.paths.LogDir, id+".log"))
	sm.cleanupHooks(id)

	if hasClient {
		ac.kick()
	}

	return err
}

// DeleteWithChildren deletes a session and all its transitive descendants.
// Returns the list of deleted session IDs.
func (sm *SessionManager) DeleteWithChildren(id string) ([]string, error) {
	sm.mu.Lock()

	if _, ok := sm.state.Sessions[id]; !ok {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q not found", id)
	}

	toDelete := sm.collectDescendants(id)

	type snapshot struct {
		id           string
		repoPath     string
		worktreePath string
		branch       string
		shared       bool
		inPlace      bool
		includes     []IncludedRepoState
		ptySess      *grpty.Session
		client       *attachedClient
	}

	snaps := make([]snapshot, 0, len(toDelete))
	for _, did := range toDelete {
		sess := sm.state.Sessions[did]
		s := snapshot{
			id:           did,
			repoPath:     sess.RepoPath,
			worktreePath: sess.WorktreePath,
			branch:       sess.Branch,
			shared:       sess.SharedWorktree,
			inPlace:      sess.InPlace,
			includes:     make([]IncludedRepoState, len(sess.Includes)),
		}
		copy(s.includes, sess.Includes)
		if pty, ok := sm.sessions[did]; ok {
			s.ptySess = pty
			delete(sm.sessions, did)
		}
		sm.stopChromeForSession(did)
		if ac, ok := sm.attachedClients[did]; ok {
			s.client = ac
			delete(sm.attachedClients, did)
		}
		snaps = append(snaps, s)
		delete(sm.state.Sessions, did)
		delete(sm.hookReports, did)
	}

	err := sm.saveState()
	sm.mu.Unlock()

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

		switch {
		case s.shared:
			_ = os.RemoveAll(filepath.Join(sm.paths.DataDir, "scratch", s.id))
		case s.inPlace:
		case s.repoPath != "" && len(s.includes) > 0:
			sm.teardownIncludes(s.repoPath, s.worktreePath, s.branch, s.includes)
		case s.repoPath != "":
			_ = git.TeardownSession(s.repoPath, s.worktreePath, s.branch)
		case s.worktreePath != "":
			_ = os.RemoveAll(s.worktreePath)
		}
		_ = os.Remove(filepath.Join(sm.paths.LogDir, s.id+".log"))
		sm.cleanupHooks(s.id)

		if s.client != nil {
			s.client.kick()
		}
	}

	if err != nil {
		return toDelete, err
	}
	return toDelete, nil
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

// Rename changes the display name of a session.
func (sm *SessionManager) Rename(id, newName string) error {
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
		case StatusStopped:
			f.Stopped++
		case StatusErrored:
			f.Errored++
		}
	}
	return f
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

	for id := range sm.chromeInstances {
		sm.stopChromeForSession(id)
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
	maxAge := sm.cfg.Messages.MaxAgeDuration()
	maxPerStream := sm.cfg.Messages.MaxPerStream
	if maxAge == 0 && maxPerStream == 0 {
		return
	}
	if sm.messages == nil {
		return
	}

	sm.runMessageCleanup(maxAge, maxPerStream)

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sm.runMessageCleanup(maxAge, maxPerStream)
		}
	}
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
}

func (sm *SessionManager) teardownIncludes(mainRepoPath, mainWorktreePath, mainBranch string, includes []IncludedRepoState) {
	for i := len(includes) - 1; i >= 0; i-- {
		inc := includes[i]
		if err := git.TeardownSession(inc.RepoPath, inc.WorktreePath, inc.Branch); err != nil {
			sm.log.Warn("failed to teardown included worktree", "repo", inc.RepoName, "path", inc.WorktreePath, "err", err)
		}
	}
	if err := git.TeardownSession(mainRepoPath, mainWorktreePath, mainBranch); err != nil {
		sm.log.Warn("failed to teardown main worktree", "path", mainWorktreePath, "err", err)
	}
	if len(includes) > 0 {
		if err := os.RemoveAll(filepath.Dir(mainWorktreePath)); err != nil {
			sm.log.Warn("failed to remove session directory", "path", filepath.Dir(mainWorktreePath), "err", err)
		}
	}
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

func (sm *SessionManager) startChromeForSession(id, agentName string) (*chrome.Instance, error) {
	merged := sm.cfg.Chrome.Merge(sm.cfg.Agents[agentName].Chrome)
	if !merged.Enabled {
		return nil, nil
	}

	inst, err := chrome.Start(chrome.StartOpts{
		ChromePath: merged.Path,
	})
	if err != nil {
		return nil, fmt.Errorf("start chrome for session %s: %w", id, err)
	}

	sm.log.Info("started chrome for session",
		"session_id", id, "chrome_pid", inst.PID(), "chrome_port", inst.Port())
	return inst, nil
}

func (sm *SessionManager) stopChromeForSession(id string) {
	inst, ok := sm.chromeInstances[id]
	if !ok {
		return
	}
	delete(sm.chromeInstances, id)

	sm.log.Info("stopping chrome for session", "session_id", id, "chrome_pid", inst.PID())
	if err := inst.Stop(); err != nil {
		sm.log.Warn("failed to stop chrome", "session_id", id, "err", err)
	}
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

// resolveStoredSandboxConfig returns the sandbox config persisted on the
// session. For sessions created before this field was stored, it falls back
// to re-merging from the current config.
func (sm *SessionManager) resolveStoredSandboxConfig(sess *SessionState) config.SandboxConfig {
	if sess.SandboxConfig != nil {
		return *sess.SandboxConfig
	}
	merged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[sess.Agent].Sandbox)
	merged.ReadDirs = expandPaths(merged.ReadDirs)
	merged.WriteDirs = expandPaths(merged.WriteDirs)
	return merged
}

func (sm *SessionManager) sandboxOptsFromConfig(merged config.SandboxConfig, sessionID, worktreePath string, envKeys []string, agentHooks bool) sandbox.WrapOpts {
	readDirs := expandPaths(merged.ReadDirs)
	writeDirs := expandPaths(merged.WriteDirs)

	if agentHooks {
		readDirs = append(readDirs, sm.hookDir(sessionID))
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
