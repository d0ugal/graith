package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/sandbox"
)

func TestExpandWhyPathsDropsMissing(t *testing.T) {
	dir := t.TempDir()

	bothy := filepath.Join(dir, "bothy")
	if err := os.Mkdir(bothy, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	missing := filepath.Join(dir, "haar")

	got := expandWhyPaths([]string{bothy, missing})
	if len(got) != 1 || got[0] != bothy {
		t.Fatalf("expandWhyPaths() = %v, want [%s]", got, bothy)
	}
}

func TestExpandWhyPathsGlob(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"croft-a", "croft-b"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	got := expandWhyPaths([]string{filepath.Join(dir, "croft-*")})
	if len(got) != 2 {
		t.Fatalf("expandWhyPaths() glob = %v, want 2 matches", got)
	}
}

func TestExpandWhyFilePathsKeepsMissing(t *testing.T) {
	dir := t.TempDir()

	present := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(present, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// A writable file grant is routinely for a file the agent creates at
	// runtime (e.g. a lockfile), so a missing literal path must be kept — not
	// stat-dropped like a directory grant.
	missing := filepath.Join(dir, "claude.json.lock")

	got := expandWhyFilePaths([]string{present, missing})
	if len(got) != 2 || got[0] != present || got[1] != missing {
		t.Fatalf("expandWhyFilePaths() = %v, want [%s %s]", got, present, missing)
	}
}

func TestExpandWhyFilePathsGlobSkipsNoMatch(t *testing.T) {
	dir := t.TempDir()

	got := expandWhyFilePaths([]string{filepath.Join(dir, "haar-*.lock")})
	if len(got) != 0 {
		t.Fatalf("expandWhyFilePaths() glob no-match = %v, want empty", got)
	}
}

func TestWhyWrapOptsIncludesFileGrants(t *testing.T) {
	dir := t.TempDir()

	writeFile := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(writeFile, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	readFile := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(readFile, []byte(""), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	opts := whyWrapOpts(config.SandboxConfig{
		Backend:    sandbox.BackendNono,
		ReadFiles:  []string{readFile},
		WriteFiles: []string{writeFile},
	})

	if !containsStr(opts.ReadFiles, readFile) {
		t.Fatalf("ReadFiles = %v, want %s", opts.ReadFiles, readFile)
	}

	if !containsStr(opts.WriteFiles, writeFile) {
		t.Fatalf("WriteFiles = %v, want %s", opts.WriteFiles, writeFile)
	}
}

func TestWhyWrapOptsIncludesBaseEnvKeys(t *testing.T) {
	opts := whyWrapOpts(config.SandboxConfig{Backend: sandbox.BackendNono})

	if !containsStr(opts.EnvKeys, "PATH") || !containsStr(opts.EnvKeys, "HOME") {
		t.Fatalf("EnvKeys = %v, want PATH and HOME", opts.EnvKeys)
	}

	if opts.Backend != sandbox.BackendNono {
		t.Fatalf("Backend = %q, want nono", opts.Backend)
	}
}

func TestRunSandboxWhyRejectsNonNonoBackend(t *testing.T) {
	oldCfg := cfg
	oldPath, oldOp := whyPath, whyOp

	t.Cleanup(func() {
		cfg = oldCfg
		whyPath, whyOp = oldPath, oldOp
	})

	cfg = config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: sandbox.BackendSafehouse}
	whyPath, whyOp, whyHost, whyPort, whyAgent = "/glen/bothy", "read", "", 0, ""

	err := runSandboxWhy()
	if err == nil || !strings.Contains(err.Error(), sandbox.BackendNono) {
		t.Fatalf("runSandboxWhy() = %v, want backend-gate error", err)
	}
}

func TestRunSandboxWhyValidatesQuery(t *testing.T) {
	oldCfg := cfg
	oldPath, oldOp := whyPath, whyOp

	t.Cleanup(func() {
		cfg = oldCfg
		whyPath, whyOp = oldPath, oldOp
	})

	cfg = config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: sandbox.BackendNono}
	// Invalid: path without op.
	whyPath, whyOp, whyHost, whyPort, whyAgent = "/glen/wynd", "", "", 0, ""

	err := runSandboxWhy()
	if err == nil || !strings.Contains(err.Error(), "--op is required") {
		t.Fatalf("runSandboxWhy() = %v, want validation error", err)
	}
}

func TestRunSandboxWhyRejectsUnknownAgent(t *testing.T) {
	oldCfg := cfg
	oldPath, oldOp, oldHost, oldPort, oldAgent := whyPath, whyOp, whyHost, whyPort, whyAgent

	t.Cleanup(func() {
		cfg = oldCfg
		whyPath, whyOp, whyHost, whyPort, whyAgent = oldPath, oldOp, oldHost, oldPort, oldAgent
	})

	cfg = config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: sandbox.BackendNono}
	// "thrawn" is not a configured agent — a typo should error, not silently
	// fall back to the global policy.
	whyPath, whyOp, whyHost, whyPort, whyAgent = "/glen/bothy", "read", "", 0, "thrawn"

	err := runSandboxWhy()
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("runSandboxWhy() = %v, want unknown-agent error", err)
	}

	// The error should list the known agents so the user can correct the typo.
	if !strings.Contains(err.Error(), "claude") {
		t.Fatalf("runSandboxWhy() error = %v, want it to list known agents", err)
	}
}

func TestRunSandboxWhyMergesKnownAgentOverride(t *testing.T) {
	oldCfg := cfg
	oldPath, oldOp, oldHost, oldPort, oldAgent := whyPath, whyOp, whyHost, whyPort, whyAgent

	t.Cleanup(func() {
		cfg = oldCfg
		whyPath, whyOp, whyHost, whyPort, whyAgent = oldPath, oldOp, oldHost, oldPort, oldAgent
	})

	cfg = config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: sandbox.BackendNono}
	// A known agent that overrides the backend proves the known-agent branch
	// ran and merged the per-agent config: the backend gate should now report
	// the agent's safehouse override, not the global nono backend.
	cfg.Agents["braw"] = config.Agent{Sandbox: config.SandboxConfig{Backend: sandbox.BackendSafehouse}}
	whyPath, whyOp, whyHost, whyPort, whyAgent = "/glen/bothy", "read", "", 0, "braw"

	err := runSandboxWhy()
	if err == nil || !strings.Contains(err.Error(), sandbox.BackendSafehouse) {
		t.Fatalf("runSandboxWhy() = %v, want backend-gate error mentioning the merged safehouse override", err)
	}
}

func TestKnownAgentNamesSortedAndEmpty(t *testing.T) {
	if got := knownAgentNames(nil); got != "(none)" {
		t.Fatalf("knownAgentNames(nil) = %q, want %q", got, "(none)")
	}

	agents := map[string]config.Agent{"codex": {}, "braw": {}, "claude": {}}
	if got := knownAgentNames(agents); got != "braw, claude, codex" {
		t.Fatalf("knownAgentNames() = %q, want sorted list", got)
	}
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}

	return false
}
