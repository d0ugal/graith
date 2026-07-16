package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
)

// newOrchTestSM builds a minimal SessionManager with a real state file so
// saveState() succeeds during backoff bookkeeping.
func newOrchTestSM(t *testing.T) *SessionManager {
	t.Helper()

	dir := t.TempDir()

	return &SessionManager{
		state:    NewState(),
		sessions: make(map[string]SessionDriver),
		cfg:      &config.Config{},
		paths: config.Paths{
			DataDir:   dir,
			LogDir:    filepath.Join(dir, "logs"),
			StateFile: filepath.Join(dir, "state.json"),
		},
		orchestratorExitCh: make(chan string, 4),
		orchestratorKickCh: make(chan struct{}, 1),
		log:                slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

func TestOrchestratorDirs_Cov(t *testing.T) {
	sm := newOrchTestSM(t)

	wantScratch := filepath.Join(sm.paths.DataDir, "orchestrator", "scratch")
	if got := sm.orchestratorScratchDir(); got != wantScratch {
		t.Errorf("orchestratorScratchDir() = %q, want %q", got, wantScratch)
	}

	wantTmp := filepath.Join(sm.paths.DataDir, "orchestrator", "tmp")
	if got := sm.orchestratorTmpDir(); got != wantTmp {
		t.Errorf("orchestratorTmpDir() = %q, want %q", got, wantTmp)
	}
}

func TestFindOrchestratorID_Cov(t *testing.T) {
	sm := newOrchTestSM(t)

	if id := sm.findOrchestratorID(); id != "" {
		t.Errorf("expected empty ID with no orchestrator, got %q", id)
	}

	sm.state.Sessions["braw"] = &SessionState{ID: "braw"}
	sm.state.Sessions["ben"] = &SessionState{ID: "ben", SystemKind: SystemKindOrchestrator}

	if id := sm.findOrchestratorID(); id != "ben" {
		t.Errorf("expected orchestrator ID 'ben', got %q", id)
	}
}

func TestSystemSessionEnabledInConfig_Cov(t *testing.T) {
	sm := newOrchTestSM(t)

	orch := &SessionState{SystemKind: SystemKindOrchestrator}

	sm.cfg.Orchestrator.Enabled = false
	if sm.systemSessionEnabledInConfig(orch) {
		t.Error("orchestrator with feature disabled should report not-enabled")
	}

	sm.cfg.Orchestrator.Enabled = true
	if !sm.systemSessionEnabledInConfig(orch) {
		t.Error("orchestrator with feature enabled should report enabled")
	}

	// Unknown system kind is treated as managed (never orphan-deleted).
	unknown := &SessionState{SystemKind: "haar-unknown"}
	if !sm.systemSessionEnabledInConfig(unknown) {
		t.Error("unknown system kind should default to enabled/managed")
	}
}

// mustBuildOrchPrompt runs buildOrchestratorPrompt and fails the test on error,
// returning just the launch args so the claude-focused cases stay terse.
func mustBuildOrchPrompt(t *testing.T, sm *SessionManager, agentName string, orchCfg config.OrchestratorConfig, repoPaths []string, notifyEnabled bool, worktreePath string) []string {
	t.Helper()

	got, err := sm.buildOrchestratorPrompt(agentName, orchCfg, repoPaths, notifyEnabled, worktreePath)
	if err != nil {
		t.Fatalf("buildOrchestratorPrompt(%q): unexpected error: %v", agentName, err)
	}

	return got
}

func TestBuildOrchestratorPrompt_Cov(t *testing.T) {
	sm := newOrchTestSM(t)

	// Empty prompt and no file: nil regardless of agent.
	if got := mustBuildOrchPrompt(t, sm, "claude", config.OrchestratorConfig{}, nil, false, ""); got != nil {
		t.Errorf("empty prompt should return nil, got %v", got)
	}

	// Inline prompt only.
	got := mustBuildOrchPrompt(t, sm, "claude", config.OrchestratorConfig{Prompt: "ken this"}, nil, false, "")
	if len(got) != 2 || got[0] != "--append-system-prompt" || got[1] != "ken this" {
		t.Errorf("inline prompt args wrong: %v", got)
	}

	// prompt_file that does not exist: warns, keeps inline prompt.
	got = mustBuildOrchPrompt(t, sm, "claude", config.OrchestratorConfig{
		Prompt:     "bide",
		PromptFile: filepath.Join(t.TempDir(), "does-not-exist.txt"),
	}, nil, false, "")
	if len(got) != 2 || got[1] != "bide" {
		t.Errorf("missing prompt_file should keep inline prompt, got %v", got)
	}

	// prompt_file present: appended to inline prompt with a blank line.
	pf := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(pf, []byte("from the croft"), 0o600); err != nil {
		t.Fatal(err)
	}

	got = mustBuildOrchPrompt(t, sm, "claude", config.OrchestratorConfig{Prompt: "bide", PromptFile: pf}, nil, false, "")
	if len(got) != 2 || got[1] != "bide\n\nfrom the croft" {
		t.Errorf("prompt_file should append after inline prompt, got %q", got[1])
	}

	// prompt_file only (no inline prompt).
	got = mustBuildOrchPrompt(t, sm, "claude", config.OrchestratorConfig{PromptFile: pf}, nil, false, "")
	if len(got) != 2 || got[1] != "from the croft" {
		t.Errorf("prompt_file-only should use file contents, got %q", got)
	}
}

// TestBuildOrchestratorPrompt_AgentAdapters is the regression guard for #1232:
// the orchestrator prompt must be routed through the same agent-aware adapter
// as ordinary sessions, so a Codex, Cursor, or custom orchestrator agent is
// never launched with Claude's --append-system-prompt flag.
func TestBuildOrchestratorPrompt_AgentAdapters(t *testing.T) {
	sm := newOrchTestSM(t)

	cfg := config.OrchestratorConfig{Prompt: "ken this"}

	// Codex: injected as developer_instructions via a config override, never
	// as Claude's --append-system-prompt.
	got := mustBuildOrchPrompt(t, sm, "codex", cfg, nil, false, "")
	if len(got) != 2 || got[0] != "-c" || !strings.HasPrefix(got[1], "developer_instructions=") {
		t.Fatalf("codex should get developer_instructions args, got %v", got)
	}

	if strings.Contains(strings.Join(got, " "), "--append-system-prompt") {
		t.Errorf("codex must never receive --append-system-prompt, got %v", got)
	}

	// Cursor: injected as a .cursor/rules file under the worktree, no launch
	// args (and definitely not a Claude flag).
	worktree := t.TempDir()

	got = mustBuildOrchPrompt(t, sm, "cursor", cfg, nil, false, worktree)
	if got != nil {
		t.Errorf("cursor should return no launch args, got %v", got)
	}

	rule := filepath.Join(worktree, ".cursor", "rules", "graith.mdc")

	data, err := os.ReadFile(rule)
	if err != nil {
		t.Fatalf("cursor rule not written: %v", err)
	}

	if !strings.Contains(string(data), "ken this") {
		t.Errorf("cursor rule should contain the prompt, got %q", string(data))
	}

	// Custom/unknown agent with no configured method: no supported injection,
	// so no args and no unsupported Claude flag.
	if got := mustBuildOrchPrompt(t, sm, "thrawn-custom", cfg, nil, false, ""); got != nil {
		t.Errorf("unknown agent should return nil prompt args, got %v", got)
	}

	// A custom orchestrator agent can declare its injection mechanism via
	// [agents.<name>].prompt_injection, and buildOrchestratorPrompt honours it.
	sm.cfg = &config.Config{
		Agents: map[string]config.Agent{
			"thrawn-custom": {PromptInjection: config.PromptInjectionAppendSystemPrompt},
		},
	}

	got = mustBuildOrchPrompt(t, sm, "thrawn-custom", cfg, nil, false, "")
	if len(got) != 2 || got[0] != "--append-system-prompt" || got[1] != "ken this" {
		t.Errorf("configured prompt_injection should drive the orchestrator prompt, got %v", got)
	}
}

// TestBuildOrchestratorPrompt_InjectPromptDisabled is the #1292 regression: when
// the selected orchestrator agent sets inject_prompt = false, prompt injection is
// suppressed — no Codex developer_instructions override, no Cursor rule file, no
// Claude flag. buildOrchestratorPrompt is the single seam both the create
// (createOrchestrator) and resume (resumeSession isOrchestrator) paths call to
// build orchestrator prompt args, so gating it here covers both.
func TestBuildOrchestratorPrompt_InjectPromptDisabled(t *testing.T) {
	disabled := false

	sm := newOrchTestSM(t)
	sm.cfg = &config.Config{
		Agents: map[string]config.Agent{
			"codex":  {PromptInjection: config.PromptInjectionDeveloperInstructions, InjectPrompt: &disabled},
			"cursor": {PromptInjection: config.PromptInjectionCursorRules, InjectPrompt: &disabled},
		},
	}

	cfg := config.OrchestratorConfig{Prompt: "ken this"}

	// Codex opted out: no developer_instructions override at all.
	if got := mustBuildOrchPrompt(t, sm, "codex", cfg, nil, true, ""); got != nil {
		t.Errorf("disabled codex orchestrator should get no prompt args, got %v", got)
	}

	// Cursor opted out: no launch args AND no rule file side effect.
	worktree := t.TempDir()

	if got := mustBuildOrchPrompt(t, sm, "cursor", cfg, nil, true, worktree); got != nil {
		t.Errorf("disabled cursor orchestrator should get no prompt args, got %v", got)
	}

	rule := filepath.Join(worktree, ".cursor", "rules", "graith.mdc")
	if _, err := os.Stat(rule); !os.IsNotExist(err) {
		t.Errorf("disabled cursor orchestrator must not write %s (stat err = %v)", rule, err)
	}
}

func TestBuildOrchestratorPrompt_RepoPaths(t *testing.T) {
	sm := newOrchTestSM(t)

	repoPaths := []string{"/glen/croft", "/glen/bothy"}

	// Inline prompt plus configured repo paths: the repos section is appended
	// after the base prompt, and each configured path is listed.
	got := mustBuildOrchPrompt(t, sm, "claude", config.OrchestratorConfig{Prompt: "ken this"}, repoPaths, false, "")
	if len(got) != 2 || got[0] != "--append-system-prompt" {
		t.Fatalf("expected append-system-prompt args, got %v", got)
	}

	body := got[1]
	if !strings.HasPrefix(body, "ken this\n\n") {
		t.Errorf("repo section should follow the base prompt, got %q", body)
	}

	for _, p := range repoPaths {
		if !strings.Contains(body, p) {
			t.Errorf("prompt should list configured repo path %q, got %q", p, body)
		}
	}

	if !strings.Contains(body, "Available repositories") {
		t.Errorf("prompt should include a repositories heading, got %q", body)
	}

	// No base prompt but repo paths configured: a prompt is still produced so
	// the orchestrator learns which repos exist.
	got = mustBuildOrchPrompt(t, sm, "claude", config.OrchestratorConfig{}, repoPaths, false, "")
	if len(got) != 2 || !strings.Contains(got[1], "/glen/croft") {
		t.Errorf("repo paths alone should still produce a prompt, got %v", got)
	}

	// No base prompt and no repo paths: nil (empty case handled gracefully).
	if got := mustBuildOrchPrompt(t, sm, "claude", config.OrchestratorConfig{}, nil, false, ""); got != nil {
		t.Errorf("empty prompt and no repo paths should return nil, got %v", got)
	}

	// Codex still gets the repo section, but via developer_instructions rather
	// than Claude's flag.
	got = mustBuildOrchPrompt(t, sm, "codex", config.OrchestratorConfig{}, repoPaths, false, "")
	if len(got) != 2 || got[0] != "-c" || !strings.Contains(got[1], "/glen/croft") {
		t.Errorf("codex should carry the repo section via developer_instructions, got %v", got)
	}
}

func TestBuildOrchestratorPrompt_Notifications(t *testing.T) {
	sm := newOrchTestSM(t)

	// notifyEnabled appends a notifications section teaching the orchestrator
	// about `gr notify`, even with no base prompt configured.
	got := mustBuildOrchPrompt(t, sm, "claude", config.OrchestratorConfig{}, nil, true, "")
	if len(got) != 2 || !strings.Contains(got[1], "gr notify") {
		t.Fatalf("notifications section should mention gr notify, got %v", got)
	}

	// When notifications are disabled the section is omitted.
	if got := mustBuildOrchPrompt(t, sm, "claude", config.OrchestratorConfig{}, nil, false, ""); got != nil {
		t.Errorf("no prompt, no repos, notifications off should be nil, got %v", got)
	}

	// It is appended after an inline prompt with a blank-line separator.
	got = mustBuildOrchPrompt(t, sm, "claude", config.OrchestratorConfig{Prompt: "ken this"}, nil, true, "")
	if len(got) != 2 || !strings.HasPrefix(got[1], "ken this\n\n") || !strings.Contains(got[1], "Notifying the human") {
		t.Errorf("notifications section should follow the base prompt, got %q", got[1])
	}
}

func TestNotifyOrchestratorExit_Cov(t *testing.T) {
	sm := newOrchTestSM(t)

	// Delivers onto the buffered channel.
	sm.notifyOrchestratorExit("ben")

	select {
	case id := <-sm.orchestratorExitCh:
		if id != "ben" {
			t.Errorf("expected 'ben', got %q", id)
		}
	default:
		t.Fatal("expected an exit notification on the channel")
	}

	// A full channel drops the notification rather than blocking.
	for i := 0; i < cap(sm.orchestratorExitCh); i++ {
		sm.orchestratorExitCh <- "filler"
	}

	done := make(chan struct{})

	go func() {
		sm.notifyOrchestratorExit("dreich") // must not block
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notifyOrchestratorExit blocked on a full channel")
	}

	// A nil channel is a no-op.
	sm.orchestratorExitCh = nil
	sm.notifyOrchestratorExit("thrawn")
}

func TestHandleOrchestratorExit_EarlyReturns_Cov(t *testing.T) {
	sm := newOrchTestSM(t)

	// A cancelled context guarantees that even if a guard regressed and fell
	// through to the backoff-scheduling path, the test would not sleep — so a
	// prompt return alone cannot mask a broken guard; the BackoffLevel assertion
	// below is what actually pins the behavior.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Unknown session: returns without touching state.
	sm.handleOrchestratorExit(ctx, "missing")

	// Non-orchestrator session: returns.
	sm.state.Sessions["braw"] = &SessionState{ID: "braw", SystemKind: ""}
	sm.handleOrchestratorExit(ctx, "braw")

	// Orchestrator stopped for a terminal reason must NOT auto-restart: it returns
	// before any backoff bookkeeping, so a non-zero sentinel BackoffLevel must be
	// left untouched (the backoff path would have bumped it).
	for _, reason := range []string{StopReasonUser, StopReasonIdle, StopReasonShutdown} {
		id := "orch-" + reason
		sm.state.Sessions[id] = &SessionState{
			ID:           id,
			SystemKind:   SystemKindOrchestrator,
			StopReason:   reason,
			BackoffLevel: 3,
		}
		sm.handleOrchestratorExit(ctx, id)

		if got := sm.state.Sessions[id].BackoffLevel; got != 3 {
			t.Errorf("terminal stop reason %q must not touch BackoffLevel, got %d", reason, got)
		}
	}
}

func TestHandleOrchestratorExit_BackoffScheduling_Cov(t *testing.T) {
	sm := newOrchTestSM(t)

	// Cancelled context: after computing the backoff delay, the scheduling
	// select returns immediately at ctx.Done() instead of sleeping.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Recently started + crash reason: backoff level increments from 0.
	sm.state.Sessions["ben"] = &SessionState{
		ID:            "ben",
		SystemKind:    SystemKindOrchestrator,
		StopReason:    StopReasonCrash,
		BackoffLevel:  0,
		LastStartedAt: time.Now(),
	}
	sm.handleOrchestratorExit(ctx, "ben")

	if got := sm.state.Sessions["ben"].BackoffLevel; got != 1 {
		t.Errorf("recent crash should bump backoff to 1, got %d", got)
	}

	// Started long ago: the stable-threshold branch resets backoff to 0 first,
	// then increments to 1.
	sm.state.Sessions["ben"].BackoffLevel = 5
	sm.state.Sessions["ben"].LastStartedAt = time.Now().Add(-2 * config.OrchestratorStableResetDefault)
	sm.handleOrchestratorExit(ctx, "ben")

	if got := sm.state.Sessions["ben"].BackoffLevel; got != 1 {
		t.Errorf("stable-threshold reset then increment should yield 1, got %d", got)
	}
}

func TestOrchestratorRestartDelayKeepsPositiveSupervisorFloor(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.OrchestratorRestartConfig
	}{
		{"zero schedule", config.OrchestratorRestartConfig{Schedule: []string{"0s"}}},
		{"negative schedule", config.OrchestratorRestartConfig{Schedule: []string{"-1s"}}},
		{"zero geometric bounds", config.OrchestratorRestartConfig{Schedule: []string{}, InitialBackoff: "0s", MaxBackoff: "0s"}},
		{"negative stable reset does not affect delay", config.OrchestratorRestartConfig{Schedule: []string{"-1s"}, StableReset: "-1s"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := orchestratorRestartDelay(tt.cfg, 0); got <= 0 {
				t.Errorf("orchestratorRestartDelay() = %v, want positive delay", got)
			}
		})
	}
}

// TestHandleOrchestratorExit_RestartConfig is a regression test for the restart
// policy being configurable (#1239): a custom stable_reset window and a custom
// fresh-start threshold must drive the backoff/reset behaviour instead of the
// hardcoded 60s / 3 that preceded the config.
func TestHandleOrchestratorExit_RestartConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // skip the sleep; we only assert the pre-delay bookkeeping

	// A run started 2m ago resets backoff under a 1m window but not under a 1h
	// window — proving stable_reset is read from config, not hardcoded to 60s.
	cases := []struct {
		name        string
		stableReset string
		want        int // BackoffLevel after the exit is handled
	}{
		{"window longer than run keeps backoff", "1h", 5},   // no reset: 4 -> 5
		{"window shorter than run resets backoff", "1m", 1}, // reset to 0 -> 1
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sm := newOrchTestSM(t)
			sm.cfg.Orchestrator.Restart.StableReset = tc.stableReset
			sm.state.Sessions["ben"] = &SessionState{
				ID:            "ben",
				SystemKind:    SystemKindOrchestrator,
				StopReason:    StopReasonCrash,
				BackoffLevel:  4,
				LastStartedAt: time.Now().Add(-2 * time.Minute),
			}
			sm.handleOrchestratorExit(ctx, "ben")

			if got := sm.state.Sessions["ben"].BackoffLevel; got != tc.want {
				t.Errorf("stable_reset=%s: BackoffLevel = %d, want %d", tc.stableReset, got, tc.want)
			}
		})
	}
}

