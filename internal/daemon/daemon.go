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

// Create starts a new agent session, either in a git worktree or as a
// standalone scratch session (when noRepo is true).
func (sm *SessionManager) Create(name, agentName, repoPath, baseBranch, prompt string, noRepo bool, shareWorktree string, agentHooks bool, rows, cols uint16) (SessionState, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	agent, ok := sm.cfg.Agents[agentName]
	if !ok {
		return SessionState{}, fmt.Errorf("unknown agent %q", agentName)
	}

	id := generateID()

	var repoRoot, repoName, worktreePath, branchName string
	var sharedWorktree bool

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
	case noRepo:
		worktreePath = filepath.Join(sm.paths.DataDir, "scratch", id)
		if err := os.MkdirAll(worktreePath, 0o700); err != nil {
			return SessionState{}, fmt.Errorf("create scratch dir: %w", err)
		}
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

		worktreePath = filepath.Join(sm.paths.DataDir, "worktrees", repoName, repoHash(repoRoot), id)

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
		if sharedWorktree {
			return
		}
		if noRepo {
			os.RemoveAll(worktreePath)
		} else {
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

	if agentHooks {
		switch agentName {
		case "claude":
			hookArgs, hookEnv, err := sm.injectClaudeHooks(id)
			if err != nil {
				sm.log.Warn("failed to inject hooks", "session_id", id, "err", err)
			} else {
				expandedArgs = append(expandedArgs, hookArgs...)
				for k, v := range hookEnv {
					env[k] = v
				}
			}
		case "codex":
			hookArgs, hookEnv, err := sm.injectCodexHooks(id)
			if err != nil {
				sm.log.Warn("failed to inject hooks", "session_id", id, "err", err)
			} else {
				expandedArgs = append(expandedArgs, hookArgs...)
				for k, v := range hookEnv {
					env[k] = v
				}
			}
		}
	}

	sandboxed, err := sm.resolveSandbox(agentName)
	if err != nil {
		cleanupOnError()
		return SessionState{}, err
	}
	command := agent.Command
	finalArgs := expandedArgs
	var scratchDir string
	if sandboxed {
		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}
		for k := range env {
			envKeys = append(envKeys, k)
		}
		opts := sm.sandboxOpts(agentName, id, worktreePath, envKeys, agentHooks)
		if sharedWorktree {
			scratchDir = filepath.Join(sm.paths.DataDir, "scratch", id)
			if err := os.MkdirAll(scratchDir, 0o700); err != nil {
				cleanupOnError()
				return SessionState{}, fmt.Errorf("create scratch dir: %w", err)
			}
			opts.ReadDirs = append(opts.ReadDirs, worktreePath)
			opts.WorktreeDir = scratchDir
		}
		command, finalArgs = sandbox.Wrap(agent.Command, expandedArgs, opts)
		sm.log.Info("sandboxing session", "id", id, "agent", agentName)
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
		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	sessState := &SessionState{
		ID:             id,
		Name:           name,
		RepoPath:       repoRoot,
		RepoName:       repoName,
		WorktreePath:   worktreePath,
		Branch:         branchName,
		BaseBranch:     baseBranch,
		Agent:          agentName,
		AgentSessionID: agentSessionID,
		Sandboxed:      sandboxed,
		SharedWorktree: sharedWorktree,
		AgentHooks:     agentHooks,
		Status:         StatusRunning,
		PID:            ptySess.Cmd.Process.Pid,
		CreatedAt:      time.Now().UTC(),
	}

	sm.state.Sessions[id] = sessState
	sm.sessions[id] = ptySess

	go sm.watchSession(id, ptySess)

	if err := sm.saveState(); err != nil {
		sm.log.Error("failed to save state", "err", err)
	}

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

	agentName := source.Agent
	agent, ok := sm.cfg.Agents[agentName]
	if !ok {
		return SessionState{}, fmt.Errorf("unknown agent %q", agentName)
	}

	id := generateID()

	repoRoot := source.RepoPath
	repoName := source.RepoName
	baseBranch := source.Branch

	username := sm.cfg.GitHubUsername
	if username == "" && repoRoot != "" {
		username, _ = git.DiscoverGitHubUsername(repoRoot)
	}
	if username == "" {
		username = "user"
	}

	branchPrefix, _ := config.Expand(sm.cfg.BranchPrefix, config.TemplateVars{Username: username})
	branchName := fmt.Sprintf("%s/%s-%s", branchPrefix, name, id)

	worktreePath := filepath.Join(sm.paths.DataDir, "worktrees", repoName, repoHash(repoRoot), id)

	if err := git.SetupSession(repoRoot, worktreePath, branchName, baseBranch, sm.cfg.FetchOnCreate); err != nil {
		return SessionState{}, fmt.Errorf("setup git session: %w", err)
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
	expandedArgs, err := config.ExpandSlice(args, vars)
	if err != nil {
		_ = git.TeardownSession(repoRoot, worktreePath, branchName)
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

	if source.AgentHooks {
		switch agentName {
		case "claude":
			hookArgs, hookEnv, err := sm.injectClaudeHooks(id)
			if err != nil {
				sm.log.Warn("failed to inject hooks", "session_id", id, "err", err)
			} else {
				expandedArgs = append(expandedArgs, hookArgs...)
				for k, v := range hookEnv {
					env[k] = v
				}
			}
		case "codex":
			hookArgs, hookEnv, err := sm.injectCodexHooks(id)
			if err != nil {
				sm.log.Warn("failed to inject hooks", "session_id", id, "err", err)
			} else {
				expandedArgs = append(expandedArgs, hookArgs...)
				for k, v := range hookEnv {
					env[k] = v
				}
			}
		}
	}

	sandboxed := source.Sandboxed
	command := agent.Command
	finalArgs := expandedArgs
	if sandboxed {
		merged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[agentName].Sandbox)
		cmd := merged.Command
		if cmd == "" {
			cmd = "safehouse"
		}
		if !sandbox.AvailableCommand(cmd) {
			_ = git.TeardownSession(repoRoot, worktreePath, branchName)
			return SessionState{}, fmt.Errorf("source session was sandboxed but %q is no longer available", cmd)
		}
		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}
		for k := range env {
			envKeys = append(envKeys, k)
		}
		opts := sm.sandboxOpts(agentName, id, worktreePath, envKeys, source.AgentHooks)
		command, finalArgs = sandbox.Wrap(agent.Command, expandedArgs, opts)
		sm.log.Info("sandboxing forked session", "id", id)
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
		_ = git.TeardownSession(repoRoot, worktreePath, branchName)
		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	sessState := &SessionState{
		ID:             id,
		Name:           name,
		RepoPath:       repoRoot,
		RepoName:       repoName,
		WorktreePath:   worktreePath,
		Branch:         branchName,
		BaseBranch:     baseBranch,
		Agent:          agentName,
		AgentSessionID: agentSessionID,
		Sandboxed:      sandboxed,
		Status:         StatusRunning,
		PID:            ptySess.Cmd.Process.Pid,
		CreatedAt:      time.Now().UTC(),
	}

	sm.state.Sessions[id] = sessState
	sm.sessions[id] = ptySess

	go sm.watchSession(id, ptySess)

	if err := sm.saveState(); err != nil {
		sm.log.Error("failed to save state", "err", err)
	}

	return *sessState, nil
}

// watchSession waits for a PTY session to exit and updates state accordingly.
func (sm *SessionManager) watchSession(id string, sess *grpty.Session) {
	<-sess.Done()

	sm.mu.Lock()
	var name string
	if s, ok := sm.state.Sessions[id]; ok {
		name = s.Name
		exitCode := sess.ExitCode()
		s.Status = StatusStopped
		s.ExitCode = &exitCode
		s.PID = 0
		if err := sm.saveState(); err != nil {
			sm.log.Error("failed to save state after session exit", "id", id, "err", err)
		}
	}
	sm.mu.Unlock()

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

	delete(sm.hookReports, id)

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

	if sessState.AgentHooks {
		switch sessState.Agent {
		case "claude":
			hookArgs, hookEnv, err := sm.injectClaudeHooks(id)
			if err != nil {
				sm.log.Warn("failed to inject hooks", "session_id", id, "err", err)
			} else {
				expandedArgs = append(expandedArgs, hookArgs...)
				for k, v := range hookEnv {
					env[k] = v
				}
			}
		case "codex":
			hookArgs, hookEnv, err := sm.injectCodexHooks(id)
			if err != nil {
				sm.log.Warn("failed to inject hooks", "session_id", id, "err", err)
			} else {
				expandedArgs = append(expandedArgs, hookArgs...)
				for k, v := range hookEnv {
					env[k] = v
				}
			}
		}
	}

	command := agent.Command
	finalArgs := expandedArgs
	if sessState.Sandboxed {
		merged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[sessState.Agent].Sandbox)
		cmd := merged.Command
		if cmd == "" {
			cmd = "safehouse"
		}
		if !sandbox.AvailableCommand(cmd) {
			return SessionState{}, fmt.Errorf("session was sandboxed but %q is no longer available — install safehouse or delete and recreate the session", cmd)
		}
		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}
		for k := range env {
			envKeys = append(envKeys, k)
		}
		opts := sm.sandboxOpts(sessState.Agent, id, sessState.WorktreePath, envKeys, sessState.AgentHooks)
		command, finalArgs = sandbox.Wrap(agent.Command, expandedArgs, opts)
		sm.log.Info("sandboxing resumed session", "id", id)
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
		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	sessState.Status = StatusRunning
	sessState.ExitCode = nil
	sessState.PID = ptySess.Cmd.Process.Pid
	sessState.AgentStatus = ""

	sm.sessions[id] = ptySess
	go sm.watchSession(id, ptySess)

	if err := sm.saveState(); err != nil {
		sm.log.Error("failed to save state", "err", err)
	}

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

	repoPath := sessState.RepoPath
	worktreePath := sessState.WorktreePath
	branch := sessState.Branch
	shared := sessState.SharedWorktree

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
		list = append(list, *s)
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
	return *s, ok
}

// GetPTY returns the live PTY session by ID.
func (sm *SessionManager) GetPTY(id string) (*grpty.Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[id]
	return s, ok
}

// FindByName looks up a session by exact name, then by prefix match.
func (sm *SessionManager) FindByName(name string) (SessionState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, s := range sm.state.Sessions {
		if s.Name == name {
			return *s, true
		}
	}

	for _, s := range sm.state.Sessions {
		if len(name) > 0 && len(s.Name) >= len(name) && s.Name[:len(name)] == name {
			return *s, true
		}
	}

	return SessionState{}, false
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
	}
	var targets []target
	for id, s := range sm.state.Sessions {
		if s.Status != StatusRunning {
			continue
		}
		if ptySess, ok := sm.sessions[id]; ok {
			targets = append(targets, target{
				id: id, name: s.Name, agent: s.Agent, prevStatus: s.AgentStatus, pty: ptySess,
				worktreePath: s.WorktreePath, baseBranch: s.BaseBranch, repoPath: s.RepoPath,
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

		if status != t.prevStatus {
			sm.onAgentStatusChange(t.id, t.name, t.prevStatus, status)
		}

		sm.mu.Lock()
		if s, ok := sm.state.Sessions[t.id]; ok {
			s.AgentStatus = status
			s.GitDirty = dirty
			s.GitUnpushed = unpushed

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

func (sm *SessionManager) sandboxOpts(agentName, sessionID, worktreePath string, envKeys []string, agentHooks bool) sandbox.WrapOpts {
	merged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[agentName].Sandbox)

	readDirs := expandPaths(merged.ReadDirs)
	writeDirs := expandPaths(merged.WriteDirs)

	// The daemon injects hook scripts that call `gr report-status`.
	// That needs: the hooks dir itself, the config dir (to find the
	// socket path), and the runtime dir (to connect to it).
	if agentHooks {
		readDirs = append(readDirs, sm.hookDir(sessionID))
	}
	readDirs = append(readDirs, filepath.Dir(sm.paths.ConfigFile))
	if grBin := resolveGrBin(); grBin != "gr" {
		readDirs = append(readDirs, filepath.Dir(grBin))
	}
	readDirs = append(readDirs, sm.paths.RuntimeDir)

	// Agents need read/write access to their own config and data directories
	// (e.g. ~/.claude, ~/.local/share/claude for Claude Code).
	home, _ := os.UserHomeDir()
	switch agentName {
	case "claude":
		readDirs = append(readDirs, filepath.Join(home, ".claude"))
		writeDirs = append(writeDirs, filepath.Join(home, ".local", "share", "claude"))
	case "codex":
		readDirs = append(readDirs, filepath.Join(home, ".codex"))
	}

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
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = config.ExpandPath(p)
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
			if _, err := fmt.Sscanf(string(data), "%d", &legacyPID); err == nil && legacyPID > 0 {
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
		cleanupLegacyDaemon(log)

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

		log.Info("daemon started", "socket", paths.SocketPath, "pid", os.Getpid())
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

	if configFile != "" {
		w := config.NewWatcher(configFile, sm.applyConfig, log)
		go func() {
			if err := w.Run(ctx); err != nil && ctx.Err() == nil {
				log.Error("config watcher stopped", "err", err)
			}
		}()
	}

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
