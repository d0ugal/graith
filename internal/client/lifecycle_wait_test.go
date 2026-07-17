package client

import (
	"errors"
	"net"
	"testing"
	"time"
)

// shortenStartPollInterval swaps in a small start-poll interval for the duration
// of a test so lifecycle-wait polling doesn't sleep real seconds.
func shortenStartPollInterval(t *testing.T, d time.Duration) {
	t.Helper()

	orig := daemonStartPollInterval
	daemonStartPollInterval = d

	t.Cleanup(func() { daemonStartPollInterval = orig })
}

// stubDialLocalDaemon replaces the local dial seam so waitForDaemon can be
// exercised without a real socket.
func stubDialLocalDaemon(t *testing.T, fn func() (net.Conn, error)) {
	t.Helper()

	orig := dialLocalDaemon
	dialLocalDaemon = func(network, address string, timeout time.Duration) (net.Conn, error) {
		return fn()
	}

	t.Cleanup(func() { dialLocalDaemon = orig })
}

// TestWaitForDaemonSucceedsWithinStartBudget proves the post-exec readiness wait
// returns true once a dial succeeds and re-probes at the configured start-poll
// interval rather than the old fixed 20×250ms cadence (issue #1319).
func TestWaitForDaemonSucceedsWithinStartBudget(t *testing.T) {
	shortenStartTimeout(t, 500*time.Millisecond)
	shortenStartPollInterval(t, time.Millisecond)

	attempts := 0

	stubDialLocalDaemon(t, func() (net.Conn, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("dreich: not up yet")
		}

		clientConn, serverConn := net.Pipe()

		t.Cleanup(func() { _ = serverConn.Close() })

		return clientConn, nil
	})

	if !waitForDaemon("/bothy/daemon.sock") {
		t.Fatal("waitForDaemon should report the daemon ready once a dial succeeds")
	}

	if attempts != 3 {
		t.Fatalf("waitForDaemon dialed %d times, want it to poll until success on the 3rd", attempts)
	}
}

// TestWaitForDaemonTimesOutUnderShortenedPolicy proves the readiness wait is
// bounded by the effective start budget and gives up (returns false) instead of
// blocking or looping unbounded when the daemon never comes back.
func TestWaitForDaemonTimesOutUnderShortenedPolicy(t *testing.T) {
	shortenStartTimeout(t, 20*time.Millisecond)
	shortenStartPollInterval(t, 2*time.Millisecond)

	attempts := 0

	stubDialLocalDaemon(t, func() (net.Conn, error) {
		attempts++

		return nil, errors.New("dreich: never up")
	})

	start := time.Now()

	if waitForDaemon("/bothy/daemon.sock") {
		t.Fatal("waitForDaemon should time out when the daemon never returns")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waitForDaemon took %v, want it bounded by the shortened start budget", elapsed)
	}

	if attempts < 2 {
		t.Fatalf("waitForDaemon dialed %d times, want it to poll more than once before giving up", attempts)
	}
}

// TestWaitForDaemonCapsSleepToRemainingBudget proves a start-poll interval far
// larger than the start timeout does not block for the full interval: the sleep
// is clamped to the remaining budget so the readiness wait still returns near the
// deadline (#1319 review).
func TestWaitForDaemonCapsSleepToRemainingBudget(t *testing.T) {
	shortenStartTimeout(t, 20*time.Millisecond)
	shortenStartPollInterval(t, time.Hour) // far larger than the timeout

	stubDialLocalDaemon(t, func() (net.Conn, error) {
		return nil, errors.New("dreich: never up")
	})

	start := time.Now()

	if waitForDaemon("/bothy/daemon.sock") {
		t.Fatal("waitForDaemon should time out")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waitForDaemon slept %v, want the sleep clamped to the ~20ms budget", elapsed)
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