// These tests cover the orchestrator lifecycle entry points that round 1 left
// at 0%: createOrchestrator's two fail-closed early returns, ensureOrchestrator's
// boot-time branch selection, and orchestratorSupervisor's dispatch/shutdown
// loop. They pin the guards that keep the daemon from spawning an unsandboxed
// orchestrator or churning on a misconfigured one, without needing a real
// sandbox backend (unavailable in CI).

// TestCreateOrchestratorAgentMissing_Cov2 asserts createOrchestrator fails
// before touching any state when the configured orchestrator agent is not in
// the agents table — the very first guard.
func TestCreateOrchestratorAgentMissing_Cov2(t *testing.T) {
	sm := newOrchTestSM(t) // empty config: Agents is nil, AgentName() defaults to "claude"

	_, err := sm.createOrchestrator(context.Background())
	if err == nil {
		t.Fatal("expected error when orchestrator agent is not configured")
	}

	if !strings.Contains(err.Error(), "not found in config") {
		t.Fatalf("error = %q, want to mention missing agent", err.Error())
	}

	// No session may have been persisted on this failure path.
	if id := sm.findOrchestratorID(); id != "" {
		t.Fatalf("createOrchestrator must not persist a session on agent-missing, got %q", id)
	}
}

// TestCreateOrchestratorRequiresSandbox_Cov2 asserts that when the agent exists
// but the sandbox is not enabled/available, createOrchestrator fails closed
// rather than starting an unsandboxed orchestrator.
func TestCreateOrchestratorRequiresSandbox_Cov2(t *testing.T) {
	sm := newOrchTestSM(t)

	// A real default config has the claude agent but sandbox disabled, so the
	// agent-missing guard passes and the requires-sandbox guard fires.
	sm.mu.Lock()
	sm.cfg = config.Default()
	sm.mu.Unlock()

	_, err := sm.createOrchestrator(context.Background())
	if err == nil {
		t.Fatal("expected error when sandbox is not available for orchestrator")
	}

	if !strings.Contains(err.Error(), "requires sandbox") {
		t.Fatalf("error = %q, want to mention sandbox requirement", err.Error())
	}

	if id := sm.findOrchestratorID(); id != "" {
		t.Fatalf("createOrchestrator must not persist a session when sandbox is unavailable, got %q", id)
	}
}

