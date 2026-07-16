package client

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func setupTestClient(t *testing.T) (*Client, net.Conn) {
	clientConn, serverConn := net.Pipe()

	t.Cleanup(func() { _ = clientConn.Close(); _ = serverConn.Close() })

	c := &Client{
		conn:   clientConn,
		reader: protocol.NewFrameReader(clientConn),
		writer: protocol.NewFrameWriter(clientConn),
		cfg:    config.Default(),
		paths:  config.Paths{},
	}

	return c, serverConn
}

func TestSendControl(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverReader := protocol.NewFrameReader(serverConn)

	type testPayload struct {
		Greeting string `json:"greeting"`
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.SendControl("hello", testPayload{Greeting: "world"})
	}()

	frame, err := serverReader.ReadFrame()
	if err != nil {
		t.Fatalf("server ReadFrame: %v", err)
	}

	if sendErr := <-errCh; sendErr != nil {
		t.Fatalf("SendControl: %v", sendErr)
	}

	if frame.Channel != protocol.ChannelControl {
		t.Errorf("channel = %d, want %d (ChannelControl)", frame.Channel, protocol.ChannelControl)
	}

	var env protocol.Envelope
	if err := json.Unmarshal(frame.Payload, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if env.Type != "hello" {
		t.Errorf("envelope type = %q, want %q", env.Type, "hello")
	}

	var got testPayload
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if got.Greeting != "world" {
		t.Errorf("payload greeting = %q, want %q", got.Greeting, "world")
	}
}

// transferFrame runs write concurrently with read, returning the frame read
// and failing if either side errors. It models one frame crossing the client
// ↔ server connection in either direction.
func transferFrame(t *testing.T, write func() error, read func() (protocol.Frame, error)) protocol.Frame {
	t.Helper()

	errCh := make(chan error, 1)
	go func() { errCh <- write() }()

	frame, err := read()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}

	if writeErr := <-errCh; writeErr != nil {
		t.Fatalf("write frame: %v", writeErr)
	}

	return frame
}

// assertFrame checks a received frame carries the expected channel and payload.
func assertFrame(t *testing.T, frame protocol.Frame, wantChannel byte, wantPayload []byte) {
	t.Helper()

	if frame.Channel != wantChannel {
		t.Errorf("channel = %d, want %d", frame.Channel, wantChannel)
	}

	if string(frame.Payload) != string(wantPayload) {
		t.Errorf("payload = %q, want %q", frame.Payload, wantPayload)
	}
}

func TestSendData(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverReader := protocol.NewFrameReader(serverConn)

	payload := []byte("hello, raw data\x00\x01\x02")

	frame := transferFrame(t, func() error { return c.SendData(payload) }, serverReader.ReadFrame)
	assertFrame(t, frame, protocol.ChannelData, payload)
}

func TestReadFrame(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverWriter := protocol.NewFrameWriter(serverConn)

	want := []byte("frame-from-server")

	frame := transferFrame(t, func() error { return serverWriter.WriteFrame(protocol.ChannelData, want) }, c.ReadFrame)
	assertFrame(t, frame, protocol.ChannelData, want)
}

func TestReadControlResponse(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverWriter := protocol.NewFrameWriter(serverConn)

	ctrlBytes, err := protocol.EncodeControl("handshake_ok", protocol.HandshakeOkMsg{
		Version:       "1.0",
		DaemonVersion: "0.1.0",
	})
	if err != nil {
		t.Fatalf("EncodeControl: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- serverWriter.WriteFrame(protocol.ChannelControl, ctrlBytes)
	}()

	env, err := c.ReadControlResponse()
	if err != nil {
		t.Fatalf("ReadControlResponse: %v", err)
	}

	if writeErr := <-errCh; writeErr != nil {
		t.Fatalf("server WriteFrame: %v", writeErr)
	}

	if env.Type != "handshake_ok" {
		t.Errorf("envelope type = %q, want %q", env.Type, "handshake_ok")
	}

	var msg protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(env, &msg); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}

	if msg.DaemonVersion != "0.1.0" {
		t.Errorf("daemon_version = %q, want %q", msg.DaemonVersion, "0.1.0")
	}
}

