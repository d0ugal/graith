package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestHandleOrchestratorExitRetriesAfterTransientResumeFailure guards issue #1284:
// a first Resume that fails must not abandon the still-enabled orchestrator — a
// later attempt restores it, with the backoff level advancing between tries.
func TestHandleOrchestratorExitRetriesAfterTransientResumeFailure(t *testing.T) {
	sm := newOrchTestSM(t)
	sm.cfg.Orchestrator.Enabled = true
	sm.cfg.Orchestrator.Restart.Schedule = []string{"1ms"}

	sm.state.Sessions["ben"] = &SessionState{
		ID: "ben", SystemKind: SystemKindOrchestrator,
		StopReason: StopReasonCrash, LastStartedAt: time.Now(),
	}

	var mu sync.Mutex

	calls := 0
	restored := make(chan struct{}, 1)

	sm.resumeFn = func(id string, _, _ uint16) (SessionState, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()

		if n == 1 {
			return SessionState{}, errors.New("transient resume failure")
		}

		select {
		case restored <- struct{}{}:
		default:
		}

		return SessionState{ID: id}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})

	go func() { sm.handleOrchestratorExit(ctx, "ben"); close(done) }()

	select {
	case <-restored:
	case <-time.After(4 * time.Second):
		t.Fatal("orchestrator was not restored after a transient resume failure")
	}

	<-done

	mu.Lock()
	got := calls
	mu.Unlock()

	if got < 2 {
		t.Fatalf("resume attempts = %d, want >= 2 (retried after the transient failure)", got)
	}
}

// TestHandleOrchestratorExitCancelStopsRetryLoop proves the reconciliation loop
// is cancellable: a cancelled context ends it after recording one backoff bump
// rather than spinning on a persistently-failing resume.
func TestHandleOrchestratorExitCancelStopsRetryLoop(t *testing.T) {
	sm := newOrchTestSM(t)
	sm.cfg.Orchestrator.Enabled = true
	sm.cfg.Orchestrator.Restart.Schedule = []string{"1ms"}

	sm.state.Sessions["ben"] = &SessionState{
		ID: "ben", SystemKind: SystemKindOrchestrator,
		StopReason: StopReasonCrash, LastStartedAt: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls int32

	sm.resumeFn = func(string, uint16, uint16) (SessionState, error) {
		calls++
		return SessionState{}, errors.New("always fails")
	}

	sm.handleOrchestratorExit(ctx, "ben")

	if calls != 0 {
		t.Fatalf("resume attempted %d times under a cancelled context, want 0", calls)
	}

	if got := sm.state.Sessions["ben"].BackoffLevel; got != 1 {
		t.Errorf("BackoffLevel = %d, want 1 (one pre-wait bump before cancel)", got)
	}
}
