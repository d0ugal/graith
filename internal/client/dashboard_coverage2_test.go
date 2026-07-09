package client

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

// richDashSessions exercises the column-sizing and rendering branches that the
// minimal two-session fixture doesn't: git dirty/unpushed, activity states,
// scenario-style branch names, and last-attached timestamps.
func richDashSessions() []protocol.SessionInfo {
	now := time.Now()

	return []protocol.SessionInfo{
		{
			ID: "s1", Name: "braw-longish-name", RepoName: "croft", Agent: "claude",
			Status: "running", AgentStatus: "approval", Branch: "graith/braw/s1",
			Dirty: true, UnpushedCount: 3,
			CreatedAt:      now.Add(-90 * time.Minute).Format(time.RFC3339),
			LastAttachedAt: now.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		{
			ID: "s2", Name: "canny", RepoName: "bothy", Agent: "codex",
			Status: "stopped", AgentStatus: "working", Branch: "feature-x",
			CreatedAt: now.Add(-3 * time.Hour).Format(time.RFC3339),
		},
		{
			ID: "s3", Name: "dreich", RepoName: "croft", Agent: "cursor",
			Status: "errored", Branch: "graith/dreich/s3",
			CreatedAt: "not-a-timestamp", LastAttachedAt: "also-bad",
		},
	}
}

func TestDashboardInitReturnsTick2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	if m.Init() == nil {
		t.Error("Init should return a tick cmd")
	}
}

func TestDashboardTickBatchesRefresh2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), func() []protocol.SessionInfo { return nil })
	m.width, m.height = 120, 40

	_, cmd := m.Update(tickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("tickMsg should schedule a batch (tick + refresh)")
	}
}

func TestDashboardDoRefreshProducesMsg2(t *testing.T) {
	want := richDashSessions()
	m := NewDashboardModel(nil, func() []protocol.SessionInfo { return want })

	cmd := m.doRefresh()
	msg := cmd()

	rm, ok := msg.(refreshMsg)
	if !ok {
		t.Fatalf("doRefresh cmd produced %T, want refreshMsg", msg)
	}

	if len(rm.sessions) != 3 {
		t.Fatalf("refreshMsg carried %d sessions, want 3", len(rm.sessions))
	}
}

func TestDashboardRefreshRepositionsCursorToSelectedID2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	m.width, m.height = 120, 40
	m.cursor = 0 // selected s1

	// Refresh with sessions reordered so s1 moves to index 2.
	reordered := []protocol.SessionInfo{
		richDashSessions()[1],
		richDashSessions()[2],
		richDashSessions()[0],
	}

	res, _ := m.Update(refreshMsg{sessions: reordered})
	dm := res.(DashboardModel)

	if dm.sessions[dm.cursor].ID != "s1" {
		t.Fatalf("cursor should track s1 after reorder, landed on %q", dm.sessions[dm.cursor].ID)
	}
}

func TestDashboardRefreshClearsConfirmWhenTargetGone2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	m.width, m.height = 120, 40
	m.state = dashStateConfirmDelete
	m.confirmSessionID = "s2"

	// Refresh without s2.
	res, _ := m.Update(refreshMsg{sessions: []protocol.SessionInfo{richDashSessions()[0]}})
	dm := res.(DashboardModel)

	if dm.state != dashStateNormal || dm.confirmSessionID != "" {
		t.Fatalf("confirm should clear when target vanishes: state=%d id=%q", dm.state, dm.confirmSessionID)
	}
}

func TestDashboardRefreshClearsStopConfirmWhenTargetStopped2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	m.width, m.height = 120, 40
	m.state = dashStateConfirmStop
	m.confirmSessionID = "s1"

	// s1 comes back stopped — the stop confirm no longer applies.
	stopped := richDashSessions()
	stopped[0].Status = "stopped"

	res, _ := m.Update(refreshMsg{sessions: stopped})
	dm := res.(DashboardModel)

	if dm.state != dashStateNormal {
		t.Fatalf("stop-confirm should clear when target stops, state=%d", dm.state)
	}
}

func TestDashboardConfirmStopThenCancelWithAnyKey2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	m.width, m.height = 120, 40
	m.state = dashStateConfirmStop
	m.confirmSessionID = "s1"

	res, _ := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	dm := res.(DashboardModel)

	if dm.state != dashStateNormal || dm.confirmSessionID != "" {
		t.Fatalf("any non-y key should cancel stop confirm: state=%d", dm.state)
	}
}

func TestDashboardConfirmStopYieldsResult2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	m.width, m.height = 120, 40
	m.state = dashStateConfirmStop
	m.confirmSessionID = "s1"

	res, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	dm := res.(DashboardModel)

	if dm.result == nil || dm.result.Action != "stop" || dm.result.SessionID != "s1" {
		t.Fatalf("stop confirm y should yield stop result: %+v", dm.result)
	}

	if cmd == nil {
		t.Error("expected quit cmd on confirmed stop")
	}
}

func TestDashboardStopKeyOnlyForRunning2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	m.width, m.height = 120, 40
	m.cursor = 1 // s2 is stopped

	res, _ := m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	dm := res.(DashboardModel)

	if dm.state != dashStateNormal {
		t.Errorf("s on a stopped session should not enter stop-confirm, state=%d", dm.state)
	}
}

