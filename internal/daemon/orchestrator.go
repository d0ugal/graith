package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/store"
)

func (sm *SessionManager) orchestratorScratchDir() string {
	return filepath.Join(sm.paths.DataDir, "orchestrator", "scratch")
}

func (sm *SessionManager) orchestratorTmpDir() string {
	return filepath.Join(sm.paths.DataDir, "orchestrator", "tmp")
}

// systemSessionEnabledInConfig reports whether the config feature that owns this
// system session is currently enabled. It protects system sessions from being
// placed in recoverable trash, which would leave their declarative name present
// and block reconciliation. A direct orchestrator delete instead takes the
// explicit hard-reset path in handleDelete. Callers must hold sm.mu (read or
// write) so the sm.cfg pointer read is race-free.
func (sm *SessionManager) systemSessionEnabledInConfig(s *SessionState) bool {
	switch s.SystemKind {
	case SystemKindOrchestrator:
		return sm.cfg.Orchestrator.Enabled
	default:
		// Unknown system kind: treat as managed so we never orphan-delete
		// something we don't understand.
		return true
	}
}

func (sm *SessionManager) findOrchestratorID() string {
	for id, s := range sm.state.Sessions {
		if s.SystemKind == SystemKindOrchestrator {
			return id
		}
	}

	return ""
}

