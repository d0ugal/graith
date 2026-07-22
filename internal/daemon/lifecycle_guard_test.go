package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/testprocess"
)

func allowDaemonLifecycleMutation(string) error { return nil }

func TestStopDaemonPIDRejectsGoTestBeforeMutationPrimitives(t *testing.T) {
	identityCalled := false
	stopCalled := false

	err := stopDaemonPIDWithGuard(
		4242,
		testprocess.RefuseDaemonLifecycleMutation,
		func(int) bool {
			identityCalled = true
			return true
		},
		func(int) error {
			stopCalled = true
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("stopDaemonPIDWithGuard() error = %v, want Go-test refusal", err)
	}

	if identityCalled || stopCalled {
		t.Fatalf("Go-test refusal reached mutation primitives: identity=%t stop=%t", identityCalled, stopCalled)
	}
}

func TestStopDaemonPIDAllowsProductionPathWithFakeStop(t *testing.T) {
	stoppedPID := 0

	err := stopDaemonPIDWithGuard(
		4242,
		allowDaemonLifecycleMutation,
		func(pid int) bool { return pid == 4242 },
		func(pid int) error {
			stoppedPID = pid
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if stoppedPID != 4242 {
		t.Fatalf("stopped PID = %d, want 4242", stoppedPID)
	}
}

func TestStopDaemonRejectsGoTestBeforePIDFileDeletion(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "canny.pid")
	if err := os.WriteFile(pidFile, []byte("dreich"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := StopDaemon(pidFile)
	if err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("StopDaemon() error = %v, want Go-test refusal", err)
	}

	if data, readErr := os.ReadFile(pidFile); readErr != nil || string(data) != "dreich" {
		t.Fatalf("refused StopDaemon changed PID file: data=%q err=%v", data, readErr)
	}
}
