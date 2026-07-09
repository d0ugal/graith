package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	grpty "github.com/d0ugal/graith/internal/pty"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}

	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func setupTestRepo(t *testing.T) (bareDir, cloneDir string) {
	t.Helper()
	tmp := t.TempDir()

	bareDir = filepath.Join(tmp, "remote.git")
	cloneDir = filepath.Join(tmp, "clone")

	gitRun(t, "", "init", "--bare", "--initial-branch=main", bareDir)
	gitRun(t, "", "clone", bareDir, cloneDir)
	_ = os.WriteFile(filepath.Join(cloneDir, "file.txt"), []byte("initial"), 0o600)
	gitRun(t, cloneDir, "add", ".")
	gitRun(t, cloneDir, "commit", "-m", "initial")
	gitRun(t, cloneDir, "push", "origin", "main")

	return bareDir, cloneDir
}

func advanceRemote(t *testing.T, bareDir, cloneDir string) {
	t.Helper()
	tmp := t.TempDir()
	secondClone := filepath.Join(tmp, "second")

	gitRun(t, "", "clone", bareDir, secondClone)
	_ = os.WriteFile(filepath.Join(secondClone, "newfile.txt"), []byte("new content"), 0o600)
	gitRun(t, secondClone, "add", ".")
	gitRun(t, secondClone, "commit", "-m", "advance remote")
	gitRun(t, secondClone, "push", "origin", "main")
}

func newTestSM(t *testing.T) *SessionManager {
	t.Helper()

	return &SessionManager{
		state:    NewState(),
		sessions: make(map[string]*grpty.Session),
		cfg: &config.Config{
			GitPull: config.GitPullConfig{
				Enabled:  true,
				Interval: "1h",
			},
		},
		log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
}

func TestPullIfClean_BehindRemote(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	sm := newTestSM(t)

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if !pulled {
		t.Fatal("expected pull to succeed")
	}

	head, _ := git.RunOutputContext(context.Background(), cloneDir, "rev-parse", "HEAD")

	remoteHead, _ := git.RunOutputContext(context.Background(), cloneDir, "rev-parse", "origin/main")
	if head != remoteHead {
		t.Fatalf("HEAD (%s) should match origin/main (%s)", head, remoteHead)
	}
}

func TestPullIfClean_AlreadyUpToDate(t *testing.T) {
	_, cloneDir := setupTestRepo(t)

	sm := newTestSM(t)

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected no pull when already up-to-date")
	}
}

func TestPullIfClean_DirtyWorktree(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	_ = os.WriteFile(filepath.Join(cloneDir, "dirty.txt"), []byte("dirty"), 0o600)

	sm := newTestSM(t)

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected skip when dirty")
	}
}

func TestPullIfClean_OnFeatureBranch(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	gitRun(t, cloneDir, "checkout", "-b", "feature-branch")

	sm := newTestSM(t)

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected skip on non-default branch")
	}
}

func TestPullIfClean_DetachedHead(t *testing.T) {
	_, cloneDir := setupTestRepo(t)

	gitRun(t, cloneDir, "checkout", "--detach", "HEAD")

	sm := newTestSM(t)

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected skip on detached HEAD")
	}
}

func TestPullIfClean_LocalAhead(t *testing.T) {
	_, cloneDir := setupTestRepo(t)

	_ = os.WriteFile(filepath.Join(cloneDir, "local.txt"), []byte("local change"), 0o600)
	gitRun(t, cloneDir, "add", ".")
	gitRun(t, cloneDir, "commit", "-m", "local commit")

	sm := newTestSM(t)

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected skip when local is ahead")
	}
}

func TestPullIfClean_BareRepo(t *testing.T) {
	bareDir, _ := setupTestRepo(t)

	sm := newTestSM(t)

	pulled, err := sm.pullIfClean(context.Background(), bareDir)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected skip on bare repo")
	}
}

func TestPullIfClean_InProgressRebase(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	gitDir, _ := git.RunOutputContext(context.Background(), cloneDir, "rev-parse", "--git-dir")
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(cloneDir, gitDir)
	}

	_ = os.WriteFile(filepath.Join(gitDir, "REBASE_HEAD"), []byte("fake"), 0o600)

	sm := newTestSM(t)

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected skip with in-progress rebase")
	}
}

func TestPullIfClean_HooksDisabled(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	hooksDir := filepath.Join(cloneDir, ".git", "hooks")
	_ = os.MkdirAll(hooksDir, 0o750)

	sentinel := filepath.Join(t.TempDir(), "hook-ran")
	hookScript := "#!/bin/sh\ntouch " + sentinel + "\n"
	_ = os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(hookScript), 0o755) //nolint:gosec // G306: script/binary must be executable

	sm := newTestSM(t)

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if !pulled {
		t.Fatal("expected pull to succeed")
	}

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("post-merge hook should not have run (hooks disabled)")
	}
}

