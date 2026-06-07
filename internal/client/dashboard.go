package client

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
)

type dashboardState int

const (
	dashStateNormal dashboardState = iota
	dashStateConfirmDelete
	dashStateConfirmStop
)

type tickMsg time.Time

type refreshMsg struct {
	sessions []protocol.SessionInfo
}

type DashboardResult struct {
	Action    string
	SessionID string
}

type DashboardModel struct {
	sessions []protocol.SessionInfo
	cursor   int
	offset   int
	width    int
	height   int
	state    dashboardState
	result   *DashboardResult
	refresh  func() []protocol.SessionInfo
}

func NewDashboardModel(sessions []protocol.SessionInfo, refresh func() []protocol.SessionInfo) DashboardModel {
	return DashboardModel{
		sessions: sessions,
		refresh:  refresh,
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m DashboardModel) doRefresh() tea.Cmd {
	refresh := m.refresh
	return func() tea.Msg {
		return refreshMsg{sessions: refresh()}
	}
}

func (m DashboardModel) Init() tea.Cmd {
	return tickCmd()
}

// visibleRows returns how many session rows fit in the viewport.
// Reserves lines for: header (2), column header (1), separator (1),
// confirmation prompt (2 when active), footer (2).
func (m DashboardModel) visibleRows() int {
	reserved := 6
	if m.state != dashStateNormal {
		reserved += 2
	}
	rows := m.height - reserved
	if rows < 1 {
		rows = 1
	}
	return rows
}

func (m *DashboardModel) scrollToCursor() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		return m, tea.Batch(tickCmd(), m.doRefresh())

	case refreshMsg:
		if msg.sessions == nil {
			return m, nil
		}
		selectedID := m.selectedSessionID()
		m.sessions = msg.sessions
		m.clampCursor()
		if selectedID != "" {
			for i, s := range m.sessions {
				if s.ID == selectedID {
					m.cursor = i
					break
				}
			}
		}
		m.scrollToCursor()
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.scrollToCursor()
		return m, nil

	case tea.KeyMsg:
		switch m.state {
		case dashStateConfirmDelete:
			switch msg.String() {
			case "y", "Y":
				if s := m.selectedSession(); s != nil {
					m.result = &DashboardResult{Action: "delete", SessionID: s.ID}
					return m, tea.Quit
				}
				m.state = dashStateNormal
			default:
				m.state = dashStateNormal
			}
			return m, nil

		case dashStateConfirmStop:
			switch msg.String() {
			case "y", "Y":
				if s := m.selectedSession(); s != nil {
					m.result = &DashboardResult{Action: "stop", SessionID: s.ID}
					return m, tea.Quit
				}
				m.state = dashStateNormal
			default:
				m.state = dashStateNormal
			}
			return m, nil

		case dashStateNormal:
			switch msg.String() {
			case "q", "ctrl+c":
				return m, tea.Quit
			case "j", "down":
				if m.cursor < len(m.sessions)-1 {
					m.cursor++
					m.scrollToCursor()
				}
				return m, nil
			case "k", "up":
				if m.cursor > 0 {
					m.cursor--
					m.scrollToCursor()
				}
				return m, nil
			case "enter", "a":
				if s := m.selectedSession(); s != nil {
					m.result = &DashboardResult{Action: "attach", SessionID: s.ID}
					return m, tea.Quit
				}
				return m, nil
			case "s":
				if s := m.selectedSession(); s != nil && s.Status == "running" {
					m.state = dashStateConfirmStop
				}
				return m, nil
			case "x", "d":
				if s := m.selectedSession(); s != nil {
					m.state = dashStateConfirmDelete
				}
				return m, nil
			case "r":
				if s := m.selectedSession(); s != nil && s.Status == "stopped" {
					m.result = &DashboardResult{Action: "resume", SessionID: s.ID}
					return m, tea.Quit
				}
				return m, nil
			}
		}
	}
	return m, nil
}