// TestEnsureOrchestratorDisabled_Cov2 asserts the boot hook is a no-op when the
// feature is disabled in config.
func TestEnsureOrchestratorDisabled_Cov2(t *testing.T) {
	sm := newOrchTestSM(t) // config.Orchestrator.Enabled defaults to false

	sm.ensureOrchestrator(context.Background())

	if id := sm.findOrchestratorID(); id != "" {
		t.Fatalf("disabled orchestrator must not be created, got %q", id)
	}
}

// TestEnsureOrchestratorCreatesWhenEnabledButFails_Cov2 drives the "no
// orchestrator exists yet" branch: ensureOrchestrator calls createOrchestrator,
// which fails closed (no sandbox), and the error is logged rather than
// propagated. The daemon stays orchestrator-less rather than crashing on boot.
func TestEnsureOrchestratorCreatesWhenEnabledButFails_Cov2(t *testing.T) {
	sm := newOrchTestSM(t)

	sm.mu.Lock()
	sm.cfg = config.Default() // has claude agent, sandbox disabled -> create fails closed
	sm.cfg.Orchestrator.Enabled = true
	sm.mu.Unlock()

	sm.ensureOrchestrator(context.Background())

	if id := sm.findOrchestratorID(); id != "" {
		t.Fatalf("failed createOrchestrator must not leave a session, got %q", id)
	}
}

