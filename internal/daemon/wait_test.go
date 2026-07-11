package daemon

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
)

func TestScanForMatch(t *testing.T) {
	re := regexp.MustCompile("bonnie")

	// ANSI escape sequences and CRs must not defeat the match.
	data := []byte("first line\r\n\x1b[32mbonnie\x1b[0m the second\r\nthird\n")

	line, ok := scanForMatch(re, data)
	if !ok {
		t.Fatal("expected a match")
	}

	if line != "bonnie the second" {
		t.Fatalf("matched line = %q, want %q", line, "bonnie the second")
	}

	if _, ok := scanForMatch(re, []byte("nothing here\n")); ok {
		t.Fatal("did not expect a match")
	}
}

func TestMatchWriterPartialLine(t *testing.T) {
	ch := make(chan string, 1)
	mw := &matchWriter{re: regexp.MustCompile("ready>"), matchCh: ch}

	// Arrives across writes and without a trailing newline (like a prompt).
	_, _ = mw.Write([]byte("waiting\nrea"))

	select {
	case <-ch:
		t.Fatal("matched too early")
	default:
	}

	_, _ = mw.Write([]byte("dy> "))

	select {
	case got := <-ch:
		if got != "ready> " {
			t.Fatalf("matched %q, want %q", got, "ready> ")
		}
	default:
		t.Fatal("expected a match on the trailing partial line")
	}
}

func TestMatchWriterTrailingEmptyGuard(t *testing.T) {
	ch := make(chan string, 1)
	mw := &matchWriter{re: regexp.MustCompile("^$"), matchCh: ch}

	// A non-matching complete line leaves an empty trailing partial, which must
	// NOT satisfy ^$ — otherwise every newline-terminated write would match.
	_, _ = mw.Write([]byte("abc\n"))

	select {
	case got := <-ch:
		t.Fatalf("empty trailing partial should not match, got %q", got)
	default:
	}

	// A genuine blank line (its own newline) is a completed empty line and must
	// match.
	_, _ = mw.Write([]byte("\n"))

	select {
	case got := <-ch:
		if got != "" {
			t.Fatalf("matched %q, want empty blank line", got)
		}
	default:
		t.Fatal("expected a blank line to match ^$")
	}
}

func TestIsIdleAgentStatus(t *testing.T) {
	idle := []string{"ready", "unknown", ""}
	for _, s := range idle {
		if !isIdleAgentStatus(s) {
			t.Errorf("isIdleAgentStatus(%q) = false, want true", s)
		}
	}

	busy := []string{"active", "approval"}
	for _, s := range busy {
		if isIdleAgentStatus(s) {
			t.Errorf("isIdleAgentStatus(%q) = true, want false", s)
		}
	}
}

// addPTYSessionCmd registers a live PTY session running a custom command, so a
// test can drive real output through the session's attached writers.
func (h *testHarness) addPTYSessionCmd(t *testing.T, id, name, command string, args ...string) {
	t.Helper()

	logPath := filepath.Join(h.sm.paths.LogDir, id+".log")

	sess, err := grpty.NewSession(grpty.SessionOpts{
		ID:      id,
		Command: command,
		Args:    args,
		Dir:     t.TempDir(),
		Rows:    24,
		Cols:    80,
		LogPath: logPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = sess.Kill()
		<-sess.Done()
		sess.Close()
	})

	h.sm.mu.Lock()
	h.sm.state.Sessions[id] = &SessionState{
		ID:        id,
		Name:      name,
		Agent:     "claude",
		Status:    StatusRunning,
		CreatedAt: time.Now().UTC(),
	}
	h.sm.sessions[id] = sess
	h.sm.mu.Unlock()
}

// readWaitOutcome reads control messages until a terminal wait result, skipping
// the informational "wait_watching"/"wait_following" acknowledgements.
func (h *testHarness) readWaitOutcome(t *testing.T) protocol.Envelope {
	t.Helper()

	for {
		env := h.readControlMsg(t)
		switch env.Type {
		case "wait_watching", "wait_following":
			continue
		default:
			return env
		}
	}
}

func TestWaitContainsScrollback(t *testing.T) {
	h := newTestHarness(t)
	h.addStoppedSession(t, "bide-wait", "bide-still", 0, "warming up\nbonnie-ready now\ndone\n")

	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "bide-wait",
		Mode:      "contains",
		Pattern:   "bonnie-ready",
	})

	env := h.readWaitOutcome(t)
	if env.Type != "wait_matched" {
		t.Fatalf("expected wait_matched, got %q", env.Type)
	}

	var m protocol.WaitMatchedMsg
	if err := protocol.DecodePayload(env, &m); err != nil {
		t.Fatal(err)
	}

	if m.MatchedLine != "bonnie-ready now" {
		t.Fatalf("matched line = %q, want %q", m.MatchedLine, "bonnie-ready now")
	}
}

func TestWaitContainsLive(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSessionCmd(t, "braw-wait", "bonnie-live", "sh", "-c", "sleep 0.2; echo bonnie-match; sleep 300")

	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "braw-wait",
		Mode:      "contains",
		Pattern:   "bonnie-match",
		TimeoutMs: 5000,
	})

	env := h.readWaitOutcome(t)
	if env.Type != "wait_matched" {
		t.Fatalf("expected wait_matched, got %q", env.Type)
	}
}

func TestWaitContainsTimeout(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "dreich-wait", "dreich-quiet")

	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "dreich-wait",
		Mode:      "contains",
		Pattern:   "never-appears",
		TimeoutMs: 200,
	})

	env := h.readWaitOutcome(t)
	if env.Type != "wait_timeout" {
		t.Fatalf("expected wait_timeout, got %q", env.Type)
	}
}

