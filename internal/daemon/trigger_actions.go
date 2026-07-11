package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/d0ugal/graith/internal/scenariofile"
)

const triggerCommandOutputCap = 4096

// actionMessage authors a fixed body and routes it via the deliver block.
func (sm *SessionManager) actionMessage(ctx context.Context, t *config.TriggerConfig, fc fireContext) (string, error) {
	vars := sm.triggerVars(t, fc)

	body, err := config.ExpandTrigger(t.Action.Body, vars)
	if err != nil {
		return "", err
	}
	// message actions have no repo; store keys must be shared: (validated).
	sm.deliver(ctx, t.Action.Deliver, body, "", vars)

	return "published", nil
}

// actionCommand runs a command rooted at its execution root (repo for schedule,
// the bound worktree for watch), captures output, and delivers it. Watch
// commands run with the worktree mounted read-only and a separate writable
// scratch dir, so a command cannot mutate the tree it watches (the feedback-loop
// guarantee) — the same mechanism --mirror uses.
func (sm *SessionManager) actionCommand(ctx context.Context, t *config.TriggerConfig, fc fireContext) (string, error) {
	execRoot := t.Action.RepoPath()
	readOnlyRoot := false

	if t.IsWatch() {
		execRoot = fc.worktree
		readOnlyRoot = true
	}

	if execRoot == "" {
		return "", fmt.Errorf("command action has no execution root")
	}

	cmdStr := t.Action.Command
	name, args := "sh", []string{"-c", cmdStr}

	// For a read-only watch command, writes go to a per-run scratch dir; the
	// process still runs (cwd) in the worktree so it can read the code.
	cwd := execRoot

	var scratch string

	if readOnlyRoot {
		scratchParent := filepath.Join(sm.paths.DataDir, "scratch")
		if err := os.MkdirAll(scratchParent, 0o700); err != nil {
			return "", fmt.Errorf("create scratch parent: %w", err)
		}

		s, err := os.MkdirTemp(scratchParent, "trigcmd-")
		if err != nil {
			// Fail closed: without a writable scratch dir the read-only guarantee
			// can't be established, so don't run the command with a broken profile.
			return "", fmt.Errorf("create command scratch dir: %w", err)
		}

		scratch = s
		defer func() { _ = os.RemoveAll(scratch) }()
	}

	cfg := sm.Config()

	if t.Action.Sandboxed() {
		wrapped, wargs, ok, err := sm.buildCommandSandbox(cfg, &t.Action, commandSandbox{
			cwd:          cwd,
			readOnlyRoot: readOnlyRoot,
			worktree:     execRoot,
			scratch:      scratch,
		}, name, args)
		if err != nil {
			return "", fmt.Errorf("sandbox: %w", err)
		}

		if !ok {
			// Fail closed: sandbox requested but not enforceable.
			return "", fmt.Errorf("command sandbox not available; set action.sandbox = false to run unconfined")
		}

		name, args = wrapped, wargs
	}

	runCtx, cancel := context.WithTimeout(ctx, t.Action.TimeoutDuration())
	defer cancel()

	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Dir = cwd
	cmd.Env = triggerCommandEnv(scratch)

	var out bytes.Buffer

	cmd.Stdout = &out
	cmd.Stderr = &out

	runErr := cmd.Run()
	exit := 0

	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}

	body := fmt.Sprintf("$ %s\n(exit %d)\n\n%s", cmdStr, exit, truncateOutput(out.String()))
	sm.deliver(ctx, t.Action.Deliver, body, t.Action.RepoPath(), sm.triggerVars(t, fc))

	// A non-zero exit / timeout is surfaced as an error so it lands in LastError,
	// even though the captured output is still delivered.
	if exit != 0 {
		return fmt.Sprintf("exit %d", exit), fmt.Errorf("command exited %d", exit)
	}

	return "exit 0", nil
}

// commandSandbox carries the resolved paths for a command action's sandbox.
type commandSandbox struct {
	cwd          string // process working directory
	readOnlyRoot bool   // watch: worktree read-only + scratch writable
	worktree     string // the read-only worktree (watch only)
	scratch      string // the writable scratch dir (watch only)
}

