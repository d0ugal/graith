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
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
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
	m := newCreateSessionModel("/tmp/croft", nil, nil, "")
	if m.repoInput.Value() != "/tmp/croft" {
		t.Errorf("expected default repo /tmp/croft, got %s", m.repoInput.Value())
	}
	if m.focus != createFieldName {
		t.Errorf("expected initial focus on name field")
	}
}

func TestNewCreateSessionModel_EmptyDefaultRepo(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	if m.repoInput.Value() != "" {
		t.Errorf("expected empty repo, got %s", m.repoInput.Value())
	}
}

func TestCreateSessionModel_FilterSuggestions(t *testing.T) {
	repos := []RepoSuggestion{
		{Name: "braw-app", Path: "/home/user/Code/braw-app"},
		{Name: "braw-lib", Path: "/home/user/Code/braw-lib"},
		{Name: "neep", Path: "/home/user/Code/neep"},
	}
	m := newCreateSessionModel("", repos, nil, "")
	m.repoInput.SetValue("braw-")
	m.updateFiltered()

	if len(m.filtered) != 2 {
		t.Fatalf("expected 2 filtered repos, got %d", len(m.filtered))
	}
}

func TestCreateSessionModel_TabMovesToRepo(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	m.nameInput.SetValue("braw-session")

	m = updateModel(m, keyPress("tab"))
	if m.focus != createFieldRepo {
		t.Errorf("expected focus on repo field after tab, got %d", m.focus)
	}
}

func TestCreateSessionModel_ShiftTabMovesToName(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	m.nameInput.SetValue("braw-session")
	m = updateModel(m, keyPress("tab"))

	m = updateModel(m, keyPress("shift+tab"))
	if m.focus != createFieldName {
		t.Errorf("expected focus on name field after shift+tab, got %d", m.focus)
	}
}

func TestCreateSessionModel_EnterOnNameAdvancesToRepo(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	m.nameInput.SetValue("braw-session")

	m = updateModel(m, keyPress("enter"))
	if m.focus != createFieldRepo {
		t.Errorf("expected focus on repo field after enter on name, got %d", m.focus)
	}
}

func TestCreateSessionModel_EnterOnEmptyNameStays(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")

	m = updateModel(m, keyPress("enter"))
	if m.focus != createFieldName {
		t.Errorf("expected focus to remain on name field when empty, got %d", m.focus)
	}
}

func TestCreateSessionModel_EnterOnRepoSubmits(t *testing.T) {
	m := newCreateSessionModel("/tmp/repo", nil, nil, "")
	m.nameInput.SetValue("braw-session")
	m = updateModel(m, keyPress("tab"))

	m = updateModel(m, keyPress("enter"))
	if !m.done {
		t.Error("expected done=true after enter on repo with valid inputs")
	}
}

func TestCreateSessionModel_EnterOnEmptyRepoDoesNotSubmit(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	m.nameInput.SetValue("braw-session")
	m = updateModel(m, keyPress("tab"))

	m = updateModel(m, keyPress("enter"))
	if m.done {
		t.Error("expected done=false when repo is empty")
	}
	if m.focus != createFieldRepo {
		t.Errorf("expected focus to remain on repo when empty, got %d", m.focus)
	}
}

func TestCreateSessionModel_EscCancels(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	m.nameInput.SetValue("braw")

	_, cmd := m.Update(keyPress("esc"))
	if cmd == nil {
		t.Error("expected tea.Quit command on esc")
	}
}

func TestCreateSessionModel_SpaceInsertsDashInName(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	m.nameInput.SetValue("braw")

	m = updateModel(m, keyPress(" "))
	val := m.nameInput.Value()
	if val != "braw-" {
		t.Errorf("expected 'braw-' after space, got %q", val)
	}
}

func TestCreateSessionModel_SpaceInRepoIsNormal(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	m.nameInput.SetValue("neep")
	m = updateModel(m, keyPress("tab"))

	m = updateModel(m, keyPress(" "))
	val := m.repoInput.Value()
	if val != " " {
		t.Errorf("expected space in repo field, got %q", val)
	}
}

