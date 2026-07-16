package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

// withLoopSeams points freshClient at the given fake and records restoreScreen
// calls, restoring both on cleanup. Output is discarded. Returns a pointer to
// the slice of session IDs restoreScreen was called with, in order.
func withLoopSeams(t *testing.T, fake *scriptedConn) *[]string {
	t.Helper()

	withDiscardOutput(t)

	origFresh := freshClient
	freshClient = func() (attachConn, error) { return fake, nil }

	restored := []string{}
	origRestore := restoreScreen
	restoreScreen = func(id string) { restored = append(restored, id) }

	t.Cleanup(func() {
		freshClient = origFresh
		restoreScreen = origRestore
	})

	return &restored
}

// newLoop builds an attachLoop with opts.Info aliased to its info field, the
// invariant the whole state machine depends on.
func newLoop(sessionID, prevID string) *attachLoop {
	l := &attachLoop{sessionID: sessionID, prevSessionID: prevID}
	l.opts.Info = &l.info

	return l
}

// assertAliased fails if opts.Info no longer points at the loop's info field —
// the aliasing that lets RunPassthrough see freshly-decoded info.
func assertAliased(t *testing.T, l *attachLoop) {
	t.Helper()

	if l.opts.Info != &l.info {
		t.Error("opts.Info is no longer aliased to &l.info")
	}
}

// --- state-transition helpers ----------------------------------------------

func TestAdoptCurrent(t *testing.T) {
	restored := withLoopSeams(t, nil)

	l := newLoop("braw", "")
	nc := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("attached", protocol.SessionInfo{ID: "braw", Name: "bonnie"})),
	}}

	l.adoptCurrent(nc)

	if l.c != nc {
		t.Error("adoptCurrent did not install nc as the live connection")
	}

	if l.opts.SessionID != "braw" || l.info.Name != "bonnie" {
		t.Errorf("opts.SessionID=%q info.Name=%q, want braw/bonnie", l.opts.SessionID, l.info.Name)
	}

	assertAliased(t, l)

	if len(*restored) != 0 {
		t.Errorf("adoptCurrent must not restore the screen, got %v", *restored)
	}

	if got := nc.sentTypes(); len(got) != 1 || got[0] != "attach" {
		t.Errorf("sent = %v, want [attach]", got)
	}
}

func TestRestoreAndAdopt(t *testing.T) {
	restored := withLoopSeams(t, nil)

	l := newLoop("braw", "")
	nc := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("attached", protocol.SessionInfo{ID: "braw"}))}}

	l.restoreAndAdopt(nc)

	if len(*restored) != 1 || (*restored)[0] != "braw" {
		t.Errorf("restoreScreen calls = %v, want [braw]", *restored)
	}

	if l.c != nc {
		t.Error("restoreAndAdopt did not install nc")
	}
}

func TestSwitchTo(t *testing.T) {
	restored := withLoopSeams(t, nil)

	l := newLoop("auld", "older")
	nc := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("attached", protocol.SessionInfo{ID: "new", Name: "bonnie"}))}}

	l.switchTo(nc, "new")

	if l.sessionID != "new" {
		t.Errorf("sessionID = %q, want new", l.sessionID)
	}

	if l.prevSessionID != "auld" {
		t.Errorf("prevSessionID = %q, want auld (the session we switched away from)", l.prevSessionID)
	}

	if l.opts.SessionID != "new" || l.info.Name != "bonnie" {
		t.Errorf("opts.SessionID=%q info.Name=%q, want new/bonnie", l.opts.SessionID, l.info.Name)
	}

	assertAliased(t, l)

	if len(*restored) != 1 || (*restored)[0] != "new" {
		t.Errorf("restoreScreen calls = %v, want [new] (repaint the target)", *restored)
	}
}

// --- handlers reachable via the freshClient / restoreScreen seams ----------

