package client

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
)

type overlayState int

const (
	stateList overlayState = iota
	stateFilter
	stateConfirmDelete
	stateConfirmRestart
)

var (
	colorGreen   = lipgloss.Color("#00ff87")
	colorRed     = lipgloss.Color("#ff5f5f")
	colorBlue    = lipgloss.Color("#87afff")
	colorGold    = lipgloss.Color("#FFD700")
	colorPurple  = lipgloss.Color("#7B61FF")
	colorDim     = lipgloss.Color("#626262")
	colorFaint   = lipgloss.Color("#444444")
	colorPreview = lipgloss.Color("#555555")
	colorPanel   = lipgloss.Color("#1a1a1a")
)

type sessionItem struct {
	info protocol.SessionInfo
}

func (s sessionItem) Title() string       { return s.info.Name }
func (s sessionItem) Description() string { return "" }
func (s sessionItem) FilterValue() string { return s.info.Name + " " + s.info.RepoName }

type groupHeader struct {
	name  string
	count int
}

func (g groupHeader) Title() string       { return g.name }
func (g groupHeader) Description() string { return "" }
func (g groupHeader) FilterValue() string { return "" }

type columnWidths struct {
	name   int
	status int
	branch int
	git    int
	last   int
}

func (cw columnWidths) totalWidth() int {
	// "  ★ ● " (6) + name + "  " + status + "  " + branch + "  " + git + "  " + last + margin(4)
	return 6 + cw.name + 2 + cw.status + 2 + cw.branch + 2 + cw.git + 2 + cw.last + 4
}

