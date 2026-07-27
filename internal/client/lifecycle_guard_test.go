package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemonservice"
	"github.com/d0ugal/graith/internal/testprocess"
)

func allowDaemonLifecycleMutation(string) error { return nil }

func TestStopDaemonIdentityRejectsGoTestBeforeSignal(t *testing.T) {
	signalCalled := false

	err := stopDaemonIdentityWith(
		4242,
		99,
		testprocess.RefuseDaemonLifecycleMutation,
		func(int) (int64, error) { return 99, nil },
		func(int, syscall.Signal) error {
			signalCalled = true
			return nil
		},
		func(time.Duration, func(time.Time) bool) bool { return true },
	)
	if err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("stopDaemonIdentityWith() error = %v, want Go-test refusal", err)
	}

	if signalCalled {
		t.Fatal("Go-test refusal reached the signal primitive")
	}
}

func TestStopDaemonByPIDRejectsGoTestBeforePIDFileAccess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "dreich.pid")
	if err := os.WriteFile(pidFile, []byte("not a pid"), 0o600); err != nil {
		t.Fatal(err)
	}

	stopped, err := stopDaemonByPID(pidFile)
	if stopped || err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("stopDaemonByPID() = (%t, %v), want Go-test refusal", stopped, err)
	}

	if data, readErr := os.ReadFile(pidFile); readErr != nil || string(data) != "not a pid" {
		t.Fatalf("refused stop changed PID file: data=%q err=%v", data, readErr)
	}
}

func TestPrepareDaemonCleanRestartRejectsGoTestBeforeResolution(t *testing.T) {
	original := detectDaemonServiceModeForCleanRestart
	called := false
	detectDaemonServiceModeForCleanRestart = func() (daemonservice.Mode, string, error) {
		called = true
		return daemonservice.ModeManaged, "braw", nil
	}

	t.Cleanup(func() { detectDaemonServiceModeForCleanRestart = original })

	err := PrepareDaemonCleanRestart(context.Background(), config.Paths{Profile: "canny"})
	if err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("PrepareDaemonCleanRestart() error = %v, want Go-test refusal", err)
	}

	if called {
		t.Fatal("Go-test refusal reached managed-service resolution")
	}
}

func TestRequestUpgradeRejectsGoTestBeforeCandidateResolution(t *testing.T) {
	original := resolveUpgradeCandidateForClient
	called := false
	resolveUpgradeCandidateForClient = func(context.Context, string, string, string, int) (string, bool, error) {
		called = true
		return "", false, errors.New("dreich")
	}

	t.Cleanup(func() { resolveUpgradeCandidateForClient = original })

	requested, managed, err := requestUpgrade(context.Background(), nil)
	if requested || managed || err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("requestUpgrade() = (%t, %t, %v), want Go-test refusal", requested, managed, err)
	}

	if called {
		t.Fatal("Go-test refusal reached managed upgrade candidate resolution")
	}
}

func TestStopDaemonIdentityAllowsProductionPathWithFakeSignal(t *testing.T) {
	var (
		signals    []syscall.Signal
		waitBudget time.Duration
	)

	err := stopDaemonIdentityWith(
		4242,
		99,
		allowDaemonLifecycleMutation,
		func(int) (int64, error) { return 99, nil },
		func(_ int, signal syscall.Signal) error {
			signals = append(signals, signal)
			if signal == 0 {
				return syscall.ESRCH
			}

			return nil
		},
		func(timeout time.Duration, check func(time.Time) bool) bool {
			waitBudget = timeout

			return check(time.Now())
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if waitBudget != daemonStopTimeout {
		t.Fatalf("stop wait budget = %s, want %s", waitBudget, daemonStopTimeout)
	}

	if len(signals) != 2 || signals[0] != syscall.SIGTERM || signals[1] != 0 {
		t.Fatalf("signals = %v, want [SIGTERM signal-0]", signals)
	}
}

func TestStopDaemonIdentityTimeoutErrorIsActionable(t *testing.T) {
	err := stopDaemonIdentityWith(
		4242,
		99,
		allowDaemonLifecycleMutation,
		func(int) (int64, error) { return 99, nil },
		func(int, syscall.Signal) error { return nil },
		func(timeout time.Duration, _ func(time.Time) bool) bool {
			if timeout != daemonStopTimeout {
				t.Fatalf("stop wait budget = %s, want %s", timeout, daemonStopTimeout)
			}

			return false
		},
	)
	if err == nil {
		t.Fatal("stopDaemonIdentityWith() succeeded despite timeout")
	}

	for _, want := range []string{"PID 4242", "shutdown may be wedged", "gr daemon restart --force", "kill -9 4242", "gr doctor --autofix"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("timeout error %q missing %q", err, want)
		}
	}
}

func TestStopDaemonIdentityForceEscalatesAfterShutdownBudget(t *testing.T) {
	alive := true

	var (
		signals []syscall.Signal
		waits   []time.Duration
	)

	err := stopDaemonIdentityWithMode(
		4242,
		99,
		allowDaemonLifecycleMutation,
		func(int) (int64, error) { return 99, nil },
		func(_ int, signal syscall.Signal) error {
			signals = append(signals, signal)
			if signal == syscall.SIGKILL {
				alive = false
			}

			if signal == 0 && !alive {
				return syscall.ESRCH
			}

			return nil
		},
		func(timeout time.Duration, check func(time.Time) bool) bool {
			waits = append(waits, timeout)

			return check(time.Now())
		},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	wantWaits := []time.Duration{daemonStopTimeout, daemonStopDiagnosticTimeout, daemonStopKillTimeout}
	if len(waits) != len(wantWaits) {
		t.Fatalf("waits = %v, want %v", waits, wantWaits)
	}

	for i := range wantWaits {
		if waits[i] != wantWaits[i] {
			t.Fatalf("waits = %v, want %v", waits, wantWaits)
		}
	}

	var delivered []syscall.Signal

	for _, signal := range signals {
		if signal != 0 {
			delivered = append(delivered, signal)
		}
	}

	wantSignals := []syscall.Signal{syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGKILL}
	if len(delivered) != len(wantSignals) {
		t.Fatalf("delivered signals = %v, want %v", delivered, wantSignals)
	}

	for i := range wantSignals {
		if delivered[i] != wantSignals[i] {
			t.Fatalf("delivered signals = %v, want %v", delivered, wantSignals)
		}
	}
}

func TestStopDaemonIdentityForceStopsBeforeEscalationOnPIDReuse(t *testing.T) {
	startTime := int64(99)

	var signals []syscall.Signal

	err := stopDaemonIdentityWithMode(
		4242,
		startTime,
		allowDaemonLifecycleMutation,
		func(int) (int64, error) { return startTime, nil },
		func(_ int, signal syscall.Signal) error {
			signals = append(signals, signal)
			return nil
		},
		func(_ time.Duration, check func(time.Time) bool) bool {
			startTime = 100

			return check(time.Now())
		},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, signal := range signals {
		if signal == syscall.SIGQUIT || signal == syscall.SIGKILL {
			t.Fatalf("signals = %v, want no escalation after PID reuse", signals)
		}
	}
}
