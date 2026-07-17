package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// controlledStopDriver lets tests choose when each Kill call returns and with
// which result. Calls are numbered in entry order, making concurrent stop
// interleavings deterministic without sleeps.
type controlledStopDriver struct {
	SessionDriver

	mu       sync.Mutex
	calls    int
	started  chan int
	releases []chan error
}

func newControlledStopDriver(calls int) *controlledStopDriver {
	releases := make([]chan error, calls)
	for i := range releases {
		releases[i] = make(chan error, 1)
	}

	return &controlledStopDriver{
		started:  make(chan int, calls),
		releases: releases,
	}
}

func (d *controlledStopDriver) ProcessPID() int { return 4242 }
func (d *controlledStopDriver) Pgid() int       { return 4242 }

func (d *controlledStopDriver) Kill() error {
	d.mu.Lock()
	d.calls++
	call := d.calls
	d.mu.Unlock()

	d.started <- call

	return <-d.releases[call-1]
}

func (d *controlledStopDriver) release(call int, err error) {
	d.releases[call-1] <- err
}

func awaitStopCall(t *testing.T, driver *controlledStopDriver, want int) {
	t.Helper()

	select {
	case got := <-driver.started:
		if got != want {
			t.Fatalf("Kill call = %d, want %d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for Kill call %d", want)
	}
}

func awaitStopResult(t *testing.T, result <-chan error) error {
	t.Helper()

	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stop result")
		return nil
	}
}

// TestStopReasonRollbackDoesNotEraseNewerSuccessfulStop is the concurrency
// regression for issue #1324. Stop A writes idle and blocks in the driver; stop
// B then writes user and succeeds before A fails. A must not restore its stale
// previous reason over B's successful user stop.
func TestStopReasonRollbackDoesNotEraseNewerSuccessfulStop(t *testing.T) {
	sm := newTestSessionManager(t)
	driver := newControlledStopDriver(2)

	putSession(sm, &SessionState{ID: "braw-stop", Name: "braw", Status: StatusRunning})
	sm.mu.Lock()
	sm.sessions["braw-stop"] = driver
	sm.mu.Unlock()

	idleResult := make(chan error, 1)
	go func() {
		idleResult <- sm.stopWithReason("braw-stop", StopReasonIdle, "idle-loop")
	}()

	awaitStopCall(t, driver, 1)

	if got := sm.state.Sessions["braw-stop"].StopReason; got != StopReasonIdle {
		t.Fatalf("reason after stop A write = %q, want %q", got, StopReasonIdle)
	}

	userResult := make(chan error, 1)
	go func() {
		userResult <- sm.stopWithReason("braw-stop", StopReasonUser, "user-stop")
	}()

	awaitStopCall(t, driver, 2)
	driver.release(2, nil)

	if err := awaitStopResult(t, userResult); err != nil {
		t.Fatalf("newer user stop failed: %v", err)
	}

	wantIdleErr := errors.New("idle signal failed")
	driver.release(1, wantIdleErr)

	if err := awaitStopResult(t, idleResult); !errors.Is(err, wantIdleErr) {
		t.Fatalf("older idle stop error = %v, want %v", err, wantIdleErr)
	}

	sm.mu.RLock()
	got := sm.state.Sessions["braw-stop"].StopReason
	sm.mu.RUnlock()

	if got != StopReasonUser {
		t.Fatalf("final StopReason = %q, want newer successful reason %q", got, StopReasonUser)
	}
}

// TestReloadConfigOrchestratorDisableFailureAndRetry proves a failed signal
// rejects the disable without publishing it, reports the field/session in both
// the returned error and structured log, then succeeds deterministically on a
// later reload of the same file.
func TestReloadConfigOrchestratorDisableFailureAndRetry(t *testing.T) {
	sm, logBuf := newLogCapturingManager(t)
	sm.cfg.Orchestrator.Enabled = true

	driver := newControlledStopDriver(2)
	putSession(sm, &SessionState{
		ID:         "orch-canny",
		Name:       OrchestratorSessionName,
		Status:     StatusRunning,
		SystemKind: SystemKindOrchestrator,
	})

	sm.mu.Lock()
	sm.sessions["orch-canny"] = driver
	sm.mu.Unlock()

	configFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configFile, []byte("[orchestrator]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm.configFile = configFile
	firstResult := make(chan error, 1)
	go func() { firstResult <- sm.ReloadConfig() }()

	awaitStopCall(t, driver, 1)
	wantErr := errors.New("canny driver refused SIGTERM")
	driver.release(1, wantErr)

	err := awaitStopResult(t, firstResult)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ReloadConfig error = %v, want wrapped %v", err, wantErr)
	}

	for _, want := range []string{"orchestrator.enabled", "orch-canny"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ReloadConfig error %q does not contain %q", err, want)
		}
	}

	sm.mu.RLock()
	enabledAfterFailure := sm.cfg.Orchestrator.Enabled
	reasonAfterFailure := sm.state.Sessions["orch-canny"].StopReason
	sm.mu.RUnlock()

	if !enabledAfterFailure {
		t.Error("orchestrator.enabled was published false after its stop failed")
	}

	if reasonAfterFailure != "" {
		t.Errorf("StopReason after failed disable = %q, want rolled back empty reason", reasonAfterFailure)
	}

	record := findRecord(logRecords(t, logBuf), "config change failed")
	if record == nil {
		t.Fatal("missing config change failure log")
	}

	if record["key"] != "orchestrator.enabled" || record["session_id"] != "orch-canny" {
		t.Errorf("config failure log fields = key:%v session_id:%v", record["key"], record["session_id"])
	}

	retryResult := make(chan error, 1)
	go func() { retryResult <- sm.ReloadConfig() }()

	awaitStopCall(t, driver, 2)
	driver.release(2, nil)

	if err := awaitStopResult(t, retryResult); err != nil {
		t.Fatalf("retry ReloadConfig error = %v", err)
	}

	sm.mu.RLock()
	enabledAfterRetry := sm.cfg.Orchestrator.Enabled
	reasonAfterRetry := sm.state.Sessions["orch-canny"].StopReason
	sm.mu.RUnlock()

	if enabledAfterRetry {
		t.Error("orchestrator.enabled remained true after successful retry")
	}

	if reasonAfterRetry != StopReasonUser {
		t.Errorf("StopReason after successful retry = %q, want %q", reasonAfterRetry, StopReasonUser)
	}
}

func TestReloadConfigRejectsOrchestratorDisableDuringCreation(t *testing.T) {
	cfg := config.Default()
	cfg.Orchestrator.Enabled = true
	sm := newSMWithConfig(t, cfg)

	putSession(sm, &SessionState{
		ID:         "orch-thrawn",
		Name:       OrchestratorSessionName,
		Status:     StatusCreating,
		SystemKind: SystemKindOrchestrator,
	})

	disabled := config.Default()
	err := sm.applyConfig(disabled)
	assertErrContains(t, err, "orchestrator.enabled")
	assertErrContains(t, err, "orch-thrawn")

	if !sm.Config().Orchestrator.Enabled {
		t.Error("orchestrator disable was published while its session was still creating")
	}
}
