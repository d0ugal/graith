package client

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case " ":
		return tea.KeyPressMsg{Code: ' ', Text: " "}
	default:
		if len(s) == 1 {
			return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
		}
		return tea.KeyPressMsg{}
	}
}

func updateModel(m createSessionModel, msg tea.Msg) createSessionModel {
	result, _ := m.Update(msg)
	return result.(createSessionModel)
}

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
	resolved, _ := filepath.EvalSymlinks(base)
	if repos[0].Path != resolved {
		t.Errorf("expected path %s, got %s", resolved, repos[0].Path)
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
		{Name: "my-app", Path: "/home/user/Code/my-app"},
		{Name: "my-lib", Path: "/home/user/Code/my-lib"},
		{Name: "other", Path: "/home/user/Code/other"},
	}
	m := newCreateSessionModel("", repos)
	m.repoInput.SetValue("my-")
	m.updateFiltered()

	if len(m.filtered) != 2 {
		t.Fatalf("expected 2 filtered repos, got %d", len(m.filtered))
	}
}

func TestCreateSessionModel_TabMovesToRepo(t *testing.T) {
	m := newCreateSessionModel("", nil)
	m.nameInput.SetValue("my-session")

	m = updateModel(m, keyPress("tab"))
	if m.focus != createFieldRepo {
		t.Errorf("expected focus on repo field after tab, got %d", m.focus)
	}
}

func TestCreateSessionModel_ShiftTabMovesToName(t *testing.T) {
	m := newCreateSessionModel("", nil)
	m.nameInput.SetValue("my-session")
	m = updateModel(m, keyPress("tab"))

	m = updateModel(m, keyPress("shift+tab"))
	if m.focus != createFieldName {
		t.Errorf("expected focus on name field after shift+tab, got %d", m.focus)
	}
}

func TestCreateSessionModel_EnterOnNameAdvancesToRepo(t *testing.T) {
	m := newCreateSessionModel("", nil)
	m.nameInput.SetValue("my-session")

	m = updateModel(m, keyPress("enter"))
	if m.focus != createFieldRepo {
		t.Errorf("expected focus on repo field after enter on name, got %d", m.focus)
	}
}

func TestCreateSessionModel_EnterOnEmptyNameStays(t *testing.T) {
	m := newCreateSessionModel("", nil)

	m = updateModel(m, keyPress("enter"))
	if m.focus != createFieldName {
		t.Errorf("expected focus to remain on name field when empty, got %d", m.focus)
	}
}

func TestCreateSessionModel_EnterOnRepoSubmits(t *testing.T) {
	m := newCreateSessionModel("/tmp/repo", nil)
	m.nameInput.SetValue("my-session")
	m = updateModel(m, keyPress("tab"))

	m = updateModel(m, keyPress("enter"))
	if !m.done {
		t.Error("expected done=true after enter on repo with valid inputs")
	}
}

func TestCreateSessionModel_EnterOnEmptyRepoDoesNotSubmit(t *testing.T) {
	m := newCreateSessionModel("", nil)
	m.nameInput.SetValue("my-session")
	m = updateModel(m, keyPress("tab"))

	m = updateModel(m, keyPress("enter"))
	if m.done {
		t.Error("expected done=false when repo is empty")
	}
}

func TestCreateSessionModel_EscCancels(t *testing.T) {
	m := newCreateSessionModel("", nil)
	m.nameInput.SetValue("something")

	_, cmd := m.Update(keyPress("esc"))
	if cmd == nil {
		t.Error("expected tea.Quit command on esc")
	}
}

func TestCreateSessionModel_SpaceInsertsDashInName(t *testing.T) {
	m := newCreateSessionModel("", nil)
	m.nameInput.SetValue("my")

	m = updateModel(m, keyPress(" "))
	val := m.nameInput.Value()
	if val != "my-" {
		t.Errorf("expected 'my-' after space, got %q", val)
	}
}

func TestCreateSessionModel_SpaceInRepoIsNormal(t *testing.T) {
	m := newCreateSessionModel("", nil)
	m.nameInput.SetValue("sess")
	m = updateModel(m, keyPress("tab"))

	m = updateModel(m, keyPress(" "))
	val := m.repoInput.Value()
	if val != " " {
		t.Errorf("expected space in repo field, got %q", val)
	}
}

func TestCreateSessionModel_DropdownNavigation(t *testing.T) {
	repos := []RepoSuggestion{
		{Name: "alpha", Path: "/alpha"},
		{Name: "beta", Path: "/beta"},
		{Name: "gamma", Path: "/gamma"},
	}
	m := newCreateSessionModel("", repos)
	m.nameInput.SetValue("test")
	m = updateModel(m, keyPress("tab"))

	if !m.showDropdown {
		t.Fatal("expected dropdown to be shown when repos available")
	}
	if m.dropdownIdx != -1 {
		t.Errorf("expected dropdownIdx=-1 initially, got %d", m.dropdownIdx)
	}

	m = updateModel(m, keyPress("down"))
	if m.dropdownIdx != 0 {
		t.Errorf("expected dropdownIdx=0 after first down, got %d", m.dropdownIdx)
	}

	m = updateModel(m, keyPress("down"))
	if m.dropdownIdx != 1 {
		t.Errorf("expected dropdownIdx=1 after second down, got %d", m.dropdownIdx)
	}

	m = updateModel(m, keyPress("up"))
	if m.dropdownIdx != 0 {
		t.Errorf("expected dropdownIdx=0 after up, got %d", m.dropdownIdx)
	}

	m = updateModel(m, keyPress("up"))
	if m.dropdownIdx != -1 {
		t.Errorf("expected dropdownIdx=-1 after up past start, got %d", m.dropdownIdx)
	}
}

func TestCreateSessionModel_DownClampedAtEnd(t *testing.T) {
	repos := []RepoSuggestion{
		{Name: "only", Path: "/only"},
	}
	m := newCreateSessionModel("", repos)
	m.nameInput.SetValue("test")
	m = updateModel(m, keyPress("tab"))

	m = updateModel(m, keyPress("down"))
	m = updateModel(m, keyPress("down"))
	m = updateModel(m, keyPress("down"))
	if m.dropdownIdx != 0 {
		t.Errorf("expected dropdownIdx clamped at 0, got %d", m.dropdownIdx)
	}
}

func TestCreateSessionModel_EnterSelectsDropdownItem(t *testing.T) {
	repos := []RepoSuggestion{
		{Name: "alpha", Path: "/path/to/alpha"},
		{Name: "beta", Path: "/path/to/beta"},
	}
	m := newCreateSessionModel("", repos)
	m.nameInput.SetValue("test")
	m = updateModel(m, keyPress("tab"))
	m = updateModel(m, keyPress("down"))

	m = updateModel(m, keyPress("enter"))
	if m.done {
		t.Error("selecting a dropdown item should not submit the form")
	}
	if m.repoInput.Value() != "/path/to/alpha" {
		t.Errorf("expected repo set to /path/to/alpha, got %q", m.repoInput.Value())
	}
	if m.showDropdown {
		t.Error("expected dropdown closed after selection")
	}
}

func TestCreateSessionModel_WindowSizeUpdates(t *testing.T) {
	m := newCreateSessionModel("", nil)
	m = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 || m.height != 40 {
		t.Errorf("expected 120x40, got %dx%d", m.width, m.height)
	}
}