// buildCommandSandbox wraps a command in a MINIMAL, dedicated command profile —
// it does NOT inherit the global agent read/write grants, only the backend
// selection, signal mode, and the action's own sandbox_config grants. Network is
// blocked by default unless sandbox_config opens it. Returns ok=false when the
// sandbox can't be enforced (caller fails closed).
func (sm *SessionManager) buildCommandSandbox(cfg *config.Config, a *config.ActionConfig, cs commandSandbox, name string, args []string) (string, []string, bool, error) {
	// Minimal base: enforcement selection only, not global agent grants.
	base := config.SandboxConfig{
		Enabled:    cfg.Sandbox.Enabled,
		Backend:    cfg.Sandbox.Backend,
		Command:    cfg.Sandbox.Command,
		SignalMode: cfg.Sandbox.SignalMode,
	}

	merged := base
	if a.SandboxConfig != nil {
		merged = base.Merge(*a.SandboxConfig)
	}

	if !merged.Enabled {
		return "", nil, false, nil
	}

	avail, err := validateSandboxBackend(merged, "trigger command")
	if err != nil {
		return "", nil, false, err
	}

	if !avail.CanEnforce {
		return "", nil, false, nil
	}

	// Network blocked by default unless the action's sandbox_config opens it.
	netPolicy := networkPolicy(merged.Network)
	if netPolicy == nil {
		netPolicy = &sandbox.NetworkPolicy{Block: true}
	}

	worktreeDir := cs.cwd
	readDirs := merged.ReadDirs
	writeDirs := merged.WriteDirs

	if cs.readOnlyRoot {
		// Read-only worktree, writable scratch: WorktreeDir (the read+write
		// "workdir" grant) is the scratch dir, the worktree is granted read-only,
		// and cwd stays in the worktree (set by the caller). Mirrors the
		// --mirror mechanism.
		worktreeDir = cs.scratch
		readDirs = append(append([]string{}, readDirs...), cs.worktree)
		writeDirs = append(append([]string{}, writeDirs...), cs.scratch)
	}

	opts := sandbox.WrapOpts{
		Backend:        merged.Backend,
		WorktreeDir:    worktreeDir,
		ReadDirs:       readDirs,
		WriteDirs:      writeDirs,
		ReadFiles:      merged.ReadFiles,
		WriteFiles:     merged.WriteFiles,
		EnvKeys:        []string{"PATH", "HOME", "SHELL", "TERM", "LANG", "TMPDIR", "GRAITH_TMPDIR"},
		SignalMode:     merged.SignalMode,
		Network:        netPolicy,
		BackendCommand: merged.Command,
	}

	cmd, wargs, err := sandbox.Wrap(name, args, opts)
	if err != nil {
		return "", nil, false, err
	}

	return cmd, wargs, true, nil
}

func triggerCommandEnv(scratch string) []string {
	keep := map[string]bool{"PATH": true, "HOME": true, "SHELL": true, "TERM": true, "LANG": true, "TMPDIR": true, "GRAITH_TMPDIR": true}

	var env []string

	for _, e := range os.Environ() {
		k, _, ok := strings.Cut(e, "=")
		if ok && keep[k] {
			// Redirect TMPDIR to the writable scratch dir for read-only watch
			// commands, so tool caches don't try to write the read-only worktree.
			if k == "TMPDIR" && scratch != "" {
				continue
			}

			env = append(env, e)
		}
	}

	if scratch != "" {
		env = append(env, "TMPDIR="+scratch)
	}

	return env
}

func truncateOutput(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= triggerCommandOutputCap {
		return s
	}

	return s[:triggerCommandOutputCap] + "\n… (truncated)"
}

