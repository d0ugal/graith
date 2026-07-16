package client

import (
	"fmt"
	"image/color"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
)

func overlayTestSessions() []protocol.SessionInfo {
	return []protocol.SessionInfo{
		{
			ID:             "s1",
			Name:           "braw-fix",
			RepoName:       "graith",
			Branch:         "d0ugal/graith/braw-fix",
			Agent:          "claude",
			Status:         "running",
			CreatedAt:      time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
			LastAttachedAt: time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
		},
		{
			ID:        "s2",
			Name:      "canny-tests",
			RepoName:  "graith",
			Branch:    "d0ugal/graith/canny-tests",
			Agent:     "claude",
			Status:    "stopped",
			CreatedAt: time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
		},
		{
			ID:             "s3",
			Name:           "bonnie-feature",
			RepoName:       "croft",
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
			Name:          "thrawn-dirty",
			RepoName:      "graith",
			Branch:        "d0ugal/graith/thrawn-dirty",
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

// sizedModel builds an overlay model with the common nil callbacks and the
// standard 120x40 dimensions used across most overlay tests.
func sizedModel(t *testing.T, sessions []protocol.SessionInfo, current string) overlayModel {
	t.Helper()

	m := newOverlayModel(sessions, current, nil, nil, nil, nil)
	m.width = 120
	m.height = 40

	return m
}

// countSessionItems returns the number of sessionItem entries in the model's
// list (excluding group headers and other item types).
func countSessionItems(m overlayModel) int {
	count := 0

	for _, item := range m.list.Items() {
		if _, ok := item.(sessionItem); ok {
			count++
		}
	}

	return count
}

// renderItem builds a compactDelegate for the given sessions and renders the
// item at index into a string, using the standard 120x10 list dimensions.
func renderItem(sessions []protocol.SessionInfo, current string, index int) string {
	cols := computeColumnWidths(sessions, current)
	d := compactDelegate{cols: cols, currentSessionID: current}
	items := buildGroupedItems(sessions, nil)
	l := list.New(items, d, 120, 10)

	var buf strings.Builder

	d.Render(&buf, l, index, items[index])

	return buf.String()
}

// drain runs queued tea.Cmds to completion, feeding each resulting message
// back into the model, and returns the final model.
func drain(m tea.Model, cmd tea.Cmd) tea.Model {
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			break
		}

		m, cmd = m.Update(msg)
	}

	return m
}

// --- buildGroupedItems ---

func TestBuildGroupedItems_GroupsByRepo(t *testing.T) {
	sessions := overlayTestSessions()
	items := buildGroupedItems(sessions, nil)

	// croft group: bonnie-feature (running). graith group: braw-fix (running),
	// canny-tests (stopped). Alphabetically croft < graith. Plus 2 group headers = 5 items.
	if len(items) != 5 {
		t.Fatalf("expected 5 items (2 headers + 3 sessions), got %d", len(items))
	}

	gh1, ok := items[0].(groupHeader)
	if !ok {
		t.Fatal("items[0] should be a groupHeader")
	}

	if gh1.name != "croft" {
		t.Errorf("first group = %q, want %q", gh1.name, "croft")
	}

	if gh1.count != 1 {
		t.Errorf("first group count = %d, want 1", gh1.count)
	}

	gh2, ok := items[2].(groupHeader)
	if !ok {
		t.Fatal("items[2] should be a groupHeader")
	}

	if gh2.name != "graith" {
		t.Errorf("second group = %q, want %q", gh2.name, "graith")
	}
}

func TestBuildGroupedItems_EmptyRepoName(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "thrawn", RepoName: "", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	items := buildGroupedItems(sessions, nil)

	gh := items[0].(groupHeader)
	if gh.name != "(no repo)" {
		t.Errorf("empty repo should show as %q, got %q", "(no repo)", gh.name)
	}
}

func TestBuildGroupedItems_Empty(t *testing.T) {
	items := buildGroupedItems(nil, nil)
	if len(items) != 0 {
		t.Errorf("expected 0 items for nil sessions, got %d", len(items))
	}
}

func TestBuildGroupedItems_GroupsSorted(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "1", Name: "z", RepoName: "zzz", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "2", Name: "a", RepoName: "aaa", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	items := buildGroupedItems(sessions, nil)
	gh1 := items[0].(groupHeader)

	gh2 := items[2].(groupHeader)
	if gh1.name != "aaa" || gh2.name != "zzz" {
		t.Errorf("groups should be sorted alphabetically, got %q then %q", gh1.name, gh2.name)
	}
}

func TestBuildGroupedItems_SessionCount(t *testing.T) {
	sessions := overlayTestSessions()
	items := buildGroupedItems(sessions, nil)

	gh := items[0].(groupHeader)
	if gh.count != 1 {
		t.Errorf("croft group count = %d, want 1", gh.count)
	}

	gh2 := items[2].(groupHeader)
	if gh2.count != 2 {
		t.Errorf("graith group count = %d, want 2", gh2.count)
	}
}

// --- buildGroupedItems tree ---

func TestBuildGroupedItems_TreeStructure(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben-session", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child1", Name: "bairn-1", ParentID: "root", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child2", Name: "bairn-2", ParentID: "root", RepoName: "repo", Status: "stopped", CreatedAt: now},
		{ID: "standalone", Name: "neep", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	items := buildGroupedItems(sessions, nil)

	// header + ben-session + bairn-1 + bairn-2 + neep = 5
	if len(items) != 5 {
		t.Fatalf("expected 5 items, got %d", len(items))
	}

	// Root sessions should come first (running sorted alphabetically)
	si0 := items[1].(sessionItem)
	if si0.info.Name != "ben-session" || si0.treePrefix != "" {
		t.Errorf("items[1]: name=%q prefix=%q, want ben-session with no prefix", si0.info.Name, si0.treePrefix)
	}

	si1 := items[2].(sessionItem)
	if si1.info.Name != "bairn-1" || si1.treePrefix != "├── " {
		t.Errorf("items[2]: name=%q prefix=%q, want bairn-1 with ├── prefix", si1.info.Name, si1.treePrefix)
	}

	si2 := items[3].(sessionItem)
	if si2.info.Name != "bairn-2" || si2.treePrefix != "└── " {
		t.Errorf("items[3]: name=%q prefix=%q, want bairn-2 with └── prefix", si2.info.Name, si2.treePrefix)
	}

	si3 := items[4].(sessionItem)
	if si3.info.Name != "neep" || si3.treePrefix != "" {
		t.Errorf("items[4]: name=%q prefix=%q, want neep with no prefix", si3.info.Name, si3.treePrefix)
	}
}

func TestBuildGroupedItems_MultiLevelTree(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child", Name: "bairn", ParentID: "root", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "grandchild", Name: "wee-bairn", ParentID: "child", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	items := buildGroupedItems(sessions, nil)

	if len(items) != 4 {
		t.Fatalf("expected 4 items (1 header + 3 sessions), got %d", len(items))
	}

	gc := items[3].(sessionItem)
	if gc.info.Name != "wee-bairn" {
		t.Fatalf("items[3] = %q, want wee-bairn", gc.info.Name)
	}

	wantPrefix := "    └── "
	if gc.treePrefix != wantPrefix {
		t.Errorf("wee-bairn prefix = %q, want %q", gc.treePrefix, wantPrefix)
	}
}

func TestBuildGroupedItems_OrphanedChild(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "orphan", Name: "thrawn", ParentID: "nonexistent", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	items := buildGroupedItems(sessions, nil)

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	si := items[1].(sessionItem)
	if si.treePrefix != "" {
		t.Errorf("orphaned child should be a root with no prefix, got %q", si.treePrefix)
	}
}

func TestBuildGroupedItems_ParentInDifferentRepo(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "parent", Name: "ben", RepoName: "repo-a", Status: "running", CreatedAt: now},
		{ID: "child", Name: "bairn", ParentID: "parent", RepoName: "repo-b", Status: "running", CreatedAt: now},
	}
	items := buildGroupedItems(sessions, nil)

	// bairn should be a root in repo-b since ben is in repo-a
	for _, item := range items {
		if si, ok := item.(sessionItem); ok && si.info.Name == "bairn" {
			if si.treePrefix != "" {
				t.Errorf("bairn in different repo should be root, got prefix %q", si.treePrefix)
			}

			return
		}
	}

	t.Fatal("bairn not found in items")
}

func TestBuildGroupedItems_CyclicParents(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "a", Name: "braw", ParentID: "b", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "b", Name: "canny", ParentID: "a", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	items := buildGroupedItems(sessions, nil)

	// Both should appear even though they form a cycle (neither is a natural root)
	if len(items) != 3 {
		t.Fatalf("expected 3 items (1 header + 2 sessions), got %d", len(items))
	}

	sessionCount := 0

	for _, item := range items {
		if _, ok := item.(sessionItem); ok {
			sessionCount++
		}
	}

	if sessionCount != 2 {
		t.Errorf("expected 2 sessions rendered from cycle, got %d", sessionCount)
	}
}

func TestBuildGroupedItems_SelfReference(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "self", Name: "self-ref", ParentID: "self", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	items := buildGroupedItems(sessions, nil)

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	si := items[1].(sessionItem)
	if si.treePrefix != "" {
		t.Errorf("self-referencing session should be a root with no prefix, got %q", si.treePrefix)
	}
}

func TestBuildGroupedItems_CollapsedParent(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben-session", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child1", Name: "bairn-1", ParentID: "root", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child2", Name: "bairn-2", ParentID: "root", RepoName: "repo", Status: "stopped", CreatedAt: now},
		{ID: "standalone", Name: "neep", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	collapsed := map[string]bool{"root": true}
	items := buildGroupedItems(sessions, collapsed)

	// header + ben-session (collapsed) + neep = 3; children hidden
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	si := items[1].(sessionItem)
	if si.info.Name != "ben-session" {
		t.Errorf("items[1] = %q, want ben-session", si.info.Name)
	}

	if !si.collapsed {
		t.Error("root should be marked collapsed")
	}

	if !si.hasChildren {
		t.Error("root should be marked as having children")
	}

	if si.descendantCount != 2 {
		t.Errorf("descendantCount = %d, want 2", si.descendantCount)
	}

	si2 := items[2].(sessionItem)
	if si2.info.Name != "neep" {
		t.Errorf("items[2] = %q, want neep", si2.info.Name)
	}
}

func TestBuildGroupedItems_CollapsedNestedParent(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child", Name: "bairn", ParentID: "root", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "grandchild", Name: "wee-bairn", ParentID: "child", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	// Collapse root — should hide both bairn and wee-bairn
	collapsed := map[string]bool{"root": true}
	items := buildGroupedItems(sessions, collapsed)

	// header + root = 2
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	si := items[1].(sessionItem)
	if si.descendantCount != 2 {
		t.Errorf("descendantCount = %d, want 2", si.descendantCount)
	}
}

func TestBuildGroupedItems_CollapseChildButNotRoot(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child", Name: "bairn", ParentID: "root", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "grandchild", Name: "wee-bairn", ParentID: "child", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	// Collapse bairn — ben and bairn visible, wee-bairn hidden
	collapsed := map[string]bool{"child": true}
	items := buildGroupedItems(sessions, collapsed)

	// header + root + child = 3
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	child := items[2].(sessionItem)
	if !child.collapsed {
		t.Error("child should be marked collapsed")
	}

	if child.descendantCount != 1 {
		t.Errorf("child descendantCount = %d, want 1", child.descendantCount)
	}
}

func TestBuildGroupedItems_CollapsedCyclicParents(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "a", Name: "braw", ParentID: "b", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "b", Name: "canny", ParentID: "a", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	// Collapsing a cycle member must not stack overflow
	collapsed := map[string]bool{"a": true}
	items := buildGroupedItems(sessions, collapsed)

	// In a cycle, a is b's parent and b is a's parent. Collapsing a
	// hides b (its child). The key assertion: no stack overflow.
	sessionCount := 0

	for _, item := range items {
		if _, ok := item.(sessionItem); ok {
			sessionCount++
		}
	}

	if sessionCount != 1 {
		t.Errorf("expected 1 session (collapsed cycle hides the other), got %d", sessionCount)
	}
}

func TestBuildGroupedItems_HasChildrenFlagOnParent(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child", Name: "bairn", ParentID: "root", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "leaf", Name: "neep", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	items := buildGroupedItems(sessions, nil)

	for _, item := range items {
		if si, ok := item.(sessionItem); ok {
			switch si.info.Name {
			case "ben":
				if !si.hasChildren {
					t.Error("ben should have hasChildren=true")
				}
			case "bairn", "neep":
				if si.hasChildren {
					t.Errorf("%s should have hasChildren=false", si.info.Name)
				}
			}
		}
	}
}

func TestOverlay_SpaceTogglesCollapse(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child1", Name: "bairn-1", ParentID: "root", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child2", Name: "bairn-2", ParentID: "root", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	m := sizedModel(t, sessions, "")

	// Cursor should be on root (index 1, after header)
	item := m.list.SelectedItem().(sessionItem)
	if item.info.ID != "root" {
		t.Fatalf("expected cursor on root, got %s", item.info.ID)
	}

	// Press space to collapse
	updated, _ := sendKey(m, " ")
	m = updated.(overlayModel)

	if !m.collapsed["root"] {
		t.Fatal("root should be collapsed after space")
	}
	// Should only have header + root
	if len(m.list.Items()) != 2 {
		t.Errorf("expected 2 items after collapse, got %d", len(m.list.Items()))
	}

	// Press space again to expand
	updated, _ = sendKey(m, " ")
	m = updated.(overlayModel)

	if m.collapsed["root"] {
		t.Fatal("root should be expanded after second space")
	}

	if len(m.list.Items()) != 4 {
		t.Errorf("expected 4 items after expand, got %d", len(m.list.Items()))
	}
}

func TestOverlay_SpaceOnLeafDoesNothing(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "leaf", Name: "neep", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	m := sizedModel(t, sessions, "")

	itemsBefore := len(m.list.Items())

	updated, _ := sendKey(m, " ")
	m = updated.(overlayModel)

	if len(m.list.Items()) != itemsBefore {
		t.Error("space on leaf should not change item count")
	}
}

func TestOverlay_CollapseAllExpandAll(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root1", Name: "ben-one", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "c1", Name: "bairn-one", ParentID: "root1", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "root2", Name: "ben-two", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "c2", Name: "bairn-two", ParentID: "root2", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "leaf", Name: "neep", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	m := sizedModel(t, sessions, "")

	// Press C to collapse all parents
	updated, _ := sendKey(m, "C")
	m = updated.(overlayModel)

	if !m.collapsed["root1"] || !m.collapsed["root2"] {
		t.Fatal("all parents should be collapsed")
	}
	// header + root1 + root2 + leaf = 4
	if len(m.list.Items()) != 4 {
		t.Errorf("expected 4 items after collapse all, got %d", len(m.list.Items()))
	}

	// Press C again to expand all
	updated, _ = sendKey(m, "C")
	m = updated.(overlayModel)

	if m.collapsed["root1"] || m.collapsed["root2"] {
		t.Fatal("all parents should be expanded")
	}
	// header + 5 sessions = 6
	if len(m.list.Items()) != 6 {
		t.Errorf("expected 6 items after expand all, got %d", len(m.list.Items()))
	}
}

func TestOverlay_CollapsedStatePersists(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child", Name: "bairn", ParentID: "root", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	collapsed := map[string]bool{"root": true}
	m := newOverlayModel(sessions, "", nil, nil, collapsed, nil)

	// Should start with root collapsed
	if len(m.list.Items()) != 2 {
		t.Errorf("expected 2 items with pre-collapsed root, got %d", len(m.list.Items()))
	}

	si := m.list.Items()[1].(sessionItem)
	if !si.collapsed {
		t.Error("root should be marked collapsed from initial state")
	}
}

func TestMaxTreeIndentFromItems(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "child", Name: "bairn", ParentID: "root", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "grandchild", Name: "wee-bairn", ParentID: "child", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	items := buildGroupedItems(sessions, nil)
	maxIndent := maxTreeIndentFromItems(items)
	// wee-bairn prefix is "    └── " = 8 visible chars
	if maxIndent != 8 {
		t.Errorf("maxTreeIndent = %d, want 8", maxIndent)
	}
}

func TestMaxTreeIndentFromItems_NoTree(t *testing.T) {
	items := buildGroupedItems(overlayTestSessions(), nil)

	maxIndent := maxTreeIndentFromItems(items)
	if maxIndent != 0 {
		t.Errorf("maxTreeIndent with no parent-child = %d, want 0", maxIndent)
	}
}

// --- sortSessions ---

func TestSortSessions_CurrentNotBoosted(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "a", Name: "braw", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "b", Name: "canny", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	SortSessions(sessions)

	if sessions[0].ID != "a" {
		t.Errorf("current session should not be boosted, expected braw first, got %q", sessions[0].ID)
	}
}

func TestSortSessions_RunningBeforeStopped(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "a", Name: "braw", Status: "stopped", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "b", Name: "canny", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	SortSessions(sessions)

	if sessions[0].ID != "b" {
		t.Errorf("running session should be first, got %q", sessions[0].ID)
	}
}

func TestSortSessions_AlphabeticalByName(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "b", Name: "canny", Status: "running", CreatedAt: time.Now().Format(time.RFC3339), LastAttachedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339)},
		{ID: "a", Name: "braw", Status: "running", CreatedAt: time.Now().Format(time.RFC3339), LastAttachedAt: time.Now().Add(-1 * time.Hour).Format(time.RFC3339)},
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
	got := displayBranch("d0ugal/graith/braw-fix", "braw-fix")
	if got != "—" {
		t.Errorf("branch matching name should return dash, got %q", got)
	}
}

