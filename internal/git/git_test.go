package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir

		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=braw", "GIT_AUTHOR_EMAIL=braw@croft.local",
			"GIT_COMMITTER_NAME=braw", "GIT_COMMITTER_EMAIL=braw@croft.local",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	writeFile(t, filepath.Join(dir, "README.md"), "braw")
	run("add", ".")
	run("commit", "-m", "auld")

	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRunOutput(t *testing.T) {
	dir := setupTestRepo(t)

	out, err := RunOutput(dir, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Fatal(err)
	}

	if out != "true" {
		t.Errorf("output = %q, want true", out)
	}
}

func TestRunCheck(t *testing.T) {
	dir := setupTestRepo(t)
	if !RunCheck(dir, "rev-parse", "--is-inside-work-tree") {
		t.Error("expected true for valid repo")
	}

	if RunCheck("/nonexistent", "status") {
		t.Error("expected false for nonexistent dir")
	}
}

func TestRefExists(t *testing.T) {
	dir := setupTestRepo(t)
	if !RefExists(dir, "main") {
		t.Error("main branch should exist")
	}

	if RefExists(dir, "glen-thrawn-nonexistent") {
		t.Error("nonexistent branch should not exist")
	}
}

func TestHasUncommittedChanges(t *testing.T) {
	dir := setupTestRepo(t)

	dirty, err := HasUncommittedChanges(dir)
	if err != nil {
		t.Fatal(err)
	}

	if dirty {
		t.Error("clean repo should not be dirty")
	}

	writeFile(t, filepath.Join(dir, "neep.txt"), "bonnie")

	dirty, err = HasUncommittedChanges(dir)
	if err != nil {
		t.Fatal(err)
	}

	if !dirty {
		t.Error("repo with new file should be dirty")
	}
}

func TestIsInsideGitRepo(t *testing.T) {
	dir := setupTestRepo(t)
	if !IsInsideGitRepo(dir) {
		t.Error("should detect git repo")
	}

	if IsInsideGitRepo(t.TempDir()) {
		t.Error("should not detect non-repo as git repo")
	}
}