// A session in its own worktree on a feature branch shares only the object
// store with the source checkout — a fast-forward of the default branch cannot
// disturb it, so it must not block the pull.
func TestPullIfClean_WorktreeSessionDoesNotBlock(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	sm := newTestSM(t)
	sm.state.Sessions["braw-session"] = &SessionState{
		ID:           "braw-session",
		RepoPath:     cloneDir,
		WorktreePath: filepath.Join(t.TempDir(), "bothy"),
		Branch:       "canny-feature",
		Status:       StatusRunning,
	}

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if !pulled {
		t.Fatal("expected pull to proceed for a worktree session on a feature branch")
	}
}

// An in-place session operates directly in the source checkout, so pulling
// would move files under an active agent — it must block the pull.
func TestPullIfClean_InPlaceSessionBlocks(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	sm := newTestSM(t)
	sm.state.Sessions["thrawn-session"] = &SessionState{
		ID:           "thrawn-session",
		RepoPath:     cloneDir,
		WorktreePath: cloneDir,
		Branch:       "main",
		Status:       StatusRunning,
	}

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected skip with in-place session on the source checkout")
	}
}

// A session whose worktree has the default branch checked out would have the
// ref moved out from under it — it must block the pull, even while creating.
func TestPullIfClean_DefaultBranchSessionBlocks(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	sm := newTestSM(t)
	sm.state.Sessions["thrawn-session"] = &SessionState{
		ID:           "thrawn-session",
		RepoPath:     cloneDir,
		WorktreePath: filepath.Join(t.TempDir(), "bothy"),
		Branch:       "main",
		Status:       StatusCreating,
	}

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected skip with session on the default branch")
	}
}

// The explicit InPlace flag blocks the pull even when the recorded WorktreePath
// does not resolve to the repo root, decoupling the guard from that invariant.
func TestPullIfClean_InPlaceFlagBlocks(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	sm := newTestSM(t)
	sm.state.Sessions["thrawn-session"] = &SessionState{
		ID:           "thrawn-session",
		RepoPath:     cloneDir,
		WorktreePath: filepath.Join(t.TempDir(), "bothy"),
		Branch:       "canny-feature",
		InPlace:      true,
		Status:       StatusRunning,
	}

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected skip with an in-place session flagged InPlace")
	}
}

// An included repo on a feature branch, like a primary worktree session, must
// not block the pull of that included repo.
func TestPullIfClean_IncludeOnFeatureBranchDoesNotBlock(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	sm := newTestSM(t)
	sm.state.Sessions["braw-session"] = &SessionState{
		ID:           "braw-session",
		RepoPath:     filepath.Join(t.TempDir(), "croft"),
		WorktreePath: filepath.Join(t.TempDir(), "bothy"),
		Branch:       "canny-feature",
		Status:       StatusRunning,
		Includes: []IncludedRepoState{{
			RepoPath:     cloneDir,
			WorktreePath: filepath.Join(t.TempDir(), "wynd"),
			Branch:       "canny-feature",
		}},
	}

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if !pulled {
		t.Fatal("expected pull to proceed for an included repo on a feature branch")
	}
}

// An included repo checked out on the default branch would have the ref moved
// out from under it — it must block the pull.
func TestPullIfClean_IncludeOnDefaultBranchBlocks(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	sm := newTestSM(t)
	sm.state.Sessions["thrawn-session"] = &SessionState{
		ID:           "thrawn-session",
		RepoPath:     filepath.Join(t.TempDir(), "croft"),
		WorktreePath: filepath.Join(t.TempDir(), "bothy"),
		Branch:       "canny-feature",
		Status:       StatusRunning,
		Includes: []IncludedRepoState{{
			RepoPath:     cloneDir,
			WorktreePath: filepath.Join(t.TempDir(), "wynd"),
			Branch:       "main",
		}},
	}

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected skip with an included repo on the default branch")
	}
}

func TestHasInProgressOp(t *testing.T) {
	dir := t.TempDir()
	if hasInProgressOp(dir) {
		t.Fatal("expected no in-progress ops in empty dir")
	}

	for _, indicator := range []string{"MERGE_HEAD", "REBASE_HEAD", "CHERRY_PICK_HEAD", "BISECT_LOG", "REVERT_HEAD"} {
		_ = os.WriteFile(filepath.Join(dir, indicator), []byte("x"), 0o600)

		if !hasInProgressOp(dir) {
			t.Fatalf("expected in-progress op for %s", indicator)
		}

		_ = os.Remove(filepath.Join(dir, indicator))
	}

	for _, indicator := range []string{"rebase-merge", "rebase-apply", "sequencer"} {
		_ = os.MkdirAll(filepath.Join(dir, indicator), 0o750)

		if !hasInProgressOp(dir) {
			t.Fatalf("expected in-progress op for %s", indicator)
		}

		_ = os.RemoveAll(filepath.Join(dir, indicator))
	}
}
