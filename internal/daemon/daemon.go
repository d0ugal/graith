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
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/approvals"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/detector"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/d0ugal/graith/internal/store"
)

const (
	gitFetchTimeout    = 2 * time.Minute
	gitMergeTimeout    = 2 * time.Minute
	gitUsernameTimeout = 15 * time.Second
)

// gitPullStartupDelay is how long the git-pull loop waits after the daemon
// starts before its first tick. A daemon (re)start re-execs the loop from
// scratch, so waiting a full interval first would leave maintenance repos stale
// for up to that interval after every restart. A short delay instead lets
// session adoption and other startup work settle before the first pull. It is a
// var, not a const, so tests can shrink it.
var gitPullStartupDelay = 15 * time.Second

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
	ReportedAt         time.Time
	AuthoritativeUntil time.Time
}

// SessionManager orchestrates PTY sessions, state persistence, and git worktrees.
type SessionManager struct {
	mu                 sync.RWMutex
	state              *State
	sessions           map[string]*grpty.Session
	attachedClients    map[string]*attachedClient
	hookReports        map[string]hookReport
	pendingApprovals   map[string]*pendingApproval
	tokenIndex         map[string]string              // token → session ID (reverse lookup)
	pendingPairings    map[string]*pendingPairing     // requestID → pending device pairing (in-memory; not persisted)
	pairWaiters        map[string]chan pairApproval   // requestID → waiter for a blocked pair_request connection
	approvalSubs       map[net.Conn]func(string, any) // conn → sendControl for approval subscribers (no attach)
	remoteTLSPin       string                         // SPKI pin of the remote listener's cert (set once at startup; "" if remote disabled)
	deviceTokenIndex   map[string]string              // client-token HMAC → device ID (reverse lookup)
	connsByDevice      map[string][]net.Conn          // device ID → live remote connections (for revocation)
	pairReqTimes       []time.Time                    // recent pair_request timestamps (rate limiting)
	cfg                *config.Config
	paths              config.Paths
	log                *slog.Logger
	configFile         string
	upgradeCh          chan string
	messages           *MsgStore
	mcpManager         *MCPManager
	startedAt          time.Time
	orchestratorExitCh chan string
	recentExits        []time.Time
	lastInboxNotifyAt  map[string]time.Time
	prWatch            *prWatchState

	// approvalsWarnOnce guards the one-time [approvals] mode deprecation warning
	// so it fires once per daemon lifetime, not per approval request.
	approvalsWarnOnce sync.Once

	// watchers tracks in-flight watchSession goroutines. StopAll waits on it so
	// that post-exit state writes and status publishes complete before the
	// daemon (or a test harness) closes the message store and removes data dirs.
	watchers sync.WaitGroup
}

// NewSessionManager creates a SessionManager with the given config and paths.
func NewSessionManager(cfg *config.Config, paths config.Paths, log *slog.Logger) *SessionManager {
	return &SessionManager{
		state:              NewState(),
		sessions:           make(map[string]*grpty.Session),
		attachedClients:    make(map[string]*attachedClient),
		hookReports:        make(map[string]hookReport),
		pendingApprovals:   make(map[string]*pendingApproval),
		tokenIndex:         make(map[string]string),
		pendingPairings:    make(map[string]*pendingPairing),
		pairWaiters:        make(map[string]chan pairApproval),
		approvalSubs:       make(map[net.Conn]func(string, any)),
		deviceTokenIndex:   make(map[string]string),
		connsByDevice:      make(map[string][]net.Conn),
		orchestratorExitCh: make(chan string, 4),
		lastInboxNotifyAt:  make(map[string]time.Time),
		prWatch:            newPRWatchState(),
		cfg:                cfg,
		paths:              paths,
		log:                log,
		startedAt:          time.Now(),
	}
}

// Config returns a snapshot of the current config pointer, safe for use
// outside the lock. The returned *Config must not be modified.
func (sm *SessionManager) Config() *config.Config {
	sm.mu.RLock()
	cfg := sm.cfg
	sm.mu.RUnlock()

	return cfg
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
	var (
		status    string
		staleness time.Duration
	)

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
		ReportedAt:         now,
		AuthoritativeUntil: now.Add(staleness),
	}

	var (
		oldStatus string
		name      string
		changed   bool
	)

	sm.mu.Lock()

	sess, ok := sm.state.Sessions[sr.SessionID]
	if !ok {
		sm.mu.Unlock()
		sm.log.Info("hook report for unknown session", "session_id", sr.SessionID)

		return
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

func (sm *SessionManager) HasAttachedClient(sessionID string) bool {
	sm.mu.RLock()
	_, ok := sm.attachedClients[sessionID]
	sm.mu.RUnlock()

	return ok
}

// LoadState reads persisted state from disk and reconciles dead processes.
func (sm *SessionManager) LoadState() error {
	state, err := LoadState(sm.paths.StateFile)
	if err != nil {
		return err
	}

	state.Reconcile()
	sm.state = state
	sm.rebuildTokenIndex()
	sm.rebuildDeviceTokenIndex()

	return sm.saveState()
}

func (sm *SessionManager) rebuildTokenIndex() {
	sm.tokenIndex = make(map[string]string, len(sm.state.Sessions))
	for id, s := range sm.state.Sessions {
		if s.Token != "" {
			sm.tokenIndex[s.Token] = id
		}
	}
}

// SessionForToken returns the session ID that owns the given token, or empty
// string if the token is not recognized. Must be called under at least RLock.
func (sm *SessionManager) SessionForToken(token string) string {
	return sm.tokenIndex[token]
}

func (sm *SessionManager) AdoptSessions(manifest *UpgradeManifest) error {
	sm.mu.Lock()

	var adoptedIDs []string

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
			applyLifecycleSummaryLocked(sessState, "Lost during daemon upgrade")

			continue
		}

		if st, err := grpty.ProcessStartTime(us.PID); err == nil {
			sessState.PIDStartTime = st
		}

		sm.sessions[us.ID] = ptySess
		sm.startWatcher(us.ID, ptySess)
		adoptedIDs = append(adoptedIDs, us.ID)
		sm.log.Info("adopted session", "id", us.ID, "pid", us.PID)
	}

	err := sm.saveState()
	sm.mu.Unlock()

	for _, id := range adoptedIDs {
		go sm.notifyUnreadInbox(id)
	}

	return err
}

func (sm *SessionManager) saveState() error {
	return SaveState(sm.paths.StateFile, sm.state)
}

// availableRepos returns the distinct repositories the daemon can offer a
// remote client (which has no local cwd) for session creation (design §C.4).
// The picker is populated from two sources:
//
//   - the repos of live sessions, marked recent; and
//   - the configured repos, discovered the same way the local CLI/overlay
//     picker discovers them (client.DiscoverRepos): each configured entry is
//     treated as a root — added if it is itself a git repo, and scanned one
//     directory level for child git repos. This matters because
//     allowed_repo_paths entries are container roots (e.g. "~/Code"), not repo
//     paths, so listing them verbatim would offer a non-git directory that
//     create rejects. [[repos]] entries point straight at a repo and so are
//     added directly.
//
// Session repos are added first so they win on duplicate paths and keep their
// recent flag and session-derived name. Including configured repos matters:
// without them a daemon with no session in a given repo offers an empty or
// incomplete picker, so a remote user cannot pick a repository at all (#896).
//
// Only session/config state is read under the lock; the filesystem probes
// (git-repo checks + directory scans) run after releasing it, so repo_list —
// called whenever the New Session screen opens — never blocks a config reload
// or session create behind stat() calls.
func (sm *SessionManager) availableRepos() []protocol.RepoEntry {
	type repoRef struct{ path, name string }

	var (
		sessionRepos []repoRef
		configRoots  []string
	)

	sm.mu.RLock()

	for _, s := range sm.state.Sessions {
		if s.RepoPath == "" || s.IsSoftDeleted() {
			continue
		}

		sessionRepos = append(sessionRepos, repoRef{s.RepoPath, s.RepoName})
	}

	if sm.cfg != nil {
		configRoots = sm.cfg.AvailableRepoPaths()
	}

	sm.mu.RUnlock()

	seen := make(map[string]bool)

	var repos []protocol.RepoEntry

	// Dedup on the resolved path so a symlinked/`~` config path and a session's
	// already-resolved RepoPath for the same repo don't both appear, while the
	// entry keeps its original path (which create accepts either way).
	add := func(path, name string, recent bool) {
		if path == "" {
			return
		}

		key := config.ResolvePath(path)
		if seen[key] {
			return
		}

		seen[key] = true

		repos = append(repos, protocol.RepoEntry{Path: path, Name: name, Recent: recent})
	}

	for _, r := range sessionRepos {
		add(r.path, r.name, true)
	}

	for _, root := range configRoots {
		expanded := config.ResolvePath(root)
		if isGitRepo(expanded) {
			add(root, filepath.Base(expanded), false)
		}

		entries, err := os.ReadDir(expanded)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}

			child := filepath.Join(expanded, e.Name())
			if isGitRepo(child) {
				add(child, filepath.Base(child), false)
			}
		}
	}

	return repos
}

// isGitRepo reports whether dir looks like a git repo (or worktree) by the
// presence of a .git entry — matching the local picker's cheap check
// (client.isGitRepo). .git is a directory in a normal clone and a file in a
// linked worktree, so os.Stat (not a dir-only check) is what we want.
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))

	return err == nil
}

func generateID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

func repoHash(repoPath string) string {
	h := uint64(0)
	for _, c := range repoPath {
		h = h*31 + uint64(c) //nolint:gosec // G115: c is a rune from range-over-string, always a non-negative code point
	}

	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(h >> (i * 8)) //nolint:gosec // G115: intentional low-byte truncation for a hash digest
	}

	return hex.EncodeToString(b)[:12]
}

func (sm *SessionManager) repoTmpDir(repoRoot string) (string, error) {
	repoName := filepath.Base(repoRoot)

	dir := filepath.Join(sm.paths.TmpDir, repoName, repoHash(repoRoot))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create tmp dir: %w", err)
	}

	return dir, nil
}

func (sm *SessionManager) repoStoreDir(repoRoot string) (string, error) {
	dir := store.StorePath(sm.paths.DataDir, repoRoot)
	if err := store.Init(dir); err != nil {
		return "", fmt.Errorf("init store: %w", err)
	}

	return dir, nil
}