func TestWaitContainsBadPattern(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "fash-wait", "fash-bad")

	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "fash-wait",
		Mode:      "contains",
		Pattern:   "[unterminated",
	})

	env := h.readWaitOutcome(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestWaitStatusImmediate(t *testing.T) {
	h := newTestHarness(t)
	h.addStoppedSession(t, "ken-wait", "ken-stopped", 0, "")

	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "ken-wait",
		Mode:      "status",
		Status:    "stopped",
	})

	env := h.readWaitOutcome(t)
	if env.Type != "wait_matched" {
		t.Fatalf("expected wait_matched, got %q", env.Type)
	}

	var m protocol.WaitMatchedMsg
	if err := protocol.DecodePayload(env, &m); err != nil {
		t.Fatal(err)
	}

	if m.Status != "stopped" {
		t.Fatalf("status = %q, want stopped", m.Status)
	}
}

func TestWaitStatusTransition(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "speir-wait", "speir-run")

	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "speir-wait",
		Mode:      "status",
		Status:    "stopped",
	})

	// The waiter acknowledges it is armed before we flip the status.
	if env := h.readControlMsg(t); env.Type != "wait_watching" {
		t.Fatalf("expected wait_watching, got %q", env.Type)
	}

	h.sm.mu.Lock()
	h.sm.state.Sessions["speir-wait"].Status = StatusStopped
	h.sm.mu.Unlock()

	// Publishing to the status topic wakes the waiter to re-check.
	if _, err := h.sm.messages.Publish(PublishOpts{Stream: "_system.status", SenderID: "speir-wait", SenderName: "speir-run", Body: "stopped"}); err != nil {
		t.Fatal(err)
	}

	env := h.readWaitOutcome(t)
	if env.Type != "wait_matched" {
		t.Fatalf("expected wait_matched, got %q", env.Type)
	}
}

func TestWaitStatusTimeout(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "thrawn-wait", "thrawn-run")

	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "thrawn-wait",
		Mode:      "status",
		Status:    "stopped",
		TimeoutMs: 200,
	})

	env := h.readWaitOutcome(t)
	if env.Type != "wait_timeout" {
		t.Fatalf("expected wait_timeout, got %q", env.Type)
	}
}

func TestWaitIdleImmediate(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "canny-wait", "canny-idle")

	h.sm.mu.Lock()
	h.sm.state.Sessions["canny-wait"].AgentStatus = "ready"
	h.sm.mu.Unlock()

	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "canny-wait",
		Mode:      "idle",
	})

	env := h.readWaitOutcome(t)
	if env.Type != "wait_matched" {
		t.Fatalf("expected wait_matched, got %q", env.Type)
	}
}

func TestWaitIdleTransition(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "whin-wait", "whin-active")

	h.sm.mu.Lock()
	h.sm.state.Sessions["whin-wait"].AgentStatus = "active"
	h.sm.mu.Unlock()

	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "whin-wait",
		Mode:      "idle",
	})

	if env := h.readControlMsg(t); env.Type != "wait_watching" {
		t.Fatalf("expected wait_watching, got %q", env.Type)
	}

	h.sm.mu.Lock()
	h.sm.state.Sessions["whin-wait"].AgentStatus = "ready"
	h.sm.mu.Unlock()

	if _, err := h.sm.messages.Publish(PublishOpts{Stream: "_system.status", SenderID: "whin-wait", SenderName: "whin-active", Body: "ready"}); err != nil {
		t.Fatal(err)
	}

	env := h.readWaitOutcome(t)
	if env.Type != "wait_matched" {
		t.Fatalf("expected wait_matched, got %q", env.Type)
	}
}

func TestWaitStatusSessionDeleted(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "fash-gone", "fash-doomed")

	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "fash-gone",
		Mode:      "status",
		Status:    "stopped",
	})

	if env := h.readControlMsg(t); env.Type != "wait_watching" {
		t.Fatalf("expected wait_watching, got %q", env.Type)
	}

	// Deleting the session out from under the waiter must fail the wait rather
	// than block forever (there is no timeout on this request).
	h.sm.mu.Lock()
	delete(h.sm.state.Sessions, "fash-gone")
	h.sm.mu.Unlock()

	if _, err := h.sm.messages.Publish(PublishOpts{Stream: "_system.status", SenderID: "fash-gone", SenderName: "fash-doomed", Body: "gone"}); err != nil {
		t.Fatal(err)
	}

	env := h.readWaitOutcome(t)
	if env.Type != "error" {
		t.Fatalf("expected error after deletion, got %q", env.Type)
	}
}

func TestWaitContainsBlankLine(t *testing.T) {
	h := newTestHarness(t)
	h.addStoppedSession(t, "skelf-wait", "skelf-blank", 0, "first\n\nthird\n")

	// A `^$` pattern should match the interior blank line.
	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "skelf-wait",
		Mode:      "contains",
		Pattern:   "^$",
	})

	env := h.readWaitOutcome(t)
	if env.Type != "wait_matched" {
		t.Fatalf("expected wait_matched, got %q", env.Type)
	}
}

func TestWaitInvalidMode(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "haar-wait", "haar-unclear")

	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "haar-wait",
		Mode:      "scunner",
	})

	env := h.readWaitOutcome(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestWaitSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "wait", protocol.WaitMsg{
		SessionID: "haar-missing",
		Mode:      "status",
		Status:    "stopped",
	})

	env := h.readWaitOutcome(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestWaitInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "wait", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readWaitOutcome(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}
