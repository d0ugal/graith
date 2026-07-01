package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/version"
)

// testHarness wraps a client connection to HandleConnection for testing.
type testHarness struct {
	sm         *SessionManager
	conn       net.Conn
	serverConn net.Conn
	reader     *protocol.FrameReader
	writer     *protocol.FrameWriter
	cancel     context.CancelFunc
	done       chan struct{}
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := config.Default()
	paths := config.Paths{
		StateFile:  filepath.Join(tmpDir, "state.json"),
		LogDir:     filepath.Join(tmpDir, "logs"),
		DataDir:    filepath.Join(tmpDir, "data"),
		MessagesDB: filepath.Join(tmpDir, "messages.db"),
	}
	_ = os.MkdirAll(paths.LogDir, 0o700)
	_ = os.MkdirAll(paths.DataDir, 0o700)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sm := NewSessionManager(cfg, paths, log)
	sm.upgradeCh = make(chan string, 1)

	msgStore, err := NewMsgStore(paths.MessagesDB)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = msgStore.Close() })

	sm.messages = msgStore

	clientConn, serverConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())

	h := &testHarness{
		sm:         sm,
		conn:       clientConn,
		serverConn: serverConn,
		reader:     protocol.NewFrameReader(clientConn),
		writer:     protocol.NewFrameWriter(clientConn),
		cancel:     cancel,
		done:       make(chan struct{}),
	}

	go func() {
		defer close(h.done)

		HandleConnection(ctx, serverConn, sm, log)
	}()

	t.Cleanup(func() {
		cancel()

		_ = clientConn.Close()
		_ = serverConn.Close()

		<-h.done
	})

	return h
}

func (h *testHarness) sendControl(t *testing.T, msgType string, payload any) {
	t.Helper()

	data, err := protocol.EncodeControl(msgType, payload)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.writer.WriteFrame(protocol.ChannelControl, data); err != nil {
		t.Fatal(err)
	}
}

func (h *testHarness) readControlMsg(t *testing.T) protocol.Envelope {
	t.Helper()

	for {
		frame, err := h.reader.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}

		if frame.Channel == protocol.ChannelControl {
			env, err := protocol.DecodeControl(frame.Payload)
			if err != nil {
				t.Fatal(err)
			}

			return env
		}
	}
}

func (h *testHarness) readControlMsgTimeout(t *testing.T, timeout time.Duration) (protocol.Envelope, bool) {
	t.Helper()

	type result struct {
		env protocol.Envelope
		err error
	}

	ch := make(chan result, 1)

	go func() {
		for {
			frame, err := h.reader.ReadFrame()
			if err != nil {
				ch <- result{err: err}
				return
			}

			if frame.Channel == protocol.ChannelControl {
				env, err := protocol.DecodeControl(frame.Payload)
				ch <- result{env: env, err: err}

				return
			}
		}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatal(r.err)
		}

		return r.env, true
	case <-time.After(timeout):
		return protocol.Envelope{}, false
	}
}

// addFakeSession adds a session to state and creates a PTY running `sleep`.
func (h *testHarness) addPTYSession(t *testing.T, id, name string) {
	t.Helper()

	logPath := filepath.Join(h.sm.paths.LogDir, id+".log")

	sess, err := grpty.NewSession(grpty.SessionOpts{
		ID:      id,
		Command: "sleep",
		Args:    []string{"300"},
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

func TestHandshake(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "handshake", protocol.HandshakeMsg{
		Version:      "1.0",
		ClientID:     "test-client",
		TerminalSize: [2]uint16{120, 40},
		Cwd:          "/tmp",
	})

	env := h.readControlMsg(t)
	if env.Type != "handshake_ok" {
		t.Fatalf("expected handshake_ok, got %q", env.Type)
	}

	var ok protocol.HandshakeOkMsg

	_ = protocol.DecodePayload(env, &ok)

	if ok.Version != protocol.Version {
		t.Errorf("version = %q, want %q", ok.Version, protocol.Version)
	}
}

func TestHandshakeVersionMismatch(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "handshake", protocol.HandshakeMsg{
		Version:      "999.0",
		ClientID:     "test-client",
		TerminalSize: [2]uint16{80, 24},
		Cwd:          "/tmp",
	})

	env := h.readControlMsg(t)
	if env.Type != "handshake_err" {
		t.Fatalf("expected handshake_err, got %q", env.Type)
	}

	var errMsg protocol.HandshakeErrMsg
	if err := protocol.DecodePayload(env, &errMsg); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(errMsg.Reason, "protocol version mismatch") {
		t.Errorf("reason = %q, want it to mention protocol version mismatch", errMsg.Reason)
	}

	if !strings.Contains(errMsg.Reason, "999.0") || !strings.Contains(errMsg.Reason, protocol.Version) {
		t.Errorf("reason = %q, want it to mention both versions", errMsg.Reason)
	}

	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after version mismatch")
	}
}

func TestHandshakeCompatibleMinorVersion(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "handshake", protocol.HandshakeMsg{
		Version:      "1.99",
		ClientID:     "test-client",
		TerminalSize: [2]uint16{80, 24},
		Cwd:          "/tmp",
	})

	env := h.readControlMsg(t)
	if env.Type != "handshake_ok" {
		t.Fatalf("expected handshake_ok for compatible minor version, got %q", env.Type)
	}
}

func TestHandshakeInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "handshake", Payload: json.RawMessage(`{"bad":`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestMalformedControlMessage(t *testing.T) {
	h := newTestHarness(t)

	_ = h.writer.WriteFrame(protocol.ChannelControl, []byte(`{not valid json`))

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var errMsg protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &errMsg)

	if errMsg.Message != "malformed message" {
		t.Errorf("error message = %q", errMsg.Message)
	}
}

func TestListSessions(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", Name: "bonnie-lass", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.state.Sessions["canny1"] = &SessionState{
		ID: "canny1", Name: "canny-lad", Status: StatusStopped,
		Agent: "codex", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "list", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "session_list" {
		t.Fatalf("expected session_list, got %q", env.Type)
	}

	var list protocol.SessionListMsg

	_ = protocol.DecodePayload(env, &list)

	if len(list.Sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(list.Sessions))
	}
}

func TestListSessionsEmpty(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "list", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "session_list" {
		t.Fatalf("expected session_list, got %q", env.Type)
	}

	var list protocol.SessionListMsg

	_ = protocol.DecodePayload(env, &list)

	if len(list.Sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(list.Sessions))
	}
}

func TestDeleteSession(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "fash1", "fash-away")

	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: "fash1"})

	env := h.readControlMsg(t)
	if env.Type != "deleted" {
		t.Fatalf("expected deleted, got %q", env.Type)
	}
}

func TestDeleteSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: "haar-mist"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestDeleteInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "delete", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestStopSession(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "bide1", "bide-still")

	h.sendControl(t, "stop", protocol.StopMsg{SessionID: "bide1"})

	env := h.readControlMsg(t)
	if env.Type != "stopped" {
		t.Fatalf("expected stopped, got %q", env.Type)
	}
}

func TestStopSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "stop", protocol.StopMsg{SessionID: "haar-mist"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestStopInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "stop", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestRenameSession(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["auld1"] = &SessionState{
		ID: "auld1", Name: "auld-kirk", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "rename", protocol.RenameMsg{SessionID: "auld1", NewName: "bonnie-kirk"})

	env := h.readControlMsg(t)
	if env.Type != "renamed" {
		t.Fatalf("expected renamed, got %q", env.Type)
	}

	h.sm.mu.RLock()
	name := h.sm.state.Sessions["auld1"].Name
	h.sm.mu.RUnlock()

	if name != "bonnie-kirk" {
		t.Errorf("name = %q, want %q", name, "bonnie-kirk")
	}
}

func TestRenameSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "rename", protocol.RenameMsg{SessionID: "haar", NewName: "x"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestRenameInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "rename", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestResumeInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "resume", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestResumeSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "resume", protocol.ResumeMsg{SessionID: "haar"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCreateInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "create", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestResizeWhileAttached(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-rsz", "braw-resize")

	// Handshake + attach
	h.sendControl(t, "handshake", protocol.HandshakeMsg{
		Version: "1.0", ClientID: "c1",
		TerminalSize: [2]uint16{80, 24}, Cwd: "/tmp",
	})
	h.readControlMsg(t) // handshake_ok

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "braw-rsz"})

	env := h.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached, got %q", env.Type)
	}

	h.sendControl(t, "resize", protocol.ResizeMsg{Cols: 200, Rows: 50})

	// Resize doesn't send a response, so we verify by sending another command
	// and confirming the handler is still alive.
	h.sendControl(t, "list", struct{}{})

	env = h.readControlMsg(t)
	if env.Type != "session_list" {
		t.Fatalf("expected session_list, got %q", env.Type)
	}
}

func TestResizeWithoutAttach(t *testing.T) {
	h := newTestHarness(t)

	// Resize with nothing attached should be silently ignored
	h.sendControl(t, "resize", protocol.ResizeMsg{Cols: 120, Rows: 40})

	// Confirm handler is still alive
	h.sendControl(t, "list", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "session_list" {
		t.Fatalf("expected session_list after no-op resize, got %q", env.Type)
	}
}

func TestAttachAndDetach(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-att", "bonnie-attach")

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "braw-att"})

	env := h.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached, got %q", env.Type)
	}

	h.sendControl(t, "detach", struct{}{})

	env = h.readControlMsg(t)
	if env.Type != "detached" {
		t.Fatalf("expected detached, got %q", env.Type)
	}

	var detached protocol.DetachedMsg

	_ = protocol.DecodePayload(env, &detached)

	if detached.Reason != "user" {
		t.Errorf("reason = %q, want %q", detached.Reason, "user")
	}
}

func TestAttachNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "haar"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestAttachInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "attach", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestDetachWithoutAttach(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "detach", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "detached" {
		t.Fatalf("expected detached, got %q", env.Type)
	}
}

func TestDataChannelForwarding(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-data", "bonnie-data")

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "braw-data"})
	h.readControlMsg(t) // attached

	// Send data on the data channel — should be forwarded to PTY input.
	// Won't error even if the PTY ignores it (sleep doesn't read stdin).
	if err := h.writer.WriteFrame(protocol.ChannelData, []byte("hello")); err != nil {
		t.Fatal(err)
	}

	// Confirm handler is still processing
	h.sendControl(t, "list", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "session_list" {
		t.Fatalf("expected session_list, got %q", env.Type)
	}
}

func TestDataChannelWithoutAttach(t *testing.T) {
	h := newTestHarness(t)

	// Data with nothing attached should be silently ignored
	if err := h.writer.WriteFrame(protocol.ChannelData, []byte("ignored")); err != nil {
		t.Fatal(err)
	}

	h.sendControl(t, "list", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "session_list" {
		t.Fatalf("expected session_list, got %q", env.Type)
	}
}

func TestLogsNonFollow(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-log", "bonnie-logs")

	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "braw-log", Lines: 100})

	env := h.readControlMsg(t)
	if env.Type != "logs_done" {
		t.Fatalf("expected logs_done, got %q", env.Type)
	}
}

func TestLogsNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "haar"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestLogsInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "logs", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestLogsDefaultLines(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "neep-log", "neep-default")

	// Lines=0 should default to 300 internally
	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "neep-log", Lines: 0})

	env := h.readControlMsg(t)
	if env.Type != "logs_done" {
		t.Fatalf("expected logs_done, got %q", env.Type)
	}
}

func TestLogsFollowThenDetach(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-logf", "bonnie-follow")

	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "braw-logf", Follow: true})

	env := h.readControlMsg(t)
	if env.Type != "logs_following" {
		t.Fatalf("expected logs_following, got %q", env.Type)
	}

	// Send detach to stop following
	h.sendControl(t, "detach", struct{}{})

	env = h.readControlMsg(t)
	if env.Type != "logs_done" {
		t.Fatalf("expected logs_done, got %q", env.Type)
	}
}

func TestTypeMessage(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-typ", "bonnie-type")

	h.sendControl(t, "type", protocol.TypeMsg{
		SessionID: "braw-typ",
		Input:     "hello",
	})

	env := h.readControlMsg(t)
	if env.Type != "typed" {
		t.Fatalf("expected typed, got %q", env.Type)
	}
}

func TestTypeMessageNoNewline(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "canny-typ", "canny-type")

	h.sendControl(t, "type", protocol.TypeMsg{
		SessionID: "canny-typ",
		Input:     "y",
		NoNewline: true,
	})

	env := h.readControlMsg(t)
	if env.Type != "typed" {
		t.Fatalf("expected typed, got %q", env.Type)
	}
}

func TestTypeSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "type", protocol.TypeMsg{SessionID: "haar", Input: "x"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestTypeInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "type", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestScreenPreview(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-sp", "bonnie-preview")

	h.sendControl(t, "screen_preview", protocol.ScreenPreviewMsg{SessionID: "braw-sp"})

	env := h.readControlMsg(t)
	if env.Type != "screen_preview_response" {
		t.Fatalf("expected screen_preview_response, got %q", env.Type)
	}

	var resp protocol.ScreenPreviewResponseMsg

	_ = protocol.DecodePayload(env, &resp)

	if resp.SessionID != "braw-sp" {
		t.Errorf("session_id = %q, want %q", resp.SessionID, "braw-sp")
	}
}

func TestScreenPreviewNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "screen_preview", protocol.ScreenPreviewMsg{SessionID: "haar"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestScreenPreviewInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "screen_preview", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestScreenSnapshot(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-ss", "bonnie-snapshot")

	h.sendControl(t, "screen_snapshot", protocol.ScreenSnapshotMsg{SessionID: "braw-ss"})

	env := h.readControlMsg(t)
	if env.Type != "screen_snapshot_response" {
		t.Fatalf("expected screen_snapshot_response, got %q", env.Type)
	}

	var resp protocol.ScreenSnapshotResponseMsg

	_ = protocol.DecodePayload(env, &resp)

	if resp.SessionID != "braw-ss" {
		t.Errorf("session_id = %q, want %q", resp.SessionID, "braw-ss")
	}

	if resp.Cols == 0 || resp.Rows == 0 {
		t.Error("expected non-zero cols/rows")
	}
}

func TestScreenSnapshotNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "screen_snapshot", protocol.ScreenSnapshotMsg{SessionID: "haar"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestScreenSnapshotInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "screen_snapshot", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestUpgrade(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "upgrade", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "upgrading" {
		t.Fatalf("expected upgrading, got %q", env.Type)
	}

	// Handler returns after upgrade, so the done channel should close.
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after upgrade")
	}
}

func TestUpgradeSameVersion(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "upgrade", protocol.UpgradeMsg{
		ExecPath:      "/tmp/test-gr",
		ClientVersion: version.Version,
	})

	env := h.readControlMsg(t)
	if env.Type != "upgrading" {
		t.Fatalf("expected upgrading for same-version request, got %q", env.Type)
	}

	select {
	case path := <-h.sm.upgradeCh:
		if path != "/tmp/test-gr" {
			t.Errorf("upgradeCh received %q, want /tmp/test-gr", path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upgradeCh did not receive exec path")
	}
}

func TestUpgradeAlreadyInProgress(t *testing.T) {
	h := newTestHarness(t)

	// Fill the channel so the next send hits default case
	h.sm.upgradeCh <- ""

	h.sendControl(t, "upgrade", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var errMsg protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &errMsg)

	if errMsg.Message != "upgrade already in progress" {
		t.Errorf("error message = %q", errMsg.Message)
	}

	// Handler should return after the error response for upgrade
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after failed upgrade")
	}
}

func TestMsgPub(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "msg_pub", protocol.MsgPubMsg{
		Stream:     "blether-topic",
		SenderID:   "braw-sender",
		SenderName: "Bonnie Lass",
		Body:       "braw day",
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_published" {
		t.Fatalf("expected msg_published, got %q", env.Type)
	}

	var msg Message

	_ = protocol.DecodePayload(env, &msg)

	if msg.Body != "braw day" {
		t.Errorf("body = %q", msg.Body)
	}

	if msg.Stream != "blether-topic" {
		t.Errorf("stream = %q", msg.Stream)
	}
}

func TestMsgPubInboxNotifiesTarget(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "bonnie-target", "bonnie-session")

	h.sendControl(t, "msg_pub", protocol.MsgPubMsg{
		Stream:     "inbox:bonnie-target",
		SenderID:   "glen-sender",
		SenderName: "Ailsa",
		Body:       "braw news frae anither tree",
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_published" {
		t.Fatalf("expected msg_published, got %q", env.Type)
	}

	// The daemon injects the notification asynchronously. Give the goroutine
	// and PTY write a moment to propagate.
	time.Sleep(200 * time.Millisecond)

	ptySess, ok := h.sm.GetPTY("bonnie-target")
	if !ok {
		t.Fatal("target PTY session not found")
	}

	tail, err := ptySess.Scrollback.Tail(500)
	if err != nil {
		t.Fatalf("scrollback tail: %v", err)
	}

	scrollback := string(tail)
	if !strings.Contains(scrollback, "New message from Ailsa") {
		t.Errorf("notification not found in scrollback; got:\n%s", scrollback)
	}

	if !strings.Contains(scrollback, "gr msg inbox --all --ack") {
		t.Errorf("notification should reference gr msg inbox command; got:\n%s", scrollback)
	}
}

func TestMsgPubInboxQuietSuppressesNotification(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "wheesht1", "wheesht-session")

	h.sendControl(t, "msg_pub", protocol.MsgPubMsg{
		Stream:     "inbox:wheesht1",
		SenderID:   "sender",
		SenderName: "Hamish",
		Body:       "wheesht message",
		Quiet:      true,
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_published" {
		t.Fatalf("expected msg_published, got %q", env.Type)
	}

	time.Sleep(100 * time.Millisecond)

	ptySess, ok := h.sm.GetPTY("wheesht1")
	if !ok {
		t.Fatal("target PTY session not found")
	}

	tail, err := ptySess.Scrollback.Tail(500)
	if err != nil {
		t.Fatalf("scrollback tail: %v", err)
	}

	if strings.Contains(string(tail), "New message from Hamish") {
		t.Error("notification should not appear when Quiet=true")
	}
}

func TestMsgPubInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "msg_pub", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestMsgSubReadAll(t *testing.T) {
	h := newTestHarness(t)

	// Publish a message first
	_, _ = h.sm.messages.Publish("blether1", "braw1", "Braw", "neep1", "", "")
	_, _ = h.sm.messages.Publish("blether1", "canny1", "Canny", "neep2", "", "")

	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream: "blether1",
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_message" {
		t.Fatalf("expected msg_message, got %q", env.Type)
	}

	var m1 Message

	_ = protocol.DecodePayload(env, &m1)

	if m1.Body != "neep1" {
		t.Errorf("first message body = %q", m1.Body)
	}

	env = h.readControlMsg(t)
	if env.Type != "msg_message" {
		t.Fatalf("expected msg_message, got %q", env.Type)
	}

	env = h.readControlMsg(t)
	if env.Type != "msg_done" {
		t.Fatalf("expected msg_done, got %q", env.Type)
	}
}

func TestMsgSubWithAck(t *testing.T) {
	h := newTestHarness(t)

	_, _ = h.sm.messages.Publish("blether-ack", "braw1", "Braw", "neep1", "", "")

	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream:     "blether-ack",
		Subscriber: "kirk1",
		Ack:        true,
	})

	h.readControlMsg(t) // msg_message
	h.readControlMsg(t) // msg_done

	// Subscribe again with only_unread — should get nothing
	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream:     "blether-ack",
		Subscriber: "kirk1",
		OnlyUnread: true,
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_done" {
		t.Fatalf("expected msg_done (no unread messages), got %q", env.Type)
	}
}

func TestMsgSubOnlyUnreadWithAck(t *testing.T) {
	h := newTestHarness(t)

	_, _ = h.sm.messages.Publish("inbox:braw-sess", "braw-sender", "Ailsa", "braw-hello", "", "")
	_, _ = h.sm.messages.Publish("inbox:braw-sess", "canny-sender", "Hamish", "bonnie-world", "", "")

	// First read: OnlyUnread + Ack (mimics check-inbox hook)
	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream:     "inbox:braw-sess",
		Subscriber: "braw-sess",
		OnlyUnread: true,
		Ack:        true,
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_message" {
		t.Fatalf("first sub: expected msg_message, got %q", env.Type)
	}

	env = h.readControlMsg(t)
	if env.Type != "msg_message" {
		t.Fatalf("first sub: expected second msg_message, got %q", env.Type)
	}

	env = h.readControlMsg(t)
	if env.Type != "msg_done" {
		t.Fatalf("first sub: expected msg_done, got %q", env.Type)
	}

	// Second read: same subscriber, OnlyUnread — should see nothing
	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream:     "inbox:braw-sess",
		Subscriber: "braw-sess",
		OnlyUnread: true,
	})

	env = h.readControlMsg(t)
	if env.Type != "msg_done" {
		t.Fatalf("second sub: expected msg_done (no unread), got %q", env.Type)
	}
}

func TestMsgSubInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "msg_sub", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestMsgSubWaitWithExistingMessages(t *testing.T) {
	h := newTestHarness(t)

	_, _ = h.sm.messages.Publish("blether-wait", "braw1", "Braw", "bide-msg", "", "")

	// --wait with existing messages should return immediately
	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream: "blether-wait",
		Wait:   true,
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_message" {
		t.Fatalf("expected msg_message, got %q", env.Type)
	}

	env = h.readControlMsg(t)
	if env.Type != "msg_done" {
		t.Fatalf("expected msg_done, got %q", env.Type)
	}
}

func TestMsgSubWaitForNewMessage(t *testing.T) {
	h := newTestHarness(t)

	// --wait with no existing messages should block until a message arrives
	go func() {
		time.Sleep(50 * time.Millisecond)

		_, _ = h.sm.messages.Publish("blether-new", "braw1", "Braw", "braw-new", "", "")
	}()

	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream: "blether-new",
		Wait:   true,
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_following" {
		t.Fatalf("expected msg_following, got %q", env.Type)
	}

	env, ok := h.readControlMsgTimeout(t, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for msg_message")
	}

	if env.Type != "msg_message" {
		t.Fatalf("expected msg_message, got %q", env.Type)
	}

	env, ok = h.readControlMsgTimeout(t, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for msg_done")
	}

	if env.Type != "msg_done" {
		t.Fatalf("expected msg_done, got %q", env.Type)
	}
}

