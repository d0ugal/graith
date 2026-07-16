package client

import (
	"bytes"
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestFormatStatusLine(t *testing.T) {
	info := statusBarInfo{
		name:        "braw-session",
		agent:       "claude",
		status:      "running",
		agentStatus: "active",
		branch:      "d0ugal/graith/braw-session-abc123",
		dirty:       true,
		unpushed:    3,
		unread:      5,
	}

	line := formatStatusLine(info, 120)

	if lipgloss.Width(line) == 0 {
		t.Fatal("expected non-empty status line")
	}

	if !strings.Contains(line, "braw-session") {
		t.Errorf("expected line to contain session name, got %q", line)
	}

	if !strings.Contains(line, "claude") {
		t.Errorf("expected line to contain agent, got %q", line)
	}

	if !strings.Contains(line, "active") {
		t.Errorf("expected line to contain agent status, got %q", line)
	}

	if !strings.Contains(line, "braw-session-abc123") {
		t.Errorf("expected line to contain short branch, got %q", line)
	}

	if strings.Contains(line, "d0ugal/graith/") {
		t.Errorf("expected short branch, got full prefix in %q", line)
	}
}

func TestFormatStatusLineMinimal(t *testing.T) {
	info := statusBarInfo{
		name:   "neep",
		agent:  "codex",
		status: "stopped",
	}

	line := formatStatusLine(info, 80)
	if !strings.Contains(line, "neep") {
		t.Errorf("expected session name, got %q", line)
	}

	if !strings.Contains(line, "stopped") {
		t.Errorf("expected stopped status, got %q", line)
	}
}

func TestFormatStatusLineNarrowTerminal(t *testing.T) {
	info := statusBarInfo{
		name:        "a-very-long-braw-session-name",
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

// TestFormatReadOnlyLine covers the read-only attach indicator (issue #31): it
// must announce READ-ONLY, name the session, and fill the terminal width.
func TestFormatReadOnlyLine(t *testing.T) {
	info := statusBarInfo{
		name:   "canny-observer",
		agent:  "claude",
		status: "running",
	}

	line := formatReadOnlyLine(info, 80)

	if !strings.Contains(line, "READ-ONLY") {
		t.Errorf("expected READ-ONLY indicator, got %q", line)
	}

	if !strings.Contains(line, "canny-observer") {
		t.Errorf("expected session name in line, got %q", line)
	}

	if !strings.Contains(line, "claude") {
		t.Errorf("expected agent in line, got %q", line)
	}

	if w := lipgloss.Width(line); w != 80 {
		t.Errorf("expected visual width 80, got %d", w)
	}
}

// TestFormatReadOnlyLineNarrow ensures the read-only line never overflows a
// narrow terminal.
func TestFormatReadOnlyLineNarrow(t *testing.T) {
	info := statusBarInfo{name: "a-very-long-session-name-that-overflows", agent: "claude"}

	line := formatReadOnlyLine(info, 20)
	if w := lipgloss.Width(line); w > 20 {
		t.Errorf("expected width <= 20, got %d", w)
	}
}

// TestStatusBarRenderReadOnly verifies the status bar renders the read-only
// indicator (not the normal status line) when readOnly is set.
func TestStatusBarRenderReadOnly(t *testing.T) {
	sb := &statusBarState{
		info:     statusBarInfo{name: "canny-observer", agent: "claude", status: "running"},
		rows:     24,
		cols:     80,
		position: "bottom",
		readOnly: true,
	}

	var buf bytes.Buffer

	sb.render(&buf)

	if !strings.Contains(buf.String(), "READ-ONLY") {
		t.Errorf("expected read-only indicator in rendered bar, got %q", buf.String())
	}
}

func TestStatusBarInfoFromSession(t *testing.T) {
	session := protocol.SessionInfo{
		Name:          "braw-session",
		Agent:         "claude",
		Status:        "running",
		AgentStatus:   "approval",
		Branch:        "user/graith/braw-abc",
		Dirty:         true,
		UnpushedCount: 2,
	}
	fleet := protocol.FleetSummary{Total: 3, Active: 1, Approval: 1, Stopped: 1}

	info := newStatusBarInfo(session, 5, fleet)
	if info.name != "braw-session" {
		t.Errorf("expected name braw-session, got %s", info.name)
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
		name:        "braw-session",
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

func TestFormatStatusLineFleetHiddenWhenSolo(t *testing.T) {
	info := statusBarInfo{
		name:        "braw-session",
		agent:       "claude",
		status:      "running",
		agentStatus: "active",
		fleet:       protocol.FleetSummary{Total: 1, Active: 1},
	}

	line := formatStatusLine(info, 120)
	if strings.Contains(line, "active") && strings.Contains(line, "1 active") {
		t.Errorf("fleet summary should be hidden when only 1 session, got %q", line)
	}
}

func TestFormatStatusLineApprovalProminence(t *testing.T) {
	info := statusBarInfo{
		name:        "braw-session",
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

// --- newStatusBarInfo: PR + CI branches ---

func TestNewStatusBarInfo_WithPRAndCI(t *testing.T) {
	s := protocol.SessionInfo{
		Name:          "braw",
		Agent:         "claude",
		Status:        "running",
		AgentStatus:   "active",
		Branch:        "d0ugal/graith/braw",
		Dirty:         true,
		UnpushedCount: 2,
		PullRequest: &protocol.PRInfo{
			Number:      56,
			State:       "open",
			Conflicting: true,
		},
		CI: &protocol.CIInfo{State: "failing"},
	}

	info := newStatusBarInfo(s, 4, protocol.FleetSummary{Total: 3})

	if info.prNumber != 56 {
		t.Errorf("prNumber = %d, want 56", info.prNumber)
	}

	if info.prState != "open" {
		t.Errorf("prState = %q, want open", info.prState)
	}

	if !info.prConflicting {
		t.Error("prConflicting should be true")
	}

	if info.ciState != "failing" {
		t.Errorf("ciState = %q, want failing", info.ciState)
	}

	if info.unread != 4 {
		t.Errorf("unread = %d, want 4", info.unread)
	}
}

func TestNewStatusBarInfo_NoPRNoCI(t *testing.T) {
	s := protocol.SessionInfo{Name: "canny", Status: "stopped"}
	info := newStatusBarInfo(s, 0, protocol.FleetSummary{})

	if info.prNumber != 0 || info.prState != "" || info.ciState != "" {
		t.Errorf("PR/CI fields should be zero when session has none: %+v", info)
	}
}

// --- formatPRSection: all PR/CI states ---

func TestFormatPRSection_States(t *testing.T) {
	cases := []struct {
		name    string
		info    statusBarInfo
		wantSub string
	}{
		{"none", statusBarInfo{prNumber: 0}, ""},
		{"merged", statusBarInfo{prNumber: 7, prState: "merged"}, "PR#7 merged"},
		{"closed", statusBarInfo{prNumber: 8, prState: "closed"}, "PR#8 closed"},
		{"draft-open", statusBarInfo{prNumber: 9, prState: "draft"}, "PR#9d"},
		{"conflicting", statusBarInfo{prNumber: 10, prState: "open", prConflicting: true}, "PR#10 ⚠conflict"},
		{"draft-conflicting", statusBarInfo{prNumber: 11, prState: "draft", prConflicting: true}, "PR#11d ⚠conflict"},
		{"ci-failing", statusBarInfo{prNumber: 12, prState: "open", ciState: "failing"}, "PR#12 ✗CI"},
		{"ci-passing", statusBarInfo{prNumber: 13, prState: "open", ciState: "passing"}, "PR#13 ✓"},
		{"ci-pending", statusBarInfo{prNumber: 14, prState: "open", ciState: "pending"}, "PR#14 ·CI"},
		{"open-no-ci", statusBarInfo{prNumber: 15, prState: "open"}, "PR#15"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatPRSection(tc.info, barBg)
			if tc.wantSub == "" {
				if got != "" {
					t.Errorf("expected empty section, got %q", got)
				}

				return
			}

			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("formatPRSection = %q, want to contain %q", got, tc.wantSub)
			}
		})
	}
}

// --- formatFleetSection: every status bucket ---

func TestFormatFleetSection_AllBuckets(t *testing.T) {
	fleet := protocol.FleetSummary{
		Total:    12,
		Approval: 1,
		Errored:  2,
		Active:   3,
		Ready:    4,
		Stopped:  2,
	}

	got := formatFleetSection(fleet, accentBg)
	for _, want := range []string{"approval", "error", "active", "ready", "stopped"} {
		if !strings.Contains(got, want) {
			t.Errorf("fleet section %q missing %q", got, want)
		}
	}
}

func TestFormatFleetSection_SoloHidden(t *testing.T) {
	if got := formatFleetSection(protocol.FleetSummary{Total: 1}, accentBg); got != "" {
		t.Errorf("solo fleet should render empty, got %q", got)
	}
}

func TestFormatFleetSection_NoInterestingBuckets(t *testing.T) {
	// Total>1 but no non-zero buckets → empty (parts stay empty).
	if got := formatFleetSection(protocol.FleetSummary{Total: 3}, accentBg); got != "" {
		t.Errorf("fleet with no counted buckets should be empty, got %q", got)
	}
}

// --- formatFleetMinimal ---

func TestFormatFleetMinimal(t *testing.T) {
	cases := []struct {
		name    string
		fleet   protocol.FleetSummary
		wantSub string
	}{
		{"solo", protocol.FleetSummary{Total: 1, Approval: 1}, ""},
		{"approval-wins", protocol.FleetSummary{Total: 5, Approval: 2, Errored: 3}, "approval"},
		{"errored", protocol.FleetSummary{Total: 5, Errored: 2}, "error"},
		{"neither", protocol.FleetSummary{Total: 5, Active: 4}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatFleetMinimal(tc.fleet, accentBg)
			if tc.wantSub == "" {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}

				return
			}

			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("formatFleetMinimal = %q, want %q", got, tc.wantSub)
			}
		})
	}
}

// --- styledStatus: each status branch maps to the right foreground/weight ---

func TestStyledStatus_Branches(t *testing.T) {
	cases := []struct {
		status   string
		wantFg   color.Color
		wantBold bool
	}{
		{"active", colorGreen, false},
		{"running", colorGreen, false},
		{"approval", colorRed, true},
		{"ready", colorBlue, false},
		{"errored", colorRed, false},
		{"stopped", colorDim, false},
		{"unknown", colorDim, false},
	}

	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			style := styledStatus(tc.status, barBg)

			if fg := style.GetForeground(); fg != tc.wantFg {
				t.Errorf("styledStatus(%q) fg = %v, want %v", tc.status, fg, tc.wantFg)
			}

			if style.GetBold() != tc.wantBold {
				t.Errorf("styledStatus(%q) bold = %v, want %v", tc.status, style.GetBold(), tc.wantBold)
			}
		})
	}
}

