package client

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestDiscoverRepos_AllowedPaths(t *testing.T) {
	base := t.TempDir()

	repo1 := filepath.Join(base, "repo1")
	repo2 := filepath.Join(base, "repo2")
	notARepo := filepath.Join(base, "not-a-repo")
	os.MkdirAll(filepath.Join(repo1, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo2, ".git"), 0o755)
	os.MkdirAll(notARepo, 0o755)

	repos := DiscoverRepos([]string{base}, nil)

	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	sort.Strings(names)

	if len(names) != 2 {
		t.Fatalf("expected 2 repos, got %d: %v", len(names), names)
	}
	if names[0] != "repo1" || names[1] != "repo2" {
		t.Errorf("expected [repo1, repo2], got %v", names)
	}
}

func TestDiscoverRepos_AllowedPathIsRepo(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, ".git"), 0o755)

	repos := DiscoverRepos([]string{base}, nil)
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if repos[0].Path != base {
		t.Errorf("expected path %s, got %s", base, repos[0].Path)
	}
}

func TestDiscoverRepos_GitFile(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "worktree-repo")
	os.MkdirAll(repo, 0o755)
	os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: /some/other/path"), 0o644)

	repos := DiscoverRepos([]string{base}, nil)
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo (git file), got %d", len(repos))
	}
	if repos[0].Name != "worktree-repo" {
		t.Errorf("expected name worktree-repo, got %s", repos[0].Name)
	}
}

func TestDiscoverRepos_SessionDerived(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "session-repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)

	sessions := []protocol.SessionInfo{
		{RepoPath: repo, SystemKind: ""},
		{RepoPath: repo, SystemKind: ""},
		{RepoPath: "", SystemKind: ""},
		{RepoPath: "/some/path", SystemKind: "orchestrator"},
	}

	repos := DiscoverRepos(nil, sessions)
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d: %v", len(repos), repos)
	}
	if repos[0].Name != "session-repo" {
		t.Errorf("expected name session-repo, got %s", repos[0].Name)
	}
}

func TestDiscoverRepos_Dedup(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "myrepo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)

	sessions := []protocol.SessionInfo{
		{RepoPath: repo},
	}

	repos := DiscoverRepos([]string{base}, sessions)
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo (dedup), got %d", len(repos))
	}
}

func TestDiscoverRepos_EmptyInputs(t *testing.T) {
	repos := DiscoverRepos(nil, nil)
	if len(repos) != 0 {
		t.Fatalf("expected 0 repos, got %d", len(repos))
	}
}

func TestDiscoverRepos_UnreadablePath(t *testing.T) {
	repos := DiscoverRepos([]string{"/nonexistent/path/that/does/not/exist"}, nil)
	if len(repos) != 0 {
		t.Fatalf("expected 0 repos for unreadable path, got %d", len(repos))
	}
}

func TestDiscoverRepos_Sorted(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"zebra", "alpha", "middle"} {
		os.MkdirAll(filepath.Join(base, name, ".git"), 0o755)
	}

	repos := DiscoverRepos([]string{base}, nil)
	if len(repos) != 3 {
		t.Fatalf("expected 3 repos, got %d", len(repos))
	}
	if repos[0].Name != "alpha" || repos[1].Name != "middle" || repos[2].Name != "zebra" {
		t.Errorf("expected [alpha, middle, zebra], got [%s, %s, %s]",
			repos[0].Name, repos[1].Name, repos[2].Name)
	}
}

func TestNewCreateSessionModel_DefaultRepo(t *testing.T) {
	m := newCreateSessionModel("/tmp/myrepo", nil)
	if m.repoInput.Value() != "/tmp/myrepo" {
		t.Errorf("expected default repo /tmp/myrepo, got %s", m.repoInput.Value())
	}
	if m.focus != createFieldName {
		t.Errorf("expected initial focus on name field")
	}
}

func TestNewCreateSessionModel_EmptyDefaultRepo(t *testing.T) {
	m := newCreateSessionModel("", nil)
	if m.repoInput.Value() != "" {
		t.Errorf("expected empty repo, got %s", m.repoInput.Value())
	}
}

func TestCreateSessionModel_FilterSuggestions(t *testing.T) {
	repos := []RepoSuggestion{
		{Name: "graith", Path: "/home/user/Code/graith"},
		{Name: "grafana", Path: "/home/user/Code/grafana"},
		{Name: "other", Path: "/home/user/Code/other"},
	}
	m := newCreateSessionModel("", repos)
	m.repoInput.SetValue("gra")
	m.updateFiltered()

	if len(m.filtered) != 2 {
		t.Fatalf("expected 2 filtered repos, got %d", len(m.filtered))
	}
}