func (sm *SessionManager) createOrchestrator(ctx context.Context) (SessionState, error) {
	cfgSnap := sm.Config()
	orchCfg := cfgSnap.Orchestrator
	agentName := orchCfg.AgentName(cfgSnap.DefaultAgent)

	agent, ok := cfgSnap.Agents[agentName]
	if !ok {
		return SessionState{}, fmt.Errorf("orchestrator agent %q not found in config", agentName)
	}

	sandboxed, err := sm.resolveSandboxFromConfig(cfgSnap, agentName)
	if err != nil {
		return SessionState{}, fmt.Errorf("orchestrator sandbox: %w", err)
	}

	if !sandboxed {
		return SessionState{}, errors.New("orchestrator requires sandbox but sandbox is not available — install safehouse and enable sandbox in config")
	}

	scratchDir := sm.orchestratorScratchDir()
	tmpDir := sm.orchestratorTmpDir()

	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		return SessionState{}, fmt.Errorf("create orchestrator scratch dir: %w", err)
	}

	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return SessionState{}, fmt.Errorf("create orchestrator tmp dir: %w", err)
	}

	id := generateID()

	token, err := generateToken()
	if err != nil {
		return SessionState{}, fmt.Errorf("generate orchestrator token: %w", err)
	}

	agentSessionID := ""
	if cfgSnap.Agents[agentName].ForcesNativeID() {
		agentSessionID = newAgentSessionID()
	}

	sm.mu.Lock()

	if existing := sm.findOrchestratorID(); existing != "" {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("orchestrator session already exists: %s", existing)
	}

	now := time.Now().UTC()
	sessState := &SessionState{
		ID:              id,
		Name:            OrchestratorSessionName,
		Agent:           agentName,
		AgentSessionID:  agentSessionID,
		NativeIDLocator: agent.NativeIDLocator(),
		Model:           orchCfg.Model,
		SystemKind:      SystemKindOrchestrator,
		Status:          StatusCreating,
		CreatedAt:       now,
		StatusChangedAt: now,
		LastStartedAt:   now,
		Token:           token,
	}

	sm.state.Sessions[id] = sessState
	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		sm.mu.Unlock()

		return SessionState{}, fmt.Errorf("persist orchestrator state: %w", err)
	}

	sandboxMerged := cfgSnap.OrchestratorSandboxMerged(agentName)
	sm.mu.Unlock()

	vars := config.TemplateVars{
		Username:       "orchestrator",
		AgentSessionID: agentSessionID,
		SessionName:    OrchestratorSessionName,
		SessionID:      id,
		WorktreePath:   scratchDir,
		Model:          orchCfg.Model,
	}

	expandedArgs, err := config.ExpandSlice(agent.Args, vars)
	if err != nil {
		sm.rollbackOrchestratorCreate(id)
		return SessionState{}, fmt.Errorf("expand orchestrator agent args: %w", err)
	}

	promptArgs, err := sm.buildOrchestratorPromptFromConfig(cfgSnap, agentName, orchCfg, cfgSnap.AvailableRepoPaths(), cfgSnap.Notifications.Enabled, scratchDir)
	if err != nil {
		sm.rollbackOrchestratorCreate(id)
		return SessionState{}, fmt.Errorf("build orchestrator prompt: %w", err)
	}

	expandedArgs = append(expandedArgs, promptArgs...)

	logPath := filepath.Join(sm.paths.LogDir, id+".log")

	env := make(map[string]string, len(agent.Env)+6)
	for k, v := range agent.Env {
		env[k] = v
	}

	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = OrchestratorSessionName
	env["GRAITH_AGENT_TYPE"] = agentName
	env["GRAITH_TOKEN"] = token
	env["GRAITH_TMPDIR"] = tmpDir

	env["TMPDIR"] = tmpDir
	if sm.paths.Profile != "" {
		env["GRAITH_PROFILE"] = sm.paths.Profile
	}

	merged := sandboxMerged
	merged.ReadDirs = expandPaths(merged.ReadDirs, sm.log, "read")
	merged.WriteDirs = expandPaths(merged.WriteDirs, sm.log, "write")
	mergedSandbox := &merged

	envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_AGENT_TYPE", "GRAITH_TMPDIR", "TMPDIR", "TERM"}
	for k := range agent.Env {
		envKeys = append(envKeys, k)
	}

	for k := range env {
		envKeys = append(envKeys, k)
	}

	opts := sm.sandboxOptsFromConfig(merged, id, scratchDir, agent.Command, envKeys, false)
	opts.WriteDirs = append(opts.WriteDirs, tmpDir)
	opts.WriteDirs = append(opts.WriteDirs, store.SharedStorePath(sm.paths.DataDir))
	opts.WriteDirs = append(opts.WriteDirs, scratchDir)

	command, finalArgs, wrapErr := sm.wrapSessionCommand(agent.Command, expandedArgs, opts)
	if wrapErr != nil {
		sm.rollbackOrchestratorCreate(id)
		return SessionState{}, fmt.Errorf("sandbox wrap: %w", wrapErr)
	}

	sm.log.Info("sandboxing orchestrator", "id", id,
		"command", command, "read_dirs", opts.ReadDirs, "write_dirs", opts.WriteDirs,
		"unix_sockets", opts.UnixSockets,
		"workdir", opts.WorktreeDir)

	// Test seam: pause here so a concurrent-reload regression can swap sm.cfg
	// before the lifecycle values below are read, proving they still come from the
	// launch-time snapshot rather than the reloaded generation (issue #1243).
	if sm.launchPhase2Hook != nil {
		sm.launchPhase2Hook("orchestrator", cfgSnap)
	}

	// Reuse the single config snapshot captured at the top of createOrchestrator
	// rather than re-reading sm.Config() here: a reload landing mid-launch must
	// not let one orchestrator start combine agent/geometry/log values from two
	// different config generations (issue #1243 round-4).
	lc := cfgSnap.Lifecycle

	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    command,
		Args:       finalArgs,
		Dir:        scratchDir,
		Env:        env,
		Rows:       lc.DefaultRowsOrDefault(),
		Cols:       lc.DefaultColsOrDefault(),
		LogPath:    logPath,
		MaxLogSize: lc.MaxLogBytesOrDefault(),
		InputDelay: lc.InputDelayDuration(),
		Logger:     sm.log,
	})
	if err != nil {
		sm.rollbackOrchestratorCreate(id)
		return SessionState{}, fmt.Errorf("start orchestrator pty: %w", err)
	}

	sm.mu.Lock()
	if _, ok := sm.state.Sessions[id]; !ok {
		sm.mu.Unlock()

		sm.logStopping(id, "", "rollback", "orchestrator-rollback", ptySess)
		_ = ptySess.Kill()
		ptySess.Close()

		return SessionState{}, errors.New("orchestrator session deleted during creation")
	}

	sess := sm.state.Sessions[id]
	sess.Status = StatusRunning
	sess.StatusChangedAt = time.Now()

	sess.PID = ptySess.Cmd.Process.Pid
	if st, err := grpty.ProcessStartTime(sess.PID); err == nil {
		sess.PIDStartTime = st
	}

	sess.Sandboxed = true
	sess.SandboxConfig = mergedSandbox
	sess.LastStartedAt = time.Now()
	sess.CreationCfg = &CreationConfig{
		Agent:         agent,
		SandboxConfig: sandboxMerged,
	}

	sm.sessions[id] = ptySess
	sm.tokenIndex[token] = id

	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		delete(sm.sessions, id)
		delete(sm.tokenIndex, token)
		sm.mu.Unlock()

		sm.logStopping(id, "", "rollback", "orchestrator-rollback", ptySess)
		_ = ptySess.Kill()
		ptySess.Close()

		return SessionState{}, fmt.Errorf("persist orchestrator state: %w", err)
	}

	result := cloneSessionState(sess)
	sm.mu.Unlock()

	// A reload can land between the snapshot above and this insertion, so
	// reconcile the live input delay against the currently-published config
	// generation now that the driver is discoverable in sm.sessions (issue #1294).
	sm.reconcileLaunchedInputDelay(ptySess)

	go sm.watchSession(id, ptySess)

	sm.log.Info("orchestrator session created",
		"id", id, "pid", result.PID, "pgid", ptySess.Pgid())

	return result, nil
}

