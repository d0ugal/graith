package daemon

import (
	"context"
	"fmt"
	"time"
)

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
			return fmt.Errorf("driver did not finish within %s after SIGKILL (SIGTERM error: %v; SIGKILL error: %v)", grace, termErr, forceErr)
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
