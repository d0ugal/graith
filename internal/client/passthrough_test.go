package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

var testOpts = PassthroughOpts{
	Keys: PassthroughKeys{Prefix: 0x02, NextSession: 'n', PrevSession: 'p'},
}

type lockedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.buf.Write(p)
}

func (w *lockedWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([]byte(nil), w.buf.Bytes()...)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.buf.String()
}

func newTestClient(conn net.Conn) *Client {
	return &Client{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
	}
}

func TestKittyCtrlSeq(t *testing.T) {
	tests := []struct {
		prefix byte
		want   string
	}{
		{0x01, "\x1b[97;5u"},  // ctrl+a
		{0x02, "\x1b[98;5u"},  // ctrl+b
		{0x1a, "\x1b[122;5u"}, // ctrl+z
	}
	for _, tt := range tests {
		got := string(kittyCtrlSeq(tt.prefix))
		if got != tt.want {
			t.Errorf("kittyCtrlSeq(0x%02x) = %q, want %q", tt.prefix, got, tt.want)
		}
	}
}

func TestPrefixKeyOverlay(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	go func() {
		writer := protocol.NewFrameWriter(daemonConn)
		for {
			if err := writer.WriteFrame(protocol.ChannelData, []byte("output\n")); err != nil {
				return
			}

			time.Sleep(10 * time.Millisecond)
		}
	}()

	time.Sleep(50 * time.Millisecond)

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(50 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02}) // ctrl+b raw byte

		time.Sleep(20 * time.Millisecond)

		_, _ = stdinW.Write([]byte{'w'})
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != ResultOverlay {
		t.Fatalf("expected ResultOverlay (%d), got %d", ResultOverlay, result)
	}
}

func TestPrefixKeyOverlayKittyProtocol(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	go func() {
		writer := protocol.NewFrameWriter(daemonConn)
		for {
			if err := writer.WriteFrame(protocol.ChannelData, []byte("output\n")); err != nil {
				return
			}

			time.Sleep(10 * time.Millisecond)
		}
	}()

	time.Sleep(50 * time.Millisecond)

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(50 * time.Millisecond)
		// Kitty keyboard protocol: ESC[98;5u = ctrl+b

		_, _ = stdinW.Write([]byte("\x1b[98;5u"))

		time.Sleep(20 * time.Millisecond)

		_, _ = stdinW.Write([]byte{'w'})
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != ResultOverlay {
		t.Fatalf("expected ResultOverlay (%d), got %d", ResultOverlay, result)
	}
}

func TestPrefixKeyDetach(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

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
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(50 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 'd'}) // ctrl+b d in one read
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != ResultDetached {
		t.Fatalf("expected ResultDetached (%d), got %d", ResultDetached, result)
	}
}

func TestPrefixKeyDetachKittyProtocol(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

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
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(50 * time.Millisecond)
		// Kitty ctrl+b followed by 'd'

		_, _ = stdinW.Write(append([]byte("\x1b[98;5u"), 'd'))
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != ResultDetached {
		t.Fatalf("expected ResultDetached (%d), got %d", ResultDetached, result)
	}
}

func TestKittyReleaseEventConsumed(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

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
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(50 * time.Millisecond)
		// Kitty ctrl+b press, then release event, then raw 'd'

		_, _ = stdinW.Write([]byte("\x1b[98;5:1u"))

		time.Sleep(10 * time.Millisecond)

		_, _ = stdinW.Write([]byte("\x1b[98;5:3u"))

		time.Sleep(10 * time.Millisecond)

		_, _ = stdinW.Write([]byte{'d'})
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != ResultDetached {
		t.Fatalf("expected ResultDetached (%d), got %d", ResultDetached, result)
	}
}

func TestKittyEncodedFollowUpKey(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

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
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(50 * time.Millisecond)
		// Kitty ctrl+b press, then Kitty-encoded 'w' (codepoint 119, no modifier)

		_, _ = stdinW.Write([]byte("\x1b[98;5u"))

		time.Sleep(10 * time.Millisecond)

		_, _ = stdinW.Write([]byte("\x1b[119u"))
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != ResultOverlay {
		t.Fatalf("expected ResultOverlay (%d), got %d", ResultOverlay, result)
	}
}

func TestKittyReleaseBeforeFollowUpKey(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

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
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(50 * time.Millisecond)
		// Kitty ctrl+b press+release in one buffer, then Kitty 's' press

		_, _ = stdinW.Write(append([]byte("\x1b[98;5:1u"), []byte("\x1b[98;5:3u")...))

		time.Sleep(10 * time.Millisecond)

		_, _ = stdinW.Write([]byte("\x1b[115;1u"))
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != ResultShell {
		t.Fatalf("expected ResultShell (%d), got %d", ResultShell, result)
	}
}