// Create starts a new agent session, either in a git worktree, in-place
// in an existing repo, or as a standalone scratch session (when noRepo is true).
//
// The method uses three-phase locking to avoid holding the daemon mutex during
// potentially blocking git/network operations (fetch, GitHub API calls, PTY spawn):
//  1. Lock: validate, reserve session as StatusCreating, unlock
//  2. Git setup and PTY spawn (no lock held)
//  3. Lock: commit to StatusRunning, unlock
func (sm *SessionManager) Create(name, agentName, repoPath, baseBranch, prompt, model, parentID string, noRepo bool, shareWorktree string, agentHooks bool, inPlace, allowConcurrent, skipModelValidation, yolo bool, rows, cols uint16, envExtra ...map[string]string) (SessionState, error) {
	if err := ValidateSessionName(name); err != nil {
		return SessionState{}, err
	}

	preLockCfg := sm.Config()

	agent, ok := preLockCfg.Agents[agentName]
	if !ok {
		return SessionState{}, fmt.Errorf("unknown agent %q", agentName)
	}

	if !skipModelValidation {
		if err := validateModel(agent, model); err != nil {
			return SessionState{}, err
		}
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

	preUsername := preLockCfg.GitHubUsername
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

	token, err := generateToken()
	if err != nil {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("generate session token: %w", err)
	}

	var (
		repoRoot, repoName, worktreePath, branchName string
		sharedWorktree                               bool
		sharedWorktreeSourceID                       string
		fetchOnCreate                                bool
		rcIncludes                                   []string
		sourceIncludes                               []IncludedRepoState
	)

	switch {
	case shareWorktree != "":
		var source *SessionState

		// Skip soft-deleted sessions: a hidden session must not be pickable as a
		// worktree source — its worktree is scheduled for purge.
		for _, s := range sm.state.Sessions {
			if s.IsSoftDeleted() {
				continue
			}

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
	if forcesID(agentName) {
		agentSessionID = newAgentSessionID()
	}

	// Resolve sandbox under the lock (reads config, fast).
	sandboxed, err := sm.resolveSandbox(agentName)
	if err != nil {
		if noRepo {
			_ = os.RemoveAll(worktreePath)
		}
		sm.mu.Unlock()

		return SessionState{}, err
	}

	// Fail closed if the configured approvals backend can't enforce.
	if err := sm.validateApprovalsBackend(yolo); err != nil {
		if noRepo {
			_ = os.RemoveAll(worktreePath)
		}
		sm.mu.Unlock()

		return SessionState{}, err
	}

	if sharedWorktree && !sandboxed {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("--share-worktree requires sandbox to be enabled so the shared worktree can be mounted read-only; set sandbox.enabled = true in config and ensure safehouse is installed (gr doctor)")
	}

	// Yolo requires the PreToolUse approval hook to function, so it forces agent
	// hooks on regardless of the requested value — otherwise a Yolo session with
	// hooks disabled would auto-mark itself yolo but never install the hook that
	// routes tool calls to the auto backend. Reassigning here keeps every
	// downstream use (MCP resolution, persisted AgentHooks, hook injection,
	// sandbox hook-dir allowance) consistent.
	agentHooks = agentHooks || yolo

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
		Yolo:                   yolo,
		Status:                 StatusCreating,
		CreatedAt:              time.Now().UTC(),
		StatusChangedAt:        time.Now().UTC(),
		Token:                  token,
	}

	sm.state.Sessions[id] = placeholder
	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)

		if noRepo {
			_ = os.RemoveAll(worktreePath)
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
		// A per-session nono profile may already have been written by
		// sandbox.Wrap before this error path ran; rollbackState deletes the
		// session from state, so no later Delete would remove it. Harmless if
		// no profile was written (os.Remove ignores a missing file).
		_ = os.Remove(sm.nonoProfilePath(id))
		_ = os.Remove(sm.safehouseFragmentPath(id))

		if sharedWorktree || inPlace {
			return
		}

		switch {
		case noRepo:
			_ = os.RemoveAll(worktreePath)
		case len(includes) > 0:
			_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
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
					_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)

					rollbackState()

					return SessionState{}, fmt.Errorf("included repo %q is not under any allowed_repo_paths", incPath)
				}

				if !git.IsInsideGitRepo(resolved) {
					_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)

					rollbackState()

					return SessionState{}, fmt.Errorf("included repo %q is not a git repository", incPath)
				}

				incRoot, err := git.RepoRootPath(resolved)
				if err != nil {
					_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)

					rollbackState()

					return SessionState{}, fmt.Errorf("find included repo root for %q: %w", incPath, err)
				}

				incName := filepath.Base(incRoot)

				incBaseBranch, err := git.DiscoverDefaultBranchOrHEAD(incRoot)
				if err != nil {
					_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)

					rollbackState()

					return SessionState{}, fmt.Errorf("discover default branch for included repo %q: %w", incPath, err)
				}

				incBranch := fmt.Sprintf("%s/%s-%s/%s", branchPrefix, name, id, incName)
				sessionDir := filepath.Dir(worktreePath)
				incWorktreePath := filepath.Join(sessionDir, incName)

				if err := git.SetupSession(gitCtx, incRoot, incWorktreePath, incBranch, incBaseBranch, fetchOnCreate); err != nil {
					_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)

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

	env := make(map[string]string, len(agent.Env)+6)
	for k, v := range agent.Env {
		env[k] = v
	}

	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = name
	env["GRAITH_AGENT_TYPE"] = agentName
	env["GRAITH_WORKTREE_PATH"] = worktreePath

	env["GRAITH_TOKEN"] = token
	if repoRoot != "" {
		env["GRAITH_REPO_PATH"] = repoRoot
	}

	if sm.paths.Profile != "" {
		env["GRAITH_PROFILE"] = sm.paths.Profile
	}

	if inPlace {
		env["GRAITH_IN_PLACE"] = "true"
	}

	for _, extra := range envExtra {
		for k, v := range extra {
			env[k] = v
		}
	}

	var storeDir string

	if repoRoot != "" {
		tmpDir, err := sm.repoTmpDir(repoRoot)
		if err != nil {
			cleanupOnError()
			rollbackState()

			return SessionState{}, err
		}

		env["GRAITH_TMPDIR"] = tmpDir
		if _, ok := env["TMPDIR"]; !ok {
			env["TMPDIR"] = tmpDir
		}

		storeDir, err = sm.repoStoreDir(repoRoot)
		if err != nil {
			cleanupOnError()
			rollbackState()

			return SessionState{}, err
		}
	}

	for _, inc := range includes {
		env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
	}

	if sharedWorktree {
		for _, inc := range sourceIncludes {
			env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
		}
	}

	for _, extra := range envExtra {
		for k, v := range extra {
			env[k] = v
		}
	}

	if agentHooks {
		hookArgs, hookEnv, err := sm.injectHooks(agentName, id, worktreePath, yolo, mcpServers)
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

	if agent.PromptInjectionEnabled() {
		promptArgs, err := sm.injectPrompt(agentName, worktreePath)
		if err != nil {
			sm.log.Warn("failed to inject prompt", "session_id", id, "err", err)
		} else {
			expandedArgs = append(expandedArgs, promptArgs...)
		}
	}

	command := agent.Command
	finalArgs := expandedArgs

	var (
		scratchDir    string
		mergedSandbox *config.SandboxConfig
	)

	if sandboxed {
		merged := sandboxMerged
		merged.ReadDirs = expandPaths(merged.ReadDirs, sm.log, "read")
		merged.WriteDirs = expandPaths(merged.WriteDirs, sm.log, "write")
		mergedSandbox = &merged

		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_AGENT_TYPE", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}

		for k := range env {
			envKeys = append(envKeys, k)
		}

		opts := sm.sandboxOptsFromConfig(merged, id, worktreePath, agent.Command, envKeys, agentHooks)
		if tmpDir := env["GRAITH_TMPDIR"]; tmpDir != "" {
			opts.WriteDirs = append(opts.WriteDirs, tmpDir)
		}

		if storeDir != "" {
			opts.WriteDirs = append(opts.WriteDirs, storeDir)
		}

		opts.WriteDirs = append(opts.WriteDirs, store.SharedStorePath(sm.paths.DataDir))
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

		var wrapErr error

		command, finalArgs, wrapErr = sandbox.Wrap(agent.Command, expandedArgs, opts)
		if wrapErr != nil {
			cleanupOnError()

			if scratchDir != "" {
				_ = os.RemoveAll(scratchDir)
			}

			rollbackState()

			return SessionState{}, fmt.Errorf("sandbox wrap: %w", wrapErr)
		}

		sm.log.Info("sandboxing session", "id", id, "agent", agentName,
			"command", command, "read_dirs", opts.ReadDirs, "write_dirs", opts.WriteDirs,
			"unix_sockets", opts.UnixSockets,
			"features", opts.Features, "workdir", opts.WorktreeDir)
	}

	// Record the pre-spawn time so native session-id capture only matches
	// transcript files this start creates (race-safe against stale rollouts).
	startedAt := time.Now()

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
			_ = os.RemoveAll(scratchDir)
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
			_ = os.RemoveAll(scratchDir)
		}

		_ = os.Remove(logPath)

		return SessionState{}, fmt.Errorf("session was deleted during creation")
	}

	sessState := sm.state.Sessions[id]
	sessState.Sandboxed = sandboxed
	sessState.SandboxConfig = mergedSandbox
	sessState.Includes = includes
	sessState.Status = StatusRunning
	sessState.StatusChangedAt = time.Now()

	sessState.PID = ptySess.Cmd.Process.Pid
	if st, err := grpty.ProcessStartTime(sessState.PID); err == nil {
		sessState.PIDStartTime = st
	}

	sessState.CreationCfg = &CreationConfig{
		Agent:         agent,
		SandboxConfig: cfgSnapshot.Sandbox.Merge(agent.Sandbox),
	}

	sm.sessions[id] = ptySess
	sm.tokenIndex[token] = id

	if sessState.ParentID != "" {
		if parent, ok := sm.state.Sessions[sessState.ParentID]; ok {
			applyLifecycleSummaryLocked(sessState, "Created by "+parent.Name)
		}
	}

	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		delete(sm.sessions, id)
		delete(sm.tokenIndex, token)
		sm.mu.Unlock()

		_ = ptySess.Kill()
		ptySess.Close()
		cleanupOnError()

		if scratchDir != "" {
			_ = os.RemoveAll(scratchDir)
		}

		sm.cleanupHooks(id, agentName, worktreePath)

		_ = os.Remove(logPath)

		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	result := cloneSessionState(sessState)
	sm.mu.Unlock()

	sm.startWatcher(id, ptySess)

	// Best-effort native session-id capture for self-minting agents (Codex):
	// graith didn't force the id, so read it from the agent's on-disk state so
	// later resume is deterministic. Skipped when the id was forced (agentSessionID
	// non-empty). Uses the session's effective state root (e.g. CODEX_HOME).
	if scrapesID(agentName) && agentSessionID == "" {
		go sm.captureNativeSessionID(id, agentName, worktreePath, env["CODEX_HOME"], startedAt, result.PID, result.PIDStartTime)
	}

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

	if IsSystemSession(source) {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("cannot fork system session %q", source.Name)
	}

	// A fork acts on a raw source ID; a soft-deleted source is stopped and would
	// otherwise fork fine, resurrecting hidden state into a live child.
	if source.IsSoftDeleted() {
		sm.mu.Unlock()
		return SessionState{}, errSoftDeleted(source.Name)
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

	token, err := generateToken()
	if err != nil {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("generate session token: %w", err)
	}

	repoRoot := source.RepoPath
	repoName := source.RepoName
	baseBranch := source.BaseBranch
	sourceModel := source.Model
	sourceAgentSessionID := source.AgentSessionID
	sourceYolo := source.Yolo
	// Yolo forces agent hooks on (see Create) so a forked yolo session always
	// installs the approval hook, even if the source had hooks disabled.
	sourceAgentHooks := source.AgentHooks || sourceYolo
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
	if forcesID(agentName) {
		agentSessionID = newAgentSessionID()
	}

	sandboxed, err := sm.resolveSandbox(agentName)
	if err != nil {
		sm.mu.Unlock()
		return SessionState{}, err
	}

	if err := sm.validateApprovalsBackend(sourceYolo); err != nil {
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
		Yolo:            sourceYolo,
		Status:          StatusCreating,
		CreatedAt:       time.Now().UTC(),
		StatusChangedAt: time.Now().UTC(),
		Token:           token,
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
		// See cleanupOnError in Create: remove any nono profile Wrap wrote
		// before the error path so it isn't orphaned when state is rolled back.
		_ = os.Remove(sm.nonoProfilePath(id))
		_ = os.Remove(sm.safehouseFragmentPath(id))

		if len(forkIncludes) > 0 {
			_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, forkIncludes)
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
				_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, forkIncludes)

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
	// Empty-source guard: if fork_args templates the source's native id but the
	// source never captured one (e.g. a pre-feature or capture-timed-out Codex
	// session), expanding would emit a literal empty arg (`codex fork ""`).
	// Start a fresh conversation instead; capture below records the new id.
	if argsNeedForkSourceID(args) && sourceAgentSessionID == "" {
		args = agent.Args
	}

	expandedArgs, err := config.ExpandSlice(args, vars)
	if err != nil {
		forkCleanup()
		rollbackState()

		return SessionState{}, fmt.Errorf("expand fork args: %w", err)
	}

	logPath := filepath.Join(sm.paths.LogDir, id+".log")

	env := make(map[string]string, len(agent.Env)+6)
	for k, v := range agent.Env {
		env[k] = v
	}

	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = name
	env["GRAITH_AGENT_TYPE"] = agentName
	env["GRAITH_WORKTREE_PATH"] = worktreePath

	env["GRAITH_TOKEN"] = token
	if repoRoot != "" {
		env["GRAITH_REPO_PATH"] = repoRoot
	}

	if sm.paths.Profile != "" {
		env["GRAITH_PROFILE"] = sm.paths.Profile
	}

	var forkStoreDir string

	if repoRoot != "" {
		tmpDir, err := sm.repoTmpDir(repoRoot)
		if err != nil {
			forkCleanup()
			rollbackState()

			return SessionState{}, err
		}

		env["GRAITH_TMPDIR"] = tmpDir
		if _, ok := env["TMPDIR"]; !ok {
			env["TMPDIR"] = tmpDir
		}

		forkStoreDir, err = sm.repoStoreDir(repoRoot)
		if err != nil {
			forkCleanup()
			rollbackState()

			return SessionState{}, err
		}
	}

	for _, inc := range forkIncludes {
		env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
	}

	if sourceAgentHooks {
		hookArgs, hookEnv, err := sm.injectHooks(agentName, id, worktreePath, sourceYolo, mcpServers)
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

	if agent.PromptInjectionEnabled() {
		promptArgs, err := sm.injectPrompt(agentName, worktreePath)
		if err != nil {
			sm.log.Warn("failed to inject prompt", "session_id", id, "err", err)
		} else {
			expandedArgs = append(expandedArgs, promptArgs...)
		}
	}

	command := agent.Command
	finalArgs := expandedArgs

	var mergedSandbox *config.SandboxConfig

	if sandboxed {
		merged := sandboxMerged
		merged.ReadDirs = expandPaths(merged.ReadDirs, sm.log, "read")
		merged.WriteDirs = expandPaths(merged.WriteDirs, sm.log, "write")
		mergedSandbox = &merged

		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_AGENT_TYPE", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}

		for k := range env {
			envKeys = append(envKeys, k)
		}

		opts := sm.sandboxOptsFromConfig(merged, id, worktreePath, agent.Command, envKeys, sourceAgentHooks)
		if tmpDir := env["GRAITH_TMPDIR"]; tmpDir != "" {
			opts.WriteDirs = append(opts.WriteDirs, tmpDir)
		}

		if forkStoreDir != "" {
			opts.WriteDirs = append(opts.WriteDirs, forkStoreDir)
		}

		opts.WriteDirs = append(opts.WriteDirs, store.SharedStorePath(sm.paths.DataDir))
		if len(forkIncludes) > 0 {
			opts.WriteDirs = append(opts.WriteDirs, sm.deriveSandboxIncludesWriteDirs(forkIncludes)...)
		}

		var wrapErr error

		command, finalArgs, wrapErr = sandbox.Wrap(agent.Command, expandedArgs, opts)
		if wrapErr != nil {
			forkCleanup()
			rollbackState()

			return SessionState{}, fmt.Errorf("sandbox wrap: %w", wrapErr)
		}

		sm.log.Info("sandboxing forked session", "id", id,
			"command", command, "read_dirs", opts.ReadDirs, "write_dirs", opts.WriteDirs,
			"unix_sockets", opts.UnixSockets,
			"features", opts.Features, "workdir", opts.WorktreeDir)
	}

	// Pre-spawn time for native session-id capture (see Create).
	startedAt := time.Now()

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

		_ = os.Remove(logPath)

		return SessionState{}, fmt.Errorf("session was deleted during creation")
	}

	sessState := sm.state.Sessions[id]
	sessState.Sandboxed = sandboxed
	sessState.SandboxConfig = mergedSandbox
	sessState.Includes = forkIncludes
	sessState.Status = StatusRunning
	sessState.StatusChangedAt = time.Now()

	sessState.PID = ptySess.Cmd.Process.Pid
	if st, err := grpty.ProcessStartTime(sessState.PID); err == nil {
		sessState.PIDStartTime = st
	}

	sessState.CreationCfg = &CreationConfig{
		Agent:         agent,
		SandboxConfig: cfgSnapshot.Sandbox.Merge(agent.Sandbox),
	}

	sm.sessions[id] = ptySess
	sm.tokenIndex[token] = id

	if src, ok := sm.state.Sessions[sessState.ParentID]; ok {
		applyLifecycleSummaryLocked(sessState, "Forked from "+src.Name)
	}

	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		delete(sm.sessions, id)
		delete(sm.tokenIndex, token)
		sm.mu.Unlock()

		_ = ptySess.Kill()
		ptySess.Close()
		forkCleanup()

		_ = os.Remove(logPath)

		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	result := cloneSessionState(sessState)
	sm.mu.Unlock()

	sm.startWatcher(id, ptySess)

	// Capture the forked child's native id for self-minting agents (Codex): the
	// fork mints a new conversation graith didn't choose, so read it from disk
	// for deterministic later resume. Skipped when the id was forced (Claude).
	if scrapesID(agentName) && agentSessionID == "" {
		go sm.captureNativeSessionID(id, agentName, worktreePath, env["CODEX_HOME"], startedAt, result.PID, result.PIDStartTime)
	}

	return result, nil
}

// startWatcher launches watchSession in a tracked goroutine. The watcher is
// registered with sm.watchers so StopAll can wait for its post-exit work
// (state writes, status publish) to finish during shutdown.
func (sm *SessionManager) startWatcher(id string, sess *grpty.Session) {
	sm.watchers.Add(1)
	go func() {
		defer sm.watchers.Done()

		sm.watchSession(id, sess)
	}()
}