func (m DashboardModel) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7B61FF"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#888888"))
	selectedStyle := lipgloss.NewStyle().Bold(true)
	warningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f"))

	// Header
	title := titleStyle.Render("graith dashboard")
	countStr := dimStyle.Render(fmt.Sprintf(" (%d sessions)", len(m.sessions)))
	header := title + countStr
	b.WriteString(header)
	b.WriteString("\n\n")

	if len(m.sessions) == 0 {
		b.WriteString(dimStyle.Render("  No sessions. Create one with: gr new <name>"))
		b.WriteString("\n")
	} else {
		cols := m.computeDashCols()

		// Column headers
		headerLine := fmt.Sprintf("  %s  %s  %s  %s  %s  %s  %s  %s  %s",
			headerStyle.Render(pad("NAME", cols.name)),
			headerStyle.Render(pad("REPO", cols.repo)),
			headerStyle.Render(pad("AGENT", cols.agent)),
			headerStyle.Render(pad("STATUS", cols.status)),
			headerStyle.Render(pad("ACTIVITY", cols.activity)),
			headerStyle.Render(pad("BRANCH", cols.branch)),
			headerStyle.Render(pad("GIT", cols.git)),
			headerStyle.Render(pad("AGE", cols.age)),
			headerStyle.Render(pad("ATTACHED", cols.attached)),
		)
		if m.width > 0 && lipgloss.Width(headerLine) > m.width {
			headerLine = ansi.Truncate(headerLine, m.width, "")
		}
		b.WriteString(headerLine)
		b.WriteString("\n")

		// Separator
		sepWidth := max(0, min(m.width-4, cols.totalDashWidth()-4))
		sep := dimStyle.Render("  " + strings.Repeat("─", sepWidth))
		b.WriteString(sep)
		b.WriteString("\n")

		// Session rows (viewport windowed)
		visible := m.visibleRows()
		end := m.offset + visible
		if end > len(m.sessions) {
			end = len(m.sessions)
		}
		now := time.Now()

		if m.offset > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  ↑ %d more above", m.offset)))
			b.WriteString("\n")
		}

		for i := m.offset; i < end; i++ {
			line := m.renderRow(m.sessions[i], cols, now, i == m.cursor, dimStyle, selectedStyle)
			if m.width > 0 && lipgloss.Width(line) > m.width {
				line = ansi.Truncate(line, m.width, "")
			}
			b.WriteString(line)
			b.WriteString("\n")
		}

		if end < len(m.sessions) {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  ↓ %d more below", len(m.sessions)-end)))
			b.WriteString("\n")
		}
	}

	// Confirmation prompts
	switch m.state {
	case dashStateConfirmDelete:
		if s := m.selectedSession(); s != nil {
			b.WriteString("\n")
			b.WriteString(warningStyle.Render(fmt.Sprintf("  Delete '%s'? [y/N]", s.Name)))
			b.WriteString("\n")
		}
	case dashStateConfirmStop:
		if s := m.selectedSession(); s != nil {
			b.WriteString("\n")
			b.WriteString(warningStyle.Render(fmt.Sprintf("  Stop '%s'? [y/N]", s.Name)))
			b.WriteString("\n")
		}
	}

	// Fill remaining space
	rendered := strings.Count(b.String(), "\n")
	footerLines := 2
	for i := rendered; i < m.height-footerLines-1; i++ {
		b.WriteString("\n")
	}

	// Footer
	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#444444"))
	b.WriteString(helpStyle.Render("  enter/a attach  s stop  x delete  r resume  q quit"))
	b.WriteString("\n")

	return b.String()
}

type dashCols struct {
	name     int
	repo     int
	agent    int
	status   int
	activity int
	branch   int
	git      int
	age      int
	attached int
}

func (dc dashCols) totalDashWidth() int {
	return 2 + dc.name + 2 + dc.repo + 2 + dc.agent + 2 + dc.status + 2 + dc.activity + 2 + dc.branch + 2 + dc.git + 2 + dc.age + 2 + dc.attached + 2
}

func (m DashboardModel) computeDashCols() dashCols {
	dc := dashCols{
		name:     4,
		repo:     4,
		agent:    5,
		status:   6,
		activity: 8,
		branch:   6,
		git:      3,
		age:      3,
		attached: 8,
	}
	now := time.Now()
	for _, s := range m.sessions {
		if n := len(s.Name); n > dc.name {
			dc.name = n
		}
		if n := len(s.RepoName); n > dc.repo {
			dc.repo = n
		}
		if n := len(s.Agent); n > dc.agent {
			dc.agent = n
		}
		if n := len(s.Status); n > dc.status {
			dc.status = n
		}
		activity := s.AgentStatus
		if s.Status != "running" {
			activity = ""
		}
		if activity == "approval" {
			activity = "⚠ approval"
		}
		if n := len(activity); n > dc.activity {
			dc.activity = n
		}
		branch := s.Branch
		if p := strings.SplitN(branch, "/", 3); len(p) == 3 {
			branch = p[2]
		}
		if n := len(branch); n > dc.branch {
			dc.branch = n
		}
		var gp []string
		if s.Dirty {
			gp = append(gp, "dirty")
		}
		if s.UnpushedCount > 0 {
			gp = append(gp, fmt.Sprintf("%d↑", s.UnpushedCount))
		}
		if n := len(strings.Join(gp, " ")); n > dc.git {
			dc.git = n
		}
		if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
			if n := len(ShortDuration(now.Sub(t))); n > dc.age {
				dc.age = n
			}
		}
		if s.LastAttachedAt != "" {
			if t, err := time.Parse(time.RFC3339, s.LastAttachedAt); err == nil {
				att := ShortDuration(now.Sub(t)) + " ago"
				if n := len(att); n > dc.attached {
					dc.attached = n
				}
			}
		}
	}
	return dc
}