func TestCreateSessionModel_DropdownNavigation(t *testing.T) {
	repos := []RepoSuggestion{
		{Name: "braw", Path: "/braw"},
		{Name: "canny", Path: "/canny"},
		{Name: "bonnie", Path: "/bonnie"},
	}
	m := newCreateSessionModel("", repos, nil, "")
	m.nameInput.SetValue("neep")
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
		{Name: "neep", Path: "/neep"},
	}
	m := newCreateSessionModel("", repos, nil, "")
	m.nameInput.SetValue("kirk")
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
		{Name: "braw", Path: "/path/to/braw"},
		{Name: "canny", Path: "/path/to/canny"},
	}
	m := newCreateSessionModel("", repos, nil, "")
	m.nameInput.SetValue("neep")
	m = updateModel(m, keyPress("tab"))
	m = updateModel(m, keyPress("down"))

	m = updateModel(m, keyPress("enter"))
	if m.done {
		t.Error("selecting a dropdown item should not submit the form")
	}
	if m.repoInput.Value() != "/path/to/braw" {
		t.Errorf("expected repo set to /path/to/braw, got %q", m.repoInput.Value())
	}
	if m.showDropdown {
		t.Error("expected dropdown closed after selection")
	}
}

func TestCreateSessionModel_DefaultAgentSelected(t *testing.T) {
	agents := []string{"claude", "codex", "cursor"}
	m := newCreateSessionModel("", nil, agents, "codex")
	if m.selectedAgent() != "codex" {
		t.Errorf("expected default agent codex selected, got %q", m.selectedAgent())
	}
}

func TestCreateSessionModel_DefaultAgentMissingFallsBackToFirst(t *testing.T) {
	agents := []string{"claude", "codex"}
	m := newCreateSessionModel("", nil, agents, "thrawn")
	if m.selectedAgent() != "claude" {
		t.Errorf("expected fallback to first agent, got %q", m.selectedAgent())
	}
}

func TestCreateSessionModel_TabReachesAgentField(t *testing.T) {
	agents := []string{"claude", "codex"}
	m := newCreateSessionModel("", nil, agents, "claude")
	m.nameInput.SetValue("braw-session")

	m = updateModel(m, keyPress("tab"))
	if m.focus != createFieldRepo {
		t.Fatalf("expected repo focus after first tab, got %d", m.focus)
	}
	m = updateModel(m, keyPress("tab"))
	if m.focus != createFieldAgent {
		t.Fatalf("expected agent focus after second tab, got %d", m.focus)
	}
}

func TestCreateSessionModel_AgentCyclesWithArrows(t *testing.T) {
	agents := []string{"claude", "codex", "cursor"}
	m := newCreateSessionModel("", nil, agents, "claude")
	m.nameInput.SetValue("braw-session")
	m = updateModel(m, keyPress("tab"))
	m = updateModel(m, keyPress("tab"))
	if m.focus != createFieldAgent {
		t.Fatalf("expected agent focus, got %d", m.focus)
	}

	m = updateModel(m, keyPress("right"))
	if m.selectedAgent() != "codex" {
		t.Errorf("expected codex after right, got %q", m.selectedAgent())
	}
	m = updateModel(m, keyPress("right"))
	m = updateModel(m, keyPress("right"))
	if m.selectedAgent() != "claude" {
		t.Errorf("expected wrap to claude after three rights, got %q", m.selectedAgent())
	}
	m = updateModel(m, keyPress("left"))
	if m.selectedAgent() != "cursor" {
		t.Errorf("expected wrap to cursor after left from first, got %q", m.selectedAgent())
	}
}

func TestCreateSessionModel_AgentCyclesWithUpDown(t *testing.T) {
	agents := []string{"claude", "codex", "cursor"}
	m := newCreateSessionModel("", nil, agents, "claude")
	m.nameInput.SetValue("braw-session")
	m = updateModel(m, keyPress("tab"))
	m = updateModel(m, keyPress("tab"))
	if m.focus != createFieldAgent {
		t.Fatalf("expected agent focus, got %d", m.focus)
	}

	m = updateModel(m, keyPress("down"))
	if m.selectedAgent() != "codex" {
		t.Errorf("expected codex after down, got %q", m.selectedAgent())
	}
	m = updateModel(m, keyPress("up"))
	if m.selectedAgent() != "claude" {
		t.Errorf("expected claude after up, got %q", m.selectedAgent())
	}
	m = updateModel(m, keyPress("up"))
	if m.selectedAgent() != "cursor" {
		t.Errorf("expected wrap to cursor after up from first, got %q", m.selectedAgent())
	}
}

