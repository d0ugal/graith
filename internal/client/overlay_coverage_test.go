package client

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

// --- sessionItem / groupHeader Title & Description ---

func TestSessionItemTitleDescription(t *testing.T) {
	si := sessionItem{info: protocol.SessionInfo{Name: "braw"}}
	if si.Title() != "braw" {
		t.Errorf("Title() = %q, want braw", si.Title())
	}

	if si.Description() != "" {
		t.Errorf("Description() = %q, want empty", si.Description())
	}
}

func TestGroupHeaderTitleDescription(t *testing.T) {
	gh := groupHeader{name: "croft", count: 3}
	if gh.Title() != "croft" {
		t.Errorf("Title() = %q, want croft", gh.Title())
	}

	if gh.Description() != "" {
		t.Errorf("Description() = %q, want empty", gh.Description())
	}
}

// --- filterStarred ---

func TestFilterStarred(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "braw", Starred: true},
		{ID: "s2", Name: "canny", Starred: false},
		{ID: "s3", Name: "bonnie", Starred: true},
	}

	starred := filterStarred(sessions)
	if len(starred) != 2 {
		t.Fatalf("expected 2 starred, got %d", len(starred))
	}

	for _, s := range starred {
		if !s.Starred {
			t.Errorf("filterStarred returned unstarred session %q", s.Name)
		}
	}
}

func TestFilterStarred_None(t *testing.T) {
	sessions := []protocol.SessionInfo{{ID: "s1", Starred: false}}
	if got := filterStarred(sessions); got != nil {
		t.Errorf("no starred sessions should return nil, got %v", got)
	}
}

// --- sortByStatusAge: zero-time handling ---

func TestSortByStatusAge_ZeroTimesStable(t *testing.T) {
	// Both zero → keep order (return false).
	sessions := []protocol.SessionInfo{
		{ID: "a", Name: "first"},
		{ID: "b", Name: "second"},
	}
	sortByStatusAge(sessions)

	if sessions[0].ID != "a" || sessions[1].ID != "b" {
		t.Errorf("zero-time entries should keep their order, got %v", sessions)
	}
}

func TestSortByStatusAge_ZeroSortsBeforeNonZero(t *testing.T) {
	old := time.Now().Add(-time.Hour).Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "hasTime", StatusChangedAt: old},
		{ID: "zero"},
	}
	sortByStatusAge(sessions)

	// A zero StatusChangedAt is treated as oldest (sorts first).
	if sessions[0].ID != "zero" {
		t.Errorf("zero-time entry should sort first, got %q", sessions[0].ID)
	}
}

// --- displayPR pending & displaySummary truncation & shortenPath ---

func TestDisplayPR_Pending(t *testing.T) {
	s := protocol.SessionInfo{
		PullRequest: &protocol.PRInfo{Number: 42, State: "open"},
		CI:          &protocol.CIInfo{State: "pending"},
	}
	if got := displayPR(s); got != "#42 ·" {
		t.Errorf("displayPR pending = %q, want #42 ·", got)
	}
}

func TestDisplayPR_DraftFallsThroughToCI(t *testing.T) {
	s := protocol.SessionInfo{
		PullRequest: &protocol.PRInfo{Number: 9, State: "draft"},
		CI:          &protocol.CIInfo{State: "passing"},
	}
	if got := displayPR(s); got != "#9d ✓" {
		t.Errorf("displayPR draft+passing = %q, want #9d ✓", got)
	}
}

func TestDisplaySummary_Truncates(t *testing.T) {
	long := strings.Repeat("x", maxSummaryWidth+20)
	s := protocol.SessionInfo{SummaryText: long}

	got := displaySummary(s)
	if len([]rune(got)) != maxSummaryWidth {
		t.Errorf("truncated summary length = %d runes, want %d", len([]rune(got)), maxSummaryWidth)
	}

	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated summary should end with ellipsis, got %q", got)
	}
}

func TestDisplaySummary_Empty(t *testing.T) {
	if got := displaySummary(protocol.SessionInfo{}); got != "" {
		t.Errorf("empty summary should stay empty, got %q", got)
	}
}

func TestShortenPath_HomePrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := shortenPath(home + "/Code/graith")
	if got != "~/Code/graith" {
		t.Errorf("shortenPath = %q, want ~/Code/graith", got)
	}
}

