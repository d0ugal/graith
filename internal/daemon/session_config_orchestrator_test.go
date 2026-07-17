package daemon

import (
	"context"
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

// TestStopReasonRollbackUnwindsConcurrentFailures proves the optimistic reason
// stack returns to its baseline when both attempts fail, regardless of which
// blocked driver call completes first.
func TestStopReasonRollbackUnwindsConcurrentFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		completion []int
	}{
		{name: "older first", completion: []int{1, 2}},
		{name: "newer first", completion: []int{2, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSessionManager(t)
			driver := newControlledStopDriver(2)
			const id = "stop-dreich"

			putSession(sm, &SessionState{
				ID:         id,
				Name:       "dreich",
				Status:     StatusRunning,
				StopReason: StopReasonShutdown,
			})
			sm.mu.Lock()
			sm.sessions[id] = driver
			sm.mu.Unlock()

			results := map[int]chan error{1: make(chan error, 1), 2: make(chan error, 1)}
			go func() { results[1] <- sm.stopWithReason(id, StopReasonIdle, "idle-loop") }()
			awaitStopCall(t, driver, 1)
			go func() { results[2] <- sm.stopWithReason(id, StopReasonUser, "user-stop") }()
			awaitStopCall(t, driver, 2)

			for _, call := range tc.completion {
				wantErr := errors.New("controlled stop failure")
				driver.release(call, wantErr)
				if err := awaitStopResult(t, results[call]); !errors.Is(err, wantErr) {
					t.Fatalf("stop %d error = %v, want %v", call, err, wantErr)
				}
			}

			sm.mu.RLock()
			got := sm.state.Sessions[id].StopReason
			sm.mu.RUnlock()
			if got != StopReasonShutdown {
				t.Fatalf("final StopReason = %q, want baseline %q", got, StopReasonShutdown)
			}
		})
	}
}

// TestStopReasonRollbackDoesNotEraseBulkStopReason covers a newer reason writer
// outside stopWithReason. The bulk user stop invalidates A's optimistic token,
// so A's later failure cannot restore its stale baseline.
func TestStopReasonRollbackDoesNotEraseBulkStopReason(t *testing.T) {
	sm := newTestSessionManager(t)
	driver := newControlledStopDriver(2)

	putSession(sm, &SessionState{ID: "root-bothy", Name: "bothy", Status: StatusStopped})
	putSession(sm, &SessionState{
		ID:       "child-bairn",
		ParentID: "root-bothy",
		Name:     "bairn",
		Status:   StatusRunning,
	})
	sm.mu.Lock()
	sm.sessions["child-bairn"] = driver
	sm.mu.Unlock()

	idleResult := make(chan error, 1)
	go func() {
		idleResult <- sm.stopWithReason("child-bairn", StopReasonIdle, "idle-loop")
	}()
	awaitStopCall(t, driver, 1)

	bulkResult := make(chan error, 1)
	go func() {
		_, err := sm.StopWithChildren("root-bothy", true)
		bulkResult <- err
	}()
	awaitStopCall(t, driver, 2)
	driver.release(2, nil)
	if err := awaitStopResult(t, bulkResult); err != nil {
		t.Fatalf("bulk stop failed: %v", err)
	}

	wantIdleErr := errors.New("idle stop failed")
	driver.release(1, wantIdleErr)
	if err := awaitStopResult(t, idleResult); !errors.Is(err, wantIdleErr) {
		t.Fatalf("idle stop error = %v, want %v", err, wantIdleErr)
	}

	sm.mu.RLock()
	got := sm.state.Sessions["child-bairn"].StopReason
	sm.mu.RUnlock()
	if got != StopReasonUser {
		t.Fatalf("final StopReason = %q, want bulk user reason %q", got, StopReasonUser)
	}
}