func TestReadControlResponseWithDataFrame(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverWriter := protocol.NewFrameWriter(serverConn)

	errCh := make(chan error, 1)
	go func() {
		errCh <- serverWriter.WriteFrame(protocol.ChannelData, []byte("not a control frame"))
	}()

	_, err := c.ReadControlResponse()
	if err == nil {
		t.Fatal("ReadControlResponse should have returned an error for a data frame")
	}

	if writeErr := <-errCh; writeErr != nil {
		t.Fatalf("server WriteFrame: %v", writeErr)
	}

	want := "expected control frame, got channel 1"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestApprovalOperationTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{"normal 10m", 10 * time.Minute, 11 * time.Minute},
		{"large 30m", 30 * time.Minute, 31 * time.Minute},
		{"zero", 0, time.Minute},
		{"negative clamped", -5 * time.Minute, time.Minute},
		{"small negative", -30 * time.Second, time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApprovalOperationTimeout(tt.timeout)
			if got != tt.want {
				t.Errorf("ApprovalOperationTimeout(%v) = %v, want %v", tt.timeout, got, tt.want)
			}
		})
	}
}

// startMockDaemon creates a Unix socket listener that completes one handshake
// and returns both the listener and the server-side connection for further use.
func startMockDaemon(t *testing.T) (string, chan net.Conn) {
	t.Helper()

	dir, err := os.MkdirTemp("", "gr")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socketPath := dir + "/s"

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = ln.Close() })

	serverReady := make(chan net.Conn, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}

		reader := protocol.NewFrameReader(conn)
		writer := protocol.NewFrameWriter(conn)

		if _, err := reader.ReadFrame(); err != nil {
			_ = conn.Close()
			return
		}

		resp, _ := protocol.EncodeControl("handshake_ok", protocol.HandshakeOkMsg{
			Version:       protocol.Version,
			DaemonVersion: "dev",
		})
		_ = writer.WriteFrame(protocol.ChannelControl, resp)

		serverReady <- conn
	}()

	return socketPath, serverReady
}

func TestConnectFastClearsDeadline(t *testing.T) {
	socketPath, serverReady := startMockDaemon(t)

	c, err := ConnectFast(config.Paths{SocketPath: socketPath})
	if err != nil {
		t.Fatalf("ConnectFast: %v", err)
	}
	defer c.Close()

	serverConn := <-serverReady
	defer func() { _ = serverConn.Close() }()

	// Send a control message after 2.5s — past the original 2s handshake
	// deadline. If the deadline was not cleared, this read will fail.
	go func() {
		time.Sleep(2500 * time.Millisecond)

		resp, _ := protocol.EncodeControl("ping", struct{}{})
		serverWriter := protocol.NewFrameWriter(serverConn)
		_ = serverWriter.WriteFrame(protocol.ChannelControl, resp)
	}()

	env, err := c.ReadControlResponse()
	if err != nil {
		t.Fatalf("ReadControlResponse after deadline window: %v (deadline was not cleared)", err)
	}

	if env.Type != "ping" {
		t.Errorf("expected ping, got %s", env.Type)
	}
}

type deadlineRecordingConn struct {
	net.Conn

	mu        sync.Mutex
	deadlines []time.Time
}

func (c *deadlineRecordingConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadlines = append(c.deadlines, deadline)
	c.mu.Unlock()

	return c.Conn.SetDeadline(deadline)
}

func (c *deadlineRecordingConn) recordedDeadlines() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]time.Time(nil), c.deadlines...)
}

func approvalPipeHandshake(t *testing.T, server net.Conn) <-chan error {
	t.Helper()

	done := make(chan error, 1)

	go func() {
		reader := protocol.NewFrameReader(server)
		writer := protocol.NewFrameWriter(server)

		if _, err := reader.ReadFrame(); err != nil {
			done <- err
			return
		}

		resp, err := protocol.EncodeControl("handshake_ok", protocol.HandshakeOkMsg{
			Version: protocol.Version, DaemonVersion: "dev",
		})
		if err == nil {
			err = writer.WriteFrame(protocol.ChannelControl, resp)
		}

		done <- err
	}()

	return done
}

