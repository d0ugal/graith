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

		HandleConnection(ctx, serverConn, ConnOrigin{}, sm, log)
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

	env := h.expectType(t, "handshake_ok")

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

	env := h.expectType(t, "handshake_err")

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

	h.expectType(t, "handshake_ok")
}

func TestHandshakeInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "handshake", Payload: json.RawMessage(`{"bad":`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
}

func TestMalformedControlMessage(t *testing.T) {
	h := newTestHarness(t)

	_ = h.writer.WriteFrame(protocol.ChannelControl, []byte(`{not valid json`))

	env := h.expectType(t, "error")

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

	env := h.expectType(t, "session_list")

	var list protocol.SessionListMsg

	_ = protocol.DecodePayload(env, &list)

	if len(list.Sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(list.Sessions))
	}
}

func TestListSessionsEmpty(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "list", struct{}{})

	env := h.expectType(t, "session_list")

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

	h.expectType(t, "deleted")
}

func TestDeleteSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: "haar-mist"})

	h.expectType(t, "error")
}

func TestDeleteInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "delete", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
}

func TestStopSession(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "bide1", "bide-still")

	h.sendControl(t, "stop", protocol.StopMsg{SessionID: "bide1"})

	h.expectType(t, "stopped")
}

func TestStopSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "stop", protocol.StopMsg{SessionID: "haar-mist"})

	h.expectType(t, "error")
}

func TestStopInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "stop", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
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

	h.expectType(t, "renamed")

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

	h.expectType(t, "error")
}

func TestRenameInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "rename", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
}

func TestResumeInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "resume", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
}

func TestResumeSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "resume", protocol.ResumeMsg{SessionID: "haar"})

	h.expectType(t, "error")
}

func TestCreateInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "create", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
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

	h.expectType(t, "attached")

	h.sendControl(t, "resize", protocol.ResizeMsg{Cols: 200, Rows: 50})

	// Resize doesn't send a response, so we verify by sending another command
	// and confirming the handler is still alive.
	h.sendControl(t, "list", struct{}{})

	h.expectType(t, "session_list")
}

func TestResizeWithoutAttach(t *testing.T) {
	h := newTestHarness(t)

	// Resize with nothing attached should be silently ignored
	h.sendControl(t, "resize", protocol.ResizeMsg{Cols: 120, Rows: 40})

	// Confirm handler is still alive
	h.sendControl(t, "list", struct{}{})

	h.expectType(t, "session_list")
}

func TestAttachAndDetach(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-att", "bonnie-attach")

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "braw-att"})

	env := h.expectType(t, "attached")

	h.sendControl(t, "detach", struct{}{})

	env = h.expectType(t, "detached")

	var detached protocol.DetachedMsg

	_ = protocol.DecodePayload(env, &detached)

	if detached.Reason != "user" {
		t.Errorf("reason = %q, want %q", detached.Reason, "user")
	}
}

func TestAttachNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "haar"})

	h.expectType(t, "error")
}

func TestAttachInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "attach", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
}

func TestDetachWithoutAttach(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "detach", struct{}{})

	h.expectType(t, "detached")
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

	h.expectType(t, "session_list")
}

func TestDataChannelWithoutAttach(t *testing.T) {
	h := newTestHarness(t)

	// Data with nothing attached should be silently ignored
	if err := h.writer.WriteFrame(protocol.ChannelData, []byte("ignored")); err != nil {
		t.Fatal(err)
	}

	h.sendControl(t, "list", struct{}{})

	h.expectType(t, "session_list")
}

func TestLogsNonFollow(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-log", "bonnie-logs")

	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "braw-log", Lines: 100})

	h.expectType(t, "logs_done")
}

func TestLogsNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "haar"})

	h.expectType(t, "error")
}

// addStoppedSession registers a session in state with no live PTY, mimicking a
// session that has stopped or crashed. If scrollback is non-empty it is written
// to the on-disk log so it can be read back without a live PTY.
func (h *testHarness) addStoppedSession(t *testing.T, id, name string, exitCode int, scrollback string) {
	t.Helper()

	if scrollback != "" {
		logPath := filepath.Join(h.sm.paths.LogDir, id+".log")
		if err := os.WriteFile(logPath, []byte(scrollback), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	code := exitCode

	h.sm.mu.Lock()
	h.sm.state.Sessions[id] = &SessionState{
		ID:        id,
		Name:      name,
		Agent:     "claude",
		Status:    StatusStopped,
		ExitCode:  &code,
		CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()
}

func TestLogsStoppedSessionWithScrollback(t *testing.T) {
	h := newTestHarness(t)
	h.addStoppedSession(t, "bide-log", "bide-still", 0, "line one\nline two\n")

	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "bide-log", Lines: 100})

	var (
		gotData []byte
		done    bool
	)

	for !done {
		frame, err := h.reader.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}

		switch frame.Channel {
		case protocol.ChannelData:
			gotData = append(gotData, frame.Payload...)
		case protocol.ChannelControl:
			env, err := protocol.DecodeControl(frame.Payload)
			if err != nil {
				t.Fatal(err)
			}

			if env.Type != "logs_done" {
				t.Fatalf("expected logs_done, got %q", env.Type)
			}

			done = true
		}
	}

	if !strings.Contains(string(gotData), "line two") {
		t.Fatalf("expected scrollback content, got %q", gotData)
	}
}

