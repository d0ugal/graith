package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestTerminalOwnedSnapshotThrottleLatencyBudget(t *testing.T) {
	t.Parallel()

	const maxSnapshotInterval = 50 * time.Millisecond

	if terminalOwnedSnapshotMinInterval > maxSnapshotInterval {
		t.Fatalf("terminal-owned snapshot throttle interval = %s, want <= %s", terminalOwnedSnapshotMinInterval, maxSnapshotInterval)
	}
}

func BenchmarkPassthroughInputForwardingLatency(b *testing.B) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)
	daemonReader := protocol.NewFrameReader(daemonConn)
	stdinR, stdinW := io.Pipe()
	stdout := io.Discard

	done := make(chan PassthroughResult, 1)
	go func() {
		done <- c.runPassthroughLoop(context.Background(), testOpts, stdinR, stdout, nil)
	}()

	payload := []byte("x")

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := stdinW.Write(payload); err != nil {
			b.Fatal(err)
		}

		readDaemonDataFrame(b, daemonReader, payload)
	}

	b.StopTimer()
	stopPassthroughBenchmark(b, stdinW, done)
}

func BenchmarkPassthroughRawEchoLatency(b *testing.B) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)
	daemonReader := protocol.NewFrameReader(daemonConn)
	daemonWriter := protocol.NewFrameWriter(daemonConn)
	stdinR, stdinW := io.Pipe()
	stdout := &latencyAckWriter{markerPrefix: []byte("x"), ch: make(chan []byte, 1)}

	done := make(chan PassthroughResult, 1)
	go func() {
		done <- c.runPassthroughLoop(context.Background(), testOpts, stdinR, stdout, nil)
	}()

	serverDone := echoDaemonDataFrames(daemonReader, daemonWriter)
	payload := []byte("x")

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := stdinW.Write(payload); err != nil {
			b.Fatal(err)
		}

		waitForLatencyMarker(b, stdout.ch, []byte("x"), "raw echo")
	}

	b.StopTimer()
	stopPassthroughBenchmark(b, stdinW, done)

	_ = daemonConn.Close()

	<-serverDone
}

func BenchmarkPassthroughTerminalOwnedEchoLatency(b *testing.B) {
	tests := map[string]struct {
		isolated bool
	}{
		"continuous": {},
		"isolated":   {isolated: true},
	}

	for name, test := range tests {
		b.Run(name, func(b *testing.B) {
			benchmarkPassthroughTerminalOwnedEchoLatency(b, test.isolated)
		})
	}
}

func benchmarkPassthroughTerminalOwnedEchoLatency(b *testing.B, isolated bool) {
	oldRefreshInterval := refreshInterval

	refreshInterval = time.Hour

	defer func() { refreshInterval = oldRefreshInterval }()

	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)
	daemonReader := protocol.NewFrameReader(daemonConn)
	daemonWriter := protocol.NewFrameWriter(daemonConn)
	stdinR, stdinW := io.Pipe()
	stdout := &latencyAckWriter{markerPrefix: []byte("latency-marker-"), ch: make(chan []byte, 16)}

	opts := testOpts
	opts.SessionID = "braw-latency"
	opts.TerminalOwned = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan PassthroughResult, 1)
	go func() {
		done <- c.runPassthroughLoop(ctx, opts, stdinR, stdout, nil)
	}()

	serverDone := serveTerminalOwnedEchoSnapshots(daemonReader, daemonWriter)
	payloads, markers := terminalOwnedLatencySamples(b.N)
	measuredEcho := time.Duration(0)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if isolated && i > 0 {
			time.Sleep(terminalOwnedSnapshotMinInterval + time.Millisecond)
		}

		start := time.Now()

		if _, err := stdinW.Write(payloads[i]); err != nil {
			b.Fatal(err)
		}

		waitForLatencyMarker(b, stdout.ch, markers[i], "terminal-owned echo repaint")

		measuredEcho += time.Since(start)
	}

	b.StopTimer()
	b.ReportMetric(float64(measuredEcho.Nanoseconds())/float64(b.N), "echo_ns/op")

	cancel()

	_ = stdinW.Close()
	_ = daemonConn.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		b.Fatal("passthrough loop did not stop")
	}

	<-serverDone
}

