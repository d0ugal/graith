package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/tools"
)

// writeFakeBinary writes an executable stub at dir/name and returns its path.
func writeFakeBinary(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // G306: stub must be executable for validation
		t.Fatalf("write fake binary: %v", err)
	}

	return path
}

func loadTOML(t *testing.T, toml string) (*Config, error) {
	t.Helper()

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return Load(cfgPath)
}

func TestGitTimeoutDefaults(t *testing.T) {
	var g GitConfig

	if got := g.FetchTimeoutDuration(); got != 2*time.Minute {
		t.Errorf("FetchTimeoutDuration default = %v, want 2m", got)
	}

	if got := g.MergeTimeoutDuration(); got != 2*time.Minute {
		t.Errorf("MergeTimeoutDuration default = %v, want 2m", got)
	}

	if got := g.UsernameTimeoutDuration(); got != 15*time.Second {
		t.Errorf("UsernameTimeoutDuration default = %v, want 15s", got)
	}
}

func TestGitTimeoutOverrideViaLoad(t *testing.T) {
	cfg, err := loadTOML(t, `
[git]
fetch_timeout = "10m"
merge_timeout = "45s"
username_timeout = "3s"
`)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Git.FetchTimeoutDuration(); got != 10*time.Minute {
		t.Errorf("FetchTimeoutDuration = %v, want 10m", got)
	}

	if got := cfg.Git.MergeTimeoutDuration(); got != 45*time.Second {
		t.Errorf("MergeTimeoutDuration = %v, want 45s", got)
	}

	if got := cfg.Git.UsernameTimeoutDuration(); got != 3*time.Second {
		t.Errorf("UsernameTimeoutDuration = %v, want 3s", got)
	}
}

func TestGitTimeoutRejectsUnparseable(t *testing.T) {
	_, err := loadTOML(t, `
[git]
fetch_timeout = "soon"
`)
	if err == nil {
		t.Fatal("Load with bad git.fetch_timeout = nil, want error")
	}

	if !strings.Contains(err.Error(), "git.fetch_timeout") {
		t.Errorf("error %q missing git.fetch_timeout", err)
	}
}

func TestGitTimeoutRejectsNonPositive(t *testing.T) {
	_, err := loadTOML(t, `
[git]
merge_timeout = "0s"
`)
	if err == nil {
		t.Fatal("Load with git.merge_timeout = 0s returned nil, want error")
	}

	if !strings.Contains(err.Error(), "greater than zero") {
		t.Errorf("error %q missing 'greater than zero'", err)
	}
}

func TestToolsResolvedCopiesFields(t *testing.T) {
	tc := ToolsConfig{
		Git:       "g",
		GH:        "h",
		Shell:     "s",
		OSAScript: "o",
		PS:        "p",
		Lsof:      "l",
	}

	want := tools.Config{Git: "g", GH: "h", Shell: "s", OSAScript: "o", PS: "p", Lsof: "l"}
	if got := tc.Resolved(); got != want {
		t.Errorf("Resolved() = %+v, want %+v", got, want)
	}
}

func TestToolsOverrideViaLoad(t *testing.T) {
	dir := t.TempDir()
	gitBin := writeFakeBinary(t, dir, "croft-git")

	cfg, err := loadTOML(t, `
[tools]
git = "`+gitBin+`"
`)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Tools.Git != gitBin {
		t.Errorf("Tools.Git = %q, want %q", cfg.Tools.Git, gitBin)
	}
}

func TestToolsRejectsMissingExecutable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-git")

	_, err := loadTOML(t, `
[tools]
git = "`+missing+`"
`)
	if err == nil {
		t.Fatal("Load with missing tools.git = nil, want error")
	}

	if !strings.Contains(err.Error(), "tools.git") {
		t.Errorf("error %q missing tools.git", err)
	}
}

func TestDefaultConfigLeavesToolsUnset(t *testing.T) {
	// The embedded default config documents [tools] only as comments, so a fresh
	// default keeps the executable overrides unset. This keeps validation from
	// rejecting the macOS-only default names (e.g. "osascript") on Linux and
	// keeps the tools.Defaults() lazy PATH-lookup semantics for unset entries.
	cfg := Default()

	if cfg.Tools != (ToolsConfig{}) {
		t.Errorf("default Tools = %+v, want zero", cfg.Tools)
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("default config Validate = %v, want nil", err)
	}
}

// TestDefaultConfigGitTimeoutsDrift asserts the git-timeout defaults live in the
// embedded default_config.toml as RAW fields, not only as Go accessor fallbacks
// (issue #1238; mirrors the epic #1230 drift-test convention). The accessor
// passes whether the value comes from TOML or the fallback, so only the raw
// check catches a default that regressed to being "implemented only in code".
func TestDefaultConfigGitTimeoutsDrift(t *testing.T) {
	cfg := Default()

	cases := map[string]string{
		"fetch_timeout":    cfg.Git.FetchTimeout,
		"merge_timeout":    cfg.Git.MergeTimeout,
		"username_timeout": cfg.Git.UsernameTimeout,
	}

	want := map[string]string{
		"fetch_timeout":    "2m",
		"merge_timeout":    "2m",
		"username_timeout": "15s",
	}
	for key, got := range cases {
		if got != want[key] {
			t.Errorf("default git.%s = %q, want %q (materialize it in default_config.toml)", key, got, want[key])
		}
	}
}