func TestMsgAck(t *testing.T) {
	h := newTestHarness(t)

	_, _ = h.sm.messages.Publish("blether-ack-stream", "braw1", "Braw", "neep1", "", "")

	h.sendControl(t, "msg_ack", protocol.MsgAckMsg{
		Stream:     "blether-ack-stream",
		Subscriber: "kirk1",
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_acked" {
		t.Fatalf("expected msg_acked, got %q", env.Type)
	}
}

func TestMsgAckInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "msg_ack", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestMsgTopics(t *testing.T) {
	h := newTestHarness(t)

	_, _ = h.sm.messages.Publish("blether-a", "braw1", "Braw", "neep1", "", "")
	_, _ = h.sm.messages.Publish("blether-b", "canny1", "Canny", "neep2", "", "")

	h.sendControl(t, "msg_topics", protocol.MsgTopicsMsg{Subscriber: "kirk1"})

	env := h.readControlMsg(t)
	if env.Type != "msg_topics_list" {
		t.Fatalf("expected msg_topics_list, got %q", env.Type)
	}

	var resp struct {
		Streams []StreamInfo `json:"streams"`
	}

	_ = protocol.DecodePayload(env, &resp)

	if len(resp.Streams) != 2 {
		t.Errorf("expected 2 streams, got %d", len(resp.Streams))
	}
}

func TestMsgTopicsInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "msg_topics", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestAttachReplacesExistingClient(t *testing.T) {
	h1 := newTestHarness(t)
	h1.addPTYSession(t, "braw-repl", "bonnie-replace")

	// First client attaches
	h1.sendControl(t, "attach", protocol.AttachMsg{SessionID: "braw-repl"})

	env := h1.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached, got %q", env.Type)
	}

	// Start reading from h1 in the background before second client attaches.
	// net.Pipe is synchronous, so the kick write blocks until we read.
	detachedCh := make(chan protocol.Envelope, 1)

	go func() {
		for {
			frame, err := h1.reader.ReadFrame()
			if err != nil {
				return
			}

			if frame.Channel == protocol.ChannelControl {
				env, _ := protocol.DecodeControl(frame.Payload)
				if env.Type == "detached" {
					detachedCh <- env
					return
				}
			}
		}
	}()

	// Second client connects and attaches to the same session
	clientConn2, serverConn2 := net.Pipe()
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	go func() {
		defer close(done2)

		HandleConnection(ctx2, serverConn2, h1.sm, log)
	}()

	writer2 := protocol.NewFrameWriter(clientConn2)
	reader2 := protocol.NewFrameReader(clientConn2)

	data, _ := protocol.EncodeControl("attach", protocol.AttachMsg{SessionID: "braw-repl"})
	_ = writer2.WriteFrame(protocol.ChannelControl, data)

	// Read attached on second client
	for {
		frame, err := reader2.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}

		if frame.Channel == protocol.ChannelControl {
			env2, _ := protocol.DecodeControl(frame.Payload)
			if env2.Type == "attached" {
				break
			}
		}
	}

	// First client should receive a detached message
	select {
	case env := <-detachedCh:
		var detached protocol.DetachedMsg

		_ = protocol.DecodePayload(env, &detached)

		if detached.Reason != "replaced" {
			t.Errorf("reason = %q, want %q", detached.Reason, "replaced")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first client did not receive detached message")
	}

	cancel2()

	_ = clientConn2.Close()
	_ = serverConn2.Close()

	<-done2
}

func TestConcurrentAttachDetach(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-conc", "bonnie-conc")

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			clientConn, serverConn := net.Pipe()
			ctx, cancel := context.WithCancel(context.Background())
			log := slog.New(slog.NewTextHandler(io.Discard, nil))

			done := make(chan struct{})
			go func() {
				defer close(done)

				HandleConnection(ctx, serverConn, h.sm, log)
			}()

			writer := protocol.NewFrameWriter(clientConn)
			reader := protocol.NewFrameReader(clientConn)

			data, _ := protocol.EncodeControl("attach", protocol.AttachMsg{SessionID: "braw-conc"})
			_ = writer.WriteFrame(protocol.ChannelControl, data)

			// Read until we get an attached or detached response
			for {
				frame, err := reader.ReadFrame()
				if err != nil {
					break
				}

				if frame.Channel == protocol.ChannelControl {
					env, _ := protocol.DecodeControl(frame.Payload)
					if env.Type == "attached" || env.Type == "detached" {
						break
					}
				}
			}

			cancel()

			_ = clientConn.Close()
			_ = serverConn.Close()

			<-done
		}()
	}

	wg.Wait()
}

func TestConnectionCloseWhileAttached(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-close", "bonnie-close")

	clientConn, serverConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	done := make(chan struct{})
	go func() {
		defer close(done)

		HandleConnection(ctx, serverConn, h.sm, log)
	}()

	writer := protocol.NewFrameWriter(clientConn)
	reader := protocol.NewFrameReader(clientConn)

	data, _ := protocol.EncodeControl("attach", protocol.AttachMsg{SessionID: "braw-close"})
	_ = writer.WriteFrame(protocol.ChannelControl, data)

	// Wait for attached
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}

		if frame.Channel == protocol.ChannelControl {
			env, _ := protocol.DecodeControl(frame.Payload)
			if env.Type == "attached" {
				break
			}
		}
	}

	// Close the connection abruptly
	_ = clientConn.Close()

	// Handler should clean up and exit
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after client disconnected")
	}

	cancel()

	_ = serverConn.Close()
}

func TestContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.Default()
	paths := config.Paths{
		StateFile:  filepath.Join(tmpDir, "state.json"),
		LogDir:     filepath.Join(tmpDir, "logs"),
		MessagesDB: filepath.Join(tmpDir, "messages.db"),
	}
	_ = os.MkdirAll(paths.LogDir, 0o700)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sm := NewSessionManager(cfg, paths, log)
	sm.upgradeCh = make(chan string, 1)

	msgStore, _ := NewMsgStore(paths.MessagesDB)
	defer func() { _ = msgStore.Close() }()

	sm.messages = msgStore

	clientConn, serverConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)

		HandleConnection(ctx, serverConn, sm, log)
	}()

	// Cancel the context
	cancel()

	// The handler should eventually return. We also close the conn
	// so ReadFrame unblocks.
	_ = clientConn.Close()
	_ = serverConn.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after context cancellation")
	}
}

func TestSafeFrameWriter(t *testing.T) {
	var buf bytes.Buffer

	inner := protocol.NewFrameWriter(&buf)
	safe := &safeFrameWriter{writer: inner}

	if err := safe.WriteFrame(protocol.ChannelData, []byte("hello")); err != nil {
		t.Fatal(err)
	}

	reader := protocol.NewFrameReader(&buf)

	frame, err := reader.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}

	if frame.Channel != protocol.ChannelData || string(frame.Payload) != "hello" {
		t.Errorf("got channel=%d payload=%q", frame.Channel, frame.Payload)
	}
}

func TestSafeFrameWriterConcurrent(t *testing.T) {
	var buf bytes.Buffer

	inner := protocol.NewFrameWriter(&buf)
	safe := &safeFrameWriter{writer: inner}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			_ = safe.WriteFrame(protocol.ChannelData, []byte("concurrent"))
		}()
	}

	wg.Wait()
}

func TestFrameDataWriter(t *testing.T) {
	var buf bytes.Buffer

	inner := protocol.NewFrameWriter(&buf)
	safe := &safeFrameWriter{writer: inner}
	fdw := &frameDataWriter{writer: safe}

	n, err := fdw.Write([]byte("test data"))
	if err != nil {
		t.Fatal(err)
	}

	if n != 9 {
		t.Errorf("Write returned %d, want 9", n)
	}

	reader := protocol.NewFrameReader(&buf)

	frame, err := reader.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}

	if frame.Channel != protocol.ChannelData || string(frame.Payload) != "test data" {
		t.Errorf("got channel=%d payload=%q", frame.Channel, frame.Payload)
	}
}