// watchSession waits for a PTY session to exit and updates state accordingly.
// If the session has been replaced (e.g. by Resume) or removed (e.g. by Delete),
// the watcher is stale and skips the state update and status event.
func (sm *SessionManager) watchSession(id string, sess *grpty.Session) {
	<-sess.Done()

	sm.mu.Lock()
	stale := sm.sessions[id] != sess

	var (
		name           string
		deleted        bool
		isOrchestrator bool
	)

	if !stale {
		if s, ok := sm.state.Sessions[id]; ok {
			name = s.Name
			isOrchestrator = s.SystemKind == SystemKindOrchestrator

			prevSummary := s.SummaryText
			prevSetAt := s.SummarySetAt

			prevTTL := sm.cfg.Status.TTLDuration()
			if s.SummaryTTL > 0 {
				prevTTL = time.Duration(s.SummaryTTL) * time.Second
			}

			exitCode := sess.ExitCode()
			s.Status = StatusStopped
			s.StatusChangedAt = time.Now()
			s.ExitCode = &exitCode
			s.PID = 0

			s.PIDStartTime = 0
			if s.StopReason == "" {
				s.StopReason = StopReasonCrash
			}

			if lastOut := sess.LastOutputAt(); !lastOut.IsZero() {
				s.LastOutputAt = &lastOut
			}

			if sig := sess.ExitSignal(); sig != 0 && s.StopReason == StopReasonCrash {
				s.ExitSignal = sig.String()
			}

			if s.StopReason != StopReasonShutdown || s.SummaryText == "" {
				text := formatStopSummary(s.StopReason, s.ExitCode, s.ExitSignal, prevSummary, prevSetAt, prevTTL)
				applyLifecycleSummaryLocked(s, text)
			}

			if err := sm.saveState(); err != nil {
				sm.log.Error("failed to save state after session exit", "id", id, "err", err)
			}

			if s.StopReason == StopReasonCrash {
				sm.recordExit()
			}

			delete(sm.sessions, id)
		} else {
			deleted = true

			delete(sm.sessions, id)
		}
	}
	sm.mu.Unlock()

	// Always close the exited PTY's handles. The watcher owns the sess pointer
	// it was passed, regardless of whether the map entry was replaced (stale)
	// or removed (deleted). Double-close is safe: os.File.Close returns
	// ErrClosed (ignored) and readDone is a closed channel (instant receive).
	sess.Close()

	if stale {
		sm.log.Info("ignoring stale session exit", "id", id, "exit_code", sess.ExitCode())
		return
	}

	if deleted {
		sm.log.Info("ignoring exit for deleted session", "id", id, "exit_code", sess.ExitCode())
		return
	}

	logAttrs := []any{
		"id", id,
		"name", name,
		"exit_code", sess.ExitCode(),
	}
	if sig := sess.ExitSignal(); sig != 0 {
		logAttrs = append(logAttrs, "signal", sig.String())
	}

	if rss := sess.PeakRSSBytes(); rss > 0 && sess.ExitCode() != 0 {
		logAttrs = append(logAttrs, "peak_rss_mb", rss/(1024*1024))
	}

	sm.log.Info("session exited", logAttrs...)

	sm.onAgentStatusChange(id, name, "running", "stopped")

	if isOrchestrator {
		sm.notifyOrchestratorExit(id)
	}
}

const (
	massExitWindow    = 2 * time.Second
	massExitThreshold = 5
)

// recordExit tracks session exit times and logs a warning when many sessions
// exit within a short window, which typically indicates an external signal
// (e.g. macOS jetsam/memory pressure killing processes). Caller must hold sm.mu.
func (sm *SessionManager) recordExit() {
	now := time.Now()
	cutoff := now.Add(-massExitWindow)

	// Prune old entries.
	start := 0
	for start < len(sm.recentExits) && sm.recentExits[start].Before(cutoff) {
		start++
	}

	sm.recentExits = append(sm.recentExits[start:], now)

	if len(sm.recentExits) == massExitThreshold {
		sm.log.Warn("mass session exit detected: likely external signal (e.g. OOM killer, jetsam)",
			"count", len(sm.recentExits),
			"window", massExitWindow.String())
	}
}

// argsNeedAgentID reports whether any arg still contains the raw
// {agent_session_id} template token (checked before ExpandSlice runs).
func argsNeedAgentID(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, "{agent_session_id}") {
			return true
		}
	}

	return false
}

// argsNeedForkSourceID reports whether any arg contains the raw
// {fork_source_agent_session_id} template token (checked before ExpandSlice).
func argsNeedForkSourceID(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, "{fork_source_agent_session_id}") {
			return true
		}
	}

	return false
}

// resolveResumeArgs picks the args for resuming a session and applies the
// empty-id guard. resume_args are used unless absent or FreshStart, in which
// case agent.Args (a fresh start) is used. When the chosen args template
// {agent_session_id} but no native id was captured, expanding would emit a
// literal empty arg (e.g. `opencode --session ""`), which misbehaves — so Codex
// falls back to its cwd-scoped `resume --last` and other agents start fresh.
// The check inspects the RAW args before expansion (the token is gone after
// ExpandSlice). Returns the args plus an optional log note when a fallback fired.
func resolveResumeArgs(agent config.Agent, sessAgent, sessAgentSessionID string, freshStart bool) ([]string, string) {
	resumeArgs := agent.ResumeArgs
	if len(resumeArgs) == 0 || freshStart {
		resumeArgs = agent.Args
	}

	if !freshStart && sessAgentSessionID == "" && argsNeedAgentID(resumeArgs) {
		if sessAgent == "codex" {
			return []string{"resume", "--last"}, "no native id captured; codex resuming --last"
		}
		// Fresh start. Guard against agent.Args *also* templating the id (e.g. a
		// future forced agent whose force was gated off) — returning it would
		// re-introduce the empty `--session ""` arg. Drop to no args in that case.
		if argsNeedAgentID(agent.Args) {
			return nil, "no native id captured; starting fresh (dropped id-templated args)"
		}

		return agent.Args, "no native id captured; starting fresh"
	}

	return resumeArgs, ""
}

// Resume restarts a stopped session using the agent's resume_args.
//
// Uses two-phase locking: the GitHub username discovery happens before the lock,
// and the PTY spawn happens after releasing the lock to avoid blocking the daemon.
func (sm *SessionManager) Resume(id string, rows, cols uint16) (SessionState, error) {
	return sm.resumeWithSummary(id, rows, cols, "Resumed")
}

func (sm *SessionManager) resumeWithSummary(id string, rows, cols uint16, lifecycleSummary string) (SessionState, error) {
	return sm.resumeWithSummaryAndPrompt(id, rows, cols, lifecycleSummary, "")
}

// resumeWithSummaryAndPrompt starts (or restarts) a session's agent in its
// existing worktree. When seedPrompt is non-empty it is appended as the agent's
// positional opening prompt — used by Migrate to seed a freshly-swapped agent
// with the rendered prior conversation. A seeded start is treated as a fresh
// start (uses agent.Args, not resume_args) and clears FreshStart afterwards so
// subsequent resumes use the new agent's native resume.
func (sm *SessionManager) resumeWithSummaryAndPrompt(id string, rows, cols uint16, lifecycleSummary, seedPrompt string) (SessionState, error) {
	// --- Pre-lock: discover GitHub username ---
	sm.mu.RLock()
	sessSnap, snapOk := sm.state.Sessions[id]

	var (
		snapRepoPath string
		snapAgent    string
	)

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

	// A soft-deleted session is stopped-and-hidden; resuming it would relaunch a
	// running agent that gr list won't show and the purge loop would later kill.
	// Require an explicit restore first.
	if sessState.IsSoftDeleted() {
		sm.mu.Unlock()
		return SessionState{}, errSoftDeleted(sessState.Name)
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

	if err := validateSessionName(sessState.Name, IsSystemSession(sessState)); err != nil {
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

	// Resume re-enforces the approvals backend too, for parity with the sandbox
	// re-check above: a config change that made the backend unenforceable must
	// not silently resume a non-enforcing approver.
	if err := sm.validateApprovalsBackend(sessState.Yolo); err != nil {
		sm.mu.Unlock()
		return SessionState{}, err
	}

	if sessState.SharedWorktree && !sandboxed {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("shared-worktree session %q requires sandbox but sandbox is not enabled in current config; enable sandbox to resume", id)
	}

	if sessState.SystemKind == SystemKindOrchestrator && !sandboxed {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("orchestrator requires sandbox but sandbox is not available — install safehouse and enable sandbox in config")
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

	// Resolve MCP servers under the lock. Yolo forces hooks on (see Create), so
	// resolve MCP servers for a yolo session even if hooks were disabled.
	var mcpServers []config.MCPServerConfig
	if sessState.AgentHooks || sessState.Yolo {
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
	if sessState.SystemKind == SystemKindOrchestrator {
		sandboxMerged = sm.cfg.OrchestratorSandboxMerged(sessState.Agent)
	}
	// Save previous state for rollback.
	prevStatus := sessState.Status
	prevExitCode := sessState.ExitCode
	prevExitSignal := sessState.ExitSignal
	prevPID := sessState.PID
	prevPIDStartTime := sessState.PIDStartTime
	prevAgentStatus := sessState.AgentStatus
	prevSandboxed := sessState.Sandboxed
	prevSandboxConfig := sessState.SandboxConfig
	prevCreationCfg := sessState.CreationCfg
	prevIdleSince := sessState.IdleSince
	prevSummaryText := sessState.SummaryText
	prevSummarySetAt := sessState.SummarySetAt
	prevSummaryTTL := sessState.SummaryTTL

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
	sessRepoPath := sessState.RepoPath
	sessWorktreePath := sessState.WorktreePath
	sessAgentSessionID := sessState.AgentSessionID
	sessModel := sessState.Model
	sessYolo := sessState.Yolo
	// Yolo forces agent hooks on (see Create) so a resumed yolo session always
	// re-installs the approval hook, even if hooks were disabled at create.
	sessAgentHooks := sessState.AgentHooks || sessYolo
	sessIncludes := make([]IncludedRepoState, len(sessState.Includes))
	copy(sessIncludes, sessState.Includes)
	sessInPlace := sessState.InPlace
	sessSharedWorktree := sessState.SharedWorktree
	sessSystemKind := sessState.SystemKind
	sessFreshStart := sessState.FreshStart
	sessToken := sessState.Token
	sessScenarioID := sessState.ScenarioID
	sessScenarioRole := sessState.ScenarioRole
	sessScenarioGoal := sessState.ScenarioGoal

	sm.mu.Unlock()

	// --- Phase 2: Build command and spawn PTY (no lock) ---
	rollbackState := func() {
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.Status = prevStatus
			s.ExitCode = prevExitCode
			s.ExitSignal = prevExitSignal
			s.PID = prevPID
			s.PIDStartTime = prevPIDStartTime
			s.AgentStatus = prevAgentStatus
			s.Sandboxed = prevSandboxed
			s.SandboxConfig = prevSandboxConfig
			s.CreationCfg = prevCreationCfg
			s.IdleSince = prevIdleSince
			s.SummaryText = prevSummaryText
			s.SummarySetAt = prevSummarySetAt
			s.SummaryTTL = prevSummaryTTL
			_ = sm.saveState()
		}
		sm.mu.Unlock()
	}

	resumeArgs, resumeNote := resolveResumeArgs(agent, sessAgent, sessAgentSessionID, sessFreshStart)
	if resumeNote != "" {
		sm.log.Info(resumeNote, "session_id", id, "agent", sessAgent)
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

	isOrchestrator := sessSystemKind == SystemKindOrchestrator

	ptyCWD := sessWorktreePath
	if isOrchestrator {
		ptyCWD = sm.orchestratorScratchDir()
	}

	env := make(map[string]string, len(agent.Env)+6)
	for k, v := range agent.Env {
		env[k] = v
	}

	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = sessName

	env["GRAITH_AGENT_TYPE"] = sessAgent
	if sessToken != "" {
		env["GRAITH_TOKEN"] = sessToken
	}

	if !isOrchestrator {
		env["GRAITH_WORKTREE_PATH"] = sessWorktreePath
	}

	if sessRepoPath != "" {
		env["GRAITH_REPO_PATH"] = sessRepoPath
	}

	if sm.paths.Profile != "" {
		env["GRAITH_PROFILE"] = sm.paths.Profile
	}

	if sessInPlace {
		env["GRAITH_IN_PLACE"] = "true"
	}

	if sessScenarioID != "" {
		env["GRAITH_SCENARIO"] = sessScenarioID

		sm.mu.RLock()

		if sc, ok := sm.state.Scenarios[sessScenarioID]; ok {
			env["GRAITH_SCENARIO_NAME"] = sc.Name
		}

		sm.mu.RUnlock()
	}

	if sessScenarioRole != "" {
		env["GRAITH_SCENARIO_ROLE"] = sessScenarioRole
	}

	if sessScenarioGoal != "" {
		env["GRAITH_SCENARIO_GOAL"] = sessScenarioGoal
	}

	var resumeStoreDir string

	if isOrchestrator {
		if err := os.MkdirAll(ptyCWD, 0o700); err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("create orchestrator scratch dir: %w", err)
		}

		tmpDir := sm.orchestratorTmpDir()
		if err := os.MkdirAll(tmpDir, 0o700); err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("create orchestrator tmp dir: %w", err)
		}

		env["GRAITH_TMPDIR"] = tmpDir
		env["TMPDIR"] = tmpDir
	} else if sessRepoPath != "" {
		tmpDir, err := sm.repoTmpDir(sessRepoPath)
		if err != nil {
			rollbackState()
			return SessionState{}, err
		}

		env["GRAITH_TMPDIR"] = tmpDir
		if _, ok := env["TMPDIR"]; !ok {
			env["TMPDIR"] = tmpDir
		}

		resumeStoreDir, err = sm.repoStoreDir(sessRepoPath)
		if err != nil {
			rollbackState()
			return SessionState{}, err
		}
	}

	for _, inc := range sessIncludes {
		if !git.IsInsideGitRepo(inc.WorktreePath) {
			rollbackState()
			return SessionState{}, fmt.Errorf("included worktree %q (%s) is no longer a valid git repo — delete and recreate the session", inc.WorktreePath, inc.RepoName)
		}

		env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
	}

	if sessAgentHooks {
		hookArgs, hookEnv, err := sm.injectHooks(sessAgent, id, sessWorktreePath, sessYolo, mcpServers)
		if err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("inject agent hooks: %w", err)
		}

		expandedArgs = append(expandedArgs, hookArgs...)

		for k, v := range hookEnv {
			env[k] = v
		}
	}

	if isOrchestrator {
		sm.mu.RLock()
		orchCfg := sm.cfg.Orchestrator
		repoPaths := sm.cfg.AvailableRepoPaths()
		sm.mu.RUnlock()
		promptArgs := sm.buildOrchestratorPrompt(sessAgent, orchCfg, repoPaths)
		expandedArgs = append(expandedArgs, promptArgs...)
	} else if agent.PromptInjectionEnabled() {
		promptArgs, err := sm.injectPrompt(sessAgent, sessWorktreePath)
		if err != nil {
			sm.log.Warn("failed to inject prompt", "session_id", id, "err", err)
		} else {
			expandedArgs = append(expandedArgs, promptArgs...)
		}
	}

	// Migration seed prompt: appended last so it reaches the agent as its
	// opening positional prompt (after any orchestrator/injected prompt).
	if seedPrompt != "" {
		expandedArgs = append(expandedArgs, seedPrompt)
	}

	command := agent.Command
	finalArgs := expandedArgs

	var mergedSandbox *config.SandboxConfig

	if sandboxed {
		merged := sandboxMerged
		merged.ReadDirs = expandPaths(merged.ReadDirs, sm.log, "read")
		merged.WriteDirs = expandPaths(merged.WriteDirs, sm.log, "write")
		mergedSandbox = &merged

		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_AGENT_TYPE", "TERM"}
		if !isOrchestrator {
			envKeys = append(envKeys, "GRAITH_WORKTREE_PATH")
		}

		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}

		for k := range env {
			envKeys = append(envKeys, k)
		}

		opts := sm.sandboxOptsFromConfig(merged, id, ptyCWD, agent.Command, envKeys, sessAgentHooks)
		if tmpDir := env["GRAITH_TMPDIR"]; tmpDir != "" {
			opts.WriteDirs = append(opts.WriteDirs, tmpDir)
		}

		if resumeStoreDir != "" {
			opts.WriteDirs = append(opts.WriteDirs, resumeStoreDir)
		}

		opts.WriteDirs = append(opts.WriteDirs, store.SharedStorePath(sm.paths.DataDir))
		if isOrchestrator {
			opts.WriteDirs = append(opts.WriteDirs, sm.orchestratorScratchDir())
		}

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

		var wrapErr error

		command, finalArgs, wrapErr = sandbox.Wrap(agent.Command, expandedArgs, opts)
		if wrapErr != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("sandbox wrap: %w", wrapErr)
		}

		sm.log.Info("sandboxing resumed session", "id", id,
			"command", command, "read_dirs", opts.ReadDirs, "write_dirs", opts.WriteDirs,
			"unix_sockets", opts.UnixSockets,
			"features", opts.Features, "workdir", opts.WorktreeDir)
	}

	// Pre-spawn time for native session-id capture (see Create).
	startedAt := time.Now()

	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    command,
		Args:       finalArgs,
		Dir:        ptyCWD,
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
	sessState.ExitSignal = ""

	sessState.PID = ptySess.Cmd.Process.Pid
	if st, err := grpty.ProcessStartTime(sessState.PID); err == nil {
		sessState.PIDStartTime = st
	}

	sessState.AgentStatus = ""
	sessState.IdleSince = nil
	sessState.StopReason = ""
	sessState.Sandboxed = sandboxed
	sessState.SandboxConfig = mergedSandbox

	sessState.CreationCfg = &CreationConfig{
		Agent:         agent,
		SandboxConfig: sandboxMerged,
	}
	if isOrchestrator {
		sessState.LastStartedAt = time.Now()
		sessState.FreshStart = false
	}
	// A seeded (migration) start used agent.Args; clear FreshStart so future
	// resumes use the new agent's native resume_args.
	if seedPrompt != "" {
		sessState.FreshStart = false
	}

	sm.sessions[id] = ptySess

	applyLifecycleSummaryLocked(sessState, lifecycleSummary)

	if err := sm.saveState(); err != nil {
		sessState.Status = prevStatus
		sessState.StatusChangedAt = prevStatusChangedAt
		sessState.ExitCode = prevExitCode
		sessState.ExitSignal = prevExitSignal
		sessState.PID = prevPID
		sessState.PIDStartTime = prevPIDStartTime
		sessState.AgentStatus = prevAgentStatus
		sessState.Sandboxed = prevSandboxed
		sessState.SandboxConfig = prevSandboxConfig
		sessState.CreationCfg = prevCreationCfg
		sessState.IdleSince = prevIdleSince
		sessState.SummaryText = prevSummaryText
		sessState.SummarySetAt = prevSummarySetAt
		sessState.SummaryTTL = prevSummaryTTL

		delete(sm.sessions, id)
		sm.mu.Unlock()

		_ = ptySess.Kill()
		ptySess.Close()

		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	delete(sm.hookReports, id)

	scenarioIDForRepublish := sessState.ScenarioID
	result := cloneSessionState(sessState)
	sm.mu.Unlock()

	sm.startWatcher(id, ptySess)
	go sm.notifyUnreadInbox(id)

	// If a self-minting agent (Codex) had no captured id, this resume fell back
	// to its empty-id behaviour; scrape the id now so the *next* resume is
	// deterministic. Skipped when an id is already known.
	if scrapesID(sessAgent) && sessAgentSessionID == "" {
		go sm.captureNativeSessionID(id, sessAgent, sessWorktreePath, env["CODEX_HOME"], startedAt, result.PID, result.PIDStartTime)
	}

	if scenarioIDForRepublish != "" {
		sm.republishManifests(scenarioIDForRepublish)
	}

	return result, nil
}