func TestLogsStoppedSessionNoOutput(t *testing.T) {
	h := newTestHarness(t)
	h.addStoppedSession(t, "dreich-log", "dreich-crash", 1, "")

	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "dreich-log"})

	env := h.expectType(t, "error")

	var e protocol.ErrorMsg
	if err := protocol.DecodePayload(env, &e); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(e.Message, "session not found") {
		t.Fatalf("crashed session should not report 'session not found', got %q", e.Message)
	}

	if !strings.Contains(e.Message, "no output captured") || !strings.Contains(e.Message, "exited with code 1") {
		t.Fatalf("expected a clear no-output message with exit code, got %q", e.Message)
	}
}

func TestLogsInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "logs", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
}

func TestLogsDefaultLines(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "neep-log", "neep-default")

	// Lines=0 should default to 300 internally
	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "neep-log", Lines: 0})

	h.expectType(t, "logs_done")
}

func TestLogsFollowThenDetach(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-logf", "bonnie-follow")

	h.sendControl(t, "logs", protocol.LogsMsg{SessionID: "braw-logf", Follow: true})

	h.expectType(t, "logs_following")

	// Send detach to stop following
	h.sendControl(t, "detach", struct{}{})

	h.expectType(t, "logs_done")
}

func TestTypeMessage(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-typ", "bonnie-type")

	h.sendControl(t, "type", protocol.TypeMsg{
		SessionID: "braw-typ",
		Input:     "hello",
	})

	h.expectType(t, "typed")
}

func TestTypeMessageNoNewline(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "canny-typ", "canny-type")

	h.sendControl(t, "type", protocol.TypeMsg{
		SessionID: "canny-typ",
		Input:     "y",
		NoNewline: true,
	})

	h.expectType(t, "typed")
}

func TestTypeSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "type", protocol.TypeMsg{SessionID: "haar", Input: "x"})

	h.expectType(t, "error")
}

func TestTypeInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "type", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
}

func TestScreenPreview(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-sp", "bonnie-preview")

	h.sendControl(t, "screen_preview", protocol.ScreenPreviewMsg{SessionID: "braw-sp"})

	env := h.expectType(t, "screen_preview_response")

	var resp protocol.ScreenPreviewResponseMsg

	_ = protocol.DecodePayload(env, &resp)

	if resp.SessionID != "braw-sp" {
		t.Errorf("session_id = %q, want %q", resp.SessionID, "braw-sp")
	}
}

func TestScreenPreviewNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "screen_preview", protocol.ScreenPreviewMsg{SessionID: "haar"})

	h.expectType(t, "error")
}

func TestScreenPreviewInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "screen_preview", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
}

func TestScreenSnapshot(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-ss", "bonnie-snapshot")

	h.sendControl(t, "screen_snapshot", protocol.ScreenSnapshotMsg{SessionID: "braw-ss"})

	env := h.expectType(t, "screen_snapshot_response")

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

	h.expectType(t, "error")
}

func TestScreenSnapshotInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "screen_snapshot", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
}

func TestUpgrade(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "upgrade", struct{}{})

	h.expectType(t, "upgrading")

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

	h.expectType(t, "upgrading")

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

	env := h.expectType(t, "error")

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

	env := h.expectType(t, "msg_published")

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

	h.expectType(t, "msg_published")

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

	h.expectType(t, "msg_published")

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

	h.expectType(t, "error")
}

func TestMsgSubReadAll(t *testing.T) {
	h := newTestHarness(t)

	// Publish a message first
	_, _ = h.sm.messages.Publish("blether1", "braw1", "Braw", "neep1", "", "")
	_, _ = h.sm.messages.Publish("blether1", "canny1", "Canny", "neep2", "", "")

	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream: "blether1",
	})

	env := h.expectType(t, "msg_message")

	var m1 Message

	_ = protocol.DecodePayload(env, &m1)

	if m1.Body != "neep1" {
		t.Errorf("first message body = %q", m1.Body)
	}

	env = h.expectType(t, "msg_message")

	env = h.expectType(t, "msg_done")
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

	h.expectType(t, "msg_done")
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

	h.expectType(t, "msg_message")

	h.expectType(t, "msg_message")

	h.expectType(t, "msg_done")

	// Second read: same subscriber, OnlyUnread — should see nothing
	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream:     "inbox:braw-sess",
		Subscriber: "braw-sess",
		OnlyUnread: true,
	})

	h.expectType(t, "msg_done")
}

func TestMsgSubInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "msg_sub", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
}

func TestMsgSubWaitWithExistingMessages(t *testing.T) {
	h := newTestHarness(t)

	_, _ = h.sm.messages.Publish("blether-wait", "braw1", "Braw", "bide-msg", "", "")

	// --wait with existing messages should return immediately
	h.sendControl(t, "msg_sub", protocol.MsgSubMsg{
		Stream: "blether-wait",
		Wait:   true,
	})

	h.expectType(t, "msg_message")

	h.expectType(t, "msg_done")
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

	env := h.expectType(t, "msg_following")

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

	h.expectType(t, "msg_acked")
}

func TestMsgAckInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "msg_ack", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	h.expectType(t, "error")
}

func TestMsgTopics(t *testing.T) {
	h := newTestHarness(t)

	_, _ = h.sm.messages.Publish("blether-a", "braw1", "Braw", "neep1", "", "")
	_, _ = h.sm.messages.Publish("blether-b", "canny1", "Canny", "neep2", "", "")

	h.sendControl(t, "msg_topics", protocol.MsgTopicsMsg{Subscriber: "kirk1"})

	env := h.expectType(t, "msg_topics_list")

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

	h.expectType(t, "error")
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

		HandleConnection(ctx2, serverConn2, ConnOrigin{}, h1.sm, log)
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

				HandleConnection(ctx, serverConn, ConnOrigin{}, h.sm, log)
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

		HandleConnection(ctx, serverConn, ConnOrigin{}, h.sm, log)
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

		HandleConnection(ctx, serverConn, ConnOrigin{}, sm, log)
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

	env := h.expectType(t, "msg_message")

	var msg Message

	_ = protocol.DecodePayload(env, &msg)

	if msg.ThreadID != "thread-1" {
		t.Errorf("thread_id = %q, want %q", msg.ThreadID, "thread-1")
	}

	env = h.expectType(t, "msg_done")
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

	env := h.expectType(t, "msg_published")

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

	env := h.expectType(t, "msg_published")

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

		HandleConnection(ctx2, serverConn2, ConnOrigin{}, h1.sm, log)
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
	go func() { defer close(doneB); HandleConnection(ctxB, serverB, ConnOrigin{}, h1.sm, log) }()

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

	h.expectType(t, "attached")

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

	h.expectType(t, "attached")

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

			env := h.expectType(t, "handshake_ok")

			nullEnvelope, _ := json.Marshal(protocol.Envelope{
				Type:    tt.msgType,
				Payload: json.RawMessage("null"),
			})
			_ = h.writer.WriteFrame(protocol.ChannelControl, nullEnvelope)

			env = h.expectType(t, "error")

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

	env := h.expectType(t, "attached")

	// Attach to second session (should detach from first)
	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "braw-sw2"})

	env = h.expectType(t, "attached")

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

	env := h.expectType(t, "diagnostics")

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

	// The handler must stamp the daemon's own version so gr doctor can detect a
	// CLI/daemon mismatch authoritatively (issue #945). Assert it's populated so
	// the population can't silently regress.
	if diag.DaemonVersion == "" {
		t.Error("expected non-empty daemon version")
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

	env := h.expectType(t, "diagnostics")

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

		HandleConnection(ctx, serverConn, ConnOrigin{}, sm, log)
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

	env := h.expectType(t, "attached")

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

	env := h.expectType(t, "error")

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

	env := h.expectType(t, "error")

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

	env := h.expectType(t, "msg_message")

	var m Message

	_ = protocol.DecodePayload(env, &m)

	if m.Body != "braw tidings" {
		t.Errorf("body = %q, want %q", m.Body, "braw tidings")
	}

	env = h.expectType(t, "msg_done")
}

func TestMsgInboxRequiresAuth(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "msg_inbox", protocol.MsgInboxMsg{})

	env := h.expectType(t, "error")

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

	h.expectType(t, "msg_done")
}

func TestMsgSubRejectsInboxForAuthenticatedAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-sub", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "msg_sub", protocol.MsgSubMsg{
		Stream: "inbox:thrawn-sub",
	}, "tok-thrawn")

	env := h.expectType(t, "error")

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

	env := h.expectType(t, "error")

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

	env := h.expectType(t, "msg_topics_list")

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

	env := h.expectType(t, "error")

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

	h.expectType(t, "error")

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

	env := h.expectType(t, "error")

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

	env := h.expectType(t, "error")

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

// This file adds handler-dispatch tests for control messages that were not
// otherwise exercised: repo listing, status summaries, star/unstar, session
// status queries, hook reports, restart/interrupt, conversation reads, fork /
// migrate payload validation, config reload, MCP connect guards, the scenario
// lifecycle messages, and the unsupported-message fallthrough. Each test drives
// HandleConnection through the net.Pipe harness with a constructed protocol
// message and asserts the reply type, so it protects the real success and
// error paths rather than padding line counts.

// sendWrongShapePayload sends a control message whose payload is syntactically
// valid JSON but cannot decode into the handler's target struct (a bare JSON
// string). This forces the *per-case* DecodePayload branch (e.g. "invalid star
// message") to fire, rather than the global malformed-frame gate that an
// unparseable frame would hit. A raw `{bad` payload cannot be used here because
// json.Marshal rejects it and yields an empty frame, which never reaches the
// per-case branch.
func (h *testHarness) sendWrongShapePayload(t *testing.T, msgType string) {
	t.Helper()

	raw, err := json.Marshal(protocol.Envelope{Type: msgType, Payload: json.RawMessage(`"scunner"`)})
	if err != nil {
		t.Fatalf("marshal envelope for %q: %v", msgType, err)
	}

	if err := h.writer.WriteFrame(protocol.ChannelControl, raw); err != nil {
		t.Fatal(err)
	}
}

// expectError reads the next control message, asserts it is an error, and
// checks the message contains wantSubstr — so a test can't pass by tripping a
// different error path than the one it targets.
func (h *testHarness) expectError(t *testing.T, wantSubstr string) {
	t.Helper()

	env := h.expectType(t, "error")

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, wantSubstr) {
		t.Fatalf("error message = %q, want substring %q", e.Message, wantSubstr)
	}
}

// expectType reads the next control message, asserts its type matches want, and
// returns the envelope so callers can decode the payload further.
func (h *testHarness) expectType(t *testing.T, want string) protocol.Envelope {
	t.Helper()

	env := h.readControlMsg(t)
	if env.Type != want {
		t.Fatalf("expected %q, got %q", want, env.Type)
	}

	return env
}

// --- unsupported / fallthrough -------------------------------------------

func TestCoverUnsupportedMessage(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "wheesht_unknown", struct{}{})

	h.expectError(t, "unsupported control message")
}

// --- repo_list ------------------------------------------------------------

func TestCoverRepoList(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "repo_list", struct{}{})

	h.expectType(t, "repo_list")
}

// --- diagnostics ----------------------------------------------------------

func TestCoverDiagnostics(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "diagnostics", struct{}{})

	env := h.expectType(t, "diagnostics")

	var d protocol.DiagnosticsMsg

	_ = protocol.DecodePayload(env, &d)

	if d.DaemonPID == 0 {
		t.Error("expected a non-zero daemon PID in diagnostics")
	}
}

// --- approval_list / approval_subscribe / approval_respond ----------------

func TestCoverApprovalList(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "approval_list", struct{}{})

	h.expectType(t, "approval_notification")
}

func TestCoverApprovalSubscribeLocalHuman(t *testing.T) {
	h := newTestHarness(t)

	// Local Unix socket (no token) resolves to the local human operator, who is
	// allowed to subscribe and immediately receives the current pending set.
	h.sendControl(t, "approval_subscribe", struct{}{})

	h.expectType(t, "approval_notification")
}

func TestCoverApprovalSubscribeRejectsAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-sess", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "approval_subscribe", struct{}{}, "tok-thrawn")

	h.expectError(t, "human operator")
}

func TestCoverApprovalRespondRejectsAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "fash-sess", "fash", "tok-fash")

	h.sendControlWithToken(t, "approval_respond", protocol.ApprovalRespondMsg{
		RequestID: "req-1", Decision: "allow",
	}, "tok-fash")

	h.expectError(t, "not permitted for agent sessions")
}

func TestCoverApprovalRespondInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	// Sent as the local human (no token) so it reaches the DecodePayload branch;
	// an agent token would short-circuit at the authenticated check first.
	h.sendWrongShapePayload(t, "approval_respond")

	h.expectError(t, "invalid approval_respond")
}

func TestCoverApprovalRespondNotFound(t *testing.T) {
	h := newTestHarness(t)

	// Local human responding to a request that does not exist.
	h.sendControl(t, "approval_respond", protocol.ApprovalRespondMsg{
		RequestID: "haar-missing", Decision: "deny",
	})

	h.expectError(t, "not found")
}

func TestCoverApprovalRequestInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "approval_request")

	h.expectError(t, "invalid approval_request")
}

// --- set_status -----------------------------------------------------------

func TestCoverSetStatusInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "set_status")

	h.expectError(t, "invalid set_status message")
}

