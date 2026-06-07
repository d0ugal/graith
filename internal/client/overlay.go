package client

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
)

type overlayState int

const (
	stateList overlayState = iota
	stateFilter
	stateConfirmDelete
)

type sessionItem struct {
	info protocol.SessionInfo
}

func (s sessionItem) Title() string       { return s.info.Name }
func (s sessionItem) Description() string { return "" }
func (s sessionItem) FilterValue() string { return s.info.Name + " " + s.info.RepoName }

type groupHeader struct {
	name string
}

func (g groupHeader) Title() string       { return g.name }
func (g groupHeader) Description() string { return "" }
func (g groupHeader) FilterValue() string { return "" }

type columnWidths struct {
	name   int
	status int
	branch int
	git    int
	age    int
}

func (cw columnWidths) totalWidth() int {
	// "  ● " + name + "  " + status + "  " + branch + "  " + git + "  " + age + margin
	return 4 + cw.name + 2 + cw.status + 2 + cw.branch + 2 + cw.git + 2 + cw.age + 4
}

func computeColumnWidths(sessions []protocol.SessionInfo) columnWidths {
	var cw columnWidths
	now := time.Now()
	for _, s := range sessions {
		if n := len(s.Name); n > cw.name {
			cw.name = n
		}
		status := s.Status
		if s.AgentStatus != "" && s.Status == "running" {
			status = s.AgentStatus
		}
		if n := len(status); n > cw.status {
			cw.status = n
		}
		branch := s.Branch
		if p := strings.SplitN(branch, "/", 3); len(p) == 3 {
			branch = p[2]
		}
		if n := len(branch); n > cw.branch {
			cw.branch = n
		}
		var gp []string
		if s.Dirty {
			gp = append(gp, "dirty")
		}
		if s.UnpushedCount > 0 {
			gp = append(gp, fmt.Sprintf("%d↑", s.UnpushedCount))
		}
		if n := len(strings.Join(gp, " ")); n > cw.git {
			cw.git = n
		}
		if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
			if n := len(ShortDuration(now.Sub(t))); n > cw.age {
				cw.age = n
			}
		}
	}
	return cw
}

// compactDelegate renders each item on a single line with aligned columns.
type compactDelegate struct {
	cols columnWidths
}

func (d compactDelegate) Height() int                         { return 1 }
func (d compactDelegate) Spacing() int                        { return 0 }
func (d compactDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func pad(s string, width int) string {
	if n := width - len(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func (d compactDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	selected := index == m.Index()
	width := m.Width()

	if gh, ok := item.(groupHeader); ok {
		style := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7B61FF"))
		line := style.Render("▸ " + gh.name)
		_, _ = fmt.Fprint(w, line)
		return
	}

	si, ok := item.(sessionItem)
	if !ok {
		return
	}

	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))

	indicator := "●"
	indicatorColor := lipgloss.Color("#00ff87")
	switch si.info.Status {
	case "stopped":
		indicator = "○"
		indicatorColor = lipgloss.Color("#626262")
	case "errored":
		indicator = "✗"
		indicatorColor = lipgloss.Color("#ff5f5f")
	}
	prefix := lipgloss.NewStyle().Foreground(indicatorColor).Render(indicator)

	name := pad(si.info.Name, d.cols.name)

	status := si.info.Status
	if si.info.AgentStatus != "" && si.info.Status == "running" {
		status = si.info.AgentStatus
	}
	status = pad(status, d.cols.status)

	branch := si.info.Branch
	if p := strings.SplitN(branch, "/", 3); len(p) == 3 {
		branch = p[2]
	}
	branch = pad(branch, d.cols.branch)

	var gitParts []string
	if si.info.Dirty {
		gitParts = append(gitParts, "dirty")
	}
	if si.info.UnpushedCount > 0 {
		gitParts = append(gitParts, fmt.Sprintf("%d↑", si.info.UnpushedCount))
	}
	gitStr := pad(strings.Join(gitParts, " "), d.cols.git)

	now := time.Now()
	age := ""
	if t, err := time.Parse(time.RFC3339, si.info.CreatedAt); err == nil {
		age = ShortDuration(now.Sub(t))
	}
	age = pad(age, d.cols.age)

	attached := ""
	if si.info.LastAttachedAt != "" {
		if t, err := time.Parse(time.RFC3339, si.info.LastAttachedAt); err == nil {
			attached = ShortDuration(now.Sub(t)) + " ago"
		}
	}

	sep := dim.Render("  ")
	line := fmt.Sprintf("  %s %s%s%s%s%s%s%s%s%s",
		prefix, name, sep, status, sep, dim.Render(branch), sep, gitStr, sep, dim.Render(age))
	if attached != "" {
		line += sep + dim.Render(attached)
	}

	if selected {
		line = fmt.Sprintf("> %s %s%s%s%s%s%s%s%s%s",
			prefix, name, sep, status, sep, dim.Render(branch), sep, gitStr, sep, dim.Render(age))
		if attached != "" {
			line += sep + dim.Render(attached)
		}
		line = lipgloss.NewStyle().Bold(true).Render(line)
	}

	if width > 0 && lipgloss.Width(line) > width {
		line = ansi.Truncate(line, width, "")
	}

	_, _ = fmt.Fprint(w, line)
}

func ShortDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dd", int(d.Hours())/24)
}

