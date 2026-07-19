package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/agent/transcript"
	"github.com/d0ugal/graith/internal/atomicfile"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/store"
	"github.com/fsnotify/fsnotify"
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
	mu             sync.RWMutex
	configReloadMu sync.Mutex
	// scenarioResultMu serializes store-write + state-commit publication. Store
	// I/O never holds mu, while the separate lock prevents two publications from
	// leaving artifact content and metadata describing different attempts.
	scenarioResultMu sync.Mutex
	state            *State
	sessions         map[string]SessionDriver
	stopAttempts     map[string]*stopAttempt
	attachedClients  map[string]*attachedClient
	hookReports      map[string]hookReport
	tokenIndex       map[string]string            // token → session ID (reverse lookup)
	humanToken       string                       // local human credential, loaded at startup
	saveStateFault   func() error                 // test-only saveState fault injection; nil in production
	sandboxResolver  func(string) (bool, error)   // test-only sandbox-availability override; nil in production
	pendingPairings  map[string]*pendingPairing   // requestID → pending device pairing (in-memory; not persisted)
	pairWaiters      map[string]chan pairApproval // requestID → waiter for a blocked pair_request connection
	remoteTLSPin     string                       // SPKI pin of the active remote generation; guarded by mu
	remoteGeneration uint64                       // active remote runtime generation; 0 means no production listener
	remote           *remoteController            // owned under configReloadMu; nil for bare SessionManagers used outside Run
	deviceTokenIndex map[string]string            // client-token HMAC → device ID (reverse lookup)
	connsByDevice    map[string][]net.Conn        // device ID → live remote connections (for revocation)
	pairReqTimes     []time.Time                  // recent pair_request timestamps (rate limiting)
	cfg              *config.Config
	paths            config.Paths
	log              *slog.Logger
	configFile       string
	upgradeCh        chan string
	messages         *MsgStore
	todos            *TodoStore
	mcpManager       *MCPManager
	startedAt        time.Time
	// instanceID is a nonce generated once per daemon process start (including
	// after an exec upgrade, which re-runs main and constructs a fresh
	// SessionManager). It is returned in handshake_ok/auth_ok so an upgrade
	// readiness wait can prove the new daemon generation is serving rather than
	// the inherited listener (issue #1319).
	instanceID         string
	orchestratorExitCh chan string
	orchestratorKickCh chan struct{}
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
	completion   *scenarioCompletionRuntime
	tokens       *tokenCache
	launch       *launchThrottle
	// sessionLaunchLocks serialize resume/restart decisions per session. They
	// close the compare-to-launch race between an automatic scenario retry and
	// a direct user/watchdog launch without coupling unrelated sessions.
	sessionLaunchLocksMu sync.Mutex
	sessionLaunchLocks   map[string]*lifecycleGate
	// scenarioPolicyPlanMu serializes durable retry claims. Process lifecycle
	// work happens after it is released so a slow member cannot stall policy
	// planning or operator commands for unrelated scenarios.
	scenarioPolicyPlanMu sync.Mutex
	// scenarioPolicyLocks are per-scenario lifecycle gates. The registry is
	// append-only for the daemon lifetime: retaining a tiny mutex per observed
	// scenario avoids deleting a lock while another goroutine is waiting on it.
	scenarioPolicyLocksMu sync.Mutex
	scenarioPolicyLocks   map[string]*lifecycleGate
	// scenarioStartIDs transiently reserves stable scenario IDs before template
	// rendering and preflight. It is guarded by mu alongside state.Scenarios, so
	// concurrent starts cannot render with the same {short_id} candidate.
	scenarioStartIDs map[string]bool
	// scenarioIDGenerator is a deterministic test seam. Production uses the
	// same CSPRNG-backed eight-hex generator as session IDs.
	scenarioIDGenerator func() string
	// scenarioPolicyInFlight contains retry dispatches currently executing in
	// this daemon. RetryDispatched is durable; this runtime companion lets a
	// concurrent planner distinguish live work from a dispatch interrupted by a
	// daemon restart.
	scenarioPolicyInFlightMu sync.Mutex
	scenarioPolicyInFlight   map[scenarioRetryAction]bool
	// scenarioPolicyNow and scenarioRestart are deterministic test seams for
	// the policy scheduler. Production falls back to time.Now and Restart.
	scenarioPolicyNow   func() time.Time
	scenarioRestart     func(id string, rows, cols uint16) error
	scenarioResume      func(id string, rows, cols uint16) error
	scenarioPolicyDirty map[string]bool // guarded by mu; retries result persistence without replaying actions
	resourceMu          sync.Mutex
	resourceSamples     map[string][]ResourceSample
	resourceKick        chan struct{}
	signalRequests      map[string]signalRequest
	newLoopTicker       func(time.Duration) loopTicker // injectable clock boundary for background-loop tests
	newLoopTimer        func(time.Duration) loopTimer  // injectable resettable clock boundary for purge tests

	// purgeStatsMu guards the last/next purge-sweep timestamps surfaced in
	// diagnostics. It is separate from sm.mu so recording a sweep never contends
	// with session mutations.
	purgeStatsMu   sync.Mutex
	lastPurgeSweep time.Time
	nextPurgeSweep time.Time

	// writeTombstoneFault injects a failure AFTER the tombstone file is durably
	// written, simulating a post-rename parent-dir fsync error where the marker
	// exists on disk yet the write reports failure; nil in production. Tests use
	// it to exercise the fail-closed cleanup of an already-landed marker (#1326).
	writeTombstoneFault func(id string) error

	// tombstoneDirSyncFault injects a parent-directory fsync failure during
	// removeTombstone, simulating an unlink that is not yet durable; nil in
	// production. Tests use it to prove the removal error is propagated on
	// abort/retry paths (issue #1326).
	tombstoneDirSyncFault func() error

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
		stopAttempts:       make(map[string]*stopAttempt),
		attachedClients:    make(map[string]*attachedClient),
		hookReports:        make(map[string]hookReport),
		tokenIndex:         make(map[string]string),
		pendingPairings:    make(map[string]*pendingPairing),
		pairWaiters:        make(map[string]chan pairApproval),
		deviceTokenIndex:   make(map[string]string),
		connsByDevice:      make(map[string][]net.Conn),
		orchestratorExitCh: make(chan string, 4),
		orchestratorKickCh: make(chan struct{}, 1),
		lastInboxNotifyAt:  make(map[string]time.Time),
		silentWarned:       make(map[string]bool),
		prWatch:            newPRWatchState(cfg.PRWatch.KickChannelSize()),
		prRefWatch:         newPRRefWatchState(),
		triggers:           newTriggerState(),
		completion:         newScenarioCompletionRuntime(),
		tokens:             newTokenCache(),
		launch:             newLaunchThrottle(cfg.Launch.MaxConcurrentOrDefault()),
		resourceSamples:    make(map[string][]ResourceSample),
		resourceKick:       make(chan struct{}, 1),
		signalRequests:     make(map[string]signalRequest),
		newLoopTicker:      newRealLoopTicker,
		newLoopTimer:       newRealLoopTimer,
		cfg:                cfg,
		paths:              paths,
		log:                log,
		startedAt:          time.Now(),
		instanceID:         newDaemonInstanceID(),
	}
	sm.pushDispatch = sm.newPushDispatch()

	// Install the transcript scanner buffer caps process-globally (mirrors
	// tools.Configure). Reads use these on the migrate/fork/token paths; the
	// reload path (applyConfig) re-applies them so a changed [transcript] block
	// takes effect without a daemon restart (issue #1250).
	transcript.Configure(cfg.Transcript.MaxLineBytesOrDefault(), cfg.Transcript.MaxMetadataLineBytesOrDefault())

	return sm
}

