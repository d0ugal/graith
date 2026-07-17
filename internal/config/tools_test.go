package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/git"
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
	if got := tc.Resolved(""); got != want {
		t.Errorf("Resolved() = %+v, want %+v", got, want)
	}
}

// TestResolveToolPath covers the normalization rules directly: bare names and
// absolute paths pass through untouched, a relative path is anchored to the
// config directory, and an empty baseDir leaves a relative path alone (#1293).
func TestResolveToolPath(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "croft", "graith")

	cases := []struct {
		name    string
		baseDir string
		in      string
		want    string
	}{
		{"empty", base, "", ""},
		{"bare name", base, "git", "git"},
		{"absolute", base, "/usr/bin/git", "/usr/bin/git"},
		{"relative dot", base, "./bin/braw-git", filepath.Join(base, "bin", "braw-git")},
		{"relative subdir", base, "bin/braw-git", filepath.Join(base, "bin", "braw-git")},
		{"relative no base", "", "./bin/braw-git", "./bin/braw-git"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveToolPath(tc.baseDir, tc.in); got != tc.want {
				t.Errorf("resolveToolPath(%q, %q) = %q, want %q", tc.baseDir, tc.in, got, tc.want)
			}
		})
	}
}

// TestToolsRelativePathResolvesAgainstConfigDir is the #1293 regression. A
// relative tool path in config.toml must validate at load and then run even when
// the executing command's working directory (exec.Cmd.Dir) is a different
// worktree. Before the fix the path resolved against the process working
// directory for validation but against exec.Cmd.Dir at run time, so it either
// failed validation or failed to execute. The config directory is neither the
// process cwd nor the run-time Cmd.Dir here, proving the anchor is config.toml's
// directory.
func TestToolsRelativePathResolvesAgainstConfigDir(t *testing.T) {
	t.Cleanup(tools.Reset)

	configDir := t.TempDir()
	binDir := filepath.Join(configDir, "bin")

	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	// A wrapper git that echoes a sentinel so the test needs no real git.
	wrapper := filepath.Join(binDir, "braw-git")
	script := "#!/bin/sh\necho \"canny-marker $@\"\n"

	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil { //nolint:gosec // G306: stub must be executable for exec
		t.Fatalf("write wrapper: %v", err)
	}

	cfgPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[tools]\ngit = \"./bin/braw-git\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Load validates the relative override; before the fix this failed because
	// "./bin/braw-git" does not exist relative to the test process's cwd.
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load with relative tools.git = %v, want nil", err)
	}

	resolved := cfg.Tools.Resolved(cfg.SourceDir)
	if !filepath.IsAbs(resolved.Git) {
		t.Fatalf("resolved git = %q, want absolute path", resolved.Git)
	}

	tools.Configure(resolved)

	// Run git with Cmd.Dir pointed at an unrelated worktree. The relative path
	// must NOT resolve against this directory.
	runDir := t.TempDir()

	out, err := git.RunOutput(runDir, "status")
	if err != nil {
		t.Fatalf("RunOutput from a different Cmd.Dir = %v, want the wrapper to run", err)
	}

	if out != "canny-marker status" {
		t.Errorf("RunOutput = %q, want the wrapper's sentinel output", out)
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