func TestMsgSubFollowThreadFilter(t *testing.T) {
	h := newTestHarness(t)

	// Publish messages in different threads
	_, _ = h.sm.messages.Publish("thread-topic", "s1", "Agent", "thread-msg", "thread-1", "")
	_, _ = h.sm.messages.Publish("thread-topic", "s1", "Agent", "other-msg", "thread-2", "")

	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream:   "thread-topic",
		ThreadID: "thread-1",
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_message" {
		t.Fatalf("expected msg_message, got %q", env.Type)
	}

	var msg Message

	_ = protocol.DecodePayload(env, &msg)

	if msg.ThreadID != "thread-1" {
		t.Errorf("thread_id = %q, want %q", msg.ThreadID, "thread-1")
	}

	env = h.readControlMsg(t)
	if env.Type != "msg_done" {
		t.Fatalf("expected msg_done, got %q", env.Type)
	}
}

func TestMsgPubWithThread(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "msg_pub", protocol.MsgPubMsg{
		Stream:   "thread-pub",
		SenderID: "s1",
		Body:     "reply",
		ThreadID: "thread-1",
		ReplyTo:  "msg_abc",
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_published" {
		t.Fatalf("expected msg_published, got %q", env.Type)
	}

	var msg Message

	_ = protocol.DecodePayload(env, &msg)

	if msg.ThreadID != "thread-1" {
		t.Errorf("thread_id = %q", msg.ThreadID)
	}

	if msg.ReplyTo != "msg_abc" {
		t.Errorf("reply_to = %q", msg.ReplyTo)
	}
}

func TestMsgPubUsesCurrentNameAfterRename(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["s1"] = &SessionState{
		ID: "s1", Name: "old-name", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	if err := h.sm.Rename("s1", "new-name"); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	h.sendControl(t, "msg_pub", protocol.MsgPubMsg{
		Stream:     "test-topic",
		SenderID:   "s1",
		SenderName: "old-name",
		Body:       "hello after rename",
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_published" {
		t.Fatalf("expected msg_published, got %q", env.Type)
	}

	var msg Message

	_ = protocol.DecodePayload(env, &msg)

	if msg.SenderName != "new-name" {
		t.Errorf("sender_name = %q, want %q", msg.SenderName, "new-name")
	}
}

func TestKickedClientConnectionClosed(t *testing.T) {
	h1 := newTestHarness(t)
	h1.addPTYSession(t, "scunner1", "scunner-kick")

	// First client attaches
	h1.sendControl(t, "attach", protocol.AttachMsg{SessionID: "scunner1"})

	env := h1.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached, got %q", env.Type)
	}

	// Read from h1 in background so the synchronous net.Pipe doesn't block
	// the kick callback's WriteFrame.
	h1ReadErr := make(chan error, 1)

	go func() {
		for {
			_, err := h1.reader.ReadFrame()
			if err != nil {
				h1ReadErr <- err
				return
			}
		}
	}()

	// Second client connects and attaches to the same session, kicking h1.
	clientConn2, serverConn2 := net.Pipe()
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	go func() {
		defer close(done2)

		HandleConnection(ctx2, serverConn2, h1.sm, log)
	}()

	writer2 := protocol.NewFrameWriter(clientConn2)
	reader2 := protocol.NewFrameReader(clientConn2)

	data, _ := protocol.EncodeControl("attach", protocol.AttachMsg{SessionID: "scunner1"})
	_ = writer2.WriteFrame(protocol.ChannelControl, data)

	for {
		frame, err := reader2.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}

		if frame.Channel == protocol.ChannelControl {
			env2, _ := protocol.DecodeControl(frame.Payload)
			if env2.Type == "attached" {
				break
			}
		}
	}

	// The kicked client's connection should be closed, causing its reader to
	// return an error. This means the handler exits and stops accepting PTY input.
	select {
	case <-h1ReadErr:
	case <-time.After(2 * time.Second):
		t.Fatal("kicked client connection was not closed")
	}

	// The first client's handler should have exited.
	select {
	case <-h1.done:
	case <-time.After(2 * time.Second):
		t.Fatal("kicked client handler did not exit")
	}

	cancel2()

	_ = clientConn2.Close()
	_ = serverConn2.Close()

	<-done2
}

func TestKickedClientInputRejected(t *testing.T) {
	h1 := newTestHarness(t)
	h1.addPTYSession(t, "thrawn1", "thrawn-reject")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Client A attaches
	h1.sendControl(t, "attach", protocol.AttachMsg{SessionID: "thrawn1"})

	env := h1.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached, got %q", env.Type)
	}

	// Verify A is the attached client
	if !h1.sm.IsAttachedClient("thrawn1", h1.serverConn) {
		t.Fatal("client A should be the attached client")
	}

	// Drain client A reads in background so kick callback can write.
	go func() {
		for {
			if _, err := h1.reader.ReadFrame(); err != nil {
				return
			}
		}
	}()

	// Client B attaches, kicking client A.
	clientB, serverB := net.Pipe()
	ctxB, cancelB := context.WithCancel(context.Background())

	doneB := make(chan struct{})
	go func() { defer close(doneB); HandleConnection(ctxB, serverB, h1.sm, log) }()

	writerB := protocol.NewFrameWriter(clientB)
	readerB := protocol.NewFrameReader(clientB)

	data, _ := protocol.EncodeControl("attach", protocol.AttachMsg{SessionID: "thrawn1"})
	_ = writerB.WriteFrame(protocol.ChannelControl, data)

	for {
		frame, err := readerB.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}

		if frame.Channel == protocol.ChannelControl {
			env2, _ := protocol.DecodeControl(frame.Payload)
			if env2.Type == "attached" {
				break
			}
		}
	}

	// Wait for client A's handler to exit.
	select {
	case <-h1.done:
	case <-time.After(2 * time.Second):
		t.Fatal("client A handler did not exit after kick")
	}

	// Client A is no longer the attached client; B is.
	if h1.sm.IsAttachedClient("thrawn1", h1.serverConn) {
		t.Error("client A should no longer be the attached client")
	}

	if !h1.sm.IsAttachedClient("thrawn1", serverB) {
		t.Error("client B should be the attached client")
	}

	cancelB()

	_ = clientB.Close()
	_ = serverB.Close()

	<-doneB
}

func TestAttachSetsLastAttachedAt(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-ts", "bonnie-timestamp")

	before := time.Now().UTC()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "braw-ts"})

	env := h.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached, got %q", env.Type)
	}

	h.sm.mu.RLock()
	s := h.sm.state.Sessions["braw-ts"]
	lastAttached := s.LastAttachedAt

	h.sm.mu.RUnlock()

	if lastAttached == nil {
		t.Fatal("LastAttachedAt should be set after attach")
	}

	if lastAttached.Before(before) {
		t.Error("LastAttachedAt is before the attach call")
	}
}

func TestAttachPersistsLastAttachedAt(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "bide-ts", "bide-timestamp")

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "bide-ts"})

	env := h.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached, got %q", env.Type)
	}

	h.sm.mu.RLock()
	inMemory := h.sm.state.Sessions["bide-ts"].LastAttachedAt
	h.sm.mu.RUnlock()

	if inMemory == nil {
		t.Fatal("LastAttachedAt should be set after attach")
	}

	reloaded, err := LoadState(h.sm.paths.StateFile)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	s, ok := reloaded.Sessions["bide-ts"]
	if !ok {
		t.Fatal("session not found in reloaded state")
	}

	if s.LastAttachedAt == nil {
		t.Fatal("LastAttachedAt not persisted to disk")
	}

	if !s.LastAttachedAt.Equal(*inMemory) {
		t.Errorf("persisted LastAttachedAt %v != in-memory %v", s.LastAttachedAt, inMemory)
	}
}

