package cli

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// withLoopSeams points freshClient at the given fake, restores it on cleanup,
// and discards output.
func withLoopSeams(t *testing.T, fake *scriptedConn) {
	t.Helper()

	withDiscardOutput(t)

	origFresh := freshClient
	freshClient = func() (attachConn, error) { return fake, nil }

	t.Cleanup(func() {
		freshClient = origFresh
	})
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
	withLoopSeams(t, nil)

	l := newLoop("braw", "")
	nc := &scriptedConn{responses: []scriptedResp{
		okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw", Name: "bonnie"})),
	}}

	if err := l.adoptCurrent(nc); err != nil {
		t.Fatalf("adoptCurrent: %v", err)
	}

	if l.c != nc {
		t.Error("adoptCurrent did not install nc as the live connection")
	}

	if l.opts.SessionID != "braw" || l.info.Name != "bonnie" {
		t.Errorf("opts.SessionID=%q info.Name=%q, want braw/bonnie", l.opts.SessionID, l.info.Name)
	}

	assertAliased(t, l)

	if got := nc.sentTypes(); len(got) != 1 || got[0] != "attach" {
		t.Errorf("sent = %v, want [attach]", got)
	}
}

func TestAdoptCurrentInstallsTerminalOwnedSeed(t *testing.T) {
	withLoopSeams(t, nil)

	l := newLoop("braw", "")

	nc := &scriptedConn{responses: []scriptedResp{
		okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw", Name: "bonnie"})),
	}}

	if err := l.adoptCurrent(nc); err != nil {
		t.Fatalf("adoptCurrent: %v", err)
	}

	if l.opts.TerminalOwnedSeed == nil {
		t.Fatal("adoptCurrent did not install a terminal-owned seed")
	}

	if len(nc.sends) != 1 {
		t.Fatalf("sent %d controls, want one attach", len(nc.sends))
	}

	msg, ok := nc.sends[0].Payload.(protocol.AttachMsg)
	if !ok || !msg.TerminalOwned {
		t.Fatalf("attach payload = %#v, want terminal-owned request", nc.sends[0].Payload)
	}
}

func TestAdoptCurrentRejectsRawAttachResponse(t *testing.T) {
	withLoopSeams(t, nil)

	l := newLoop("braw", "")
	old := &scriptedConn{}
	l.c = old

	nc := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("attached", protocol.SessionInfo{ID: "braw"})),
	}}

	err := l.adoptCurrent(nc)
	if err == nil || !strings.Contains(err.Error(), "raw attached response") {
		t.Fatalf("adoptCurrent() error = %v, want raw attached rejection", err)
	}

	if l.c != old {
		t.Fatal("adoptCurrent installed the raw attach connection after seed failure")
	}

	if nc.closed != 1 {
		t.Fatalf("raw attach connection closed %d times, want 1", nc.closed)
	}

	if l.opts.TerminalOwnedSeed != nil {
		t.Fatal("terminal-owned seed was populated after raw attach rejection")
	}
}

func TestSwitchTo(t *testing.T) {
	withLoopSeams(t, nil)

	l := newLoop("auld", "older")
	nc := &scriptedConn{responses: []scriptedResp{okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "new", Name: "bonnie"}))}}

	if err := l.switchTo(nc, "new"); err != nil {
		t.Fatalf("switchTo: %v", err)
	}

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
}

func TestSwitchToTerminalOwnedSeedSkipsRestore(t *testing.T) {
	withLoopSeams(t, nil)

	l := newLoop("auld", "older")
	seed := protocol.TerminalOwnedAttachSeedMsg{
		Session:  protocol.SessionInfo{ID: "new", Name: "bonnie"},
		Snapshot: protocol.ScreenSnapshotResponseMsg{SessionID: "new", Frame: "braw frame"},
	}
	nc := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("terminal_owned_attached", seed)),
	}}

	if err := l.switchTo(nc, "new"); err != nil {
		t.Fatalf("switchTo: %v", err)
	}

	if l.sessionID != "new" || l.prevSessionID != "auld" {
		t.Errorf("session/prev = %q/%q, want new/auld", l.sessionID, l.prevSessionID)
	}

	if l.opts.TerminalOwnedSeed == nil {
		t.Fatal("terminal-owned attach did not install a seed")
	}
}