// actionSession spawns (or, with ensure, reuses) a session parented to the
// orchestrator. For a watch source with ensure=true this is the ensure-reviewer
// behaviour with per-binding reservation.
func (sm *SessionManager) actionSession(ctx context.Context, t *config.TriggerConfig, fc fireContext) (string, error) {
	_ = ctx // reserved; spawned sessions run their own lifecycle detached from the fire ctx

	orchestratorID := sm.orchestratorID()
	if orchestratorID == "" {
		return "", fmt.Errorf("no orchestrator session; cannot own spawned session")
	}

	vars := sm.triggerVars(t, fc)

	prompt, err := config.ExpandTrigger(t.Action.Prompt, vars)
	if err != nil {
		return "", err
	}

	// Session delivery is best-effort prompt injection: the daemon can't capture a
	// long-running agent's answer, so it instructs the agent to deliver itself.
	instr, err := sm.sessionDeliveryInstruction(t.Action.Deliver, vars)
	if err != nil {
		return "", err
	}

	prompt += instr

	agent := t.Action.Agent
	model := t.Action.Model

	cleanup, err := t.Action.AutoCleanupMode()
	if err != nil {
		return "", err
	}

	idle, err := t.Action.SessionIdleTimeout()
	if err != nil {
		return "", err
	}

	// ensure-reviewer (watch): reuse the binding's existing reactor if alive.
	if t.Action.Ensure && t.IsWatch() {
		if existing := sm.reuseReactor(t.Name, fc.sessionID); existing != "" {
			//nolint:contextcheck // notifyFromDaemon detaches its auto-resume; it must outlive this call.
			_ = sm.notifyFromDaemon(existing, prompt)
			return "messaged " + existing, nil
		}
	}

	repo := t.Action.RepoPath()

	mirror := ""
	if t.IsWatch() {
		// The reactor mirrors the bound session's worktree read-only.
		mirror = fc.sessionID
		repo = "" // mirror sessions don't create their own repo worktree
	}

	name := triggerReactorName(t.Name, fc.sessionName)

	//nolint:contextcheck // Create spawns a PTY-backed session that outlives the fire ctx (matches scenario start).
	sess, err := sm.createTriggerSession(createTriggerReq{
		name:            name,
		agent:           agent,
		repo:            repo,
		prompt:          prompt,
		model:           model,
		parentID:        orchestratorID,
		mirror:          mirror,
		triggerName:     t.Name,
		reactor:         t.Action.Ensure && t.IsWatch(),
		autoCleanup:     cleanup,
		idleTimeoutSecs: int(idle.Seconds()),
	})
	if err != nil {
		return "", err
	}

	if t.Action.Ensure && t.IsWatch() {
		sm.setBindingReactor(t.Name, fc.sessionID, sess.ID)
	}

	return "spawned " + sess.ID, nil
}

func triggerReactorName(triggerName, sessionName string) string {
	base := triggerName
	if sessionName != "" {
		base = triggerName + "-" + sessionName
	}

	if len(base) > 40 {
		base = base[:40]
	}

	return base
}

// actionScenario starts a named scenario, owned by the orchestrator.
func (sm *SessionManager) actionScenario(ctx context.Context, t *config.TriggerConfig) (string, error) {
	_ = ctx // reserved; StartScenario runs its own lifecycle detached from the fire ctx

	orchestratorID := sm.orchestratorID()
	if orchestratorID == "" {
		return "", fmt.Errorf("no orchestrator session; cannot start scenario")
	}

	dir := filepath.Join(filepath.Dir(sm.paths.ConfigFile), "scenarios")
	path := filepath.Join(dir, t.Action.Scenario+".toml")

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read scenario %q: %w", t.Action.Scenario, err)
	}

	sf, err := scenariofile.Parse(data)
	if err != nil {
		return "", err
	}

	inputs, err := scenariofile.SessionInputs(sf)
	if err != nil {
		return "", err
	}

	msg := protocol.ScenarioStartMsg{
		CallerSessionID: orchestratorID,
		Name:            sf.Scenario.Name,
		Goal:            sf.Scenario.Goal,
		Sessions:        inputs,
	}
	//nolint:contextcheck // StartScenario runs its own session lifecycle, detached from the fire ctx.
	st, err := sm.StartScenario(msg, 24, 80)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("started %d sessions", len(st.SessionIDs)), nil
}

// orchestratorID returns the current orchestrator session ID (or "").
func (sm *SessionManager) orchestratorID() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return sm.findOrchestratorID()
}