func (sm *SessionManager) rollbackOrchestratorCreate(id string) {
	sm.mu.Lock()
	delete(sm.state.Sessions, id)
	_ = sm.saveState()
	sm.mu.Unlock()

	// createOrchestrator wraps via sandboxOptsFromConfig, which may have written
	// a nono profile before this error path ran; state is now gone so no later
	// Delete would remove it. Mirrors cleanupOnError/forkCleanup in Create/Fork.
	_ = os.Remove(sm.nonoProfilePath(id))
	_ = os.Remove(sm.safehouseFragmentPath(id))
}

// buildOrchestratorPrompt assembles the orchestrator's system prompt from
// config (inline prompt, prompt_file, available repos, notifications) and then
// routes it through the agent-aware promptInjectionArgs adapter so that a
// Codex, Cursor, or custom orchestrator agent receives the injection mechanism
// it actually supports instead of Claude's --append-system-prompt flag. For
// Cursor the rule file is written under worktreePath (the orchestrator scratch
// dir); an agent with no supported injection method silently gets no prompt
// args. agents.<name>.inject_prompt controls only the generic top-level
// agent_prompt: when it is false, this still delivers the orchestrator-specific
// role/repository/notification prompt. It returns an error only when a
// side-effecting injection (e.g. writing the Cursor rule) fails.
func (sm *SessionManager) buildOrchestratorPrompt(agentName string, orchCfg config.OrchestratorConfig, repoPaths []string, notifyEnabled bool, worktreePath string) ([]string, error) {
	return sm.buildOrchestratorPromptFromConfig(sm.Config(), agentName, orchCfg, repoPaths, notifyEnabled, worktreePath)
}

func (sm *SessionManager) buildOrchestratorPromptFromConfig(cfgSnapshot *config.Config, agentName string, orchCfg config.OrchestratorConfig, repoPaths []string, notifyEnabled bool, worktreePath string) ([]string, error) {
	prompt := orchCfg.Prompt

	if orchCfg.PromptFile != "" {
		expanded := config.ExpandPath(orchCfg.PromptFile)

		data, err := os.ReadFile(expanded)
		if err != nil {
			sm.log.Warn("failed to read orchestrator prompt_file", "path", expanded, "err", err)
		} else {
			if prompt != "" {
				prompt += "\n\n"
			}

			prompt += string(data)
		}
	}

	if section := orchestratorRepoPathsSection(repoPaths); section != "" {
		if prompt != "" {
			prompt += "\n\n"
		}

		prompt += section
	}

	if notifyEnabled {
		if prompt != "" {
			prompt += "\n\n"
		}

		prompt += orchestratorNotificationsSection()
	}

	// The generic operating prompt and the orchestrator role prompt have separate
	// configuration contracts. inject_prompt opts this agent out of agent_prompt,
	// but must never silently remove the role context that tells a privileged
	// orchestrator what it is and which repos/capabilities it owns (#1292).
	agent := cfgSnapshot.Agents[agentName]
	if agent.PromptInjectionEnabled() && cfgSnapshot.AgentPrompt != "" {
		if prompt == "" {
			prompt = cfgSnapshot.AgentPrompt
		} else {
			prompt = cfgSnapshot.AgentPrompt + "\n\n" + prompt
		}
	}

	return promptInjectionArgs(agentName, agent, prompt, worktreePath)
}

// orchestratorNotificationsSection tells the orchestrator it can proactively
// get the human's attention with `gr notify`, and when it is (and isn't)
// appropriate — the orchestrator is the primary sender, so this belongs in its
// system prompt rather than being left for it to discover.
func orchestratorNotificationsSection() string {
	return "## Notifying the human\n\n" +
		"You can send a desktop/push notification to the human with " +
		"`gr notify \"<message>\" --priority low|normal|high`. Unlike an inbox " +
		"message, this proactively interrupts them, so use it sparingly and only " +
		"for things genuinely worth their attention — a finished briefing, a CI " +
		"failure that needs a decision, a blocked task. Use `--priority high` " +
		"(plays a sound, bypasses quiet hours and rate limits) only for urgent " +
		"items. Routine progress belongs in `gr status`, not a notification."
}

