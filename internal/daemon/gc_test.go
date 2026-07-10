package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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

func TestRunGCSkipsDirtyWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	sm := newTestSessionManager(t)
	dataDir := sm.paths.DataDir

	orphanWT := worktreeDir(dataDir, "croft", "/Code/croft", "dreich01")
	if err := os.MkdirAll(orphanWT, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Make it a git repo with an uncommitted file so it reads as dirty.
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "graith@localhost"},
		{"config", "user.name", "graith"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = orphanWT

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

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
