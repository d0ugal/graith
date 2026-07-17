package cli

import (
	"net"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// fakeConnClock is a deterministic clock for lifecycle-wait tests: Sleep advances
// virtual time instead of blocking, so start-policy polling can be exercised
// without real delays.
type fakeConnClock struct {
	now time.Time
}

func (c *fakeConnClock) Now() time.Time { return c.now }

func (c *fakeConnClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

// preserveLifecyclePolicy snapshots the CLI-package start-policy seams and
// restores them after the test.
func preserveLifecyclePolicy(t *testing.T) {
	t.Helper()

	oldCfg := cfg
	oldNow, oldSleep, oldProbe, oldDial := connectionNow, connectionSleep, probeDaemonIdentityFn, dialLocalDaemon

	t.Cleanup(func() {
		cfg = oldCfg
		connectionNow, connectionSleep, probeDaemonIdentityFn, dialLocalDaemon = oldNow, oldSleep, oldProbe, oldDial
	})
}

func installFakeClock(t *testing.T) *fakeConnClock {
	t.Helper()

	clk := &fakeConnClock{now: time.Unix(1_000_000, 0)}
	connectionNow = clk.Now
	connectionSleep = clk.Sleep

	return clk
}

// TestPollLocalDaemonSucceedsBeforeBudget proves the CLI lifecycle poller stops
// as soon as the predicate is satisfied and honours the configured start-poll
// interval rather than a fixed retry count (issue #1319).
func TestPollLocalDaemonSucceedsBeforeBudget(t *testing.T) {
	preserveLifecyclePolicy(t)
	installFakeClock(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{
		StartTimeout:      "1s",
		StartPollInterval: "10ms",
	}}

	calls := 0
	ok := pollLocalDaemon(func(time.Time) bool {
		calls++

		return calls == 3
	})

	if !ok {
		t.Fatal("pollLocalDaemon should return true once the predicate holds")
	}

	if calls != 3 {
		t.Fatalf("pollLocalDaemon called ready %d times, want 3", calls)
	}
}

// TestPollLocalDaemonTimesOutOnConfiguredBudget proves the poller gives up after
// the effective start budget elapses instead of looping unbounded.
func TestPollLocalDaemonTimesOutOnConfiguredBudget(t *testing.T) {
	preserveLifecyclePolicy(t)
	installFakeClock(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{
		StartTimeout:      "100ms",
		StartPollInterval: "10ms",
	}}

	calls := 0
	ok := pollLocalDaemon(func(time.Time) bool {
		calls++

		return false
	})

	if ok {
		t.Fatal("pollLocalDaemon should time out when the predicate never holds")
	}

	// deadline = now + 100ms, sleeping 10ms each miss: checks run at 0..90ms.
	// Once virtual time reaches the deadline, the poller refuses another probe.
	if calls != 10 {
		t.Fatalf("pollLocalDaemon called ready %d times, want 10 for a 100ms/10ms budget", calls)
	}
}

// TestWaitForNewLocalDaemonGenerationReportsReady proves the post-exec wait
// returns once the replacement daemon reports the wanted version AND a fresh
// instance ID — the inherited old-generation probes (same instance ID) in
// between do not satisfy it (issue #1319).
func TestWaitForNewLocalDaemonGenerationReportsReady(t *testing.T) {
	preserveLifecyclePolicy(t)
	installFakeClock(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{
		StartTimeout:      "1s",
		StartPollInterval: "10ms",
	}}

	type probe struct{ v, id string }

	responses := []probe{
		{"", ""},                  // not reachable yet
		{"0.69.1-new", "old-gen"}, // inherited listener: right version, OLD instance
		{"0.69.1-new", "new-gen"}, // exec'd replacement: fresh instance
	}
	probeDaemonIdentityFn = func(time.Time) (string, string) {
		p := responses[0]
		if len(responses) > 1 {
			responses = responses[1:]
		}

		return p.v, p.id
	}

	got, ready := waitForNewLocalDaemonGeneration("0.69.1-new", "old-gen")
	if !ready {
		t.Fatal("should report ready once a fresh instance ID with the wanted version appears")
	}

	if got != "0.69.1-new" {
		t.Fatalf("last version = %q, want 0.69.1-new", got)
	}
}

// TestWaitForNewLocalDaemonGenerationRejectsInheritedListener is the core #1319
// regression: an inherited listener answering with the RIGHT version but the
// SAME (pre-upgrade) instance ID must never be reported ready.
func TestWaitForNewLocalDaemonGenerationRejectsInheritedListener(t *testing.T) {
	preserveLifecyclePolicy(t)
	installFakeClock(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{
		StartTimeout:      "50ms",
		StartPollInterval: "10ms",
	}}

	// Right version, unchanged instance ID: the old daemon on the inherited socket.
	probeDaemonIdentityFn = func(time.Time) (string, string) { return "0.69.1-new", "old-gen" }

	got, ready := waitForNewLocalDaemonGeneration("0.69.1-new", "old-gen")
	if ready {
		t.Fatal("an inherited listener with an unchanged instance ID must not be reported ready")
	}

	if got != "0.69.1-new" {
		t.Fatalf("last version = %q, want the last-observed 0.69.1-new", got)
	}
}

// TestWaitForNewLocalDaemonGenerationReturnsLastOnWrongVersion proves that when
// the daemon exec's into the wrong (old) version, the wait returns that
// last-observed value so execUpgrade can report the mismatch.
func TestWaitForNewLocalDaemonGenerationReturnsLastOnWrongVersion(t *testing.T) {
	preserveLifecyclePolicy(t)
	installFakeClock(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{
		StartTimeout:      "50ms",
		StartPollInterval: "10ms",
	}}

	// A fresh instance but the wrong version (exec'd back into the old binary).
	probeDaemonIdentityFn = func(time.Time) (string, string) { return "0.68.0-old", "new-gen" }

	got, ready := waitForNewLocalDaemonGeneration("0.69.1-new", "old-gen")
	if ready {
		t.Fatal("a wrong-version replacement must not be reported as ready")
	}

	if got != "0.68.0-old" {
		t.Fatalf("last version = %q, want the last-observed 0.68.0-old", got)
	}
}

// TestWaitForNewLocalDaemonGenerationTimesOutWhenUnreachable proves an empty
// probe (replacement never becomes reachable) is reported as not-ready.
func TestWaitForNewLocalDaemonGenerationTimesOutWhenUnreachable(t *testing.T) {
	preserveLifecyclePolicy(t)
	installFakeClock(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{
		StartTimeout:      "30ms",
		StartPollInterval: "10ms",
	}}

	probeDaemonIdentityFn = func(time.Time) (string, string) { return "", "" }

	got, ready := waitForNewLocalDaemonGeneration("0.69.1-new", "old-gen")
	if ready {
		t.Fatal("an unreachable replacement must not be reported as ready")
	}

	if got != "" {
		t.Fatalf("last version = %q, want empty on an unreachable daemon", got)
	}
}

// The post-exec listener can accept immediately while the replacement never
// completes its handshake. The first readiness probe must inherit the absolute
// start deadline instead of blocking for the longer handshake policy (#1319).
func TestWaitForNewLocalDaemonGenerationCapsStalledHandshake(t *testing.T) {
	preserveLifecyclePolicy(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{
		DialTimeout:       "5s",
		HandshakeTimeout:  "5s",
		StartTimeout:      "60ms",
		StartPollInterval: "5ms",
	}}
	connectionNow = time.Now
	connectionSleep = time.Sleep

	var capturedDialTimeout time.Duration

	dialLocalDaemon = func(_, _ string, timeout time.Duration) (net.Conn, error) {
		capturedDialTimeout = timeout
		clientConn, serverConn := net.Pipe()

		go func() {
			defer func() { _ = serverConn.Close() }()

			reader := protocol.NewFrameReader(serverConn)
			_, _ = reader.ReadFrame() // accept and drain the readiness handshake
			_, _ = reader.ReadFrame() // never reply; wait for the aggregate deadline
		}()

		return clientConn, nil
	}

	start := time.Now()

	_, ready := waitForNewLocalDaemonGeneration("0.69.1-new", "old-gen")
	if ready {
		t.Fatal("a listener that never completes the handshake must not be ready")
	}

	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("upgrade readiness took %v, want the stalled handshake capped by the 60ms aggregate start budget", elapsed)
	}

	if capturedDialTimeout <= 0 || capturedDialTimeout > 60*time.Millisecond {
		t.Fatalf("readiness dial timeout = %v, want it capped by the remaining 60ms start budget", capturedDialTimeout)
	}
}

// TestPollLocalDaemonCapsSleepToRemainingBudget proves a start-poll interval
// larger than the start timeout does not overshoot the aggregate budget: the
// single sleep is clamped to the remaining time, so the poller gives up right at
// the deadline (#1319 review).
func TestPollLocalDaemonCapsSleepToRemainingBudget(t *testing.T) {
	preserveLifecyclePolicy(t)
	clk := installFakeClock(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{
		StartTimeout:      "100ms",
		StartPollInterval: "10s", // deliberately far larger than the timeout
	}}

	start := clk.now

	calls := 0
	ok := pollLocalDaemon(func(time.Time) bool {
		calls++

		return false
	})

	if ok {
		t.Fatal("pollLocalDaemon should time out")
	}

	if calls != 1 {
		t.Fatalf("pollLocalDaemon called ready %d times, want 1 (no probe after the clamped sleep reaches the deadline)", calls)
	}

	if elapsed := clk.now.Sub(start); elapsed != 100*time.Millisecond {
		t.Fatalf("pollLocalDaemon advanced %v, want exactly the 100ms budget (no overshoot)", elapsed)
	}
}

// TestLocalDaemonStartPolicyDefaults proves the accessors fall back to the shared
// [connection] defaults when config is absent, invalid, or non-positive — the
// defensive path that keeps a bad value from disabling the budget entirely.
func TestLocalDaemonStartPolicyDefaults(t *testing.T) {
	preserveLifecyclePolicy(t)

	cfg = nil

	if got := localDaemonStartTimeout(); got != config.ConnectionStartTimeoutDefault {
		t.Errorf("nil-config start timeout = %v, want default %v", got, config.ConnectionStartTimeoutDefault)
	}

	if got := localDaemonStartPollInterval(); got != config.ConnectionStartPollIntervalDefault {
		t.Errorf("nil-config start poll = %v, want default %v", got, config.ConnectionStartPollIntervalDefault)
	}

	// Garbage and negative values must fall back to the defaults, not zero.
	cfg = &config.Config{Connection: config.ConnectionConfig{
		StartTimeout:      "thrawn",
		StartPollInterval: "-5s",
	}}

	if got := localDaemonStartTimeout(); got != config.ConnectionStartTimeoutDefault {
		t.Errorf("invalid start timeout = %v, want default %v", got, config.ConnectionStartTimeoutDefault)
	}

	if got := localDaemonStartPollInterval(); got != config.ConnectionStartPollIntervalDefault {
		t.Errorf("negative start poll = %v, want default %v", got, config.ConnectionStartPollIntervalDefault)
	}
}
