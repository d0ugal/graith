package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

type teardownFakeDriver struct {
	SessionDriver

	mu          sync.Mutex
	done        chan struct{}
	doneOnce    sync.Once
	exitOnForce bool
	killAt      time.Time
	forceAt     time.Time
	kills       int
	forces      int
	closes      int
	detaches    int
}

func newTeardownFakeDriver(exitOnForce bool) *teardownFakeDriver {
	return &teardownFakeDriver{done: make(chan struct{}), exitOnForce: exitOnForce}
}

func (d *teardownFakeDriver) ProcessPID() int       { return 4242 }
func (d *teardownFakeDriver) Pgid() int             { return 4242 }
func (d *teardownFakeDriver) Done() <-chan struct{} { return d.done }
func (d *teardownFakeDriver) ExitCode() int         { return 137 }
func (d *teardownFakeDriver) BytesRead() int64      { return 0 }
func (d *teardownFakeDriver) Exited() bool {
	select {
	case <-d.done:
		return true
	default:
		return false
	}
}
func (d *teardownFakeDriver) Kill() error {
	d.mu.Lock()
	d.kills++
	d.killAt = time.Now()
	d.mu.Unlock()

	return nil
}
func (d *teardownFakeDriver) ForceKill() error {
	d.mu.Lock()
	d.forces++
	d.forceAt = time.Now()
	exit := d.exitOnForce
	d.mu.Unlock()

	if exit {
		d.doneOnce.Do(func() { close(d.done) })
	}

	return nil
}
func (d *teardownFakeDriver) Close() {
	d.mu.Lock()
	d.closes++
	d.mu.Unlock()
}
func (d *teardownFakeDriver) Detach() {
	d.mu.Lock()
	d.detaches++
	d.mu.Unlock()
}

func (d *teardownFakeDriver) teardownStats() (kills, forces, closes int, termToKill time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.kills, d.forces, d.closes, d.forceAt.Sub(d.killAt)
}

func newLiveDriverLifecycleTestManager(t *testing.T, grace time.Duration) *SessionManager {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Lifecycle.ProcessKillGrace = grace.String()

	sm := NewSessionManager(cfg, config.Paths{
		DataDir:    dir,
		LogDir:     filepath.Join(dir, "logs"),
		RuntimeDir: filepath.Join(dir, "runtime"),
		StateFile:  filepath.Join(dir, "state.json"),
	}, quietLogger())
	if err := os.MkdirAll(sm.paths.LogDir, 0o700); err != nil {
		t.Fatal(err)
	}

	return sm
}

func assertFakeDriverEscalated(t *testing.T, driver *teardownFakeDriver, grace time.Duration) {
	t.Helper()

	kills, forces, closes, termToKill := driver.teardownStats()
	if kills != 1 || forces != 1 || closes != 1 {
		t.Fatalf("teardown calls TERM=%d KILL=%d Close=%d, want 1/1/1", kills, forces, closes)
	}

	if termToKill < grace*3/4 {
		t.Fatalf("TERM→KILL delay = %v, want configured grace %v", termToKill, grace)
	}
}

func TestTeardownLiveDriverUsesConfiguredGrace(t *testing.T) {
	const grace = 40 * time.Millisecond

	sm := newLiveDriverLifecycleTestManager(t, grace)
	driver := newTeardownFakeDriver(true)

	if err := sm.teardownLiveDriver(context.Background(), driver); err != nil {
		t.Fatalf("teardownLiveDriver: %v", err)
	}

	assertFakeDriverEscalated(t, driver, grace)
}

