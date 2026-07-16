package daemon

import (
	"context"
	"errors"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestParseProcessResources(t *testing.T) {
	got := parseProcessResources(`
  101   101  2048  1.5 sandbox-wrapper
  102   101 16384 22.4 /usr/local/bin/agent worker
 malformed row
`)
	if len(got) != 2 {
		t.Fatalf("parseProcessResources returned %d rows, want 2: %#v", len(got), got)
	}

	if got[1].pid != 102 || got[1].pgid != 101 || got[1].rssKB != 16384 || got[1].cpu != 22.4 {
		t.Errorf("second process = %#v", got[1])
	}

	if got[1].command != "/usr/local/bin/agent worker" {
		t.Errorf("command = %q", got[1].command)
	}
}

func TestReadProcessResources(t *testing.T) {
	original := processListOutput

	t.Cleanup(func() { processListOutput = original })

	processListOutput = func() ([]byte, error) { return []byte("42 42 1024 3.5 agent\n"), nil }

	got, err := readProcessResources()
	if err != nil || len(got) != 1 || got[0].pid != 42 {
		t.Fatalf("readProcessResources = %#v, %v", got, err)
	}

	processListOutput = func() ([]byte, error) { return nil, errors.New("denied") }

	if _, err := readProcessResources(); err == nil {
		t.Fatal("readProcessResources accepted command failure")
	}
}

func TestSampleSessionResourcesAggregatesProcessGroup(t *testing.T) {
	sm, buf := newLogCapturingManager(t)
	id := "braw-resources"
	sess := newTestPTYSession(t, "sleep", "100")
	t.Cleanup(func() {
		_ = sess.Kill()
		<-sess.Done()
		sess.Close()
	})

	pgid := sess.Pgid()
	sm.state.Sessions[id] = &SessionState{ID: id, Name: "braw", Status: StatusRunning}
	sm.sessions[id] = sess

	originalList, originalFDs := processListOutput, fdCountReader

	t.Cleanup(func() { processListOutput, fdCountReader = originalList, originalFDs })

	processListOutput = func() ([]byte, error) {
		return []byte(
			strconv.Itoa(sess.ProcessPID()) + " " + strconv.Itoa(pgid) + " 2048 1.5 wrapper\n" +
				"999 " + strconv.Itoa(pgid) + " 16384 22.5 agent\n"), nil
	}
	fdCountReader = func(_ []int) map[int]int {
		return map[int]int{sess.ProcessPID(): 8, 999: 40}
	}

	sm.sampleSessionResources()

	samples := sm.resourceSamples[id]
	if len(samples) != 1 {
		t.Fatalf("samples = %#v", samples)
	}

	got := samples[0]
	if got.RSSMB != 18 || got.CPUPercent != 24 || got.OpenFDs != 48 || got.ProcessCount != 2 || got.TopProcess != "agent" {
		t.Errorf("aggregate sample = %#v", got)
	}

	if rec := findRecord(logRecords(t, buf), "session resource sample"); rec == nil {
		t.Fatal("resource sample was not logged")
	}
}

func TestSampleSessionResourcesRetainsPartialFDCountAndCapsHistory(t *testing.T) {
	sm, _ := newLogCapturingManager(t)
	id := "braw-partial"
	sess := newTestPTYSession(t, "sleep", "100")
	t.Cleanup(func() {
		_ = sess.Kill()
		<-sess.Done()
		sess.Close()
	})

	pgid := sess.Pgid()
	sm.state.Sessions[id] = &SessionState{ID: id, Name: "braw", Status: StatusRunning}
	sm.sessions[id] = sess

	originalList, originalFDs := processListOutput, fdCountReader

	t.Cleanup(func() { processListOutput, fdCountReader = originalList, originalFDs })

	processListOutput = func() ([]byte, error) {
		return []byte(strconv.Itoa(sess.ProcessPID()) + " " + strconv.Itoa(pgid) + " 1024 1 agent\n" +
			"999 " + strconv.Itoa(pgid) + " 512 1 transient\n"), nil
	}
	fdCountReader = func(_ []int) map[int]int { return map[int]int{sess.ProcessPID(): 12} }

	for range resourceSampleHistory + 2 {
		sm.sampleSessionResources()
		sm.resourceMu.Lock()
		history := sm.resourceSamples[id]
		history[len(history)-1].At = time.Now().Add(-resourceSampleInterval)
		sm.resourceSamples[id] = history
		sm.resourceMu.Unlock()
	}

	samples := sm.resourceSamples[id]
	if len(samples) != resourceSampleHistory {
		t.Fatalf("history length = %d, want %d", len(samples), resourceSampleHistory)
	}

	last := samples[len(samples)-1]
	if last.OpenFDs != 12 || !last.FDsPartial {
		t.Errorf("partial FD sample = %#v", last)
	}
}

func TestCleanExitDiscardsResourceSamples(t *testing.T) {
	sm, _ := newLogCapturingManager(t)
	id := "braw-clean"
	sess := newTestPTYSession(t, "true")
	waitExit(t, sess)
	t.Cleanup(sess.Close)
	sm.resourceSamples[id] = []ResourceSample{{ProcessIDs: []int{sess.ProcessPID()}}}

	sm.logAbnormalExitReport(id, "braw", StopReasonCrash, sess, nil)

	if _, ok := sm.resourceSamples[id]; ok {
		t.Error("clean exit retained resource history")
	}
}

func TestDeletedSessionExitDiscardsResourceSamples(t *testing.T) {
	sm, _ := newLogCapturingManager(t)
	id := "braw-deleted"
	sess := newTestPTYSession(t, "true")
	waitExit(t, sess)
	sm.resourceSamples[id] = []ResourceSample{{ProcessIDs: []int{sess.ProcessPID()}}}

	// No state or live-driver entry models the hard-delete path: its watcher is
	// stale, but unlike a replaced generation there is no new session to own the
	// resource-history key.
	sm.watchSession(id, sess)

	if _, ok := sm.resourceSamples[id]; ok {
		t.Error("deleted session retained resource history")
	}
}

func TestSoftDeletedSessionExitDiscardsResourceSamples(t *testing.T) {
	sm, _ := newLogCapturingManager(t)
	id := "braw-soft-deleted"
	sess := newTestPTYSession(t, "true")
	waitExit(t, sess)

	now := time.Now()
	sm.state.Sessions[id] = &SessionState{ID: id, DeletedAt: &now}
	sm.resourceSamples[id] = []ResourceSample{{ProcessIDs: []int{sess.ProcessPID()}}}

	sm.watchSession(id, sess)

	if _, ok := sm.resourceSamples[id]; ok {
		t.Error("soft-deleted session retained resource history")
	}
}

func TestResourceMonitorStopsWithContext(t *testing.T) {
	sm, _ := newLogCapturingManager(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan struct{})

	go func() {
		sm.RunResourceMonitorLoop(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("resource monitor did not stop")
	}
}

func TestTakeResourceSamplesFiltersOldProcessGeneration(t *testing.T) {
	sm, _ := newLogCapturingManager(t)
	sm.resourceSamples["braw"] = []ResourceSample{
		{At: time.Unix(1, 0), ProcessIDs: []int{100}},
		{At: time.Unix(2, 0), ProcessIDs: []int{200, 201}},
		{At: time.Unix(3, 0), ProcessIDs: []int{200}},
	}

	got := sm.takeResourceSamples("braw", 200)
	if len(got) != 2 || !got[0].At.Equal(time.Unix(2, 0)) || !got[1].At.Equal(time.Unix(3, 0)) {
		t.Fatalf("takeResourceSamples = %#v, want only current PID generation", got)
	}

	if _, ok := sm.resourceSamples["braw"]; ok {
		t.Error("takeResourceSamples did not discard consumed history")
	}
}

func TestResourceSampleDuePreservesHistoryAcrossLaunchKicks(t *testing.T) {
	sm, _ := newLogCapturingManager(t)
	now := time.Now()

	sm.resourceSamples["braw"] = []ResourceSample{{At: now.Add(-time.Second)}}
	if sm.resourceSampleDue("braw", now) {
		t.Fatal("recently sampled session was due during launch burst")
	}

	sm.resourceSamples["braw"][0].At = now.Add(-resourceSampleInterval)
	if !sm.resourceSampleDue("braw", now) {
		t.Fatal("session was not due after sample interval")
	}

	if !sm.resourceSampleDue("new", now) {
		t.Fatal("new session did not receive immediate baseline")
	}
}

func TestSignalRequestIsBoundToProcessGeneration(t *testing.T) {
	sm, _ := newLogCapturingManager(t)
	sm.recordSignalRequest("braw", 101, syscall.SIGTERM, "user-stop")

	if got := sm.takeSignalRequest("braw", 202); got != nil {
		t.Fatalf("replacement generation consumed request: %#v", got)
	}

	got := sm.takeSignalRequest("braw", 101)
	if got == nil || got.Signal != syscall.SIGTERM || got.Initiator != "user-stop" {
		t.Fatalf("signal request = %#v", got)
	}
}

func TestSignalRequestSupportsNarrowManagerHarness(t *testing.T) {
	// Some daemon unit tests intentionally construct SessionManager directly.
	// Runtime-only diagnostic maps must initialize lazily on those paths.
	sm := &SessionManager{}
	sm.recordSignalRequest("braw", 101, syscall.SIGTERM, "watchdog")

	if got := sm.takeSignalRequest("braw", 101); got == nil {
		t.Fatal("signal request was not recorded on zero-value manager maps")
	}
}

func TestAbnormalExitReportAttributesUnrequestedSignal(t *testing.T) {
	sm, buf := newLogCapturingManager(t)
	id := "dreich-crash"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "dreich", Status: StatusRunning, Sandboxed: true,
	}

	sess := newTestPTYSession(t, "sh", "-c", "kill -TERM $$")
	t.Cleanup(sess.Close)
	waitExit(t, sess)
	sm.resourceSamples[id] = []ResourceSample{{
		At: time.Now(), RSSMB: 512, CPUPercent: 91.2, OpenFDs: 240,
		ProcessCount: 4, TopProcess: "agent", ProcessIDs: []int{sess.ProcessPID()},
	}}

	sm.logAbnormalExitReport(id, "dreich", StopReasonCrash, sess, nil)

	rec := findRecord(logRecords(t, buf), "session abnormal exit report")
	if rec == nil {
		t.Fatal("no abnormal exit report")
	}

	if rec["category"] != "signal-external-or-unknown" || rec["signal_source"] != "external-or-unknown" {
		t.Errorf("category/source = %v/%v", rec["category"], rec["signal_source"])
	}

	if rec["signal"] != "terminated" {
		t.Errorf("signal = %v, want terminated", rec["signal"])
	}

	samples, ok := rec["resource_samples"].([]any)
	if !ok || len(samples) != 1 {
		t.Fatalf("resource_samples = %#v, want one sample", rec["resource_samples"])
	}

	sample := samples[0].(map[string]any)
	if sample["rss_mb"] != float64(512) || sample["open_fds"] != float64(240) {
		t.Errorf("resource sample = %#v", sample)
	}
}

func TestClassifyExitAttributesDaemonSignal(t *testing.T) {
	request := &signalRequest{Signal: syscall.SIGTERM}

	category, source := classifyExit(-1, syscall.SIGTERM, request)
	if category != "signal-after-graith-request" || source != "graith-requested" {
		t.Errorf("category/source = %v/%v", category, source)
	}
}

func TestWatchSessionEmitsAbnormalExitReport(t *testing.T) {
	sm, buf := newLogCapturingManager(t)
	id := "haar-signal"
	sm.state.Sessions[id] = &SessionState{ID: id, Name: "haar", Status: StatusRunning}
	sess := newTestPTYSession(t, "sh", "-c", "kill -TERM $$")
	waitExit(t, sess)
	sm.sessions[id] = sess

	sm.watchSession(id, sess)

	records := logRecords(t, buf)

	exit := findRecord(records, "session exited")
	if exit == nil || exit["exit_category"] != "signal-external-or-unknown" {
		t.Fatalf("session exited record = %#v", exit)
	}

	if report := findRecord(records, "session abnormal exit report"); report == nil {
		t.Fatal("watchSession did not emit abnormal exit report")
	}
}

func TestWatchSessionDoesNotInferSignalRequestFromStopReason(t *testing.T) {
	sm, buf := newLogCapturingManager(t)
	id := "haar-hook-exit"
	// Models a hook-derived logout reason followed by an external SIGTERM: no
	// stopping-session request was recorded for this PID.
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "haar", Status: StatusRunning, StopReason: StopReasonUser,
	}
	sess := newTestPTYSession(t, "sh", "-c", "kill -TERM $$")
	waitExit(t, sess)
	sm.sessions[id] = sess
	sm.watchSession(id, sess)

	exit := findRecord(logRecords(t, buf), "session exited")
	if exit == nil || exit["signal_source"] != "external-or-unknown" {
		t.Fatalf("session exited record = %#v", exit)
	}
}

func TestWatchSessionReportsMatchingSignalRequest(t *testing.T) {
	sm, buf := newLogCapturingManager(t)
	id := "haar-requested-exit"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "haar", Status: StatusRunning, StopReason: StopReasonUser,
	}
	sess := newTestPTYSession(t, "sleep", "100")
	sm.sessions[id] = sess
	sm.logStopping(id, "haar", StopReasonUser, "test-stop", sess)

	if err := sess.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	waitExit(t, sess)
	sm.watchSession(id, sess)

	exit := findRecord(logRecords(t, buf), "session exited")
	if exit == nil || exit["signal_source"] != "graith-requested" || exit["signal_request_initiator"] != "test-stop" {
		t.Fatalf("session exited record = %#v", exit)
	}
}
