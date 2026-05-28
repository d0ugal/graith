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
	stateConfirmRestartAll
)

type viewMode int

const (
	viewAll viewMode = iota
	viewNeedsAttention
	viewActive
	viewStarred
)

var viewNames = []string{"All", "Needs Attention", "Active", "Starred"}

func (v viewMode) next() viewMode {
	return (v + 1) % viewMode(len(viewNames))
}

func (v viewMode) prev() viewMode {
	return (v + viewMode(len(viewNames)) - 1) % viewMode(len(viewNames))
}

func filterNeedsAttention(sessions []protocol.SessionInfo) []protocol.SessionInfo {
	var result []protocol.SessionInfo
	for _, s := range sessions {
		switch {
		case s.AgentStatus == "approval":
			result = append(result, s)
		case s.Status == "errored":
			result = append(result, s)
		case s.Status == "running" && s.AgentStatus == "ready":
			result = append(result, s)
		case s.Status == "stopped" && (s.Dirty || s.UnpushedCount > 0):
			result = append(result, s)
		}
	}
	sortByStatusAge(result)
	return result
}

func filterActive(sessions []protocol.SessionInfo) []protocol.SessionInfo {
	var result []protocol.SessionInfo
	for _, s := range sessions {
		if s.Status == "running" {
			result = append(result, s)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, result[i].CreatedAt)
		tj, _ := time.Parse(time.RFC3339, result[j].CreatedAt)
		return ti.After(tj)
	})
	return result
}

func filterStarred(sessions []protocol.SessionInfo) []protocol.SessionInfo {
	var result []protocol.SessionInfo
	for _, s := range sessions {
		if s.Starred {
			result = append(result, s)
		}
	}
	return result
}

func sortByStatusAge(sessions []protocol.SessionInfo) {
	sort.SliceStable(sessions, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, sessions[i].StatusChangedAt)
		tj, _ := time.Parse(time.RFC3339, sessions[j].StatusChangedAt)
		if ti.IsZero() && tj.IsZero() {
			return false
		}
		if ti.IsZero() {
			return true
		}
		if tj.IsZero() {
			return false
		}
		return ti.Before(tj)
	})
}

var (
	colorGreen   = lipgloss.Color("#00ff87")
	colorRed     = lipgloss.Color("#ff5f5f")
	colorBlue    = lipgloss.Color("#87afff")
	colorGold    = lipgloss.Color("#FFD700")
	colorPurple  = lipgloss.Color("#7B61FF")
	colorDim     = lipgloss.Color("#626262")
	colorFaint   = lipgloss.Color("#444444")
	colorYellow  = lipgloss.Color("#FFD75F")
	colorPreview = lipgloss.Color("#555555")
	colorPanel   = lipgloss.Color("#1a1a1a")
)

type sessionItem struct {
	info            protocol.SessionInfo
	treePrefix      string
	hasChildren     bool
	collapsed       bool
	descendantCount int
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
	name       int
	treeIndent int
	status     int
	summary    int
	git        int
	output     int
}