func TestReconcileOrchestratorPresenceAfterDelete(t *testing.T) {
	sm := newOrchTestSM(t)
	sm.cfg.Orchestrator.Enabled = true
	sm.state.Sessions["auld-orch"] = &SessionState{
		ID:         "auld-orch",
		Name:       OrchestratorSessionName,
		SystemKind: SystemKindOrchestrator,
		Status:     StatusStopped,
	}

	if err := sm.Delete("auld-orch"); err != nil {
		t.Fatalf("delete orchestrator: %v", err)
	}

	created := 0

	sm.reconcileOrchestratorPresenceWith(context.Background(), func(context.Context) (SessionState, error) {
		created++
		fresh := &SessionState{
			ID:         "fresh-orch",
			Name:       OrchestratorSessionName,
			SystemKind: SystemKindOrchestrator,
			Status:     StatusRunning,
		}

		sm.mu.Lock()
		sm.state.Sessions[fresh.ID] = fresh
		sm.mu.Unlock()

		return cloneSessionState(fresh), nil
	})

	if created != 1 {
		t.Fatalf("create calls = %d, want 1", created)
	}

	if got := sm.findOrchestratorID(); got != "fresh-orch" {
		t.Fatalf("reconciled orchestrator ID = %q, want fresh-orch", got)
	}

	// A later tick sees the replacement and must not create another one.
	sm.reconcileOrchestratorPresenceWith(context.Background(), func(context.Context) (SessionState, error) {
		created++
		return SessionState{}, nil
	})

	if created != 1 {
		t.Fatalf("existing replacement triggered another create; calls = %d", created)
	}
}

