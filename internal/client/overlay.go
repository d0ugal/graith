package client

import (
	"fmt"
	"image/color"
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
	stateConfirmStop
	stateConfirmRestart
	stateRestartMenu
	stateRestartingAll
	stateCreate
)

type viewMode int

const (
	viewAll viewMode = iota
	viewNeedsAttention
	viewActive
	viewStarred
	viewScenario
	viewDeleted
)

var viewNames = []string{"All", "Needs Attention", "Active", "Starred", "Scenarios", "Deleted"}

// sortDeleted orders soft-deleted sessions most-recently-deleted first.
func sortDeleted(sessions []protocol.SessionInfo) []protocol.SessionInfo {
	result := make([]protocol.SessionInfo, len(sessions))
	copy(result, sessions)

	sort.SliceStable(result, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, result[i].DeletedAt)
		tj, _ := time.Parse(time.RFC3339, result[j].DeletedAt)

		return ti.After(tj)
	})

	return result
}

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
		case s.Status == "stopped" && !s.Mirror && (s.Dirty || s.UnpushedCount > 0):
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
	// colorSelectBg is the background of the highlighted row in the picker, so
	// the whole selected line stands out rather than just the "> " cursor. Dark
	// and purple-tinted to echo the accent used for the selected name and the
	// active view label.
	colorSelectBg = lipgloss.Color("#2f2b45")
)

type sessionItem struct {
	info            protocol.SessionInfo
	treePrefix      string
	hasChildren     bool
	collapsed       bool
	descendantCount int
	sessionIndex    int
}

func assignSessionIndices(items []list.Item) {
	idx := 0

	for i, item := range items {
		if si, ok := item.(sessionItem); ok {
			idx++
			si.sessionIndex = idx
			items[i] = si
		}
	}
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
	pr         int
	output     int
	// trailing holds the computed width of every ShowTUI column keyed by
	// SessionColumn.Key. The named fields above mirror the well-known columns
	// for convenience (and test stability); trailing is the generic lookup the
	// renderer uses so columns added to the registry are sized automatically.
	trailing map[string]int
}

// col returns the computed TUI width for a registry column by key.
func (cw columnWidths) col(key string) int {
	return cw.trailing[key]
}

func (cw columnWidths) totalWidth() int {
	// "  N ★▸● " (9) + treeIndent + name, then "  " + width for every TUI column
	// (sourced from the shared registry so a new ShowTUI column extends the
	// panel automatically rather than being silently truncated), + margin(4).
	width := 9 + cw.treeIndent + cw.name + 4
	for _, c := range tuiColumns() {
		width += 2 + cw.col(c.Key)
	}

	return width
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

// ciCounts formats the "<passed>/<total>" progress fragment for a CI badge,
// reporting false when no count is available (Total == 0) so callers fall back
// to a plain indicator. A live poll that resolves a pending/failing state always
// carries a count, so this fallback is the defensive path (no CI badge recorded
// yet, or a badge set without counts).
func ciCounts(ci *protocol.CIInfo) (string, bool) {
	if ci == nil || ci.Total <= 0 {
		return "", false
	}

	return fmt.Sprintf("%d/%d", ci.Passed, ci.Total), true
}

// displayPR is the compact per-row PR/CI token for the overlay list, e.g.
// "#56 19/22 1✗" (CI failing), "#56 16/22" (CI running), "#56 ⚠" (conflict),
// "#1615 ✓" (passing), "#583 merged". While CI is running/failing the count of
// passed vs total checks replaces the bare indicator so progress is visible;
// it falls back to "·"/"✗" when no count is available. The review decision is a
// separate column (displayReview) so it can carry its own colour independent of
// the CI/conflict signal.
func displayPR(s protocol.SessionInfo) string {
	if s.PullRequest == nil {
		return "—"
	}

	pr := s.PullRequest

	out := fmt.Sprintf("#%d", pr.Number)
	switch pr.State {
	case "merged":
		return out + " merged"
	case "closed":
		return out + " closed"
	case "draft":
		out += "d"
	}

	if pr.Conflicting {
		return out + " ⚠" // merge conflict — highest-priority signal
	}

	if s.CI != nil {
		counts, haveCounts := ciCounts(s.CI)
		switch s.CI.State {
		case "passing":
			return out + " ✓"
		case "failing":
			if haveCounts && len(s.CI.FailingChecks) > 0 {
				return fmt.Sprintf("%s %s %d✗", out, counts, len(s.CI.FailingChecks))
			}

			return out + " ✗"
		case "pending":
			if haveCounts {
				return out + " " + counts
			}

			return out + " ·"
		}
	}

	return out
}

// reviewActiveDecision returns a PR's review decision only while it is live (an
// open/draft PR). A merged/closed PR's decision is stale and suppressed, mirroring
// displayPR/prColor (issue #773). Empty when there is no PR or no decision.
func reviewActiveDecision(s protocol.SessionInfo) string {
	pr := s.PullRequest
	if pr == nil || pr.State == "merged" || pr.State == "closed" {
		return ""
	}

	return pr.ReviewDecision
}

// displayReview is the compact TUI review-decision glyph: "a" (approved),
// "c" (changes requested), "r" (review required), or "—" when there is no live
// decision. Its colour comes from reviewColor, not prColor.
func displayReview(s protocol.SessionInfo) string {
	switch reviewActiveDecision(s) {
	case "approved":
		return "a"
	case "changes_requested":
		return "c"
	case "review_required":
		return "r"
	default:
		return "—"
	}
}

// reviewColor colours the review indicator by decision, independent of the PR/CI
// token colour: approved = green, changes_requested = red, review_required =
// dim/grey (not yet reviewed — deliberately NOT green, which reads as "good").
// Returns nil when there is no live decision.
func reviewColor(s protocol.SessionInfo) color.Color {
	switch reviewActiveDecision(s) {
	case "approved":
		return colorGreen
	case "changes_requested":
		return colorRed
	case "review_required":
		return colorDim
	default:
		return nil
	}
}

// prColor returns the color for a PR token by its worst signal.
func prColor(s protocol.SessionInfo) color.Color {
	pr := s.PullRequest
	if pr == nil {
		return colorDim
	}

	// A merged/closed PR is a terminal state — its stale CI badge (or a stale
	// CONFLICTING mergeable state) must not paint the token. Mirror displayPR's
	// ordering exactly: terminal state is checked before conflict and CI,
	// because resolvePR stops fetching checks once a PR leaves open/draft and
	// writePRState keeps the last-known CI badge (issue #773).
	if pr.State == "merged" || pr.State == "closed" {
		return colorDim
	}

	if pr.Conflicting {
		return colorRed
	}

	if s.CI != nil {
		switch s.CI.State {
		case "failing":
			return colorRed
		case "passing":
			return colorGreen
		case "pending":
			return colorYellow
		}
	}

	return colorBlue
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
		strings.ToLower(s.Status),
		strings.ToLower(s.AgentStatus),
		strings.ToLower(s.Agent),
		strings.ToLower(s.SummaryText),
	}
	if !s.Mirror {
		parts = append(parts, strings.ToLower(s.Branch))
		if s.Dirty {
			parts = append(parts, "dirty", "modified")
		} else {
			parts = append(parts, "clean")
		}

		if s.UnpushedCount > 0 {
			parts = append(parts, "unpushed")
		}
	}

	return strings.Join(parts, " ")
}