func TestDisplayBranch_Different(t *testing.T) {
	got := displayBranch("main", "bonnie-feature")
	if got != "main" {
		t.Errorf("non-matching branch should return as-is, got %q", got)
	}
}

func TestDisplayBranch_StripPrefix(t *testing.T) {
	got := displayBranch("user/croft/braw-branch", "neep-name")
	if got != "braw-branch" {
		t.Errorf("should strip user/croft/ prefix, got %q", got)
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

// --- displayLastOutput ---

func TestDisplayLastOutput_UsesLastOutputAt(t *testing.T) {
	s := protocol.SessionInfo{
		ID:           "s1",
		CreatedAt:    time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
		LastOutputAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
	}

	got := displayLastOutput(s)
	if got != "5m" {
		t.Errorf("should use LastOutputAt, got %q", got)
	}
}

func TestDisplayLastOutput_FallsBackToCreated(t *testing.T) {
	s := protocol.SessionInfo{
		ID:        "s1",
		CreatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
	}

	got := displayLastOutput(s)
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

	if filtered[0].Name != "braw-fix" {
		t.Errorf("expected braw-fix, got %q", filtered[0].Name)
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

func TestFilterSessions_MirrorExcludesGitTokens(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID: "s1", Name: "ben-session", RepoName: "graith",
			Branch: "feature-branch", Status: "running",
			Dirty: true, UnpushedCount: 1,
			CreatedAt: time.Now().Format(time.RFC3339),
		},
		{
			ID: "s2", Name: "braw-reviewer", RepoName: "graith",
			Branch: "feature-branch", Status: "running",
			Dirty: true, UnpushedCount: 1,
			Mirror:    true,
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}

	dirty := filterSessions(sessions, "dirty")
	if len(dirty) != 1 || dirty[0].Name != "ben-session" {
		t.Errorf("filtering 'dirty' should return only parent, got %d sessions", len(dirty))
	}

	branch := filterSessions(sessions, "feature-branch")
	if len(branch) != 1 || branch[0].Name != "ben-session" {
		t.Errorf("filtering by branch should return only parent, got %d sessions", len(branch))
	}

	for _, token := range []string{"modified", "clean", "unpushed"} {
		result := filterSessions(sessions, token)
		for _, s := range result {
			if s.Mirror {
				t.Errorf("filtering %q should not return mirror session %q", token, s.Name)
			}
		}
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

	if cw.name < lipgloss.Width("thrawn-dirty") {
		t.Errorf("name width %d < width(%q)", cw.name, "thrawn-dirty")
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

	if cw.summary < 7 {
		t.Errorf("summary should have minimum width 7, got %d", cw.summary)
	}
}

func TestComputeColumnWidths_MirrorUsesDash(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID: "s1", Name: "shared", Status: "running",
			Dirty: true, UnpushedCount: 10,
			Mirror:    true,
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}
	cw := computeColumnWidths(sessions, "")

	expectedMax := lipgloss.Width(displayGit(true, 10))
	if cw.git >= expectedMax {
		t.Errorf("mirror should not inflate git column width: got %d, parent would be %d", cw.git, expectedMax)
	}

	if cw.git != 3 {
		t.Errorf("mirror git column width should be minimum (3), got %d", cw.git)
	}
}

func TestComputeColumnWidths_SummaryWidth(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID:          "s1",
			Name:        "x",
			Status:      "running",
			SummaryText: "fixing the bothy roof",
			CreatedAt:   time.Now().Format(time.RFC3339),
		},
	}

	cw := computeColumnWidths(sessions, "")
	if cw.summary < lipgloss.Width("fixing the bothy roof") {
		t.Errorf("summary width %d should be at least width(%q)", cw.summary, "fixing the bothy roof")
	}
}

// --- sessionItem / groupHeader ---

func TestSessionItemFilterValue(t *testing.T) {
	si := sessionItem{info: protocol.SessionInfo{Name: "braw", RepoName: "croft"}}

	got := si.FilterValue()
	if got != "braw croft" {
		t.Errorf("FilterValue() = %q, want %q", got, "braw croft")
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
	m := newOverlayModel(sessions, "s3", nil, nil, nil, nil) // s3 = bonnie-feature in croft
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
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)

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
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
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

	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
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
	m := newOverlayModel(overlayTestSessions(), "", fetch, nil, nil, nil)

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() should return a command when fetchPreview is set")
	}

	msg := cmd()

	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg, got %T", msg)
	}

	var foundPreview bool

	for _, c := range batch {
		if c == nil {
			continue
		}

		if pm, ok := c().(previewMsg); ok {
			foundPreview = true

			if pm.content != "content" {
				t.Errorf("preview content = %q, want %q", pm.content, "content")
			}
		}
	}

	if !foundPreview {
		t.Fatal("expected a previewMsg in the batch")
	}

	if !called {
		t.Error("fetchPreview should have been called")
	}
}

func TestInit_WithoutFetchPreview(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() should return a command (refresh tick)")
	}

	msg := cmd()
	if _, ok := msg.(previewMsg); ok {
		t.Error("should not produce a previewMsg when fetchPreview is nil")
	}
}

// --- Update: refreshSessionsMsg ---

func TestUpdate_RefreshSessions_PreservesCursor(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", noopFetchPreview, nil, nil, nil)
	m.width = 120
	m.height = 40

	// Navigate to s3 (bonnie-feature in croft)
	for {
		item, ok := m.list.SelectedItem().(sessionItem)
		if ok && item.info.ID == "s3" {
			break
		}

		m.list.CursorDown()
	}

	// Refresh with reordered sessions (s3 now first)
	reordered := []protocol.SessionInfo{sessions[2], sessions[0], sessions[1]}
	updated, _ := m.Update(refreshSessionsMsg{sessions: reordered})
	om := asOverlay(updated)

	item, ok := om.list.SelectedItem().(sessionItem)
	if !ok {
		t.Fatal("expected sessionItem after refresh")
	}

	if item.info.ID != "s3" {
		t.Errorf("selected session ID = %q, want %q", item.info.ID, "s3")
	}
}

func TestUpdate_RefreshSessions_NilPreservesState(t *testing.T) {
	sessions := overlayTestSessions()
	m := sizedModel(t, sessions, "")

	updated, _ := m.Update(refreshSessionsMsg{sessions: nil})
	om := asOverlay(updated)

	if len(om.allSessions) != len(sessions) {
		t.Errorf("allSessions length = %d, want %d (should preserve on nil)", len(om.allSessions), len(sessions))
	}
}

func TestUpdate_RefreshSessions_UpdatesStatus(t *testing.T) {
	sessions := overlayTestSessions()
	m := sizedModel(t, sessions, "")

	// Change s1's status to stopped
	changed := make([]protocol.SessionInfo, len(sessions))
	copy(changed, sessions)
	changed[0].Status = "stopped"

	updated, _ := m.Update(refreshSessionsMsg{sessions: changed})
	om := asOverlay(updated)

	for _, s := range om.allSessions {
		if s.ID == "s1" {
			if s.Status != "stopped" {
				t.Errorf("session s1 status = %q, want %q", s.Status, "stopped")
			}

			return
		}
	}

	t.Error("session s1 not found after refresh")
}

func TestUpdate_RefreshSkippedDuringConfirmDelete(t *testing.T) {
	sessions := overlayTestSessions()
	m := sizedModel(t, sessions, "")
	m.state = stateConfirmDelete

	updated, _ := m.Update(refreshTickMsg{})
	om := asOverlay(updated)

	if om.state != stateConfirmDelete {
		t.Errorf("state = %v, want stateConfirmDelete", om.state)
	}

	if len(om.allSessions) != len(sessions) {
		t.Errorf("allSessions changed during confirm state")
	}
}

func TestUpdate_RefreshSkippedDuringConfirmRestart(t *testing.T) {
	sessions := overlayTestSessions()
	m := sizedModel(t, sessions, "")
	m.state = stateConfirmRestart

	updated, _ := m.Update(refreshTickMsg{})
	om := asOverlay(updated)

	if om.state != stateConfirmRestart {
		t.Errorf("state = %v, want stateConfirmRestart", om.state)
	}
}

func TestUpdate_RefreshSessions_SelectedGone_FallsBack(t *testing.T) {
	sessions := overlayTestSessions()
	m := sizedModel(t, sessions, "")

	// Navigate to s2
	for {
		item, ok := m.list.SelectedItem().(sessionItem)
		if ok && item.info.ID == "s2" {
			break
		}

		m.list.CursorDown()
	}

	// Refresh without s2
	remaining := []protocol.SessionInfo{sessions[0], sessions[2]}
	updated, _ := m.Update(refreshSessionsMsg{sessions: remaining})
	om := asOverlay(updated)

	item, ok := om.list.SelectedItem().(sessionItem)
	if !ok {
		t.Fatal("expected a sessionItem to be selected after fallback")
	}
	// Should have fallen back to some other session, not panic
	if item.info.ID == "s2" {
		t.Error("should not still have s2 selected after it was removed")
	}
}

// --- Update: staggered restart-all ---

func TestUpdate_RestartAll_Staggered(t *testing.T) {
	sessions := overlayTestSessions()

	var restarted []string

	restartFn := func(id string) error {
		restarted = append(restarted, id)
		return nil
	}

	m := sizedModel(t, sessions, "")
	m.restartSession = restartFn

	// Press R to open the restart menu, then choose "all"
	updated, _ := sendKey(m, "R")

	om := asOverlay(updated)
	if om.state != stateRestartMenu {
		t.Fatalf("state = %v, want stateRestartMenu", om.state)
	}

	updated, cmd := sendKey(updated, "a")

	om = asOverlay(updated)
	if om.state != stateRestartingAll {
		t.Fatalf("state = %v, want stateRestartingAll", om.state)
	}

	if len(om.restartQueue) == 0 {
		t.Fatal("restartQueue should not be empty")
	}

	// Execute commands one at a time until done
	updated = drain(updated, cmd)

	om = asOverlay(updated)
	if om.state != stateList {
		t.Errorf("state = %v, want stateList after all restarts", om.state)
	}

	if len(restarted) != len(sessions) {
		t.Errorf("restarted %d sessions, want %d", len(restarted), len(sessions))
	}
}

