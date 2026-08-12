package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

var testOpts = PassthroughOpts{
	Keys: PassthroughKeys{
		Prefix:           0x02,
		Detach:           NewPassthroughKey('d'),
		SessionNavigator: NewPassthroughKey('w'),
		Shell:            NewPassthroughKey('s'),
		NextSession:      NewPassthroughKey('n'),
		PrevSession:      NewPassthroughKey('p'),
	},
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

	if result != ResultSessionNavigator {
		t.Fatalf("expected ResultSessionNavigator (%d), got %d", ResultSessionNavigator, result)
	}
}

func TestTerminalOwnedDataTriggersSnapshotRepaint(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	stdinR, stdinW := io.Pipe()
	defer func() { _ = stdinW.Close() }()

	stdout := &lockedWriter{}
	opts := testOpts
	opts.SessionID = "braw"
	opts.TerminalOwned = true
	terminalOutput := make(chan struct{}, 1)
	opts.OnTerminalOutput = func() {
		select {
		case terminalOutput <- struct{}{}:
		default:
		}
	}

	done := make(chan PassthroughResult, 1)
	go func() {
		done <- c.runPassthroughLoop(context.Background(), opts, stdinR, stdout, nil)
	}()

	daemonReader := protocol.NewFrameReader(daemonConn)
	daemonWriter := protocol.NewFrameWriter(daemonConn)

	if err := daemonWriter.WriteFrame(protocol.ChannelData, []byte("raw-never")); err != nil {
		t.Fatal(err)
	}

	frame, err := daemonReader.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}

	if frame.Channel != protocol.ChannelControl {
		t.Fatalf("client frame channel = %d, want control", frame.Channel)
	}

	env, err := protocol.DecodeControl(frame.Payload)
	if err != nil {
		t.Fatal(err)
	}

	if env.Type != "screen_snapshot" {
		t.Fatalf("client control = %q, want screen_snapshot", env.Type)
	}

	var req protocol.ScreenSnapshotMsg
	if err := protocol.DecodePayload(env, &req); err != nil {
		t.Fatal(err)
	}

	if req.SessionID != "braw" {
		t.Fatalf("snapshot request session = %q, want braw", req.SessionID)
	}

	select {
	case <-terminalOutput:
	default:
		t.Fatal("terminal-owned data did not notify terminal output callback")
	}

	resp, err := protocol.EncodeControl("screen_snapshot_response", protocol.ScreenSnapshotResponseMsg{
		SessionID:     "braw",
		Frame:         "screen-braw",
		CursorVisible: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelControl, resp); err != nil {
		t.Fatal(err)
	}

	detached, err := protocol.EncodeControl("detached", protocol.DetachedMsg{Reason: "user"})
	if err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelControl, detached); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-done:
		if result != ResultDetached {
			t.Fatalf("result = %d, want detached", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runPassthroughLoop did not return")
	}

	got := stdout.String()
	if !strings.Contains(got, "screen-braw") {
		t.Fatalf("stdout = %q, want rendered snapshot", got)
	}

	if strings.Contains(got, "raw-never") {
		t.Fatalf("terminal-owned mode wrote raw data to stdout: %q", got)
	}
}

func TestTerminalOwnedSnapshotRequestsUseDeltaBase(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	stdinR, stdinW := io.Pipe()
	defer func() { _ = stdinW.Close() }()

	opts := testOpts
	opts.SessionID = "braw"
	opts.TerminalOwned = true
	opts.terminalOwnedSnapshotID = 41

	done := make(chan PassthroughResult, 1)
	go func() {
		done <- c.runPassthroughLoop(context.Background(), opts, stdinR, io.Discard, nil)
	}()

	daemonReader := protocol.NewFrameReader(daemonConn)
	daemonWriter := protocol.NewFrameWriter(daemonConn)

	if err := daemonWriter.WriteFrame(protocol.ChannelData, []byte("first")); err != nil {
		t.Fatal(err)
	}

	req := readSnapshotRequest(t, daemonReader)
	if req.DeltaFrom != 41 {
		t.Fatalf("first delta_from = %d, want 41", req.DeltaFrom)
	}

	resp, err := protocol.EncodeControl("screen_snapshot_response", protocol.ScreenSnapshotResponseMsg{
		SessionID:     "braw",
		Delta:         true,
		SnapshotID:    42,
		CursorVisible: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelControl, resp); err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelData, []byte("second")); err != nil {
		t.Fatal(err)
	}

	req = readSnapshotRequest(t, daemonReader)
	if req.DeltaFrom != 42 {
		t.Fatalf("second delta_from = %d, want 42", req.DeltaFrom)
	}

	detached, err := protocol.EncodeControl("detached", protocol.DetachedMsg{Reason: "user"})
	if err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelControl, detached); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-done:
		if result != ResultDetached {
			t.Fatalf("result = %d, want detached", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runPassthroughLoop did not return")
	}
}