func TestCreateSessionModel_ShiftTabFromAgentToRepo(t *testing.T) {
	agents := []string{"claude", "codex"}
	m := newCreateSessionModel("/tmp/repo", nil, agents, "claude")
	m.nameInput.SetValue("braw-session")
	m = updateModel(m, keyPress("tab"))   // name -> repo
	m = updateModel(m, keyPress("enter")) // repo (non-empty) -> agent
	if m.focus != createFieldAgent {
		t.Fatalf("expected agent focus, got %d", m.focus)
	}

	m = updateModel(m, keyPress("shift+tab"))
	if m.focus != createFieldRepo {
		t.Errorf("expected shift+tab from agent to return to repo, got %d", m.focus)
	}
	m = updateModel(m, keyPress("shift+tab"))
	if m.focus != createFieldName {
		t.Errorf("expected shift+tab from repo to return to name, got %d", m.focus)
	}
}

func TestCreateSessionModel_EnterOnRepoAdvancesToAgent(t *testing.T) {
	agents := []string{"claude", "codex"}
	m := newCreateSessionModel("/tmp/repo", nil, agents, "claude")
	m.nameInput.SetValue("braw-session")
	m = updateModel(m, keyPress("tab"))

	m = updateModel(m, keyPress("enter"))
	if m.done {
		t.Error("enter on repo should advance to agent field, not submit, when agents exist")
	}
	if m.focus != createFieldAgent {
		t.Errorf("expected focus on agent field, got %d", m.focus)
	}
}

func TestCreateSessionModel_EnterOnAgentSubmits(t *testing.T) {
	agents := []string{"claude", "codex"}
	m := newCreateSessionModel("/tmp/repo", nil, agents, "claude")
	m.nameInput.SetValue("braw-session")
	m = updateModel(m, keyPress("tab"))
	m = updateModel(m, keyPress("enter"))
	m = updateModel(m, keyPress("right"))

	m = updateModel(m, keyPress("enter"))
	if !m.done {
		t.Error("expected done=true after enter on agent field with valid inputs")
	}
	if m.selectedAgent() != "codex" {
		t.Errorf("expected codex selected at submit, got %q", m.selectedAgent())
	}
}

func TestCreateSessionModel_EnterOnEmptyRepoWithAgentsStaysOnRepo(t *testing.T) {
	agents := []string{"claude", "codex"}
	m := newCreateSessionModel("", nil, agents, "claude")
	m.nameInput.SetValue("braw-session")
	m = updateModel(m, keyPress("tab"))
	if m.focus != createFieldRepo {
		t.Fatalf("expected repo focus, got %d", m.focus)
	}

	m = updateModel(m, keyPress("enter"))
	if m.done {
		t.Error("enter on empty repo should not submit")
	}
	if m.focus != createFieldRepo {
		t.Errorf("enter on empty repo should keep focus on repo, not advance to agent; got %d", m.focus)
	}
}

func TestCreateSessionModel_NoAgentsEnterOnRepoSubmits(t *testing.T) {
	m := newCreateSessionModel("/tmp/repo", nil, nil, "")
	m.nameInput.SetValue("braw-session")
	m = updateModel(m, keyPress("tab"))

	m = updateModel(m, keyPress("enter"))
	if !m.done {
		t.Error("with no agents, enter on repo should submit directly")
	}
	if m.selectedAgent() != "" {
		t.Errorf("expected empty agent when none configured, got %q", m.selectedAgent())
	}
}

func TestCreateSessionModel_WindowSizeUpdates(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	m = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 || m.height != 40 {
		t.Errorf("expected 120x40, got %dx%d", m.width, m.height)
	}
}