func TestReconcileOrchestratorPresenceDisabled(t *testing.T) {
	sm := newOrchTestSM(t)
	sm.cfg.Orchestrator.Enabled = false

	created := 0

	sm.reconcileOrchestratorPresenceWith(context.Background(), func(context.Context) (SessionState, error) {
		created++
		return SessionState{}, nil
	})

	if created != 0 {
		t.Fatalf("disabled orchestrator triggered %d creates, want 0", created)
	}
}

func TestDeleteKicksOrchestratorReconcileLoop(t *testing.T) {
	sm := newOrchTestSM(t)
	sm.cfg.Orchestrator.Enabled = true
	sm.state.Sessions["auld-orch"] = &SessionState{
		ID:         "auld-orch",
		Name:       OrchestratorSessionName,
		SystemKind: SystemKindOrchestrator,
		Status:     StatusStopped,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})

	go func() {
		runOrchestratorReconcileLoop(ctx, sm.orchestratorKickCh, func(context.Context) {
			sm.reconcileOrchestratorPresenceWith(ctx, func(context.Context) (SessionState, error) {
				fresh := &SessionState{
					ID:         "fresh-orch",
					Name:       OrchestratorSessionName,
					SystemKind: SystemKindOrchestrator,
					Status:     StatusRunning,
				}

				sm.mu.Lock()
				sm.state.Sessions[fresh.ID] = fresh
				sm.mu.Unlock()

				return cloneSessionState(fresh), nil
			})
		})
		close(done)
	}()

	if err := sm.Delete("auld-orch"); err != nil {
		t.Fatalf("delete orchestrator: %v", err)
	}

	deadline := time.Now().Add(time.Second)

	for {
		sm.mu.RLock()
		got := sm.findOrchestratorID()
		sm.mu.RUnlock()

		if got == "fresh-orch" {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("orchestrator was not recreated after delete kick")
		}

		time.Sleep(time.Millisecond)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("orchestrator reconcile loop did not stop after cancellation")
	}
}