func TestShortenPath_NoHomePrefix(t *testing.T) {
	got := shortenPath("/opt/other/place")
	if got != "/opt/other/place" {
		t.Errorf("path outside home should be unchanged, got %q", got)
	}
}

// --- buildScenarioGroupedItems ---

func scenarioSessions() []protocol.SessionInfo {
	return []protocol.SessionInfo{
		{ID: "a", Name: "backend", Status: "running", ScenarioID: "sc-1", ScenarioName: "strath"},
		{ID: "b", Name: "frontend", Status: "stopped", ScenarioID: "sc-1", ScenarioName: "strath"},
		{ID: "c", Name: "loner", Status: "running"}, // no scenario
	}
}

func TestBuildScenarioGroupedItems_GroupsAndUngrouped(t *testing.T) {
	items := buildScenarioGroupedItems(scenarioSessions(), nil)

	var headers []groupHeader

	sessionCount := 0

	for _, it := range items {
		switch v := it.(type) {
		case groupHeader:
			headers = append(headers, v)
		case sessionItem:
			sessionCount++
		}
	}

	if len(headers) != 2 {
		t.Fatalf("expected 2 headers (strath + no scenario), got %d", len(headers))
	}

	if !strings.HasPrefix(headers[0].name, "strath") {
		t.Errorf("first header = %q, want strath...", headers[0].name)
	}
	// strath has one running + one stopped → "(partial)".
	if !strings.Contains(headers[0].name, "(partial)") {
		t.Errorf("mixed scenario should be partial, got %q", headers[0].name)
	}

	if headers[1].name != "(no scenario)" {
		t.Errorf("ungrouped header = %q, want (no scenario)", headers[1].name)
	}

	if sessionCount != 3 {
		t.Errorf("expected 3 session items, got %d", sessionCount)
	}
}

func TestBuildScenarioGroupedItems_StatusLabels(t *testing.T) {
	cases := []struct {
		name     string
		statuses []string
		want     string
	}{
		{"all-running", []string{"running", "running"}, "(running)"},
		{"all-stopped", []string{"stopped", "stopped"}, "(stopped)"},
		{"errored", []string{"running", "errored"}, "(errored)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sessions []protocol.SessionInfo
			for i, st := range tc.statuses {
				sessions = append(sessions, protocol.SessionInfo{
					ID:           string(rune('a' + i)),
					Name:         "sess",
					Status:       st,
					ScenarioID:   "sc",
					ScenarioName: "clachan",
				})
			}

			items := buildScenarioGroupedItems(sessions, nil)
			gh := items[0].(groupHeader)

			if !strings.Contains(gh.name, tc.want) {
				t.Errorf("header %q should contain %q", gh.name, tc.want)
			}
		})
	}
}

func TestBuildScenarioGroupedItems_FallsBackToScenarioID(t *testing.T) {
	// No ScenarioName set → the group name falls back to the scenario id.
	sessions := []protocol.SessionInfo{
		{ID: "a", Name: "x", Status: "running", ScenarioID: "sc-xyz"},
	}
	items := buildScenarioGroupedItems(sessions, nil)

	gh := items[0].(groupHeader)
	if !strings.HasPrefix(gh.name, "sc-xyz") {
		t.Errorf("header should fall back to scenario id, got %q", gh.name)
	}
}

// --- scenario view via the model ---

func TestOverlay_ScenarioViewGroups(t *testing.T) {
	m := newOverlayModel(scenarioSessions(), "", nil, nil, nil, nil)
	m.width = 120
	m.height = 40
	m.view = viewScenario
	m.rebuildForView()

	foundHeader := false

	for _, it := range m.list.Items() {
		if gh, ok := it.(groupHeader); ok && strings.HasPrefix(gh.name, "strath") {
			foundHeader = true
			break
		}
	}

	if !foundHeader {
		t.Error("scenario view should build scenario-grouped headers")
	}
}

// --- refreshSessionsCmd ---

func TestRefreshSessionsCmd_Nil(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	if m.refreshSessionsCmd() != nil {
		t.Error("refreshSessionsCmd should be nil when refreshSessions is unset")
	}
}

