package client

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
)

// shortenStartPollInterval swaps in a small start-poll interval for the duration
// of a test so lifecycle-wait polling doesn't sleep real seconds.
func shortenStartPollInterval(t *testing.T, d time.Duration) {
	t.Helper()

	orig := daemonStartPollInterval
	daemonStartPollInterval = d

	t.Cleanup(func() { daemonStartPollInterval = orig })
}

// stubDialLocalDaemon replaces the local dial seam so the generation-aware
// readiness probe can be exercised without a real socket.
func stubDialLocalDaemon(t *testing.T, fn func() (net.Conn, error)) {
	t.Helper()

	orig := dialLocalDaemon
	dialLocalDaemon = func(network, address string, timeout time.Duration) (net.Conn, error) {
		return fn()
	}

	t.Cleanup(func() { dialLocalDaemon = orig })
}

// handshakeReplyConn returns a client-side net.Pipe conn whose server side
// answers a single handshake with a handshake_ok carrying the given daemon
// version + instance ID, modelling one probe round-trip of probeDaemonIdentity.
func handshakeReplyConn(t *testing.T, daemonVersion, instanceID string) net.Conn {
	t.Helper()

	clientConn, serverConn := net.Pipe()

	go func() {
		defer func() { _ = serverConn.Close() }()

		r := protocol.NewFrameReader(serverConn)
		w := protocol.NewFrameWriter(serverConn)

		if _, err := r.ReadFrame(); err != nil {
			return
		}

		data, _ := protocol.EncodeControl("handshake_ok", protocol.HandshakeOkMsg{
			Version:          protocol.Version,
			DaemonVersion:    daemonVersion,
			DaemonInstanceID: instanceID,
		})
		_ = w.WriteFrame(protocol.ChannelControl, data)
	}()

	t.Cleanup(func() { _ = clientConn.Close() })

	return clientConn
}

// TestWaitForNewDaemonGenerationSucceedsOnChangedInstance proves readiness is
// reached only once the daemon reports a DIFFERENT instance ID (the new
// generation), re-probing at the configured start-poll interval (issue #1319).
func TestWaitForNewDaemonGenerationSucceedsOnChangedInstance(t *testing.T) {
	shortenStartTimeout(t, 500*time.Millisecond)
	shortenStartPollInterval(t, time.Millisecond)

	attempts := 0

	stubDialLocalDaemon(t, func() (net.Conn, error) {
		attempts++
		// First two probes see the inherited old daemon (same instance ID);
		// the third sees the exec'd replacement with a fresh instance ID.
		if attempts < 3 {
			return handshakeReplyConn(t, version.Version, "old-generation"), nil
		}

		return handshakeReplyConn(t, version.Version, "new-generation"), nil
	})

	if !waitForNewDaemonGeneration("/bothy/daemon.sock", config.Paths{}, version.Version, "old-generation") {
		t.Fatal("readiness should be reached once the instance ID changes")
	}

	if attempts != 3 {
		t.Fatalf("probed %d times, want it to poll until the new generation on the 3rd", attempts)
	}
}

// TestWaitForNewDaemonGenerationRejectsInheritedListener is the core #1319
// regression: a listener that keeps answering with the SAME version AND the SAME
// instance ID (the inherited socket / a same-version restart before exec) must
// NOT be reported ready. Readiness times out instead of falsely succeeding.
func TestWaitForNewDaemonGenerationRejectsInheritedListener(t *testing.T) {
	shortenStartTimeout(t, 20*time.Millisecond)
	shortenStartPollInterval(t, 2*time.Millisecond)

	attempts := 0

	stubDialLocalDaemon(t, func() (net.Conn, error) {
		attempts++
		// Same version, same instance ID every time: the old daemon still serving.
		return handshakeReplyConn(t, version.Version, "old-generation"), nil
	})

	start := time.Now()

	if waitForNewDaemonGeneration("/bothy/daemon.sock", config.Paths{}, version.Version, "old-generation") {
		t.Fatal("an inherited listener with an unchanged instance ID must not be reported ready")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("readiness wait took %v, want it bounded by the shortened start budget", elapsed)
	}

	if attempts < 2 {
		t.Fatalf("probed %d times, want it to poll more than once before giving up", attempts)
	}
}

// TestWaitForNewDaemonGenerationTimesOutWhenUnreachable proves the wait is
// bounded when the daemon never becomes reachable at all.
func TestWaitForNewDaemonGenerationTimesOutWhenUnreachable(t *testing.T) {
	shortenStartTimeout(t, 20*time.Millisecond)
	shortenStartPollInterval(t, 2*time.Millisecond)

	stubDialLocalDaemon(t, func() (net.Conn, error) {
		return nil, errors.New("dreich: never up")
	})

	if waitForNewDaemonGeneration("/bothy/daemon.sock", config.Paths{}, version.Version, "old-generation") {
		t.Fatal("readiness should time out when the daemon never returns")
	}
}

// TestWaitForNewDaemonGenerationCapsSleepToRemainingBudget proves a start-poll
// interval far larger than the start timeout does not block for the full
// interval: the sleep is clamped to the remaining budget (#1319 review).
func TestWaitForNewDaemonGenerationCapsSleepToRemainingBudget(t *testing.T) {
	shortenStartTimeout(t, 20*time.Millisecond)
	shortenStartPollInterval(t, time.Hour) // far larger than the timeout

	stubDialLocalDaemon(t, func() (net.Conn, error) {
		return nil, errors.New("dreich: never up")
	})

	start := time.Now()

	if waitForNewDaemonGeneration("/bothy/daemon.sock", config.Paths{}, version.Version, "old-generation") {
		t.Fatal("readiness should time out")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("readiness wait slept %v, want the sleep clamped to the ~20ms budget", elapsed)
	}
}

// TestPollDaemonReadyReturnsImmediatelyWhenReady proves the shared poller checks
// once before sleeping so an already-ready daemon incurs no poll delay.
func TestPollDaemonReadyReturnsImmediatelyWhenReady(t *testing.T) {
	shortenStartTimeout(t, time.Hour)
	shortenStartPollInterval(t, time.Hour)

	calls := 0

	start := time.Now()
	ok := pollDaemonReady(func() bool {
		calls++

		return true
	})

	if !ok {
		t.Fatal("pollDaemonReady should return true when ready immediately")
	}

	if calls != 1 {
		t.Fatalf("pollDaemonReady called ready %d times, want exactly 1 with no sleep", calls)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("pollDaemonReady slept %v before the first check", elapsed)
	}
}
