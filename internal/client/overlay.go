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

type overlayModel struct {
	list        list.Model
	filterInput textinput.Model
	state       overlayState
	selected    *protocol.SessionInfo
	width       int
	height      int
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

func newOverlayModel(sessions []protocol.SessionInfo) overlayModel {
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
		list:        l,
		filterInput: fi,
		state:       stateList,
	}
}

func (m overlayModel) Init() tea.Cmd {
	return nil
}

func (m overlayModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width, msg.Height-3)
		return m, nil

	case tea.KeyMsg:
		switch m.state {
		case stateFilter:
			switch msg.String() {
			case "esc", "enter":
				m.state = stateList
				m.filterInput.Blur()
				return m, nil
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
				return m, nil

			case "k", "up":
				m.list.CursorUp()
				if _, ok := m.list.SelectedItem().(groupHeader); ok {
					m.list.CursorUp()
				}
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m overlayModel) View() string {
	var b strings.Builder

	if m.state == stateFilter {
		b.WriteString("Filter: ")
		b.WriteString(m.filterInput.View())
		b.WriteString("\n\n")
	}

	b.WriteString(m.list.View())

	if m.state == stateConfirmDelete {
		if item, ok := m.list.SelectedItem().(sessionItem); ok {
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().
				Foreground(lipgloss.Color("#ff5f5f")).
				Render(fmt.Sprintf("Delete '%s'? [y/N]", item.info.Name)))
		}
	}

	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#444444"))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("enter attach  x delete  / filter  q quit"))

	return b.String()
}

// RunOverlay launches the bubbletea overlay listing sessions grouped by repo.
// It returns the user's chosen action or nil if dismissed.
func RunOverlay(sessions []protocol.SessionInfo) *OverlayResult {
	m := newOverlayModel(sessions)
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