func TestOnLastSession(t *testing.T) {
	t.Run("swaps when a previous session exists", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("attached", protocol.SessionInfo{ID: "auld"}))}}
		withLoopSeams(t, fake)

		l := newLoop("braw", "auld")

		done, err := l.onLastSession()
		if done || err != nil {
			t.Fatalf("onLastSession = (%v,%v), want (false,nil)", done, err)
		}

		if l.sessionID != "auld" || l.prevSessionID != "braw" {
			t.Errorf("session/prev = %q/%q, want auld/braw (swapped)", l.sessionID, l.prevSessionID)
		}
	})

	t.Run("no previous session keeps current", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("attached", protocol.SessionInfo{ID: "braw"}))}}
		withLoopSeams(t, fake)

		l := newLoop("braw", "")

		if _, err := l.onLastSession(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if l.sessionID != "braw" || l.prevSessionID != "" {
			t.Errorf("session/prev = %q/%q, want braw/\"\" (no swap)", l.sessionID, l.prevSessionID)
		}
	})
}

func TestOnCycleSession(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "id-a", Name: "aiken", RepoName: "bothy", Status: "running"},
		{ID: "id-b", Name: "bonnie", RepoName: "bothy", Status: "running"},
		{ID: "id-c", Name: "canny", RepoName: "bothy", Status: "running"},
	}
	list := protocol.SessionListMsg{Sessions: sessions}

	t.Run("forward moves to the next session and records prev", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", list)),
			okResp(payloadEnv("attached", protocol.SessionInfo{ID: "id-c"})),
		}}
		withLoopSeams(t, fake)

		l := newLoop("id-b", "")

		if _, err := l.onCycleSession(true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if l.sessionID != "id-c" || l.prevSessionID != "id-b" {
			t.Errorf("session/prev = %q/%q, want id-c/id-b", l.sessionID, l.prevSessionID)
		}
	})

	t.Run("backward wraps to the last session", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", list)),
			okResp(payloadEnv("attached", protocol.SessionInfo{ID: "id-c"})),
		}}
		withLoopSeams(t, fake)

		l := newLoop("id-a", "")

		if _, err := l.onCycleSession(false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if l.sessionID != "id-c" {
			t.Errorf("sessionID = %q, want id-c (wrapped backward)", l.sessionID)
		}
	})

	t.Run("list read error closes the connection and aborts", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{errResp(io.EOF)}}
		withLoopSeams(t, fake)

		l := newLoop("id-b", "")

		done, err := l.onCycleSession(true)
		if done || err == nil {
			t.Fatalf("onCycleSession = (%v,%v), want (false, error)", done, err)
		}

		if fake.closed != 1 {
			t.Errorf("connection closed %d times, want 1", fake.closed)
		}

		if l.sessionID != "id-b" {
			t.Errorf("sessionID changed to %q on error, want unchanged id-b", l.sessionID)
		}
	})
}

func TestOnRestart(t *testing.T) {
	t.Run("resume success reattaches", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{
			okResp(typeEnv("ok")),
			okResp(payloadEnv("attached", protocol.SessionInfo{ID: "braw"})),
		}}
		withLoopSeams(t, fake)

		l := newLoop("braw", "")

		if _, err := l.onRestart(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		want := []string{"resume", "attach"}
		if got := fake.sentTypes(); !equalStrings(got, want) {
			t.Errorf("sent = %v, want %v", got, want)
		}
	})

	t.Run("resume error is reported but still reattaches", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{
			okResp(errEnv("cannae resume")),
			okResp(payloadEnv("attached", protocol.SessionInfo{ID: "braw"})),
		}}

		withLoopSeams(t, fake)

		// Capture the user-facing "Resume failed" notice.
		out := captureStdout(t, func() {
			if _, err := l0Restart(t); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		if !strings.Contains(out, "cannae resume") {
			t.Errorf("expected the resume error surfaced to the user, got %q", out)
		}
	})
}

// l0Restart runs onRestart on a fresh loop; split out so captureStdout wraps
// only the call (captureStdout rebinds out, which must stay active during the
// handler's out.Printf).
func l0Restart(t *testing.T) (bool, error) {
	t.Helper()

	l := newLoop("braw", "")

	return l.onRestart()
}