// shouldPurge reports whether a soft-deleted session's recovery window has
// elapsed and it is due for a hard delete. It is the single predicate shared by
// the purge loop and Restore's after-expiry check, so the two can never
// disagree about whether a session is still recoverable. `now` is injected so
// callers (and tests) control the clock.
//
// The deadline is normally the session's frozen ExpiresAt (set at delete time),
// NOT a recomputation from current retention — a config change must not
// retroactively shift the "Recoverable until <time>" the user was promised.
// When ExpiresAt is nil on a soft-deleted session (corrupt/hand-edited state, or
// an interrupted pre-ExpiresAt delete), fallbackExpiry is used so such a session
// is neither hidden-forever nor purged without a deadline (trash leak). Callers
// compute fallbackExpiry = DeletedAt + current retention (or now) and log the
// fallback.
func shouldPurge(s *SessionState, now, fallbackExpiry time.Time) bool {
	if s.DeletedAt == nil {
		return false
	}

	expiry := fallbackExpiry
	if s.ExpiresAt != nil {
		expiry = *s.ExpiresAt
	}

	return !now.Before(expiry)
}

// fallbackExpiryLocked computes the fallback purge deadline for a soft-deleted
// session whose ExpiresAt is missing, and reports whether the fallback applies
// (so callers can log it). Must be called with sm.mu held.
func (sm *SessionManager) fallbackExpiryLocked(s *SessionState, now time.Time) (time.Time, bool) {
	if s.ExpiresAt != nil {
		return *s.ExpiresAt, false
	}

	if s.DeletedAt != nil {
		return s.DeletedAt.Add(sm.cfg.Delete.RetentionDuration()), true
	}

	return now, true
}

// SoftDelete marks a session as deleted without removing its worktree or state.
// The agent process is stopped and the session moves to the stopped state, but
// everything is preserved so `gr restore` can recover it within the configured
// retention window. The daemon's purge loop hard-deletes it once the window
// elapses. System and starred sessions are protected, matching Delete. Returns
// a snapshot of the soft-deleted session so the caller can report the expiry.
func (sm *SessionManager) SoftDelete(id string) (SessionState, error) {
	sm.mu.Lock()

	sessState, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q not found", id)
	}

	if IsSystemSession(sessState) && sm.systemSessionEnabledInConfig(sessState) {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is a system session managed by config.toml — disable it there and reload before deleting", sessState.Name)
	}

	if sessState.Starred {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is starred; unstar it first to delete", id)
	}

	if sessState.IsSoftDeleted() {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is already deleted; use `gr restore` to recover it or `gr purge` to remove it now", sessState.Name)
	}

	// Unlike Delete — which special-cases a mid-creation session by removing the
	// placeholder so the in-flight create's Phase 3 cleans up — soft-deleting a
	// half-created session is not meaningful, so we reject it outright.
	if sessState.Status == StatusCreating {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is still being created; wait for it to finish before deleting", sessState.Name)
	}

	if sessState.Status == StatusDeleting {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is already being deleted", id)
	}

	orphanPID := sessState.PID
	orphanStartTime := sessState.PIDStartTime

	// Persist the marker BEFORE the blocking kill (crash-safety): if the daemon
	// died mid-kill with DeletedAt unwritten, Reconcile would find a dead PID and
	// mark the session a live stopped session, silently undoing the delete.
	// ExpiresAt is frozen here (DeletedAt + retention) so a later config change
	// never shifts the promised deadline. The PID is intentionally left recorded
	// through this save: if we crash before the kill below completes, the startup
	// sweep uses the recorded PID to re-kill the orphaned agent (Reconcile skips
	// stopped sessions). It is zeroed only after the kill succeeds.
	//
	// The save is done BEFORE removing the PTY/client from the runtime maps and
	// before killing, and its error is load-bearing: if it fails, the marker is
	// not durable, so we roll back the in-memory fields and abort rather than
	// kill the agent and report a delete that a crash could silently undo.
	prevStatus := sessState.Status
	now := time.Now()
	retention := sm.cfg.Delete.RetentionDuration()
	expiresAt := now.Add(retention)
	sessState.DeletedAt = &now
	sessState.ExpiresAt = &expiresAt
	sessState.Status = StatusStopped
	sessState.StatusChangedAt = now
	applyLifecycleSummaryLocked(sessState, softDeleteSummary(expiresAt))

	if err := sm.saveState(); err != nil {
		// Roll back: the session stays live and fully consistent (nothing has
		// been removed from the runtime maps or killed yet).
		sessState.DeletedAt = nil
		sessState.ExpiresAt = nil
		sessState.Status = prevStatus
		sm.mu.Unlock()

		return SessionState{}, fmt.Errorf("soft delete aborted: could not persist marker: %w", err)
	}

	// Marker is durable — now detach the client and remove the PTY from
	// sm.sessions BEFORE killing it: watchSession treats a session as stale when
	// it is no longer in the map, so the exit watcher won't race in and clobber
	// DeletedAt/Status when the agent exits. Mirrors Delete.
	ac, hasClient := sm.attachedClients[id]
	if hasClient {
		delete(sm.attachedClients, id)
	}

	ptySess, hasPTY := sm.sessions[id]
	if hasPTY {
		delete(sm.sessions, id)
	}

	sm.mu.Unlock()

	// Stop the agent outside the lock using Delete's kill path (detach → kill →
	// grace → force-kill → Close), NOT Stop's single SIGTERM. The marker is
	// already durable, so a best-effort kill is fine.
	killedOK := true

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
	} else if orphanPID > 0 {
		if _, err := sm.killVerifiedProcess(orphanPID, orphanStartTime); err != nil {
			sm.log.Warn("failed to kill process during soft delete", "id", id, "pid", orphanPID, "err", err)
			// Keep the PID recorded so the startup orphan sweep can retry the
			// kill; clearing it would strand a live, hidden agent unmanaged.
			killedOK = false
		}
	}

	// Snapshot the result. Clear the recorded PID only if the kill succeeded —
	// otherwise leave it for reconcileSoftDeletedOrphans to re-kill on restart.
	sm.mu.Lock()

	snapshot := SessionState{ID: id}
	if s, ok := sm.state.Sessions[id]; ok {
		if killedOK {
			s.PID = 0
			s.PIDStartTime = 0
		}

		_ = sm.saveState()
		snapshot = cloneSessionState(s)
	}

	sm.mu.Unlock()

	if hasClient {
		ac.kick()
	}

	return snapshot, nil
}

// softDeleteSummary builds the lifecycle summary shown in the overlay/logs for a
// soft-deleted session, including the frozen recovery deadline.
func softDeleteSummary(expiresAt time.Time) string {
	return fmt.Sprintf("Soft-deleted, recoverable until %s", expiresAt.Format("2006-01-02 15:04"))
}

// softDeletableLocked reports whether a session is a candidate for soft delete
// in a bulk/sweep context. Must be called with sm.mu held (read or write).
func softDeletableLocked(sess *SessionState) bool {
	return sess != nil && !sess.IsSoftDeleted() && !sess.Starred && !IsSystemSession(sess) &&
		sess.Status != StatusCreating && sess.Status != StatusDeleting
}

// SoftDeleteWithChildren soft-deletes a session and all of its transitive
// descendants. If excludeRoot is true, the root session itself is left alone.
// Sessions that are already soft-deleted, starred, system, or mid-creation are
// skipped. A lightweight sweep re-marks descendants that appear mid-operation
// (a child agent spawning a new session) so the subtree stays coherent — it
// only re-marks, never tears down, since deferring teardown is the whole point.
// Returns the list of session IDs that were soft-deleted.
func (sm *SessionManager) SoftDeleteWithChildren(rootID string, excludeRoot bool) ([]string, error) {
	sm.mu.RLock()
	_, ok := sm.state.Sessions[rootID]
	sm.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("session %q not found", rootID)
	}

	deletedSet := make(map[string]bool)

	var deleted []string

	softDeleteOne := func(id string) {
		if deletedSet[id] {
			return
		}

		sm.mu.RLock()
		ok := softDeletableLocked(sm.state.Sessions[id])
		sm.mu.RUnlock()

		if !ok {
			// Mark it seen so descendants of a skipped (e.g. starred) session are
			// still reachable in the sweep below.
			deletedSet[id] = true
			return
		}

		if _, err := sm.SoftDelete(id); err != nil {
			sm.log.Warn("soft delete of descendant failed", "id", id, "err", err)
			return
		}

		deletedSet[id] = true
		deleted = append(deleted, id)
	}

	sm.mu.RLock()
	initial := sm.collectDescendants(rootID)
	sm.mu.RUnlock()

	if excludeRoot {
		initial = filterExcludeRoot(initial, rootID)
	}

	for _, id := range initial {
		softDeleteOne(id)
	}

	// Sweep for descendants created between collectDescendants and now, up to a
	// bounded number of rounds. Cheap: each round only re-marks.
	const maxSweepRounds = 10
	for sweep := 0; sweep < maxSweepRounds; sweep++ {
		sm.mu.RLock()

		var late []string

		for sid, sess := range sm.state.Sessions {
			if deletedSet[sid] || sess.ParentID == "" || !deletedSet[sess.ParentID] {
				continue
			}

			late = append(late, sid)
		}

		sm.mu.RUnlock()

		if len(late) == 0 {
			break
		}

		for _, id := range late {
			softDeleteOne(id)
		}
	}

	return deleted, nil
}

// Restore un-deletes a soft-deleted session, clearing its deletion marker and
// leaving it in the stopped state so it can be resumed. Returns an error if the
// session does not exist, is not soft-deleted, or its recovery window has
// already elapsed (in which case it is scheduled for purge and must not be
// resurrected past its advertised deadline).
func (sm *SessionManager) Restore(id string) (SessionState, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	return sm.restoreLocked(id)
}