// --- statusBarState: barRow / scrollRegion for both positions ---

func TestStatusBarState_BarRowAndScrollRegion(t *testing.T) {
	bottom := &statusBarState{rows: 24, cols: 80, position: "bottom"}
	if bottom.barRow() != 24 {
		t.Errorf("bottom barRow = %d, want 24", bottom.barRow())
	}

	if got := bottom.scrollRegion(); got != "\x1b[1;23r" {
		t.Errorf("bottom scrollRegion = %q, want \\x1b[1;23r", got)
	}

	top := &statusBarState{rows: 24, cols: 80, position: "top"}
	if top.barRow() != 1 {
		t.Errorf("top barRow = %d, want 1", top.barRow())
	}

	if got := top.scrollRegion(); got != "\x1b[2;24r" {
		t.Errorf("top scrollRegion = %q, want \\x1b[2;24r", got)
	}
}

// --- statusBarState: render / setup / teardown write escape sequences ---

func TestStatusBarState_RenderWritesLine(t *testing.T) {
	sb := &statusBarState{rows: 24, cols: 80, position: "bottom"}
	sb.updateInfo(statusBarInfo{name: "braw", agent: "claude", status: "running"})

	var buf bytes.Buffer
	sb.render(&buf)

	out := buf.String()
	if !strings.Contains(out, "braw") {
		t.Errorf("render output should include the session name: %q", out)
	}
	// Save/restore cursor sequences frame the write.
	if !strings.Contains(out, "\x1b7") || !strings.Contains(out, "\x1b8") {
		t.Errorf("render should save/restore cursor, got %q", out)
	}
}