func terminalOwnedLatencySamples(n int) ([][]byte, [][]byte) {
	payloads := make([][]byte, n)
	markers := make([][]byte, n)

	for i := 0; i < n; i++ {
		payload := strconv.AppendInt([]byte("sample-"), int64(i), 10)
		marker := append([]byte("latency-marker-"), payload...)

		payloads[i] = payload
		markers[i] = marker
	}

	return payloads, markers
}

func readDaemonDataFrame(b *testing.B, reader *protocol.FrameReader, payload []byte) {
	b.Helper()

	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			b.Fatal(err)
		}

		if frame.Channel == protocol.ChannelData && bytes.Equal(frame.Payload, payload) {
			return
		}
	}
}

func echoDaemonDataFrames(reader *protocol.FrameReader, writer *protocol.FrameWriter) <-chan struct{} {
	done := make(chan struct{})

	go func() {
		defer close(done)

		for {
			frame, err := reader.ReadFrame()
			if err != nil {
				return
			}

			if frame.Channel == protocol.ChannelData {
				_ = writer.WriteFrame(protocol.ChannelData, frame.Payload)
			}
		}
	}()

	return done
}

func serveTerminalOwnedEchoSnapshots(reader *protocol.FrameReader, writer *protocol.FrameWriter) <-chan struct{} {
	done := make(chan struct{})

	go func() {
		defer close(done)

		var pendingMarkers [][]byte

		for {
			frame, err := reader.ReadFrame()
			if err != nil {
				return
			}

			switch frame.Channel {
			case protocol.ChannelData:
				// Terminal-owned attach writes coalesced output hints instead
				// of raw PTY bytes; the payload lets this benchmark associate
				// the following snapshot repaint with the current sample.
				marker := append([]byte("latency-marker-"), frame.Payload...)
				pendingMarkers = append(pendingMarkers, marker)
				_ = writer.WriteFrame(protocol.ChannelData, nil)
			case protocol.ChannelControl:
				if isScreenSnapshotRequest(frame.Payload) {
					marker := []byte("refresh-marker")
					if len(pendingMarkers) > 0 {
						marker = pendingMarkers[0]
						pendingMarkers = pendingMarkers[1:]
					}

					writeLatencySnapshot(writer, marker)
				}
			}
		}
	}()

	return done
}

func isScreenSnapshotRequest(payload []byte) bool {
	env, err := protocol.DecodeControl(payload)
	if err != nil {
		return false
	}

	return env.Type == "screen_snapshot"
}

func writeLatencySnapshot(writer *protocol.FrameWriter, marker []byte) {
	resp, err := protocol.EncodeControl("screen_snapshot_response", protocol.ScreenSnapshotResponseMsg{
		SessionID:     "braw-latency",
		Frame:         string(marker),
		CursorVisible: true,
		Cols:          80,
		Rows:          24,
	})
	if err != nil {
		return
	}

	_ = writer.WriteFrame(protocol.ChannelControl, resp)
}

func waitForLatencyMarker(b *testing.B, ch <-chan []byte, marker []byte, label string) {
	b.Helper()

	deadline := time.After(time.Second)

	for {
		select {
		case payload := <-ch:
			if bytes.Contains(payload, marker) {
				return
			}
		case <-deadline:
			b.Fatalf("timed out waiting for %s", label)
		}
	}
}

func stopPassthroughBenchmark(b *testing.B, stdinW *io.PipeWriter, done <-chan PassthroughResult) {
	b.Helper()

	_, _ = stdinW.Write([]byte{0x02, 'd'})

	select {
	case <-done:
	case <-time.After(time.Second):
		b.Fatal("passthrough loop did not stop")
	}
}

type latencyAckWriter struct {
	markerPrefix []byte
	ch           chan []byte
}

func (w *latencyAckWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, w.markerPrefix) {
		payload := append([]byte(nil), p...)

		select {
		case w.ch <- payload:
		default:
		}
	}

	return len(p), nil
}
