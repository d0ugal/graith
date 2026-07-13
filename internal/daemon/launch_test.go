package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newLaunchTestSM builds a minimal SessionManager for launch throttle/watchdog
// tests with a given [launch] config.
func newLaunchTestSM(t *testing.T, launch config.LaunchConfig) *SessionManager {
	t.Helper()

	cfg := config.Default()
	cfg.Launch = launch

	return &SessionManager{
		state:    NewState(),
		sessions: make(map[string]SessionDriver),
		cfg:      cfg,
		log:      quietLogger(),
		launch:   newLaunchThrottle(launch.MaxConcurrentOrDefault()),
	}
}

// --- launchThrottle unit tests ---

func TestLaunchThrottleBoundsConcurrency(t *testing.T) {
	lt := newLaunchThrottle(2)
	ctx := context.Background()

	s1, err := lt.acquire(ctx)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}

	s2, err := lt.acquire(ctx)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}

	if s2.inflight != 2 || s2.capacity != 2 {
		t.Fatalf("second slot inflight=%d capacity=%d, want 2/2", s2.inflight, s2.capacity)
	}

	// A third acquire must block until a slot frees.
	acquired := make(chan launchSlot, 1)

	go func() {
		s3, aerr := lt.acquire(ctx)
		if aerr == nil {
			acquired <- s3
		}
	}()

	select {
	case <-acquired:
		t.Fatal("third acquire should block while 2 slots are held")
	case <-time.After(100 * time.Millisecond):
	}

	s1.release()

	select {
	case s3 := <-acquired:
		s3.release()
	case <-time.After(time.Second):
		t.Fatal("third acquire should proceed after a slot is released")
	}

	s2.release()
}

func TestLaunchThrottleReleaseIdempotent(t *testing.T) {
	lt := newLaunchThrottle(1)

	slot, err := lt.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	slot.release()
	slot.release() // must not panic or over-drain

	// The single slot must be reusable after the double release.
	done := make(chan struct{})

	go func() {
		s2, _ := lt.acquire(context.Background())
		s2.release()

		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slot not freed after idempotent double release")
	}
}

func TestLaunchThrottleAcquireContextCancel(t *testing.T) {
	lt := newLaunchThrottle(1)

	held, err := lt.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	defer held.release()

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)

	go func() {
		_, aerr := lt.acquire(ctx)
		errCh <- aerr
	}()

	cancel()

	select {
	case aerr := <-errCh:
		if aerr == nil {
			t.Fatal("blocked acquire should fail when its context is cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled acquire did not return")
	}

	// waiting counter must have been decremented on the cancel path.
	lt.mu.Lock()
	waiting := lt.waiting
	lt.mu.Unlock()

	if waiting != 0 {
		t.Fatalf("waiting = %d after cancel, want 0", waiting)
	}
}

func TestLaunchThrottleResize(t *testing.T) {
	lt := newLaunchThrottle(1)

	// Hold the only slot against the old (cap-1) channel.
	old, err := lt.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Grow to 2: a fresh channel is swapped in, so two new acquires succeed
	// even while the old slot is still held.
	lt.resize(2)

	a, err := lt.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire after resize: %v", err)
	}

	b, err := lt.acquire(context.Background())
	if err != nil {
		t.Fatalf("second acquire after resize: %v", err)
	}

	if b.capacity != 2 {
		t.Fatalf("post-resize capacity = %d, want 2", b.capacity)
	}

	// Releasing the old slot against the old channel must not panic.
	old.release()
	a.release()
	b.release()
}

// --- releaseLaunchSlotWhenSettled tests ---

// startSleeper spawns a PTY that produces no output (sleep), simulating a
// launch-stuck session.
func startSleeper(t *testing.T, sm *SessionManager, id string) *grpty.Session {
	t.Helper()

	logDir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}

	sess, err := grpty.NewSession(grpty.SessionOpts{
		ID: id, Command: "sleep", Args: []string{"60"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: filepath.Join(logDir, id+".log"),
	})
	if err != nil {
		t.Fatalf("start sleeper pty: %v", err)
	}

	t.Cleanup(func() { _ = sess.Kill() })

	return sess
}

// startTalker spawns a PTY that emits output then sleeps, simulating a healthy
// launched session.
func startTalker(t *testing.T, id string) *grpty.Session {
	t.Helper()

	logDir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}

	sess, err := grpty.NewSession(grpty.SessionOpts{
		ID: id, Command: "sh", Args: []string{"-c", "echo bonnie; sleep 60"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: filepath.Join(logDir, id+".log"),
	})
	if err != nil {
		t.Fatalf("start talker pty: %v", err)
	}

	t.Cleanup(func() { _ = sess.Kill() })

	return sess
}

