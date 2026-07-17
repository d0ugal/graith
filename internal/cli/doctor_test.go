package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
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

func TestCheckHumanToken(t *testing.T) {
	oldCfg, oldPaths, oldOut := cfg, paths, out

	t.Cleanup(func() { cfg, paths, out = oldCfg, oldPaths, oldOut })

	out = output.NewWithWriter(false, io.Discard)

	dataDir := t.TempDir()

	tokenPath := filepath.Join(dataDir, "human.token")
	if err := os.WriteFile(tokenPath, []byte("canny-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths = config.Paths{HumanTokenFile: tokenPath}

	t.Run("braw secure token", func(t *testing.T) {
		cfg = &config.Config{}
		dc := newDoctorContext()
		dc.checkHumanToken()

		if !dc.ok {
			t.Fatalf("secure token failed checks: %+v", dc.checks)
		}
	})

	t.Run("thrawn mode", func(t *testing.T) {
		if err := os.Chmod(tokenPath, 0o644); err != nil { //nolint:gosec // deliberately over-permissive to exercise the doctor check
			t.Fatal(err)
		}

		t.Cleanup(func() { _ = os.Chmod(tokenPath, 0o600) })

		cfg = &config.Config{}
		dc := newDoctorContext()
		dc.checkHumanToken()

		if dc.ok {
			t.Fatal("expected permissive token mode to fail")
		}
	})

	t.Run("dreich sandbox exposure", func(t *testing.T) {
		cfg = &config.Config{Sandbox: config.SandboxConfig{Enabled: true, ReadDirs: []string{dataDir}}}
		dc := newDoctorContext()
		dc.checkHumanToken()

		if dc.ok {
			t.Fatal("expected sandbox-readable token to fail")
		}
	})

	t.Run("haar missing token", func(t *testing.T) {
		paths.HumanTokenFile = filepath.Join(dataDir, "haar.token")

		t.Cleanup(func() { paths.HumanTokenFile = tokenPath })

		cfg = &config.Config{}
		dc := newDoctorContext()
		dc.checkHumanToken()

		if dc.ok {
			t.Fatal("expected missing token to fail")
		}
	})

	t.Run("thrawn not a regular file", func(t *testing.T) {
		dir := filepath.Join(dataDir, "not-a-file")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		paths.HumanTokenFile = dir

		t.Cleanup(func() { paths.HumanTokenFile = tokenPath })

		cfg = &config.Config{}
		dc := newDoctorContext()
		dc.checkHumanToken()

		if dc.ok {
			t.Fatal("expected a non-regular token to fail")
		}
	})

	// The daemon opens the token with O_NOFOLLOW and rejects a symlink; doctor
	// must not follow one and report it healthy (it uses Lstat, not Stat).
	t.Run("thrawn symlink", func(t *testing.T) {
		realTok := filepath.Join(dataDir, "real-secure.token")
		if err := os.WriteFile(realTok, []byte("bonnie-token\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		link := filepath.Join(dataDir, "linked.token")
		if err := os.Symlink(realTok, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}

		paths.HumanTokenFile = link

		t.Cleanup(func() { paths.HumanTokenFile = tokenPath })

		cfg = &config.Config{}
		dc := newDoctorContext()
		dc.checkHumanToken()

		if dc.ok {
			t.Fatal("expected a symlinked token to fail (daemon rejects it via O_NOFOLLOW)")
		}
	})

	t.Run("dreich per-agent sandbox exposure", func(t *testing.T) {
		cfg = &config.Config{
			Sandbox: config.SandboxConfig{Enabled: true},
			Agents: map[string]config.Agent{
				"canny": {Sandbox: config.SandboxConfig{ReadDirs: []string{dataDir}}},
			},
		}
		dc := newDoctorContext()
		dc.checkHumanToken()

		if dc.ok {
			t.Fatal("expected a per-agent sandbox grant exposing the token to fail")
		}
	})

	t.Run("bonnie sandbox enabled but token not exposed", func(t *testing.T) {
		cfg = &config.Config{Sandbox: config.SandboxConfig{Enabled: true, ReadDirs: []string{t.TempDir()}}}
		dc := newDoctorContext()
		dc.checkHumanToken()

		if !dc.ok {
			t.Fatalf("expected a non-exposed token under an enabled sandbox to pass: %+v", dc.checks)
		}
	})
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

func TestRenderPurgeDiagnostic(t *testing.T) {
	oldOut := out

	t.Cleanup(func() { out = oldOut })

	tests := []struct {
		name  string
		purge protocol.PurgeDiagnostic
		want  []string
	}{
		{
			name: "before first sweep",
			purge: protocol.PurgeDiagnostic{
				StartupDelay: "45s",
				Interval:     "7m0s",
			},
			want: []string{
				"Purge",
				"Startup delay: 45s",
				"Interval: 7m0s",
				"Last sweep: not yet run",
				"Next sweep: awaiting first sweep",
			},
		},
		{
			name: "after a sweep",
			purge: protocol.PurgeDiagnostic{
				StartupDelay: "45s",
				Interval:     "7m0s",
				LastSweep:    "2026-07-17T08:00:00Z",
				NextSweep:    "2026-07-17T08:07:00Z",
			},
			want: []string{
				"Purge",
				"Startup delay: 45s",
				"Interval: 7m0s",
				"Last sweep: 2026-07-17T08:00:00Z",
				"Next sweep: 2026-07-17T08:07:00Z",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			out = output.NewWithWriter(false, &buf)

			dc := newDoctorContext()
			dc.renderPurgeDiagnostic(&protocol.DiagnosticsMsg{Purge: &tt.purge})

			rendered := buf.String()
			for _, want := range tt.want {
				if !strings.Contains(rendered, want) {
					t.Errorf("expected doctor output to contain %q, got:\n%s", want, rendered)
				}
			}

			// diagnostics.purge already carries this data in JSON. Plain rendering
			// must not add duplicate entries to the stable top-level checks array.
			if len(dc.checks) != 0 {
				t.Errorf("purge renderer added %d check(s), want none: %+v", len(dc.checks), dc.checks)
			}
		})
	}
}

// TestDoctorPlainOutputRendersPurgeDiagnostic is the command-level regression
// for issue #1312: the daemon payload already contained purge timing, but the
// plain doctor dispatch omitted it entirely.
func TestDoctorPlainOutputRendersPurgeDiagnostic(t *testing.T) {
	oldCfg, oldCfgFile, oldPaths := cfg, cfgFile, paths
	oldOut, oldJSON := out, jsonOutput
	oldAutofix, oldDisk := doctorAutofix, doctorDisk
	oldProbe, oldGCFetch := doctorDaemonProbe, daemonGCFetch

	t.Cleanup(func() {
		cfg, cfgFile, paths = oldCfg, oldCfgFile, oldPaths
		out, jsonOutput = oldOut, oldJSON
		doctorAutofix, doctorDisk = oldAutofix, oldDisk
		doctorDaemonProbe = oldProbe
		daemonGCFetch = oldGCFetch
	})

	dataDir := t.TempDir()
	paths = config.Paths{
		DataDir:        dataDir,
		SocketPath:     filepath.Join(dataDir, "d.sock"),
		PIDFile:        filepath.Join(dataDir, "d.pid"),
		StateFile:      filepath.Join(dataDir, "state.json"),
		HumanTokenFile: filepath.Join(dataDir, "human.token"),
		LogDir:         filepath.Join(dataDir, "logs"),
		DaemonLog:      filepath.Join(dataDir, "daemon.log"),
		MessagesDB:     filepath.Join(dataDir, "messages.sqlite"),
		TmpDir:         filepath.Join(dataDir, "tmp"),
	}
	if err := os.WriteFile(paths.HumanTokenFile, []byte("canny-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgFile = filepath.Join(dataDir, "canny.toml")
	cfg = &config.Config{AgentPrompt: config.DefaultAgentPrompt()}
	cfg.Updates.Enabled = false
	doctorAutofix, doctorDisk, jsonOutput = false, false, false
	daemonGCFetch = func(bool) ([]protocol.GCOrphanInfo, error) { return nil, nil }

	var buf bytes.Buffer
	out = output.NewWithWriter(false, &buf)

	doctorDaemonProbe = func(*doctorContext) daemonProbe {
		return daemonProbe{
			reach:         daemonReachOK,
			daemonVersion: version.Version,
			diag: &protocol.DiagnosticsMsg{
				DaemonPID:     777,
				DaemonVersion: version.Version,
				DaemonUptime:  "3m",
				Purge: &protocol.PurgeDiagnostic{
					StartupDelay: "45s",
					Interval:     "7m0s",
					LastSweep:    "2026-07-17T08:00:00Z",
					NextSweep:    "2026-07-17T08:07:00Z",
				},
			},
		}
	}

	if err := doctorCmd.RunE(doctorCmd, nil); err != nil {
		t.Fatalf("doctor command failed: %v\n%s", err, buf.String())
	}

	rendered := buf.String()
	for _, want := range []string{
		"Purge",
		"Startup delay: 45s",
		"Interval: 7m0s",
		"Last sweep: 2026-07-17T08:00:00Z",
		"Next sweep: 2026-07-17T08:07:00Z",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("expected doctor output to contain %q, got:\n%s", want, rendered)
		}
	}
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

// TestCheckTriggersDegraded verifies gr doctor surfaces a degraded watch-trigger
// binding (issue #1029) as a warning naming the trigger, session, reason, and
// the scheduled retry — so the operator knows the watch is temporarily blind and
// that it recovers on its own.
func TestCheckTriggersDegraded(t *testing.T) {
	oldOut := out

	t.Cleanup(func() { out = oldOut })

	var buf bytes.Buffer

	out = output.NewWithWriter(false, &buf)

	diag := &protocol.DiagnosticsMsg{
		Triggers: []protocol.TriggerDiagnostic{{
			Name:        "test-on-change",
			SessionID:   "abc123",
			SessionName: "ben",
			Degraded:    "watcher.Add failed: no space left on device",
			RetryCount:  2,
			NextRetryAt: "2026-07-15T10:00:00Z",
		}},
	}

	dc := newDoctorContext()
	dc.checkTriggers(diag)

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	for _, want := range []string{"test-on-change", "ben", "no space left on device"} {
		if !strings.Contains(warned, want) {
			t.Errorf("expected degraded warning to contain %q, got: %q", want, warned)
		}
	}

	// The retry schedule is printed as a hint (not stored in checks), so assert it
	// against the rendered output.
	rendered := buf.String()
	for _, want := range []string{"next attempt at 2026-07-15T10:00:00Z", "Retried 2 time(s)"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("expected doctor output to contain %q, got:\n%s", want, rendered)
		}
	}

	// A degraded binding is recoverable, so it must not fail the overall report.
	if !dc.ok {
		t.Error("degraded trigger should warn, not fail the report")
	}
}

// TestCheckTriggersHealthy verifies gr doctor stays quiet about triggers when no
// binding is degraded — no Triggers section noise on a healthy daemon.
func TestCheckTriggersHealthy(t *testing.T) {
	oldOut := out

	t.Cleanup(func() { out = oldOut })

	out = output.NewWithWriter(false, io.Discard)

	dc := newDoctorContext()
	dc.checkTriggers(&protocol.DiagnosticsMsg{})

	if len(dc.checks) != 0 {
		t.Errorf("expected no checks for healthy triggers, got %d", len(dc.checks))
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

// discardOut swaps the package-level output writer for one that discards and
// pins doctorAutofix to a known-off state, restoring both on cleanup. Isolating
// doctorAutofix here (not just out) means tests that read it implicitly — the
// storage/tmp/session checks all branch on it — never inherit a `true` leaked
// by an autofix test whose own cleanup regressed.
func discardOut(t *testing.T) {
	t.Helper()

	oldOut, oldFix := out, doctorAutofix

	t.Cleanup(func() { out, doctorAutofix = oldOut, oldFix })

	out = output.NewWithWriter(false, io.Discard)
	doctorAutofix = false
}

// deadPID starts and immediately reaps a short-lived process, returning its now-
// dead PID. This gives checkStalePID a deterministic non-graith target: the PID
// is not alive, so IsGraithDaemon returns false regardless of what the test
// binary happens to be named (using os.Getpid() would misfire if the binary
// were ever run as "gr"/"graith").
func deadPID(t *testing.T) int {
	t.Helper()

	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start throwaway process: %v", err)
	}

	_ = cmd.Wait() // reap; the PID is now dead

	return cmd.Process.Pid
}

// TestFormatBytesBoundaries checks the human-readable formatter across every
// unit boundary — bytes, KB, MB, GB — including the exact powers of 1024 where
// the switch flips to the next unit.
func TestFormatBytesBoundaries(t *testing.T) {
	cases := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024*1024 - 1, "1024.0 KB"}, // just below the MB flip
		{1024 * 1024, "1.0 MB"},
		{3 * 1024 * 1024, "3.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{5 * 1024 * 1024 * 1024, "5.0 GB"},
	}

	for _, c := range cases {
		if got := formatBytes(c.bytes); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
}

// TestDirSizeTree builds a nested directory tree with files of known sizes and
// verifies dirSize sums only the file bytes (directories contribute nothing).
func TestDirSizeTree(t *testing.T) {
	root := t.TempDir()

	// bothy/ (10 bytes) and bothy/glen/ (25 bytes) — 35 total across the tree.
	if err := os.WriteFile(filepath.Join(root, "braw.txt"), bytes.Repeat([]byte("a"), 10), 0o600); err != nil {
		t.Fatal(err)
	}

	sub := filepath.Join(root, "glen")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(sub, "loch.txt"), bytes.Repeat([]byte("b"), 25), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := dirSize(root)
	if err != nil {
		t.Fatalf("dirSize: %v", err)
	}

	if got != 35 {
		t.Errorf("dirSize = %d, want 35", got)
	}
}

// TestDirSizeEmpty verifies an empty directory sums to zero.
func TestDirSizeEmpty(t *testing.T) {
	got, err := dirSize(t.TempDir())
	if err != nil {
		t.Fatalf("dirSize: %v", err)
	}

	if got != 0 {
		t.Errorf("dirSize(empty) = %d, want 0", got)
	}
}

// TestDirSizeNonexistent verifies dirSize on a missing path returns 0 without
// error — the WalkDir callback swallows the lstat error, matching how doctor
// treats an absent data dir as simply having no size to report.
func TestDirSizeNonexistent(t *testing.T) {
	got, err := dirSize(filepath.Join(t.TempDir(), "haar-nae-sic-dir"))
	if err != nil {
		t.Fatalf("dirSize returned error for missing path: %v", err)
	}

	if got != 0 {
		t.Errorf("dirSize(missing) = %d, want 0", got)
	}
}

// TestTruncateFileKeepTailOverLimit verifies a file larger than the keep limit
// is truncated to exactly the trailing keepBytes.
func TestTruncateFileKeepTailOverLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")

	// 100 bytes: 50 'x' then 50 'y'. Keeping 50 must leave only the 'y' tail.
	content := append(bytes.Repeat([]byte("x"), 50), bytes.Repeat([]byte("y"), 50)...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := truncateFileKeepTail(path, 50); err != nil {
		t.Fatalf("truncateFileKeepTail: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if want := bytes.Repeat([]byte("y"), 50); !bytes.Equal(got, want) {
		t.Errorf("truncated content = %q, want the 50-byte 'y' tail", got)
	}
}

// TestTruncateFileKeepTailUnderLimit verifies a file at or below the limit is
// left byte-for-byte unchanged.
func TestTruncateFileKeepTailUnderLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wee.log")

	content := []byte("skelf") // 5 bytes, well under the limit
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := truncateFileKeepTail(path, 1024); err != nil {
		t.Fatalf("truncateFileKeepTail: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, content) {
		t.Errorf("under-limit file changed: got %q, want %q", got, content)
	}
}

// TestTruncateFileKeepTailExactLimit verifies a file exactly at the limit is
// unchanged — the boundary must not truncate (uses <= keepBytes).
func TestTruncateFileKeepTailExactLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exact.log")

	content := bytes.Repeat([]byte("z"), 64)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := truncateFileKeepTail(path, 64); err != nil {
		t.Fatalf("truncateFileKeepTail: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, content) {
		t.Errorf("exact-limit file changed: got %d bytes, want 64 unchanged", len(got))
	}
}

// TestTruncateFileKeepTailMissing verifies a read failure (missing file) is
// surfaced as an error rather than swallowed.
func TestTruncateFileKeepTailMissing(t *testing.T) {
	err := truncateFileKeepTail(filepath.Join(t.TempDir(), "thrawn-nae-file.log"), 10)
	if err == nil {
		t.Error("truncateFileKeepTail(missing) = nil, want error")
	}
}

// TestCheckSandboxBackendNoBackend verifies an enabled sandbox with no backend
// selected fails closed — mirroring the daemon's fail-closed rule.
func TestCheckSandboxBackendNoBackend(t *testing.T) {
	old := cfg

	t.Cleanup(func() { cfg = old })

	discardOut(t)

	cfg = &config.Config{}
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: ""}

	dc := newDoctorContext()
	dc.checkSandboxBackend()

	failed := strings.Join(checkResults(dc, "fail"), "\n")
	if !strings.Contains(failed, "no backend selected") {
		t.Errorf("expected fail for missing backend, got: %q", failed)
	}
}

// TestCheckSandboxBackendInvalid verifies an unknown backend name is reported as
// invalid (CheckAvailability returns an error).
func TestCheckSandboxBackendInvalid(t *testing.T) {
	old := cfg

	t.Cleanup(func() { cfg = old })

	discardOut(t)

	cfg = &config.Config{}
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: "thrawn-nae-backend"}

	dc := newDoctorContext()
	dc.checkSandboxBackend()

	failed := strings.Join(checkResults(dc, "fail"), "\n")
	if !strings.Contains(failed, "invalid") {
		t.Errorf("expected fail naming an invalid backend, got: %q", failed)
	}
}

// TestCheckStalePIDStale verifies checkStalePID fails when the PID file names a
// process that is not a live graith daemon. deadPID gives a reaped (dead) PID,
// so IsGraithDaemon returns false deterministically.
func TestCheckStalePIDStale(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	pidFile := filepath.Join(t.TempDir(), "graith.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(deadPID(t))), 0o600); err != nil {
		t.Fatal(err)
	}

	paths.PIDFile = pidFile

	dc := newDoctorContext()
	dc.checkStalePID()

	failed := strings.Join(checkResults(dc, "fail"), "\n")
	if !strings.Contains(failed, "stale") {
		t.Errorf("expected a stale-PID fail, got: %q", failed)
	}
}

// TestCheckStalePIDAutofix verifies --autofix both detects the stale PID and
// removes the file.
func TestCheckStalePIDAutofix(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	pidFile := filepath.Join(t.TempDir(), "graith.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(deadPID(t))), 0o600); err != nil {
		t.Fatal(err)
	}

	paths.PIDFile = pidFile
	doctorAutofix = true // restored by discardOut

	dc := newDoctorContext()
	dc.checkStalePID()

	if failed := strings.Join(checkResults(dc, "fail"), "\n"); !strings.Contains(failed, "stale") {
		t.Errorf("expected a stale-PID fail before removal, got: %q", failed)
	}

	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Errorf("expected stale PID file removed by autofix, stat err = %v", err)
	}
}

// TestCheckStalePIDNoFile verifies a missing PID file records nothing — there is
// no daemon to be stale about.
func TestCheckStalePIDNoFile(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	paths.PIDFile = filepath.Join(t.TempDir(), "absent.pid")

	dc := newDoctorContext()
	dc.checkStalePID()

	if len(dc.checks) != 0 {
		t.Errorf("expected no checks for absent PID file, got: %v", dc.checks)
	}
}

// TestCheckStalePIDGarbage verifies a PID file whose contents aren't a number is
// ignored (no panic, no check).
func TestCheckStalePIDGarbage(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	pidFile := filepath.Join(t.TempDir(), "garbage.pid")
	if err := os.WriteFile(pidFile, []byte("nae-a-number"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths.PIDFile = pidFile

	dc := newDoctorContext()
	dc.checkStalePID()

	if len(dc.checks) != 0 {
		t.Errorf("expected no checks for unparseable PID file, got: %v", dc.checks)
	}
}

// TestCheckSessionsClean verifies a fleet of healthy sessions records a single
// passing check when the sandbox is enabled (so the isolation warning is off).
func TestCheckSessionsClean(t *testing.T) {
	old := cfg

	t.Cleanup(func() { cfg = old })

	discardOut(t)

	cfg = &config.Config{}
	cfg.Sandbox = config.SandboxConfig{Enabled: true}

	diag := &protocol.DiagnosticsMsg{
		Fleet: protocol.FleetSummary{Total: 1, Active: 1},
		Sessions: []protocol.SessionDiagnostic{
			{ID: "abc", Name: "braw", Status: "running", PID: 42, PIDAlive: true, HasToken: true},
		},
	}

	dc := newDoctorContext()
	dc.checkSessions(diag)

	if failed := checkResults(dc, "fail"); len(failed) != 0 {
		t.Errorf("healthy fleet should not fail, got: %v", failed)
	}

	if passed := strings.Join(checkResults(dc, "ok"), "\n"); !strings.Contains(passed, "No issues found") {
		t.Errorf("expected 'No issues found', got: %q", passed)
	}
}

// TestCheckSessionsDeadPID verifies a session marked running whose PID is not
// alive is reported as a failure.
func TestCheckSessionsDeadPID(t *testing.T) {
	old := cfg

	t.Cleanup(func() { cfg = old })

	discardOut(t)

	cfg = &config.Config{}
	cfg.Sandbox = config.SandboxConfig{Enabled: true}

	diag := &protocol.DiagnosticsMsg{
		Fleet: protocol.FleetSummary{Total: 1},
		Sessions: []protocol.SessionDiagnostic{
			{ID: "def", Name: "dreich", Status: "running", PID: 99, PIDAlive: false, HasToken: true},
		},
	}

	dc := newDoctorContext()
	dc.checkSessions(diag)

	failed := strings.Join(checkResults(dc, "fail"), "\n")
	if !strings.Contains(failed, "not alive but status is running") {
		t.Errorf("expected dead-PID fail, got: %q", failed)
	}
}

// TestCheckSessionsIssues exercises the range of per-session problems: an
// orphaned process, a missing worktree, config drift, saturation, and a missing
// auth token. Each should record a fail or warn.
func TestCheckSessionsIssues(t *testing.T) {
	old := cfg

	t.Cleanup(func() { cfg = old })

	discardOut(t)

	cfg = &config.Config{}
	cfg.Sandbox = config.SandboxConfig{Enabled: true}

	no := false

	diag := &protocol.DiagnosticsMsg{
		Fleet: protocol.FleetSummary{Total: 3, Errored: 1},
		Sessions: []protocol.SessionDiagnostic{
			// Orphaned: alive but no PTY managed by daemon.
			{ID: "1", Name: "canny", Status: "running", PID: 10, PIDAlive: true, HasPTY: &no, HasToken: true},
			// Errored with PID still recorded.
			{ID: "2", Name: "fash", Status: "errored", PID: 20, HasToken: true},
			// Worktree missing + config drift + saturated + no token.
			{
				ID: "3", Name: "haar", Status: "stopped",
				WorktreePath: "/nae/sic/path", WorktreeExists: false,
				ConfigStale: true, Saturated: true, ScrollbackMax: 2048, HasToken: false,
			},
		},
	}

	dc := newDoctorContext()
	dc.checkSessions(diag)

	failed := strings.Join(checkResults(dc, "fail"), "\n")
	warned := strings.Join(checkResults(dc, "warn"), "\n")

	if !strings.Contains(failed, "orphaned after crash") {
		t.Errorf("expected orphaned-process fail, got: %q", failed)
	}

	if !strings.Contains(failed, "worktree path does not exist") {
		t.Errorf("expected missing-worktree fail, got: %q", failed)
	}

	if !strings.Contains(warned, "errored with PID") {
		t.Errorf("expected errored-PID warn, got: %q", warned)
	}

	if !strings.Contains(warned, "config has drifted") {
		t.Errorf("expected config-drift warn, got: %q", warned)
	}

	if !strings.Contains(warned, "scrollback saturated") {
		t.Errorf("expected saturation warn, got: %q", warned)
	}

	if !strings.Contains(warned, "missing auth token") {
		t.Errorf("expected missing-token warn, got: %q", warned)
	}
}

// TestCheckSessionsSandboxDisabledMultiRunning verifies the isolation warning
// fires when the sandbox is off and more than one session is running.
func TestCheckSessionsSandboxDisabledMultiRunning(t *testing.T) {
	old := cfg

	t.Cleanup(func() { cfg = old })

	discardOut(t)

	cfg = &config.Config{}
	cfg.Sandbox = config.SandboxConfig{Enabled: false}

	diag := &protocol.DiagnosticsMsg{
		Fleet: protocol.FleetSummary{Total: 2, Active: 2},
		Sessions: []protocol.SessionDiagnostic{
			{ID: "1", Name: "braw", Status: "running", PID: 10, PIDAlive: true, HasToken: true},
			{ID: "2", Name: "bonnie", Status: "running", PID: 11, PIDAlive: true, HasToken: true},
		},
	}

	dc := newDoctorContext()
	dc.checkSessions(diag)

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "Sandbox is disabled with 2 running sessions") {
		t.Errorf("expected sandbox-disabled isolation warn, got: %q", warned)
	}
}

// TestCheckStorage exercises the storage section end-to-end: scrollback and
// message counts pass, an orphaned .log file (no matching session) warns, and
// the tmp-dir sub-check runs against an empty tmp dir.
// stubNoOrphans replaces the daemon GC round-trip with a no-op returning no
// orphans, so checkStorage tests that aren't about orphan cleanup neither dial
// the socket nor (critically) reach the auto-starting client path.
func stubNoOrphans(t *testing.T) {
	t.Helper()

	old := daemonGCFetch
	daemonGCFetch = func(bool) ([]protocol.GCOrphanInfo, error) { return nil, nil }

	t.Cleanup(func() { daemonGCFetch = old })
}

func TestCheckStorage(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	stubNoOrphans(t)
	discardOut(t)

	dataDir := t.TempDir()

	logDir := filepath.Join(dataDir, "logs")
	if err := os.Mkdir(logDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// One log belongs to a live session, one is orphaned.
	if err := os.WriteFile(filepath.Join(logDir, "live.log"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(logDir, "orphan.log"), []byte("bye"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths.DataDir = dataDir
	paths.LogDir = logDir
	paths.TmpDir = filepath.Join(dataDir, "tmp") // absent → treated as empty

	diag := &protocol.DiagnosticsMsg{
		Scrollback: protocol.ScrollbackDiagnostic{TotalFiles: 2, TotalBytes: 5},
		Messages:   protocol.MessagesDiagnostic{TotalStreams: 1, TotalMessages: 3},
		Sessions:   []protocol.SessionDiagnostic{{ID: "live"}},
	}

	dc := newDoctorContext()
	dc.checkStorage(diag)

	passed := strings.Join(checkResults(dc, "ok"), "\n")
	if !strings.Contains(passed, "Messages: 1 streams, 3 messages") {
		t.Errorf("expected messages summary, got: %q", passed)
	}

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "orphaned scrollback file") {
		t.Errorf("expected orphaned scrollback warn, got: %q", warned)
	}
}

// TestCheckStorageSaturatedScrollback verifies a saturated scrollback count is
// surfaced as a warning rather than a plain pass.
func TestCheckStorageSaturatedScrollback(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	stubNoOrphans(t)
	discardOut(t)

	dataDir := t.TempDir()
	paths.DataDir = dataDir
	paths.LogDir = filepath.Join(dataDir, "logs") // absent → no orphans
	paths.TmpDir = filepath.Join(dataDir, "tmp")

	diag := &protocol.DiagnosticsMsg{
		Scrollback: protocol.ScrollbackDiagnostic{TotalFiles: 4, TotalBytes: 4096, SaturatedCount: 2},
		Messages:   protocol.MessagesDiagnostic{},
	}

	dc := newDoctorContext()
	dc.checkStorage(diag)

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "2 saturated") {
		t.Errorf("expected saturated scrollback warn, got: %q", warned)
	}
}

// TestCheckTmpDirEmpty verifies an absent tmp dir reports as empty.
func TestCheckTmpDirEmpty(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	paths.TmpDir = filepath.Join(t.TempDir(), "nae-tmp")

	dc := newDoctorContext()
	dc.checkTmpDir()

	passed := strings.Join(checkResults(dc, "ok"), "\n")
	if !strings.Contains(passed, "empty") {
		t.Errorf("expected empty tmp dir report, got: %q", passed)
	}
}

// TestCheckTmpDirWithRepos builds the <tmp>/<repoName>/<repoHash> layout doctor
// walks and verifies the repo count and non-empty size are reported. Sizes are
// only computed under --disk (the default run skips the walk for speed), so this
// exercises the --disk path.
func TestCheckTmpDirWithRepos(t *testing.T) {
	oldPaths, oldDisk := paths, doctorDisk

	t.Cleanup(func() { paths, doctorDisk = oldPaths, oldDisk })

	discardOut(t)

	doctorDisk = true

	tmpDir := t.TempDir()

	// tmp/croft/<hash>/file — one repo checkout with content.
	hashDir := filepath.Join(tmpDir, "croft", "deadbeef")
	if err := os.MkdirAll(hashDir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(hashDir, "neep.txt"), bytes.Repeat([]byte("x"), 128), 0o600); err != nil {
		t.Fatal(err)
	}

	paths.TmpDir = tmpDir

	dc := newDoctorContext()
	dc.checkTmpDir()

	passed := strings.Join(checkResults(dc, "ok"), "\n")
	if !strings.Contains(passed, "1 repo(s)") {
		t.Errorf("expected '1 repo(s)' in tmp dir report, got: %q", passed)
	}

	// Under --disk the 128-byte checkout must be summed and rendered — guards
	// against dirSize being dropped or the total always reported as 0 B.
	if !strings.Contains(passed, "128 B") {
		t.Errorf("expected summed checkout size '128 B' in tmp dir report, got: %q", passed)
	}
}

// TestCheckTmpDirLegacyShareDir verifies the legacy sibling share/ dir (renamed
// to tmp/ in v0.39.0) is surfaced as a warning.
func TestCheckTmpDirLegacyShareDir(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	base := t.TempDir()

	tmpDir := filepath.Join(base, "tmp")
	if err := os.Mkdir(tmpDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Sibling legacy share/ dir with content.
	shareDir := filepath.Join(base, "share")
	if err := os.Mkdir(shareDir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(shareDir, "auld.txt"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths.TmpDir = tmpDir

	dc := newDoctorContext()
	dc.checkTmpDir()

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "Legacy share dir exists") {
		t.Errorf("expected legacy share dir warn, got: %q", warned)
	}
}

// TestCheckStorageOrphanedWorktree verifies checkStorage surfaces the daemon's
// reported orphaned worktree dirs as a warning with a per-path hint. Detection
// itself lives on the daemon (internal/daemon gc tests); here the daemon fetch
// is stubbed so the CLI rendering can be exercised without a running daemon.
func TestCheckStorageOrphanedWorktree(t *testing.T) {
	oldPaths, oldFetch := paths, daemonGCFetch

	t.Cleanup(func() { paths, daemonGCFetch = oldPaths, oldFetch })

	discardOut(t)

	dataDir := t.TempDir()
	paths.DataDir = dataDir
	paths.LogDir = filepath.Join(dataDir, "logs") // absent → no orphan logs
	paths.TmpDir = filepath.Join(dataDir, "tmp")

	orphanWT := filepath.Join(dataDir, "worktrees", "croft", "deadbeef", "gane-sess")

	daemonGCFetch = func(_ bool) ([]protocol.GCOrphanInfo, error) {
		return []protocol.GCOrphanInfo{{Type: "worktree", Path: orphanWT, ID: "gane-sess"}}, nil
	}

	diag := &protocol.DiagnosticsMsg{
		Scrollback: protocol.ScrollbackDiagnostic{},
		Messages:   protocol.MessagesDiagnostic{},
		Sessions:   []protocol.SessionDiagnostic{},
	}

	dc := newDoctorContext()
	dc.checkStorage(diag)

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "orphaned worktree dir") {
		t.Errorf("expected orphaned worktree warn, got: %q", warned)
	}
}

// TestCheckStorageOrphanedWorktreeAutofix verifies that --autofix runs a dry-run
// listing first and then a force pass, and reports the removed/skipped counts
// the daemon returns.
func TestCheckStorageOrphanedWorktreeAutofix(t *testing.T) {
	oldPaths, oldFetch := paths, daemonGCFetch

	t.Cleanup(func() { paths, daemonGCFetch = oldPaths, oldFetch })

	dataDir := t.TempDir()
	paths.DataDir = dataDir
	paths.LogDir = filepath.Join(dataDir, "logs")
	paths.TmpDir = filepath.Join(dataDir, "tmp")

	oldFix := doctorAutofix
	doctorAutofix = true

	t.Cleanup(func() { doctorAutofix = oldFix })

	var calls []bool // records force flag per call, in order

	daemonGCFetch = func(force bool) ([]protocol.GCOrphanInfo, error) {
		calls = append(calls, force)
		if force {
			return []protocol.GCOrphanInfo{
				{Type: "worktree", Path: "/x/braw", ID: "braw", Removed: true},
				{Type: "worktree", Path: "/x/dreich", ID: "dreich", HasDirtyFiles: true, Skipped: true, Reason: "uncommitted changes — remove manually"},
			}, nil
		}

		return []protocol.GCOrphanInfo{
			{Type: "worktree", Path: "/x/braw", ID: "braw"},
			{Type: "worktree", Path: "/x/dreich", ID: "dreich", HasDirtyFiles: true},
		}, nil
	}

	diag := &protocol.DiagnosticsMsg{Sessions: []protocol.SessionDiagnostic{}}

	dc := newDoctorContext()

	got := captureStdout(t, func() { dc.checkStorage(diag) })

	// Dry-run before force: first call force=false, second force=true.
	if len(calls) != 2 || calls[0] != false || calls[1] != true {
		t.Fatalf("expected [dry-run, force] call order, got %v", calls)
	}

	if !strings.Contains(got, "Removed 1 orphaned worktree dir") {
		t.Errorf("expected 'Removed 1' in output, got: %q", got)
	}

	if !strings.Contains(got, "Skipped 1") {
		t.Errorf("expected 'Skipped 1' in output, got: %q", got)
	}
}

// TestCheckStorageOrphanUndeterminedLabel verifies an orphan whose git state
// could not be determined is labelled distinctly (not as confirmed WIP).
func TestCheckStorageOrphanUndeterminedLabel(t *testing.T) {
	oldPaths, oldFetch, oldFix := paths, daemonGCFetch, doctorAutofix

	t.Cleanup(func() { paths, daemonGCFetch, doctorAutofix = oldPaths, oldFetch, oldFix })

	dataDir := t.TempDir()
	paths.DataDir = dataDir
	paths.LogDir = filepath.Join(dataDir, "logs")
	paths.TmpDir = filepath.Join(dataDir, "tmp")
	doctorAutofix = false

	daemonGCFetch = func(bool) ([]protocol.GCOrphanInfo, error) {
		return []protocol.GCOrphanInfo{
			{Type: "worktree", Path: "/x/haar", ID: "haar", HasDirtyFiles: true, DirtyUndetermined: true},
		}, nil
	}

	dc := newDoctorContext()

	got := captureStdout(t, func() { dc.checkStorage(&protocol.DiagnosticsMsg{}) })

	if !strings.Contains(got, "[git state undetermined]") {
		t.Errorf("expected undetermined label, got: %q", got)
	}

	if strings.Contains(got, "[has uncommitted changes]") {
		t.Errorf("undetermined orphan mislabelled as confirmed WIP: %q", got)
	}
}

// TestCheckEnvironment drives the Environment section against a temp data dir
// with no running daemon: an existing config file, sized state/messages files,
// a sandbox disabled (so it warns), and an empty agent prompt. It asserts the
// section reports the config path, records the file sizes, and surfaces the
// sandbox-disabled and empty-prompt warnings.
func TestCheckEnvironment(t *testing.T) {
	oldCfg, oldPaths, oldCfgFile := cfg, paths, cfgFile

	t.Cleanup(func() { cfg, paths, cfgFile = oldCfg, oldPaths, oldCfgFile })

	discardOut(t)

	dir := t.TempDir()

	cfgFile = filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte("[sandbox]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "messages.db"), bytes.Repeat([]byte("m"), 42), 0o600); err != nil {
		t.Fatal(err)
	}

	paths = config.Paths{
		DataDir:    dir,
		DaemonLog:  filepath.Join(dir, "daemon.log"), // absent → plain pass
		StateFile:  filepath.Join(dir, "state.json"),
		MessagesDB: filepath.Join(dir, "messages.db"),
	}

	cfg = &config.Config{}
	cfg.Sandbox = config.SandboxConfig{Enabled: false}
	cfg.AgentPrompt = "" // → empty-prompt warning

	dc := newDoctorContext()
	dc.checkEnvironment()

	passed := strings.Join(checkResults(dc, "ok"), "\n")
	if !strings.Contains(passed, cfgFile) {
		t.Errorf("expected config path %q in a passing check, got: %q", cfgFile, passed)
	}

	if !strings.Contains(passed, "Messages DB") {
		t.Errorf("expected Messages DB check, got: %q", passed)
	}

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "Sandbox disabled") {
		t.Errorf("expected sandbox-disabled warn, got: %q", warned)
	}

	if !strings.Contains(warned, "Agent prompt is empty") {
		t.Errorf("expected empty-prompt warn, got: %q", warned)
	}
}

// TestCheckEnvironmentLargeDaemonLog verifies a daemon log over the 10 MB
// threshold is warned about, and that --autofix truncates it to exactly the
// trailing 1 MB. The log is 10 MB of 'H' (head) followed by exactly 1 MB of 'T'
// (tail) with distinct bytes, so the assertions pin both the 1 MB keep size
// checkEnvironment wires into truncateFileKeepTail and that it keeps the tail
// (not the head) — a regression to a different keep size, or to head-keeping,
// fails here even though the low-level helper test also covers the tail.
func TestCheckEnvironmentLargeDaemonLog(t *testing.T) {
	oldCfg, oldPaths, oldCfgFile := cfg, paths, cfgFile

	t.Cleanup(func() { cfg, paths, cfgFile = oldCfg, oldPaths, oldCfgFile })

	discardOut(t)

	dir := t.TempDir()

	cfgFile = filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte("[sandbox]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const oneMB = 1024 * 1024

	// 11 MB total: 10 MB 'H' head + exactly 1 MB 'T' tail. Over the 10 MB warn
	// threshold; the kept tail must be the pure 'T' block.
	logPath := filepath.Join(dir, "daemon.log")

	content := append(bytes.Repeat([]byte("H"), 10*oneMB), bytes.Repeat([]byte("T"), oneMB)...)

	if err := os.WriteFile(logPath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	paths = config.Paths{DataDir: dir, DaemonLog: logPath}

	cfg = &config.Config{}
	cfg.Sandbox = config.SandboxConfig{Enabled: false}

	doctorAutofix = true // restored by discardOut

	dc := newDoctorContext()
	dc.checkEnvironment()

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "Daemon log") {
		t.Errorf("expected daemon-log size warn, got: %q", warned)
	}

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	if int64(len(got)) != oneMB {
		t.Errorf("autofix should have truncated daemon log to exactly 1 MB, size now %d", len(got))
	}

	if want := bytes.Repeat([]byte("T"), oneMB); !bytes.Equal(got, want) {
		t.Errorf("autofix kept the wrong bytes: expected the 1 MB 'T' tail, got head bytes present=%v", bytes.Contains(got, []byte("H")))
	}
}

// TestSectionEmitsHeader verifies section() writes the header text to the output
// writer (blank line + name) without recording a check.
func TestSectionEmitsHeader(t *testing.T) {
	old := out

	t.Cleanup(func() { out = old })

	var buf bytes.Buffer

	out = output.NewWithWriter(false, &buf)

	dc := newDoctorContext()
	dc.section("Kirk")

	if !strings.Contains(buf.String(), "Kirk") {
		t.Errorf("expected section header %q in output, got: %q", "Kirk", buf.String())
	}

	if len(dc.checks) != 0 {
		t.Errorf("section should not record a check, got: %v", dc.checks)
	}
}

// TestCheckVersionCov2NoSocket verifies the version section against a host with
// no daemon socket: it records the CLI version and skips the daemon-version
// probe entirely (no fail). version.Version is "dev" in tests, so the update
// check short-circuits and never touches the network.
func TestCheckVersionCov2NoSocket(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	dir := t.TempDir()
	paths.DataDir = dir
	paths.SocketPath = filepath.Join(dir, "absent.sock")

	dc := newDoctorContext()

	dc.checkVersion(daemonProbe{reach: daemonReachNoSocket})

	if failed := checkResults(dc, "fail"); len(failed) != 0 {
		t.Errorf("no-socket version check should not fail, got: %v", failed)
	}

	passed := strings.Join(checkResults(dc, "ok"), "\n")
	if !strings.Contains(passed, "CLI version") {
		t.Errorf("expected CLI version pass, got: %q", passed)
	}
}

// TestCheckVersionCov2StaleSocket verifies that a socket path which exists but is
// not a live listener (here a plain file) is reported as "daemon not responding"
// and, under --autofix, the stale socket file is removed.
func TestCheckVersionCov2StaleSocket(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	dir := t.TempDir()
	paths.DataDir = dir

	// A regular file at the socket path: os.Stat succeeds, but DialTimeout on a
	// non-socket fails, driving the "not responding" branch.
	sockPath := filepath.Join(dir, "graith.sock")
	if err := os.WriteFile(sockPath, []byte("nae a socket"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths.SocketPath = sockPath
	doctorAutofix = true // restored by discardOut

	dc := newDoctorContext()
	dc.checkVersion(daemonProbe{reach: daemonReachDown})

	failed := strings.Join(checkResults(dc, "fail"), "\n")
	if !strings.Contains(failed, "not responding") {
		t.Errorf("expected 'daemon not responding' fail, got: %q", failed)
	}

	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("expected autofix to remove stale socket, stat err = %v", err)
	}
}

// TestCheckDaemonCov2NotRunning verifies the daemon section reports "not running"
// (a warn, not a fail) and returns nil diagnostics when no socket exists.
func TestCheckDaemonCov2NotRunning(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	paths.SocketPath = filepath.Join(t.TempDir(), "absent.sock")

	dc := newDoctorContext()

	diag := dc.checkDaemon(daemonProbe{reach: daemonReachNoSocket})
	if diag != nil {
		t.Errorf("expected nil diagnostics with no socket, got %+v", diag)
	}

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "Not running") {
		t.Errorf("expected 'Not running' warn, got: %q", warned)
	}
}

// TestCheckDaemonCov2CannotConnect verifies that a socket path that exists but
// can't be dialled fails the daemon check and triggers the stale-PID sub-check.
func TestCheckDaemonCov2CannotConnect(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	dir := t.TempDir()

	sockPath := filepath.Join(dir, "graith.sock")
	if err := os.WriteFile(sockPath, []byte("nae a socket"), 0o600); err != nil {
		t.Fatal(err)
	}

	pidFile := filepath.Join(dir, "graith.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(deadPID(t))), 0o600); err != nil {
		t.Fatal(err)
	}

	paths.SocketPath = sockPath
	paths.PIDFile = pidFile

	dc := newDoctorContext()

	diag := dc.checkDaemon(daemonProbe{reach: daemonReachDown, dialErr: errors.New("connection refused")})
	if diag != nil {
		t.Errorf("expected nil diagnostics when daemon unreachable, got %+v", diag)
	}

	failed := strings.Join(checkResults(dc, "fail"), "\n")
	if !strings.Contains(failed, "Cannot connect") {
		t.Errorf("expected 'Cannot connect' fail, got: %q", failed)
	}

	// checkStalePID should have run and flagged the dead PID.
	if !strings.Contains(failed, "stale") {
		t.Errorf("expected stale-PID fail from checkStalePID, got: %q", failed)
	}
}

// TestCheckApprovalsBackendCov2Deprecation verifies a legacy [approvals] mode
// (with no explicit backend) surfaces the deprecation nudge as a warning. Using
// mode="localmost" maps to the "command" backend, which — with no command set —
// also fails closed, so both the deprecation warn and the enforce fail appear.
func TestCheckApprovalsBackendCov2Deprecation(t *testing.T) {
	old := cfg

	t.Cleanup(func() { cfg = old })

	discardOut(t)

	cfg = &config.Config{}
	cfg.Approvals = config.Approvals{Mode: "localmost"}

	dc := newDoctorContext()
	dc.checkApprovalsBackend()

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "deprecated") {
		t.Errorf("expected a deprecation warn for legacy mode, got: %q", warned)
	}
}

// TestCheckConfigKeysCov2NoSuggestion verifies an unknown key with no close
// known neighbour warns without a "did you mean" suggestion.
func TestCheckConfigKeysCov2NoSuggestion(t *testing.T) {
	discardOut(t)

	dir := t.TempDir()

	// A top-level key far from any known key: no suggestion should be offered.
	cfgPath := filepath.Join(dir, "haar.toml")
	if err := os.WriteFile(cfgPath, []byte("zxqwv_nonsense = 42\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dc := newDoctorContext()
	dc.checkConfigKeys(cfgPath)

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "zxqwv_nonsense") {
		t.Errorf("expected warn naming the unknown key, got: %q", warned)
	}

	if strings.Contains(warned, "did you mean") {
		t.Errorf("did not expect a suggestion for a distant key, got: %q", warned)
	}
}

// TestCheckConfigKeysCov2ParseError verifies a config file that can't be parsed
// records nothing — the daemon's own config load already reports parse failures,
// so doctor must not double-report them.
func TestCheckConfigKeysCov2ParseError(t *testing.T) {
	discardOut(t)

	cfgPath := filepath.Join(t.TempDir(), "broken.toml")
	if err := os.WriteFile(cfgPath, []byte("this is = = not valid toml ["), 0o600); err != nil {
		t.Fatal(err)
	}

	dc := newDoctorContext()
	dc.checkConfigKeys(cfgPath)

	if len(dc.checks) != 0 {
		t.Errorf("expected no checks recorded on parse error, got: %v", dc.checks)
	}
}

// TestCheckStorageCov2OrphanScrollbackAutofix verifies --autofix deletes an
// orphaned scrollback .log (no matching session) while leaving a live one.
func TestCheckStorageCov2OrphanScrollbackAutofix(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	stubNoOrphans(t)
	discardOut(t)

	dataDir := t.TempDir()

	logDir := filepath.Join(dataDir, "logs")
	if err := os.Mkdir(logDir, 0o750); err != nil {
		t.Fatal(err)
	}

	liveLog := filepath.Join(logDir, "live.log")
	orphanLog := filepath.Join(logDir, "gane.log")

	for _, p := range []string{liveLog, orphanLog} {
		if err := os.WriteFile(p, []byte("scrollback"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	paths.DataDir = dataDir
	paths.LogDir = logDir
	paths.TmpDir = filepath.Join(dataDir, "tmp")

	doctorAutofix = true // restored by discardOut

	diag := &protocol.DiagnosticsMsg{
		Scrollback: protocol.ScrollbackDiagnostic{},
		Messages:   protocol.MessagesDiagnostic{},
		Sessions:   []protocol.SessionDiagnostic{{ID: "live"}},
	}

	dc := newDoctorContext()
	dc.checkStorage(diag)

	if _, err := os.Stat(orphanLog); !os.IsNotExist(err) {
		t.Errorf("expected orphaned scrollback removed by autofix, stat err = %v", err)
	}

	if _, err := os.Stat(liveLog); err != nil {
		t.Errorf("live scrollback must be preserved, stat err = %v", err)
	}
}

// TestCheckStorageAutofixPreservesSoftDeletedScrollback is the regression test
// for doctor treating a recoverable session's scrollback as an orphan. Deleted
// sessions are intentionally absent from the live diagnostics list, so their
// IDs must be carried separately when doctor decides which logs it may remove.
func TestCheckStorageAutofixPreservesSoftDeletedScrollback(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	stubNoOrphans(t)
	discardOut(t)

	dataDir := t.TempDir()

	logDir := filepath.Join(dataDir, "logs")
	if err := os.Mkdir(logDir, 0o750); err != nil {
		t.Fatal(err)
	}

	liveLog := filepath.Join(logDir, "braw.log")
	deletedLog := filepath.Join(logDir, "bide.log")
	orphanLog := filepath.Join(logDir, "gane.log")

	for _, p := range []string{liveLog, deletedLog, orphanLog} {
		if err := os.WriteFile(p, []byte("scrollback"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	paths.DataDir = dataDir
	paths.LogDir = logDir
	paths.TmpDir = filepath.Join(dataDir, "tmp")
	doctorAutofix = true // restored by discardOut

	diag := &protocol.DiagnosticsMsg{
		Sessions:          []protocol.SessionDiagnostic{{ID: "braw"}},
		DeletedSessionIDs: []string{"bide"},
	}

	dc := newDoctorContext()
	dc.checkStorage(diag)

	for _, p := range []string{liveLog, deletedLog} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("owned scrollback %q must be preserved: %v", filepath.Base(p), err)
		}
	}

	if _, err := os.Stat(orphanLog); !os.IsNotExist(err) {
		t.Errorf("orphaned scrollback should be removed, stat err = %v", err)
	}
}