func TestParseKittyCSIu(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantCP  int
		wantMod int
		wantEv  int
		wantLen int
		wantOK  bool
	}{
		{"basic", "\x1b[100u", 100, 1, 0, 6, true},
		{"with modifier", "\x1b[98;5u", 98, 5, 0, 7, true},
		{"press event", "\x1b[98;5:1u", 98, 5, 1, 9, true},
		{"release event", "\x1b[98;5:3u", 98, 5, 3, 9, true},
		{"repeat event", "\x1b[98;5:2u", 98, 5, 2, 9, true},
		{"no modifier explicit", "\x1b[119;1u", 119, 1, 0, 8, true},
		{"too short", "\x1b[u", 0, 0, 0, 0, false},
		{"not CSI", "\x1b[A", 0, 0, 0, 0, false},
		{"arrow key", "\x1b[1;5A", 0, 0, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp, mods, evType, seqLen, ok := parseKittyCSIu([]byte(tt.input), 0)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}

			if !ok {
				return
			}

			if cp != tt.wantCP || mods != tt.wantMod || evType != tt.wantEv || seqLen != tt.wantLen {
				t.Fatalf("got (%d, %d, %d, %d), want (%d, %d, %d, %d)",
					cp, mods, evType, seqLen, tt.wantCP, tt.wantMod, tt.wantEv, tt.wantLen)
			}
		})
	}
}

func TestProcessKittyPrefix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"press only", "\x1b[98;5u", "\x02"},
		{"press with event type", "\x1b[98;5:1u", "\x02"},
		{"release stripped", "\x1b[98;5:3u", ""},
		{"press then release", "\x1b[98;5u\x1b[98;5:3u", "\x02"},
		{"surrounded by data", "hello\x1b[98;5uworld", "hello\x02world"},
		{"non-ctrl same codepoint", "\x1b[98u", "\x1b[98u"},
		{"unrelated sequence", "\x1b[100;5u", "\x1b[100;5u"},
		{"no sequences", "plain text", "plain text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(processKittyPrefix([]byte(tt.input), 0x02))
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrefixKeyShell(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

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
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(50 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 's'})
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != ResultShell {
		t.Fatalf("expected ResultShell (%d), got %d", ResultShell, result)
	}
}

func TestDisconnectDetection(t *testing.T) {
	clientConn, daemonConn := net.Pipe()

	c := newTestClient(clientConn)

	stdinR, _ := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(50 * time.Millisecond)

		_ = daemonConn.Close()
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != ResultDisconnected {
		t.Fatalf("expected ResultDisconnected (%d), got %d", ResultDisconnected, result)
	}
}

func TestOverlayUnderHeavyOutput(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	go func() {
		writer := protocol.NewFrameWriter(daemonConn)

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

		_, _ = stdinW.Write([]byte{0x02})

		time.Sleep(10 * time.Millisecond)

		_, _ = stdinW.Write([]byte{'w'})
	}()

	ctx := context.Background()

	done := make(chan PassthroughResult, 1)
	go func() {
		done <- c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)
	}()

	select {
	case result := <-done:
		if result != ResultOverlay {
			t.Fatalf("expected ResultOverlay (%d), got %d", ResultOverlay, result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runPassthroughLoop did not return within 5s (deadlock)")
	}
}

func TestOverlayUnderHeavyOutputKittyProtocol(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	go func() {
		writer := protocol.NewFrameWriter(daemonConn)

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

		_, _ = stdinW.Write([]byte("\x1b[98;5u"))

		time.Sleep(10 * time.Millisecond)

		_, _ = stdinW.Write([]byte{'w'})
	}()

	ctx := context.Background()

	done := make(chan PassthroughResult, 1)
	go func() {
		done <- c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)
	}()

	select {
	case result := <-done:
		if result != ResultOverlay {
			t.Fatalf("expected ResultOverlay (%d), got %d", ResultOverlay, result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runPassthroughLoop did not return within 5s (deadlock)")
	}
}

func TestNormalDataPassthrough(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

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

	go func() {
		writer := protocol.NewFrameWriter(daemonConn)

		_ = writer.WriteFrame(protocol.ChannelData, []byte("hello"))
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte("abc"))

		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 'd'})
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != ResultDetached {
		t.Fatalf("expected ResultDetached, got %d", result)
	}

	select {
	case data := <-received:
		if string(data) != "abc" {
			t.Fatalf("expected 'abc' forwarded, got %q", data)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for forwarded data")
	}

	if !bytes.Contains(stdout.Bytes(), []byte("hello")) {
		t.Fatalf("expected 'hello' in stdout, got %q", stdout.String())
	}
}

func TestDaemonDetachesClient(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)
	writer := protocol.NewFrameWriter(daemonConn)

	go func() {
		_ = writer.WriteFrame(protocol.ChannelData, []byte("hello"))

		time.Sleep(50 * time.Millisecond)

		data, _ := protocol.EncodeControl("detached", struct{ Reason string }{"replaced"})

		_ = writer.WriteFrame(protocol.ChannelControl, data)
	}()

	stdinR, _ := io.Pipe()
	stdout := &lockedWriter{}

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != ResultDetached {
		t.Fatalf("expected ResultDetached (%d), got %d", ResultDetached, result)
	}
}

func TestEscapeSequenceNotPrefixIsForwarded(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

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

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(30 * time.Millisecond)
		// Arrow key escape sequence — should NOT be treated as prefix

		_, _ = stdinW.Write([]byte("\x1b[A"))

		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 'd'}) // then detach
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != ResultDetached {
		t.Fatalf("expected ResultDetached, got %d", result)
	}

	// Arrow key should have been forwarded as data
	select {
	case data := <-received:
		if string(data) != "\x1b[A" {
			t.Fatalf("expected arrow key forwarded, got %x", data)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for forwarded escape sequence")
	}
}