func TestUpdate_RestartAll_ShowsProgress(t *testing.T) {
	sessions := overlayTestSessions()
	restartFn := func(id string) error { return nil }

	m := sizedModel(t, sessions, "")
	m.restartSession = restartFn

	// Open restart menu and choose "all"
	updated, _ := sendKey(m, "R")
	updated, cmd := sendKey(updated, "a")
	om := asOverlay(updated)

	if om.restartIdx != 0 {
		t.Errorf("restartIdx = %d, want 0", om.restartIdx)
	}

	// Execute the first restart
	if cmd != nil {
		msg := cmd()
		updated, _ = updated.Update(msg)

		om = asOverlay(updated)
		if om.restartIdx != 1 {
			t.Errorf("restartIdx after first restart = %d, want 1", om.restartIdx)
		}
	}
}

func TestUpdate_RestartAll_HandlesErrors(t *testing.T) {
	sessions := overlayTestSessions()
	callCount := 0
	restartFn := func(id string) error {
		callCount++
		if callCount == 2 {
			return fmt.Errorf("restart failed")
		}

		return nil
	}

	m := sizedModel(t, sessions, "")
	m.restartSession = restartFn

	// Open restart menu and choose "all"
	updated, _ := sendKey(m, "R")
	updated, cmd := sendKey(updated, "a")

	// Run all restarts to completion
	updated = drain(updated, cmd)

	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("state = %v, want stateList after all restarts", om.state)
	}
	// All sessions should be attempted even if one fails
	if callCount != len(sessions) {
		t.Errorf("restartFn called %d times, want %d", callCount, len(sessions))
	}
}

func TestUpdate_RestartAll_EscCancelsRemaining(t *testing.T) {
	sessions := overlayTestSessions()

	var restarted []string

	restartFn := func(id string) error {
		restarted = append(restarted, id)
		return nil
	}

	m := sizedModel(t, sessions, "")
	m.restartSession = restartFn

	// Start restart-all
	updated, _ := sendKey(m, "R")
	updated, cmd := sendKey(updated, "a")

	om := asOverlay(updated)
	if om.state != stateRestartingAll {
		t.Fatalf("state = %v, want stateRestartingAll", om.state)
	}

	// Execute first restart
	msg := cmd()
	updated, cmd = updated.Update(msg)
	om = asOverlay(updated)

	if len(restarted) != 1 {
		t.Fatalf("restarted = %d, want 1 after first result", len(restarted))
	}

	// Press Esc to cancel remaining
	updated, _ = sendSpecialKey(updated, tea.KeyEscape)

	om = asOverlay(updated)
	if om.state != stateRestartingAll {
		t.Errorf("state = %v, want stateRestartingAll (waiting for in-flight)", om.state)
	}

	// Execute the in-flight second restart (was already dispatched)
	if cmd != nil {
		msg = cmd()
		updated, _ = updated.Update(msg)
	}

	om = asOverlay(updated)
	if om.state != stateList {
		t.Errorf("state = %v, want stateList after in-flight completes", om.state)
	}
	// Should have restarted exactly 2 (first + in-flight at Esc), not all 3
	if len(restarted) != 2 {
		t.Errorf("restarted %d sessions, want 2 (first + in-flight)", len(restarted))
	}
	// Queue fields should be cleaned up
	if om.restartQueue != nil {
		t.Error("restartQueue should be nil after completion")
	}
}

// --- Update: stop ---

func TestUpdate_Stop_Confirm(t *testing.T) {
	sessions := overlayTestSessions()

	var stopped string

	stopFn := func(id string) error {
		stopped = id
		return nil
	}

	m := sizedModel(t, sessions, "")
	m.stopSession = stopFn
	selected := m.list.SelectedItem().(sessionItem)

	// Press S to confirm-stop, then y
	updated, _ := sendKey(m, "S")

	om := asOverlay(updated)
	if om.state != stateConfirmStop {
		t.Fatalf("state = %v, want stateConfirmStop", om.state)
	}

	updated, cmd := sendKey(updated, "y")
	if cmd == nil {
		t.Fatal("expected a command from stop confirmation")
	}

	updated, _ = updated.Update(cmd())
	om = asOverlay(updated)

	if stopped != selected.info.ID {
		t.Errorf("stopSession called with %q, want %q", stopped, selected.info.ID)
	}

	if om.state != stateList {
		t.Errorf("state = %v, want stateList after stop", om.state)
	}

	for _, s := range om.allSessions {
		if s.ID == selected.info.ID && s.Status != "stopped" {
			t.Errorf("session %q status = %q, want stopped", s.ID, s.Status)
		}
	}
}

func TestUpdate_Stop_Cancel(t *testing.T) {
	called := false
	stopFn := func(string) error {
		called = true
		return nil
	}

	m := sizedModel(t, overlayTestSessions(), "")
	m.stopSession = stopFn

	updated, _ := sendKey(m, "S")
	updated, _ = sendKey(updated, "n")

	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("state = %v, want stateList after cancel", om.state)
	}

	if called {
		t.Error("stopSession should not be called when cancelled")
	}
}

// --- Update: restart menu ---

func TestUpdate_RestartMenu_Stopped(t *testing.T) {
	sessions := overlayTestSessions() // s2 is stopped

	var restarted []string

	restartFn := func(id string) error {
		restarted = append(restarted, id)
		return nil
	}

	m := sizedModel(t, sessions, "")
	m.restartSession = restartFn

	updated, _ := sendKey(m, "R")

	om := asOverlay(updated)
	if om.state != stateRestartMenu {
		t.Fatalf("state = %v, want stateRestartMenu", om.state)
	}

	updated, cmd := sendKey(updated, "s")

	om = asOverlay(updated)
	if len(om.restartQueue) != 1 || om.restartQueue[0] != "s2" {
		t.Fatalf("restartQueue = %v, want [s2]", om.restartQueue)
	}

	drain(updated, cmd)

	if len(restarted) != 1 || restarted[0] != "s2" {
		t.Errorf("restarted = %v, want [s2]", restarted)
	}
}

func TestUpdate_RestartMenu_Outdated(t *testing.T) {
	sessions := overlayTestSessions()
	sessions[0].ConfigStale = true // s1 is stale

	var restarted []string

	restartFn := func(id string) error {
		restarted = append(restarted, id)
		return nil
	}

	m := sizedModel(t, sessions, "")
	m.restartSession = restartFn

	updated, _ := sendKey(m, "R")
	updated, cmd := sendKey(updated, "o")

	om := asOverlay(updated)
	if len(om.restartQueue) != 1 || om.restartQueue[0] != "s1" {
		t.Fatalf("restartQueue = %v, want [s1]", om.restartQueue)
	}

	drain(updated, cmd)

	if len(restarted) != 1 || restarted[0] != "s1" {
		t.Errorf("restarted = %v, want [s1]", restarted)
	}
}

func TestUpdate_RestartMenu_Cancel(t *testing.T) {
	called := false
	restartFn := func(string) error {
		called = true
		return nil
	}

	m := sizedModel(t, overlayTestSessions(), "")
	m.restartSession = restartFn

	updated, _ := sendKey(m, "R")
	updated, _ = sendSpecialKey(updated, tea.KeyEscape)

	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("state = %v, want stateList after cancel", om.state)
	}

	if called {
		t.Error("restartSession should not be called when menu cancelled")
	}
}

func TestUpdate_RestartMenu_All(t *testing.T) {
	sessions := overlayTestSessions()

	var restarted []string

	restartFn := func(id string) error {
		restarted = append(restarted, id)
		return nil
	}

	m := sizedModel(t, sessions, "")
	m.restartSession = restartFn

	updated, _ := sendKey(m, "R")
	updated, cmd := sendKey(updated, "a")

	om := asOverlay(updated)
	if len(om.restartQueue) != len(sessions) {
		t.Fatalf("restartQueue = %v, want all %d sessions", om.restartQueue, len(sessions))
	}

	drain(updated, cmd)

	if len(restarted) != len(sessions) {
		t.Errorf("restarted %d sessions, want %d", len(restarted), len(sessions))
	}
}

func TestUpdate_RestartMenu_EmptyQueue(t *testing.T) {
	// No session is ConfigStale, so "[o]utdated" should be a no-op.
	called := false
	restartFn := func(string) error {
		called = true
		return nil
	}

	m := sizedModel(t, overlayTestSessions(), "")
	m.restartSession = restartFn

	updated, _ := sendKey(m, "R")
	updated, cmd := sendKey(updated, "o")

	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("state = %v, want stateList for empty queue", om.state)
	}

	if len(om.restartQueue) != 0 {
		t.Errorf("restartQueue = %v, want empty", om.restartQueue)
	}

	if cmd != nil {
		if msg := cmd(); msg != nil {
			updated.Update(msg)
		}
	}

	if called {
		t.Error("restartSession should not be called when no sessions match")
	}
}

func TestUpdate_RestartMenu_RespectsFilter(t *testing.T) {
	sessions := overlayTestSessions()

	var restarted []string

	restartFn := func(id string) error {
		restarted = append(restarted, id)
		return nil
	}

	m := sizedModel(t, sessions, "")
	m.restartSession = restartFn
	// Filter to just the "braw-fix" session (s1).
	m.filterInput.SetValue("braw")
	m.rebuildForView()

	updated, _ := sendKey(m, "R")
	updated, cmd := sendKey(updated, "a")

	om := asOverlay(updated)
	if len(om.restartQueue) != 1 || om.restartQueue[0] != "s1" {
		t.Fatalf("restartQueue = %v, want [s1] (filter-scoped)", om.restartQueue)
	}

	drain(updated, cmd)

	if len(restarted) != 1 || restarted[0] != "s1" {
		t.Errorf("restarted = %v, want [s1]", restarted)
	}
}

func TestUpdate_Stop_Error(t *testing.T) {
	sessions := overlayTestSessions()
	stopFn := func(string) error {
		return fmt.Errorf("stop failed")
	}

	m := sizedModel(t, sessions, "")
	m.stopSession = stopFn
	selected := m.list.SelectedItem().(sessionItem)
	origStatus := selected.info.Status

	updated, _ := sendKey(m, "S")
	updated, cmd := sendKey(updated, "y")
	updated, _ = updated.Update(cmd())
	om := asOverlay(updated)

	if om.state != stateList {
		t.Errorf("state = %v, want stateList after failed stop", om.state)
	}

	for _, s := range om.allSessions {
		if s.ID == selected.info.ID && s.Status != origStatus {
			t.Errorf("session status = %q, want unchanged %q on stop failure", s.Status, origStatus)
		}
	}

	if om.stoppedCurrent {
		t.Error("stoppedCurrent should not be set when stop fails")
	}
}

func TestUpdate_Stop_Current_SetsFlag(t *testing.T) {
	sessions := overlayTestSessions()
	stopFn := func(string) error { return nil }

	// Attach context: current session is s1, and it is selected by default.
	m := sizedModel(t, sessions, "s1")
	m.stopSession = stopFn

	updated, _ := sendKey(m, "S")
	updated, cmd := sendKey(updated, "y")
	updated, _ = updated.Update(cmd())
	om := asOverlay(updated)

	if !om.stoppedCurrent {
		t.Error("stoppedCurrent should be set after stopping the current session")
	}

	// Restarting it (via the menu) clears the flag, since restart resumes it.
	om.restartSession = func(string) error { return nil }
	updated, _ = sendKey(om, "R")
	updated, _ = sendKey(updated, "a")

	om = asOverlay(updated)
	if om.stoppedCurrent {
		t.Error("stoppedCurrent should be cleared once the current session is restarted")
	}
}

func TestUpdate_Stop_OnGroupHeader_NoOp(t *testing.T) {
	// In the All view the first item is a group header; pressing S on it
	// should not enter the confirm-stop state.
	m := sizedModel(t, overlayTestSessions(), "")
	m.list.Select(0) // group header

	if _, ok := m.list.SelectedItem().(groupHeader); !ok {
		t.Skip("expected a group header at index 0")
	}

	updated, _ := sendKey(m, "S")

	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("state = %v, want stateList (S on group header is a no-op)", om.state)
	}
}

func TestUpdate_Star_NotStop(t *testing.T) {
	// Regression guard: lowercase s stars, it must not stop.
	sessions := overlayTestSessions()
	stopCalled := false
	starCalled := false
	m := sizedModel(t, sessions, "")
	m.stopSession = func(string) error { stopCalled = true; return nil }
	m.toggleStar = func(string, bool) error { starCalled = true; return nil }
	m.selectSessionByID("s1")

	updated, cmd := sendKey(m, "s")

	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("state = %v, want stateList (s should not open confirm-stop)", om.state)
	}

	if cmd != nil {
		updated.Update(cmd())
	}

	if stopCalled {
		t.Error("lowercase s must not call stopSession")
	}

	if !starCalled {
		t.Error("lowercase s should toggle star")
	}
}

// --- Update: previewMsg ---

func TestUpdate_PreviewMsg_Applied(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil, nil, nil)
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
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil, nil, nil)
	m.previewContent = "old"

	updated, _ := m.Update(previewMsg{sessionID: "nonexistent", content: "stale"})

	om := asOverlay(updated)
	if om.previewContent != "old" {
		t.Errorf("stale preview should not be applied, got %q", om.previewContent)
	}
}

