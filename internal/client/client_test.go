package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
	"golang.org/x/sys/unix"
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

func TestDaemonPIDRejectsConnectionWithoutSocketDescriptor(t *testing.T) {
	clientConn, serverConn := net.Pipe()

	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	c := &Client{conn: clientConn}
	if _, err := c.DaemonPID(); err == nil {
		t.Fatal("DaemonPID() succeeded for an in-memory connection without Unix peer credentials")
	}
}

func TestDaemonPIDUsesUnixPeerCredentials(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}

	clientFile := os.NewFile(uintptr(fds[0]), "client.sock")
	serverFile := os.NewFile(uintptr(fds[1]), "server.sock")

	t.Cleanup(func() {
		_ = clientFile.Close()
		_ = serverFile.Close()
	})

	clientConn, err := net.FileConn(clientFile)
	if err != nil {
		t.Fatal(err)
	}

	serverConn, err := net.FileConn(serverFile)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	c := &Client{conn: clientConn}

	pid, err := c.DaemonPID()
	if err != nil {
		t.Fatalf("DaemonPID() error = %v", err)
	}

	if pid != os.Getpid() {
		t.Fatalf("DaemonPID() = %d, want socket peer PID %d", pid, os.Getpid())
	}
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
		Version:       "2.0",
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