// orchestratorRepoPathsSection renders the configured repo paths as a prompt
// section so the orchestrator is told which repos are available instead of
// having to discover them. It returns "" when no repo paths are configured.
func orchestratorRepoPathsSection(repoPaths []string) string {
	if len(repoPaths) == 0 {
		return ""
	}

	var b strings.Builder

	b.WriteString("## Available repositories\n\n")
	b.WriteString("These repository paths are configured for use with `gr new --repo <path>`. " +
		"Some entries are concrete repos and some are parent directories that " +
		"contain repos, so pass either a listed path or a repo found under one:\n\n")

	for _, p := range repoPaths {
		b.WriteString("- ")
		b.WriteString(p)
		b.WriteString("\n")
	}

	return b.String()
}

func (sm *SessionManager) ensureOrchestrator(ctx context.Context) {
	sm.mu.RLock()
	enabled := sm.cfg.Orchestrator.Enabled
	sm.mu.RUnlock()

	if !enabled {
		return
	}

	sm.mu.RLock()
	orchID := sm.findOrchestratorID()

	var (
		orchStatus     SessionStatus
		orchStopReason string
	)

	if orchID != "" {
		if s := sm.state.Sessions[orchID]; s != nil {
			orchStatus = s.Status
			orchStopReason = s.StopReason
		}
	}

	_, hasLivePTY := sm.sessions[orchID]
	sm.mu.RUnlock()

	// Default launch geometry for the orchestrator (no attaching client yet); a
	// client resizes on attach.
	lc := sm.Config().Lifecycle
	defRows, defCols := lc.DefaultRowsOrDefault(), lc.DefaultColsOrDefault()

	switch {
	case orchID == "":
		sm.reconcileOrchestratorPresence(ctx)

	case orchStatus == StatusRunning && hasLivePTY:
		sm.log.Info("orchestrator already running", "id", orchID)

	case orchStatus == StatusRunning && !hasLivePTY:
		sm.log.Info("orchestrator marked running but no live PTY, recovering", "id", orchID)
		sm.mu.Lock()
		if s := sm.state.Sessions[orchID]; s != nil {
			s.Status = StatusStopped
			s.StatusChangedAt = time.Now()
			s.StopReason = StopReasonCrash
			s.PID = 0
			s.PIDStartTime = 0
		}

		_ = sm.saveState()
		sm.mu.Unlock()

		//nolint:contextcheck // session lifecycle is intentionally detached from the daemon-boot ctx: the orchestrator session must persist, so Resume uses its own bounded background timeouts rather than this transient ctx.
		if _, err := sm.Resume(orchID, defRows, defCols); err != nil {
			sm.log.Error("failed to resume orchestrator after recovery", "id", orchID, "err", err)
		}

	case orchStatus == StatusStopped && orchStopReason == StopReasonUser:
		sm.log.Info("orchestrator stopped by user, clearing stop reason on boot", "id", orchID)
		sm.mu.Lock()
		if s := sm.state.Sessions[orchID]; s != nil {
			s.StopReason = ""
		}

		_ = sm.saveState()
		sm.mu.Unlock()

		//nolint:contextcheck // session lifecycle is intentionally detached from the daemon-boot ctx: the orchestrator session must persist, so Resume uses its own bounded background timeouts rather than this transient ctx.
		if _, err := sm.Resume(orchID, defRows, defCols); err != nil {
			sm.log.Error("failed to resume user-stopped orchestrator on boot", "id", orchID, "err", err)
		}

	case orchStatus == StatusStopped || orchStatus == StatusErrored:
		sm.log.Info("resuming orchestrator", "id", orchID, "status", orchStatus)

		//nolint:contextcheck // session lifecycle is intentionally detached from the daemon-boot ctx: the orchestrator session must persist, so Resume uses its own bounded background timeouts rather than this transient ctx.
		if _, err := sm.Resume(orchID, defRows, defCols); err != nil {
			sm.log.Error("failed to resume orchestrator", "id", orchID, "err", err)
		}
	}
}

