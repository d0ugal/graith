package daemon

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/testutil"
)

// mkOrphanDir creates a directory and back-dates its mtime past gcOrphanMinAge
// so it is old enough for GC to consider it an orphan.
func mkOrphanDir(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}

	old := time.Now().Add(-gcOrphanMinAge - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// worktreeDir returns the on-disk path GC scans for a session worktree.
func worktreeDir(dataDir, repoName, repoPath, id string) string {
	return filepath.Join(dataDir, "worktrees", repoName, repoHash(repoPath), id)
}

// initGitRepo turns dir into a git repo so git.IsInsideGitRepo reports true.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	testutil.IsolateGit(t)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "graith@localhost"},
		{"config", "user.name", "graith"},
	} {
		cmd := testutil.GitCommand(args...)
		cmd.Dir = dir

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestFindOrphansDetectsWorktreeAndScratch(t *testing.T) {
	sm := newTestSessionManager(t)
	dataDir := sm.paths.DataDir

	orphanWT := worktreeDir(dataDir, "croft", "/Code/croft", "deadbeef")
	mkOrphanDir(t, orphanWT)

	orphanScratch := filepath.Join(dataDir, "scratch", "cafebabe")
	mkOrphanDir(t, orphanScratch)

	orphans := sm.FindOrphans(time.Now())
	if len(orphans) != 2 {
		t.Fatalf("found %d orphans, want 2: %+v", len(orphans), orphans)
	}

	byType := map[string]GCOrphan{}
	for _, o := range orphans {
		byType[o.Type] = o
	}

	if byType[GCOrphanWorktree].Path != orphanWT {
		t.Errorf("worktree orphan path = %q, want %q", byType[GCOrphanWorktree].Path, orphanWT)
	}

	if byType[GCOrphanScratch].ID != "cafebabe" {
		t.Errorf("scratch orphan ID = %q, want cafebabe", byType[GCOrphanScratch].ID)
	}
}

func TestFindOrphansIgnoresKnownSessions(t *testing.T) {
	sm := newTestSessionManager(t)
	dataDir := sm.paths.DataDir

	// A directory whose leaf name matches a live session must never be an orphan.
	liveWT := worktreeDir(dataDir, "croft", "/Code/croft", "braw1234")
	mkOrphanDir(t, liveWT)

	liveScratch := filepath.Join(dataDir, "scratch", "braw5678")
	mkOrphanDir(t, liveScratch)

	sm.state.Sessions["braw1234"] = &SessionState{ID: "braw1234", Name: "braw"}
	sm.state.Sessions["braw5678"] = &SessionState{ID: "braw5678", Name: "bonnie"}

	if orphans := sm.FindOrphans(time.Now()); len(orphans) != 0 {
		t.Fatalf("found %d orphans, want 0: %+v", len(orphans), orphans)
	}
}

func TestFindOrphansRespectsMinAge(t *testing.T) {
	sm := newTestSessionManager(t)
	dataDir := sm.paths.DataDir

	// A freshly-created dir (recent mtime) belongs to a possibly in-flight
	// create and must not be collected.
	youngWT := worktreeDir(dataDir, "croft", "/Code/croft", "young123")
	if err := os.MkdirAll(youngWT, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if orphans := sm.FindOrphans(time.Now()); len(orphans) != 0 {
		t.Fatalf("found %d orphans, want 0 (too young): %+v", len(orphans), orphans)
	}
}

// TestFindOrphansHonoursConfiguredMinAge verifies the orphan age floor is read
// from [gc] orphan_min_age, not baked in: a directory older than the default 5m
// but younger than a widened floor is spared, while shrinking the floor makes a
// recent directory eligible.
func TestFindOrphansHonoursConfiguredMinAge(t *testing.T) {
	sm := newTestSessionManager(t)
	dataDir := sm.paths.DataDir

	// A dir aged ~10 minutes: an orphan under the 5m default, but not under a
	// widened 1h floor.
	wt := worktreeDir(dataDir, "croft", "/Code/croft", "aged10m")
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tenMinAgo := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(wt, tenMinAgo, tenMinAgo); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Default 5m floor: 10-minute-old dir is an orphan.
	if orphans := sm.FindOrphans(time.Now()); len(orphans) != 1 {
		t.Fatalf("default floor: found %d orphans, want 1", len(orphans))
	}

	// Widen the floor past its age: now spared.
	sm.cfg.GC.OrphanMinAge = "1h"
	if orphans := sm.FindOrphans(time.Now()); len(orphans) != 0 {
		t.Fatalf("widened floor: found %d orphans, want 0: %+v", len(orphans), orphans)
	}

	// Opt out of the floor entirely: even a freshly-created dir is eligible.
	fresh := worktreeDir(dataDir, "croft", "/Code/croft", "brandnew")
	if err := os.MkdirAll(fresh, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	sm.cfg.GC.OrphanMinAge = "0"

	if orphans := sm.FindOrphans(time.Now()); len(orphans) != 2 {
		t.Fatalf("zero floor: found %d orphans, want 2 (both dirs): %+v", len(orphans), orphans)
	}
}

func TestRunGCDryRunRemovesNothing(t *testing.T) {
	sm := newTestSessionManager(t)
	dataDir := sm.paths.DataDir

	orphanWT := worktreeDir(dataDir, "croft", "/Code/croft", "deadbeef")
	mkOrphanDir(t, orphanWT)

	orphans := sm.RunGC(false, time.Now())
	if len(orphans) != 1 {
		t.Fatalf("found %d orphans, want 1", len(orphans))
	}

	if orphans[0].Removed {
		t.Error("dry run reported Removed=true")
	}

	if _, err := os.Stat(orphanWT); err != nil {
		t.Errorf("dry run deleted the orphan dir: %v", err)
	}
}

func TestRunGCForceRemovesOrphans(t *testing.T) {
	sm := newTestSessionManager(t)
	dataDir := sm.paths.DataDir

	orphanWT := worktreeDir(dataDir, "croft", "/Code/croft", "deadbeef")
	mkOrphanDir(t, orphanWT)

	orphanScratch := filepath.Join(dataDir, "scratch", "cafebabe")
	mkOrphanDir(t, orphanScratch)

	orphans := sm.RunGC(true, time.Now())
	if len(orphans) != 2 {
		t.Fatalf("found %d orphans, want 2", len(orphans))
	}

	for _, o := range orphans {
		if !o.Removed {
			t.Errorf("orphan %s not removed: %+v", o.Path, o)
		}

		if _, err := os.Stat(o.Path); !os.IsNotExist(err) {
			t.Errorf("orphan %s still on disk (err=%v)", o.Path, err)
		}
	}
}

// TestRunGCSkipsUndeterminableWorktree is the regression test for fail-open
// teardown: a git worktree whose dirty state can't be read must be preserved,
// not force-removed.
func TestRunGCSkipsUndeterminableWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	sm := newTestSessionManager(t)
	dataDir := sm.paths.DataDir

	orphanWT := worktreeDir(dataDir, "croft", "/Code/croft", "haar0001")
	initGitRepo(t, orphanWT)

	old := time.Now().Add(-gcOrphanMinAge - time.Minute)
	if err := os.Chtimes(orphanWT, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Simulate `git status` failing for this worktree (corrupt index, broken
	// gitlink, transient error) — the dirty state is undeterminable.
	orig := gcHasUncommittedChanges
	gcHasUncommittedChanges = func(string) (bool, error) {
		return false, errors.New("simulated git failure")
	}

	t.Cleanup(func() { gcHasUncommittedChanges = orig })

	orphans := sm.RunGC(true, time.Now())
	if len(orphans) != 1 {
		t.Fatalf("found %d orphans, want 1", len(orphans))
	}

	if orphans[0].Removed {
		t.Error("undeterminable worktree was removed; it should be skipped")
	}

	if !orphans[0].Skipped {
		t.Error("undeterminable worktree not marked Skipped")
	}

	if _, err := os.Stat(orphanWT); err != nil {
		t.Errorf("undeterminable worktree deleted despite skip: %v", err)
	}
}

// TestRunGCSkipsBrokenGitWorktree covers the fail-open trap in repository
// DETECTION (not just the dirty check): a worktree with a .git marker whose
// rev-parse probe fails looks like a plain directory to git.IsInsideGitRepo,
// but must still be preserved — its WIP can't be ruled out.
func TestRunGCSkipsBrokenGitWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	sm := newTestSessionManager(t)
	dataDir := sm.paths.DataDir

	orphanWT := worktreeDir(dataDir, "croft", "/Code/croft", "brokengit")
	if err := os.MkdirAll(orphanWT, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// A .git gitlink pointing nowhere: `git rev-parse` fails (so IsInsideGitRepo
	// returns false), but the marker proves this is a damaged worktree.
	if err := os.WriteFile(filepath.Join(orphanWT, ".git"), []byte("gitdir: /nonexistent/scunner\n"), 0o600); err != nil {
		t.Fatalf("write broken gitlink: %v", err)
	}

	old := time.Now().Add(-gcOrphanMinAge - time.Minute)
	if err := os.Chtimes(orphanWT, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	orphans := sm.RunGC(true, time.Now())
	if len(orphans) != 1 {
		t.Fatalf("found %d orphans, want 1", len(orphans))
	}

	o := orphans[0]
	if !o.dirtyUndetermined {
		t.Error("broken git worktree not flagged dirtyUndetermined")
	}

	if o.Removed || !o.Skipped {
		t.Errorf("broken git worktree should be skipped, not removed: %+v", o)
	}

	if _, err := os.Stat(orphanWT); err != nil {
		t.Errorf("broken git worktree deleted despite skip: %v", err)
	}
}

func TestRunGCSkipsDirtyWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	sm := newTestSessionManager(t)
	dataDir := sm.paths.DataDir

	orphanWT := worktreeDir(dataDir, "croft", "/Code/croft", "dreich01")
	initGitRepo(t, orphanWT)

	// An uncommitted file makes the worktree read as dirty.
	if err := os.WriteFile(filepath.Join(orphanWT, "scunner.txt"), []byte("wip"), 0o600); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	old := time.Now().Add(-gcOrphanMinAge - time.Minute)
	if err := os.Chtimes(orphanWT, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	orphans := sm.RunGC(true, time.Now())
	if len(orphans) != 1 {
		t.Fatalf("found %d orphans, want 1", len(orphans))
	}

	o := orphans[0]
	if !o.HasDirtyFiles {
		t.Error("expected HasDirtyFiles=true for uncommitted worktree")
	}

	if o.Removed {
		t.Error("dirty worktree was removed; it should be skipped")
	}

	if !o.Skipped {
		t.Error("dirty worktree not marked Skipped")
	}

	if _, err := os.Stat(orphanWT); err != nil {
		t.Errorf("dirty worktree deleted despite skip: %v", err)
	}
}
