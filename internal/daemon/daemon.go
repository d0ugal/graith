package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

	"github.com/d0ugal/graith/internal/agent/transcript"
	"github.com/d0ugal/graith/internal/approvals"
	"github.com/d0ugal/graith/internal/atomicfile"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/detector"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/headless"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/d0ugal/graith/internal/store"
	"github.com/fsnotify/fsnotify"
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
	sessions           map[string]SessionDriver
	attachedClients    map[string]*attachedClient
	hookReports        map[string]hookReport
	pendingApprovals   map[string]*pendingApproval
	headlessEscalated  map[string]bool                // session ID → orchestrator already escalated once (headless non-blocking deny)
	tokenIndex         map[string]string              // token → session ID (reverse lookup)
	humanToken         string                         // local human credential, loaded at startup
	saveStateFault     func() error                   // test-only saveState fault injection; nil in production
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
	// silentWarned tracks session IDs already flagged by the silent-session
	// diagnostic (running with a live PTY but zero output past the threshold),
	// so the Warn fires once per PTY lifetime rather than every detection tick.
	// Cleared when a session (re)spawns a PTY so a restart can warn afresh.
	silentWarned map[string]bool
	prWatch      *prWatchState
	prRefWatch   *prRefWatchState
	triggers     *triggerState
	tokens       *tokenCache
	launch       *launchThrottle

	// restartStuck is the startup watchdog's recovery action; nil in production
	// (falls back to Restart). Tests override it to observe watchdog decisions
	// without driving a full session respawn.
	restartStuck func(id string, rows, cols uint16) error

	// watchAdd overrides fsnotify directory registration for watch-trigger
	// bindings; nil in production (uses the watcher's Add). Tests set it to
	// simulate an exhausted watch limit (fs.inotify.max_user_watches) and its
	// subsequent recovery.
	watchAdd func(w *fsnotify.Watcher, path string) error

	// pushNotify guards proactive `gr notify` push-notification gating state:
	// a rolling window of delivered timestamps (rate limit) and a per-key map of
	// the last delivered time (coalescing of identical rapid-fire notifications,
	// so interleaved A/B/A duplicates are each coalesced against their own last
	// send, not just the immediately-previous one).
	pushMu       sync.Mutex
	pushLog      []time.Time
	pushCoalesce map[string]time.Time
	pushDispatch func(backend, title, message, priority string) error // overridable in tests

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
	sm := &SessionManager{
		state:              NewState(),
		sessions:           make(map[string]SessionDriver),
		attachedClients:    make(map[string]*attachedClient),
		hookReports:        make(map[string]hookReport),
		pendingApprovals:   make(map[string]*pendingApproval),
		headlessEscalated:  make(map[string]bool),
		tokenIndex:         make(map[string]string),
		pendingPairings:    make(map[string]*pendingPairing),
		pairWaiters:        make(map[string]chan pairApproval),
		approvalSubs:       make(map[net.Conn]func(string, any)),
		deviceTokenIndex:   make(map[string]string),
		connsByDevice:      make(map[string][]net.Conn),
		orchestratorExitCh: make(chan string, 4),
		lastInboxNotifyAt:  make(map[string]time.Time),
		silentWarned:       make(map[string]bool),
		prWatch:            newPRWatchState(),
		prRefWatch:         newPRRefWatchState(),
		triggers:           newTriggerState(),
		tokens:             newTokenCache(),
		launch:             newLaunchThrottle(cfg.Launch.MaxConcurrentOrDefault()),
		cfg:                cfg,
		paths:              paths,
		log:                log,
		startedAt:          time.Now(),
	}
	sm.pushDispatch = sm.newPushDispatch()

	return sm
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
	// Context-pressure and sub-agent events are runtime signals that must NOT
	// change AgentStatus — a compacting agent, or one that spawned a sub-agent,
	// is still active, and clobbering an approval/ready status here would be a
	// regression. They update runtime-only fields and return early (issue #1073).
	switch sr.Event {
	case "PreCompact", "PostCompact", "SubagentStart", "SubagentStop":
		sm.handleContextSubagentReport(sr)
		return
	}

	// SessionEnd is logical-session metadata, not a process-exit reason, and must
	// never touch AgentStatus (Claude fires it on /clear and interactive /resume
	// without terminating the PTY). Record the raw reason bound to the current
	// process generation; the process-exit path maps only process-ending reasons
	// onto a StopReason (mapSessionEndReason), and SessionStart/resume clears it
	// so a stale reason can't outlive its turn.
	if sr.Event == "SessionEnd" {
		sm.mu.Lock()
		if sess, ok := sm.state.Sessions[sr.SessionID]; ok {
			sess.SessionEndReason = sr.Reason
			sess.SessionEndReasonGen = sess.PIDStartTime
		} else {
			sm.mu.Unlock()
			sm.log.Info("session end for unknown session", "session_id", sr.SessionID)

			return
		}
		sm.mu.Unlock()

		sm.log.Info("session end reported", "session_id", sr.SessionID, "reason", sr.Reason)

		return
	}

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
	case "Notification":
		// A Claude Notification's meaning is in its subtype. The CLI forwards
		// the raw notification_type (empty when stdin didn't parse); the daemon
		// decides. Only idle_prompt (agent awaiting input) and permission_prompt
		// (approval needed) change status. Everything else — auth_success,
		// elicitation_*, and crucially an empty/unknown/unparsed subtype — is
		// logged without touching AgentStatus, so a parse timeout can no longer
		// spuriously flag a session as needing attention (the pre-subtype code
		// mapped every Notification to approval).
		switch sr.NotificationType {
		case "idle_prompt":
			status = "ready"
			staleness = 30 * time.Minute
		case "permission_prompt":
			status = "approval"
			staleness = 30 * time.Minute
		default:
			sm.log.Info("ignoring notification subtype",
				"event", sr.Event, "notification_type", sr.NotificationType,
				"session_id", sr.SessionID)

			return
		}
	case "PermissionRequest":
		// Codex's PreToolUse approval hook; not subtype-carrying.
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

	// Stop carries the agent's final message (already truncated by the CLI). Keep
	// it runtime-only; it is not placed on the guest-visible SessionInfo unredacted.
	if sr.Event == "Stop" && sr.LastMessage != "" {
		sess.LastMessage = sr.LastMessage
	}

	// A fresh SessionStart (a new turn, or Claude's /clear) resets the runtime
	// signals that don't carry across a turn: context-pressure and sub-agents
	// (issue #1073), plus any pending SessionEnd reason + its generation and the
	// captured final message, so a stale reason can't outlive the turn that
	// produced it. SubAgents is replaced (nil'd) rather than mutated in place so
	// an off-lock cloneSessionState stays race-free.
	if sr.Event == "SessionStart" {
		sess.ContextPressure = false
		sess.ContextPressureAt = time.Time{}
		sess.SubAgents = nil
		sess.SessionEndReason = ""
		sess.SessionEndReasonGen = 0
		sess.LastMessage = ""
	}

	sm.mu.Unlock()

	sm.log.Info("hook report processed",
		"session_id", sr.SessionID, "event", sr.Event,
		"status", status, "tool_name", sr.ToolName)

	// SessionStart is the agent's first sign of life after a launch, so the
	// launch→active gap tells a slow-but-healthy start apart from a stuck one
	// (issue #1104). The PTY's createdAt is the spawn instant; a fresh PTY is
	// created on every start/resume, so this measures the current launch.
	// Gate on oldStatus == "" so only the first activation is timed: create
	// leaves AgentStatus unset and resume clears it, whereas a mid-session
	// SessionStart (Claude's /clear or /compact) has a non-empty prior status
	// and would otherwise log a huge, misleading duration against the old PTY.
	if sr.Event == "SessionStart" && oldStatus == "" {
		if sess, ok := sm.GetPTY(sr.SessionID); ok {
			sm.log.Info("session active",
				"session_id", sr.SessionID, "name", name,
				"since_launch_ms", time.Since(sess.CreatedAt()).Milliseconds())
		}
	}

	if changed {
		sm.onAgentStatusChange(sr.SessionID, name, oldStatus, status)
	}
}

// handleContextSubagentReport processes the PreCompact/PostCompact and
// SubagentStart/SubagentStop hook events (issue #1073). These update runtime-only
// signals on the session and deliberately do NOT touch AgentStatus: a compacting
// agent, or one that has spawned a sub-agent, is still whatever it was before
// (active / approval / ready). The SubAgents map is replaced, never mutated in
// place, so an off-lock cloneSessionState reading len() is race-free.
func (sm *SessionManager) handleContextSubagentReport(sr protocol.StatusReportMsg) {
	sm.mu.Lock()

	sess, ok := sm.state.Sessions[sr.SessionID]
	if !ok {
		sm.mu.Unlock()
		sm.log.Info("hook report for unknown session", "session_id", sr.SessionID)

		return
	}

	now := time.Now()

	switch sr.Event {
	case "PreCompact":
		sess.ContextPressure = true
		sess.ContextPressureAt = now
	case "PostCompact":
		sess.ContextPressure = false
		sess.ContextPressureAt = now
	case "SubagentStart":
		// A sub-agent with no id is unusable for idempotent stop tracking; skip it
		// rather than key an entry we could never delete.
		if sr.AgentID != "" {
			next := make(map[string]string, len(sess.SubAgents)+1)
			for k, v := range sess.SubAgents {
				next[k] = v
			}

			next[sr.AgentID] = sr.AgentType
			sess.SubAgents = next
		}
	case "SubagentStop":
		// Idempotent: a duplicate or missing stop is a no-op, so the count never
		// underflows. Only rebuild the map when the id is actually present.
		if _, present := sess.SubAgents[sr.AgentID]; present {
			next := make(map[string]string, len(sess.SubAgents))
			for k, v := range sess.SubAgents {
				if k == sr.AgentID {
					continue
				}

				next[k] = v
			}

			sess.SubAgents = next
		}
	}

	contextPressure := sess.ContextPressure
	subAgents := len(sess.SubAgents)
	sm.mu.Unlock()

	sm.log.Info("hook report processed (runtime signal)",
		"session_id", sr.SessionID, "event", sr.Event,
		"context_pressure", contextPressure, "sub_agents", subAgents,
		"agent_id", sr.AgentID, "agent_type", sr.AgentType)
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

// loadOrCreateHumanToken loads the stable local-human credential, creating it
// crash-safely on first startup. It must never silently replace an existing
// credential, even when that credential cannot be read.
func (sm *SessionManager) loadOrCreateHumanToken() error {
	// Open without following symlinks so the credential can't be redirected to a
	// file an attacker controls; O_NOFOLLOW makes a symlink final component fail
	// to open rather than silently resolving.
	f, err := os.OpenFile(sm.paths.HumanTokenFile, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			// Includes ELOOP (symlink) and any other open failure: fail closed
			// rather than fall back to permissive behaviour.
			return fmt.Errorf("open human token: %w", err)
		}

		token, genErr := generateToken()
		if genErr != nil {
			return genErr
		}

		if writeErr := atomicfile.Write(sm.paths.HumanTokenFile, []byte(token+"\n"), 0o600); writeErr != nil {
			return fmt.Errorf("write human token: %w", writeErr)
		}

		sm.setHumanToken(token)

		return nil
	}
	defer func() { _ = f.Close() }()

	// The token is the roleLocalHuman bearer credential; its whole privilege
	// boundary rests on it being a private regular file. Validate the metadata on
	// the open descriptor (not a pre-open stat) so there is no check/use race.
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat human token: %w", err)
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("human token %s is not a regular file", sm.paths.HumanTokenFile)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		return fmt.Errorf("human token %s has insecure mode %04o, want 0600", sm.paths.HumanTokenFile, perm)
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read human token: %w", err)
	}

	token := strings.TrimSpace(string(data))
	if token == "" {
		return fmt.Errorf("read human token: token is empty")
	}

	sm.setHumanToken(token)

	return nil
}

func (sm *SessionManager) setHumanToken(token string) {
	sm.mu.Lock()
	sm.humanToken = token
	sm.mu.Unlock()
}