func TestTerminalOwnedSnapshotRefreshForcesFullResync(t *testing.T) {
	oldRefreshInterval := refreshInterval
	refreshInterval = 50 * time.Millisecond

	t.Cleanup(func() { refreshInterval = oldRefreshInterval })

	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	stdinR, stdinW := io.Pipe()
	defer func() { _ = stdinW.Close() }()

	opts := testOpts
	opts.SessionID = "braw"
	opts.TerminalOwned = true
	opts.terminalOwnedSnapshotID = 41

	done := make(chan PassthroughResult, 1)
	go func() {
		done <- c.runPassthroughLoop(context.Background(), opts, stdinR, io.Discard, nil)
	}()

	daemonReader := protocol.NewFrameReader(daemonConn)
	daemonWriter := protocol.NewFrameWriter(daemonConn)

	if err := daemonWriter.WriteFrame(protocol.ChannelData, []byte("first")); err != nil {
		t.Fatal(err)
	}

	req := readSnapshotRequest(t, daemonReader)
	if req.DeltaFrom != 41 {
		t.Fatalf("first delta_from = %d, want 41", req.DeltaFrom)
	}

	resp, err := protocol.EncodeControl("screen_snapshot_response", protocol.ScreenSnapshotResponseMsg{
		SessionID:     "braw",
		Delta:         true,
		SnapshotID:    42,
		CursorVisible: true,
		Cols:          80,
		Rows:          24,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelControl, resp); err != nil {
		t.Fatal(err)
	}

	req = readSnapshotRequest(t, daemonReader)
	if req.DeltaFrom != 0 {
		t.Fatalf("refresh delta_from = %d, want 0 for periodic full resync", req.DeltaFrom)
	}

	detached, err := protocol.EncodeControl("detached", protocol.DetachedMsg{Reason: "user"})
	if err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelControl, detached); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-done:
		if result != ResultDetached {
			t.Fatalf("result = %d, want detached", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runPassthroughLoop did not return")
	}
}

func TestTerminalOwnedSnapshotGeometryMismatchReassertsResize(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	stdinR, stdinW := io.Pipe()
	defer func() { _ = stdinW.Close() }()

	stdout := &lockedWriter{}
	viewport := &terminalOwnedAttachViewport{}
	viewport.update(80, 24)

	opts := testOpts
	opts.SessionID = "braw"
	opts.TerminalOwned = true
	opts.terminalOwnedSnapshotID = 41
	opts.terminalOwnedViewport = viewport

	done := make(chan PassthroughResult, 1)
	go func() {
		done <- c.runPassthroughLoop(context.Background(), opts, stdinR, stdout, nil)
	}()

	daemonReader := protocol.NewFrameReader(daemonConn)
	daemonWriter := protocol.NewFrameWriter(daemonConn)

	if err := daemonWriter.WriteFrame(protocol.ChannelData, []byte("first")); err != nil {
		t.Fatal(err)
	}

	req := readSnapshotRequest(t, daemonReader)
	if req.DeltaFrom != 41 {
		t.Fatalf("first delta_from = %d, want 41", req.DeltaFrom)
	}

	resp, err := protocol.EncodeControl("screen_snapshot_response", protocol.ScreenSnapshotResponseMsg{
		SessionID: "braw",
		Delta:     true,
		RowDeltas: []protocol.ScreenSnapshotRowMsg{
			{Y: 0, Frame: "wrong-geometry\x1b[0m"},
		},
		SnapshotID:    42,
		CursorVisible: true,
		Cols:          120,
		Rows:          40,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelControl, resp); err != nil {
		t.Fatal(err)
	}

	frame, err := daemonReader.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}

	env, err := protocol.DecodeControl(frame.Payload)
	if err != nil {
		t.Fatal(err)
	}

	if frame.Channel != protocol.ChannelControl || env.Type != "resize" {
		t.Fatalf("mismatch client frame = (%d, %q), want resize control", frame.Channel, env.Type)
	}

	var resize protocol.ResizeMsg
	if err := protocol.DecodePayload(env, &resize); err != nil {
		t.Fatal(err)
	}

	if resize.Cols != 80 || resize.Rows != 24 {
		t.Fatalf("mismatch resize = %+v, want 80x24", resize)
	}

	req = readSnapshotRequest(t, daemonReader)
	if req.DeltaFrom != 0 {
		t.Fatalf("mismatch follow-up delta_from = %d, want 0", req.DeltaFrom)
	}

	if got := stdout.String(); strings.Contains(got, "wrong-geometry") {
		t.Fatalf("mismatched snapshot was painted: %q", got)
	}

	detached, err := protocol.EncodeControl("detached", protocol.DetachedMsg{Reason: "user"})
	if err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelControl, detached); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-done:
		if result != ResultDetached {
			t.Fatalf("result = %d, want detached", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runPassthroughLoop did not return")
	}
}

func TestTerminalOwnedSnapshotRequestsCoalesceWhilePending(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	stdinR, stdinW := io.Pipe()
	defer func() { _ = stdinW.Close() }()

	opts := testOpts
	opts.SessionID = "canny"
	opts.TerminalOwned = true

	done := make(chan PassthroughResult, 1)
	go func() {
		done <- c.runPassthroughLoop(context.Background(), opts, stdinR, io.Discard, nil)
	}()

	daemonReader := protocol.NewFrameReader(daemonConn)
	daemonWriter := protocol.NewFrameWriter(daemonConn)

	if err := daemonWriter.WriteFrame(protocol.ChannelData, []byte("first")); err != nil {
		t.Fatal(err)
	}

	frame, err := daemonReader.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}

	env, err := protocol.DecodeControl(frame.Payload)
	if err != nil {
		t.Fatal(err)
	}

	if frame.Channel != protocol.ChannelControl || env.Type != "screen_snapshot" {
		t.Fatalf("first client frame = (%d, %q), want screen_snapshot control", frame.Channel, env.Type)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelData, []byte("second")); err != nil {
		t.Fatal(err)
	}

	if err := daemonConn.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	_, err = daemonReader.ReadFrame()
	if err == nil {
		t.Fatal("received a second snapshot request while the first was still pending")
	}

	var netErr net.Error

	ok := errors.As(err, &netErr)
	if !ok || !netErr.Timeout() {
		t.Fatalf("second read error = %v, want timeout", err)
	}

	if err := daemonConn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}

	resp, err := protocol.EncodeControl("screen_snapshot_response", protocol.ScreenSnapshotResponseMsg{
		SessionID:     "canny",
		Frame:         "screen-canny",
		CursorVisible: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelControl, resp); err != nil {
		t.Fatal(err)
	}

	if err := daemonConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	frame, err = daemonReader.ReadFrame()
	if err != nil {
		t.Fatalf("reading dirty follow-up snapshot request: %v", err)
	}

	env, err = protocol.DecodeControl(frame.Payload)
	if err != nil {
		t.Fatal(err)
	}

	if frame.Channel != protocol.ChannelControl || env.Type != "screen_snapshot" {
		t.Fatalf("follow-up client frame = (%d, %q), want screen_snapshot control", frame.Channel, env.Type)
	}

	if err := daemonConn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}

	detached, err := protocol.EncodeControl("detached", protocol.DetachedMsg{Reason: "user"})
	if err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelControl, detached); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-done:
		if result != ResultDetached {
			t.Fatalf("result = %d, want detached", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runPassthroughLoop did not return")
	}
}

