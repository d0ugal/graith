package daemonservice

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/testprocess"
)

func TestClearRunningProcessRejectsGoTestBeforeHostRootResolution(t *testing.T) {
	resolvedRoot := false

	err := clearRunningProcessWith(
		"canny",
		os.Getpid(),
		"braw",
		os.Geteuid(),
		func(int) (string, error) {
			resolvedRoot = true
			return t.TempDir(), nil
		},
		testprocess.RefuseDaemonLifecycleMutation,
	)
	if err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("clearRunningProcessWith() error = %v, want Go-test refusal", err)
	}

	if resolvedRoot {
		t.Fatal("Go-test refusal resolved the managed-service receipt root")
	}
}

func TestClearRunningProcessAllowsHermeticInjectedReceipt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service-control")

	store := ReceiptStore{Root: root, UID: os.Geteuid()}
	if _, err := store.Update(true, func(receipt *Receipt) error {
		receipt.Leases["canny"] = Lease{
			Profile:              "canny",
			Slot:                 "01",
			UID:                  os.Geteuid(),
			RegisteredGeneration: "braw",
			RunningGeneration:    "braw",
			RunningPID:           4242,
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err := clearRunningProcessWith(
		"canny",
		4242,
		"braw",
		os.Geteuid(),
		func(int) (string, error) { return root, nil },
		allowDaemonLifecycleMutation,
	)
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}

	lease := receipt.Leases["canny"]
	if lease.RunningPID != 0 || lease.RunningGeneration != "" {
		t.Fatalf("running lease = %#v, want cleared", lease)
	}
}

func TestResolveManagedRejectsGoTestBeforeBundleOrReceiptMutation(t *testing.T) {
	_, err := ResolveWith(ResolveOptions{
		GOOS:       "darwin",
		MacOSMajor: 13,
		Managed:    true,
		Executable: filepath.Join(t.TempDir(), "Graith.app", "Contents", "MacOS", "graith"),
		UID:        os.Geteuid(),
	})
	if err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("ResolveWith() error = %v, want Go-test refusal", err)
	}
}

func TestManagerMutationEntrypointsRejectGoTestBeforeStateAccess(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Manager) error
	}{
		{
			name: "launch",
			run: func(manager *Manager) error {
				return manager.Launch(context.Background(), config.Default(), config.Paths{}, "", time.Second, nil)
			},
		},
		{
			name: "reserve clean restart",
			run: func(manager *Manager) error {
				return manager.ReserveForCleanRestart(context.Background(), "canny")
			},
		},
		{
			name: "mark stopped",
			run: func(manager *Manager) error {
				return manager.MarkStopped("canny", 4242)
			},
		},
		{
			name: "garbage collect cache",
			run: func(manager *Manager) error {
				_, err := manager.GarbageCollectCache()
				return err
			},
		},
		{
			name: "repair",
			run: func(manager *Manager) error {
				_, err := manager.Repair(context.Background())
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.run(&Manager{})
			if err == nil || !strings.Contains(err.Error(), "Go test binary") {
				t.Fatalf("mutation entrypoint error = %v, want Go-test refusal", err)
			}
		})
	}
}
