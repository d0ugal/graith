package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
)

// writeStubExecutable creates an executable file in dir and returns its base
// name, which is then resolvable via exec.LookPath once dir is on PATH.
func writeStubExecutable(t *testing.T, dir, name string) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		name += ".bat"
	}

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // G306: stub must be executable for exec.LookPath
		t.Fatalf("write stub executable: %v", err)
	}

	return name
}

// makeTmpRepo creates the <repo>/<hash> layout gr doctor's checkTmpDir expects
// under root, writing a file of the given size so a size walk has something to
// count. The caller points paths.TmpDir at root.
func makeTmpRepo(t *testing.T, root, repo, hash string, fileBytes int) {
	t.Helper()

	dir := filepath.Join(root, repo, hash)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: test fixture
		t.Fatalf("mkdir tmp repo: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "neep"), bytes.Repeat([]byte("x"), fileBytes), 0o600); err != nil {
		t.Fatalf("write tmp repo file: %v", err)
	}
}

// TestCheckTmpDirSkipsSizeByDefault verifies the default (no --disk) run reports
// the tmp repo count without walking the tree for a byte size. This is the crux
// of the doctor perf fix: the expensive full-tree walk must not run unless
// explicitly requested. It must also NOT set suggestDisk — a tmp repo dir
// exists on every active install, so the --disk hint would otherwise fire on
// essentially every run.
func TestCheckTmpDirSkipsSizeByDefault(t *testing.T) {
	oldOut, oldPaths, oldDisk := out, paths, doctorDisk

	t.Cleanup(func() {
		out, paths, doctorDisk = oldOut, oldPaths, oldDisk
	})

	tmpRoot := t.TempDir()
	makeTmpRepo(t, tmpRoot, "croft", "abc123", 2048)

	paths = config.Paths{TmpDir: tmpRoot}

	doctorDisk = false
	buf := &bytes.Buffer{}
	out = output.NewWithWriter(false, buf)

	dc := newDoctorContext()
	dc.checkTmpDir()

	got := buf.String()
	if !strings.Contains(got, "1 repo(s)") {
		t.Errorf("expected tmp repo count in default output, got: %q", got)
	}

	if strings.Contains(got, "KB") || strings.Contains(got, "MB") {
		t.Errorf("default run should not report a byte size (no full-tree walk), got: %q", got)
	}

	if dc.suggestDisk {
		t.Error("suggestDisk should not fire for ordinary tmp repos (present on every install)")
	}
}

// TestCheckTmpDirLegacyDirSuggestsDisk verifies a leftover legacy "share" dir
// (a genuine anomaly, unlike ordinary tmp repos) does set suggestDisk in the
// default run, so doctor recommends --disk to measure it.
func TestCheckTmpDirLegacyDirSuggestsDisk(t *testing.T) {
	oldOut, oldPaths, oldDisk := out, paths, doctorDisk

	t.Cleanup(func() {
		out, paths, doctorDisk = oldOut, oldPaths, oldDisk
	})

	// checkTmpDir looks for a "share" dir alongside tmpDir, so tmpDir must sit
	// under a parent we control.
	parent := t.TempDir()
	tmpRoot := filepath.Join(parent, "tmp")
	makeTmpRepo(t, tmpRoot, "croft", "abc123", 1024)

	if err := os.MkdirAll(filepath.Join(parent, "share"), 0o755); err != nil { //nolint:gosec // G301: test fixture
		t.Fatalf("mkdir legacy share: %v", err)
	}

	paths = config.Paths{TmpDir: tmpRoot}

	doctorDisk = false
	out = output.NewWithWriter(false, io.Discard)

	dc := newDoctorContext()
	dc.checkTmpDir()

	if !dc.suggestDisk {
		t.Error("expected suggestDisk to be set when a legacy share dir exists")
	}
}