func TestRefreshSessionsCmd_ProducesMsg(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	m.refreshSessions = func() []protocol.SessionInfo { return sessions }

	cmd := m.refreshSessionsCmd()
	if cmd == nil {
		t.Fatal("refreshSessionsCmd should return a command")
	}

	produced := cmd()

	msg, ok := produced.(refreshSessionsMsg)
	if !ok {
		t.Fatalf("expected refreshSessionsMsg, got %T", produced)
	}

	if len(msg.sessions) != len(sessions) {
		t.Errorf("refreshed %d sessions, want %d", len(msg.sessions), len(sessions))
	}
}

func TestRefreshTickMsg_TriggersRefresh(t *testing.T) {
	sessions := overlayTestSessions()
	fetched := false
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	m.width, m.height = 120, 40
	m.refreshSessions = func() []protocol.SessionInfo {
		fetched = true
		return sessions
	}

	_, cmd := m.Update(refreshTickMsg{})
	if cmd == nil {
		t.Fatal("refreshTickMsg in list state should return a refresh command")
	}

	cmd() // run the refresh
	if !fetched {
		t.Error("refresh tick should invoke refreshSessions in list state")
	}
}

// --- selectSessionByID: parent-chain fallback ---

func TestSelectSessionByID_WalksToVisibleAncestor(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child", Name: "bairn", ParentID: "root", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	// Collapse root so the child isn't directly visible.
	collapsed := map[string]bool{"root": true}
	m := newOverlayModel(sessions, "", nil, nil, collapsed, nil)
	m.width, m.height = 120, 40

	m.selectSessionByID("child")

	item, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		t.Fatal("expected a sessionItem selected")
	}
	// child is hidden, so it should land on the visible ancestor (root).
	if item.info.ID != "root" {
		t.Errorf("selectSessionByID(child) landed on %q, want visible ancestor root", item.info.ID)
	}
}

func TestSelectSessionByID_UnknownFallsToFirstSession(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.width, m.height = 120, 40

	m.selectSessionByID("does-not-exist")
	// Should not panic; selection should be a session item (skips header).
	if _, ok := m.list.SelectedItem().(groupHeader); ok {
		t.Error("selection should not rest on a group header")
	}
}

// --- newOverlayModel: cursor walks parent chain when current is hidden ---

func TestNewOverlayModel_CursorWalksToVisibleParent(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child", Name: "bairn", ParentID: "root", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	collapsed := map[string]bool{"root": true}
	// current session is the hidden child.
	m := newOverlayModel(sessions, "child", nil, nil, collapsed, nil)

	item, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		t.Fatal("expected a sessionItem selected")
	}

	if item.info.ID != "root" {
		t.Errorf("cursor should walk to visible parent root, got %q", item.info.ID)
	}
}

// --- Update: view switching left/right ---

func TestUpdate_ViewSwitchWraps(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil, nil, nil)
	m.width, m.height = 120, 40

	// Left from viewAll wraps to the last view (viewScenario).
	updated, _ := sendKey(m, "h")
	om := asOverlay(updated)

	if om.view != viewScenario {
		t.Errorf("left from viewAll should wrap to viewScenario, got %v", om.view)
	}

	// Right wraps back to viewAll.
	updated, _ = sendKey(om, "l")
	om = asOverlay(updated)

	if om.view != viewAll {
		t.Errorf("right should wrap back to viewAll, got %v", om.view)
	}
}

// --- Update: "n" opens the create form ---

func TestUpdate_NewOpensCreateForm(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil, nil, nil)
	m.width, m.height = 120, 40

	updated, _ := sendKey(m, "n")
	om := asOverlay(updated)

	if om.state != stateCreate {
		t.Errorf("n should enter stateCreate, got %v", om.state)
	}

	if om.createModel == nil {
		t.Error("n should build a createModel")
	}
}

func TestUpdate_CreateFormEscReturnsToList(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil, nil, nil)
	m.width, m.height = 120, 40

	updated, _ := sendKey(m, "n")
	updated, _ = sendSpecialKey(updated, tea.KeyEscape)
	om := asOverlay(updated)

	if om.state != stateList {
		t.Errorf("esc in create form should return to list, got %v", om.state)
	}

	if om.createModel != nil {
		t.Error("createModel should be cleared after esc")
	}
}

// --- Update: restart single confirm ---