func (cw columnWidths) totalWidth() int {
	// "  ★▸● " (7) + treeIndent + name + "  " + status + "  " + summary + "  " + git + "  " + output + margin(4)
	return 7 + cw.treeIndent + cw.name + 2 + cw.status + 2 + cw.summary + 2 + cw.git + 2 + cw.output + 4
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

func displayLastOutput(s protocol.SessionInfo) string {
	ts := s.LastOutputAt
	if ts == "" {
		ts = s.CreatedAt
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return ShortDuration(time.Since(t))
	}
	return ""
}

const maxSummaryWidth = 40

func displaySummary(s protocol.SessionInfo) string {
	text := s.SummaryText
	if text == "" {
		return ""
	}
	if lipgloss.Width(text) > maxSummaryWidth {
		text = text[:maxSummaryWidth-1] + "…"
	}
	return text
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
		strings.ToLower(s.SummaryText),
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
		}
		if n := lipgloss.Width(status); n > cw.status {
			cw.status = n
		}
		summary := displaySummary(s)
		if n := lipgloss.Width(summary); n > cw.summary {
			cw.summary = n
		}
		git := displayGit(s.Dirty, s.UnpushedCount)
		if n := lipgloss.Width(git); n > cw.git {
			cw.git = n
		}
		output := displayLastOutput(s)
		if n := lipgloss.Width(output); n > cw.output {
			cw.output = n
		}
	}
	if cw.name < 7 {
		cw.name = 7
	}
	if cw.status < 6 {
		cw.status = 6
	}
	if cw.summary < 7 {
		cw.summary = 7
	}
	if cw.git < 3 {
		cw.git = 3
	}
	if cw.output < 6 {
		cw.output = 6
	}
	if cw.summary > maxSummaryWidth {
		cw.summary = maxSummaryWidth
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
	staleMarker := " "
	if si.info.ConfigStale {
		staleMarker = lipgloss.NewStyle().Foreground(colorYellow).Render("↻")
	}
	styledIndicator += staleMarker

	starredMark := " "
	if si.info.Starred {
		starredMark = lipgloss.NewStyle().Foreground(colorGold).Render("★")
	}
	currentMark := " "
	if isCurrent {
		currentMark = lipgloss.NewStyle().Foreground(colorGold).Render("▸")
	}

	treePrefixRendered := ""
	if si.treePrefix != "" {
		treePrefixRendered = dim.Render(si.treePrefix)
	}

	collapseIndicator := ""
	childSuffix := ""
	if si.hasChildren {
		if si.collapsed {
			collapseIndicator = dim.Render("▸ ")
			childSuffix = dim.Render(fmt.Sprintf(" (%d)", si.descendantCount))
		} else {
			collapseIndicator = dim.Render("▾ ")
		}
	}
	nameText := si.info.Name + childSuffix
	nameWidth := d.cols.treeIndent + d.cols.name - lipgloss.Width(si.treePrefix) - lipgloss.Width(collapseIndicator)
	name := collapseIndicator + pad(nameText, nameWidth)

	status := si.info.Status
	if si.info.AgentStatus != "" && si.info.Status == "running" {
		status = si.info.AgentStatus
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

	summaryVal := displaySummary(si.info)
	summaryRendered := pad(summaryVal, d.cols.summary)
	if si.info.SummaryFaded {
		summaryRendered = dim.Render(summaryRendered)
	}

	gitVal := displayGit(si.info.Dirty, si.info.UnpushedCount)
	var gitRendered string
	if gitVal == "clean" {
		gitRendered = dim.Render(pad(gitVal, d.cols.git))
	} else {
		gitRendered = pad(gitVal, d.cols.git)
	}

	outputVal := displayLastOutput(si.info)
	outputRendered := dim.Render(pad(outputVal, d.cols.output))

	sep := dim.Render("  ")

	selPrefix := "  "
	if selected {
		selPrefix = "> "
	}

	line := fmt.Sprintf("%s%s%s%s %s%s%s%s%s%s%s%s%s%s",
		selPrefix, starredMark, currentMark, styledIndicator,
		treePrefixRendered, name, sep, statusRendered, sep, summaryRendered, sep, gitRendered, sep, outputRendered)

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

type restartResultMsg struct {
	sessionID string
	err       error
}

type restartAllResultMsg struct {
	errors []error
}

type starResultMsg struct {
	sessionID string
	starred   bool
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
	view             viewMode
	fetchPreview     func(sessionID string) string
	deleteSession    func(sessionID string) error
	restartSession   func(sessionID string) error
	toggleStar       func(sessionID string, star bool) error
	previewContent   string
	previewSessionID string
	profile          string
	collapsed        map[string]bool
}

// OverlayResult holds the outcome of the overlay interaction.
type OverlayResult struct {
	Action    string
	SessionID string
	Collapsed map[string]bool
}

func SortSessions(sessions []protocol.SessionInfo) {
	sort.SliceStable(sessions, func(i, j int) bool {
		si, sj := sessions[i], sessions[j]

		if si.Starred != sj.Starred {
			return si.Starred
		}

		ri := si.Status == "running"
		rj := sj.Status == "running"
		if ri != rj {
			return ri
		}

		return si.Name < sj.Name
	})
}

func buildGroupedItems(sessions []protocol.SessionInfo, collapsed map[string]bool) []list.Item {
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
		g := groups[repo]
		items = append(items, groupHeader{name: repo, count: len(g)})

		idSet := make(map[string]bool, len(g))
		for _, s := range g {
			idSet[s.ID] = true
		}

		children := make(map[string][]protocol.SessionInfo)
		var roots []protocol.SessionInfo
		for _, s := range g {
			if s.ParentID == "" || s.ParentID == s.ID || !idSet[s.ParentID] {
				roots = append(roots, s)
			} else {
				children[s.ParentID] = append(children[s.ParentID], s)
			}
		}

		SortSessions(roots)
		for k := range children {
			SortSessions(children[k])
		}

		var countDescendants func(id string, seen map[string]bool) int
		countDescendants = func(id string, seen map[string]bool) int {
			kids := children[id]
			n := 0
			for _, kid := range kids {
				if !seen[kid.ID] {
					seen[kid.ID] = true
					n++
					n += countDescendants(kid.ID, seen)
				}
			}
			return n
		}

		visited := make(map[string]bool)
		var walk func(s protocol.SessionInfo, prefix, childPrefix string)
		walk = func(s protocol.SessionInfo, prefix, childPrefix string) {
			if visited[s.ID] {
				return
			}
			visited[s.ID] = true
			kids := children[s.ID]
			hasKids := len(kids) > 0
			isCollapsed := collapsed[s.ID] && hasKids
			desc := 0
			if hasKids {
				desc = countDescendants(s.ID, map[string]bool{s.ID: true})
			}
			items = append(items, sessionItem{
				info:            s,
				treePrefix:      prefix,
				hasChildren:     hasKids,
				collapsed:       isCollapsed,
				descendantCount: desc,
			})
			if isCollapsed {
				var markVisited func(id string)
				markVisited = func(id string) {
					for _, kid := range children[id] {
						if !visited[kid.ID] {
							visited[kid.ID] = true
							markVisited(kid.ID)
						}
					}
				}
				markVisited(s.ID)
				return
			}
			for i, kid := range kids {
				if i == len(kids)-1 {
					walk(kid, childPrefix+"└── ", childPrefix+"    ")
				} else {
					walk(kid, childPrefix+"├── ", childPrefix+"│   ")
				}
			}
		}

		for _, root := range roots {
			walk(root, "", "")
		}
		// Render any cycle members that weren't reachable from roots.
		for _, s := range g {
			if !visited[s.ID] {
				walk(s, "", "")
			}
		}
	}
	return items
}

func maxTreeIndentFromItems(items []list.Item) int {
	maxIndent := 0
	for _, item := range items {
		if si, ok := item.(sessionItem); ok {
			if w := lipgloss.Width(si.treePrefix); w > maxIndent {
				maxIndent = w
			}
		}
	}
	return maxIndent
}

func newOverlayModel(sessions []protocol.SessionInfo, currentSessionID string, fetchPreview func(sessionID string) string, deleteSession func(sessionID string) error, collapsed map[string]bool) overlayModel {
	if collapsed == nil {
		collapsed = make(map[string]bool)
	}
	items := buildGroupedItems(sessions, collapsed)
	cols := computeColumnWidths(sessions, currentSessionID)
	cols.treeIndent = maxTreeIndentFromItems(items)
	contentWidth := cols.totalWidth()

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
		if !cursorSet {
			parentOf := make(map[string]string)
			for _, s := range sessions {
				if s.ParentID != "" && s.ParentID != s.ID {
					parentOf[s.ID] = s.ParentID
				}
			}
			seen := map[string]bool{currentSessionID: true}
			cur := parentOf[currentSessionID]
			for cur != "" && !seen[cur] {
				seen[cur] = true
				for i, item := range items {
					if si, ok := item.(sessionItem); ok && si.info.ID == cur {
						l.Select(i)
						cursorSet = true
						break
					}
				}
				if cursorSet {
					break
				}
				cur = parentOf[cur]
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
		collapsed:        collapsed,
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

func (m *overlayModel) rebuildForView() {
	filtered := m.sessionsForView()
	if m.filterInput.Value() != "" {
		filtered = filterSessions(filtered, m.filterInput.Value())
	}

	var items []list.Item
	if m.view == viewAll {
		items = buildGroupedItems(filtered, m.collapsed)
	} else {
		for _, s := range filtered {
			items = append(items, sessionItem{info: s})
		}
	}

	m.cols = computeColumnWidths(filtered, m.currentSessionID)
	if m.view == viewAll {
		m.cols.treeIndent = maxTreeIndentFromItems(items)
	}
	m.contentWidth = m.cols.totalWidth()
	m.list.SetItems(items)
	m.list.SetDelegate(compactDelegate{cols: m.cols, currentSessionID: m.currentSessionID})
	m.list.Select(0)
	if len(items) > 0 {
		if _, ok := m.list.SelectedItem().(groupHeader); ok {
			m.list.CursorDown()
		}
	}
}

func (m *overlayModel) selectSessionByID(id string) {
	for i, item := range m.list.Items() {
		if si, ok := item.(sessionItem); ok && si.info.ID == id {
			m.list.Select(i)
			return
		}
	}
	// ID not visible — walk up the parent chain to find a visible ancestor.
	parentOf := make(map[string]string)
	for _, s := range m.allSessions {
		if s.ParentID != "" && s.ParentID != s.ID {
			parentOf[s.ID] = s.ParentID
		}
	}
	seen := map[string]bool{id: true}
	cur := parentOf[id]
	for cur != "" && !seen[cur] {
		seen[cur] = true
		for i, item := range m.list.Items() {
			if si, ok := item.(sessionItem); ok && si.info.ID == cur {
				m.list.Select(i)
				return
			}
		}
		cur = parentOf[cur]
	}
	if _, ok := m.list.SelectedItem().(groupHeader); ok {
		m.list.CursorDown()
	}
}

func (m *overlayModel) parentsWithChildren() []string {
	childOf := make(map[string]bool)
	idSet := make(map[string]bool)
	for _, s := range m.allSessions {
		idSet[s.ID] = true
	}
	for _, s := range m.allSessions {
		if s.ParentID != "" && s.ParentID != s.ID && idSet[s.ParentID] {
			childOf[s.ParentID] = true
		}
	}
	var parents []string
	for id := range childOf {
		parents = append(parents, id)
	}
	return parents
}

func (m *overlayModel) sessionsForView() []protocol.SessionInfo {
	switch m.view {
	case viewNeedsAttention:
		return filterNeedsAttention(m.allSessions)
	case viewActive:
		return filterActive(m.allSessions)
	case viewStarred:
		return filterStarred(m.allSessions)
	default:
		return m.allSessions
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

	case starResultMsg:
		if msg.err == nil {
			for i, s := range m.allSessions {
				if s.ID == msg.sessionID {
					m.allSessions[i].Starred = msg.starred
					break
				}
			}
			m.rebuildForView()
		}
		return m, m.fetchPreviewCmd()

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
		m.rebuildForView()
		if curIdx >= len(m.list.Items()) {
			curIdx = len(m.list.Items()) - 1
		}
		if curIdx >= 0 {
			m.list.Select(curIdx)
		}
		if _, ok := m.list.SelectedItem().(groupHeader); ok {
			m.list.CursorDown()
			if _, ok := m.list.SelectedItem().(groupHeader); ok {
				m.list.CursorUp()
			}
		}
		m.state = stateList
		return m, m.fetchPreviewCmd()

	case restartResultMsg:
		m.state = stateList
		return m, m.fetchPreviewCmd()

	case restartAllResultMsg:
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
				m.rebuildForView()
				return m, m.fetchPreviewCmd()
			case "enter":
				m.state = stateList
				m.filterInput.Blur()
				return m, m.fetchPreviewCmd()
			default:
				var cmd tea.Cmd
				m.filterInput, cmd = m.filterInput.Update(msg)
				viewFiltered := m.sessionsForView()
				filtered := filterSessions(viewFiltered, m.filterInput.Value())
				var items []list.Item
				if m.view == viewAll {
					items = buildGroupedItems(filtered, m.collapsed)
				} else {
					for _, s := range filtered {
						items = append(items, sessionItem{info: s})
					}
				}
				m.list.SetItems(items)
				m.list.Select(0)
				if len(items) > 0 {
					if _, ok := m.list.SelectedItem().(groupHeader); ok {
						m.list.CursorDown()
					}
				}
				if _, ok := m.list.SelectedItem().(sessionItem); !ok {
					m.previewContent = ""
					m.previewSessionID = ""
				}
				return m, tea.Batch(cmd, m.fetchPreviewCmd())
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
				if item, ok := m.list.SelectedItem().(sessionItem); ok && m.restartSession != nil {
					sid := item.info.ID
					restartFn := m.restartSession
					return m, func() tea.Msg {
						return restartResultMsg{sessionID: sid, err: restartFn(sid)}
					}
				}
				m.state = stateList
				return m, nil
			default:
				m.state = stateList
				return m, nil
			}

		case stateConfirmRestartAll:
			switch msg.String() {
			case "y", "Y":
				if m.restartSession != nil {
					restartFn := m.restartSession
					sessions := m.sessionsForView()
					return m, func() tea.Msg {
						var errs []error
						for _, s := range sessions {
							if err := restartFn(s.ID); err != nil {
								errs = append(errs, err)
							}
						}
						return restartAllResultMsg{errors: errs}
					}
				}
				m.state = stateList
				return m, nil
			default:
				m.state = stateList
				return m, nil
			}

		case stateList:
			switch msg.String() {
			case "q", "esc":
				return m, tea.Quit

			case "left", "h":
				m.view = m.view.prev()
				m.rebuildForView()
				return m, m.fetchPreviewCmd()

			case "right", "l":
				m.view = m.view.next()
				m.rebuildForView()
				return m, m.fetchPreviewCmd()

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
				if _, ok := m.list.SelectedItem().(sessionItem); ok {
					m.state = stateConfirmRestart
				}
				return m, nil

			case "R":
				m.state = stateConfirmRestartAll
				return m, nil

			case "s":
				if item, ok := m.list.SelectedItem().(sessionItem); ok && m.toggleStar != nil {
					sid := item.info.ID
					newStarred := !item.info.Starred
					toggleFn := m.toggleStar
					return m, func() tea.Msg {
						return starResultMsg{sessionID: sid, starred: newStarred, err: toggleFn(sid, newStarred)}
					}
				}
				return m, nil

			case " ", "space":
				if item, ok := m.list.SelectedItem().(sessionItem); ok && item.hasChildren {
					sid := item.info.ID
					if m.collapsed[sid] {
						delete(m.collapsed, sid)
					} else {
						m.collapsed[sid] = true
					}
					m.rebuildForView()
					m.selectSessionByID(sid)
					return m, m.fetchPreviewCmd()
				}
				return m, nil

			case "C":
				if m.view != viewAll {
					return m, nil
				}
				parents := m.parentsWithChildren()
				if len(parents) == 0 {
					return m, nil
				}
				allCollapsed := true
				for _, id := range parents {
					if !m.collapsed[id] {
						allCollapsed = false
						break
					}
				}
				curSID := ""
				if item, ok := m.list.SelectedItem().(sessionItem); ok {
					curSID = item.info.ID
				}
				if allCollapsed {
					for _, id := range parents {
						delete(m.collapsed, id)
					}
				} else {
					for _, id := range parents {
						m.collapsed[id] = true
					}
				}
				m.rebuildForView()
				if curSID != "" {
					m.selectSessionByID(curSID)
				}
				return m, m.fetchPreviewCmd()

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
		dimArrow := lipgloss.NewStyle().Foreground(colorDim)
		activeViewStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPurple)

		var titleParts []string
		for i, name := range viewNames {
			if viewMode(i) == m.view {
				titleParts = append(titleParts, activeViewStyle.Render(name))
			} else {
				titleParts = append(titleParts, dimArrow.Render(name))
			}
		}
		title := dimArrow.Render("◂ ") + strings.Join(titleParts, dimArrow.Render(" │ ")) + dimArrow.Render(" ▸")
		if m.profile != "" {
			dimStyle := lipgloss.NewStyle().Foreground(colorDim)
			title += " " + dimStyle.Render("["+m.profile+"]")
		}
		panelContent.WriteString(title)
		panelContent.WriteString("\n")
	}

	headerPrefix := "       "
	nameColWidth := m.cols.treeIndent + m.cols.name
	headerLine := fmt.Sprintf("%s%s  %s  %s  %s  %s",
		headerPrefix,
		pad("Session", nameColWidth),
		pad("Status", m.cols.status),
		pad("Summary", m.cols.summary),
		pad("Git", m.cols.git),
		"Output")
	panelContent.WriteString(dim.Render(headerLine))
	panelContent.WriteString("\n")
	sepLine := fmt.Sprintf("%s%s  %s  %s  %s  %s",
		headerPrefix,
		strings.Repeat("─", nameColWidth),
		strings.Repeat("─", m.cols.status),
		strings.Repeat("─", m.cols.summary),
		strings.Repeat("─", m.cols.git),
		strings.Repeat("─", m.cols.output))
	panelContent.WriteString(dim.Render(sepLine))
	panelContent.WriteString("\n")

	if len(m.list.Items()) == 0 && m.view != viewAll {
		emptyStyle := lipgloss.NewStyle().Foreground(colorDim).Italic(true)
		emptyMsg := ""
		switch m.view {
		case viewNeedsAttention:
			emptyMsg = "Nothing needs your attention"
		case viewActive:
			emptyMsg = "No active sessions"
		case viewStarred:
			emptyMsg = "No starred sessions"
		}
		panelContent.WriteString("\n  ")
		panelContent.WriteString(emptyStyle.Render(emptyMsg))
		panelContent.WriteString("\n")
	} else {
		panelContent.WriteString(m.list.View())
	}

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
		if s.ConfigStale {
			panelContent.WriteString("\n")
			panelContent.WriteString(lipgloss.NewStyle().Foreground(colorYellow).Render("config stale — restart to apply changes"))
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
	case stateConfirmRestartAll:
		count := len(m.sessionsForView())
		panelContent.WriteString("\n")
		panelContent.WriteString(lipgloss.NewStyle().
			Foreground(colorGreen).
			Render(fmt.Sprintf("Restart all %d sessions? [y/N]", count)))
	}

	helpStyle := lipgloss.NewStyle().Foreground(colorFaint)
	panelContent.WriteString("\n")
	panelContent.WriteString(helpStyle.Render("enter attach  ◂▸ view  / filter  tab group  s star  space fold  C fold-all  x delete  r/R restart  q quit"))

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
func RunOverlay(sessions []protocol.SessionInfo, currentSessionID string, fetchPreview func(sessionID string) string, deleteSession func(sessionID string) error, restartSession func(sessionID string) error, toggleStar func(sessionID string, star bool) error, profile string, collapsed map[string]bool) *OverlayResult {
	m := newOverlayModel(sessions, currentSessionID, fetchPreview, deleteSession, collapsed)
	m.restartSession = restartSession
	m.toggleStar = toggleStar
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

	overlayResult := &OverlayResult{
		Collapsed: result.collapsed,
	}

	if result.selected != nil {
		overlayResult.SessionID = result.selected.ID
		overlayResult.Action = "attach"
		if result.state == stateConfirmDelete {
			overlayResult.Action = "delete"
		}
		return overlayResult
	}

	return overlayResult
}
