package client

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
)

type statusBarInfo struct {
	name        string
	agent       string
	status      string
	agentStatus string
	branch      string
	dirty       bool
	unpushed    int
	unread      int
	fleet       protocol.FleetSummary
}

func newStatusBarInfo(s protocol.SessionInfo, unreadCount int, fleet protocol.FleetSummary) statusBarInfo {
	return statusBarInfo{
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
}

var (
	barBg = lipgloss.Color("#1a1a1a")

	barSession = lipgloss.NewStyle().Bold(true).Background(barBg)
	barAgent   = lipgloss.NewStyle().Foreground(colorDim).Background(barBg)
	barSep     = lipgloss.NewStyle().Foreground(colorFaint).Background(barBg)
	barBranch  = lipgloss.NewStyle().Foreground(colorDim).Background(barBg)
	barDirty   = lipgloss.NewStyle().Foreground(colorGold).Background(barBg)
	barAhead   = lipgloss.NewStyle().Foreground(colorBlue).Background(barBg)
	barUnread  = lipgloss.NewStyle().Foreground(colorGold).Background(barBg)
	barFill    = lipgloss.NewStyle().Background(barBg)
)

func statusStyle(status string) lipgloss.Style {
	base := lipgloss.NewStyle().Background(barBg)
	switch status {
	case "active", "running":
		return base.Foreground(colorGreen)
	case "approval":
		return base.Foreground(colorRed).Bold(true)
	case "ready":
		return base.Foreground(colorBlue)
	case "errored":
		return base.Foreground(colorRed)
	default:
		return base.Foreground(colorDim)
	}
}

func statusDot(status string) string {
	switch status {
	case "active", "running":
		return statusStyle(status).Render("●")
	case "approval":
		return statusStyle(status).Render("⚠")
	case "errored":
		return statusStyle(status).Render("✗")
	case "ready":
		return statusStyle(status).Render("●")
	default:
		return statusStyle(status).Render("○")
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

	sep := barSep.Render(" │ ")

	// Left section: session identity
	left := " " + statusDot(status) + " " +
		barSession.Render(info.name) + "  " +
		barAgent.Render(info.agent) + "  " +
		statusStyle(status).Render(status)

	// Middle section: git info
	var mid string
	if branch != "" {
		mid = sep + barBranch.Render(branch)
		if info.dirty {
			mid += " " + barDirty.Render("●")
		}
		if info.unpushed > 0 {
			mid += " " + barAhead.Render(fmt.Sprintf("↑%d", info.unpushed))
		}
	}

	// Right section: fleet summary + unread (right-aligned)
	right := formatFleetSection(info.fleet)
	if info.unread > 0 {
		right += sep + barUnread.Render(fmt.Sprintf("✉ %d", info.unread))
	}
	right += " "

	leftW := lipgloss.Width(left)
	midW := lipgloss.Width(mid)
	rightW := lipgloss.Width(right)

	// Progressive degradation for narrow terminals
	if leftW+midW+rightW > cols {
		// Drop git info first
		mid = ""
		midW = 0
	}
	if leftW+rightW > cols {
		// Drop fleet details, keep only approval count if any
		right = formatFleetMinimal(info.fleet)
		if info.unread > 0 {
			right += sep + barUnread.Render(fmt.Sprintf("✉ %d", info.unread))
		}
		right += " "
		rightW = lipgloss.Width(right)
	}
	if leftW+rightW > cols {
		// Ultra-narrow: just session + status
		right = " "
		rightW = 1
	}

	gap := max(cols-leftW-midW-rightW, 0)

	line := left + mid + barFill.Render(strings.Repeat(" ", gap)) + right
	if w := lipgloss.Width(line); w > cols {
		line = ansi.Truncate(line, cols, "")
		if pad := cols - lipgloss.Width(line); pad > 0 {
			line += barFill.Render(strings.Repeat(" ", pad))
		}
	}
	return line
}

func formatFleetSection(fleet protocol.FleetSummary) string {
	if fleet.Total <= 1 {
		return ""
	}

	sep := barSep.Render(" │ ")
	var parts []string

	// Approval always first and most prominent
	if fleet.Approval > 0 {
		parts = append(parts, statusStyle("approval").Render(fmt.Sprintf("⚠ %d approval", fleet.Approval)))
	}
	if fleet.Errored > 0 {
		parts = append(parts, statusStyle("errored").Render(fmt.Sprintf("✗ %d error", fleet.Errored)))
	}
	if fleet.Active > 0 {
		parts = append(parts, statusStyle("active").Render(fmt.Sprintf("● %d active", fleet.Active)))
	}
	if fleet.Ready > 0 {
		parts = append(parts, statusStyle("ready").Render(fmt.Sprintf("● %d ready", fleet.Ready)))
	}
	if fleet.Stopped > 0 {
		parts = append(parts, statusStyle("stopped").Render(fmt.Sprintf("○ %d stopped", fleet.Stopped)))
	}

	if len(parts) == 0 {
		return ""
	}
	return sep + strings.Join(parts, "  ")
}

func formatFleetMinimal(fleet protocol.FleetSummary) string {
	if fleet.Total <= 1 {
		return ""
	}
	sep := barSep.Render(" │ ")
	if fleet.Approval > 0 {
		return sep + statusStyle("approval").Render(fmt.Sprintf("⚠ %d approval", fleet.Approval))
	}
	if fleet.Errored > 0 {
		return sep + statusStyle("errored").Render(fmt.Sprintf("✗ %d error", fleet.Errored))
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
	sb.mu.Unlock()

	line := formatStatusLine(info, cols)
	seq := fmt.Sprintf("\x1b7\x1b[%d;1H%s\x1b8", row, line)
	w.Write([]byte(seq))
}

func (sb *statusBarState) setup(w io.Writer) {
	sb.mu.Lock()
	region := sb.scrollRegion()
	pos := sb.position
	sb.mu.Unlock()

	w.Write([]byte(region))
	if pos == "top" {
		w.Write([]byte("\x1b[2;1H"))
	}
	sb.render(w)
}

func (sb *statusBarState) teardown(w io.Writer) {
	sb.mu.Lock()
	row := sb.barRow()
	sb.mu.Unlock()

	fmt.Fprintf(w, "\x1b[%d;1H\x1b[2K", row)
	w.Write([]byte("\x1b[r"))
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