func TestNullPayloadRejected(t *testing.T) {
	types := []struct {
		msgType string
		errText string
	}{
		{"create", "invalid create message"},
		{"attach", "invalid attach message"},
		{"delete", "invalid delete message"},
		{"stop", "invalid stop message"},
		{"rename", "invalid rename message"},
		{"resume", "invalid resume message"},
		{"logs", "invalid logs message"},
		{"type", "invalid type message"},
		{"msg_pub", "invalid msg_pub message"},
		{"msg_sub", "invalid msg_sub message"},
		{"msg_ack", "invalid msg_ack message"},
		{"msg_topics", "invalid msg_topics message"},
		{"fork", "invalid fork message"},
		{"screen_preview", "invalid screen_preview message"},
		{"screen_snapshot", "invalid screen_snapshot message"},
		{"status", "invalid status message"},
		{"status_report", "invalid status_report"},
		{"approval_request", "invalid approval_request"},
		{"approval_respond", "invalid approval_respond"},
		{"star", "invalid star message"},
		{"unstar", "invalid unstar message"},
	}

	for _, tt := range types {
		t.Run(tt.msgType, func(t *testing.T) {
			h := newTestHarness(t)

			h.sendControl(t, "handshake", protocol.HandshakeMsg{
				Version:      "1.0",
				ClientID:     "test",
				TerminalSize: [2]uint16{80, 24},
				Cwd:          "/tmp",
			})

			env := h.readControlMsg(t)
			if env.Type != "handshake_ok" {
				t.Fatalf("handshake: got %q", env.Type)
			}

			nullEnvelope, _ := json.Marshal(protocol.Envelope{
				Type:    tt.msgType,
				Payload: json.RawMessage("null"),
			})
			_ = h.writer.WriteFrame(protocol.ChannelControl, nullEnvelope)

			env = h.readControlMsg(t)
			if env.Type != "error" {
				t.Fatalf("expected error for null %s payload, got %q", tt.msgType, env.Type)
			}

			var errMsg protocol.ErrorMsg

			_ = protocol.DecodePayload(env, &errMsg)

			if errMsg.Message != tt.errText {
				t.Errorf("error message = %q, want %q", errMsg.Message, tt.errText)
			}
		})
	}
}

func TestAttachSwitchSession(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-sw1", "bonnie-one")
	h.addPTYSession(t, "braw-sw2", "bonnie-two")

	// Attach to first session
	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "braw-sw1"})

	env := h.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached, got %q", env.Type)
	}

	// Attach to second session (should detach from first)
	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "braw-sw2"})

	env = h.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached for second session, got %q", env.Type)
	}

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(env, &info)

	if info.ID != "braw-sw2" {
		t.Errorf("attached to %q, want %q", info.ID, "braw-sw2")
	}
}

func TestDiagnostics(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", Name: "ken-session", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
		WorktreePath: t.TempDir(),
	}
	h.sm.state.Sessions["canny1"] = &SessionState{
		ID: "canny1", Name: "bide-session", Status: StatusStopped,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "diagnostics", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "diagnostics" {
		t.Fatalf("expected diagnostics, got %q", env.Type)
	}

	var diag protocol.DiagnosticsMsg
	if err := protocol.DecodePayload(env, &diag); err != nil {
		t.Fatal(err)
	}

	if diag.DaemonPID == 0 {
		t.Error("expected non-zero daemon PID")
	}

	if diag.DaemonUptime == "" {
		t.Error("expected non-empty uptime")
	}

	if diag.Fleet.Total != 2 {
		t.Errorf("fleet total = %d, want 2", diag.Fleet.Total)
	}

	if len(diag.Sessions) != 2 {
		t.Errorf("session diagnostics = %d, want 2", len(diag.Sessions))
	}

	for _, sd := range diag.Sessions {
		if sd.ID == "braw1" {
			if !sd.WorktreeExists {
				t.Error("expected worktree to exist for braw1")
			}
		}
	}
}

func TestDiagnosticsEmpty(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "diagnostics", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "diagnostics" {
		t.Fatalf("expected diagnostics, got %q", env.Type)
	}

	var diag protocol.DiagnosticsMsg
	if err := protocol.DecodePayload(env, &diag); err != nil {
		t.Fatal(err)
	}

	if diag.Fleet.Total != 0 {
		t.Errorf("fleet total = %d, want 0", diag.Fleet.Total)
	}

	if len(diag.Sessions) != 0 {
		t.Errorf("session diagnostics = %d, want 0", len(diag.Sessions))
	}
}

func newTestHarnessWithConfig(t *testing.T, cfg *config.Config) *testHarness {
	t.Helper()

	tmpDir := os.TempDir()

	dir, err := os.MkdirTemp(tmpDir, t.Name())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		for range 5 {
			if err := os.RemoveAll(dir); err == nil {
				return
			}

			time.Sleep(50 * time.Millisecond)
		}
	})

	paths := config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		LogDir:     filepath.Join(dir, "logs"),
		DataDir:    filepath.Join(dir, "data"),
		MessagesDB: filepath.Join(dir, "messages.db"),
	}
	_ = os.MkdirAll(paths.LogDir, 0o700)
	_ = os.MkdirAll(paths.DataDir, 0o700)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sm := NewSessionManager(cfg, paths, log)
	sm.upgradeCh = make(chan string, 1)

	msgStore, err := NewMsgStore(paths.MessagesDB)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = msgStore.Close() })

	sm.messages = msgStore

	clientConn, serverConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())

	h := &testHarness{
		sm:         sm,
		conn:       clientConn,
		serverConn: serverConn,
		reader:     protocol.NewFrameReader(clientConn),
		writer:     protocol.NewFrameWriter(clientConn),
		cancel:     cancel,
		done:       make(chan struct{}),
	}

	go func() {
		defer close(h.done)

		HandleConnection(ctx, serverConn, sm, log)
	}()

	t.Cleanup(func() {
		cancel()

		_ = clientConn.Close()
		_ = serverConn.Close()

		<-h.done
	})

	return h
}

// assertAttachAutoResumes verifies that attaching to a session in the given
// non-running status auto-resumes it: the daemon returns an "attached" message
// with running status and a live PTY.
func assertAttachAutoResumes(t *testing.T, name string, status SessionStatus) {
	t.Helper()

	cfg := config.Default()
	cfg.Agents["test"] = config.Agent{
		Command:    "sleep",
		Args:       []string{"300"},
		ResumeArgs: []string{"300"},
	}

	h := newTestHarnessWithConfig(t, cfg)
	workDir := t.TempDir()

	h.sm.mu.Lock()
	h.sm.state.Sessions["s1"] = &SessionState{
		ID:           "s1",
		Name:         name,
		Agent:        "test",
		Status:       status,
		WorktreePath: workDir,
		CreatedAt:    time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "s1"})

	env := h.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached after auto-resume, got %q", env.Type)
	}

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(env, &info)

	if info.Status != "running" {
		t.Errorf("status = %q, want running", info.Status)
	}

	if ptySess, ok := h.sm.GetPTY("s1"); ok {
		t.Cleanup(func() {
			_ = ptySess.Kill()
			<-ptySess.Done()
			ptySess.Close()
		})
	} else {
		t.Error("expected PTY to exist after auto-resume attach")
	}
}

func TestAttachAutoResumesStoppedSession(t *testing.T) {
	assertAttachAutoResumes(t, "stopped-test", StatusStopped)
}

func TestAttachAutoResumesErroredSession(t *testing.T) {
	assertAttachAutoResumes(t, "errored-test", StatusErrored)
}