// reuseReactor returns the binding's reactor session ID if it exists and is not
// soft-deleted (running or stopped — messaging auto-resumes a stopped one).
func (sm *SessionManager) reuseReactor(triggerName, sessionID string) string {
	sm.triggers.mu.Lock()
	b := sm.triggers.bindings[bindingKey(triggerName, sessionID)]

	reactorID := ""
	if b != nil {
		reactorID = b.reactorID
	}
	sm.triggers.mu.Unlock()

	if reactorID == "" {
		return ""
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	s, ok := sm.state.Sessions[reactorID]
	if !ok || s.IsSoftDeleted() {
		return ""
	}

	if s.Status == StatusRunning || s.Status == StatusStopped {
		return reactorID
	}

	return ""
}

// sessionDeliveryInstruction builds the prompt suffix that tells a spawned
// session how to deliver its result (best-effort, since the daemon can't capture
// it). Empty when no deliver block is set. Templated keys are pre-expanded.
func (sm *SessionManager) sessionDeliveryInstruction(d config.DeliverConfig, vars config.TriggerVars) (string, error) {
	if d == (config.DeliverConfig{}) {
		return "", nil
	}

	var b strings.Builder

	b.WriteString("\n\nWhen you finish, deliver your result:")

	if d.Inbox != "" {
		target, err := config.ExpandTrigger(d.Inbox, vars)
		if err != nil {
			return "", err
		}

		fmt.Fprintf(&b, "\n- send it to the %q session's inbox with `gr msg send %s \"…\"`", target, target)
	}

	if d.Topic != "" {
		topic, err := config.ExpandTrigger(d.Topic, vars)
		if err != nil {
			return "", err
		}

		fmt.Fprintf(&b, "\n- publish it to the %q topic with `gr msg pub --topic %s \"…\"`", topic, topic)
	}

	if d.Store != "" {
		key, err := config.ExpandTrigger(d.Store, vars)
		if err != nil {
			return "", err
		}

		fmt.Fprintf(&b, "\n- write it to the store with `gr store put %s \"…\"`", key)
	}

	return b.String(), nil
}

// findReactor locates an existing ensure-reviewer reactor for a (trigger, source)
// binding by its persisted TriggerID/TriggerReactor tags, so reuse survives a
// daemon restart or binding recreation (not just the in-memory binding lifetime).
func (sm *SessionManager) findReactor(triggerName, sourceSessionID string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for id, s := range sm.state.Sessions {
		if s.TriggerReactor && s.TriggerID == triggerName &&
			s.MirrorSourceID == sourceSessionID && !s.IsSoftDeleted() {
			return id
		}
	}

	return ""
}

func (sm *SessionManager) setBindingReactor(triggerName, sessionID, reactorID string) {
	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	if b := sm.triggers.bindings[bindingKey(triggerName, sessionID)]; b != nil {
		b.reactorID = reactorID
	}
}

type createTriggerReq struct {
	name            string
	agent           string
	repo            string
	prompt          string
	model           string
	parentID        string
	mirror          string
	triggerName     string
	reactor         bool
	autoCleanup     string
	idleTimeoutSecs int
}

// createTriggerSession creates a session via sm.Create, tagging it with the
// owning trigger inside the durable creation reservation (so reactor ownership
// survives a crash between create and a separate tag-and-save).
func (sm *SessionManager) createTriggerSession(req createTriggerReq) (SessionState, error) {
	agent := req.agent
	if agent == "" {
		agent = sm.Config().DefaultAgent
	}

	return sm.Create(CreateOpts{
		Name:            req.name,
		AgentName:       agent,
		RepoPath:        req.repo,
		Prompt:          req.prompt,
		Model:           req.model,
		ParentID:        req.parentID,
		Mirror:          req.mirror,
		AgentHooks:      true,
		TriggerID:       req.triggerName,
		TriggerReactor:  req.reactor,
		AutoCleanup:     req.autoCleanup,
		IdleTimeoutSecs: req.idleTimeoutSecs,
		Rows:            24,
		Cols:            80,
	})
}

// autoCleanupStopped soft-deletes a just-stopped trigger-spawned session when
// its recorded auto_cleanup mode calls for it, so finished briefing/report
// sessions don't accumulate. It must run after the exit path has committed the
// stopped status + exit code.
//
// The whole decision-and-mark runs under a single write-lock hold, deliberately
// NOT via the public SoftDelete: SoftDelete re-locks, which would open a
// check→act race where a resume landing between the status check and the delete
// (inbox wake, pr_watch auto-resume, manual `gr resume`) could soft-delete — and
// kill — a session that is running again. Holding the lock across the guard and
// the marker close that window. A genuinely-stopped session has no live PTY
// (the exit watcher already dropped it from sm.sessions and zeroed the PID), so
// none of SoftDelete's kill/detach machinery is needed here — only the marker.
//
// It is a no-op unless the session is (still) a stopped, non-deleted,
// trigger-spawned session whose stop matches the mode; it also declines to run
// when soft delete is disabled (retention <= 0), because turning cleanup into an
// eventual hard purge would violate the "delete never destroys" invariant, and
// when the stop was a daemon shutdown, so `gr daemon restart` preserves
// in-flight sessions.
func (sm *SessionManager) autoCleanupStopped(id string) {
	sm.mu.Lock()

	s, ok := sm.state.Sessions[id]
	// AutoCleanup is only ever set on trigger-spawned sessions, and TriggerID is
	// required as belt-and-braces so cleanup can never reach a manual session.
	if !ok || s.AutoCleanup == "" || s.TriggerID == "" || s.IsSoftDeleted() {
		sm.mu.Unlock()
		return
	}

	// Status may have flipped back to running (resumed) since the exit path ran;
	// only clean up a session that is actually still stopped.
	if s.Status != StatusStopped {
		sm.mu.Unlock()
		return
	}

	// A shutdown-interrupted session didn't finish its work — leave it so restart
	// resumes it, matching the documented restart-preserves-sessions guarantee.
	if s.StopReason == StopReasonShutdown {
		sm.mu.Unlock()
		return
	}

	// Starred / system sessions are never auto-deleted (mirrors SoftDelete).
	if s.Starred || IsSystemSession(s) {
		sm.mu.Unlock()
		return
	}

	mode := s.AutoCleanup
	name := s.Name
	exitCode := 0

	if s.ExitCode != nil {
		exitCode = *s.ExitCode
	}

	if !shouldAutoCleanup(mode, exitCode) {
		sm.mu.Unlock()
		return
	}

	retention := sm.cfg.Delete.RetentionDuration()
	if retention <= 0 {
		sm.mu.Unlock()
		sm.log.Info("trigger auto-cleanup skipped: soft delete disabled (retention=0)", "id", id, "name", name)

		return
	}

	now := time.Now()
	expiresAt := now.Add(retention)
	s.DeletedAt = &now
	s.ExpiresAt = &expiresAt
	s.StatusChangedAt = now
	applyLifecycleSummaryLocked(s, softDeleteSummary(expiresAt))

	if err := sm.saveState(); err != nil {
		// Roll back: the session stays a live (stopped) session, fully consistent.
		s.DeletedAt = nil
		s.ExpiresAt = nil
		sm.mu.Unlock()
		sm.log.Warn("trigger auto-cleanup soft-delete failed to persist", "id", id, "name", name, "err", err)

		return
	}

	sm.mu.Unlock()
	sm.log.Info("trigger auto-cleanup soft-deleted stopped session", "id", id, "name", name, "mode", mode)
}

// shouldAutoCleanup decides whether a stopped session should be cleaned up given
// its auto_cleanup mode and the exit code it stopped with. "always" cleans up
// unconditionally; "on_success" only on a clean (exit 0) stop.
func shouldAutoCleanup(mode string, exitCode int) bool {
	switch mode {
	case config.CleanupAlways:
		return true
	case config.CleanupOnSuccess:
		return exitCode == 0
	default:
		return false
	}
}

// triggerNow is a small helper so watch fires share the schedule fire path's
// run recording.
func (sm *SessionManager) fireWatch(ctx context.Context, t *config.TriggerConfig, fc fireContext) {
	// Per-binding rate-limit backstop.
	n, win := t.Policy.RateLimitParsed()
	if sm.rateLimited(bindingKey(t.Name, fc.sessionID), n, win, time.Now()) {
		sm.log.Info("trigger: watch fire rate-limited", "trigger", t.Name, "session", fc.sessionID)
		return
	}

	// Daemon-wide concurrency cap.
	if !sm.acquireSlot() {
		sm.log.Info("trigger: max_concurrent reached, skipping watch fire", "trigger", t.Name)
		return
	}
	defer sm.releaseSlot()

	result, err := sm.fireAction(ctx, t, fc)
	sm.recordTriggerRun(t.Name, TriggerRun{ScheduledAt: time.Now(), SourceSessionID: fc.sessionID, Cause: causeFile, Result: result})

	if err != nil {
		sm.recordTriggerError(t.Name, err.Error())
		sm.log.Warn("trigger: watch action failed", "trigger", t.Name, "err", err)
	}
}
