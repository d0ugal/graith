package client

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type nameInputModel struct {
	input  textinput.Model
	title  string
	done   bool
	width  int
	height int
}

func newNameInputModel(title string) nameInputModel {
	ti := textinput.New()
	ti.Placeholder = "session-name"
	ti.Focus()
	ti.CharLimit = 64
	ti.SetWidth(40)

	return nameInputModel{
		input: ti,
		title: title,
	}
}

func (m nameInputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m nameInputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter":
			if strings.TrimSpace(m.input.Value()) != "" {
				m.done = true
				return m, tea.Quit
			}
			return m, nil
		case "esc", "ctrl+c":
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m nameInputModel) View() tea.View {
	w := m.width
	h := m.height
	if w == 0 || h == 0 {
		return tea.NewView("")
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPurple)
	dim := lipgloss.NewStyle().Foreground(colorFaint)

	var content strings.Builder
	content.WriteString(titleStyle.Render(m.title))
	content.WriteString("\n\n")
	content.WriteString(m.input.View())
	content.WriteString("\n\n")
	content.WriteString(dim.Render("enter confirm  esc cancel"))

	panel := lipgloss.NewStyle().
		Background(colorPanel).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorFaint).
		Padding(1, 2).
		Render(content.String())

	panelLines := strings.Split(panel, "\n")
	panelH := len(panelLines)
	panelW := 0
	for _, pl := range panelLines {
		if lw := lipgloss.Width(pl); lw > panelW {
			panelW = lw
		}
	}

	offsetY := (h - panelH) / 2
	offsetX := (w - panelW) / 2
	if offsetY < 0 {
		offsetY = 0
	}
	if offsetX < 0 {
		offsetX = 0
	}

	bgLines := make([]string, h)
	for i := range bgLines {
		bgLines[i] = strings.Repeat(" ", w)
	}

	leftPad := strings.Repeat(" ", offsetX)
	for i, pl := range panelLines {
		row := offsetY + i
		if row >= 0 && row < h {
			line := leftPad + pl
			if vis := lipgloss.Width(line); vis < w {
				line += strings.Repeat(" ", w-vis)
			}
			bgLines[row] = line
		}
	}

	v := tea.NewView(strings.Join(bgLines, "\n"))
	v.AltScreen = true
	return v
}

// RunNameInput launches a bubbletea prompt asking for a session name.
// Returns the entered name, or "" if the user cancelled.
func RunNameInput(title string) string {
	m := newNameInputModel(title)
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		return ""
	}
	result, ok := final.(nameInputModel)
	if !ok || !result.done {
		return ""
	}
	return strings.TrimSpace(result.input.Value())
}