// restoreLocked performs the restore under an already-held write lock.
func (sm *SessionManager) restoreLocked(id string) (SessionState, error) {
	sessState, ok := sm.state.Sessions[id]
	if !ok {
		return SessionState{}, fmt.Errorf("session %q not found", id)
	}

	if !sessState.IsSoftDeleted() {
		return SessionState{}, fmt.Errorf("session %q is not deleted", sessState.Name)
	}

	// Use the same predicate purge uses: never resurrect a session past its
	// advertised deadline, even if the coarse purge cadence hasn't reaped it yet.
	now := time.Now()

	fallback, fellBack := sm.fallbackExpiryLocked(sessState, now)
	if fellBack {
		sm.log.Warn("soft-deleted session missing ExpiresAt; using fallback deadline for restore check", "id", id)
	}

	if shouldPurge(sessState, now, fallback) {
		return SessionState{}, fmt.Errorf("session %q has expired its recovery window and is scheduled for purge", sessState.Name)
	}

	sessState.DeletedAt = nil
	sessState.ExpiresAt = nil
	sessState.Status = StatusStopped
	sessState.StatusChangedAt = time.Now()
	applyLifecycleSummaryLocked(sessState, "Restored — resume to continue")

	if err := sm.saveState(); err != nil {
		return SessionState{}, err
	}

	return cloneSessionState(sessState), nil
}

// RestoreWithChildren restores a soft-deleted session and every soft-deleted
// descendant, bringing a subtree hidden by a `--children` delete back at once.
// Non-deleted or expired descendants are skipped. Returns the restored IDs.
func (sm *SessionManager) RestoreWithChildren(rootID string) ([]SessionState, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, ok := sm.state.Sessions[rootID]; !ok {
		return nil, fmt.Errorf("session %q not found", rootID)
	}

	ids := sm.collectDescendants(rootID)

	var restored []SessionState

	for _, id := range ids {
		sess, ok := sm.state.Sessions[id]
		if !ok || !sess.IsSoftDeleted() {
			continue
		}

		s, err := sm.restoreLocked(id)
		if err != nil {
			sm.log.Warn("restore of descendant failed", "id", id, "err", err)
			continue
		}

		restored = append(restored, s)
	}

	if len(restored) == 0 {
		return nil, fmt.Errorf("session %q is not deleted", rootID)
	}

	return restored, nil
}

// softDeletedDescendantCount returns how many transitive descendants of id are
// currently soft-deleted (excluding id itself). Used to warn on a bare restore
// that leaves hidden children behind.
func (sm *SessionManager) softDeletedDescendantCount(id string) int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	n := 0

	for _, did := range sm.collectDescendants(id) {
		if did == id {
			continue
		}

		if sess, ok := sm.state.Sessions[did]; ok && sess.IsSoftDeleted() {
			n++
		}
	}

	return n
}

// sessionName returns a session's name, or "" if it no longer exists. Used to
// capture a name before a hard delete removes the session from state.
func (sm *SessionManager) sessionName(id string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if s, ok := sm.state.Sessions[id]; ok {
		return s.Name
	}

	return ""
}

// sessionSnapshot returns a clone of a session's state, or a zero value with the
// ID set if it no longer exists.
func (sm *SessionManager) sessionSnapshot(id string) SessionState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if s, ok := sm.state.Sessions[id]; ok {
		return cloneSessionState(s)
	}

	return SessionState{ID: id}
}

// purgeExpired hard-deletes every soft-deleted session whose frozen ExpiresAt
// has passed. It snapshots the expired sessions under a read lock, then
// hard-deletes each via a compare-and-delete: it re-checks under a read lock
// that the session is still soft-deleted with the *same* ExpiresAt and still
// expired before calling Delete.
//
// Why this is race-free against a concurrent gr restore, even though the
// re-check and Delete are not one atomic critical section: purge only ever
// targets *expired* sessions, and Restore refuses to un-delete an expired
// session using the *same* shouldPurge predicate (see restoreLocked). So a
// session that qualifies for purge cannot be flipped back to live in the window
// between the re-check and Delete — the only operation that could clear the
// marker (Restore) is itself gated on the session NOT being expired. The
// compare-and-delete then additionally guards the delete/restore/re-delete case
// (a new ExpiresAt won't Equal the snapshot). This invariant is load-bearing:
// any future change that lets Restore succeed on an expired session must also
// make this delete atomic.
func (sm *SessionManager) purgeExpired(now time.Time) {
	sm.mu.RLock()

	type candidate struct {
		id        string
		expiresAt time.Time
	}

	var expired []candidate

	for id, s := range sm.state.Sessions {
		if s.DeletedAt == nil {
			continue
		}

		expiry, fellBack := sm.fallbackExpiryLocked(s, now)
		if fellBack {
			sm.log.Warn("soft-deleted session missing ExpiresAt; using fallback deadline for purge", "id", id)
		}

		if shouldPurge(s, now, expiry) {
			expired = append(expired, candidate{id: id, expiresAt: expiry})
		}
	}

	sm.mu.RUnlock()

	for _, c := range expired {
		// Compare-and-delete: verify the session is still soft-deleted and its
		// deadline is unchanged before purging, so a concurrent restore (or
		// delete/restore/re-delete, which mints a new ExpiresAt) is not clobbered.
		sm.mu.RLock()
		s, ok := sm.state.Sessions[c.id]

		var stillExpired bool

		if ok && s.DeletedAt != nil {
			expiry, _ := sm.fallbackExpiryLocked(s, now)
			stillExpired = expiry.Equal(c.expiresAt) && shouldPurge(s, now, expiry)
		}

		sm.mu.RUnlock()

		if !stillExpired {
			continue
		}

		sm.log.Info("purging expired soft-deleted session", "id", c.id)

		if err := sm.Delete(c.id); err != nil {
			sm.log.Warn("purge of expired session failed, will retry", "id", c.id, "err", err)
		}
	}
}

// reconcileSoftDeletedOrphans kills any agent process still alive on a
// soft-deleted session and clears its recorded PID. It closes the crash window
// in SoftDelete between persisting the marker (with the PID still recorded) and
// completing the kill: Reconcile only re-checks liveness for running sessions,
// so a soft-deleted (stopped) session with a live PID would otherwise leave an
// orphaned, invisible agent. Run once at startup, before the first purge sweep.
func (sm *SessionManager) reconcileSoftDeletedOrphans() {
	sm.mu.RLock()

	type orphan struct {
		id        string
		pid       int
		startTime int64
	}

	var orphans []orphan

	for id, s := range sm.state.Sessions {
		if s.IsSoftDeleted() && s.PID > 0 {
			orphans = append(orphans, orphan{id: id, pid: s.PID, startTime: s.PIDStartTime})
		}
	}

	sm.mu.RUnlock()

	for _, o := range orphans {
		sm.log.Info("re-killing orphaned process on soft-deleted session", "id", o.id, "pid", o.pid)

		if _, err := sm.killVerifiedProcess(o.pid, o.startTime); err != nil {
			// Leave the PID recorded so a later run can retry; clearing it would
			// strand a live orphan with no handle to kill it.
			sm.log.Warn("failed to re-kill orphan on soft-deleted session", "id", o.id, "pid", o.pid, "err", err)
			continue
		}

		sm.mu.Lock()
		// Generation check: only clear if the session is still soft-deleted with
		// the same PID we killed. A concurrent restore+resume could have replaced
		// the process; we must not zero the new generation's PID.
		if s, ok := sm.state.Sessions[o.id]; ok && s.IsSoftDeleted() && s.PID == o.pid {
			s.PID = 0
			s.PIDStartTime = 0
			_ = sm.saveState()
		}
		sm.mu.Unlock()
	}
}

// purgeStartupDelay is how long after startup the first purge sweep runs,
// catching windows that expired while the daemon was down without racing the
// rest of Run's initialization.
const purgeStartupDelay = 30 * time.Second

// purgeInterval is how often the daemon sweeps for expired soft-deleted
// sessions after the first sweep. It is intentionally coarse: the retention
// window is measured in hours, so purging a little late is harmless.
const purgeInterval = 10 * time.Minute

// RunPurgeLoop periodically hard-deletes soft-deleted sessions whose retention
// window has elapsed. Modeled on RunGitPullLoop: one sweep shortly after startup
// (to catch windows that elapsed while the daemon was down), then a coarse
// ticker. Stops cleanly on context cancel.
func (sm *SessionManager) RunPurgeLoop(ctx context.Context) {
	// Close the SoftDelete crash window first: re-kill any agent left alive on a
	// soft-deleted session before the state is otherwise trusted.
	sm.reconcileSoftDeletedOrphans()

	timer := time.NewTimer(purgeStartupDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			sm.purgeExpired(time.Now())
			timer.Reset(purgeInterval)
		}
	}
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

	if IsSystemSession(sessState) && sm.systemSessionEnabledInConfig(sessState) {
		sm.mu.Unlock()
		return fmt.Errorf("session %q is a system session managed by config.toml — disable it there and reload before deleting", sessState.Name)
	}

	if sessState.Starred {
		sm.mu.Unlock()
		return fmt.Errorf("session %q is starred; unstar it first to delete", id)
	}

	if sessState.Status == StatusDeleting {
		sm.mu.Unlock()
		return fmt.Errorf("session %q is already being deleted", id)
	}

	// Remove any staged migration context (full prior conversation) on delete.
	defer sm.removeMigrationContext(sessState)

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
	sessSystemKind := sessState.SystemKind
	prevStatus := sessState.Status
	sessToken := sessState.Token
	parentID := sessState.ParentID
	sessionIncludes := make([]IncludedRepoState, len(sessState.Includes))
	copy(sessionIncludes, sessState.Includes)

	if sessState.Status == StatusCreating {
		// Session is mid-creation (Phase 2). Remove from state so Phase 3 detects
		// the deletion and handles cleanup (worktree, PTY).
		sm.reparentChildrenLocked(id, parentID)
		delete(sm.state.Sessions, id)
		delete(sm.hookReports, id)

		for _, s := range sm.state.Sessions {
			if s.ParentID == id {
				s.ParentID = ""
			}
		}

		_ = sm.saveState()
		sm.mu.Unlock()

		if hasClient {
			ac.kick()
		}

		return nil
	}

	orphanPID := sessState.PID
	orphanStartTime := sessState.PIDStartTime
	sessState.Status = StatusDeleting
	sessState.StatusChangedAt = time.Now()
	sessState.PID = 0
	sessState.PIDStartTime = 0
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
	} else if orphanPID > 0 {
		if _, err := sm.killVerifiedProcess(orphanPID, orphanStartTime); err != nil {
			sm.log.Warn("failed to kill orphaned process during delete", "id", id, "pid", orphanPID, "err", err)
			sm.mu.Lock()
			if s, ok := sm.state.Sessions[id]; ok {
				s.Status = StatusErrored
				s.StatusChangedAt = time.Now()
				s.PID = orphanPID
				s.PIDStartTime = orphanStartTime
				applyLifecycleSummaryLocked(s, fmt.Sprintf("Delete aborted: orphaned process (PID %d) could not be killed: %v", orphanPID, err))

				_ = sm.saveState()
			}
			sm.mu.Unlock()

			if hasClient {
				ac.kick()
			}

			return fmt.Errorf("delete aborted: orphaned process (PID %d) could not be killed: %w", orphanPID, err)
		}
	}

	// Attempt git teardown before removing the session from state.
	var teardownErr error

	switch {
	case sessSystemKind == SystemKindOrchestrator:
		// The orchestrator has no worktree/branch; its scratch + tmp live under
		// DataDir/orchestrator (see orchestratorScratchDir/orchestratorTmpDir),
		// which the per-session scratch cleanup below doesn't cover. Remove the
		// whole tree so a disabled-then-deleted orchestrator leaves nothing behind.
		teardownErr = os.RemoveAll(filepath.Join(sm.paths.DataDir, "orchestrator"))
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
	// Re-read parentID under the lock: a concurrent Delete of our parent may
	// have updated sessState.ParentID while we were doing teardown without the
	// lock held. Using the stale captured value would reparent children to a
	// session that no longer exists.
	if s, ok := sm.state.Sessions[id]; ok {
		parentID = s.ParentID
	}

	sm.reparentChildrenLocked(id, parentID)
	delete(sm.state.Sessions, id)
	delete(sm.hookReports, id)

	if sessToken != "" {
		delete(sm.tokenIndex, sessToken)
	}

	for _, s := range sm.state.Sessions {
		if s.ParentID == id {
			s.ParentID = ""
		}
	}

	err := sm.saveState()
	sm.mu.Unlock()

	_ = os.Remove(filepath.Join(sm.paths.LogDir, id+".log"))
	_ = os.Remove(sm.nonoProfilePath(id))
	_ = os.Remove(sm.safehouseFragmentPath(id))
	sm.cleanupHooks(id, agentName, worktreePath)

	if hasClient {
		ac.kick()
	}

	return err
}

