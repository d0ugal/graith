package client

import (
	"fmt"
	"image/color"
	"io"
	"strings"
	"sync"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
)

type statusBarInfo struct {
	name          string
	agent         string
	status        string
	agentStatus   string
	branch        string
	dirty         bool
	unpushed      int
	unread        int
	fleet         protocol.FleetSummary
	prNumber      int
	prState       string // open | draft | merged | closed
	prConflicting bool
	ciState       string // passing | failing | pending
}

func newStatusBarInfo(s protocol.SessionInfo, unreadCount int, fleet protocol.FleetSummary) statusBarInfo {
	info := statusBarInfo{
		name:        s.Name,
		agent:       s.Agent,
		status:      s.Status,
		agentStatus: s.AgentStatus,
		branch:      s.Branch,
		dirty:       s.Dirty,
		unpushed:    s.UnpushedCount,
		unread:      unreadCount,
		fleet:       fleet,
	}
	if s.PullRequest != nil {
		info.prNumber = s.PullRequest.Number
		info.prState = s.PullRequest.State
		info.prConflicting = s.PullRequest.Conflicting
	}

	if s.CI != nil {
		info.ciState = s.CI.State
	}

	return info
}

const (
	plRight = ""
	plLeft  = ""
)

var (
	barBg    = lipgloss.Color("#1a1a1a")
	accentBg = lipgloss.Color("#44475a")
)

func styledStatus(status string, bg color.Color) lipgloss.Style {
	base := lipgloss.NewStyle().Background(bg)

	switch status {
	case "active", "running":
		return base.Foreground(colorGreen)
	case "ready":
		return base.Foreground(colorBlue)
	case "errored":
		return base.Foreground(colorRed)
	default:
		return base.Foreground(colorDim)
	}
}

func formatStatusLine(info statusBarInfo, cols int) string {
	status := info.status
	if info.agentStatus != "" && info.status == "running" {
		status = info.agentStatus
	}

	branch := info.branch
	if p := strings.SplitN(branch, "/", 3); len(p) == 3 {
		branch = p[2]
	}

	accent := accentBg
	fill := barBg

	accentPad := lipgloss.NewStyle().Background(accent)
	fillPad := lipgloss.NewStyle().Background(fill)
	nameStyle := lipgloss.NewStyle().Bold(true).Background(accent)
	agentStyle := lipgloss.NewStyle().Foreground(colorDim).Background(fill)
	branchStyle := lipgloss.NewStyle().Foreground(colorDim).Background(fill)
	dirtyStyle := lipgloss.NewStyle().Foreground(colorGold).Background(fill)
	aheadStyle := lipgloss.NewStyle().Foreground(colorBlue).Background(fill)
	fillSep := lipgloss.NewStyle().Foreground(colorFaint).Background(fill)

	transAccentToFill := lipgloss.NewStyle().Foreground(accent).Background(fill).Render(plRight)
	transFillToAccent := lipgloss.NewStyle().Foreground(accent).Background(fill).Render(plLeft)

	var dot string

	switch {
	case status == "errored":
		dot = styledStatus(status, accent).Render("✗")
	case status != "active" && status != "running" && status != "ready":
		dot = styledStatus(status, accent).Render("○")
	default:
		dot = styledStatus(status, accent).Render("●")
	}

	leftAccent := accentPad.Render(" ") + dot + accentPad.Render(" ") +
		nameStyle.Render(info.name) + accentPad.Render(" ")

	sep := fillSep.Render(" │ ")
	leftContent := fillPad.Render(" ") + agentStyle.Render(info.agent) +
		fillPad.Render("  ") + styledStatus(status, fill).Render(status)

	var mid string
	if branch != "" {
		mid = sep + branchStyle.Render(branch)
		if info.dirty {
			mid += fillPad.Render(" ") + dirtyStyle.Render("●")
		}

		if info.unpushed > 0 {
			mid += fillPad.Render(" ") + aheadStyle.Render(fmt.Sprintf("↑%d", info.unpushed))
		}
	}

	if pr := formatPRSection(info, fill); pr != "" {
		mid += sep + pr
	}

	wrapRight := func(body string) (string, string, int) {
		if body == "" {
			return "", "", 0
		}

		section := accentPad.Render(" ") + body + accentPad.Render(" ")

		return transFillToAccent, section, lipgloss.Width(transFillToAccent) + lipgloss.Width(section)
	}

	rightSep, rightSection, rightW := wrapRight(formatRightSection(info, accent, statusRightFull))

	left := leftAccent + transAccentToFill + leftContent
	leftW := lipgloss.Width(left)
	midW := lipgloss.Width(mid)

	if leftW+midW+rightW > cols {
		mid = ""
		midW = 0
	}

	if leftW+rightW > cols {
		rightSep, rightSection, rightW = wrapRight(formatRightSection(info, accent, statusRightCompact))
	}

	if leftW+rightW > cols {
		rightSep, rightSection, rightW = wrapRight(formatRightSection(info, accent, statusRightCritical))
	}

	if leftW+rightW > cols {
		rightSep, rightSection, rightW = "", "", 0
	}

	gap := max(cols-leftW-midW-rightW, 0)
	line := left + mid + fillPad.Render(strings.Repeat(" ", gap)) + rightSep + rightSection

	if w := lipgloss.Width(line); w > cols {
		line = ansi.Truncate(line, cols, "")
		if pad := cols - lipgloss.Width(line); pad > 0 {
			line += fillPad.Render(strings.Repeat(" ", pad))
		}
	}

	return line
}