func TestOnOrchestratorSession(t *testing.T) {
	orchRunning := protocol.SessionListMsg{Sessions: []protocol.SessionInfo{
		{ID: "orch", SystemKind: "orchestrator", Status: "running"},
		{ID: "braw", Status: "running"},
	}}
	orchStopped := protocol.SessionListMsg{Sessions: []protocol.SessionInfo{
		{ID: "orch", SystemKind: "orchestrator", Status: "stopped"},
		{ID: "braw", Status: "running"},
	}}
	noOrch := protocol.SessionListMsg{Sessions: []protocol.SessionInfo{{ID: "braw", Status: "running"}}}

	t.Run("not enabled keeps current session", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", noOrch)),
			okResp(payloadEnv("attached", protocol.SessionInfo{ID: "braw"})),
		}}
		restored := withLoopSeams(t, fake)

		l := newLoop("braw", "")

		if _, err := l.onOrchestratorSession(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if l.sessionID != "braw" {
			t.Errorf("sessionID = %q, want unchanged braw", l.sessionID)
		}

		if len(*restored) != 1 || (*restored)[0] != "braw" {
			t.Errorf("restoreScreen = %v, want [braw]", *restored)
		}
	})

	t.Run("running orchestrator switches to it", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", orchRunning)),
			okResp(payloadEnv("attached", protocol.SessionInfo{ID: "orch"})),
		}}
		restored := withLoopSeams(t, fake)

		l := newLoop("braw", "")

		if _, err := l.onOrchestratorSession(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if l.sessionID != "orch" || l.prevSessionID != "braw" {
			t.Errorf("session/prev = %q/%q, want orch/braw", l.sessionID, l.prevSessionID)
		}

		if len(*restored) != 1 || (*restored)[0] != "orch" {
			t.Errorf("restoreScreen = %v, want [orch] (switchTo repaints target)", *restored)
		}
	})

	t.Run("already on orchestrator with prev swaps back without repaint", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", orchRunning)),
			okResp(payloadEnv("attached", protocol.SessionInfo{ID: "braw"})),
		}}
		restored := withLoopSeams(t, fake)

		l := newLoop("orch", "braw")

		if _, err := l.onOrchestratorSession(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if l.sessionID != "braw" || l.prevSessionID != "orch" {
			t.Errorf("session/prev = %q/%q, want braw/orch (swapped)", l.sessionID, l.prevSessionID)
		}

		if len(*restored) != 0 {
			t.Errorf("swap-back must not repaint, got restoreScreen %v", *restored)
		}
	})

	t.Run("stopped orchestrator is resumed then switched to", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", orchStopped)),
			okResp(typeEnv("ok")), // resume
			okResp(payloadEnv("attached", protocol.SessionInfo{ID: "orch"})),
		}}
		withLoopSeams(t, fake)

		l := newLoop("braw", "")

		if _, err := l.onOrchestratorSession(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if l.sessionID != "orch" {
			t.Errorf("sessionID = %q, want orch", l.sessionID)
		}

		want := []string{"list", "resume", "attach"}
		if got := fake.sentTypes(); !equalStrings(got, want) {
			t.Errorf("sent = %v, want %v", got, want)
		}
	})

	t.Run("resume error keeps current session", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", orchStopped)),
			okResp(errEnv("resume fashed")),
			okResp(payloadEnv("attached", protocol.SessionInfo{ID: "braw"})),
		}}
		withLoopSeams(t, fake)

		l := newLoop("braw", "")

		if _, err := l.onOrchestratorSession(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if l.sessionID != "braw" {
			t.Errorf("sessionID = %q, want braw (resume failed, stay put)", l.sessionID)
		}
	})
}

func TestReattachAfterOverlayFailure(t *testing.T) {
	nc2 := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("attached", protocol.SessionInfo{ID: "braw"}))}}
	restored := withLoopSeams(t, nc2)

	// The connection the failed create was issued on; it must be closed.
	failed := &scriptedConn{}

	l := newLoop("braw", "")

	got, err := reattachAfterOverlayFailure(failed, "braw", "Create", errEnv("name taken"), &l.opts, &l.info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != nc2 {
		t.Error("expected the freshly dialled connection to be returned")
	}

	if failed.closed != 1 {
		t.Errorf("failed connection closed %d times, want 1", failed.closed)
	}

	if len(*restored) != 1 || (*restored)[0] != "braw" {
		t.Errorf("restoreScreen = %v, want [braw]", *restored)
	}

	if l.opts.SessionID != "braw" || l.opts.Info != &l.info {
		t.Errorf("opts not restored to braw/&info: %+v", l.opts)
	}
}