func computeColumnWidths(sessions []protocol.SessionInfo, _ string) columnWidths {
	tuiCols := tuiColumns()
	widths := make(map[string]int, len(tuiCols))

	var nameWidth int

	for _, s := range sessions {
		if n := lipgloss.Width(s.Name); n > nameWidth {
			nameWidth = n
		}

		for _, c := range tuiCols {
			if n := lipgloss.Width(c.TUIValue(s)); n > widths[c.Key] {
				widths[c.Key] = n
			}
		}
	}

	if nameWidth < 7 {
		nameWidth = 7
	}

	for _, c := range tuiCols {
		if widths[c.Key] < c.MinWidth {
			widths[c.Key] = c.MinWidth
		}

		if c.MaxWidth > 0 && widths[c.Key] > c.MaxWidth {
			widths[c.Key] = c.MaxWidth
		}
	}

	return columnWidths{
		name:     nameWidth,
		status:   widths["status"],
		summary:  widths["summary"],
		git:      widths["git"],
		pr:       widths["pr"],
		output:   widths["output"],
		trailing: widths,
	}
}

// tuiColumns returns the registry columns shown in the TUI picker, in order.
func tuiColumns() []SessionColumn {
	var cols []SessionColumn

	for _, c := range SessionColumns() {
		if c.ShowTUI {
			cols = append(cols, c)
		}
	}

	return cols
}

// compactDelegate renders each item on a single line with aligned columns.
type compactDelegate struct {
	cols             columnWidths
	currentSessionID string
	shortcutKeys     []rune
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

	numberLabel := "  "
	if si.sessionIndex >= 1 && si.sessionIndex <= len(d.shortcutKeys) {
		numberLabel = dim.Render(string(d.shortcutKeys[si.sessionIndex-1])) + " "
	}

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

	collapseIndicator := "  "
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

	sep := dim.Render("  ")

	selPrefix := "  "
	if selected {
		selPrefix = "> "
	}

	// Render each TUI column from the shared registry so the CLI table and the
	// picker stay in sync. Every trailing column contributes "  <cell>".
	var b strings.Builder

	b.WriteString(selPrefix)
	b.WriteString(numberLabel)
	b.WriteString(starredMark)
	b.WriteString(currentMark)
	b.WriteString(styledIndicator)
	b.WriteString(" ")
	b.WriteString(treePrefixRendered)
	b.WriteString(name)

	for _, c := range tuiColumns() {
		b.WriteString(sep)
		b.WriteString(c.TUIStyle(si.info).Render(pad(c.TUIValue(si.info), d.cols.col(c.Key))))
	}

	line := b.String()

	if width > 0 && lipgloss.Width(line) > width {
		line = ansi.Truncate(line, width, "")
	}

	if selected {
		line = highlightSelectedRow(line, width)
	}

	_, _ = fmt.Fprint(w, line)
}

