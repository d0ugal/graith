package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/d0ugal/graith/internal/store"
)

var orchestratorBackoffDelays = []time.Duration{
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
	60 * time.Second,
	300 * time.Second,
}

const orchestratorStableThreshold = 60 * time.Second
const orchestratorFreshStartThreshold = 3

func (sm *SessionManager) orchestratorScratchDir() string {
	return filepath.Join(sm.paths.DataDir, "orchestrator", "scratch")
}

func (sm *SessionManager) orchestratorTmpDir() string {
	return filepath.Join(sm.paths.DataDir, "orchestrator", "tmp")
}

// systemSessionEnabledInConfig reports whether the config feature that owns this
// system session is currently enabled. A system session whose feature has been
// disabled in config is an orphan (the daemon no longer manages it) and may be
// deleted directly; an enabled one is daemon-managed and must be turned off in
// config.toml first. Callers must hold sm.mu (read or write) so the sm.cfg
// pointer read is race-free.
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
	agentName := orchCfg.AgentName()

	agent, ok := cfgSnap.Agents[agentName]
	if !ok {
		return SessionState{}, fmt.Errorf("orchestrator agent %q not found in config", agentName)
	}

	sandboxed, err := sm.resolveSandboxFromConfig(cfgSnap, agentName)
	if err != nil {
		return SessionState{}, fmt.Errorf("orchestrator sandbox: %w", err)
	}

	if !sandboxed {
		return SessionState{}, fmt.Errorf("orchestrator requires sandbox but sandbox is not available — install safehouse and enable sandbox in config")
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
	if forcesID(agentName) {
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

	promptArgs := sm.buildOrchestratorPrompt(agentName, orchCfg, cfgSnap.AvailableRepoPaths(), cfgSnap.Notifications.Enabled)
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

	command, finalArgs, wrapErr := sandbox.Wrap(agent.Command, expandedArgs, opts)
	if wrapErr != nil {
		sm.rollbackOrchestratorCreate(id)
		return SessionState{}, fmt.Errorf("sandbox wrap: %w", wrapErr)
	}

	sm.log.Info("sandboxing orchestrator", "id", id,
		"command", command, "read_dirs", opts.ReadDirs, "write_dirs", opts.WriteDirs,
		"unix_sockets", opts.UnixSockets,
		"workdir", opts.WorktreeDir)

	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    command,
		Args:       finalArgs,
		Dir:        scratchDir,
		Env:        env,
		Rows:       24,
		Cols:       80,
		LogPath:    logPath,
		MaxLogSize: 100 * 1024 * 1024,
	})
	if err != nil {
		sm.rollbackOrchestratorCreate(id)
		return SessionState{}, fmt.Errorf("start orchestrator pty: %w", err)
	}

	sm.mu.Lock()
	if _, ok := sm.state.Sessions[id]; !ok {
		sm.mu.Unlock()

		_ = ptySess.Kill()
		ptySess.Close()

		return SessionState{}, fmt.Errorf("orchestrator session deleted during creation")
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

		_ = ptySess.Kill()
		ptySess.Close()

		return SessionState{}, fmt.Errorf("persist orchestrator state: %w", err)
	}

	result := cloneSessionState(sess)
	sm.mu.Unlock()

	go sm.watchSession(id, ptySess)

	sm.log.Info("orchestrator session created", "id", id)

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

func (sm *SessionManager) buildOrchestratorPrompt(agentName string, orchCfg config.OrchestratorConfig, repoPaths []string, notifyEnabled bool) []string {
	if agentName != "claude" {
		return nil
	}

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

	if prompt == "" {
		return nil
	}

	return []string{"--append-system-prompt", prompt}
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

	switch {
	case orchID == "":
		sm.log.Info("creating orchestrator session")

		if _, err := sm.createOrchestrator(ctx); err != nil {
			sm.log.Error("failed to create orchestrator", "err", err)
		}

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
		if _, err := sm.Resume(orchID, 24, 80); err != nil {
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
		if _, err := sm.Resume(orchID, 24, 80); err != nil {
			sm.log.Error("failed to resume user-stopped orchestrator on boot", "id", orchID, "err", err)
		}

	case orchStatus == StatusStopped || orchStatus == StatusErrored:
		sm.log.Info("resuming orchestrator", "id", orchID, "status", orchStatus)

		//nolint:contextcheck // session lifecycle is intentionally detached from the daemon-boot ctx: the orchestrator session must persist, so Resume uses its own bounded background timeouts rather than this transient ctx.
		if _, err := sm.Resume(orchID, 24, 80); err != nil {
			sm.log.Error("failed to resume orchestrator", "id", orchID, "err", err)
		}
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

	sm.mu.RUnlock()

	if stopReason == StopReasonUser || stopReason == StopReasonIdle || stopReason == StopReasonShutdown {
		sm.log.Info("orchestrator stopped, not auto-restarting", "id", id, "reason", stopReason)
		return
	}

	if time.Since(lastStarted) >= orchestratorStableThreshold {
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.BackoffLevel = 0
		}

		_ = sm.saveState()
		sm.mu.Unlock()

		backoffLevel = 0
	}

	delayIdx := backoffLevel
	if delayIdx >= len(orchestratorBackoffDelays) {
		delayIdx = len(orchestratorBackoffDelays) - 1
	}

	delay := orchestratorBackoffDelays[delayIdx]

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

	if backoffLevel+1 >= orchestratorFreshStartThreshold {
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok && forcesID(s.Agent) {
			s.AgentSessionID = newAgentSessionID()
			s.FreshStart = true

			sm.log.Info("regenerating orchestrator agent session ID for fresh start", "id", id)
		}

		_ = sm.saveState()
		sm.mu.Unlock()
	}

	//nolint:contextcheck // session lifecycle is intentionally detached from the restart-scheduling ctx: the orchestrator session must persist, so Resume uses its own bounded background timeouts rather than this transient ctx.
	if _, err := sm.Resume(id, 24, 80); err != nil {
		sm.log.Error("failed to auto-restart orchestrator", "id", id, "err", err)
	} else {
		sm.log.Info("orchestrator auto-restarted", "id", id)
	}
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
)
