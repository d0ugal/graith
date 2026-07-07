package cli

import (
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