func TestCoverSetStatusSetAndClear(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["ken1"] = &SessionState{
		ID: "ken1", Name: "ken-lad", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	// Set a summary.
	h.sendControl(t, "set_status", protocol.SetStatusMsg{SessionID: "ken1", Text: "workin awa"})

	h.expectType(t, "status_set")

	h.sm.mu.RLock()
	got := h.sm.state.Sessions["ken1"].SummaryText
	h.sm.mu.RUnlock()

	if got != "workin awa" {
		t.Errorf("summary text = %q, want %q", got, "workin awa")
	}

	// Clear it.
	h.sendControl(t, "set_status", protocol.SetStatusMsg{SessionID: "ken1", Clear: true})

	h.expectType(t, "status_set")

	h.sm.mu.RLock()
	got = h.sm.state.Sessions["ken1"].SummaryText
	h.sm.mu.RUnlock()

	if got != "" {
		t.Errorf("summary text after clear = %q, want empty", got)
	}
}

func TestCoverSetStatusNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "set_status", protocol.SetStatusMsg{SessionID: "haar", Text: "nae session"})

	h.expectError(t, "not found")
}

func TestCoverSetStatusClearNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "set_status", protocol.SetStatusMsg{SessionID: "haar", Clear: true})

	h.expectError(t, "not found")
}

func TestCoverSetStatusForcedToOwnSession(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "canny-own", "canny", "tok-canny")

	// An authenticated session's set_status is forced onto its own session ID,
	// even if it names a different target.
	h.sendControlWithToken(t, "set_status", protocol.SetStatusMsg{
		SessionID: "some-other", Text: "mine",
	}, "tok-canny")

	h.expectType(t, "status_set")

	h.sm.mu.RLock()
	got := h.sm.state.Sessions["canny-own"].SummaryText
	h.sm.mu.RUnlock()

	if got != "mine" {
		t.Errorf("summary applied to wrong session; own session text = %q", got)
	}
}

// --- status (session status query) ---------------------------------------

func TestCoverStatusInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "status")

	h.expectError(t, "invalid status message")
}

func TestCoverStatusNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "status", protocol.StatusRequestMsg{SessionID: "haar"})

	h.expectError(t, "not found")
}

func TestCoverStatusResponse(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["ken2"] = &SessionState{
		ID: "ken2", Name: "ken-status", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	_, _ = h.sm.messages.Publish("inbox:ken2", "brae-sender", "Brae", "unread bide", "", "")

	h.sendControl(t, "status", protocol.StatusRequestMsg{SessionID: "ken2"})

	env := h.expectType(t, "status_response")

	var resp protocol.StatusResponseMsg

	_ = protocol.DecodePayload(env, &resp)

	if resp.Session.ID != "ken2" {
		t.Errorf("session id = %q, want ken2", resp.Session.ID)
	}

	if resp.UnreadCount != 1 {
		t.Errorf("unread count = %d, want 1", resp.UnreadCount)
	}
}

// --- status_report --------------------------------------------------------

func TestCoverStatusReportInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "status_report")

	h.expectError(t, "invalid status_report")
}

func TestCoverStatusReport(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "kirk-rep", "kirk", "tok-kirk")

	// A real hook event drives the session's agent status and tool name, not
	// just the ack — so the test would catch HandleHookReport being skipped.
	h.sendControlWithToken(t, "status_report", protocol.StatusReportMsg{
		SessionID: "kirk-rep",
		Event:     "PreToolUse",
		ToolName:  "Edit",
	}, "tok-kirk")

	h.expectType(t, "status_reported")

	h.sm.mu.RLock()
	sess := h.sm.state.Sessions["kirk-rep"]
	agentStatus, toolName := sess.AgentStatus, sess.HookToolName

	h.sm.mu.RUnlock()

	if agentStatus != "active" {
		t.Errorf("agent status = %q, want active", agentStatus)
	}

	if toolName != "Edit" {
		t.Errorf("hook tool name = %q, want Edit", toolName)
	}
}

// --- star / unstar --------------------------------------------------------

func TestCoverStarUnstar(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["bonnie-star"] = &SessionState{
		ID: "bonnie-star", Name: "bonnie", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "star", protocol.StarMsg{SessionID: "bonnie-star"})

	h.expectType(t, "starred")

	h.sm.mu.RLock()
	starred := h.sm.state.Sessions["bonnie-star"].Starred
	h.sm.mu.RUnlock()

	if !starred {
		t.Error("expected session to be starred")
	}

	h.sendControl(t, "unstar", protocol.UnstarMsg{SessionID: "bonnie-star"})

	h.expectType(t, "unstarred")

	h.sm.mu.RLock()
	starred = h.sm.state.Sessions["bonnie-star"].Starred
	h.sm.mu.RUnlock()

	if starred {
		t.Error("expected session to be unstarred")
	}
}

func TestCoverStarNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "star", protocol.StarMsg{SessionID: "haar"})

	h.expectError(t, "not found")
}

func TestCoverUnstarNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "unstar", protocol.UnstarMsg{SessionID: "haar"})

	h.expectError(t, "not found")
}

func TestCoverStarInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "star")

	h.expectError(t, "invalid star message")
}

func TestCoverUnstarInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "unstar")

	h.expectError(t, "invalid unstar message")
}