// reconcileOrchestratorPresence recreates the declarative orchestrator when it
// is enabled but absent. It deliberately does nothing to an existing stopped
// orchestrator: explicit stops are preserved until the daemon starts again or
// the user resumes the session.
func (sm *SessionManager) reconcileOrchestratorPresence(ctx context.Context) {
	sm.reconcileOrchestratorPresenceWith(ctx, sm.createOrchestrator)
}

// reconcileOrchestratorPresenceWith contains the presence decision behind an
// injectable creation boundary so the delete/recreate behavior can be tested
// without requiring a sandbox backend in CI.
func (sm *SessionManager) reconcileOrchestratorPresenceWith(
	ctx context.Context,
	create func(context.Context) (SessionState, error),
) {
	sm.mu.RLock()
	enabled := sm.cfg.Orchestrator.Enabled
	orchID := sm.findOrchestratorID()
	sm.mu.RUnlock()

	if !enabled || orchID != "" {
		return
	}

	sm.log.Info("creating missing orchestrator session")

	if _, err := create(ctx); err != nil {
		sm.log.Error("failed to create orchestrator", "err", err)
	}
}

// RunOrchestratorReconcileLoop restores the config-managed orchestrator after a
// successful delete emits a presence kick. Crash restarts remain in
// orchestratorSupervisor so their backoff cannot delay a deliberate reset.
func (sm *SessionManager) RunOrchestratorReconcileLoop(ctx context.Context) {
	runOrchestratorReconcileLoop(
		ctx,
		sm.orchestratorKickCh,
		sm.reconcileOrchestratorPresence,
	)
}

func runOrchestratorReconcileLoop(
	ctx context.Context,
	kicks <-chan struct{},
	reconcile func(context.Context),
) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-kicks:
			reconcile(ctx)
		}
	}
}

func (sm *SessionManager) notifyOrchestratorReconcile() {
	if sm.orchestratorKickCh == nil {
		return
	}

	select {
	case sm.orchestratorKickCh <- struct{}{}:
	default:
		// Presence kicks are level-triggered: one queued wake-up is enough to
		// observe the latest config and session state.
	}
}

func (sm *SessionManager) orchestratorSupervisor(ctx context.Context, exitCh <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-exitCh:
			sm.handleOrchestratorExit(ctx, id)
		}
	}
}

func (sm *SessionManager) handleOrchestratorExit(ctx context.Context, id string) {
	sm.mu.RLock()

	sess, ok := sm.state.Sessions[id]
	if !ok || sess.SystemKind != SystemKindOrchestrator {
		sm.mu.RUnlock()
		return
	}

	stopReason := sess.StopReason
	backoffLevel := sess.BackoffLevel
	lastStarted := sess.LastStartedAt
	restartCfg := sm.cfg.Orchestrator.Restart

	sm.mu.RUnlock()

	if stopReason == StopReasonUser || stopReason == StopReasonIdle || stopReason == StopReasonShutdown {
		sm.log.Info("orchestrator stopped, not auto-restarting", "id", id, "reason", stopReason)
		return
	}

	if time.Since(lastStarted) >= restartCfg.StableResetDuration() {
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.BackoffLevel = 0
		}

		_ = sm.saveState()
		sm.mu.Unlock()

		backoffLevel = 0
	}

	// Reconciliation loop toward the desired enabled state. A failed Resume no
	// longer abandons an orchestrator that config still says should exist: it
	// advances to the next backoff level and tries again, so a transient failure
	// self-heals on a later attempt (issue #1284). The delay grows geometrically
	// and is capped by max_backoff, so retries never become a restart storm, and
	// ctx cancellation (daemon shutdown) ends the loop promptly.
	for {
		delay := orchestratorRestartDelay(restartCfg, backoffLevel)

		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.BackoffLevel = backoffLevel + 1
		}

		_ = sm.saveState()
		sm.mu.Unlock()

		sm.log.Info("scheduling orchestrator restart", "id", id, "delay", delay, "backoff_level", backoffLevel+1)

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		sm.mu.RLock()

		sess, ok = sm.state.Sessions[id]
		if !ok {
			sm.mu.RUnlock()
			return
		}

		enabled := sm.cfg.Orchestrator.Enabled
		_, hasLivePTY := sm.sessions[id]
		currentReason := sess.StopReason

		sm.mu.RUnlock()

		if !enabled || hasLivePTY || currentReason == StopReasonUser || currentReason == StopReasonIdle || currentReason == StopReasonShutdown {
			sm.log.Info("orchestrator restart preconditions not met, skipping", "id", id)
			return
		}

		if backoffLevel+1 >= restartCfg.FreshStartThresholdOrDefault() {
			// Snapshot config off-lock: the mutation below holds sm.mu, under which
			// sm.Config() (an RLock) must not be called.
			cfg := sm.Config()

			sm.mu.Lock()
			if s, ok := sm.state.Sessions[id]; ok && cfg.Agents[s.Agent].ForcesNativeID() {
				s.AgentSessionID = newAgentSessionID()
				s.FreshStart = true

				sm.log.Info("regenerating orchestrator agent session ID for fresh start", "id", id)
			}

			_ = sm.saveState()
			sm.mu.Unlock()
		}

		lc := sm.Config().Lifecycle

		//nolint:contextcheck // session lifecycle is intentionally detached from the restart-scheduling ctx: the orchestrator session must persist, so Resume uses its own bounded background timeouts rather than this transient ctx.
		if _, err := sm.resumeOrchestrator(id, lc.DefaultRowsOrDefault(), lc.DefaultColsOrDefault()); err != nil {
			// Retry rather than give up: advance the backoff level so the next
			// attempt waits longer, and loop. The precondition re-check above will
			// still bail if the orchestrator is disabled or already live.
			sm.log.Error("failed to auto-restart orchestrator; retrying after backoff", "id", id, "err", err, "next_backoff_level", backoffLevel+1)

			backoffLevel++

			continue
		}

		sm.log.Info("orchestrator auto-restarted", "id", id)

		return
	}
}

