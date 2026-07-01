package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRepoRootPath(t *testing.T) {
	dir := setupTestRepo(t)

	root, err := RepoRootPath(dir)
	if err != nil {
		t.Fatalf("RepoRootPath: %v", err)
	}

	// Resolve symlinks for macOS /private/var vs /var (t.TempDir can differ).
	wantResolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	gotResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	if gotResolved != wantResolved {
		t.Errorf("RepoRootPath = %q, want %q", gotResolved, wantResolved)
	}
}

func TestDiscoverDefaultBranch(t *testing.T) {
	// setupTestRepo creates a bare "origin" remote so that DiscoverDefaultBranch
	// can check origin/<branch>. We set that up manually here.
	dir := setupTestRepo(t)

	// Create a bare clone that acts as origin.
	bare := t.TempDir()
	if _, err := RunOutput(bare, "clone", "--bare", dir, bare+"/repo.git"); err != nil {
		t.Fatal(err)
	}
	// Point the original repo's origin at the bare repo.
	if _, err := RunOutput(dir, "remote", "add", "origin", bare+"/repo.git"); err != nil {
		t.Fatal(err)
	}

	if _, err := RunOutput(dir, "fetch", "origin"); err != nil {
		t.Fatal(err)
	}

	branch, err := DiscoverDefaultBranch(dir)
	if err != nil {
		t.Fatalf("DiscoverDefaultBranch: %v", err)
	}

	if branch != "main" {
		t.Errorf("DiscoverDefaultBranch = %q, want %q", branch, "main")
	}
}

func TestDiscoverDefaultBranchLocalOnly(t *testing.T) {
	dir := setupTestRepo(t)

	branch, err := DiscoverDefaultBranch(dir)
	if err != nil {
		t.Fatalf("DiscoverDefaultBranch (no origin): %v", err)
	}

	if branch != "main" {
		t.Errorf("DiscoverDefaultBranch = %q, want %q", branch, "main")
	}
}

func TestDiscoverDefaultBranchLocalMaster(t *testing.T) {
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
	run("init", "-b", "master")
	writeFile(t, filepath.Join(dir, "README.md"), "braw")
	run("add", ".")
	run("commit", "-m", "auld")

	branch, err := DiscoverDefaultBranch(dir)
	if err != nil {
		t.Fatalf("DiscoverDefaultBranch (local master): %v", err)
	}

	if branch != "master" {
		t.Errorf("DiscoverDefaultBranch = %q, want %q", branch, "master")
	}
}

func TestCreateBranch(t *testing.T) {
	tests := []struct {
		name       string
		branchName string
		fromRef    string
	}{
		{name: "from HEAD", branchName: "glen-braw", fromRef: "HEAD"},
		{name: "from main", branchName: "glen-bonnie", fromRef: "main"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupTestRepo(t)

			if err := CreateBranch(dir, tt.branchName, tt.fromRef); err != nil {
				t.Fatalf("CreateBranch(%q, %q): %v", tt.branchName, tt.fromRef, err)
			}

			if !RefExists(dir, tt.branchName) {
				t.Errorf("branch %q should exist after creation", tt.branchName)
			}
		})
	}
}

func TestDeleteBranch(t *testing.T) {
	tests := []struct {
		name       string
		branchName string
	}{
		{name: "delete simple branch", branchName: "glen-auld"},
		{name: "delete hyphenated branch", branchName: "glen-thrawn-neep"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupTestRepo(t)

			if err := CreateBranch(dir, tt.branchName, "HEAD"); err != nil {
				t.Fatalf("setup CreateBranch: %v", err)
			}

			if !RefExists(dir, tt.branchName) {
				t.Fatal("branch should exist before delete")
			}

			if err := DeleteBranch(dir, tt.branchName); err != nil {
				t.Fatalf("DeleteBranch: %v", err)
			}

			if RefExists(dir, tt.branchName) {
				t.Errorf("branch %q should not exist after deletion", tt.branchName)
			}
		})
	}
}

func TestCreateWorktree(t *testing.T) {
	dir := setupTestRepo(t)
	branchName := "glen-bothy"
	worktreePath := filepath.Join(t.TempDir(), "bothy-braw")

	if err := CreateBranch(dir, branchName, "HEAD"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	if err := CreateWorktree(dir, worktreePath, branchName); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	// The worktree directory should exist and be a git worktree.
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatal("worktree directory should exist after creation")
	}

	if !IsInsideGitRepo(worktreePath) {
		t.Error("worktree should be inside a git repo")
	}
}

func TestRemoveWorktree(t *testing.T) {
	dir := setupTestRepo(t)
	branchName := "glen-auld-remove"
	worktreePath := filepath.Join(t.TempDir(), "bothy-auld")

	if err := CreateBranch(dir, branchName, "HEAD"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	if err := CreateWorktree(dir, worktreePath, branchName); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := RemoveWorktree(dir, worktreePath); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Error("worktree directory should be removed")
	}
}

