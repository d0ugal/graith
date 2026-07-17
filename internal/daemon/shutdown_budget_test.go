package daemon

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func TestSaturatingScaleDuration(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		n    int64
		want time.Duration
	}{
		{"normal", 5 * time.Second, 3, 15 * time.Second},
		{"zero duration", 0, 3, 0},
		{"zero factor", time.Second, 0, 0},
		{"overflow saturates", time.Duration(math.MaxInt64 - 10), 3, math.MaxInt64},
		{"exact-fit no overflow", time.Duration(math.MaxInt64 / 3), 3, time.Duration(math.MaxInt64/3) * 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := saturatingScaleDuration(tc.d, tc.n); got != tc.want {
				t.Errorf("saturatingScaleDuration(%v, %d) = %v, want %v", tc.d, tc.n, got, tc.want)
			}
		})
	}
}

// TestShutdownBudgetSaturatesOnExtremeGrace guards issue #1243 round-4: a valid
// but very large process_kill_grace must not make the 3× shutdown budget wrap
// negative (which would cancel shutdown immediately and force an instant SIGKILL).
func TestShutdownBudgetSaturatesOnExtremeGrace(t *testing.T) {
	sm := newLiveDriverLifecycleTestManager(t, time.Second)
	// ~292 years, just under math.MaxInt64 ns; 3× overflows int64.
	sm.cfg.Lifecycle.ProcessKillGrace = "2562047h"

	grace := sm.cfg.Lifecycle.ProcessKillGraceDuration()
	budget := sm.shutdownBudget()

	if budget < grace {
		t.Fatalf("shutdown budget %v < grace %v: the 3× scaling wrapped negative", budget, grace)
	}

	if budget != math.MaxInt64 {
		t.Fatalf("shutdown budget = %v, want saturation to math.MaxInt64", budget)
	}
}

// TestCreateOrchestratorUsesOneConfigGenerationAcrossReload guards issue #1243
// round-4: a reload landing mid-launch must not let the orchestrator combine its
// launch-time agent snapshot with lifecycle values from the reloaded generation.
// The launched PTY's geometry must come from the launch generation.
func TestCreateOrchestratorUsesOneConfigGenerationAcrossReload(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}

	agent := config.Agent{Command: "/bin/sh", Args: []string{"-c", "sleep 30"}}
	sm := newRunnableOrchestratorTestManager(t, "bothy-agent", agent)

	sm.mu.Lock()
	sm.cfg.Lifecycle.DefaultRows = 11
	sm.cfg.Lifecycle.DefaultCols = 13
	oldCfg := sm.cfg
	sm.mu.Unlock()

	entered := make(chan *config.Config, 1)
	release := make(chan struct{})
	sm.launchPhase2Hook = func(op string, snap *config.Config) {
		if op != "orchestrator" {
			return
		}

		entered <- snap

		<-release
	}

	type result struct {
		sess SessionState
		err  error
	}

	done := make(chan result, 1)

	go func() {
		s, err := sm.createOrchestrator(context.Background())
		done <- result{s, err}
	}()

	snap := <-entered
	if snap != oldCfg {
		t.Fatalf("phase-2 snapshot %p is not the launch generation %p", snap, oldCfg)
	}

	// Reload with a different geometry while the launch is paused.
	newCfg := *oldCfg
	newCfg.Lifecycle.DefaultRows = 40
	newCfg.Lifecycle.DefaultCols = 100

	if err := sm.applyConfig(&newCfg); err != nil {
		t.Fatalf("apply reloaded config: %v", err)
	}

	close(release)

	got := <-done
	if got.err != nil {
		t.Fatalf("createOrchestrator: %v", got.err)
	}

	t.Cleanup(func() { stopRunnableOrchestrator(t, sm, got.sess.ID) })

	driver, ok := sm.GetPTY(got.sess.ID)
	if !ok {
		t.Fatal("orchestrator has no live driver")
	}

	shot := driver.ScreenSnapshot()
	if shot.Rows != 11 || shot.Cols != 13 {
		t.Fatalf("orchestrator geometry = %dx%d, want the launch generation 11x13 (config generations were mixed)", shot.Rows, shot.Cols)
	}
}