// resumeOrchestrator resumes the orchestrator session. It is a thin seam over
// Resume so the supervisor's retry loop (issue #1284) can be driven with an
// injected transient failure in tests; production leaves sm.resumeFn nil.
func (sm *SessionManager) resumeOrchestrator(id string, rows, cols uint16) (SessionState, error) {
	if sm.resumeFn != nil {
		return sm.resumeFn(id, rows, cols)
	}

	return sm.Resume(id, rows, cols)
}

// orchestratorRestartDelay is the supervisor's final safety floor. Config load
// rejects non-positive restart policy, and the accessors fall back defensively,
// but a directly-constructed or mutated config must still never create an
// immediate restart loop.
func orchestratorRestartDelay(cfg config.OrchestratorRestartConfig, level int) time.Duration {
	delay := cfg.DelayForLevel(level)
	if delay <= 0 {
		return config.OrchestratorInitialBackoffDefault
	}

	return delay
}

func (sm *SessionManager) notifyOrchestratorExit(id string) {
	if sm.orchestratorExitCh != nil {
		select {
		case sm.orchestratorExitCh <- id:
		default:
			sm.log.Warn("orchestrator exit notification dropped, supervisor busy", "id", id)
		}
	}
}

// StopReason constants
const (
	StopReasonCrash    = "crash"
	StopReasonIdle     = "idle"
	StopReasonUser     = "user"
	StopReasonShutdown = "shutdown"
	StopReasonWatchdog = "watchdog"
	// StopReasonDelete labels a daemon-initiated kill that is part of a
	// delete/purge teardown. It is used only for the "stopping session" audit
	// line (issue #1104); a deleted session is removed from state, so its exit
	// is ignored and never reaches the "session exited" log path.
	StopReasonDelete = "delete"
	// StopReasonConvert labels the headless process stop that
	// ConvertToInteractive performs before relaunching the session as an
	// interactive PTY (headless convert-on-attach, issue #1075/#1137). The stop
	// is caused by graith, not a crash, so it must not be attributed as one.
	StopReasonConvert = "convert"
)

// mapSessionEndReason maps Claude's raw SessionEnd reason onto one of graith's
// StopReason constants, returning ok=false when the reason is not proof of a
// clean process exit. Only process-ending reasons map:
//
//   - logout / prompt_input_exit -> StopReasonUser (the human ended the session)
//   - clear / resume             -> ("", false): these are logical-session
//     transitions that do NOT terminate the PTY process, so they set no reason
//   - other / anything else      -> ("", false): not proof of a clean exit, so
//     the process-exit path falls back to StopReasonCrash
//
// A raw Claude string must never be assigned to StopReason directly — that field
// also drives restart suppression, stop-summary text, and trigger auto-cleanup,
// so a reason may only become a StopReason through this explicit mapping.
func mapSessionEndReason(reason string) (string, bool) {
	switch reason {
	case "logout", "prompt_input_exit":
		return StopReasonUser, true
	default:
		return "", false
	}
}
