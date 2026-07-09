package client

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// cleanScrollback turns raw PTY scrollback bytes into plain, scroll-friendly
// text. It strips ANSI escape sequences (cursor addressing, colours) so an
// agent TUI's redraws don't corrupt the pager, collapses carriage-return
// overwrites (progress bars/spinners rewrite a line in place) to the final
// text, and drops trailing blank lines. The result is line-oriented history
// suitable for a viewport.
func cleanScrollback(raw string) string {
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
}

func newScrollViewModel(title, content string) scrollViewModel {
	return scrollViewModel{title: title, content: content}
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
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "g", "home":
			m.viewport.GotoTop()
			return m, nil
		case "G", "end":
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
	header := titleStyle.Render(m.title)
	footer := dim.Render(fmt.Sprintf("↑/↓ scroll · pgup/pgdn page · g/G top/bottom · q quit · %d%%", pct))

	v := tea.NewView(header + "\n" + m.viewport.View() + "\n" + footer)
	v.AltScreen = true

	return v
}

// RunScrollView launches a full-screen pager over the given scrollback content
// and blocks until the user quits. An empty content shows a placeholder so the
// pager still opens cleanly rather than flashing an empty screen.
func RunScrollView(title, content string) {
	if strings.TrimSpace(content) == "" {
		content = "(no scrollback captured for this session)"
	}

	p := tea.NewProgram(newScrollViewModel(title, content))
	_, _ = p.Run()
}