// EnsureHumanToken loads or creates the local human credential. It is exported
// for tests and embedders that construct a SessionManager without going through
// Run (which calls loadOrCreateHumanToken during startup).
func (sm *SessionManager) EnsureHumanToken() error {
	return sm.loadOrCreateHumanToken()
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
			Logger:     sm.log,
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
	if sm.saveStateFault != nil {
		if err := sm.saveStateFault(); err != nil {
			return err
		}
	}

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

// uniqueSessionIDLocked returns a freshly generated session ID that no current
// session already holds. Soft-deleted sessions retain their state entry (their
// worktree/branch persist pending purge), so their IDs are treated as taken.
// The caller must hold sm.mu: the ID is only guaranteed unique until the lock
// is released, so it must be reserved in the same critical section.
func (sm *SessionManager) uniqueSessionIDLocked() string {
	for {
		id := generateID()
		if _, exists := sm.state.Sessions[id]; !exists {
			return id
		}
	}
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

// mergeIncludes combines the repo-config includes with any per-session extra
// includes (e.g. from a scenario file), preserving order and dropping
// duplicates that resolve to the same path. Repo-config includes come first.
func mergeIncludes(repoIncludes, extra []string) []string {
	if len(repoIncludes) == 0 && len(extra) == 0 {
		return nil
	}

	merged := make([]string, 0, len(repoIncludes)+len(extra))
	seen := make(map[string]bool, len(repoIncludes)+len(extra))

	for _, inc := range append(append([]string{}, repoIncludes...), extra...) {
		key := config.ResolvePath(inc)
		if seen[key] {
			continue
		}

		seen[key] = true

		merged = append(merged, inc)
	}

	return merged
}

// CreateOpts holds the parameters for SessionManager.Create. Using a struct
// keeps call sites self-documenting and lets new options default to their
// zero value without breaking existing callers.
type CreateOpts struct {
	// ID, when non-empty, is the session ID to use instead of generating a
	// fresh one. It must match the generated ID format (8 lowercase hex chars)
	// and not collide with an existing session — Create validates both and
	// fails closed otherwise. Callers that must know the ID before Create
	// returns (e.g. scenario reservation, where a placeholder ID would
	// otherwise differ from the final session ID) supply it here. When empty,
	// Create generates the ID as before.
	ID         string
	Name       string
	AgentName  string
	RepoPath   string
	BaseBranch string
	Prompt     string
	Model      string
	// Codex carries typed per-session Codex CLI options (issue #1186). Ignored
	// (and rejected if non-zero) for non-codex agents.
	Codex               config.CodexOptions
	ParentID            string
	NoRepo              bool
	Mirror              string
	AgentHooks          bool
	InPlace             bool
	AllowConcurrent     bool
	SkipModelValidation bool
	Yolo                bool
	// Headless requests a headless stream-json session instead of an
	// interactive PTY (issue #1075). Honoured only when the agent is
	// headless_capable and [headless] experimental is enabled; otherwise Create
	// fails closed rather than silently downgrading. See resolveDriverKind.
	Headless bool
	// NoFetch skips the `git fetch origin` that normally runs before the
	// worktree is created (issue #1012), so a session can be created from local
	// repo state when SSH auth is unavailable (Secretive/biometric, offline).
	// It overrides fetch_on_create for this one creation only.
	NoFetch  bool
	Rows     uint16
	Cols     uint16
	EnvExtra []map[string]string
	// TriggerID / TriggerReactor tag a session spawned by a trigger, applied in
	// the same durable reservation as creation so reactor ownership survives a
	// crash between Create and a separate tag-and-save.
	TriggerID      string
	TriggerReactor bool
	// TrackerIssue tags a session spawned by a tracker action with its issue key
	// (see SessionState.TrackerIssue), applied in the same durable reservation as
	// creation so the reconcile dedup key survives a crash between Create and a
	// separate tag-and-save.
	TrackerIssue string
	// AutoCleanup marks a trigger-spawned session for soft-deletion when it
	// stops (config.CleanupAlways / config.CleanupOnSuccess; empty disables).
	AutoCleanup string
	// IdleTimeoutSecs overrides the agent-default idle-stop window for this
	// session (seconds; 0 = agent default).
	IdleTimeoutSecs int
	// Includes attaches extra worktrees to the session in addition to any
	// configured on the repo's [[repos]] entry. Merged with (and deduplicated
	// against) the repo config includes. Used by scenarios (issue #1046).
	Includes []string
	// Starred creates the session already starred, protecting it from an
	// accidental manual `gr delete`. Used by scenarios (issue #1046).
	Starred bool
}

// Create starts a new agent session, either in a git worktree, in-place
// in an existing repo, or as a standalone scratch session (when noRepo is true).
//
// The method uses three-phase locking to avoid holding the daemon mutex during
// potentially blocking git/network operations (fetch, GitHub API calls, PTY spawn):
//  1. Lock: validate, reserve session as StatusCreating, unlock
//  2. Git setup and PTY spawn (no lock held)
//  3. Lock: commit to StatusRunning, unlock
func (sm *SessionManager) Create(opts CreateOpts) (SessionState, error) {
	// Destructure into local names so the body below stays unchanged.
	name := opts.Name
	agentName := opts.AgentName
	repoPath := opts.RepoPath
	baseBranch := opts.BaseBranch
	prompt := opts.Prompt
	model := opts.Model
	parentID := opts.ParentID
	noRepo := opts.NoRepo
	mirror := opts.Mirror
	agentHooks := opts.AgentHooks
	inPlace := opts.InPlace
	allowConcurrent := opts.AllowConcurrent
	skipModelValidation := opts.SkipModelValidation
	yolo := opts.Yolo
	rows := opts.Rows
	cols := opts.Cols
	envExtra := opts.EnvExtra

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

	// Typed Codex options are codex-only; reject rather than silently drop them
	// against another agent (issue #1186).
	codexOpts := opts.Codex
	if !codexOpts.IsZero() && agentName != "codex" {
		return SessionState{}, fmt.Errorf("codex options require --agent codex (got %q)", agentName)
	}

	if err := config.ValidateCodexOptions(codexOpts); err != nil {
		return SessionState{}, err
	}

	// Early validation that doesn't require the lock.
	if inPlace && noRepo {
		return SessionState{}, fmt.Errorf("--in-place and --no-repo are mutually exclusive")
	}

	if inPlace && mirror != "" {
		return SessionState{}, fmt.Errorf("--in-place and --mirror are mutually exclusive")
	}

	if inPlace && baseBranch != "" {
		return SessionState{}, fmt.Errorf("--in-place and --base are mutually exclusive (in-place sessions don't create branches)")
	}

	// --- Pre-lock: resolve repo root and discover GitHub username ---
	// These can involve network calls (gh api) and must not hold the mutex.
	var preRepoRoot string

	if !noRepo && mirror == "" && repoPath != "" {
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

	// Validate a caller-supplied ID before taking the lock; uniqueness is
	// checked under the lock below, atomically with the reservation.
	if opts.ID != "" {
		if err := validateSessionID(opts.ID); err != nil {
			return SessionState{}, err
		}
	}

	// --- Phase 1: Lock, validate state, reserve session ---
	sm.mu.Lock()

	id := opts.ID
	if id == "" {
		id = sm.uniqueSessionIDLocked()
	} else if _, exists := sm.state.Sessions[id]; exists {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session id %q already in use", id)
	}

	token, err := generateToken()
	if err != nil {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("generate session token: %w", err)
	}

	var (
		repoRoot, repoName, worktreePath, branchName string
		isMirror                                     bool
		mirrorSourceID                               string
		fetchOnCreate                                bool
		rcIncludes                                   []string
		sourceIncludes                               []IncludedRepoState
	)

	switch {
	case mirror != "":
		var source *SessionState

		// Skip soft-deleted sessions: a hidden session must not be pickable as a
		// worktree source — its worktree is scheduled for purge.
		for _, s := range sm.state.Sessions {
			if s.IsSoftDeleted() {
				continue
			}

			if s.Name == mirror || s.ID == mirror {
				source = s
				break
			}
		}

		if source == nil {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("session %q not found for --mirror", mirror)
		}

		if source.WorktreePath == "" {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("session %q has no worktree to mirror", mirror)
		}

		worktreePath = source.WorktreePath
		repoRoot = source.RepoPath
		repoName = source.RepoName
		baseBranch = source.BaseBranch
		isMirror = true
		mirrorSourceID = source.ID
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

		if len(rc.Includes) > 0 || len(opts.Includes) > 0 {
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

		rcIncludes = mergeIncludes(rc.Includes, opts.Includes)

		// Per-session includes (e.g. from a scenario) don't go through
		// RepoConfig.Validate, so validate the merged set here for the same
		// collisions (self-include, duplicate basename, env-var clash) — otherwise
		// they'd surface as a confusing low-level git error mid worktree setup.
		if err := config.ValidateIncludes(repoRoot, rcIncludes); err != nil {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("invalid includes: %w", err)
		}

		if len(rcIncludes) > 0 {
			worktreePath = filepath.Join(sessionDir, repoName)
		} else {
			worktreePath = sessionDir
		}

		fetchOnCreate = sm.cfg.FetchOnCreate && !opts.NoFetch
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

	if isMirror && !sandboxed {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("--mirror requires sandbox to be enabled so the mirrored worktree can be mounted read-only; set sandbox.enabled = true in config and ensure safehouse is installed (gr doctor)")
	}

	// Yolo requires the PreToolUse approval hook to function, so it forces agent
	// hooks on regardless of the requested value — otherwise a Yolo session with
	// hooks disabled would auto-mark itself yolo but never install the hook that
	// routes tool calls to the auto backend.
	hooksEnabled := agentHooks || yolo

	// MCP config injection is now a mechanism distinct from lifecycle-hook
	// injection (injectMCPConfig vs injectHooks — see issue #1135). The policy
	// gates still coincide (mcpEnabled == hooksEnabled), so PTY behaviour is
	// unchanged and yolo still transitively governs MCP for now; the separate
	// variable is the seam a later headless phase widens to inject MCP without
	// generated hooks.
	mcpEnabled := hooksEnabled

	// Resolve MCP servers under the lock (reads config).
	var mcpServers []config.MCPServerConfig
	if mcpEnabled {
		mcpServers = sm.resolveMCPServers(agentName)
	}

	// Snapshot config values needed for Phase 2.
	cfgSnapshot := sm.cfg
	sandboxMerged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[agentName].Sandbox)

	// Reserve the session with StatusCreating so concurrent operations
	// (list, singleton checks) see it exists.
	placeholder := &SessionState{
		ID:              id,
		ParentID:        parentID,
		Name:            name,
		RepoPath:        repoRoot,
		RepoName:        repoName,
		WorktreePath:    worktreePath,
		Branch:          branchName,
		BaseBranch:      baseBranch,
		Agent:           agentName,
		AgentSessionID:  agentSessionID,
		Model:           model,
		Codex:           codexStatePtr(codexOpts),
		Mirror:          isMirror,
		MirrorSourceID:  mirrorSourceID,
		InPlace:         inPlace,
		AgentHooks:      hooksEnabled,
		Yolo:            yolo,
		TriggerID:       opts.TriggerID,
		TriggerReactor:  opts.TriggerReactor,
		TrackerIssue:    opts.TrackerIssue,
		AutoCleanup:     opts.AutoCleanup,
		IdleTimeoutSecs: opts.IdleTimeoutSecs,
		Status:          StatusCreating,
		CreatedAt:       time.Now().UTC(),
		StatusChangedAt: time.Now().UTC(),
		Token:           token,
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

		if isMirror || inPlace {
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
	if repoRoot != "" && !isMirror && !inPlace {
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

		// Post-create hook: rewrite absolute source-repo paths in known
		// orchestrator config files to the session's worktree paths, so an
		// includes orchestrator that hard-codes sibling paths (and can't use the
		// GRAITH_INCLUDE_*_PATH env vars) sees the session's code, not the main
		// checkout (#1033). Config files present in the worktrees are rewritten;
		// best-effort and never fatal.
		sm.applyIncludePathRewrites(repoRoot, worktreePath, includes)
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

	driverKind, err := resolveDriverKind(opts.Headless, agent, cfgSnapshot.Headless, sandboxed)
	if err != nil {
		cleanupOnError()
		rollbackState()

		return SessionState{}, err
	}

	// A headless session needs a prompt to run one-shot. An explicit --headless
	// without one is an error; a headless preference coming only from [headless]
	// default yields to PTY (matching resolveDriverKind's soft-preference rule).
	if driverKind == DriverHeadless && prompt == "" {
		if opts.Headless {
			cleanupOnError()
			rollbackState()

			return SessionState{}, fmt.Errorf("headless session requires a prompt (-p)")
		}

		driverKind = DriverPTY
	}

	// Conditional Codex flags (model + typed options) precede the positional
	// prompt so options come before it (issue #1186); no-op for other agents.
	expandedArgs = append(expandedArgs, codexExtraArgs(agentName, model, codexStatePtr(codexOpts))...)

	if driverKind == DriverHeadless {
		// The prompt is delivered as an initial stdin user message by the
		// headless driver (the control-channel launch takes no positional
		// prompt), so it is not appended to argv here.
		expandedArgs = headlessArgs(expandedArgs)
	} else if prompt != "" {
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

	if isMirror {
		for _, inc := range sourceIncludes {
			env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
		}
	}

	for _, extra := range envExtra {
		for k, v := range extra {
			env[k] = v
		}
	}

	// Headless sessions skip graith's generated status/approval hooks: the typed
	// stream is the status/approval feed.
	if hooksEnabled && driverKind != DriverHeadless {
		hookArgs, hookEnv, err := sm.injectHooks(agentName, id, worktreePath, yolo)
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

	// MCP config is injected by its own block, separate from the hook block above
	// (issue #1135). The gate (mcpEnabled) still tracks hooksEnabled for PTY, so
	// today this fires under the same condition as the hook block — the split is a
	// no-op for PTY. Widening mcpEnabled and dropping the headless guard (so a
	// hooks-disabled or headless session gets MCP) is a deliberate follow-up
	// (issue #1075).
	if mcpEnabled && driverKind != DriverHeadless {
		mcpArgs, err := sm.injectMCPConfig(agentName, id, mcpServers)
		if err != nil {
			cleanupOnError()
			rollbackState()

			return SessionState{}, fmt.Errorf("inject mcp config: %w", err)
		}

		expandedArgs = append(expandedArgs, mcpArgs...)
	}

	if agent.PromptInjectionEnabled() && driverKind != DriverHeadless {
		promptArgs, err := sm.injectPrompt(agentName, worktreePath)
		if err != nil {
			sm.log.Warn("failed to inject prompt", "session_id", id, "err", err)
		} else {
			expandedArgs = append(expandedArgs, promptArgs...)
		}
	}

	// Make each included repo's co-located worktree visible to the agent via
	// --add-dir. For a mirror session the includes live on the source session
	// (its own git setup is skipped), so use those. Appended last — after any
	// positional prompt — because Claude's --add-dir is variadic and would
	// otherwise swallow a following prompt argument as another directory.
	effectiveIncludes := includes
	if isMirror {
		effectiveIncludes = sourceIncludes
	}

	expandedArgs = append(expandedArgs, includeAddDirArgs(agentName, effectiveIncludes)...)

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

		opts := sm.sandboxOptsFromConfig(merged, id, worktreePath, agent.Command, envKeys, hooksEnabled || mcpEnabled)
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

		if isMirror {
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

	// Throttle concurrent launches so a burst doesn't stampede (#1092). The slot
	// is held across the agent's startup window and released on first output or a
	// settle timeout (releaseLaunchSlotWhenSettled), or immediately on spawn error.
	slot, err := sm.acquireLaunchSlot(context.Background(), id, name)
	if err != nil {
		cleanupOnError()

		if scratchDir != "" {
			_ = os.RemoveAll(scratchDir)
		}

		rollbackState()

		return SessionState{}, fmt.Errorf("acquire launch slot: %w", err)
	}

	// Record the pre-spawn time so native session-id capture only matches
	// transcript files this start creates (race-safe against stale rollouts).
	startedAt := time.Now()

	var ptySess SessionDriver

	if driverKind == DriverHeadless {
		ptySess, err = headless.New(headless.Opts{
			ID:           id,
			Command:      command,
			Args:         finalArgs,
			Dir:          worktreePath,
			Env:          env,
			LogPath:      logPath,
			MaxLogSize:   100 * 1024 * 1024,
			Prompt:       prompt,
			Control:      true,
			OnPermission: sm.headlessPermissionFunc(id),
		})
	} else {
		ptySess, err = grpty.NewSession(grpty.SessionOpts{
			ID:         id,
			Command:    command,
			Args:       finalArgs,
			Dir:        worktreePath,
			Env:        env,
			Rows:       rows,
			Cols:       cols,
			LogPath:    logPath,
			MaxLogSize: 100 * 1024 * 1024,
			Logger:     sm.log,
		})
	}

	if err != nil {
		slot.release()
		cleanupOnError()

		if scratchDir != "" {
			_ = os.RemoveAll(scratchDir)
		}

		rollbackState()

		return SessionState{}, fmt.Errorf("start %s session: %w", driverKind, err)
	}

	sm.releaseLaunchSlotWhenSettled(slot, id, name, ptySess)

	// --- Phase 3: Lock, commit to running ---
	sm.mu.Lock()

	// Check the session wasn't deleted while we were setting up.
	if _, ok := sm.state.Sessions[id]; !ok {
		sm.mu.Unlock()

		sm.logStopping(id, name, "rollback", "create-rollback", ptySess)
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
	sessState.DriverKind = driverKind
	sessState.Status = StatusRunning
	sessState.StatusChangedAt = time.Now()

	if opts.Starred {
		sessState.Starred = true
	}

	sessState.PID = ptySess.ProcessPID()
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

		sm.logStopping(id, name, "rollback", "create-rollback", ptySess)
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

	// Record the fresh-session spawn symmetrically with "resume: pty spawned"
	// (issue #1104): pid/pgid for OS-level signal forensics, sandboxed so a
	// later peak-RSS reading can be interpreted against the wrapper.
	sm.log.Info("session spawned",
		"id", id, "name", name, "agent", agentName,
		"pid", result.PID, "pgid", ptySess.Pgid(), "sandboxed", sandboxed,
		"scrollback_path", logPath)

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

// Fork creates a new session/worktree that natively continues the source
// agent's conversation (same agent type), using the agent's fork_args to carry
// over the history. It is a thin wrapper over ForkWithAgent with no override.
func (sm *SessionManager) Fork(name, sourceSessionID string, rows, cols uint16) (SessionState, error) {
	return sm.ForkWithAgent(name, sourceSessionID, "", "", rows, cols)
}

// ForkWithAgent forks a session into a new worktree. When targetAgent is empty
// or equal to the source's agent, this is a native same-agent fork (the source
// agent's conversation is resumed via fork_args). When targetAgent differs, it
// is a CROSS-AGENT fork: the source's on-disk conversation is rendered to a
// neutral Markdown file and the new agent is seeded with it (reusing the
// migration reader/renderer), while the source session keeps running.
//
// Git state: like any fork, the new worktree branches from the base branch, so
// the source's uncommitted edits are dropped. For a cross-agent fork the seed
// prompt says so explicitly (BuildForkSeedPrompt).
//
// Uses three-phase locking like Create to avoid holding the mutex during git
// fetch and PTY spawn.
//
// See docs/design/2026-06-24-cross-agent-conversation-migration-design.md
// ("Future: cross-agent fork").
func (sm *SessionManager) ForkWithAgent(name, sourceSessionID, targetAgent, targetModel string, rows, cols uint16) (SessionState, error) {
	if err := ValidateSessionName(name); err != nil {
		return SessionState{}, err
	}

	// --- Pre-lock: discover GitHub username ---
	sm.mu.RLock()
	cfgSnapshot := sm.cfg
	source, sourceOk := sm.state.Sessions[sourceSessionID]

	var (
		sourceRepoPath string
		srcAgentPre    string
	)

	if sourceOk {
		sourceRepoPath = source.RepoPath
		srcAgentPre = source.Agent
	}

	sm.mu.RUnlock()

	// Validate the target model outside the lock — validateModel may exec an
	// external validator (up to a 10s timeout), and holding sm.mu across it would
	// freeze the whole control plane (Create/Migrate validate pre-lock too). The
	// cheap agent/transcript checks are re-done under the lock below.
	crossAgentPre := targetAgent != "" && sourceOk && targetAgent != srcAgentPre
	if targetModel != "" && !crossAgentPre {
		return SessionState{}, fmt.Errorf("--model requires forking to a different agent (--agent); it is ignored for a same-agent fork")
	}

	if crossAgentPre && targetModel != "" {
		tgtCfg, ok := cfgSnapshot.Agents[targetAgent]
		if !ok {
			return SessionState{}, fmt.Errorf("unknown target agent %q", targetAgent)
		}

		if err := validateModel(tgtCfg, targetModel); err != nil {
			return SessionState{}, err
		}
	}

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

	srcAgent := source.Agent
	srcWorktree := source.WorktreePath

	// A cross-agent fork changes the agent type; empty or equal to the source is
	// a native same-agent fork.
	crossAgent := targetAgent != "" && targetAgent != srcAgent

	agentName := srcAgent
	if crossAgent {
		agentName = targetAgent
	}

	agent, ok := sm.cfg.Agents[agentName]
	if !ok {
		sm.mu.Unlock()

		if crossAgent {
			return SessionState{}, fmt.Errorf("unknown target agent %q", agentName)
		}

		return SessionState{}, fmt.Errorf("unknown agent %q", agentName)
	}

	if crossAgent {
		// The source's conversation must be readable to seed the new agent.
		// (targetModel was validated pre-lock to avoid exec under sm.mu.)
		if !transcript.Supported(srcAgent) {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("cannot fork session %q to agent %q: forking from %q is not supported (no transcript reader)", source.Name, targetAgent, srcAgent)
		}
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
	// A same-agent fork inherits the source model; a cross-agent fork uses the
	// requested target model (empty = the target agent's default).
	sourceModel := source.Model

	effectiveModel := sourceModel
	if crossAgent {
		effectiveModel = targetModel
	}

	// A fork replays the source's typed Codex options (issue #1186). A cross-agent
	// fork into codex from a non-codex source simply has none to inherit (nil).
	sourceCodex := cloneCodexOptions(source.Codex)

	sourceAgentSessionID := source.AgentSessionID
	sourceYolo := source.Yolo
	// Yolo forces agent hooks on (see Create) so a forked yolo session always
	// installs the approval hook, even if the source had hooks disabled.
	sourceAgentHooks := source.AgentHooks || sourceYolo
	// MCP config injection is decided separately from hooks (see #1135). Fork is
	// PTY-only, so the two coincide here.
	sourceMCPEnabled := sourceAgentHooks
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
	if sourceMCPEnabled {
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
		Model:           effectiveModel,
		Codex:           sourceCodex,
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

	// Cross-agent fork: rendered source conversation + its staging dir + the
	// seed prompt pointing the new agent at it. Empty for a same-agent fork.
	var (
		forkContextDir       string
		forkContextPath      string
		seedPrompt           string
		forkContextCommitted bool
	)

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

	// Guarantee the staged context dir is removed on ANY early return before the
	// Phase 3 commit — not every failure path calls forkCleanup (git-setup errors
	// call only rollbackState), and a leaked dir holds the full source
	// conversation. Disarmed only once the swap is persisted (forkContextCommitted).
	defer func() {
		if forkContextDir != "" && !forkContextCommitted {
			_ = os.RemoveAll(forkContextDir)
		}
	}()

	// --- Phase 1.5: render + stage the source transcript (cross-agent only) ---
	// Done before the (expensive) git worktree setup so a doomed cross-agent
	// fork — unsupported/missing/empty source transcript — fails fast. The
	// source session keeps running throughout; we only read its on-disk history.
	if crossAgent {
		conv, err := transcript.Read(srcAgent, sourceAgentSessionID, srcWorktree)
		if err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("read source transcript: %w", err)
		}

		rendered := conv.Render(transcript.RenderOptions{Kind: transcript.RenderFork})

		tmpDir, err := sm.repoTmpDir(repoRoot)
		if err != nil {
			rollbackState()
			return SessionState{}, err
		}

		// Staged in a per-session subdir under the repo tmp dir (already on the
		// new session's sandbox write-list, so the target can read it). NOTE: this
		// does NOT isolate the file from sibling sessions on the same repo — they
		// share GRAITH_TMPDIR and run as the same user, so 0o700/0o600 don't gate
		// them. The subdir is for tidy per-session cleanup, not confidentiality.
		// Same trade-off as Migrate's migrate-<id> dir; true isolation would need
		// a per-session grant outside the shared root (tracked separately).
		forkContextDir = filepath.Join(tmpDir, "fork-"+id)
		if err := os.MkdirAll(forkContextDir, 0o700); err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("create fork context dir: %w", err)
		}

		forkContextPath = filepath.Join(forkContextDir, "context.md")
		if err := writeFileAtomic(forkContextPath, []byte(rendered)); err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("write fork context: %w", err)
		}

		seedPrompt = transcript.BuildForkSeedPrompt(srcAgent, forkContextPath)
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

		// Same post-create hook as Create: a forked includes session gets fresh
		// worktrees, so its config files still hold the source paths and must be
		// rewritten too (#1033).
		sm.applyIncludePathRewrites(repoRoot, worktreePath, forkIncludes)
	} else {
		if err := git.SetupSession(gitCtx, repoRoot, worktreePath, branchName, baseBranch, fetchOnCreate); err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("setup git session: %w", err)
		}
	}

	// For a cross-agent fork the source's native id belongs to a different agent
	// and is meaningless to the target, so it never templates into the target's
	// args; the model is the requested target model.
	forkSourceID := sourceAgentSessionID
	if crossAgent {
		forkSourceID = ""
	}

	vars := config.TemplateVars{
		Username:                 preUsername,
		AgentSessionID:           agentSessionID,
		SessionName:              name,
		SessionID:                id,
		WorktreePath:             worktreePath,
		ForkSourceAgentSessionID: forkSourceID,
		Model:                    effectiveModel,
	}

	var args []string
	if crossAgent {
		// A cross-agent fork cannot natively resume the source's conversation, so
		// start the target fresh (agent.Args, not fork_args) and seed it with the
		// rendered history via seedPrompt below.
		args = agent.Args
	} else {
		args = agent.ForkArgs
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
	}

	expandedArgs, err := config.ExpandSlice(args, vars)
	if err != nil {
		forkCleanup()
		rollbackState()

		return SessionState{}, fmt.Errorf("expand fork args: %w", err)
	}

	// Replay the conditional Codex flags after the fork/args (issue #1186); no-op
	// for other agents. Codex accepts these on its `fork` subcommand too.
	expandedArgs = append(expandedArgs, codexExtraArgs(agentName, effectiveModel, sourceCodex)...)

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
		hookArgs, hookEnv, err := sm.injectHooks(agentName, id, worktreePath, sourceYolo)
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

	if sourceMCPEnabled {
		mcpArgs, err := sm.injectMCPConfig(agentName, id, mcpServers)
		if err != nil {
			forkCleanup()
			rollbackState()

			return SessionState{}, fmt.Errorf("inject mcp config: %w", err)
		}

		expandedArgs = append(expandedArgs, mcpArgs...)
	}

	if agent.PromptInjectionEnabled() {
		promptArgs, err := sm.injectPrompt(agentName, worktreePath)
		if err != nil {
			sm.log.Warn("failed to inject prompt", "session_id", id, "err", err)
		} else {
			expandedArgs = append(expandedArgs, promptArgs...)
		}
	}

	// Cross-agent fork seed: the rendered-history pointer is delivered as the new
	// agent's opening positional prompt (like `gr new --prompt`). Appended after
	// any injected prompt but before includeAddDirArgs, because Claude's variadic
	// --add-dir would otherwise swallow it as another directory (see Create).
	if seedPrompt != "" {
		expandedArgs = append(expandedArgs, seedPrompt)
	}

	// Make each included repo's forked worktree visible to the agent via
	// --add-dir (a fork re-creates the source's includes as forkIncludes).
	// Appended last, after any injected prompt (see Create for why).
	expandedArgs = append(expandedArgs, includeAddDirArgs(agentName, forkIncludes)...)

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

		opts := sm.sandboxOptsFromConfig(merged, id, worktreePath, agent.Command, envKeys, sourceAgentHooks || sourceMCPEnabled)
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

	// Throttle concurrent launches (#1092); see Create for the slot lifecycle.
	slot, err := sm.acquireLaunchSlot(context.Background(), id, name)
	if err != nil {
		forkCleanup()
		rollbackState()

		return SessionState{}, fmt.Errorf("acquire launch slot: %w", err)
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
		Logger:     sm.log,
	})
	if err != nil {
		slot.release()
		forkCleanup()
		rollbackState()

		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	sm.releaseLaunchSlotWhenSettled(slot, id, name, ptySess)

	// --- Phase 3: Lock, commit ---
	sm.mu.Lock()

	if _, ok := sm.state.Sessions[id]; !ok {
		sm.mu.Unlock()

		sm.logStopping(id, name, "rollback", "fork-rollback", ptySess)
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

	// Record cross-agent provenance (surfaced via SessionInfo.MigratedFrom) and,
	// crucially, the RenderedPath so the staged context file is cleaned up on
	// delete (removeMigrationContext keys off it). MigrationInfo is shared with
	// Migrate; a fork is distinguished by having a live ParentID.
	if crossAgent {
		sessState.MigratedFrom = &MigrationInfo{
			Agent:          srcAgent,
			Model:          sourceModel,
			AgentSessionID: sourceAgentSessionID,
			RenderedPath:   forkContextPath,
			At:             time.Now().UTC(),
		}
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

		sm.logStopping(id, name, "rollback", "fork-rollback", ptySess)
		_ = ptySess.Kill()
		ptySess.Close()
		forkCleanup()

		_ = os.Remove(logPath)

		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	// The swap is persisted: the staged context is now owned by the session
	// (cleaned up on delete), so disarm the early-return cleanup guard.
	forkContextCommitted = true

	result := cloneSessionState(sessState)
	sm.mu.Unlock()

	// Symmetric with Create/resume spawn logging (issue #1104).
	sm.log.Info("session spawned",
		"id", id, "name", name, "agent", agentName, "forked", true,
		"pid", result.PID, "pgid", ptySess.Pgid(), "sandboxed", sandboxed,
		"scrollback_path", logPath)

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
func (sm *SessionManager) startWatcher(id string, sess SessionDriver) {
	sm.watchers.Add(1)
	go func() {
		defer sm.watchers.Done()

		sm.watchSession(id, sess)
	}()
}

// watchSession waits for a PTY session to exit and updates state accordingly.
// If the session has been replaced (e.g. by Resume) or removed (e.g. by Delete),
// the watcher is stale and skips the state update and status event.
func (sm *SessionManager) watchSession(id string, sess SessionDriver) {
	<-sess.Done()

	sm.mu.Lock()
	stale := sm.sessions[id] != sess

	var (
		name           string
		deleted        bool
		isOrchestrator bool
		stopReason     string
		sandboxed      bool
	)

	if !stale {
		if s, ok := sm.state.Sessions[id]; ok {
			name = s.Name
			isOrchestrator = s.SystemKind == SystemKindOrchestrator

			prevSummary, prevSetAt := sm.prevStopSummaryLocked(s, id)

			prevTTL := sm.cfg.Status.TTLDuration()
			if s.SummaryTTL > 0 {
				prevTTL = time.Duration(s.SummaryTTL) * time.Second
			}

			// A watchdog give-up (StopReasonWatchdog) has already set StatusErrored
			// and a descriptive summary before killing the PTY; preserve both so
			// budget exhaustion stays distinguishable from an ordinary stop/crash.
			watchdogGaveUp := s.StopReason == StopReasonWatchdog

			exitCode := sess.ExitCode()

			if !watchdogGaveUp {
				s.Status = StatusStopped
			}

			s.StatusChangedAt = time.Now()
			s.ExitCode = &exitCode
			s.PID = 0

			// Capture the exiting process generation before zeroing it so a
			// pending SessionEnd reason is consumed only for the same process.
			exitingGen := s.PIDStartTime
			s.PIDStartTime = 0

			if s.StopReason == "" {
				// Precedence: an already-set StopReason (e.g. an explicit gr stop)
				// has already skipped this block. Otherwise a process-ending
				// SessionEnd reason bound to THIS generation maps to a StopReason;
				// otherwise fall back to crash (a hard crash never emits SessionEnd,
				// and clear/resume/other leave no clean-exit reason).
				if mapped, ok := mapSessionEndReason(s.SessionEndReason); ok && s.SessionEndReasonGen == exitingGen {
					s.StopReason = mapped
				} else {
					s.StopReason = StopReasonCrash
				}
			}

			if lastOut := sess.LastOutputAt(); !lastOut.IsZero() {
				s.LastOutputAt = &lastOut
			}

			if sig := sess.ExitSignal(); sig != 0 && s.StopReason == StopReasonCrash {
				s.ExitSignal = sig.String()
			}

			// Capture the finalized reason + sandbox flag under the lock so the
			// "session exited" log below can attribute the stop and label the
			// peak-RSS source without racing a concurrent resume (issue #1104).
			stopReason = s.StopReason
			sandboxed = s.Sandboxed

			if !watchdogGaveUp && (s.StopReason != StopReasonShutdown || s.SummaryText == "") {
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

	if stopReason == "" {
		stopReason = StopReasonCrash
	}

	logAttrs := []any{
		"id", id,
		"name", name,
		"stop_reason", stopReason,
		"exit_code", sess.ExitCode(),
		"pid", sess.ProcessPID(),
		"pgid", sess.Pgid(),
	}
	if sig := sess.ExitSignal(); sig != 0 {
		logAttrs = append(logAttrs, "signal", sig.String())
	}

	if rss := sess.PeakRSSBytes(); rss > 0 && sess.ExitCode() != 0 {
		// peak_rss_mb comes from the waited child's rusage, which is the direct
		// child graith spawned — the sandbox wrapper when sandboxed, not the
		// agent underneath it. Labelling the source (issue #1104) stops a
		// single-digit wrapper RSS from being read as the agent's footprint.
		logAttrs = append(logAttrs, "peak_rss_mb", rss/(1024*1024), "peak_rss_proc", peakRSSProcLabel(sandboxed))
	}

	sm.log.Info("session exited", logAttrs...)

	sm.onAgentStatusChange(id, name, "running", "stopped")

	if isOrchestrator {
		sm.notifyOrchestratorExit(id)
	}

	// A trigger-spawned session with auto_cleanup configured is soft-deleted now
	// that it has stopped, so finished briefing/report sessions don't accumulate.
	sm.autoCleanupStopped(id)
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

// forcedIDConversationExists reports whether a forced-id agent (Claude) has a
// non-empty on-disk transcript for the given session id. Resuming such an agent
// uses `--resume <id>`, which fails hard ("No conversation found with session
// ID") when no conversation was ever persisted for that id — e.g. the first
// launch was killed before (or mid-) writing the transcript. A zero-byte
// transcript is that same wedge (the file was created but nothing written), so
// it counts as "no conversation". Any locate error (including no match) is
// likewise treated as "no conversation".
//
// Note: the locator resolves Claude's config root from the daemon's own
// environment (CLAUDE_CONFIG_DIR / ~/.claude), matching the token-accounting
// loop (pollTokens). A per-agent `[agents.claude.env] CLAUDE_CONFIG_DIR`
// override is not threaded through here yet — that's a shared limitation of the
// transcript subsystem, tracked separately.
func forcedIDConversationExists(agent, agentSessionID, worktreePath string) bool {
	sources, err := transcript.Locate(agent, agentSessionID, worktreePath)
	if err != nil {
		return false
	}

	for _, s := range sources {
		if s.Size > 0 {
			return true
		}
	}

	return false
}

// resolveForcedIDResume decides how to resume a forced-id agent (Claude) based
// solely on whether a conversation exists on disk for its session id — the
// stored freshStart flag is deliberately NOT trusted as the primary signal:
//
//   - conversation exists → native --resume (fresh=false), even if a stale
//     freshStart=true was left behind by a fresh start that crashed between
//     persisting the minted id and clearing the flag. Safe because a forced-id
//     freshStart id is always freshly minted, so a transcript under it means the
//     fresh start already ran far enough to write one.
//   - no conversation, not already fresh → the #1091 wedge: mint a new id and
//     start fresh so `claude --resume <id>` can't fail with "No conversation
//     found". Reported via fellBack so the caller clears the one-shot flag once
//     the start succeeds.
//   - no conversation, already fresh (migration seed / a prior pending fallback)
//     → keep the minted id and the pending fresh start unchanged.
//
// Non-forced agents and empty ids are returned unchanged.
func resolveForcedIDResume(agent, agentSessionID, worktreePath string, freshStart bool, mint func() string) (id string, fresh, fellBack bool) {
	if !forcesID(agent) || agentSessionID == "" {
		return agentSessionID, freshStart, false
	}

	if forcedIDConversationExists(agent, agentSessionID, worktreePath) {
		return agentSessionID, false, false
	}

	if freshStart {
		return agentSessionID, true, false
	}

	return mint(), true, true
}

// convertSettleTimeout bounds how long ConvertToInteractive waits for an
// interrupted headless process to settle and exit before escalating to SIGTERM.
// A one-shot headless agent normally winds down its current turn and exits
// promptly on interrupt; the escalation guards against one that ignores it (a
// wedged tool call, a signal-swallowing wrapper). Vars (not consts) so tests can
// shrink them.
var (
	convertSettleTimeout = 5 * time.Second
	// convertKillTimeout bounds the SIGTERM step before the final SIGKILL.
	convertKillTimeout = 3 * time.Second
	// convertForceKillTimeout bounds the final wait after SIGKILL so a process
	// whose Done() never closes can't stall the convert forever.
	convertForceKillTimeout = 3 * time.Second
)

// ConvertToInteractive turns a headless (one-shot stream-json) session into an
// interactive PTY session, preserving its conversation (Claude reloads it from
// the transcript on `--resume`), worktree, branch, graith session id, and env.
// This backs `gr attach` on a headless session (headless phase 5, issue #1137):
// a headless session has no ptmx to stream, so attach converts instead.
//
// The swap is transactional. Under the manager lock it validates the target and
// marks it StatusCreating (a busy guard that locks out a concurrent
// attach/stop/resume/convert) and removes the live driver from the sessions map
// so its exit watcher goes stale and can't race the relaunch. It then stops the
// headless process outside the lock (interrupt → settle → SIGTERM → SIGKILL
// fallback), flips the persisted DriverKind to "pty", and relaunches through the
// ordinary resume path — which already knows how to spawn `claude --resume
// <agent_session_id>` in a real PTY. If the relaunch fails, the session is left
// stopped-and-interactive (resumable), not wedged.
//
// A headless session that has already exited (StatusStopped/Errored) is
// converted the same way, minus the process stop: DriverKind flips and the
// resume path relaunches it interactively. Converting an already-interactive
// session is a no-op that returns the current state.
func (sm *SessionManager) ConvertToInteractive(id string, rows, cols uint16) (SessionState, error) {
	sm.mu.Lock()

	sessState, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q not found", id)
	}

	// A soft-deleted session is hidden; ID-addressable lifecycle ops reject it.
	// Checked before the idempotent early return so a raw attach_convert against
	// a soft-deleted (even already-interactive) session is refused, not "converted".
	if sessState.IsSoftDeleted() {
		sm.mu.Unlock()
		return SessionState{}, errSoftDeleted(sessState.Name)
	}

	// Already interactive — nothing to convert. Idempotent so a racing second
	// convert (or a client that retries) sees success, not an error.
	if sessState.DriverKind != DriverHeadless {
		result := cloneSessionState(sessState)
		sm.mu.Unlock()

		return result, nil
	}

	if sessState.Status == StatusDeleting || sessState.Status == StatusCreating {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is busy (%s); cannot convert", id, sessState.Status)
	}

	name := sessState.Name
	driver := sm.sessions[id]
	running := sessState.Status == StatusRunning && driver != nil

	// Snapshot for rollback if the guard save can't be committed.
	prevStatus := sessState.Status
	prevStatusChangedAt := sessState.StatusChangedAt

	// Mark busy under the lock. A live headless driver is removed from the map so
	// its exit watcher (which fires when the process we're about to stop exits)
	// sees sm.sessions[id] != sess and skips the state update — the convert owns
	// the transition, not the watcher.
	sessState.Status = StatusCreating
	sessState.StatusChangedAt = time.Now()

	if running {
		delete(sm.sessions, id)
	}

	if err := sm.saveState(); err != nil {
		sessState.Status = prevStatus
		sessState.StatusChangedAt = prevStatusChangedAt

		if running {
			sm.sessions[id] = driver
		}

		sm.mu.Unlock()

		return SessionState{}, fmt.Errorf("persist convert guard: %w", err)
	}

	sm.mu.Unlock()

	// Stop the headless process outside the lock: interrupt first (a control-
	// protocol `interrupt` request when the channel is live, falling back to
	// SIGINT — see headless.Session.Interrupt) so a mid-turn tool call is
	// cancelled cleanly, then settle, escalating to SIGTERM/SIGKILL if it refuses
	// to exit. We do NOT call driver.Close() here:
	// the launch-time exit watcher (startWatcher) closes the driver's handles
	// when it exits (watchSession always Close()s, staling out on the map
	// removal above), so closing here too would double-close and — worse — block
	// the convert if a SIGKILL-surviving grandchild keeps an inherited pipe open
	// (Close waits on the stdout/stderr drains). Leaving Close to the watcher
	// keeps ConvertToInteractive bounded even in that pathological case.
	if running {
		sm.logStopping(id, name, StopReasonConvert, "convert", driver)
		sm.stopDriverForConvert(driver)
	}

	// Commit the driver flip: the session is now a stopped PTY session the resume
	// path can relaunch. Persist before the relaunch so a crash mid-convert leaves
	// a resumable interactive session, not a headless one the attach guard blocks.
	sm.mu.Lock()

	sessState, ok = sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q was deleted during convert", id)
	}

	sessState.DriverKind = DriverPTY
	sessState.Status = StatusStopped
	sessState.StatusChangedAt = time.Now()
	sessState.StopReason = StopReasonConvert

	if err := sm.saveState(); err != nil {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("persist driver flip: %w", err)
	}

	sm.mu.Unlock()

	// Relaunch interactively via the existing resume path (spawns a real PTY with
	// resume_args → `claude --resume <agent_session_id>`).
	return sm.resumeWithSummary(id, rows, cols, "Converted from headless to interactive")
}

// stopDriverForConvert stops a live headless driver for conversion: an interrupt
// (control-protocol `interrupt` request, or SIGINT fallback) to cancel the
// current turn, then escalate to SIGTERM and finally SIGKILL if the process
// ignores gentler stops. Every wait — including the final one after SIGKILL — is
// bounded, so a process whose Done() never closes (e.g. a group-escaping
// grandchild that keeps an inherited pipe open past a group SIGKILL) can't stall
// the convert forever. The exit watcher still reaps and closes the driver if it
// ever exits.
func (sm *SessionManager) stopDriverForConvert(driver SessionDriver) {
	_ = driver.Interrupt(1, 0)

	if waitDriverDone(driver, convertSettleTimeout) {
		return
	}

	_ = driver.Kill()

	if waitDriverDone(driver, convertKillTimeout) {
		return
	}

	_ = driver.ForceKill()
	_ = waitDriverDone(driver, convertForceKillTimeout)
}

// waitDriverDone reports whether driver.Done() closed within d.
func waitDriverDone(driver SessionDriver, d time.Duration) bool {
	select {
	case <-driver.Done():
		return true
	case <-time.After(d):
		return false
	}
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

	// A headless session is one-shot (issue #1075): it ran its prompt to a
	// terminal result and exited. The resume path only knows how to relaunch an
	// interactive PTY, which would silently change the transport while leaving
	// DriverKind=headless (and the attach guard would then lock it out). Refuse
	// rather than convert; convert-to-interactive on attach is a planned phase.
	if sessState.DriverKind == DriverHeadless {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is headless (one-shot) and cannot be resumed; create a new session", id)
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

	if sessState.Mirror && !sandboxed {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("mirror session %q requires sandbox but sandbox is not enabled in current config; enable sandbox to resume", id)
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
	// resolve MCP servers for a yolo session even if hooks were disabled. MCP is a
	// decision distinct from hooks (see #1135); resume is PTY-only, so they
	// coincide here.
	var mcpServers []config.MCPServerConfig
	if sessState.AgentHooks || sessState.Yolo {
		mcpServers = sm.resolveMCPServers(sessState.Agent)
	}

	// Snapshot mirror source includes under lock.
	var sharedSourceIncludes []IncludedRepoState

	if sessState.Mirror {
		if source, ok := sm.state.Sessions[sessState.MirrorSourceID]; ok {
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
	prevToken := sessState.Token
	prevAgentSessionID := sessState.AgentSessionID
	prevFreshStart := sessState.FreshStart

	// For a forced-id agent (Claude), the resume command is decided by whether a
	// conversation exists on disk for the captured id (see resolveForcedIDResume):
	// a missing/empty transcript would make `--resume <id>` fail permanently, so
	// mint a fresh id + fresh start instead; and a stale FreshStart left by an
	// interrupted fresh start is corrected back to native --resume once its
	// transcript exists. Applied to the shared *sessState so the StatusCreating
	// save below persists it before the PTY spawn.
	forcedFreshFallback := false

	if forcesID(sessState.Agent) && sessState.AgentSessionID != "" {
		oldID := sessState.AgentSessionID
		oldFresh := sessState.FreshStart
		newID, fresh, fellBack := resolveForcedIDResume(sessState.Agent, oldID, sessState.WorktreePath, oldFresh, newAgentSessionID)

		// For a forced-id agent the helper returns fresh=false only via the
		// conversation-exists branch, so !fresh is exactly "conversation exists".
		sm.log.Info("resume transcript lookup",
			"session_id", id, "agent", sessState.Agent,
			"agent_session_id", oldID, "conversation_exists", !fresh)

		sessState.AgentSessionID = newID
		sessState.FreshStart = fresh
		forcedFreshFallback = fellBack

		switch {
		case fellBack:
			sm.log.Info("resume decision: fresh-start (no conversation found for forced-id session)",
				"session_id", id, "agent", sessState.Agent,
				"old_agent_session_id", oldID, "new_agent_session_id", newID)
		case oldFresh && !fresh:
			sm.log.Info("resume decision: native --resume (conversation exists; cleared stale fresh-start left by an interrupted start)",
				"session_id", id, "agent", sessState.Agent, "agent_session_id", newID)
		}
	}

	newToken, err := generateToken()
	if err != nil {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("generate session token: %w", err)
	}

	if prevToken != "" {
		delete(sm.tokenIndex, prevToken)
	}

	sessState.Token = newToken
	sm.tokenIndex[newToken] = id

	// Mark as creating so concurrent operations see it's busy.
	prevStatusChangedAt := sessState.StatusChangedAt
	sessState.Status = StatusCreating

	sessState.StatusChangedAt = time.Now().UTC()
	if err := sm.saveState(); err != nil {
		sessState.Status = prevStatus
		sessState.StatusChangedAt = prevStatusChangedAt
		sessState.Token = prevToken

		// This save is the first to commit a forced-id fresh fallback (minted id +
		// FreshStart), so undo it here too — otherwise memory holds the minted id
		// while disk still has the original, and a later restart re-wedges.
		sessState.AgentSessionID = prevAgentSessionID
		sessState.FreshStart = prevFreshStart

		delete(sm.tokenIndex, newToken)

		if prevToken != "" {
			sm.tokenIndex[prevToken] = id
		}
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
	sessCodex := cloneCodexOptions(sessState.Codex)
	sessYolo := sessState.Yolo
	// Yolo forces agent hooks on (see Create) so a resumed yolo session always
	// re-installs the approval hook, even if hooks were disabled at create.
	sessAgentHooks := sessState.AgentHooks || sessYolo
	// MCP config injection is decided separately from hooks (see #1135). Resume is
	// PTY-only, so the two coincide here.
	sessMCPEnabled := sessAgentHooks
	sessIncludes := make([]IncludedRepoState, len(sessState.Includes))
	copy(sessIncludes, sessState.Includes)
	sessInPlace := sessState.InPlace
	sessMirror := sessState.Mirror
	sessSystemKind := sessState.SystemKind
	sessFreshStart := sessState.FreshStart
	sessToken := newToken
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
			s.Token = prevToken

			// Undo the forced-id fresh fallback: the StatusCreating save already
			// committed the minted id + FreshStart, so restore the originals to keep
			// rollback meaning "this attempt changed nothing".
			s.AgentSessionID = prevAgentSessionID
			s.FreshStart = prevFreshStart

			delete(sm.tokenIndex, newToken)

			if prevToken != "" {
				sm.tokenIndex[prevToken] = id
			}

			// Re-persist so disk matches the rolled-back token (the first save may
			// have committed the rotated one). Log rather than swallow: a failure
			// here means durable state still holds a credential no process knows.
			if err := sm.saveState(); err != nil {
				sm.log.Warn("failed to persist token rollback on resume failure", "session_id", id, "err", err)
			}
		}
		sm.mu.Unlock()
	}

	resumeArgs, resumeNote := resolveResumeArgs(agent, sessAgent, sessAgentSessionID, sessFreshStart)
	if resumeNote != "" {
		sm.log.Info(resumeNote, "session_id", id, "agent", sessAgent)
	}

	sm.log.Info("resume decision",
		"session_id", id, "agent", sessAgent,
		"agent_session_id", sessAgentSessionID, "fresh_start", sessFreshStart,
		"forced_id_fresh_fallback", forcedFreshFallback, "args", resumeArgs)

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

	// Replay the conditional Codex flags after the resume subcommand/args
	// (issue #1186) so a resumed session keeps its model and typed options; no-op
	// for other agents.
	expandedArgs = append(expandedArgs, codexExtraArgs(sessAgent, sessModel, sessCodex)...)

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

	// A mirror session persists no includes of its own (its git setup is
	// skipped); its siblings live on the source session, snapshotted above as
	// sharedSourceIncludes. Use those so a mirror keeps sibling visibility across
	// a restart, matching how Create seeds a mirror from sourceIncludes.
	resumeIncludes := resumeIncludeSet(sessMirror, sessIncludes, sharedSourceIncludes)

	for _, inc := range resumeIncludes {
		if !git.IsInsideGitRepo(inc.WorktreePath) {
			rollbackState()
			return SessionState{}, fmt.Errorf("included worktree %q (%s) is no longer a valid git repo — delete and recreate the session", inc.WorktreePath, inc.RepoName)
		}

		env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
	}

	if sessAgentHooks {
		hookArgs, hookEnv, err := sm.injectHooks(sessAgent, id, sessWorktreePath, sessYolo)
		if err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("inject agent hooks: %w", err)
		}

		expandedArgs = append(expandedArgs, hookArgs...)

		for k, v := range hookEnv {
			env[k] = v
		}
	}

	if sessMCPEnabled {
		mcpArgs, err := sm.injectMCPConfig(sessAgent, id, mcpServers)
		if err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("inject mcp config: %w", err)
		}

		expandedArgs = append(expandedArgs, mcpArgs...)
	}

	if isOrchestrator {
		sm.mu.RLock()
		orchCfg := sm.cfg.Orchestrator
		repoPaths := sm.cfg.AvailableRepoPaths()
		notifyEnabled := sm.cfg.Notifications.Enabled
		sm.mu.RUnlock()
		promptArgs := sm.buildOrchestratorPrompt(sessAgent, orchCfg, repoPaths, notifyEnabled)
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

	// Re-add each included worktree via --add-dir so it persists across restarts
	// (resume_args don't carry it). Appended after every prompt — including the
	// migration seed — so Claude's variadic --add-dir can't swallow the prompt.
	expandedArgs = append(expandedArgs, includeAddDirArgs(sessAgent, resumeIncludes)...)

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

		opts := sm.sandboxOptsFromConfig(merged, id, ptyCWD, agent.Command, envKeys, sessAgentHooks || sessMCPEnabled)
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

		if sessMirror {
			scratchDir := filepath.Join(sm.paths.DataDir, "scratch", id)
			if err := os.MkdirAll(scratchDir, 0o700); err != nil {
				rollbackState()
				return SessionState{}, fmt.Errorf("create scratch dir for mirror resume: %w", err)
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

	// Throttle concurrent launches (#1092); see Create for the slot lifecycle.
	slot, err := sm.acquireLaunchSlot(context.Background(), id, sessName)
	if err != nil {
		rollbackState()
		return SessionState{}, fmt.Errorf("acquire launch slot: %w", err)
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
		Logger:     sm.log,
	})
	if err != nil {
		slot.release()
		rollbackState()

		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	sm.releaseLaunchSlotWhenSettled(slot, id, sessName, ptySess)

	// --- Phase 3: Lock, commit to running ---
	sm.mu.Lock()

	if _, ok := sm.state.Sessions[id]; !ok {
		sm.mu.Unlock()

		sm.logStopping(id, sm.sessionName(id), "rollback", "resume-rollback", ptySess)
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
	// Runtime signals belong to the previous agent generation; a resume/restart
	// starts clean and hooks re-establish the picture as they fire (issue #1073):
	// context-pressure + sub-agents, plus any pending SessionEnd reason (and its
	// generation binding) and the captured final message, so none of them leak
	// across into the new process's lifecycle.
	sessState.ContextPressure = false
	sessState.ContextPressureAt = time.Time{}
	sessState.SubAgents = nil
	sessState.SessionEndReason = ""
	sessState.SessionEndReasonGen = 0
	sessState.LastMessage = ""
	sessState.Sandboxed = sandboxed
	sessState.SandboxConfig = mergedSandbox

	sessState.CreationCfg = &CreationConfig{
		Agent:         agent,
		SandboxConfig: sandboxMerged,
	}
	if isOrchestrator {
		sessState.LastStartedAt = time.Now()
	}
	// A forced-id fresh fallback minted a new id and started clean; the
	// conversation now exists under that id, so clear the one-shot FreshStart so
	// the next resume uses native --resume.
	if forcedFreshFallback {
		sessState.FreshStart = false
	}

	// Clear FreshStart unconditionally once a start has consumed it. resolveResumeArgs
	// (above) already read sessFreshStart to pick the args for THIS start; leaving the
	// flag set would silently route every FUTURE user resume around --resume too,
	// discarding conversation history. Callers that need another fresh start (the
	// orchestrator supervisor, migration, and the startup watchdog) re-set FreshStart
	// on each attempt, so consecutive recoveries still start fresh — only stray later
	// resumes are corrected. This generalises the previous orchestrator/seed-only clears.
	sessState.FreshStart = false

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

		// Roll back the rotated token too, otherwise the session keeps a
		// credential no surviving process knows (the new PTY is killed below).
		sessState.Token = prevToken

		// Roll back the forced-id fresh fallback (minted id + FreshStart).
		sessState.AgentSessionID = prevAgentSessionID
		sessState.FreshStart = prevFreshStart

		delete(sm.tokenIndex, newToken)

		if prevToken != "" {
			sm.tokenIndex[prevToken] = id
		}

		delete(sm.sessions, id)

		// The first save already durably committed the rotated token, so restoring
		// only in-memory would leave disk holding a credential no process knows.
		// Best-effort re-persist the rolled-back state; if it also fails, startup
		// reconciliation recovers from the divergence.
		if saveErr := sm.saveState(); saveErr != nil {
			sm.log.Warn("failed to persist rollback after resume save failure", "session_id", id, "err", saveErr)
		}

		sm.mu.Unlock()

		sm.logStopping(id, sm.sessionName(id), "rollback", "resume-rollback", ptySess)
		_ = ptySess.Kill()
		ptySess.Close()

		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	delete(sm.hookReports, id)
	delete(sm.silentWarned, id)

	scenarioIDForRepublish := sessState.ScenarioID
	result := cloneSessionState(sessState)
	sm.mu.Unlock()

	// Record the resume spawn with the scrollback wiring so the restart pipeline
	// is traceable end to end (issue #1087): which PID now owns the session and
	// which log the fresh PTY appends to. Paired with the pty package's
	// "pty first output" / "no output" logs, this makes a blank-screen restart
	// diagnosable from the daemon log alone.
	sm.log.Info("resume: pty spawned",
		"session_id", id, "summary", lifecycleSummary, "agent", sessAgent,
		"pid", result.PID, "pgid", ptySess.Pgid(), "sandboxed", sandboxed,
		"scrollback_path", logPath)

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
			sm.logStopping(id, sm.sessionName(id), StopReasonDelete, "soft-delete", ptySess)

			_ = ptySess.Kill()
			select {
			case <-ptySess.Done():
			case <-time.After(5 * time.Second):
				_ = ptySess.ForceKill()
			}
		}

		ptySess.Close()
	} else if orphanPID > 0 {
		sm.logStoppingPID(id, sm.sessionName(id), StopReasonDelete, "soft-delete-orphan", orphanPID, orphanPID)

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

	name := sessState.Name
	repoPath := sessState.RepoPath
	worktreePath := sessState.WorktreePath
	branch := sessState.Branch
	shared := sessState.Mirror
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
	// The PID stays in state until the tombstone (which carries it) is durably
	// written, so a crash before the tombstone lands still leaves a reap-able
	// PID rather than a silently-orphaned process.
	_ = sm.saveState()
	sm.mu.Unlock()

	// Write a tombstone before any teardown so a crash mid-delete is resumed on
	// next startup. This must be durable BEFORE the destructive teardown runs:
	// if it can't be written, fail closed — revert the session and abort rather
	// than tear down a worktree with no recovery marker.
	spec := teardownSpec{
		ID:           id,
		RepoPath:     repoPath,
		WorktreePath: worktreePath,
		Branch:       branch,
		Shared:       shared,
		InPlace:      inPlace,
		SystemKind:   sessSystemKind,
		Includes:     sessionIncludes,
	}
	if err := sm.writeTombstone(tombstone{
		teardownSpec: spec,
		Name:         name,
		PID:          orphanPID,
		PIDStartTime: orphanStartTime,
		CreatedAt:    time.Now(),
	}); err != nil {
		sm.log.Error("failed to write delete tombstone; aborting delete", "id", id, "err", err)
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.Status = prevStatus
			s.StatusChangedAt = time.Now()

			// Restore manager ownership removed above: nothing has been killed or
			// torn down on this path, so the session must return fully intact
			// (still tracked, process still reachable) rather than a live agent
			// orphaned from sm.sessions and shown as stopped.
			if hasPTY {
				sm.sessions[id] = ptySess
			}

			if hasClient {
				sm.attachedClients[id] = ac
			}

			_ = sm.saveState()
		}
		sm.mu.Unlock()

		// writeTombstone can fail after the temp file was renamed into place (a
		// dir-fsync error), leaving a marker on disk. Remove it so a later startup
		// doesn't resume a delete we just reported as aborted.
		sm.removeTombstone(id)

		return fmt.Errorf("delete aborted: could not write recovery tombstone: %w", err)
	}

	// Tombstone is durable; the PID it carries lets resume reap the process, so
	// drop the PID from live state now.
	sm.mu.Lock()
	if s, ok := sm.state.Sessions[id]; ok {
		s.PID = 0
		s.PIDStartTime = 0
		_ = sm.saveState()
	}
	sm.mu.Unlock()

	// Blocking operations outside the lock.
	if hasPTY {
		ptySess.Detach()

		if !ptySess.Exited() {
			sm.logStopping(id, sm.sessionName(id), StopReasonDelete, "delete", ptySess)

			_ = ptySess.Kill()
			select {
			case <-ptySess.Done():
			case <-time.After(5 * time.Second):
				_ = ptySess.ForceKill()
			}
		}

		ptySess.Close()
	} else if orphanPID > 0 {
		sm.logStoppingPID(id, sm.sessionName(id), StopReasonDelete, "delete-orphan", orphanPID, orphanPID)

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

			// Delete is aborted and the session is kept, so drop the tombstone —
			// there is no interrupted delete to resume.
			sm.removeTombstone(id)

			if hasClient {
				ac.kick()
			}

			return fmt.Errorf("delete aborted: orphaned process (PID %d) could not be killed: %w", orphanPID, err)
		}
	}

	// Attempt git teardown before removing the session from state.
	if teardownErr := sm.teardownArtifacts(spec); teardownErr != nil {
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

		// Teardown failed and the session is kept for retry, so drop the
		// tombstone — the delete did not commit and must not auto-resume.
		sm.removeTombstone(id)

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
	delete(sm.silentWarned, id)
	delete(sm.headlessEscalated, id)

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

	// The removal-from-state save is the durable commit point. Only drop the
	// tombstone once it succeeds; if it failed, keep the tombstone so startup
	// finishes the delete (state.json may still list this now-torn-down session).
	if err == nil {
		sm.removeTombstone(id)
	}

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
		name         string
		agent        string
		repoPath     string
		worktreePath string
		branch       string
		shared       bool
		inPlace      bool
		prevStatus   SessionStatus
		includes     []IncludedRepoState
		ptySess      SessionDriver
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
			name:         sess.Name,
			agent:        sess.Agent,
			repoPath:     sess.RepoPath,
			worktreePath: sess.WorktreePath,
			branch:       sess.Branch,
			shared:       sess.Mirror,
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
				sm.logStopping(s.id, s.name, StopReasonDelete, "delete-children", s.ptySess)

				_ = s.ptySess.Kill()
				select {
				case <-s.ptySess.Done():
				case <-time.After(5 * time.Second):
					_ = s.ptySess.ForceKill()
				}
			}

			s.ptySess.Close()
		} else if s.pid > 0 {
			sm.logStoppingPID(s.id, s.name, StopReasonDelete, "delete-children-orphan", s.pid, s.pid)

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
				name:         sess.Name,
				agent:        sess.Agent,
				repoPath:     sess.RepoPath,
				worktreePath: sess.WorktreePath,
				branch:       sess.Branch,
				shared:       sess.Mirror,
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
					sm.logStopping(s.id, s.name, StopReasonDelete, "delete-children-sweep", s.ptySess)

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

		spec := teardownSpec{
			ID:           s.id,
			RepoPath:     s.repoPath,
			WorktreePath: s.worktreePath,
			Branch:       s.branch,
			Shared:       s.shared,
			InPlace:      s.inPlace,
			Includes:     s.includes,
		}

		// Tombstone before teardown so a crash mid-delete is resumed on startup.
		// Fail closed: if the recovery marker can't be written, skip this
		// session's teardown and keep it for retry rather than tear down a
		// worktree with no way to resume the delete.
		if err := sm.writeTombstone(tombstone{
			teardownSpec: spec,
			Name:         s.name,
			PID:          s.pid,
			PIDStartTime: s.pidStartTime,
			CreatedAt:    time.Now(),
		}); err != nil {
			sm.log.Error("failed to write delete tombstone; keeping session for retry",
				"id", s.id, "err", err)
			teardownErrs = append(teardownErrs, fmt.Errorf("session %s: write tombstone: %w", s.id, err))

			continue
		}

		if err := sm.teardownArtifacts(spec); err != nil {
			sm.log.Error("git teardown failed, session kept for retry",
				"session_id", s.id, "err", err)
			teardownErrs = append(teardownErrs, fmt.Errorf("session %s: %w", s.id, err))
			// Delete did not commit; drop the tombstone so it does not auto-resume.
			sm.removeTombstone(s.id)
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
			// Only drop the tombstone once the removal-from-state save is durable;
			// if it failed, keep the tombstone so startup finishes the delete.
			if stateErr == nil {
				sm.removeTombstone(s.id)
			}

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

// peakRSSProcLabel names which process the peak_rss_mb reading belongs to
// (issue #1104). The value is the waited child's rusage — the sandbox wrapper
// when the session is sandboxed, otherwise the agent itself.
func peakRSSProcLabel(sandboxed bool) string {
	if sandboxed {
		return "sandbox-wrapper"
	}

	return "agent"
}

// logStopping records a daemon-initiated stop on the daemon log the instant
// before the SIGTERM is sent, so every kill is attributable from the log alone
// (issue #1104). reason is the StopReason category being applied; initiator
// names the code path that requested the stop (idle-loop, user-stop, delete,
// restart, shutdown, …). pid/pgid come from the live PTY (nil-safe) and enable
// OS-level signal forensics. Paired with the "session exited" line, this closes
// the "which subsystem killed this session, and when?" gap.
func (sm *SessionManager) logStopping(id, name, reason, initiator string, sess SessionDriver) {
	pid, pgid := 0, 0
	if sess != nil {
		pid = sess.ProcessPID()
		pgid = sess.Pgid()
	}

	sm.logStoppingPID(id, name, reason, initiator, pid, pgid)
}

// logStoppingPID is logStopping for the orphan-reap paths where there is no live
// PTY, only a recorded pid (killVerifiedProcess signals the process group via
// -pid, so pgid == pid). Keeping these on the same "stopping session" line means
// `grep "stopping session"` is a complete daemon-kill audit (issue #1104).
func (sm *SessionManager) logStoppingPID(id, name, reason, initiator string, pid, pgid int) {
	sm.log.Info("stopping session",
		"id", id, "name", name, "reason", reason, "initiator", initiator,
		"pid", pid, "pgid", pgid)
}

// Stop sends SIGTERM to a session's process without removing the session or worktree.
func (sm *SessionManager) Stop(id string) error {
	return sm.stopWithReason(id, StopReasonUser, "user-stop")
}

func (sm *SessionManager) stopWithReason(id, reason, initiator string) error {
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

	name := sessState.Name
	sessState.StopReason = reason
	_ = sm.saveState()
	sm.mu.Unlock()

	ptySess, ok := sm.GetPTY(id)
	if ok {
		sm.logStopping(id, name, reason, initiator, ptySess)

		if err := ptySess.Kill(); err != nil {
			return fmt.Errorf("send SIGTERM: %w", err)
		}

		return nil
	}

	sm.mu.Lock()
	pid := sessState.PID
	startTime := sessState.PIDStartTime
	sm.mu.Unlock()

	sm.logStoppingPID(id, name, reason, initiator+"-orphan", pid, pid)

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
		name := sess.Name
		pid := sess.PID
		startTime := sess.PIDStartTime
		_ = sm.saveState()
		sm.mu.Unlock()

		ptySess, ok := sm.GetPTY(id)
		if ok {
			sm.logStopping(id, name, StopReasonUser, "stop-children", ptySess)

			if err := ptySess.Kill(); err != nil {
				sm.log.Warn("stop child failed", "session_id", id, "error", err)
				continue
			}

			stopped = append(stopped, id)

			continue
		}

		sm.logStoppingPID(id, name, StopReasonUser, "stop-children-orphan", pid, pid)

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

			sm.logStopping(lid, sm.sessionName(lid), StopReasonUser, "stop-children-sweep", ptySess)

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
// picking up the current agent and sandbox configuration. A plain user restart
// attributes the teardown to StopReasonUser; internal callers use
// restartWithReason to preserve the true subsystem (e.g. the startup watchdog).
func (sm *SessionManager) Restart(id string, rows, cols uint16) (SessionState, error) {
	return sm.restartWithReason(id, rows, cols, StopReasonUser, "restart")
}

// restartWithReason is Restart with an explicit stop attribution so a watchdog
// recovery isn't logged as an authenticated user restart (issue #1104).
func (sm *SessionManager) restartWithReason(id string, rows, cols uint16, stopReason, initiator string) (SessionState, error) {
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

	sm.log.Info("restart requested", "session_id", id, "has_live_pty", hasPTY,
		"pty_exited", hasPTY && ptySess.Exited(),
		"scrollback_path", sm.scrollbackLogPath(id))

	if hasPTY && !ptySess.Exited() {
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.StopReason = stopReason
		}
		sm.mu.Unlock()

		sm.logStopping(id, sm.sessionName(id), stopReason, initiator, ptySess)

		if err := ptySess.Kill(); err != nil {
			return SessionState{}, fmt.Errorf("stop session: %w", err)
		}

		<-ptySess.Done()

		// Close the old PTY so its Ptmx fd and scrollback file handle are
		// released promptly at restart time. The stale watcher for this PTY also
		// closes it once it observes the exit (watchSession documents double-close
		// as safe), but closing here makes the release deterministic. The new PTY
		// (spawned by resume below) reopens the same scrollback log in append
		// mode, so post-restart output is preserved.
		sm.log.Info("restart: old pty stopped, closing", "session_id", id,
			"old_output_bytes", ptySess.BytesRead())
		ptySess.Close()

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
			name := sess.Name
			sm.mu.Unlock()

			sm.logStoppingPID(id, name, stopReason, initiator+"-orphan", pid, pid)

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
		sessions          []protocol.SessionDiagnostic
		deletedSessionIDs []string
		sbDiag            protocol.ScrollbackDiagnostic
		fleet             protocol.FleetSummary
	)

	for id, s := range sm.state.Sessions {
		// Soft-deleted sessions are hidden trash awaiting purge; exclude them from
		// diagnostics and the fleet tally so `gr doctor` reflects live work only.
		// Keep their IDs as a separate ownership signal so doctor's orphan cleanup
		// does not destroy resources that remain recoverable until purge.
		if s.IsSoftDeleted() {
			deletedSessionIDs = append(deletedSessionIDs, id)
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
			written, maxSize, saturated := ptySess.ScrollbackFile().Stats()
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
		DaemonPID:         os.Getpid(),
		DaemonUptime:      now.Sub(sm.startedAt).Truncate(time.Second).String(),
		Fleet:             fleet,
		Sessions:          sessions,
		DeletedSessionIDs: deletedSessionIDs,
		Scrollback:        sbDiag,
		Messages:          msgDiag,
		Triggers:          sm.degradedTriggerDiagnostics(),
	}
}

// degradedTriggerDiagnostics reports the currently-degraded watch-trigger
// bindings for gr doctor. Binding facts are snapshotted under triggers.mu, then
// session names are resolved after releasing it (sessionName takes sm.mu) to
// avoid holding both locks at once.
func (sm *SessionManager) degradedTriggerDiagnostics() []protocol.TriggerDiagnostic {
	sm.triggers.mu.Lock()

	out := make([]protocol.TriggerDiagnostic, 0)

	for _, b := range sm.triggers.bindings {
		if b.degraded == "" {
			continue
		}

		td := protocol.TriggerDiagnostic{
			Name:       b.triggerName,
			SessionID:  b.sessionID,
			Degraded:   b.degraded,
			RetryCount: b.retryCount,
		}
		if !b.nextRetryAt.IsZero() {
			td.NextRetryAt = b.nextRetryAt.Format(time.RFC3339)
		}

		out = append(out, td)
	}
	sm.triggers.mu.Unlock()

	for i := range out {
		out[i].SessionName = sm.sessionName(out[i].SessionID)
	}

	if len(out) == 0 {
		return nil
	}

	return out
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

// GetPTY returns the live session driver by ID. Named for its historic
// PTY-only past; today it may return any SessionDriver implementation.
func (sm *SessionManager) GetPTY(id string) (SessionDriver, bool) {
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
	for id, s := range sm.state.Sessions {
		if s.Status == StatusRunning {
			prevSummary, prevSetAt := sm.prevStopSummaryLocked(s, id)

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
		name string
		sess SessionDriver
	}

	sessions := make([]snapshot, 0, len(sm.sessions))
	for id, sess := range sm.sessions {
		name := ""
		if s, ok := sm.state.Sessions[id]; ok {
			name = s.Name
		}

		sessions = append(sessions, snapshot{id, name, sess})
	}
	sm.mu.Unlock()

	for _, s := range sessions {
		if !s.sess.Exited() {
			sm.logStopping(s.id, s.name, StopReasonShutdown, "shutdown", s.sess)
			_ = s.sess.Kill()
		}
	}

	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(id string, sess SessionDriver) {
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
		if s.Status != StatusRunning || s.Mirror {
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

// silentSessionThreshold is how long a running session's PTY may produce zero
// output before the daemon flags it as silent. Interactive agents (Claude,
// Codex, …) render their UI within a second or two of starting, so a session
// still at zero bytes well past this window is almost certainly stuck — blocked
// on a pre-render prompt, or otherwise not writing to its PTY (issue #1087).
const silentSessionThreshold = 20 * time.Second

// checkSilentSession warns once per PTY lifetime when a running session has
// produced no PTY output past silentSessionThreshold. This is the signal that
// was missing when #1087 (blank screen after restart) was diagnosed: the agent
// process was alive and writing its transcript, but nothing reached the PTY, so
// scrollback stayed empty and attach showed nothing — with no trace in the log.
func (sm *SessionManager) checkSilentSession(id, name, agent string, pty SessionDriver) {
	sm.checkSilentSessionWithThreshold(id, name, agent, pty, silentSessionThreshold)
}

// checkSilentSessionWithThreshold is checkSilentSession with an injectable
// threshold so tests don't have to wait out the production window.
func (sm *SessionManager) checkSilentSessionWithThreshold(id, name, agent string, pty SessionDriver, threshold time.Duration) {
	if pty.BytesRead() > 0 {
		return
	}

	// An adopted PTY (daemon upgrade re-attaching to a surviving agent) starts
	// at zero bytes even though the agent likely rendered before the upgrade, so
	// zero-output can't be read as "never rendered" — exempt it outright rather
	// than emit a false diagnosis for every idle adopted session (issue #1087).
	if pty.WasAdopted() {
		return
	}

	created := pty.CreatedAt()
	if created.IsZero() || time.Since(created) < threshold {
		return
	}

	// Mark-and-warn under the lock, and only for the PTY still installed for
	// this id. A concurrent restart may have swapped in a new PTY and cleared
	// silentWarned between the snapshot and here; without the identity check a
	// stale snapshot of the old PTY could warn spuriously and consume the new
	// PTY's once-per-lifetime slot.
	sm.mu.Lock()
	if sm.sessions[id] != pty {
		sm.mu.Unlock()
		return
	}

	warned := sm.silentWarned[id]
	sm.silentWarned[id] = true
	sm.mu.Unlock()

	if warned {
		return
	}

	sm.log.Warn("session running but producing no PTY output",
		"session_id", id, "name", name, "agent", agent,
		"running_for", time.Since(created).Round(time.Second),
		"scrollback_path", sm.scrollbackLogPath(id),
		"hint", "agent is alive but has rendered nothing — likely blocked on a pre-render prompt or not writing to the PTY (issue #1087)")
}

func (sm *SessionManager) detectAgentStatuses() {
	sm.mu.RLock()

	type target struct {
		id           string
		name         string
		agent        string
		prevStatus   string
		pty          SessionDriver
		worktreePath string
		baseBranch   string
		repoPath     string
		includes     []IncludedRepoState
		mirror       bool
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
				includes: inc, mirror: s.Mirror,
			})
		}
	}

	sm.mu.RUnlock()

	var toAutoStop []string

	for _, t := range targets {
		sm.checkSilentSession(t.id, t.name, t.agent, t.pty)

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

		if !t.mirror {
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
				// The session is producing output, so it launched successfully:
				// clear any startup-watchdog restart count so the cap only bounds
				// *consecutive* stuck restarts (#1092).
				resetStuckRestartsLocked(s)
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

		if err := sm.stopWithReason(id, StopReasonIdle, "idle-loop"); err != nil {
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

		switch {
		case s.IdleTimeoutSecs > 0:
			// Per-session override (trigger-spawned auto_cleanup / explicit
			// idle_timeout) takes precedence over the agent default.
			timeout = time.Duration(s.IdleTimeoutSecs) * time.Second
		case s.SystemKind == SystemKindOrchestrator:
			timeout = sm.cfg.Orchestrator.IdleTimeoutDuration()
		default:
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
	// Resize the launch throttle under the same lock that publishes the config so
	// the two can't diverge if two reloads (fsnotify + SIGHUP) interleave — the
	// live limit always matches the currently-published cfg. resize only takes the
	// throttle's own mutex, so the sm.mu -> launch.mu order introduces no cycle.
	if sm.launch != nil {
		sm.launch.resize(newCfg.Launch.MaxConcurrentOrDefault())
	}
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

	// The throttle was already resized atomically with the cfg swap above; here we
	// only log the change for observability.
	if oldMax, newMax := old.Launch.MaxConcurrentOrDefault(), newCfg.Launch.MaxConcurrentOrDefault(); oldMax != newMax {
		sm.log.Info("config changed", "key", "launch.max_concurrent", "old", oldMax, "new", newMax)
	}

	if old.Launch.StartupTimeout != newCfg.Launch.StartupTimeout {
		sm.log.Info("config changed", "key", "launch.startup_timeout", "old", old.Launch.StartupTimeout, "new", newCfg.Launch.StartupTimeout)
	}

	if old.Launch.SettleTimeout != newCfg.Launch.SettleTimeout {
		sm.log.Info("config changed", "key", "launch.settle_timeout", "old", old.Launch.SettleTimeout, "new", newCfg.Launch.SettleTimeout)
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

	// If the PR-comment author-trust config changed, re-evaluate jailed comments
	// against the new config and auto-release any whose author is now trusted
	// (issue #1082). A config reload is a local-human action, so this release is
	// implicitly human-authorized. Run detached: it hits the message DB and may
	// auto-resume stopped sessions, which must outlive the reload request.
	if prWatchTrustChanged(old.PRWatch, newCfg.PRWatch) {
		sm.log.Info("config changed", "key", "pr_watch.comment_trust")

		// autoReleaseNewlyTrusted reads the current config itself (sm.cfg was set
		// above), so a later reload that tightens trust wins over this worker.
		go sm.autoReleaseNewlyTrusted()
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
				_ = sm.stopWithReason(orchID, StopReasonUser, "orchestrator-disabled")
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

// includeAddDirArgs builds the `--add-dir <worktree>` flags that make each
// included repo's co-located worktree visible to the agent at launch. Only the
// agents graith knows accept the flag get it (see agentSupportsAddDir), so a
// repo's includes never inject an unknown flag into an agent that would reject
// it and fail to launch. Included worktrees are still exposed via the
// GRAITH_INCLUDE_*_PATH env vars for every agent regardless. Worktrees without a
// path are skipped defensively; the result is nil (not an empty slice) when
// nothing is emitted.
func includeAddDirArgs(agentType string, includes []IncludedRepoState) []string {
	if !agentSupportsAddDir(agentType) || len(includes) == 0 {
		return nil
	}

	args := make([]string, 0, len(includes)*2)
	for _, inc := range includes {
		if inc.WorktreePath == "" {
			continue
		}

		args = append(args, "--add-dir", inc.WorktreePath)
	}

	if len(args) == 0 {
		return nil
	}

	return args
}

// cloneCodexOptions returns an independent copy of opts (or nil), so a fork's or
// resume's persisted options don't alias the source session's struct.
func cloneCodexOptions(opts *config.CodexOptions) *config.CodexOptions {
	if opts == nil {
		return nil
	}

	o := *opts

	return &o
}

// codexStatePtr returns a heap copy of opts for persisting on SessionState, or
// nil when nothing is set so a non-codex (or option-less) session stores no
// `codex` block.
func codexStatePtr(opts config.CodexOptions) *config.CodexOptions {
	if opts.IsZero() {
		return nil
	}

	o := opts

	return &o
}

// codexOptsFromMsg flattens the wire pointer to a value for CreateOpts, treating
// nil as "no options set".
func codexOptsFromMsg(opts *config.CodexOptions) config.CodexOptions {
	if opts == nil {
		return config.CodexOptions{}
	}

	return *opts
}

// codexExtraArgs builds the backend-aware conditional flags for the Codex CLI
// from the session's model and typed options (issue #1186). Each flag is emitted
// only when its value is set, so an unset option leaves Codex's own default
// untouched — the reason these can't just live as `{model}` templates in the
// agent args (an empty model would expand to a literal `--model ""`). Returns
// nil for any non-codex agent, so it is safe to call unconditionally on every
// launch path (create/resume/fork). Reasoning effort and service tier have no
// dedicated Codex flag, so they ride `-c key=value` config overrides; the rest
// map to real flags. All of these are accepted on the bare invocation and on the
// `resume`/`fork` subcommands, so appending them after existing args is valid.
func codexExtraArgs(agentType, model string, opts *config.CodexOptions) []string {
	if agentType != "codex" {
		return nil
	}

	var args []string

	if model != "" {
		args = append(args, "--model", model)
	}

	if opts != nil {
		if opts.Profile != "" {
			args = append(args, "--profile", opts.Profile)
		}

		if opts.ReasoningEffort != "" {
			args = append(args, "-c", "model_reasoning_effort="+opts.ReasoningEffort)
		}

		if opts.ServiceTier != "" {
			args = append(args, "-c", "service_tier="+opts.ServiceTier)
		}

		if opts.WebSearch {
			args = append(args, "--search")
		}

		if opts.ApprovalPolicy != "" {
			args = append(args, "--ask-for-approval", opts.ApprovalPolicy)
		}
	}

	return args
}

// resumeIncludeSet picks the includes a resuming session should re-grant (both
// as GRAITH_INCLUDE_*_PATH env vars and --add-dir flags). A mirror session
// persists none of its own — its git setup is skipped at create — so it takes
// the source session's includes (snapshotted as sharedSourceIncludes). Every
// other session uses its own. This keeps a mirror's sibling visibility across a
// restart, matching how Create seeds a mirror from the source's includes.
func resumeIncludeSet(mirror bool, sessIncludes, sharedSourceIncludes []IncludedRepoState) []IncludedRepoState {
	if mirror {
		return sharedSourceIncludes
	}

	return sessIncludes
}

// agentSupportsAddDir reports whether the named agent's CLI accepts the
// `--add-dir <path>` flag graith uses to grant included-repo worktrees. Claude,
// Codex, and Cursor all do; other agents (e.g. opencode, agy, or a custom
// command) may reject an unknown flag, so they are left without it.
func agentSupportsAddDir(agentType string) bool {
	switch agentType {
	case "claude", "codex", "cursor":
		return true
	default:
		return false
	}
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

func (sm *SessionManager) sandboxOptsFromConfig(merged config.SandboxConfig, sessionID, worktreePath, agentCommand string, envKeys []string, grantHookDir bool) sandbox.WrapOpts {
	readDirs := expandPaths(merged.ReadDirs, sm.log, "read")
	writeDirs := expandPaths(merged.WriteDirs, sm.log, "write")
	readFiles := expandFilePaths(merged.ReadFiles, sm.log, "read")
	writeFiles := expandFilePaths(merged.WriteFiles, sm.log, "write")

	// The hook dir holds both the generated settings (hooks) file and the MCP
	// config file, so grant it read whenever either was injected (see #1135).
	if grantHookDir {
		hd := sm.hookDir(sessionID)
		if _, err := os.Stat(hd); err == nil {
			readDirs = append(readDirs, hd)
		}
	}

	readDirs = append(readDirs, filepath.Dir(sm.paths.ConfigFile))
	if dir, ok := grBinReadDir(resolveGrBin()); ok {
		readDirs = append(readDirs, dir)
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

		if err := sm.loadOrCreateHumanToken(); err != nil {
			_ = l.Close()
			return fmt.Errorf("initialize human authentication: %w", err)
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

		if err := sm.loadOrCreateHumanToken(); err != nil {
			_ = l.Close()

			ReleasePIDFile(paths.PIDFile)

			return fmt.Errorf("initialize human authentication: %w", err)
		}

		// Finish any deletes interrupted mid-flight (crash/kill/power loss)
		// before reaping orphaned processes, so a half-deleted session's
		// worktree and state entry are removed together.
		sm.resumeTombstones()

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
	go sm.RunStartupWatchdogLoop(ctx)
	go sm.RunMessageCleanupLoop(ctx)
	go sm.RunGitPullLoop(ctx)
	go sm.RunPRWatchLoop(ctx)
	go sm.RunPurgeLoop(ctx)
	go sm.RunTriggerLoop(ctx)
	go sm.RunFileWatchLoop(ctx)
	go sm.RunTokenLoop(ctx)

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