func TestSetupAndTeardownSession(t *testing.T) {
	tests := []struct {
		name       string
		branchName string
	}{
		{name: "basic session lifecycle", branchName: "glen-canny"},
		{name: "session with slashes", branchName: "graith/glen-canny-session"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupTestRepo(t)
			worktreePath := filepath.Join(t.TempDir(), "bothy-canny")

			// Create a bare clone as origin so SetupSession can use origin/<base>.
			bare := t.TempDir()
			if _, err := RunOutput(bare, "clone", "--bare", dir, bare+"/repo.git"); err != nil {
				t.Fatal(err)
			}

			if _, err := RunOutput(dir, "remote", "add", "origin", bare+"/repo.git"); err != nil {
				t.Fatal(err)
			}

			if _, err := RunOutput(dir, "fetch", "origin"); err != nil {
				t.Fatal(err)
			}

			// SetupSession with fetch=false since we already fetched.
			if err := SetupSession(context.Background(), dir, worktreePath, tt.branchName, "main", false); err != nil {
				t.Fatalf("SetupSession: %v", err)
			}

			// Branch should exist.
			if !RefExists(dir, tt.branchName) {
				t.Error("branch should exist after SetupSession")
			}
			// Worktree directory should exist.
			if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
				t.Error("worktree directory should exist after SetupSession")
			}

			// TeardownSession.
			if err := TeardownSession(dir, worktreePath, tt.branchName); err != nil {
				t.Fatalf("TeardownSession: %v", err)
			}

			// Branch should be gone.
			if RefExists(dir, tt.branchName) {
				t.Error("branch should not exist after TeardownSession")
			}
			// Worktree directory should be gone.
			if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
				t.Error("worktree directory should be removed after TeardownSession")
			}
		})
	}
}

func TestTeardownSessionIdempotent(t *testing.T) {
	t.Run("worktree already removed", func(t *testing.T) {
		dir := setupTestRepo(t)
		worktreePath := filepath.Join(t.TempDir(), "bothy-thrawn")
		branchName := "graith/glen-thrawn-idempotent"

		if err := SetupSession(context.Background(), dir, worktreePath, branchName, "main", false); err != nil {
			t.Fatalf("SetupSession: %v", err)
		}

		// Manually remove the worktree directory to simulate partial teardown.
		if err := os.RemoveAll(worktreePath); err != nil {
			t.Fatalf("RemoveAll: %v", err)
		}

		// TeardownSession should succeed despite the missing worktree.
		if err := TeardownSession(dir, worktreePath, branchName); err != nil {
			t.Fatalf("TeardownSession with missing worktree should succeed: %v", err)
		}

		if RefExists(dir, branchName) {
			t.Error("branch should be deleted after teardown")
		}
	})

	t.Run("branch already deleted", func(t *testing.T) {
		dir := setupTestRepo(t)
		worktreePath := filepath.Join(t.TempDir(), "bothy-auld-gone")
		branchName := "graith/glen-auld-gone"

		if err := SetupSession(context.Background(), dir, worktreePath, branchName, "main", false); err != nil {
			t.Fatalf("SetupSession: %v", err)
		}

		// First teardown removes everything.
		if err := TeardownSession(dir, worktreePath, branchName); err != nil {
			t.Fatalf("first TeardownSession: %v", err)
		}

		// Second teardown should be a no-op, not an error.
		if err := TeardownSession(dir, worktreePath, branchName); err != nil {
			t.Fatalf("second TeardownSession (fully idempotent) should succeed: %v", err)
		}
	})

	t.Run("worktree and branch both already gone", func(t *testing.T) {
		dir := setupTestRepo(t)
		worktreePath := filepath.Join(t.TempDir(), "bothy-thrawn-never")

		if err := TeardownSession(dir, worktreePath, "glen-thrawn-nonexistent"); err != nil {
			t.Fatalf("TeardownSession for never-created session should succeed: %v", err)
		}
	})

	t.Run("empty branch name", func(t *testing.T) {
		dir := setupTestRepo(t)
		worktreePath := filepath.Join(t.TempDir(), "bothy-neep")

		if err := TeardownSession(dir, worktreePath, ""); err != nil {
			t.Fatalf("TeardownSession with empty branch name should succeed: %v", err)
		}
	})
}

func TestSetupSessionNoOrigin(t *testing.T) {
	dir := setupTestRepo(t)
	worktreePath := filepath.Join(t.TempDir(), "bothy-croft")
	branchName := "graith/glen-croft-session"

	if err := SetupSession(context.Background(), dir, worktreePath, branchName, "main", true); err != nil {
		t.Fatalf("SetupSession with fetch=true and no origin should succeed: %v", err)
	}

	if !RefExists(dir, branchName) {
		t.Error("branch should exist after SetupSession")
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Error("worktree directory should exist after SetupSession")
	}

	if err := TeardownSession(dir, worktreePath, branchName); err != nil {
		t.Fatalf("TeardownSession: %v", err)
	}
}