type statusRightMode int

const (
	statusRightFull statusRightMode = iota
	statusRightCompact
	statusRightCritical
)

func formatRightSection(info statusBarInfo, bg color.Color, mode statusRightMode) string {
	sep := lipgloss.NewStyle().Foreground(colorDim).Background(bg).Render(" │ ")
	parts := formatAttentionSignals(info.fleet, bg, mode)

	switch mode {
	case statusRightFull:
		if fleet := formatFleetSection(info.fleet, bg); fleet != "" {
			parts = append(parts, fleet)
		}
	case statusRightCompact:
		if fleet := formatFleetMinimal(info.fleet, bg); fleet != "" {
			parts = append(parts, fleet)
		}
	case statusRightCritical:
		if len(parts) == 0 {
			if fleet := formatFleetMinimal(info.fleet, bg); fleet != "" {
				parts = append(parts, fleet)
			}
		}
	}

	if info.unread > 0 && mode != statusRightCritical {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorGold).Background(bg).Render(fmt.Sprintf("✉ %d", info.unread)))
	}

	if len(parts) == 0 && info.unread > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorGold).Background(bg).Render(fmt.Sprintf("✉ %d", info.unread)))
	}

	return strings.Join(parts, sep)
}

func formatAttentionSignals(fleet protocol.FleetSummary, bg color.Color, mode statusRightMode) []string {
	var parts []string

	if text := strings.TrimSpace(fleet.OrchestratorAttention); text != "" {
		parts = append(parts, formatOrchestratorAttention(text, bg, mode))
	}

	if fleet.JailedComments > 0 && (mode != statusRightCritical || len(parts) == 0) {
		parts = append(parts, formatJailedAttention(fleet, bg, mode))
	}

	return parts
}

func formatOrchestratorAttention(text string, bg color.Color, mode statusRightMode) string {
	style := lipgloss.NewStyle().Foreground(colorRed).Background(bg).Bold(true)
	if mode != statusRightFull {
		return style.Render("‼ orch")
	}

	return style.Render("‼ orch " + compactStatusToken(text, 28))
}