func TestStopWithReasonPersistenceFailureDoesNotSignal(t *testing.T) {
	sm := newTestSessionManager(t)
	driver := newControlledStopDriver(1)
	const id = "stop-croft"

	putSession(sm, &SessionState{
		ID:         id,
		Name:       "croft",
		Status:     StatusRunning,
		StopReason: StopReasonShutdown,
	})
	sm.mu.Lock()
	sm.sessions[id] = driver
	if err := sm.saveState(); err != nil {
		sm.mu.Unlock()
		t.Fatal(err)
	}
	sm.mu.Unlock()

	wantErr := errors.New("dreich disk")
	faultCalls := 0
	sm.saveStateFault = func() error {
		faultCalls++
		if faultCalls == 1 {
			return wantErr
		}

		return nil
	}

	result := make(chan error, 1)
	go func() { result <- sm.stopWithReason(id, StopReasonUser, "user-stop") }()

	select {
	case err := <-result:
		if !errors.Is(err, wantErr) {
			t.Fatalf("stop error = %v, want %v", err, wantErr)
		}
	case call := <-driver.started:
		driver.release(call, nil)
		t.Fatal("driver was signaled after the stop reason failed to persist")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for persistence failure")
	}

	sm.mu.RLock()
	gotReason := sm.state.Sessions[id].StopReason
	_, pending := sm.stopAttempts[id]
	sm.mu.RUnlock()
	if gotReason != StopReasonShutdown || pending {
		t.Fatalf("in-memory rollback = reason %q pending %v, want %q/false", gotReason, pending, StopReasonShutdown)
	}

	persisted, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := persisted.Sessions[id].StopReason; got != StopReasonShutdown {
		t.Fatalf("persisted StopReason = %q, want %q", got, StopReasonShutdown)
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

func TestReloadConfigRejectsOrchestratorGenerationCreatedAfterSnapshot(t *testing.T) {
	cfg := config.Default()
	cfg.Orchestrator.Enabled = true
	sm := newSMWithConfig(t, cfg)

	entered := make(chan struct{})
	release := make(chan struct{})
	orchestratorDisableSnapshotHook = func() {
		close(entered)
		<-release
	}
	t.Cleanup(func() { orchestratorDisableSnapshotHook = nil })

	result := make(chan error, 1)
	go func() { result <- sm.applyConfig(config.Default()) }()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for orchestrator snapshot")
	}

	putSession(sm, &SessionState{
		ID:         "orch-strath",
		Name:       OrchestratorSessionName,
		Status:     StatusCreating,
		SystemKind: SystemKindOrchestrator,
	})
	close(release)

	err := awaitStopResult(t, result)
	assertErrContains(t, err, "orchestrator.enabled")
	assertErrContains(t, err, "orch-strath")
	if !sm.Config().Orchestrator.Enabled {
		t.Error("disable was published over a newly reserved orchestrator generation")
	}
}

func TestReloadConfigRetriesErroredOrchestratorOrphan(t *testing.T) {
	cfg := config.Default()
	cfg.Orchestrator.Enabled = true
	sm := newSMWithConfig(t, cfg)
	pid := spawnContainedSleeper(t)

	putSession(sm, &SessionState{
		ID:         "orch-blether",
		Name:       OrchestratorSessionName,
		Status:     StatusRunning,
		SystemKind: SystemKindOrchestrator,
		PID:        pid,
		// No PIDStartTime deliberately makes the contained process unverifiable,
		// forcing a deterministic kill failure without timing or real signals.
	})

	configFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configFile, []byte("[orchestrator]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sm.configFile = configFile

	for attempt := 1; attempt <= 2; attempt++ {
		err := sm.ReloadConfig()
		assertErrContains(t, err, "orchestrator.enabled")
		assertErrContains(t, err, "orch-blether")
		assertErrContains(t, err, "no process identity recorded")
		if !sm.Config().Orchestrator.Enabled {
			t.Fatalf("attempt %d published disable despite live orphan", attempt)
		}

		sm.mu.RLock()
		status := sm.state.Sessions["orch-blether"].Status
		gotPID := sm.state.Sessions["orch-blether"].PID
		sm.mu.RUnlock()
		if status != StatusErrored || gotPID != pid {
			t.Fatalf("attempt %d state = status %q PID %d, want errored/%d", attempt, status, gotPID, pid)
		}
	}
}

func TestWatchedConfigReReadsLatestGeneration(t *testing.T) {
	sm := newTestSessionManager(t)
	configFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configFile, []byte("default_agent = \"codex\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sm.configFile = configFile

	stale := config.Default()
	stale.DefaultAgent = "claude"
	if err := sm.applyWatchedConfig(stale); err != nil {
		t.Fatal(err)
	}
	if got := sm.Config().DefaultAgent; got != "codex" {
		t.Fatalf("DefaultAgent = %q, want latest on-disk generation %q", got, "codex")
	}
}

func TestDisabledOrchestratorCannotReserveCreateOrResume(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		sm := newSMWithConfig(t, config.Default())
		sm.sandboxResolver = func(string) (bool, error) { return true, nil }

		_, err := sm.createOrchestrator(context.Background())
		assertErrContains(t, err, "orchestrator is disabled")
		if id := sm.findOrchestratorID(); id != "" {
			t.Fatalf("disabled create reserved orchestrator %q", id)
		}
	})

	t.Run("resume", func(t *testing.T) {
		sm := newSMWithConfig(t, config.Default())
		putSession(sm, &SessionState{
			ID:         "orch-canny-resume",
			Name:       OrchestratorSessionName,
			Status:     StatusStopped,
			SystemKind: SystemKindOrchestrator,
		})

		_, err := sm.Resume("orch-canny-resume", 24, 80)
		assertErrContains(t, err, "orchestrator is disabled")
	})
}
