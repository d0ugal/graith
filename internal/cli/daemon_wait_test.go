package cli

import (
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
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
	oldNow, oldSleep, oldProbe := connectionNow, connectionSleep, probeDaemonVersionFn

	t.Cleanup(func() {
		cfg = oldCfg
		connectionNow, connectionSleep, probeDaemonVersionFn = oldNow, oldSleep, oldProbe
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
	ok := pollLocalDaemon(func() bool {
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
	ok := pollLocalDaemon(func() bool {
		calls++

		return false
	})

	if ok {
		t.Fatal("pollLocalDaemon should time out when the predicate never holds")
	}

	// deadline = now + 100ms, sleeping 10ms each miss: one check before the first
	// sleep, then 10 more checks until virtual time reaches the deadline.
	if calls != 11 {
		t.Fatalf("pollLocalDaemon called ready %d times, want 11 for a 100ms/10ms budget", calls)
	}
}

// TestWaitForLocalDaemonVersionReportsReady proves the post-exec version wait
// returns once the replacement daemon reports the wanted version.
func TestWaitForLocalDaemonVersionReportsReady(t *testing.T) {
	preserveLifecyclePolicy(t)
	installFakeClock(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{
		StartTimeout:      "1s",
		StartPollInterval: "10ms",
	}}

	responses := []string{"", "0.68.0-old", "0.69.1-new"}
	probeDaemonVersionFn = func() string {
		v := responses[0]
		if len(responses) > 1 {
			responses = responses[1:]
		}

		return v
	}

	got, ready := waitForLocalDaemonVersion("0.69.1-new")
	if !ready {
		t.Fatal("waitForLocalDaemonVersion should report ready once the wanted version appears")
	}

	if got != "0.69.1-new" {
		t.Fatalf("waitForLocalDaemonVersion = %q, want 0.69.1-new", got)
	}
}

// TestWaitForLocalDaemonVersionReturnsLastOnTimeout proves that when the daemon
// exec's into the wrong (old) version, the wait returns that last-observed value
// so execUpgrade can report the mismatch instead of silently succeeding.
func TestWaitForLocalDaemonVersionReturnsLastOnTimeout(t *testing.T) {
	preserveLifecyclePolicy(t)
	installFakeClock(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{
		StartTimeout:      "50ms",
		StartPollInterval: "10ms",
	}}

	probeDaemonVersionFn = func() string { return "0.68.0-old" }

	got, ready := waitForLocalDaemonVersion("0.69.1-new")
	if ready {
		t.Fatal("waitForLocalDaemonVersion should not report ready when the wanted version never appears")
	}

	if got != "0.68.0-old" {
		t.Fatalf("waitForLocalDaemonVersion = %q, want the last-observed 0.68.0-old", got)
	}
}

// TestWaitForLocalDaemonVersionTimesOutWhenUnreachable proves an empty probe
// (replacement never becomes reachable) is reported as not-ready so execUpgrade
// fails instead of silently printing success (#1319 review).
func TestWaitForLocalDaemonVersionTimesOutWhenUnreachable(t *testing.T) {
	preserveLifecyclePolicy(t)
	installFakeClock(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{
		StartTimeout:      "30ms",
		StartPollInterval: "10ms",
	}}

	probeDaemonVersionFn = func() string { return "" }

	got, ready := waitForLocalDaemonVersion("0.69.1-new")
	if ready {
		t.Fatal("an unreachable replacement must not be reported as ready")
	}

	if got != "" {
		t.Fatalf("waitForLocalDaemonVersion = %q, want empty on an unreachable daemon", got)
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
	ok := pollLocalDaemon(func() bool {
		calls++

		return false
	})

	if ok {
		t.Fatal("pollLocalDaemon should time out")
	}

	if calls != 2 {
		t.Fatalf("pollLocalDaemon called ready %d times, want 2 (one miss, one clamped sleep, final miss)", calls)
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
