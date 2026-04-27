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
	name             string
	agent            string
	status           string
	agentStatus      string
	branch           string
	dirty            bool
	unpushed         int
	unread           int
	fleet            protocol.FleetSummary
	pendingApprovals int
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
	barBg  = lipgloss.Color("#1a1a1a")
	barSep = lipgloss.NewStyle().Foreground(colorFaint).Background(barBg)
)


func formatStatusLine(info statusBarInfo, cols int) string {
	status := info.status
	if info.agentStatus != "" && info.status == "running" {
		status = info.agentStatus
	}

	branch := info.branch
	if p := strings.SplitN(branch, "/", 3); len(p) == 3 {
		branch = p[2]
	}

	bg := barBg
	if info.pendingApprovals > 0 {
		bg = lipgloss.Color("#5f0000")
	}

	session := lipgloss.NewStyle().Bold(true).Background(bg)
	agent := lipgloss.NewStyle().Foreground(colorDim).Background(bg)
	sepStyle := lipgloss.NewStyle().Foreground(colorFaint).Background(bg)
	branchStyle := lipgloss.NewStyle().Foreground(colorDim).Background(bg)
	dirtyStyle := lipgloss.NewStyle().Foreground(colorGold).Background(bg)
	aheadStyle := lipgloss.NewStyle().Foreground(colorBlue).Background(bg)
	unreadStyle := lipgloss.NewStyle().Foreground(colorGold).Background(bg)
	fillStyle := lipgloss.NewStyle().Background(bg)
	stStyle := func(s string) lipgloss.Style {
		base := lipgloss.NewStyle().Background(bg)
		switch s {
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

	sep := sepStyle.Render(" │ ")

	dot := stStyle(status).Render("●")
	if status == "approval" {
		dot = stStyle(status).Render("⚠")
	} else if status == "errored" {
		dot = stStyle(status).Render("✗")
	} else if status != "active" && status != "running" && status != "ready" {
		dot = stStyle(status).Render("○")
	}

	// Left section: session identity
	left := " " + dot + " " +
		session.Render(info.name) + "  " +
		agent.Render(info.agent) + "  " +
		stStyle(status).Render(status)

	if info.pendingApprovals > 0 {
		left += sep + lipgloss.NewStyle().Foreground(colorRed).Bold(true).Background(bg).
			Render(fmt.Sprintf("⚠ %d pending", info.pendingApprovals))
	}

	// Middle section: git info
	var mid string
	if branch != "" {
		mid = sep + branchStyle.Render(branch)
		if info.dirty {
			mid += " " + dirtyStyle.Render("●")
		}
		if info.unpushed > 0 {
			mid += " " + aheadStyle.Render(fmt.Sprintf("↑%d", info.unpushed))
		}
	}

	// Right section: fleet summary + unread (right-aligned)
	right := formatFleetSection(info.fleet)
	if info.unread > 0 {
		right += sep + unreadStyle.Render(fmt.Sprintf("✉ %d", info.unread))
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
			right += sep + unreadStyle.Render(fmt.Sprintf("✉ %d", info.unread))
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

	line := left + mid + fillStyle.Render(strings.Repeat(" ", gap)) + right
	if w := lipgloss.Width(line); w > cols {
		line = ansi.Truncate(line, cols, "")
		if pad := cols - lipgloss.Width(line); pad > 0 {
			line += fillStyle.Render(strings.Repeat(" ", pad))
		}
	}
	return line
}

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
	mu               sync.Mutex
	sessionID        string
	info             statusBarInfo
	rows             int
	cols             int
	position         string
	pendingApprovals int
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
	info.pendingApprovals = sb.pendingApprovals
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

func (sb *statusBarState) updatePendingApprovals(count int) {
	sb.mu.Lock()
	sb.pendingApprovals = count
	sb.mu.Unlock()
}

func (sb *statusBarState) updateSize(rows, cols int) {
	sb.mu.Lock()
	sb.rows = rows
	sb.cols = cols
	sb.mu.Unlock()
}