// newDaemonInstanceID returns a fresh per-process nonce. On the (near-impossible)
// event that the CSPRNG read fails it falls back to a start-time+PID string,
// which is still distinct across an exec upgrade — the property #1319 needs — so
// the readiness signal degrades rather than becoming empty.
func newDaemonInstanceID() string {
	if id, err := randomHex(16); err == nil {
		return id
	}

	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
}

// InstanceID returns this daemon process's boot nonce (see the instanceID field).
func (sm *SessionManager) InstanceID() string {
	return sm.instanceID
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
	// is still active, and clobbering a ready status here would be a
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

	// Hook-authority windows are configurable via [detection] (issue #1241);
	// snapshot them under the read lock before classifying the event.
	sm.mu.RLock()
	det := sm.cfg.Detection
	sm.mu.RUnlock()

	var (
		hookStartWindow    = det.HookStartWindowDuration()
		hookActivityWindow = det.HookActivityWindowDuration()
		hookTerminalWindow = det.HookTerminalWindowDuration()
	)

	switch sr.Event {
	case "SessionStart":
		status = "active"
		staleness = hookStartWindow
	case "UserPromptSubmit", "PreToolUse", "PostToolUse":
		status = "active"
		staleness = hookActivityWindow
	case "Notification":
		// A Claude Notification's meaning is in its subtype. The CLI forwards
		// the raw notification_type (empty when stdin didn't parse); the daemon
		// decides. Only idle_prompt (agent awaiting input) changes status. A
		// permission_prompt belongs entirely to the agent's native TUI and is an
		// ordinary running state from Graith's perspective. Everything else —
		// auth_success, elicitation_*, and crucially an empty/unknown/unparsed
		// subtype — is logged without touching AgentStatus.
		switch sr.NotificationType {
		case "idle_prompt":
			status = "ready"
			staleness = hookTerminalWindow
		case "permission_prompt":
			sm.log.Info("agent entered native permission prompt",
				"event", sr.Event, "notification_type", sr.NotificationType,
				"session_id", sr.SessionID)

			return
		default:
			sm.log.Info("ignoring notification subtype",
				"event", sr.Event, "notification_type", sr.NotificationType,
				"session_id", sr.SessionID)

			return
		}
	case "PermissionRequest":
		// Agent-native approval prompts are not Graith workflow states. The
		// agent's own TUI remains responsible for rendering and resolving them.
		sm.log.Info("agent entered native PermissionRequest", "session_id", sr.SessionID)

		return
	case "Stop":
		status = "ready"
		staleness = hookTerminalWindow
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
// (active / error / ready). The SubAgents map is replaced, never mutated in
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
	recoverInterruptedScenarioStarts(state, time.Now().UTC())
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
		return errors.New("read human token: token is empty")
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

	// sm.mu is held, so sm.cfg is read directly rather than via Config() (which
	// would take the read lock and deadlock).
	lc := sm.cfg.Lifecycle

	type terminationCandidate struct {
		stateID, label, summary string
		pid                     int
		startTime               int64
	}

	type adoptedSession struct {
		id     string
		driver SessionDriver
	}

	var (
		adopted       []adoptedSession
		adoptedIDs    = make(map[string]struct{})
		adoptedPIDs   = make(map[int]struct{})
		candidates    []terminationCandidate
		candidateKeys = make(map[string]struct{})
		adoptionErrs  []error
		closedFDs     = make(map[int]struct{})
		preflightBad  bool
	)

	closeFD := func(us UpgradeSession) {
		if !us.HasPTY || us.Fd < 0 {
			return
		}

		if _, closed := closedFDs[us.Fd]; closed {
			return
		}

		closedFDs[us.Fd] = struct{}{}
		if f := os.NewFile(uintptr(us.Fd), "rejected-upgrade-session"); f != nil {
			_ = f.Close()
		}
	}
	schedule := func(stateID, label string, pid int, startTime int64, summary string) {
		key := fmt.Sprintf("%s:%d", stateID, pid)
		if stateID == "" {
			key = fmt.Sprintf("manifest:%s:%d", label, pid)
		}

		if _, exists := candidateKeys[key]; exists {
			return
		}

		candidateKeys[key] = struct{}{}

		candidates = append(candidates, terminationCandidate{
			stateID: stateID, label: label, summary: summary, pid: pid, startTime: startTime,
		})
	}
	scheduleState := func(id, summary string) {
		if sess := sm.state.Sessions[id]; sess != nil && sess.PID > 1 {
			schedule(id, id, sess.PID, sess.PIDStartTime, summary)
		}
	}

	// Preflight the entire manifest before attaching any PTY. If one entry
	// cannot be authenticated, no process is adopted into a daemon that will
	// immediately abort; every verifiable process is instead terminated below.
	for _, us := range manifest.Sessions {
		sessState, ok := sm.state.Sessions[us.ID]
		if !ok {
			closeFD(us)
			schedule("", us.ID, us.PID, us.PIDStartTime, "")
			adoptionErrs = append(adoptionErrs, fmt.Errorf("upgrade manifest references unknown session %q", us.ID))
			preflightBad = true

			continue
		}

		if sessState.PID <= 1 || sessState.PID != us.PID {
			closeFD(us)
			scheduleState(us.ID, "Stopped after invalid daemon-upgrade process identity")

			manifestStateID := ""
			if sessState.PID <= 1 {
				manifestStateID = us.ID
			}

			schedule(manifestStateID, us.ID, us.PID, us.PIDStartTime, "Stopped after invalid daemon-upgrade process identity")
			adoptionErrs = append(adoptionErrs, fmt.Errorf("upgrade session %q PID mismatch: state has %d, manifest has %d", us.ID, sessState.PID, us.PID))
			preflightBad = true

			continue
		}

		if us.PIDStartTime != 0 && sessState.PIDStartTime != 0 && us.PIDStartTime != sessState.PIDStartTime {
			closeFD(us)
			scheduleState(us.ID, "Stopped after invalid daemon-upgrade process identity")
			adoptionErrs = append(adoptionErrs, fmt.Errorf("upgrade session %q process start-time mismatch", us.ID))
			preflightBad = true

			continue
		}

		if reason := invalidUpgradeAdoptionReason(sessState); reason != "" {
			closeFD(us)
			scheduleState(us.ID, "Stopped during incompatible process-identity upgrade")
			sm.log.Warn("terminating upgrade session that lacks process identity", "id", us.ID, "pid", us.PID, "reason", reason)

			continue
		}

		currentStart, err := grpty.ProcessStartTime(us.PID)
		if err != nil || currentStart != sessState.PIDStartTime {
			closeFD(us)
			scheduleState(us.ID, "Stopped after invalid daemon-upgrade process identity")
			adoptionErrs = append(adoptionErrs, fmt.Errorf("upgrade session %q process identity cannot be verified (recorded=%d, current=%d, err=%w)", us.ID, sessState.PIDStartTime, currentStart, err))
			preflightBad = true
		}
	}

	if preflightBad {
		for _, us := range manifest.Sessions {
			closeFD(us)
			scheduleState(us.ID, "Stopped because secure daemon upgrade preflight failed")
		}
	} else {
		for _, us := range manifest.Sessions {
			if !us.HasPTY {
				continue
			}

			sessState := sm.state.Sessions[us.ID]
			if invalidUpgradeAdoptionReason(sessState) != "" {
				continue
			}

			logPath := filepath.Join(sm.paths.LogDir, us.ID+".log")

			ptySess, err := grpty.AdoptSession(grpty.AdoptOpts{
				ID: us.ID, Fd: uintptr(us.Fd), PID: us.PID, LogPath: logPath,
				MaxLogSize: lc.MaxLogBytesOrDefault(), DefaultRows: lc.DefaultRowsOrDefault(),
				DefaultCols: lc.DefaultColsOrDefault(), HydrationBytes: lc.ScrollbackHydrationBytesOrDefault(),
				PollTimeout: lc.AdoptedTimeoutDuration(), PollInterval: lc.AdoptedPollIntervalDuration(), Logger: sm.log,
			})
			if err != nil {
				sm.log.Warn("failed to adopt session", "id", us.ID, "err", err, "action", "terminate process")
				scheduleState(us.ID, "Lost during daemon upgrade")

				continue
			}

			sm.sessions[us.ID] = ptySess
			adopted = append(adopted, adoptedSession{id: us.ID, driver: ptySess})
			adoptedIDs[us.ID] = struct{}{}
			adoptedPIDs[us.PID] = struct{}{}
			sm.log.Info("adopted session", "id", us.ID, "pid", us.PID)
		}
	}

	// Headless drivers have no PTY to hand off, and a PTY may also have been
	// omitted after an old-daemon fd error. Neither may survive unmanaged.
	for id, sess := range sm.state.Sessions {
		if sess.Status != StatusRunning || sess.PID <= 1 {
			continue
		}

		if _, ok := adoptedIDs[id]; ok {
			continue
		}

		summary := "Stopped during daemon upgrade"
		if reason := invalidUpgradeAdoptionReason(sess); reason != "" {
			summary = "Stopped during incompatible process-identity upgrade"
		} else if sess.DriverKind == DriverHeadless {
			summary = "Headless session stopped during daemon upgrade"
		}

		scheduleState(id, summary)
	}

	sm.mu.Unlock()

	type terminationResult struct {
		candidate terminationCandidate
		err       error
	}

	results := make([]terminationResult, 0, len(candidates))
	for _, candidate := range candidates {
		if _, inUse := adoptedPIDs[candidate.pid]; inUse {
			results = append(results, terminationResult{candidate: candidate, err: fmt.Errorf("PID %d is shared with an adopted session", candidate.pid)})
			continue
		}

		_, err := sm.killVerifiedProcess(candidate.pid, candidate.startTime)

		results = append(results, terminationResult{candidate: candidate, err: err})
		if err != nil {
			adoptionErrs = append(adoptionErrs, fmt.Errorf("terminate unadopted upgrade session %q: %w", candidate.label, err))
		}
	}

	sm.mu.Lock()
	for _, result := range results {
		candidate := result.candidate
		if candidate.stateID == "" {
			continue
		}

		sess := sm.state.Sessions[candidate.stateID]
		if sess == nil || (sess.PID > 1 && sess.PID != candidate.pid) {
			continue
		}

		sess.StatusChangedAt = time.Now()
		if result.err != nil {
			sess.Status = StatusErrored
			applyLifecycleSummaryLocked(sess, fmt.Sprintf("Daemon upgrade could not terminate PID %d: %v", candidate.pid, result.err))

			continue
		}

		sess.Status = StatusStopped
		sess.PID = 0
		sess.PIDStartTime = 0
		applyLifecycleSummaryLocked(sess, candidate.summary)
	}

	saveErr := sm.saveState()
	sm.mu.Unlock()

	if saveErr != nil {
		adoptionErrs = append(adoptionErrs, saveErr)
	}

	if len(adoptionErrs) > 0 {
		// A daemon that aborts startup cannot retain ownership of partially
		// adopted processes. Tear them down before returning the fatal error.
		for _, session := range adopted {
			teardownErr := sm.teardownLiveDriver(session.driver)
			sm.mu.Lock()
			if sess := sm.state.Sessions[session.id]; sess != nil {
				sess.StatusChangedAt = time.Now()

				if teardownErr == nil {
					delete(sm.sessions, session.id)

					sess.Status = StatusStopped
					sess.PID = 0
					sess.PIDStartTime = 0
					applyLifecycleSummaryLocked(sess, "Stopped because secure daemon upgrade failed")
				} else {
					sess.Status = StatusErrored
					applyLifecycleSummaryLocked(sess, fmt.Sprintf("Secure daemon upgrade failed and PID %d could not be terminated: %v", sess.PID, teardownErr))
				}
			}
			sm.mu.Unlock()

			if teardownErr != nil {
				adoptionErrs = append(adoptionErrs, fmt.Errorf("terminate adopted session %q after upgrade failure: %w", session.id, teardownErr))
			}
		}

		sm.mu.Lock()
		if err := sm.saveState(); err != nil {
			adoptionErrs = append(adoptionErrs, err)
		}
		sm.mu.Unlock()

		return errors.Join(adoptionErrs...)
	}

	for _, session := range adopted {
		sm.startWatcher(session.id, session.driver)
		go sm.notifyUnreadInbox(session.id)
	}

	return nil
}

func invalidUpgradeAdoptionReason(sess *SessionState) string {
	if sess.PID <= 1 {
		return "session has no recorded process ID"
	}

	if sess.PIDStartTime == 0 {
		return "session has no recorded process identity"
	}

	return ""
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
	Codex    config.CodexOptions
	ParentID string
	NoRepo   bool
	Mirror   string
	// MirrorSourceID is the internal exact-ID mirror contract. Unlike Mirror,
	// it never treats the value as a session name. Scenario startup uses it
	// after resolving and reserving an authoritative source identity.
	MirrorSourceID      string
	AgentHooks          bool
	InPlace             bool
	AllowConcurrent     bool
	SkipModelValidation bool
	// Headless requests a headless stream-json session instead of an
	// interactive PTY (issue #1075). Honoured only when the agent is
	// headless_capable and [headless] experimental is enabled; otherwise Create
	// fails closed rather than silently downgrading. See resolveDriverKind.
	Headless bool
	// ForcePTY disables the soft global headless default for lifecycle owners
	// that require resumability, such as bounded scenario retries. It is an
	// internal option and must not be combined with Headless.
	ForcePTY bool
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
	// Completion* tags a session spawned by a scenario completion action so a
	// daemon restart can adopt it without making it an owned scenario member.
	CompletionScenarioID string
	CompletionEpoch      int
	CompletionAction     string
	CompletionAttempt    int
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