func pad(s string, width int) string {
	if n := width - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func displayBranch(branch, name string) string {
	stripped := branch
	if p := strings.SplitN(branch, "/", 3); len(p) == 3 {
		stripped = p[2]
	}
	if stripped == name {
		return "—"
	}
	return stripped
}

func displayGit(dirty bool, unpushed int) string {
	if !dirty && unpushed == 0 {
		return "clean"
	}
	var parts []string
	if dirty {
		parts = append(parts, "M")
	}
	if unpushed > 0 {
		parts = append(parts, fmt.Sprintf("↑%d", unpushed))
	}
	return strings.Join(parts, " ")
}

func displayLastActive(s protocol.SessionInfo, currentSessionID string) string {
	if s.ID == currentSessionID {
		return "now"
	}
	ts := s.LastAttachedAt
	if ts == "" {
		ts = s.CreatedAt
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return ShortDuration(time.Since(t))
	}
	return ""
}

func shortenPath(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
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

func filterSessions(sessions []protocol.SessionInfo, query string) []protocol.SessionInfo {
	query = strings.TrimSpace(query)
	if query == "" {
		return sessions
	}
	terms := strings.Fields(strings.ToLower(query))
	var result []protocol.SessionInfo
	for _, s := range sessions {
		matchStr := buildMatchString(s)
		allMatch := true
		for _, term := range terms {
			if !strings.Contains(matchStr, term) {
				allMatch = false
				break
			}
		}
		if allMatch {
			result = append(result, s)
		}
	}
	return result
}

func buildMatchString(s protocol.SessionInfo) string {
	parts := []string{
		strings.ToLower(s.Name),
		strings.ToLower(s.RepoName),
		strings.ToLower(s.Branch),
		strings.ToLower(s.Status),
		strings.ToLower(s.AgentStatus),
		strings.ToLower(s.Agent),
	}
	if s.Dirty {
		parts = append(parts, "dirty", "modified")
	} else {
		parts = append(parts, "clean")
	}
	if s.UnpushedCount > 0 {
		parts = append(parts, "unpushed")
	}
	return strings.Join(parts, " ")
}

func computeColumnWidths(sessions []protocol.SessionInfo, currentSessionID string) columnWidths {
	var cw columnWidths
	for _, s := range sessions {
		if n := lipgloss.Width(s.Name); n > cw.name {
			cw.name = n
		}
		status := s.Status
		if s.AgentStatus != "" && s.Status == "running" {
			status = s.AgentStatus
			if status == "active" && s.ToolName != "" {
				status = fmt.Sprintf("active(%s)", s.ToolName)
			}
		}
		if n := lipgloss.Width(status); n > cw.status {
			cw.status = n
		}
		branch := displayBranch(s.Branch, s.Name)
		if n := lipgloss.Width(branch); n > cw.branch {
			cw.branch = n
		}
		git := displayGit(s.Dirty, s.UnpushedCount)
		if n := lipgloss.Width(git); n > cw.git {
			cw.git = n
		}
		last := displayLastActive(s, currentSessionID)
		if n := lipgloss.Width(last); n > cw.last {
			cw.last = n
		}
	}
	if cw.name < 7 {
		cw.name = 7
	}
	if cw.status < 6 {
		cw.status = 6
	}
	if cw.branch < 6 {
		cw.branch = 6
	}
	if cw.git < 3 {
		cw.git = 3
	}
	if cw.last < 4 {
		cw.last = 4
	}
	return cw
}

// compactDelegate renders each item on a single line with aligned columns.
type compactDelegate struct {
	cols             columnWidths
	currentSessionID string
}

func (d compactDelegate) Height() int                         { return 1 }
func (d compactDelegate) Spacing() int                        { return 0 }
func (d compactDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d compactDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	selected := index == m.Index()
	width := m.Width()

	if gh, ok := item.(groupHeader); ok {
		style := lipgloss.NewStyle().Bold(true).Foreground(colorPurple)
		line := style.Render(fmt.Sprintf("▸ %s (%d)", gh.name, gh.count))
		_, _ = fmt.Fprint(w, line)
		return
	}

	si, ok := item.(sessionItem)
	if !ok {
		return
	}

	dim := lipgloss.NewStyle().Foreground(colorDim)
	isCurrent := si.info.ID == d.currentSessionID

	indicator := "●"
	indicatorColor := colorGreen
	switch si.info.Status {
	case "stopped":
		indicator = "○"
		indicatorColor = colorDim
	case "errored":
		indicator = "✗"
		indicatorColor = colorRed
	}
	styledIndicator := lipgloss.NewStyle().Foreground(indicatorColor).Render(indicator)

	currentMark := "  "
	if isCurrent {
		currentMark = lipgloss.NewStyle().Foreground(colorGold).Render("★") + " "
	}

	name := pad(si.info.Name, d.cols.name)

	status := si.info.Status
	if si.info.AgentStatus != "" && si.info.Status == "running" {
		status = si.info.AgentStatus
		if status == "active" && si.info.ToolName != "" {
			status = fmt.Sprintf("active(%s)", si.info.ToolName)
		}
	}
	statusRendered := pad(status, d.cols.status)
	switch status {
	case "active", "running":
		statusRendered = lipgloss.NewStyle().Foreground(colorGreen).Render(statusRendered)
	case "approval":
		statusRendered = lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render(statusRendered)
	case "ready":
		statusRendered = lipgloss.NewStyle().Foreground(colorBlue).Render(statusRendered)
	case "errored":
		statusRendered = lipgloss.NewStyle().Foreground(colorRed).Render(statusRendered)
	default:
		statusRendered = dim.Render(statusRendered)
	}

	branchVal := displayBranch(si.info.Branch, si.info.Name)
	if branchVal == "" && si.info.InPlace {
		branchVal = "(in-place)"
	}
	branchRendered := dim.Render(pad(branchVal, d.cols.branch))

	gitVal := displayGit(si.info.Dirty, si.info.UnpushedCount)
	var gitRendered string
	if gitVal == "clean" {
		gitRendered = dim.Render(pad(gitVal, d.cols.git))
	} else {
		gitRendered = pad(gitVal, d.cols.git)
	}

	last := displayLastActive(si.info, d.currentSessionID)
	lastRendered := dim.Render(pad(last, d.cols.last))

	sep := dim.Render("  ")

	selPrefix := "  "
	if selected {
		selPrefix = "> "
	}

	line := fmt.Sprintf("%s%s%s %s%s%s%s%s%s%s%s%s",
		selPrefix, currentMark, styledIndicator,
		name, sep, statusRendered, sep, branchRendered, sep, gitRendered, sep, lastRendered)

	if selected {
		line = lipgloss.NewStyle().Bold(true).Render(line)
	}

	if width > 0 && lipgloss.Width(line) > width {
		line = ansi.Truncate(line, width, "")
	}

	_, _ = fmt.Fprint(w, line)
}

type previewMsg struct {
	sessionID string
	content   string
}

type deleteResultMsg struct {
	sessionID string
	err       error
}

type overlayModel struct {
	list             list.Model
	filterInput      textinput.Model
	state            overlayState
	selected         *protocol.SessionInfo
	width            int
	height           int
	contentWidth     int
	cols             columnWidths
	currentSessionID string
	allSessions      []protocol.SessionInfo
	fetchPreview     func(sessionID string) string
	deleteSession    func(sessionID string) error
	previewContent   string
	previewSessionID string
	profile          string
}

// OverlayResult holds the outcome of the overlay interaction.
type OverlayResult struct {
	Action    string
	SessionID string
}

func SortSessions(sessions []protocol.SessionInfo) {
	sort.SliceStable(sessions, func(i, j int) bool {
		si, sj := sessions[i], sessions[j]

		ri := si.Status == "running"
		rj := sj.Status == "running"
		if ri != rj {
			return ri
		}

		return si.Name < sj.Name
	})
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
	for _, g := range groups {
		SortSessions(g)
	}

	var items []list.Item
	for _, repo := range repoOrder {
		g := groups[repo]
		items = append(items, groupHeader{name: repo, count: len(g)})
		for _, s := range g {
			items = append(items, sessionItem{info: s})
		}
	}
	return items
}

func newOverlayModel(sessions []protocol.SessionInfo, currentSessionID string, fetchPreview func(sessionID string) string, deleteSession func(sessionID string) error) overlayModel {
	cols := computeColumnWidths(sessions, currentSessionID)
	contentWidth := cols.totalWidth()
	items := buildGroupedItems(sessions)

	delegate := compactDelegate{cols: cols, currentSessionID: currentSessionID}
	l := list.New(items, delegate, contentWidth, len(items)+4)
	l.Title = ""
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.KeyMap.Quit = key.NewBinding(key.WithKeys())

	cursorSet := false
	if currentSessionID != "" {
		for i, item := range items {
			if si, ok := item.(sessionItem); ok && si.info.ID == currentSessionID {
				l.Select(i)
				cursorSet = true
				break
			}
		}
	}
	if !cursorSet {
		if _, ok := l.SelectedItem().(groupHeader); ok {
			l.CursorDown()
		}
	}

	fi := textinput.New()
	fi.Placeholder = "filter..."
	fi.CharLimit = 64
	fi.SetWidth(contentWidth)

	return overlayModel{
		list:             l,
		filterInput:      fi,
		state:            stateList,
		contentWidth:     contentWidth,
		cols:             cols,
		currentSessionID: currentSessionID,
		allSessions:      sessions,
		fetchPreview:     fetchPreview,
		deleteSession:    deleteSession,
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
		if item, ok := m.list.SelectedItem().(sessionItem); ok && item.info.ID == msg.sessionID {
			m.previewContent = msg.content
			if strings.TrimSpace(msg.content) != "" {
				m.previewSessionID = msg.sessionID
			}
		}
		return m, nil

	case deleteResultMsg:
		if msg.err != nil {
			m.state = stateList
			return m, nil
		}
		var newSessions []protocol.SessionInfo
		for _, s := range m.allSessions {
			if s.ID != msg.sessionID {
				newSessions = append(newSessions, s)
			}
		}
		m.allSessions = newSessions
		if len(newSessions) == 0 {
			return m, tea.Quit
		}
		curIdx := m.list.Index()
		m.cols = computeColumnWidths(newSessions, m.currentSessionID)
		m.contentWidth = m.cols.totalWidth()
		items := buildGroupedItems(newSessions)
		m.list.SetItems(items)
		m.list.SetDelegate(compactDelegate{cols: m.cols, currentSessionID: m.currentSessionID})
		if curIdx >= len(items) {
			curIdx = len(items) - 1
		}
		m.list.Select(curIdx)
		if _, ok := m.list.SelectedItem().(groupHeader); ok {
			m.list.CursorDown()
			if _, ok := m.list.SelectedItem().(groupHeader); ok {
				m.list.CursorUp()
			}
		}
		m.state = stateList
		return m, m.fetchPreviewCmd()

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		panelWidth := min(m.contentWidth+4, msg.Width-4)
		listHeight := min(len(m.list.Items())+4, msg.Height-14)
		if listHeight < 4 {
			listHeight = 4
		}
		m.list.SetSize(panelWidth-4, listHeight)
		return m, nil

	case tea.KeyPressMsg:
		switch m.state {
		case stateFilter:
			switch msg.String() {
			case "esc":
				m.state = stateList
				m.filterInput.Blur()
				m.filterInput.SetValue("")
				items := buildGroupedItems(m.allSessions)
				m.list.SetItems(items)
				found := false
				if m.currentSessionID != "" {
					for i, item := range items {
						if si, ok := item.(sessionItem); ok && si.info.ID == m.currentSessionID {
							m.list.Select(i)
							found = true
							break
						}
					}
				}
				if !found {
					if _, ok := m.list.SelectedItem().(groupHeader); ok {
						m.list.CursorDown()
					}
				}
				return m, m.fetchPreviewCmd()
			case "enter":
				m.state = stateList
				m.filterInput.Blur()
				return m, m.fetchPreviewCmd()
			default:
				var cmd tea.Cmd
				m.filterInput, cmd = m.filterInput.Update(msg)
				filtered := filterSessions(m.allSessions, m.filterInput.Value())
				items := buildGroupedItems(filtered)
				m.list.SetItems(items)
				m.list.Select(0)
				if len(items) > 0 {
					if _, ok := m.list.SelectedItem().(groupHeader); ok {
						m.list.CursorDown()
					}
				}
				return m, cmd
			}

		case stateConfirmDelete:
			switch msg.String() {
			case "y", "Y":
				if item, ok := m.list.SelectedItem().(sessionItem); ok && m.deleteSession != nil {
					sid := item.info.ID
					deleteFn := m.deleteSession
					return m, func() tea.Msg {
						return deleteResultMsg{sessionID: sid, err: deleteFn(sid)}
					}
				}
				m.state = stateList
				return m, nil
			default:
				m.state = stateList
				return m, nil
			}

		case stateConfirmRestart:
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

			case "r":
				if item, ok := m.list.SelectedItem().(sessionItem); ok && item.info.Status == "stopped" {
					m.state = stateConfirmRestart
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

			case "tab":
				items := m.list.Items()
				cur := m.list.Index()
				for i := cur + 1; i < len(items); i++ {
					if _, ok := items[i].(groupHeader); ok {
						if i+1 < len(items) {
							m.list.Select(i + 1)
							return m, m.fetchPreviewCmd()
						}
					}
				}
				for i := 0; i <= cur; i++ {
					if _, ok := items[i].(groupHeader); ok {
						if i+1 < len(items) {
							m.list.Select(i + 1)
							return m, m.fetchPreviewCmd()
						}
					}
				}
				return m, nil

			case "shift+tab":
				items := m.list.Items()
				cur := m.list.Index()
				currentGroupHeader := -1
				for i := cur; i >= 0; i-- {
					if _, ok := items[i].(groupHeader); ok {
						currentGroupHeader = i
						break
					}
				}
				prevGroupHeader := -1
				for i := currentGroupHeader - 1; i >= 0; i-- {
					if _, ok := items[i].(groupHeader); ok {
						prevGroupHeader = i
						break
					}
				}
				if prevGroupHeader == -1 {
					for i := len(items) - 1; i > cur; i-- {
						if _, ok := items[i].(groupHeader); ok {
							prevGroupHeader = i
							break
						}
					}
				}
				if prevGroupHeader >= 0 && prevGroupHeader+1 < len(items) {
					m.list.Select(prevGroupHeader + 1)
					return m, m.fetchPreviewCmd()
				}
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m overlayModel) View() tea.View {
	w := m.width
	h := m.height
	if w == 0 || h == 0 {
		return tea.NewView("")
	}

	panelWidth := min(m.contentWidth+4, w-4)
	dim := lipgloss.NewStyle().Foreground(colorDim)

	var panelContent strings.Builder

	if m.state == stateFilter {
		panelContent.WriteString("Filter: ")
		panelContent.WriteString(m.filterInput.View())
		panelContent.WriteString("\n")
	} else {
		titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPurple)
		title := titleStyle.Render("Sessions")
		if m.profile != "" {
			dimStyle := lipgloss.NewStyle().Foreground(colorDim)
			title += " " + dimStyle.Render("["+m.profile+"]")
		}
		panelContent.WriteString(title)
		panelContent.WriteString("\n")
	}

	headerPrefix := "      "
	headerLine := fmt.Sprintf("%s%s  %s  %s  %s  %s",
		headerPrefix,
		pad("Session", m.cols.name),
		pad("Status", m.cols.status),
		pad("Branch", m.cols.branch),
		pad("Git", m.cols.git),
		"Last")
	panelContent.WriteString(dim.Render(headerLine))
	panelContent.WriteString("\n")
	sepLine := fmt.Sprintf("%s%s  %s  %s  %s  %s",
		headerPrefix,
		strings.Repeat("─", m.cols.name),
		strings.Repeat("─", m.cols.status),
		strings.Repeat("─", m.cols.branch),
		strings.Repeat("─", m.cols.git),
		strings.Repeat("─", m.cols.last))
	panelContent.WriteString(dim.Render(sepLine))
	panelContent.WriteString("\n")

	panelContent.WriteString(m.list.View())

	if item, ok := m.list.SelectedItem().(sessionItem); ok {
		s := item.info
		panelContent.WriteString("\n")

		var line1 []string
		if s.Branch != "" {
			branch := s.Branch
			if p := strings.SplitN(branch, "/", 3); len(p) == 3 {
				branch = p[2]
			}
			line1 = append(line1, "branch: "+branch)
		} else if s.InPlace {
			line1 = append(line1, "mode: in-place")
		}
		if s.BaseBranch != "" {
			line1 = append(line1, "base: "+s.BaseBranch)
		}
		if s.Agent != "" {
			line1 = append(line1, "agent: "+s.Agent)
		}
		if len(line1) > 0 {
			panelContent.WriteString(dim.Render(strings.Join(line1, "  ")))
		}

		var line2 []string
		if s.WorktreePath != "" {
			line2 = append(line2, shortenPath(s.WorktreePath))
		}
		if len(s.ID) >= 7 {
			line2 = append(line2, "id: "+s.ID[:7])
		}
		if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
			line2 = append(line2, "created "+ShortDuration(time.Since(t))+" ago")
		}
		if len(line2) > 0 {
			panelContent.WriteString("\n")
			panelContent.WriteString(dim.Render(strings.Join(line2, "  ")))
		}
	}

	switch m.state {
	case stateConfirmDelete:
		if item, ok := m.list.SelectedItem().(sessionItem); ok {
			s := item.info
			if s.Dirty || s.UnpushedCount > 0 {
				warnStyle := lipgloss.NewStyle().Foreground(colorRed).Bold(true)
				panelContent.WriteString("\n")
				panelContent.WriteString(warnStyle.Render("⚠ Session has unsaved work:"))
				if s.Dirty {
					panelContent.WriteString("\n")
					panelContent.WriteString(warnStyle.Render("  • Uncommitted changes"))
				}
				if s.UnpushedCount > 0 {
					panelContent.WriteString("\n")
					label := "commits"
					if s.UnpushedCount == 1 {
						label = "commit"
					}
					panelContent.WriteString(warnStyle.Render(fmt.Sprintf("  • %d unpushed %s", s.UnpushedCount, label)))
				}
			}
			panelContent.WriteString("\n")
			panelContent.WriteString(lipgloss.NewStyle().
				Foreground(colorRed).
				Render(fmt.Sprintf("Delete '%s'? [y/N]", s.Name)))
		}
	case stateConfirmRestart:
		if item, ok := m.list.SelectedItem().(sessionItem); ok {
			panelContent.WriteString("\n")
			panelContent.WriteString(lipgloss.NewStyle().
				Foreground(colorGreen).
				Render(fmt.Sprintf("Restart '%s'? [y/N]", item.info.Name)))
		}
	}

	helpStyle := lipgloss.NewStyle().Foreground(colorFaint)
	panelContent.WriteString("\n")
	panelContent.WriteString(helpStyle.Render("enter attach  / filter  tab group  x delete  r restart  q quit"))

	panel := lipgloss.NewStyle().
		Width(panelWidth).
		Background(colorPanel).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorFaint).
		Padding(0, 1).
		Render(panelContent.String())

	// --- Build background from preview scrollback ---
	dimStyle := lipgloss.NewStyle().Foreground(colorPreview)
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

	v := tea.NewView(strings.Join(bgLines, "\n"))
	v.AltScreen = true
	return v
}

// RunOverlay launches the bubbletea overlay listing sessions grouped by repo.
// currentSessionID highlights the session the user was just attached to.
// fetchPreview is called asynchronously to load scrollback for the selected session.
func RunOverlay(sessions []protocol.SessionInfo, currentSessionID string, fetchPreview func(sessionID string) string, deleteSession func(sessionID string) error, profile string) *OverlayResult {
	m := newOverlayModel(sessions, currentSessionID, fetchPreview, deleteSession)
	m.profile = profile
	p := tea.NewProgram(m)

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
		switch result.state {
		case stateConfirmDelete:
			action = "delete"
		case stateConfirmRestart:
			action = "restart"
		}
		return &OverlayResult{
			Action:    action,
			SessionID: result.selected.ID,
		}
	}

	return nil
}