func TestUpdate_RestartSingleConfirm(t *testing.T) {
	sessions := overlayTestSessions()

	var restarted string

	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	m.restartSession = func(id string) error {
		restarted = id
		return nil
	}
	m.width, m.height = 120, 40

	selected := m.list.SelectedItem().(sessionItem)

	updated, _ := sendKey(m, "r")
	om := asOverlay(updated)

	if om.state != stateConfirmRestart {
		t.Fatalf("r should enter stateConfirmRestart, got %v", om.state)
	}

	updated, cmd := sendKey(updated, "y")
	if cmd == nil {
		t.Fatal("y should return a restart command")
	}

	updated, _ = updated.Update(cmd())
	om = asOverlay(updated)

	if restarted != selected.info.ID {
		t.Errorf("restart called with %q, want %q", restarted, selected.info.ID)
	}

	if om.state != stateList {
		t.Errorf("state after restart = %v, want stateList", om.state)
	}
}

// --- Update: delete removes session and rebuilds ---

func TestUpdate_DeleteResultRemovesSession(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", noopFetchPreview, nil, nil, nil)
	m.width, m.height = 120, 40

	updated, _ := m.Update(deleteResultMsg{sessionID: "s1"})
	om := asOverlay(updated)

	for _, s := range om.allSessions {
		if s.ID == "s1" {
			t.Fatal("s1 should be removed after deleteResultMsg")
		}
	}

	if len(om.allSessions) != len(sessions)-1 {
		t.Errorf("allSessions = %d, want %d", len(om.allSessions), len(sessions)-1)
	}
}

func TestUpdate_DeleteResultLastSessionQuits(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "only", Name: "neep", RepoName: "repo", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	m := newOverlayModel(sessions, "", noopFetchPreview, nil, nil, nil)
	m.width, m.height = 120, 40

	_, cmd := m.Update(deleteResultMsg{sessionID: "only"})
	if cmd == nil {
		t.Fatal("deleting the last session should return a command")
	}

	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("deleting the last session should quit the overlay, got %T", cmd())
	}
}

func TestUpdate_DeleteResultErrorReturnsToList(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.width, m.height = 120, 40
	m.state = stateConfirmDelete

	updated, _ := m.Update(deleteResultMsg{sessionID: "s1", err: errFake})
	om := asOverlay(updated)

	if om.state != stateList {
		t.Errorf("delete error should return to list, got %v", om.state)
	}

	if len(om.allSessions) != 3 {
		t.Errorf("delete error should not remove the session, got %d", len(om.allSessions))
	}
}

// --- Update: star toggle result updates state ---

func TestUpdate_StarResultUpdatesSession(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil, nil, nil)
	m.width, m.height = 120, 40

	updated, _ := m.Update(starResultMsg{sessionID: "s1", starred: true})
	om := asOverlay(updated)

	for _, s := range om.allSessions {
		if s.ID == "s1" && !s.Starred {
			t.Error("s1 should be starred after starResultMsg")
		}
	}
}

// --- View: starred and scenario empty states ---

func TestView_StarredEmptyState(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.width, m.height = 120, 40
	m.view = viewStarred
	m.rebuildForView()

	out := m.View().Content
	if !strings.Contains(out, "No starred sessions") {
		t.Errorf("empty starred view should show its empty message:\n%s", out)
	}
}

func TestView_ActiveEmptyState(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "neep", RepoName: "repo", Status: "stopped", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	m.width, m.height = 120, 40
	m.view = viewActive
	m.rebuildForView()

	out := m.View().Content
	if !strings.Contains(out, "No active sessions") {
		t.Errorf("empty active view should show its empty message:\n%s", out)
	}
}

func TestView_ProfileShownInTitle(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.width, m.height = 120, 40
	m.profile = "bothy"

	out := m.View().Content
	if !strings.Contains(out, "bothy") {
		t.Errorf("view title should include the active profile:\n%s", out)
	}
}

func TestView_RestartMenuShowsCounts(t *testing.T) {
	// overlayTestSessions(): 3 sessions, one stopped (s2), none config-stale.
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.restartSession = func(string) error { return nil }
	m.width, m.height = 120, 40
	m.state = stateRestartMenu

	out := m.View().Content
	for _, want := range []string{"Restart:", "[a]ll (3)", "[o]utdated (0)", "[s]topped (1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("restart menu should show %q:\n%s", want, out)
		}
	}
}

var errFake = fakeErr("boom")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }
