package daemon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// shutdownBudget is the Run wrapper's outer deadline for whole-daemon shutdown,
// derived from the effective per-driver kill grace rather than a fixed wrapper
// (issue #1243). A driver teardown spends up to one grace on the SIGTERM→SIGKILL
// wait and another bounding post-SIGKILL completion (teardownLiveDriver), and
// StopAll then waits one further grace for exit watchers — three configured grace
// windows in total. Deriving the budget from the grace means a configured
// process_kill_grace above the old fixed 10s wrapper is honored to its SIGKILL
// transition instead of being cut off early.
//
// The 3× scaling saturates rather than overflows: a valid but very large
// process_kill_grace (Config.Validate accepts any positive duration) multiplied
// by three could otherwise wrap to a negative Duration, which would make the
// shutdown context cancel immediately and force an instant SIGKILL — the exact
// early cut-off deriving the budget was meant to prevent (issue #1243 round-4).
func (sm *SessionManager) shutdownBudget() time.Duration {
	return saturatingScaleDuration(sm.Config().Lifecycle.ProcessKillGraceDuration(), 3)
}

// saturatingScaleDuration returns d*n, clamped to math.MaxInt64 instead of
// overflowing to a negative Duration. d is expected positive (the grace
// accessor never returns a non-positive value); a non-positive d or n yields 0.
func saturatingScaleDuration(d time.Duration, n int64) time.Duration {
	if d <= 0 || n <= 0 {
		return 0
	}

	if d > math.MaxInt64/time.Duration(n) {
		return math.MaxInt64
	}

	return d * time.Duration(n)
}

// teardownLiveDriver applies the shared bounded lifecycle policy for removing a
// live session driver: SIGTERM, the configured process_kill_grace, SIGKILL, and
// one final grace-bounded completion wait. It closes driver handles only after
// Done reports completion; if even SIGKILL does not close Done, the launch-time
// watcher retains ownership so this lifecycle operation cannot wedge in Close.
//
// Ordinary Stop deliberately does not use this helper: it is a non-blocking
// SIGTERM request. ConvertToInteractive also keeps its distinct
// interrupt→TERM→KILL three-phase policy.
func (sm *SessionManager) teardownLiveDriver(ctx context.Context, driver SessionDriver) error {
	grace := sm.Config().Lifecycle.ProcessKillGraceDuration()

	if !driver.Exited() {
		termErr := driver.Kill()
		if waitDriverDoneWithContext(ctx, driver, grace) {
			driver.Close()
			return nil
		}

		forceErr := driver.ForceKill()
		// Once SIGKILL is requested, give process reaping/output drains their own
		// bounded completion window even when the caller's shutdown context was
		// what ended the gentler TERM phase.
		if !waitDriverDone(driver, grace) {
			timeoutErr := fmt.Errorf("driver did not finish within %s after SIGKILL", grace)
			if termErr != nil {
				termErr = fmt.Errorf("SIGTERM: %w", termErr)
			}

			if forceErr != nil {
				forceErr = fmt.Errorf("SIGKILL: %w", forceErr)
			}

			return errors.Join(timeoutErr, termErr, forceErr)
		}
	}

	driver.Close()

	return nil
}

func waitDriverDoneWithContext(ctx context.Context, driver SessionDriver, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-driver.Done():
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}
