package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
)

// discardOut swaps the package-level output writer for one that discards, and
// restores it on cleanup. Returns nothing — callers just want the side effect.
func discardOut(t *testing.T) {
	t.Helper()

	old := out
	t.Cleanup(func() { out = old })

	out = output.NewWithWriter(false, io.Discard)
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
	if err := os.Mkdir(sub, 0o755); err != nil {
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
// process that is not a graith daemon. The test's own PID is alive but its
// process name is not "gr"/"graith", so IsGraithDaemon reports it stale.
func TestCheckStalePIDStale(t *testing.T) {
	oldPaths := paths
	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	pidFile := filepath.Join(t.TempDir(), "graith.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
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

// TestCheckStalePIDAutofix verifies --autofix removes a stale PID file.
func TestCheckStalePIDAutofix(t *testing.T) {
	oldPaths, oldFix := paths, doctorAutofix
	t.Cleanup(func() { paths, doctorAutofix = oldPaths, oldFix })

	discardOut(t)

	pidFile := filepath.Join(t.TempDir(), "graith.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	paths.PIDFile = pidFile
	doctorAutofix = true

	dc := newDoctorContext()
	dc.checkStalePID()

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
func TestCheckStorage(t *testing.T) {
	oldPaths := paths
	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	dataDir := t.TempDir()

	logDir := filepath.Join(dataDir, "logs")
	if err := os.Mkdir(logDir, 0o755); err != nil {
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
// walks and verifies the repo count and non-empty size are reported.
func TestCheckTmpDirWithRepos(t *testing.T) {
	oldPaths := paths
	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	tmpDir := t.TempDir()

	// tmp/croft/<hash>/file — one repo checkout with content.
	hashDir := filepath.Join(tmpDir, "croft", "deadbeef")
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
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
}

// TestCheckTmpDirLegacyShareDir verifies the legacy sibling share/ dir (renamed
// to tmp/ in v0.39.0) is surfaced as a warning.
func TestCheckTmpDirLegacyShareDir(t *testing.T) {
	oldPaths := paths
	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	base := t.TempDir()

	tmpDir := filepath.Join(base, "tmp")
	if err := os.Mkdir(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Sibling legacy share/ dir with content.
	shareDir := filepath.Join(base, "share")
	if err := os.Mkdir(shareDir, 0o755); err != nil {
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

// TestFindOrphanedWorktrees builds the on-disk worktree and scratch layouts
// doctor walks, ages them past orphanMinAge, and verifies a session ID absent
// from the live set is reported as orphaned while a live one is skipped.
func TestFindOrphanedWorktrees(t *testing.T) {
	oldPaths := paths
	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	dataDir := t.TempDir()
	paths.DataDir = dataDir

	// Worktrees live at <DataDir>/worktrees/<repoName>/<repoHash>/<sessionID>.
	orphanWT := filepath.Join(dataDir, "worktrees", "croft", "deadbeef", "orphan-sess")
	liveWT := filepath.Join(dataDir, "worktrees", "croft", "deadbeef", "live-sess")

	for _, d := range []string{orphanWT, liveWT} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(d, "whin.txt"), []byte("bide"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Scratch dirs live at <DataDir>/scratch/<sessionID>.
	orphanScratch := filepath.Join(dataDir, "scratch", "orphan-scratch")
	if err := os.MkdirAll(orphanScratch, 0o755); err != nil {
		t.Fatal(err)
	}

	// Age every candidate past orphanMinAge so the recency guard doesn't skip
	// them. Use a fixed epoch well in the past.
	old := time.Now().Add(-time.Hour)
	for _, d := range []string{orphanWT, liveWT, orphanScratch} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}

	sessionIDs := map[string]bool{"live-sess": true}

	dc := newDoctorContext()
	orphans := dc.findOrphanedWorktrees(sessionIDs)

	var paths_ []string
	for _, o := range orphans {
		paths_ = append(paths_, o.path)
	}

	joined := strings.Join(paths_, "\n")

	if !strings.Contains(joined, orphanWT) {
		t.Errorf("expected orphaned worktree %q, got: %v", orphanWT, paths_)
	}

	if !strings.Contains(joined, orphanScratch) {
		t.Errorf("expected orphaned scratch dir %q, got: %v", orphanScratch, paths_)
	}

	if strings.Contains(joined, liveWT) {
		t.Errorf("live session worktree %q should not be reported orphaned", liveWT)
	}
}

// TestCheckStorageOrphanedWorktree verifies checkStorage surfaces an aged
// orphaned worktree dir as a warning with a per-path hint.
func TestCheckStorageOrphanedWorktree(t *testing.T) {
	oldPaths := paths
	t.Cleanup(func() { paths = oldPaths })

	discardOut(t)

	dataDir := t.TempDir()
	paths.DataDir = dataDir
	paths.LogDir = filepath.Join(dataDir, "logs") // absent → no orphan logs
	paths.TmpDir = filepath.Join(dataDir, "tmp")

	orphanWT := filepath.Join(dataDir, "worktrees", "croft", "deadbeef", "gane-sess")
	if err := os.MkdirAll(orphanWT, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(orphanWT, "neep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(orphanWT, old, old); err != nil {
		t.Fatal(err)
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
// threshold is warned about, and that --autofix truncates it to the tail.
func TestCheckEnvironmentLargeDaemonLog(t *testing.T) {
	oldCfg, oldPaths, oldCfgFile, oldFix := cfg, paths, cfgFile, doctorAutofix
	t.Cleanup(func() { cfg, paths, cfgFile, doctorAutofix = oldCfg, oldPaths, oldCfgFile, oldFix })

	discardOut(t)

	dir := t.TempDir()

	cfgFile = filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte("[sandbox]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 11 MB daemon log — over the 10 MB warn threshold.
	logPath := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(logPath, bytes.Repeat([]byte("z"), 11*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}

	paths = config.Paths{DataDir: dir, DaemonLog: logPath}

	cfg = &config.Config{}
	cfg.Sandbox = config.SandboxConfig{Enabled: false}

	doctorAutofix = true

	dc := newDoctorContext()
	dc.checkEnvironment()

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "Daemon log") {
		t.Errorf("expected daemon-log size warn, got: %q", warned)
	}

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}

	if info.Size() > 2*1024*1024 {
		t.Errorf("autofix should have truncated daemon log to ~1 MB, size now %d", info.Size())
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
