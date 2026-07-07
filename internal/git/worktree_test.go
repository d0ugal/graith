package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeWorktree adds a worktree+branch to repo and returns the worktree path.
func makeWorktree(t *testing.T, repo, branch string) string {
	t.Helper()

	wt := filepath.Join(t.TempDir(), "bothy")
	if err := CreateBranch(repo, branch, "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	if err := CreateWorktree(repo, wt, branch); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	return wt
}

// worktreeCount returns the number of registered worktrees (including the main
// repo) so tests can assert stale registrations were pruned.
func worktreeCount(t *testing.T, repo string) int {
	t.Helper()

	out, err := RunOutput(repo, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}

	n := 0
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			n++
		}
	}

	return n
}

// A session whose worktree git link is broken (directory present, but no longer
// a valid working tree) must still tear down cleanly instead of wedging on the
// exit-128 "is not a working tree" error (#741).
func TestTeardownSessionBrokenGitLink(t *testing.T) {
	repo := setupTestRepo(t)
	wt := makeWorktree(t, repo, "thrawn")

	// Break the worktree's link to the repo by removing its .git file. `git
	// worktree remove --force` now fails with "is not a working tree".
	if err := os.Remove(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("remove .git: %v", err)
	}

	// Sanity: the direct remove should indeed fail now.
	if err := RemoveWorktree(repo, wt); err == nil {
		t.Fatal("expected RemoveWorktree to fail on broken worktree")
	}

	if err := TeardownSession(repo, wt, "thrawn"); err != nil {
		t.Fatalf("TeardownSession with broken git link: %v", err)
	}

	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("broken worktree dir still present: %v", err)
	}

	if RefExists(repo, "thrawn") {
		t.Error("branch thrawn still exists")
	}

	if n := worktreeCount(t, repo); n != 1 {
		t.Errorf("stale registration not pruned: worktree count = %d, want 1", n)
	}
}

// TeardownSession must prune the stale worktree registration left behind when
// the directory is removed out from under graith (#741).
func TestTeardownSessionMissingDirPrunes(t *testing.T) {
	repo := setupTestRepo(t)
	wt := makeWorktree(t, repo, "dreich")

	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	if err := TeardownSession(repo, wt, "dreich"); err != nil {
		t.Fatalf("TeardownSession with missing dir: %v", err)
	}

	if RefExists(repo, "dreich") {
		t.Error("branch dreich still exists")
	}

	if n := worktreeCount(t, repo); n != 1 {
		t.Errorf("stale registration not pruned: worktree count = %d, want 1", n)
	}
}

// PruneWorktrees must clear a registration whose directory has been removed.
func TestPruneWorktreesFallback(t *testing.T) {
	repo := setupTestRepo(t)
	wt := makeWorktree(t, repo, "bide")

	if n := worktreeCount(t, repo); n != 2 {
		t.Fatalf("setup worktree count = %d, want 2", n)
	}

	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	if err := PruneWorktrees(repo); err != nil {
		t.Fatalf("PruneWorktrees: %v", err)
	}

	if n := worktreeCount(t, repo); n != 1 {
		t.Errorf("worktree count after prune = %d, want 1", n)
	}
}
