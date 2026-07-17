package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

// fakeClock is a deterministic now/sleep pair: sleep advances the clock by the
// requested duration, so reconnectLoop's deadline is driven entirely by how
// many retries it attempts — no real time passes.
type fakeClock struct {
	t      time.Time
	slept  []time.Duration
	onTick func() // invoked after each sleep, before now() is next read
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(1700000000, 0)}
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) sleep(d time.Duration) {
	c.slept = append(c.slept, d)
	c.t = c.t.Add(d)

	if c.onTick != nil {
		c.onTick()
	}
}

func TestReconnectLoopSucceeds(t *testing.T) {
	clock := newFakeClock()

	attached := payloadEnv("attached", protocol.SessionInfo{ID: "braw"})
	conn := &scriptedConn{responses: []scriptedResp{okResp(attached)}}

	got, resp, err := reconnectLoop("braw",
		func() (reconnectConn, error) { return conn, nil },
		clock.now, clock.sleep, 10*time.Second, 250*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != conn {
		t.Error("expected the successful connection to be returned")
	}

	if resp.Type != "attached" {
		t.Errorf("resp.Type = %q, want attached", resp.Type)
	}

	if conn.closed != 0 {
		t.Error("a successful connection must not be closed by reconnectLoop")
	}
}

func TestReconnectLoopRetriesThenSucceeds(t *testing.T) {
	clock := newFakeClock()

	good := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("attached", protocol.SessionInfo{ID: "braw"}))}}

	// First dial fails, second returns a read error (and must be closed), third
	// succeeds.
	badRead := &scriptedConn{responses: []scriptedResp{errResp(errors.New("half-open"))}}

	var attempt int

	dial := func() (reconnectConn, error) {
		attempt++
		switch attempt {
		case 1:
			return nil, errors.New("connection refused")
		case 2:
			return badRead, nil
		default:
			return good, nil
		}
	}

	got, _, err := reconnectLoop("braw", dial, clock.now, clock.sleep, 10*time.Second, 250*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != good {
		t.Error("expected the third (good) connection")
	}

	if badRead.closed != 1 {
		t.Errorf("the read-error connection was closed %d times, want 1", badRead.closed)
	}

	if len(clock.slept) != 3 {
		t.Errorf("slept %d times, want 3 (one per attempt)", len(clock.slept))
	}
}

func TestReconnectLoopSessionGone(t *testing.T) {
	clock := newFakeClock()

	conn := &scriptedConn{responses: []scriptedResp{okResp(errEnv("session deleted"))}}

	_, _, err := reconnectLoop("braw",
		func() (reconnectConn, error) { return conn, nil },
		clock.now, clock.sleep, 10*time.Second, 250*time.Millisecond)
	if err == nil || err.Error() != "session unavailable: session deleted" {
		t.Fatalf("err = %v, want \"session unavailable: session deleted\"", err)
	}

	if conn.closed != 1 {
		t.Errorf("the errored connection was closed %d times, want 1", conn.closed)
	}
}

func TestReconnectLoopTimesOut(t *testing.T) {
	clock := newFakeClock()

	// Every dial fails, so the loop only ends when now() passes the deadline.
	dials := 0
	dial := func() (reconnectConn, error) {
		dials++
		return nil, errors.New("still down")
	}

	_, _, err := reconnectLoop("braw", dial, clock.now, clock.sleep, 1*time.Second, 250*time.Millisecond)
	if err == nil || err.Error() != "timed out after 1s" {
		t.Fatalf("err = %v, want \"timed out after 1s\"", err)
	}

	// 1s / 250ms fits 3 attempts strictly before the deadline; the 4th sleep
	// lands exactly on the deadline, so no dial is started there (no overshoot).
	if dials != 3 {
		t.Errorf("dialed %d times, want 3 strictly before timeout", dials)
	}
}

// TestReconnectLoopCapsSleepToRemaining proves a reconnect_interval larger than
// the aggregate reconnect_timeout does not overshoot the budget: the pre-dial
// sleep is capped to the remaining time, and once that single sleep consumes the
// whole window the loop stops WITHOUT dialing — a dial started at the deadline
// carries its own dial/handshake budget and would run past reconnect_timeout
// (issue #1242). The dial here fails the test if ever called, proving no overshoot.
func TestReconnectLoopCapsSleepToRemaining(t *testing.T) {
	clock := newFakeClock()

	dial := func() (reconnectConn, error) {
		t.Fatal("dial started at/after the aggregate deadline — reconnect_timeout overshoot")

		return nil, nil
	}

	// Interval (10s) far exceeds the timeout (1s): a single capped sleep of 1s
	// advances exactly to the deadline, then the loop breaks before any dial.
	_, _, err := reconnectLoop("braw", dial, clock.now, clock.sleep, 1*time.Second, 10*time.Second)
	if err == nil || err.Error() != "timed out after 1s" {
		t.Fatalf("err = %v, want \"timed out after 1s\"", err)
	}

	if len(clock.slept) != 1 || clock.slept[0] != 1*time.Second {
		t.Errorf("slept = %v, want a single 1s sleep capped to the remaining budget", clock.slept)
	}
}

// TestReconnectLoopImmediateTimeout: a zero timeout returns before any dial.
func TestReconnectLoopImmediateTimeout(t *testing.T) {
	clock := newFakeClock()

	dialed := false
	dial := func() (reconnectConn, error) {
		dialed = true
		return nil, nil
	}

	_, _, err := reconnectLoop("braw", dial, clock.now, clock.sleep, 0, 250*time.Millisecond)
	if err == nil {
		t.Fatal("expected an immediate timeout with zero budget")
	}

	if dialed {
		t.Error("dial should not run when the deadline has already passed")
	}
}

// TestConfigureReconnect installs the [connection] reconnect overrides into the
// package vars reconnectToSession reads, and leaves the current value untouched
// for a non-positive input so a partial config can't disable recovery.
func TestConfigureReconnect(t *testing.T) {
	origDeadline, origInterval := reconnectDeadline, reconnectInterval

	t.Cleanup(func() {
		reconnectDeadline = origDeadline
		reconnectInterval = origInterval
	})

	ConfigureReconnect(30*time.Second, 400*time.Millisecond)

	if reconnectDeadline != 30*time.Second {
		t.Errorf("reconnectDeadline = %v, want 30s", reconnectDeadline)
	}

	if reconnectInterval != 400*time.Millisecond {
		t.Errorf("reconnectInterval = %v, want 400ms", reconnectInterval)
	}

	// Non-positive values are ignored, preserving the installed values.
	ConfigureReconnect(0, -1)

	if reconnectDeadline != 30*time.Second {
		t.Errorf("zero deadline changed reconnectDeadline to %v, want unchanged", reconnectDeadline)
	}

	if reconnectInterval != 400*time.Millisecond {
		t.Errorf("negative interval changed reconnectInterval to %v, want unchanged", reconnectInterval)
	}
}