func TestAttachCreatingSessionReturnsStatusError(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["s1"] = &SessionState{
		ID:        "s1",
		Name:      "creating-test",
		Agent:     "claude",
		Status:    StatusCreating,
		CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "s1"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "being created") {
		t.Errorf("error = %q, want it to mention 'being created'", e.Message)
	}
}

func TestAttachDeletingSessionReturnsStatusError(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["s1"] = &SessionState{
		ID:        "s1",
		Name:      "deleting-test",
		Agent:     "claude",
		Status:    StatusDeleting,
		CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "s1"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "being deleted") {
		t.Errorf("error = %q, want it to mention 'being deleted'", e.Message)
	}
}

func TestResumeForInbox_SkipsNonStoppedSession(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-running", "braw-session")

	h.sm.notifyInbox("braw-running", "sender", "Sender")

	sess, ok := h.sm.Get("braw-running")
	if !ok {
		t.Fatal("session should still exist")
	}

	if sess.Status != StatusRunning {
		t.Errorf("status = %q, want %q — running sessions should not be affected", sess.Status, StatusRunning)
	}
}

func TestResumeForInbox_SkipsMissingSession(t *testing.T) {
	h := newTestHarness(t)

	// Should not panic when session doesn't exist
	h.sm.resumeForInbox("nonexistent", "sender", "Sender")
}

func TestResumeForInbox_AttemptsResumeForStoppedSession(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["bide-stopped"] = &SessionState{
		ID:     "bide-stopped",
		Name:   "bide",
		Agent:  "claude",
		Status: StatusStopped,
	}
	h.sm.mu.Unlock()

	h.sm.resumeForInbox("bide-stopped", "sender", "Sender")

	sess, ok := h.sm.Get("bide-stopped")
	if !ok {
		t.Fatal("session should still exist")
	}
	// Resume may succeed or fail depending on agent binary availability.
	// Either way it should not panic or leave the session in a bad state.
	if sess.Status != StatusRunning && sess.Status != StatusStopped {
		t.Errorf("status = %q, want running or stopped", sess.Status)
	}
}

func (h *testHarness) sendControlWithToken(t *testing.T, msgType string, payload any, token string) {
	t.Helper()

	data, err := protocol.EncodeControlWithToken(msgType, payload, token)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.writer.WriteFrame(protocol.ChannelControl, data); err != nil {
		t.Fatal(err)
	}
}

func (h *testHarness) addAuthenticatedSession(t *testing.T, id, name, token string) {
	t.Helper()
	h.sm.mu.Lock()
	h.sm.state.Sessions[id] = &SessionState{
		ID:        id,
		Name:      name,
		Agent:     "claude",
		Status:    StatusRunning,
		Token:     token,
		CreatedAt: time.Now().UTC(),
	}
	h.sm.tokenIndex[token] = id
	h.sm.mu.Unlock()
}

func TestMsgInboxReadUnread(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "bonnie-inbox", "bonnie", "tok-bonnie")

	_, _ = h.sm.messages.Publish("inbox:bonnie-inbox", "glen-sender", "Glen", "braw tidings", "", "")

	h.sendControlWithToken(t, "msg_inbox", protocol.MsgInboxMsg{
		OnlyUnread: true,
	}, "tok-bonnie")

	env := h.readControlMsg(t)
	if env.Type != "msg_message" {
		t.Fatalf("expected msg_message, got %q", env.Type)
	}

	var m Message

	_ = protocol.DecodePayload(env, &m)

	if m.Body != "braw tidings" {
		t.Errorf("body = %q, want %q", m.Body, "braw tidings")
	}

	env = h.readControlMsg(t)
	if env.Type != "msg_done" {
		t.Fatalf("expected msg_done, got %q", env.Type)
	}
}

func TestMsgInboxRequiresAuth(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "msg_inbox", protocol.MsgInboxMsg{})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "authenticated") {
		t.Errorf("error = %q, want mention of 'authenticated'", e.Message)
	}
}

func TestMsgInboxWithAck(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "canny-inbox", "canny", "tok-canny")

	_, _ = h.sm.messages.Publish("inbox:canny-inbox", "sender", "Sender", "first blether", "", "")

	h.sendControlWithToken(t, "msg_inbox", protocol.MsgInboxMsg{
		OnlyUnread: true,
		Ack:        true,
	}, "tok-canny")

	h.readControlMsg(t) // msg_message
	h.readControlMsg(t) // msg_done

	h.sendControlWithToken(t, "msg_inbox", protocol.MsgInboxMsg{
		OnlyUnread: true,
	}, "tok-canny")

	env := h.readControlMsg(t)
	if env.Type != "msg_done" {
		t.Fatalf("expected msg_done (no unread after ack), got %q", env.Type)
	}
}

func TestMsgSubRejectsInboxForAuthenticatedAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-sub", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "msg_sub", protocol.MsgSubMsg{
		Stream: "inbox:thrawn-sub",
	}, "tok-thrawn")

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "msg inbox") {
		t.Errorf("error = %q, want mention of 'msg inbox'", e.Message)
	}
}

func TestMsgAckRejectsInboxForAuthenticatedAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "fash-ack", "fash", "tok-fash")

	h.sendControlWithToken(t, "msg_ack", protocol.MsgAckMsg{
		Stream:     "inbox:fash-ack",
		Subscriber: "fash-ack",
	}, "tok-fash")

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "msg inbox") {
		t.Errorf("error = %q, want mention of 'msg inbox'", e.Message)
	}
}

func TestMsgTopicsFiltersInboxForAuthenticatedAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "ken-topics", "ken", "tok-ken")

	_, _ = h.sm.messages.Publish("inbox:ken-topics", "sender", "Sender", "private", "", "")
	_, _ = h.sm.messages.Publish("blether-public", "sender", "Sender", "public", "", "")

	h.sendControlWithToken(t, "msg_topics", protocol.MsgTopicsMsg{}, "tok-ken")

	env := h.readControlMsg(t)
	if env.Type != "msg_topics_list" {
		t.Fatalf("expected msg_topics_list, got %q", env.Type)
	}

	var resp struct {
		Streams []StreamInfo `json:"streams"`
	}

	_ = protocol.DecodePayload(env, &resp)

	for _, s := range resp.Streams {
		if strings.HasPrefix(s.Name, "inbox:") {
			t.Errorf("inbox stream %q should be filtered from topics for authenticated agents", s.Name)
		}
	}

	found := false

	for _, s := range resp.Streams {
		if s.Name == "blether-public" {
			found = true
		}
	}

	if !found {
		names := make([]string, len(resp.Streams))
		for i, s := range resp.Streams {
			names[i] = s.Name
		}

		t.Errorf("expected blether-public in topics, got %v", names)
	}
}

// setParent sets a session's ParentID under lock (test helper for #568 auth tests).
func (h *testHarness) setParent(t *testing.T, id, parentID string) {
	t.Helper()
	h.sm.mu.Lock()
	defer h.sm.mu.Unlock()

	s, ok := h.sm.state.Sessions[id]
	if !ok {
		t.Fatalf("session %q not in state", id)
	}

	s.ParentID = parentID
}

func (h *testHarness) parentOf(t *testing.T, id string) string {
	t.Helper()

	h.sm.mu.RLock()
	defer h.sm.mu.RUnlock()

	s, ok := h.sm.state.Sessions[id]
	if !ok {
		t.Fatalf("session %q not in state", id)
	}

	return s.ParentID
}

