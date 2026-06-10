package client

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

func overlayTestSessions() []protocol.SessionInfo {
	return []protocol.SessionInfo{
		{
			ID:             "s1",
			Name:           "fix-overlay",
			RepoName:       "graith",
			Branch:         "d0ugal/graith/fix-overlay",
			Agent:          "claude",
			Status:         "running",
			CreatedAt:      time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
			LastAttachedAt: time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
		},
		{
			ID:        "s2",
			Name:      "add-tests",
			RepoName:  "graith",
			Branch:    "d0ugal/graith/add-tests",
			Agent:     "claude",
			Status:    "stopped",
			CreatedAt: time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
		},
		{
			ID:             "s3",
			Name:           "feature-x",
			RepoName:       "other-repo",
			Branch:         "main",
			Agent:          "codex",
			Status:         "running",
			CreatedAt:      time.Now().Add(-30 * time.Minute).Format(time.RFC3339),
			LastAttachedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
		},
	}
}

func overlayTestSessionsWithGitStatus() []protocol.SessionInfo {
	return []protocol.SessionInfo{
		{
			ID:            "s1",
			Name:          "dirty-session",
			RepoName:      "graith",
			Branch:        "d0ugal/graith/dirty",
			Agent:         "claude",
			Status:        "running",
			AgentStatus:   "thinking",
			Dirty:         true,
			UnpushedCount: 3,
			CreatedAt:     time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
		},
	}
}

func noopFetchPreview(sessionID string) string {
	return "preview for " + sessionID
}

func sendKey(m tea.Model, key string) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
}

func sendSpecialKey(m tea.Model, k rune) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyPressMsg{Code: k})
}

func sendShiftTab(m tea.Model) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
}

func sendWindowSize(m tea.Model, w, h int) (tea.Model, tea.Cmd) {
	return m.Update(tea.WindowSizeMsg{Width: w, Height: h})
}

func asOverlay(m tea.Model) overlayModel {
	return m.(overlayModel)
}

// --- buildGroupedItems ---

func TestBuildGroupedItems_GroupsByRepo(t *testing.T) {
	sessions := overlayTestSessions()
	items := buildGroupedItems(sessions)

	// With new sorting (running+recent first), the graith group has:
	// fix-overlay (running, recently attached) then add-tests (stopped)
	// Plus 2 group headers = 5 items total
	if len(items) != 5 {
		t.Fatalf("expected 5 items (2 headers + 3 sessions), got %d", len(items))
	}

	gh1, ok := items[0].(groupHeader)
	if !ok {
		t.Fatal("items[0] should be a groupHeader")
	}
	if gh1.name != "graith" {
		t.Errorf("first group = %q, want %q", gh1.name, "graith")
	}
	if gh1.count != 2 {
		t.Errorf("first group count = %d, want 2", gh1.count)
	}

	gh2, ok := items[3].(groupHeader)
	if !ok {
		t.Fatal("items[3] should be a groupHeader")
	}
	if gh2.name != "other-repo" {
		t.Errorf("second group = %q, want %q", gh2.name, "other-repo")
	}
}

func TestBuildGroupedItems_EmptyRepoName(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "orphan", RepoName: "", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	items := buildGroupedItems(sessions)
	gh := items[0].(groupHeader)
	if gh.name != "(no repo)" {
		t.Errorf("empty repo should show as %q, got %q", "(no repo)", gh.name)
	}
}

func TestBuildGroupedItems_Empty(t *testing.T) {
	items := buildGroupedItems(nil)
	if len(items) != 0 {
		t.Errorf("expected 0 items for nil sessions, got %d", len(items))
	}
}

func TestBuildGroupedItems_GroupsSorted(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "1", Name: "z", RepoName: "zzz", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "2", Name: "a", RepoName: "aaa", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	items := buildGroupedItems(sessions)
	gh1 := items[0].(groupHeader)
	gh2 := items[2].(groupHeader)
	if gh1.name != "aaa" || gh2.name != "zzz" {
		t.Errorf("groups should be sorted alphabetically, got %q then %q", gh1.name, gh2.name)
	}
}

func TestBuildGroupedItems_SessionCount(t *testing.T) {
	sessions := overlayTestSessions()
	items := buildGroupedItems(sessions)
	gh := items[0].(groupHeader)
	if gh.count != 2 {
		t.Errorf("graith group count = %d, want 2", gh.count)
	}
	gh2 := items[3].(groupHeader)
	if gh2.count != 1 {
		t.Errorf("other-repo group count = %d, want 1", gh2.count)
	}
}

// --- sortSessions ---

func TestSortSessions_CurrentNotBoosted(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "a", Name: "alpha", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "b", Name: "beta", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	SortSessions(sessions)
	if sessions[0].ID != "a" {
		t.Errorf("current session should not be boosted, expected alpha first, got %q", sessions[0].ID)
	}
}

func TestSortSessions_RunningBeforeStopped(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "a", Name: "alpha", Status: "stopped", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "b", Name: "beta", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	SortSessions(sessions)
	if sessions[0].ID != "b" {
		t.Errorf("running session should be first, got %q", sessions[0].ID)
	}
}

func TestSortSessions_AlphabeticalByName(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "b", Name: "beta", Status: "running", CreatedAt: time.Now().Format(time.RFC3339), LastAttachedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339)},
		{ID: "a", Name: "alpha", Status: "running", CreatedAt: time.Now().Format(time.RFC3339), LastAttachedAt: time.Now().Add(-1 * time.Hour).Format(time.RFC3339)},
	}
	SortSessions(sessions)
	if sessions[0].ID != "a" {
		t.Errorf("alphabetically first name should be first, got %q", sessions[0].Name)
	}
	if sessions[1].ID != "b" {
		t.Errorf("alphabetically second name should be second, got %q", sessions[1].Name)
	}
}

