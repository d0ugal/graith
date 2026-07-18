package client

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
)

type listWatchState int

const (
	listWatchStateNormal listWatchState = iota
	listWatchStateConfirmDelete
	listWatchStateConfirmStop
)

type tickMsg time.Time

type refreshMsg struct {
	sessions []protocol.SessionInfo
}

type ListWatchResult struct {
	Action    string
	SessionID string
}

// ListWatchOptions controls the presentation shared with `gr list` snapshot
// mode. Filtering is performed by the CLI before sessions reach the model.
type ListWatchOptions struct {
	Wide    bool
	Tree    bool
	NoColor bool
}

type ListWatchModel struct {
	sessions         []protocol.SessionInfo
	cursor           int
	offset           int
	width            int
	height           int
	state            listWatchState
	confirmSessionID string
	result           *ListWatchResult
	refresh          func() []protocol.SessionInfo
	keys             ListWatchKeys
	options          ListWatchOptions
	treeNames        map[string]string
}

func NewListWatchModel(sessions []protocol.SessionInfo, refresh func() []protocol.SessionInfo) *ListWatchModel {
	return &ListWatchModel{
		sessions:  sessions,
		refresh:   refresh,
		keys:      DefaultListWatchKeys(),
		treeNames: make(map[string]string),
	}
}

func newListWatchModel(sessions []protocol.SessionInfo, refresh func() []protocol.SessionInfo, options ListWatchOptions) *ListWatchModel {
	m := NewListWatchModel(sessions, refresh)
	m.options = options
	if options.Tree {
		m.sessions, m.treeNames = prepareListWatchTree(sessions)
	}

	return m
}

func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m *ListWatchModel) doRefresh() tea.Cmd {
	refresh := m.refresh

	return func() tea.Msg {
		return refreshMsg{sessions: refresh()}
	}
}

func (m *ListWatchModel) Init() tea.Cmd {
	return tickCmd()
}

// visibleRows returns how many session rows fit in the viewport.
// Reserves lines for: header (2), column header (1), separator (1),
// confirmation prompt (2 when active), footer (2).
func (m *ListWatchModel) visibleRows() int {
	reserved := 6
	if m.state != listWatchStateNormal {
		reserved += 2
	}

	rows := m.height - reserved
	if rows < 1 {
		rows = 1
	}

	return rows
}