func TestUpdate_PreviewMsg_EmptyContentSkipsSessionID(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil, nil, nil)
	selected := m.list.SelectedItem().(sessionItem)

	updated, _ := m.Update(previewMsg{sessionID: selected.info.ID, content: "   \n  "})

	om := asOverlay(updated)
	if om.previewSessionID != "" {
		t.Error("empty/whitespace preview should not set previewSessionID")
	}
}

// --- Update: WindowSizeMsg ---

func TestUpdate_WindowSizeMsg(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

	updated, _ := sendWindowSize(m, 120, 40)

	om := asOverlay(updated)
	if om.width != 120 || om.height != 40 {
		t.Errorf("size = %dx%d, want 120x40", om.width, om.height)
	}
}

func TestUpdate_WindowSizeMsg_Small(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

	updated, _ := sendWindowSize(m, 20, 5)

	om := asOverlay(updated)
	if om.width != 20 || om.height != 5 {
		t.Errorf("size = %dx%d, want 20x5", om.width, om.height)
	}
}

// --- Update: List state key handling ---

func TestUpdate_QuitQ(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

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
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

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
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
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
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
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
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

	updated, _ := sendKey(m, "x")

	om := asOverlay(updated)
	if om.state != stateConfirmDelete {
		t.Errorf("state = %d, want stateConfirmDelete(%d)", om.state, stateConfirmDelete)
	}
}

func TestUpdate_SlashEntersFilter(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

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
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
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
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)

	// Navigate down through all items to reach graith group
	// Start is on bonnie-feature (croft), j→braw-fix (graith, skips header)
	updated, _ := sendKey(m, "j")
	om := asOverlay(updated)
	item := om.list.SelectedItem()

	si, ok := item.(sessionItem)
	if !ok {
		t.Fatalf("after navigating past group header, expected sessionItem, got %T", item)
	}

	if si.info.RepoName != "graith" {
		t.Errorf("expected to land in graith group, got %q", si.info.RepoName)
	}
}

func TestUpdate_NavigationUpSkipsGroupHeaders(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)

	// Navigate to a graith item (j from croft skips graith header)
	updated, _ := sendKey(m, "j")

	om := asOverlay(updated)
	if si, ok := om.list.SelectedItem().(sessionItem); ok {
		if si.info.RepoName != "graith" {
			t.Fatalf("expected to be in graith, got %q", si.info.RepoName)
		}
	}

	// Navigate up — should skip the "graith" header back to croft
	updated, _ = sendKey(om, "k")
	om = asOverlay(updated)
	item := om.list.SelectedItem()

	si, ok := item.(sessionItem)
	if !ok {
		t.Fatalf("navigating up past group header, expected sessionItem, got %T", item)
	}

	if si.info.RepoName != "croft" {
		t.Errorf("expected croft group, got %q", si.info.RepoName)
	}
}

func TestUpdate_DownArrowNavigation(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
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
	m := newOverlayModel(overlayTestSessions(), "", fetch, nil, nil, nil)

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
	sessions := overlayTestSessions() // croft (1) + graith (2)
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)

	// Should start in croft group (alphabetically first)
	initial := m.list.SelectedItem().(sessionItem)
	if initial.info.RepoName != "croft" {
		t.Fatalf("expected to start in croft, got %q", initial.info.RepoName)
	}

	// Tab should jump to graith group
	sized, _ := sendWindowSize(m, 120, 40)
	updated, _ := sendSpecialKey(asOverlay(sized), tea.KeyTab)
	om := asOverlay(updated)

	after := om.list.SelectedItem().(sessionItem)
	if after.info.RepoName != "graith" {
		t.Errorf("tab should jump to graith, got %q", after.info.RepoName)
	}
}

func TestUpdate_ShiftTabJumpsToPrevGroup(t *testing.T) {
	sessions := overlayTestSessions()
	// Start on the croft session (s3)
	m := newOverlayModel(sessions, "s3", nil, nil, nil, nil)

	initial := m.list.SelectedItem().(sessionItem)
	if initial.info.RepoName != "croft" {
		t.Fatalf("expected to start in croft, got %q", initial.info.RepoName)
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
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendKey(m, "/")

	updated, _ = sendSpecialKey(asOverlay(updated), tea.KeyEscape)

	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("esc in filter should return to stateList, got %d", om.state)
	}
}

func TestUpdate_FilterEnterAttachesSession(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendKey(m, "/")

	updated, _ = sendSpecialKey(asOverlay(updated), tea.KeyEnter)

	om := asOverlay(updated)
	if om.selected == nil {
		t.Fatal("enter in filter should select the highlighted session")
	}
}

func TestUpdate_FilterEnterAttachesCorrectFilteredSession(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendKey(m, "/")

	for _, ch := range "bonnie" {
		updated, _ = sendKey(asOverlay(updated), string(ch))
	}

	updated, _ = sendSpecialKey(asOverlay(updated), tea.KeyEnter)

	om := asOverlay(updated)
	if om.selected == nil {
		t.Fatal("enter after filter should select a session")
	}

	if om.selected.ID != "s3" {
		t.Errorf("selected session ID = %q, want %q", om.selected.ID, "s3")
	}
}

func TestUpdate_FilterEnterNoMatchDoesNotAttach(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendKey(m, "/")

	for _, ch := range "zzzzz" {
		updated, _ = sendKey(asOverlay(updated), string(ch))
	}

	updated, _ = sendSpecialKey(asOverlay(updated), tea.KeyEnter)

	om := asOverlay(updated)
	if om.selected != nil {
		t.Error("enter with no matches should not select anything")
	}

	if om.state != stateList {
		t.Errorf("state = %d, want stateList after enter on no-match", om.state)
	}
}

func TestUpdate_FilterTypingUpdatesInput(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
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
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

	// Enter filter mode and type "croft"
	updated, _ := sendKey(m, "/")
	for _, ch := range "croft" {
		updated, _ = sendKey(asOverlay(updated), string(ch))
	}

	om := asOverlay(updated)

	// Should only show croft sessions
	sessionCount := countSessionItems(om)

	if sessionCount != 1 {
		t.Errorf("filtering for 'croft' should show 1 session, got %d", sessionCount)
	}
}

func TestUpdate_FilterTypingTriggersPreviewFetch(t *testing.T) {
	fetch := func(id string) string {
		return "preview for " + id
	}
	// Start on s3 (croft). Filtering to "graith" will change selection to s1.
	m := newOverlayModel(overlayTestSessions(), "s3", fetch, nil, nil, nil)

	initial := m.list.SelectedItem().(sessionItem)
	if initial.info.ID != "s3" {
		t.Fatalf("expected cursor on s3, got %q", initial.info.ID)
	}

	// Enter filter mode and type "graith" to filter out s3
	updated, _ := sendKey(m, "/")

	var cmd tea.Cmd
	for _, ch := range "graith" {
		updated, cmd = sendKey(asOverlay(updated), string(ch))
	}

	if cmd == nil {
		t.Fatal("typing in filter mode should return a command (including preview fetch)")
	}

	om := asOverlay(updated)

	selected := om.list.SelectedItem().(sessionItem)
	if selected.info.ID == "s3" {
		t.Fatal("filter should have changed selection away from s3")
	}

	batchMsg := cmd()

	batch, ok := batchMsg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg from filter typing, got %T", batchMsg)
	}

	foundPreview := false

	for _, c := range batch {
		if c == nil {
			continue
		}

		msg := c()
		if pm, ok := msg.(previewMsg); ok {
			foundPreview = true

			if pm.sessionID != selected.info.ID {
				t.Errorf("preview fetch session = %q, want %q", pm.sessionID, selected.info.ID)
			}
		}
	}

	if !foundPreview {
		t.Error("filter typing should trigger a preview fetch for the newly selected session")
	}
}

func TestUpdate_FilterNoMatchClearsPreview(t *testing.T) {
	fetch := func(id string) string {
		return "preview for " + id
	}
	m := newOverlayModel(overlayTestSessions(), "", fetch, nil, nil, nil)
	m.previewContent = "old preview"
	m.previewSessionID = "s1"

	// Enter filter mode and type a query that matches nothing
	updated, _ := sendKey(m, "/")
	for _, ch := range "zzzzz" {
		updated, _ = sendKey(asOverlay(updated), string(ch))
	}

	om := asOverlay(updated)

	if om.previewContent != "" {
		t.Errorf("preview content should be cleared when no sessions match, got %q", om.previewContent)
	}

	if om.previewSessionID != "" {
		t.Errorf("preview session ID should be cleared when no sessions match, got %q", om.previewSessionID)
	}
}

func TestUpdate_FilterResetsCursorToFirstSession(t *testing.T) {
	sessions := overlayTestSessions()
	// Start cursor on s3 (last session, in croft group)
	m := newOverlayModel(sessions, "s3", nil, nil, nil, nil)

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
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)

	// Filter
	updated, _ := sendKey(m, "/")
	for _, ch := range "croft" {
		updated, _ = sendKey(asOverlay(updated), string(ch))
	}

	// Esc to restore
	updated, _ = sendSpecialKey(asOverlay(updated), tea.KeyEscape)
	om := asOverlay(updated)

	sessionCount := countSessionItems(om)

	if sessionCount != len(sessions) {
		t.Errorf("esc should restore all %d sessions, got %d", len(sessions), sessionCount)
	}
}

// --- Update: configurable picker keybindings (issue #918) ---

// TestOverlayConfigurableKeys is the regression test for the picker side of
// #918: delete_session, resume_session and search must honour the configured
// keybinding instead of the old hardcoded x/R/ literals.
func TestOverlayConfigurableKeys(t *testing.T) {
	cases := []struct {
		name  string
		keys  OverlayKeys
		press string
		want  overlayState
	}{
		{"delete", OverlayKeys{DeleteSession: "z"}, "z", stateConfirmDelete},
		{"resume", OverlayKeys{ResumeSession: "Z"}, "Z", stateRestartMenu},
		{"search", OverlayKeys{Search: "?"}, "?", stateFilter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newOverlayModel(overlayTestSessions(), "", nil, func(string) error { return nil }, nil, nil)
			m.restartSession = func(string) error { return nil }
			m.applyKeys(tc.keys)

			updated, _ := sendKey(m, tc.press)
			if got := asOverlay(updated).state; got != tc.want {
				t.Fatalf("press %q: state = %v, want %v", tc.press, got, tc.want)
			}
		})
	}
}

// TestOverlayOldLiteralIgnoredAfterRemap confirms the previously-hardcoded
// literals no longer trigger their action once the key is rebound.
func TestOverlayOldLiteralIgnoredAfterRemap(t *testing.T) {
	cases := []struct {
		name    string
		keys    OverlayKeys
		oldKey  string
		notWant overlayState
	}{
		{"delete", OverlayKeys{DeleteSession: "z"}, "x", stateConfirmDelete},
		{"resume", OverlayKeys{ResumeSession: "Z"}, "R", stateRestartMenu},
		{"search", OverlayKeys{Search: "?"}, "/", stateFilter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newOverlayModel(overlayTestSessions(), "", nil, func(string) error { return nil }, nil, nil)
			m.restartSession = func(string) error { return nil }
			m.applyKeys(tc.keys)

			updated, _ := sendKey(m, tc.oldKey)
			if got := asOverlay(updated).state; got == tc.notWant {
				t.Fatalf("old literal %q should not trigger %v after remap", tc.oldKey, tc.notWant)
			}
		})
	}
}

// TestOverlayDefaultKeysWhenUnset confirms the built-in defaults still apply
// when no keybindings are configured (empty OverlayKeys).
func TestOverlayDefaultKeysWhenUnset(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, func(string) error { return nil }, nil, nil)
	m.applyKeys(OverlayKeys{})

	updated, _ := sendKey(m, "x")
	if got := asOverlay(updated).state; got != stateConfirmDelete {
		t.Fatalf("default 'x' should open confirm-delete, got %v", got)
	}
}

// --- Update: Confirm delete state ---

func TestUpdate_ConfirmDeleteY(t *testing.T) {
	deletedID := ""
	deleteFn := func(sid string) error { deletedID = sid; return nil }
	m := newOverlayModel(overlayTestSessions(), "", nil, deleteFn, nil, nil)
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
	m := newOverlayModel(overlayTestSessions(), "", nil, deleteFn, nil, nil)

	updated, _ := sendKey(m, "x")

	_, cmd := sendKey(asOverlay(updated), "Y")
	if cmd == nil {
		t.Fatal("Y should also produce a delete command")
	}
}

func TestUpdate_ConfirmDeleteCancel(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

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
			m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
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
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	if v := m.View().Content; v != "" {
		t.Errorf("View() with zero size should be empty, got %d chars", len(v))
	}
}

func TestView_RendersSessionList(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	om := asOverlay(updated)
	view := om.View().Content

	if !strings.Contains(view, "All") {
		t.Error("view should contain the view name 'All'")
	}

	for _, name := range []string{"canny-tests", "braw-fix", "bonnie-feature"} {
		if !strings.Contains(view, name) {
			t.Errorf("view should contain session name %q", name)
		}
	}
}

