package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/d0ugal/graith/internal/store"
)

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
		return SessionState{}, errors.New("orchestrator requires sandbox but sandbox is not available — install safehouse and enable sandbox in config")
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

		promptArgs, err := sm.buildOrchestratorPrompt(sessAgent, orchCfg, repoPaths, notifyEnabled, sm.orchestratorScratchDir())
		if err != nil {
			sm.log.Warn("failed to build orchestrator prompt", "session_id", id, "err", err)
		} else {
			expandedArgs = append(expandedArgs, promptArgs...)
		}
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

		return SessionState{}, errors.New("session was deleted during resume")
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
