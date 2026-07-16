package client

import (
	"fmt"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// scrollbackRowBreakRE matches escape sequences that reposition the cursor to a
// different row or clear the screen: cursor position (CSI H / CSI f), cursor
// next/previous line (CSI E / CSI F), and erase-display (CSI J). These are
// turned into newlines by cleanScrollback so regions the TUI drew at different
// rows aren't concatenated onto one line once the escapes are stripped.
var scrollbackRowBreakRE = regexp.MustCompile(`\x1b\[[0-9;]*[HfEF]|\x1b\[[0-9]*J`)

// cleanScrollback turns raw PTY scrollback bytes into plain, scroll-friendly
// text. This is a best-effort, lossy transform: it converts cursor-row moves
// and screen clears to newlines, strips the remaining ANSI (colours, other
// escapes), and collapses carriage-return overwrites (progress bars/spinners
// rewriting a line in place) to the final text. It is clean and readable for
// line-oriented output (shell sessions, plain logs); a full-screen agent TUI
// that repaints via absolute cursor addressing on the alternate screen won't
// reconstruct exactly, but stays legible rather than a run-together escape
// soup. Leading and trailing blank lines are dropped.
func cleanScrollback(raw string) string {
	raw = scrollbackRowBreakRE.ReplaceAllString(raw, "\n")
	stripped := ansi.Strip(raw)
	lines := strings.Split(stripped, "\n")

	out := make([]string, 0, len(lines))
	for _, line := range lines {
		// A carriage return moves the cursor to column 0; text after the last
		// \r overwrites what came before, so keep only the final segment.
		if idx := strings.LastIndex(line, "\r"); idx >= 0 {
			line = line[idx+1:]
		}

		out = append(out, strings.TrimRight(line, " \t"))
	}

	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}

	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}

	return strings.Join(out, "\n")
}

// scrollViewModel is a read-only pager over a session's scrollback history. It
// wraps a bubbles viewport for the actual scrolling and adds a title header and
// a help/percent footer. It is entered from passthrough via ctrl+b + scroll_mode.
type scrollViewModel struct {
	viewport viewport.Model
	content  string
	title    string
	ready    bool
	width    int
	height   int
	keys     ScrollKeys
}

func newScrollViewModel(title, content string) scrollViewModel {
	return scrollViewModel{title: title, content: content, keys: DefaultScrollKeys()}
}

func (m scrollViewModel) Init() tea.Cmd { return nil }

func (m scrollViewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// One row each for the header and footer.
		vpHeight := msg.Height - 2
		if vpHeight < 1 {
			vpHeight = 1
		}

		if !m.ready {
			m.viewport = viewport.New(viewport.WithWidth(msg.Width), viewport.WithHeight(vpHeight))
			m.viewport.SetContent(m.content)
			m.viewport.GotoBottom()
			m.ready = true
		} else {
			m.viewport.SetWidth(msg.Width)
			m.viewport.SetHeight(vpHeight)
		}

		return m, nil
	case tea.KeyPressMsg:
		s := msg.String()

		switch {
		case matchKey(m.keys.Cancel, s):
			return m, tea.Quit
		case matchKey(m.keys.Top, s):
			m.viewport.GotoTop()
			return m, nil
		case matchKey(m.keys.Bottom, s):
			m.viewport.GotoBottom()
			return m, nil
		}
	}

	var cmd tea.Cmd

	m.viewport, cmd = m.viewport.Update(msg)

	return m, cmd
}

func (m scrollViewModel) View() tea.View {
	if m.width == 0 || m.height == 0 || !m.ready {
		return tea.NewView("")
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPurple)
	dim := lipgloss.NewStyle().Foreground(colorFaint)

	pct := int(m.viewport.ScrollPercent() * 100)

	// Truncate header/footer to the terminal width so a long title or a narrow
	// terminal can't wrap them onto a second row — that would push the viewport
	// (sized at height-2) past the bottom and clobber content.
	header := ansi.Truncate(titleStyle.Render(m.title), m.width, "")
	footer := ansi.Truncate(dim.Render(fmt.Sprintf("↑/↓ scroll · pgup/pgdn page · %s/%s top/bottom · %s quit · %d%%",
		primaryKey(m.keys.Top), primaryKey(m.keys.Bottom), primaryKey(m.keys.Cancel), pct)), m.width, "")

	v := tea.NewView(header + "\n" + m.viewport.View() + "\n" + footer)
	v.AltScreen = true

	return v
}

// RunScrollView launches a full-screen pager over the given scrollback content
// and blocks until the user quits. An empty content shows a placeholder so the
// pager still opens cleanly rather than flashing an empty screen.
func RunScrollView(title, content string, keys ScrollKeys) {
	if strings.TrimSpace(content) == "" {
		content = "(no scrollback captured for this session)"
	}

	m := newScrollViewModel(title, content)
	m.keys = keys
	p := tea.NewProgram(m)
	_, _ = p.Run()
}
