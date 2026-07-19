//go:build darwin

package daemonservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceControlRootUsesOSAccountHome(t *testing.T) {
	home, err := OSUserHome(os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}

	got, err := ServiceControlRoot(os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(home, "Library", "Application Support", "Graith", "services", "control", "bootstrap")
	if got != want {
		t.Fatalf("ServiceControlRoot() = %q, want %q", got, want)
	}
}

func TestServiceControlRootHonorsStartupCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := ServiceControlRootContext(ctx, os.Geteuid()); !errors.Is(err, context.Canceled) {
		t.Fatalf("ServiceControlRootContext() = %v, want canceled startup", err)
	}
}