// TestCheckTmpDirReportsSizeWithDisk verifies --disk restores the byte size,
// walking the tree as before.
func TestCheckTmpDirReportsSizeWithDisk(t *testing.T) {
	oldOut, oldPaths, oldDisk := out, paths, doctorDisk

	t.Cleanup(func() {
		out, paths, doctorDisk = oldOut, oldPaths, oldDisk
	})

	tmpRoot := t.TempDir()
	makeTmpRepo(t, tmpRoot, "croft", "abc123", 2048)

	paths = config.Paths{TmpDir: tmpRoot}

	doctorDisk = true
	buf := &bytes.Buffer{}
	out = output.NewWithWriter(false, buf)

	dc := newDoctorContext()
	dc.checkTmpDir()

	got := buf.String()
	if !strings.Contains(got, "1 repo(s)") {
		t.Errorf("expected tmp repo count with --disk, got: %q", got)
	}

	if !strings.Contains(got, "KB") {
		t.Errorf("--disk run should report a byte size, got: %q", got)
	}

	if dc.suggestDisk {
		t.Error("suggestDisk should not be set when --disk already measured sizes")
	}
}

func TestAgentInstalled(t *testing.T) {
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)

	name := writeStubExecutable(t, binDir, "braw")

	if !agentInstalled(name) {
		t.Errorf("agentInstalled(%q) = false, want true", name)
	}

	if agentInstalled("") {
		t.Error("agentInstalled(\"\") = true, want false")
	}

	if agentInstalled("thrawn-nae-sic-binary") {
		t.Error("agentInstalled(nonexistent) = true, want false")
	}
}

// TestCheckSandboxPathsSkipsUninstalledAgents verifies that per-agent sandbox
// dirs are only checked for agents whose command is resolvable on PATH — the
// fix for the wall of spurious warnings from built-in defaults of agents the
// user never runs.
func TestCheckSandboxPathsSkipsUninstalledAgents(t *testing.T) {
	oldCfg, oldOut := cfg, out

	t.Cleanup(func() {
		cfg, out = oldCfg, oldOut
	})

	out = output.NewWithWriter(false, io.Discard)

	binDir := t.TempDir()
	t.Setenv("PATH", binDir)

	installedCmd := writeStubExecutable(t, binDir, "canny")

	// Both agents point at dirs that don't exist.
	installedMissing := filepath.Join(t.TempDir(), "bothy")
	uninstalledMissing := filepath.Join(t.TempDir(), "haar")

	cfg = &config.Config{}
	cfg.Agents = map[string]config.Agent{
		"canny": {
			Command: installedCmd,
			Sandbox: config.SandboxConfig{ReadDirs: []string{installedMissing}},
		},
		"dreich": {
			Command: "thrawn-nae-sic-binary",
			Sandbox: config.SandboxConfig{ReadDirs: []string{uninstalledMissing}},
		},
	}

	dc := newDoctorContext()
	dc.checkSandboxPaths()

	var warned []string

	for _, c := range dc.checks {
		if c.Level == "warn" {
			warned = append(warned, c.Message)
		}
	}

	joined := strings.Join(warned, "\n")

	if !strings.Contains(joined, installedMissing) {
		t.Errorf("expected warning for installed agent's missing dir %q, got: %v", installedMissing, warned)
	}

	if strings.Contains(joined, uninstalledMissing) {
		t.Errorf("did not expect warning for uninstalled agent's missing dir %q, got: %v", uninstalledMissing, warned)
	}
}

// checkResults collects the checks a doctorContext accumulated, split by level.
func checkResults(dc *doctorContext, level string) []string {
	var out []string

	for _, c := range dc.checks {
		if c.Level == level {
			out = append(out, c.Message)
		}
	}

	return out
}

