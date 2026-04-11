package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/dougalmatthews/graith/internal/protocol"
)

func newTestClient(conn net.Conn) *Client {
	return &Client{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
	}
}

// fakeDaemon simulates the daemon side: continuously sends data frames
// and responds to control messages.
func fakeDaemon(t *testing.T, conn net.Conn) {
	t.Helper()
	reader := protocol.NewFrameReader(conn)
	writer := protocol.NewFrameWriter(conn)

	// Send data frames continuously until we get a control message
	stopData := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopData:
				return
			case <-ticker.C:
				writer.WriteFrame(protocol.ChannelData, []byte("output\n"))
			}
		}
	}()

	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			close(stopData)
			return
		}
		if frame.Channel == protocol.ChannelControl {
			msg, _ := protocol.DecodeControl(frame.Payload)
			switch msg.Type {
			case "detach":
				close(stopData)
				data, _ := protocol.EncodeControl("detached", struct{ Reason string }{"user"})
				writer.WriteFrame(protocol.ChannelControl, data)
				// Now respond to further control messages
				for {
					frame, err := reader.ReadFrame()
					if err != nil {
						return
					}
					if frame.Channel == protocol.ChannelControl {
						msg, _ := protocol.DecodeControl(frame.Payload)
						if msg.Type == "list" {
							data, _ := protocol.EncodeControl("session_list", struct{ Sessions []string }{[]string{}})
							writer.WriteFrame(protocol.ChannelControl, data)
						}
					}
				}
			}
		}
	}
}

func TestPrefixKeyOverlay(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer clientConn.Close()
	defer daemonConn.Close()

	c := newTestClient(clientConn)
	go fakeDaemon(t, daemonConn)

	// Give daemon time to start sending data
	time.Sleep(50 * time.Millisecond)

	// Simulate stdin: send ctrl+b then 'w'
	stdinR, stdinW := io.Pipe()
	stdout := &bytes.Buffer{}

	go func() {
		time.Sleep(50 * time.Millisecond)
		stdinW.Write([]byte{0x02}) // ctrl+b
		time.Sleep(20 * time.Millisecond)
		stdinW.Write([]byte{'w'}) // 'w'
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, 0x02, stdinR, stdout)

	if result != ResultOverlay {
		t.Fatalf("expected ResultOverlay (%d), got %d", ResultOverlay, result)
	}

	// THE CRITICAL CHECK: after passthrough returns, the connection must
	// be clean for control messages. This is what was broken before —
	// the leaked reader goroutine would consume frames meant for us.
	c.SendControl("detach", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		t.Fatalf("ReadControlResponse after overlay failed: %v", err)
	}
	if resp.Type != "detached" {
		t.Fatalf("expected detached response, got %q", resp.Type)
	}
}

func TestPrefixKeyDetach(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer clientConn.Close()
	defer daemonConn.Close()

	c := newTestClient(clientConn)

	// Simple daemon: just send some data
	go func() {
		writer := protocol.NewFrameWriter(daemonConn)
		for {
			if err := writer.WriteFrame(protocol.ChannelData, []byte("x")); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &bytes.Buffer{}

	go func() {
		time.Sleep(50 * time.Millisecond)
		stdinW.Write([]byte{0x02}) // ctrl+b
		time.Sleep(20 * time.Millisecond)
		stdinW.Write([]byte{'d'}) // 'd' = detach
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, 0x02, stdinR, stdout)

	if result != ResultDetached {
		t.Fatalf("expected ResultDetached (%d), got %d", ResultDetached, result)
	}
}

func TestPrefixKeyShell(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer clientConn.Close()
	defer daemonConn.Close()

	c := newTestClient(clientConn)

	go func() {
		writer := protocol.NewFrameWriter(daemonConn)
		for {
			if err := writer.WriteFrame(protocol.ChannelData, []byte("x")); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &bytes.Buffer{}

	go func() {
		time.Sleep(50 * time.Millisecond)
		stdinW.Write([]byte{0x02, 's'}) // ctrl+b + s in one read
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, 0x02, stdinR, stdout)

	if result != ResultShell {
		t.Fatalf("expected ResultShell (%d), got %d", ResultShell, result)
	}
}

func TestDisconnectDetection(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer clientConn.Close()

	c := newTestClient(clientConn)

	stdinR, _ := io.Pipe()
	stdout := &bytes.Buffer{}

	// Close daemon side to simulate disconnect
	go func() {
		time.Sleep(50 * time.Millisecond)
		daemonConn.Close()
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, 0x02, stdinR, stdout)

	if result != ResultDisconnected {
		t.Fatalf("expected ResultDisconnected (%d), got %d", ResultDisconnected, result)
	}
}

func TestOverlayUnderHeavyOutput(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer clientConn.Close()
	defer daemonConn.Close()

	c := newTestClient(clientConn)

	// Flood data frames as fast as possible — simulates a busy agent session
	writer := protocol.NewFrameWriter(daemonConn)
	floodDone := make(chan struct{})
	go func() {
		defer close(floodDone)
		chunk := bytes.Repeat([]byte("x"), 4096)
		for {
			if err := writer.WriteFrame(protocol.ChannelData, chunk); err != nil {
				return
			}
		}
	}()

	stdinR, stdinW := io.Pipe()
	stdout := io.Discard

	go func() {
		time.Sleep(100 * time.Millisecond)
		stdinW.Write([]byte{0x02})
		time.Sleep(10 * time.Millisecond)
		stdinW.Write([]byte{'w'})
	}()

	ctx := context.Background()
	done := make(chan PassthroughResult, 1)
	go func() {
		done <- c.runPassthroughLoop(ctx, 0x02, stdinR, stdout)
	}()

	select {
	case result := <-done:
		if result != ResultOverlay {
			t.Fatalf("expected ResultOverlay (%d), got %d", ResultOverlay, result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runPassthroughLoop did not return within 5s (deadlock in drain)")
	}

	// Verify connection is clean after overlay
	daemonConn.Close() // stop flood
	<-floodDone
}

func TestNormalDataPassthrough(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer clientConn.Close()
	defer daemonConn.Close()

	c := newTestClient(clientConn)

	// Collect data sent by client to daemon
	daemonReader := protocol.NewFrameReader(daemonConn)
	received := make(chan []byte, 10)
	go func() {
		for {
			frame, err := daemonReader.ReadFrame()
			if err != nil {
				return
			}
			if frame.Channel == protocol.ChannelData {
				received <- append([]byte{}, frame.Payload...)
			}
		}
	}()

	// Send data from daemon to client
	go func() {
		writer := protocol.NewFrameWriter(daemonConn)
		writer.WriteFrame(protocol.ChannelData, []byte("hello"))
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &bytes.Buffer{}

	go func() {
		// Type "abc" then ctrl+b d to detach
		time.Sleep(30 * time.Millisecond)
		stdinW.Write([]byte("abc"))
		time.Sleep(30 * time.Millisecond)
		stdinW.Write([]byte{0x02, 'd'})
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, 0x02, stdinR, stdout)

	if result != ResultDetached {
		t.Fatalf("expected ResultDetached, got %d", result)
	}

	// Verify "abc" was forwarded to daemon
	select {
	case data := <-received:
		if string(data) != "abc" {
			t.Fatalf("expected 'abc' forwarded, got %q", data)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for forwarded data")
	}

	// Verify daemon output reached stdout
	if !bytes.Contains(stdout.Bytes(), []byte("hello")) {
		t.Fatalf("expected 'hello' in stdout, got %q", stdout.String())
	}
}
