package daemon

import (
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// TestDaemonTickerCadencesNeverNonPositive proves the four advanced loop
// cadences that feed time.NewTicker resolve positive even when the config sets
// them to "0", "0s", or a negative duration, so the daemon's poll/reconcile
// loops can never panic with "non-positive interval for NewTicker" (issue
// #1285). It constructs the tickers exactly as prwatch.go / prrefwatch.go /
// trigger.go / filewatch.go do.
func TestDaemonTickerCadencesNeverNonPositive(t *testing.T) {
	cfg := config.Default()
	cfg.PRWatch.Advanced.BaseTick = "0s"
	cfg.PRWatch.Advanced.RefReconcileInterval = "-1s"
	cfg.TriggersRuntime.Advanced.SchedulerTick = "0"
	cfg.TriggersRuntime.Advanced.WatchReconcileInterval = "-2m"

	sm := newSMWithConfig(t, cfg)

	cadences := map[string]time.Duration{
		"pr_watch.advanced.base_tick":                sm.Config().PRWatch.BaseTickDuration(),
		"pr_watch.advanced.ref_reconcile_interval":   sm.Config().PRWatch.RefReconcileIntervalDuration(),
		"triggers.advanced.scheduler_tick":           sm.Config().TriggersRuntime.SchedulerTickDuration(),
		"triggers.advanced.watch_reconcile_interval": sm.Config().TriggersRuntime.WatchReconcileIntervalDuration(),
	}

	for name, d := range cadences {
		if d <= 0 {
			t.Fatalf("%s resolved to %v; time.NewTicker would panic", name, d)
		}

		// Construct and immediately stop the ticker the loop would build; a
		// non-positive interval would panic here.
		tk := time.NewTicker(d)
		tk.Stop()
	}
}
