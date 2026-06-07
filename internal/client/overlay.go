package client

import (
	"fmt"
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

func (s sessionItem) Title() string {
	indicator := "●"
	color := lipgloss.Color("#00ff87")
	switch s.info.Status {
	case "stopped":
		indicator = "○"
		color = lipgloss.Color("#626262")
	case "errored":
		indicator = "✗"
		color = lipgloss.Color("#ff5f5f")
	}
	styled := lipgloss.NewStyle().Foreground(color).Render(indicator)
	return fmt.Sprintf("%s %s", styled, s.info.Name)
}

func (s sessionItem) Description() string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))

	var parts []string
	parts = append(parts, s.info.Agent)

	if s.info.AgentStatus != "" && s.info.Status == "running" {
		parts = append(parts, fmt.Sprintf("[%s]", s.info.AgentStatus))
	} else {
		parts = append(parts, s.info.Status)
	}

	if s.info.Branch != "" {
		branch := s.info.Branch
		// Strip common graith prefix (username/graith/...) to save space.
		if p := strings.SplitN(branch, "/", 3); len(p) == 3 {
			branch = p[2]
		}
		parts = append(parts, dim.Render(branch))
	}

	var gitParts []string
	if s.info.Dirty {
		gitParts = append(gitParts, "dirty")
	}
	if s.info.UnpushedCount > 0 {
		gitParts = append(gitParts, fmt.Sprintf("%d ahead", s.info.UnpushedCount))
	}
	if len(gitParts) > 0 {
		parts = append(parts, strings.Join(gitParts, ", "))
	}

	now := time.Now()
	if t, err := time.Parse(time.RFC3339, s.info.CreatedAt); err == nil {
		parts = append(parts, dim.Render(shortDur(now.Sub(t))))
	}
	if s.info.LastAttachedAt != "" {
		if t, err := time.Parse(time.RFC3339, s.info.LastAttachedAt); err == nil {
			parts = append(parts, dim.Render("attached "+shortDur(now.Sub(t))+" ago"))
		}
	}

	return strings.Join(parts, "  ")
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

func (s sessionItem) FilterValue() string {
	return s.info.Name + " " + s.info.RepoName
}

type groupHeader struct {
	name string
}

func (g groupHeader) Title() string {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7B61FF")).Render(g.name)
}
func (g groupHeader) Description() string { return "" }
func (g groupHeader) FilterValue() string { return "" }

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
		if !seen[s.RepoName] {
			repoOrder = append(repoOrder, s.RepoName)
			seen[s.RepoName] = true
		}
		groups[s.RepoName] = append(groups[s.RepoName], s)
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

	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = true

	l := list.New(items, delegate, 60, 20)
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
