package client

import (
	"encoding/json"
	"net"
	"os"
	"strings"
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

func TestSendData(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverReader := protocol.NewFrameReader(serverConn)

	payload := []byte("hello, raw data\x00\x01\x02")

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.SendData(payload)
	}()

	frame, err := serverReader.ReadFrame()
	if err != nil {
		t.Fatalf("server ReadFrame: %v", err)
	}

	if sendErr := <-errCh; sendErr != nil {
		t.Fatalf("SendData: %v", sendErr)
	}

	if frame.Channel != protocol.ChannelData {
		t.Errorf("channel = %d, want %d (ChannelData)", frame.Channel, protocol.ChannelData)
	}

	if string(frame.Payload) != string(payload) {
		t.Errorf("payload = %q, want %q", frame.Payload, payload)
	}
}

func TestReadFrame(t *testing.T) {
	c, serverConn := setupTestClient(t)
	serverWriter := protocol.NewFrameWriter(serverConn)

	want := []byte("frame-from-server")

	errCh := make(chan error, 1)
	go func() {
		errCh <- serverWriter.WriteFrame(protocol.ChannelData, want)
	}()

	frame, err := c.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}

	if writeErr := <-errCh; writeErr != nil {
		t.Fatalf("server WriteFrame: %v", writeErr)
	}

	if frame.Channel != protocol.ChannelData {
		t.Errorf("channel = %d, want %d", frame.Channel, protocol.ChannelData)
	}
	if string(frame.Payload) != string(want) {
		t.Errorf("payload = %q, want %q", frame.Payload, want)
	}
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

func TestApprovalDeadline(t *testing.T) {
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
			got := approvalDeadline(tt.timeout)
			if got != tt.want {
				t.Errorf("approvalDeadline(%v) = %v, want %v", tt.timeout, got, tt.want)
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
	t.Cleanup(func() { os.RemoveAll(dir) })
	socketPath := dir + "/s"
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	serverReady := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		reader := protocol.NewFrameReader(conn)
		writer := protocol.NewFrameWriter(conn)

		if _, err := reader.ReadFrame(); err != nil {
			conn.Close()
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
	defer serverConn.Close()

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

func TestConnectForApprovalClearsDeadline(t *testing.T) {
	socketPath, serverReady := startMockDaemon(t)

	c, err := ConnectForApproval(config.Paths{SocketPath: socketPath}, 5*time.Minute)
	if err != nil {
		t.Fatalf("ConnectForApproval: %v", err)
	}
	defer c.Close()

	serverConn := <-serverReady
	defer serverConn.Close()

	// Same check: send after the ConnectFast deadline (2s) would have fired.
	// ConnectForApproval uses a longer deadline, but if it wasn't cleared,
	// the connection would still have a fixed expiry.
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

func startMockDaemonWithVersion(t *testing.T, ver string) (string, chan net.Conn) {
	t.Helper()
	dir, err := os.MkdirTemp("", "gr")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socketPath := dir + "/s"
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	serverReady := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		reader := protocol.NewFrameReader(conn)
		writer := protocol.NewFrameWriter(conn)

		if _, err := reader.ReadFrame(); err != nil {
			conn.Close()
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