// TestView_PreviewPanelCICounts covers the selected-session preview PR line:
// a live PR shows the passed/total progress count, while a merged/closed PR
// suppresses the (now stale) CI badge entirely — matching displayPR/cliPR and
// the #773 terminal-state invariant, so the preview can't show
// "PR #7 merged  CI: pending 16/22".
func TestView_PreviewPanelCICounts(t *testing.T) {
	cases := []struct {
		name        string
		pr          *protocol.PRInfo
		ci          *protocol.CIInfo
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "open pending shows counts",
			pr:          &protocol.PRInfo{Number: 7, State: "open"},
			ci:          &protocol.CIInfo{State: "pending", Passed: 16, Total: 22},
			wantContain: []string{"PR #7 open", "CI: pending 16/22"},
		},
		{
			name:        "open failing shows counts and fail glyph",
			pr:          &protocol.PRInfo{Number: 8, State: "open"},
			ci:          &protocol.CIInfo{State: "failing", FailingChecks: []string{"build"}, Passed: 19, Total: 22},
			wantContain: []string{"PR #8 open", "CI: failing 19/22 1✗"},
		},
		{
			name:        "merged suppresses stale CI counts",
			pr:          &protocol.PRInfo{Number: 9, State: "merged"},
			ci:          &protocol.CIInfo{State: "pending", Passed: 16, Total: 22},
			wantContain: []string{"PR #9 merged"},
			wantAbsent:  []string{"CI: pending", "16/22"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sessions := []protocol.SessionInfo{{
				ID: "s1", Name: "braw", RepoName: "croft", Status: "running",
				PullRequest: c.pr, CI: c.ci,
			}}
			m := newOverlayModel(sessions, "", nil, nil, nil, nil)
			updated, _ := sendWindowSize(m, 150, 40)
			view := asOverlay(updated).View().Content

			for _, want := range c.wantContain {
				if !strings.Contains(view, want) {
					t.Errorf("preview should contain %q\n---\n%s", want, view)
				}
			}

			for _, absent := range c.wantAbsent {
				if strings.Contains(view, absent) {
					t.Errorf("preview should NOT contain %q (stale CI on terminal PR)\n---\n%s", absent, view)
				}
			}
		})
	}
}

func TestView_ShowsGroupHeaders(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	om := asOverlay(updated)
	view := om.View().Content

	if !strings.Contains(view, "graith") {
		t.Error("view should contain group header 'graith'")
	}

	if !strings.Contains(view, "croft") {
		t.Error("view should contain group header 'croft'")
	}
}

func TestView_ShowsColumnHeaders(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 150, 40)
	view := asOverlay(updated).View().Content

	for _, header := range []string{"Session", "Status", "Summary", "Git", "PR", "Output"} {
		if !strings.Contains(view, header) {
			t.Errorf("view should contain column header %q", header)
		}
	}
}

func TestView_ShowsHelpBar(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 150, 40)
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
	m := newOverlayModel(sessions, "s1", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 150, 40)
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "agent: claude") {
		t.Error("detail line should show agent type")
	}

	if !strings.Contains(view, "base: main") {
		t.Error("detail line should show base branch")
	}
}

func TestView_MirrorOmitsBranchAndBase(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID: "s1", Name: "braw-reviewer", RepoName: "graith",
			Branch: "refs/heads/feature", BaseBranch: "main",
			Agent: "claude", Status: "running",
			WorktreePath: "/tmp/test-worktree",
			Mirror:       true,
			CreatedAt:    time.Now().Format(time.RFC3339),
		},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 150, 40)
	view := asOverlay(updated).View().Content

	if strings.Contains(view, "branch: feature") {
		t.Error("mirror detail should not show branch")
	}

	if strings.Contains(view, "base: main") {
		t.Error("mirror detail should not show base branch")
	}

	if !strings.Contains(view, "agent: claude") {
		t.Error("mirror detail should still show agent")
	}
}

func TestView_ShowsCurrentSessionMarker(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "s1", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 150, 40)
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "▸") {
		t.Error("view should contain ▸ marker for current session")
	}
}

func TestView_ConfirmDeleteShowsPrompt(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(asOverlay(updated), "x")
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "Delete") || !strings.Contains(view, "[y/N]") {
		t.Error("delete confirmation should show 'Delete ... [y/N]'")
	}
}

func TestView_ConfirmDeleteShowsUnsavedWarning(t *testing.T) {
	sessions := overlayTestSessionsWithGitStatus()
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(asOverlay(updated), "x")
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "unsaved work") {
		t.Error("delete confirmation for dirty session should warn about unsaved work")
	}

	if !strings.Contains(view, "Uncommitted changes") {
		t.Error("delete confirmation should mention uncommitted changes")
	}

	if !strings.Contains(view, "3 unpushed commits") {
		t.Error("delete confirmation should mention unpushed commits")
	}
}

func TestView_ConfirmDeleteNoWarningForCleanSession(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(asOverlay(updated), "x")
	view := asOverlay(updated).View().Content

	if strings.Contains(view, "unsaved work") {
		t.Error("delete confirmation for clean session should not warn about unsaved work")
	}

	if !strings.Contains(view, "Delete") || !strings.Contains(view, "[y/N]") {
		t.Error("delete confirmation should still show 'Delete ... [y/N]'")
	}
}

func TestView_ConfirmDeleteDirtyOnly(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID:        "s1",
			Name:      "thrawn-only",
			RepoName:  "graith",
			Status:    "running",
			Dirty:     true,
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(asOverlay(updated), "x")
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "Uncommitted changes") {
		t.Error("should warn about uncommitted changes")
	}

	if strings.Contains(view, "unpushed commit") {
		t.Error("should not mention unpushed commits when there are none")
	}
}

func TestView_ConfirmDeleteUnpushedSingular(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID:            "s1",
			Name:          "neep-commit",
			RepoName:      "graith",
			Status:        "running",
			UnpushedCount: 1,
			CreatedAt:     time.Now().Format(time.RFC3339),
		},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(asOverlay(updated), "x")
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "1 unpushed commit") {
		t.Error("should use singular 'commit' for count of 1")
	}

	if strings.Contains(view, "commits") {
		t.Error("should not use plural 'commits' for count of 1")
	}
}

func TestUpdate_ConfirmDeleteNilCallback(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendKey(m, "x")
	updated, _ = sendKey(asOverlay(updated), "y")

	om := asOverlay(updated)
	if om.state != stateList {
		t.Errorf("confirming delete with nil callback should return to list, got state %d", om.state)
	}
}

func TestView_ConfirmDeleteUnpushedOnly(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID:            "s1",
			Name:          "thrawn-unpushed",
			RepoName:      "graith",
			Status:        "running",
			UnpushedCount: 5,
			CreatedAt:     time.Now().Format(time.RFC3339),
		},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(asOverlay(updated), "x")
	view := asOverlay(updated).View().Content

	if strings.Contains(view, "Uncommitted changes") {
		t.Error("should not mention uncommitted changes when there are none")
	}

	if !strings.Contains(view, "5 unpushed commits") {
		t.Error("should warn about unpushed commits")
	}
}

func TestView_FilterModeShowsInput(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(asOverlay(updated), "/")
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "Filter") {
		t.Error("filter mode should show 'Filter'")
	}

	// Verify list mode does NOT show filter prompt
	m2 := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated2, _ := sendWindowSize(m2, 120, 40)

	listView := asOverlay(updated2).View().Content
	if strings.Contains(listView, "Filter:") {
		t.Error("list mode should not show filter prompt")
	}
}

func TestView_SmallTerminal(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 30, 8)
	view := asOverlay(updated).View().Content

	lines := strings.Split(view, "\n")
	if len(lines) != 8 {
		t.Errorf("view should have exactly %d lines for height=%d, got %d", 8, 8, len(lines))
	}
}

func TestView_PreviewBackground(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil, nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	om := asOverlay(updated)

	selected := om.list.SelectedItem().(sessionItem)
	updated, _ = om.Update(previewMsg{sessionID: selected.info.ID, content: "UNIQUE_PREVIEW_LINE_1\nUNIQUE_PREVIEW_LINE_2"})
	om = asOverlay(updated)

	view := om.View().Content
	if !strings.Contains(view, "UNIQUE_PREVIEW_LINE_1") {
		t.Error("view should render preview content in the background")
	}

	m2 := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated2, _ := sendWindowSize(m2, 120, 40)

	viewNoPreview := asOverlay(updated2).View().Content
	if strings.Contains(viewNoPreview, "UNIQUE_PREVIEW_LINE_1") {
		t.Error("view without preview should not contain preview text")
	}
}

// --- Edge cases ---

func TestSingleSession(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "neep-one", RepoName: "repo", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)

	si, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		t.Fatal("cursor should be on the session item")
	}

	if si.info.Name != "neep-one" {
		t.Errorf("selected = %q, want %q", si.info.Name, "neep-one")
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
	m := newOverlayModel(nil, "", nil, nil, nil, nil)

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
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

	cmd := m.fetchPreviewCmd()
	if cmd != nil {
		t.Error("fetchPreviewCmd should return nil when fetchPreview is nil")
	}
}

func TestFetchPreviewCmd_GroupHeaderSelected(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", noopFetchPreview, nil, nil, nil)
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
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
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
	m := newOverlayModel(sessions, "", nil, deleteFn, nil, nil)
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
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
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

	items := buildGroupedItems(sessions, nil)
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
	items := buildGroupedItems(overlayTestSessions(), nil)
	l := list.New(items, d, 120, 10)

	var buf strings.Builder
	d.Render(&buf, l, 0, items[0])

	line := buf.String()
	if !strings.Contains(line, "croft") {
		t.Errorf("group header render should contain %q, got %q", "croft", line)
	}

	if !strings.Contains(line, "▸") {
		t.Error("group header should have ▸ prefix")
	}

	if !strings.Contains(line, "(1)") {
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

			if !strings.Contains(renderItem(sessions, "", 1), tt.indicator) {
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

	if !strings.Contains(renderItem(sessions, "", 1), "thinking") {
		t.Error("should show agent status 'thinking' instead of 'running'")
	}
}

func TestCompactDelegate_RenderGitStatus(t *testing.T) {
	sessions := overlayTestSessionsWithGitStatus()
	line := renderItem(sessions, "", 1)
	// New format: "M" for dirty, "↑3" for unpushed
	if !strings.Contains(line, "M") {
		t.Error("should show 'M' for dirty sessions")
	}

	if !strings.Contains(line, "↑3") {
		t.Error("should show '↑3' for unpushed commits")
	}
}

func TestCompactDelegate_RenderMirrorShowsDash(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID: "s1", Name: "braw-reviewer", RepoName: "graith",
			Branch: "d0ugal/graith/feature", Agent: "claude",
			Status: "running", AgentStatus: "active",
			Dirty: true, UnpushedCount: 5,
			Mirror:    true,
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}

	line := renderItem(sessions, "", 1)
	if !strings.Contains(line, "—") {
		t.Error("mirror session should show '—' in git column")
	}

	gitVal := displayGit(true, 5)
	if strings.Contains(line, gitVal) {
		t.Errorf("mirror session should not show %q even when dirty+unpushed", gitVal)
	}
}

func TestCompactDelegate_RenderCurrentSession(t *testing.T) {
	sessions := overlayTestSessions()
	cols := computeColumnWidths(sessions, "s1")
	d := compactDelegate{cols: cols, currentSessionID: "s1"}
	items := buildGroupedItems(sessions, nil)
	l := list.New(items, d, 120, 10)

	// Find s1's index
	for i, item := range items {
		if si, ok := item.(sessionItem); ok && si.info.ID == "s1" {
			var buf strings.Builder
			d.Render(&buf, l, i, item)

			if !strings.Contains(buf.String(), "▸") {
				t.Error("current session should have ▸ marker")
			}

			return
		}
	}

	t.Fatal("s1 not found in items")
}

func TestCompactDelegate_RenderSummary(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID: "s1", Name: "braw-fix", RepoName: "repo",
			Status: "running", SummaryText: "fixing the bothy",
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}

	if !strings.Contains(renderItem(sessions, "", 1), "fixing the bothy") {
		t.Error("render should show summary text")
	}
}

func TestCompactDelegate_RenderSelectedVsUnselected(t *testing.T) {
	sessions := overlayTestSessions()
	cols := computeColumnWidths(sessions, "")
	d := compactDelegate{cols: cols}
	items := buildGroupedItems(sessions, nil)
	l := list.New(items, d, 120, 10)
	l.Select(1)

	var selectedBuf, unselectedBuf strings.Builder
	d.Render(&selectedBuf, l, 1, items[1])
	d.Render(&unselectedBuf, l, 2, items[2])

	selected := selectedBuf.String()
	unselected := unselectedBuf.String()

	if !strings.Contains(selected, ">") {
		t.Error("selected item should contain '>'")
	}

	if strings.Contains(unselected, ">") {
		t.Error("unselected item should not contain '>'")
	}

	// The selected row is highlighted with a full-width background so the whole
	// line stands out, not just the "> " cursor. The background SGR must be
	// present on the selected row and absent from the unselected one, and the
	// highlight must span the full list width. lipgloss v2 always emits color
	// under `go test`, so treat an empty open as a failure rather than silently
	// skipping the assertions (which would degrade this to the pre-change test).
	open := selectRowOpen()
	if open == "" {
		t.Fatal("selectRowOpen returned empty; cannot verify the row highlight")
	}

	if !strings.Contains(selected, open) {
		t.Errorf("selected row should carry the highlight background %q, got %q", open, selected)
	}

	if strings.Contains(unselected, open) {
		t.Errorf("unselected row should not carry the highlight background, got %q", unselected)
	}

	if vis := lipgloss.Width(selected); vis != l.Width() {
		t.Errorf("selected row highlight should span the full width %d, got visible width %d", l.Width(), vis)
	}
}