func (m *ListWatchModel) scrollToCursor() {
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

func (m *ListWatchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		return m, tea.Batch(tickCmd(), m.doRefresh())

	case refreshMsg:
		if msg.sessions == nil {
			return m, nil
		}

		selectedID := m.selectedSessionID()
		m.sessions = msg.sessions
		if m.options.Tree {
			m.sessions, m.treeNames = prepareListWatchTree(msg.sessions)
		}
		m.clampCursor()

		if selectedID != "" {
			for i, s := range m.sessions {
				if s.ID == selectedID {
					m.cursor = i
					break
				}
			}
		}

		if m.confirmSessionID != "" {
			target := m.sessionByID(m.confirmSessionID)
			if target == nil || (m.state == listWatchStateConfirmStop && target.Status != "running") {
				m.state = listWatchStateNormal
				m.confirmSessionID = ""
			}
		}

		m.scrollToCursor()

		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.scrollToCursor()

		return m, nil

	case tea.KeyPressMsg:
		switch m.state {
		case listWatchStateConfirmDelete:
			if matchKey(m.keys.Confirm, msg.String()) && m.confirmSessionID != "" {
				m.result = &ListWatchResult{Action: "delete", SessionID: m.confirmSessionID}
				return m, tea.Quit
			}

			m.state = listWatchStateNormal
			m.confirmSessionID = ""

			return m, nil

		case listWatchStateConfirmStop:
			if matchKey(m.keys.Confirm, msg.String()) && m.confirmSessionID != "" {
				m.result = &ListWatchResult{Action: "stop", SessionID: m.confirmSessionID}
				return m, tea.Quit
			}

			m.state = listWatchStateNormal
			m.confirmSessionID = ""

			return m, nil

		case listWatchStateNormal:
			s := msg.String()

			switch {
			case matchKey(m.keys.Cancel, s):
				return m, tea.Quit
			case matchKey(m.keys.Down, s):
				if m.cursor < len(m.sessions)-1 {
					m.cursor++
					m.scrollToCursor()
				}

				return m, nil
			case matchKey(m.keys.Up, s):
				if m.cursor > 0 {
					m.cursor--
					m.scrollToCursor()
				}

				return m, nil
			case matchKey(m.keys.Attach, s):
				if sel := m.selectedSession(); sel != nil {
					m.result = &ListWatchResult{Action: "attach", SessionID: sel.ID}
					return m, tea.Quit
				}

				return m, nil
			case matchKey(m.keys.Stop, s):
				if sel := m.selectedSession(); sel != nil && sel.Status == "running" {
					m.state = listWatchStateConfirmStop
					m.confirmSessionID = sel.ID
				}

				return m, nil
			case matchKey(m.keys.Delete, s):
				if sel := m.selectedSession(); sel != nil {
					m.state = listWatchStateConfirmDelete
					m.confirmSessionID = sel.ID
				}

				return m, nil
			case matchKey(m.keys.Resume, s):
				if sel := m.selectedSession(); sel != nil && sel.Status == "stopped" {
					m.result = &ListWatchResult{Action: "resume", SessionID: sel.ID}
					return m, tea.Quit
				}

				return m, nil
			}
		}
	}

	return m, nil
}