func TestReleaseLaunchSlotWhenSettledFirstOutput(t *testing.T) {
	// Long settle timeout: the slot should be released well before it elapses,
	// because the session produces output almost immediately.
	sm := newLaunchTestSM(t, config.LaunchConfig{MaxConcurrent: 1, SettleTimeout: "30s"})

	slot, err := sm.launch.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	sess := startTalker(t, "bonnie")
	start := time.Now()

	sm.releaseLaunchSlotWhenSettled(slot, "bonnie", "bonnie", sess)

	// The slot should free once the echo lands, far sooner than 30s.
	waitSlotFree(t, sm.launch, 5*time.Second)

	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("slot released after %s, expected release on first output", elapsed)
	}
}

func TestReleaseLaunchSlotWhenSettledTimeout(t *testing.T) {
	// Short settle timeout, silent session: the slot must free on the timeout.
	launchSlotPollInterval = 20 * time.Millisecond

	t.Cleanup(func() { launchSlotPollInterval = 100 * time.Millisecond })

	sm := newLaunchTestSM(t, config.LaunchConfig{MaxConcurrent: 1, SettleTimeout: "300ms"})

	slot, err := sm.launch.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	sess := startSleeper(t, sm, "dreich")

	// Slot is still held immediately after handing it off.
	if slotFree(sm.launch) {
		t.Fatal("slot should still be held right after settle handoff")
	}

	sm.releaseLaunchSlotWhenSettled(slot, "dreich", "dreich", sess)

	waitSlotFree(t, sm.launch, 3*time.Second)
}

func TestReleaseLaunchSlotSettleDisabled(t *testing.T) {
	sm := newLaunchTestSM(t, config.LaunchConfig{MaxConcurrent: 1, SettleTimeout: "0"})

	slot, err := sm.launch.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	sess := startSleeper(t, sm, "neep")
	sm.releaseLaunchSlotWhenSettled(slot, "neep", "neep", sess)

	// With settle disabled the slot frees right after spawn, even though the
	// session never produces output.
	waitSlotFree(t, sm.launch, time.Second)
}

func slotFree(lt *launchThrottle) bool {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	return len(lt.sem) == 0
}

func waitSlotFree(t *testing.T, lt *launchThrottle, within time.Duration) {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if slotFree(lt) {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("launch slot not freed within %s", within)
}

// --- startup watchdog tests ---

func TestStuckLaunchCandidates(t *testing.T) {
	sm := newLaunchTestSM(t, config.LaunchConfig{StartupTimeout: "2m"})

	timeout := 2 * time.Minute
	old := time.Now().Add(-5 * time.Minute)
	recent := time.Now().Add(-10 * time.Second)

	// Stuck: running, unknown, no output, old, live silent PTY.
	stuckPty := startSleeper(t, sm, "thrawn")
	sm.state.Sessions["thrawn"] = &SessionState{
		ID: "thrawn", Name: "thrawn", Status: StatusRunning,
		AgentStatus: "unknown", StatusChangedAt: old, PID: 1234,
	}
	sm.sessions["thrawn"] = stuckPty

	// Recent: too young to be considered stuck.
	recentPty := startSleeper(t, sm, "canny")
	sm.state.Sessions["canny"] = &SessionState{
		ID: "canny", Name: "canny", Status: StatusRunning,
		AgentStatus: "unknown", StatusChangedAt: recent,
	}
	sm.sessions["canny"] = recentPty

	// Active: has an agent status other than unknown.
	activePty := startSleeper(t, sm, "braw")
	sm.state.Sessions["braw"] = &SessionState{
		ID: "braw", Name: "braw", Status: StatusRunning,
		AgentStatus: "active", StatusChangedAt: old,
	}
	sm.sessions["braw"] = activePty

	// Orchestrator: excluded (own supervisor).
	orchPty := startSleeper(t, sm, "ben")
	sm.state.Sessions["ben"] = &SessionState{
		ID: "ben", Name: "orchestrator", Status: StatusRunning,
		SystemKind: SystemKindOrchestrator, AgentStatus: "unknown", StatusChangedAt: old,
	}
	sm.sessions["ben"] = orchPty

	// Stopped: not running.
	sm.state.Sessions["auld"] = &SessionState{
		ID: "auld", Name: "auld", Status: StatusStopped, StatusChangedAt: old,
	}

	got := sm.stuckLaunchCandidates(time.Now(), timeout)

	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(got), got)
	}

	if got[0].id != "thrawn" {
		t.Fatalf("stuck candidate = %q, want thrawn", got[0].id)
	}
}