// --- interrupt ------------------------------------------------------------

func TestCoverInterruptInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "interrupt")

	h.expectError(t, "invalid interrupt message")
}

func TestCoverInterruptNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "interrupt", protocol.InterruptMsg{SessionID: "haar"})

	h.expectError(t, "no live process to interrupt")
}

// --- restart --------------------------------------------------------------

func TestCoverRestartInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "restart")

	h.expectError(t, "invalid restart message")
}

func TestCoverRestartNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "restart", protocol.RestartMsg{SessionID: "haar"})

	h.expectError(t, "not found")
}

func TestCoverRestartWithChildrenNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "restart", protocol.RestartMsg{SessionID: "haar", Children: true})

	h.expectError(t, "not found")
}

// --- msg_conversation -----------------------------------------------------

func TestCoverMsgConversationInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "msg_conversation")

	h.expectError(t, "invalid msg_conversation message")
}

func TestCoverMsgConversation(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["blether-sess"] = &SessionState{
		ID: "blether-sess", Name: "blether", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	// A message in the session's inbox should appear in its conversation.
	_, _ = h.sm.messages.Publish("inbox:blether-sess", "glen-sender", "Glen", "haud on", "", "")

	h.sendControl(t, "msg_conversation", protocol.MsgConversationMsg{SessionID: "blether-sess"})

	env := h.expectType(t, "msg_conversation_list")

	var resp protocol.MsgConversationListMsg

	_ = protocol.DecodePayload(env, &resp)

	if len(resp.Messages) == 0 {
		t.Error("expected at least one conversation message")
	}
}

func TestCoverMsgConversationOversizedLimit(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["skelf-sess"] = &SessionState{
		ID: "skelf-sess", Name: "skelf", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	// Exercises the oversized-limit clamp branch (limit > maxConversationLimit):
	// the request must still succeed rather than be rejected for asking too much.
	h.sendControl(t, "msg_conversation", protocol.MsgConversationMsg{
		SessionID: "skelf-sess", Limit: 999999,
	})

	h.expectType(t, "msg_conversation_list")
}

// --- fork / migrate payload validation ------------------------------------

func TestCoverForkInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "fork")

	h.expectError(t, "invalid fork message")
}

func TestCoverMigrateInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "migrate")

	h.expectError(t, "invalid migrate message")
}

// --- reload ---------------------------------------------------------------

func TestCoverReloadLocalHuman(t *testing.T) {
	h := newTestHarness(t)

	// Point at a nonexistent config file so the reload deterministically falls
	// back to defaults (which match the harness config) instead of reading the
	// developer's real ~/.config/graith/config.toml.
	h.sm.configFile = filepath.Join(t.TempDir(), "nae.toml")

	h.sendControl(t, "reload", struct{}{})

	h.expectType(t, "reloaded")
}

func TestCoverReloadRejectsAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-reload", "thrawn", "tok-rl")

	h.sendControlWithToken(t, "reload", struct{}{}, "tok-rl")

	h.expectError(t, "not permitted for agent sessions")
}

// --- mcp_connect guards ---------------------------------------------------

func TestCoverMCPConnectInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "mcp_connect")

	h.expectError(t, "invalid mcp_connect")
}

func TestCoverMCPConnectNoManager(t *testing.T) {
	h := newTestHarness(t)

	// The harness has no MCP manager configured, so a connect must fail closed.
	h.sendControl(t, "mcp_connect", protocol.MCPConnectMsg{Server: "chrome"})

	h.expectError(t, "MCP manager not initialized")
}

// --- scenario lifecycle ---------------------------------------------------

func TestCoverScenarioStartRequiresAuth(t *testing.T) {
	h := newTestHarness(t)

	// Local human (unauthenticated) may not start a scenario.
	h.sendControl(t, "scenario_start", protocol.ScenarioStartMsg{Name: "strath"})

	h.expectError(t, "requires authentication")
}

func TestCoverScenarioStartInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "scenario_start")

	h.expectError(t, "invalid scenario_start message")
}

func TestCoverScenarioStatusNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_status", protocol.ScenarioStatusMsg{Name: "haar-strath"})

	h.expectError(t, "not found")
}

func TestCoverScenarioStatusInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "scenario_status")

	h.expectError(t, "invalid scenario_status message")
}

func TestCoverScenarioList(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_list", struct{}{})

	env := h.expectType(t, "scenario_list")

	var resp protocol.ScenarioListResponse

	_ = protocol.DecodePayload(env, &resp)

	if len(resp.Scenarios) != 0 {
		t.Errorf("expected no scenarios, got %d", len(resp.Scenarios))
	}
}

func TestCoverScenarioStopNotFound(t *testing.T) {
	h := newTestHarness(t)

	// Local human passes the scenario-op authorization; the operation then fails
	// because there is no such scenario.
	h.sendControl(t, "scenario_stop", protocol.ScenarioStopMsg{Name: "haar-strath"})

	h.expectError(t, "not found")
}

func TestCoverScenarioStopInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "scenario_stop")

	h.expectError(t, "invalid scenario_stop message")
}

func TestCoverScenarioDeleteNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_delete", protocol.ScenarioDeleteMsg{Name: "haar-strath"})

	h.expectError(t, "not found")
}

func TestCoverScenarioDeleteInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "scenario_delete")

	h.expectError(t, "invalid scenario_delete message")
}

func TestCoverScenarioResumeNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_resume", protocol.ScenarioResumeMsg{Name: "haar-strath"})

	h.expectError(t, "not found")
}

func TestCoverScenarioResumeInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "scenario_resume")

	h.expectError(t, "invalid scenario_resume message")
}

func TestCoverScenarioAddIncompleteSession(t *testing.T) {
	h := newTestHarness(t)

	// The local human passes the scenario-op check; AddToScenario then validates
	// the session input and rejects it (a valid name but no repo). This exercises
	// the scenario_add dispatch surfacing the operation error to the client.
	h.sendControl(t, "scenario_add", protocol.ScenarioAddMsg{
		Name:    "haar-strath",
		Session: protocol.ScenarioSessionInput{Name: "bairn"},
	})

	h.expectError(t, "repo is required")
}

func TestCoverScenarioAddInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "scenario_add")

	h.expectError(t, "invalid scenario_add message")
}

func TestCoverScenarioTaskDoneRequiresAuth(t *testing.T) {
	h := newTestHarness(t)

	// Unauthenticated (local human) task-done is rejected — there is no session
	// whose task could be marked done.
	h.sendControl(t, "scenario_task_done", protocol.ScenarioTaskDoneMsg{Name: "strath"})

	h.expectError(t, "authenticated session")
}

func TestCoverScenarioTaskDoneInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "scenario_task_done")

	h.expectError(t, "invalid scenario_task_done message")
}

// --- session lifecycle with children -------------------------------------

func TestCoverStopWithChildren(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "ben-root", "ben-parent")

	// Stop the session (and any descendants) — exercises the batch branch of
	// handleSessionLifecycle and its multi-session response shape.
	h.sendControl(t, "stop", protocol.StopMsg{SessionID: "ben-root", Children: true})

	env := h.expectType(t, "stopped")

	var resp struct {
		SessionID string   `json:"session_id"`
		Stopped   []string `json:"stopped"`
	}

	_ = protocol.DecodePayload(env, &resp)

	if resp.SessionID != "ben-root" {
		t.Errorf("session_id = %q, want ben-root", resp.SessionID)
	}

	// The root must actually appear in the affected list — a test that only
	// checked the event type would pass even on an empty result. (The PTY is
	// killed here; the state transition to StatusStopped happens asynchronously
	// once process death is observed, so it is not asserted synchronously.)
	if !containsString(resp.Stopped, "ben-root") {
		t.Errorf("stopped list %v does not include ben-root", resp.Stopped)
	}
}

func TestCoverDeleteWithChildren(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "brae-root", "brae-parent")

	// With the default retention, `gr delete --children` soft-deletes: the
	// subtree is hidden but preserved, not removed from state.
	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: "brae-root", Children: true})

	env := h.expectType(t, "deleted")

	var resp protocol.DeleteResultMsg

	_ = protocol.DecodePayload(env, &resp)

	if resp.SessionID != "brae-root" {
		t.Errorf("session_id = %q, want brae-root", resp.SessionID)
	}

	if !resp.Soft {
		t.Error("expected soft delete with default retention")
	}

	found := false

	for _, a := range resp.Affected {
		if a.SessionID == "brae-root" {
			found = true
		}
	}

	if !found {
		t.Errorf("affected list %v does not include brae-root", resp.Affected)
	}

	// The session must still be present in state, marked soft-deleted.
	s, ok := h.sm.Get("brae-root")
	if !ok {
		t.Fatal("expected brae-root to remain in state after soft delete")
	}

	if !s.IsSoftDeleted() {
		t.Error("expected brae-root to be marked soft-deleted")
	}
}

func TestCoverScenarioTaskDoneUnknownScenario(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "strath-sess", "strath", "tok-strath")

	h.sendControlWithToken(t, "scenario_task_done", protocol.ScenarioTaskDoneMsg{
		Name: "haar-strath",
	}, "tok-strath")

	h.expectError(t, "not found")
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}

	return false
}

// This file covers handler-dispatch branches round 1 did not reach: the
// attach guards for transient session states, the handshake profile-mismatch
// rejection, the per-case authorization checks that stop a session from acting
// on a target it doesn't own, and the two pure helpers that format a session's
// exit for logs/errors.

// --- attach transient-state guards --------------------------------------