// --- ShortDuration ---

func TestShortDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{0, "0s"},
		{5 * time.Minute, "5m"},
		{59 * time.Minute, "59m"},
		{1 * time.Hour, "1h"},
		{2*time.Hour + 30*time.Minute, "2h30m"},
		{3 * time.Hour, "3h"},
		{25 * time.Hour, "1d"},
		{48 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		t.Run(tt.d.String(), func(t *testing.T) {
			got := ShortDuration(tt.d)
			if got != tt.want {
				t.Errorf("ShortDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// --- displayBranch ---

func TestDisplayBranch_MatchesName(t *testing.T) {
	got := displayBranch("d0ugal/graith/fix-overlay", "fix-overlay")
	if got != "—" {
		t.Errorf("branch matching name should return dash, got %q", got)
	}
}

func TestDisplayBranch_Different(t *testing.T) {
	got := displayBranch("main", "feature-x")
	if got != "main" {
		t.Errorf("non-matching branch should return as-is, got %q", got)
	}
}

func TestDisplayBranch_StripPrefix(t *testing.T) {
	got := displayBranch("user/repo/my-branch", "other-name")
	if got != "my-branch" {
		t.Errorf("should strip user/repo/ prefix, got %q", got)
	}
}

// --- displayGit ---

func TestDisplayGit(t *testing.T) {
	tests := []struct {
		dirty    bool
		unpushed int
		want     string
	}{
		{false, 0, "clean"},
		{true, 0, "M"},
		{false, 3, "↑3"},
		{true, 2, "M ↑2"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := displayGit(tt.dirty, tt.unpushed)
			if got != tt.want {
				t.Errorf("displayGit(%v, %d) = %q, want %q", tt.dirty, tt.unpushed, got, tt.want)
			}
		})
	}
}

// --- displayLastActive ---

func TestDisplayLastActive_CurrentSession(t *testing.T) {
	s := protocol.SessionInfo{ID: "s1", CreatedAt: time.Now().Format(time.RFC3339)}
	got := displayLastActive(s, "s1")
	if got != "now" {
		t.Errorf("current session should show 'now', got %q", got)
	}
}

func TestDisplayLastActive_UsesLastAttached(t *testing.T) {
	s := protocol.SessionInfo{
		ID:             "s1",
		CreatedAt:      time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
		LastAttachedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
	}
	got := displayLastActive(s, "other")
	if got != "5m" {
		t.Errorf("should use LastAttachedAt, got %q", got)
	}
}

func TestDisplayLastActive_FallsBackToCreated(t *testing.T) {
	s := protocol.SessionInfo{
		ID:        "s1",
		CreatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
	}
	got := displayLastActive(s, "other")
	if got != "2h" {
		t.Errorf("should fall back to CreatedAt, got %q", got)
	}
}

// --- filterSessions ---

func TestFilterSessions_EmptyQuery(t *testing.T) {
	sessions := overlayTestSessions()
	filtered := filterSessions(sessions, "")
	if len(filtered) != len(sessions) {
		t.Errorf("empty query should return all sessions, got %d", len(filtered))
	}
}

func TestFilterSessions_SingleTerm(t *testing.T) {
	sessions := overlayTestSessions()
	filtered := filterSessions(sessions, "graith")
	if len(filtered) != 2 {
		t.Errorf("expected 2 graith sessions, got %d", len(filtered))
	}
}

func TestFilterSessions_MultiTerm(t *testing.T) {
	sessions := overlayTestSessions()
	filtered := filterSessions(sessions, "graith running")
	if len(filtered) != 1 {
		t.Errorf("expected 1 running graith session, got %d", len(filtered))
	}
	if filtered[0].Name != "fix-overlay" {
		t.Errorf("expected fix-overlay, got %q", filtered[0].Name)
	}
}

func TestFilterSessions_CaseInsensitive(t *testing.T) {
	sessions := overlayTestSessions()
	filtered := filterSessions(sessions, "GRAITH")
	if len(filtered) != 2 {
		t.Errorf("filter should be case-insensitive, got %d results", len(filtered))
	}
}

func TestFilterSessions_GitTokens(t *testing.T) {
	sessions := overlayTestSessionsWithGitStatus()
	filtered := filterSessions(sessions, "dirty")
	if len(filtered) != 1 {
		t.Errorf("expected 1 dirty session, got %d", len(filtered))
	}
}

func TestFilterSessions_NoMatch(t *testing.T) {
	sessions := overlayTestSessions()
	filtered := filterSessions(sessions, "nonexistent")
	if len(filtered) != 0 {
		t.Errorf("expected 0 results, got %d", len(filtered))
	}
}

// --- computeColumnWidths ---

func TestComputeColumnWidths(t *testing.T) {
	sessions := overlayTestSessionsWithGitStatus()
	cw := computeColumnWidths(sessions, "")

	if cw.name < lipgloss.Width("dirty-session") {
		t.Errorf("name width %d < width(%q)", cw.name, "dirty-session")
	}
	if cw.status < lipgloss.Width("thinking") {
		t.Errorf("status width %d < width(%q) (agent status should override running)", cw.status, "thinking")
	}
	// New git format: "M ↑3"
	expectedGit := displayGit(true, 3)
	if cw.git < lipgloss.Width(expectedGit) {
		t.Errorf("git width %d < width(%q)", cw.git, expectedGit)
	}
}

func TestComputeColumnWidths_MinimumWidths(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "x", Status: "running", Branch: "m", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	cw := computeColumnWidths(sessions, "")
	if cw.name < 7 {
		t.Errorf("name should have minimum width 7, got %d", cw.name)
	}
	if cw.status < 6 {
		t.Errorf("status should have minimum width 6, got %d", cw.status)
	}
	if cw.branch < 6 {
		t.Errorf("branch should have minimum width 6, got %d", cw.branch)
	}
}

func TestComputeColumnWidths_BranchStripping(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID:        "s1",
			Name:      "x",
			Branch:    "user/repo/short",
			Status:    "running",
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}
	cw := computeColumnWidths(sessions, "")
	if cw.branch < lipgloss.Width("short") {
		t.Errorf("branch width %d should be at least width(%q)", cw.branch, "short")
	}
}

// --- sessionItem / groupHeader ---

func TestSessionItemFilterValue(t *testing.T) {
	si := sessionItem{info: protocol.SessionInfo{Name: "foo", RepoName: "bar"}}
	got := si.FilterValue()
	if got != "foo bar" {
		t.Errorf("FilterValue() = %q, want %q", got, "foo bar")
	}
}

func TestGroupHeaderFilterValue(t *testing.T) {
	gh := groupHeader{name: "graith"}
	if gh.FilterValue() != "" {
		t.Errorf("groupHeader FilterValue() should be empty, got %q", gh.FilterValue())
	}
}

// --- newOverlayModel ---

func TestNewOverlayModel_CursorOnCurrentSession(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "s3", nil, nil) // s3 = feature-x in other-repo
	item := m.list.SelectedItem()
	si, ok := item.(sessionItem)
	if !ok {
		t.Fatal("selected item should be a sessionItem")
	}
	if si.info.ID != "s3" {
		t.Errorf("cursor should be on current session s3, got %q", si.info.ID)
	}
}

func TestNewOverlayModel_CursorSkipsGroupHeader(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", nil, nil)
	item := m.list.SelectedItem()
	if _, ok := item.(groupHeader); ok {
		t.Error("cursor should skip the initial group header")
	}
	_, ok := item.(sessionItem)
	if !ok {
		t.Fatal("selected item should be a sessionItem")
	}
}

func TestNewOverlayModel_InitialState(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	if m.state != stateList {
		t.Errorf("initial state = %d, want stateList(%d)", m.state, stateList)
	}
	if m.selected != nil {
		t.Error("selected should be nil initially")
	}
	if m.previewContent != "" {
		t.Error("preview content should be empty initially")
	}
}

func TestNewOverlayModel_StoresAllSessions(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", nil, nil)
	if len(m.allSessions) != len(sessions) {
		t.Errorf("allSessions should store all %d sessions, got %d", len(sessions), len(m.allSessions))
	}
}

// --- Init ---

func TestInit_WithFetchPreview(t *testing.T) {
	called := false
	fetch := func(id string) string {
		called = true
		return "content"
	}
	m := newOverlayModel(overlayTestSessions(), "", fetch, nil)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() should return a command when fetchPreview is set")
	}
	msg := cmd()
	pm, ok := msg.(previewMsg)
	if !ok {
		t.Fatalf("expected previewMsg, got %T", msg)
	}
	if !called {
		t.Error("fetchPreview should have been called")
	}
	if pm.content != "content" {
		t.Errorf("preview content = %q, want %q", pm.content, "content")
	}
}

func TestInit_WithoutFetchPreview(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	cmd := m.Init()
	if cmd != nil {
		t.Error("Init() should return nil when fetchPreview is nil")
	}
}

// --- Update: previewMsg ---

func TestUpdate_PreviewMsg_Applied(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil)
	selected := m.list.SelectedItem().(sessionItem)

	updated, _ := m.Update(previewMsg{sessionID: selected.info.ID, content: "hello"})
	om := asOverlay(updated)
	if om.previewContent != "hello" {
		t.Errorf("preview content = %q, want %q", om.previewContent, "hello")
	}
	if om.previewSessionID != selected.info.ID {
		t.Errorf("preview session ID = %q, want %q", om.previewSessionID, selected.info.ID)
	}
}

