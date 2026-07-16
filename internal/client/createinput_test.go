package client

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
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

func updateModel(m *createSessionModel, msg tea.Msg) *createSessionModel {
	result, _ := m.Update(msg)
	return result.(*createSessionModel)
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path string, data []byte, perm os.FileMode) {
	t.Helper()

	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDiscoverRepos_AllowedPaths(t *testing.T) {
	base := t.TempDir()

	repo1 := filepath.Join(base, "repo1")
	repo2 := filepath.Join(base, "repo2")
	notARepo := filepath.Join(base, "not-a-repo")
	mkdirAll(t, filepath.Join(repo1, ".git"))
	mkdirAll(t, filepath.Join(repo2, ".git"))
	mkdirAll(t, notARepo)

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
	mkdirAll(t, filepath.Join(base, ".git"))

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
	mkdirAll(t, repo)
	writeFile(t, filepath.Join(repo, ".git"), []byte("gitdir: /some/other/path"), 0o600)

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
	mkdirAll(t, filepath.Join(repo, ".git"))

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
	mkdirAll(t, filepath.Join(repo, ".git"))

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
		mkdirAll(t, filepath.Join(base, name, ".git"))
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
	m = updateModel(m, keyPress("tab")) // name -> repo

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

func TestCreateSessionModel_InitReturnsBlink(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	if m.Init() == nil {
		t.Error("Init should return a (blink) command, got nil")
	}
}

func TestExpandPath_TildeOnly(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}

	if got := expandPath("~"); got != filepath.Clean(home) {
		t.Errorf("expandPath(\"~\") = %q, want %q", got, filepath.Clean(home))
	}
}

func TestExpandPath_TildeSlashPrefix(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}

	want := filepath.Join(home, "Code", "croft")
	if got := expandPath("~/Code/croft"); got != want {
		t.Errorf("expandPath(\"~/Code/croft\") = %q, want %q", got, want)
	}
}

func TestExpandPath_RelativeBecomesAbsolute(t *testing.T) {
	got := expandPath("glen/wynd")
	if !filepath.IsAbs(got) {
		t.Errorf("expandPath of a relative path should be absolute, got %q", got)
	}

	if !strings.HasSuffix(got, filepath.Join("glen", "wynd")) {
		t.Errorf("expandPath result %q should end with glen/wynd", got)
	}
}

func TestTrySubmit_BlockedWhenFieldsEmpty(t *testing.T) {
	// Missing repo.
	m := newCreateSessionModel("", nil, nil, "")
	m.nameInput.SetValue("braw")

	if cmd := m.trySubmit(); cmd != nil {
		t.Error("trySubmit should return nil when repo is empty")
	}

	if m.done {
		t.Error("trySubmit should not mark done when repo is empty")
	}

	// Missing name.
	m2 := newCreateSessionModel("", nil, nil, "")
	m2.repoInput.SetValue("/tmp/croft")

	if cmd := m2.trySubmit(); cmd != nil {
		t.Error("trySubmit should return nil when name is empty")
	}

	if m2.done {
		t.Error("trySubmit should not mark done when name is empty")
	}
}

func TestTrySubmit_SucceedsWhenBothFilled(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	m.nameInput.SetValue("braw")
	m.repoInput.SetValue("/tmp/croft")

	cmd := m.trySubmit()
	if cmd == nil {
		t.Error("trySubmit should return a quit command when both fields are filled")
	}

	if !m.done {
		t.Error("trySubmit should mark the model done when both fields are filled")
	}
}

func TestUpdateFiltered_ClampsDropdownIndexWhenResultsShrink(t *testing.T) {
	repos := []RepoSuggestion{
		{Name: "braw", Path: "/tmp/braw"},
		{Name: "bonnie", Path: "/tmp/bonnie"},
		{Name: "canny", Path: "/tmp/canny"},
	}
	m := newCreateSessionModel("", repos, nil, "")

	// Point at the last suggestion, then filter down to a single match: the
	// index must be clamped to the new (smaller) result set.
	m.dropdownIdx = 2
	m.repoInput.SetValue("bonn")
	m.updateFiltered()

	if len(m.filtered) != 1 {
		t.Fatalf("expected 1 filtered repo, got %d", len(m.filtered))
	}

	if m.dropdownIdx != 0 {
		t.Errorf("dropdownIdx = %d, want 0 (clamped to last valid index)", m.dropdownIdx)
	}
}

func TestUpdateFiltered_NoMatchesResetsIndex(t *testing.T) {
	repos := []RepoSuggestion{{Name: "braw", Path: "/tmp/braw"}}
	m := newCreateSessionModel("", repos, nil, "")

	m.dropdownIdx = 0
	m.repoInput.SetValue("nae-such-repo")
	m.updateFiltered()

	if len(m.filtered) != 0 {
		t.Fatalf("expected no filtered repos, got %d", len(m.filtered))
	}

	if m.dropdownIdx != -1 {
		t.Errorf("dropdownIdx = %d, want -1 when there are no matches", m.dropdownIdx)
	}
}

func TestCreateSessionModel_ViewZeroSizeIsEmpty(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	if got := m.View().Content; got != "" {
		t.Errorf("View with zero dimensions should be empty, got %q", got)
	}
}

func TestCreateSessionModel_ViewRendersFieldsAndAgents(t *testing.T) {
	repos := []RepoSuggestion{{Name: "croft", Path: "/tmp/croft"}}
	m := newCreateSessionModel("/tmp/croft", repos, []string{"claude", "codex"}, "codex")
	m = updateModel(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	out := m.View().Content
	for _, want := range []string{"Create Session", "Name:", "Repo:", "Agent:", "claude", "codex"} {
		if !strings.Contains(out, want) {
			t.Errorf("View should contain %q, got:\n%s", want, out)
		}
	}

	if !m.View().AltScreen {
		t.Error("View should enable AltScreen")
	}

	if lines := strings.Count(out, "\n") + 1; lines != 30 {
		t.Errorf("rendered frame has %d lines, want 30 (full height)", lines)
	}
}

func TestCreateSessionModel_ViewAgentHintWithoutAgents(t *testing.T) {
	m := newCreateSessionModel("", nil, nil, "")
	m = updateModel(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	out := m.View().Content
	// With no agents the hint should not mention agent cycling.
	if strings.Contains(out, "cycle agent") || strings.Contains(out, "←→ agent") {
		t.Errorf("hint should omit agent controls when there are no agents, got:\n%s", out)
	}
}

func TestCreateSessionModel_ViewDropdownScrollIndicators(t *testing.T) {
	// Enough repos that the 8-row dropdown window must scroll, exposing both the
	// "↑ N more" and "↓ N more" indicators.
	var repos []RepoSuggestion

	for i := range 20 {
		name := fmt.Sprintf("clachan-%02d", i)
		repos = append(repos, RepoSuggestion{Name: name, Path: "/tmp/" + name})
	}

	m := newCreateSessionModel("", repos, nil, "")
	m = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Fill the name, then advance to the repo field so the dropdown opens.
	m.nameInput.SetValue("braw")
	m = updateModel(m, keyPress("enter"))
	m.showDropdown = true
	m.dropdownIdx = 15

	out := m.View().Content
	if !strings.Contains(out, "more") {
		t.Errorf("scrolled dropdown should show a 'more' indicator, got:\n%s", out)
	}

	// The focused row should be marked with the selection prefix.
	if !strings.Contains(out, "▸") {
		t.Errorf("focused dropdown row should be marked with ▸, got:\n%s", out)
	}
}
