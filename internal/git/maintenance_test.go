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
		if err := os.WriteFile(gitconfig, []byte("[user]\n\tname = test\n"), 0644); err != nil {
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
		content := "[maintenance]\n\trepo = /foo/bar\n\trepo = /baz/qux\n"
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
		if repos[0] != "/foo/bar" || repos[1] != "/baz/qux" {
			t.Fatalf("unexpected repos: %v", repos)
		}
	})

	t.Run("duplicate entries returned as-is", func(t *testing.T) {
		content := "[maintenance]\n\trepo = /same/path\n\trepo = /same/path\n"
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
}
