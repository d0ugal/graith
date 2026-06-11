package client

import (
	"encoding/json"
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
	panelWidth := max(0, min(80, w-4))
	contentWidth := max(0, panelWidth-4)

	var panelContent strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPurple)
	panelContent.WriteString(titleStyle.Render(fmt.Sprintf("Pending Approvals (%d)", len(m.approvals))))
	panelContent.WriteString("\n")

	if len(m.approvals) == 0 {
		panelContent.WriteString(dim.Render("  No pending approvals"))
		panelContent.WriteString("\n")
	} else {
		for i, a := range m.approvals {
			selected := i == m.cursor
			prefix := "  "
			if selected {
				prefix = "> "
			}

			age := ""
			if t, err := time.Parse(time.RFC3339, a.RequestedAt); err == nil {
				age = ShortDuration(time.Since(t))
			}

			toolSummary := formatToolSummary(a.ToolName, a.ToolInput)

			line := fmt.Sprintf("%s%s  %s  %s  %s",
				prefix,
				lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render("⚠"),
				lipgloss.NewStyle().Bold(true).Render(a.SessionName),
				lipgloss.NewStyle().Foreground(colorBlue).Render(toolSummary),
				dim.Render(age))

			if selected {
				line = lipgloss.NewStyle().Bold(true).Render(line)
			}

			panelContent.WriteString(line)
			panelContent.WriteString("\n")
		}

		panelContent.WriteString("\n")
		selected := m.approvals[m.cursor]
		panelContent.WriteString(formatToolDetail(selected, contentWidth))
	}

	helpStyle := lipgloss.NewStyle().Foreground(colorFaint)
	panelContent.WriteString("\n")
	panelContent.WriteString(helpStyle.Render("y allow  n deny  a allow-all  q cancel"))

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

func formatToolSummary(toolName, toolInput string) string {
	if toolInput == "" {
		return toolName
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(toolInput), &parsed) != nil {
		return toolName
	}

	switch toolName {
	case "Bash":
		if cmd, ok := parsed["command"].(string); ok {
			short := firstLine(cmd)
			if len(short) > 50 {
				short = short[:47] + "..."
			}
			return fmt.Sprintf("Bash: %s", short)
		}
	case "Write":
		if fp, ok := parsed["file_path"].(string); ok {
			return fmt.Sprintf("Write: %s", shortPath(fp))
		}
	case "Edit":
		if fp, ok := parsed["file_path"].(string); ok {
			return fmt.Sprintf("Edit: %s", shortPath(fp))
		}
	case "Read":
		if fp, ok := parsed["file_path"].(string); ok {
			return fmt.Sprintf("Read: %s", shortPath(fp))
		}
	case "Skill":
		if skill, ok := parsed["skill"].(string); ok {
			return fmt.Sprintf("Skill: %s", skill)
		}
	case "Agent":
		if desc, ok := parsed["description"].(string); ok {
			return fmt.Sprintf("Agent: %s", desc)
		}
	}
	return toolName
}

