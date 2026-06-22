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
		addRepo(s.RepoPath)
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
)

type createSessionModel struct {
	nameInput textinput.Model
	repoInput textinput.Model
	repos     []RepoSuggestion
	filtered  []RepoSuggestion
	focus     int
	done      bool
	width     int
	height    int

	showDropdown bool
	dropdownIdx  int
}

func newCreateSessionModel(defaultRepo string, repos []RepoSuggestion) createSessionModel {
	ni := textinput.New()
	ni.Placeholder = "session-name"
	ni.Focus()
	ni.CharLimit = 64
	ni.SetWidth(40)

	ri := textinput.New()
	ri.Placeholder = "/path/to/repo"
	ri.CharLimit = 256
	ri.SetWidth(40)
	if defaultRepo != "" {
		ri.SetValue(defaultRepo)
	}

	m := createSessionModel{
		nameInput: ni,
		repoInput: ri,
		repos:     repos,
		focus:     createFieldName,
	}
	m.updateFiltered()
	return m
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
	if m.dropdownIdx >= len(m.filtered) {
		m.dropdownIdx = max(0, len(m.filtered)-1)
	}
}

func (m createSessionModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m createSessionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			if m.focus == createFieldName {
				m.nameInput.Blur()
				m.repoInput.Focus()
				m.focus = createFieldRepo
				m.showDropdown = len(m.filtered) > 0
				m.dropdownIdx = -1
				return m, textinput.Blink
			}
			return m, nil

		case "shift+tab":
			if m.focus == createFieldRepo {
				m.repoInput.Blur()
				m.nameInput.Focus()
				m.focus = createFieldName
				m.showDropdown = false
				return m, textinput.Blink
			}
			return m, nil

		case "enter":
			if m.focus == createFieldName {
				if strings.TrimSpace(m.nameInput.Value()) == "" {
					return m, nil
				}
				m.nameInput.Blur()
				m.repoInput.Focus()
				m.focus = createFieldRepo
				m.showDropdown = len(m.filtered) > 0
				m.dropdownIdx = -1
				return m, textinput.Blink
			}
			if m.focus == createFieldRepo {
				if m.showDropdown && m.dropdownIdx >= 0 && m.dropdownIdx < len(m.filtered) {
					m.repoInput.SetValue(m.filtered[m.dropdownIdx].Path)
					m.showDropdown = false
					m.dropdownIdx = -1
					m.updateFiltered()
					return m, nil
				}
				name := strings.TrimSpace(m.nameInput.Value())
				repo := strings.TrimSpace(m.repoInput.Value())
				if name == "" || repo == "" {
					return m, nil
				}
				m.done = true
				return m, tea.Quit
			}

		case "down":
			if m.focus == createFieldRepo && m.showDropdown && len(m.filtered) > 0 {
				m.dropdownIdx++
				if m.dropdownIdx >= len(m.filtered) {
					m.dropdownIdx = len(m.filtered) - 1
				}
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

		case " ", "space":
			if m.focus == createFieldName {
				m.nameInput.SetValue(m.nameInput.Value() + "-")
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	if m.focus == createFieldName {
		m.nameInput, cmd = m.nameInput.Update(msg)
	} else {
		prevVal := m.repoInput.Value()
		m.repoInput, cmd = m.repoInput.Update(msg)
		if m.repoInput.Value() != prevVal {
			m.updateFiltered()
			m.showDropdown = len(m.filtered) > 0
			m.dropdownIdx = -1
		}
	}
	return m, cmd
}

func (m createSessionModel) View() tea.View {
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
		for i := 0; i < maxShow; i++ {
			r := m.filtered[i]
			prefix := "  "
			if i == m.dropdownIdx {
				prefix = "▸ "
			}
			display := r.Name + "  " + dimStyle.Render(shortenPath(r.Path))
			content.WriteString("\n" + prefix + display)
		}
		if remaining := len(m.filtered) - maxShow; remaining > 0 {
			content.WriteString(fmt.Sprintf("\n  +%d more", remaining))
		}
	}

	content.WriteString("\n\n")
	content.WriteString(dimStyle.Render("tab next field  enter confirm  esc cancel"))

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
// Returns (name, repoPath) or ("", "") on cancel.
func RunCreateInput(defaultRepo string, repos []RepoSuggestion) (string, string) {
	m := newCreateSessionModel(defaultRepo, repos)
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		return "", ""
	}
	result, ok := final.(createSessionModel)
	if !ok || !result.done {
		return "", ""
	}
	name := strings.TrimSpace(result.nameInput.Value())
	repo := strings.TrimSpace(result.repoInput.Value())
	if repo != "" {
		repo = expandPath(repo)
	}
	return name, repo
}