func formatJailedAttention(fleet protocol.FleetSummary, bg color.Color, mode statusRightMode) string {
	style := lipgloss.NewStyle().Foreground(jailedAttentionColor(fleet.JailedComments)).Background(bg).Bold(true)

	label := fmt.Sprintf("⚠ jail %d", fleet.JailedComments)
	if mode != statusRightFull {
		return style.Render(label)
	}

	if author := compactStatusToken(strings.TrimPrefix(fleet.JailedNewestAuthor, "@"), 14); author != "" {
		label += " @" + author
	}

	if fleet.JailedNewestPR > 0 {
		label += fmt.Sprintf(" #%d", fleet.JailedNewestPR)
	} else if repo := compactStatusToken(fleet.JailedNewestRepo, 18); repo != "" {
		label += " " + repo
	}

	return style.Render(label)
}

func jailedAttentionColor(count int) color.Color {
	if count >= 5 {
		return colorRed
	}

	return colorYellow
}

func compactStatusToken(s string, width int) string {
	s = sanitizeStatusText(s)
	s = strings.Join(strings.Fields(s), " ")

	if s == "" || width <= 0 {
		return ""
	}

	if lipgloss.Width(s) <= width {
		return s
	}

	if width <= 1 {
		return ansi.Truncate(s, width, "")
	}

	return ansi.Truncate(s, width-1, "") + "…"
}

func sanitizeStatusText(s string) string {
	s = ansi.Strip(s)

	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}

		return r
	}, s)
}

// formatReadOnlyLine renders the persistent read-only indicator shown during a
// read-only attach (issue #31): a bold badge followed by the session name and
// agent/status, padded to the full terminal width so it reads as a bar.
func formatReadOnlyLine(info statusBarInfo, cols int) string {
	badgeBg := lipgloss.Color("#8b0000")

	badgeStyle := lipgloss.NewStyle().Bold(true).Foreground(colorGold).Background(badgeBg)
	nameStyle := lipgloss.NewStyle().Bold(true).Foreground(colorGold).Background(barBg)
	dimStyle := lipgloss.NewStyle().Foreground(colorDim).Background(barBg)
	fillStyle := lipgloss.NewStyle().Background(barBg)

	badge := badgeStyle.Render(" 🔒 READ-ONLY ")

	body := fillStyle.Render(" ") + nameStyle.Render(info.name)
	if info.agent != "" {
		body += dimStyle.Render(" · " + info.agent)
	}

	body += dimStyle.Render(" · input disabled")

	line := badge + body

	gap := cols - lipgloss.Width(line)
	if gap > 0 {
		line += fillStyle.Render(strings.Repeat(" ", gap))
	}

	if lipgloss.Width(line) > cols {
		line = ansi.Truncate(line, cols, "")
	}

	return line
}

// formatPRSection renders the linked-PR token for the status bar, e.g.
// "PR#56 ✗" (CI failing) or "PR#56 ⚠conflict", colored by the worst signal.
func formatPRSection(info statusBarInfo, bg color.Color) string {
	if info.prNumber == 0 {
		return ""
	}

	base := lipgloss.NewStyle().Background(bg)
	label := fmt.Sprintf("PR#%d", info.prNumber)

	switch info.prState {
	case "merged":
		return base.Foreground(colorDim).Render(label + " merged")
	case "closed":
		return base.Foreground(colorDim).Render(label + " closed")
	case "draft":
		// Mark drafts with a compact "d" suffix, then fall through to
		// conflict/CI rendering. (`gr list` shows the state textually, e.g.
		// "#9 draft"; overlay rows use a stronger inline marker.)
		label += "d"
	}

	if info.prConflicting {
		return base.Foreground(colorRed).Bold(true).Render(label + " ⚠conflict")
	}

	switch info.ciState {
	case "failing":
		return base.Foreground(colorRed).Render(label + " ✗CI")
	case "passing":
		return base.Foreground(colorGreen).Render(label + " ✓")
	case "pending":
		return base.Foreground(colorYellow).Render(label + " ·CI")
	}

	return base.Foreground(colorBlue).Render(label)
}

