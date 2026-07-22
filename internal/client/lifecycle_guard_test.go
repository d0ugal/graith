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
		func(func(time.Time) bool) bool { return true },
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
	var signals []syscall.Signal

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
		func(check func(time.Time) bool) bool { return check(time.Now()) },
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(signals) != 2 || signals[0] != syscall.SIGTERM || signals[1] != 0 {
		t.Fatalf("signals = %v, want [SIGTERM signal-0]", signals)
	}
}