func TestTeardownLiveDriverBoundsPostKillCompletion(t *testing.T) {
	const grace = 25 * time.Millisecond

	sm := newLiveDriverLifecycleTestManager(t, grace)
	driver := newTeardownFakeDriver(false)
	start := time.Now()

	err := sm.teardownLiveDriver(context.Background(), driver)
	if err == nil || !strings.Contains(err.Error(), "after SIGKILL") {
		t.Fatalf("teardown error = %v, want bounded post-KILL failure", err)
	}

	if elapsed := time.Since(start); elapsed < 2*grace*3/4 || elapsed > time.Second {
		t.Fatalf("teardown elapsed = %v, want two bounded grace phases", elapsed)
	}

	kills, forces, closes, _ := driver.teardownStats()
	if kills != 1 || forces != 1 || closes != 0 {
		t.Fatalf("wedged teardown calls TERM=%d KILL=%d Close=%d, want 1/1/0", kills, forces, closes)
	}
}

func TestHardDeleteUsesLiveDriverTeardownPolicy(t *testing.T) {
	const grace = 25 * time.Millisecond

	sm := newLiveDriverLifecycleTestManager(t, grace)
	driver := newTeardownFakeDriver(true)
	sm.state.Sessions["braw-delete"] = &SessionState{
		ID: "braw-delete", Name: "braw-delete", Status: StatusRunning, InPlace: true,
	}
	sm.sessions["braw-delete"] = driver

	if err := sm.Delete("braw-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	assertFakeDriverEscalated(t, driver, grace)
}

func TestSoftDeleteUsesLiveDriverTeardownPolicy(t *testing.T) {
	const grace = 25 * time.Millisecond

	sm := newLiveDriverLifecycleTestManager(t, grace)
	driver := newTeardownFakeDriver(true)
	sm.state.Sessions["canny-delete"] = &SessionState{
		ID: "canny-delete", Name: "canny-delete", Status: StatusRunning, InPlace: true,
	}
	sm.sessions["canny-delete"] = driver

	if _, err := sm.SoftDelete("canny-delete"); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	assertFakeDriverEscalated(t, driver, grace)
}

func TestShutdownUsesLiveDriverTeardownPolicy(t *testing.T) {
	const grace = 25 * time.Millisecond

	sm := newLiveDriverLifecycleTestManager(t, grace)
	driver := newTeardownFakeDriver(true)
	sm.state.Sessions["dreich-shutdown"] = &SessionState{
		ID: "dreich-shutdown", Name: "dreich-shutdown", Status: StatusRunning,
	}
	sm.sessions["dreich-shutdown"] = driver

	sm.StopAll(context.Background())
	assertFakeDriverEscalated(t, driver, grace)
}

// TestShutdownBudgetHonoursGraceAboveWrapperDefault guards the issue #1243
// round-3 fix: the whole-daemon shutdown budget must be derived from the
// configured lifecycle grace so a process_kill_grace above the old fixed 10s
// wrapper reaches its SIGKILL transition instead of being cancelled early. The
// budget must exceed both the grace (else the SIGTERM→SIGKILL wait is cut) and
// the old 10s cap.
func TestShutdownBudgetHonoursGraceAboveWrapperDefault(t *testing.T) {
	const grace = 30 * time.Second // above the old fixed 10s shutdown wrapper

	sm := newLiveDriverLifecycleTestManager(t, grace)

	budget := sm.shutdownBudget()
	if budget <= grace {
		t.Fatalf("shutdown budget %v <= grace %v: the TERM→SIGKILL wait would be cut short", budget, grace)
	}

	if budget <= 10*time.Second {
		t.Fatalf("shutdown budget %v is not derived from the grace (still near the old 10s cap)", budget)
	}

	// Two teardown windows plus the exit-watcher window.
	if want := 3 * grace; budget != want {
		t.Fatalf("shutdown budget = %v, want %v", budget, want)
	}
}

// TestShutdownReachesSIGKILLWithinDerivedBudget drives StopAll with a context
// built exactly as the daemon wrapper does (from shutdownBudget) against a
// TERM-ignoring driver, and asserts the derived budget is wide enough for the
// full SIGTERM→grace→SIGKILL escalation rather than cutting it off.
func TestShutdownReachesSIGKILLWithinDerivedBudget(t *testing.T) {
	const grace = 40 * time.Millisecond

	sm := newLiveDriverLifecycleTestManager(t, grace)
	driver := newTeardownFakeDriver(true) // ignores TERM; exits only on SIGKILL
	sm.state.Sessions["strath-shutdown"] = &SessionState{
		ID: "strath-shutdown", Name: "strath-shutdown", Status: StatusRunning,
	}
	sm.sessions["strath-shutdown"] = driver

	shutdownCtx, cancel := context.WithTimeout(context.Background(), sm.shutdownBudget())
	defer cancel()

	sm.StopAll(shutdownCtx)
	assertFakeDriverEscalated(t, driver, grace)
}

func TestRestartUsesLiveDriverTeardownPolicy(t *testing.T) {
	const grace = 25 * time.Millisecond

	sm := newLiveDriverLifecycleTestManager(t, grace)
	driver := newTeardownFakeDriver(true)
	sm.state.Sessions["thrawn-restart"] = &SessionState{
		ID: "thrawn-restart", Name: "thrawn-restart", Agent: "missing-agent", Status: StatusRunning,
	}
	sm.sessions["thrawn-restart"] = driver

	if _, err := sm.Restart("thrawn-restart", 24, 80); err == nil {
		t.Fatal("Restart should fail after teardown because the test agent is missing")
	}

	assertFakeDriverEscalated(t, driver, grace)
}

func TestMigrateUsesLiveDriverTeardownPolicy(t *testing.T) {
	const grace = 25 * time.Millisecond

	sm := newMigrateTestManager(t)
	sm.cfg.Lifecycle.ProcessKillGrace = grace.String()
	sm.cfg.Migration.HealthWindow = "50ms"
	driver := newTeardownFakeDriver(true)
	repo := initTempGitRepo(t)

	claudeRoot := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeRoot)
	t.Setenv("CODEX_HOME", t.TempDir())

	const sid = "33333333-4444-5555-6666-777777777777"

	projDir := filepath.Join(claudeRoot, "projects", "-migrate-live")
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(projDir, sid+".jsonl"), []byte(
		`{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"bide in the bothy"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm.state.Sessions["bothy-migrate"] = &SessionState{
		ID: "bothy-migrate", Name: "bothy-migrate", Agent: "claude", AgentSessionID: sid,
		Status: StatusRunning, WorktreePath: repo, RepoPath: repo, CreatedAt: time.Now(),
	}
	sm.sessions["bothy-migrate"] = driver

	if _, err := sm.Migrate("bothy-migrate", "codex", "", 24, 80); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	t.Cleanup(func() { stopRunnableOrchestrator(t, sm, "bothy-migrate") })
	assertFakeDriverEscalated(t, driver, grace)
}

// TestKillProcessGroupHonoursGrace proves the [lifecycle] process_kill_grace
// drives the SIGTERM→SIGKILL escalation: a process group that ignores SIGTERM is
// force-killed after the configured grace, and killProcessGroup returns only once
// the group is gone. A short grace keeps the test quick while still exercising the
// full escalation.
func TestKillProcessGroupHonoursGrace(t *testing.T) {
	// The shell ignores SIGTERM and re-spawns its sleep, so the group as a whole
	// survives SIGTERM and only dies on the SIGKILL that follows the grace.
	cmd := exec.Command("sh", "-c", "trap '' TERM; while true; do sleep 0.05; done")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start term-ignoring sleeper: %v", err)
	}

	pid := cmd.Process.Pid
	done := make(chan struct{})

	go func() { _ = cmd.Wait(); close(done) }()

	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)

		<-done
	})

	// Let the shell install its SIGTERM trap before we signal, otherwise the
	// default disposition kills it during startup and the grace never elapses.
	time.Sleep(250 * time.Millisecond)

	start := time.Now()

	if err := killProcessGroup(pid, 400*time.Millisecond); err != nil {
		t.Fatalf("killProcessGroup = %v, want nil", err)
	}

	// SIGTERM is ignored, so it must have escalated to SIGKILL after the grace —
	// well before the built-in 5s default would have fired.
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("returned after %v, want >= ~400ms grace (escalated too early)", elapsed)
	} else if elapsed > 3*time.Second {
		t.Errorf("returned after %v, want ~400ms grace (did not honour the short grace)", elapsed)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("process group still alive after killProcessGroup returned")
	}
}

// TestWatchdogMaxRestartsConfigured proves the [launch] max_restarts budget — not
// the historical hard-coded 3 — decides when the startup watchdog gives up. With
// a budget of 1, a session that has already been restarted once is errored rather
// than restarted again.
func TestWatchdogMaxRestartsConfigured(t *testing.T) {
	sm := newLaunchTestSM(t, config.LaunchConfig{StartupTimeout: "2m", MaxRestarts: 1})

	restarted := false
	sm.restartStuck = func(string, uint16, uint16) error { restarted = true; return nil }

	pty := startSleeper(t, sm, "thrawn")
	sm.state.Sessions["thrawn"] = &SessionState{
		ID: "thrawn", Name: "thrawn", Status: StatusRunning,
		AgentStatus: "unknown", StatusChangedAt: time.Now().Add(-5 * time.Minute),
		StuckRestarts: 1, // already at the configured budget
	}
	sm.sessions["thrawn"] = pty

	sm.recoverStuckLaunch(stuckSession{id: "thrawn", name: "thrawn", attempts: 1, pty: pty}, 2*time.Minute)

	if restarted {
		t.Fatal("watchdog restarted despite reaching the configured max_restarts=1 budget")
	}

	<-pty.Done()

	if s := sm.state.Sessions["thrawn"]; s.Status != StatusErrored {
		t.Errorf("Status = %q, want %q once the configured budget is exhausted", s.Status, StatusErrored)
	}
}

// TestWatchdogMaxRestartsAllowsUpToBudget proves a higher configured budget lets
// the watchdog keep restarting: with max_restarts=5 a session at 3 prior restarts
// is restarted again rather than errored.
func TestWatchdogMaxRestartsAllowsUpToBudget(t *testing.T) {
	sm := newLaunchTestSM(t, config.LaunchConfig{StartupTimeout: "2m", MaxRestarts: 5})

	restarted := false
	sm.restartStuck = func(string, uint16, uint16) error { restarted = true; return nil }

	pty := startSleeper(t, sm, "canny")
	sm.state.Sessions["canny"] = &SessionState{
		ID: "canny", Name: "canny", Status: StatusRunning,
		AgentStatus: "unknown", StatusChangedAt: time.Now().Add(-5 * time.Minute),
		StuckRestarts: 3,
	}
	sm.sessions["canny"] = pty

	sm.recoverStuckLaunch(stuckSession{id: "canny", name: "canny", attempts: 3, pty: pty}, 2*time.Minute)

	if !restarted {
		t.Fatal("watchdog gave up at 3 restarts despite the configured max_restarts=5 budget")
	}

	if s := sm.state.Sessions["canny"]; s.StuckRestarts != 4 {
		t.Errorf("StuckRestarts = %d, want 4 (incremented on restart)", s.StuckRestarts)
	}
}

// TestMassExitThresholdConfigured proves the [lifecycle] mass_exit_threshold
// drives when the mass-exit warning fires. A capturing logger records the warn
// line; with a threshold of 3, the third exit within the window triggers it.
func TestMassExitThresholdConfigured(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.Lifecycle.MassExitThreshold = 3
	sm.cfg.Lifecycle.MassExitWindow = "1m"

	var logs syncBuffer

	sm.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))

	const warnMsg = "mass session exit detected"

	sm.mu.Lock()
	for i := 0; i < 2; i++ {
		sm.recordExit()
	}
	sm.mu.Unlock()

	if strings.Contains(logs.String(), warnMsg) {
		t.Fatal("mass-exit warning fired before reaching the configured threshold of 3")
	}

	sm.mu.Lock()
	sm.recordExit()
	sm.mu.Unlock()

	if !strings.Contains(logs.String(), warnMsg) {
		t.Fatal("mass-exit warning did not fire at the configured threshold of 3")
	}
}