// TestCheckApprovalsBackendFailClosed verifies gr doctor surfaces the reason an
// unenforceable approvals backend would fail closed at session-create — the fix
// for issue #738, where that reason was buried in daemon.log and every new
// session crashed with a bare "exit 1" and zero output.
func TestCheckApprovalsBackendFailClosed(t *testing.T) {
	oldCfg, oldOut := cfg, out

	t.Cleanup(func() {
		cfg, out = oldCfg, oldOut
	})

	out = output.NewWithWriter(false, io.Discard)

	// backend="command" with no command set cannot enforce.
	cfg = &config.Config{}
	cfg.Approvals = config.Approvals{Backend: "command"}

	dc := newDoctorContext()
	dc.checkApprovalsBackend()

	failed := strings.Join(checkResults(dc, "fail"), "\n")

	if !strings.Contains(failed, "command") {
		t.Errorf("expected a fail check naming the approvals backend, got: %q", failed)
	}

	if !strings.Contains(failed, "cannot enforce") {
		t.Errorf("expected the fail check to explain it cannot enforce, got: %q", failed)
	}
}

// TestNonoInstallHintNotPipedShell verifies gr doctor's nono install guidance
// never recommends the `curl … | sh` piped-shell pattern the project moved away
// from in commit 0fa84fa / #697 — emitting that advice from a security-focused
// tool would contradict the supply-chain hardening (issue #795). The hint must
// point at brew and the pinned, attestation-verified release instead.
func TestNonoInstallHintNotPipedShell(t *testing.T) {
	lowered := strings.ToLower(nonoInstallHint)

	if strings.Contains(lowered, "| sh") || strings.Contains(lowered, "|sh") {
		t.Errorf("nono install hint recommends a piped shell (issue #795): %q", nonoInstallHint)
	}

	if strings.Contains(lowered, "install.sh") {
		t.Errorf("nono install hint references the unpinned remote install script (issue #795): %q", nonoInstallHint)
	}

	if !strings.Contains(lowered, "brew install nono") {
		t.Errorf("nono install hint should recommend brew, got: %q", nonoInstallHint)
	}

	if !strings.Contains(lowered, "attestation verify") {
		t.Errorf("nono install hint should point at the pinned, attestation-verified install, got: %q", nonoInstallHint)
	}
}

// TestCheckApprovalsBackendPromptDefault verifies the default (prompt) backend
// passes — it always defers to the human and can never fail closed.
func TestCheckApprovalsBackendPromptDefault(t *testing.T) {
	oldCfg, oldOut := cfg, out

	t.Cleanup(func() {
		cfg, out = oldCfg, oldOut
	})

	out = output.NewWithWriter(false, io.Discard)

	cfg = &config.Config{}

	dc := newDoctorContext()
	dc.checkApprovalsBackend()

	if len(checkResults(dc, "fail")) != 0 {
		t.Errorf("prompt backend should not fail, got: %v", dc.checks)
	}

	if len(checkResults(dc, "ok")) == 0 {
		t.Errorf("prompt backend should record a passing check, got: %v", dc.checks)
	}
}

// TestCheckApprovalsBackendAvailable verifies an enforceable backend passes.
//
// The command backend's Availability only checks that the command string is
// non-empty (see approvals.commandBackend.Availability) — it deliberately does
// NOT resolve the command on PATH, and the daemon's validateApprovalsBackend
// shares that exact check. We therefore use a command name that is *not*
// resolvable on PATH to pin that semantics: doctor must pass on any non-empty
// command, mirroring session-create. If command availability ever grows a
// PATH-resolution requirement, this test breaks loudly and both this check and
// the daemon must change together.
func TestCheckApprovalsBackendAvailable(t *testing.T) {
	oldCfg, oldOut := cfg, out

	t.Cleanup(func() {
		cfg, out = oldCfg, oldOut
	})

	out = output.NewWithWriter(false, io.Discard)

	cfg = &config.Config{}
	cfg.Approvals = config.Approvals{Backend: "command", Command: "thrawn-not-on-path"}

	dc := newDoctorContext()
	dc.checkApprovalsBackend()

	if len(checkResults(dc, "fail")) != 0 {
		t.Errorf("enforceable command backend should not fail, got: %v", dc.checks)
	}

	// Assert the passing check is specifically for the command backend, not just
	// that *some* ok check exists — a regression that reported the prompt backend
	// (or any other) as OK would otherwise slip through a bare len(ok) > 0 check.
	passed := strings.Join(checkResults(dc, "ok"), "\n")
	if !strings.Contains(passed, "command") {
		t.Errorf("expected a passing check naming the command backend, got: %q", passed)
	}
}

