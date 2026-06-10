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
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
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
	os.MkdirAll(paths.LogDir, 0o700)
	os.MkdirAll(paths.DataDir, 0o700)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sm := NewSessionManager(cfg, paths, log)
	sm.upgradeCh = make(chan string, 1)

	msgStore, err := NewMsgStore(paths.MessagesDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { msgStore.Close() })
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
		clientConn.Close()
		serverConn.Close()
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
		h.conn.Close()
		h.serverConn.Close()
		sess.Kill()
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
	protocol.DecodePayload(env, &ok)
	if ok.Version != protocol.Version {
		t.Errorf("version = %q, want %q", ok.Version, protocol.Version)
	}
}

func TestHandshakeInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "handshake", Payload: json.RawMessage(`{"bad":`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestMalformedControlMessage(t *testing.T) {
	h := newTestHarness(t)

	h.writer.WriteFrame(protocol.ChannelControl, []byte(`{not valid json`))

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
	var errMsg protocol.ErrorMsg
	protocol.DecodePayload(env, &errMsg)
	if errMsg.Message != "malformed message" {
		t.Errorf("error message = %q", errMsg.Message)
	}
}

func TestListSessions(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["s1"] = &SessionState{
		ID: "s1", Name: "session-one", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.state.Sessions["s2"] = &SessionState{
		ID: "s2", Name: "session-two", Status: StatusStopped,
		Agent: "codex", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "list", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "session_list" {
		t.Fatalf("expected session_list, got %q", env.Type)
	}
	var list protocol.SessionListMsg
	protocol.DecodePayload(env, &list)
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
	protocol.DecodePayload(env, &list)
	if len(list.Sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(list.Sessions))
	}
}

func TestDeleteSession(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "del1", "to-delete")

	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: "del1"})

	env := h.readControlMsg(t)
	if env.Type != "deleted" {
		t.Fatalf("expected deleted, got %q", env.Type)
	}
}

func TestDeleteSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: "nonexistent"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestDeleteInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "delete", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestStopSession(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "stop1", "to-stop")

	h.sendControl(t, "stop", protocol.StopMsg{SessionID: "stop1"})

	env := h.readControlMsg(t)
	if env.Type != "stopped" {
		t.Fatalf("expected stopped, got %q", env.Type)
	}
}

func TestStopSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "stop", protocol.StopMsg{SessionID: "nonexistent"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestStopInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "stop", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestRenameSession(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["ren1"] = &SessionState{
		ID: "ren1", Name: "old-name", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "rename", protocol.RenameMsg{SessionID: "ren1", NewName: "new-name"})

	env := h.readControlMsg(t)
	if env.Type != "renamed" {
		t.Fatalf("expected renamed, got %q", env.Type)
	}

	h.sm.mu.RLock()
	name := h.sm.state.Sessions["ren1"].Name
	h.sm.mu.RUnlock()
	if name != "new-name" {
		t.Errorf("name = %q, want %q", name, "new-name")
	}
}

func TestRenameSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "rename", protocol.RenameMsg{SessionID: "nope", NewName: "x"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestRenameInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "rename", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestResumeInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "resume", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestResumeSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "resume", protocol.ResumeMsg{SessionID: "nope"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCreateInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "create", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestResizeWhileAttached(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "rsz1", "resize-test")

	// Handshake + attach
	h.sendControl(t, "handshake", protocol.HandshakeMsg{
		Version: "1.0", ClientID: "c1",
		TerminalSize: [2]uint16{80, 24}, Cwd: "/tmp",
	})
	h.readControlMsg(t) // handshake_ok

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "rsz1"})
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
	h.addPTYSession(t, "att1", "attach-test")

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "att1"})
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
	protocol.DecodePayload(env, &detached)
	if detached.Reason != "user" {
		t.Errorf("reason = %q, want %q", detached.Reason, "user")
	}
}

func TestAttachNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "nope"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestAttachInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "attach", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

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
	h.addPTYSession(t, "data1", "data-test")

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "data1"})
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
	h.addPTYSession(t, "log1", "logs-test")

	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "log1", Lines: 100})

	env := h.readControlMsg(t)
	if env.Type != "logs_done" {
		t.Fatalf("expected logs_done, got %q", env.Type)
	}
}

func TestLogsNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "nope"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestLogsInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "logs", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestLogsDefaultLines(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "logd", "logs-default")

	// Lines=0 should default to 300 internally
	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "logd", Lines: 0})

	env := h.readControlMsg(t)
	if env.Type != "logs_done" {
		t.Fatalf("expected logs_done, got %q", env.Type)
	}
}

func TestLogsFollowThenDetach(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "logf", "logs-follow")

	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "logf", Follow: true})

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
	h.addPTYSession(t, "typ1", "type-test")

	h.sendControl(t, "type", protocol.TypeMsg{
		SessionID: "typ1",
		Input:     "hello",
	})

	env := h.readControlMsg(t)
	if env.Type != "typed" {
		t.Fatalf("expected typed, got %q", env.Type)
	}
}