func TestUpdate_PreviewMsg_StaleGuard(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil)
	m.previewContent = "old"

	updated, _ := m.Update(previewMsg{sessionID: "nonexistent", content: "stale"})
	om := asOverlay(updated)
	if om.previewContent != "old" {
		t.Errorf("stale preview should not be applied, got %q", om.previewContent)
	}
}

func TestUpdate_PreviewMsg_EmptyContentSkipsSessionID(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil)
	selected := m.list.SelectedItem().(sessionItem)

	updated, _ := m.Update(previewMsg{sessionID: selected.info.ID, content: "   \n  "})
	om := asOverlay(updated)
	if om.previewSessionID != "" {
		t.Error("empty/whitespace preview should not set previewSessionID")
	}
}

// --- Update: WindowSizeMsg ---

func TestUpdate_WindowSizeMsg(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)

	updated, _ := sendWindowSize(m, 120, 40)
	om := asOverlay(updated)
	if om.width != 120 || om.height != 40 {
		t.Errorf("size = %dx%d, want 120x40", om.width, om.height)
	}
}

func TestUpdate_WindowSizeMsg_Small(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)

	updated, _ := sendWindowSize(m, 20, 5)
	om := asOverlay(updated)
	if om.width != 20 || om.height != 5 {
		t.Errorf("size = %dx%d, want 20x5", om.width, om.height)
	}
}

// --- Update: List state key handling ---

func TestUpdate_QuitQ(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	_, cmd := sendKey(m, "q")
	if cmd == nil {
		t.Fatal("q should produce a command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected QuitMsg, got %T", msg)
	}
}

func TestUpdate_QuitEsc(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	_, cmd := sendSpecialKey(m, tea.KeyEscape)
	if cmd == nil {
		t.Fatal("esc should produce a command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected QuitMsg, got %T", msg)
	}
}