func readSnapshotRequest(t *testing.T, reader *protocol.FrameReader) protocol.ScreenSnapshotMsg {
	t.Helper()

	frame, err := reader.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}

	if frame.Channel != protocol.ChannelControl {
		t.Fatalf("client frame channel = %d, want control", frame.Channel)
	}

	env, err := protocol.DecodeControl(frame.Payload)
	if err != nil {
		t.Fatal(err)
	}

	if env.Type != "screen_snapshot" {
		t.Fatalf("client control = %q, want screen_snapshot", env.Type)
	}

	var req protocol.ScreenSnapshotMsg
	if err := protocol.DecodePayload(env, &req); err != nil {
		t.Fatal(err)
	}

	return req
}

func TestTerminalOwnedStatusChromeRefreshes(t *testing.T) {
	oldRefreshInterval := refreshInterval
	refreshInterval = 200 * time.Millisecond

	t.Cleanup(func() { refreshInterval = oldRefreshInterval })

	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	stdinR, stdinW := io.Pipe()
	defer func() { _ = stdinW.Close() }()

	stdout := &lockedWriter{}
	opts := testOpts
	opts.SessionID = "canny"
	opts.TerminalOwned = true
	opts.terminalOwnedChrome = newTerminalOwnedAttachChrome(protocol.SessionInfo{
		ID:     "canny",
		Name:   "old-status",
		Agent:  "codex",
		Status: "running",
	}, false, "bottom", 24, 80)

	done := make(chan PassthroughResult, 1)
	go func() {
		done <- c.runPassthroughLoop(context.Background(), opts, stdinR, stdout, nil)
	}()

	daemonReader := protocol.NewFrameReader(daemonConn)
	daemonWriter := protocol.NewFrameWriter(daemonConn)

	frame, err := daemonReader.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}

	if frame.Channel != protocol.ChannelControl {
		t.Fatalf("client frame channel = %d, want control", frame.Channel)
	}

	env, err := protocol.DecodeControl(frame.Payload)
	if err != nil {
		t.Fatal(err)
	}

	if env.Type != "status" {
		t.Fatalf("client control = %q, want status", env.Type)
	}

	resp, err := protocol.EncodeControl("status_response", protocol.StatusResponseMsg{
		Session: protocol.SessionInfo{
			ID:     "canny",
			Name:   "fresh-status",
			Agent:  "codex",
			Status: "running",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelControl, resp); err != nil {
		t.Fatal(err)
	}

	detached, err := protocol.EncodeControl("detached", protocol.DetachedMsg{Reason: "user"})
	if err != nil {
		t.Fatal(err)
	}

	if err := daemonWriter.WriteFrame(protocol.ChannelControl, detached); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-done:
		if result != ResultDetached {
			t.Fatalf("result = %d, want detached", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runPassthroughLoop did not return")
	}

	if got := stdout.String(); !strings.Contains(got, "fresh-status") {
		t.Fatalf("stdout = %q, want refreshed experimental status", got)
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

	if result != ResultSessionNavigator {
		t.Fatalf("expected ResultSessionNavigator (%d), got %d", ResultSessionNavigator, result)
	}
}

// assertPrefixKeyResult runs the passthrough loop while the daemon streams data
// frames, injects a ctrl+b prefix immediately followed by key (in a single
// read), and asserts the loop exits with want.
func assertPrefixKeyResult(t *testing.T, key byte, want PassthroughResult) {
	t.Helper()

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

		_, _ = stdinW.Write([]byte{0x02, key}) // ctrl+b <key> in one read
	}()

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, testOpts, stdinR, stdout, nil)

	if result != want {
		t.Fatalf("expected result %d, got %d", want, result)
	}
}

func BenchmarkTerminalOwnedAttachFullRepaintFrameBytes(b *testing.B) {
	tests := map[string]struct {
		cols int
		rows int
	}{
		"80x24":  {cols: 80, rows: 24},
		"120x40": {cols: 120, rows: 40},
		"160x48": {cols: 160, rows: 48},
		"240x72": {cols: 240, rows: 72},
	}

	for name, test := range tests {
		b.Run(name, func(b *testing.B) {
			frame := repaintBenchmarkFrame(test.cols, test.rows)
			snap := protocol.ScreenSnapshotResponseMsg{
				SessionID:     "braw",
				Frame:         frame,
				CursorX:       test.cols - 1,
				CursorY:       test.rows - 1,
				CursorVisible: true,
				Cols:          test.cols,
				Rows:          test.rows,
			}

			payload, err := protocol.EncodeControl("screen_snapshot_response", snap)
			if err != nil {
				b.Fatal(err)
			}

			b.SetBytes(int64(len(payload) + 5))
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				if _, err := protocol.EncodeControl("screen_snapshot_response", snap); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportMetric(float64(len(payload)+5), "wire_B/op")
		})
	}
}

func repaintBenchmarkFrame(cols, rows int) string {
	row := strings.Repeat("x", cols)

	var buf strings.Builder
	buf.Grow((cols + 2) * rows)

	for i := 0; i < rows; i++ {
		if i > 0 {
			buf.WriteString("\r\n")
		}

		buf.WriteString(row)
	}

	buf.WriteString("\x1b[0m")

	return buf.String()
}

func TestPrefixKeyDetach(t *testing.T) {
	assertPrefixKeyResult(t, 'd', ResultDetached)
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

	if result != ResultSessionNavigator {
		t.Fatalf("expected ResultSessionNavigator (%d), got %d", ResultSessionNavigator, result)
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
		{"unknown event type unchanged", "\x1b[98;5:9u", "\x1b[98;5:9u"},
		{"printable prefix ignores kitty ctrl sequence", "\x1b[161;5u", "\x1b[161;5u"},
		{"no sequences", "plain text", "plain text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := byte(0x02)
			if tt.name == "printable prefix ignores kitty ctrl sequence" {
				prefix = 'A'
			}

			got := string(processKittyPrefix([]byte(tt.input), prefix))
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrefixKeyShell(t *testing.T) {
	assertPrefixKeyResult(t, 's', ResultShell)
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
		if result != ResultSessionNavigator {
			t.Fatalf("expected ResultSessionNavigator (%d), got %d", ResultSessionNavigator, result)
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
		if result != ResultSessionNavigator {
			t.Fatalf("expected ResultSessionNavigator (%d), got %d", ResultSessionNavigator, result)
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

// TestReadOnlyBlocksInput is the regression test for issue #31: in a read-only
// attach, typed bytes must never be forwarded to the daemon, while the daemon's
// output still streams to stdout.
func TestReadOnlyBlocksInput(t *testing.T) {
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

		_ = writer.WriteFrame(protocol.ChannelData, []byte("output"))
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(30 * time.Millisecond)

		// Typed bytes must be dropped in read-only mode.
		_, _ = stdinW.Write([]byte("please do not type this"))

		time.Sleep(30 * time.Millisecond)

		// The prefix key still works: ctrl+b d detaches.
		_, _ = stdinW.Write([]byte{0x02, 'd'})
	}()

	opts := testOpts
	opts.ReadOnly = true

	ctx := context.Background()
	result := c.runPassthroughLoop(ctx, opts, stdinR, stdout, nil)

	if result != ResultDetached {
		t.Fatalf("expected ResultDetached, got %d", result)
	}

	select {
	case data := <-received:
		t.Fatalf("read-only mode forwarded input to daemon: %q", data)
	case <-time.After(200 * time.Millisecond):
		// No data forwarded — correct.
	}

	if !bytes.Contains(stdout.Bytes(), []byte("output")) {
		t.Fatalf("expected daemon output in stdout, got %q", stdout.String())
	}
}

// TestReadOnlyBlocksDoublePrefixAndUnknownKey verifies that the two prefix
// paths that would otherwise inject bytes (a doubled prefix, and an unrecognised
// follow-up key) are also suppressed in read-only mode.
func TestReadOnlyBlocksDoublePrefixAndUnknownKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []byte
	}{
		{"double-prefix", []byte{0x02, 0x02}},
		{"unknown-key", []byte{0x02, 'Z'}},
	} {
		t.Run(tc.name, func(t *testing.T) {
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

				_, _ = stdinW.Write(tc.keys)

				time.Sleep(30 * time.Millisecond)

				_, _ = stdinW.Write([]byte{0x02, 'd'})
			}()

			opts := testOpts
			opts.ReadOnly = true

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			result := c.runPassthroughLoop(ctx, opts, stdinR, stdout, nil)
			if result != ResultDetached {
				t.Fatalf("expected ResultDetached, got %d", result)
			}

			select {
			case data := <-received:
				t.Fatalf("read-only mode forwarded input to daemon: %q", data)
			case <-time.After(150 * time.Millisecond):
			}
		})
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

// prefixKeyOpts mirrors the real key bindings so every prefix branch is
// reachable from a test.
var prefixKeyOpts = PassthroughOpts{
	Keys: PassthroughKeys{
		Prefix:              0x02, // ctrl+b
		Detach:              NewPassthroughKey('d'),
		SessionNavigator:    NewPassthroughKey('w'),
		Shell:               NewPassthroughKey('s'),
		NextSession:         NewPassthroughKey('n'),
		PrevSession:         NewPassthroughKey('p'),
		LastSession:         NewPassthroughKey('l'),
		NewSession:          NewPassthroughKey('c'),
		ForkSession:         NewPassthroughKey('f'),
		OrchestratorSession: NewPassthroughKey('o'),
		RenameSession:       NewPassthroughKey(','),
		ScrollMode:          NewPassthroughKey('['),
		Messages:            NewPassthroughKey('m'),
		RestartSession:      NewPassthroughKey('r'),
	},
}

func TestPassthroughActionBindingsCoverConfigOrder(t *testing.T) {
	want := map[string]passthroughActionBinding{
		"detach":               {key: NewPassthroughKey('d'), result: ResultDetached},
		"session_navigator":    {key: NewPassthroughKey('w'), result: ResultSessionNavigator},
		"messages":             {key: NewPassthroughKey('m'), result: ResultMessageOverlay},
		"shell":                {key: NewPassthroughKey('s'), result: ResultShell},
		"next_session":         {key: NewPassthroughKey('n'), result: ResultNextSession},
		"prev_session":         {key: NewPassthroughKey('p'), result: ResultPrevSession},
		"restart_session":      {key: NewPassthroughKey('r'), result: ResultRestart},
		"last_session":         {key: NewPassthroughKey('l'), result: ResultLastSession},
		"new_session":          {key: NewPassthroughKey('c'), result: ResultNewSession},
		"fork_session":         {key: NewPassthroughKey('f'), result: ResultForkSession},
		"orchestrator_session": {key: NewPassthroughKey('o'), result: ResultOrchestratorSession},
		"rename_session":       {key: NewPassthroughKey(','), result: ResultRenameSession},
		"scroll_mode":          {key: NewPassthroughKey('['), result: ResultScrollMode},
	}

	order := config.PassthroughKeybindingActionOrder()

	got := prefixKeyOpts.Keys.actionBindings()
	if len(got) != len(order) {
		t.Fatalf("actionBindings len = %d, want %d for %v", len(got), len(order), order)
	}

	for i, name := range order {
		binding, ok := want[name]
		if !ok {
			t.Fatalf("config action order contains untested action %q", name)
		}

		if got[i] != binding {
			t.Errorf("actionBindings[%d] for %s = %+v, want %+v", i, name, got[i], binding)
		}
	}
}

// runPrefixSequence feeds the raw prefix byte followed by the given key(s) into
// a passthrough loop and returns the resulting action. A background writer
// keeps the daemon side of the pipe drained so the loop stays alive until the
// key is processed.
func runPrefixSequence(t *testing.T, keys []byte) PassthroughResult {
	t.Helper()

	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	// Drain anything the client sends so SendData never blocks on net.Pipe.
	go func() {
		r := protocol.NewFrameReader(daemonConn)
		for {
			if _, err := r.ReadFrame(); err != nil {
				return
			}
		}
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02}) // prefix

		time.Sleep(10 * time.Millisecond)

		_, _ = stdinW.Write(keys)
	}()

	return c.runPassthroughLoop(context.Background(), prefixKeyOpts, stdinR, stdout, nil)
}

func TestPrefixKeyActions2(t *testing.T) {
	cases := []struct {
		name string
		key  byte
		want PassthroughResult
	}{
		{"messages", 'm', ResultMessageOverlay},
		{"restart", 'r', ResultRestart},
		{"next", 'n', ResultNextSession},
		{"prev", 'p', ResultPrevSession},
		{"last", 'l', ResultLastSession},
		{"new", 'c', ResultNewSession},
		{"fork", 'f', ResultForkSession},
		{"orchestrator", 'o', ResultOrchestratorSession},
		{"rename", ',', ResultRenameSession},
		{"scroll", '[', ResultScrollMode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runPrefixSequence(t, []byte{tc.key}); got != tc.want {
				t.Fatalf("prefix+%q = %d, want %d", tc.key, got, tc.want)
			}
		})
	}
}

// runPrefixSequenceWithOpts drives one prefix+key sequence through the loop with
// caller-supplied keybindings. A short context deadline guards against a hang
// when the key never maps to an action (old hardcoded behaviour): the loop then
// returns the default ResultQuit instead of blocking forever.
func runPrefixSequenceWithOpts(t *testing.T, opts PassthroughOpts, keys []byte) PassthroughResult {
	t.Helper()

	result, _ := runPrefixSequenceWithOptsAndData(t, opts, keys, nil)

	return result
}

func runPrefixSequenceWithOptsAndData(t *testing.T, opts PassthroughOpts, keys []byte, followup []byte) (PassthroughResult, [][]byte) {
	t.Helper()

	clientConn, daemonConn := net.Pipe()

	c := newTestClient(clientConn)
	received := make(chan []byte, 10)

	go func() {
		defer close(received)

		r := protocol.NewFrameReader(daemonConn)
		for {
			frame, err := r.ReadFrame()
			if err != nil {
				return
			}

			if frame.Channel == protocol.ChannelData {
				received <- append([]byte(nil), frame.Payload...)
			}
		}
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(30 * time.Millisecond)

		prefix := opts.Keys.Prefix
		if prefix == 0 {
			prefix = config.DefaultPrefixByte
		}

		_, _ = stdinW.Write([]byte{prefix})

		time.Sleep(10 * time.Millisecond)

		_, _ = stdinW.Write(keys)

		if len(followup) > 0 {
			time.Sleep(20 * time.Millisecond)

			_, _ = stdinW.Write(followup)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.runPassthroughLoop(ctx, opts, stdinR, stdout, nil)

	_ = daemonConn.Close()

	var frames [][]byte
	for frame := range received {
		frames = append(frames, frame)
	}

	return result, frames
}

// TestPrefixKeyConfigurable is the regression test for issue #918: the detach,
// session_navigator and shell prefix keys must honour the configured keybinding
// instead of the old hardcoded d/w/s literals. Rebinding them to q/z/v and
// pressing those keys must trigger the corresponding action.
func TestPrefixKeyConfigurable(t *testing.T) {
	opts := PassthroughOpts{Keys: PassthroughKeys{
		Prefix:           0x02,
		Detach:           NewPassthroughKey('q'),
		SessionNavigator: NewPassthroughKey('z'),
		Shell:            NewPassthroughKey('v'),
	}}

	cases := []struct {
		name string
		key  byte
		want PassthroughResult
	}{
		{"detach", 'q', ResultDetached},
		{"session_navigator", 'z', ResultSessionNavigator},
		{"shell", 'v', ResultShell},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runPrefixSequenceWithOpts(t, opts, []byte{tc.key}); got != tc.want {
				t.Fatalf("prefix+%q = %d, want %d", tc.key, got, tc.want)
			}
		})
	}
}

// TestPrefixKeyOldLiteralIgnoredAfterRemap confirms the previously-hardcoded
// literal no longer triggers detach once the key is rebound — pressing 'd' with
// detach mapped to 'q' forwards the byte and takes no action (ResultQuit via the
// context deadline).
func TestPrefixKeyOldLiteralIgnoredAfterRemap(t *testing.T) {
	opts := PassthroughOpts{Keys: PassthroughKeys{Prefix: 0x02, Detach: NewPassthroughKey('q')}}

	if got := runPrefixSequenceWithOpts(t, opts, []byte{'d'}); got == ResultDetached {
		t.Fatal("prefix+'d' should not detach when detach is remapped to 'q'")
	}
}

func TestPrefixKeyZeroBindingIsNotLiveAction(t *testing.T) {
	opts := PassthroughOpts{Keys: PassthroughKeys{
		Prefix: 0x02,
		Detach: NewPassthroughKey('d'),
	}}

	result, frames := runPrefixSequenceWithOptsAndData(t, opts, []byte{0}, []byte{0x02, 'd'})
	if result != ResultDetached {
		t.Fatalf("result = %d, want trailing detach", result)
	}

	if len(frames) == 0 {
		t.Fatal("prefix+NUL should be forwarded to the daemon, got no data frames")
	}

	if !bytes.Equal(frames[0], []byte{0x02, 0}) {
		t.Fatalf("prefix+NUL forwarded %v, want [0x02 0x00]", frames[0])
	}
}

func TestPrefixKeyZeroPrefixFallsBackToDefault(t *testing.T) {
	opts := PassthroughOpts{Keys: PassthroughKeys{
		Detach: NewPassthroughKey('d'),
	}}

	if got := runPrefixSequenceWithOpts(t, opts, []byte{'d'}); got != ResultDetached {
		t.Fatalf("zero prefix fallback result = %d, want %d", got, ResultDetached)
	}
}

func TestPrefixKeyCollisionExecutesRuntimeWinner(t *testing.T) {
	opts := PassthroughOpts{Keys: PassthroughKeys{
		Prefix:   0x02,
		Shell:    NewPassthroughKey('s'),
		Messages: NewPassthroughKey('s'),
	}}

	if got := runPrefixSequenceWithOpts(t, opts, []byte{'s'}); got != ResultMessageOverlay {
		t.Fatalf("prefix+s = %d, want messages to win over shell", got)
	}
}

func TestPrefixKeyConfiguredActionDoesNotForwardPTYBytes(t *testing.T) {
	opts := PassthroughOpts{Keys: PassthroughKeys{
		Prefix:   0x02,
		Messages: NewPassthroughKey('m'),
	}}

	result, frames := runPrefixSequenceWithOptsAndData(t, opts, []byte{'m'}, nil)
	if result != ResultMessageOverlay {
		t.Fatalf("prefix+m = %d, want message overlay", result)
	}

	if len(frames) != 0 {
		t.Fatalf("configured prefix action forwarded PTY data: %q", frames)
	}
}

// TestShowHelpBarReflectsConfiguredKeys checks the help bar renders the
// configured keys rather than fixed letters.
func TestShowHelpBarReflectsConfiguredKeys(t *testing.T) {
	var buf bytes.Buffer

	showHelpBar(&buf, PassthroughKeys{
		Detach:              NewPassthroughKey('Q'),
		SessionNavigator:    NewPassthroughKey('Z'),
		Shell:               NewPassthroughKey(' '),
		OrchestratorSession: NewPassthroughKey('O'),
		LastSession:         NewPassthroughKey('L'),
		NextSession:         NewPassthroughKey('N'),
		PrevSession:         NewPassthroughKey('P'),
		NewSession:          NewPassthroughKey('C'),
		ForkSession:         NewPassthroughKey('F'),
		RenameSession:       NewPassthroughKey('M'),
		ScrollMode:          NewPassthroughKey('B'),
		Messages:            NewPassthroughKey('G'),
		RestartSession:      NewPassthroughKey('R'),
	})

	got := buf.String()
	for _, want := range []string{"Q detach", "Z navigator", "space shell", "O orch", "L last", "N/P next/prev", "C new", "F fork", "M rename", "B scroll", "G messages", "R restart"} {
		if !strings.Contains(got, want) {
			t.Errorf("help bar missing %q; got %q", want, got)
		}
	}
}

// TestPrefixRenameScrollHonorConfiguredKeys is the regression test for #919:
// rename_session and scroll_mode must be driven by their configured key bytes,
// not hardcoded. It rebinds both to non-default keys and verifies the custom
// keys trigger the actions while the default keys (',' / '[') no longer do.
func TestPrefixRenameScrollHonorConfiguredKeys(t *testing.T) {
	opts := PassthroughOpts{
		Keys: PassthroughKeys{
			Prefix:        0x02,
			Detach:        NewPassthroughKey('d'), // detach is config-driven (#918); bind it so the trailing prefix+d ends the loop
			RenameSession: NewPassthroughKey('R'),
			ScrollMode:    NewPassthroughKey('S'),
		},
	}

	run := func(t *testing.T, key byte) PassthroughResult {
		t.Helper()

		clientConn, daemonConn := net.Pipe()
		defer func() { _ = daemonConn.Close() }()

		c := newTestClient(clientConn)

		go func() {
			r := protocol.NewFrameReader(daemonConn)
			for {
				if _, err := r.ReadFrame(); err != nil {
					return
				}
			}
		}()

		stdinR, stdinW := io.Pipe()
		stdout := &lockedWriter{}

		go func() {
			time.Sleep(30 * time.Millisecond)

			_, _ = stdinW.Write([]byte{0x02})

			time.Sleep(10 * time.Millisecond)

			_, _ = stdinW.Write([]byte{key})

			// A default-key case falls through to the agent, so follow up with
			// prefix+d to end the loop deterministically.
			time.Sleep(20 * time.Millisecond)

			_, _ = stdinW.Write([]byte{0x02, 'd'})
		}()

		return c.runPassthroughLoop(context.Background(), opts, stdinR, stdout, nil)
	}

	if got := run(t, 'R'); got != ResultRenameSession {
		t.Fatalf("prefix+R = %d, want ResultRenameSession (%d)", got, ResultRenameSession)
	}

	if got := run(t, 'S'); got != ResultScrollMode {
		t.Fatalf("prefix+S = %d, want ResultScrollMode (%d)", got, ResultScrollMode)
	}

	// The default keys are no longer bound, so they fall through and the loop
	// only ends on the trailing prefix+d.
	if got := run(t, ','); got != ResultDetached {
		t.Fatalf("prefix+, with rename rebound = %d, want ResultDetached (%d)", got, ResultDetached)
	}
}

// TestPrefixKeyDoublePrefixSendsRawByte verifies that prefix+prefix forwards a
// single raw prefix byte to the daemon (the escape hatch to send ctrl+b to the
// agent), and does not change the passthrough action.
func TestPrefixKeyDoublePrefixSendsRawByte2(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	got := make(chan []byte, 1)

	go func() {
		r := protocol.NewFrameReader(daemonConn)
		for {
			frame, err := r.ReadFrame()
			if err != nil {
				return
			}

			if frame.Channel == protocol.ChannelData && bytes.Contains(frame.Payload, []byte{0x02}) {
				select {
				case got <- frame.Payload:
				default:
				}
			}
		}
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 0x02}) // prefix, prefix → forwards raw prefix byte

		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 'd'}) // prefix+d detaches to end the loop
	}()

	res := c.runPassthroughLoop(context.Background(), prefixKeyOpts, stdinR, stdout, nil)
	if res != ResultDetached {
		t.Fatalf("expected detach after double-prefix then d, got %d", res)
	}

	select {
	case payload := <-got:
		if !bytes.Contains(payload, []byte{0x02}) {
			t.Errorf("expected raw prefix byte forwarded, got %v", payload)
		}
	case <-time.After(time.Second):
		t.Error("prefix byte was not forwarded to the daemon")
	}
}

