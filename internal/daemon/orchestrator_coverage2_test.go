package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
)

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
