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

// ANSI sequences — background is set once; only foreground/bold change within.
const (
	barBgOn  = "\x1b[0;48;2;48;48;64m"
	barReset = "\x1b[0m"

	fgGreen  = "\x1b[38;2;0;255;135m"
	fgRed    = "\x1b[38;2;255;95;95m"
	fgBlue   = "\x1b[38;2;135;175;255m"
	fgGold   = "\x1b[38;2;255;215;0m"
	fgDim    = "\x1b[38;2;98;98;98m"
	fgFaint  = "\x1b[38;2;68;68;68m"
	fgBright = "\x1b[38;2;224;224;224m"

	boldOn  = "\x1b[1m"
	boldOff = "\x1b[22m"
)

func fgForStatus(status string) string {
	switch status {
	case "active", "running":
		return fgGreen
	case "approval":
		return fgRed + boldOn
	case "ready":
		return fgBlue
	case "errored":
		return fgRed
	default:
		return fgDim
	}
}

func dotForStatus(status string) string {
	fg := fgForStatus(status)
	switch status {
	case "approval":
		return fg + "⚠" + boldOff
	case "errored":
		return fg + "✗"
	case "stopped":
		return fg + "○"
	default:
		return fg + "●"
	}
}

func formatStatusLine(info statusBarInfo, cols int) string {
	status := info.status
	if info.agentStatus != "" && info.status == "running" {
		status = info.agentStatus
	}

	branch := displayBranch(info.branch, info.name)
	sFg := fgForStatus(status)
	sep := fgFaint + " │ "

	// Left: dot + name + agent + status
	left := " " + dotForStatus(status) +
		" " + boldOn + fgBright + info.name + boldOff +
		"  " + fgDim + info.agent +
		"  " + sFg + status

	// Mid: git info
	var mid string
	if branch != "" && branch != "—" {
		mid = sep + fgDim + branch
		if info.dirty {
			mid += " " + fgGold + "●"
		}
		if info.unpushed > 0 {
			mid += " " + fgBlue + fmt.Sprintf("↑%d", info.unpushed)
		}
	}

	// Right: fleet + unread
	var right string
	fleet := formatFleetSection(info.fleet)
	if fleet != "" {
		right += sep + fleet
	}
	if info.unread > 0 {
		right += sep + fgGold + fmt.Sprintf("✉ %d", info.unread)
	}
	right += " "

	leftW := visWidth(left)
	midW := visWidth(mid)
	rightW := visWidth(right)

	if leftW+midW+rightW > cols {
		mid = ""
		midW = 0
	}
	if leftW+rightW > cols {
		right = formatFleetMinimal(info.fleet)
		if right != "" {
			right = sep + right
		}
		right += " "
		rightW = visWidth(right)
	}
	if leftW+rightW > cols {
		right = " "
		rightW = 1
	}

	gap := max(cols-leftW-midW-rightW, 0)

	// Wrap entire line in a single background — no gaps.
	inner := left + mid + strings.Repeat(" ", gap) + right
	line := barBgOn + inner + barReset

	if w := visWidth(line); w > cols {
		line = ansi.Truncate(line, cols, "")
		if pad := cols - visWidth(line); pad > 0 {
			line = line + barBgOn + strings.Repeat(" ", pad) + barReset
		}
	}
	return line
}

func visWidth(s string) int {
	return lipgloss.Width(s)
}

func formatFleetSection(fleet protocol.FleetSummary) string {
	if fleet.Total == 0 {
		return ""
	}

	var parts []string
	if fleet.Approval > 0 {
		parts = append(parts, fgRed+boldOn+fmt.Sprintf("⚠ %d approval", fleet.Approval)+boldOff)
	}
	if fleet.Errored > 0 {
		parts = append(parts, fgRed+fmt.Sprintf("✗ %d error", fleet.Errored))
	}
	if fleet.Active > 0 {
		parts = append(parts, fgGreen+fmt.Sprintf("● %d active", fleet.Active))
	}
	if fleet.Ready > 0 {
		parts = append(parts, fgBlue+fmt.Sprintf("● %d ready", fleet.Ready))
	}
	if fleet.Stopped > 0 {
		parts = append(parts, fgDim+fmt.Sprintf("○ %d stopped", fleet.Stopped))
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "  ")
}

func formatFleetMinimal(fleet protocol.FleetSummary) string {
	if fleet.Approval > 0 {
		return fgRed + boldOn + fmt.Sprintf("⚠ %d approval", fleet.Approval) + boldOff
	}
	if fleet.Errored > 0 {
		return fgRed + fmt.Sprintf("✗ %d error", fleet.Errored)
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
