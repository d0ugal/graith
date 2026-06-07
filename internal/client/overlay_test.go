package client

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/d0ugal/graith/internal/protocol"
)

func testSessions() []protocol.SessionInfo {
	return []protocol.SessionInfo{
		{
			ID:        "s1",
			Name:      "fix-overlay",
			RepoName:  "graith",
			Branch:    "d0ugal/graith/fix-overlay",
			Agent:     "claude",
			Status:    "running",
			CreatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
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
			ID:        "s3",
			Name:      "feature-x",
			RepoName:  "other-repo",
			Branch:    "main",
			Agent:     "codex",
			Status:    "running",
			CreatedAt: time.Now().Add(-30 * time.Minute).Format(time.RFC3339),
		},
	}
}

func testSessionsWithGitStatus() []protocol.SessionInfo {
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
	return m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}

func sendSpecialKey(m tea.Model, k tea.KeyType) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyMsg{Type: k})
}

func sendWindowSize(m tea.Model, w, h int) (tea.Model, tea.Cmd) {
	return m.Update(tea.WindowSizeMsg{Width: w, Height: h})
}

func asOverlay(m tea.Model) overlayModel {
	return m.(overlayModel)
}

// --- buildGroupedItems ---

func TestBuildGroupedItems_GroupsByRepo(t *testing.T) {
	sessions := testSessions()
	items := buildGroupedItems(sessions)

	// Expect: groupHeader("graith"), 2 sessions, groupHeader("other-repo"), 1 session
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

	gh2, ok := items[3].(groupHeader)
	if !ok {
		t.Fatal("items[3] should be a groupHeader")
	}
	if gh2.name != "other-repo" {
		t.Errorf("second group = %q, want %q", gh2.name, "other-repo")
	}

	// Sessions within a group are sorted by name
	s1 := items[1].(sessionItem)
	s2 := items[2].(sessionItem)
	if s1.info.Name >= s2.info.Name {
		t.Errorf("sessions should be sorted: got %q before %q", s1.info.Name, s2.info.Name)
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

// --- shortDur ---

func TestShortDur(t *testing.T) {
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
			got := shortDur(tt.d)
			if got != tt.want {
				t.Errorf("shortDur(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// --- computeColumnWidths ---

func TestComputeColumnWidths(t *testing.T) {
	sessions := testSessionsWithGitStatus()
	cw := computeColumnWidths(sessions)

	if cw.name < len("dirty-session") {
		t.Errorf("name width %d < len(%q)", cw.name, "dirty-session")
	}
	if cw.status < len("thinking") {
		t.Errorf("status width %d < len(%q) (agent status should override running)", cw.status, "thinking")
	}
	if cw.git < len("dirty 3↑") {
		t.Errorf("git width %d < len(%q)", cw.git, "dirty 3↑")
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
	cw := computeColumnWidths(sessions)
	if cw.branch < len("short") {
		t.Errorf("branch width %d should be at least len(%q)", cw.branch, "short")
	}
	// The stripped branch is shorter than the full one
	if cw.branch >= len("user/repo/short") {
		t.Errorf("branch width %d should be < len(full branch)", cw.branch)
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

func TestNewOverlayModel_CursorSkipsGroupHeader(t *testing.T) {
	sessions := testSessions()
	m := newOverlayModel(sessions, nil)
	item := m.list.SelectedItem()
	if _, ok := item.(groupHeader); ok {
		t.Error("cursor should skip the initial group header")
	}
	si, ok := item.(sessionItem)
	if !ok {
		t.Fatal("selected item should be a sessionItem")
	}
	// First session in sorted "graith" group
	if si.info.Name != "add-tests" {
		t.Errorf("first selected session = %q, want %q", si.info.Name, "add-tests")
	}
}

func TestNewOverlayModel_InitialState(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
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

// --- Init ---

func TestInit_WithFetchPreview(t *testing.T) {
	called := false
	fetch := func(id string) string {
		called = true
		return "content"
	}
	m := newOverlayModel(testSessions(), fetch)
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
	m := newOverlayModel(testSessions(), nil)
	cmd := m.Init()
	if cmd != nil {
		t.Error("Init() should return nil when fetchPreview is nil")
	}
}

// --- Update: previewMsg ---

func TestUpdate_PreviewMsg_Applied(t *testing.T) {
	m := newOverlayModel(testSessions(), noopFetchPreview)
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
	m := newOverlayModel(testSessions(), noopFetchPreview)
	m.previewContent = "old"

	updated, _ := m.Update(previewMsg{sessionID: "nonexistent", content: "stale"})
	om := asOverlay(updated)
	if om.previewContent != "old" {
		t.Errorf("stale preview should not be applied, got %q", om.previewContent)
	}
}

func TestUpdate_PreviewMsg_EmptyContentSkipsSessionID(t *testing.T) {
	m := newOverlayModel(testSessions(), noopFetchPreview)
	selected := m.list.SelectedItem().(sessionItem)

	updated, _ := m.Update(previewMsg{sessionID: selected.info.ID, content: "   \n  "})
	om := asOverlay(updated)
	if om.previewSessionID != "" {
		t.Error("empty/whitespace preview should not set previewSessionID")
	}
}

// --- Update: WindowSizeMsg ---

func TestUpdate_WindowSizeMsg(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)

	updated, _ := sendWindowSize(m, 120, 40)
	om := asOverlay(updated)
	if om.width != 120 || om.height != 40 {
		t.Errorf("size = %dx%d, want 120x40", om.width, om.height)
	}
}

func TestUpdate_WindowSizeMsg_Small(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)

	updated, _ := sendWindowSize(m, 20, 5)
	om := asOverlay(updated)
	if om.width != 20 || om.height != 5 {
		t.Errorf("size = %dx%d, want 20x5", om.width, om.height)
	}
}

// --- Update: List state key handling ---

func TestUpdate_QuitKeys(t *testing.T) {
	for _, k := range []string{"q", "esc"} {
		t.Run(k, func(t *testing.T) {
			m := newOverlayModel(testSessions(), nil)
			_, cmd := sendKey(m, k)
			if cmd == nil {
				t.Fatal("quit key should produce a command")
			}
			msg := cmd()
			if _, ok := msg.(tea.QuitMsg); !ok {
				t.Errorf("expected QuitMsg, got %T", msg)
			}
		})
	}
}

func TestUpdate_EnterSelectsSession(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
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
	m := newOverlayModel(sessions, nil)
	// Force cursor to group header by moving up
	m.list.CursorUp()
	m.list.CursorUp()

	// If selected item is a group header, enter should not select anything
	if _, ok := m.list.SelectedItem().(groupHeader); ok {
		updated, _ := sendSpecialKey(m, tea.KeyEnter)
		om := asOverlay(updated)
		if om.selected != nil {
			t.Error("enter on group header should not select anything")
		}
	}
}

func TestUpdate_XEntersDeleteConfirm(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)

	updated, _ := sendKey(m, "x")
	om := asOverlay(updated)
	if om.state != stateConfirmDelete {
		t.Errorf("state = %d, want stateConfirmDelete(%d)", om.state, stateConfirmDelete)
	}
}

func TestUpdate_SlashEntersFilter(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)

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
	m := newOverlayModel(testSessions(), nil)
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
	sessions := testSessions() // graith group (2 items) + other-repo group (1 item)
	m := newOverlayModel(sessions, nil)

	// Cursor starts on first session in "graith" group (add-tests)
	// Move down to second session (fix-overlay)
	updated, _ := sendKey(m, "j")
	om := asOverlay(updated)

	// Move down again — should skip "other-repo" header and land on "feature-x"
	updated, _ = sendKey(om, "j")
	om = asOverlay(updated)
	item := om.list.SelectedItem()
	si, ok := item.(sessionItem)
	if !ok {
		t.Fatalf("after navigating past group header, expected sessionItem, got %T", item)
	}
	if si.info.Name != "feature-x" {
		t.Errorf("expected to land on %q, got %q", "feature-x", si.info.Name)
	}
}

func TestUpdate_NavigationUpSkipsGroupHeaders(t *testing.T) {
	sessions := testSessions()
	m := newOverlayModel(sessions, nil)

	// Navigate to the last item (feature-x in other-repo group)
	updated, _ := sendKey(m, "j")
	updated, _ = sendKey(asOverlay(updated), "j")
	om := asOverlay(updated)
	if si, ok := om.list.SelectedItem().(sessionItem); ok {
		if si.info.Name != "feature-x" {
			t.Fatalf("expected to be on feature-x, got %q", si.info.Name)
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
	if si.info.Name != "fix-overlay" {
		t.Errorf("expected %q, got %q", "fix-overlay", si.info.Name)
	}
}

func TestUpdate_DownArrowNavigation(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
	initial := m.list.SelectedItem().(sessionItem)

	updated, _ := sendKey(m, "down")
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
	m := newOverlayModel(testSessions(), fetch)

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

// --- Update: Filter state ---

func TestUpdate_FilterEscReturnsToList(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
	updated, _ := sendKey(m, "/")

	updated, _ = sendKey(asOverlay(updated), "esc")
	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("esc in filter should return to stateList, got %d", om.state)
	}
}

func TestUpdate_FilterEnterReturnsToList(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
	updated, _ := sendKey(m, "/")

	updated, _ = sendSpecialKey(asOverlay(updated), tea.KeyEnter)
	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("enter in filter should return to stateList, got %d", om.state)
	}
}

func TestUpdate_FilterTypingUpdatesInput(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
	updated, _ := sendKey(m, "/")

	updated, _ = sendKey(asOverlay(updated), "f")
	updated, _ = sendKey(asOverlay(updated), "i")
	updated, _ = sendKey(asOverlay(updated), "x")
	om := asOverlay(updated)
	if om.filterInput.Value() != "fix" {
		t.Errorf("filter input = %q, want %q", om.filterInput.Value(), "fix")
	}
}

// --- Update: Confirm delete state ---

func TestUpdate_ConfirmDeleteY(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
	selected := m.list.SelectedItem().(sessionItem)

	// Enter delete confirmation
	updated, _ := sendKey(m, "x")
	om := asOverlay(updated)

	// Confirm with y
	updated, cmd := sendKey(om, "y")
	om = asOverlay(updated)
	if om.selected == nil {
		t.Fatal("y should select the session for deletion")
	}
	if om.selected.ID != selected.info.ID {
		t.Errorf("selected ID = %q, want %q", om.selected.ID, selected.info.ID)
	}
	if cmd == nil {
		t.Fatal("y should produce a quit command")
	}
}

func TestUpdate_ConfirmDeleteUpperY(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)

	updated, _ := sendKey(m, "x")
	updated, cmd := sendKey(asOverlay(updated), "Y")
	om := asOverlay(updated)
	if om.selected == nil {
		t.Fatal("Y should also confirm deletion")
	}
	if cmd == nil {
		t.Fatal("Y should produce a quit command")
	}
}

func TestUpdate_ConfirmDeleteCancel(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)

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
			m := newOverlayModel(testSessions(), nil)
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
	m := newOverlayModel(testSessions(), nil)
	if v := m.View(); v != "" {
		t.Errorf("View() with zero size should be empty, got %d chars", len(v))
	}
}

func TestView_RendersSessionList(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
	updated, _ := sendWindowSize(m, 120, 40)
	om := asOverlay(updated)
	view := om.View()

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
	m := newOverlayModel(testSessions(), nil)
	updated, _ := sendWindowSize(m, 120, 40)
	om := asOverlay(updated)
	view := om.View()

	if !strings.Contains(view, "graith") {
		t.Error("view should contain group header 'graith'")
	}
	if !strings.Contains(view, "other-repo") {
		t.Error("view should contain group header 'other-repo'")
	}
}

func TestView_ShowsHelpBar(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
	updated, _ := sendWindowSize(m, 120, 40)
	view := asOverlay(updated).View()

	if !strings.Contains(view, "enter attach") {
		t.Error("view should contain help bar")
	}
}

func TestView_ConfirmDeleteShowsPrompt(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(asOverlay(updated), "x")
	view := asOverlay(updated).View()

	if !strings.Contains(view, "Delete") || !strings.Contains(view, "[y/N]") {
		t.Error("delete confirmation should show 'Delete ... [y/N]'")
	}
}

func TestView_FilterModeShowsInput(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(asOverlay(updated), "/")
	view := asOverlay(updated).View()

	if !strings.Contains(view, "Filter") {
		t.Error("filter mode should show 'Filter'")
	}
}

func TestView_SmallTerminal(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
	updated, _ := sendWindowSize(m, 30, 8)
	view := asOverlay(updated).View()

	lines := strings.Split(view, "\n")
	if len(lines) != 8 {
		t.Errorf("view should have exactly %d lines for height=%d, got %d", 8, 8, len(lines))
	}
}

func TestView_PreviewBackground(t *testing.T) {
	m := newOverlayModel(testSessions(), noopFetchPreview)
	updated, _ := sendWindowSize(m, 120, 40)
	om := asOverlay(updated)

	// Apply a preview
	selected := om.list.SelectedItem().(sessionItem)
	updated, _ = om.Update(previewMsg{sessionID: selected.info.ID, content: "line1\nline2\nline3"})
	om = asOverlay(updated)

	view := om.View()
	if view == "" {
		t.Fatal("view should not be empty with preview content")
	}
}

// --- Edge cases ---

func TestSingleSession(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "only-one", RepoName: "repo", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	m := newOverlayModel(sessions, nil)

	// Should start on the session, not the header
	si, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		t.Fatal("cursor should be on the session item")
	}
	if si.info.Name != "only-one" {
		t.Errorf("selected = %q, want %q", si.info.Name, "only-one")
	}

	// Enter should select it
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
	m := newOverlayModel(nil, nil)

	if len(m.list.Items()) != 0 {
		t.Errorf("expected 0 items, got %d", len(m.list.Items()))
	}

	// q should still quit
	_, cmd := sendKey(m, "q")
	if cmd == nil {
		t.Fatal("q should still quit with no sessions")
	}

	// View should not panic
	updated, _ := sendWindowSize(m, 80, 24)
	view := asOverlay(updated).View()
	if view == "" {
		t.Error("view should render something even with no sessions")
	}
}

func TestFetchPreviewCmd_NilFetchPreview(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
	cmd := m.fetchPreviewCmd()
	if cmd != nil {
		t.Error("fetchPreviewCmd should return nil when fetchPreview is nil")
	}
}

func TestFetchPreviewCmd_GroupHeaderSelected(t *testing.T) {
	sessions := testSessions()
	m := newOverlayModel(sessions, noopFetchPreview)
	// Force cursor onto a group header
	m.list.Select(0)

	cmd := m.fetchPreviewCmd()
	if cmd != nil {
		t.Error("fetchPreviewCmd should return nil when a group header is selected")
	}
}

// --- RunOverlay result construction ---

func TestRunOverlay_AttachAction(t *testing.T) {
	// Test the result construction logic by simulating what RunOverlay does
	m := newOverlayModel(testSessions(), nil)
	updated, _ := sendSpecialKey(m, tea.KeyEnter)
	om := asOverlay(updated)

	if om.selected == nil {
		t.Fatal("selected should not be nil after enter")
	}
	if om.state == stateConfirmDelete {
		t.Error("state should not be confirmDelete for attach")
	}
}

func TestRunOverlay_DeleteAction(t *testing.T) {
	m := newOverlayModel(testSessions(), nil)
	updated, _ := sendKey(m, "x")
	updated, _ = sendKey(asOverlay(updated), "y")
	om := asOverlay(updated)

	if om.selected == nil {
		t.Fatal("selected should not be nil after delete confirm")
	}
	if om.state != stateConfirmDelete {
		t.Errorf("state should be stateConfirmDelete, got %d", om.state)
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
	sessions := testSessions()
	cols := computeColumnWidths(sessions)
	d := compactDelegate{cols: cols}

	items := buildGroupedItems(sessions)
	l := list.New(items, d, 120, 10)
	l.Select(1) // select first session

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
	items := buildGroupedItems(testSessions())
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
			cols := computeColumnWidths(sessions)
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
	cols := computeColumnWidths(sessions)
	d := compactDelegate{cols: cols}
	items := buildGroupedItems(sessions)
	l := list.New(items, d, 120, 10)

	var buf strings.Builder
	d.Render(&buf, l, 1, items[1])
	if !strings.Contains(buf.String(), "thinking") {
		t.Error("should show agent status 'thinking' instead of 'running'")
	}
}

func TestCompactDelegate_RenderDirtyAndUnpushed(t *testing.T) {
	sessions := testSessionsWithGitStatus()
	cols := computeColumnWidths(sessions)
	d := compactDelegate{cols: cols}
	items := buildGroupedItems(sessions)
	l := list.New(items, d, 120, 10)

	var buf strings.Builder
	d.Render(&buf, l, 1, items[1])
	line := buf.String()
	if !strings.Contains(line, "dirty") {
		t.Error("should show 'dirty' for dirty sessions")
	}
	if !strings.Contains(line, "3↑") {
		t.Error("should show '3↑' for unpushed commits")
	}
}

func TestCompactDelegate_RenderSelectedVsUnselected(t *testing.T) {
	sessions := testSessions()
	cols := computeColumnWidths(sessions)
	d := compactDelegate{cols: cols}
	items := buildGroupedItems(sessions)
	l := list.New(items, d, 120, 10)
	l.Select(1) // select index 1

	var selectedBuf, unselectedBuf strings.Builder
	d.Render(&selectedBuf, l, 1, items[1])   // selected
	d.Render(&unselectedBuf, l, 2, items[2]) // not selected

	if !strings.HasPrefix(selectedBuf.String(), ">") {
		t.Error("selected item should start with '>'")
	}
	if strings.HasPrefix(unselectedBuf.String(), ">") {
		t.Error("unselected item should not start with '>'")
	}
}

func TestCompactDelegate_RenderLastAttached(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID: "s1", Name: "test", RepoName: "repo",
			Status: "running", Branch: "main",
			CreatedAt:      time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
			LastAttachedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
		},
	}
	cols := computeColumnWidths(sessions)
	d := compactDelegate{cols: cols}
	items := buildGroupedItems(sessions)
	l := list.New(items, d, 120, 10)

	var buf strings.Builder
	d.Render(&buf, l, 1, items[1])
	if !strings.Contains(buf.String(), "ago") {
		t.Error("should show last-attached time with 'ago'")
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
	cols := computeColumnWidths(sessions)
	d := compactDelegate{cols: cols}
	items := buildGroupedItems(sessions)
	l := list.New(items, d, 40, 10) // narrow width

	var buf strings.Builder
	d.Render(&buf, l, 1, items[1])
	// Should not panic, and line should be truncated
	if buf.Len() == 0 {
		t.Error("render should produce output even when truncating")
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
	cw := columnWidths{name: 10, status: 8, branch: 15, git: 5, age: 3}
	got := cw.totalWidth()
	// 4 + 10 + 2 + 8 + 2 + 15 + 2 + 5 + 2 + 3 + 4 = 57
	if got != 57 {
		t.Errorf("totalWidth() = %d, want 57", got)
	}
}
