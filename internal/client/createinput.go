package client

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

type RepoSuggestion struct {
	Name string
	Path string
}

func DiscoverRepos(allowedPaths []string, sessions []protocol.SessionInfo) []RepoSuggestion {
	seen := make(map[string]bool)

	var suggestions []RepoSuggestion

	addRepo := func(absPath string) {
		resolved := resolveRepoPath(absPath)
		if resolved == "" || seen[resolved] {
			return
		}

		seen[resolved] = true
		suggestions = append(suggestions, RepoSuggestion{
			Name: filepath.Base(resolved),
			Path: resolved,
		})
	}

	for _, p := range allowedPaths {
		expanded := expandPath(p)
		if expanded == "" {
			continue
		}

		if isGitRepo(expanded) {
			addRepo(expanded)
		}

		entries, err := os.ReadDir(expanded)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}

			child := filepath.Join(expanded, e.Name())
			if isGitRepo(child) {
				addRepo(child)
			}
		}
	}

	for _, s := range sessions {
		if s.RepoPath == "" || s.SystemKind != "" {
			continue
		}

		if isGitRepo(expandPath(s.RepoPath)) {
			addRepo(s.RepoPath)
		}
	}

	sort.Slice(suggestions, func(i, j int) bool {
		return strings.ToLower(suggestions[i].Name) < strings.ToLower(suggestions[j].Name)
	})

	return suggestions
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func expandPath(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}

	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}

	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}

	return filepath.Clean(p)
}

func resolveRepoPath(p string) string {
	p = expandPath(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}

	return p
}

const (
	createFieldName = iota
	createFieldRepo
	createFieldAgent
	createFieldLabels
)

type createSessionModel struct {
	nameInput   textinput.Model
	repoInput   textinput.Model
	labelsInput textinput.Model
	repos       []RepoSuggestion
	filtered    []RepoSuggestion
	agents      []string
	agentIdx    int
	focus       int
	done        bool
	width       int
	height      int

	showDropdown bool
	dropdownIdx  int
}

func newCreateSessionModel(defaultRepo string, repos []RepoSuggestion, agents []string, defaultAgent string) *createSessionModel {
	ni := textinput.New()
	ni.Placeholder = "session-name"
	ni.Focus()
	ni.CharLimit = 64
	ni.SetWidth(40)

	ri := textinput.New()
	ri.Placeholder = "/path/to/repo"
	ri.CharLimit = 256
	ri.SetWidth(40)

	li := textinput.New()
	li.Placeholder = "bugfix, urgent"
	li.CharLimit = 512
	li.SetWidth(40)

	if defaultRepo != "" {
		ri.SetValue(defaultRepo)
	}

	agentIdx := 0

	for i, a := range agents {
		if a == defaultAgent {
			agentIdx = i
			break
		}
	}

	m := createSessionModel{
		nameInput:   ni,
		repoInput:   ri,
		labelsInput: li,
		repos:       repos,
		agents:      agents,
		agentIdx:    agentIdx,
		focus:       createFieldName,
	}
	m.updateFiltered()

	return &m
}

// setFocus moves focus to the given field, updating text input focus and the
// repo dropdown visibility accordingly. Returns the cursor blink command.
func (m *createSessionModel) setFocus(f int) tea.Cmd {
	m.focus = f
	m.nameInput.Blur()
	m.repoInput.Blur()
	m.labelsInput.Blur()
	m.showDropdown = false

	switch f {
	case createFieldName:
		m.nameInput.Focus()
	case createFieldRepo:
		m.repoInput.Focus()
		m.showDropdown = len(m.filtered) > 0
		m.dropdownIdx = -1
	case createFieldLabels:
		m.labelsInput.Focus()
	}

	return textinput.Blink
}

// trySubmit marks the form done when the required fields are filled in.
func (m *createSessionModel) trySubmit() tea.Cmd {
	name := strings.TrimSpace(m.nameInput.Value())

	repo := strings.TrimSpace(m.repoInput.Value())
	if name == "" || repo == "" {
		return nil
	}

	m.done = true

	return tea.Quit
}

// selectedAgent returns the currently chosen agent name, or "" if none.
func (m *createSessionModel) selectedAgent() string {
	if m.agentIdx < 0 || m.agentIdx >= len(m.agents) {
		return ""
	}

	return m.agents[m.agentIdx]
}