func TestUpdate_EnterSelectsSession(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	selected := m.list.SelectedItem().(sessionItem)

	updated, cmd := sendSpecialKey(m, tea.KeyEnter)
	om := asOverlay(updated)
	if om.selected == nil {
		t.Fatal("enter should select the current session")
	}
	if om.selected.ID != selected.info.ID {
		t.Errorf("selected session ID = %q, want %q", om.selected.ID, selected.info.ID)
	}
	if cmd == nil {
		t.Fatal("enter should produce a quit command")
	}
}

func TestUpdate_EnterOnGroupHeader_NoSelection(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "only", RepoName: "repo", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	m := newOverlayModel(sessions, "", nil, nil)
	// Force cursor to group header
	m.list.Select(0)

	if _, ok := m.list.SelectedItem().(groupHeader); !ok {
		t.Fatal("setup failed: expected cursor on group header")
	}

	updated, _ := sendSpecialKey(m, tea.KeyEnter)
	om := asOverlay(updated)
	if om.selected != nil {
		t.Error("enter on group header should not select anything")
	}
}

func TestUpdate_XEntersDeleteConfirm(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)

	updated, _ := sendKey(m, "x")
	om := asOverlay(updated)
	if om.state != stateConfirmDelete {
		t.Errorf("state = %d, want stateConfirmDelete(%d)", om.state, stateConfirmDelete)
	}
}

func TestUpdate_SlashEntersFilter(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)

	updated, cmd := sendKey(m, "/")
	om := asOverlay(updated)
	if om.state != stateFilter {
		t.Errorf("state = %d, want stateFilter(%d)", om.state, stateFilter)
	}
	if cmd == nil {
		t.Error("entering filter mode should return a blink command")
	}
}

// --- Update: Navigation ---

func TestUpdate_JKNavigation(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	initial := m.list.SelectedItem().(sessionItem)

	// Move down
	updated, _ := sendKey(m, "j")
	om := asOverlay(updated)
	after := om.list.SelectedItem().(sessionItem)
	if after.info.ID == initial.info.ID {
		t.Error("j should move cursor down to a different session")
	}

	// Move back up
	updated, _ = sendKey(om, "k")
	om = asOverlay(updated)
	back := om.list.SelectedItem().(sessionItem)
	if back.info.ID != initial.info.ID {
		t.Error("k should move cursor back up")
	}
}

func TestUpdate_NavigationSkipsGroupHeaders(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", nil, nil)

	// Navigate down through all items to reach other-repo group
	updated, _ := sendKey(m, "j")
	om := asOverlay(updated)
	updated, _ = sendKey(om, "j")
	om = asOverlay(updated)
	item := om.list.SelectedItem()
	si, ok := item.(sessionItem)
	if !ok {
		t.Fatalf("after navigating past group header, expected sessionItem, got %T", item)
	}
	if si.info.RepoName != "other-repo" {
		t.Errorf("expected to land in other-repo group, got %q", si.info.RepoName)
	}
}

func TestUpdate_NavigationUpSkipsGroupHeaders(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", nil, nil)

	// Navigate to the last item
	updated, _ := sendKey(m, "j")
	updated, _ = sendKey(asOverlay(updated), "j")
	om := asOverlay(updated)
	if si, ok := om.list.SelectedItem().(sessionItem); ok {
		if si.info.RepoName != "other-repo" {
			t.Fatalf("expected to be in other-repo, got %q", si.info.RepoName)
		}
	}

	// Navigate up — should skip the "other-repo" header
	updated, _ = sendKey(om, "k")
	om = asOverlay(updated)
	item := om.list.SelectedItem()
	si, ok := item.(sessionItem)
	if !ok {
		t.Fatalf("navigating up past group header, expected sessionItem, got %T", item)
	}
	if si.info.RepoName != "graith" {
		t.Errorf("expected graith group, got %q", si.info.RepoName)
	}
}

func TestUpdate_DownArrowNavigation(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	initial := m.list.SelectedItem().(sessionItem)

	updated, _ := sendSpecialKey(m, tea.KeyDown)
	om := asOverlay(updated)
	after := om.list.SelectedItem().(sessionItem)
	if after.info.ID == initial.info.ID {
		t.Error("down arrow should move cursor")
	}
}

func TestUpdate_NavigationFetchesPreview(t *testing.T) {
	fetched := make(map[string]bool)
	fetch := func(id string) string {
		fetched[id] = true
		return "preview"
	}
	m := newOverlayModel(overlayTestSessions(), "", fetch, nil)

	_, cmd := sendKey(m, "j")
	if cmd == nil {
		t.Fatal("navigation should return a preview fetch command")
	}
	msg := cmd()
	pm, ok := msg.(previewMsg)
	if !ok {
		t.Fatalf("expected previewMsg from navigation, got %T", msg)
	}
	if !fetched[pm.sessionID] {
		t.Error("fetchPreview should have been called for the new selection")
	}
}

// --- Update: Tab navigation ---

func TestUpdate_TabJumpsToNextGroup(t *testing.T) {
	sessions := overlayTestSessions() // graith (2) + other-repo (1)
	m := newOverlayModel(sessions, "", nil, nil)

	// Should start in graith group
	initial := m.list.SelectedItem().(sessionItem)
	if initial.info.RepoName != "graith" {
		t.Fatalf("expected to start in graith, got %q", initial.info.RepoName)
	}

	// Tab should jump to other-repo group
	sized, _ := sendWindowSize(m, 120, 40)
	updated, _ := sendSpecialKey(asOverlay(sized), tea.KeyTab)
	om := asOverlay(updated)
	after := om.list.SelectedItem().(sessionItem)
	if after.info.RepoName != "other-repo" {
		t.Errorf("tab should jump to other-repo, got %q", after.info.RepoName)
	}
}

