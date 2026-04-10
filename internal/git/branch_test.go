package git

import (
	"os"
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

func TestCreateBranch(t *testing.T) {
	tests := []struct {
		name       string
		branchName string
		fromRef    string
	}{
		{name: "from HEAD", branchName: "feature-1", fromRef: "HEAD"},
		{name: "from main", branchName: "feature-2", fromRef: "main"},
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
		{name: "delete simple branch", branchName: "to-delete"},
		{name: "delete hyphenated branch", branchName: "my-feature-branch"},
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
	branchName := "wt-branch"
	worktreePath := filepath.Join(t.TempDir(), "my-worktree")

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
	branchName := "wt-remove-branch"
	worktreePath := filepath.Join(t.TempDir(), "remove-worktree")

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
		{name: "basic session lifecycle", branchName: "session-branch"},
		{name: "session with slashes", branchName: "graith/test-session"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupTestRepo(t)
			worktreePath := filepath.Join(t.TempDir(), "session-wt")

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
			if err := SetupSession(dir, worktreePath, tt.branchName, "main", false); err != nil {
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