func (m *createSessionModel) selectedLabels() []string {
	value := strings.TrimSpace(m.labelsInput.Value())
	if value == "" {
		return []string{}
	}

	parts := strings.Split(value, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	return parts
}

func (m *createSessionModel) nextField() (int, bool) {
	switch m.focus {
	case createFieldName:
		return createFieldRepo, true
	case createFieldRepo:
		if len(m.agents) > 0 {
			return createFieldAgent, true
		}

		return createFieldLabels, true
	case createFieldAgent:
		return createFieldLabels, true
	default:
		return 0, false
	}
}

func (m *createSessionModel) previousField() (int, bool) {
	switch m.focus {
	case createFieldRepo:
		return createFieldName, true
	case createFieldAgent:
		return createFieldRepo, true
	case createFieldLabels:
		if len(m.agents) > 0 {
			return createFieldAgent, true
		}

		return createFieldRepo, true
	default:
		return 0, false
	}
}

func (m *createSessionModel) updateFiltered() {
	query := strings.ToLower(strings.TrimSpace(m.repoInput.Value()))
	if query == "" {
		m.filtered = m.repos
	} else {
		m.filtered = nil
		for _, r := range m.repos {
			if strings.Contains(strings.ToLower(r.Name), query) ||
				strings.Contains(strings.ToLower(r.Path), query) {
				m.filtered = append(m.filtered, r)
			}
		}
	}

	if len(m.filtered) == 0 {
		m.dropdownIdx = -1
	} else if m.dropdownIdx >= len(m.filtered) {
		m.dropdownIdx = len(m.filtered) - 1
	}
}

func (m *createSessionModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m *createSessionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m, tea.Quit

		case "tab":
			if next, ok := m.nextField(); ok {
				cmd := m.setFocus(next)
				return m, cmd
			}

			return m, nil

		case "shift+tab":
			if previous, ok := m.previousField(); ok {
				cmd := m.setFocus(previous)
				return m, cmd
			}

			return m, nil

		case "enter":
			switch m.focus {
			case createFieldName:
				if strings.TrimSpace(m.nameInput.Value()) == "" {
					return m, nil
				}

				cmd := m.setFocus(createFieldRepo)

				return m, cmd
			case createFieldRepo:
				if m.showDropdown && m.dropdownIdx >= 0 && m.dropdownIdx < len(m.filtered) {
					m.repoInput.SetValue(m.filtered[m.dropdownIdx].Path)
					m.showDropdown = false
					m.dropdownIdx = -1
					m.updateFiltered()

					return m, nil
				}
				// Don't advance (or submit) on an empty repo — keep focus here so
				// the blocking field stays visible, matching the name field's
				// validate-before-advance behaviour.
				if strings.TrimSpace(m.repoInput.Value()) == "" {
					return m, nil
				}

				if len(m.agents) > 0 {
					cmd := m.setFocus(createFieldAgent)
					return m, cmd
				}

				cmd := m.trySubmit()

				return m, cmd
			case createFieldAgent:
				cmd := m.trySubmit()
				return m, cmd
			case createFieldLabels:
				cmd := m.trySubmit()
				return m, cmd
			}

		case "down":
			if m.focus == createFieldRepo && m.showDropdown && len(m.filtered) > 0 {
				m.dropdownIdx++
				if m.dropdownIdx >= len(m.filtered) {
					m.dropdownIdx = len(m.filtered) - 1
				}

				return m, nil
			}

			if m.focus == createFieldAgent && len(m.agents) > 0 {
				m.agentIdx = (m.agentIdx + 1) % len(m.agents)
				return m, nil
			}

		case "up":
			if m.focus == createFieldRepo && m.showDropdown {
				m.dropdownIdx--
				if m.dropdownIdx < -1 {
					m.dropdownIdx = -1
				}

				return m, nil
			}

			if m.focus == createFieldAgent && len(m.agents) > 0 {
				m.agentIdx--
				if m.agentIdx < 0 {
					m.agentIdx = len(m.agents) - 1
				}

				return m, nil
			}

		case "left":
			if m.focus == createFieldAgent && len(m.agents) > 0 {
				m.agentIdx--
				if m.agentIdx < 0 {
					m.agentIdx = len(m.agents) - 1
				}

				return m, nil
			}

		case "right":
			if m.focus == createFieldAgent && len(m.agents) > 0 {
				m.agentIdx = (m.agentIdx + 1) % len(m.agents)
				return m, nil
			}

		case " ", "space":
			if m.focus == createFieldName {
				var cmd tea.Cmd

				m.nameInput, cmd = m.nameInput.Update(tea.KeyPressMsg{Code: '-', Text: "-"})

				return m, cmd
			}
		}
	}

	var cmd tea.Cmd

	switch m.focus {
	case createFieldName:
		m.nameInput, cmd = m.nameInput.Update(msg)
	case createFieldRepo:
		prevVal := m.repoInput.Value()

		m.repoInput, cmd = m.repoInput.Update(msg)
		if m.repoInput.Value() != prevVal {
			m.updateFiltered()
			m.showDropdown = len(m.filtered) > 0
			m.dropdownIdx = -1
		}
	case createFieldLabels:
		m.labelsInput, cmd = m.labelsInput.Update(msg)
	}

	return m, cmd
}