func TestRequestUpgradeUsesExactPreflightBeforeMutation(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverReader := protocol.NewFrameReader(serverConn)
	serverWriter := protocol.NewFrameWriter(serverConn)
	errCh := make(chan error, 1)

	go func() {
		preflightFrame, err := serverReader.ReadFrame()
		if err != nil {
			errCh <- err
			return
		}

		preflight, err := protocol.DecodeControl(preflightFrame.Payload)
		if err != nil || preflight.Type != "upgrade_preflight" {
			errCh <- errors.New("first request was not upgrade_preflight")
			return
		}

		var preflightMsg protocol.UpgradeMsg
		if err := protocol.DecodePayload(preflight, &preflightMsg); err != nil {
			errCh <- err
			return
		}

		ok, _ := protocol.EncodeControl("upgrade_preflight_ok", struct{}{})
		if err := serverWriter.WriteFrame(protocol.ChannelControl, ok); err != nil {
			errCh <- err
			return
		}

		upgradeFrame, err := serverReader.ReadFrame()
		if err != nil {
			errCh <- err
			return
		}

		upgrade, err := protocol.DecodeControl(upgradeFrame.Payload)
		if err != nil || upgrade.Type != "upgrade" {
			errCh <- errors.New("second request was not upgrade")
			return
		}

		var upgradeMsg protocol.UpgradeMsg
		if err := protocol.DecodePayload(upgrade, &upgradeMsg); err != nil {
			errCh <- err
			return
		}

		if upgradeMsg != preflightMsg {
			errCh <- errors.New("upgrade request differed from preflight")
			return
		}

		ack, _ := protocol.EncodeControl("upgrading", struct{}{})
		errCh <- serverWriter.WriteFrame(protocol.ChannelControl, ack)
	}()

	requested, _, err := requestUpgradeWithGuard(context.Background(), c, allowDaemonLifecycleMutation)
	if err != nil {
		t.Fatal(err)
	}

	if !requested {
		t.Fatal("upgrade was acknowledged but not reported as requested")
	}

	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestRequestUpgradeNegotiationFloorCoversHealthyAdmission(t *testing.T) {
	c, serverConn := setupTestClient(t)
	originalHandshake := daemonHandshakeTimeout
	originalFloor := upgradeNegotiationFloor
	daemonHandshakeTimeout = 10 * time.Millisecond
	upgradeNegotiationFloor = 200 * time.Millisecond

	t.Cleanup(func() {
		daemonHandshakeTimeout = originalHandshake
		upgradeNegotiationFloor = originalFloor
	})

	serverReader := protocol.NewFrameReader(serverConn)
	serverWriter := protocol.NewFrameWriter(serverConn)
	errCh := make(chan error, 1)

	go func() {
		if _, err := serverReader.ReadFrame(); err != nil {
			errCh <- err
			return
		}

		time.Sleep(40 * time.Millisecond)

		preflight, _ := protocol.EncodeControl("upgrade_preflight_ok", struct{}{})
		if err := serverWriter.WriteFrame(protocol.ChannelControl, preflight); err != nil {
			errCh <- err
			return
		}

		if _, err := serverReader.ReadFrame(); err != nil {
			errCh <- err
			return
		}

		time.Sleep(40 * time.Millisecond)

		ack, _ := protocol.EncodeControl("upgrading", struct{}{})
		errCh <- serverWriter.WriteFrame(protocol.ChannelControl, ack)
	}()

	requested, _, err := requestUpgradeWithGuard(context.Background(), c, allowDaemonLifecycleMutation)
	if err != nil {
		t.Fatalf("healthy delayed upgrade negotiation failed: %v", err)
	}

	if !requested {
		t.Fatal("healthy delayed upgrade was not reported as requested")
	}

	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestRequestUpgradeRefusalNeverSendsMutatingRequest(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverReader := protocol.NewFrameReader(serverConn)
	serverWriter := protocol.NewFrameWriter(serverConn)
	seen := make(chan string, 2)

	go func() {
		frame, err := serverReader.ReadFrame()
		if err != nil {
			seen <- "read-error"
			return
		}

		env, _ := protocol.DecodeControl(frame.Payload)
		seen <- env.Type

		refusal, _ := protocol.EncodeControl("error", protocol.ErrorMsg{Message: "canny refusal"})
		_ = serverWriter.WriteFrame(protocol.ChannelControl, refusal)
		_ = serverConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))

		if frame, err := serverReader.ReadFrame(); err == nil {
			env, _ := protocol.DecodeControl(frame.Payload)
			seen <- env.Type
		}

		close(seen)
	}()

	requested, _, err := requestUpgradeWithGuard(context.Background(), c, allowDaemonLifecycleMutation)
	if err == nil {
		t.Fatal("preflight refusal was accepted")
	}

	if requested {
		t.Fatal("preflight refusal was reported as a requested upgrade")
	}

	var got []string
	for msgType := range seen {
		got = append(got, msgType)
	}

	if len(got) != 1 || got[0] != "upgrade_preflight" {
		t.Fatalf("requests after refusal = %v, want preflight only", got)
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

func TestConnectForPolicyRetainsEndToEndDeadline(t *testing.T) {
	socketPath, serverReady := startMockDaemon(t)

	c, err := ConnectForPolicy(config.Paths{SocketPath: socketPath}, 250*time.Millisecond)
	if err != nil {
		t.Fatalf("ConnectForPolicy: %v", err)
	}
	defer c.Close()

	serverConn := <-serverReady
	defer func() { _ = serverConn.Close() }()

	// Unlike long-lived attach/subscription clients, the policy connection must
	// retain an aggregate deadline so a daemon that handshakes and then stalls
	// can never strand the agent hook.
	time.Sleep(300 * time.Millisecond)

	if _, err := c.ReadControlResponse(); err == nil {
		t.Fatal("ReadControlResponse succeeded after the policy deadline")
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

func TestConnectForPolicyRejectsIncompatibleVersion(t *testing.T) {
	socketPath, _ := startMockDaemonWithVersion(t, "999.0")

	_, err := ConnectForPolicy(config.Paths{SocketPath: socketPath}, 5*time.Minute)
	if err == nil {
		t.Fatal("expected error for incompatible protocol version")
	}

	if !strings.Contains(err.Error(), "protocol version mismatch") {
		t.Errorf("error = %q, want it to mention protocol version mismatch", err)
	}
}

// captureDialTimeout replaces the dialLocalDaemon seam with a stub that records
// the timeout passed to it and answers with a scripted handshake_ok over an
// in-memory pipe. It returns a pointer to the captured timeout. The exchange is
// synchronous (net.Pipe), so the fast paths complete without sleeping on real
// network timing. Restored on cleanup.
func captureDialTimeout(t *testing.T) *time.Duration {
	t.Helper()

	orig := dialLocalDaemon

	t.Cleanup(func() { dialLocalDaemon = orig })

	var captured time.Duration

	dialLocalDaemon = func(_, _ string, timeout time.Duration) (net.Conn, error) {
		captured = timeout

		clientConn, serverConn := net.Pipe()

		go func() {
			defer func() { _ = serverConn.Close() }()

			reader := protocol.NewFrameReader(serverConn)
			writer := protocol.NewFrameWriter(serverConn)

			if _, err := reader.ReadFrame(); err != nil {
				return
			}

			resp, _ := protocol.EncodeControl("handshake_ok", protocol.HandshakeOkMsg{
				Version:       protocol.Version,
				DaemonVersion: "dev",
			})
			_ = writer.WriteFrame(protocol.ChannelControl, resp)
		}()

		return clientConn, nil
	}

	return &captured
}

// TestConnectFastUsesConfiguredDialTimeout proves the hook fast path dials with
// the configured [connection].dial_timeout rather than a hard-coded literal. It
// installs a deliberately non-default duration, so restoring the old 500ms
// literal would fail this test. See issue #1286.
func TestConnectFastUsesConfiguredDialTimeout(t *testing.T) {
	saveConnectionTimeouts(t)

	const braw = 321 * time.Millisecond

	daemonDialTimeout = braw

	captured := captureDialTimeout(t)

	c, err := ConnectFast(config.Paths{SocketPath: "/bothy/fast.sock"})
	if err != nil {
		t.Fatalf("ConnectFast: %v", err)
	}
	defer c.Close()

	if *captured != braw {
		t.Errorf("ConnectFast dial timeout = %v, want the configured %v", *captured, braw)
	}
}

// TestConnectForPolicyUsesConfiguredDialTimeout proves the policy fast path
// dials with the configured [connection].dial_timeout. The long policy
// deadline stays independent of the dial timeout. See issue #1286.
func TestConnectForPolicyUsesConfiguredDialTimeout(t *testing.T) {
	saveConnectionTimeouts(t)

	const canny = 274 * time.Millisecond

	daemonDialTimeout = canny

	captured := captureDialTimeout(t)

	c, err := ConnectForPolicy(config.Paths{SocketPath: "/bothy/policy.sock"}, 5*time.Minute)
	if err != nil {
		t.Fatalf("ConnectForPolicy: %v", err)
	}
	defer c.Close()

	if *captured != canny {
		t.Errorf("ConnectForPolicy dial timeout = %v, want the configured %v", *captured, canny)
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

func TestWriteScreenRestoreResetsTerminalStateBeforeFrame(t *testing.T) {
	snap := &protocol.ScreenSnapshotResponseMsg{
		Frame:         "braw\r\nbothy",
		CursorVisible: true,
	}

	out := captureStdout(t, func() { WriteScreenRestore(snap) })
	wantPrefix := "\x1b[?2026h\x1b[r\x1b[0m\x1b[?25l\x1b[H"

	if !strings.HasPrefix(out, wantPrefix) {
		t.Fatalf("screen restore prefix = %q, want %q", out, wantPrefix)
	}
}

func TestWriteExperimentalAttachSeed(t *testing.T) {
	var buf bytes.Buffer

	writeExperimentalAttachSeed(&buf, &protocol.ExperimentalAttachSeedMsg{
		Snapshot: protocol.ScreenSnapshotResponseMsg{
			Frame:         "hello bothy",
			CursorX:       4,
			CursorY:       2,
			CursorVisible: true,
		},
	})

	out := buf.String()
	if !strings.Contains(out, "\x1b[?2026h") || !strings.Contains(out, "\x1b[?2026l") {
		t.Fatalf("experimental seed should wrap repaint in synchronized update, got %q", out)
	}

	if !strings.Contains(out, "hello bothy") {
		t.Fatalf("experimental seed should contain the frame body, got %q", out)
	}

	if !strings.Contains(out, "\x1b[3;5H") {
		t.Fatalf("experimental seed should place cursor at row 3 col 5, got %q", out)
	}

	if !strings.Contains(out, "\x1b[?25h") {
		t.Fatalf("experimental seed should show a visible cursor, got %q", out)
	}
}

func TestExperimentalAttachRepaintResetsScrollRegionBeforeFrame(t *testing.T) {
	var buf bytes.Buffer

	writeExperimentalScreenSnapshot(&buf, &protocol.ScreenSnapshotResponseMsg{
		Frame:         "braw\r\nbothy",
		CursorVisible: true,
	})

	out := buf.String()
	wantPrefix := "\x1b[?2026h\x1b[r\x1b[0m\x1b[?25l\x1b[H\x1b[2J"

	if !strings.HasPrefix(out, wantPrefix) {
		t.Fatalf("experimental repaint prefix = %q, want %q", out, wantPrefix)
	}
}

func TestExperimentalAttachEnterResetsScrollRegionBeforeClear(t *testing.T) {
	out := experimentalAttachEnterSequence()
	wantPrefix := "\x1b[?1049h\x1b[r\x1b[0m\x1b[?25l\x1b[H\x1b[2J"

	if out != wantPrefix {
		t.Fatalf("experimental enter sequence = %q, want %q", out, wantPrefix)
	}
}

func TestWriteExperimentalScreenSnapshotIgnoresEmptyFrame(t *testing.T) {
	var buf bytes.Buffer

	writeExperimentalScreenSnapshot(&buf, &protocol.ScreenSnapshotResponseMsg{
		SessionID:     "canny",
		Frame:         "",
		CursorVisible: true,
	})

	if got := buf.String(); got != "" {
		t.Fatalf("empty experimental snapshot wrote %q, want no output", got)
	}
}

func TestWriteExperimentalScreenSnapshotDrawsReadOnlyChrome(t *testing.T) {
	var buf bytes.Buffer

	chrome := newExperimentalAttachChrome(protocol.SessionInfo{
		Name:  "canny-observer",
		Agent: "codex",
	}, true, "bottom", 24, 80)

	writeExperimentalScreenSnapshotWithChrome(&buf, &protocol.ScreenSnapshotResponseMsg{
		SessionID:     "canny",
		Frame:         "hello bothy",
		CursorVisible: true,
		Cols:          80,
		Rows:          23,
	}, chrome)

	out := buf.String()
	if !strings.Contains(out, "hello bothy") {
		t.Fatalf("experimental snapshot should contain frame body, got %q", out)
	}

	if !strings.Contains(out, "\x1b[24;1H") || !strings.Contains(out, "READ-ONLY") {
		t.Fatalf("experimental read-only chrome missing from output %q", out)
	}
}

func TestWriteExperimentalScreenSnapshotDrawsStatusChrome(t *testing.T) {
	var buf bytes.Buffer

	chrome := newExperimentalAttachChrome(protocol.SessionInfo{
		Name:        "canny-status",
		Agent:       "codex",
		Status:      "running",
		AgentStatus: "active",
	}, false, "bottom", 24, 80)

	writeExperimentalScreenSnapshotWithChrome(&buf, &protocol.ScreenSnapshotResponseMsg{
		SessionID:     "canny",
		Frame:         "hello bothy",
		CursorVisible: true,
		Cols:          80,
		Rows:          23,
	}, chrome)

	out := buf.String()
	if !strings.Contains(out, "\x1b[24;1H") || !strings.Contains(out, "canny-status") {
		t.Fatalf("experimental status chrome missing from output %q", out)
	}

	if strings.Contains(out, "READ-ONLY") {
		t.Fatalf("normal experimental status chrome rendered read-only indicator: %q", out)
	}
}

func TestWriteExperimentalScreenSnapshotShiftsFrameBelowTopChrome(t *testing.T) {
	var buf bytes.Buffer

	chrome := newExperimentalAttachChrome(protocol.SessionInfo{
		Name:   "top-braw",
		Agent:  "codex",
		Status: "running",
	}, false, "top", 24, 80)

	writeExperimentalScreenSnapshotWithChrome(&buf, &protocol.ScreenSnapshotResponseMsg{
		SessionID:     "canny",
		Frame:         "hello bothy",
		CursorX:       4,
		CursorY:       2,
		CursorVisible: true,
		Cols:          80,
		Rows:          23,
	}, chrome)

	out := buf.String()
	if !strings.Contains(out, "\x1b[1;1H") || !strings.Contains(out, "top-braw") {
		t.Fatalf("top experimental status chrome missing from output %q", out)
	}

	if !strings.Contains(out, "\x1b[2;1H") || !strings.Contains(out, "hello bothy") {
		t.Fatalf("child frame was not shifted below top chrome: %q", out)
	}

	if !strings.Contains(out, "\x1b[4;5H") {
		t.Fatalf("cursor was not shifted below top chrome: %q", out)
	}
}

func TestExperimentalStatusChromeRefreshRestoresChildCursor(t *testing.T) {
	tests := map[string]struct {
		position      string
		readOnly      bool
		cursorX       int
		cursorY       int
		cursorVisible bool
		refreshName   string
		wantStatusRow string
		wantStatus    string
		wantCursor    string
		wantShow      bool
	}{
		"bottom status bar": {
			position:      "bottom",
			cursorX:       6,
			cursorY:       4,
			cursorVisible: true,
			refreshName:   "braw-refresh",
			wantStatusRow: "\x1b[24;1H",
			wantStatus:    "braw-refresh",
			wantCursor:    "\x1b[5;7H",
			wantShow:      true,
		},
		"top status bar offsets child cursor": {
			position:      "top",
			cursorX:       4,
			cursorY:       2,
			cursorVisible: true,
			refreshName:   "canny-refresh",
			wantStatusRow: "\x1b[1;1H",
			wantStatus:    "canny-refresh",
			wantCursor:    "\x1b[4;5H",
			wantShow:      true,
		},
		"read-only status bar": {
			position:      "bottom",
			readOnly:      true,
			cursorX:       2,
			cursorY:       1,
			cursorVisible: true,
			refreshName:   "dreich-observer",
			wantStatusRow: "\x1b[24;1H",
			wantStatus:    "READ-ONLY",
			wantCursor:    "\x1b[2;3H",
			wantShow:      true,
		},
		"hidden child cursor": {
			position:      "bottom",
			cursorX:       9,
			cursorY:       7,
			cursorVisible: false,
			refreshName:   "thrawn-refresh",
			wantStatusRow: "\x1b[24;1H",
			wantStatus:    "thrawn-refresh",
			wantCursor:    "\x1b[8;10H",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			chrome := newExperimentalAttachChrome(protocol.SessionInfo{
				Name:   "dreich-seed",
				Agent:  "codex",
				Status: "running",
			}, test.readOnly, test.position, 24, 80)

			var seed bytes.Buffer
			writeExperimentalScreenSnapshotWithChrome(&seed, &protocol.ScreenSnapshotResponseMsg{
				SessionID:     "canny",
				Frame:         "hello bothy",
				CursorX:       test.cursorX,
				CursorY:       test.cursorY,
				CursorVisible: test.cursorVisible,
				Cols:          80,
				Rows:          23,
			}, chrome)

			chrome.updateInfo(newStatusBarInfo(protocol.SessionInfo{
				Name:        test.refreshName,
				Agent:       "codex",
				Status:      "running",
				AgentStatus: "active",
			}, 0, protocol.FleetSummary{}))

			var buf bytes.Buffer
			chrome.renderTo(&buf)

			out := buf.String()
			if !strings.Contains(out, test.wantStatusRow) || !strings.Contains(out, test.wantStatus) {
				t.Fatalf("status-only chrome refresh missing status row or text: %q", out)
			}

			statusAt := strings.LastIndex(out, test.wantStatusRow)
			cursorAt := strings.LastIndex(out, test.wantCursor)

			if cursorAt == -1 {
				t.Fatalf("status-only chrome refresh did not restore child cursor %q in %q", test.wantCursor, out)
			}

			if cursorAt < statusAt {
				t.Fatalf("status-only chrome refresh restored cursor before drawing chrome: %q", out)
			}

			if !strings.Contains(out[:statusAt], "\x1b[?25l") {
				t.Fatalf("status-only chrome refresh did not hide the cursor before drawing chrome: %q", out)
			}

			syncStartAt := strings.LastIndex(out, "\x1b[?2026h")
			syncEndAt := strings.LastIndex(out, "\x1b[?2026l")

			if syncStartAt == -1 || syncEndAt == -1 {
				t.Fatalf("status-only chrome refresh did not bracket chrome with synchronized update: %q", out)
			}

			if syncStartAt > statusAt || syncEndAt < cursorAt {
				t.Fatalf("synchronized update did not enclose chrome redraw and cursor restore: %q", out)
			}

			if strings.Count(out, "\x1b[?2026h") != strings.Count(out, "\x1b[?2026l") {
				t.Fatalf("status-only chrome refresh emitted unbalanced synchronized update markers: %q", out)
			}

			if test.wantShow && !strings.Contains(out[cursorAt:], "\x1b[?25h") {
				t.Fatalf("visible child cursor was not shown after restore: %q", out)
			}

			if !test.wantShow && strings.Contains(out, "\x1b[?25h") {
				t.Fatalf("hidden child cursor should remain hidden after status refresh: %q", out)
			}
		})
	}
}

func TestExperimentalStatusChromeRefreshBeforeSnapshotDoesNotTouchCursor(t *testing.T) {
	chrome := newExperimentalAttachChrome(protocol.SessionInfo{
		Name:   "blether-refresh",
		Agent:  "codex",
		Status: "running",
	}, false, "bottom", 24, 80)

	var buf bytes.Buffer
	chrome.renderTo(&buf)

	out := buf.String()
	if !strings.Contains(out, "\x1b[24;1H") || !strings.Contains(out, "blether-refresh") {
		t.Fatalf("pre-snapshot chrome refresh missing status row or text: %q", out)
	}

	if strings.Contains(out, "\x1b[?25l") || strings.Contains(out, "\x1b[?25h") {
		t.Fatalf("pre-snapshot chrome refresh should not change cursor visibility: %q", out)
	}

	if strings.Contains(out, "\x1b[?2026h") || strings.Contains(out, "\x1b[?2026l") {
		t.Fatalf("pre-snapshot chrome refresh should not emit synchronized update markers: %q", out)
	}

	if strings.Count(out, "H") > 1 {
		t.Fatalf("pre-snapshot chrome refresh should only position the chrome row: %q", out)
	}
}

func TestWriteExperimentalScreenSnapshotAppliesRowDeltas(t *testing.T) {
	var buf bytes.Buffer

	writeExperimentalScreenSnapshotWithChrome(&buf, &protocol.ScreenSnapshotResponseMsg{
		SessionID: "canny",
		Delta:     true,
		RowDeltas: []protocol.ScreenSnapshotRowMsg{
			{Y: -1, Frame: "dreich low\x1b[0m"},
			{Y: 1, Frame: "changed bothy\x1b[0m"},
			{Y: 24, Frame: "dreich high\x1b[0m"},
		},
		CursorX:       4,
		CursorY:       1,
		CursorVisible: true,
		Cols:          80,
		Rows:          24,
	}, nil)

	out := buf.String()
	if !strings.Contains(out, "\x1b[?2026h") || !strings.Contains(out, "\x1b[?2026l") {
		t.Fatalf("row delta should use synchronized update, got %q", out)
	}

	if strings.Contains(out, "\x1b[2J") {
		t.Fatalf("row delta should not clear the full screen, got %q", out)
	}

	if !strings.Contains(out, "\x1b[2;1H\x1b[0m\x1b[2Kchanged bothy") {
		t.Fatalf("row delta did not repaint addressed row: %q", out)
	}

	if strings.Contains(out, "dreich low") || strings.Contains(out, "dreich high") {
		t.Fatalf("row delta rendered out-of-bounds rows: %q", out)
	}

	if !strings.Contains(out, "\x1b[2;5H") || !strings.Contains(out, "\x1b[?25h") {
		t.Fatalf("row delta did not restore cursor: %q", out)
	}
}

func TestWriteExperimentalScreenSnapshotOffsetsRowDeltasBelowTopChrome(t *testing.T) {
	var buf bytes.Buffer

	chrome := newExperimentalAttachChrome(protocol.SessionInfo{
		Name:   "top-delta",
		Agent:  "codex",
		Status: "running",
	}, false, "top", 24, 80)

	writeExperimentalScreenSnapshotWithChrome(&buf, &protocol.ScreenSnapshotResponseMsg{
		SessionID: "canny",
		Delta:     true,
		RowDeltas: []protocol.ScreenSnapshotRowMsg{
			{Y: 0, Frame: "delta row\x1b[0m"},
		},
		CursorX:       2,
		CursorY:       0,
		CursorVisible: true,
		Cols:          80,
		Rows:          23,
	}, chrome)

	out := buf.String()
	if !strings.Contains(out, "\x1b[2;1H\x1b[0m\x1b[2Kdelta row") {
		t.Fatalf("top chrome did not offset child delta row: %q", out)
	}

	if !strings.Contains(out, "\x1b[1;1H") || !strings.Contains(out, "top-delta") {
		t.Fatalf("top chrome missing from row delta output: %q", out)
	}

	if !strings.Contains(out, "\x1b[2;3H") {
		t.Fatalf("top chrome did not offset cursor in row delta output: %q", out)
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

func TestFetchTopicMessagesOnReadsUntilDone(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverReader := protocol.NewFrameReader(serverConn)
	serverWriter := protocol.NewFrameWriter(serverConn)

	type result struct {
		messages []protocol.ConversationMessage
		err      error
	}

	resultCh := make(chan result, 1)

	go func() {
		messages, err := fetchTopicMessagesOn(c, "blether", "ben")
		resultCh <- result{messages: messages, err: err}
	}()

	frame, err := serverReader.ReadFrame()
	if err != nil {
		t.Fatalf("server read request: %v", err)
	}

	if frame.Channel != protocol.ChannelControl {
		t.Fatalf("request channel = %d, want control", frame.Channel)
	}

	env, err := protocol.DecodeControl(frame.Payload)
	if err != nil {
		t.Fatalf("decode request: %v", err)
	}

	if env.Type != "msg_sub" {
		t.Fatalf("request type = %q, want msg_sub", env.Type)
	}

	var sub protocol.MsgSubMsg
	if err := protocol.DecodePayload(env, &sub); err != nil {
		t.Fatalf("decode msg_sub: %v", err)
	}

	if sub.Stream != "blether" || sub.Subscriber != "ben" || sub.OnlyUnread || sub.Ack || sub.Wait || sub.Follow {
		t.Fatalf("msg_sub payload = %+v, want all messages from blether for ben without ack/wait/follow", sub)
	}

	msgBytes, err := protocol.EncodeControl("msg_message", protocol.ConversationMessage{
		ID:        "msg-braw",
		Stream:    "blether",
		SenderID:  "bairn",
		Body:      "canny work",
		CreatedAt: "2026-06-25T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("encode msg_message: %v", err)
	}

	doneBytes, err := protocol.EncodeControl("msg_done", struct{}{})
	if err != nil {
		t.Fatalf("encode msg_done: %v", err)
	}

	if err := serverWriter.WriteFrame(protocol.ChannelControl, msgBytes); err != nil {
		t.Fatalf("write msg_message: %v", err)
	}

	if err := serverWriter.WriteFrame(protocol.ChannelControl, doneBytes); err != nil {
		t.Fatalf("write msg_done: %v", err)
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("fetchTopicMessagesOn error: %v", result.err)
		}

		if len(result.messages) != 1 || result.messages[0].ID != "msg-braw" || result.messages[0].Stream != "blether" {
			t.Fatalf("messages = %+v, want one blether message", result.messages)
		}
	case <-time.After(time.Second):
		t.Fatal("fetchTopicMessagesOn did not return after msg_done")
	}
}

func TestFetchTopicsOnFiltersInboxStreams(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverReader := protocol.NewFrameReader(serverConn)
	serverWriter := protocol.NewFrameWriter(serverConn)

	resultCh := make(chan struct {
		topics []MsgTopicInfo
		err    error
	}, 1)

	go func() {
		topics, err := fetchTopicsOn(c, "ben")
		resultCh <- struct {
			topics []MsgTopicInfo
			err    error
		}{topics: topics, err: err}
	}()

	frame, err := serverReader.ReadFrame()
	if err != nil {
		t.Fatalf("server read request: %v", err)
	}

	env, err := protocol.DecodeControl(frame.Payload)
	if err != nil {
		t.Fatalf("decode request: %v", err)
	}

	if env.Type != "msg_topics" {
		t.Fatalf("request type = %q, want msg_topics", env.Type)
	}

	respBytes, err := protocol.EncodeControl("msg_topics_list", struct {
		Streams []MsgTopicInfo `json:"streams"`
	}{
		Streams: []MsgTopicInfo{
			{Name: "inbox:ben", Total: 2},
			{Name: "blether", Total: 3, Unread: 1},
		},
	})
	if err != nil {
		t.Fatalf("encode topics response: %v", err)
	}

	if err := serverWriter.WriteFrame(protocol.ChannelControl, respBytes); err != nil {
		t.Fatalf("write topics response: %v", err)
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("fetchTopicsOn error: %v", result.err)
		}

		if len(result.topics) != 1 || result.topics[0].Name != "blether" {
			t.Fatalf("topics = %+v, want only blether", result.topics)
		}
	case <-time.After(time.Second):
		t.Fatal("fetchTopicsOn did not return after msg_topics_list")
	}
}

func TestUpgradeMessageUsesResolvedManagedCandidate(t *testing.T) {
	originalResolver := resolveUpgradeCandidateForClient
	originalVersion := version.Version
	originalCommit := version.CommitSHA

	t.Cleanup(func() {
		resolveUpgradeCandidateForClient = originalResolver
		version.Version = originalVersion
		version.CommitSHA = originalCommit
	})

	version.Version = "2.0.0"
	version.CommitSHA = "canny"
	wantPath := "/bothy/Graith.app/Contents/MacOS/gr"

	resolveUpgradeCandidateForClient = func(_ context.Context, currentPath, gotVersion, gotCommit string, uid int) (string, bool, error) {
		if currentPath == "" || gotVersion != version.Version || gotCommit != version.CommitSHA || uid != os.Getuid() {
			t.Fatalf("resolver inputs = (%q, %q, %q, %d)", currentPath, gotVersion, gotCommit, uid)
		}

		return wantPath, true, nil
	}

	msg, managed, err := upgradeMessageForClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if msg.ExecPath != wantPath || msg.ClientVersion != version.Version || !managed {
		t.Fatalf("upgrade message = %#v, managed = %t", msg, managed)
	}
}

func TestRequestUpgradeReportsDaemonOutcomes(t *testing.T) {
	originalResolver := resolveUpgradeCandidateForClient

	t.Cleanup(func() { resolveUpgradeCandidateForClient = originalResolver })

	const wantPath = "/bothy/Graith.app/Contents/MacOS/gr"

	resolveUpgradeCandidateForClient = func(context.Context, string, string, string, int) (string, bool, error) {
		return wantPath, true, nil
	}

	tests := []struct {
		name          string
		responseType  string
		response      any
		drop          bool
		wantRequested bool
		wantError     string
	}{
		{name: "accepted", responseType: "upgrading", response: struct{}{}, wantRequested: true},
		{name: "connection drop after exec", drop: true, wantRequested: true},
		{name: "rejected", responseType: "error", response: protocol.ErrorMsg{Message: "thrawn candidate"}, wantError: "thrawn candidate"},
		{name: "empty rejection", responseType: "error", response: protocol.ErrorMsg{}, wantError: "daemon rejected exec upgrade"},
		{name: "unexpected response", responseType: "blether", response: struct{}{}, wantError: `unexpected daemon upgrade response "blether"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, serverConn := setupTestClient(t)
			serverErr := make(chan error, 1)

			go func() {
				reader := protocol.NewFrameReader(serverConn)

				preflightFrame, err := reader.ReadFrame()
				if err != nil {
					serverErr <- err

					return
				}

				preflight, err := protocol.DecodeControl(preflightFrame.Payload)
				if err != nil {
					serverErr <- err

					return
				}

				if preflight.Type != "upgrade_preflight" {
					serverErr <- errors.New("client sent a non-preflight request")

					return
				}

				var preflightMsg protocol.UpgradeMsg
				if err := protocol.DecodePayload(preflight, &preflightMsg); err != nil {
					serverErr <- err

					return
				}

				if preflightMsg.ExecPath != wantPath || preflightMsg.ClientVersion != version.Version {
					serverErr <- errors.New("client sent the wrong preflight identity")

					return
				}

				preflightOK, err := protocol.EncodeControl("upgrade_preflight_ok", struct{}{})
				if err == nil {
					err = protocol.NewFrameWriter(serverConn).WriteFrame(protocol.ChannelControl, preflightOK)
				}

				if err != nil {
					serverErr <- err

					return
				}

				frame, err := reader.ReadFrame()
				if err != nil {
					serverErr <- err

					return
				}

				envelope, err := protocol.DecodeControl(frame.Payload)
				if err != nil {
					serverErr <- err

					return
				}

				if envelope.Type != "upgrade" {
					serverErr <- errors.New("client sent a non-upgrade request")

					return
				}

				var msg protocol.UpgradeMsg
				if err := protocol.DecodePayload(envelope, &msg); err != nil {
					serverErr <- err

					return
				}

				if msg.ExecPath != wantPath || msg.ClientVersion != version.Version {
					serverErr <- errors.New("client sent the wrong upgrade identity")

					return
				}

				if msg != preflightMsg {
					serverErr <- errors.New("upgrade request differed from preflight")

					return
				}

				if test.drop {
					serverErr <- serverConn.Close()

					return
				}

				data, err := protocol.EncodeControl(test.responseType, test.response)
				if err == nil {
					err = protocol.NewFrameWriter(serverConn).WriteFrame(protocol.ChannelControl, data)
				}

				serverErr <- err
			}()

			requested, managed, err := requestUpgradeWithGuard(context.Background(), c, allowDaemonLifecycleMutation)
			if requested != test.wantRequested || !managed {
				t.Fatalf("requestUpgrade() = (requested %t, managed %t), want (%t, true)", requested, managed, test.wantRequested)
			}

			if test.wantError == "" && err != nil {
				t.Fatalf("requestUpgrade() error = %v", err)
			}

			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("requestUpgrade() error = %v, want %q", err, test.wantError)
			}

			if err := <-serverErr; err != nil {
				t.Fatalf("daemon side: %v", err)
			}
		})
	}
}

func TestRequestUpgradeRejectsCandidateResolutionFailure(t *testing.T) {
	originalResolver := resolveUpgradeCandidateForClient

	t.Cleanup(func() { resolveUpgradeCandidateForClient = originalResolver })

	resolveUpgradeCandidateForClient = func(context.Context, string, string, string, int) (string, bool, error) {
		return "", true, errors.New("dreich cache")
	}

	c, _ := setupTestClient(t)

	requested, managed, err := requestUpgradeWithGuard(context.Background(), c, allowDaemonLifecycleMutation)
	if requested || managed || err == nil || !strings.Contains(err.Error(), "dreich cache") {
		t.Fatalf("requestUpgrade() = (%t, %t, %v)", requested, managed, err)
	}
}

func TestConnectExistingHandshakeLifecycle(t *testing.T) {
	originalDial := dialLocalDaemon

	t.Cleanup(func() { dialLocalDaemon = originalDial })

	tests := []struct {
		name         string
		responseType string
		drop         bool
		wantError    string
	}{
		{name: "connected", responseType: "handshake_ok"},
		{name: "rejected", responseType: "error", wantError: "handshake rejected"},
		{name: "daemon drops handshake", drop: true, wantError: "EOF"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()

			t.Cleanup(func() {
				_ = clientConn.Close()
				_ = serverConn.Close()
			})

			dialLocalDaemon = func(network, address string, timeout time.Duration) (net.Conn, error) {
				if network != "unix" || address != "/bothy/daemon.sock" || timeout != daemonDialTimeout {
					t.Fatalf("dial = (%q, %q, %v)", network, address, timeout)
				}

				return clientConn, nil
			}

			serverErr := make(chan error, 1)

			go func() {
				frame, err := protocol.NewFrameReader(serverConn).ReadFrame()
				if err != nil {
					serverErr <- err

					return
				}

				envelope, err := protocol.DecodeControl(frame.Payload)
				if err != nil || envelope.Type != "handshake" {
					if err == nil {
						err = errors.New("client sent a non-handshake request")
					}

					serverErr <- err

					return
				}

				if test.drop {
					serverErr <- serverConn.Close()

					return
				}

				data, err := protocol.EncodeControl(test.responseType, struct{}{})
				if err == nil {
					err = protocol.NewFrameWriter(serverConn).WriteFrame(protocol.ChannelControl, data)
				}

				serverErr <- err
			}()

			c, err := ConnectExisting(config.Default(), config.Paths{SocketPath: "/bothy/daemon.sock"})
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("ConnectExisting() error = %v", err)
				}

				if c == nil {
					t.Fatal("ConnectExisting() returned a nil client")
				}

				c.Close()
			} else if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ConnectExisting() error = %v, want %q", err, test.wantError)
			}

			if err := <-serverErr; err != nil {
				t.Fatalf("daemon side: %v", err)
			}
		})
	}

	dialLocalDaemon = func(string, string, time.Duration) (net.Conn, error) {
		return nil, errors.New("canny socket")
	}

	if _, err := ConnectExisting(config.Default(), config.Paths{SocketPath: "/bothy/daemon.sock"}); err == nil || !strings.Contains(err.Error(), "canny socket") {
		t.Fatalf("dial error = %v", err)
	}
}
