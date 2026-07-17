package daemon

import (
	"errors"
	"sync"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// killFakeDriver is a minimal SessionDriver whose Kill() fails for the first
// failN calls, so a reload's orchestrator-disable stop can be made to fail once
// and then succeed on retry.
type killFakeDriver struct {
	SessionDriver

	mu    sync.Mutex
	failN int
	kills int
}

func (d *killFakeDriver) ProcessPID() int { return 5150 }
func (d *killFakeDriver) Pgid() int       { return 5150 }
func (d *killFakeDriver) Kill() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.kills++
	if d.kills <= d.failN {
		return errors.New("simulated SIGTERM failure")
	}

	return nil
}

func (d *killFakeDriver) killCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.kills
}

// TestApplyConfigOrchestratorDisableStopFailure guards issue #1324: when
// disabling the orchestrator, a failed stop must not be discarded. The reload
// must surface the error, leave the orchestrator marked enabled (coherent with
// the still-running session) rather than claim it stopped, and a later reload
// must retry the stop and succeed.
func TestApplyConfigOrchestratorDisableStopFailure(t *testing.T) {
	cfg := config.Default()
	cfg.Orchestrator.Enabled = true

	sm := newSMWithConfig(t, cfg)

	const orchID = "orch-ben"

	driver := &killFakeDriver{failN: 1} // first Kill fails, second succeeds

	sm.mu.Lock()
	sm.state.Sessions[orchID] = &SessionState{
		ID:         orchID,
		Name:       orchID,
		Status:     StatusRunning,
		SystemKind: SystemKindOrchestrator,
	}
	sm.sessions[orchID] = driver
	sm.mu.Unlock()

	disabled := config.Default()
	disabled.Orchestrator.Enabled = false

	// First reload: the stop fails.
	err := sm.applyConfig(disabled)
	if err == nil {
		t.Fatal("applyConfig should return the orchestrator disable failure, got nil")
	}

	if driver.killCount() != 1 {
		t.Fatalf("expected exactly one Kill attempt, got %d", driver.killCount())
	}

	// The published config must NOT claim the orchestrator is disabled while its
	// session is still running.
	if !sm.Config().Orchestrator.Enabled {
		t.Fatal("orchestrator.enabled was published as false despite the stop failing")
	}

	// The session must still be present/running AND its lifecycle state must stay
	// coherent: a failed stop must not durably mark it StopReasonUser, or the
	// supervisor would suppress a restart if it later exited on its own even though
	// config still has it enabled.
	sm.mu.RLock()

	s := sm.state.Sessions[orchID]
	if s == nil || s.Status != StatusRunning {
		sm.mu.RUnlock()
		t.Fatalf("orchestrator session should still be running after a failed disable, got %+v", s)
	}

	if s.StopReason != "" {
		sm.mu.RUnlock()
		t.Fatalf("failed disable left StopReason=%q; want it restored so lifecycle state stays coherent", s.StopReason)
	}

	sm.mu.RUnlock()

	// Retry: the second Kill succeeds, so the disable completes cleanly.
	if err := sm.applyConfig(disabled); err != nil {
		t.Fatalf("retry reload should succeed once the stop succeeds, got %v", err)
	}

	if driver.killCount() != 2 {
		t.Fatalf("retry should re-attempt the stop, kills = %d, want 2", driver.killCount())
	}

	if sm.Config().Orchestrator.Enabled {
		t.Fatal("orchestrator.enabled should be published as false after a successful stop")
	}
}
