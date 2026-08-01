package client

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
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
			// Cursor-row moves and screen clears become newlines so regions
			// drawn at different rows aren't concatenated onto one line.
			"cursor addressing becomes row breaks",
			"\x1b[2J\x1b[Hbothy\x1b[10;5Hglen",
			"bothy\nglen",
		},
		{
			"cursor next/prev line breaks rows",
			"braw\x1b[1Ebonnie\x1b[Fcanny",
			"braw\nbonnie\ncanny",
		},
		{
			"leading blank lines dropped",
			"\n\n\nwhin",
			"whin",
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

func TestFormatTerminalHistoryPreservesSoftWraps(t *testing.T) {
	history := protocol.TerminalHistoryMsg{
		Lines: []protocol.TerminalHistoryLineMsg{
			{Frame: "braw", Wrapped: true},
			{Frame: "canny"},
			{Frame: "dreich"},
			{Frame: " thrawn", WrapContinuation: true},
		},
	}

	got := FormatTerminalHistory(history)
	if got != "brawcanny\ndreich thrawn" {
		t.Fatalf("formatted history = %q, want soft-wrapped first two rows", got)
	}
}

func TestFormatTerminalHistoryEmpty(t *testing.T) {
	if got := FormatTerminalHistory(protocol.TerminalHistoryMsg{}); got != "" {
		t.Fatalf("empty history formatted as %q, want empty", got)
	}
}

func TestFormatTerminalScrollback(t *testing.T) {
	tests := map[string]struct {
		history  protocol.TerminalHistoryMsg
		snapshot protocol.ScreenSnapshotResponseMsg
		want     string
	}{
		"history and visible frame": {
			history: protocol.TerminalHistoryMsg{
				Lines: []protocol.TerminalHistoryLineMsg{
					{Frame: "scrolled off 1"},
					{Frame: "scrolled off 2"},
				},
			},
			snapshot: protocol.ScreenSnapshotResponseMsg{
				Frame: "visible screen\r\ncurrent prompt\r\n   \x1b[0m",
			},
			want: "scrolled off 1\nscrolled off 2\nvisible screen\ncurrent prompt\x1b[0m",
		},
		"soft wrap crosses history and frame boundary": {
			history: protocol.TerminalHistoryMsg{
				Lines: []protocol.TerminalHistoryLineMsg{
					{Frame: "wrapped ", Wrapped: true},
				},
			},
			snapshot: protocol.ScreenSnapshotResponseMsg{
				Frame: "continuation\r\n\x1b[0m",
			},
			want: "wrapped continuation\x1b[0m",
		},
		"empty history preserves raw log fallback": {
			snapshot: protocol.ScreenSnapshotResponseMsg{
				Frame: "visible screen\r\n\x1b[0m",
			},
			want: "",
		},
		"empty frame returns history": {
			history: protocol.TerminalHistoryMsg{
				Lines: []protocol.TerminalHistoryLineMsg{
					{Frame: "braw"},
				},
			},
			want: "braw",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := FormatTerminalScrollback(test.history, test.snapshot)
			if got != test.want {
				t.Fatalf("FormatTerminalScrollback() = %q, want %q", got, test.want)
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

func TestScrollViewModel_MouseOpenedWheelDownAtBottomQuits(t *testing.T) {
	content := strings.Repeat("bothy\n", 40)

	m := newScrollViewModel("kirk", content)
	m.mode = scrollViewModeMouse
	m = updateScrollModel(m, tea.WindowSizeMsg{Width: 80, Height: 10})
	m.viewport.ScrollUp(1)

	if m.viewport.AtBottom() {
		t.Fatal("test setup should leave viewport above the bottom")
	}

	result, cmd := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	got := result.(scrollViewModel)

	if !got.viewport.AtBottom() {
		t.Fatal("wheel down should scroll to the bottom")
	}

	if cmd == nil {
		t.Fatal("wheel down at bottom should quit mouse-opened scroll mode")
	}

	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("wheel-down command = %T, want tea.QuitMsg", msg)
	}
}

func TestScrollViewModel_MouseOpenedWheelDownAboveBottomStaysOpen(t *testing.T) {
	content := strings.Repeat("bothy\n", 80)

	m := newScrollViewModel("kirk", content)
	m.mode = scrollViewModeMouse
	m = updateScrollModel(m, tea.WindowSizeMsg{Width: 80, Height: 10})
	m.viewport.GotoTop()
	before := m.viewport.YOffset()

	result, cmd := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	got := result.(scrollViewModel)

	if got.viewport.AtBottom() {
		t.Fatal("wheel down from the top should not reach the bottom")
	}

	if got.viewport.YOffset() <= before {
		t.Fatalf("wheel down y offset = %d, want greater than %d", got.viewport.YOffset(), before)
	}

	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("mouse-opened scroll mode should not quit before reaching bottom")
		}
	}
}

func TestScrollViewModel_MouseOpenedShiftWheelDownAtBottomStaysOpen(t *testing.T) {
	content := strings.Repeat("bothy\n", 40)

	m := newScrollViewModel("kirk", content)
	m.mode = scrollViewModeMouse
	m = updateScrollModel(m, tea.WindowSizeMsg{Width: 80, Height: 10})

	if !m.viewport.AtBottom() {
		t.Fatal("test setup should start at the bottom")
	}

	result, cmd := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, Mod: tea.ModShift})
	got := result.(scrollViewModel)

	if !got.viewport.AtBottom() {
		t.Fatal("shift wheel down should not move vertically from bottom")
	}

	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("shift wheel down should not quit mouse-opened scroll mode")
		}
	}
}