func TestTypeMessageNoNewline(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "typ2", "type-nonl")

	h.sendControl(t, "type", protocol.TypeMsg{
		SessionID: "typ2",
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

	h.sendControl(t, "type", protocol.TypeMsg{SessionID: "nope", Input: "x"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestTypeInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "type", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestScreenPreview(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "sp1", "preview-test")

	h.sendControl(t, "screen_preview", protocol.ScreenPreviewMsg{SessionID: "sp1"})

	env := h.readControlMsg(t)
	if env.Type != "screen_preview_response" {
		t.Fatalf("expected screen_preview_response, got %q", env.Type)
	}
	var resp protocol.ScreenPreviewResponseMsg
	protocol.DecodePayload(env, &resp)
	if resp.SessionID != "sp1" {
		t.Errorf("session_id = %q, want %q", resp.SessionID, "sp1")
	}
}

func TestScreenPreviewNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "screen_preview", protocol.ScreenPreviewMsg{SessionID: "nope"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestScreenPreviewInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "screen_preview", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestScreenSnapshot(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "ss1", "snapshot-test")

	h.sendControl(t, "screen_snapshot", protocol.ScreenSnapshotMsg{SessionID: "ss1"})

	env := h.readControlMsg(t)
	if env.Type != "screen_snapshot_response" {
		t.Fatalf("expected screen_snapshot_response, got %q", env.Type)
	}
	var resp protocol.ScreenSnapshotResponseMsg
	protocol.DecodePayload(env, &resp)
	if resp.SessionID != "ss1" {
		t.Errorf("session_id = %q, want %q", resp.SessionID, "ss1")
	}
	if resp.Cols == 0 || resp.Rows == 0 {
		t.Error("expected non-zero cols/rows")
	}
}

func TestScreenSnapshotNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "screen_snapshot", protocol.ScreenSnapshotMsg{SessionID: "nope"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestScreenSnapshotInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "screen_snapshot", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

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
	protocol.DecodePayload(env, &errMsg)
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
		Stream:     "test-topic",
		SenderID:   "sender1",
		SenderName: "Agent One",
		Body:       "hello world",
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_published" {
		t.Fatalf("expected msg_published, got %q", env.Type)
	}
	var msg Message
	protocol.DecodePayload(env, &msg)
	if msg.Body != "hello world" {
		t.Errorf("body = %q", msg.Body)
	}
	if msg.Stream != "test-topic" {
		t.Errorf("stream = %q", msg.Stream)
	}
}

func TestMsgPubInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "msg_pub", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestMsgSubReadAll(t *testing.T) {
	h := newTestHarness(t)

	// Publish a message first
	h.sm.messages.Publish("topic1", "s1", "Agent", "msg1", "", "")
	h.sm.messages.Publish("topic1", "s2", "Agent2", "msg2", "", "")

	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream: "topic1",
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_message" {
		t.Fatalf("expected msg_message, got %q", env.Type)
	}
	var m1 Message
	protocol.DecodePayload(env, &m1)
	if m1.Body != "msg1" {
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

	h.sm.messages.Publish("ack-topic", "s1", "Agent", "msg1", "", "")

	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream:     "ack-topic",
		Subscriber: "sub1",
		Ack:        true,
	})

	h.readControlMsg(t) // msg_message
	h.readControlMsg(t) // msg_done

	// Subscribe again with only_unread — should get nothing
	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream:     "ack-topic",
		Subscriber: "sub1",
		OnlyUnread: true,
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_done" {
		t.Fatalf("expected msg_done (no unread messages), got %q", env.Type)
	}
}

func TestMsgSubInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "msg_sub", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestMsgSubWaitWithExistingMessages(t *testing.T) {
	h := newTestHarness(t)

	h.sm.messages.Publish("wait-topic", "s1", "Agent", "existing", "", "")

	// --wait with existing messages should return immediately
	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream: "wait-topic",
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
		h.sm.messages.Publish("wait-new", "s1", "Agent", "new-msg", "", "")
	}()

	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream: "wait-new",
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

	h.sm.messages.Publish("ack-stream", "s1", "Agent", "msg1", "", "")

	h.sendControl(t, "msg_ack", protocol.MsgAckMsg{
		Stream:     "ack-stream",
		Subscriber: "sub1",
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_acked" {
		t.Fatalf("expected msg_acked, got %q", env.Type)
	}
}

func TestMsgAckInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "msg_ack", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestMsgTopics(t *testing.T) {
	h := newTestHarness(t)

	h.sm.messages.Publish("topicA", "s1", "Agent", "msg1", "", "")
	h.sm.messages.Publish("topicB", "s2", "Agent2", "msg2", "", "")

	h.sendControl(t, "msg_topics", protocol.MsgTopicsMsg{Subscriber: "sub1"})

	env := h.readControlMsg(t)
	if env.Type != "msg_topics_list" {
		t.Fatalf("expected msg_topics_list, got %q", env.Type)
	}

	var resp struct {
		Streams []StreamInfo `json:"streams"`
	}
	protocol.DecodePayload(env, &resp)
	if len(resp.Streams) != 2 {
		t.Errorf("expected 2 streams, got %d", len(resp.Streams))
	}
}

func TestMsgTopicsInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "msg_topics", Payload: json.RawMessage(`{bad`)})
	h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestAttachReplacesExistingClient(t *testing.T) {
	h1 := newTestHarness(t)
	h1.addPTYSession(t, "repl1", "replace-test")

	// First client attaches
	h1.sendControl(t, "attach", protocol.AttachMsg{SessionID: "repl1"})
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

	data, _ := protocol.EncodeControl("attach", protocol.AttachMsg{SessionID: "repl1"})
	writer2.WriteFrame(protocol.ChannelControl, data)

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
		protocol.DecodePayload(env, &detached)
		if detached.Reason != "replaced" {
			t.Errorf("reason = %q, want %q", detached.Reason, "replaced")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first client did not receive detached message")
	}

	cancel2()
	clientConn2.Close()
	serverConn2.Close()
	<-done2
}

func TestConcurrentAttachDetach(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "conc1", "concurrent-test")

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

			data, _ := protocol.EncodeControl("attach", protocol.AttachMsg{SessionID: "conc1"})
			writer.WriteFrame(protocol.ChannelControl, data)

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
			clientConn.Close()
			serverConn.Close()
			<-done
		}()
	}
	wg.Wait()
}

func TestConnectionCloseWhileAttached(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "close1", "close-test")

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

	data, _ := protocol.EncodeControl("attach", protocol.AttachMsg{SessionID: "close1"})
	writer.WriteFrame(protocol.ChannelControl, data)

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
	clientConn.Close()

	// Handler should clean up and exit
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after client disconnected")
	}

	cancel()
	serverConn.Close()
}

func TestContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.Default()
	paths := config.Paths{
		StateFile:  filepath.Join(tmpDir, "state.json"),
		LogDir:     filepath.Join(tmpDir, "logs"),
		MessagesDB: filepath.Join(tmpDir, "messages.db"),
	}
	os.MkdirAll(paths.LogDir, 0o700)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sm := NewSessionManager(cfg, paths, log)
	sm.upgradeCh = make(chan string, 1)

	msgStore, _ := NewMsgStore(paths.MessagesDB)
	defer msgStore.Close()
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
	clientConn.Close()
	serverConn.Close()

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
			safe.WriteFrame(protocol.ChannelData, []byte("concurrent"))
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
	h.sm.messages.Publish("thread-topic", "s1", "Agent", "thread-msg", "thread-1", "")
	h.sm.messages.Publish("thread-topic", "s1", "Agent", "other-msg", "thread-2", "")

	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream:   "thread-topic",
		ThreadID: "thread-1",
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_message" {
		t.Fatalf("expected msg_message, got %q", env.Type)
	}
	var msg Message
	protocol.DecodePayload(env, &msg)
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
	protocol.DecodePayload(env, &msg)
	if msg.ThreadID != "thread-1" {
		t.Errorf("thread_id = %q", msg.ThreadID)
	}
	if msg.ReplyTo != "msg_abc" {
		t.Errorf("reply_to = %q", msg.ReplyTo)
	}
}

func TestKickedClientConnectionClosed(t *testing.T) {
	h1 := newTestHarness(t)
	h1.addPTYSession(t, "kick1", "kick-test")

	// First client attaches
	h1.sendControl(t, "attach", protocol.AttachMsg{SessionID: "kick1"})
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

	data, _ := protocol.EncodeControl("attach", protocol.AttachMsg{SessionID: "kick1"})
	writer2.WriteFrame(protocol.ChannelControl, data)

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
	clientConn2.Close()
	serverConn2.Close()
	<-done2
}

func TestAttachSetsLastAttachedAt(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "att-ts", "attach-timestamp")

	before := time.Now().UTC()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "att-ts"})
	env := h.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached, got %q", env.Type)
	}

	h.sm.mu.RLock()
	s := h.sm.state.Sessions["att-ts"]
	lastAttached := s.LastAttachedAt
	h.sm.mu.RUnlock()

	if lastAttached == nil {
		t.Fatal("LastAttachedAt should be set after attach")
	}
	if lastAttached.Before(before) {
		t.Error("LastAttachedAt is before the attach call")
	}
}

func TestAttachSwitchSession(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "sw1", "session-one")
	h.addPTYSession(t, "sw2", "session-two")

	// Attach to first session
	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "sw1"})
	env := h.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached, got %q", env.Type)
	}

	// Attach to second session (should detach from first)
	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "sw2"})
	env = h.readControlMsg(t)
	if env.Type != "attached" {
		t.Fatalf("expected attached for second session, got %q", env.Type)
	}

	var info protocol.SessionInfo
	protocol.DecodePayload(env, &info)
	if info.ID != "sw2" {
		t.Errorf("attached to %q, want %q", info.ID, "sw2")
	}
}
