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
// the bound worktree for watch), captures output, and delivers it.
func (sm *SessionManager) actionCommand(ctx context.Context, t *config.TriggerConfig, fc fireContext) (string, error) {
	execRoot := t.Action.Repo
	if t.IsWatch() {
		execRoot = fc.worktree
	}

	if execRoot == "" {
		return "", fmt.Errorf("command action has no execution root")
	}

	cmdStr := t.Action.Command
	name, args := "sh", []string{"-c", cmdStr}

	cfg := sm.Config()
	if t.Action.Sandboxed() {
		wrapped, wargs, ok, err := sm.buildCommandSandbox(cfg, &t.Action, execRoot, name, args)
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
	cmd.Dir = execRoot
	cmd.Env = triggerCommandEnv()

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
	sm.deliver(ctx, t.Action.Deliver, body, t.Action.Repo, sm.triggerVars(t, fc))

	return fmt.Sprintf("exit %d", exit), nil
}

// buildCommandSandbox wraps a command in the sandbox. Returns (cmd, args, ok,
// err): ok=false means the sandbox couldn't be enforced (caller fails closed).
func (sm *SessionManager) buildCommandSandbox(cfg *config.Config, a *config.ActionConfig, execRoot, name string, args []string) (string, []string, bool, error) {
	merged := cfg.Sandbox
	if a.SandboxConfig != nil {
		merged = merged.Merge(*a.SandboxConfig)
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

	opts := sandbox.WrapOpts{
		Backend:        merged.Backend,
		WorktreeDir:    execRoot,
		ReadDirs:       merged.ReadDirs,
		WriteDirs:      merged.WriteDirs,
		ReadFiles:      merged.ReadFiles,
		WriteFiles:     merged.WriteFiles,
		Features:       merged.Features,
		EnvKeys:        []string{"PATH", "HOME", "SHELL", "TERM", "LANG", "TMPDIR", "GRAITH_TMPDIR"},
		SignalMode:     merged.SignalMode,
		Profile:        merged.Profile,
		Network:        netPolicy,
		BackendCommand: merged.Command,
	}

	cmd, wargs, err := sandbox.Wrap(name, args, opts)
	if err != nil {
		return "", nil, false, err
	}

	return cmd, wargs, true, nil
}

func triggerCommandEnv() []string {
	keep := map[string]bool{"PATH": true, "HOME": true, "SHELL": true, "TERM": true, "LANG": true, "TMPDIR": true, "GRAITH_TMPDIR": true}

	var env []string

	for _, e := range os.Environ() {
		k, _, ok := strings.Cut(e, "=")
		if ok && keep[k] {
			env = append(env, e)
		}
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

	agent := t.Action.Agent
	model := t.Action.Model

	// ensure-reviewer (watch): reuse the binding's existing reactor if alive.
	if t.Action.Ensure && t.IsWatch() {
		if existing := sm.reuseReactor(t.Name, fc.sessionID); existing != "" {
			//nolint:contextcheck // notifyFromDaemon detaches its auto-resume; it must outlive this call.
			sm.notifyFromDaemon(existing, prompt)
			return "messaged " + existing, nil
		}
	}

	repo := t.Action.Repo

	shareWorktree := ""
	if t.IsWatch() {
		// Reactor shares the bound session's worktree read-only.
		shareWorktree = fc.sessionID
		repo = "" // shared-worktree sessions don't create their own repo worktree
	}

	name := triggerReactorName(t.Name, fc.sessionName)

	//nolint:contextcheck // Create spawns a PTY-backed session that outlives the fire ctx (matches scenario start).
	sess, err := sm.createTriggerSession(createTriggerReq{
		name:          name,
		agent:         agent,
		repo:          repo,
		prompt:        prompt,
		model:         model,
		parentID:      orchestratorID,
		shareWorktree: shareWorktree,
		triggerName:   t.Name,
		reactor:       t.Action.Ensure && t.IsWatch(),
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

func (sm *SessionManager) setBindingReactor(triggerName, sessionID, reactorID string) {
	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	if b := sm.triggers.bindings[bindingKey(triggerName, sessionID)]; b != nil {
		b.reactorID = reactorID
	}
}

type createTriggerReq struct {
	name          string
	agent         string
	repo          string
	prompt        string
	model         string
	parentID      string
	shareWorktree string
	triggerName   string
	reactor       bool
}

// createTriggerSession creates a session via sm.Create and tags it with the
// owning trigger. (The tag is applied immediately after creation; a full
// create-options refactor to tag inside the durable reservation is a follow-up.)
func (sm *SessionManager) createTriggerSession(req createTriggerReq) (SessionState, error) {
	agent := req.agent
	if agent == "" {
		agent = sm.Config().DefaultAgent
	}

	sess, err := sm.Create(
		req.name, agent, req.repo, "", req.prompt, req.model, req.parentID,
		false, req.shareWorktree, true,
		false, false, false, false, 24, 80,
	)
	if err != nil {
		return SessionState{}, err
	}

	sm.mu.Lock()
	if s, ok := sm.state.Sessions[sess.ID]; ok {
		s.TriggerID = req.triggerName
		s.TriggerReactor = req.reactor
	}

	_ = sm.saveState()
	sm.mu.Unlock()

	return sess, nil
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

	result, err := sm.fireAction(ctx, t, fc)
	sm.recordTriggerRun(t.Name, TriggerRun{ScheduledAt: time.Now(), SourceSessionID: fc.sessionID, Cause: causeFile, Result: result})

	if err != nil {
		sm.recordTriggerError(t.Name, err.Error())
		sm.log.Warn("trigger: watch action failed", "trigger", t.Name, "err", err)
	}
}