func TestSwitchToAttachErrorKeepsCurrentSession(t *testing.T) {
	withLoopSeams(t, nil)

	l := newLoop("auld", "")
	nc := &scriptedConn{responses: []scriptedResp{
		okResp(errEnv("nae such session")),
		okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "auld", Name: "still-here"})),
	}}

	if err := l.switchTo(nc, "new"); err != nil {
		t.Fatalf("switchTo: %v", err)
	}

	if l.sessionID != "auld" {
		t.Errorf("sessionID = %q, want unchanged auld", l.sessionID)
	}

	if l.opts.SessionID != "auld" || l.info.Name != "still-here" {
		t.Errorf("opts.SessionID=%q info.Name=%q, want auld/still-here", l.opts.SessionID, l.info.Name)
	}

	if got := nc.sentTypes(); !reflect.DeepEqual(got, []string{"attach", "attach"}) {
		t.Errorf("sent = %v, want target attach then current attach", got)
	}
}

// TestOnScrollModeUsesDaemonDefaultOnEveryReconnect is the CLI-side regression
// for issue #1320. When terminal-owned attach yields no history rows, scroll
// mode must send the zero sentinel on every raw-log fallback so a long-lived
// attach process does not pin either the historical 2,000-line literal or a
// client-side snapshot of [limits].log_lines across daemon reloads.
func TestOnScrollModeUsesDaemonDefaultOnEveryReconnect(t *testing.T) {
	fake := &scriptedConn{responses: []scriptedResp{
		okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"})),
		okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"})),
		okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"})),
		okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"})),
	}}
	withLoopSeams(t, fake)

	origCfg := cfg
	origFetch := fetchScrollback
	origView := runScrollView

	t.Cleanup(func() {
		cfg = origCfg
		fetchScrollback = origFetch
		runScrollView = origView
	})

	cfg = config.Default()
	requested := make([]int, 0, 2)
	effective := make([]int, 0, 2)
	fetchScrollback = func(c *config.Config, _ config.Paths, _ string, sessionID string, lines int) string {
		if sessionID != "braw" {
			t.Errorf("sessionID = %q, want braw", sessionID)
		}

		requested = append(requested, lines)
		effective = append(effective, c.Limits.LogLinesOrDefault())

		return "canny history"
	}
	runScrollView = func(_ string, _ string, _ client.ScrollKeys) {}

	l := newLoop("braw", "")

	for _, logLines := range []int{17, 29} {
		cfg.Limits.LogLines = logLines

		if done, err := l.onScrollMode(); done || err != nil {
			t.Fatalf("onScrollMode() = (%v, %v), want (false, nil)", done, err)
		}
	}

	if want := []int{0, 0}; !reflect.DeepEqual(requested, want) {
		t.Errorf("requested lines = %v, want daemon-default sentinels %v", requested, want)
	}

	if want := []int{17, 29}; !reflect.DeepEqual(effective, want) {
		t.Errorf("test config limits = %v, want non-default reload sequence %v", effective, want)
	}
}