func formatToolDetail(a protocol.ApprovalInfo, maxWidth int) string {
	dim := lipgloss.NewStyle().Foreground(colorDim)
	label := lipgloss.NewStyle().Foreground(colorBlue).Bold(true)
	code := lipgloss.NewStyle().Foreground(lipgloss.Color("#d0d0d0"))

	var b strings.Builder

	b.WriteString(label.Render("Session") + "  " + a.SessionName)
	if a.Agent != "" {
		b.WriteString(dim.Render(" (" + a.Agent + ")"))
	}
	b.WriteString("\n")

	b.WriteString(label.Render("Tool   ") + "  " + a.ToolName)
	b.WriteString("\n")

	if a.ToolInput == "" {
		return b.String()
	}

	var parsed map[string]any
	if json.Unmarshal([]byte(a.ToolInput), &parsed) != nil {
		b.WriteString(label.Render("Input  ") + "  " + truncate(a.ToolInput, maxWidth))
		b.WriteString("\n")
		return b.String()
	}

	switch a.ToolName {
	case "Bash":
		if cmd, ok := parsed["command"].(string); ok {
			b.WriteString("\n")
			for _, line := range wrapLines(cmd, maxWidth) {
				b.WriteString("  " + code.Render(line) + "\n")
			}
		}
	case "Write":
		if fp, ok := parsed["file_path"].(string); ok {
			b.WriteString(label.Render("File   ") + "  " + fp + "\n")
		}
		if content, ok := parsed["content"].(string); ok {
			b.WriteString("\n")
			lines := strings.Split(content, "\n")
			shown := min(10, len(lines))
			for _, line := range lines[:shown] {
				b.WriteString("  " + code.Render(truncate(line, maxWidth-2)) + "\n")
			}
			if len(lines) > shown {
				b.WriteString(dim.Render(fmt.Sprintf("  ... +%d more lines", len(lines)-shown)) + "\n")
			}
		}
	case "Edit":
		if fp, ok := parsed["file_path"].(string); ok {
			b.WriteString(label.Render("File   ") + "  " + fp + "\n")
		}
		if old, ok := parsed["old_string"].(string); ok {
			b.WriteString("\n" + dim.Render("  old:") + "\n")
			for _, line := range wrapLines(truncateBlock(old, 5), maxWidth-2) {
				b.WriteString("  " + lipgloss.NewStyle().Foreground(colorRed).Render(line) + "\n")
			}
		}
		if newS, ok := parsed["new_string"].(string); ok {
			b.WriteString(dim.Render("  new:") + "\n")
			for _, line := range wrapLines(truncateBlock(newS, 5), maxWidth-2) {
				b.WriteString("  " + lipgloss.NewStyle().Foreground(colorGreen).Render(line) + "\n")
			}
		}
	case "Skill":
		if skill, ok := parsed["skill"].(string); ok {
			b.WriteString(label.Render("Skill  ") + "  " + code.Render(skill) + "\n")
		}
		if args, ok := parsed["args"].(string); ok && args != "" {
			b.WriteString("\n")
			for _, line := range wrapLines(args, maxWidth-2) {
				b.WriteString("  " + code.Render(line) + "\n")
			}
		}
	case "Agent":
		if desc, ok := parsed["description"].(string); ok {
			b.WriteString(label.Render("Desc   ") + "  " + code.Render(desc) + "\n")
		}
		if prompt, ok := parsed["prompt"].(string); ok {
			short := truncateBlock(prompt, 5)
			b.WriteString("\n")
			for _, line := range wrapLines(short, maxWidth-2) {
				b.WriteString("  " + code.Render(line) + "\n")
			}
		}
	default:
		b.WriteString("\n")
		for key, val := range parsed {
			valStr := fmt.Sprintf("%v", val)
			if s, ok := val.(string); ok {
				valStr = s
			}
			valStr = strings.ReplaceAll(valStr, "\n", " ")
			b.WriteString("  " + dim.Render(key+":") + " " + code.Render(truncate(valStr, maxWidth-len(key)-4)) + "\n")
		}
	}

	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func shortPath(fp string) string {
	parts := strings.Split(fp, "/")
	if len(parts) <= 3 {
		return fp
	}
	return ".../" + strings.Join(parts[len(parts)-3:], "/")
}

func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen < 4 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func truncateBlock(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n... +%d more lines", len(lines)-maxLines)
}

func wrapLines(s string, maxWidth int) []string {
	if maxWidth <= 0 {
		maxWidth = 80
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if len(line) <= maxWidth {
			out = append(out, line)
			continue
		}
		words := strings.Fields(line)
		current := ""
		for _, word := range words {
			switch {
			case current == "":
				current = word
			case len(current)+1+len(word) <= maxWidth:
				current += " " + word
			default:
				out = append(out, current)
				current = word
			}
			for len(current) > maxWidth {
				out = append(out, current[:maxWidth])
				current = current[maxWidth:]
			}
		}
		if current != "" {
			out = append(out, current)
		}
	}
	return out
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