func TestDirtyFiles(t *testing.T) {
	dir := setupTestRepo(t)

	files, err := DirtyFiles(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 0 {
		t.Errorf("clean repo: got %d dirty files, want 0", len(files))
	}

	writeFile(t, filepath.Join(dir, "neep.txt"), "neep")
	writeFile(t, filepath.Join(dir, "README.md"), "bonnie")

	files, err = DirtyFiles(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 2 {
		t.Errorf("got %d dirty files, want 2: %v", len(files), files)
	}
}

func TestHasRemote(t *testing.T) {
	dir := setupTestRepo(t)

	if HasRemote(dir, "origin") {
		t.Error("fresh repo should not have origin remote")
	}

	bare := t.TempDir()
	if _, err := RunOutput(bare, "clone", "--bare", dir, bare+"/repo.git"); err != nil {
		t.Fatal(err)
	}

	if _, err := RunOutput(dir, "remote", "add", "origin", bare+"/repo.git"); err != nil {
		t.Fatal(err)
	}

	if !HasRemote(dir, "origin") {
		t.Error("repo with origin should report HasRemote=true")
	}

	if HasRemote(dir, "upstream") {
		t.Error("repo without upstream should report HasRemote=false")
	}
}

func TestDirtyFilesInvalidDir(t *testing.T) {
	_, err := DirtyFiles("/nonexistent-dir-abc123")
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

func TestUnpushedCommitSummaries(t *testing.T) {
	dir := setupTestRepo(t)

	commits, err := UnpushedCommitSummaries(dir, "main")
	if err != nil {
		t.Fatal(err)
	}

	if len(commits) != 0 {
		t.Errorf("no extra commits: got %d, want 0", len(commits))
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir

		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=braw", "GIT_AUTHOR_EMAIL=braw@croft.local",
			"GIT_COMMITTER_NAME=braw", "GIT_COMMITTER_EMAIL=braw@croft.local",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("checkout", "-b", "glen-feature")
	writeFile(t, filepath.Join(dir, "neep-a.txt"), "neep")
	run("add", ".")
	run("commit", "-m", "braw glen commit")
	writeFile(t, filepath.Join(dir, "neep-b.txt"), "neep")
	run("add", ".")
	run("commit", "-m", "bonnie glen commit")

	commits, err = UnpushedCommitSummaries(dir, "main")
	if err != nil {
		t.Fatal(err)
	}

	if len(commits) != 2 {
		t.Errorf("got %d unpushed commits, want 2: %v", len(commits), commits)
	}
}

func TestUnpushedCommitCount(t *testing.T) {
	dir := setupTestRepo(t)

	run := func(wd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = wd

		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=braw", "GIT_AUTHOR_EMAIL=braw@croft.local",
			"GIT_COMMITTER_NAME=braw", "GIT_COMMITTER_EMAIL=braw@croft.local",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Create a bare "origin" and push main so origin/main exists.
	origin := t.TempDir()
	run(origin, "init", "--bare", "-b", "main")
	run(dir, "remote", "add", "origin", origin)
	run(dir, "push", "origin", "main")
	run(dir, "fetch", "origin")

	n, err := UnpushedCommitCount(dir, "main")
	if err != nil {
		t.Fatalf("UnpushedCommitCount: %v", err)
	}

	if n != 0 {
		t.Errorf("fresh push: got %d unpushed commits, want 0", n)
	}

	// Add two local commits that have not been pushed.
	writeFile(t, filepath.Join(dir, "neep-a.txt"), "neep")
	run(dir, "add", ".")
	run(dir, "commit", "-m", "braw local commit")
	writeFile(t, filepath.Join(dir, "neep-b.txt"), "neep")
	run(dir, "add", ".")
	run(dir, "commit", "-m", "bonnie local commit")

	n, err = UnpushedCommitCount(dir, "main")
	if err != nil {
		t.Fatalf("UnpushedCommitCount: %v", err)
	}

	if n != 2 {
		t.Errorf("got %d unpushed commits, want 2", n)
	}
}

func TestUnpushedCommitSummariesNoRemote(t *testing.T) {
	dir := setupTestRepo(t)

	_, err := UnpushedCommitSummaries(dir, "glen-thrawn-nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent base branch")
	}
}

// TestFetchRemoteUpdatesTrackingRef verifies FetchRemote advances the local
// origin/main ref after the remote moves on, which is what keeps the
// diverged-from-base fallback count fresh (#197).
func TestFetchRemoteUpdatesTrackingRef(t *testing.T) {
	dir := setupTestRepo(t)

	run := func(wd string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = wd

		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=braw", "GIT_AUTHOR_EMAIL=braw@croft.local",
			"GIT_COMMITTER_NAME=braw", "GIT_COMMITTER_EMAIL=braw@croft.local",
		)

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	origin := t.TempDir()
	run(origin, "init", "--bare", "-b", "main")
	run(dir, "remote", "add", "origin", origin)
	run(dir, "push", "origin", "main")
	run(dir, "fetch", "origin")

	// A second clone pushes a new commit to origin/main behind our back.
	other := t.TempDir()
	run(other, "clone", origin, ".")
	writeFile(t, filepath.Join(other, "neep.txt"), "neep")
	run(other, "add", ".")
	run(other, "commit", "-m", "bonnie remote commit")
	run(other, "push", "origin", "main")

	remoteTip := run(other, "rev-parse", "HEAD")

	// Before fetch our origin/main is stale.
	if before := run(dir, "rev-parse", "origin/main"); before == remoteTip {
		t.Fatal("origin/main should be stale before fetch")
	}

	if err := FetchRemote(context.Background(), dir); err != nil {
		t.Fatalf("FetchRemote: %v", err)
	}

	if after := run(dir, "rev-parse", "origin/main"); after != remoteTip {
		t.Errorf("origin/main not updated: got %s, want %s", after, remoteTip)
	}
}

func TestFetchRemoteNoRemote(t *testing.T) {
	dir := setupTestRepo(t)

	if err := FetchRemote(context.Background(), dir); err == nil {
		t.Error("expected error fetching with no origin remote")
	}
}

func TestParseGitHubUsernameSSH(t *testing.T) {
	u, ok := ParseGitHubUsername("git@github.com:d0ugal/graith.git")
	if !ok || u != "d0ugal" {
		t.Errorf("got %q, %v", u, ok)
	}
}

func TestParseGitHubUsernameHTTPS(t *testing.T) {
	u, ok := ParseGitHubUsername("https://github.com/d0ugal/graith.git")
	if !ok || u != "d0ugal" {
		t.Errorf("got %q, %v", u, ok)
	}
}

// resolveT resolves symlinks for path comparison (macOS /var → /private/var),
// falling back to a lexical clean when the path can't be resolved.
func resolveT(t *testing.T, p string) string {
	t.Helper()

	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}

	return filepath.Clean(p)
}

// runGit runs a git command in dir with a deterministic author/committer,
// returning any error (with combined output on failure).
func runGit(t *testing.T, dir string, args ...string) error {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=braw", "GIT_AUTHOR_EMAIL=braw@croft.local",
		"GIT_COMMITTER_NAME=braw", "GIT_COMMITTER_EMAIL=braw@croft.local",
	)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("git %v: %s", args, out)
		return err
	}

	return nil
}

// addBareOrigin creates a bare clone of repo and wires it up as "origin".
func addBareOrigin(t *testing.T, repo string) string {
	t.Helper()

	bare := t.TempDir()
	if _, err := RunOutput(bare, "clone", "--bare", repo, bare+"/repo.git"); err != nil {
		t.Fatalf("clone --bare: %v", err)
	}

	if _, err := RunOutput(repo, "remote", "add", "origin", bare+"/repo.git"); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	return bare + "/repo.git"
}

func TestRunContextCov(t *testing.T) {
	dir := setupTestRepo(t)

	stdout, _, err := RunContext(context.Background(), dir, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Fatalf("RunContext: %v", err)
	}

	if stdout != "true" {
		t.Errorf("stdout = %q, want true", stdout)
	}
}

func TestRunOutputContextCov(t *testing.T) {
	dir := setupTestRepo(t)

	out, err := RunOutputContext(context.Background(), dir, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Fatalf("RunOutputContext: %v", err)
	}

	if out != "true" {
		t.Errorf("out = %q, want true", out)
	}

	// Error path: a failing git subcommand is wrapped with stderr.
	if _, err := RunOutputContext(context.Background(), dir, "rev-parse", "--verify", "no-such-ref-thrawn"); err == nil {
		t.Error("expected error for missing ref")
	}
}

func TestFetchOriginCov(t *testing.T) {
	dir := setupTestRepo(t)
	addBareOrigin(t, dir)

	if err := FetchOrigin(dir); err != nil {
		t.Errorf("FetchOrigin: %v", err)
	}
}

func TestFetchOriginContextCov(t *testing.T) {
	dir := setupTestRepo(t)
	addBareOrigin(t, dir)

	if err := FetchOriginContext(context.Background(), dir); err != nil {
		t.Errorf("FetchOriginContext: %v", err)
	}

	// Error path: no origin remote configured.
	fresh := setupTestRepo(t)
	if err := FetchOriginContext(context.Background(), fresh); err == nil {
		t.Error("expected error fetching with no origin")
	}
}

func TestWorktreeGitDirsCov(t *testing.T) {
	repo := setupTestRepo(t)
	wt := makeWorktree(t, repo, "bothy")

	gitDir, commonDir, err := WorktreeGitDirs(wt)
	if err != nil {
		t.Fatalf("WorktreeGitDirs: %v", err)
	}

	// For a linked worktree the per-worktree git dir must differ from the
	// shared common dir, and the common dir must be the source repo's .git.
	if gitDir == commonDir {
		t.Errorf("gitDir and commonDir should differ for a linked worktree: both %q", gitDir)
	}

	wantCommon := resolveT(t, filepath.Join(repo, ".git"))
	if resolveT(t, commonDir) != wantCommon {
		t.Errorf("commonDir = %q, want the repo .git %q", commonDir, wantCommon)
	}

	// The per-worktree git dir lives under the common dir's worktrees/ area.
	if !strings.HasPrefix(resolveT(t, gitDir), resolveT(t, filepath.Join(repo, ".git", "worktrees"))) {
		t.Errorf("gitDir = %q, want it under %q", gitDir, filepath.Join(repo, ".git", "worktrees"))
	}

	// Error path: not a git repo.
	if _, _, err := WorktreeGitDirs(t.TempDir()); err == nil {
		t.Error("expected error for non-repo dir")
	}
}

func TestRepoRootFromWorktreeCov(t *testing.T) {
	repo := setupTestRepo(t)
	wt := makeWorktree(t, repo, "glen")

	root, err := RepoRootFromWorktree(wt)
	if err != nil {
		t.Fatalf("RepoRootFromWorktree: %v", err)
	}

	wantResolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}

	gotResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	if gotResolved != wantResolved {
		t.Errorf("RepoRootFromWorktree = %q, want %q", gotResolved, wantResolved)
	}

	// Error path: not a git repo.
	if _, err := RepoRootFromWorktree(t.TempDir()); err == nil {
		t.Error("expected error for non-repo dir")
	}
}

func TestDiscoverDefaultBranchOrHEADFallbackCov(t *testing.T) {
	// A repo with neither main/master nor an origin: DiscoverDefaultBranch
	// fails, so the current branch name is returned instead.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()

		if err := runGit(t, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	run("init", "-b", "canny-wynd")
	writeFile(t, filepath.Join(dir, "README.md"), "braw")
	run("add", ".")
	run("commit", "-m", "auld")

	branch, err := DiscoverDefaultBranchOrHEAD(dir)
	if err != nil {
		t.Fatalf("DiscoverDefaultBranchOrHEAD: %v", err)
	}

	if branch != "canny-wynd" {
		t.Errorf("branch = %q, want canny-wynd", branch)
	}
}

func TestDiscoverDefaultBranchOrHEADPrefersDefaultCov(t *testing.T) {
	dir := setupTestRepo(t) // has a main branch

	branch, err := DiscoverDefaultBranchOrHEAD(dir)
	if err != nil {
		t.Fatalf("DiscoverDefaultBranchOrHEAD: %v", err)
	}

	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
}

func TestDiscoverDefaultBranchOrHEADDetachedCov(t *testing.T) {
	// Detached HEAD with no main/master: both discovery paths fail.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()

		if err := runGit(t, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	run("init", "-b", "haar")
	writeFile(t, filepath.Join(dir, "README.md"), "braw")
	run("add", ".")
	run("commit", "-m", "auld")

	sha, err := RunOutput(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	run("checkout", sha) // detach

	if _, err := DiscoverDefaultBranchOrHEAD(dir); err == nil {
		t.Error("expected error on detached HEAD with no default branch")
	}
}

func TestHasUncommittedChangesErrorCov(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone-glen")
	if _, err := HasUncommittedChanges(gone); err == nil {
		t.Error("expected error for nonexistent dir")
	}
}

func TestUnpushedCommitCountErrorCov(t *testing.T) {
	dir := setupTestRepo(t)

	// No tracking ref and a base branch that doesn't exist locally either, so
	// rev-list <base>..HEAD fails.
	if _, err := UnpushedCommitCount(dir, "glen-thrawn-nonexistent"); err == nil {
		t.Error("expected error counting against a missing base ref")
	}
}

// TestUnpushedCommitCountAfterMerge is the regression test for issue #197: a
// branch's commits are pushed, then the PR is merged into main (advancing
// origin/main). The unpushed count must read 0 because we compare against the
// branch's own tracking ref, not against origin/main.
func TestUnpushedCommitCountAfterMerge(t *testing.T) {
	dir := setupTestRepo(t)

	run := func(wd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = wd

		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=braw", "GIT_AUTHOR_EMAIL=braw@croft.local",
			"GIT_COMMITTER_NAME=braw", "GIT_COMMITTER_EMAIL=braw@croft.local",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	origin := t.TempDir()
	run(origin, "init", "--bare", "-b", "main")
	run(dir, "remote", "add", "origin", origin)
	run(dir, "push", "origin", "main")

	// Create a feature branch with two commits and push it.
	run(dir, "checkout", "-b", "glen-feature")
	writeFile(t, filepath.Join(dir, "neep-a.txt"), "neep")
	run(dir, "add", ".")
	run(dir, "commit", "-m", "braw feature commit")
	writeFile(t, filepath.Join(dir, "neep-b.txt"), "neep")
	run(dir, "add", ".")
	run(dir, "commit", "-m", "bonnie feature commit")
	run(dir, "push", "-u", "origin", "glen-feature")

	// Everything is pushed to the branch's tracking ref: 0 unpushed.
	n, err := UnpushedCommitCount(dir, "main")
	if err != nil {
		t.Fatalf("UnpushedCommitCount: %v", err)
	}

	if n != 0 {
		t.Errorf("after push: got %d unpushed, want 0", n)
	}

	// Simulate the PR being merged: advance origin/main to the feature tip via
	// a clone that pushes into the bare origin. The stale local origin/main
	// ref would previously make the count read 2 "ahead"; we count against the
	// tracking ref, so it stays 0.
	run(dir, "push", "origin", "glen-feature:main")

	n, err = UnpushedCommitCount(dir, "main")
	if err != nil {
		t.Fatalf("UnpushedCommitCount after merge: %v", err)
	}

	if n != 0 {
		t.Errorf("after merge: got %d unpushed, want 0 (no false ahead-of-main)", n)
	}
}

// TestUnpushedCommitCountNeverPushed covers the fallback path: a branch that
// has never been pushed has no tracking ref, so the count reflects commits
// ahead of the base branch.
func TestUnpushedCommitCountNeverPushed(t *testing.T) {
	dir := setupTestRepo(t)

	run := func(wd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = wd

		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=braw", "GIT_AUTHOR_EMAIL=braw@croft.local",
			"GIT_COMMITTER_NAME=braw", "GIT_COMMITTER_EMAIL=braw@croft.local",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	origin := t.TempDir()
	run(origin, "init", "--bare", "-b", "main")
	run(dir, "remote", "add", "origin", origin)
	run(dir, "push", "origin", "main")
	run(dir, "fetch", "origin")

	// Feature branch with one commit, never pushed (no origin/glen-feature).
	run(dir, "checkout", "-b", "glen-feature")
	writeFile(t, filepath.Join(dir, "neep-a.txt"), "neep")
	run(dir, "add", ".")
	run(dir, "commit", "-m", "braw feature commit")

	n, err := UnpushedCommitCount(dir, "main")
	if err != nil {
		t.Fatalf("UnpushedCommitCount: %v", err)
	}

	if n != 1 {
		t.Errorf("never pushed: got %d unpushed, want 1 (ahead of base)", n)
	}
}

func TestSetupSessionBranchExistsCov(t *testing.T) {
	dir := setupTestRepo(t)

	if err := CreateBranch(dir, "graith/glen-clash", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	// Re-creating the same branch during setup must fail at the branch step.
	err := SetupSession(context.Background(), dir, filepath.Join(t.TempDir(), "bothy"), "graith/glen-clash", "main", false)
	if err == nil {
		t.Fatal("expected SetupSession to fail on existing branch")
	}
}

func TestSetupSessionWorktreeFailsRollsBackCov(t *testing.T) {
	dir := setupTestRepo(t)

	// Point the worktree at an existing regular file so `git worktree add`
	// fails, forcing the branch-rollback path.
	clash := filepath.Join(t.TempDir(), "skelf")
	if err := os.WriteFile(clash, []byte("neep"), 0o600); err != nil {
		t.Fatal(err)
	}

	branch := "graith/glen-rollback"

	err := SetupSession(context.Background(), dir, clash, branch, "main", false)
	if err == nil {
		t.Fatal("expected SetupSession to fail creating the worktree")
	}

	// The branch created before the failed worktree add must be rolled back.
	if RefExists(dir, branch) {
		t.Error("branch should have been rolled back after worktree failure")
	}
}

func TestIsRegisteredWorktreeNonexistentPathCov(t *testing.T) {
	repo := setupTestRepo(t)

	// Exercises resolvePath's lexical-clean fallback for a path that can't be
	// resolved on disk.
	gone := filepath.Join(t.TempDir(), "glen", "wynd")
	if IsRegisteredWorktree(repo, gone) {
		t.Error("a path that was never a worktree should not be registered")
	}
}
