package git

import (
	"context"
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

func commitFile(t *testing.T, repo, path, content, message string) string {
	t.Helper()

	writeFile(t, filepath.Join(repo, path), content)

	if _, err := RunOutput(repo, "add", path); err != nil {
		t.Fatalf("git add %s: %v", path, err)
	}

	if _, err := RunOutput(repo, "commit", "-m", message); err != nil {
		t.Fatalf("git commit %q: %v", message, err)
	}

	rev, err := RunOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	return rev
}

func TestSetupReadOnlySessionCreatesDetachedWorktreeAtBranchRevision(t *testing.T) {
	repo := setupTestRepo(t)
	if _, err := RunOutput(repo, "checkout", "-b", "canny"); err != nil {
		t.Fatalf("checkout canny: %v", err)
	}

	wantRev := commitFile(t, repo, "canny.txt", "canny\n", "canny branch")
	if _, err := RunOutput(repo, "checkout", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	wt := filepath.Join(t.TempDir(), "reader")

	gotRev, err := SetupReadOnlySession(context.Background(), repo, wt, "canny", false)
	if err != nil {
		t.Fatalf("SetupReadOnlySession: %v", err)
	}

	if gotRev != wantRev {
		t.Fatalf("revision = %q, want %q", gotRev, wantRev)
	}

	head, err := RunOutput(wt, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("worktree HEAD: %v", err)
	}

	if head != wantRev {
		t.Errorf("worktree HEAD = %q, want %q", head, wantRev)
	}

	branch, err := RunOutput(wt, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("worktree branch: %v", err)
	}

	if branch != "HEAD" {
		t.Errorf("worktree branch = %q, want detached HEAD", branch)
	}
}

func TestRefreshReadOnlySessionFetchesAndMovesDetachedWorktree(t *testing.T) {
	repo := setupTestRepo(t)

	origin := addBareOrigin(t, repo)
	if _, err := RunOutput(repo, "checkout", "-b", "canny"); err != nil {
		t.Fatalf("checkout canny: %v", err)
	}

	initialRev := commitFile(t, repo, "canny.txt", "auld\n", "auld canny")
	if _, err := RunOutput(repo, "push", "origin", "canny"); err != nil {
		t.Fatalf("push canny: %v", err)
	}

	if _, err := RunOutput(repo, "checkout", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	wt := filepath.Join(t.TempDir(), "reader")

	gotRev, err := SetupReadOnlySession(context.Background(), repo, wt, "canny", false)
	if err != nil {
		t.Fatalf("SetupReadOnlySession: %v", err)
	}

	if gotRev != initialRev {
		t.Fatalf("initial revision = %q, want %q", gotRev, initialRev)
	}

	other := filepath.Join(t.TempDir(), "other")
	if _, err := RunOutput("", "clone", origin, other); err != nil {
		t.Fatalf("clone origin: %v", err)
	}

	if _, err := RunOutput(other, "checkout", "canny"); err != nil {
		t.Fatalf("checkout other canny: %v", err)
	}

	wantRev := commitFile(t, other, "canny.txt", "braw\n", "braw canny")
	if _, err := RunOutput(other, "push", "origin", "canny"); err != nil {
		t.Fatalf("push moved canny: %v", err)
	}

	refreshedRev, err := RefreshReadOnlySession(context.Background(), repo, wt, "canny", true)
	if err != nil {
		t.Fatalf("RefreshReadOnlySession: %v", err)
	}

	if refreshedRev != wantRev {
		t.Fatalf("refreshed revision = %q, want %q", refreshedRev, wantRev)
	}

	head, err := RunOutput(wt, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("worktree HEAD: %v", err)
	}

	if head != wantRev {
		t.Errorf("worktree HEAD = %q, want %q", head, wantRev)
	}
}

func TestRefreshReadOnlySessionRecreatesMissingRegisteredWorktree(t *testing.T) {
	repo := setupTestRepo(t)
	if _, err := RunOutput(repo, "checkout", "-b", "canny"); err != nil {
		t.Fatalf("checkout canny: %v", err)
	}

	wantRev := commitFile(t, repo, "canny.txt", "braw\n", "braw canny")
	if _, err := RunOutput(repo, "checkout", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	wt := filepath.Join(t.TempDir(), "reader")

	if _, err := SetupReadOnlySession(context.Background(), repo, wt, "canny", false); err != nil {
		t.Fatalf("SetupReadOnlySession: %v", err)
	}

	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}

	refreshedRev, err := RefreshReadOnlySession(context.Background(), repo, wt, "canny", false)
	if err != nil {
		t.Fatalf("RefreshReadOnlySession: %v", err)
	}

	if refreshedRev != wantRev {
		t.Fatalf("refreshed revision = %q, want %q", refreshedRev, wantRev)
	}

	head, err := RunOutput(wt, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("recreated worktree HEAD: %v", err)
	}

	if head != wantRev {
		t.Errorf("recreated worktree HEAD = %q, want %q", head, wantRev)
	}
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
	// worktree remove --force` now fails ("cannot remove working tree"), but
	// git still lists the worktree as a prunable registration.
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

// A directory at worktreePath that graith does not own (not a registered
// worktree of the repo) must never be removed by teardown, even though `git
// worktree remove` reports "is not a working tree" for it. Instead the error is
// surfaced so the session is kept for retry (#741 safety: don't delete unowned
// paths).
func TestTeardownSessionUnregisteredPathNotRemoved(t *testing.T) {
	repo := setupTestRepo(t)

	// A plain directory that was never a worktree of repo.
	stray := filepath.Join(t.TempDir(), "clachan")
	if err := os.MkdirAll(stray, 0o700); err != nil {
		t.Fatalf("mkdir stray: %v", err)
	}

	if IsRegisteredWorktree(repo, stray) {
		t.Fatal("stray dir should not be a registered worktree")
	}

	err := TeardownSession(repo, stray, "")
	if err == nil {
		t.Fatal("expected error tearing down an unregistered path")
	}

	if _, statErr := os.Stat(stray); statErr != nil {
		t.Errorf("unregistered dir must not be removed: %v", statErr)
	}
}

// When the source repo is unreachable, teardown of an existing worktree
// directory must surface an error (keep-for-retry) and must not remove the
// directory, since it can't confirm ownership.
func TestTeardownSessionRepoUnreachable(t *testing.T) {
	notARepo := t.TempDir()
	wt := filepath.Join(t.TempDir(), "bothy")

	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatalf("mkdir wt: %v", err)
	}

	err := TeardownSession(notARepo, wt, "")
	if err == nil {
		t.Fatal("expected error when repo is not a git repo")
	}

	if _, statErr := os.Stat(wt); statErr != nil {
		t.Errorf("worktree dir must not be removed when repo is unreachable: %v", statErr)
	}
}

func TestIsRegisteredWorktree(t *testing.T) {
	repo := setupTestRepo(t)
	wt := makeWorktree(t, repo, "braw")

	if !IsRegisteredWorktree(repo, wt) {
		t.Error("valid worktree should be registered")
	}

	// Broken .git link — still registered (prunable) until removed.
	if err := os.Remove(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("remove .git: %v", err)
	}

	if !IsRegisteredWorktree(repo, wt) {
		t.Error("broken-link worktree should still be registered")
	}

	// An unrelated directory is not registered.
	stray := filepath.Join(t.TempDir(), "haar")
	if err := os.MkdirAll(stray, 0o700); err != nil {
		t.Fatalf("mkdir stray: %v", err)
	}

	if IsRegisteredWorktree(repo, stray) {
		t.Error("unrelated dir should not be registered")
	}

	// An unreachable repo yields no registrations.
	if IsRegisteredWorktree(t.TempDir(), wt) {
		t.Error("non-repo path should report no registered worktrees")
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