func TestStuckLaunchCandidatesSkipsSessionWithOutput(t *testing.T) {
	sm := newLaunchTestSM(t, config.LaunchConfig{StartupTimeout: "2m"})

	old := time.Now().Add(-5 * time.Minute)

	// Live PTY that has produced output — must be skipped even though state
	// still shows unknown/no-output (detection loop hasn't caught up).
	talker := startTalker(t, "bonnie")
	waitForOutput(t, talker, 3*time.Second)

	sm.state.Sessions["bonnie"] = &SessionState{
		ID: "bonnie", Name: "bonnie", Status: StatusRunning,
		AgentStatus: "unknown", StatusChangedAt: old,
	}
	sm.sessions["bonnie"] = talker

	got := sm.stuckLaunchCandidates(time.Now(), 2*time.Minute)
	if len(got) != 0 {
		t.Fatalf("session with live output should not be stuck, got %+v", got)
	}
}

// TestStuckLaunchCandidatesHistoricalOutput is the regression test for the
// resume blind spot: a session that emitted output in an earlier process life
// carries a non-nil persisted SessionState.LastOutputAt, but if its current
// (resumed) PTY is silent and stuck it must still be a watchdog candidate. The
// live PTY — not the persisted timestamp — is the source of truth.
func TestStuckLaunchCandidatesHistoricalOutput(t *testing.T) {
	sm := newLaunchTestSM(t, config.LaunchConfig{StartupTimeout: "2m"})

	old := time.Now().Add(-5 * time.Minute)
	past := time.Now().Add(-1 * time.Hour)

	silentPty := startSleeper(t, sm, "thrawn")
	sm.state.Sessions["thrawn"] = &SessionState{
		ID: "thrawn", Name: "thrawn", Status: StatusRunning,
		AgentStatus: "unknown", StatusChangedAt: old,
		LastOutputAt: &past, // produced output in a PREVIOUS process life
	}
	sm.sessions["thrawn"] = silentPty

	got := sm.stuckLaunchCandidates(time.Now(), 2*time.Minute)
	if len(got) != 1 || got[0].id != "thrawn" {
		t.Fatalf("session with historical (but not live) output must still be stuck, got %+v", got)
	}
}

func TestCheckStuckLaunchesDisabled(t *testing.T) {
	sm := newLaunchTestSM(t, config.LaunchConfig{StartupTimeout: "0"})

	restarted := false
	sm.restartStuck = func(string, uint16, uint16) error { restarted = true; return nil }

	stuckPty := startSleeper(t, sm, "thrawn")
	sm.state.Sessions["thrawn"] = &SessionState{
		ID: "thrawn", Name: "thrawn", Status: StatusRunning,
		AgentStatus: "unknown", StatusChangedAt: time.Now().Add(-time.Hour),
	}
	sm.sessions["thrawn"] = stuckPty

	sm.checkStuckLaunches(context.Background())

	if restarted {
		t.Fatal("watchdog must not act when startup_timeout is 0 (disabled)")
	}
}

func TestCheckStuckLaunchesRestartsAndMarksFresh(t *testing.T) {
	sm := newLaunchTestSM(t, config.LaunchConfig{StartupTimeout: "2m"})

	var (
		mu    sync.Mutex
		calls []string
	)

	sm.restartStuck = func(id string, _, _ uint16) error {
		mu.Lock()

		calls = append(calls, id)

		mu.Unlock()

		return nil
	}

	stuckPty := startSleeper(t, sm, "thrawn")
	sm.state.Sessions["thrawn"] = &SessionState{
		ID: "thrawn", Name: "thrawn", Status: StatusRunning,
		AgentStatus: "unknown", StatusChangedAt: time.Now().Add(-5 * time.Minute),
	}
	sm.sessions["thrawn"] = stuckPty

	sm.checkStuckLaunches(context.Background())

	mu.Lock()
	gotCalls := make([]string, len(calls))
	copy(gotCalls, calls)
	mu.Unlock()

	if len(gotCalls) != 1 || gotCalls[0] != "thrawn" {
		t.Fatalf("restart calls = %v, want [thrawn]", gotCalls)
	}

	s := sm.state.Sessions["thrawn"]
	if !s.FreshStart {
		t.Error("stuck session should be marked FreshStart for recovery")
	}

	if s.StuckRestarts != 1 {
		t.Errorf("StuckRestarts = %d, want 1", s.StuckRestarts)
	}
}

