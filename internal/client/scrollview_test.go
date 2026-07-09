package client

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func updateScrollModel(m scrollViewModel, msg tea.Msg) scrollViewModel {
	result, _ := m.Update(msg)

	return result.(scrollViewModel)
}

func TestCleanScrollback(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "braw\nbonnie", "braw\nbonnie"},
		{
			"strips ansi colours",
			"\x1b[31mdreich\x1b[0m\n\x1b[1;32mbraw\x1b[0m",
			"dreich\nbraw",
		},
		{
			"strips cursor addressing",
			"\x1b[2J\x1b[Hbothy\x1b[10;5Hglen",
			"bothyglen",
		},
		{
			"collapses carriage-return overwrite",
			"loading 10%\rloading 50%\rloading 100%",
			"loading 100%",
		},
		{
			"trims trailing whitespace per line",
			"canny   \nken\t",
			"canny\nken",
		},
		{
			"drops trailing blank lines",
			"whin\n\n\n",
			"whin",
		},
		{"empty", "", ""},
		{"only blanks", "\n\n", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanScrollback(tc.in); got != tc.want {
				t.Errorf("cleanScrollback(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestScrollViewModel_ViewEmptyBeforeReady(t *testing.T) {
	m := newScrollViewModel("Scrollback — braw", "some history")
	if got := m.View().Content; got != "" {
		t.Errorf("View before sizing = %q, want empty", got)
	}
}

func TestScrollViewModel_WindowSizeInitializesViewport(t *testing.T) {
	m := newScrollViewModel("Scrollback — braw", "line one\nline two\nline three")
	m = updateScrollModel(m, tea.WindowSizeMsg{Width: 80, Height: 24})

	if !m.ready {
		t.Fatal("model should be ready after a window size message")
	}

	if m.viewport.Height() != 22 {
		t.Errorf("viewport height = %d, want 22 (24 - header - footer)", m.viewport.Height())
	}

	if m.viewport.Width() != 80 {
		t.Errorf("viewport width = %d, want 80", m.viewport.Width())
	}
}

// TestScrollViewModel_TinyWindowClampsHeight guards the height floor so a
// 1-row terminal doesn't produce a zero/negative viewport height.
func TestScrollViewModel_TinyWindowClampsHeight(t *testing.T) {
	m := newScrollViewModel("kirk", "a\nb")
	m = updateScrollModel(m, tea.WindowSizeMsg{Width: 40, Height: 1})

	if m.viewport.Height() < 1 {
		t.Errorf("viewport height = %d, want >= 1", m.viewport.Height())
	}
}

func TestScrollViewModel_QuitKeys(t *testing.T) {
	for _, key := range []string{"q", "esc", "ctrl+c"} {
		t.Run(key, func(t *testing.T) {
			m := newScrollViewModel("kirk", "content")
			m = updateScrollModel(m, tea.WindowSizeMsg{Width: 80, Height: 24})

			var msg tea.Msg

			switch key {
			case "esc":
				msg = tea.KeyPressMsg{Code: tea.KeyEscape}
			case "ctrl+c":
				msg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
			default:
				msg = tea.KeyPressMsg{Code: 'q', Text: "q"}
			}

			_, cmd := m.Update(msg)
			if cmd == nil {
				t.Fatalf("%s should return a quit command", key)
			}

			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Errorf("%s command did not produce a QuitMsg", key)
			}
		})
	}
}

func TestScrollViewModel_GotoTopBottom(t *testing.T) {
	content := strings.Repeat("bothy\n", 200)

	m := newScrollViewModel("kirk", content)
	m = updateScrollModel(m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Initialised at the bottom (freshest output).
	if !m.viewport.AtBottom() {
		t.Error("viewport should start at the bottom")
	}

	m = updateScrollModel(m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if !m.viewport.AtTop() {
		t.Error("g should scroll to the top")
	}

	m = updateScrollModel(m, tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !m.viewport.AtBottom() {
		t.Error("G should scroll to the bottom")
	}
}

func TestScrollViewModel_ViewRendersTitle(t *testing.T) {
	m := newScrollViewModel("Scrollback — bonnie", "content line")
	m = updateScrollModel(m, tea.WindowSizeMsg{Width: 80, Height: 24})

	out := m.View().Content
	if !strings.Contains(out, "Scrollback") {
		t.Errorf("rendered view should contain the title, got %q", out)
	}

	if !strings.Contains(out, "quit") {
		t.Errorf("rendered view should contain the footer help, got %q", out)
	}
}