// reparentChildrenLocked reassigns all direct children of the deleted session
// to its parent. Must be called with sm.mu held.
func (sm *SessionManager) reparentChildrenLocked(deletedID, newParentID string) {
	for _, s := range sm.state.Sessions {
		if s.ParentID == deletedID {
			s.ParentID = newParentID
		}
	}
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
		pid          int
		pidStartTime int64
	}

	snaps := make([]snapshot, 0, len(toDelete))

	var creatingIDs []string

	for _, did := range toDelete {
		sess := sm.state.Sessions[did]
		if IsSystemSession(sess) {
			sm.log.Info("skipping system session in bulk delete", "session_id", did, "name", sess.Name)
			continue
		}

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

		s.pid = sess.PID
		s.pidStartTime = sess.PIDStartTime
		snaps = append(snaps, s)
		sess.Status = StatusDeleting
		sess.StatusChangedAt = time.Now()
		sess.PID = 0
		sess.PIDStartTime = 0
	}

	_ = sm.saveState()
	sm.mu.Unlock()

	// Kill all PTY processes outside the lock.
	killFailed := make(map[string]bool)

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
		} else if s.pid > 0 {
			if _, err := sm.killVerifiedProcess(s.pid, s.pidStartTime); err != nil {
				sm.log.Warn("failed to kill orphaned process during delete", "id", s.id, "pid", s.pid, "err", err)
				sm.mu.Lock()
				if sess, ok := sm.state.Sessions[s.id]; ok {
					sess.Status = StatusErrored
					sess.StatusChangedAt = time.Now()
					sess.PID = s.pid
					sess.PIDStartTime = s.pidStartTime
					applyLifecycleSummaryLocked(sess, fmt.Sprintf("Delete aborted: orphaned process (PID %d) could not be killed: %v", s.pid, err))
				}

				_ = sm.saveState()
				sm.mu.Unlock()

				killFailed[s.id] = true
			}
		}
	}

	// Sweep for sessions created between collectDescendants and PTY kills.
	// Child agents may have spawned new sessions during that window.
	deletedSet := make(map[string]bool, len(snaps)+len(creatingIDs))
	for _, s := range snaps {
		deletedSet[s.id] = true
	}

	for _, cid := range creatingIDs {
		deletedSet[cid] = true
	}

	const maxSweepRounds = 10
	for sweep := 0; sweep < maxSweepRounds; sweep++ {
		sm.mu.Lock()

		var lateSnaps []snapshot

		progress := false

		for sid, sess := range sm.state.Sessions {
			if deletedSet[sid] || sess.ParentID == "" || !deletedSet[sess.ParentID] {
				continue
			}
			// Add to traversal set before skip checks so descendants of
			// starred/system sessions are still reachable in later rounds.
			deletedSet[sid] = true

			if IsSystemSession(sess) || sess.Starred || sess.Status == StatusDeleting {
				continue
			}

			progress = true

			if sess.Status == StatusCreating {
				delete(sm.state.Sessions, sid)
				delete(sm.hookReports, sid)

				if ac, ok := sm.attachedClients[sid]; ok {
					delete(sm.attachedClients, sid)

					_ = ac
				}

				creatingIDs = append(creatingIDs, sid)

				continue
			}

			ls := snapshot{
				id:           sid,
				agent:        sess.Agent,
				repoPath:     sess.RepoPath,
				worktreePath: sess.WorktreePath,
				branch:       sess.Branch,
				shared:       sess.SharedWorktree,
				inPlace:      sess.InPlace,
				prevStatus:   sess.Status,
				includes:     make([]IncludedRepoState, len(sess.Includes)),
			}
			copy(ls.includes, sess.Includes)

			if pty, ok := sm.sessions[sid]; ok {
				ls.ptySess = pty

				delete(sm.sessions, sid)
			}

			if ac, ok := sm.attachedClients[sid]; ok {
				ls.client = ac

				delete(sm.attachedClients, sid)
			}

			lateSnaps = append(lateSnaps, ls)
			sess.Status = StatusDeleting
			sess.StatusChangedAt = time.Now()
			sess.PID = 0
		}

		if !progress {
			sm.mu.Unlock()
			break
		}

		sm.log.Info("sweep found late-arriving descendants", "count", len(lateSnaps), "round", sweep+1)
		_ = sm.saveState()
		sm.mu.Unlock()

		for _, s := range lateSnaps {
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

		snaps = append(snaps, lateSnaps...)

		if sweep == maxSweepRounds-1 {
			sm.log.Warn("sweep reached round cap, some descendants may remain", "cap", maxSweepRounds)
		}
	}

	// Attempt teardowns, tracking which succeed.
	var teardownErrs []error

	succeeded := make(map[string]bool, len(snaps))
	for _, s := range snaps {
		if killFailed[s.id] {
			continue
		}

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

	removedSet := make(map[string]bool, len(creatingIDs))
	for _, cid := range creatingIDs {
		removedSet[cid] = true
	}

	for _, s := range snaps {
		if succeeded[s.id] {
			if sess, ok := sm.state.Sessions[s.id]; ok && sess.Token != "" {
				delete(sm.tokenIndex, sess.Token)
			}

			delete(sm.state.Sessions, s.id)
			delete(sm.hookReports, s.id)
			deletedIDs = append(deletedIDs, s.id)
			removedSet[s.id] = true
		} else if sess, ok := sm.state.Sessions[s.id]; ok {
			if s.prevStatus == StatusRunning {
				sess.Status = StatusStopped
			} else {
				sess.Status = s.prevStatus
			}
		}
	}

	for _, s := range sm.state.Sessions {
		if s.ParentID != "" && removedSet[s.ParentID] {
			s.ParentID = ""
		}
	}

	stateErr := sm.saveState()
	sm.mu.Unlock()

	for _, s := range snaps {
		if succeeded[s.id] {
			_ = os.Remove(filepath.Join(sm.paths.LogDir, s.id+".log"))
			_ = os.Remove(sm.nonoProfilePath(s.id))
			_ = os.Remove(sm.safehouseFragmentPath(s.id))
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

// killProcessGroup sends SIGTERM to the process group led by pid, waits up to
// 5 seconds for the group to exit, then sends SIGKILL if still alive.
func killProcessGroup(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}

	pgid := -pid
	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}

		return fmt.Errorf("SIGTERM to pgid %d: %w", pgid, err)
	}

	deadline := time.After(5 * time.Second)

	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			_ = syscall.Kill(pgid, syscall.SIGKILL)
			return nil
		case <-tick.C:
			if err := syscall.Kill(pgid, 0); err != nil {
				return nil
			}
		}
	}
}

func (sm *SessionManager) killVerifiedProcess(pid int, startTime int64) (killed bool, err error) {
	if pid <= 0 || !isProcessAlive(pid) {
		return false, nil
	}

	if startTime == 0 {
		return false, fmt.Errorf("no process identity recorded for PID %d", pid)
	}

	current, err := grpty.ProcessStartTime(pid)
	if err != nil {
		if !isProcessAlive(pid) {
			return false, nil
		}

		return false, fmt.Errorf("could not read process start time for PID %d: %w", pid, err)
	}

	if current != startTime {
		return false, fmt.Errorf("PID %d identity mismatch (recorded=%d, current=%d)", pid, startTime, current)
	}

	err = killProcessGroup(pid)

	return err == nil, err
}

type orphanCandidate struct {
	id           string
	pid          int
	pidStartTime int64
}

func (sm *SessionManager) cleanupOrphanedProcesses() {
	sm.mu.Lock()

	var candidates []orphanCandidate

	for id, sess := range sm.state.Sessions {
		if sess.Status != StatusRunning || sess.PID <= 0 {
			continue
		}

		if !isProcessAlive(sess.PID) {
			continue
		}

		if _, hasPTY := sm.sessions[id]; hasPTY {
			continue
		}

		candidates = append(candidates, orphanCandidate{
			id: id, pid: sess.PID, pidStartTime: sess.PIDStartTime,
		})
	}
	sm.mu.Unlock()

	for _, c := range candidates {
		verified := c.pidStartTime != 0
		if verified {
			current, err := grpty.ProcessStartTime(c.pid)
			if err != nil || current != c.pidStartTime {
				verified = false
			}
		}

		if verified {
			sm.log.Warn("killing orphaned process group",
				"id", c.id, "pid", c.pid)
			err := killProcessGroup(c.pid)

			sm.mu.Lock()
			if sess := sm.state.Sessions[c.id]; sess != nil {
				if err != nil {
					sess.Status = StatusErrored
					sess.StatusChangedAt = time.Now()
					sess.StopReason = StopReasonCrash
					applyLifecycleSummaryLocked(sess,
						fmt.Sprintf("Orphaned process (PID %d) — kill failed: %v", c.pid, err))
				} else {
					sess.Status = StatusStopped
					sess.StatusChangedAt = time.Now()
					sess.PID = 0
					sess.PIDStartTime = 0
					sess.StopReason = StopReasonCrash
					applyLifecycleSummaryLocked(sess,
						"Orphaned by daemon crash — killed")
				}
			}
			sm.mu.Unlock()
		} else {
			sm.mu.Lock()
			if sess := sm.state.Sessions[c.id]; sess != nil {
				sm.log.Warn("cannot verify orphaned process identity",
					"id", c.id, "pid", c.pid,
					"recorded_start_time", c.pidStartTime)

				sess.Status = StatusErrored
				sess.StatusChangedAt = time.Now()
				sess.StopReason = StopReasonCrash
				applyLifecycleSummaryLocked(sess, fmt.Sprintf(
					"Orphaned process (PID %d) — identity unverified, manual cleanup needed",
					c.pid))
			}
			sm.mu.Unlock()
		}
	}

	if len(candidates) > 0 {
		sm.mu.Lock()
		_ = sm.saveState()
		sm.mu.Unlock()
	}
}

// Stop sends SIGTERM to a session's process without removing the session or worktree.
func (sm *SessionManager) Stop(id string) error {
	return sm.stopWithReason(id, StopReasonUser)
}

func (sm *SessionManager) stopWithReason(id, reason string) error {
	sm.mu.Lock()
	sessState, ok := sm.state.Sessions[id]

	var status SessionStatus
	if ok {
		status = sessState.Status
	}

	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("session %q not found", id)
	}

	if status != StatusRunning {
		sm.mu.Unlock()
		return fmt.Errorf("session %q is not running", id)
	}

	sessState.StopReason = reason
	_ = sm.saveState()
	sm.mu.Unlock()

	ptySess, ok := sm.GetPTY(id)
	if ok {
		if err := ptySess.Kill(); err != nil {
			return fmt.Errorf("send SIGTERM: %w", err)
		}

		return nil
	}

	sm.mu.Lock()
	pid := sessState.PID
	startTime := sessState.PIDStartTime
	sm.mu.Unlock()

	killed, err := sm.killVerifiedProcess(pid, startTime)

	sm.mu.Lock()
	if s, ok := sm.state.Sessions[id]; ok {
		switch {
		case killed:
			s.Status = StatusStopped
			s.StatusChangedAt = time.Now()
			s.PID = 0
			s.PIDStartTime = 0
			applyLifecycleSummaryLocked(s, "Orphaned process killed")
		case err != nil:
			s.Status = StatusErrored
			s.StatusChangedAt = time.Now()
			applyLifecycleSummaryLocked(s, fmt.Sprintf("Could not kill orphaned process (PID %d): %v", pid, err))
		default:
			s.Status = StatusStopped
			s.StatusChangedAt = time.Now()
			s.PID = 0
			s.PIDStartTime = 0
			applyLifecycleSummaryLocked(s, "Process already exited")
		}

		_ = sm.saveState()
	}
	sm.mu.Unlock()

	return err
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
		if !ok {
			sm.mu.Unlock()
			continue
		}

		if sess.Starred {
			sm.mu.Unlock()
			sm.log.Info("skipping starred session in bulk stop", "session_id", id, "name", sess.Name)

			continue
		}

		if sess.Status != StatusRunning {
			sm.mu.Unlock()
			continue
		}

		sess.StopReason = StopReasonUser
		pid := sess.PID
		startTime := sess.PIDStartTime
		_ = sm.saveState()
		sm.mu.Unlock()

		ptySess, ok := sm.GetPTY(id)
		if ok {
			if err := ptySess.Kill(); err != nil {
				sm.log.Warn("stop child failed", "session_id", id, "error", err)
				continue
			}

			stopped = append(stopped, id)

			continue
		}

		killed, killErr := sm.killVerifiedProcess(pid, startTime)
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			switch {
			case killed:
				s.Status = StatusStopped
				s.StatusChangedAt = time.Now()
				s.PID = 0
				s.PIDStartTime = 0
				applyLifecycleSummaryLocked(s, "Orphaned process killed")

				stopped = append(stopped, id)
			case killErr != nil:
				s.Status = StatusErrored
				s.StatusChangedAt = time.Now()
				applyLifecycleSummaryLocked(s, fmt.Sprintf("Could not kill orphaned process (PID %d): %v", pid, killErr))
			default:
				s.Status = StatusStopped
				s.StatusChangedAt = time.Now()
				s.PID = 0
				s.PIDStartTime = 0
				applyLifecycleSummaryLocked(s, "Process already exited")

				stopped = append(stopped, id)
			}

			_ = sm.saveState()
		}
		sm.mu.Unlock()
	}

	// Sweep for sessions created between collectDescendants and the stop loop.
	stoppedSet := make(map[string]bool, len(toStop))
	for _, id := range toStop {
		stoppedSet[id] = true
	}

	const maxSweepRounds = 10
	for sweep := 0; sweep < maxSweepRounds; sweep++ {
		sm.mu.Lock()

		var late []string

		progress := false

		for sid, sess := range sm.state.Sessions {
			if stoppedSet[sid] || sess.ParentID == "" || !stoppedSet[sess.ParentID] {
				continue
			}

			stoppedSet[sid] = true

			if sess.Starred {
				continue
			}

			if sess.Status == StatusCreating {
				// Remove placeholder so Phase 3 of Create detects the
				// cancellation and cleans up the PTY/worktree.
				delete(sm.state.Sessions, sid)
				delete(sm.hookReports, sid)

				progress = true

				continue
			}

			if sess.Status != StatusRunning {
				continue
			}

			progress = true

			late = append(late, sid)
			sess.StopReason = StopReasonUser
		}

		if !progress {
			sm.mu.Unlock()
			break
		}

		if len(late) > 0 {
			sm.log.Info("sweep found late-arriving descendants to stop", "count", len(late), "round", sweep+1)
		}

		_ = sm.saveState()
		sm.mu.Unlock()

		for _, lid := range late {
			ptySess, ok := sm.GetPTY(lid)
			if !ok {
				continue
			}

			if err := ptySess.Kill(); err != nil {
				sm.log.Warn("stop late child failed", "session_id", lid, "error", err)
				continue
			}

			stopped = append(stopped, lid)
		}

		if sweep == maxSweepRounds-1 {
			sm.log.Warn("stop sweep reached round cap, some descendants may remain", "cap", maxSweepRounds)
		}
	}

	return stopped, nil
}

// Restart stops a running session (or no-ops if already stopped) and resumes it,
// picking up the current agent and sandbox configuration.
func (sm *SessionManager) Restart(id string, rows, cols uint16) (SessionState, error) {
	sm.mu.RLock()

	softDeleted := false
	if s, ok := sm.state.Sessions[id]; ok {
		softDeleted = s.IsSoftDeleted()
	}

	sm.mu.RUnlock()

	if softDeleted {
		return SessionState{}, errSoftDeleted(sm.sessionName(id))
	}

	ptySess, hasPTY := sm.GetPTY(id)
	if hasPTY && !ptySess.Exited() {
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.StopReason = StopReasonUser
		}
		sm.mu.Unlock()

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
			s.PIDStartTime = 0

			_ = sm.saveState()
		}
		sm.mu.Unlock()
	} else if !hasPTY {
		sm.mu.Lock()

		sess, ok := sm.state.Sessions[id]
		if ok && sess.Status == StatusRunning && sess.PID > 0 {
			pid := sess.PID
			startTime := sess.PIDStartTime
			sm.mu.Unlock()

			killed, killErr := sm.killVerifiedProcess(pid, startTime)

			sm.mu.Lock()
			if s, ok := sm.state.Sessions[id]; ok && s.Status == StatusRunning {
				switch {
				case killed:
					s.Status = StatusStopped
					s.StatusChangedAt = time.Now()
					s.PID = 0
					s.PIDStartTime = 0
					s.StopReason = StopReasonUser
					applyLifecycleSummaryLocked(s, "Orphaned process killed for restart")

					_ = sm.saveState()
				case killErr == nil:
					s.Status = StatusStopped
					s.StatusChangedAt = time.Now()
					s.PID = 0
					s.PIDStartTime = 0
					s.StopReason = StopReasonUser
					applyLifecycleSummaryLocked(s, "Process already exited")

					_ = sm.saveState()
				default:
					s.Status = StatusErrored
					s.StatusChangedAt = time.Now()
					applyLifecycleSummaryLocked(s,
						fmt.Sprintf("Cannot restart: orphaned process (PID %d) — %v", pid, killErr))

					_ = sm.saveState()
					sm.mu.Unlock()

					return SessionState{}, fmt.Errorf("cannot restart: orphaned process (PID %d) could not be killed: %w", pid, killErr)
				}
			}
			sm.mu.Unlock()
		} else {
			sm.mu.Unlock()
		}
	}

	return sm.resumeWithSummary(id, rows, cols, "Restarted")
}

