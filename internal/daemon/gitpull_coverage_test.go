package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveUpstream_Cov(t *testing.T) {
	// A clone tracks origin/main, so resolveUpstream reports the remote and ref.
	_, cloneDir := setupTestRepo(t)

	remote, ref := resolveUpstream(context.Background(), cloneDir, "main")
	if remote != "origin" {
		t.Errorf("expected remote 'origin', got %q", remote)
	}

	if ref != "origin/main" {
		t.Errorf("expected upstream ref 'origin/main', got %q", ref)
	}

	// A repo with an origin remote but a branch that has no @{upstream} falls
	// back to ("origin", "").
	gitRun(t, cloneDir, "checkout", "-b", "canny-feature")

	remote, ref = resolveUpstream(context.Background(), cloneDir, "canny-feature")
	if remote != "origin" || ref != "" {
		t.Errorf("untracked branch with origin should be ('origin',''), got (%q,%q)", remote, ref)
	}

	// A repo with no remote at all → ("", "").
	local := filepath.Join(t.TempDir(), "hame")
	gitRun(t, "", "init", "--initial-branch=main", local)
	gitRun(t, local, "commit", "--allow-empty", "-m", "initial")

	remote, ref = resolveUpstream(context.Background(), local, "main")
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

	// Isolate git's global config in a temp HOME so ListMaintenanceRepos only
	// sees the repo we register — never the developer's real maintenance repos.
	home := t.TempDir()
	t.Setenv("HOME", home)
	// git resolves --global from $HOME/.gitconfig; write via git config itself.
	gitRunHome(t, home, cloneDir, "config", "--global", "maintenance.repo", cloneDir)

	sm := newTestSM(t)

	sm.runGitPullTick(context.Background())

	head, _ := gitOutHome(t, home, cloneDir, "rev-parse", "HEAD")
	remoteHead, _ := gitOutHome(t, home, cloneDir, "rev-parse", "origin/main")
	if head != remoteHead {
		t.Fatalf("maintenance repo should have been fast-forwarded: HEAD %q vs origin/main %q", head, remoteHead)
	}
}

// gitCmdHome builds a git command with an overridden HOME so --global config
// writes/reads land in the test's throwaway home.
func gitCmdHome(home, dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}

	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
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
