package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
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
		sessions: make(map[string]*grpty.Session),
		cfg:      &config.Config{},
		paths: config.Paths{
			DataDir:   dir,
			LogDir:    filepath.Join(dir, "logs"),
			StateFile: filepath.Join(dir, "state.json"),
		},
		orchestratorExitCh: make(chan string, 4),
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

func TestBuildOrchestratorPrompt_Cov(t *testing.T) {
	sm := newOrchTestSM(t)

	// Non-claude agent: prompt injection is claude-only.
	if got := sm.buildOrchestratorPrompt("codex", config.OrchestratorConfig{Prompt: "ignored"}); got != nil {
		t.Errorf("non-claude agent should return nil prompt args, got %v", got)
	}

	// Empty prompt and no file: nil.
	if got := sm.buildOrchestratorPrompt("claude", config.OrchestratorConfig{}); got != nil {
		t.Errorf("empty prompt should return nil, got %v", got)
	}

	// Inline prompt only.
	got := sm.buildOrchestratorPrompt("claude", config.OrchestratorConfig{Prompt: "ken this"})
	if len(got) != 2 || got[0] != "--append-system-prompt" || got[1] != "ken this" {
		t.Errorf("inline prompt args wrong: %v", got)
	}

	// prompt_file that does not exist: warns, keeps inline prompt.
	got = sm.buildOrchestratorPrompt("claude", config.OrchestratorConfig{
		Prompt:     "bide",
		PromptFile: filepath.Join(t.TempDir(), "does-not-exist.txt"),
	})
	if len(got) != 2 || got[1] != "bide" {
		t.Errorf("missing prompt_file should keep inline prompt, got %v", got)
	}

	// prompt_file present: appended to inline prompt with a blank line.
	pf := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(pf, []byte("from the croft"), 0o600); err != nil {
		t.Fatal(err)
	}

	got = sm.buildOrchestratorPrompt("claude", config.OrchestratorConfig{Prompt: "bide", PromptFile: pf})
	if len(got) != 2 || got[1] != "bide\n\nfrom the croft" {
		t.Errorf("prompt_file should append after inline prompt, got %q", got[1])
	}

	// prompt_file only (no inline prompt).
	got = sm.buildOrchestratorPrompt("claude", config.OrchestratorConfig{PromptFile: pf})
	if len(got) != 2 || got[1] != "from the croft" {
		t.Errorf("prompt_file-only should use file contents, got %q", got)
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
	sm.state.Sessions["ben"].LastStartedAt = time.Now().Add(-2 * orchestratorStableThreshold)
	sm.handleOrchestratorExit(ctx, "ben")

	if got := sm.state.Sessions["ben"].BackoffLevel; got != 1 {
		t.Errorf("stable-threshold reset then increment should yield 1, got %d", got)
	}
}