// TestCheckApprovalsBackendInlineRules verifies the builtin backend with only
// inline [approvals.builtin] rules (no external config file) is reported as
// enforceable. doctor must render inline rules the same way the daemon does at
// session-create; otherwise it falsely reports "cannot enforce" for a
// first-class, fully-supported configuration (issue #790 convergence with the
// daemon's resolution).
func TestCheckApprovalsBackendInlineRules(t *testing.T) {
	oldCfg, oldOut := cfg, out

	t.Cleanup(func() {
		cfg, out = oldCfg, oldOut
	})

	out = output.NewWithWriter(false, io.Discard)

	cfg = &config.Config{}
	cfg.Approvals = config.Approvals{
		Backend: "builtin",
		Builtin: config.ApprovalsBuiltin{Allow: []any{"@arg @*"}},
	}

	dc := newDoctorContext()
	dc.checkApprovalsBackend()

	if failed := checkResults(dc, "fail"); len(failed) != 0 {
		t.Errorf("builtin backend with inline rules should not fail, got: %v", failed)
	}

	passed := strings.Join(checkResults(dc, "ok"), "\n")
	if !strings.Contains(passed, "builtin") {
		t.Errorf("expected a passing check naming the builtin backend, got: %q", passed)
	}
}

// TestCheckApprovalsBackendRelativeConfig verifies doctor resolves a relative
// [approvals.builtin] config path against the config dir (via approvalsConfigDir
// → ExpandPathRelative) so a valid file found there is reported enforceable,
// matching how the daemon resolves the same value at session-create.
func TestCheckApprovalsBackendRelativeConfig(t *testing.T) {
	oldCfg, oldOut, oldCfgFile := cfg, out, cfgFile

	t.Cleanup(func() {
		cfg, out, cfgFile = oldCfg, oldOut, oldCfgFile
	})

	out = output.NewWithWriter(false, io.Discard)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "approvals.json"), []byte(`{"allow":["@arg @*"]}`), 0o600); err != nil {
		t.Fatalf("write approvals.json: %v", err)
	}

	// cfgFile points at a config.toml in dir, so approvalsConfigDir() resolves
	// the relative "approvals.json" against dir.
	cfgFile = filepath.Join(dir, "config.toml")
	cfg = &config.Config{}
	cfg.Approvals = config.Approvals{
		Backend: "builtin",
		Builtin: config.ApprovalsBuiltin{Config: "approvals.json"},
	}

	dc := newDoctorContext()
	dc.checkApprovalsBackend()

	if failed := checkResults(dc, "fail"); len(failed) != 0 {
		t.Errorf("builtin backend with a resolvable relative config should not fail, got: %v", failed)
	}
}

// TestCheckSandboxPathsChecksGlobal verifies global sandbox dirs are always
// checked regardless of installed agents.
func TestCheckSandboxPathsChecksGlobal(t *testing.T) {
	oldCfg, oldOut := cfg, out

	t.Cleanup(func() {
		cfg, out = oldCfg, oldOut
	})

	out = output.NewWithWriter(false, io.Discard)

	globalMissing := filepath.Join(t.TempDir(), "glen")

	cfg = &config.Config{}
	cfg.Sandbox = config.SandboxConfig{ReadDirs: []string{globalMissing}}

	dc := newDoctorContext()
	dc.checkSandboxPaths()

	found := false

	for _, c := range dc.checks {
		if c.Level == "warn" && strings.Contains(c.Message, globalMissing) {
			found = true
		}
	}

	if !found {
		t.Errorf("expected warning for missing global dir %q, got checks: %v", globalMissing, dc.checks)
	}
}