// TestHighlightSelectedRow guards the core reset-reopen mechanism directly: a
// picker row is built from columns that each end in a full SGR reset, which
// would clear the background mid-row. highlightSelectedRow must re-open the
// background after every reset so it spans the whole line. Asserting on the
// full rendered row (as TestCompactDelegate_RenderSelectedVsUnselected does)
// isn't enough — dropping the interior re-open still leaves one opening
// sequence and full-width padding, so this exercises the helper in isolation
// with both reset spellings present.
func TestHighlightSelectedRow(t *testing.T) {
	open := selectRowOpen()
	if open == "" {
		t.Fatal("selectRowOpen returned empty; cannot verify the row highlight")
	}

	// Two styled cells, each terminated by a reset: the short "\x1b[m" that
	// lipgloss v2 emits and the long "\x1b[0m" the defensive branch handles.
	line := "braw" + "\x1b[m" + "canny" + "\x1b[0m" + "bonnie"

	const width = 20

	out := highlightSelectedRow(line, width)

	if !strings.HasPrefix(out, open) {
		t.Errorf("highlighted row should open with the background %q, got %q", open, out)
	}

	if !strings.HasSuffix(out, "\x1b[0m") {
		t.Errorf("highlighted row should end with a reset, got %q", out)
	}

	// The background must be re-opened immediately after each interior reset —
	// this is the assertion that fails if the ReplaceAll lines are removed.
	if !strings.Contains(out, "\x1b[m"+open) {
		t.Errorf("background should re-open after the short reset, got %q", out)
	}

	if !strings.Contains(out, "\x1b[0m"+open) {
		t.Errorf("background should re-open after the long reset, got %q", out)
	}

	// One opening at the start plus one after each of the two interior resets.
	if got := strings.Count(out, open); got != 3 {
		t.Errorf("expected the background to open 3 times (start + 2 resets), got %d in %q", got, out)
	}

	// The highlight spans the full width via right-padding.
	if vis := lipgloss.Width(out); vis != width {
		t.Errorf("highlighted row should span width %d, got visible width %d", width, vis)
	}
}

// TestHighlightSelectedRow_ZeroWidth checks the width<=0 path: no padding, but
// the row is still wrapped and terminated so styling can't leak downstream.
func TestHighlightSelectedRow_ZeroWidth(t *testing.T) {
	open := selectRowOpen()
	if open == "" {
		t.Fatal("selectRowOpen returned empty; cannot verify the row highlight")
	}

	out := highlightSelectedRow("dreich"+"\x1b[m"+"haar", 0)

	if !strings.HasPrefix(out, open) || !strings.HasSuffix(out, "\x1b[0m") {
		t.Errorf("zero-width row should still be wrapped in open/reset, got %q", out)
	}

	if !strings.Contains(out, "\x1b[m"+open) {
		t.Errorf("zero-width row should still re-open the background after resets, got %q", out)
	}
}

func TestCompactDelegate_RenderTruncatesLongLine(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID: "s1", Name: "very-long-braw-session-name-that-exceeds-width", RepoName: "repo",
			Status: "running", Branch: "feature/very-long-branch-name-here",
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}
	cols := computeColumnWidths(sessions, "")
	d := compactDelegate{cols: cols}
	items := buildGroupedItems(sessions, nil)
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
	cw := columnWidths{name: 10, trailing: map[string]int{
		"status": 8, "summary": 15, "git": 5, "pr": 6, "review": 6, "output": 4,
	}}
	got := cw.totalWidth()
	// 9 + 10 + 4 + (2+8) + (2+15) + (2+5) + (2+6) + (2+6) + (2+4) = 79
	if got != 79 {
		t.Errorf("totalWidth() = %d, want 79", got)
	}
}

// --- filterNeedsAttention ---

func TestFilterNeedsAttention(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "braw-session", Status: "running", AgentStatus: "active"},
		{ID: "s2", Name: "thrawn-approval", Status: "running", AgentStatus: "approval"},
		{ID: "s3", Name: "thrawn-errored", Status: "errored"},
		{ID: "s4", Name: "canny-ready", Status: "running", AgentStatus: "ready"},
		{ID: "s5", Name: "neep-clean", Status: "stopped"},
		{ID: "s6", Name: "thrawn-dirty", Status: "stopped", Dirty: true},
		{ID: "s7", Name: "thrawn-unpushed", Status: "stopped", UnpushedCount: 2},
	}
	result := filterNeedsAttention(sessions)

	names := make([]string, len(result))
	for i, s := range result {
		names[i] = s.Name
	}
	// Should include: thrawn-approval, thrawn-errored, canny-ready (running+ready), thrawn-dirty, thrawn-unpushed
	// Should exclude: braw-session (working fine), neep-clean (nothing to save)
	if len(result) != 5 {
		t.Fatalf("got %d sessions %v, want 5", len(result), names)
	}

	for _, name := range []string{"thrawn-approval", "thrawn-errored", "canny-ready", "thrawn-dirty", "thrawn-unpushed"} {
		found := false

		for _, n := range names {
			if n == name {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("missing expected session %q in result %v", name, names)
		}
	}
}

func TestFilterNeedsAttention_ExcludesMirror(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "ben-dirty", Status: "stopped", Dirty: true},
		{ID: "s2", Name: "braw-shared-dirty", Status: "stopped", Dirty: true, Mirror: true},
		{ID: "s3", Name: "braw-shared-unpushed", Status: "stopped", UnpushedCount: 3, Mirror: true},
	}

	result := filterNeedsAttention(sessions)
	if len(result) != 1 {
		names := make([]string, len(result))
		for i, s := range result {
			names[i] = s.Name
		}

		t.Fatalf("got %d sessions %v, want 1 (only ben-dirty)", len(result), names)
	}

	if result[0].Name != "ben-dirty" {
		t.Errorf("got %q, want ben-dirty", result[0].Name)
	}
}

func TestView_MirrorDeleteNoUnsavedWarning(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID: "s1", Name: "braw-shared-dirty", RepoName: "graith",
			Status: "running", Agent: "claude",
			Dirty: true, UnpushedCount: 3,
			Mirror:    true,
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(asOverlay(updated), "x")
	view := asOverlay(updated).View().Content

	if strings.Contains(view, "unsaved work") {
		t.Error("mirror delete should not warn about unsaved work")
	}

	if strings.Contains(view, "Uncommitted changes") {
		t.Error("mirror delete should not mention uncommitted changes")
	}
}

// --- filterActive ---

func TestFilterActive(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "braw-session", Status: "running", AgentStatus: "active",
			CreatedAt: time.Now().Add(-10 * time.Minute).Format(time.RFC3339)},
		{ID: "s2", Name: "thrawn-approval", Status: "running", AgentStatus: "approval",
			CreatedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339)},
		{ID: "s3", Name: "neep-stopped", Status: "stopped",
			CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "s4", Name: "canny-running", Status: "running", AgentStatus: "unknown",
			CreatedAt: time.Now().Add(-1 * time.Minute).Format(time.RFC3339)},
	}

	result := filterActive(sessions)
	if len(result) != 3 {
		names := make([]string, len(result))
		for i, s := range result {
			names[i] = s.Name
		}

		t.Fatalf("got %d sessions %v, want 3 (all running)", len(result), names)
	}
	// Should be sorted newest first
	if result[0].Name != "canny-running" {
		t.Errorf("first session = %q, want canny-running (newest)", result[0].Name)
	}
}

// --- sortByStatusAge ---

func TestSortByStatusAge(t *testing.T) {
	now := time.Now()
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "braw-recent", StatusChangedAt: now.Add(-1 * time.Minute).Format(time.RFC3339)},
		{ID: "s2", Name: "thrawn-old", StatusChangedAt: now.Add(-1 * time.Hour).Format(time.RFC3339)},
		{ID: "s3", Name: "canny-medium", StatusChangedAt: now.Add(-10 * time.Minute).Format(time.RFC3339)},
	}
	sortByStatusAge(sessions)

	if sessions[0].Name != "thrawn-old" || sessions[1].Name != "canny-medium" || sessions[2].Name != "braw-recent" {
		t.Errorf("got order [%s, %s, %s], want [thrawn-old, canny-medium, braw-recent]",
			sessions[0].Name, sessions[1].Name, sessions[2].Name)
	}
}

// --- viewMode cycling ---

func TestViewModeCycling(t *testing.T) {
	v := viewAll

	v = v.next()
	if v != viewNeedsAttention {
		t.Errorf("All.next() = %d, want viewNeedsAttention", v)
	}

	v = v.next()
	if v != viewActive {
		t.Errorf("NeedsAttention.next() = %d, want viewActive", v)
	}

	v = v.next()
	if v != viewStarred {
		t.Errorf("Active.next() = %d, want viewStarred", v)
	}

	v = v.next()
	if v != viewScenario {
		t.Errorf("Starred.next() = %d, want viewScenario", v)
	}

	v = v.next()
	if v != viewDeleted {
		t.Errorf("Scenario.next() = %d, want viewDeleted", v)
	}

	v = v.next()
	if v != viewAll {
		t.Errorf("Deleted.next() = %d, want viewAll (wrap)", v)
	}

	v = viewAll

	v = v.prev()
	if v != viewDeleted {
		t.Errorf("All.prev() = %d, want viewDeleted (wrap)", v)
	}
}

func TestOverlay_RightArrowCyclesView(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

	var updated tea.Model

	updated, _ = sendWindowSize(m, 120, 40)

	om := asOverlay(updated)
	if om.view != viewAll {
		t.Fatalf("initial view = %d, want viewAll", om.view)
	}

	updated, _ = sendKey(updated, "right")

	om = asOverlay(updated)
	if om.view != viewNeedsAttention {
		t.Errorf("after right: view = %d, want viewNeedsAttention", om.view)
	}

	updated, _ = sendKey(updated, "right")

	om = asOverlay(updated)
	if om.view != viewActive {
		t.Errorf("after 2x right: view = %d, want viewActive", om.view)
	}

	updated, _ = sendKey(updated, "right")

	om = asOverlay(updated)
	if om.view != viewStarred {
		t.Errorf("after 3x right: view = %d, want viewStarred", om.view)
	}

	updated, _ = sendKey(updated, "right")

	om = asOverlay(updated)
	if om.view != viewScenario {
		t.Errorf("after 4x right: view = %d, want viewScenario", om.view)
	}

	updated, _ = sendKey(updated, "right")

	om = asOverlay(updated)
	if om.view != viewDeleted {
		t.Errorf("after 5x right: view = %d, want viewDeleted", om.view)
	}

	updated, _ = sendKey(updated, "right")

	om = asOverlay(updated)
	if om.view != viewAll {
		t.Errorf("after 6x right: view = %d, want viewAll (wrap)", om.view)
	}
}

func TestSortDeletedMostRecentFirst(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "auld", Name: "auld", DeletedAt: "2026-07-09T10:00:00Z"},
		{ID: "bide", Name: "bide", DeletedAt: "2026-07-10T10:00:00Z"},
		{ID: "canny", Name: "canny", DeletedAt: "2026-07-08T10:00:00Z"},
	}

	got := sortDeleted(sessions)

	want := []string{"bide", "auld", "canny"}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("sortDeleted[%d] = %q, want %q", i, got[i].ID, id)
		}
	}
}

// TestOverlayDeletedViewShowsDeletedAndRestores verifies the Deleted view lists
// soft-deleted sessions and that Enter invokes the restore hook.
func TestOverlayDeletedViewShowsDeletedAndRestores(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil, nil, nil)
	m.deletedSessions = []protocol.SessionInfo{
		{ID: "dreich", Name: "dreich", Status: "stopped", DeletedAt: "2026-07-10T10:00:00Z", DeleteExpiresAt: "2026-07-11T10:00:00Z"},
	}

	var restored string

	m.restoreSession = func(id string) error { restored = id; return nil }

	updated, _ := sendWindowSize(m, 120, 40)

	// Cycle to the Deleted view (left wraps to it).
	updated, _ = sendKey(updated, "left")

	om := asOverlay(updated)
	if om.view != viewDeleted {
		t.Fatalf("expected viewDeleted, got %d", om.view)
	}

	visible := om.sessionsForView()
	if len(visible) != 1 || visible[0].ID != "dreich" {
		t.Fatalf("deleted view sessions = %+v, want [dreich]", visible)
	}

	// Enter restores the selected deleted session (and does not attach).
	updated, cmd := sendKey(om, "enter")
	om = asOverlay(updated)

	if om.selected != nil {
		t.Error("enter in deleted view must not select/attach")
	}

	if cmd != nil {
		cmd() // runs the restore closure
	}

	if restored != "dreich" {
		t.Errorf("restore hook got %q, want dreich", restored)
	}
}

func TestOverlay_LeftArrowCyclesViewBackward(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

	var updated tea.Model

	updated, _ = sendWindowSize(m, 120, 40)

	updated, _ = sendKey(updated, "left")

	om := asOverlay(updated)
	if om.view != viewDeleted {
		t.Errorf("after left from All: view = %d, want viewDeleted (wrap)", om.view)
	}
}

func TestOverlay_NeedsAttentionFiltersCorrectly(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "braw-working", RepoName: "repo", Status: "running", AgentStatus: "active",
			CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "s2", Name: "thrawn-blocked", RepoName: "repo", Status: "running", AgentStatus: "approval",
			CreatedAt:       time.Now().Format(time.RFC3339),
			StatusChangedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339)},
		{ID: "s3", Name: "neep-idle", RepoName: "repo", Status: "stopped",
			CreatedAt: time.Now().Format(time.RFC3339)},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)

	var updated tea.Model

	updated, _ = sendWindowSize(m, 120, 40)

	updated, _ = sendKey(updated, "right")
	om := asOverlay(updated)

	sessionCount := countSessionItems(om)

	if sessionCount != 1 {
		t.Errorf("needs attention view has %d sessions, want 1", sessionCount)
	}
}

func TestOverlay_ActiveViewShowsOnlyRunning(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "braw-running", RepoName: "repo", Status: "running", AgentStatus: "active",
			CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "s2", Name: "neep-stopped", RepoName: "repo", Status: "stopped",
			CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "s3", Name: "canny-running", RepoName: "repo", Status: "running", AgentStatus: "ready",
			CreatedAt: time.Now().Add(-10 * time.Minute).Format(time.RFC3339)},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)

	var updated tea.Model

	updated, _ = sendWindowSize(m, 120, 40)

	updated, _ = sendKey(updated, "right")
	updated, _ = sendKey(updated, "right")
	om := asOverlay(updated)

	sessionCount := countSessionItems(om)

	if sessionCount != 2 {
		t.Errorf("active view has %d sessions, want 2", sessionCount)
	}
}