func (m *ListWatchModel) View() tea.View {
	if m.width == 0 || m.height == 0 {
		return tea.NewView("")
	}

	var b strings.Builder

	titleStyle := m.style(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7B61FF")))
	dimStyle := m.style(lipgloss.NewStyle().Foreground(lipgloss.Color("#626262")))
	headerStyle := m.style(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#888888")))
	warningStyle := m.style(lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f")))

	// Header
	title := titleStyle.Render("graith list --watch")
	countStr := dimStyle.Render(fmt.Sprintf(" (%d sessions)", len(m.sessions)))
	header := title + countStr
	b.WriteString(header)
	b.WriteString("\n\n")

	if len(m.sessions) == 0 {
		b.WriteString(dimStyle.Render("  No sessions. Create one with: gr new <name>"))
		b.WriteString("\n")
	} else {
		layout := m.computeListWatchLayout()

		// Column headers
		headerCells := []string{headerStyle.Render(pad("NAME", layout.name))}
		for _, col := range layout.columns {
			headerCells = append(headerCells,
				headerStyle.Render(pad(strings.ToUpper(col.column.Header), col.width)))
		}

		headerLine := "  " + strings.Join(headerCells, "  ")
		if m.width > 0 && lipgloss.Width(headerLine) > m.width {
			headerLine = ansi.Truncate(headerLine, m.width, "")
		}

		b.WriteString(headerLine)
		b.WriteString("\n")

		// Separator
		sepWidth := max(0, min(m.width-4, layout.totalWidth()-4))
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
			line := m.renderRow(m.sessions[i], layout, now, i == m.cursor)
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
	case listWatchStateConfirmDelete:
		if s := m.sessionByID(m.confirmSessionID); s != nil {
			b.WriteString("\n")
			b.WriteString(warningStyle.Render(fmt.Sprintf("  Delete '%s'? [y/N]", s.Name)))
			b.WriteString("\n")
		}
	case listWatchStateConfirmStop:
		if s := m.sessionByID(m.confirmSessionID); s != nil {
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
	helpStyle := m.style(lipgloss.NewStyle().Foreground(lipgloss.Color("#444444")))
	b.WriteString(helpStyle.Render(fmt.Sprintf("  %s attach  %s stop  %s delete  %s resume  %s quit",
		keyHint(m.keys.Attach),
		keyHint(m.keys.Stop),
		keyHint(m.keys.Delete),
		keyHint(m.keys.Resume),
		keyHint(m.keys.Cancel),
	)))
	b.WriteString("\n")

	v := tea.NewView(b.String())
	v.AltScreen = true

	return v
}

type listWatchColumn struct {
	column SessionColumn
	width  int
}

type listWatchLayout struct {
	name    int
	columns []listWatchColumn
}

func (l listWatchLayout) totalWidth() int {
	width := 2 + l.name
	for _, col := range l.columns {
		width += 2 + col.width
	}

	return width + 2
}

// listWatchColumns uses the snapshot-list subset of the shared column
// registry. This keeps compact/wide watch mode in lockstep with `gr list`
// without introducing another field registry or renderer.
func listWatchColumns(wide bool) []SessionColumn {
	var columns []SessionColumn
	for _, column := range SessionColumns() {
		if column.ShowCLI && (wide || !column.Wide) {
			columns = append(columns, column)
		}
	}

	return columns
}

func (m *ListWatchModel) computeListWatchLayout() listWatchLayout {
	layout := listWatchLayout{name: lipgloss.Width("NAME")}
	for _, column := range listWatchColumns(m.options.Wide) {
		layout.columns = append(layout.columns, listWatchColumn{
			column: column,
			width:  lipgloss.Width(strings.ToUpper(column.Header)),
		})
	}

	now := time.Now()
	for _, session := range m.sessions {
		if width := lipgloss.Width(m.displayName(session)); width > layout.name {
			layout.name = width
		}

		for i := range layout.columns {
			value := layout.columns[i].column.CLIValue(session, now)
			if width := lipgloss.Width(value); width > layout.columns[i].width {
				layout.columns[i].width = width
			}
		}
	}

	return layout
}

func (m *ListWatchModel) renderRow(session protocol.SessionInfo, layout listWatchLayout, now time.Time, selected bool) string {
	indicator := "●"
	indicatorColor := lipgloss.Color("#00ff87")

	switch session.Status {
	case "stopped":
		indicator = "○"
		indicatorColor = lipgloss.Color("#626262")
	case "errored":
		indicator = "✗"
		indicatorColor = lipgloss.Color("#ff5f5f")
	}

	indicator = m.style(lipgloss.NewStyle().Foreground(indicatorColor)).Render(indicator)
	cursor := " "
	if selected {
		cursor = ">"
	}

	cells := []string{pad(m.displayName(session), layout.name)}
	for _, col := range layout.columns {
		value := pad(col.column.CLIValue(session, now), col.width)
		if col.column.CLIColor != nil {
			value = m.style(lipgloss.NewStyle().Foreground(col.column.CLIColor(session))).Render(value)
		}

		cells = append(cells, value)
	}

	line := fmt.Sprintf("%s %s %s", cursor, indicator, strings.Join(cells, "  "))
	if selected {
		line = m.style(lipgloss.NewStyle().Bold(true)).Render(line)
	}

	return line
}

func (m *ListWatchModel) displayName(session protocol.SessionInfo) string {
	name := session.Name
	if session.Starred {
		name = "★ " + name
	}

	if m.options.Tree {
		name = m.treeNames[session.ID] + name
	}

	return name
}

func (m *ListWatchModel) style(style lipgloss.Style) lipgloss.Style {
	if m.options.NoColor {
		return lipgloss.NewStyle()
	}

	return style
}

func (m *ListWatchModel) selectedSession() *protocol.SessionInfo {
	if m.cursor >= 0 && m.cursor < len(m.sessions) {
		return &m.sessions[m.cursor]
	}

	return nil
}

func (m *ListWatchModel) sessionByID(id string) *protocol.SessionInfo {
	for i, s := range m.sessions {
		if s.ID == id {
			return &m.sessions[i]
		}
	}

	return nil
}

func (m *ListWatchModel) selectedSessionID() string {
	if s := m.selectedSession(); s != nil {
		return s.ID
	}

	return ""
}

func (m *ListWatchModel) clampCursor() {
	if m.cursor >= len(m.sessions) {
		m.cursor = len(m.sessions) - 1
	}

	if m.cursor < 0 {
		m.cursor = 0
	}
}

func RunListWatch(sessions []protocol.SessionInfo, keys ListWatchKeys, options ListWatchOptions, refresh func() []protocol.SessionInfo) (*ListWatchResult, error) {
	initial := append([]protocol.SessionInfo(nil), sessions...)
	if !options.Tree {
		sortListWatchSessions(initial)
	}

	m := newListWatchModel(initial, func() []protocol.SessionInfo {
		s := refresh()
		if !options.Tree {
			sortListWatchSessions(s)
		}

		return s
	}, options)
	m.keys = keys
	p := tea.NewProgram(m)

	final, err := p.Run()
	if err != nil {
		return nil, err
	}

	result, ok := final.(*ListWatchModel)
	if !ok {
		return nil, fmt.Errorf("list watch returned unexpected model %T", final)
	}

	return result.result, nil
}

func sortListWatchSessions(sessions []protocol.SessionInfo) {
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].RepoName != sessions[j].RepoName {
			return sessions[i].RepoName < sessions[j].RepoName
		}

		return sessions[i].Name < sessions[j].Name
	})
}

// prepareListWatchTree orders sessions using the same parent/child shape as
// snapshot `gr list --tree` and returns display prefixes keyed by stable
// session ID. Orphans and cycles are retained as roots rather than disappearing
// from a live monitoring view.
func prepareListWatchTree(sessions []protocol.SessionInfo) ([]protocol.SessionInfo, map[string]string) {
	byID := make(map[string]protocol.SessionInfo, len(sessions))
	children := make(map[string][]protocol.SessionInfo)
	var roots []protocol.SessionInfo

	for _, session := range sessions {
		byID[session.ID] = session
	}

	for _, session := range sessions {
		if session.ParentID == "" {
			roots = append(roots, session)
			continue
		}

		if _, ok := byID[session.ParentID]; !ok {
			roots = append(roots, session)
			continue
		}

		children[session.ParentID] = append(children[session.ParentID], session)
	}

	sortGroup := func(group []protocol.SessionInfo) {
		sort.Slice(group, func(i, j int) bool {
			if group[i].RepoName != group[j].RepoName {
				return group[i].RepoName < group[j].RepoName
			}

			return group[i].Name < group[j].Name
		})
	}
	sortGroup(roots)
	for id := range children {
		sortGroup(children[id])
	}

	ordered := make([]protocol.SessionInfo, 0, len(sessions))
	prefixes := make(map[string]string, len(sessions))
	seen := make(map[string]bool, len(sessions))

	var walk func(protocol.SessionInfo, string, string)
	walk = func(session protocol.SessionInfo, prefix, childPrefix string) {
		if seen[session.ID] {
			return
		}

		seen[session.ID] = true
		ordered = append(ordered, session)
		prefixes[session.ID] = prefix

		group := children[session.ID]
		for i, child := range group {
			if i == len(group)-1 {
				walk(child, childPrefix+"`-- ", childPrefix+"    ")
			} else {
				walk(child, childPrefix+"|-- ", childPrefix+"|   ")
			}
		}
	}

	for _, root := range roots {
		walk(root, "", "")
	}

	// A cycle has no root. Keep every remaining session visible and let the
	// first sorted member of each cycle act as a root.
	remainder := append([]protocol.SessionInfo(nil), sessions...)
	sortGroup(remainder)
	for _, session := range remainder {
		walk(session, "", "")
	}

	return ordered, prefixes
}
