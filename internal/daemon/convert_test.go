package daemon

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/headless"
)

// convertTestManager builds a minimal SessionManager whose state persists to a
// temp file, enough to exercise ConvertToInteractive's guard/validation paths
// without a full daemon.
func convertTestManager(t *testing.T) *SessionManager {
	t.Helper()

	return &SessionManager{
		state:    NewState(),
		sessions: make(map[string]SessionDriver),
		cfg:      &config.Config{GitHubUsername: "ken"}, // non-empty: skip git discovery
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		paths:    config.Paths{StateFile: filepath.Join(t.TempDir(), "state.json")},
	}
}

// startConvertFake launches a real headless session backed by a shell script,
// registered in the manager as a running headless session.
func startConvertFake(t *testing.T, sm *SessionManager, id, script string) *headless.Session {
	t.Helper()

	dir := t.TempDir()

	s, err := headless.New(headless.Opts{
		ID:      id,
		Command: "sh",
		Args:    []string{"-c", script},
		Dir:     dir,
		LogPath: filepath.Join(dir, "scrollback.log"),
	})
	if err != nil {
		t.Fatalf("headless.New: %v", err)
	}

	t.Cleanup(s.Close)
	t.Cleanup(func() { _ = s.ForceKill() })

	return s
}

func TestConvertToInteractiveNotFound(t *testing.T) {
	t.Parallel()

	sm := convertTestManager(t)

	if _, err := sm.ConvertToInteractive("fash", 24, 80); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestConvertToInteractiveAlreadyInteractive(t *testing.T) {
	t.Parallel()

	sm := convertTestManager(t)
	sm.state.Sessions["braw"] = &SessionState{
		ID:         "braw",
		Name:       "braw",
		Status:     StatusRunning,
		DriverKind: DriverPTY,
	}

	got, err := sm.ConvertToInteractive("braw", 24, 80)
	if err != nil {
		t.Fatalf("convert of an already-interactive session should be a no-op, got err=%v", err)
	}

	if got.DriverKind != DriverPTY {
		t.Fatalf("DriverKind = %q, want pty", got.DriverKind)
	}
}

func TestConvertToInteractiveSoftDeleted(t *testing.T) {
	t.Parallel()

	sm := convertTestManager(t)
	deleted := time.Now()
	sm.state.Sessions["dreich"] = &SessionState{
		ID:         "dreich",
		Name:       "dreich",
		Status:     StatusStopped,
		DriverKind: DriverHeadless,
		DeletedAt:  &deleted,
	}

	if _, err := sm.ConvertToInteractive("dreich", 24, 80); err == nil {
		t.Fatalf("convert of a soft-deleted session should be refused")
	}
}

func TestConvertToInteractiveBusy(t *testing.T) {
	t.Parallel()

	for _, status := range []SessionStatus{StatusCreating, StatusDeleting} {
		sm := convertTestManager(t)
		sm.state.Sessions["thrawn"] = &SessionState{
			ID:         "thrawn",
			Name:       "thrawn",
			Status:     status,
			DriverKind: DriverHeadless,
		}

		if _, err := sm.ConvertToInteractive("thrawn", 24, 80); err == nil || !strings.Contains(err.Error(), "busy") {
			t.Fatalf("status %s: want busy error, got %v", status, err)
		}
	}
}

// TestConvertGuardSaveFailureRollsBack asserts that when the guard-state save
// fails, the running headless driver is left in place and the status restored —
// so a failed convert changes nothing.
func TestConvertGuardSaveFailureRollsBack(t *testing.T) {
	sm := convertTestManager(t)
	sm.saveStateFault = func() error { return io.ErrClosedPipe }

	driver := startConvertFake(t, sm, "canny", "sleep 30")
	sm.sessions["canny"] = driver
	sm.state.Sessions["canny"] = &SessionState{
		ID:         "canny",
		Name:       "canny",
		Status:     StatusRunning,
		DriverKind: DriverHeadless,
	}

	if _, err := sm.ConvertToInteractive("canny", 24, 80); err == nil {
		t.Fatalf("convert should fail when the guard save faults")
	}

	if sm.state.Sessions["canny"].Status != StatusRunning {
		t.Fatalf("status = %q, want running after rollback", sm.state.Sessions["canny"].Status)
	}

	if sm.state.Sessions["canny"].DriverKind != DriverHeadless {
		t.Fatalf("DriverKind = %q, want headless after rollback", sm.state.Sessions["canny"].DriverKind)
	}

	if sm.sessions["canny"] != driver {
		t.Fatalf("driver should be re-inserted into the map after rollback")
	}
}

// TestStopDriverForConvertSettlesOnInterrupt: a process that exits on SIGINT is
// stopped by the first (gentlest) step. It must return well before the settle
// timeout would fire, proving it settled on the interrupt rather than escalating
// to SIGTERM/SIGKILL.
func TestStopDriverForConvertSettlesOnInterrupt(t *testing.T) {
	const settle = 5 * time.Second

	sm := convertTestManager(t)
	sm.cfg.Lifecycle.ConvertSettleTimeout = settle.String()

	driver := startConvertFake(t, sm, "bonnie", "trap 'exit 0' INT; sleep 30")

	start := time.Now()
	done := make(chan struct{})

	go func() {
		sm.stopDriverForConvert(driver)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("stopDriverForConvert did not settle on SIGINT (would have escalated)")
	}

	if elapsed := time.Since(start); elapsed >= settle {
		t.Fatalf("settled after %v (>= settle timeout %v): it escalated instead of settling", elapsed, settle)
	}

	if !driver.Exited() {
		t.Fatalf("driver should have exited")
	}
}

// TestStopDriverForConvertEscalates: a process ignoring SIGINT and SIGTERM is
// eventually SIGKILLed. Timeouts are shrunk so the test is quick.
func TestStopDriverForConvertEscalates(t *testing.T) {
	sm := convertTestManager(t)
	sm.cfg.Lifecycle.ConvertSettleTimeout = "200ms"
	sm.cfg.Lifecycle.ConvertKillTimeout = "200ms"

	driver := startConvertFake(t, sm, "scunner", "trap '' INT TERM; sleep 30")

	done := make(chan struct{})

	go func() {
		sm.stopDriverForConvert(driver)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatalf("stopDriverForConvert did not escalate to SIGKILL")
	}

	if !driver.Exited() {
		t.Fatalf("driver should have been force-killed")
	}
}

// TestConvertStaleWatcherDoesNotClobber locks in the trickiest concurrency
// invariant of the swap: when the interrupted headless process exits, its
// now-stale exit watcher must NOT overwrite the freshly-converted session's
// state. It reproduces convert's key moves — remove the driver from the map and
// promote the session to the new (pty/running) generation under the lock — then
// lets the old driver exit so its watcher fires, and asserts the post-convert
// state survives.
func TestConvertStaleWatcherDoesNotClobber(t *testing.T) {
	sm := convertTestManager(t)
	driver := startConvertFake(t, sm, "braw", "trap 'exit 0' INT; sleep 30")

	sm.sessions["braw"] = driver
	sm.state.Sessions["braw"] = &SessionState{
		ID:         "braw",
		Name:       "braw",
		Status:     StatusRunning,
		DriverKind: DriverHeadless,
	}

	// The watcher registered at launch, now waiting on the old driver's exit.
	sm.startWatcher("braw", driver)

	// Convert's swap: drop the old driver from the map (staling its watcher) and
	// promote the session to the new interactive generation.
	sm.mu.Lock()
	delete(sm.sessions, "braw")
	s := sm.state.Sessions["braw"]
	s.DriverKind = DriverPTY
	s.Status = StatusRunning
	sm.mu.Unlock()

	// Let the old headless process exit so its watcher fires and takes the stale
	// path.
	_ = driver.Interrupt(1, 0)

	watchersDone := make(chan struct{})

	go func() {
		sm.watchers.Wait()
		close(watchersDone)
	}()

	select {
	case <-watchersDone:
	case <-time.After(4 * time.Second):
		t.Fatalf("stale watcher did not finish")
	}

	// The watcher saw itself stale and left the converted state untouched.
	got := sm.state.Sessions["braw"]
	if got.DriverKind != DriverPTY {
		t.Fatalf("DriverKind = %q, want pty (stale watcher clobbered it)", got.DriverKind)
	}

	if got.Status != StatusRunning {
		t.Fatalf("Status = %q, want running (stale watcher clobbered it)", got.Status)
	}
}