func (sm *SessionManager) RestartWithChildren(rootID string, excludeRoot bool, rows, cols uint16) ([]string, error) {
	sm.mu.Lock()
	if _, ok := sm.state.Sessions[rootID]; !ok {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q not found", rootID)
	}

	toRestart := sm.collectDescendants(rootID)
	if excludeRoot {
		toRestart = filterExcludeRoot(toRestart, rootID)
	}
	sm.mu.Unlock()

	var restarted []string

	for _, id := range toRestart {
		sm.mu.RLock()

		sess, ok := sm.state.Sessions[id]
		if !ok {
			sm.mu.RUnlock()
			continue
		}

		if sess.Starred {
			sm.mu.RUnlock()
			sm.log.Info("skipping starred session in bulk restart", "session_id", id, "name", sess.Name)

			continue
		}

		if sess.Status == StatusDeleting || sess.Status == StatusCreating {
			sm.mu.RUnlock()
			continue
		}

		if sess.IsSoftDeleted() {
			sm.mu.RUnlock()
			sm.log.Info("skipping soft-deleted session in bulk restart", "session_id", id, "name", sess.Name)

			continue
		}

		sm.mu.RUnlock()

		if _, err := sm.Restart(id, rows, cols); err != nil {
			sm.log.Warn("restart child failed", "session_id", id, "error", err)
			continue
		}

		restarted = append(restarted, id)
	}

	return restarted, nil
}