// #568: an authenticated session must not be able to adopt an unrelated session
// as its child via update — doing so would manufacture a descendant
// relationship and bypass the descendant-based auth model.
func TestUpdateRejectsUnauthorizedReparent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn", "thrawn", "tok-thrawn")
	h.addAuthenticatedSession(t, "scunner", "scunner", "tok-scunner")

	// thrawn tries to adopt the unrelated scunner as its own child.
	parent := "thrawn"
	h.sendControlWithToken(t, "update", protocol.UpdateMsg{
		SessionID: "scunner",
		ParentID:  &parent,
	}, "tok-thrawn")

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "not authorized") {
		t.Errorf("error = %q, want 'not authorized'", e.Message)
	}

	if got := h.parentOf(t, "scunner"); got != "" {
		t.Errorf("scunner ParentID = %q, want unchanged (empty)", got)
	}
}

// #568: a session may rearrange sessions within its own subtree.
func TestUpdateAllowsReparentingWithinOwnSubtree(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "ben", "ben", "tok-ben")
	h.addAuthenticatedSession(t, "bairn", "bairn", "tok-bairn")
	h.addAuthenticatedSession(t, "wee-bairn", "wee-bairn", "tok-wee")
	h.setParent(t, "bairn", "ben")
	h.setParent(t, "wee-bairn", "ben")

	// ben moves wee-bairn under bairn — both are within ben's subtree.
	parent := "bairn"
	h.sendControlWithToken(t, "update", protocol.UpdateMsg{
		SessionID: "wee-bairn",
		ParentID:  &parent,
	}, "tok-ben")

	env := h.readControlMsg(t)
	if env.Type != "updated" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(env, &e)
		t.Fatalf("expected updated, got %q (%s)", env.Type, e.Message)
	}

	if got := h.parentOf(t, "wee-bairn"); got != "bairn" {
		t.Errorf("wee-bairn ParentID = %q, want bairn", got)
	}
}

// #568: a session must not be able to graft one of its own sessions under an
// unrelated parent it has no authority over.
func TestUpdateRejectsAdoptingUnrelatedParent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "ben", "ben", "tok-ben")
	h.addAuthenticatedSession(t, "bairn", "bairn", "tok-bairn")
	h.addAuthenticatedSession(t, "scunner", "scunner", "tok-scunner")
	h.setParent(t, "bairn", "ben")

	// ben owns bairn (target ok) but tries to set its parent to the unrelated
	// scunner (new parent not authorized).
	parent := "scunner"
	h.sendControlWithToken(t, "update", protocol.UpdateMsg{
		SessionID: "bairn",
		ParentID:  &parent,
	}, "tok-ben")

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	if got := h.parentOf(t, "bairn"); got != "ben" {
		t.Errorf("bairn ParentID = %q, want unchanged (ben)", got)
	}
}

// #568: the orchestrator is the fleet control plane and may reparent any
// session under any parent.
func TestUpdateAllowsOrchestratorReparent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "orch", "orchestrator", "tok-orch")
	h.addAuthenticatedSession(t, "thrawn", "thrawn", "tok-thrawn")
	h.addAuthenticatedSession(t, "scunner", "scunner", "tok-scunner")
	h.sm.mu.Lock()
	h.sm.state.Sessions["orch"].SystemKind = SystemKindOrchestrator
	h.sm.mu.Unlock()

	// The orchestrator grafts one unrelated session under another unrelated
	// session — neither the target nor the new parent is the caller, so this
	// only succeeds via the orchestrator's new-parent exemption.
	parent := "scunner"
	h.sendControlWithToken(t, "update", protocol.UpdateMsg{
		SessionID: "thrawn",
		ParentID:  &parent,
	}, "tok-orch")

	env := h.readControlMsg(t)
	if env.Type != "updated" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(env, &e)
		t.Fatalf("expected updated, got %q (%s)", env.Type, e.Message)
	}

	if got := h.parentOf(t, "thrawn"); got != "scunner" {
		t.Errorf("thrawn ParentID = %q, want scunner", got)
	}
}

// #568: clearing a parent removes the session from its ancestors' authority, so
// a regular authenticated session must not be able to orphan itself (or a
// descendant) to escape its parent's control.
func TestUpdateRejectsSelfOrphan(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "ben", "ben", "tok-ben")
	h.addAuthenticatedSession(t, "bairn", "bairn", "tok-bairn")
	h.setParent(t, "bairn", "ben")

	// bairn tries to orphan itself out of ben's subtree.
	empty := ""
	h.sendControlWithToken(t, "update", protocol.UpdateMsg{
		SessionID: "bairn",
		ParentID:  &empty,
	}, "tok-bairn")

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "not authorized") {
		t.Errorf("error = %q, want 'not authorized'", e.Message)
	}

	if got := h.parentOf(t, "bairn"); got != "ben" {
		t.Errorf("bairn ParentID = %q, want unchanged (ben)", got)
	}
}

// #568: the orchestrator may orphan any session.
func TestUpdateAllowsOrchestratorOrphan(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "orch", "orchestrator", "tok-orch")
	h.addAuthenticatedSession(t, "bairn", "bairn", "tok-bairn")
	h.addAuthenticatedSession(t, "ben", "ben", "tok-ben")
	h.setParent(t, "bairn", "ben")
	h.sm.mu.Lock()
	h.sm.state.Sessions["orch"].SystemKind = SystemKindOrchestrator
	h.sm.mu.Unlock()

	empty := ""
	h.sendControlWithToken(t, "update", protocol.UpdateMsg{
		SessionID: "bairn",
		ParentID:  &empty,
	}, "tok-orch")

	env := h.readControlMsg(t)
	if env.Type != "updated" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(env, &e)
		t.Fatalf("expected updated, got %q (%s)", env.Type, e.Message)
	}

	if got := h.parentOf(t, "bairn"); got != "" {
		t.Errorf("bairn ParentID = %q, want cleared", got)
	}
}

// #568: the human CLI (unauthenticated) may orphan any session.
func TestUpdateAllowsHumanOrphan(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "bairn", "bairn", "tok-bairn")
	h.addAuthenticatedSession(t, "ben", "ben", "tok-ben")
	h.setParent(t, "bairn", "ben")

	empty := ""
	h.sendControl(t, "update", protocol.UpdateMsg{
		SessionID: "bairn",
		ParentID:  &empty,
	})

	env := h.readControlMsg(t)
	if env.Type != "updated" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(env, &e)
		t.Fatalf("expected updated, got %q (%s)", env.Type, e.Message)
	}

	if got := h.parentOf(t, "bairn"); got != "" {
		t.Errorf("bairn ParentID = %q, want cleared", got)
	}
}

// #568: rename-only updates (no ParentID) are still gated by the target check —
// a session cannot rename an unrelated session.
func TestUpdateRejectsRenameOnlyUnrelated(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn", "thrawn", "tok-thrawn")
	h.addAuthenticatedSession(t, "scunner", "scunner", "tok-scunner")

	newName := "bonnie"
	h.sendControlWithToken(t, "update", protocol.UpdateMsg{
		SessionID: "scunner",
		Name:      &newName,
	}, "tok-thrawn")

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "not authorized") {
		t.Errorf("error = %q, want 'not authorized'", e.Message)
	}
}

// #568: the human CLI (unauthenticated connection) retains unrestricted access.
func TestUpdateAllowsHumanReparent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn", "thrawn", "tok-thrawn")
	h.addAuthenticatedSession(t, "scunner", "scunner", "tok-scunner")

	// No token = human CLI.
	parent := "scunner"
	h.sendControl(t, "update", protocol.UpdateMsg{
		SessionID: "thrawn",
		ParentID:  &parent,
	})

	env := h.readControlMsg(t)
	if env.Type != "updated" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(env, &e)
		t.Fatalf("expected updated, got %q (%s)", env.Type, e.Message)
	}

	if got := h.parentOf(t, "thrawn"); got != "scunner" {
		t.Errorf("thrawn ParentID = %q, want scunner", got)
	}
}
