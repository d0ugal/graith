package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestListMaintenanceRepos(t *testing.T) {
	tmpDir := t.TempDir()
	gitconfig := filepath.Join(tmpDir, "gitconfig")

	t.Run("no maintenance repos", func(t *testing.T) {
		if err := os.WriteFile(gitconfig, []byte("[user]\n\tname = braw\n"), 0644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
		repos, err := ListMaintenanceRepos(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(repos) != 0 {
			t.Fatalf("expected 0 repos, got %d", len(repos))
		}
	})

	t.Run("multiple repos", func(t *testing.T) {
		content := "[maintenance]\n\trepo = /croft/braw\n\trepo = /croft/bonnie\n"
		if err := os.WriteFile(gitconfig, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
		repos, err := ListMaintenanceRepos(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(repos) != 2 {
			t.Fatalf("expected 2 repos, got %d: %v", len(repos), repos)
		}
		if repos[0] != "/croft/braw" || repos[1] != "/croft/bonnie" {
			t.Fatalf("unexpected repos: %v", repos)
		}
	})

	t.Run("duplicate entries returned as-is", func(t *testing.T) {
		content := "[maintenance]\n\trepo = /croft/glen\n\trepo = /croft/glen\n"
		if err := os.WriteFile(gitconfig, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
		repos, err := ListMaintenanceRepos(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(repos) != 2 {
			t.Fatalf("expected 2 repos (dedup happens at caller), got %d", len(repos))
		}
	})

	t.Run("works with deleted cwd", func(t *testing.T) {
		content := "[maintenance]\n\trepo = /croft/haar\n"
		if err := os.WriteFile(gitconfig, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)

		stale := filepath.Join(t.TempDir(), "bothy")
		if err := os.Mkdir(stale, 0755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(stale)
		if err := os.Remove(stale); err != nil {
			t.Fatal(err)
		}

		repos, err := ListMaintenanceRepos(context.Background())
		if err != nil {
			t.Fatalf("should succeed even with stale cwd: %v", err)
		}
		if len(repos) != 1 || repos[0] != "/croft/haar" {
			t.Fatalf("unexpected repos: %v", repos)
		}
	})
}