// Star marks a session as starred.
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

	if s.IsSoftDeleted() {
		return errSoftDeleted(s.Name)
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

	if s.IsSoftDeleted() {
		return errSoftDeleted(s.Name)
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

	// A soft-deleted session's summary is the "recoverable until …" trash marker;
	// a lingering background `gr status` must not overwrite it and mask the
	// session's deleted state in the overlay/logs.
	if s.IsSoftDeleted() {
		return errSoftDeleted(s.Name)
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

	if s.IsSoftDeleted() {
		return errSoftDeleted(s.Name)
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

	if IsSystemSession(s) {
		return fmt.Errorf("cannot rename system session %q", s.Name)
	}

	if s.IsSoftDeleted() {
		return errSoftDeleted(s.Name)
	}

	s.Name = newName

	return sm.saveState()
}

func (sm *SessionManager) Update(id string, name *string, parentID *string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}

	if IsSystemSession(s) {
		return fmt.Errorf("cannot update system session %q", s.Name)
	}

	if s.IsSoftDeleted() {
		return errSoftDeleted(s.Name)
	}

	if name != nil {
		if err := ValidateSessionName(*name); err != nil {
			return err
		}
	}

	newParentValue := s.ParentID

	if parentID != nil {
		newParent := *parentID
		if newParent == "" {
			newParentValue = ""
		} else {
			if newParent == id {
				return fmt.Errorf("cannot set session as its own parent")
			}

			if _, ok := sm.state.Sessions[newParent]; !ok {
				return fmt.Errorf("parent session %q not found", newParent)
			}

			descendants := sm.collectDescendants(id)
			for _, d := range descendants {
				if d == newParent {
					return fmt.Errorf("cannot set descendant %q as parent (would create cycle)", newParent)
				}
			}

			newParentValue = newParent
		}
	}

	if name != nil {
		s.Name = *name
	}

	s.ParentID = newParentValue

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
		if s.IsSoftDeleted() {
			continue
		}

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

	var (
		sessions []protocol.SessionDiagnostic
		sbDiag   protocol.ScrollbackDiagnostic
		fleet    protocol.FleetSummary
	)

	for id, s := range sm.state.Sessions {
		// Soft-deleted sessions are hidden trash awaiting purge; exclude them from
		// diagnostics and the fleet tally so `gr doctor` reflects live work only.
		if s.IsSoftDeleted() {
			continue
		}

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

		_, hasPTY := sm.sessions[id]
		hasPTYVal := hasPTY
		sd.HasPTY = &hasPTYVal

		if s.WorktreePath != "" {
			sd.WorktreePath = s.WorktreePath
			if _, err := os.Stat(s.WorktreePath); err == nil {
				sd.WorktreeExists = true
			}
		}

		sd.ConfigStale = isConfigStale(*s, cfg)
		sd.HasToken = s.Token != ""

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

// scrollbackLogPath returns the on-disk scrollback log path for a session ID.
// The file persists after the live PTY is torn down, so it can be read for
// stopped or crashed sessions.
func (sm *SessionManager) scrollbackLogPath(id string) string {
	return filepath.Join(sm.paths.LogDir, id+".log")
}

// GetPTY returns the live PTY session by ID.
func (sm *SessionManager) GetPTY(id string) (*grpty.Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	s, ok := sm.sessions[id]

	return s, ok
}

func (sm *SessionManager) getHookReport(sessionID string) *hookReport {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if hr, ok := sm.hookReports[sessionID]; ok {
		return &hr
	}

	return nil
}

// StopAll gracefully terminates all running sessions concurrently.
// Each session gets up to 5 seconds to exit after SIGTERM before being
// force-killed. Sessions are waited on in parallel so the total wait
// time is bounded by the slowest session, not the sum.
func (sm *SessionManager) StopAll(ctx context.Context) {
	sm.mu.Lock()
	for _, s := range sm.state.Sessions {
		if s.Status == StatusRunning {
			prevSummary := s.SummaryText
			prevSetAt := s.SummarySetAt

			prevTTL := sm.cfg.Status.TTLDuration()
			if s.SummaryTTL > 0 {
				prevTTL = time.Duration(s.SummaryTTL) * time.Second
			}

			s.StopReason = StopReasonShutdown
			text := formatStopSummary(StopReasonShutdown, nil, "", prevSummary, prevSetAt, prevTTL)
			applyLifecycleSummaryLocked(s, text)
		}
	}

	_ = sm.saveState()

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

	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(id string, sess *grpty.Session) {
			defer wg.Done()

			select {
			case <-sess.Done():
			case <-ctx.Done():
				sm.log.Warn("shutdown context expired, force killing session", "id", id)

				_ = sess.ForceKill()
			case <-time.After(5 * time.Second):
				sm.log.Warn("force killing session", "id", id)

				_ = sess.ForceKill()
			}
		}(s.id, s.sess)
	}

	wg.Wait()

	// Wait for the exit watchers to finish their post-exit work (state writes
	// and status publishes). Every killed session above has now exited, so the
	// watchers can proceed and will not block. This guarantees no watcher is
	// still writing state or publishing to the message store after StopAll
	// returns, which matters when the caller then closes the message store or
	// removes the data dir.
	sm.watchers.Wait()
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

// RunDetectionLoop periodically scans PTY scrollback to detect low-risk agent
// status (active/ready) for all running sessions. Approval status comes from
// hooks or the daemon approval queue, not PTY text.
func (sm *SessionManager) RunDetectionLoop(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// The detection tick is far too frequent to fetch on (network I/O). A
	// slower fetch keeps origin/<base> reasonably fresh so the fallback
	// diverged-from-base count doesn't go stale after remote merges (#197).
	fetchTicker := time.NewTicker(fetchInterval)
	defer fetchTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sm.detectAgentStatuses()
		case <-fetchTicker.C:
			sm.fetchRemotes(ctx)
		}
	}
}

// fetchInterval is how often the detection loop refreshes remote tracking refs.
const fetchInterval = 5 * time.Minute

// fetchPerRepoTimeout bounds a single `git fetch` so a slow or hung remote
// can't stall the fetch pass for other sessions.
const fetchPerRepoTimeout = 30 * time.Second

// fetchRemotes runs a best-effort `git fetch` for every running session's
// worktree (and included repos) so remote tracking refs stay fresh. Failures
// are logged at debug level and otherwise ignored — a session may be offline
// or have no remote.
func (sm *SessionManager) fetchRemotes(ctx context.Context) {
	sm.mu.RLock()

	seen := make(map[string]struct{})

	var dirs []string

	for _, s := range sm.state.Sessions {
		if s.Status != StatusRunning || s.SharedWorktree {
			continue
		}

		if s.WorktreePath != "" {
			if _, ok := seen[s.WorktreePath]; !ok {
				seen[s.WorktreePath] = struct{}{}
				dirs = append(dirs, s.WorktreePath)
			}
		}

		for i := range s.Includes {
			wp := s.Includes[i].WorktreePath
			if wp == "" {
				continue
			}

			if _, ok := seen[wp]; !ok {
				seen[wp] = struct{}{}
				dirs = append(dirs, wp)
			}
		}
	}

	sm.mu.RUnlock()

	for _, dir := range dirs {
		if ctx.Err() != nil {
			return
		}

		if !git.HasRemote(dir, "origin") {
			continue
		}

		fetchCtx, cancel := context.WithTimeout(ctx, fetchPerRepoTimeout)
		if err := git.FetchRemote(fetchCtx, dir); err != nil {
			sm.log.Debug("periodic fetch failed", "dir", dir, "error", err)
		}

		cancel()
	}
}

func (sm *SessionManager) detectAgentStatuses() {
	sm.mu.RLock()

	type target struct {
		id             string
		name           string
		agent          string
		prevStatus     string
		pty            *grpty.Session
		worktreePath   string
		baseBranch     string
		repoPath       string
		includes       []IncludedRepoState
		sharedWorktree bool
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
				includes: inc, sharedWorktree: s.SharedWorktree,
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

		var (
			dirty    bool
			unpushed int
		)

		if !t.sharedWorktree {
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
			if lastOut := t.pty.LastOutputAt(); !lastOut.IsZero() {
				s.LastOutputAt = &lastOut
			}

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

		if err := sm.stopWithReason(id, StopReasonIdle); err != nil {
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
		var timeout time.Duration
		if s.SystemKind == SystemKindOrchestrator {
			timeout = sm.cfg.Orchestrator.IdleTimeoutDuration()
		} else {
			agentCfg := sm.cfg.Agents[s.Agent]
			timeout = agentCfg.IdleTimeoutDuration()
		}

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

	if old.GitPull.Enabled != newCfg.GitPull.Enabled {
		sm.log.Info("config changed", "key", "git_pull.enabled", "old", old.GitPull.Enabled, "new", newCfg.GitPull.Enabled)
	}

	if old.GitPull.Interval != newCfg.GitPull.Interval {
		sm.log.Info("config changed", "key", "git_pull.interval", "old", old.GitPull.Interval, "new", newCfg.GitPull.Interval)
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

	if fmt.Sprint(old.Sandbox.ReadFiles) != fmt.Sprint(newCfg.Sandbox.ReadFiles) {
		sm.log.Info("config changed", "key", "sandbox.read_files", "old", old.Sandbox.ReadFiles, "new", newCfg.Sandbox.ReadFiles)
	}

	if fmt.Sprint(old.Sandbox.WriteFiles) != fmt.Sprint(newCfg.Sandbox.WriteFiles) {
		sm.log.Info("config changed", "key", "sandbox.write_files", "old", old.Sandbox.WriteFiles, "new", newCfg.Sandbox.WriteFiles)
	}

	if fmt.Sprint(old.Sandbox.Features) != fmt.Sprint(newCfg.Sandbox.Features) {
		sm.log.Info("config changed", "key", "sandbox.features", "old", old.Sandbox.Features, "new", newCfg.Sandbox.Features)
	}

	if sm.mcpManager != nil {
		sm.mcpManager.Reload(newCfg)
		sm.log.Info("MCP manager config reloaded")
	}

	if old.Orchestrator.Enabled != newCfg.Orchestrator.Enabled {
		sm.log.Info("config changed", "key", "orchestrator.enabled", "old", old.Orchestrator.Enabled, "new", newCfg.Orchestrator.Enabled)

		if newCfg.Orchestrator.Enabled {
			go sm.ensureOrchestrator(context.Background())
		} else {
			if orchID := func() string {
				sm.mu.RLock()
				defer sm.mu.RUnlock()

				return sm.findOrchestratorID()
			}(); orchID != "" {
				_ = sm.stopWithReason(orchID, StopReasonUser)
			}
		}
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
	return sm.resolveSandboxFromConfig(sm.cfg, agentName)
}

// approvalsConfigDir returns the directory holding graith's config file, used to
// resolve a relative [approvals.builtin] config path deterministically (rather
// than against the daemon's working directory). It prefers the explicit global
// --config override (sm.configFile, the file the daemon actually loaded) over
// the default resolved path, mirroring the CLI's approvalsConfigDir so
// `gr --config X approvals validate` and daemon enforcement resolve a relative
// path against the same directory. Returns "" when no config path is known, in
// which case a relative path is left for the caller to resolve against the
// working directory as before.
func (sm *SessionManager) approvalsConfigDir() string {
	if f := strings.TrimSpace(sm.configFile); f != "" {
		return filepath.Dir(f)
	}

	if sm.paths.ConfigFile == "" {
		return ""
	}

	return filepath.Dir(sm.paths.ConfigFile)
}

// validateApprovalsBackend fails closed at session-create when the configured
// approvals backend can't enforce — a command backend with no command, a
// missing localmost binary, or an unreadable/invalid builtin config. This
// mirrors the sandbox availability check (resolveSandboxFromConfig) so a
// misconfigured approvals backend errors loudly at create time instead of
// silently deferring every request to the human. The default (prompt) backend
// always enforces. Callers hold sm.mu.
//
// A yolo session resolves every request through the auto backend, which always
// enforces, so the global [approvals] backend is irrelevant to it — validating
// (and failing on) an unavailable global backend would contradict yolo's
// per-session override. Yolo sessions therefore skip the global check.
func (sm *SessionManager) validateApprovalsBackend(yolo bool) error {
	if yolo {
		return nil
	}

	acfg := sm.cfg.Approvals

	backend, _, err := acfg.ResolveBackend()
	if err != nil {
		return err
	}

	if backend == "" || backend == approvals.BackendPrompt {
		return nil
	}

	be, err := approvals.BackendByName(backend)
	if err != nil {
		return err
	}

	beCfg, err := approvalsBackendConfig(backend, acfg, sm.approvalsConfigDir())
	if err != nil {
		return err
	}

	if av := be.Availability(beCfg); !av.CanEnforce {
		return fmt.Errorf("approvals backend %q cannot enforce: %s", backend, av.Detail)
	}

	return nil
}

func (sm *SessionManager) resolveSandboxFromConfig(cfg *config.Config, agentName string) (bool, error) {
	merged := cfg.Sandbox.Merge(cfg.Agents[agentName].Sandbox)
	if !merged.Enabled {
		return false, nil
	}

	avail, err := validateSandboxBackend(merged, fmt.Sprintf("agent %q", agentName))
	if err != nil {
		return false, err
	}

	if avail.Degraded {
		sm.log.Warn("sandbox enforcement degraded", "agent", agentName, "backend", merged.Backend, "detail", avail.Detail)
	}

	return true, nil
}

// validateSandboxBackend enforces the explicit-backend rule and availability
// check for an already-enabled merged sandbox config, returning the resolved
// availability on success. subject names the process being sandboxed (e.g.
// `agent "claude"` or `MCP server chrome`) and is interpolated into the
// fail-closed errors. It is shared by the session (resolveSandboxFromConfig)
// and MCP-server (MCPManager.startProcess) startup paths so both fail closed
// identically — in particular, neither may silently fall back to safehouse
// when no backend is selected (see #787). sandbox.Wrap keeps its empty-backend
// compatibility only for low-level helpers that don't represent user config.
func validateSandboxBackend(merged config.SandboxConfig, subject string) (sandbox.Availability, error) {
	// Backend must be chosen explicitly — there is no default. Fail closed with
	// an actionable error rather than silently picking one.
	if merged.Backend == "" {
		return sandbox.Availability{}, fmt.Errorf(
			"sandbox enabled for %s but no backend selected — set [sandbox] backend = %q (macOS) or %q (Linux/macOS) in config",
			subject, sandbox.BackendSafehouse, sandbox.BackendNono)
	}

	req := sandbox.Requirements{Network: merged.Network.IsSet()}

	avail, err := sandbox.CheckAvailability(merged.Backend, merged.Command, req)
	if err != nil {
		return sandbox.Availability{}, fmt.Errorf("sandbox enabled for %s: %w", subject, err)
	}

	if !avail.CanEnforce {
		return sandbox.Availability{}, fmt.Errorf(
			"sandbox enabled for %s with backend %q but it cannot enforce: %s",
			subject, merged.Backend, avail.Detail)
	}

	return avail, nil
}

// nonoProfilePath returns the location of the per-session nono sandbox profile
// for the given session ID. The nono backend writes this file under RuntimeDir
// (see sandboxOptsFromConfig); session teardown removes it here so the small
// JSON files don't accumulate for the lifetime of the daemon's data dir.
func (sm *SessionManager) nonoProfilePath(sessionID string) string {
	return filepath.Join(sm.paths.RuntimeDir, "nono", sessionID+".json")
}

// resolveSocketPath returns the symlink-resolved daemon socket path. Seatbelt
// and Landlock match canonical (symlink-resolved) paths, but sm.paths.SocketPath
// comes from filepath.Join and is not resolved — so a data/runtime dir under a
// symlinked prefix (e.g. macOS /tmp -> /private/tmp, /var -> /private/var) would
// make the sandbox grant's path-literal miss and silently re-deny the connect,
// reintroducing the original bug with a green test. Resolve here, at the single
// choke point, so every backend gets the canonical path. Falls back to resolving
// the parent dir + basename (the socket's own inode is a live AF_UNIX node), then
// to the raw path if resolution fails (e.g. before the socket file exists).
func resolveSocketPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}

	dir, base := filepath.Split(p)
	if rdir, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(rdir, base)
	}

	return p
}

// safehouseFragmentPath returns the location of the per-session safehouse
// Seatbelt fragment (the --append-profile file that grants the daemon socket
// connect access) for the given session ID. Written under RuntimeDir by the
// safehouse backend; session teardown removes it alongside the nono profile.
func (sm *SessionManager) safehouseFragmentPath(sessionID string) string {
	return filepath.Join(sm.paths.RuntimeDir, "safehouse", sessionID+".sb")
}

func (sm *SessionManager) sandboxOptsFromConfig(merged config.SandboxConfig, sessionID, worktreePath, agentCommand string, envKeys []string, agentHooks bool) sandbox.WrapOpts {
	readDirs := expandPaths(merged.ReadDirs, sm.log, "read")
	writeDirs := expandPaths(merged.WriteDirs, sm.log, "write")
	readFiles := expandFilePaths(merged.ReadFiles, sm.log, "read")
	writeFiles := expandFilePaths(merged.WriteFiles, sm.log, "write")

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

	// The runtime dir grant above is read-only, which lets the agent see the
	// daemon socket but NOT connect() to it (Seatbelt/Landlock gate socket
	// connect separately from file read). Grant the socket explicitly so
	// sandboxed agents can reach the daemon for `gr msg`, `gr status`, etc.
	// This is scoped to the single socket file, not the whole runtime/data dir.
	// The path is symlink-resolved (see resolveSocketPath): Seatbelt/Landlock
	// match canonical paths, so a data/runtime dir under a symlinked prefix
	// would otherwise make the grant's path-literal miss and silently re-deny.
	unixSockets := []string{resolveSocketPath(sm.paths.SocketPath)}

	// nono does not auto-grant the launched command's location (only system
	// paths like /usr/bin). Grant read on the agent binary's directory so the
	// sandboxed process can exec it. safehouse is unaffected by the extra dir.
	if dir := agentBinaryDir(agentCommand); dir != "" {
		readDirs = append(readDirs, dir)
	}

	// Under nono, a non-empty env allowlist scrubs everything else, so the vars
	// the agent needs to function (PATH, HOME) must be present. safehouse
	// re-adds these itself; including them in EnvKeys is harmless there.
	envKeys = ensureEnvKeys(envKeys, "PATH", "HOME")

	// The nono backend writes a per-session profile under RuntimeDir, which is
	// already granted read access above, so the profile is readable inside the
	// sandbox and lives for the process lifetime (incl. resume). The safehouse
	// backend likewise writes its --append-profile socket fragment under
	// RuntimeDir (read by safehouse before the sandbox is applied).
	profilePath := sm.nonoProfilePath(sessionID)
	fragmentPath := sm.safehouseFragmentPath(sessionID)

	return sandbox.WrapOpts{
		Backend:               merged.Backend,
		WorktreeDir:           worktreePath,
		ReadDirs:              readDirs,
		WriteDirs:             writeDirs,
		ReadFiles:             readFiles,
		WriteFiles:            writeFiles,
		UnixSockets:           unixSockets,
		Features:              merged.Features,
		EnvKeys:               envKeys,
		SignalMode:            merged.SignalMode,
		Profile:               merged.Profile,
		Network:               networkPolicy(merged.Network),
		BackendCommand:        merged.Command,
		ProfilePath:           profilePath,
		SafehouseFragmentPath: fragmentPath,
	}
}

// networkPolicy converts a config network policy into the sandbox package's
// resolved NetworkPolicy. Nil (or an empty policy) yields nil so the backend
// leaves nono's allow-by-default posture untouched.
func networkPolicy(n *config.SandboxNetworkConfig) *sandbox.NetworkPolicy {
	if !n.IsSet() {
		return nil
	}

	return &sandbox.NetworkPolicy{
		Block:        n.Block,
		AllowDomains: n.AllowDomains,
	}
}

// agentBinaryDir resolves the directory containing the agent command so it can
// be granted read access in the sandbox. It resolves bare command names via
// PATH; returns "" if it cannot be resolved (e.g. a shell builtin).
func agentBinaryDir(command string) string {
	if command == "" {
		return ""
	}

	if strings.ContainsRune(command, filepath.Separator) {
		return filepath.Dir(command)
	}

	if resolved, err := exec.LookPath(command); err == nil {
		return filepath.Dir(resolved)
	}

	return ""
}

// ensureEnvKeys appends any of want not already present in keys.
func ensureEnvKeys(keys []string, want ...string) []string {
	have := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		have[k] = struct{}{}
	}

	for _, w := range want {
		if _, ok := have[w]; !ok {
			keys = append(keys, w)
			have[w] = struct{}{}
		}
	}

	return keys
}

func expandPaths(paths []string, log *slog.Logger, kind string) []string {
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

		if _, err := os.Stat(expanded); err != nil {
			log.Warn("sandbox: skipping non-existent path", "kind", kind, "path", expanded)
			continue
		}

		out = append(out, expanded)
	}

	return out
}

// expandFilePaths expands ~ and globs in single-file grant paths (read_files /
// write_files). Unlike expandPaths (for directories), it does NOT drop a path
// that doesn't exist on disk: a writable file grant is routinely for a file the
// agent creates at runtime — e.g. Claude's ~/.claude.json.lock / ~/.claude.lock
// lockfiles, which don't exist until a write happens. Stat-filtering those would
// silently drop the grant (and under nono deny the agent from creating the
// file). Globs that match nothing are still skipped (there is nothing to grant);
// a literal path whose parent directory is missing is kept but warned, since
// nono cannot create the file without a grantable parent.
func expandFilePaths(paths []string, log *slog.Logger, kind string) []string {
	if len(paths) == 0 {
		return nil
	}

	var out []string

	for _, p := range paths {
		expanded := config.ExpandPath(p)
		if strings.ContainsAny(expanded, "*?[") {
			if matches, err := filepath.Glob(expanded); err == nil && len(matches) > 0 {
				out = append(out, matches...)
			} else {
				log.Warn("sandbox: file grant glob matched nothing", "kind", kind, "path", expanded)
			}

			continue
		}

		if _, err := os.Stat(filepath.Dir(expanded)); err != nil {
			log.Warn("sandbox: file grant parent dir does not exist", "kind", kind, "path", expanded)
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
			_ = os.Remove(sock)
			_ = os.Remove(pid)

			log.Info("removed stale legacy socket", "path", sock)

			continue
		}

		_ = conn.Close()

		data, err := os.ReadFile(pid)
		if err == nil {
			var legacyPID int
			if _, err := fmt.Sscanf(string(data), "%d", &legacyPID); err == nil && IsGraithDaemon(legacyPID) {
				log.Info("stopping legacy daemon", "pid", legacyPID, "socket", sock)
				_ = syscall.Kill(legacyPID, syscall.SIGTERM)
			}
		}

		_ = os.Remove(sock)
		_ = os.Remove(pid)
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

		_ = os.Remove(adoptFrom)

		if manifest.Profile != paths.Profile {
			return fmt.Errorf("profile mismatch: manifest has %q but daemon is %q", manifest.Profile, paths.Profile)
		}

		f := os.NewFile(uintptr(manifest.ListenerFd), "listener")
		l, err = net.FileListener(f)
		_ = f.Close()

		if err != nil {
			return fmt.Errorf("adopt listener from fd %d: %w", manifest.ListenerFd, err)
		}

		if err := sm.LoadState(); err != nil {
			var ve *StateVersionError
			if errors.As(err, &ve) {
				return fmt.Errorf("refusing to start: %w (downgrade would discard the newer state)", err)
			}

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
			var ve *StateVersionError
			if errors.As(err, &ve) {
				ReleasePIDFile(paths.PIDFile)
				return fmt.Errorf("refusing to start: %w (downgrade would discard the newer state)", err)
			}

			log.Warn("failed to load state", "err", err)
		}

		sm.cleanupOrphanedProcesses()

		logAttrs := []any{"socket", paths.SocketPath, "pid", os.Getpid()}
		if paths.Profile != "" {
			logAttrs = append(logAttrs, "profile", paths.Profile)
		}

		log.Info("daemon started", logAttrs...)
	}

	defer func() { _ = l.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(l, func(ctx context.Context, conn net.Conn) {
		HandleConnection(ctx, conn, ConnOrigin{}, sm, log)
	}, log)

	go func() { _ = srv.Serve(ctx) }()

	// Optional tailnet-facing remote control surface (design §A). Off by
	// default; the local Unix socket above is unaffected when disabled.
	if sm.cfg.Remote.Enabled {
		if len(sm.cfg.Remote.AllowTailnetUsers) == 0 {
			log.Warn("[remote] enabled with empty allow_tailnet_users — all remote connections denied (Gate 1 fail-closed)")
		}

		certPath := filepath.Join(paths.DataDir, "remote-tls.crt")
		keyPath := filepath.Join(paths.DataDir, "remote-tls.key")

		if cert, pin, tErr := loadOrCreateRemoteTLS(certPath, keyPath, sm.cfg.Remote.Hostname, time.Now()); tErr != nil {
			log.Error("[remote] TLS setup failed; remote surface disabled", "err", tErr)
		} else if rl, rErr := newRemoteListener(ctx, sm.cfg.Remote, paths.DataDir); rErr != nil {
			log.Error("[remote] listener setup failed; remote surface disabled", "err", rErr)
		} else {
			sm.remoteTLSPin = pin

			log.Info("[remote] starting control surface", "mode", sm.cfg.Remote.Mode, "port", sm.cfg.Remote.Port, "tls_spki", pin)

			go func() {
				if err := sm.serveRemote(ctx, rl, sm.cfg.Remote, cert); err != nil {
					log.Error("[remote] control surface failed", "err", err)
				}
			}()
		}
	}

	go sm.RunDetectionLoop(ctx)
	go sm.RunMessageCleanupLoop(ctx)
	go sm.RunGitPullLoop(ctx)
	go sm.RunPRWatchLoop(ctx)
	go sm.RunPurgeLoop(ctx)

	go sm.orchestratorSupervisor(ctx, sm.orchestratorExitCh)
	go sm.ensureOrchestrator(ctx)

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

			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
			sm.StopAll(shutdownCtx)
			shutdownCancel()
			srv.Shutdown()

			_ = os.Remove(paths.SocketPath)
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
				_ = listenerFile.Close()

				log.Error("prepare upgrade", "err", err)

				return fmt.Errorf("upgrade failed: %w", err)
			}

			manifestPath, err := WriteManifest(paths.RuntimeDir, manifest)
			if err != nil {
				_ = listenerFile.Close()

				log.Error("write manifest", "err", err)

				return fmt.Errorf("upgrade failed: %w", err)
			}

			log.Info("exec-ing new binary", "manifest", manifestPath, "sessions", len(manifest.Sessions))

			if err := ExecUpgrade(manifestPath, configFile, clientExecPath); err != nil {
				_ = listenerFile.Close()
				_ = os.Remove(manifestPath)

				log.Error("exec failed", "err", err)

				return fmt.Errorf("upgrade exec failed: %w", err)
			}

			return nil
		}
	}
}
