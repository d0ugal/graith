package git

import (
	"os"
	"os/exec"
	"path/filepath"
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
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("test"), 0o644)
	run("add", ".")
	run("commit", "-m", "initial")
	return dir
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
	if RefExists(dir, "nonexistent-branch") {
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
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("change"), 0o644)
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

	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("modified"), 0o644)

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
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("checkout", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	run("add", ".")
	run("commit", "-m", "first feature commit")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)
	run("add", ".")
	run("commit", "-m", "second feature commit")

	commits, err = UnpushedCommitSummaries(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Errorf("got %d unpushed commits, want 2: %v", len(commits), commits)
	}
}

func TestUnpushedCommitSummariesNoRemote(t *testing.T) {
	dir := setupTestRepo(t)

	_, err := UnpushedCommitSummaries(dir, "nonexistent-branch")
	if err == nil {
		t.Error("expected error for nonexistent base branch")
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