// TestCheckConfigKeysWarnsAndPasses verifies checkConfigKeys emits a warn for an
// unrecognised key (with a suggestion) and an ok when every key is recognised.
func TestCheckConfigKeysWarnsAndPasses(t *testing.T) {
	oldOut := out

	t.Cleanup(func() {
		out = oldOut
	})

	out = output.NewWithWriter(false, io.Discard)

	dir := t.TempDir()

	bad := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(bad, []byte("[sandbox]\nread_dir = [\"/etc\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dc := newDoctorContext()
	dc.checkConfigKeys(bad)

	warned := false

	for _, c := range dc.checks {
		if c.Level == "warn" && strings.Contains(c.Message, "read_dir") && strings.Contains(c.Message, "read_dirs") {
			warned = true
		}
	}

	if !warned {
		t.Errorf("expected warn naming read_dir and suggesting read_dirs, got: %v", dc.checks)
	}

	good := filepath.Join(dir, "good.toml")
	if err := os.WriteFile(good, []byte("[sandbox]\nread_dirs = [\"/etc\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dc2 := newDoctorContext()
	dc2.checkConfigKeys(good)

	recognised := false

	for _, c := range dc2.checks {
		if c.Level == "ok" && strings.Contains(c.Message, "all recognised") {
			recognised = true
		}
	}

	if !recognised {
		t.Errorf("expected ok 'all recognised' for valid config, got: %v", dc2.checks)
	}
}

// TestCheckSandboxPathsWriteFileNotExistenceChecked verifies that a write_files
// grant for a file that does not exist but whose parent dir does exist produces
// NO warning — mirroring expandFilePaths, which keeps such grants because they
// are routinely files the agent creates at runtime (e.g. ~/.claude.json.lock).
// This is the fix for issue #794, where the recommended config was flagged as
// unhealthy by gr doctor.
func TestCheckSandboxPathsWriteFileNotExistenceChecked(t *testing.T) {
	oldCfg, oldOut := cfg, out

	t.Cleanup(func() {
		cfg, out = oldCfg, oldOut
	})

	out = output.NewWithWriter(false, io.Discard)

	// Parent dir exists (t.TempDir()), lockfile itself does not.
	lockfile := filepath.Join(t.TempDir(), ".claude.json.lock")

	cfg = &config.Config{}
	cfg.Sandbox = config.SandboxConfig{WriteFiles: []string{lockfile}}

	dc := newDoctorContext()
	dc.checkSandboxPaths()

	for _, c := range dc.checks {
		if c.Level == "warn" && strings.Contains(c.Message, lockfile) {
			t.Errorf("did not expect a warning for a non-existent write file whose parent exists, got: %q", c.Message)
		}
	}
}

// TestCheckSandboxPathsWriteFileMissingParent verifies that a write_files grant
// whose *parent directory* does not exist still warns — nono cannot create the
// file without a grantable parent, so this mirrors expandFilePaths' parent-dir
// warning.
func TestCheckSandboxPathsWriteFileMissingParent(t *testing.T) {
	oldCfg, oldOut := cfg, out

	t.Cleanup(func() {
		cfg, out = oldCfg, oldOut
	})

	out = output.NewWithWriter(false, io.Discard)

	missingParent := filepath.Join(t.TempDir(), "wynd")
	orphanFile := filepath.Join(missingParent, ".claude.json.lock")

	cfg = &config.Config{}
	cfg.Sandbox = config.SandboxConfig{WriteFiles: []string{orphanFile}}

	dc := newDoctorContext()
	dc.checkSandboxPaths()

	found := false

	for _, c := range dc.checks {
		if c.Level == "warn" && strings.Contains(c.Message, missingParent) {
			found = true
		}
	}

	if !found {
		t.Errorf("expected warning for write file with missing parent dir %q, got checks: %v", missingParent, dc.checks)
	}
}