func TestOnScrollModePrefersTerminalHistory(t *testing.T) {
	fake := &scriptedConn{responses: []scriptedResp{
		okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"})),
	}}
	withLoopSeams(t, fake)

	origCfg := cfg
	origFetch := fetchScrollback
	origView := runScrollView

	t.Cleanup(func() {
		cfg = origCfg
		fetchScrollback = origFetch
		runScrollView = origView
	})

	cfg = config.Default()
	fetchScrollback = func(*config.Config, config.Paths, string, string, int) string {
		t.Fatal("raw scrollback should not be fetched when terminal history is available")

		return ""
	}

	var viewed string

	runScrollView = func(_ string, content string, _ client.ScrollKeys) {
		viewed = content
	}

	l := newLoop("braw", "bothy")
	l.hasTerminalHistory = true
	l.terminalHistory = protocol.TerminalHistoryMsg{
		Lines: []protocol.TerminalHistoryLineMsg{
			{Frame: "braw", Wrapped: true},
			{Frame: "canny"},
		},
	}

	if done, err := l.onScrollMode(); done || err != nil {
		t.Fatalf("onScrollMode() = (%v, %v), want (false, nil)", done, err)
	}

	if viewed != "brawcanny" {
		t.Fatalf("scroll view content = %q, want formatted terminal history", viewed)
	}
}

func TestOnScrollModeRefreshesTerminalHistoryBeforeRawLogs(t *testing.T) {
	fake := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("terminal_owned_attached", protocol.TerminalOwnedAttachSeedMsg{
			Session: protocol.SessionInfo{ID: "braw", Name: "braw"},
			Snapshot: protocol.ScreenSnapshotResponseMsg{
				SessionID: "braw",
				Frame:     "visible screen\r\ncurrent prompt\r\n   \x1b[0m",
			},
			History: protocol.TerminalHistoryMsg{
				Lines: []protocol.TerminalHistoryLineMsg{
					{Frame: "scrolled off 1"},
					{Frame: "scrolled off 2"},
				},
			},
		})),
		okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"})),
	}}
	withLoopSeams(t, fake)

	origCfg := cfg
	origFetch := fetchScrollback
	origView := runScrollView

	t.Cleanup(func() {
		cfg = origCfg
		fetchScrollback = origFetch
		runScrollView = origView
	})

	cfg = config.Default()
	fetchScrollback = func(*config.Config, config.Paths, string, string, int) string {
		t.Fatal("raw scrollback should not be fetched when fresh terminal history is available")

		return ""
	}

	var viewed string

	runScrollView = func(_ string, content string, _ client.ScrollKeys) {
		viewed = content
	}

	l := newLoop("braw", "")

	if done, err := l.onScrollMode(); done || err != nil {
		t.Fatalf("onScrollMode() = (%v, %v), want (false, nil)", done, err)
	}

	want := "scrolled off 1\nscrolled off 2\nvisible screen\ncurrent prompt\x1b[0m"
	if viewed != want {
		t.Fatalf("scroll view content = %q, want fresh terminal history", viewed)
	}

	if got := fake.sentTypes(); !reflect.DeepEqual(got, []string{"attach", "attach"}) {
		t.Fatalf("sent = %v, want fresh-history attach then reattach", got)
	}

	if fake.closed != 1 {
		t.Fatalf("fresh-history attach closed %d times, want 1", fake.closed)
	}
}

func TestOnScrollModeFallsBackToRawLogsWhenFreshTerminalHistoryFails(t *testing.T) {
	fake := &scriptedConn{responses: []scriptedResp{
		okResp(errEnv("dreich terminal history")),
		okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"})),
	}}
	withLoopSeams(t, fake)

	origCfg := cfg
	origFetch := fetchScrollback
	origView := runScrollView

	t.Cleanup(func() {
		cfg = origCfg
		fetchScrollback = origFetch
		runScrollView = origView
	})

	cfg = config.Default()

	var requested []int

	fetchScrollback = func(_ *config.Config, _ config.Paths, _ string, sessionID string, lines int) string {
		if sessionID != "braw" {
			t.Errorf("sessionID = %q, want braw", sessionID)
		}

		requested = append(requested, lines)

		return "raw dreich logs"
	}

	var viewed string

	runScrollView = func(_ string, content string, _ client.ScrollKeys) {
		viewed = content
	}

	l := newLoop("braw", "")

	if done, err := l.onScrollMode(); done || err != nil {
		t.Fatalf("onScrollMode() = (%v, %v), want (false, nil)", done, err)
	}

	if viewed != "raw dreich logs" {
		t.Fatalf("scroll view content = %q, want raw log fallback", viewed)
	}

	if want := []int{0}; !reflect.DeepEqual(requested, want) {
		t.Fatalf("requested lines = %v, want daemon-default sentinel %v", requested, want)
	}

	if got := fake.sentTypes(); !reflect.DeepEqual(got, []string{"attach", "attach"}) {
		t.Fatalf("sent = %v, want failed fresh-history attach then reattach", got)
	}

	if fake.closed != 1 {
		t.Fatalf("fresh-history attach closed %d times, want 1", fake.closed)
	}
}

