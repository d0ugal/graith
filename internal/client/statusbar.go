package client

import (
	"fmt"
	"io"
	"strings"
	"sync"

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
}

func newStatusBarInfo(s protocol.SessionInfo, unreadCount int) statusBarInfo {
	return statusBarInfo{
		name:        s.Name,
		agent:       s.Agent,
		status:      s.Status,
		agentStatus: s.AgentStatus,
		branch:      s.Branch,
		dirty:       s.Dirty,
		unpushed:    s.UnpushedCount,
		unread:      unreadCount,
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

	left := fmt.Sprintf(" %s [%s] %s", info.name, info.agent, status)

	var mid string
	if branch != "" {
		mid = " │ " + branch
		var gitParts []string
		if info.dirty {
			gitParts = append(gitParts, "●")
		}
		if info.unpushed > 0 {
			gitParts = append(gitParts, fmt.Sprintf("%d↑", info.unpushed))
		}
		if len(gitParts) > 0 {
			mid += " " + strings.Join(gitParts, " ")
		}
	}

	var right string
	if info.unread > 0 {
		right = fmt.Sprintf(" │ ✉ %d ", info.unread)
	}

	line := left + mid + right

	if len(line) > cols {
		line = line[:cols]
	}
	if len(line) < cols {
		line += strings.Repeat(" ", cols-len(line))
	}

	return line
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
	seq := fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[7m%s\x1b[0m\x1b8", row, line)
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