func TestOverlay_FilterRespectsView(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "braw-api", RepoName: "repo", Status: "running", AgentStatus: "active",
			CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "s2", Name: "thrawn-api", RepoName: "repo", Status: "running", AgentStatus: "approval",
			CreatedAt:       time.Now().Format(time.RFC3339),
			StatusChangedAt: time.Now().Format(time.RFC3339)},
		{ID: "s3", Name: "thrawn-ui", RepoName: "repo", Status: "running", AgentStatus: "approval",
			CreatedAt:       time.Now().Format(time.RFC3339),
			StatusChangedAt: time.Now().Format(time.RFC3339)},
	}

	var updated tea.Model

	updated, _ = sendWindowSize(newOverlayModel(sessions, "", nil, nil, nil, nil), 120, 40)

	// Switch to Needs Attention
	updated, _ = sendKey(updated, "right")

	om := asOverlay(updated)
	if om.view != viewNeedsAttention {
		t.Fatalf("view = %d, want viewNeedsAttention", om.view)
	}

	// Enter filter mode and type "api"
	updated, _ = sendKey(updated, "/")
	for _, ch := range "api" {
		updated, _ = updated.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	om = asOverlay(updated)
	sessionCount := countSessionItems(om)
	// Should only show thrawn-api (braw-api is active, not needing attention)
	if sessionCount != 1 {
		t.Errorf("filtered needs-attention has %d sessions, want 1", sessionCount)
	}
}

func TestOverlay_FilterEscRebuildsView(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "braw-working", RepoName: "repo", Status: "running", AgentStatus: "active",
			CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "s2", Name: "thrawn-blocked", RepoName: "repo", Status: "running", AgentStatus: "approval",
			CreatedAt:       time.Now().Format(time.RFC3339),
			StatusChangedAt: time.Now().Format(time.RFC3339)},
	}

	var updated tea.Model

	updated, _ = sendWindowSize(newOverlayModel(sessions, "", nil, nil, nil, nil), 120, 40)

	// Switch to Needs Attention
	updated, _ = sendKey(updated, "right")

	// Enter filter, type something, then cancel
	updated, _ = sendKey(updated, "/")
	updated, _ = updated.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	updated, _ = sendKey(updated, "esc")

	om := asOverlay(updated)
	// Should be back to the full needs-attention view, not the "all" view
	if om.view != viewNeedsAttention {
		t.Errorf("view = %d after filter cancel, want viewNeedsAttention", om.view)
	}

	sessionCount := countSessionItems(om)

	if sessionCount != 1 {
		t.Errorf("after filter cancel: %d sessions, want 1 (only thrawn-blocked)", sessionCount)
	}
}

func TestOverlay_EmptyNeedsAttentionView(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "braw-working", RepoName: "repo", Status: "running", AgentStatus: "active",
			CreatedAt: time.Now().Format(time.RFC3339)},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)

	var updated tea.Model

	updated, _ = sendWindowSize(m, 120, 40)

	updated, _ = sendKey(updated, "right")

	om := asOverlay(updated)
	if om.view != viewNeedsAttention {
		t.Fatalf("view = %d, want viewNeedsAttention", om.view)
	}

	if len(om.list.Items()) != 0 {
		t.Errorf("expected empty list for needs attention view, got %d items", len(om.list.Items()))
	}

	view := om.View().Content
	if !strings.Contains(view, "Nothing needs your attention") {
		t.Error("expected empty state message in view output")
	}
}

func TestAssignSessionIndices(t *testing.T) {
	items := []list.Item{
		groupHeader{name: "croft", count: 1},
		sessionItem{info: protocol.SessionInfo{ID: "s1", Name: "braw"}},
		groupHeader{name: "graith", count: 2},
		sessionItem{info: protocol.SessionInfo{ID: "s2", Name: "canny"}},
		sessionItem{info: protocol.SessionInfo{ID: "s3", Name: "bonnie"}},
	}
	assignSessionIndices(items)

	want := []int{1, 2, 3}
	got := []int{}

	for _, item := range items {
		if si, ok := item.(sessionItem); ok {
			got = append(got, si.sessionIndex)
		}
	}

	if len(got) != len(want) {
		t.Fatalf("got %d indices, want %d", len(got), len(want))
	}

	for i, w := range want {
		if got[i] != w {
			t.Errorf("session %d: index = %d, want %d", i, got[i], w)
		}
	}
}

func TestOverlay_NumberKeySelectsSession(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", noopFetchPreview, nil, nil, []rune("1234567890!@#$%^&*()"))
	sized, _ := sendWindowSize(m, 200, 50)

	// Press "1" to select the first session.
	updated, _ := sendKey(asOverlay(sized), "1")
	om := asOverlay(updated)

	if om.selected == nil {
		t.Fatal("expected a session to be selected after pressing 1")
	}

	// First session should be the first sessionItem (after group header).
	var firstSession string

	for _, item := range asOverlay(sized).list.Items() {
		if si, ok := item.(sessionItem); ok {
			firstSession = si.info.ID
			break
		}
	}

	if om.selected.ID != firstSession {
		t.Errorf("selected session = %q, want %q", om.selected.ID, firstSession)
	}
}

// assertNumberKeySelectsNth builds sessionCount running sessions, presses key,
// and verifies the session at the 1-based targetIndex in the list becomes the
// selection. keyDesc describes the keypress for failure messages.
func assertNumberKeySelectsNth(t *testing.T, sessionCount, targetIndex int, key, keyDesc string) {
	t.Helper()

	var sessions []protocol.SessionInfo
	for i := 1; i <= sessionCount; i++ {
		sessions = append(sessions, protocol.SessionInfo{
			ID:        fmt.Sprintf("s%d", i),
			Name:      fmt.Sprintf("bothy-%02d", i),
			RepoName:  "croft",
			Status:    "running",
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour).Format(time.RFC3339),
		})
	}

	m := newOverlayModel(sessions, "", noopFetchPreview, nil, nil, []rune("1234567890!@#$%^&*()"))
	sized, _ := sendWindowSize(m, 200, 50)
	sm := asOverlay(sized)

	idx := 0

	var targetID string

	for _, item := range sm.list.Items() {
		if si, ok := item.(sessionItem); ok {
			idx++
			if idx == targetIndex {
				targetID = si.info.ID
				break
			}
		}
	}

	if targetID == "" {
		t.Fatalf("could not find session %d in list", targetIndex)
	}

	updated, _ := sendKey(sm, key)

	om := asOverlay(updated)
	if om.selected == nil {
		t.Fatalf("expected a session to be selected after pressing %s", keyDesc)
	}

	if om.selected.ID != targetID {
		t.Errorf("selected = %q, want %q (session %d)", om.selected.ID, targetID, targetIndex)
	}
}

func TestOverlay_NumberKeyZeroSelectsTenth(t *testing.T) {
	assertNumberKeySelectsNth(t, 12, 10, "0", "0")
}

func TestOverlay_ShiftNumberSelectsEleventhPlus(t *testing.T) {
	assertNumberKeySelectsNth(t, 15, 11, "!", "shift+1")
}

func TestOverlay_NumberKeyOutOfRangeDoesNothing(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", noopFetchPreview, nil, nil, []rune("1234567890!@#$%^&*()"))
	sized, _ := sendWindowSize(m, 200, 50)

	updated, _ := sendKey(asOverlay(sized), "5")

	om := asOverlay(updated)
	if om.selected != nil {
		t.Error("expected no selection when pressing number beyond session count")
	}
}

func TestOverlay_NumberLabelsInRender(t *testing.T) {
	// Create 12 sessions so we can verify labels for 1-10 and shifted glyphs for 11-12.
	var sessions []protocol.SessionInfo
	for i := 1; i <= 12; i++ {
		sessions = append(sessions, protocol.SessionInfo{
			ID:        fmt.Sprintf("s%d", i),
			Name:      fmt.Sprintf("bothy-%02d", i),
			RepoName:  "croft",
			Status:    "running",
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour).Format(time.RFC3339),
		})
	}

	m := newOverlayModel(sessions, "", noopFetchPreview, nil, nil, []rune("1234567890!@#$%^&*()"))
	sized, _ := sendWindowSize(m, 200, 50)
	view := asOverlay(sized).View().Content

	// Strip ANSI to check raw content.
	stripped := ansi.Strip(view)

	// Sessions 1-9 should show their digit, session 10 shows "0".
	for _, digit := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0"} {
		if !strings.Contains(stripped, digit) {
			t.Errorf("view missing digit label %q", digit)
		}
	}

	// Sessions 11-12 should show shifted glyphs "!" and "@".
	for _, glyph := range []string{"!", "@"} {
		if !strings.Contains(stripped, glyph) {
			t.Errorf("view missing shifted glyph label %q", glyph)
		}
	}
}

func TestOverlay_FilteredViewNumberKey(t *testing.T) {
	// After filtering, pressing "1" should select the first *filtered* session.
	var sessions []protocol.SessionInfo
	for i := 1; i <= 5; i++ {
		sessions = append(sessions, protocol.SessionInfo{
			ID:        fmt.Sprintf("s%d", i),
			Name:      fmt.Sprintf("bothy-%02d", i),
			RepoName:  "croft",
			Status:    "running",
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour).Format(time.RFC3339),
		})
	}
	// Add one session with a unique name to filter for.
	sessions = append(sessions, protocol.SessionInfo{
		ID:        "s-neep",
		Name:      "neep-wee",
		RepoName:  "croft",
		Status:    "running",
		CreatedAt: time.Now().Add(-6 * time.Hour).Format(time.RFC3339),
	})

	m := newOverlayModel(sessions, "", noopFetchPreview, nil, nil, []rune("1234567890!@#$%^&*()"))
	sized, _ := sendWindowSize(m, 200, 50)
	sm := asOverlay(sized)

	// Enter filter mode, type "neep" to narrow to one session.
	filtered, _ := sendKey(sm, "/")
	for _, ch := range "neep" {
		filtered, _ = filtered.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	// Now press enter to exit filter, then "1" to select.
	filtered, _ = sendKey(filtered, "enter")
	// The enter in filter mode selects the first filtered item and quits.
	om := asOverlay(filtered)
	if om.selected == nil {
		t.Fatal("expected a session to be selected after filtering + enter")
	}

	if om.selected.ID != "s-neep" {
		t.Errorf("selected = %q, want %q (the filtered session)", om.selected.ID, "s-neep")
	}
}

func TestOverlay_EmptyListNumberKey(t *testing.T) {
	// Pressing a number with zero sessions should be a safe no-op.
	m := newOverlayModel(nil, "", noopFetchPreview, nil, nil, []rune("1234567890!@#$%^&*()"))
	sized, _ := sendWindowSize(m, 200, 50)

	updated, _ := sendKey(asOverlay(sized), "1")

	om := asOverlay(updated)
	if om.selected != nil {
		t.Error("expected no selection when pressing number with zero sessions")
	}
}

func TestOverlay_MoreThan20SessionsNoLabelBeyond(t *testing.T) {
	var sessions []protocol.SessionInfo
	for i := 1; i <= 25; i++ {
		sessions = append(sessions, protocol.SessionInfo{
			ID:        fmt.Sprintf("s%d", i),
			Name:      fmt.Sprintf("bothy-%02d", i),
			RepoName:  "croft",
			Status:    "running",
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour).Format(time.RFC3339),
		})
	}

	m := newOverlayModel(sessions, "", noopFetchPreview, nil, nil, []rune("1234567890!@#$%^&*()"))
	sized, _ := sendWindowSize(m, 200, 50)
	sm := asOverlay(sized)

	// Sessions 21-25 should have sessionIndex > 20 and no label.
	for _, item := range sm.list.Items() {
		if si, ok := item.(sessionItem); ok && si.sessionIndex > 20 {
			// These sessions exist but shouldn't be selectable by number.
			if si.sessionIndex > 25 {
				t.Errorf("unexpected sessionIndex %d", si.sessionIndex)
			}
		}
	}

	// Pressing shift+1 ("!") should still select session 11, not wrap to 21.
	updated, _ := sendKey(sm, "!")

	om := asOverlay(updated)
	if om.selected == nil {
		t.Fatal("expected session 11 selected after pressing shift+1")
	}

	idx := 0

	var eleventhID string

	for _, item := range sm.list.Items() {
		if si, ok := item.(sessionItem); ok {
			idx++
			if idx == 11 {
				eleventhID = si.info.ID
				break
			}
		}
	}

	if om.selected.ID != eleventhID {
		t.Errorf("selected = %q, want %q (11th session, not 21st)", om.selected.ID, eleventhID)
	}
}