// TestConnectForApprovalRetainsOperationDeadline records every SetDeadline
// call and proves the handshake deadline is replaced by a later, nonzero
// backend+human operation deadline rather than cleared after handshake (#1251).
func TestConnectForApprovalRetainsOperationDeadline(t *testing.T) {
	saveConnectionTimeouts(t)

	clientConn, serverConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()

	recorded := &deadlineRecordingConn{Conn: clientConn}

	origDial := dialLocalDaemon
	dialLocalDaemon = func(string, string, time.Duration) (net.Conn, error) { return recorded, nil }

	t.Cleanup(func() { dialLocalDaemon = origDial })

	daemonHandshakeTimeout = 75 * time.Millisecond
	approvalResponseGrace = 200 * time.Millisecond
	handshakeDone := approvalPipeHandshake(t, serverConn)

	c, err := ConnectForApproval(config.Paths{}, 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := <-handshakeDone; err != nil {
		t.Fatal(err)
	}

	deadlines := recorded.recordedDeadlines()
	if len(deadlines) != 2 {
		t.Fatalf("SetDeadline calls = %d, want handshake + operation: %v", len(deadlines), deadlines)
	}

	if deadlines[0].IsZero() || deadlines[1].IsZero() {
		t.Fatalf("deadlines must both be nonzero: %v", deadlines)
	}

	if !deadlines[1].After(deadlines[0]) {
		t.Errorf("operation deadline %v is not after handshake deadline %v", deadlines[1], deadlines[0])
	}
}

// TestConnectForApprovalStalledAfterHandshakeTimesOut is the regression for a
// daemon that answers handshake_ok and then never starts/responds to the
// approval operation. The retained deadline must break the read (#1251/#244).
func TestConnectForApprovalStalledAfterHandshakeTimesOut(t *testing.T) {
	saveConnectionTimeouts(t)

	clientConn, serverConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()

	origDial := dialLocalDaemon
	dialLocalDaemon = func(string, string, time.Duration) (net.Conn, error) { return clientConn, nil }

	t.Cleanup(func() { dialLocalDaemon = origDial })

	daemonHandshakeTimeout = 200 * time.Millisecond
	approvalResponseGrace = 20 * time.Millisecond
	handshakeDone := approvalPipeHandshake(t, serverConn)

	c, err := ConnectForApproval(config.Paths{}, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := <-handshakeDone; err != nil {
		t.Fatal(err)
	}

	started := time.Now()

	_, err = c.ReadControlResponse()
	if err == nil {
		t.Fatal("stalled post-handshake read returned nil error")
	}

	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("stalled read error = %v, want timeout", err)
	}

	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("stalled read took %v, retained operation deadline was not effective", elapsed)
	}
}

func startMockDaemonWithVersion(t *testing.T, ver string) (string, chan net.Conn) {
	t.Helper()

	dir, err := os.MkdirTemp("", "gr")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socketPath := dir + "/s"

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = ln.Close() })

	serverReady := make(chan net.Conn, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}

		reader := protocol.NewFrameReader(conn)
		writer := protocol.NewFrameWriter(conn)

		if _, err := reader.ReadFrame(); err != nil {
			_ = conn.Close()
			return
		}

		resp, _ := protocol.EncodeControl("handshake_ok", protocol.HandshakeOkMsg{
			Version:       ver,
			DaemonVersion: "dev",
		})
		_ = writer.WriteFrame(protocol.ChannelControl, resp)

		serverReady <- conn
	}()

	return socketPath, serverReady
}

func TestConnectFastRejectsIncompatibleVersion(t *testing.T) {
	socketPath, _ := startMockDaemonWithVersion(t, "999.0")

	_, err := ConnectFast(config.Paths{SocketPath: socketPath})
	if err == nil {
		t.Fatal("expected error for incompatible protocol version")
	}

	if !strings.Contains(err.Error(), "protocol version mismatch") {
		t.Errorf("error = %q, want it to mention protocol version mismatch", err)
	}
}

func TestConnectForApprovalRejectsIncompatibleVersion(t *testing.T) {
	socketPath, _ := startMockDaemonWithVersion(t, "999.0")

	_, err := ConnectForApproval(config.Paths{SocketPath: socketPath}, 5*time.Minute)
	if err == nil {
		t.Fatal("expected error for incompatible protocol version")
	}

	if !strings.Contains(err.Error(), "protocol version mismatch") {
		t.Errorf("error = %q, want it to mention protocol version mismatch", err)
	}
}

func TestClose(t *testing.T) {
	c, serverConn := setupTestClient(t)

	c.Close()

	buf := make([]byte, 1)

	_, err := serverConn.Read(buf)
	if err == nil {
		t.Fatal("expected error reading from serverConn after client Close, got nil")
	}
}

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

	frame := transferFrame(t, func() error { return c.SendFrame(protocol.ChannelControl, payload) }, serverReader.ReadFrame)
	assertFrame(t, frame, protocol.ChannelControl, payload)
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