func TestCoverAttachSessionCreating(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["haar-creating"] = &SessionState{
		ID: "haar-creating", Name: "haar-creating", Status: StatusCreating,
		CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "haar-creating"})
	h.expectError(t, "is being created")
}

func TestCoverAttachSessionDeleting(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["haar-deleting"] = &SessionState{
		ID: "haar-deleting", Name: "haar-deleting", Status: StatusDeleting,
		CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "haar-deleting"})
	h.expectError(t, "is being deleted")
}

// --- handshake profile mismatch -----------------------------------------

// TestCoverHandshakeProfileMismatch asserts a client whose profile differs from
// the daemon's is rejected with a handshake_err, not silently accepted. The
// test harness daemon runs with an empty profile, so any non-empty client
// profile trips the guard.
func TestCoverHandshakeProfileMismatch(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "handshake", protocol.HandshakeMsg{
		Version:      protocol.Version,
		Profile:      "thrawn",
		TerminalSize: [2]uint16{80, 24},
	})

	env := h.expectType(t, "handshake_err")

	var he protocol.HandshakeErrMsg
	if err := protocol.DecodePayload(env, &he); err != nil {
		t.Fatal(err)
	}

	if he.Reason == "" {
		t.Fatal("handshake_err reason should explain the profile mismatch")
	}
}

// TestCoverHandshakeVersionOkThenAuthOk is a positive control confirming the
// harness handshake itself succeeds with matching version/profile, so the
// mismatch test above is isolating the profile check rather than a broken
// handshake.
func TestCoverHandshakeOkBaseline(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "handshake", protocol.HandshakeMsg{
		Version:      protocol.Version,
		TerminalSize: [2]uint16{80, 24},
	})

	env := h.expectType(t, "handshake_ok")

	var ok protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(env, &ok); err != nil {
		t.Fatal(err)
	}

	if ok.DaemonVersion != version.Version {
		t.Errorf("daemon version = %q, want %q", ok.DaemonVersion, version.Version)
	}
}

// --- per-case authorization: session targeting a foreign session --------

// TestCoverSessionCannotTargetForeignSession drives each session-scoped control
// message from an authenticated session whose target is an unrelated session
// (not self, not a descendant). Every case must reject with "not authorized",
// proving the per-case checkTarget gate — the descendant-based authority model
// — holds across the dispatch table rather than only on the paths round 1
// happened to exercise.
func TestCoverSessionCannotTargetForeignSession(t *testing.T) {
	const callerToken = "tok-bairn"

	msgTypes := []string{
		"rename", "star", "unstar", "resume", "restart",
		"logs", "wait", "stop", "delete",
	}

	for _, mt := range msgTypes {
		t.Run(mt, func(t *testing.T) {
			h := newTestHarness(t)
			h.addAuthenticatedSession(t, "bairn-caller", "bairn-caller", callerToken)

			// An unrelated session the caller has no authority over.
			h.sm.mu.Lock()
			h.sm.state.Sessions["ben-foreign"] = &SessionState{
				ID: "ben-foreign", Name: "ben-foreign", Status: StatusRunning,
				CreatedAt: time.Now().UTC(),
			}
			h.sm.mu.Unlock()

			// A generic payload carrying only session_id; every targeted message
			// type decodes this into its SessionID field.
			payload := map[string]any{"session_id": "ben-foreign", "new_name": "scunner"}
			h.sendControlWithToken(t, mt, payload, callerToken)
			h.expectError(t, "not authorized")
		})
	}
}

// --- exit-description helpers -------------------------------------------

func TestCoverDescribeSessionExit(t *testing.T) {
	code := 137

	cases := []struct {
		name  string
		state SessionState
		want  string
	}{
		{"signal", SessionState{ExitSignal: "SIGKILL"}, "killed by signal SIGKILL"},
		{"exit code", SessionState{ExitCode: &code}, "exited with code 137"},
		{"status fallback", SessionState{Status: StatusStopped}, "status: stopped"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeSessionExit(tc.state); got != tc.want {
				t.Errorf("describeSessionExit() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCoverSessionLabel(t *testing.T) {
	if got := sessionLabel(SessionState{ID: "abc123", Name: "bonnie"}); got != "bonnie" {
		t.Errorf("sessionLabel with name = %q, want bonnie", got)
	}

	if got := sessionLabel(SessionState{ID: "abc123"}); got != "abc123" {
		t.Errorf("sessionLabel without name = %q, want id abc123", got)
	}
}

// TestCoverExitDescriptionSignalPrecedence pins that ExitSignal wins over
// ExitCode when both are set — the order the switch relies on.
func TestCoverExitDescriptionSignalPrecedence(t *testing.T) {
	code := 1
	got := describeSessionExit(SessionState{ExitSignal: "SIGTERM", ExitCode: &code, Status: StatusStopped})

	if got != "killed by signal SIGTERM" {
		t.Errorf("describeSessionExit precedence = %q, want signal to win", got)
	}
}
