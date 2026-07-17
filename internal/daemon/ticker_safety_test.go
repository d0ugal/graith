package daemon

import (
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// TestDaemonLoopTickersRejectNonPositiveCadence is the daemon-construction
// regression guard for issue #1285: the four newly-configurable loop cadences
// (pr_watch.advanced.base_tick / ref_reconcile_interval and
// triggers.advanced.scheduler_tick / watch_reconcile_interval) are read via the
// SessionManager config accessors and fed straight to time.NewTicker, which
// panics on a non-positive interval. It sets every one of them to a non-positive
// value, builds a SessionManager exactly as the daemon would, and reproduces the
// four ticker constructions from prwatch.go, prrefwatch.go, trigger.go, and
// filewatch.go. Before the fix the accessors returned the parsed 0/negative and
// time.NewTicker panicked; now they fall back to their positive defaults.
func TestDaemonLoopTickersRejectNonPositiveCadence(t *testing.T) {
	for _, bad := range []string{"0", "0s", "-1s"} {
		t.Run(bad, func(t *testing.T) {
			cfg := config.Default()
			cfg.PRWatch.Advanced.BaseTick = bad
			cfg.PRWatch.Advanced.RefReconcileInterval = bad
			cfg.TriggersRuntime.Advanced.SchedulerTick = bad
			cfg.TriggersRuntime.Advanced.WatchReconcileInterval = bad

			sm := newSMWithConfig(t, cfg)

			// These expressions mirror the daemon loops verbatim; a non-positive
			// value would panic time.NewTicker.
			tickers := []struct {
				name string
				d    time.Duration
			}{
				{"prwatch.base_tick", sm.Config().PRWatch.BaseTickDuration()},
				{"prrefwatch.ref_reconcile_interval", sm.Config().PRWatch.RefReconcileIntervalDuration()},
				{"trigger.scheduler_tick", sm.Config().TriggersRuntime.SchedulerTickDuration()},
				{"filewatch.watch_reconcile_interval", sm.Config().TriggersRuntime.WatchReconcileIntervalDuration()},
			}

			for _, tk := range tickers {
				if tk.d <= 0 {
					t.Fatalf("%s cadence for %q = %v, must be > 0 (would panic time.NewTicker)", tk.name, bad, tk.d)
				}

				// Construct and immediately stop, proving the daemon's exact call
				// cannot panic on the accepted config.
				ticker := time.NewTicker(tk.d)
				ticker.Stop()
			}
		})
	}
}