func TestFailedDeleteDoesNotKickOrchestratorReconcile(t *testing.T) {
	sm := newOrchTestSM(t)
	sm.cfg.Orchestrator.Enabled = true
	sm.state.Sessions["thrawn-orch"] = &SessionState{
		ID:         "thrawn-orch",
		Name:       OrchestratorSessionName,
		SystemKind: SystemKindOrchestrator,
		Status:     StatusStopped,
	}
	sm.saveStateFault = func() error { return errors.New("dreich disk") }

	if err := sm.Delete("thrawn-orch"); err == nil {
		t.Fatal("delete should fail when its state commit fails")
	}

	select {
	case <-sm.orchestratorKickCh:
		t.Fatal("failed delete must not kick orchestrator recreation")
	default:
	}
}

// TestEnsureOrchestratorAlreadyRunning_Cov2 drives the "running with a live PTY"
// branch: the existing orchestrator is left untouched (no restart, no error).
func TestEnsureOrchestratorAlreadyRunning_Cov2(t *testing.T) {
	sm := newOrchTestSM(t)

	logDir := filepath.Join(sm.paths.DataDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}

	pty, err := grpty.NewSession(grpty.SessionOpts{
		ID: "ben-orch", Command: "sleep", Args: []string{"60"},
		Dir: sm.paths.DataDir, Rows: 24, Cols: 80,
		LogPath: filepath.Join(logDir, "orch.log"),
	})
	if err != nil {
		t.Fatalf("start sleeper pty: %v", err)
	}

	t.Cleanup(func() { _ = pty.Kill() })

	sm.mu.Lock()
	sm.cfg = config.Default()
	sm.cfg.Orchestrator.Enabled = true
	sm.state.Sessions["ben-orch"] = &SessionState{
		ID: "ben-orch", Name: OrchestratorSessionName,
		SystemKind: SystemKindOrchestrator, Status: StatusRunning,
	}
	sm.sessions["ben-orch"] = pty
	sm.mu.Unlock()

	sm.ensureOrchestrator(context.Background())

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.state.Sessions["ben-orch"].Status != StatusRunning {
		t.Fatalf("already-running orchestrator must stay running, got %q", sm.state.Sessions["ben-orch"].Status)
	}
}

