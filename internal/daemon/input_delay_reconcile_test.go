package daemon

import (
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	grpty "github.com/d0ugal/graith/internal/pty"
)

// inputDelayFakeDriver records SetInputDelay pushes so the reconcile/reload path
// can be asserted without a real PTY. It embeds SessionDriver so it satisfies the
// interface; only SetInputDelay is exercised.
type inputDelayFakeDriver struct {
	SessionDriver

	mu    sync.Mutex
	delay time.Duration
}

func newInputDelayFakeDriver(initial time.Duration) *inputDelayFakeDriver {
	return &inputDelayFakeDriver{delay: initial}
}

func (d *inputDelayFakeDriver) SetInputDelay(v time.Duration) {
	d.mu.Lock()
	d.delay = v
	d.mu.Unlock()
}

func (d *inputDelayFakeDriver) current() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.delay
}

// TestReconcileLaunchedInputDelayBindsReloadedGeneration guards issue #1294: a
// reload that lands after a launch snapshots input_delay but before its driver is
// inserted into sm.sessions is missed by applyConfig's live push; the post-
// insertion reconcile must then bind the driver to the reloaded generation.
func TestReconcileLaunchedInputDelayBindsReloadedGeneration(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	base := sm.Config()
	launchGen := *base
	launchGen.Lifecycle.InputDelay = "50ms"

	if err := sm.applyConfig(&launchGen); err != nil {
		t.Fatalf("apply launch generation: %v", err)
	}

	// A launch built its driver against the 50ms generation but has not yet
	// inserted it into sm.sessions.
	driver := newInputDelayFakeDriver(50 * time.Millisecond)

	// A reload lands in that window. applyConfig cannot see the not-yet-inserted
	// driver, so it must not receive the push.
	reloaded := launchGen
	reloaded.Lifecycle.InputDelay = "200ms"

	if err := sm.applyConfig(&reloaded); err != nil {
		t.Fatalf("apply reloaded generation: %v", err)
	}

	if got := driver.current(); got != 50*time.Millisecond {
		t.Fatalf("pre-insertion reload reached the driver (%v); it should have been missed", got)
	}

	// The launch completes: the driver is inserted, then reconciled.
	sm.mu.Lock()
	sm.sessions["braw"] = driver
	sm.mu.Unlock()

	sm.reconcileLaunchedInputDelay(driver)

	if got := driver.current(); got != 200*time.Millisecond {
		t.Fatalf("reconcile bound the driver to %v, want the reloaded 200ms", got)
	}
}

// TestAdoptSessionsAppliesConfiguredInputDelay guards issue #1294: an adopted PTY
// must honour the configured [lifecycle] input_delay rather than falling back to
// the hard-coded pty default across a daemon upgrade.
func TestAdoptSessionsAppliesConfiguredInputDelay(t *testing.T) {
	sm := newLiveDriverLifecycleTestManager(t, time.Second)
	sm.cfg.Lifecycle.InputDelay = "175ms"

	// A live process + stand-in fd to adopt.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	sm.state.Sessions["bothy"] = &SessionState{
		ID: "bothy", Name: "bothy", Agent: "sleeper", Status: StatusRunning,
	}

	manifest := &UpgradeManifest{
		Sessions: []UpgradeSession{
			{ID: "bothy", Fd: int(r.Fd()), PID: cmd.Process.Pid},
		},
	}

	if err := sm.AdoptSessions(manifest); err != nil {
		t.Fatalf("AdoptSessions: %v", err)
	}

	driver, ok := sm.GetPTY("bothy")
	if !ok {
		t.Fatal("adopted session has no live driver")
	}

	// Fully retire the adopted session's goroutines before TempDir cleanup, or the
	// still-open scrollback log races the directory removal. Close the pipe writer
	// first so the read loop reaches EOF — otherwise Close blocks waiting on it.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = w.Close()

		select {
		case <-driver.Done():
		case <-time.After(5 * time.Second):
		}

		driver.Close()
		_ = r.Close()
	})

	sess, ok := driver.(*grpty.Session)
	if !ok {
		t.Fatalf("driver type = %T, want *pty.Session", driver)
	}

	if got := sess.InputDelay(); got != 175*time.Millisecond {
		t.Errorf("adopted session InputDelay = %v, want the configured 175ms", got)
	}
}