func TestDashboardResumeKeyOnlyForStopped2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	m.width, m.height = 120, 40

	// cursor 0 (running) — r does nothing.
	res, _ := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if res.(DashboardModel).result != nil {
		t.Error("r on running session should not resume")
	}

	// cursor 1 (stopped) — r resumes.
	m.cursor = 1
	res, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	dm := res.(DashboardModel)

	if dm.result == nil || dm.result.Action != "resume" || dm.result.SessionID != "s2" {
		t.Fatalf("r on stopped session should resume: %+v", dm.result)
	}

	if cmd == nil {
		t.Error("expected quit cmd on resume")
	}
}

func TestDashboardQuitKeys2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	m.width, m.height = 120, 40

	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd == nil {
		t.Error("q should quit")
	}
}

func TestDashboardWindowSizeScrolls2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	res, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	dm := res.(DashboardModel)

	if dm.width != 100 || dm.height != 20 {
		t.Fatalf("window size not applied: w=%d h=%d", dm.width, dm.height)
	}
}

func TestDashboardVisibleRowsReservesForConfirm2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	m.height = 20

	normal := m.visibleRows()

	m.state = dashStateConfirmDelete
	confirming := m.visibleRows()

	if confirming != normal-2 {
		t.Fatalf("confirm state should reserve 2 extra rows: normal=%d confirm=%d", normal, confirming)
	}
}

func TestDashboardVisibleRowsFloorsAtOne2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	m.height = 2

	if got := m.visibleRows(); got != 1 {
		t.Fatalf("visibleRows should floor at 1, got %d", got)
	}
}

func TestDashboardScrollToCursorWindows2(t *testing.T) {
	sessions := make([]protocol.SessionInfo, 20)
	for i := range sessions {
		sessions[i] = protocol.SessionInfo{ID: string(rune('a' + i)), Name: "s", Status: "running"}
	}

	m := NewDashboardModel(sessions, nil)
	m.height = 12 // ~6 visible rows

	// Jump cursor to the bottom; offset must follow so cursor stays visible.
	m.cursor = 19
	m.scrollToCursor()

	if m.cursor < m.offset || m.cursor >= m.offset+m.visibleRows() {
		t.Fatalf("cursor %d outside window [%d,%d)", m.cursor, m.offset, m.offset+m.visibleRows())
	}

	// Move cursor above the window; offset must move up to it.
	m.cursor = 0
	m.scrollToCursor()

	if m.offset != 0 {
		t.Fatalf("offset should reset to 0 when cursor at top, got %d", m.offset)
	}
}

func TestDashboardClampCursorEmpty2(t *testing.T) {
	m := NewDashboardModel(nil, nil)
	m.cursor = 5
	m.clampCursor()

	if m.cursor != 0 {
		t.Fatalf("clampCursor on empty list should yield 0, got %d", m.cursor)
	}
}

func TestDashboardComputeColsAndRenderRow2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	cols := m.computeDashCols()

	// The longest name drives the name column.
	if cols.name < len("braw-longish-name") {
		t.Errorf("name column %d too small for longest name", cols.name)
	}

	// Row for the dirty+unpushed running session with approval activity.
	dim := m.View() // ensure View path is exercised end-to-end
	_ = dim

	now := time.Now()
	style := lipgloss.NewStyle()
	row := m.renderRow(richDashSessions()[0], cols, now, true, style, style)

	for _, want := range []string{"braw-longish-name", "dirty", "3↑"} {
		if !strings.Contains(row, want) {
			t.Errorf("rendered row missing %q:\n%s", want, row)
		}
	}
}

func TestDashboardViewErroredAndBadTimestamps2(t *testing.T) {
	m := NewDashboardModel(richDashSessions(), nil)
	m.width, m.height = 140, 40
	m.cursor = 2 // errored session with bad timestamps

	out := m.View().Content
	if !strings.Contains(out, "dreich") {
		t.Errorf("view should render the errored session name:\n%s", out)
	}
}

func TestDashboardViewScrollIndicators2(t *testing.T) {
	sessions := make([]protocol.SessionInfo, 30)
	for i := range sessions {
		sessions[i] = protocol.SessionInfo{
			ID: string(rune('a' + i)), Name: "sesh", RepoName: "croft",
			Agent: "claude", Status: "running",
			CreatedAt: time.Now().Format(time.RFC3339),
		}
	}

	m := NewDashboardModel(sessions, nil)
	m.width, m.height = 120, 16
	m.cursor = 20
	m.scrollToCursor()

	out := m.View().Content
	if !strings.Contains(out, "more above") || !strings.Contains(out, "more below") {
		t.Errorf("expected both scroll indicators in windowed view:\n%s", out)
	}
}

func TestSortDashboardSessions2(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{Name: "zed", RepoName: "croft"},
		{Name: "aaa", RepoName: "croft"},
		{Name: "mid", RepoName: "bothy"},
	}

	sortDashboardSessions(sessions)

	// Sorted by repo then name: bothy/mid, croft/aaa, croft/zed.
	if sessions[0].RepoName != "bothy" || sessions[1].Name != "aaa" || sessions[2].Name != "zed" {
		t.Fatalf("unexpected sort order: %+v", sessions)
	}
}