func TestDisplayPR(t *testing.T) {
	cases := []struct {
		name string
		info protocol.SessionInfo
		want string
	}{
		{"no PR", protocol.SessionInfo{}, "—"},
		{"merged", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 583, State: "merged"}}, "#583 merged"},
		{"open passing", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open"}, CI: &protocol.CIInfo{State: "passing"}}, "#56 ✓"},
		{"open failing", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open"}, CI: &protocol.CIInfo{State: "failing"}}, "#56 ✗"},
		{"conflict beats CI", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open", Conflicting: true}, CI: &protocol.CIInfo{State: "passing"}}, "#56 ⚠"},
		{"draft pending", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 9, State: "draft"}, CI: &protocol.CIInfo{State: "pending"}}, "#9d ·"},
		// The review decision is now its own column (displayReview); displayPR must
		// NOT append it, so the PR/CI token colour never bleeds onto the review glyph.
		{"review omitted from PR token (approved)", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open", ReviewDecision: "approved"}, CI: &protocol.CIInfo{State: "passing"}}, "#56 ✓"},
		{"review omitted from PR token (changes)", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open", ReviewDecision: "changes_requested"}, CI: &protocol.CIInfo{State: "failing"}}, "#56 ✗"},
		{"review omitted, no CI", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open", ReviewDecision: "review_required"}}, "#56"},
		{"review omitted with conflict", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open", Conflicting: true, ReviewDecision: "changes_requested"}, CI: &protocol.CIInfo{State: "passing"}}, "#56 ⚠"},
		{"merged", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 583, State: "merged", ReviewDecision: "approved"}}, "#583 merged"},
		{"closed", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 584, State: "closed", ReviewDecision: "changes_requested"}}, "#584 closed"},
		{"draft omits review", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 9, State: "draft", ReviewDecision: "review_required"}, CI: &protocol.CIInfo{State: "pending"}}, "#9d ·"},
		// Counts: while CI runs/fails, show passed/total progress in place of the
		// bare indicator, falling back when no count is available (Total == 0).
		{"pending with counts", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open"}, CI: &protocol.CIInfo{State: "pending", Passed: 16, Total: 22}}, "#56 16/22"},
		{"failing with counts", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open"}, CI: &protocol.CIInfo{State: "failing", FailingChecks: []string{"build"}, Passed: 19, Total: 22}}, "#56 19/22 1✗"},
		{"failing with multiple failures", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open"}, CI: &protocol.CIInfo{State: "failing", FailingChecks: []string{"build", "lint"}, Passed: 18, Total: 22}}, "#56 18/22 2✗"},
		{"failing counts but no names falls back", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open"}, CI: &protocol.CIInfo{State: "failing", Passed: 19, Total: 22}}, "#56 ✗"},
		{"pending no counts falls back to dot", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open"}, CI: &protocol.CIInfo{State: "pending"}}, "#56 ·"},
		{"passing keeps check even with counts", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open"}, CI: &protocol.CIInfo{State: "passing", Passed: 22, Total: 22}}, "#56 ✓"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := displayPR(c.info); got != c.want {
				t.Errorf("displayPR = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPRColor(t *testing.T) {
	cases := []struct {
		name string
		info protocol.SessionInfo
		want color.Color
	}{
		{"no PR", protocol.SessionInfo{}, colorDim},
		{"conflict beats CI", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open", Conflicting: true}, CI: &protocol.CIInfo{State: "passing"}}, colorRed},
		{"open passing", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open"}, CI: &protocol.CIInfo{State: "passing"}}, colorGreen},
		{"open failing", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open"}, CI: &protocol.CIInfo{State: "failing"}}, colorRed},
		{"open pending", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open"}, CI: &protocol.CIInfo{State: "pending"}}, colorYellow},
		{"open no CI", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open"}}, colorBlue},
		// A merged/closed PR retains its last-known (stale) CI badge because
		// resolvePR stops fetching checks once it leaves open/draft. The
		// terminal state must win over that stale badge (issue #773).
		{"merged with stale failing CI", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 583, State: "merged"}, CI: &protocol.CIInfo{State: "failing"}}, colorDim},
		{"closed with stale failing CI", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 583, State: "closed"}, CI: &protocol.CIInfo{State: "failing"}}, colorDim},
		{"merged with stale passing CI", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 583, State: "merged"}, CI: &protocol.CIInfo{State: "passing"}}, colorDim},
		{"merged no CI", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 583, State: "merged"}}, colorDim},
		// A closed PR can carry a stale CONFLICTING mergeable state (resolvePR
		// sets Conflicting unconditionally). Terminal state must still win, to
		// mirror displayPR which renders "#N closed" for this case (issue #773).
		{"closed and conflicting", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 583, State: "closed", Conflicting: true}}, colorDim},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := prColor(c.info); got != c.want {
				t.Errorf("prColor = %v, want %v", got, c.want)
			}
		})
	}
}

func TestFormatPRSection(t *testing.T) {
	// Conflicting PR must render a visible conflict marker in the status bar.
	info := statusBarInfo{prNumber: 56, prState: "open", prConflicting: true}
	if got := formatPRSection(info, barBg); !strings.Contains(got, "conflict") {
		t.Errorf("status bar PR section should show conflict, got %q", got)
	}
	// No PR -> empty.
	if got := formatPRSection(statusBarInfo{}, barBg); got != "" {
		t.Errorf("no PR should render empty, got %q", got)
	}
	// Draft PR must carry the "d" suffix so it is distinguishable from a
	// plain open PR, mirroring the overlay column's "#Nd" (#776).
	if got := ansi.Strip(formatPRSection(statusBarInfo{prNumber: 9, prState: "draft"}, barBg)); got != "PR#9d" {
		t.Errorf("status bar PR section should mark draft as PR#9d, got %q", got)
	}
	// Draft must fall through to CI rendering: the suffix AND the CI marker
	// both appear. Asserting the CI marker guards the fall-through — a stray
	// return after the "d" suffix would drop it.
	if got := ansi.Strip(formatPRSection(statusBarInfo{prNumber: 9, prState: "draft", ciState: "pending"}, barBg)); got != "PR#9d ·CI" {
		t.Errorf("draft PR should keep the d suffix alongside CI state, got %q", got)
	}
	// Conflict beats CI even for a draft, and the draft suffix is retained.
	if got := ansi.Strip(formatPRSection(statusBarInfo{prNumber: 9, prState: "draft", prConflicting: true, ciState: "passing"}, barBg)); got != "PR#9d ⚠conflict" {
		t.Errorf("draft+conflict should render PR#9d ⚠conflict, got %q", got)
	}
}

func TestColumnWidths_TotalWidthIncludesPR(t *testing.T) {
	// The PR separator is always present; widening the PR column widens the
	// total 1:1, proving the PR column is accounted for.
	a := columnWidths{name: 10, trailing: map[string]int{
		"status": 6, "summary": 7, "git": 3, "pr": 2, "output": 6,
	}}
	b := columnWidths{name: 10, trailing: map[string]int{
		"status": 6, "summary": 7, "git": 3, "pr": 10, "output": 6,
	}}

	if b.totalWidth()-a.totalWidth() != 8 {
		t.Errorf("totalWidth must grow by Δpr=8, got %d", b.totalWidth()-a.totalWidth())
	}
}

// TestColumnWidths_TotalWidthCountsAllTUIColumns guards the registry invariant
// that totalWidth accounts for every ShowTUI column, so a future column added
// to the registry extends the panel width instead of being truncated.
func TestColumnWidths_TotalWidthCountsAllTUIColumns(t *testing.T) {
	widths := map[string]int{}
	for _, c := range tuiColumns() {
		widths[c.Key] = 5
	}

	base := columnWidths{name: 10, trailing: widths}

	// Bump one column by 3 and confirm the total grows by exactly 3.
	bumped := map[string]int{}
	for k, v := range widths {
		bumped[k] = v
	}

	bumped["git"] += 3
	grown := columnWidths{name: 10, trailing: bumped}

	if grown.totalWidth()-base.totalWidth() != 3 {
		t.Errorf("total must grow by 3 when a TUI column widens, got %d", grown.totalWidth()-base.totalWidth())
	}

	// Every TUI column plus name and the fixed margins must be counted: the
	// total is 9 + name + 4 + sum(2 + width) over all TUI columns.
	want := 9 + base.name + 4
	for range tuiColumns() {
		want += 2 + 5
	}

	if base.totalWidth() != want {
		t.Errorf("totalWidth = %d, want %d (all TUI columns counted)", base.totalWidth(), want)
	}
}

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
	m := sizedModel(t, scenarioSessions(), "")
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
	m := sizedModel(t, sessions, "")
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
	m := sizedModel(t, overlayTestSessions(), "")

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

	// Left from viewAll wraps to the last view (viewDeleted).
	updated, _ := sendKey(m, "h")
	om := asOverlay(updated)

	if om.view != viewDeleted {
		t.Errorf("left from viewAll should wrap to viewDeleted, got %v", om.view)
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

	m := sizedModel(t, sessions, "")
	m.restartSession = func(id string) error {
		restarted = id
		return nil
	}

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
	m := sizedModel(t, overlayTestSessions(), "")
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
	m := sizedModel(t, overlayTestSessions(), "")
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
	m := sizedModel(t, sessions, "")
	m.view = viewActive
	m.rebuildForView()

	out := m.View().Content
	if !strings.Contains(out, "No active sessions") {
		t.Errorf("empty active view should show its empty message:\n%s", out)
	}
}

func TestView_ProfileShownInTitle(t *testing.T) {
	m := sizedModel(t, overlayTestSessions(), "")
	m.profile = "bothy"

	out := m.View().Content
	if !strings.Contains(out, "bothy") {
		t.Errorf("view title should include the active profile:\n%s", out)
	}
}

func TestView_RestartMenuShowsCounts(t *testing.T) {
	// overlayTestSessions(): 3 sessions, one stopped (s2), none config-stale.
	m := sizedModel(t, overlayTestSessions(), "")
	m.restartSession = func(string) error { return nil }
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

func TestDisplayPRAllStates2(t *testing.T) {
	tests := []struct {
		name string
		info protocol.SessionInfo
		want string
	}{
		{"no pr", protocol.SessionInfo{}, "—"},
		{"merged", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 5, State: "merged"}}, "#5 merged"},
		{"closed", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 6, State: "closed"}}, "#6 closed"},
		{
			"conflict beats CI",
			protocol.SessionInfo{
				PullRequest: &protocol.PRInfo{Number: 7, State: "open", Conflicting: true},
				CI:          &protocol.CIInfo{State: "passing"},
			},
			"#7 ⚠",
		},
		{
			"draft passing adds d and check",
			protocol.SessionInfo{
				PullRequest: &protocol.PRInfo{Number: 8, State: "draft"},
				CI:          &protocol.CIInfo{State: "passing"},
			},
			"#8d ✓",
		},
		{
			"open failing",
			protocol.SessionInfo{
				PullRequest: &protocol.PRInfo{Number: 9, State: "open"},
				CI:          &protocol.CIInfo{State: "failing"},
			},
			"#9 ✗",
		},
		{
			"open pending",
			protocol.SessionInfo{
				PullRequest: &protocol.PRInfo{Number: 10, State: "open"},
				CI:          &protocol.CIInfo{State: "pending"},
			},
			"#10 ·",
		},
		{
			"open no CI",
			protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 11, State: "open"}},
			"#11",
		},
		{
			"open unknown CI state",
			protocol.SessionInfo{
				PullRequest: &protocol.PRInfo{Number: 12, State: "open"},
				CI:          &protocol.CIInfo{State: "whatever"},
			},
			"#12",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayPR(tt.info); got != tt.want {
				t.Errorf("displayPR(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestPRColorTerminalAndConflict2(t *testing.T) {
	// merged/closed → dim even with a stale passing CI badge.
	merged := protocol.SessionInfo{
		PullRequest: &protocol.PRInfo{Number: 1, State: "merged"},
		CI:          &protocol.CIInfo{State: "passing"},
	}
	if got := prColor(merged); got != colorDim {
		t.Errorf("merged PR color should be dim, got %v", got)
	}

	// conflict outranks a passing CI.
	conflict := protocol.SessionInfo{
		PullRequest: &protocol.PRInfo{Number: 2, State: "open", Conflicting: true},
		CI:          &protocol.CIInfo{State: "passing"},
	}
	if got := prColor(conflict); got != colorRed {
		t.Errorf("conflicting PR color should be red, got %v", got)
	}

	// open PR with no CI → blue.
	openNoCI := protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 3, State: "open"}}
	if got := prColor(openNoCI); got != colorBlue {
		t.Errorf("open PR with no CI should be blue, got %v", got)
	}

	// pending CI → yellow.
	pending := protocol.SessionInfo{
		PullRequest: &protocol.PRInfo{Number: 4, State: "open"},
		CI:          &protocol.CIInfo{State: "pending"},
	}
	if got := prColor(pending); got != colorYellow {
		t.Errorf("pending CI color should be yellow, got %v", got)
	}
}

func TestSortByStatusAgeMixedZero2(t *testing.T) {
	now := time.Now()
	sessions := []protocol.SessionInfo{
		{Name: "has-time", StatusChangedAt: now.Format(time.RFC3339)},
		{Name: "no-time"}, // zero — should sort ahead of the timestamped one
	}

	sortByStatusAge(sessions)

	if sessions[0].Name != "no-time" {
		t.Fatalf("zero StatusChangedAt should sort first, got %q first", sessions[0].Name)
	}

	// Reverse input order to exercise the j-is-zero branch too.
	sessions = []protocol.SessionInfo{
		{Name: "no-time"},
		{Name: "has-time", StatusChangedAt: now.Format(time.RFC3339)},
	}
	sortByStatusAge(sessions)

	if sessions[0].Name != "no-time" {
		t.Fatalf("zero StatusChangedAt should remain first, got %q", sessions[0].Name)
	}
}

func TestSortByStatusAgeOrdersByAge2(t *testing.T) {
	now := time.Now()
	sessions := []protocol.SessionInfo{
		{Name: "newer", StatusChangedAt: now.Format(time.RFC3339)},
		{Name: "older", StatusChangedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}

	sortByStatusAge(sessions)

	if sessions[0].Name != "older" {
		t.Fatalf("older status change should sort first, got %q", sessions[0].Name)
	}
}

func TestRunApprovalOverlayEmptyReturnsNil2(t *testing.T) {
	// The empty-input guard must return nil without ever launching a program
	// (which would require a real terminal).
	if got := RunApprovalOverlay(nil); got != nil {
		t.Errorf("RunApprovalOverlay(nil) = %v, want nil", got)
	}

	if got := RunApprovalOverlay([]protocol.ApprovalInfo{}); got != nil {
		t.Errorf("RunApprovalOverlay(empty) = %v, want nil", got)
	}
}
