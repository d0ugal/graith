package client

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestBuildHandshake_PopulatesFields(t *testing.T) {
	paths := config.Paths{Profile: "bothy"}

	hs := BuildHandshake(paths, 120, 40, "/tmp/glen")
	if hs.Version != protocol.Version {
		t.Errorf("Version = %q, want %q", hs.Version, protocol.Version)
	}

	if hs.Profile != "bothy" {
		t.Errorf("Profile = %q, want %q", hs.Profile, "bothy")
	}

	if hs.Cwd != "/tmp/glen" {
		t.Errorf("Cwd = %q, want %q", hs.Cwd, "/tmp/glen")
	}

	if hs.TerminalSize != [2]uint16{120, 40} {
		t.Errorf("TerminalSize = %v, want [120 40]", hs.TerminalSize)
	}

	if hs.ClientID == "" {
		t.Error("ClientID should be set (the pid)")
	}
}

func TestSendFrame_WritesGivenChannel(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverReader := protocol.NewFrameReader(serverConn)

	payload := []byte("braw-frame")

	errCh := make(chan error, 1)
	go func() { errCh <- c.SendFrame(protocol.ChannelControl, payload) }()

	frame, err := serverReader.ReadFrame()
	if err != nil {
		t.Fatalf("server ReadFrame: %v", err)
	}

	if sendErr := <-errCh; sendErr != nil {
		t.Fatalf("SendFrame: %v", sendErr)
	}

	if frame.Channel != protocol.ChannelControl {
		t.Errorf("channel = %d, want %d", frame.Channel, protocol.ChannelControl)
	}

	if string(frame.Payload) != string(payload) {
		t.Errorf("payload = %q, want %q", frame.Payload, payload)
	}
}

func TestSendControl_IncludesTokenWhenSet(t *testing.T) {
	c, serverConn := setupTestClient(t)
	c.token = "kelpie-token"

	serverReader := protocol.NewFrameReader(serverConn)

	errCh := make(chan error, 1)
	go func() { errCh <- c.SendControl("speir", struct{}{}) }()

	frame, err := serverReader.ReadFrame()
	if err != nil {
		t.Fatalf("server ReadFrame: %v", err)
	}

	if sendErr := <-errCh; sendErr != nil {
		t.Fatalf("SendControl: %v", sendErr)
	}

	var env protocol.Envelope
	if err := json.Unmarshal(frame.Payload, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if env.Token != "kelpie-token" {
		t.Errorf("token = %q, want %q (token should be included when set)", env.Token, "kelpie-token")
	}
}

func TestHandshake_SendsHandshakeControl(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverReader := protocol.NewFrameReader(serverConn)

	errCh := make(chan error, 1)
	go func() { errCh <- c.Handshake() }()

	frame, err := serverReader.ReadFrame()
	if err != nil {
		t.Fatalf("server ReadFrame: %v", err)
	}

	if hsErr := <-errCh; hsErr != nil {
		t.Fatalf("Handshake: %v", hsErr)
	}

	if frame.Channel != protocol.ChannelControl {
		t.Errorf("channel = %d, want ChannelControl", frame.Channel)
	}

	env, err := protocol.DecodeControl(frame.Payload)
	if err != nil {
		t.Fatalf("DecodeControl: %v", err)
	}

	if env.Type != "handshake" {
		t.Errorf("type = %q, want %q", env.Type, "handshake")
	}
}

func TestReadControlResponse_PropagatesReadError(t *testing.T) {
	c, serverConn := setupTestClient(t)

	// Closing the server side makes the client's next ReadFrame fail.
	_ = serverConn.Close()

	if _, err := c.ReadControlResponse(); err == nil {
		t.Fatal("ReadControlResponse should return an error when the connection is closed")
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns whatever
// was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	os.Stdout = w

	done := make(chan string, 1)

	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()

	fn()

	_ = w.Close()

	os.Stdout = orig

	return <-done
}

func TestWriteScreenRestore_NilAndEmptyAreNoOps(t *testing.T) {
	out := captureStdout(t, func() {
		WriteScreenRestore(nil)
		WriteScreenRestore(&protocol.ScreenSnapshotResponseMsg{Frame: ""})
	})

	if out != "" {
		t.Errorf("nil/empty snapshot should write nothing, got %q", out)
	}
}

func TestWriteScreenRestore_EmitsFrameAndCursor(t *testing.T) {
	snap := &protocol.ScreenSnapshotResponseMsg{
		Frame:         "hello bothy",
		CursorX:       4,
		CursorY:       2,
		CursorVisible: true,
	}

	out := captureStdout(t, func() { WriteScreenRestore(snap) })

	if !strings.Contains(out, "hello bothy") {
		t.Errorf("output should contain the frame body, got %q", out)
	}

	// Cursor is placed at (Y+1;X+1) = row 3, col 5.
	if !strings.Contains(out, "\x1b[3;5H") {
		t.Errorf("output should position the cursor at row 3 col 5, got %q", out)
	}

	// Visible cursor → the show-cursor sequence must be present.
	if !strings.Contains(out, "\x1b[?25h") {
		t.Errorf("output should show the cursor when CursorVisible is true, got %q", out)
	}
}

func TestWriteScreenRestore_HiddenCursorOmitsShowSequence(t *testing.T) {
	snap := &protocol.ScreenSnapshotResponseMsg{
		Frame:         "dreich",
		CursorVisible: false,
	}

	out := captureStdout(t, func() { WriteScreenRestore(snap) })

	if strings.Contains(out, "\x1b[?25h") {
		t.Errorf("hidden cursor should not emit the show-cursor sequence, got %q", out)
	}
}

// withStubDaemonStart makes the daemon unreachable: it points at a dead socket
// and stubs the daemon-spawn to fail, so any Connect-based helper fails fast.
func withStubDaemonStart(t *testing.T) (config.Paths, *config.Config) {
	t.Helper()

	shortenHandshakeTimeout(t, 100*time.Millisecond)
	shortenStartTimeout(t, 100*time.Millisecond)
	stubStartDaemon(t, func(string) error {
		return errors.New("nae daemon the day")
	})

	return config.Paths{SocketPath: "/tmp/graith-nonexistent-scunner.sock"}, config.Default()
}

func TestFetchScreenSnapshot_NilWhenDaemonUnreachable(t *testing.T) {
	paths, cfg := withStubDaemonStart(t)

	if got := FetchScreenSnapshot(cfg, paths, "", "braw"); got != nil {
		t.Errorf("FetchScreenSnapshot should return nil when the daemon is unreachable, got %+v", got)
	}
}

func TestFetchScrollbackPreview_EmptyWhenDaemonUnreachable(t *testing.T) {
	paths, cfg := withStubDaemonStart(t)

	if got := FetchScrollbackPreview(cfg, paths, "", "braw"); got != "" {
		t.Errorf("FetchScrollbackPreview should return \"\" when the daemon is unreachable, got %q", got)
	}
}

func TestFetchConversation_ErrorWhenDaemonUnreachable(t *testing.T) {
	paths, cfg := withStubDaemonStart(t)

	msgs, err := FetchConversation(cfg, paths, "", "braw")
	if err == nil {
		t.Error("FetchConversation should return an error when the daemon is unreachable")
	}

	if msgs != nil {
		t.Errorf("FetchConversation should return nil messages on error, got %+v", msgs)
	}
}
