package client

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
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

	line := formatStatusLine(info, 120)

	if lipgloss.Width(line) == 0 {
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
	if w := lipgloss.Width(line); w > 40 {
		t.Errorf("visual width %d exceeds terminal width 40", w)
	}
}

func TestFormatStatusLineExactWidth(t *testing.T) {
	info := statusBarInfo{
		name:   "s",
		agent:  "a",
		status: "ok",
	}
	line := formatStatusLine(info, 80)
	if w := lipgloss.Width(line); w != 80 {
		t.Errorf("expected visual width 80, got %d", w)
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
	fleet := protocol.FleetSummary{Total: 3, Active: 1, Approval: 1, Stopped: 1}
	info := newStatusBarInfo(session, 5, fleet)
	if info.name != "test-session" {
		t.Errorf("expected name test-session, got %s", info.name)
	}
	if info.unread != 5 {
		t.Errorf("expected unread 5, got %d", info.unread)
	}
	if info.agentStatus != "approval" {
		t.Errorf("expected agentStatus approval, got %s", info.agentStatus)
	}
	if info.fleet.Total != 3 {
		t.Errorf("expected fleet total 3, got %d", info.fleet.Total)
	}
}

func TestFormatStatusLineFleetSummary(t *testing.T) {
	info := statusBarInfo{
		name:        "my-session",
		agent:       "claude",
		status:      "running",
		agentStatus: "active",
		fleet:       protocol.FleetSummary{Total: 5, Active: 3, Approval: 2},
	}

	line := formatStatusLine(info, 120)
	if !strings.Contains(line, "2 approval") {
		t.Errorf("expected line to contain approval count, got %q", line)
	}
	if !strings.Contains(line, "3 active") {
		t.Errorf("expected line to contain active count, got %q", line)
	}
}

func TestFormatStatusLineFleetHiddenWhenEmpty(t *testing.T) {
	info := statusBarInfo{
		name:        "my-session",
		agent:       "claude",
		status:      "running",
		agentStatus: "active",
		fleet:       protocol.FleetSummary{},
	}

	line := formatStatusLine(info, 120)
	if strings.Contains(line, "0 active") || strings.Contains(line, "0 stopped") {
		t.Errorf("fleet summary should be hidden when no sessions, got %q", line)
	}
}

func TestFormatStatusLineFleetShownForSingleSession(t *testing.T) {
	info := statusBarInfo{
		name:        "my-session",
		agent:       "claude",
		status:      "running",
		agentStatus: "active",
		fleet:       protocol.FleetSummary{Total: 1, Active: 1},
	}

	line := formatStatusLine(info, 120)
	if !strings.Contains(line, "1 active") {
		t.Errorf("fleet summary should show for single session, got %q", line)
	}
}

func TestFormatStatusLineApprovalProminence(t *testing.T) {
	info := statusBarInfo{
		name:        "my-session",
		agent:       "claude",
		status:      "running",
		agentStatus: "active",
		fleet:       protocol.FleetSummary{Total: 4, Active: 2, Approval: 1, Errored: 1},
	}

	line := formatStatusLine(info, 120)
	approvalIdx := strings.Index(line, "approval")
	errorIdx := strings.Index(line, "error")
	activeIdx := strings.LastIndex(line, "active")

	if approvalIdx < 0 {
		t.Fatal("expected approval in fleet summary")
	}
	if errorIdx < 0 {
		t.Fatal("expected error in fleet summary")
	}
	// Approval should appear before error and active in the fleet section
	if approvalIdx > errorIdx {
		t.Errorf("approval should appear before error in fleet summary")
	}
	// activeIdx here is from fleet section (last occurrence), should be after approval
	if activeIdx < approvalIdx {
		t.Errorf("fleet active count should appear after approval")
	}
}
