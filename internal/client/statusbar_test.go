package client

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestFormatStatusLine(t *testing.T) {
	info := statusBarInfo{
		name:        "my-session",
		agent:       "claude",
		status:      "running",
		agentStatus: "active",
		branch:      "d0ugal/graith/my-session-abc123",
		dirty:       true,
		unpushed:    3,
		unread:      5,
	}

	line := formatStatusLine(info, 80)

	if len(line) == 0 {
		t.Fatal("expected non-empty status line")
	}
	if !strings.Contains(line, "my-session") {
		t.Errorf("expected line to contain session name, got %q", line)
	}
	if !strings.Contains(line, "claude") {
		t.Errorf("expected line to contain agent, got %q", line)
	}
	if !strings.Contains(line, "active") {
		t.Errorf("expected line to contain agent status, got %q", line)
	}
	if !strings.Contains(line, "my-session-abc123") {
		t.Errorf("expected line to contain short branch, got %q", line)
	}
	if strings.Contains(line, "d0ugal/graith/") {
		t.Errorf("expected short branch, got full prefix in %q", line)
	}
}

func TestFormatStatusLineMinimal(t *testing.T) {
	info := statusBarInfo{
		name:   "test",
		agent:  "codex",
		status: "stopped",
	}

	line := formatStatusLine(info, 80)
	if !strings.Contains(line, "test") {
		t.Errorf("expected session name, got %q", line)
	}
	if !strings.Contains(line, "stopped") {
		t.Errorf("expected stopped status, got %q", line)
	}
}

func TestFormatStatusLineNarrowTerminal(t *testing.T) {
	info := statusBarInfo{
		name:        "a-very-long-session-name",
		agent:       "claude",
		status:      "running",
		agentStatus: "active",
		branch:      "some/long/branch/name",
		dirty:       true,
		unpushed:    10,
		unread:      99,
	}

	line := formatStatusLine(info, 40)
	if len(line) > 40 {
		t.Errorf("line length %d exceeds terminal width 40", len(line))
	}
}

func TestFormatStatusLineExactWidth(t *testing.T) {
	info := statusBarInfo{
		name:   "s",
		agent:  "a",
		status: "ok",
	}
	line := formatStatusLine(info, 80)
	if len(line) != 80 {
		t.Errorf("expected line length 80, got %d", len(line))
	}
}

func TestStatusBarInfoFromSession(t *testing.T) {
	session := protocol.SessionInfo{
		Name:          "test-session",
		Agent:         "claude",
		Status:        "running",
		AgentStatus:   "approval",
		Branch:        "user/graith/test-abc",
		Dirty:         true,
		UnpushedCount: 2,
	}
	info := newStatusBarInfo(session, 5)
	if info.name != "test-session" {
		t.Errorf("expected name test-session, got %s", info.name)
	}
	if info.unread != 5 {
		t.Errorf("expected unread 5, got %d", info.unread)
	}
	if info.agentStatus != "approval" {
		t.Errorf("expected agentStatus approval, got %s", info.agentStatus)
	}
}
