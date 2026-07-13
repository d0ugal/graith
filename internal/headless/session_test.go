package headless

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	grpty "github.com/d0ugal/graith/internal/pty"
)

// newBareSession builds a Session backed by a real scrollback file but with no
// child process, for exercising the pure state helpers directly.
func newBareSession(t *testing.T) *Session {
	t.Helper()

	sb, err := grpty.NewScrollback(filepath.Join(t.TempDir(), "scroll.log"), 0)
	if err != nil {
		t.Fatalf("NewScrollback: %v", err)
	}

	t.Cleanup(func() { _ = sb.Close() })

	return &Session{
		id:         "braw",
		scrollback: sb,
		status:     StatusActive,
		pending:    make(map[string]chan json.RawMessage),
		done:       make(chan struct{}),
		readDone:   make(chan struct{}),
	}
}

// startFake launches a real headless Session whose "agent" is a shell script.
func startFake(t *testing.T, script string, onPerm func(PermissionRequest) PermissionDecision) *Session {
	t.Helper()

	dir := t.TempDir()

	s, err := New(Opts{
		ID:           "braw",
		Command:      "sh",
		Args:         []string{"-c", script},
		Dir:          dir,
		LogPath:      filepath.Join(dir, "scrollback.log"),
		OnPermission: onPerm,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(s.Close)

	return s
}

func waitDone(t *testing.T, s *Session, d time.Duration) {
	t.Helper()

	select {
	case <-s.Done():
	case <-time.After(d):
		t.Fatalf("timeout waiting for session to finish")
	}
}

// --- unit tests on the state helpers ----------------------------------------

func TestSetStatusKeepsToolName(t *testing.T) {
	t.Parallel()

	s := newBareSession(t)

	s.setStatus(StatusApproval, "Bash")

	if snap := s.Snapshot(); snap.Status != StatusApproval || snap.ToolName != "Bash" {
		t.Fatalf("after setStatus: %+v", snap)
	}

	// An empty tool name must not clobber the retained one.
	s.setStatus(StatusReady, "")

	if snap := s.Snapshot(); snap.Status != StatusReady || snap.ToolName != "Bash" {
		t.Fatalf("after second setStatus: %+v", snap)
	}
}

func TestSetResult(t *testing.T) {
	t.Parallel()

	s := newBareSession(t)

	isErr := true
	cost := 1.5
	turns := 4

	var (
		dms  int64 = 1200
		dapi int64 = 900
	)

	ev := event{
		Type:        "result",
		IsError:     &isErr,
		TotalCost:   &cost,
		NumTurns:    &turns,
		DurationMS:  &dms,
		DurationAPI: &dapi,
		Usage:       json.RawMessage(`{"input_tokens":10}`),
		ResultText:  "bide",
	}

	s.setResult(ev)

	res := s.Snapshot().Result
	if res == nil {
		t.Fatal("Result is nil")
	}

	if !res.IsError || res.TotalCost != 1.5 || res.NumTurns != 4 || res.DurationMS != 1200 || res.DurationAPI != 900 {
		t.Fatalf("result mismatch: %+v", res)
	}

	if res.Text != "bide" || string(res.Usage) != `{"input_tokens":10}` {
		t.Fatalf("result text/usage mismatch: %+v", res)
	}

	if res.At.IsZero() {
		t.Fatal("result At not set")
	}
}

func TestSetResultDefaultsForNilPointers(t *testing.T) {
	t.Parallel()

	s := newBareSession(t)
	s.setResult(event{Type: "result"})

	res := s.Snapshot().Result
	if res == nil {
		t.Fatal("Result is nil")
	}

	if res.IsError || res.TotalCost != 0 || res.NumTurns != 0 || res.DurationMS != 0 || res.DurationAPI != 0 {
		t.Fatalf("expected zero-valued result, got %+v", res)
	}
}

func TestMarkDegradedAndTouch(t *testing.T) {
	t.Parallel()

	s := newBareSession(t)
	if s.Snapshot().Degraded {
		t.Fatal("expected not degraded initially")
	}

	s.markDegraded()

	if !s.Snapshot().Degraded {
		t.Fatal("expected degraded after markDegraded")
	}

	before := s.LastOutputAt()
	s.touch(7)

	if !s.LastOutputAt().After(before) && s.LastOutputAt().IsZero() {
		t.Fatal("touch did not update lastOutputAt")
	}

	if s.BytesRead() != 7 {
		t.Fatalf("BytesRead = %d, want 7", s.BytesRead())
	}
}

func TestAppendScrollbackFansToWriters(t *testing.T) {
	t.Parallel()

	s := newBareSession(t)

	var w1, w2 bytes.Buffer
	s.Attach(&w1)
	s.Attach(&w2)

	s.appendScrollback([]byte(`{"type":"system","session_id":"braw"}`))

	const want = "● session started (braw)\n"
	if w1.String() != want || w2.String() != want {
		t.Fatalf("attached writers: w1=%q w2=%q, want %q", w1.String(), w2.String(), want)
	}

	if got := s.ScreenPreview(); !strings.Contains(got, "session started (braw)") {
		t.Fatalf("ScreenPreview = %q", got)
	}

	if frame := s.ScreenSnapshot().Frame; !strings.Contains(frame, "session started (braw)") {
		t.Fatalf("ScreenSnapshot frame = %q", frame)
	}
}

func TestDetachWriterRemovesOne(t *testing.T) {
	t.Parallel()

	s := newBareSession(t)

	var w1, w2 bytes.Buffer
	s.Attach(&w1)
	s.Attach(&w2)
	s.DetachWriter(&w1)

	s.appendScrollback([]byte("dreich banner"))

	if w1.Len() != 0 {
		t.Fatalf("detached writer w1 still got %q", w1.String())
	}

	if w2.String() != "dreich banner\n" {
		t.Fatalf("w2 = %q", w2.String())
	}
}

func TestDetachClearsAllWriters(t *testing.T) {
	t.Parallel()

	s := newBareSession(t)

	var w bytes.Buffer
	s.Attach(&w)
	s.Detach()

	s.appendScrollback([]byte("dreich banner"))

	if w.Len() != 0 {
		t.Fatalf("detached writer still got %q", w.String())
	}
}

func TestDeliverResponse(t *testing.T) {
	t.Parallel()

	s := newBareSession(t)

	ch := make(chan json.RawMessage, 1)
	s.pending["req-1"] = ch

	body := json.RawMessage(`{"ok":true}`)
	s.deliverResponse("req-1", body)

	select {
	case got := <-ch:
		if string(got) != string(body) {
			t.Fatalf("delivered %s, want %s", got, body)
		}
	default:
		t.Fatal("response was not delivered to the waiter")
	}

	if s.Snapshot().Degraded {
		t.Fatal("matched delivery should not mark degraded")
	}
}

func TestDeliverResponseEmptyIDMarksDegraded(t *testing.T) {
	t.Parallel()

	s := newBareSession(t)
	s.deliverResponse("", json.RawMessage(`{}`))

	if !s.Snapshot().Degraded {
		t.Fatal("empty request id should mark the session degraded")
	}
}

func TestDeliverResponseUnmatchedIDTolerated(t *testing.T) {
	t.Parallel()

	s := newBareSession(t)
	s.deliverResponse("nae-such", json.RawMessage(`{}`)) // must not panic

	if s.Snapshot().Degraded {
		t.Fatal("an unmatched id is tolerated, not degraded")
	}
}

func TestControlAfterExitErrors(t *testing.T) {
	t.Parallel()

	s := &Session{
		pending:  make(map[string]chan json.RawMessage),
		done:     make(chan struct{}),
		readDone: make(chan struct{}),
		exited:   true,
	}

	// Interrupt is a best-effort SIGINT to the process group. With no process
	// (ProcessPID == 0) it is a harmless no-op, not an error.
	if err := s.Interrupt(1, time.Second); err != nil {
		t.Fatalf("Interrupt with no process should be a no-op, got %v", err)
	}

	// ContextUsage is a stdin control request and must fail once exited.
	if _, err := s.ContextUsage(); err == nil {
		t.Fatal("ContextUsage after exit should error")
	}
}

func TestWriteInputAfterExitErrors(t *testing.T) {
	t.Parallel()

	s := &Session{exited: true}

	if err := s.WriteInput([]byte("bide")); err == nil {
		t.Fatal("WriteInput after exit should error")
	}

	if err := s.WriteInputAndSubmit([]byte("bide")); err == nil {
		t.Fatal("WriteInputAndSubmit after exit should error")
	}
}

func TestNoOpAndConstantSurfaces(t *testing.T) {
	t.Parallel()

	s := &Session{}

	if s.ProcessPID() != 0 {
		t.Fatalf("ProcessPID = %d, want 0", s.ProcessPID())
	}

	if s.Fd() != 0 {
		t.Fatalf("Fd = %d, want 0", s.Fd())
	}

	if s.PeakRSSBytes() != 0 {
		t.Fatalf("PeakRSSBytes = %d, want 0", s.PeakRSSBytes())
	}

	if s.RecentlyAdopted(time.Minute) {
		t.Fatal("RecentlyAdopted should be false")
	}

	if err := s.Resize(80, 24); err != nil {
		t.Fatalf("Resize should be a no-op, got %v", err)
	}

	if !s.WaitForUserIdle(time.Second, time.Second) {
		t.Fatal("WaitForUserIdle should return true")
	}

	// Poke and NotifyUserInput are pure no-ops; call them for coverage and to
	// prove they don't panic.
	s.Poke()
	s.NotifyUserInput()
}

// --- end-to-end tests with a fake agent -------------------------------------

func TestEndToEndReady(t *testing.T) {
	t.Parallel()

	script := `printf '%s\n' ` +
		`'{"type":"system","session_id":"braw"}' ` +
		`'{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"blether"}]}}' ` +
		`'{"type":"result","is_error":false,"total_cost_usd":0.1234,"num_turns":3,"result":"bide"}'`

	s := startFake(t, script, nil)
	waitDone(t, s, 10*time.Second)

	snap := s.Snapshot()
	if snap.Status != StatusReady {
		t.Fatalf("status = %q, want ready", snap.Status)
	}

	if snap.Degraded {
		t.Fatal("clean stream should not be degraded")
	}

	if snap.Result == nil {
		t.Fatal("result envelope not captured")
	}

	if snap.Result.IsError || snap.Result.TotalCost != 0.1234 || snap.Result.NumTurns != 3 {
		t.Fatalf("result envelope mismatch: %+v", snap.Result)
	}

	if s.ExitCode() != 0 {
		t.Fatalf("ExitCode = %d, want 0", s.ExitCode())
	}

	if !s.Exited() {
		t.Fatal("Exited should be true")
	}

	if preview := s.ScreenPreview(); !strings.Contains(preview, "● result (turns=3, cost=$0.1234)") {
		t.Fatalf("scrollback missing rendered result line:\n%s", preview)
	}
}

func TestEndToEndErroredResult(t *testing.T) {
	t.Parallel()

	script := `printf '%s\n' ` +
		`'{"type":"system","session_id":"canny"}' ` +
		`'{"type":"result","is_error":true,"total_cost_usd":0.02,"num_turns":1,"result":"thrawn"}'`

	s := startFake(t, script, nil)
	waitDone(t, s, 10*time.Second)

	res := s.Snapshot().Result
	if res == nil {
		t.Fatal("result envelope not captured")
	}

	if !res.IsError {
		t.Fatal("envelope.IsError should be true for an errored result")
	}

	if preview := s.ScreenPreview(); !strings.Contains(preview, "[error]") {
		t.Fatalf("scrollback missing error flag:\n%s", preview)
	}
}

func TestEndToEndMalformedLineSkipped(t *testing.T) {
	t.Parallel()

	// A non-JSON line mid-stream must not break parsing of later valid lines,
	// and must appear verbatim in the scrollback.
	script := `printf '%s\n' ` +
		`'{"type":"system","session_id":"haar"}' ` +
		`'this is not json at all' ` +
		`'{"type":"result","is_error":false,"total_cost_usd":0.01,"num_turns":2,"result":"bonnie"}'`

	s := startFake(t, script, nil)
	waitDone(t, s, 10*time.Second)

	snap := s.Snapshot()
	if snap.Status != StatusReady {
		t.Fatalf("status = %q, want ready (later valid lines must still parse)", snap.Status)
	}

	if snap.Result == nil || snap.Result.NumTurns != 2 {
		t.Fatalf("result after malformed line not captured: %+v", snap.Result)
	}

	if preview := s.ScreenPreview(); !strings.Contains(preview, "this is not json at all") {
		t.Fatalf("malformed line missing from scrollback:\n%s", preview)
	}
}

func TestEndToEndExitCode(t *testing.T) {
	t.Parallel()

	s := startFake(t, `exit 3`, nil)
	waitDone(t, s, 10*time.Second)

	if s.ExitCode() != 3 {
		t.Fatalf("ExitCode = %d, want 3", s.ExitCode())
	}
}

func TestEndToEndPermissionApprovedRoundTrip(t *testing.T) {
	t.Parallel()

	gotReq := make(chan PermissionRequest, 1)
	onPerm := func(r PermissionRequest) PermissionDecision {
		gotReq <- r

		return PermissionDecision{Allow: true, Reason: "bonnie"}
	}

	// The fake emits a can_use_tool request, then reads the control_response
	// graith writes back and echoes it to stdout (prefixed so it renders as a
	// verbatim banner the test can inspect).
	script := `printf '%s\n' '{"type":"control_request","request_id":"ctl-1","request":{"subtype":"can_use_tool","tool_name":"Bash"}}'; IFS= read -r line; printf 'RESP %s\n' "$line"`

	s := startFake(t, script, onPerm)

	select {
	case req := <-gotReq:
		if req.RequestID != "ctl-1" {
			t.Fatalf("request id = %q, want ctl-1", req.RequestID)
		}

		if req.ToolName != "Bash" {
			t.Fatalf("tool name = %q, want Bash", req.ToolName)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OnPermission callback was never invoked")
	}

	waitDone(t, s, 10*time.Second)

	preview := s.ScreenPreview()
	if !strings.Contains(preview, `"behavior":"allow"`) {
		t.Fatalf("expected an allow control_response in scrollback:\n%s", preview)
	}

	if !strings.Contains(preview, `"request_id":"ctl-1"`) {
		t.Fatalf("control_response missing request id:\n%s", preview)
	}

	if s.Snapshot().Degraded {
		t.Fatal("a well-formed approval round-trip should not be degraded")
	}
}

func TestEndToEndPermissionFailClosed(t *testing.T) {
	t.Parallel()

	// With OnPermission nil the session must fail closed: deny with a reason,
	// without invoking any callback path.
	script := `printf '%s\n' '{"type":"control_request","request_id":"ctl-9","request":{"subtype":"can_use_tool","tool_name":"Bash"}}'; IFS= read -r line; printf 'RESP %s\n' "$line"`

	s := startFake(t, script, nil)
	waitDone(t, s, 10*time.Second)

	preview := s.ScreenPreview()
	if !strings.Contains(preview, `"behavior":"deny"`) {
		t.Fatalf("expected a deny control_response in scrollback:\n%s", preview)
	}

	if !strings.Contains(preview, "no approval backend") {
		t.Fatalf("expected the fail-closed reason in scrollback:\n%s", preview)
	}

	if s.Snapshot().Status != StatusApproval {
		t.Fatalf("status = %q, want approval", s.Snapshot().Status)
	}
}

func TestEndToEndControlResponseEmptyIDDegrades(t *testing.T) {
	t.Parallel()

	// A control_response with no request_id is a malformed control frame and
	// must flag the session degraded.
	script := `printf '%s\n' '{"type":"control_response","request_id":"","response":{}}'`

	s := startFake(t, script, nil)
	waitDone(t, s, 10*time.Second)

	if !s.Snapshot().Degraded {
		t.Fatal("empty-id control_response should mark the session degraded")
	}
}

func TestEndToEndControlRoundTrip(t *testing.T) {
	t.Parallel()

	// The fake reads graith's control_request, then replies with a matching
	// control_response. The first control request graith sends is deterministic
	// ("req-1"), which lets the fake reply without parsing the id.
	script := `IFS= read -r line; printf '%s\n' '{"type":"control_response","request_id":"req-1","response":{"tokens":42}}'`

	s := startFake(t, script, nil)

	resp, err := s.ContextUsage()
	if err != nil {
		t.Fatalf("ContextUsage: %v", err)
	}

	if !strings.Contains(string(resp), "42") {
		t.Fatalf("control response = %s, want it to contain 42", resp)
	}

	waitDone(t, s, 10*time.Second)
}

func TestScrollbackFile(t *testing.T) {
	t.Parallel()

	s := newBareSession(t)
	if s.ScrollbackFile() == nil {
		t.Fatal("ScrollbackFile returned nil")
	}
}

func TestKillAndForceKillNoProcessAreNoops(t *testing.T) {
	t.Parallel()

	s := &Session{}
	if err := s.Kill(); err != nil {
		t.Fatalf("Kill with no process = %v, want nil", err)
	}

	if err := s.ForceKill(); err != nil {
		t.Fatalf("ForceKill with no process = %v, want nil", err)
	}
}

func TestExitSignalReflectsSignaledExit(t *testing.T) {
	t.Parallel()

	// The fake terminates itself with SIGTERM so waitLoop records the signal.
	s := startFake(t, `kill -TERM $$`, nil)
	waitDone(t, s, 10*time.Second)

	if s.ExitSignal() != syscall.SIGTERM {
		t.Fatalf("ExitSignal = %v, want SIGTERM", s.ExitSignal())
	}
}

func TestKillTerminatesRunningProcess(t *testing.T) {
	t.Parallel()

	s := startFake(t, `sleep 30`, nil)

	if s.ProcessPID() <= 0 {
		t.Fatalf("ProcessPID = %d, want > 0 for a live process", s.ProcessPID())
	}

	if err := s.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	waitDone(t, s, 10*time.Second)

	if !s.Exited() {
		t.Fatal("session should have exited after Kill")
	}
}

func TestForceKillTerminatesRunningProcess(t *testing.T) {
	t.Parallel()

	s := startFake(t, `sleep 30`, nil)

	if err := s.ForceKill(); err != nil {
		t.Fatalf("ForceKill: %v", err)
	}

	waitDone(t, s, 10*time.Second)

	if !s.Exited() {
		t.Fatal("session should have exited after ForceKill")
	}
}

func TestEndToEndWriteInputLive(t *testing.T) {
	t.Parallel()

	// A fake that echoes one line of stdin to stdout, so WriteInput's stdin
	// write is exercised against a live process.
	script := `IFS= read -r line; printf 'GOT %s\n' "$line"`

	s := startFake(t, script, nil)

	if err := s.WriteInput([]byte("bide a wee")); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}

	waitDone(t, s, 10*time.Second)

	if preview := s.ScreenPreview(); !strings.Contains(preview, "bide a wee") {
		t.Fatalf("fake did not echo the written input:\n%s", preview)
	}
}