// highlightSelectedRow makes the picker's selected row stand out by giving the
// whole line a subtle background (and keeping it bold), so the eye doesn't have
// to trace the "> " cursor across a wide terminal to find the current row.
//
// A plain background wrap won't do: each column is pre-rendered with its own
// foreground style and ends in a full SGR reset ("\x1b[m"), which also clears
// the background — so the highlight would stop at the first styled segment. We
// pad the line to the full width and re-open the background after every reset
// so it spans the whole row.
func highlightSelectedRow(line string, width int) string {
	open := selectRowOpen()
	if open == "" {
		// Defensive: nothing to open (a renderer emitting no SGR at all).
		return line
	}

	if width > 0 {
		if vis := lipgloss.Width(line); vis < width {
			line += strings.Repeat(" ", width-vis)
		}
	}

	// lipgloss v2 emits the short reset "\x1b[m"; the "\x1b[0m" replace is
	// defensive against non-lipgloss ANSI (e.g. ansi.Truncate's terminator or
	// hand-written escapes) and must not be removed as dead. The two patterns
	// don't overlap, so their order doesn't matter.
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+open)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+open)

	return open + line + "\x1b[0m"
}

// selectRowOpen returns the SGR sequence that opens the selected-row style
// (bold + background). lipgloss v2's Render always emits full-fidelity truecolor
// here; downsampling to a limited palette (or stripping under a no-color
// profile) happens downstream in Bubble Tea's output layer, exactly as it does
// for every other cell in the row. The "" return is a defensive fallback for a
// renderer that emits no SGR at all.
func selectRowOpen() string {
	probe := lipgloss.NewStyle().Bold(true).Background(colorSelectBg).Render("x")
	if i := strings.IndexByte(probe, 'x'); i > 0 {
		return probe[:i]
	}

	return ""
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

type stopResultMsg struct {
	sessionID string
	err       error
}

type restartOneResultMsg struct {
	index int
	err   error
}

type starResultMsg struct {
	sessionID string
	starred   bool
	err       error
}

type restoreResultMsg struct {
	sessionID string
	err       error
}

type refreshTickMsg struct{}

type refreshSessionsMsg struct {
	sessions []protocol.SessionInfo
	deleted  []protocol.SessionInfo
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
	refreshSessions  func() []protocol.SessionInfo
	refreshDeleted   func() []protocol.SessionInfo
	deleteSession    func(sessionID string) error
	restartSession   func(sessionID string) error
	stopSession      func(sessionID string) error
	toggleStar       func(sessionID string, star bool) error
	restoreSession   func(sessionID string) error
	deletedSessions  []protocol.SessionInfo
	previewContent   string
	previewSessionID string
	profile          string
	collapsed        map[string]bool
	shortcutKeys     []rune

	restartQueue  []string
	restartIdx    int
	restartErrors []error

	// stoppedCurrent is set when the user stops the session they are currently
	// attached to. RunOverlay turns this into a distinct result action so the
	// attach loop exits instead of reattaching (and auto-resuming) it.
	stoppedCurrent bool

	createModel     *createSessionModel
	createName      string
	createRepoPath  string
	createAgent     string
	createDone      bool
	repoSuggestions []RepoSuggestion
	agents          []string
	defaultAgent    string

	// Configurable list-mode keybindings. Populated with defaults by
	// newOverlayModel and overridden from config by RunOverlay.
	keyDelete string
	keyResume string
	keySearch string
}

// OverlayKeys carries the configurable picker keybindings from [keybindings].
// Empty fields fall back to the built-in defaults (see newOverlayModel).
type OverlayKeys struct {
	DeleteSession string
	ResumeSession string
	Search        string
}

// applyKeys overrides the model's list-mode keybindings with any non-empty
// values from keys, leaving the built-in defaults in place otherwise.
func (m *overlayModel) applyKeys(keys OverlayKeys) {
	if keys.DeleteSession != "" {
		m.keyDelete = keys.DeleteSession
	}

	if keys.ResumeSession != "" {
		m.keyResume = keys.ResumeSession
	}

	if keys.Search != "" {
		m.keySearch = keys.Search
	}
}

func (m *overlayModel) resizeList() {
	if m.width == 0 || m.height == 0 {
		return
	}

	reserve := 10
	if m.state == stateConfirmDelete || m.state == stateConfirmStop || m.state == stateConfirmRestart || m.state == stateRestartMenu || m.state == stateRestartingAll {
		reserve = 14
	}

	panelWidth := min(m.contentWidth+4, m.width-4)

	listHeight := min(len(m.list.Items())+4, m.height-reserve)
	if listHeight < 4 {
		listHeight = 4
	}

	m.list.SetSize(panelWidth-4, listHeight)
}

// OverlayResult holds the outcome of the overlay interaction.
type OverlayResult struct {
	Action         string
	SessionID      string
	CreateName     string
	CreateRepoPath string
	CreateAgent    string
	Collapsed      map[string]bool
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

	var (
		systemSessions []protocol.SessionInfo
		repoOrder      []string
	)

	seen := map[string]bool{}

	for _, s := range sessions {
		if s.SystemKind != "" {
			systemSessions = append(systemSessions, s)
			continue
		}

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

	if len(systemSessions) > 0 {
		repoOrder = append([]string{"System"}, repoOrder...)
		groups["System"] = systemSessions
	}

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

func buildScenarioGroupedItems(sessions []protocol.SessionInfo, collapsed map[string]bool) []list.Item {
	type scenarioGroup struct {
		name     string
		sessions []protocol.SessionInfo
	}

	scenarioMap := map[string]*scenarioGroup{}

	var (
		scenarioOrder []string
		ungrouped     []protocol.SessionInfo
	)

	for _, s := range sessions {
		sid := s.ScenarioID
		if sid == "" {
			ungrouped = append(ungrouped, s)
			continue
		}

		if _, ok := scenarioMap[sid]; !ok {
			scenarioOrder = append(scenarioOrder, sid)
			scenarioMap[sid] = &scenarioGroup{sessions: nil}
		}

		scenarioMap[sid].sessions = append(scenarioMap[sid].sessions, s)
	}

	for _, sid := range scenarioOrder {
		g := scenarioMap[sid]
		if len(g.sessions) > 0 {
			name := sid

			for _, s := range g.sessions {
				if s.ScenarioName != "" {
					name = s.ScenarioName
					break
				}
			}

			g.name = name
		}
	}

	var items []list.Item

	for _, sid := range scenarioOrder {
		g := scenarioMap[sid]
		if g.name == "" {
			continue
		}

		SortSessions(g.sessions)

		running := 0
		stopped := 0
		errored := 0

		for _, s := range g.sessions {
			switch s.Status {
			case "running":
				running++
			case "stopped":
				stopped++
			case "errored":
				errored++
			}
		}

		var status string

		switch {
		case errored > 0:
			status = " (errored)"
		case running > 0 && stopped > 0:
			status = " (partial)"
		case running == len(g.sessions):
			status = " (running)"
		case stopped == len(g.sessions):
			status = " (stopped)"
		}

		items = append(items, groupHeader{name: g.name + status, count: len(g.sessions)})
		for _, s := range g.sessions {
			items = append(items, sessionItem{info: s})
		}
	}

	if len(ungrouped) > 0 {
		SortSessions(ungrouped)

		items = append(items, groupHeader{name: "(no scenario)", count: len(ungrouped)})
		for _, s := range ungrouped {
			items = append(items, sessionItem{info: s})
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

func newOverlayModel(sessions []protocol.SessionInfo, currentSessionID string, fetchPreview func(sessionID string) string, deleteSession func(sessionID string) error, collapsed map[string]bool, shortcutKeys []rune) overlayModel {
	if collapsed == nil {
		collapsed = make(map[string]bool)
	}

	items := buildGroupedItems(sessions, collapsed)
	assignSessionIndices(items)

	cols := computeColumnWidths(sessions, currentSessionID)
	cols.treeIndent = maxTreeIndentFromItems(items)
	contentWidth := cols.totalWidth()

	delegate := compactDelegate{cols: cols, currentSessionID: currentSessionID, shortcutKeys: shortcutKeys}
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
		shortcutKeys:     shortcutKeys,
		keyDelete:        "x",
		keyResume:        "R",
		keySearch:        "/",
	}
}

func (m overlayModel) Init() tea.Cmd {
	return tea.Batch(m.fetchPreviewCmd(), m.refreshTickCmd())
}

func (m overlayModel) refreshTickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return refreshTickMsg{}
	})
}

func (m overlayModel) refreshSessionsCmd() tea.Cmd {
	if m.refreshSessions == nil {
		return nil
	}

	fetch := m.refreshSessions
	fetchDeleted := m.refreshDeleted

	return func() tea.Msg {
		msg := refreshSessionsMsg{sessions: fetch()}
		if fetchDeleted != nil {
			msg.deleted = fetchDeleted()
		}

		return msg
	}
}

// beginRestartQueue builds a restart queue from the current view, keeping only
// sessions for which match returns true, and starts processing it. If nothing
// matches or restart is unavailable, it returns to the list.
func (m overlayModel) beginRestartQueue(match func(protocol.SessionInfo) bool) (tea.Model, tea.Cmd) {
	if m.restartSession != nil {
		var queue []string

		for _, s := range m.visibleSessions() {
			if match(s) {
				queue = append(queue, s.ID)
				// Restarting the current session resumes it, so it should no
				// longer count as "stopped on exit".
				if s.ID == m.currentSessionID {
					m.stoppedCurrent = false
				}
			}
		}

		if len(queue) > 0 {
			m.restartQueue = queue
			m.restartIdx = 0
			m.restartErrors = nil
			m.state = stateRestartingAll
			m.resizeList()

			return m, m.restartNextCmd()
		}
	}

	m.state = stateList
	m.resizeList()

	return m, nil
}

func (m overlayModel) restartNextCmd() tea.Cmd {
	if m.restartIdx >= len(m.restartQueue) {
		return nil
	}

	sid := m.restartQueue[m.restartIdx]
	idx := m.restartIdx
	restartFn := m.restartSession

	return func() tea.Msg {
		return restartOneResultMsg{index: idx, err: restartFn(sid)}
	}
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

	switch m.view {
	case viewAll:
		items = buildGroupedItems(filtered, m.collapsed)
	case viewScenario:
		items = buildScenarioGroupedItems(filtered, m.collapsed)
	default:
		for _, s := range filtered {
			items = append(items, sessionItem{info: s})
		}
	}

	assignSessionIndices(items)

	m.cols = computeColumnWidths(filtered, m.currentSessionID)
	if m.view == viewAll || m.view == viewScenario {
		m.cols.treeIndent = maxTreeIndentFromItems(items)
	}

	m.contentWidth = m.cols.totalWidth()
	m.list.SetItems(items)
	m.list.SetDelegate(compactDelegate{cols: m.cols, currentSessionID: m.currentSessionID, shortcutKeys: m.shortcutKeys})
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
	case viewDeleted:
		return sortDeleted(m.deletedSessions)
	default:
		return m.allSessions
	}
}

// visibleSessions returns the sessions currently shown in the list: the view
// filter plus any active text filter. Bulk actions (the restart menu) operate
// on this set so they match what the user can see.
func (m *overlayModel) visibleSessions() []protocol.SessionInfo {
	sessions := m.sessionsForView()
	if m.filterInput.Value() != "" {
		sessions = filterSessions(sessions, m.filterInput.Value())
	}

	return sessions
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

	case restoreResultMsg:
		if msg.err == nil {
			// The session is no longer deleted; drop it from the deleted list and
			// re-fetch so it reappears in the live views.
			var remaining []protocol.SessionInfo

			for _, s := range m.deletedSessions {
				if s.ID != msg.sessionID {
					remaining = append(remaining, s)
				}
			}

			m.deletedSessions = remaining
			m.rebuildForView()
		}

		return m, tea.Batch(m.fetchPreviewCmd(), m.refreshSessionsCmd())

	case deleteResultMsg:
		if msg.err != nil {
			m.state = stateList
			m.resizeList()

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
		m.resizeList()

		return m, m.fetchPreviewCmd()

	case restartResultMsg:
		m.state = stateList
		m.resizeList()

		return m, m.fetchPreviewCmd()

	case stopResultMsg:
		if msg.err == nil {
			for i, s := range m.allSessions {
				if s.ID == msg.sessionID {
					m.allSessions[i].Status = "stopped"
					break
				}
			}

			if msg.sessionID == m.currentSessionID {
				m.stoppedCurrent = true
			}
			// Preserve the cursor on the session that was stopped rather than
			// resetting to the top of the list.
			curSID := msg.sessionID
			if item, ok := m.list.SelectedItem().(sessionItem); ok {
				curSID = item.info.ID
			}

			m.rebuildForView()
			m.selectSessionByID(curSID)
		}

		m.state = stateList
		m.resizeList()

		return m, m.fetchPreviewCmd()

	case restartOneResultMsg:
		if m.state != stateRestartingAll {
			return m, nil
		}

		if msg.err != nil {
			m.restartErrors = append(m.restartErrors, msg.err)
		}

		m.restartIdx = msg.index + 1
		if m.restartIdx >= len(m.restartQueue) {
			m.state = stateList
			m.restartQueue = nil
			m.restartIdx = 0
			m.restartErrors = nil
			m.resizeList()

			return m, m.fetchPreviewCmd()
		}

		return m, m.restartNextCmd()

	case refreshTickMsg:
		if m.state != stateList && m.state != stateFilter {
			return m, m.refreshTickCmd()
		}

		return m, m.refreshSessionsCmd()

	case refreshSessionsMsg:
		if msg.sessions == nil {
			return m, m.refreshTickCmd()
		}

		curSID := ""
		if item, ok := m.list.SelectedItem().(sessionItem); ok {
			curSID = item.info.ID
		}

		m.allSessions = msg.sessions
		m.deletedSessions = msg.deleted
		m.rebuildForView()
		m.resizeList()

		if curSID != "" {
			m.selectSessionByID(curSID)
		}

		return m, tea.Batch(m.fetchPreviewCmd(), m.refreshTickCmd())

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeList()

		if m.state == stateCreate && m.createModel != nil {
			m.createModel.width = msg.Width
			m.createModel.height = msg.Height
		}

		return m, nil
	}

	if m.state == stateCreate && m.createModel != nil {
		updated, cmd := m.createModel.Update(msg)

		cm, ok := updated.(createSessionModel)
		if !ok {
			m.createModel = nil
			m.state = stateList
			m.resizeList()

			return m, m.fetchPreviewCmd()
		}

		m.createModel = &cm
		if cm.done {
			m.createName = strings.TrimSpace(cm.nameInput.Value())

			m.createRepoPath = strings.TrimSpace(cm.repoInput.Value())
			if m.createRepoPath != "" {
				m.createRepoPath = expandPath(m.createRepoPath)
			}

			m.createAgent = cm.selectedAgent()
			m.createDone = true

			return m, tea.Quit
		}

		if keyMsg, isKey := msg.(tea.KeyPressMsg); isKey {
			if keyMsg.String() == "esc" || keyMsg.String() == "ctrl+c" {
				m.createModel = nil
				m.state = stateList
				m.resizeList()

				return m, m.fetchPreviewCmd()
			}
		}

		return m, cmd
	}

	if msg, ok := msg.(tea.KeyPressMsg); ok {
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
				m.filterInput.Blur()

				if item, ok := m.list.SelectedItem().(sessionItem); ok {
					m.selected = &item.info
					return m, tea.Quit
				}

				m.state = stateList

				return m, m.fetchPreviewCmd()
			default:
				var cmd tea.Cmd

				m.filterInput, cmd = m.filterInput.Update(msg)
				viewFiltered := m.sessionsForView()
				filtered := filterSessions(viewFiltered, m.filterInput.Value())

				var items []list.Item

				switch m.view {
				case viewAll:
					items = buildGroupedItems(filtered, m.collapsed)
				case viewScenario:
					items = buildScenarioGroupedItems(filtered, m.collapsed)
				default:
					for _, s := range filtered {
						items = append(items, sessionItem{info: s})
					}
				}

				assignSessionIndices(items)
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
				m.resizeList()

				return m, nil
			default:
				m.state = stateList
				m.resizeList()

				return m, nil
			}

		case stateConfirmStop:
			switch msg.String() {
			case "y", "Y":
				if item, ok := m.list.SelectedItem().(sessionItem); ok && m.stopSession != nil {
					sid := item.info.ID
					stopFn := m.stopSession

					return m, func() tea.Msg {
						return stopResultMsg{sessionID: sid, err: stopFn(sid)}
					}
				}

				m.state = stateList
				m.resizeList()

				return m, nil
			default:
				m.state = stateList
				m.resizeList()

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
				m.resizeList()

				return m, nil
			default:
				m.state = stateList
				m.resizeList()

				return m, nil
			}

		case stateRestartMenu:
			switch msg.String() {
			case "a", "A":
				return m.beginRestartQueue(func(protocol.SessionInfo) bool { return true })
			case "o", "O":
				return m.beginRestartQueue(func(s protocol.SessionInfo) bool { return s.ConfigStale })
			case "s", "S":
				return m.beginRestartQueue(func(s protocol.SessionInfo) bool { return s.Status == "stopped" })
			default:
				m.state = stateList
				m.resizeList()

				return m, nil
			}

		case stateRestartingAll:
			if msg.String() == "esc" {
				end := m.restartIdx + 1
				if end > len(m.restartQueue) {
					end = len(m.restartQueue)
				}

				m.restartQueue = m.restartQueue[:end]
			}

			return m, nil

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
				// In the Deleted view, the only action is restore — attaching to a
				// trashed session makes no sense.
				if m.view == viewDeleted {
					if item, ok := m.list.SelectedItem().(sessionItem); ok && m.restoreSession != nil {
						sid := item.info.ID
						restoreFn := m.restoreSession

						return m, func() tea.Msg {
							return restoreResultMsg{sessionID: sid, err: restoreFn(sid)}
						}
					}

					return m, nil
				}

				if item, ok := m.list.SelectedItem().(sessionItem); ok {
					m.selected = &item.info
				}

				return m, tea.Quit

			// keyDelete/keyResume/keySearch are the configurable picker keys.
			// This is a first-match-wins switch, so a user who rebinds one onto
			// an existing literal (e.g. search onto "q") gets whichever case
			// appears first. Defaults (x/R//) don't collide with any literal.
			case m.keyDelete:
				// The Deleted view offers only restore (enter); destructive/mutating
				// actions are disabled there.
				if m.view == viewDeleted {
					return m, nil
				}

				if _, ok := m.list.SelectedItem().(sessionItem); ok {
					m.state = stateConfirmDelete
					m.resizeList()
				}

				return m, nil

			case "r":
				if m.view == viewDeleted {
					return m, nil
				}

				if _, ok := m.list.SelectedItem().(sessionItem); ok {
					m.state = stateConfirmRestart
					m.resizeList()
				}

				return m, nil

			case m.keyResume:
				if m.view == viewDeleted {
					return m, nil
				}

				m.state = stateRestartMenu
				m.resizeList()

				return m, nil

			case "S":
				if m.view == viewDeleted {
					return m, nil
				}

				if _, ok := m.list.SelectedItem().(sessionItem); ok {
					m.state = stateConfirmStop
					m.resizeList()
				}

				return m, nil

			case "s":
				if m.view == viewDeleted {
					return m, nil
				}

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

			case m.keySearch:
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

			case "n":
				defaultRepo := ""
				if item, ok := m.list.SelectedItem().(sessionItem); ok {
					defaultRepo = item.info.RepoPath
				} else if m.currentSessionID != "" {
					for _, s := range m.allSessions {
						if s.ID == m.currentSessionID {
							defaultRepo = s.RepoPath
							break
						}
					}
				}

				cm := newCreateSessionModel(defaultRepo, m.repoSuggestions, m.agents, m.defaultAgent)
				cm.width = m.width
				cm.height = m.height
				m.createModel = &cm
				m.state = stateCreate

				return m, textinput.Blink

			default:
				if pressed := []rune(msg.String()); len(pressed) == 1 {
					for idx, k := range m.shortcutKeys {
						if pressed[0] == k {
							target := idx + 1
							for i, item := range m.list.Items() {
								if si, ok := item.(sessionItem); ok && si.sessionIndex == target {
									m.list.Select(i)
									m.selected = &si.info

									return m, tea.Quit
								}
							}

							return m, nil
						}
					}
				}
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

	if m.state == stateCreate && m.createModel != nil {
		cm := *m.createModel
		cm.width = w
		cm.height = h

		return cm.View()
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

	headerPrefix := "         "
	nameColWidth := m.cols.treeIndent + m.cols.name

	// Build the header and separator rows from the shared column registry so
	// they always match the cells rendered per row. The Session/name column is
	// special (tree indentation), so it is prepended here; the trailing header
	// is padded to its width except for the last column, which flows freely.
	headerCells := []string{pad("Session", nameColWidth)}
	sepCells := []string{strings.Repeat("─", nameColWidth)}

	tuiCols := tuiColumns()
	for i, c := range tuiCols {
		w := m.cols.col(c.Key)
		if i == len(tuiCols)-1 {
			headerCells = append(headerCells, c.Header)
		} else {
			headerCells = append(headerCells, pad(c.Header, w))
		}

		sepCells = append(sepCells, strings.Repeat("─", w))
	}

	headerLine := headerPrefix + strings.Join(headerCells, "  ")
	panelContent.WriteString(dim.Render(headerLine))
	panelContent.WriteString("\n")

	sepLine := headerPrefix + strings.Join(sepCells, "  ")
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

		if !s.Mirror {
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

		if s.PullRequest != nil {
			pr := s.PullRequest
			prLine := fmt.Sprintf("PR #%d %s", pr.Number, pr.State)
			lineColor := lipgloss.NewStyle().Foreground(prColor(s))

			// A merged/closed PR is terminal: its conflict/CI badges are stale
			// (resolvePR stops fetching checks once a PR leaves open/draft and
			// writePRState keeps the last-known values), so suppress them here just
			// as displayPR/cliPR do — otherwise the preview shows a stale
			// "CI: pending 16/22" on a PR the row already reports as merged (#773).
			if pr.State != "merged" && pr.State != "closed" {
				if pr.Conflicting {
					prLine += "  ⚠ merge conflict"
				}

				if s.CI != nil {
					counts, haveCounts := ciCounts(s.CI)
					switch s.CI.State {
					case "passing":
						prLine += "  CI: passing"
					case "failing":
						prLine += "  CI: failing"
						if haveCounts {
							prLine += " " + counts
							if len(s.CI.FailingChecks) > 0 {
								prLine += fmt.Sprintf(" %d✗", len(s.CI.FailingChecks))
							}
						}
					case "pending":
						prLine += "  CI: pending"
						if haveCounts {
							prLine += " " + counts
						}
					}
				}
			}

			panelContent.WriteString("\n")
			panelContent.WriteString(lineColor.Render(prLine))
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
			if !s.Mirror && (s.Dirty || s.UnpushedCount > 0) {
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
	case stateConfirmStop:
		if item, ok := m.list.SelectedItem().(sessionItem); ok {
			panelContent.WriteString("\n")
			panelContent.WriteString(lipgloss.NewStyle().
				Foreground(colorYellow).
				Render(fmt.Sprintf("Stop '%s'? [y/N]", item.info.Name)))
		}
	case stateConfirmRestart:
		if item, ok := m.list.SelectedItem().(sessionItem); ok {
			panelContent.WriteString("\n")
			panelContent.WriteString(lipgloss.NewStyle().
				Foreground(colorGreen).
				Render(fmt.Sprintf("Restart '%s'? [y/N]", item.info.Name)))
		}
	case stateRestartMenu:
		sessions := m.visibleSessions()
		all := len(sessions)
		outdated, stopped := 0, 0

		for _, s := range sessions {
			if s.ConfigStale {
				outdated++
			}

			if s.Status == "stopped" {
				stopped++
			}
		}

		green := lipgloss.NewStyle().Foreground(colorGreen)

		panelContent.WriteString("\n")
		panelContent.WriteString(green.Render("Restart:"))
		panelContent.WriteString(green.Render(fmt.Sprintf("  [a]ll (%d)   [o]utdated (%d)   [s]topped (%d)   esc cancel", all, outdated, stopped)))
	case stateRestartingAll:
		panelContent.WriteString("\n")

		progress := min(m.restartIdx+1, len(m.restartQueue))
		panelContent.WriteString(lipgloss.NewStyle().
			Foreground(colorGreen).
			Render(fmt.Sprintf("Restarting %d/%d sessions…", progress, len(m.restartQueue))))

		if len(m.restartErrors) > 0 {
			panelContent.WriteString(lipgloss.NewStyle().
				Foreground(colorRed).
				Render(fmt.Sprintf("  (%d failed)", len(m.restartErrors))))
		}
	}

	// The global key hints describe list-mode keys. While a confirm prompt or
	// the restart menu is open those keys are remapped (e.g. S = "restart
	// stopped" in the menu), so only show them in the list/filter states.
	if m.state == stateList || m.state == stateFilter {
		helpStyle := lipgloss.NewStyle().Foreground(colorFaint)

		panelContent.WriteString("\n")

		helpParts := []string{}

		if len(m.shortcutKeys) > 0 {
			first := string(m.shortcutKeys[0])
			last := string(m.shortcutKeys[len(m.shortcutKeys)-1])
			helpParts = append(helpParts, first+"-"+last+" jump")
		}

		if m.view == viewDeleted {
			// The Deleted view offers only restore.
			helpParts = append(helpParts, "enter restore", "◂▸ view", m.keySearch+" filter", "q quit")
		} else {
			helpParts = append(helpParts, "enter attach", "n new", "◂▸ view", m.keySearch+" filter", "tab group", "s star", "space fold", "C fold-all", m.keyDelete+" delete", "S stop", "r/"+m.keyResume+" restart", "q quit")
		}

		panelContent.WriteString(helpStyle.Render(strings.Join(helpParts, "  ")))
	}

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

	for i, pl := range panelLines {
		row := offsetY + i
		if row >= 0 && row < h {
			bg := bgLines[row]
			left := ansi.Truncate(bg, offsetX, "")
			right := ansi.TruncateLeft(bg, offsetX+panelRenderedW, "")
			bgLines[row] = left + pl + right
		}
	}

	v := tea.NewView(strings.Join(bgLines, "\n"))
	v.AltScreen = true

	return v
}

// RunOverlayOpts configures the session-picker overlay. It replaces the long
// positional parameter list of RunOverlay — several of its fields are
// structurally identical callbacks (five `func(sessionID string) error`) that
// were trivially transposable when passed positionally.
type RunOverlayOpts struct {
	// Sessions is the initial list rendered in the overlay.
	Sessions []protocol.SessionInfo
	// CurrentSessionID highlights the session the user was just attached to.
	CurrentSessionID string
	// FetchPreview is called asynchronously to load scrollback for the
	// selected session.
	FetchPreview func(sessionID string) string
	// RefreshSessions re-fetches the live session list.
	RefreshSessions func() []protocol.SessionInfo
	// RefreshDeleted re-fetches the soft-deleted session list.
	RefreshDeleted func() []protocol.SessionInfo
	// DeleteSession soft-deletes a session by ID.
	DeleteSession func(sessionID string) error
	// RestartSession restarts a stopped session by ID.
	RestartSession func(sessionID string) error
	// StopSession stops a running session by ID.
	StopSession func(sessionID string) error
	// ToggleStar stars or unstars a session by ID.
	ToggleStar func(sessionID string, star bool) error
	// RestoreSession restores a soft-deleted session by ID.
	RestoreSession func(sessionID string) error
	// Profile is the active configuration profile name.
	Profile string
	// Collapsed is the initial per-repo collapse state.
	Collapsed map[string]bool
	// RepoSuggestions seeds the create-session repo picker.
	RepoSuggestions []RepoSuggestion
	// ShortcutKeys is the set of quick-jump shortcut runes.
	ShortcutKeys string
	// Agents is the list of available agent types for create-session.
	Agents []string
	// DefaultAgent is the pre-selected agent for create-session.
	DefaultAgent string
	// Keys is the resolved overlay keybinding set.
	Keys OverlayKeys
}

// RunOverlay launches the bubbletea overlay listing sessions grouped by repo.
func RunOverlay(opts RunOverlayOpts) *OverlayResult {
	m := newOverlayModel(opts.Sessions, opts.CurrentSessionID, opts.FetchPreview, opts.DeleteSession, opts.Collapsed, []rune(opts.ShortcutKeys))
	m.refreshSessions = opts.RefreshSessions
	m.refreshDeleted = opts.RefreshDeleted
	m.restartSession = opts.RestartSession
	m.stopSession = opts.StopSession
	m.toggleStar = opts.ToggleStar
	m.restoreSession = opts.RestoreSession
	m.profile = opts.Profile
	m.repoSuggestions = opts.RepoSuggestions
	m.agents = opts.Agents
	m.defaultAgent = opts.DefaultAgent
	m.applyKeys(opts.Keys)
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

	if result.createDone {
		overlayResult.Action = "create"
		overlayResult.CreateName = result.createName
		overlayResult.CreateRepoPath = result.createRepoPath
		overlayResult.CreateAgent = result.createAgent

		return overlayResult
	}

	if result.selected != nil {
		overlayResult.SessionID = result.selected.ID

		overlayResult.Action = "attach"
		if result.state == stateConfirmDelete {
			overlayResult.Action = "delete"
		}

		return overlayResult
	}

	// The user stopped the session they were attached to and left the overlay
	// without picking another. Signal the attach loop to exit rather than
	// reattach (which would auto-resume the session it just stopped).
	if result.stoppedCurrent {
		overlayResult.Action = "stopped-current"
		overlayResult.SessionID = result.currentSessionID

		return overlayResult
	}

	return overlayResult
}
