package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/testutil"
	"github.com/d0ugal/graith/internal/tools"
)

// TestPullIfCleanPinsToolGenerationAcrossReload is the #1287 git-pull A/B
// regression: a single pull runs many git subprocesses (rev-parse, symbolic-ref,
// fetch, merge-base, merge). They must all run against one resolved git
// generation. This configures wrapper A, starts a pull whose `fetch` blocks,
// reloads the tools registry to wrapper B mid-operation, then releases. The
// whole pull must stay on A; a subsequent pull must run entirely on B.
func TestPullIfCleanPinsToolGenerationAcrossReload(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found on PATH")
	}

	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	t.Cleanup(tools.Reset)

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "calls.log")
	releasePath := filepath.Join(binDir, "release")

	wrapperA := writePullGitWrapper(t, binDir, "gitA", "A", realGit, logPath, releasePath)
	wrapperB := writePullGitWrapper(t, binDir, "gitB", "B", realGit, logPath, "")

	sm := newTestSM(t)

	tools.Configure(tools.Config{Git: wrapperA})

	pullErr := make(chan error, 1)

	go func() {
		_, err := sm.pullIfClean(context.Background(), cloneDir)
		pullErr <- err
	}()

	// Wait until the blocking fetch has started, proving the operation pinned A.
	waitForLogContains(t, logPath, " fetch")

	// An accepted reload swaps the git executable mid-pull.
	tools.Configure(tools.Config{Git: wrapperB})

	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-pullErr:
		if err != nil {
			t.Fatalf("first pull: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("first pull did not complete after release")
	}

	op1 := readNonEmptyLines(t, logPath)
	if len(op1) == 0 {
		t.Fatal("first pull ran no git subcommands")
	}

	sawFetch := false

	for _, line := range op1 {
		if !strings.HasPrefix(line, "A ") {
			t.Fatalf("first pull ran a subcommand on the wrong generation: %q (the pull must stay entirely on A across the reload)", line)
		}

		if strings.Contains(line, " fetch") {
			sawFetch = true
		}
	}

	if !sawFetch {
		t.Fatal("first pull never reached the fetch subcommand; the test did not exercise the reload window")
	}

	// A fresh pull picks up the new generation wholesale — every subcommand on B.
	if err := os.Truncate(logPath, 0); err != nil {
		t.Fatal(err)
	}

	// Advance the remote again with distinct content so the second pull has a
	// real fast-forward to perform (gitRun uses the real git, not the wrappers).
	secondClone := filepath.Join(t.TempDir(), "advance2")
	gitRun(t, "", "clone", bareDir, secondClone)

	if err := os.WriteFile(filepath.Join(secondClone, "second-advance.txt"), []byte("second generation content"), 0o600); err != nil {
		t.Fatal(err)
	}

	gitRun(t, secondClone, "add", ".")
	gitRun(t, secondClone, "commit", "-m", "second advance")
	gitRun(t, secondClone, "push", "origin", "main")

	if _, err := sm.pullIfClean(context.Background(), cloneDir); err != nil {
		t.Fatalf("second pull: %v", err)
	}

	op2 := readNonEmptyLines(t, logPath)
	if len(op2) == 0 {
		t.Fatal("second pull ran no git subcommands")
	}

	for _, line := range op2 {
		if !strings.HasPrefix(line, "B ") {
			t.Fatalf("second pull ran a subcommand on the old generation: %q (a new operation must run entirely on B)", line)
		}
	}
}

// writePullGitWrapper writes an executable git wrapper that logs "<tag> <args>"
// and, when releasePath is non-empty, blocks on any `fetch` subcommand until
// releasePath exists, then execs the real git so the pull still succeeds.
func writePullGitWrapper(t *testing.T, dir, name, tag, realGit, logPath, releasePath string) string {
	t.Helper()

	var block string
	if releasePath != "" {
		block = "for a in \"$@\"; do\n" +
			"  if [ \"$a\" = fetch ]; then\n" +
			"    while [ ! -e '" + releasePath + "' ]; do sleep 0.02; done\n" +
			"    break\n" +
			"  fi\n" +
			"done\n"
	}

	script := "#!/bin/sh\n" +
		"printf '" + tag + " %s\\n' \"$*\" >> '" + logPath + "'\n" +
		block +
		"exec '" + realGit + "' \"$@\"\n"

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	return path
}

