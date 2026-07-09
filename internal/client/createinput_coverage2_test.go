package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

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