type previewMsg struct {
	sessionID string
	content   string
}

type overlayModel struct {
	list             list.Model
	filterInput      textinput.Model
	state            overlayState
	selected         *protocol.SessionInfo
	width            int
	height           int
	contentWidth     int
	fetchPreview     func(sessionID string) string
	previewContent   string
	previewSessionID string
}

// OverlayResult holds the outcome of the overlay interaction.
type OverlayResult struct {
	Action    string
	SessionID string
}

func buildGroupedItems(sessions []protocol.SessionInfo) []list.Item {
	groups := map[string][]protocol.SessionInfo{}
	var repoOrder []string
	seen := map[string]bool{}

	for _, s := range sessions {
		repo := s.RepoName
		if repo == "" {
			repo = "(no repo)"
		}
		if !seen[repo] {
			repoOrder = append(repoOrder, repo)
			seen[repo] = true
		}
		groups[repo] = append(groups[repo], s)
	}
	sort.Strings(repoOrder)
	for _, sessions := range groups {
		sort.Slice(sessions, func(i, j int) bool {
			return sessions[i].Name < sessions[j].Name
		})
	}

	var items []list.Item
	for _, repo := range repoOrder {
		items = append(items, groupHeader{name: repo})
		for _, s := range groups[repo] {
			items = append(items, sessionItem{info: s})
		}
	}
	return items
}

func newOverlayModel(sessions []protocol.SessionInfo, fetchPreview func(sessionID string) string) overlayModel {
	items := buildGroupedItems(sessions)
	cols := computeColumnWidths(sessions)
	contentWidth := cols.totalWidth()

	l := list.New(items, compactDelegate{cols: cols}, contentWidth, len(items)+2)
	l.Title = "Sessions"
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.KeyMap.Quit = key.NewBinding(key.WithKeys())

	// Skip past the initial group header so the cursor starts on the first session.
	if _, ok := l.SelectedItem().(groupHeader); ok {
		l.CursorDown()
	}

	fi := textinput.New()
	fi.Placeholder = "filter..."
	fi.CharLimit = 64

	return overlayModel{
		list:         l,
		filterInput:  fi,
		state:        stateList,
		contentWidth: contentWidth,
		fetchPreview: fetchPreview,
	}
}

func (m overlayModel) Init() tea.Cmd {
	return m.fetchPreviewCmd()
}

func (m overlayModel) fetchPreviewCmd() tea.Cmd {
	if m.fetchPreview == nil {
		return nil
	}
	item, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		return nil
	}
	sid := item.info.ID
	fetch := m.fetchPreview
	return func() tea.Msg {
		return previewMsg{sessionID: sid, content: fetch(sid)}
	}
}