func waitForLogContains(t *testing.T, logPath, want string) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), want) {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("log %q did not contain %q in time", logPath, want)
}

func readNonEmptyLines(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var lines []string

	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}

	return lines
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := testutil.GitCommand(args...)
	if dir != "" {
		cmd.Dir = dir
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func setupTestRepo(t *testing.T) (bareDir, cloneDir string) {
	t.Helper()
	testutil.IsolateGit(t)
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
		sessions: make(map[string]SessionDriver),
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

func headRev(t *testing.T, dir string) string {
	t.Helper()

	rev, err := git.RunOutputContext(context.Background(), dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD in %s: %v", dir, err)
	}

	return rev
}

// The loop must perform its first tick shortly after startup, not after a full
// interval. A daemon restart re-execs the loop from scratch, so a wait-first
// loop would leave maintenance repos stale for up to the interval after every
// restart. With a tiny startup delay and a 24h interval, the clone can only
// fast-forward within the test window if that initial tick fires.
func TestRunGitPullLoop_InitialTickBeforeInterval(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	// Register the clone as a git maintenance repo under a temp global config so
	// ListMaintenanceRepos returns it. Point both HOME (which it resolves) and
	// GIT_CONFIG_GLOBAL at the seeded config to be robust against an inherited
	// GIT_CONFIG_GLOBAL in the environment.
	home := t.TempDir()
	gitConfig := filepath.Join(home, ".gitconfig")

	if err := os.WriteFile(gitConfig, []byte("[maintenance]\n\trepo = "+cloneDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)

	origDelay := gitPullStartupDelay
	gitPullStartupDelay = 5 * time.Millisecond

	t.Cleanup(func() { gitPullStartupDelay = origDelay })

	sm := newTestSM(t)
	sm.cfg.GitPull.Interval = "24h"

	before := headRev(t, cloneDir)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})

	go func() {
		defer close(done)

		sm.RunGitPullLoop(ctx)
	}()

	// Stop the loop and wait for it — and any git subprocess it cancels — to
	// exit before t.TempDir removes the clone. Registered after setupTestRepo's
	// t.TempDir, so LIFO cleanup order runs this first: otherwise a git process
	// still finishing the fast-forward would race the directory removal.
	t.Cleanup(func() {
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("git-pull loop did not exit after context cancellation")
		}
	})

	// The clone can only fast-forward within this window via the initial tick;
	// with a 24h interval, a wait-first loop would not have ticked yet.
	pulled := false

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if headRev(t, cloneDir) != before {
			pulled = true

			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	if !pulled {
		t.Fatal("git-pull loop did not perform its initial tick before the interval elapsed")
	}
}

func TestResolveUpstream_Cov(t *testing.T) {
	// A clone tracks origin/main, so resolveUpstream reports the remote and ref.
	_, cloneDir := setupTestRepo(t)

	remote, ref := resolveUpstream(context.Background(), git.NewRunner(), cloneDir, "main")
	if remote != "origin" {
		t.Errorf("expected remote 'origin', got %q", remote)
	}

	if ref != "origin/main" {
		t.Errorf("expected upstream ref 'origin/main', got %q", ref)
	}

	// A repo with an origin remote but a branch that has no @{upstream} falls
	// back to ("origin", "").
	gitRun(t, cloneDir, "checkout", "-b", "canny-feature")

	remote, ref = resolveUpstream(context.Background(), git.NewRunner(), cloneDir, "canny-feature")
	if remote != "origin" || ref != "" {
		t.Errorf("untracked branch with origin should be ('origin',''), got (%q,%q)", remote, ref)
	}

	// A repo with no remote at all → ("", "").
	local := filepath.Join(t.TempDir(), "hame")
	gitRun(t, "", "init", "--initial-branch=main", local)
	gitRun(t, local, "commit", "--allow-empty", "-m", "initial")

	remote, ref = resolveUpstream(context.Background(), git.NewRunner(), local, "main")
	if remote != "" || ref != "" {
		t.Errorf("repo with no remote should be ('',''), got (%q,%q)", remote, ref)
	}
}

// TestPullIfClean_NoUpstream covers the branch where the default branch has no
// upstream and no origin remote — the pull is skipped without error.
func TestPullIfClean_NoUpstream_Cov(t *testing.T) {
	local := filepath.Join(t.TempDir(), "hame")
	gitRun(t, "", "init", "--initial-branch=main", local)
	gitRun(t, local, "commit", "--allow-empty", "-m", "initial")

	sm := newTestSM(t)

	pulled, err := sm.pullIfClean(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected skip for a repo with no upstream/remote")
	}
}

// TestPullIfClean_Diverged covers the divergence branch: local and remote have
// each moved on, so an ff-only pull is impossible and the repo is skipped.
func TestPullIfClean_Diverged_Cov(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	// Create a divergent local commit so HEAD is neither ancestor nor descendant
	// of origin/main after fetch.
	_ = os.WriteFile(filepath.Join(cloneDir, "local.txt"), []byte("local"), 0o600)
	gitRun(t, cloneDir, "add", ".")
	gitRun(t, cloneDir, "commit", "-m", "divergent local commit")

	sm := newTestSM(t)

	pulled, err := sm.pullIfClean(context.Background(), cloneDir)
	if err != nil {
		t.Fatal(err)
	}

	if pulled {
		t.Fatal("expected skip when local and remote have diverged")
	}
}

// TestRunGitPullLoop_CancelledCtxReturns ensures the loop exits when its context
// is cancelled (rather than sleeping out its interval).
func TestRunGitPullLoop_CancelledCtxReturns_Cov(t *testing.T) {
	sm := newTestSM(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})

	go func() {
		sm.RunGitPullLoop(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RunGitPullLoop did not return on cancelled context")
	}
}

// TestRunGitPullTick_PullsMaintenanceRepo drives a full tick through
// git.ListMaintenanceRepos by pointing HOME at a throwaway global git config
// whose maintenance.repo list contains a clone that is behind its remote. The
// tick should fast-forward it.
func TestRunGitPullTick_PullsMaintenanceRepo_Cov(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	advanceRemote(t, bareDir, cloneDir)

	// Fully isolate git's global/system config so ListMaintenanceRepos only sees
	// the repo we register — never the developer's real maintenance repos.
	// Overriding HOME alone is NOT enough: git's --global scope also reads/writes
	// $XDG_CONFIG_HOME/git/config, so a developer with XDG_CONFIG_HOME set (common
	// on Linux/CI) could have their real config read (and its repos then fetched)
	// or overwritten. Pin GIT_CONFIG_GLOBAL to a temp file, disable system config,
	// and redirect XDG so every read/write path lands inside the temp home.
	// RunContextEnv/RunOutputContext build their env from os.Environ(), so these
	// t.Setenv values reach the production ListMaintenanceRepos + pullIfClean paths.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// Register the maintenance repo in the isolated global config.
	gitRunHome(t, home, cloneDir, "config", "--global", "maintenance.repo", cloneDir)

	sm := newTestSM(t)

	sm.runGitPullTick(context.Background())

	head, err := gitOutHome(t, home, cloneDir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	remoteHead, err := gitOutHome(t, home, cloneDir, "rev-parse", "origin/main")
	if err != nil {
		t.Fatalf("rev-parse origin/main: %v", err)
	}

	if head == "" || head != remoteHead {
		t.Fatalf("maintenance repo should have been fast-forwarded: HEAD %q vs origin/main %q", head, remoteHead)
	}
}

// gitCmdHome builds a git command with an overridden HOME so --global config
// writes/reads land in the test's throwaway home.
func gitCmdHome(home, dir string, args ...string) *exec.Cmd {
	cmd := testutil.GitCommand(args...)
	if dir != "" {
		cmd.Dir = dir
	}

	cmd.Env = testutil.GitEnv(
		"HOME="+home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig"),
	)

	return cmd
}

func gitRunHome(t *testing.T, home, dir string, args ...string) {
	t.Helper()

	out, err := gitCmdHome(home, dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func gitOutHome(t *testing.T, home, dir string, args ...string) (string, error) {
	t.Helper()

	out, err := gitCmdHome(home, dir, args...).Output()

	return strings.TrimSpace(string(out)), err
}