func TestUpdate_ShiftTabJumpsToPrevGroup(t *testing.T) {
	sessions := overlayTestSessions()
	// Start on the other-repo session (s3)
	m := newOverlayModel(sessions, "s3", nil, nil)

	initial := m.list.SelectedItem().(sessionItem)
	if initial.info.RepoName != "other-repo" {
		t.Fatalf("expected to start in other-repo, got %q", initial.info.RepoName)
	}

	updated, _ := sendShiftTab(m)
	om := asOverlay(updated)
	after := om.list.SelectedItem().(sessionItem)
	if after.info.RepoName != "graith" {
		t.Errorf("shift+tab should jump to graith, got %q", after.info.RepoName)
	}
}

// --- Update: Filter state ---

func TestUpdate_FilterEscReturnsToList(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated, _ := sendKey(m, "/")

	updated, _ = sendSpecialKey(asOverlay(updated), tea.KeyEscape)
	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("esc in filter should return to stateList, got %d", om.state)
	}
}

func TestUpdate_FilterEnterReturnsToList(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated, _ := sendKey(m, "/")

	updated, _ = sendSpecialKey(asOverlay(updated), tea.KeyEnter)
	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("enter in filter should return to stateList, got %d", om.state)
	}
}

func TestUpdate_FilterTypingUpdatesInput(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated, _ := sendKey(m, "/")

	updated, _ = sendKey(asOverlay(updated), "f")
	updated, _ = sendKey(asOverlay(updated), "i")
	updated, _ = sendKey(asOverlay(updated), "x")
	om := asOverlay(updated)
	if om.filterInput.Value() != "fix" {
		t.Errorf("filter input = %q, want %q", om.filterInput.Value(), "fix")
	}
}

func TestUpdate_FilterActuallyFilters(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)

	// Enter filter mode and type "other"
	updated, _ := sendKey(m, "/")
	for _, ch := range "other" {
		updated, _ = sendKey(asOverlay(updated), string(ch))
	}
	om := asOverlay(updated)

	// Should only show other-repo sessions
	sessionCount := 0
	for _, item := range om.list.Items() {
		if _, ok := item.(sessionItem); ok {
			sessionCount++
		}
	}
	if sessionCount != 1 {
		t.Errorf("filtering for 'other' should show 1 session, got %d", sessionCount)
	}
}

func TestUpdate_FilterResetsCursorToFirstSession(t *testing.T) {
	sessions := overlayTestSessions()
	// Start cursor on s3 (last session, in other-repo group)
	m := newOverlayModel(sessions, "s3", nil, nil)
	initial := m.list.SelectedItem().(sessionItem)
	if initial.info.ID != "s3" {
		t.Fatalf("expected cursor on s3, got %q", initial.info.ID)
	}

	// Filter to only graith sessions — cursor was on index 4 (s3),
	// but filtered list only has 3 items. Without reset, SelectedItem() is nil.
	updated, _ := sendKey(m, "/")
	for _, ch := range "graith" {
		updated, _ = sendKey(asOverlay(updated), string(ch))
	}
	om := asOverlay(updated)

	item := om.list.SelectedItem()
	if item == nil {
		t.Fatal("SelectedItem() should not be nil after filtering")
	}
	si, ok := item.(sessionItem)
	if !ok {
		t.Fatal("cursor should be on a sessionItem, not a groupHeader")
	}
	if si.info.RepoName != "graith" {
		t.Errorf("cursor should be on a graith session, got repo %q", si.info.RepoName)
	}
}

func TestUpdate_FilterEscRestoresFullList(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", nil, nil)

	// Filter
	updated, _ := sendKey(m, "/")
	for _, ch := range "other" {
		updated, _ = sendKey(asOverlay(updated), string(ch))
	}

	// Esc to restore
	updated, _ = sendSpecialKey(asOverlay(updated), tea.KeyEscape)
	om := asOverlay(updated)

	sessionCount := 0
	for _, item := range om.list.Items() {
		if _, ok := item.(sessionItem); ok {
			sessionCount++
		}
	}
	if sessionCount != len(sessions) {
		t.Errorf("esc should restore all %d sessions, got %d", len(sessions), sessionCount)
	}
}

// --- Update: Confirm delete state ---

func TestUpdate_ConfirmDeleteY(t *testing.T) {
	deletedID := ""
	deleteFn := func(sid string) error { deletedID = sid; return nil }
	m := newOverlayModel(overlayTestSessions(), "", nil, deleteFn)
	selected := m.list.SelectedItem().(sessionItem)

	updated, _ := sendKey(m, "x")
	om := asOverlay(updated)

	_, cmd := sendKey(om, "y")
	if cmd == nil {
		t.Fatal("y should produce a delete command")
	}
	msg := cmd()
	drm, ok := msg.(deleteResultMsg)
	if !ok {
		t.Fatalf("expected deleteResultMsg, got %T", msg)
	}
	if drm.sessionID != selected.info.ID {
		t.Errorf("deleted session = %q, want %q", drm.sessionID, selected.info.ID)
	}
	if deletedID != selected.info.ID {
		t.Errorf("deleteSession called with %q, want %q", deletedID, selected.info.ID)
	}
}

func TestUpdate_ConfirmDeleteUpperY(t *testing.T) {
	deleteFn := func(sid string) error { return nil }
	m := newOverlayModel(overlayTestSessions(), "", nil, deleteFn)

	updated, _ := sendKey(m, "x")
	_, cmd := sendKey(asOverlay(updated), "Y")
	if cmd == nil {
		t.Fatal("Y should also produce a delete command")
	}
}