func (m *createSessionModel) View() tea.View {
	w := m.width

	h := m.height
	if w == 0 || h == 0 {
		return tea.NewView("")
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPurple)
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBlue)
	dimStyle := lipgloss.NewStyle().Foreground(colorFaint)

	var content strings.Builder
	content.WriteString(titleStyle.Render("Create Session"))
	content.WriteString("\n\n")
	content.WriteString(labelStyle.Render("Name: "))
	content.WriteString(m.nameInput.View())
	content.WriteString("\n\n")
	content.WriteString(labelStyle.Render("Repo: "))
	content.WriteString(m.repoInput.View())

	if m.showDropdown && m.focus == createFieldRepo && len(m.filtered) > 0 {
		content.WriteString("\n")

		maxShow := min(8, len(m.filtered))

		start := 0
		if m.dropdownIdx >= maxShow {
			start = m.dropdownIdx - maxShow + 1
		}

		end := start + maxShow
		if end > len(m.filtered) {
			end = len(m.filtered)
			start = max(0, end-maxShow)
		}

		if start > 0 {
			fmt.Fprintf(&content, "\n  ↑ %d more", start)
		}

		for i := start; i < end; i++ {
			r := m.filtered[i]

			prefix := "  "
			if i == m.dropdownIdx {
				prefix = "▸ "
			}

			display := r.Name + "  " + dimStyle.Render(shortenPath(r.Path))
			content.WriteString("\n" + prefix + display)
		}

		if remaining := len(m.filtered) - end; remaining > 0 {
			fmt.Fprintf(&content, "\n  ↓ %d more", remaining)
		}
	}

	if len(m.agents) > 0 {
		selStyle := lipgloss.NewStyle().Bold(true).Foreground(colorGreen)

		content.WriteString("\n\n")
		content.WriteString(labelStyle.Render("Agent: "))

		parts := make([]string, len(m.agents))
		for i, a := range m.agents {
			switch {
			case i == m.agentIdx && m.focus == createFieldAgent:
				parts[i] = selStyle.Render("‹ " + a + " ›")
			case i == m.agentIdx:
				parts[i] = selStyle.Render(a)
			default:
				parts[i] = dimStyle.Render(a)
			}
		}

		content.WriteString(strings.Join(parts, "  "))
	}

	content.WriteString("\n\n")
	content.WriteString(labelStyle.Render("Labels: "))
	content.WriteString(m.labelsInput.View())
	content.WriteString("\n")
	content.WriteString(dimStyle.Render("comma-separated; tab here before confirming to add labels"))

	content.WriteString("\n\n")

	var hint string

	switch {
	case m.focus == createFieldAgent:
		// On the agent field both ↑↓ and ←→ cycle the selection.
		hint = "tab labels  ↑↓ ←→ cycle agent  enter confirm  esc cancel"
	case m.focus == createFieldLabels:
		hint = "shift+tab previous field  enter confirm  esc cancel"
	case len(m.agents) > 0:
		hint = "tab next field  ↑↓ suggestions  ←→ agent  enter confirm  esc cancel"
	default:
		hint = "tab next field  ↑↓ suggestions  enter confirm  esc cancel"
	}

	content.WriteString(dimStyle.Render(hint))

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

// RunCreateInput launches a bubbletea prompt for creating a session.
// Returns (name, repoPath, agent, labels) or empty values on cancel.
func RunCreateInput(defaultRepo string, repos []RepoSuggestion, agents []string, defaultAgent string) (string, string, string, []string) {
	m := newCreateSessionModel(defaultRepo, repos, agents, defaultAgent)
	p := tea.NewProgram(m)

	final, err := p.Run()
	if err != nil {
		return "", "", "", nil
	}

	result, ok := final.(*createSessionModel)
	if !ok || !result.done {
		return "", "", "", nil
	}

	name := strings.TrimSpace(result.nameInput.Value())

	repo := strings.TrimSpace(result.repoInput.Value())
	if repo != "" {
		repo = expandPath(repo)
	}

	return name, repo, result.selectedAgent(), result.selectedLabels()
}
