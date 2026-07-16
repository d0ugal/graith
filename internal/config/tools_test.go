package config

import (
	"os"
	"os/exec"
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

// TestNormalizeRelativeToolPaths covers the resolution rules for #1293: a bare
// name keeps PATH-lookup semantics, an absolute path is untouched, a relative
// path resolves against the config dir, and a leading ~/ expands.
func TestNormalizeRelativeToolPaths(t *testing.T) {
	base := "/glen/bothy"

	home, _ := os.UserHomeDir()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare name kept for PATH lookup", "git", "git"},
		{"bare name with dash kept", "git-wrapper", "git-wrapper"},
		{"absolute path untouched", "/usr/local/bin/git", "/usr/local/bin/git"},
		{"dot-relative resolves against config dir", "./bin/git-wrapper", filepath.Join(base, "bin/git-wrapper")},
		{"plain relative path resolves against config dir", "bin/git-wrapper", filepath.Join(base, "bin/git-wrapper")},
		{"parent-relative resolves and cleans", "../tools/git", "/glen/tools/git"},
		{"empty stays empty", "", ""},
		{"tilde expands to home", "~/bin/git", filepath.Join(home, "bin/git")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeToolPath(tt.in, base); got != tt.want {
				t.Errorf("normalizeToolPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestRelativeToolWrapperExecutesAfterCmdDirChange is the #1293 regression: a
// relative wrapper path passes startup validation AND still executes when a
// later git command sets exec.Cmd.Dir to an unrelated worktree. Before the fix
// the relative path was validated against one directory and re-evaluated by Go
// against exec.Cmd.Dir, so the first git operation failed with "no such file".
func TestRelativeToolWrapperExecutesAfterCmdDirChange(t *testing.T) {
	configDir := t.TempDir()

	binDir := filepath.Join(configDir, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	wrapper := filepath.Join(binDir, "git-wrapper")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\necho braw-wrapper-ran\n"), 0o755); err != nil { //nolint:gosec // G306: stub must be executable
		t.Fatalf("write wrapper: %v", err)
	}

	cfgPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[tools]\ngit = \"./bin/git-wrapper\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load (validation should accept the relative wrapper): %v", err)
	}

	// The stored path is the absolute wrapper, so validation and execution agree.
	if cfg.Tools.Git != wrapper {
		t.Fatalf("Tools.Git = %q, want normalized absolute %q", cfg.Tools.Git, wrapper)
	}

	tools.Configure(cfg.Tools.Resolved())
	t.Cleanup(tools.Reset)

	// Run the resolved git with Dir pointed at a DIFFERENT directory, the exact
	// condition (internal/git sets Cmd.Dir to a worktree) that broke a relative
	// executable path before the fix.
	cmd := exec.Command(tools.Git())
	cmd.Dir = t.TempDir()

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running relative wrapper with changed Cmd.Dir: %v", err)
	}

	if !strings.Contains(string(out), "braw-wrapper-ran") {
		t.Errorf("wrapper output = %q, want it to contain the sentinel", out)
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
