package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/tools"
)

// TestApplyConfigReinstallsToolResolver proves a changed [tools] block takes
// effect on config reload without a daemon restart (issue #1238).
func TestApplyConfigReinstallsToolResolver(t *testing.T) {
	t.Cleanup(tools.Reset)

	dir := t.TempDir()

	fakeGit := filepath.Join(dir, "bothy-git")
	if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // G306: stub must be executable
		t.Fatalf("write fake git: %v", err)
	}

	sm := newSMWithConfig(t, config.Default())

	// Baseline: nothing configured yet, so the resolver returns the default.
	if got := tools.Git(); got != "git" {
		t.Fatalf("baseline Git() = %q, want git", got)
	}

	newCfg := config.Default()
	newCfg.Tools.Git = fakeGit
	_ = sm.applyConfig(newCfg)

	if got := tools.Git(); got != fakeGit {
		t.Errorf("after reload, Git() = %q, want %q", got, fakeGit)
	}
}

// TestGitTimeoutsAreReadFromConfig proves the lifecycle git timeouts come from
// the live config (so a reload changes them for free) rather than a fixed const.
func TestGitTimeoutsAreReadFromConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Git.FetchTimeout = "42s"

	sm := newSMWithConfig(t, cfg)

	if got := sm.cfg.Git.FetchTimeoutDuration().String(); got != "42s" {
		t.Errorf("FetchTimeoutDuration = %s, want 42s", got)
	}
}