func TestStatusBarState_SetupBottomAndTop(t *testing.T) {
	bottom := &statusBarState{rows: 24, cols: 80, position: "bottom"}
	bottom.updateInfo(statusBarInfo{name: "canny", status: "running"})

	var b1 bytes.Buffer
	bottom.setup(&b1)

	if !strings.Contains(b1.String(), "\x1b[1;23r") {
		t.Errorf("bottom setup should emit its scroll region, got %q", b1.String())
	}

	top := &statusBarState{rows: 24, cols: 80, position: "top"}
	top.updateInfo(statusBarInfo{name: "bonnie", status: "running"})

	var b2 bytes.Buffer
	top.setup(&b2)

	out := b2.String()
	if !strings.Contains(out, "\x1b[2;24r") {
		t.Errorf("top setup should emit its scroll region, got %q", out)
	}
	// Top position also positions the cursor at row 2.
	if !strings.Contains(out, "\x1b[2;1H") {
		t.Errorf("top setup should home the cursor below the bar, got %q", out)
	}
}

func TestStatusBarState_Teardown(t *testing.T) {
	sb := &statusBarState{rows: 24, cols: 80, position: "bottom"}

	var buf bytes.Buffer
	sb.teardown(&buf)

	out := buf.String()
	// Clears the bar row and resets the scroll region.
	if !strings.Contains(out, "\x1b[24;1H") || !strings.Contains(out, "\x1b[2K") || !strings.Contains(out, "\x1b[r") {
		t.Errorf("teardown should clear the row and reset scroll region, got %q", out)
	}
}

// --- statusBarState: mutators are reflected in render ---

func TestStatusBarState_PendingApprovalsInRender(t *testing.T) {
	sb := &statusBarState{rows: 24, cols: 120, position: "bottom"}
	sb.updateInfo(statusBarInfo{name: "thrawn", agent: "claude", status: "running"})
	sb.updatePendingApprovals(3)

	var buf bytes.Buffer
	sb.render(&buf)

	if !strings.Contains(buf.String(), "3 pending") {
		t.Errorf("render should reflect pending approvals, got %q", buf.String())
	}
}

func TestStatusBarState_UpdateSize(t *testing.T) {
	sb := &statusBarState{position: "bottom"}
	sb.updateSize(40, 100)

	sb.mu.Lock()
	rows, cols := sb.rows, sb.cols
	sb.mu.Unlock()

	if rows != 40 || cols != 100 {
		t.Errorf("updateSize set rows=%d cols=%d, want 40/100", rows, cols)
	}

	if sb.barRow() != 40 {
		t.Errorf("barRow after resize = %d, want 40", sb.barRow())
	}
}
