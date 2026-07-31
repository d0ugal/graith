package daemon

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
)

type daemonInputLatencySample struct {
	total time.Duration
	hint  time.Duration
}

const daemonLatencyMaxVisibleP95 = 75 * time.Millisecond

func TestTerminalOwnedAttachInputLatencyDiagnostic(t *testing.T) {
	if os.Getenv("GRAITH_INPUT_LATENCY_DIAGNOSTIC") == "" {
		t.Skip("set GRAITH_INPUT_LATENCY_DIAGNOSTIC=1 to run the terminal-owned attach latency diagnostic")
	}

	h := newTestHarness(t)
	sessionID := "braw-latency"
	addLatencyPTYSession(t, h, sessionID)
	waitForDaemonLatencyEchoReady(t, h, sessionID)

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: sessionID, TerminalOwned: true})

	attachResp := h.readControlMsg(t)
	if attachResp.Type == "error" {
		t.Fatalf("attach error: %s", daemonLatencyErrorMessage(attachResp))
	}

	if attachResp.Type != "terminal_owned_attached" {
		t.Fatalf("attach response = %q, want terminal_owned_attached", attachResp.Type)
	}

	var seed protocol.TerminalOwnedAttachSeedMsg
	if err := protocol.DecodePayload(attachResp, &seed); err != nil {
		t.Fatal(err)
	}

	frames := startDaemonLatencyFrameStream(h)
	base := seed.Snapshot.SnapshotID
	samples := daemonLatencySampleCount(t)

	idle := runDaemonTerminalOwnedLatencySamples(t, h, frames, sessionID, &base, "braw", samples, 45*time.Millisecond)
	burst := runDaemonTerminalOwnedLatencySamples(t, h, frames, sessionID, &base, "canny", samples, 5*time.Millisecond)

	reportDaemonLatencySamples(t, "idle-45ms", idle)
	reportDaemonLatencySamples(t, "burst-5ms", burst)
}

func addLatencyPTYSession(t *testing.T, h *testHarness, sessionID string) {
	t.Helper()

	logPath := filepath.Join(h.sm.paths.LogDir, sessionID+".log")

	sess, err := newDaemonPTYSession(t, grpty.SessionOpts{
		ID:      sessionID,
		Command: "sh",
		Args: []string{
			"-c",
			`stty -echo; echo ready; while IFS= read -r line; do printf '\r%s' "$line"; done`,
		},
		Dir:     t.TempDir(),
		Rows:    24,
		Cols:    80,
		LogPath: logPath,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = sess.Kill()
		<-sess.Done()
		sess.Close()
	})

	h.sm.mu.Lock()
	h.sm.state.Sessions[sessionID] = &SessionState{
		ID:        sessionID,
		Name:      sessionID,
		Agent:     "latency-echo",
		Status:    StatusRunning,
		CreatedAt: time.Now().UTC(),
	}
	h.sm.sessions[sessionID] = sess
	h.sm.mu.Unlock()
}

