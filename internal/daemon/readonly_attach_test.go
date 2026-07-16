package daemon

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
)

// addEchoPTYSession registers a running PTY session whose command echoes stdin
// back to stdout (`cat`), so a test can observe whether client input actually
// reached the PTY: forwarded input comes back as a data frame, dropped input
// produces nothing.
func (h *testHarness) addEchoPTYSession(t *testing.T, id, name string) {
	t.Helper()

	logPath := filepath.Join(h.sm.paths.LogDir, id+".log")

	sess, err := grpty.NewSession(grpty.SessionOpts{
		ID:      id,
		Command: "cat",
		Dir:     t.TempDir(),
		Rows:    24,
		Cols:    80,
		LogPath: logPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		h.cancel()
		_ = h.conn.Close()
		_ = h.serverConn.Close()
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

// readDataFrameTimeout waits for a single data-channel frame, returning ok=false
// if none arrives within timeout. It uses a read deadline on the pipe (rather
// than a detached goroutine) so no reader is left racing a later read on the
// shared FrameReader. Control frames are skipped.
func (h *testHarness) readDataFrameTimeout(t *testing.T, timeout time.Duration) ([]byte, bool) {
	t.Helper()

	_ = h.conn.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = h.conn.SetReadDeadline(time.Time{}) }()

	for {
		frame, err := h.reader.ReadFrame()
		if err != nil {
			return nil, false
		}

		if frame.Channel == protocol.ChannelData {
			return append([]byte(nil), frame.Payload...), true
		}
	}
}

// TestReadOnlyAttachDropsInput is the daemon-side backstop for issue #31: a
// read-only attach must never inject client input into the PTY, even if a
// misbehaving client sends data frames.
func TestReadOnlyAttachDropsInput(t *testing.T) {
	h := newTestHarness(t)
	h.addEchoPTYSession(t, "dreich-ro", "dreich-observer")

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "dreich-ro", ReadOnly: true})
	h.readControlMsg(t) // attached

	if err := h.writer.WriteFrame(protocol.ChannelData, []byte("scunner\n")); err != nil {
		t.Fatal(err)
	}

	if data, ok := h.readDataFrameTimeout(t, 500*time.Millisecond); ok {
		t.Fatalf("read-only attach forwarded input to the PTY (got echo %q)", data)
	}

	// The handler must still be alive after dropping the input.
	h.sendControl(t, "list", struct{}{})
	h.expectType(t, "session_list")
}

// TestReadWriteAttachForwardsInput is the positive control: a normal (non
// read-only) attach forwards input, which the echoing PTY sends back. This
// proves the drop test above is meaningful rather than merely observing an
// always-silent PTY.
func TestReadWriteAttachForwardsInput(t *testing.T) {
	h := newTestHarness(t)
	h.addEchoPTYSession(t, "braw-rw", "braw-driver")

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "braw-rw"})
	h.readControlMsg(t) // attached

	if err := h.writer.WriteFrame(protocol.ChannelData, []byte("bonnie\n")); err != nil {
		t.Fatal(err)
	}

	data, ok := h.readDataFrameTimeout(t, 2*time.Second)
	if !ok {
		t.Fatal("read-write attach did not forward input to the PTY (no echo)")
	}

	if !bytes.Contains(data, []byte("bonnie")) {
		// The echo may arrive in chunks; drain a little more before failing.
		if more, ok := h.readDataFrameTimeout(t, time.Second); ok {
			data = append(data, more...)
		}
	}

	if !bytes.Contains(data, []byte("bonnie")) {
		t.Fatalf("expected echoed input, got %q", data)
	}
}
