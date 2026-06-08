package client

import (
	"fmt"
	"image/color"
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

var barBg = lipgloss.Color("#303040")

func barStyle(fg color.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(fg).Background(barBg)
}

func barBold(fg color.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(fg).Background(barBg).Bold(true)
}

var (
	barFill = lipgloss.NewStyle().Background(barBg)
	barSep  = barStyle(colorFaint)
	barDim  = barStyle(colorDim)
)

func statusColor(status string) color.Color {
	switch status {
	case "active", "running":
		return colorGreen
	case "approval":
		return colorRed
	case "ready":
		return colorBlue
	case "errored":
		return colorRed
	default:
		return colorDim
	}
}

func statusDot(status string) string {
	c := statusColor(status)
	switch status {
	case "approval":
		return barBold(c).Render("⚠")
	case "errored":
		return barStyle(c).Render("✗")
	case "stopped":
		return barStyle(c).Render("○")
	default:
		return barStyle(c).Render("●")
	}
}

func sp() string  { return barFill.Render(" ") }
func dsp() string { return barFill.Render("  ") }

func formatStatusLine(info statusBarInfo, cols int) string {
	status := info.status
	if info.agentStatus != "" && info.status == "running" {
		status = info.agentStatus
	}

	branch := displayBranch(info.branch, info.name)

	sep := barSep.Render(" │ ")

	// Left: dot + session name + agent + status
	left := sp() + statusDot(status) + sp() +
		barBold(lipgloss.Color("#e0e0e0")).Render(info.name) + dsp() +
		barDim.Render(info.agent) + dsp() +
		barStyle(statusColor(status)).Render(status)

	// Mid: git info (only if branch is meaningful)
	var mid string
	if branch != "" && branch != "—" {
		mid = sep + barDim.Render(branch)
		if info.dirty {
			mid += sp() + barStyle(colorGold).Render("●")
		}
		if info.unpushed > 0 {
			mid += sp() + barStyle(colorBlue).Render(fmt.Sprintf("↑%d", info.unpushed))
		}
	}

	// Right: fleet + unread
	var right string
	fleet := formatFleetSection(info.fleet)
	if fleet != "" {
		right += sep + fleet
	}
	if info.unread > 0 {
		right += sep + barStyle(colorGold).Render(fmt.Sprintf("✉ %d", info.unread))
	}
	right += sp()

	leftW := lipgloss.Width(left)
	midW := lipgloss.Width(mid)
	rightW := lipgloss.Width(right)

	if leftW+midW+rightW > cols {
		mid = ""
		midW = 0
	}
	if leftW+rightW > cols {
		right = formatFleetMinimal(info.fleet)
		if right != "" {
			right = sep + right
		}
		right += sp()
		rightW = lipgloss.Width(right)
	}
	if leftW+rightW > cols {
		right = sp()
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
	if fleet.Total == 0 {
		return ""
	}

	var parts []string

	if fleet.Approval > 0 {
		parts = append(parts, barBold(colorRed).Render(fmt.Sprintf("⚠ %d approval", fleet.Approval)))
	}
	if fleet.Errored > 0 {
		parts = append(parts, barStyle(colorRed).Render(fmt.Sprintf("✗ %d error", fleet.Errored)))
	}
	if fleet.Active > 0 {
		parts = append(parts, barStyle(colorGreen).Render(fmt.Sprintf("● %d active", fleet.Active)))
	}
	if fleet.Ready > 0 {
		parts = append(parts, barStyle(colorBlue).Render(fmt.Sprintf("● %d ready", fleet.Ready)))
	}
	if fleet.Stopped > 0 {
		parts = append(parts, barStyle(colorDim).Render(fmt.Sprintf("○ %d stopped", fleet.Stopped)))
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, dsp())
}

func formatFleetMinimal(fleet protocol.FleetSummary) string {
	if fleet.Approval > 0 {
		return barBold(colorRed).Render(fmt.Sprintf("⚠ %d approval", fleet.Approval))
	}
	if fleet.Errored > 0 {
		return barStyle(colorRed).Render(fmt.Sprintf("✗ %d error", fleet.Errored))
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
