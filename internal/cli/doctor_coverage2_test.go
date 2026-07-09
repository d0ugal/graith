package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

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
	report := &doctorReport{}

	daemonVersion := dc.checkVersion(report)

	if daemonVersion != "" {
		t.Errorf("expected empty daemon version with no socket, got %q", daemonVersion)
	}

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
	dc.checkVersion(&doctorReport{})

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

	diag := dc.checkDaemon("")
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

	diag := dc.checkDaemon("")
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

// TestFindOrphanedInWorktreesCov2GitDirty builds a real git worktree with an
// untracked (dirty) file under the worktrees/<repo>/<hash>/<sess> layout, ages
// it past orphanMinAge, and verifies findOrphanedInWorktrees flags it as a git
// worktree with uncommitted changes so autofix will skip it.
func TestFindOrphanedInWorktreesCov2GitDirty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dataDir := t.TempDir()
	worktreesDir := filepath.Join(dataDir, "worktrees")

	sessDir := filepath.Join(worktreesDir, "croft", "deadbeef", "gane-sess")
	if err := os.MkdirAll(sessDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// A bare `git init` plus an untracked file makes HasUncommittedChanges true
	// without needing a commit (which the sandboxed env can't always produce).
	if out, err := exec.Command("git", "-C", sessDir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}

	if err := os.WriteFile(filepath.Join(sessDir, "skelf.txt"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(sessDir, old, old); err != nil {
		t.Fatal(err)
	}

	repos, err := os.ReadDir(worktreesDir)
	if err != nil {
		t.Fatal(err)
	}

	orphans := findOrphanedInWorktrees(repos, worktreesDir, map[string]bool{}, time.Now())
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %d (%+v)", len(orphans), orphans)
	}

	o := orphans[0]
	if !o.isGitWorktree {
		t.Error("expected isGitWorktree = true for a git-initialised dir")
	}

	if !o.hasDirtyFiles {
		t.Error("expected hasDirtyFiles = true for an untracked file")
	}
}