func TestUpdate_ConfirmDeleteCancel(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)

	updated, _ := sendKey(m, "x")
	updated, _ = sendKey(asOverlay(updated), "n")
	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("cancelling delete should return to stateList, got %d", om.state)
	}
	if om.selected != nil {
		t.Error("cancelling delete should not select a session")
	}
}

func TestUpdate_ConfirmDeleteAnyKeyCancel(t *testing.T) {
	for _, k := range []string{"a", "q", "z"} {
		t.Run(k, func(t *testing.T) {
			m := newOverlayModel(overlayTestSessions(), "", nil, nil)
			updated, _ := sendKey(m, "x")
			updated, _ = sendKey(asOverlay(updated), k)
			om := asOverlay(updated)
			if om.state != stateList {
				t.Errorf("key %q in delete confirm should cancel, got state %d", k, om.state)
			}
		})
	}
}

// --- View ---

func TestView_ZeroSize(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	if v := m.View().Content; v != "" {
		t.Errorf("View() with zero size should be empty, got %d chars", len(v))
	}
}

func TestView_RendersSessionList(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	om := asOverlay(updated)
	view := om.View().Content

	if !strings.Contains(view, "Sessions") {
		t.Error("view should contain the title 'Sessions'")
	}
	for _, name := range []string{"add-tests", "fix-overlay", "feature-x"} {
		if !strings.Contains(view, name) {
			t.Errorf("view should contain session name %q", name)
		}
	}
}

func TestView_ShowsGroupHeaders(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	om := asOverlay(updated)
	view := om.View().Content

	if !strings.Contains(view, "graith") {
		t.Error("view should contain group header 'graith'")
	}
	if !strings.Contains(view, "other-repo") {
		t.Error("view should contain group header 'other-repo'")
	}
}

func TestView_ShowsColumnHeaders(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated, _ := sendWindowSize(m, 150, 40)
	view := asOverlay(updated).View().Content

	for _, header := range []string{"Session", "Status", "Branch", "Git", "Last"} {
		if !strings.Contains(view, header) {
			t.Errorf("view should contain column header %q", header)
		}
	}
}

func TestView_ShowsHelpBar(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "enter attach") {
		t.Error("view should contain help bar")
	}
	if !strings.Contains(view, "tab group") {
		t.Error("help bar should mention tab group navigation")
	}
}

func TestView_ShowsDetailLine(t *testing.T) {
	sessions := overlayTestSessions()
	sessions[0].BaseBranch = "main"
	sessions[0].WorktreePath = "/tmp/test-worktree"
	m := newOverlayModel(sessions, "s1", nil, nil)
	updated, _ := sendWindowSize(m, 150, 40)
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "agent: claude") {
		t.Error("detail line should show agent type")
	}
	if !strings.Contains(view, "base: main") {
		t.Error("detail line should show base branch")
	}
}

func TestView_ShowsCurrentSessionMarker(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "s1", nil, nil)
	updated, _ := sendWindowSize(m, 150, 40)
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "★") {
		t.Error("view should contain ★ marker for current session")
	}
}

func TestView_ConfirmDeleteShowsPrompt(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(asOverlay(updated), "x")
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "Delete") || !strings.Contains(view, "[y/N]") {
		t.Error("delete confirmation should show 'Delete ... [y/N]'")
	}
}

func TestView_FilterModeShowsInput(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(asOverlay(updated), "/")
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "Filter") {
		t.Error("filter mode should show 'Filter'")
	}

	// Verify list mode does NOT show filter prompt
	m2 := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated2, _ := sendWindowSize(m2, 120, 40)
	listView := asOverlay(updated2).View().Content
	if strings.Contains(listView, "Filter:") {
		t.Error("list mode should not show filter prompt")
	}
}

func TestView_SmallTerminal(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated, _ := sendWindowSize(m, 30, 8)
	view := asOverlay(updated).View().Content

	lines := strings.Split(view, "\n")
	if len(lines) != 8 {
		t.Errorf("view should have exactly %d lines for height=%d, got %d", 8, 8, len(lines))
	}
}

func TestView_PreviewBackground(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	om := asOverlay(updated)

	selected := om.list.SelectedItem().(sessionItem)
	updated, _ = om.Update(previewMsg{sessionID: selected.info.ID, content: "UNIQUE_PREVIEW_LINE_1\nUNIQUE_PREVIEW_LINE_2"})
	om = asOverlay(updated)

	view := om.View().Content
	if !strings.Contains(view, "UNIQUE_PREVIEW_LINE_1") {
		t.Error("view should render preview content in the background")
	}

	m2 := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated2, _ := sendWindowSize(m2, 120, 40)
	viewNoPreview := asOverlay(updated2).View().Content
	if strings.Contains(viewNoPreview, "UNIQUE_PREVIEW_LINE_1") {
		t.Error("view without preview should not contain preview text")
	}
}

// --- Edge cases ---

func TestSingleSession(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "only-one", RepoName: "repo", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	m := newOverlayModel(sessions, "", nil, nil)

	si, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		t.Fatal("cursor should be on the session item")
	}
	if si.info.Name != "only-one" {
		t.Errorf("selected = %q, want %q", si.info.Name, "only-one")
	}

	updated, cmd := sendSpecialKey(m, tea.KeyEnter)
	om := asOverlay(updated)
	if om.selected == nil || om.selected.ID != "s1" {
		t.Error("enter should select the single session")
	}
	if cmd == nil {
		t.Fatal("should quit after selection")
	}
}

func TestEmptySessionList(t *testing.T) {
	m := newOverlayModel(nil, "", nil, nil)

	if len(m.list.Items()) != 0 {
		t.Errorf("expected 0 items, got %d", len(m.list.Items()))
	}

	_, cmd := sendKey(m, "q")
	if cmd == nil {
		t.Fatal("q should still quit with no sessions")
	}

	updated, _ := sendWindowSize(m, 80, 24)
	view := asOverlay(updated).View().Content
	if view == "" {
		t.Error("view should render something even with no sessions")
	}
}