func formatFleetSection(fleet protocol.FleetSummary, bg color.Color) string {
	if fleet.Total <= 1 {
		return ""
	}

	pad := lipgloss.NewStyle().Background(bg)

	var parts []string

	if fleet.Errored > 0 {
		parts = append(parts, styledStatus("errored", bg).Render(fmt.Sprintf("✗ %d error", fleet.Errored)))
	}

	if fleet.Active > 0 {
		parts = append(parts, styledStatus("active", bg).Render(fmt.Sprintf("● %d active", fleet.Active)))
	}

	if fleet.Ready > 0 {
		parts = append(parts, styledStatus("ready", bg).Render(fmt.Sprintf("● %d ready", fleet.Ready)))
	}

	if fleet.Stopped > 0 {
		parts = append(parts, styledStatus("stopped", bg).Render(fmt.Sprintf("○ %d stopped", fleet.Stopped)))
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, pad.Render("  "))
}

func formatFleetMinimal(fleet protocol.FleetSummary, bg color.Color) string {
	if fleet.Total <= 1 {
		return ""
	}

	if fleet.Errored > 0 {
		return styledStatus("errored", bg).Render(fmt.Sprintf("✗ %d error", fleet.Errored))
	}

	return ""
}

type statusBarState struct {
	mu        sync.Mutex
	sessionID string
	info      statusBarInfo
	rows      int
	cols      int
	position  string
	// readOnly renders a read-only indicator instead of the normal status line,
	// used as the persistent indicator for a read-only attach (issue #31).
	readOnly bool
}

func (sb *statusBarState) barRow() int {
	if sb.position == "top" {
		return 1
	}

	return sb.rows
}

func (sb *statusBarState) scrollRegion() string {
	if sb.position == "top" {
		return fmt.Sprintf("\x1b[2;%dr", sb.rows)
	}

	return fmt.Sprintf("\x1b[1;%dr", sb.rows-1)
}

func (sb *statusBarState) render(w io.Writer) {
	sb.mu.Lock()
	info := sb.info
	cols := sb.cols
	row := sb.barRow()
	readOnly := sb.readOnly
	sb.mu.Unlock()

	line := formatStatusLine(info, cols)
	if readOnly {
		line = formatReadOnlyLine(info, cols)
	}

	seq := fmt.Sprintf("\x1b7\x1b[%d;1H%s\x1b8", row, line)
	_, _ = w.Write([]byte(seq))
}

func (sb *statusBarState) setup(w io.Writer) {
	sb.mu.Lock()
	region := sb.scrollRegion()
	pos := sb.position
	info := sb.info
	cols := sb.cols
	row := sb.barRow()
	readOnly := sb.readOnly
	sb.mu.Unlock()

	line := formatStatusLine(info, cols)
	if readOnly {
		line = formatReadOnlyLine(info, cols)
	}

	var buf []byte

	buf = append(buf, region...)
	if pos == "top" {
		buf = append(buf, "\x1b[2;1H"...)
	}

	buf = append(buf, fmt.Sprintf("\x1b7\x1b[%d;1H%s\x1b8", row, line)...)
	_, _ = w.Write(buf)
}

func (sb *statusBarState) teardown(w io.Writer) {
	sb.mu.Lock()
	row := sb.barRow()
	sb.mu.Unlock()

	_, _ = fmt.Fprintf(w, "\x1b[%d;1H\x1b[2K\x1b[r", row)
}

func (sb *statusBarState) updateInfo(info statusBarInfo) {
	sb.mu.Lock()
	sb.info = info
	sb.mu.Unlock()
}

func (sb *statusBarState) updateSize(rows, cols int) {
	sb.mu.Lock()
	sb.rows = rows
	sb.cols = cols
	sb.mu.Unlock()
}