func (m DashboardModel) renderRow(s protocol.SessionInfo, cols dashCols, now time.Time, selected bool, dimStyle, selectedStyle lipgloss.Style) string {
	indicator := "●"
	indicatorColor := lipgloss.Color("#00ff87")
	switch s.Status {
	case "stopped":
		indicator = "○"
		indicatorColor = lipgloss.Color("#626262")
	case "errored":
		indicator = "✗"
		indicatorColor = lipgloss.Color("#ff5f5f")
	}

	activity := s.AgentStatus
	if s.Status != "running" {
		activity = ""
	}
	if activity == "approval" {
		activity = "⚠ approval"
	}

	branch := s.Branch
	if p := strings.SplitN(branch, "/", 3); len(p) == 3 {
		branch = p[2]
	}

	var gitParts []string
	if s.Dirty {
		gitParts = append(gitParts, "dirty")
	}
	if s.UnpushedCount > 0 {
		gitParts = append(gitParts, fmt.Sprintf("%d↑", s.UnpushedCount))
	}
	gitStr := strings.Join(gitParts, " ")

	age := ""
	if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
		age = ShortDuration(now.Sub(t))
	}

	attached := ""
	if s.LastAttachedAt != "" {
		if t, err := time.Parse(time.RFC3339, s.LastAttachedAt); err == nil {
			attached = ShortDuration(now.Sub(t)) + " ago"
		}
	}

	prefix := lipgloss.NewStyle().Foreground(indicatorColor).Render(indicator)

	cursor := " "
	if selected {
		cursor = ">"
	}

	sep := dimStyle.Render("  ")
	line := fmt.Sprintf("%s %s %s%s%s%s%s%s%s%s%s%s%s%s%s%s%s",
		cursor, prefix,
		pad(s.Name, cols.name), sep,
		dimStyle.Render(pad(s.RepoName, cols.repo)), sep,
		pad(s.Agent, cols.agent), sep,
		pad(s.Status, cols.status), sep,
		pad(activity, cols.activity), sep,
		dimStyle.Render(pad(branch, cols.branch)), sep,
		pad(gitStr, cols.git), sep,
		dimStyle.Render(pad(age, cols.age)),
	)
	if attached != "" {
		line += sep + dimStyle.Render(pad(attached, cols.attached))
	}

	if selected {
		line = selectedStyle.Render(line)
	}

	return line
}

func (m DashboardModel) selectedSession() *protocol.SessionInfo {
	if m.cursor >= 0 && m.cursor < len(m.sessions) {
		return &m.sessions[m.cursor]
	}
	return nil
}

func (m DashboardModel) selectedSessionID() string {
	if s := m.selectedSession(); s != nil {
		return s.ID
	}
	return ""
}

func (m *DashboardModel) clampCursor() {
	if m.cursor >= len(m.sessions) {
		m.cursor = len(m.sessions) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func RunDashboard(sessions []protocol.SessionInfo, refresh func() []protocol.SessionInfo) *DashboardResult {
	sortDashboardSessions(sessions)
	m := NewDashboardModel(sessions, func() []protocol.SessionInfo {
		s := refresh()
		sortDashboardSessions(s)
		return s
	})
	p := tea.NewProgram(m, tea.WithAltScreen())

	final, err := p.Run()
	if err != nil {
		return nil
	}

	result, ok := final.(DashboardModel)
	if !ok {
		return nil
	}
	return result.result
}

func sortDashboardSessions(sessions []protocol.SessionInfo) {
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].RepoName != sessions[j].RepoName {
			return sessions[i].RepoName < sessions[j].RepoName
		}
		return sessions[i].Name < sessions[j].Name
	})
}
