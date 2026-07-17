package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// blockCursorRule plants a regular file where writeCursorRule would MkdirAll the
// .cursor/rules directory, so a Cursor prompt injection fails with ENOTDIR. This
// is a real (not mocked) construction failure inside buildOrchestratorPrompt,
// deterministic and portable across platforms, that exercises the fail-closed
// handling both create and resume must share (issue #1306). The scratch dir is
// created first because the caller MkdirAll's it before injecting.
func blockCursorRule(t *testing.T, scratchDir string) {
	t.Helper()

	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(scratchDir, ".cursor"), []byte("thrawn"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newOrchPromptTestSM builds a manager whose orchestrator prompt is non-empty
// and routed through agentName. The sandboxResolver seam forces sandbox
// availability to true so the orchestrator launch path is reached on any
// platform without a real (darwin-only safehouse / Linux nono) backend; no PTY
// is ever spawned because these tests force the prompt build to fail first.
func newOrchPromptTestSM(t *testing.T, agentName string) (*SessionManager, string) {
	t.Helper()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Orchestrator.Enabled = true
	cfg.Orchestrator.Prompt = "ken this"
	cfg.Orchestrator.Agent = agentName
	cfg.DefaultAgent = agentName
	cfg.Sandbox = config.SandboxConfig{Enabled: true}

	ag := cfg.Agents[agentName]
	ag.Command = "echo"
	cfg.Agents[agentName] = ag

	sm := NewSessionManager(cfg, config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		DataDir:    dir,
		LogDir:     dir,
		RuntimeDir: dir,
	}, slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))

	sm.sandboxResolver = func(string) (bool, error) { return true, nil }

	return sm, dir
}

// TestBuildOrchestratorPromptMechanismParity is the portable, sandbox-free core
// of the #1306 acceptance: for every configured prompt-injection mechanism the
// orchestrator prompt assembles through the same agent-aware adapter, so create
// and resume inject identical args and handle a construction failure identically.
// Only cursor_rules performs I/O that can fail; append_system_prompt and
// developer_instructions cannot, and must return their args with no error.
func TestBuildOrchestratorPromptMechanismParity(t *testing.T) {
	sm := newOrchTestSM(t)
	cfg := config.OrchestratorConfig{Prompt: "ken this"}

	// append_system_prompt (claude): flag + prompt, no error.
	got, err := sm.buildOrchestratorPrompt("claude", cfg, nil, false, t.TempDir())
	if err != nil {
		t.Fatalf("append_system_prompt: unexpected error: %v", err)
	}

	if len(got) != 2 || got[0] != "--append-system-prompt" || got[1] != "ken this" {
		t.Errorf("append_system_prompt args wrong: %v", got)
	}

	// developer_instructions (codex): config override, no error.
	got, err = sm.buildOrchestratorPrompt("codex", cfg, nil, false, t.TempDir())
	if err != nil {
		t.Fatalf("developer_instructions: unexpected error: %v", err)
	}

	if len(got) != 2 || got[0] != "-c" || !strings.HasPrefix(got[1], "developer_instructions=") {
		t.Errorf("developer_instructions args wrong: %v", got)
	}

	// cursor_rules (cursor): writes a rule file, no launch args, no error.
	worktree := t.TempDir()

	got, err = sm.buildOrchestratorPrompt("cursor", cfg, nil, false, worktree)
	if err != nil {
		t.Fatalf("cursor_rules: unexpected error: %v", err)
	}

	if got != nil {
		t.Errorf("cursor_rules should return no launch args, got %v", got)
	}

	if _, statErr := os.Stat(filepath.Join(worktree, ".cursor", "rules", "graith.mdc")); statErr != nil {
		t.Errorf("cursor_rules should have written the rule file: %v", statErr)
	}

	// Injected construction failure: cursor_rules cannot write its file, so the
	// error must propagate rather than be swallowed. This is the failure both
	// create and resume must treat as fatal (fail closed).
	blocked := t.TempDir()
	if writeErr := os.WriteFile(filepath.Join(blocked, ".cursor"), []byte("thrawn"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	if _, err := sm.buildOrchestratorPrompt("cursor", cfg, nil, false, blocked); err == nil {
		t.Fatal("cursor prompt injection failure should surface as an error, got nil")
	}
}

// TestResumeOrchestratorPromptFailureFailsClosed is the primary #1306 regression
// test: before the fix, resume logged a warning and launched the privileged
// orchestrator without its role prompt. It must instead fail closed — roll back
// to the exact stopped/resumable state (status, token, durable state) and return
// a clear error — matching createOrchestrator. Deterministic on all platforms
// via the sandboxResolver seam and a real (filesystem) construction failure.
func TestResumeOrchestratorPromptFailureFailsClosed(t *testing.T) {
	sm, dir := newOrchPromptTestSM(t, "cursor")

	const oldToken = "auld-orch-token" //nolint:gosec // test fixture, not a real credential

	sm.state.Sessions["orch-id"] = &SessionState{
		ID:           "orch-id",
		Name:         OrchestratorSessionName,
		Agent:        "cursor",
		SystemKind:   SystemKindOrchestrator,
		Status:       StatusStopped,
		WorktreePath: dir,
		Token:        oldToken,
	}
	sm.tokenIndex[oldToken] = "orch-id"

	if err := sm.saveState(); err != nil {
		t.Fatal(err)
	}

	blockCursorRule(t, sm.orchestratorScratchDir())

	_, err := sm.Resume("orch-id", 24, 80)
	if err == nil {
		stopAndClosePTY(sm, "orch-id")
		t.Fatal("Resume() succeeded despite prompt-build failure — orchestrator would launch without its role prompt")
	}

	if !strings.Contains(err.Error(), "orchestrator prompt") {
		t.Errorf("error should name the orchestrator prompt build, got %v", err)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sess := sm.state.Sessions["orch-id"]
	if sess.Status != StatusStopped {
		t.Errorf("status = %q, want rolled back to %q", sess.Status, StatusStopped)
	}

	if sess.Token != oldToken {
		t.Errorf("token = %q, want rolled back to %q", sess.Token, oldToken)
	}

	if got := sm.SessionForToken(oldToken); got != "orch-id" {
		t.Errorf("old token resolves to %q, want orch-id", got)
	}

	if len(sm.tokenIndex) != 1 {
		t.Errorf("token index has %d entries, want 1: %v", len(sm.tokenIndex), sm.tokenIndex)
	}

	persisted, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}

	ps := persisted.Sessions["orch-id"]
	if ps.Status != StatusStopped {
		t.Errorf("persisted status = %q, want %q", ps.Status, StatusStopped)
	}

	if ps.Token != oldToken {
		t.Errorf("persisted token = %q, want durably rolled back to %q", ps.Token, oldToken)
	}
}

// TestCreateOrchestratorPromptFailureRollsBack pins the create side of the #1306
// parity: a prompt-build failure rolls the nascent session back out of state, so
// create and resume share the same fail-closed policy. Deterministic on all
// platforms via the sandboxResolver seam.
func TestCreateOrchestratorPromptFailureRollsBack(t *testing.T) {
	sm, _ := newOrchPromptTestSM(t, "cursor")

	blockCursorRule(t, sm.orchestratorScratchDir())

	if _, err := sm.createOrchestrator(context.Background()); err == nil {
		t.Fatal("createOrchestrator() succeeded despite prompt-build failure")
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if id := sm.findOrchestratorID(); id != "" {
		t.Errorf("createOrchestrator left orchestrator session %q after prompt-build failure; expected rollback", id)
	}
}