// TestPrefixKeyUnknownForwardsBoth verifies that an unrecognized key after the
// prefix forwards both the prefix byte and the key to the daemon (the default
// case), rather than being swallowed.
func TestPrefixKeyUnknownForwardsBoth2(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	got := make(chan []byte, 4)

	go func() {
		r := protocol.NewFrameReader(daemonConn)
		for {
			frame, err := r.ReadFrame()
			if err != nil {
				return
			}

			if frame.Channel == protocol.ChannelData {
				select {
				case got <- append([]byte(nil), frame.Payload...):
				default:
				}
			}
		}
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 'Z'}) // prefix + unbound key

		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 'd'}) // detach to end
	}()

	res := c.runPassthroughLoop(context.Background(), prefixKeyOpts, stdinR, stdout, nil)
	if res != ResultDetached {
		t.Fatalf("expected detach, got %d", res)
	}

	found := false

	timeout := time.After(time.Second)

	for !found {
		select {
		case p := <-got:
			if bytes.Contains(p, []byte{0x02, 'Z'}) {
				found = true
			}
		case <-timeout:
			t.Fatal("did not observe prefix+Z forwarded to daemon")
		}
	}
}

func TestProcessKittyPrefixNonMatching2(t *testing.T) {
	// A CSI u sequence for a different key (codepoint 122 = 'z') must be left
	// untouched.
	in := []byte("\x1b[122;5u")
	if got := processKittyPrefix(in, 0x02); !bytes.Equal(got, in) {
		t.Errorf("non-matching sequence altered: %q", got)
	}

	// A matching press sequence for ctrl+b (98) is replaced by the raw byte.
	press := []byte("\x1b[98;5u")
	if got := processKittyPrefix(press, 0x02); !bytes.Equal(got, []byte{0x02}) {
		t.Errorf("matching press not replaced: %q", got)
	}

	// A matching release sequence (event type 3) is stripped entirely.
	release := []byte("\x1b[98;5:3u")
	if got := processKittyPrefix(release, 0x02); len(got) != 0 {
		t.Errorf("release should be stripped, got %q", got)
	}

	// Surrounding data is preserved around a replaced sequence.
	mixed := []byte("ab\x1b[98;5ucd")
	if got := processKittyPrefix(mixed, 0x02); !bytes.Equal(got, []byte("ab\x02cd")) {
		t.Errorf("mixed surrounding data wrong: %q", got)
	}

	// Input with no escape byte returns the original slice.
	plain := []byte("hello")
	if got := processKittyPrefix(plain, 0x02); !bytes.Equal(got, plain) {
		t.Errorf("plain input altered: %q", got)
	}
}

func TestParseKittyCSIuEdgeCases2(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"too short", "\x1b[", false},
		{"not escape", "X[98u", false},
		{"no digits", "\x1b[;u", false},
		{"missing u terminator", "\x1b[98;5", false},
		{"valid with modifiers", "\x1b[98;5u", true},
		{"valid with event type", "\x1b[98;5:3u", true},
		{"valid no modifiers", "\x1b[98u", true},
		{"bad modifier no digits", "\x1b[98;u", false},
		{"bad event no digits", "\x1b[98;5:u", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, ok := parseKittyCSIu([]byte(tc.in), 0)
			if ok != tc.ok {
				t.Errorf("parseKittyCSIu(%q) ok=%v, want %v", tc.in, ok, tc.ok)
			}
		})
	}
}

func TestKittyCtrlSeqOutOfRange2(t *testing.T) {
	if kittyCtrlSeq(0) != nil {
		t.Error("prefix 0 should yield nil (out of ctrl-letter range)")
	}

	if kittyCtrlSeq(27) != nil {
		t.Error("prefix 27 should yield nil (out of ctrl-letter range)")
	}
}

func TestSyncWriterConcurrent2(t *testing.T) {
	var buf bytes.Buffer

	sw := &syncWriter{w: &buf}

	done := make(chan struct{})

	for i := 0; i < 4; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				_, _ = sw.Write([]byte("x"))
			}

			done <- struct{}{}
		}()
	}

	for i := 0; i < 4; i++ {
		<-done
	}

	if buf.Len() != 200 {
		t.Fatalf("syncWriter lost writes under concurrency: got %d bytes, want 200", buf.Len())
	}
}
