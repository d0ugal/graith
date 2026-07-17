package daemon

import (
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// startSleeperGroup starts a real process in its own process group and returns
// the command. The caller owns killing it (a t.Cleanup does). It exercises
// processGroupGone against a genuinely-present, signalable group.
func startSleeperGroup(t *testing.T) *exec.Cmd {
	t.Helper()

	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}

	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	return cmd
}

// withProcKill swaps the signal seam for the duration of a test so signal
// outcomes real processes cannot produce on demand (EPERM, a failed SIGKILL, a
// delayed death) can be exercised deterministically.
func withProcKill(t *testing.T, fn func(pid int, sig syscall.Signal) error) {
	t.Helper()

	orig := procKill
	procKill = fn

	t.Cleanup(func() { procKill = orig })
}

// TestKillProcessGroupTreatsEPERMAsAlive proves an EPERM probe/SIGKILL is never
// mistaken for a dead group: killProcessGroup must return an error rather than
// report success and let the caller tear down a live process's worktree (#1326).
func TestKillProcessGroupTreatsEPERMAsAlive(t *testing.T) {
	withProcKill(t, func(_ int, sig syscall.Signal) error {
		if sig == syscall.SIGTERM {
			return nil
		}

		return syscall.EPERM // probe (signal 0) and SIGKILL: present but unsignalable
	})

	if err := killProcessGroup(9999, 20*time.Millisecond); err == nil {
		t.Fatal("killProcessGroup returned nil for a group that only ever reports EPERM")
	}
}

// TestKillProcessGroupSurfacesUnconfirmedDeath proves that a SIGKILL reporting no
// error is still not treated as success while the group is present at the
// post-kill confirmation deadline (issue #1326).
func TestKillProcessGroupSurfacesUnconfirmedDeath(t *testing.T) {
	withProcKill(t, func(int, syscall.Signal) error {
		// Every signal and probe "succeeds"; the group never disappears (never ESRCH).
		return nil
	})

	if err := killProcessGroup(9999, 20*time.Millisecond); err == nil {
		t.Fatal("killProcessGroup returned nil for a group still present after SIGKILL")
	}
}

// TestKillProcessGroupConfirmsDelayedDeath proves the bounded post-signal wait
// keeps polling until absence is confirmed: a group that only disappears after a
// few probes is a success, not a timeout (issue #1326).
func TestKillProcessGroupConfirmsDelayedDeath(t *testing.T) {
	var probes int32

	withProcKill(t, func(_ int, sig syscall.Signal) error {
		if sig != 0 {
			return nil // SIGTERM/SIGKILL accepted
		}

		if atomic.AddInt32(&probes, 1) >= 3 {
			return syscall.ESRCH // gone on the third probe
		}

		return nil // still alive for the first two probes
	})

	if err := killProcessGroup(9999, time.Second); err != nil {
		t.Fatalf("killProcessGroup = %v, want nil once delayed death is confirmed", err)
	}
}

// TestProcessGroupGoneRejectsLivePresentGroup ties the seam-based tests to
// reality: a live, signalable group is never reported gone, and a killed one is
// (issue #1326).
func TestProcessGroupGoneRejectsLivePresentGroup(t *testing.T) {
	cmd := startSleeperGroup(t)
	pid := cmd.Process.Pid

	if processGroupGone(-pid, 100*time.Millisecond) {
		t.Fatal("a live signalable group was reported gone")
	}

	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = cmd.Wait()

	if !processGroupGone(-pid, 2*time.Second) {
		t.Fatal("a killed group was not confirmed gone")
	}
}