// --- handlers reachable via the freshClient seam ---------------------------

func TestOnLastSession(t *testing.T) {
	t.Run("swaps when a previous session exists", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "auld"}))}}
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
		fake := &scriptedConn{responses: []scriptedResp{okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"}))}}
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
			okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "id-c"})),
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
			okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "id-c"})),
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
			okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"})),
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
			okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"})),
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

	runOrchestrator := func(t *testing.T, current, prev string, responses []scriptedResp) *attachLoop {
		t.Helper()

		fake := &scriptedConn{responses: responses}
		withLoopSeams(t, fake)

		l := newLoop(current, prev)

		if _, err := l.onOrchestratorSession(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		return l
	}

	t.Run("not enabled keeps current session", func(t *testing.T) {
		l := runOrchestrator(t, "braw", "", []scriptedResp{
			okResp(payloadEnv("session_list", noOrch)),
			okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"})),
		})

		if l.sessionID != "braw" {
			t.Errorf("sessionID = %q, want unchanged braw", l.sessionID)
		}
	})

	t.Run("running orchestrator switches to it", func(t *testing.T) {
		l := runOrchestrator(t, "braw", "", []scriptedResp{
			okResp(payloadEnv("session_list", orchRunning)),
			okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "orch"})),
		})

		if l.sessionID != "orch" || l.prevSessionID != "braw" {
			t.Errorf("session/prev = %q/%q, want orch/braw", l.sessionID, l.prevSessionID)
		}
	})

	t.Run("already on orchestrator with prev swaps back without repaint", func(t *testing.T) {
		l := runOrchestrator(t, "orch", "braw", []scriptedResp{
			okResp(payloadEnv("session_list", orchRunning)),
			okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"})),
		})

		if l.sessionID != "braw" || l.prevSessionID != "orch" {
			t.Errorf("session/prev = %q/%q, want braw/orch (swapped)", l.sessionID, l.prevSessionID)
		}
	})

	t.Run("stopped orchestrator is resumed then switched to", func(t *testing.T) {
		fake := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", orchStopped)),
			okResp(typeEnv("ok")), // resume
			okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "orch"})),
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
			okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"})),
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
	nc2 := &scriptedConn{responses: []scriptedResp{okResp(terminalOwnedEnv(protocol.SessionInfo{ID: "braw"}))}}
	withLoopSeams(t, nc2)

	// The connection the failed create was issued on; it must be closed.
	failed := &scriptedConn{}

	l := newLoop("braw", "")

	got, seed, err := reattachAfterOverlayFailure(failed, "braw", "Create", errEnv("name taken"), &l.opts, &l.info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if seed == nil {
		t.Fatal("seed = nil, want terminal-owned seed")
	}

	if got != nc2 {
		t.Error("expected the freshly dialled connection to be returned")
	}

	if failed.closed != 1 {
		t.Errorf("failed connection closed %d times, want 1", failed.closed)
	}

	if l.opts.SessionID != "braw" || l.opts.Info != &l.info {
		t.Errorf("opts not restored to braw/&info: %+v", l.opts)
	}
}