func TestFetchPreviewCmd_NilFetchPreview(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	cmd := m.fetchPreviewCmd()
	if cmd != nil {
		t.Error("fetchPreviewCmd should return nil when fetchPreview is nil")
	}
}

func TestFetchPreviewCmd_GroupHeaderSelected(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", noopFetchPreview, nil)
	// Force cursor onto a group header
	m.list.Select(0)

	cmd := m.fetchPreviewCmd()
	if cmd != nil {
		t.Error("fetchPreviewCmd should return nil when a group header is selected")
	}
}

// --- OverlayResult construction ---

func overlayResultFromModel(om overlayModel) *OverlayResult {
	if om.selected != nil {
		action := "attach"
		if om.state == stateConfirmDelete {
			action = "delete"
		}
		return &OverlayResult{
			Action:    action,
			SessionID: om.selected.ID,
		}
	}
	return nil
}

func TestOverlayResult_Attach(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	selected := m.list.SelectedItem().(sessionItem)

	updated, _ := sendSpecialKey(m, tea.KeyEnter)
	result := overlayResultFromModel(asOverlay(updated))

	if result == nil {
		t.Fatal("result should not be nil after enter")
	}
	if result.Action != "attach" {
		t.Errorf("action = %q, want %q", result.Action, "attach")
	}
	if result.SessionID != selected.info.ID {
		t.Errorf("session ID = %q, want %q", result.SessionID, selected.info.ID)
	}
}

func TestOverlayResult_Delete_StaysOpen(t *testing.T) {
	deletedID := ""
	deleteFn := func(sid string) error {
		deletedID = sid
		return nil
	}
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", nil, deleteFn)
	selected := m.list.SelectedItem().(sessionItem)

	updated, _ := sendKey(m, "x")
	om := asOverlay(updated)
	if om.state != stateConfirmDelete {
		t.Fatalf("state = %d, want stateConfirmDelete", om.state)
	}

	updated, cmd := sendKey(om, "y")
	om = asOverlay(updated)
	if cmd == nil {
		t.Fatal("confirming delete should return a command")
	}

	msg := cmd()
	drm, ok := msg.(deleteResultMsg)
	if !ok {
		t.Fatalf("expected deleteResultMsg, got %T", msg)
	}
	if drm.sessionID != selected.info.ID {
		t.Errorf("deleted session = %q, want %q", drm.sessionID, selected.info.ID)
	}
	if deletedID != selected.info.ID {
		t.Errorf("deleteSession called with %q, want %q", deletedID, selected.info.ID)
	}

	updated, _ = om.Update(drm)
	om = asOverlay(updated)
	if om.state != stateList {
		t.Errorf("state after delete = %d, want stateList", om.state)
	}
	if len(om.allSessions) != len(sessions)-1 {
		t.Errorf("sessions after delete = %d, want %d", len(om.allSessions), len(sessions)-1)
	}
	for _, s := range om.allSessions {
		if s.ID == selected.info.ID {
			t.Error("deleted session should not remain in allSessions")
		}
	}
}

func TestOverlayResult_Quit(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil)
	updated, _ := sendKey(m, "q")
	result := overlayResultFromModel(asOverlay(updated))

	if result != nil {
		t.Error("quitting without selection should return nil result")
	}
}

// --- compactDelegate ---

func TestCompactDelegate_Dimensions(t *testing.T) {
	d := compactDelegate{}
	if d.Height() != 1 {
		t.Errorf("Height() = %d, want 1", d.Height())
	}
	if d.Spacing() != 0 {
		t.Errorf("Spacing() = %d, want 0", d.Spacing())
	}
}

func TestCompactDelegate_Update(t *testing.T) {
	d := compactDelegate{}
	cmd := d.Update(nil, nil)
	if cmd != nil {
		t.Error("Update should always return nil")
	}
}

func TestCompactDelegate_RenderSessionItem(t *testing.T) {
	sessions := overlayTestSessions()
	cols := computeColumnWidths(sessions, "")
	d := compactDelegate{cols: cols}

	items := buildGroupedItems(sessions)
	l := list.New(items, d, 120, 10)
	l.Select(1)

	var buf strings.Builder
	d.Render(&buf, l, 1, items[1])
	line := buf.String()
	si := items[1].(sessionItem)
	if !strings.Contains(line, si.info.Name) {
		t.Errorf("render should contain session name %q, got %q", si.info.Name, line)
	}
}

func TestCompactDelegate_RenderGroupHeader(t *testing.T) {
	d := compactDelegate{}
	items := buildGroupedItems(overlayTestSessions())
	l := list.New(items, d, 120, 10)

	var buf strings.Builder
	d.Render(&buf, l, 0, items[0])
	line := buf.String()
	if !strings.Contains(line, "graith") {
		t.Errorf("group header render should contain %q, got %q", "graith", line)
	}
	if !strings.Contains(line, "▸") {
		t.Error("group header should have ▸ prefix")
	}
	if !strings.Contains(line, "(2)") {
		t.Error("group header should show session count")
	}
}

func TestCompactDelegate_RenderStatusIndicators(t *testing.T) {
	tests := []struct {
		status    string
		indicator string
	}{
		{"running", "●"},
		{"stopped", "○"},
		{"errored", "✗"},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			sessions := []protocol.SessionInfo{
				{ID: "s1", Name: "test", RepoName: "repo", Status: tt.status, Branch: "main", CreatedAt: time.Now().Format(time.RFC3339)},
			}
			cols := computeColumnWidths(sessions, "")
			d := compactDelegate{cols: cols}
			items := buildGroupedItems(sessions)
			l := list.New(items, d, 120, 10)

			var buf strings.Builder
			d.Render(&buf, l, 1, items[1])
			if !strings.Contains(buf.String(), tt.indicator) {
				t.Errorf("status %q should render indicator %q", tt.status, tt.indicator)
			}
		})
	}
}

