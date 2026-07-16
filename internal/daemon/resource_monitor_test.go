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

	sm.logAbnormalExitReport(id, "dreich", StopReasonCrash, sess)
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
	category, source := classifyExit(StopReasonUser, -1, syscall.SIGTERM)
	if category != "signal-internal" || source != "graith" {
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