func TestScrollViewModel_KeyboardOpenedWheelDownAtBottomStaysOpen(t *testing.T) {
	content := strings.Repeat("bothy\n", 40)

	m := newScrollViewModel("kirk", content)
	m = updateScrollModel(m, tea.WindowSizeMsg{Width: 80, Height: 10})
	m.viewport.ScrollUp(1)

	result, cmd := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	got := result.(scrollViewModel)

	if !got.viewport.AtBottom() {
		t.Fatal("wheel down should still scroll keyboard-opened mode to the bottom")
	}

	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("keyboard-opened scroll mode should not quit on wheel down at bottom")
		}
	}
}

func TestScrollViewModel_MouseOpenedKeyboardBottomStaysOpen(t *testing.T) {
	content := strings.Repeat("bothy\n", 40)

	m := newScrollViewModel("kirk", content)
	m.mode = scrollViewModeMouse
	m = updateScrollModel(m, tea.WindowSizeMsg{Width: 80, Height: 10})
	m = updateScrollModel(m, tea.KeyPressMsg{Code: 'g', Text: "g"})

	if !m.viewport.AtTop() {
		t.Fatal("test setup should leave viewport at the top")
	}

	result, cmd := m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	got := result.(scrollViewModel)

	if !got.viewport.AtBottom() {
		t.Fatal("G should move mouse-opened scroll mode to the bottom")
	}

	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("keyboard bottom command should not quit mouse-opened scroll mode")
		}
	}
}

// TestScrollViewModel_HeaderFooterFitWidth guards against the header/footer
// wrapping on a narrow terminal (or with a long title), which would push the
// viewport past the terminal height and clobber content.
func TestScrollViewModel_HeaderFooterFitWidth(t *testing.T) {
	longTitle := "Scrollback — " + strings.Repeat("verra-lang-name-", 8)

	m := newScrollViewModel(longTitle, "content")
	m = updateScrollModel(m, tea.WindowSizeMsg{Width: 30, Height: 24})

	lines := strings.Split(m.View().Content, "\n")
	if len(lines) != 24 {
		t.Fatalf("view has %d lines, want 24 (must equal terminal height)", len(lines))
	}

	// Header (line 0) and footer (last line) must not exceed the width once
	// ANSI styling is discounted.
	if w := ansi.StringWidth(lines[0]); w > 30 {
		t.Errorf("header visible width = %d, want <= 30", w)
	}

	if w := ansi.StringWidth(lines[len(lines)-1]); w > 30 {
		t.Errorf("footer visible width = %d, want <= 30", w)
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

func TestScrollViewModel_MouseModeOnlyForMouseOpenedPager(t *testing.T) {
	content := strings.Repeat("bothy\n", 40)

	keyboard := newScrollViewModel("Scrollback — bonnie", content)
	keyboard = updateScrollModel(keyboard, tea.WindowSizeMsg{Width: 80, Height: 24})

	if mode := keyboard.View().MouseMode; mode != tea.MouseModeNone {
		t.Errorf("keyboard-opened view mouse mode = %v, want none", mode)
	}

	mouse := newScrollViewModel("Scrollback — bonnie", content)
	mouse.mode = scrollViewModeMouse
	mouse = updateScrollModel(mouse, tea.WindowSizeMsg{Width: 80, Height: 24})

	if mode := mouse.View().MouseMode; mode != tea.MouseModeCellMotion {
		t.Errorf("mouse-opened view mouse mode = %v, want cell-motion reporting", mode)
	}
}
