package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setupTestRepo(t *testing.T) string {
	t.Helper()
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