func TestCompactDelegate_RenderAgentStatusOverride(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID: "s1", Name: "test", RepoName: "repo",
			Status: "running", AgentStatus: "thinking",
			Branch: "main", CreatedAt: time.Now().Format(time.RFC3339),
		},
	}
	cols := computeColumnWidths(sessions, "")
	d := compactDelegate{cols: cols}
	items := buildGroupedItems(sessions)
	l := list.New(items, d, 120, 10)

	var buf strings.Builder
	d.Render(&buf, l, 1, items[1])
	if !strings.Contains(buf.String(), "thinking") {
		t.Error("should show agent status 'thinking' instead of 'running'")
	}
}

func TestCompactDelegate_RenderGitStatus(t *testing.T) {
	sessions := overlayTestSessionsWithGitStatus()
	cols := computeColumnWidths(sessions, "")
	d := compactDelegate{cols: cols}
	items := buildGroupedItems(sessions)
	l := list.New(items, d, 120, 10)

	var buf strings.Builder
	d.Render(&buf, l, 1, items[1])
	line := buf.String()
	// New format: "M" for dirty, "↑3" for unpushed
	if !strings.Contains(line, "M") {
		t.Error("should show 'M' for dirty sessions")
	}
	if !strings.Contains(line, "↑3") {
		t.Error("should show '↑3' for unpushed commits")
	}
}

func TestCompactDelegate_RenderCurrentSession(t *testing.T) {
	sessions := overlayTestSessions()
	cols := computeColumnWidths(sessions, "s1")
	d := compactDelegate{cols: cols, currentSessionID: "s1"}
	items := buildGroupedItems(sessions)
	l := list.New(items, d, 120, 10)

	// Find s1's index
	for i, item := range items {
		if si, ok := item.(sessionItem); ok && si.info.ID == "s1" {
			var buf strings.Builder
			d.Render(&buf, l, i, item)
			if !strings.Contains(buf.String(), "★") {
				t.Error("current session should have ★ marker")
			}
			return
		}
	}
	t.Fatal("s1 not found in items")
}

func TestCompactDelegate_RenderBranchDash(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID: "s1", Name: "fix-overlay", RepoName: "repo",
			Status: "running", Branch: "d0ugal/graith/fix-overlay",
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}
	cols := computeColumnWidths(sessions, "")
	d := compactDelegate{cols: cols}
	items := buildGroupedItems(sessions)
	l := list.New(items, d, 120, 10)

	var buf strings.Builder
	d.Render(&buf, l, 1, items[1])
	if !strings.Contains(buf.String(), "—") {
		t.Error("branch matching name should show —")
	}
}

func TestCompactDelegate_RenderSelectedVsUnselected(t *testing.T) {
	sessions := overlayTestSessions()
	cols := computeColumnWidths(sessions, "")
	d := compactDelegate{cols: cols}
	items := buildGroupedItems(sessions)
	l := list.New(items, d, 120, 10)
	l.Select(1)

	var selectedBuf, unselectedBuf strings.Builder
	d.Render(&selectedBuf, l, 1, items[1])
	d.Render(&unselectedBuf, l, 2, items[2])

	if !strings.Contains(selectedBuf.String(), ">") {
		t.Error("selected item should contain '>'")
	}
	if strings.Contains(unselectedBuf.String(), ">") {
		t.Error("unselected item should not contain '>'")
	}
}

func TestCompactDelegate_RenderTruncatesLongLine(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID: "s1", Name: "very-long-session-name-that-exceeds-width", RepoName: "repo",
			Status: "running", Branch: "feature/very-long-branch-name-here",
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}
	cols := computeColumnWidths(sessions, "")
	d := compactDelegate{cols: cols}
	items := buildGroupedItems(sessions)
	narrowWidth := 40
	l := list.New(items, d, narrowWidth, 10)

	var narrowBuf strings.Builder
	d.Render(&narrowBuf, l, 1, items[1])

	wideList := list.New(items, d, 200, 10)
	var wideBuf strings.Builder
	d.Render(&wideBuf, wideList, 1, items[1])

	narrowVis := lipgloss.Width(narrowBuf.String())
	wideVis := lipgloss.Width(wideBuf.String())
	if narrowVis >= wideVis {
		t.Errorf("narrow render (%d visible chars) should be shorter than wide render (%d visible chars)", narrowVis, wideVis)
	}
	if narrowVis > narrowWidth {
		t.Errorf("truncated line visual width %d exceeds list width %d", narrowVis, narrowWidth)
	}
}

// --- pad ---

func TestPad(t *testing.T) {
	tests := []struct {
		s     string
		width int
		want  string
	}{
		{"abc", 5, "abc  "},
		{"abc", 3, "abc"},
		{"abc", 2, "abc"},
		{"", 3, "   "},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q/%d", tt.s, tt.width), func(t *testing.T) {
			got := pad(tt.s, tt.width)
			if got != tt.want {
				t.Errorf("pad(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
			}
		})
	}
}

// --- columnWidths.totalWidth ---

func TestColumnWidths_TotalWidth(t *testing.T) {
	cw := columnWidths{name: 10, status: 8, branch: 15, git: 5, last: 4}
	got := cw.totalWidth()
	// 6 + 10 + 2 + 8 + 2 + 15 + 2 + 5 + 2 + 4 + 4 = 60
	if got != 60 {
		t.Errorf("totalWidth() = %d, want 60", got)
	}
}