func waitForDaemonLatencyEchoReady(t *testing.T, h *testHarness, sessionID string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ptySess, ok := h.sm.GetPTY(sessionID)
		if ok {
			if tail, err := ptySess.ScrollbackFile().TailBytes(4096); err == nil && strings.Contains(string(tail), "ready") {
				return
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("timed out waiting for latency echo agent to become ready")
}

func daemonLatencySampleCount(t *testing.T) int {
	t.Helper()

	raw := os.Getenv("GRAITH_INPUT_LATENCY_SAMPLES")
	if raw == "" {
		return 24
	}

	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		t.Fatalf("invalid GRAITH_INPUT_LATENCY_SAMPLES=%q", raw)
	}

	return n
}

func runDaemonTerminalOwnedLatencySamples(
	t *testing.T,
	h *testHarness,
	frames <-chan daemonLatencyFrameResult,
	sessionID string,
	base *uint64,
	prefix string,
	count int,
	delay time.Duration,
) []daemonInputLatencySample {
	t.Helper()

	samples := make([]daemonInputLatencySample, 0, count)
	for i := 0; i < count; i++ {
		marker := fmt.Sprintf("%s%02d ", prefix, i)
		samples = append(samples, measureDaemonTerminalOwnedEcho(t, h, frames, sessionID, base, marker))

		time.Sleep(delay)
	}

	return samples
}

func measureDaemonTerminalOwnedEcho(
	t *testing.T,
	h *testHarness,
	frames <-chan daemonLatencyFrameResult,
	sessionID string,
	base *uint64,
	marker string,
) daemonInputLatencySample {
	t.Helper()

	started := time.Now()

	inputErr := make(chan error, 1)
	go func() {
		inputErr <- h.writer.WriteFrame(protocol.ChannelData, []byte(marker+"\r"))
	}()

	var hint time.Duration

	frameWait := time.NewTimer(time.Second)
	defer frameWait.Stop()

	deadline := time.After(5 * time.Second)
	inputSent := false

	for {
		select {
		case err := <-inputErr:
			if err != nil {
				t.Fatalf("send input marker %q: %v", marker, err)
			}

			inputSent = true
			inputErr = nil
		case <-deadline:
			t.Fatalf("timed out waiting for marker %q", marker)
		case <-frameWait.C:
			t.Fatalf("timed out waiting for protocol frame after %s for marker %q (input_sent=%v)", time.Second, marker, inputSent)
		case got := <-frames:
			if got.err != nil {
				t.Fatal(got.err)
			}

			frame := got.frame

			if !frameWait.Stop() {
				select {
				case <-frameWait.C:
				default:
				}
			}

			frameWait.Reset(time.Second)

			switch frame.Channel {
			case protocol.ChannelData:
				if hint == 0 {
					hint = time.Since(started)
				}

				h.sendControl(t, "screen_snapshot", protocol.ScreenSnapshotMsg{
					SessionID: sessionID,
					DeltaFrom: *base,
				})

			case protocol.ChannelControl:
				env, err := protocol.DecodeControl(frame.Payload)
				if err != nil {
					t.Fatal(err)
				}

				if env.Type != "screen_snapshot_response" {
					continue
				}

				var snap protocol.ScreenSnapshotResponseMsg
				if err := protocol.DecodePayload(env, &snap); err != nil {
					t.Fatal(err)
				}

				if snap.SessionID != sessionID {
					continue
				}

				if snap.SnapshotID != 0 {
					*base = snap.SnapshotID
				}

				if daemonScreenSnapshotContains(snap, marker) {
					return daemonInputLatencySample{
						total: time.Since(started),
						hint:  hint,
					}
				}
			}
		}
	}
}

type daemonLatencyFrameResult struct {
	frame protocol.Frame
	err   error
}

func startDaemonLatencyFrameStream(h *testHarness) <-chan daemonLatencyFrameResult {
	ch := make(chan daemonLatencyFrameResult, 128)

	go func() {
		for {
			frame, err := h.reader.ReadFrame()
			ch <- daemonLatencyFrameResult{frame: frame, err: err}

			if err != nil {
				return
			}
		}
	}()

	return ch
}

func daemonScreenSnapshotContains(snap protocol.ScreenSnapshotResponseMsg, marker string) bool {
	if strings.Contains(snap.Frame, marker) {
		return true
	}

	for _, row := range snap.RowDeltas {
		if strings.Contains(row.Frame, marker) {
			return true
		}
	}

	return false
}

func reportDaemonLatencySamples(t *testing.T, name string, samples []daemonInputLatencySample) {
	t.Helper()

	totals := make([]time.Duration, len(samples))
	hints := make([]time.Duration, 0, len(samples))

	for i, sample := range samples {
		totals[i] = sample.total

		if sample.hint > 0 {
			hints = append(hints, sample.hint)
		}
	}

	sort.Slice(totals, func(i, j int) bool { return totals[i] < totals[j] })
	sort.Slice(hints, func(i, j int) bool { return hints[i] < hints[j] })

	visibleP95 := daemonLatencyPercentile(totals, 0.95)

	t.Logf("%s visible latency: min=%s median=%s p95=%s max=%s samples=%d",
		name, totals[0], daemonLatencyPercentile(totals, 0.50), visibleP95, totals[len(totals)-1], len(totals))

	if visibleP95 > daemonLatencyMaxVisibleP95 {
		t.Fatalf("%s visible latency p95 = %s, want <= %s", name, visibleP95, daemonLatencyMaxVisibleP95)
	}

	if len(hints) > 0 {
		t.Logf("%s output hint latency: min=%s median=%s p95=%s max=%s hints=%d",
			name, hints[0], daemonLatencyPercentile(hints, 0.50), daemonLatencyPercentile(hints, 0.95), hints[len(hints)-1], len(hints))
	} else {
		t.Logf("%s output hint latency: none; daemon pushed screen snapshots directly", name)
	}
}

func daemonLatencyPercentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}

	idx := int(float64(len(samples)-1) * p)

	return samples[idx]
}

func daemonLatencyErrorMessage(env protocol.Envelope) string {
	var msg protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &msg)

	return msg.Message
}
