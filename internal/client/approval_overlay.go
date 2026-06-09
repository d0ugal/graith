package client

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

type ApprovalResult struct {
	RequestID string
	Decision  string
	Reason    string
}

type approvalModel struct {
	approvals []protocol.ApprovalInfo
	results   []ApprovalResult
	cursor    int
	width     int
	height    int
}

func newApprovalModel(approvals []protocol.ApprovalInfo) approvalModel {
	return approvalModel{
		approvals: approvals,
	}
}

func (m approvalModel) Init() tea.Cmd {
	return nil
}

func (m approvalModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc":
			return m, tea.Quit

		case "j", "down":
			if m.cursor < len(m.approvals)-1 {
				m.cursor++
			}
			return m, nil

		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

		case "y", "enter":
			if len(m.approvals) > 0 {
				m.results = append(m.results, ApprovalResult{
					RequestID: m.approvals[m.cursor].RequestID,
					Decision:  "allow",
				})
				m.approvals = append(m.approvals[:m.cursor], m.approvals[m.cursor+1:]...)
				if m.cursor >= len(m.approvals) && m.cursor > 0 {
					m.cursor--
				}
				if len(m.approvals) == 0 {
					return m, tea.Quit
				}
			}
			return m, nil

		case "n", "x":
			if len(m.approvals) > 0 {
				m.results = append(m.results, ApprovalResult{
					RequestID: m.approvals[m.cursor].RequestID,
					Decision:  "block",
					Reason:    "denied by user",
				})
				m.approvals = append(m.approvals[:m.cursor], m.approvals[m.cursor+1:]...)
				if m.cursor >= len(m.approvals) && m.cursor > 0 {
					m.cursor--
				}
				if len(m.approvals) == 0 {
					return m, tea.Quit
				}
			}
			return m, nil

		case "a":
			for _, a := range m.approvals {
				m.results = append(m.results, ApprovalResult{
					RequestID: a.RequestID,
					Decision:  "allow",
				})
			}
			m.approvals = nil
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m approvalModel) View() tea.View {
	w := m.width
	h := m.height
	if w == 0 || h == 0 {
		return tea.NewView("")
	}

	dim := lipgloss.NewStyle().Foreground(colorDim)

	var panelContent strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPurple)
	panelContent.WriteString(titleStyle.Render(fmt.Sprintf("Pending Approvals (%d)", len(m.approvals))))
	panelContent.WriteString("\n")

	if len(m.approvals) == 0 {
		panelContent.WriteString(dim.Render("  No pending approvals"))
		panelContent.WriteString("\n")
	} else {
		nameW := 0
		toolW := 0
		for _, a := range m.approvals {
			if n := len(a.SessionName); n > nameW {
				nameW = n
			}
			toolDisplay := formatToolDisplay(a.ToolName, a.ToolInput)
			if n := len(toolDisplay); n > toolW {
				toolW = n
			}
		}
		if nameW < 7 {
			nameW = 7
		}
		if toolW > 40 {
			toolW = 40
		}

		for i, a := range m.approvals {
			selected := i == m.cursor
			prefix := "  "
			if selected {
				prefix = "> "
			}

			indicator := lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render("⚠")
			name := pad(a.SessionName, nameW)
			toolDisplay := formatToolDisplay(a.ToolName, a.ToolInput)
			if len(toolDisplay) > toolW {
				toolDisplay = toolDisplay[:toolW-3] + "..."
			}
			tool := pad(toolDisplay, toolW)

			age := ""
			if t, err := time.Parse(time.RFC3339, a.RequestedAt); err == nil {
				age = ShortDuration(time.Since(t))
			}

			sep := dim.Render("  ")
			line := fmt.Sprintf("%s%s %s%s%s%s%s",
				prefix, indicator, name, sep,
				lipgloss.NewStyle().Foreground(colorBlue).Render(tool), sep,
				dim.Render(age))

			if selected {
				line = lipgloss.NewStyle().Bold(true).Render(line)
			}

			panelContent.WriteString(line)
			panelContent.WriteString("\n")
		}
	}

	helpStyle := lipgloss.NewStyle().Foreground(colorFaint)
	panelContent.WriteString("\n")
	panelContent.WriteString(helpStyle.Render("y allow  n deny  a allow-all  q cancel"))

	panelWidth := min(60, w-4)
	panel := lipgloss.NewStyle().
		Width(panelWidth).
		Background(colorPanel).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorFaint).
		Padding(0, 1).
		Render(panelContent.String())

	panelLines := strings.Split(panel, "\n")
	panelH := len(panelLines)
	panelRenderedW := 0
	for _, pl := range panelLines {
		if lw := lipgloss.Width(pl); lw > panelRenderedW {
			panelRenderedW = lw
		}
	}

	bgLines := make([]string, h)
	for i := range bgLines {
		bgLines[i] = strings.Repeat(" ", w)
	}

	offsetY := (h - panelH) / 2
	offsetX := (w - panelRenderedW) / 2
	if offsetY < 0 {
		offsetY = 0
	}
	if offsetX < 0 {
		offsetX = 0
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

func formatToolDisplay(toolName, toolInput string) string {
	if toolInput == "" {
		return toolName
	}
	input := toolInput
	if len(input) > 30 {
		input = input[:30] + "..."
	}
	input = strings.ReplaceAll(input, "\n", " ")
	return fmt.Sprintf("%s(%s)", toolName, input)
}

// RunApprovalOverlay launches the bubbletea approval overlay listing pending
// approvals. Returns the list of decisions made by the user. After each
// approve/deny the overlay stays open; it auto-closes when empty.
func RunApprovalOverlay(approvals []protocol.ApprovalInfo) []ApprovalResult {
	if len(approvals) == 0 {
		return nil
	}

	m := newApprovalModel(approvals)
	p := tea.NewProgram(m)

	final, err := p.Run()
	if err != nil {
		return nil
	}

	result, ok := final.(approvalModel)
	if !ok {
		return nil
	}

	return result.results
}