// TestOrchestratorSupervisorShutdown_Cov2 asserts the supervisor loop returns
// promptly when its context is cancelled.
func TestOrchestratorSupervisorShutdown_Cov2(t *testing.T) {
	sm := newOrchTestSM(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})

	go func() {
		sm.orchestratorSupervisor(ctx, sm.orchestratorExitCh)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("orchestratorSupervisor did not return on cancelled context")
	}
}

// TestOrchestratorSupervisorDispatchesExit_Cov2 asserts the supervisor forwards
// an exit notification to handleOrchestratorExit: a recently-started crashed
// orchestrator gets its backoff level bumped. The context is cancelled once the
// bump is observed, so the test never sits through the real restart delay.
func TestOrchestratorSupervisorDispatchesExit_Cov2(t *testing.T) {
	sm := newOrchTestSM(t)

	sm.mu.Lock()
	sm.state.Sessions["canny-orch"] = &SessionState{
		ID: "canny-orch", SystemKind: SystemKindOrchestrator,
		StopReason: StopReasonCrash, BackoffLevel: 0, LastStartedAt: time.Now(),
	}
	sm.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sm.orchestratorSupervisor(ctx, sm.orchestratorExitCh)

	sm.orchestratorExitCh <- "canny-orch"

	deadline := time.Now().Add(2 * time.Second)

	for {
		sm.mu.RLock()
		lvl := sm.state.Sessions["canny-orch"].BackoffLevel
		sm.mu.RUnlock()

		if lvl == 1 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("supervisor did not dispatch exit to handleOrchestratorExit")
		}

		time.Sleep(5 * time.Millisecond)
	}

	// Cancel to release the backoff-scheduling select inside handleOrchestratorExit.
	cancel()
}
