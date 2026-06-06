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
	"github.com/dougalmatthews/graith/internal/protocol"
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

// compactDelegate renders each item on a single line using horizontal columns.
type compactDelegate struct{}

func (d compactDelegate) Height() int                          { return 1 }
func (d compactDelegate) Spacing() int                         { return 0 }
func (d compactDelegate) Update(tea.Msg, *list.Model) tea.Cmd  { return nil }

func (d compactDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	selected := index == m.Index()
	width := m.Width()

	if gh, ok := item.(groupHeader); ok {
		style := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7B61FF"))
		line := style.Render("▸ " + gh.name)
		fmt.Fprint(w, line)
		return
	}

	si, ok := item.(sessionItem)
	if !ok {
		return
	}

	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))

	// Status indicator
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

	// Name
	name := si.info.Name

	// Agent status or session status
	status := si.info.Status
	if si.info.AgentStatus != "" && si.info.Status == "running" {
		status = si.info.AgentStatus
	}

	// Branch (stripped prefix)
	branch := si.info.Branch
	if p := strings.SplitN(branch, "/", 3); len(p) == 3 {
		branch = p[2]
	}

	// Git
	var gitParts []string
	if si.info.Dirty {
		gitParts = append(gitParts, "dirty")
	}
	if si.info.UnpushedCount > 0 {
		gitParts = append(gitParts, fmt.Sprintf("%d↑", si.info.UnpushedCount))
	}
	gitStr := strings.Join(gitParts, " ")

	// Age
	now := time.Now()
	age := ""
	if t, err := time.Parse(time.RFC3339, si.info.CreatedAt); err == nil {
		age = shortDur(now.Sub(t))
	}

	// Last attached
	attached := ""
	if si.info.LastAttachedAt != "" {
		if t, err := time.Parse(time.RFC3339, si.info.LastAttachedAt); err == nil {
			attached = shortDur(now.Sub(t)) + " ago"
		}
	}

	// Build the line: indicator name | status | branch | git | age | attached
	var right []string
	right = append(right, status)
	if branch != "" {
		right = append(right, dim.Render(branch))
	}
	if gitStr != "" {
		right = append(right, gitStr)
	}
	if age != "" {
		right = append(right, dim.Render(age))
	}
	if attached != "" {
		right = append(right, dim.Render(attached))
	}

	detail := strings.Join(right, dim.Render("  "))
	line := fmt.Sprintf("  %s %s  %s", prefix, name, detail)

	if selected {
		cursor := "> "
		line = fmt.Sprintf("%s%s %s  %s", cursor, prefix, name, detail)
		line = lipgloss.NewStyle().Bold(true).Render(line)
	}

	// Truncate to terminal width
	if width > 0 && lipgloss.Width(line) > width {
		line = line[:width]
	}

	fmt.Fprint(w, line)
}

func shortDur(d time.Duration) string {
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

	l := list.New(items, compactDelegate{}, 80, 20)
	l.Title = "Sessions"
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.KeyMap.Quit = key.NewBinding(key.WithKeys())

	fi := textinput.New()
	fi.Placeholder = "filter..."
	fi.CharLimit = 64

	return overlayModel{
		list:         l,
		filterInput:  fi,
		state:        stateList,
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
	if sid == m.previewSessionID {
		return nil
	}
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
			m.previewSessionID = msg.sessionID
			m.previewContent = msg.content
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		panelWidth := min(60, msg.Width-4)
		m.list.SetSize(panelWidth-4, msg.Height-6)
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
	panelWidth := min(60, w-4)

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
	panelContent.WriteString(helpStyle.Render("enter attach  x delete  / filter  q quit"))

	panel := lipgloss.NewStyle().
		Width(panelWidth).
		Background(lipgloss.Color("#1a1a1a")).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444444")).
		Padding(0, 1).
		Render(panelContent.String())

	// --- Build background from preview scrollback ---
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#333333"))
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
					line = line[:w]
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

	pad := strings.Repeat(" ", offsetX)
	for i, pl := range panelLines {
		row := offsetY + i
		if row >= 0 && row < h {
			bgLines[row] = pad + pl
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

	result := final.(overlayModel)
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
