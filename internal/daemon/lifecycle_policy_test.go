package daemon

import (
	"log/slog"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

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