func (m overlayModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case previewMsg:
		// Guard against stale fetches: only apply if the result
		// matches the currently selected session.
		if item, ok := m.list.SelectedItem().(sessionItem); ok && item.info.ID == msg.sessionID {
			m.previewContent = msg.content
			if strings.TrimSpace(msg.content) != "" {
				m.previewSessionID = msg.sessionID
			}
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		panelWidth := min(m.contentWidth+4, msg.Width-4)
		panelHeight := min(len(m.list.Items())+4, msg.Height-6)
		m.list.SetSize(panelWidth-4, panelHeight)
		return m, nil

	case tea.KeyMsg:
		switch m.state {
		case stateFilter:
			switch msg.String() {
			case "esc", "enter":
				m.state = stateList
				m.filterInput.Blur()
				return m, m.fetchPreviewCmd()
			default:
				var cmd tea.Cmd
				m.filterInput, cmd = m.filterInput.Update(msg)
				return m, cmd
			}

		case stateConfirmDelete:
			switch msg.String() {
			case "y", "Y":
				if item, ok := m.list.SelectedItem().(sessionItem); ok {
					m.selected = &item.info
				}
				return m, tea.Quit
			default:
				m.state = stateList
				return m, nil
			}

		case stateList:
			switch msg.String() {
			case "q", "esc":
				return m, tea.Quit

			case "enter":
				if item, ok := m.list.SelectedItem().(sessionItem); ok {
					m.selected = &item.info
				}
				return m, tea.Quit

			case "x":
				if _, ok := m.list.SelectedItem().(sessionItem); ok {
					m.state = stateConfirmDelete
				}
				return m, nil

			case "/":
				m.filterInput.SetValue("")
				m.filterInput.Focus()
				m.state = stateFilter
				return m, textinput.Blink

			case "j", "down":
				m.list.CursorDown()
				if _, ok := m.list.SelectedItem().(groupHeader); ok {
					m.list.CursorDown()
				}
				return m, m.fetchPreviewCmd()

			case "k", "up":
				m.list.CursorUp()
				if _, ok := m.list.SelectedItem().(groupHeader); ok {
					m.list.CursorUp()
				}
				return m, m.fetchPreviewCmd()
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m overlayModel) View() string {
	w := m.width
	h := m.height
	if w == 0 || h == 0 {
		return ""
	}

	// --- Build panel content ---
	panelWidth := min(m.contentWidth+4, w-4)

	var panelContent strings.Builder
	if m.state == stateFilter {
		panelContent.WriteString("Filter: ")
		panelContent.WriteString(m.filterInput.View())
		panelContent.WriteString("\n\n")
	}

	panelContent.WriteString(m.list.View())

	if m.state == stateConfirmDelete {
		if item, ok := m.list.SelectedItem().(sessionItem); ok {
			panelContent.WriteString("\n")
			panelContent.WriteString(lipgloss.NewStyle().
				Foreground(lipgloss.Color("#ff5f5f")).
				Render(fmt.Sprintf("Delete '%s'? [y/N]", item.info.Name)))
		}
	}

	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#444444"))
	panelContent.WriteString("\n")
	panelContent.WriteString(helpStyle.Render("enter attach  n/p next/prev  x delete  / filter  q quit"))

	panel := lipgloss.NewStyle().
		Width(panelWidth).
		Background(lipgloss.Color("#1a1a1a")).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444444")).
		Padding(0, 1).
		Render(panelContent.String())

	// --- Build background from preview scrollback ---
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#555555"))
	bgLines := make([]string, h)
	if m.previewContent != "" {
		raw := strings.Split(m.previewContent, "\n")
		start := 0
		if len(raw) > h {
			start = len(raw) - h
		}
		for i := 0; i < h; i++ {
			idx := start + i
			if idx < len(raw) {
				line := raw[idx]
				// Pad to full width so dim styling covers the whole line
				if vis := lipgloss.Width(line); vis < w {
					line += strings.Repeat(" ", w-vis)
				} else if vis > w {
					line = ansi.Truncate(line, w, "")
				}
				bgLines[i] = dimStyle.Render(line)
			} else {
				bgLines[i] = strings.Repeat(" ", w)
			}
		}
	} else {
		for i := range bgLines {
			bgLines[i] = strings.Repeat(" ", w)
		}
	}

	// --- Overlay panel on background ---
	panelLines := strings.Split(panel, "\n")
	panelH := len(panelLines)
	panelRenderedW := 0
	for _, pl := range panelLines {
		if lw := lipgloss.Width(pl); lw > panelRenderedW {
			panelRenderedW = lw
		}
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

	return strings.Join(bgLines, "\n")
}

// RunOverlay launches the bubbletea overlay listing sessions grouped by repo.
// fetchPreview is called asynchronously to load scrollback for the selected session.
// It may be nil, in which case no preview is shown.
func RunOverlay(sessions []protocol.SessionInfo, fetchPreview func(sessionID string) string) *OverlayResult {
	m := newOverlayModel(sessions, fetchPreview)
	p := tea.NewProgram(m, tea.WithAltScreen())

	final, err := p.Run()
	if err != nil {
		return nil
	}

	result, ok := final.(overlayModel)
	if !ok {
		return nil
	}
	if result.selected != nil {
		action := "attach"
		if result.state == stateConfirmDelete {
			action = "delete"
		}
		return &OverlayResult{
			Action:    action,
			SessionID: result.selected.ID,
		}
	}

	return nil
}