func TestRecoverStuckLaunchBudgetExhausted(t *testing.T) {
	sm := newLaunchTestSM(t, config.LaunchConfig{StartupTimeout: "2m"})

	restarted := false
	sm.restartStuck = func(string, uint16, uint16) error { restarted = true; return nil }

	pty := startSleeper(t, sm, "thrawn")
	sm.state.Sessions["thrawn"] = &SessionState{
		ID: "thrawn", Name: "thrawn", Status: StatusRunning,
		AgentStatus: "unknown", StatusChangedAt: time.Now().Add(-5 * time.Minute),
		StuckRestarts: maxStuckRestarts,
	}
	sm.sessions["thrawn"] = pty

	sm.recoverStuckLaunch(stuckSession{
		id: "thrawn", name: "thrawn", attempts: maxStuckRestarts, pty: pty,
	}, 2*time.Minute)

	if restarted {
		t.Fatal("watchdog must not restart once the budget is exhausted")
	}

	// The zombie PTY should have been killed.
	<-pty.Done()

	s := sm.state.Sessions["thrawn"]
	if s.SummaryText == "" {
		t.Error("budget-exhausted session should carry a lifecycle summary")
	}

	if s.Status != StatusErrored {
		t.Errorf("Status = %q, want %q for budget-exhausted session", s.Status, StatusErrored)
	}

	if s.StopReason != StopReasonWatchdog {
		t.Errorf("StopReason = %q, want %q", s.StopReason, StopReasonWatchdog)
	}
}

// TestRecoverStuckLaunchSkipsWhenPTYReplaced covers the TOCTOU guard: if the
// live PTY has been swapped for a different (healthy) one between candidate
// selection and recovery, the watchdog must not act on the stale snapshot.
func TestRecoverStuckLaunchSkipsWhenPTYReplaced(t *testing.T) {
	sm := newLaunchTestSM(t, config.LaunchConfig{StartupTimeout: "2m"})

	restarted := false
	sm.restartStuck = func(string, uint16, uint16) error { restarted = true; return nil }

	stalePty := startSleeper(t, sm, "thrawn-stale")
	livePty := startSleeper(t, sm, "thrawn-live")

	sm.state.Sessions["thrawn"] = &SessionState{
		ID: "thrawn", Name: "thrawn", Status: StatusRunning,
		AgentStatus: "unknown", StatusChangedAt: time.Now().Add(-5 * time.Minute),
	}
	// The manager now holds a DIFFERENT pty than the one snapshotted.
	sm.sessions["thrawn"] = livePty

	sm.recoverStuckLaunch(stuckSession{
		id: "thrawn", name: "thrawn", attempts: 0, pty: stalePty,
	}, 2*time.Minute)

	if restarted {
		t.Fatal("watchdog must not restart when the live PTY differs from the snapshot")
	}

	if s := sm.state.Sessions["thrawn"]; s.FreshStart || s.StuckRestarts != 0 {
		t.Errorf("stale-snapshot recovery must not mutate state: FreshStart=%v StuckRestarts=%d", s.FreshStart, s.StuckRestarts)
	}
}

func TestResetStuckRestartsLocked(t *testing.T) {
	s := &SessionState{StuckRestarts: 2}
	resetStuckRestartsLocked(s)

	if s.StuckRestarts != 0 {
		t.Fatalf("StuckRestarts = %d after reset, want 0", s.StuckRestarts)
	}
}

// TestResumeClearsFreshStart is the regression test for the watchdog FreshStart
// leak: the watchdog sets FreshStart on a plain session to force a fresh
// recovery start, but the flag must be cleared once a start consumes it, or
// every later user resume would silently start fresh and discard conversation
// history. Before the fix, FreshStart was only cleared for orchestrator/seed
// resumes, so a watchdog-set flag on a plain session leaked forever.
func TestResumeClearsFreshStart(t *testing.T) {
	// os.MkdirTemp (not t.TempDir): writeFileAtomic's syncDir can leave a
	// recently-closed dir fd that races t.TempDir's strict RemoveAll on macOS.
	tmpDir, err := os.MkdirTemp("", "TestResumeClearsFreshStart")
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		for range 5 {
			if err := os.RemoveAll(tmpDir); err == nil {
				return
			}

			time.Sleep(50 * time.Millisecond)
		}
	}()

	cfg := config.Default()
	cfg.Agents["claude"] = config.Agent{
		Command:    "true",
		Args:       []string{},
		ResumeArgs: []string{},
	}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(tmpDir, "state.json"),
		DataDir:   tmpDir,
		LogDir:    tmpDir,
	}, quietLogger())

	id := "bide-fresh"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "bide-fresh", Status: StatusStopped, Agent: "claude",
		WorktreePath: tmpDir,
		FreshStart:   true, // as the watchdog would leave it before recovery
	}

	if _, rerr := sm.Resume(id, 24, 80); rerr != nil {
		t.Fatalf("Resume() error = %v", rerr)
	}

	sm.mu.RLock()
	fresh := sm.state.Sessions[id].FreshStart
	ptySess := sm.sessions[id]
	sm.mu.RUnlock()

	if fresh {
		t.Error("FreshStart must be cleared after a resume consumes it, else future resumes discard history")
	}

	if ptySess != nil {
		<-ptySess.Done()
		ptySess.Close()
	}
}

func waitForOutput(t *testing.T, sess *grpty.Session, within time.Duration) {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !sess.LastOutputAt().IsZero() {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("session produced no output in time")
}
