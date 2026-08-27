package client

import (
	"errors"
	"fmt"
	"image/color"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/sessionlabel"
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

func sendF1(m tea.Model) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyPressMsg{Code: tea.KeyF1})
}

func sendWindowSize(m tea.Model, w, h int) (tea.Model, tea.Cmd) {
	return m.Update(tea.WindowSizeMsg{Width: w, Height: h})
}

func asOverlay(m tea.Model) *overlayModel {
	return m.(*overlayModel)
}

func columnOfText(t *testing.T, content, needle string) int {
	t.Helper()

	for _, line := range strings.Split(ansi.Strip(content), "\n") {
		if col := strings.Index(line, needle); col >= 0 {
			return col
		}
	}

	t.Fatalf("content missing %q:\n%s", needle, ansi.Strip(content))

	return -1
}

func rowOfText(t *testing.T, content, needle string) int {
	t.Helper()

	for i, line := range strings.Split(ansi.Strip(content), "\n") {
		if strings.Contains(line, needle) {
			return i
		}
	}

	t.Fatalf("content missing %q:\n%s", needle, ansi.Strip(content))

	return -1
}

// sizedModel builds an overlay model with the common nil callbacks and the
// standard 120x40 dimensions used across most overlay tests.
func sizedModel(t *testing.T, sessions []protocol.SessionInfo, current string) *overlayModel {
	t.Helper()

	m := newOverlayModel(sessions, current, nil, nil, nil, nil)
	m.width = 120
	m.height = 40

	return m
}

// countSessionItems returns the number of sessionItem entries in the model's
// list (excluding group headers and other item types).
func countSessionItems(m *overlayModel) int {
	count := 0

	for _, item := range m.list.Items() {
		if _, ok := item.(sessionItem); ok {
			count++
		}
	}

	return count
}

func sessionItemsForGroup(t *testing.T, items []list.Item, headerPrefix string) []sessionItem {
	t.Helper()

	var result []sessionItem

	inGroup := false

	for _, item := range items {
		switch item := item.(type) {
		case groupHeader:
			if inGroup {
				return result
			}

			inGroup = strings.HasPrefix(item.name, headerPrefix)
		case sessionItem:
			if inGroup {
				result = append(result, item)
			}
		}
	}

	if !inGroup {
		t.Fatalf("group %q not found", headerPrefix)
	}

	return result
}

func sessionItemsByID(items []list.Item) map[string]sessionItem {
	result := make(map[string]sessionItem)

	for _, item := range items {
		if item, ok := item.(sessionItem); ok {
			result[item.info.ID] = item
		}
	}

	return result
}

func requireSelectedSessionID(t *testing.T, m *overlayModel, want string) {
	t.Helper()

	item, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		t.Fatalf("selected item = %T, want sessionItem", m.list.SelectedItem())
	}

	if item.info.ID != want {
		t.Fatalf("selected session = %q, want %q", item.info.ID, want)
	}
}

// renderItem builds a compactDelegate for the given sessions and renders the
// item at index into a string, using the standard 120x10 list dimensions.
func renderItem(sessions []protocol.SessionInfo, current string, index int) string {
	items := buildGroupedItems(sessions, nil)
	cols := computeColumnWidths(sessions, current)
	cols.name = maxSessionNameWidthFromItems(items, cols.name)
	cols.treeIndent = maxTreeIndentFromItems(items)
	cols.labels = maxCompactSessionLabelWidthFromItems(items)
	d := compactDelegate{cols: cols, currentSessionID: current}
	l := list.New(items, d, 120, 10)

	var buf strings.Builder

	d.Render(&buf, l, index, items[index])

	return buf.String()
}

func firstLineContaining(text, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}

	return ""
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

func TestBuildTreeItems_CycleOrderingStable(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "a", Name: "canny", ParentID: "b", Status: "running"},
		{ID: "b", Name: "braw", ParentID: "a", Status: "running"},
	}

	order := func(items []list.Item) string {
		var rows []string

		for _, item := range items {
			if item, ok := item.(sessionItem); ok {
				rows = append(rows, item.info.ID+":"+item.treePrefix)
			}
		}

		return strings.Join(rows, ",")
	}

	forward := order(buildTreeItems(sessions, nil))

	reversed := order(buildTreeItems([]protocol.SessionInfo{sessions[1], sessions[0]}, nil))
	if forward != reversed {
		t.Fatalf("cycle order changed with input order: %q vs %q", forward, reversed)
	}

	if forward != "b:,a:└── " {
		t.Fatalf("cycle order = %q, want stable name order with one connector", forward)
	}

	sameName := []protocol.SessionInfo{
		{ID: "a", Name: "braw", ParentID: "b", Status: "running"},
		{ID: "b", Name: "braw", ParentID: "a", Status: "running"},
	}
	forward = order(buildTreeItems(sameName, nil))

	reversed = order(buildTreeItems([]protocol.SessionInfo{sameName[1], sameName[0]}, nil))
	if forward != reversed || forward != "a:,b:└── " {
		t.Fatalf("same-name cycle order = %q vs %q, want ID-stable order", forward, reversed)
	}
}

func TestBuildTreeItems_CycleChildDoesNotAdvertiseAncestor(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "a", Name: "braw", ParentID: "b", Status: "running"},
		{ID: "b", Name: "canny", ParentID: "a", Status: "running"},
	}

	items := buildTreeItems(sessions, nil)
	root := items[0].(sessionItem)
	child := items[1].(sessionItem)

	if !root.hasChildren || root.descendantCount != 1 {
		t.Fatalf("cycle root = %+v, want one rendered descendant", root)
	}

	if child.hasChildren || child.descendantCount != 0 {
		t.Fatalf("cycle child = %+v, want ancestor edge excluded", child)
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

	// Cursor should be on the global-tree root.
	item := m.list.SelectedItem().(sessionItem)
	if item.info.ID != "root" {
		t.Fatalf("expected cursor on root, got %s", item.info.ID)
	}

	// Press space to collapse
	updated, _ := sendKey(m, " ")
	m = updated.(*overlayModel)

	if !m.collapsed["root"] {
		t.Fatal("root should be collapsed after space")
	}
	// The global All tree has no group header, so only the root remains.
	if len(m.list.Items()) != 1 {
		t.Errorf("expected 1 item after collapse, got %d", len(m.list.Items()))
	}

	// Press space again to expand
	updated, _ = sendKey(m, " ")
	m = updated.(*overlayModel)

	if m.collapsed["root"] {
		t.Fatal("root should be expanded after second space")
	}

	if len(m.list.Items()) != 3 {
		t.Errorf("expected 3 items after expand, got %d", len(m.list.Items()))
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
	m = updated.(*overlayModel)

	if len(m.list.Items()) != itemsBefore {
		t.Error("space on leaf should not change item count")
	}
}

func TestOverlay_CollapseAllExpandAll(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "root1", Name: "ben-one", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "c1", Name: "bairn-one", ParentID: "root1", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "gc1", Name: "wee-bairn", ParentID: "c1", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "root2", Name: "ben-two", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "c2", Name: "bairn-two", ParentID: "root2", RepoName: "repo", Status: "running", CreatedAt: now},
		{ID: "leaf", Name: "neep", RepoName: "repo", Status: "running", CreatedAt: now},
	}
	m := sizedModel(t, sessions, "")

	// Press C to collapse all parents
	updated, _ := sendKey(m, "C")
	m = updated.(*overlayModel)

	if !m.collapsed["root1"] || !m.collapsed["c1"] || !m.collapsed["root2"] {
		t.Fatal("all parents should be collapsed")
	}
	// The global All tree has no header: root1 + root2 + leaf = 3.
	if len(m.list.Items()) != 3 {
		t.Errorf("expected 3 items after collapse all, got %d", len(m.list.Items()))
	}

	// Press C again to expand all
	updated, _ = sendKey(m, "C")
	m = updated.(*overlayModel)

	if m.collapsed["root1"] || m.collapsed["c1"] || m.collapsed["root2"] {
		t.Fatal("all parents should be expanded")
	}

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
	if len(m.list.Items()) != 1 {
		t.Errorf("expected 1 item with pre-collapsed root, got %d", len(m.list.Items()))
	}

	si := m.list.Items()[0].(sessionItem)
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

func TestCompactSessionLabelTextLimitsAndTruncates(t *testing.T) {
	tests := map[string]struct {
		labels       []string
		wantContains []string
		wantAbsent   []string
	}{
		"limits labels and reports overflow": {
			labels:       []string{"strath", "bothy", "canny", "dreich"},
			wantContains: []string{"strath", "+3"},
			wantAbsent:   []string{"bothy", "canny", "dreich"},
		},
		"truncates long labels": {
			labels:       []string{"strath-label-for-dreich-weather", "bothy", "canny"},
			wantContains: []string{"…", "+2"},
			wantAbsent:   []string{"weather", "bothy", "canny"},
		},
		"ignores blank defensive labels": {
			labels:       []string{" ", "braw"},
			wantContains: []string{"braw"},
			wantAbsent:   []string{"+1"},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := compactSessionLabelText(test.labels)
			if width := labelChipSequenceWidth(compactSessionLabelChips(test.labels)); width > compactSessionLabelColumnMaxWidth {
				t.Fatalf("compact label chip width = %d, want <= %d: %q", width, compactSessionLabelColumnMaxWidth, got)
			}

			for _, want := range test.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("compact label text %q missing %q", got, want)
				}
			}

			for _, absent := range test.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("compact label text %q unexpectedly contains %q", got, absent)
				}
			}
		})
	}
}

func TestCompactSessionLabelTextTruncatesWideLabels(t *testing.T) {
	got := compactSessionLabelText([]string{"日本語のラベルはとても長い"})
	if got == "" {
		t.Fatal("compact label text should not be empty")
	}

	if width := labelChipSequenceWidth(compactSessionLabelChips([]string{"日本語のラベルはとても長い"})); width > compactSessionLabelColumnMaxWidth {
		t.Fatalf("wide compact label width = %d, want <= %d: %q", width, compactSessionLabelColumnMaxWidth, got)
	}

	if !strings.Contains(got, "…") {
		t.Fatalf("wide compact label should be elided, got %q", got)
	}
}

func TestGroupHeaderFilterValue(t *testing.T) {
	gh := groupHeader{name: "graith"}
	if gh.FilterValue() != "" {
		t.Errorf("groupHeader FilterValue() should be empty, got %q", gh.FilterValue())
	}
}

func TestBuildLabelGroupedItemsCrossesReposAndDuplicatesMultiLabelSessions(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "braw", Name: "braw", RepoName: "croft", Status: "running", Labels: []string{"Urgent", "release"}},
		{ID: "canny", Name: "canny", RepoName: "bothy", Status: "stopped", Labels: []string{"urgent"}},
		{ID: "dreich", Name: "dreich", RepoName: "glen", Status: "running"},
	}

	items := buildLabelGroupedItems(sessions, nil)
	groups := map[string][]string{}
	current := ""

	for _, item := range items {
		switch item := item.(type) {
		case groupHeader:
			current = item.name
		case sessionItem:
			groups[current] = append(groups[current], item.info.ID)
			if item.labelGroup != current {
				t.Fatalf("session label group = %q, want %q", item.labelGroup, current)
			}
		}
	}

	if got := groups["Urgent"]; len(got) != 2 || got[0] != "braw" || got[1] != "canny" {
		t.Fatalf("Urgent group = %v, want braw and canny across repos", got)
	}

	if got := groups["release"]; len(got) != 1 || got[0] != "braw" {
		t.Fatalf("release group = %v, want braw", got)
	}

	if got := groups["(no label)"]; len(got) != 1 || got[0] != "dreich" {
		t.Fatalf("(no label) group = %v, want dreich", got)
	}

	var groupNames []string

	for _, item := range items {
		if header, ok := item.(groupHeader); ok {
			groupNames = append(groupNames, header.name)
		}
	}

	if got, want := groupNames, []string{"release", "Urgent", "(no label)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group order = %v, want %v", got, want)
	}

	labelled := items[1].(sessionItem)
	if got := labelled.displayName(); got != "braw" {
		t.Fatalf("label view display name = %q, want session name", got)
	}

	if width := maxSessionNameWidthFromItems(items, 0); width < lipgloss.Width("braw") {
		t.Fatalf("label view name width = %d, want at least %d", width, lipgloss.Width("braw"))
	}
}

func TestBuildLabelGroupedItemsPreservesNestedCrossRepoTrees(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "croft", Status: "running", Labels: []string{"Urgent", "release"}},
		{ID: "child", Name: "bairn", ParentID: "root", RepoName: "bothy", Status: "running", Labels: []string{"urgent", "release"}},
		{ID: "grandchild", Name: "wee-bairn", ParentID: "child", RepoName: "glen", Status: "running", Labels: []string{"URGENT"}},
	}

	items := buildLabelGroupedItems(sessions, nil)

	urgent := sessionItemsForGroup(t, items, "Urgent")
	if len(urgent) != 3 {
		t.Fatalf("Urgent group has %d sessions, want 3", len(urgent))
	}

	wantIDs := []string{"root", "child", "grandchild"}

	wantPrefixes := []string{"", "└── ", "    └── "}
	for i := range urgent {
		if urgent[i].info.ID != wantIDs[i] || urgent[i].treePrefix != wantPrefixes[i] {
			t.Errorf("Urgent[%d] = %q prefix %q, want %q prefix %q", i, urgent[i].info.ID, urgent[i].treePrefix, wantIDs[i], wantPrefixes[i])
		}
	}

	if !urgent[0].hasChildren || urgent[0].descendantCount != 2 {
		t.Errorf("Urgent root children=%t descendants=%d, want true/2", urgent[0].hasChildren, urgent[0].descendantCount)
	}

	release := sessionItemsForGroup(t, items, "release")
	if len(release) != 2 || release[0].info.ID != "root" || release[1].info.ID != "child" || release[1].treePrefix != "└── " {
		t.Fatalf("release group = %+v, want root with child", release)
	}
}

func TestLabelViewCollapseRetainsGroupSelection(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "croft", Status: "running", Labels: []string{"alpha", "beta"}},
		{ID: "child", Name: "bairn", ParentID: "root", RepoName: "bothy", Status: "running", Labels: []string{"alpha", "beta"}},
	}

	m := sizedModel(t, sessions, "")
	m.view = viewLabels
	m.rebuildForView()
	m.selectSessionByIDAndLabel("root", "beta")

	updated, _ := sendKey(m, " ")

	m = asOverlay(updated)

	selected, ok := m.list.SelectedItem().(sessionItem)
	if !ok || selected.info.ID != "root" || selected.labelGroup != "beta" {
		t.Fatalf("selection after collapse = %+v, ok=%t; want root in beta", selected, ok)
	}

	if !selected.collapsed || selected.descendantCount != 1 {
		t.Errorf("collapsed root = %+v, want one hidden descendant", selected)
	}
}

func TestStarredViewPreservesVisibleTreeAndExcludedParentBecomesRoot(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "croft", Status: "running", Starred: true},
		{ID: "child", Name: "bairn", ParentID: "root", RepoName: "bothy", Status: "running", Starred: true},
		{ID: "grandchild", Name: "wee-bairn", ParentID: "child", RepoName: "glen", Status: "running", Starred: true},
		{ID: "hidden-parent", Name: "auld", RepoName: "croft", Status: "running"},
		{ID: "visible-orphan", Name: "thrawn", ParentID: "hidden-parent", RepoName: "bothy", Status: "running", Starred: true},
	}

	m := sizedModel(t, sessions, "")
	m.view = viewStarred
	m.rebuildForView()
	byID := sessionItemsByID(m.list.Items())

	if got := byID["child"].treePrefix; got != "└── " {
		t.Errorf("cross-repo child prefix = %q, want tree connector", got)
	}

	if got := byID["grandchild"].treePrefix; got != "    └── " {
		t.Errorf("grandchild prefix = %q, want nested connector", got)
	}

	if root := byID["root"]; !root.hasChildren || root.descendantCount != 2 {
		t.Errorf("starred root = %+v, want two descendants", root)
	}

	if got := byID["visible-orphan"].treePrefix; got != "" {
		t.Errorf("child of excluded parent prefix = %q, want root", got)
	}

	if _, ok := byID["hidden-parent"]; ok {
		t.Error("unstarred parent must not be rendered")
	}
}

func TestFilteredTreeViewsHandleOrphansAndCycles(t *testing.T) {
	base := []protocol.SessionInfo{
		{ID: "orphan", Name: "thrawn", ParentID: "missing", Status: "running", Starred: true, Labels: []string{"braw"}, ScenarioID: "strath", ScenarioName: "strath"},
		{ID: "a", Name: "canny", ParentID: "b", Status: "running", Starred: true, Labels: []string{"braw"}, ScenarioID: "strath", ScenarioName: "strath"},
		{ID: "b", Name: "dreich", ParentID: "a", Status: "running", Starred: true, Labels: []string{"braw"}, ScenarioID: "strath", ScenarioName: "strath"},
	}

	for _, tc := range []struct {
		name string
		view viewMode
	}{
		{name: "starred", view: viewStarred},
		{name: "labels", view: viewLabels},
		{name: "scenarios", view: viewScenario},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sizedModel(t, base, "")
			m.view = tc.view
			m.rebuildForView()

			byID := sessionItemsByID(m.list.Items())
			if len(byID) != 3 {
				t.Fatalf("rendered %d distinct sessions, want 3", len(byID))
			}

			if got := byID["orphan"].treePrefix; got != "" {
				t.Errorf("orphan prefix = %q, want root", got)
			}

			if byID["a"].treePrefix == "" && byID["b"].treePrefix == "" {
				t.Error("cycle was flattened; want one cycle member rendered below the other")
			}

			m.collapsed["a"] = true
			m.rebuildForView()

			if got := countSessionItems(m); got != 2 {
				t.Errorf("collapsed cycle rendered %d sessions, want orphan plus one cycle member", got)
			}
		})
	}
}

func TestFilteredTreeSearchDoesNotExpandBulkActionTargets(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "parent", Name: "ben", Status: "running", Starred: true},
		{ID: "child", Name: "bairn", ParentID: "parent", Status: "running", Starred: true},
		{ID: "other", Name: "dreich", Status: "running", Starred: true},
		{ID: "unstarred", Name: "bairn-hidden", Status: "running"},
	}

	m := sizedModel(t, sessions, "")
	m.view = viewStarred
	m.filterInput.SetValue("bairn")
	m.restartSession = func(string) error { return nil }
	m.rebuildForView()

	items := sessionItemsByID(m.list.Items())
	if len(items) != 1 || items["child"].treePrefix != "" {
		t.Fatalf("filtered items = %+v, want matching child promoted to root", items)
	}

	visible := m.visibleSessions()
	if len(visible) != 1 || visible[0].ID != "child" {
		t.Fatalf("visible sessions = %+v, want only matching starred child", visible)
	}

	updated, _ := sendKey(m, "R")
	updated, _ = sendKey(updated, "a")

	m = asOverlay(updated)
	if len(m.restartQueue) != 1 || m.restartQueue[0] != "child" {
		t.Fatalf("restart queue = %v, want only matching child", m.restartQueue)
	}
}

func TestFilteredTreeViewsCollapseAll(t *testing.T) {
	base := []protocol.SessionInfo{
		{ID: "root", Name: "ben", Status: "running", Starred: true, Labels: []string{"braw"}, ScenarioID: "strath", ScenarioName: "strath"},
		{ID: "child", Name: "bairn", ParentID: "root", Status: "running", Starred: true, Labels: []string{"braw"}, ScenarioID: "strath", ScenarioName: "strath"},
		{ID: "grandchild", Name: "wee-bairn", ParentID: "child", Status: "running", Starred: true, Labels: []string{"braw"}, ScenarioID: "strath", ScenarioName: "strath"},
	}

	for _, tc := range []struct {
		name string
		view viewMode
	}{
		{name: "starred", view: viewStarred},
		{name: "labels", view: viewLabels},
		{name: "scenarios", view: viewScenario},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sizedModel(t, base, "")
			m.view = tc.view
			m.rebuildForView()

			updated, _ := sendKey(m, "C")

			m = asOverlay(updated)
			if !m.collapsed["root"] || !m.collapsed["child"] || countSessionItems(m) != 1 {
				t.Fatalf("collapse-all state=%v sessions=%d, want nested parents collapsed and one row", m.collapsed, countSessionItems(m))
			}

			updated, _ = sendKey(m, "C")

			m = asOverlay(updated)
			if m.collapsed["root"] || m.collapsed["child"] || countSessionItems(m) != 3 {
				t.Fatalf("expand-all state=%v sessions=%d, want fully expanded three-row tree", m.collapsed, countSessionItems(m))
			}
		})
	}
}

func TestFilteredTreeViewsRefreshRetainsNestedSelection(t *testing.T) {
	base := []protocol.SessionInfo{
		{ID: "root", Name: "ben", Status: "running", Starred: true, Labels: []string{"braw"}, ScenarioID: "strath", ScenarioName: "strath"},
		{ID: "child", Name: "bairn", ParentID: "root", Status: "running", Starred: true, Labels: []string{"braw"}, ScenarioID: "strath", ScenarioName: "strath"},
	}

	for _, tc := range []struct {
		name string
		view viewMode
	}{
		{name: "starred", view: viewStarred},
		{name: "scenarios", view: viewScenario},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sizedModel(t, base, "")
			m.view = tc.view
			m.rebuildForView()
			m.selectSessionByID("child")

			updated, _ := m.Update(refreshSessionsMsg{sessions: []protocol.SessionInfo{base[1], base[0]}})

			m = asOverlay(updated)

			selected, ok := m.list.SelectedItem().(sessionItem)
			if !ok || selected.info.ID != "child" || selected.treePrefix != "└── " {
				t.Fatalf("selection after refresh = %+v, ok=%t; want nested child", selected, ok)
			}
		})
	}
}

func TestLabelViewSearchAndRefreshPreserveLabelSelection(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "braw", Name: "braw", RepoName: "croft", Status: "running", Labels: []string{"alpha", "beta"}},
		{ID: "canny", Name: "canny", RepoName: "bothy", Status: "running", Labels: []string{"beta"}},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	m.view = viewLabels
	m.rebuildForView()

	if got := filterSessions(sessions, "BETA"); len(got) != 2 {
		t.Fatalf("label search returned %d sessions, want 2", len(got))
	}

	m.selectSessionByIDAndLabel("braw", "beta")

	selected, ok := m.list.SelectedItem().(sessionItem)
	if !ok || selected.info.ID != "braw" || selected.labelGroup != "beta" {
		t.Fatalf("selected = %+v, ok=%t", selected, ok)
	}

	updated, _ := m.Update(refreshSessionsMsg{sessions: []protocol.SessionInfo{sessions[1], sessions[0]}, deleted: []protocol.SessionInfo{}})
	m = asOverlay(updated)

	selected, ok = m.list.SelectedItem().(sessionItem)
	if !ok || selected.info.ID != "braw" || selected.labelGroup != "beta" {
		t.Fatalf("selection after refresh = %+v, ok=%t", selected, ok)
	}
}

func TestLabelViewUnlabelledOnlyShowsNoLabelGroup(t *testing.T) {
	m := newOverlayModel([]protocol.SessionInfo{{ID: "braw", Name: "braw", Status: "running"}}, "", nil, nil, nil, nil)
	m.view = viewLabels
	m.rebuildForView()
	updated, _ := sendWindowSize(m, 100, 30)

	view := asOverlay(updated).View().Content
	if !strings.Contains(view, "(no label)") || !strings.Contains(view, "braw") {
		t.Fatalf("Labels view missing no-label group or session:\n%s", view)
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

func TestRepoView_CursorSkipsGroupHeader(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	m.view = viewRepo
	m.rebuildForView()

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

func TestRefreshDeletedNowLoadsOwnershipBeforeInput(t *testing.T) {
	sessions := overlayTestSessions()
	deleted := []protocol.SessionInfo{
		{ID: "bairn", Name: "bairn", ParentID: sessions[0].ID, DeletedAt: time.Now().Format(time.RFC3339)},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	m.refreshDeleted = func() []protocol.SessionInfo { return deleted }

	m.refreshDeletedNow()

	if !m.deletedReady {
		t.Fatal("deleted ownership data should be ready before Navigator input")
	}

	if len(m.deletedSessions) != 1 || m.deletedSessions[0].ID != "bairn" {
		t.Fatalf("initial deleted sessions = %+v, want bairn", m.deletedSessions)
	}
}

func TestRestoreDeletedSessionNavigatorStateAfterOwnershipLoad(t *testing.T) {
	deleted := protocol.SessionInfo{
		ID:        "bairn",
		Name:      "bairn",
		DeletedAt: time.Now().Format(time.RFC3339),
	}
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.refreshDeleted = func() []protocol.SessionInfo { return []protocol.SessionInfo{deleted} }

	m.refreshDeletedNow()
	m.restoreSessionNavigatorState(SessionNavigatorState{View: SessionNavigatorViewDeleted, SessionID: deleted.ID})

	if m.view != viewDeleted {
		t.Fatalf("restored view = %v, want Deleted", m.view)
	}

	item, ok := m.list.SelectedItem().(sessionItem)
	if !ok || item.info.ID != deleted.ID {
		t.Fatalf("restored deleted selection = %+v, ok=%t; want bairn", item, ok)
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

func TestUpdate_RefreshSessions_DeletedFailurePreservesOwnership(t *testing.T) {
	sessions := overlayTestSessions()
	m := sizedModel(t, sessions, "")
	m.deletedSessions = []protocol.SessionInfo{
		{ID: "bairn", Name: "bairn", ParentID: sessions[0].ID, DeletedAt: time.Now().Format(time.RFC3339)},
	}
	m.deletedReady = true

	updated, _ := m.Update(refreshSessionsMsg{sessions: sessions, deleted: nil})
	om := asOverlay(updated)

	if !om.deletedReady {
		t.Fatal("failed deleted refresh should preserve readiness")
	}

	if len(om.deletedSessions) != 1 || om.deletedSessions[0].ID != "bairn" {
		t.Fatalf("deleted sessions after failed refresh = %+v, want preserved bairn", om.deletedSessions)
	}

	if got := om.knownDescendantCount(sessions[0].ID); got != 1 {
		t.Fatalf("known descendants after failed refresh = %d, want 1", got)
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
			return errors.New("restart failed")
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

func TestUpdate_Stop_StaleSessionDropsOutdatedIndicator(t *testing.T) {
	sessions := []protocol.SessionInfo{{
		ID:          "braw",
		Name:        "braw",
		RepoName:    "graith",
		Agent:       "claude",
		Status:      "running",
		ConfigStale: true,
	}}

	m := sizedModel(t, sessions, "")
	m.stopSession = func(string) error { return nil }

	updated, _ := sendKey(m, "S")
	updated, cmd := sendKey(updated, "y")

	if cmd == nil {
		t.Fatal("expected a command from stop confirmation")
	}

	updated, _ = updated.Update(cmd())
	om := asOverlay(updated)

	if got := om.allSessions[0].Status; got != "stopped" {
		t.Fatalf("status after stop = %q, want stopped", got)
	}

	if !om.allSessions[0].ConfigStale {
		t.Fatal("test setup lost ConfigStale; client guard should filter it without waiting for refresh")
	}

	om.state = stateRestartMenu
	out := ansi.Strip(om.View().Content)

	if !strings.Contains(out, "[o]utdated (0)") {
		t.Errorf("restart menu should stop counting locally-stopped stale session:\n%s", out)
	}

	line := ansi.Strip(renderItem(om.allSessions, "", 1))
	if strings.Contains(line, "↻") {
		t.Errorf("stopped stale session should not render stale marker: %q", line)
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

func TestUpdate_Stop_EnterDeclines(t *testing.T) {
	called := false
	stopFn := func(string) error {
		called = true
		return nil
	}

	m := sizedModel(t, overlayTestSessions(), "")
	m.stopSession = stopFn

	updated, _ := sendKey(m, "S")
	updated, cmd := sendKey(updated, "enter")

	if cmd != nil {
		t.Fatal("enter should decline stop confirmation, not return a stop command")
	}

	if asOverlay(updated).state != stateList {
		t.Fatalf("state = %v, want stateList after enter declines", asOverlay(updated).state)
	}

	if called {
		t.Fatal("enter should not call stopSession")
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
	sessions[1].ConfigStale = true // s2 is stopped, so stale config is not actionable

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
		return errors.New("stop failed")
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
	// In the Repo view the first item is a group header; pressing S on it
	// should not enter the confirm-stop state.
	m := sizedModel(t, overlayTestSessions(), "")
	m.view = viewRepo
	m.rebuildForView()
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
	m.view = viewRepo
	m.rebuildForView()
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
	m.view = viewRepo
	m.rebuildForView()

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
	m.view = viewRepo
	m.rebuildForView()

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
	m.view = viewRepo
	m.rebuildForView()

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
	m.view = viewRepo
	m.rebuildForView()
	m.selectSessionByID("s3")

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

// --- Update: configurable Navigator keybindings (issue #918) ---

// TestOverlayConfigurableKeys is the regression test for the Navigator side of
// #918: delete_session, resume_session and search must honour the configured
// keybinding instead of the old hardcoded x/R/ literals.
func TestOverlayConfigurableKeys(t *testing.T) {
	cases := []struct {
		name  string
		keys  SessionNavigatorKeys
		press string
		want  overlayState
	}{
		{"delete", SessionNavigatorKeys{DeleteSession: "z"}, "z", stateConfirmDelete},
		{"resume", SessionNavigatorKeys{ResumeSession: "Z"}, "Z", stateRestartMenu},
		{"search", SessionNavigatorKeys{Search: "?"}, "?", stateFilter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newOverlayModel(overlayTestSessions(), "", nil, func(string, bool) error { return nil }, nil, nil)
			m.restartSession = func(string) error { return nil }
			m.applyKeys(tc.keys)

			updated, _ := sendKey(m, tc.press)
			if got := asOverlay(updated).state; got != tc.want {
				t.Fatalf("press %q: state = %v, want %v", tc.press, got, tc.want)
			}
		})
	}
}

func TestOverlayConfigurableSpaceKey(t *testing.T) {
	cases := map[string]struct {
		keys SessionNavigatorKeys
		want overlayState
	}{
		"delete": {keys: SessionNavigatorKeys{DeleteSession: "space"}, want: stateConfirmDelete},
		"resume": {keys: SessionNavigatorKeys{ResumeSession: "space"}, want: stateRestartMenu},
		"search": {keys: SessionNavigatorKeys{Search: "space"}, want: stateFilter},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			m := newOverlayModel(overlayTestSessions(), "", nil, func(string, bool) error { return nil }, nil, nil)
			m.applyKeys(test.keys)

			updated, _ := sendSpecialKey(m, tea.KeySpace)
			if got := asOverlay(updated).state; got != test.want {
				t.Fatalf("space-bound %s state = %v, want %v", name, got, test.want)
			}
		})
	}
}

// TestOverlayOldLiteralIgnoredAfterRemap confirms the previously-hardcoded
// literals no longer trigger their action once the key is rebound.
func TestOverlayOldLiteralIgnoredAfterRemap(t *testing.T) {
	cases := []struct {
		name    string
		keys    SessionNavigatorKeys
		oldKey  string
		notWant overlayState
	}{
		{"delete", SessionNavigatorKeys{DeleteSession: "z"}, "x", stateConfirmDelete},
		{"resume", SessionNavigatorKeys{ResumeSession: "Z"}, "R", stateRestartMenu},
		{"search", SessionNavigatorKeys{Search: "?"}, "/", stateFilter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newOverlayModel(overlayTestSessions(), "", nil, func(string, bool) error { return nil }, nil, nil)
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
// when no keybindings are configured (empty SessionNavigatorKeys).
func TestOverlayDefaultKeysWhenUnset(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, func(string, bool) error { return nil }, nil, nil)
	m.applyKeys(SessionNavigatorKeys{})

	updated, _ := sendKey(m, "x")
	if got := asOverlay(updated).state; got != stateConfirmDelete {
		t.Fatalf("default 'x' should open confirm-delete, got %v", got)
	}
}

func TestOverlayDefaultCancelIncludesCtrlC(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)

	_, cmd := sendKey(m, "ctrl+c")
	if cmd == nil {
		t.Fatal("ctrl+c should quit the Navigator by default")
	}

	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c command should produce QuitMsg")
	}
}

func TestOverlayCancelKeyRemapped(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.applyKeys(SessionNavigatorKeys{Cancel: []string{"z"}})

	if _, cmd := sendKey(m, "q"); cmd != nil {
		t.Fatal("old cancel key q should be inert after cancel remap")
	}

	_, cmd := sendKey(m, "z")
	if cmd == nil {
		t.Fatal("remapped cancel key should quit")
	}

	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("remapped cancel command should produce QuitMsg")
	}
}

// --- Update: Confirm delete state ---

func TestUpdate_ConfirmDeleteY(t *testing.T) {
	deletedID := ""
	deleteFn := func(sid string, children bool) error { deletedID = sid; return nil }
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
	deleteFn := func(sid string, children bool) error { return nil }
	m := newOverlayModel(overlayTestSessions(), "", nil, deleteFn, nil, nil)

	updated, _ := sendKey(m, "x")

	_, cmd := sendKey(asOverlay(updated), "Y")
	if cmd == nil {
		t.Fatal("Y should also produce a delete command")
	}
}

func TestOverlayDeleteSubtreeUsesCompleteHierarchy(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "orchestrator", Status: "running"},
		{ID: "child", Name: "bairn", ParentID: "root", Status: "running"},
		{ID: "grandchild", Name: "wee-bairn", ParentID: "child", Status: "running"},
	}

	var gotChildren bool

	m := newOverlayModel(sessions, "", nil, func(_ string, children bool) error {
		gotChildren = children
		return nil
	}, nil, nil)
	m.collapsed["root"] = true
	m.filterInput.SetValue("orchestrator")
	m.rebuildForView()
	m.width, m.height = 120, 40

	updated, _ := sendKey(m, "x")

	om := asOverlay(updated)

	if !strings.Contains(om.View().Content, "has 2 descendants") {
		t.Fatalf("subtree confirmation missing complete descendant count:\n%s", om.View().Content)
	}

	updated, cmd := sendKey(om, "y")
	if cmd == nil {
		t.Fatal("confirming subtree delete should return a command")
	}

	_ = updated

	if _, ok := cmd().(deleteResultMsg); !ok {
		t.Fatal("subtree delete should produce deleteResultMsg")
	}

	if !gotChildren {
		t.Error("subtree delete should pass children=true")
	}
}

func TestOverlayDeleteSubtreeIncludesSoftDeletedDescendants(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "croft", Status: "running"},
	}

	var gotChildren bool

	m := newOverlayModel(sessions, "", nil, func(_ string, children bool) error {
		gotChildren = children
		return nil
	}, nil, nil)
	m.deletedSessions = []protocol.SessionInfo{
		{ID: "hidden-child", Name: "bairn", ParentID: "root", DeletedAt: time.Now().Format(time.RFC3339)},
		{ID: "hidden-grandchild", Name: "wee-bairn", ParentID: "hidden-child", DeletedAt: time.Now().Format(time.RFC3339)},
	}
	m.width, m.height = 120, 40

	updated, _ := sendKey(m, "x")
	om := asOverlay(updated)

	if !strings.Contains(om.View().Content, "has 2 descendants") {
		t.Fatalf("subtree confirmation omitted soft-deleted descendants:\n%s", om.View().Content)
	}

	_, cmd := sendKey(om, "y")
	if cmd == nil {
		t.Fatal("confirming subtree delete should return a command")
	}

	if _, ok := cmd().(deleteResultMsg); !ok {
		t.Fatal("subtree delete should produce deleteResultMsg")
	}

	if !gotChildren {
		t.Error("soft-deleted descendants should pass children=true")
	}
}

func TestOverlayDeleteFailureShowsBlockerWithoutRepeatingPrompt(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", Status: "running"},
		{ID: "child", Name: "bairn", ParentID: "root", Status: "running"},
	}

	calls := 0
	refreshed := false
	m := newOverlayModel(sessions, "", nil, func(_ string, children bool) error {
		calls++

		if !children {
			t.Error("subtree delete should pass children=true")
		}

		return errors.New(`cannot delete root session "ben": session is starred; unstar it first to delete`)
	}, nil, nil)
	m.refreshSessions = func() []protocol.SessionInfo {
		refreshed = true
		return sessions
	}
	m.width, m.height = 120, 40

	updated, _ := sendKey(m, "x")
	om := asOverlay(updated)

	updated, cmd := sendKey(om, "y")
	if cmd == nil {
		t.Fatal("confirming subtree delete should return a command")
	}

	updated, _ = asOverlay(updated).Update(cmd())
	om = asOverlay(updated)
	content := om.View().Content

	if !strings.Contains(content, `Delete failed: cannot delete root session "ben": session is starred`) {
		t.Fatalf("delete blocker missing from failed-delete view:\n%s", content)
	}

	if strings.Contains(content, "Delete the entire subtree? [y/N]") {
		t.Fatalf("failed delete repeated the confirmation prompt:\n%s", content)
	}

	if !strings.Contains(content, "Delete blocked. Press any key to return to the list.") {
		t.Fatalf("failed delete view missing acknowledgement prompt:\n%s", content)
	}

	updated, cmd = sendKey(om, "y")
	if cmd == nil {
		t.Fatal("acknowledging a failed delete should request a refresh")
	}

	msg := cmd()
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if c != nil {
				_ = c()
			}
		}
	case refreshSessionsMsg:
	default:
		t.Fatalf("acknowledging failed delete returned %T, want refresh", msg)
	}

	if calls != 1 {
		t.Fatalf("deleteSession called %d times, want 1", calls)
	}

	if !refreshed {
		t.Fatal("acknowledging failed delete should refresh sessions")
	}

	if asOverlay(updated).state != stateList {
		t.Fatal("acknowledging failed delete should return to list")
	}
}

func TestOverlayDeleteWaitsForInitialOwnershipData(t *testing.T) {
	called := false
	m := newOverlayModel(
		[]protocol.SessionInfo{{ID: "root", Name: "croft", Status: "running"}},
		"",
		nil,
		func(_ string, _ bool) error {
			called = true
			return nil
		},
		nil,
		nil,
	)
	m.refreshDeleted = func() []protocol.SessionInfo { return nil }
	m.width, m.height = 120, 40

	updated, _ := sendKey(m, "x")
	om := asOverlay(updated)

	if !strings.Contains(om.View().Content, "Delete unavailable: waiting for session ownership data") {
		t.Fatalf("missing unavailable state while ownership is unknown:\n%s", om.View().Content)
	}

	_, cmd := sendKey(om, "y")
	if cmd != nil || called {
		t.Fatalf("delete before ownership load called=%v cmd=%v, want no mutation", called, cmd)
	}
}

func TestOverlayDeleteRecoversWhenOwnershipRetrySucceeds(t *testing.T) {
	called := false
	m := newOverlayModel(
		[]protocol.SessionInfo{{ID: "root", Name: "croft", Status: "running"}},
		"",
		nil,
		func(_ string, children bool) error {
			called = children
			return nil
		},
		nil,
		nil,
	)
	m.refreshDeleted = func() []protocol.SessionInfo {
		return []protocol.SessionInfo{{ID: "bairn", Name: "bairn", ParentID: "root"}}
	}
	m.width, m.height = 120, 40

	updated, cmd := sendKey(m, "x")
	if cmd == nil {
		t.Fatal("opening delete while ownership is unknown should retry the deleted-session fetch")
	}

	updated, _ = asOverlay(updated).Update(cmd())
	om := asOverlay(updated)

	if !om.deletedReady || !strings.Contains(om.View().Content, "has 1 descendant") {
		t.Fatalf("ownership retry did not update delete confirmation:\n%s", om.View().Content)
	}

	_, cmd = sendKey(om, "y")
	if cmd == nil {
		t.Fatal("delete should become actionable after ownership retry")
	}

	_ = cmd()

	if !called {
		t.Fatal("recovered delete should include the hidden child")
	}
}

func TestOverlayDeleteSubtreeCancellationDoesNothing(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "orchestrator"},
		{ID: "child", Name: "bairn", ParentID: "root"},
	}

	called := false

	m := newOverlayModel(sessions, "", nil, func(_ string, _ bool) error {
		called = true
		return nil
	}, nil, nil)
	updated, _ := sendKey(m, "x")

	updated, cmd := sendKey(updated, "n")

	if cmd != nil || called {
		t.Fatalf("cancelling subtree delete called=%v cmd=%v, want no mutation", called, cmd)
	}

	if asOverlay(updated).state != stateList {
		t.Fatal("cancelling subtree delete should return to list")
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

func TestUpdate_ConfirmDeleteEnterDeclines(t *testing.T) {
	called := false
	deleteFn := func(string, bool) error {
		called = true
		return nil
	}

	m := newOverlayModel(overlayTestSessions(), "", nil, deleteFn, nil, nil)

	updated, _ := sendKey(m, "x")
	updated, cmd := sendKey(asOverlay(updated), "enter")

	if cmd != nil {
		t.Fatal("enter should decline delete confirmation, not return a delete command")
	}

	om := asOverlay(updated)
	if om.state != stateList {
		t.Fatalf("state = %v, want stateList after enter declines", om.state)
	}

	if called {
		t.Fatal("enter should not call deleteSession")
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

	if !strings.Contains(view, sessionNavigatorTitle) {
		t.Errorf("view should contain the navigator title %q", sessionNavigatorTitle)
	}

	if !strings.Contains(view, "All") {
		t.Error("view should contain the view name 'All'")
	}

	for _, name := range []string{"canny-tests", "braw-fix", "bonnie-feature"} {
		if !strings.Contains(view, name) {
			t.Errorf("view should contain session name %q", name)
		}
	}
}

func TestView_SessionNavigatorContextAtCompactAndWideSizes(t *testing.T) {
	tests := map[string]struct {
		width  int
		height int
	}{
		"compact 80x24": {width: 80, height: 24},
		"wide 160x40":   {width: 160, height: 40},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			m := newOverlayModel(overlayTestSessions(), "s1", nil, nil, nil, nil)
			updated, _ := sendWindowSize(m, test.width, test.height)
			view := ansi.Strip(asOverlay(updated).View().Content)

			for _, want := range []string{
				sessionNavigatorTitle,
				"3 sessions",
				"Selected: braw-fix",
				"attached",
				"status: running",
			} {
				if !strings.Contains(view, want) {
					t.Errorf("navigator view should contain %q at %s:\n%s", want, name, view)
				}
			}
		})
	}
}

func TestView_WideSelectedSessionDetailPanel(t *testing.T) {
	const terminalWidth = 240

	now := time.Now()
	sessions := []protocol.SessionInfo{
		{
			ID:              "braw-wide-detail",
			Name:            "braw-detail",
			RepoName:        "graith",
			Branch:          "d0ugal/graith/issue-1870-wide-details",
			BaseBranch:      "main",
			Agent:           "codex",
			Model:           "gpt-5",
			Status:          "running",
			SummaryText:     "polishing navigator detail",
			WorktreePath:    "/tmp/graith/strath/braw-detail",
			Labels:          []string{"cli", "polish"},
			CreatedAt:       now.Add(-2 * time.Hour).Format(time.RFC3339),
			LastAttachedAt:  now.Add(-20 * time.Minute).Format(time.RFC3339),
			StatusChangedAt: now.Add(-5 * time.Minute).Format(time.RFC3339),
			PullRequest:     &protocol.PRInfo{Number: 1870, State: "open", ReviewDecision: "review_required"},
			CI:              &protocol.CIInfo{State: "pending", Passed: 16, Total: 22},
		},
	}

	m := newOverlayModel(sessions, "braw-wide-detail", noopFetchPreview, nil, nil, nil)
	updated, _ := sendWindowSize(m, terminalWidth, 40)
	om := asOverlay(updated)
	updated, _ = om.Update(previewMsg{
		sessionID: "braw-wide-detail",
		content:   "UNIQUE_PREVIEW_LINE_1\nUNIQUE_PREVIEW_LINE_2",
	})

	view := asOverlay(updated).View().Content
	plain := ansi.Strip(view)

	for _, want := range []string{
		"Selected Session",
		"Status:",
		"polishing navigator detail",
		"Branch:   issue-1870-wide-details",
		"Base:     main",
		"Worktree:",
		"braw-detail",
		"PR:       #1870 open CI:16/22",
		"Review:   needed",
		"Labels:",
		"Created:",
		"Attached:",
		"Changed:",
		"UNIQUE_PREVIEW_LINE_2",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("wide detail view should contain %q:\n%s", want, plain)
		}
	}

	labelLine := firstLineContaining(plain, "Labels:")
	if !strings.Contains(labelLine, "cli") || !strings.Contains(labelLine, "polish") {
		t.Errorf("wide detail label line should contain coloured label text, got %q:\n%s", labelLine, plain)
	}

	for _, absent := range []string{
		"Summary:  polishing navigator detail",
		"branch: issue-1870-wide-details",
		"PR #1870 open",
		"/tmp/graith/strath/braw-detail  id:",
	} {
		if strings.Contains(plain, absent) {
			t.Errorf("wide detail view should not duplicate footer metadata %q:\n%s", absent, plain)
		}
	}

	for i, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > terminalWidth {
			t.Errorf("line %d width = %d, want <= %d: %q", i+1, width, terminalWidth, line)
		}
	}
}

func TestView_WideSelectedSessionDetailShowsFullStatusAtBottom(t *testing.T) {
	longStatus := "checking a canny navigator follow-up with full selected session status visible beside the wide detail panel"
	sessions := []protocol.SessionInfo{{
		ID:          "braw-full-status",
		Name:        "braw-status",
		RepoName:    "graith",
		Branch:      "d0ugal/graith/issue-1870-wide-details",
		Agent:       "codex",
		Status:      "running",
		SummaryText: longStatus,
		CreatedAt:   time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
	}}

	m := newOverlayModel(sessions, "braw-full-status", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 240, 40)
	view := ansi.Strip(asOverlay(updated).View().Content)

	if !strings.Contains(view, "Selected Session") {
		t.Fatalf("wide detail panel should render:\n%s", view)
	}

	if strings.Contains(view, "Summary:") {
		t.Fatalf("wide detail panel should not keep the old truncated Summary row:\n%s", view)
	}

	for _, want := range []string{
		"Status:",
		"checking a canny navigator follow-up",
		"full selected session status visible",
		"detail panel",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("bottom status should contain %q from the full selected-session status:\n%s", want, view)
		}
	}
}

func TestView_WideSelectedSessionDetailVeryLongStatusKeepsHelpVisible(t *testing.T) {
	sessions := []protocol.SessionInfo{{
		ID:          "braw-long-status",
		Name:        "braw-status",
		RepoName:    "graith",
		Agent:       "codex",
		Status:      "running",
		SummaryText: strings.Repeat("checking navigator status wrapping ", 40),
		CreatedAt:   time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
	}}

	m := newOverlayModel(sessions, "braw-long-status", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 240, 24)
	view := ansi.Strip(asOverlay(updated).View().Content)

	for _, want := range []string{
		"Selected Session",
		"Status:",
		"…",
		"enter attach",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("very long status should preserve %q in the wide Navigator:\n%s", want, view)
		}
	}

	for i, line := range strings.Split(asOverlay(updated).View().Content, "\n") {
		if width := ansi.StringWidth(line); width > 240 {
			t.Fatalf("line %d width = %d, want <= 240: %q", i+1, width, line)
		}
	}
}

func TestView_WideSelectedSessionDetailSelectionChangeReflowsStatusReserve(t *testing.T) {
	now := time.Now()
	sessions := []protocol.SessionInfo{
		{
			ID:          "auld-short",
			Name:        "auld-short",
			RepoName:    "graith",
			Agent:       "codex",
			Status:      "running",
			SummaryText: "short status",
			CreatedAt:   now.Add(-2 * time.Hour).Format(time.RFC3339),
		},
		{
			ID:          "braw-long",
			Name:        "braw-long",
			RepoName:    "graith",
			Agent:       "codex",
			Status:      "running",
			SummaryText: strings.Repeat("checking navigator status wrapping ", 20),
			CreatedAt:   now.Add(-2 * time.Hour).Format(time.RFC3339),
		},
	}

	for i := 0; i < 18; i++ {
		sessions = append(sessions, protocol.SessionInfo{
			ID:        fmt.Sprintf("croft-%02d", i),
			Name:      fmt.Sprintf("croft-%02d", i),
			RepoName:  "graith",
			Agent:     "codex",
			Status:    "running",
			CreatedAt: now.Add(-time.Duration(i+1) * time.Hour).Format(time.RFC3339),
		})
	}

	m := newOverlayModel(sessions, "auld-short", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 160, 24)
	om := asOverlay(updated)
	shortHeight := om.list.Height()

	updated, _ = sendKey(om, "down")
	om = asOverlay(updated)

	if item, ok := om.list.SelectedItem().(sessionItem); !ok || item.info.ID != "braw-long" {
		t.Fatalf("selected item after down = %#v, want braw-long", om.list.SelectedItem())
	}

	if om.list.Height() >= shortHeight {
		t.Fatalf("list height should shrink after selecting a long status: before=%d after=%d", shortHeight, om.list.Height())
	}

	view := ansi.Strip(om.View().Content)
	if !strings.Contains(view, "enter attach") {
		t.Fatalf("reflowed long-status selection should keep help visible:\n%s", view)
	}
}

func TestView_CompactOmitsWideSelectedSessionDetailPanel(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID:           "braw-compact-detail",
			Name:         "braw-detail",
			RepoName:     "graith",
			Branch:       "d0ugal/graith/issue-1870-wide-details",
			Agent:        "codex",
			Status:       "running",
			SummaryText:  "checking a canny navigator follow-up with full selected session status visible beside the wide detail panel",
			Labels:       []string{"cli", "polish"},
			WorktreePath: "/tmp/graith/strath/braw-detail",
			CreatedAt:    time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
		},
	}

	m := newOverlayModel(sessions, "braw-compact-detail", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 80, 24)
	view := ansi.Strip(asOverlay(updated).View().Content)

	if strings.Contains(view, "Selected Session") {
		t.Fatalf("compact view should not show the wide-only detail panel:\n%s", view)
	}

	if strings.Contains(view, "Status:") {
		t.Fatalf("compact view should not spend footer space on the wide-only full-status block:\n%s", view)
	}

	for _, want := range []string{
		"Selected: braw-detail",
		"attached",
		"enter attach",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("compact view should retain existing footer detail %q:\n%s", want, view)
		}
	}
}

func TestView_WideSelectedSessionDetailMinHeightFallbackKeepsFooter(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID:           "braw-short-detail",
			Name:         "braw-detail",
			RepoName:     "graith",
			Branch:       "d0ugal/graith/issue-1870-wide-details",
			Agent:        "codex",
			Status:       "running",
			WorktreePath: "/tmp/graith/strath/braw-detail",
			CreatedAt:    time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
		},
	}

	m := newOverlayModel(sessions, "braw-short-detail", nil, nil, nil, nil)
	m.selectedDetail.MinTerminalHeight = 40

	updated, _ := sendWindowSize(m, 160, 24)
	view := ansi.Strip(asOverlay(updated).View().Content)

	if strings.Contains(view, "Selected Session") {
		t.Fatalf("short terminal should not show the wide-only detail panel:\n%s", view)
	}

	for _, want := range []string{
		"Selected: braw-detail  attached",
		"branch: issue-1870-wide-details",
		"agent: codex",
		"/tmp/graith/strath/braw-detail  id:",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("short terminal should retain compact footer detail %q:\n%s", want, view)
		}
	}
}

func TestView_WideSelectedSessionDetailSpareWidthFallbackKeepsFooter(t *testing.T) {
	longName := "braw-" + strings.Repeat("strath-", 18)
	sessions := []protocol.SessionInfo{
		{
			ID:           "braw-spare-detail",
			Name:         longName,
			RepoName:     "graith",
			Branch:       "d0ugal/graith/issue-1870-wide-details",
			Agent:        "codex",
			Status:       "running",
			WorktreePath: "/tmp/graith/strath/braw-spare-detail",
			CreatedAt:    time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
		},
	}

	m := newOverlayModel(sessions, "braw-spare-detail", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 160, 40)
	view := ansi.Strip(asOverlay(updated).View().Content)

	if strings.Contains(view, "Selected Session") {
		t.Fatalf("wide terminal without spare width should not show selected detail panel:\n%s", view)
	}

	for _, want := range []string{
		"branch: issue-1870-wide-details",
		"agent: codex",
		"/tmp/graith/strath/braw-spare-detail  id:",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("spare-width fallback should retain compact footer detail %q:\n%s", want, view)
		}
	}
}

func TestView_WideSelectedSessionDetailSpareWidthFallbackDoesNotReserveStatusRows(t *testing.T) {
	now := time.Now()
	sessions := []protocol.SessionInfo{{
		ID:          "braw-width-detail",
		Name:        "braw-detail",
		RepoName:    "graith",
		Branch:      "d0ugal/graith/issue-1870-wide-details",
		Agent:       "codex",
		Status:      "running",
		SummaryText: strings.Repeat("checking navigator status wrapping ", 20),
		CreatedAt:   now.Add(-2 * time.Hour).Format(time.RFC3339),
	}}

	for i := 0; i < 20; i++ {
		sessions = append(sessions, protocol.SessionInfo{
			ID:        fmt.Sprintf("croft-width-%02d", i),
			Name:      fmt.Sprintf("croft-width-%02d", i),
			RepoName:  "graith",
			Agent:     "codex",
			Status:    "running",
			CreatedAt: now.Add(-time.Duration(i+1) * time.Hour).Format(time.RFC3339),
		})
	}

	for width := 120; width <= 220; width++ {
		m := newOverlayModel(sessions, "braw-width-detail", nil, nil, nil, nil)
		updated, _ := sendWindowSize(m, width, 24)
		om := asOverlay(updated)
		view := ansi.Strip(om.View().Content)

		if strings.Contains(view, "Selected Session") || om.wideDetailPanelWidth(om.panelWidth()) == 0 {
			continue
		}

		if strings.Contains(view, "Status:") {
			t.Fatalf("spare-width fallback should not render the wide-only status block at width %d:\n%s", width, view)
		}

		wantHeight := min(len(om.list.Items())+4, om.height-om.baseListReserve())
		if om.list.Height() != wantHeight {
			t.Fatalf("spare-width fallback should not reserve hidden status rows at width %d: height=%d want=%d", width, om.list.Height(), wantHeight)
		}

		return
	}

	t.Fatal("test setup did not find a width where the pre-render gate allowed wide detail but final layout rejected it")
}

func TestView_WideSelectedSessionDetailTooTallFallbackKeepsFooter(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID:              "braw-tall-detail",
			Name:            "braw-detail",
			RepoName:        "graith",
			Branch:          "d0ugal/graith/issue-1870-wide-details",
			BaseBranch:      "main",
			Agent:           "codex",
			Model:           "gpt-5",
			Status:          "running",
			SummaryText:     "polishing navigator detail",
			WorktreePath:    "/tmp/graith/strath/braw-detail",
			CWD:             "/tmp/graith/strath/braw-detail",
			Labels:          []string{"cli", "polish"},
			CreatedAt:       time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
			LastAttachedAt:  time.Now().Add(-20 * time.Minute).Format(time.RFC3339),
			StatusChangedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
			PullRequest:     &protocol.PRInfo{Number: 1870, State: "open", ReviewDecision: "review_required"},
			CI:              &protocol.CIInfo{State: "pending", Passed: 16, Total: 22},
		},
	}

	m := newOverlayModel(sessions, "braw-tall-detail", nil, nil, nil, nil)

	m.selectedDetail.Fields = append(cloneSelectedDetailFields(defaultSelectedDetailFields),
		"cwd", "cwd", "cwd", "cwd", "cwd", "cwd", "cwd", "cwd", "cwd", "cwd",
	)

	updated, _ := sendWindowSize(m, 160, 24)
	view := ansi.Strip(asOverlay(updated).View().Content)

	if strings.Contains(view, "Selected Session") {
		t.Fatalf("detail panel taller than the terminal should fall back to inline footer:\n%s", view)
	}

	for _, want := range []string{
		"Selected: braw-detail  attached",
		"branch: issue-1870-wide-details  base: main  agent: codex",
		"PR #1870 open",
		"/tmp/graith/strath/braw-detail  id:",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("too-tall fallback should retain compact footer detail %q:\n%s", want, view)
		}
	}
}

func TestView_WideSelectedSessionDetailTooTallFallbackDoesNotReserveStatusRows(t *testing.T) {
	now := time.Now()
	sessions := []protocol.SessionInfo{{
		ID:              "braw-tall-detail",
		Name:            "braw-detail",
		RepoName:        "graith",
		Branch:          "d0ugal/graith/issue-1870-wide-details",
		BaseBranch:      "main",
		Agent:           "codex",
		Model:           "gpt-5",
		Status:          "running",
		SummaryText:     strings.Repeat("checking navigator status wrapping ", 20),
		WorktreePath:    "/tmp/graith/strath/braw-detail",
		CWD:             "/tmp/graith/strath/braw-detail",
		Labels:          []string{"cli", "polish"},
		CreatedAt:       now.Add(-2 * time.Hour).Format(time.RFC3339),
		LastAttachedAt:  now.Add(-20 * time.Minute).Format(time.RFC3339),
		StatusChangedAt: now.Add(-5 * time.Minute).Format(time.RFC3339),
		PullRequest:     &protocol.PRInfo{Number: 1870, State: "open", ReviewDecision: "review_required"},
		CI:              &protocol.CIInfo{State: "pending", Passed: 16, Total: 22},
	}}

	for i := 0; i < 20; i++ {
		sessions = append(sessions, protocol.SessionInfo{
			ID:        fmt.Sprintf("croft-%02d", i),
			Name:      fmt.Sprintf("croft-%02d", i),
			RepoName:  "graith",
			Agent:     "codex",
			Status:    "running",
			CreatedAt: now.Add(-time.Duration(i+1) * time.Hour).Format(time.RFC3339),
		})
	}

	m := newOverlayModel(sessions, "braw-tall-detail", nil, nil, nil, nil)

	m.selectedDetail.Fields = append(cloneSelectedDetailFields(defaultSelectedDetailFields),
		"cwd", "cwd", "cwd", "cwd", "cwd", "cwd", "cwd", "cwd", "cwd", "cwd",
	)

	updated, _ := sendWindowSize(m, 160, 24)
	om := asOverlay(updated)
	view := ansi.Strip(om.View().Content)

	if strings.Contains(view, "Selected Session") {
		t.Fatalf("detail panel taller than the terminal should fall back to inline footer:\n%s", view)
	}

	if strings.Contains(view, "Status:") {
		t.Fatalf("too-tall fallback should not render the wide-only status block:\n%s", view)
	}

	wantHeight := min(len(om.list.Items())+4, om.height-om.baseListReserve())
	if om.list.Height() != wantHeight {
		t.Fatalf("too-tall fallback should not reserve hidden status rows: height=%d want=%d", om.list.Height(), wantHeight)
	}
}

func TestView_DisablesWideSelectedSessionDetailPanel(t *testing.T) {
	sessions := []protocol.SessionInfo{{
		ID:        "braw-disabled-detail",
		Name:      "braw-detail",
		RepoName:  "graith",
		Branch:    "d0ugal/graith/issue-1870-wide-details",
		Status:    "running",
		CreatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
	}}
	m := newOverlayModel(sessions, "braw-disabled-detail", nil, nil, nil, nil)
	m.selectedDetail.Enabled = false

	updated, _ := sendWindowSize(m, 160, 40)
	view := ansi.Strip(asOverlay(updated).View().Content)

	if strings.Contains(view, "Selected Session") {
		t.Fatalf("disabled selected-detail config should hide the wide panel:\n%s", view)
	}

	if !strings.Contains(view, "Selected: braw-detail") {
		t.Fatalf("disabled selected-detail config should preserve the compact footer:\n%s", view)
	}
}

func TestView_WideSelectedSessionDetailMaxWidthClampsToMinimum(t *testing.T) {
	sessions := []protocol.SessionInfo{{
		ID:        "braw-narrow-detail",
		Name:      "braw-detail",
		RepoName:  "graith",
		Branch:    "d0ugal/graith/issue-1870-wide-details",
		Status:    "running",
		CreatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
	}}
	m := newOverlayModel(sessions, "braw-narrow-detail", nil, nil, nil, nil)
	m.selectedDetail.MaxWidth = 10

	updated, _ := sendWindowSize(m, 160, 40)
	view := ansi.Strip(asOverlay(updated).View().Content)

	if !strings.Contains(view, "Selected Session") {
		t.Fatalf("small direct max width should clamp instead of hiding the wide panel:\n%s", view)
	}
}

func TestView_WideSelectedSessionDetailKeepsGroupedPanelPosition(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.view = viewRepo
	m.rebuildForView()

	updated, _ := sendWindowSize(m, 200, 40)
	om := asOverlay(updated)

	om.list.Select(0)
	headerView := om.View().Content
	headerCol := columnOfText(t, headerView, sessionNavigatorTitle)

	if !strings.Contains(ansi.Strip(headerView), "Session Group") {
		t.Fatalf("group header selection should render a stable side placeholder:\n%s", ansi.Strip(headerView))
	}

	if got, want := rowOfText(t, headerView, "Session Group"), rowOfText(t, headerView, sessionNavigatorTitle); got != want {
		t.Fatalf("group detail panel row = %d, want aligned with Navigator row %d", got, want)
	}

	om.list.Select(1)
	sessionView := om.View().Content
	sessionCol := columnOfText(t, sessionView, sessionNavigatorTitle)

	if !strings.Contains(ansi.Strip(sessionView), "Selected Session") {
		t.Fatalf("session selection should render selected-session details:\n%s", ansi.Strip(sessionView))
	}

	if got, want := rowOfText(t, sessionView, "Selected Session"), rowOfText(t, sessionView, sessionNavigatorTitle); got != want {
		t.Fatalf("selected detail panel row = %d, want aligned with Navigator row %d", got, want)
	}

	if headerCol != sessionCol {
		t.Fatalf("Navigator column changed from %d on group header to %d on session row", headerCol, sessionCol)
	}
}

func TestView_WideSelectedSessionDetailFieldsConfig(t *testing.T) {
	sessions := []protocol.SessionInfo{{
		ID:           "braw-field-detail",
		Name:         "braw-detail",
		RepoName:     "graith",
		Branch:       "d0ugal/graith/issue-1870-wide-details",
		Agent:        "codex",
		Status:       "running",
		SummaryText:  "should be hidden from wide panel",
		WorktreePath: "/tmp/graith/strath/braw-detail",
		Labels:       []string{"cli", "polish"},
		CreatedAt:    time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
		PullRequest:  &protocol.PRInfo{Number: 1870, State: "open"},
	}}
	m := newOverlayModel(sessions, "braw-field-detail", nil, nil, nil, nil)
	m.selectedDetail.Fields = []string{"branch", "labels"}

	updated, _ := sendWindowSize(m, 240, 40)
	view := ansi.Strip(asOverlay(updated).View().Content)

	for _, want := range []string{
		"Selected Session",
		"Branch:   issue-1870-wide-details",
		"Labels:",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("custom detail fields should contain %q:\n%s", want, view)
		}
	}

	labelLine := firstLineContaining(view, "Labels:")
	if !strings.Contains(labelLine, "cli") || !strings.Contains(labelLine, "polish") {
		t.Errorf("custom detail label line should contain label chips, got %q:\n%s", labelLine, view)
	}

	for _, absent := range []string{
		"Summary:  should be hidden from wide panel",
		"Status:",
		"Agent:    codex",
		"PR:       #1870 open",
	} {
		if strings.Contains(view, absent) {
			t.Errorf("custom detail fields should omit %q:\n%s", absent, view)
		}
	}
}

func TestView_WideSelectedSessionDetailEmptyFieldsKeepHeaderOnly(t *testing.T) {
	sessions := []protocol.SessionInfo{{
		ID:          "braw-empty-detail",
		Name:        "braw-detail",
		RepoName:    "graith",
		Branch:      "d0ugal/graith/issue-1870-wide-details",
		Status:      "running",
		SummaryText: "should not render as metadata",
		CreatedAt:   time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
	}}
	m := newOverlayModel(sessions, "braw-empty-detail", nil, nil, nil, nil)
	m.selectedDetail.Fields = []string{}

	updated, _ := sendWindowSize(m, 160, 40)
	view := ansi.Strip(asOverlay(updated).View().Content)

	for _, want := range []string{
		"Selected Session",
		"braw-detail",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("empty detail fields should preserve header %q:\n%s", want, view)
		}
	}

	for _, absent := range []string{
		"Summary:  should not render as metadata",
		"Branch:   issue-1870-wide-details",
	} {
		if strings.Contains(view, absent) {
			t.Errorf("empty detail fields should omit %q:\n%s", absent, view)
		}
	}
}

func TestView_SessionNavigatorCompactRichDetailsKeepHelpVisible(t *testing.T) {
	sessions := make([]protocol.SessionInfo, 18)
	for i := range sessions {
		sessions[i] = protocol.SessionInfo{
			ID:        fmt.Sprintf("session-%02d", i),
			Name:      fmt.Sprintf("strath-%02d", i),
			RepoName:  "graith",
			Branch:    fmt.Sprintf("d0ugal/graith/strath-%02d", i),
			Agent:     "codex",
			Status:    "running",
			CreatedAt: time.Now().Add(-time.Duration(i+1) * time.Hour).Format(time.RFC3339),
		}
	}

	sessions[0].ID = "selected-rich"
	sessions[0].Name = "braw-rich"
	sessions[0].BaseBranch = "main"
	sessions[0].ConfigStale = true
	sessions[0].WorktreePath = "/tmp/graith/strath/braw-rich"
	sessions[0].PullRequest = &protocol.PRInfo{Number: 7, State: "open"}
	sessions[0].CI = &protocol.CIInfo{State: "pending", Passed: 16, Total: 22}

	m := newOverlayModel(sessions, "selected-rich", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 80, 24)
	view := ansi.Strip(asOverlay(updated).View().Content)

	for _, want := range []string{
		"Selected: braw-rich",
		"config stale",
		"PR #7 open",
		"enter attach",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("compact rich-detail navigator should contain %q:\n%s", want, view)
		}
	}
}

func TestView_SessionNavigatorCompactTitleKeepsActiveView(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.profile = "oncall"
	m.view = viewDeleted
	m.rebuildForView()

	updated, _ := sendWindowSize(m, 80, 24)
	view := ansi.Strip(asOverlay(updated).View().Content)

	for _, want := range []string{
		sessionNavigatorTitle,
		"view: Deleted",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("compact title should contain %q:\n%s", want, view)
		}
	}
}

func TestView_SessionNavigatorTitleCountsUniqueSessionsInLabelsView(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "braw", RepoName: "croft", Status: "running", Labels: []string{"alpha", "beta"}},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	m.view = viewLabels
	m.rebuildForView()

	updated, _ := sendWindowSize(m, 120, 40)
	view := ansi.Strip(asOverlay(updated).View().Content)

	if !strings.Contains(view, "1 session") {
		t.Fatalf("labels title should count unique sessions:\n%s", view)
	}

	if strings.Contains(view, "2 sessions") {
		t.Fatalf("labels title should not count repeated label rows as sessions:\n%s", view)
	}
}

func TestView_SelectedContextAddsRepoInGroupedView(t *testing.T) {
	m := sizedModel(t, overlayTestSessions(), "s1")
	m.view = viewRepo
	m.rebuildForView()
	m.selectSessionByID("s1")

	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Selected: braw-fix", "repo: graith", "status: running"} {
		if !strings.Contains(view, want) {
			t.Errorf("grouped selected context should contain %q:\n%s", want, view)
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
			updated, _ := sendWindowSize(m, 120, 40)
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
	m.view = viewRepo
	m.rebuildForView()
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
	updated, _ := sendWindowSize(m, m.contentWidth+8, 40)
	view := asOverlay(updated).View().Content

	for _, header := range []string{"Session", "Status", "Summary", "Git", "PR", "Output"} {
		if !strings.Contains(view, header) {
			t.Errorf("view should contain column header %q:\n%s", header, ansi.Strip(view))
		}
	}
}

func TestView_ShowsHelpBar(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.view = viewRepo
	m.rebuildForView()
	updated, _ := sendWindowSize(m, 150, 40)
	view := asOverlay(updated).View().Content

	if !strings.Contains(view, "enter attach") {
		t.Error("view should contain help bar")
	}

	if !strings.Contains(view, "tab group") {
		t.Error("help bar should mention tab group navigation")
	}
}

func TestView_NavigatorCompactHelpAtCompactAndWideSizes(t *testing.T) {
	tests := map[string]struct {
		width  int
		height int
	}{
		"compact 80x24": {width: 80, height: 24},
		"wide 160x40":   {width: 160, height: 40},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
			updated, _ := sendWindowSize(m, test.width, test.height)
			view := ansi.Strip(asOverlay(updated).View().Content)

			for _, want := range []string{"enter attach", "n new", "h/l view", "/ filter", "? help", "q quit"} {
				if !strings.Contains(view, want) {
					t.Errorf("compact help should contain %q at %s:\n%s", want, name, view)
				}
			}

			for _, notWant := range []string{"S stop", "C fold-all", "x delete"} {
				if strings.Contains(view, notWant) {
					t.Errorf("compact help should move %q behind expanded help at %s:\n%s", notWant, name, view)
				}
			}
		})
	}
}

func TestView_NavigatorCompactHelpTruncatesWholeActions(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.view = viewRepo
	m.rebuildForView()

	got := m.compactNavigatorHelpLine(40)

	for _, want := range []string{"enter attach", "? help", "q quit"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact help = %q, want to preserve %q", got, want)
		}
	}

	if strings.Contains(got, "? h…") || strings.Contains(got, "q q…") {
		t.Fatalf("compact help should not truncate inside preserved actions: %q", got)
	}
}

func TestView_NavigatorExpandedHelp(t *testing.T) {
	tests := map[string]struct {
		width  int
		height int
	}{
		"compact 80x24": {width: 80, height: 24},
		"wide 160x40":   {width: 160, height: 40},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, []rune("1234567890"))
			updated, _ := sendWindowSize(m, test.width, test.height)
			updated, _ = sendKey(asOverlay(updated), "?")
			om := asOverlay(updated)

			if !om.helpExpanded {
				t.Fatal("pressing ? should expand Navigator help")
			}

			view := ansi.Strip(om.View().Content)
			for _, want := range []string{
				"j/k move",
				"g/G top/bottom",
				"h/l view",
				"1-0 jump",
				"enter attach",
				"n new",
				"s star",
				"space fold",
				"C fold-all",
				"x delete",
				"S stop",
				"r restart",
				"R restart menu",
			} {
				if !strings.Contains(view, want) {
					t.Errorf("expanded help should contain %q at %s:\n%s", want, name, view)
				}
			}

			updated, _ = sendKey(om, "?")

			om = asOverlay(updated)
			if om.helpExpanded {
				t.Fatal("pressing ? again should collapse Navigator help")
			}
		})
	}
}

func TestView_NavigatorHelpUsesConfiguredActions(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.applyHelp(SessionNavigatorHelp{
		CompactActions:    []string{"help", "quit"},
		ExpandedActions:   []string{"delete", "stop", "help"},
		ToggleKeys:        []string{"f2"},
		ExpandedByDefault: true,
	})
	updated, _ := sendWindowSize(m, 80, 24)
	view := ansi.Strip(asOverlay(updated).View().Content)

	for _, want := range []string{"x delete", "S stop", "f2 hide"} {
		if !strings.Contains(view, want) {
			t.Errorf("configured expanded help should contain %q:\n%s", want, view)
		}
	}

	for _, notWant := range []string{"enter attach", "n new", "/ filter"} {
		if strings.Contains(view, notWant) {
			t.Errorf("configured expanded help should omit %q:\n%s", notWant, view)
		}
	}
}

func TestSessionNavigatorHelpToggleDoesNotStealExistingActions(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, func(string, bool) error { return nil }, nil, nil)
	m.applyHelp(SessionNavigatorHelp{ToggleKeys: []string{"x", "f1"}})

	updated, _ := sendKey(m, "x")

	om := asOverlay(updated)
	if got := om.state; got != stateConfirmDelete {
		t.Fatalf("x state = %v, want %v", got, stateConfirmDelete)
	}

	if om.helpExpanded {
		t.Fatal("x delete should not toggle help")
	}

	updated, _ = sendWindowSize(om, 80, 24)

	view := ansi.Strip(asOverlay(updated).View().Content)
	if strings.Contains(view, "x help") {
		t.Fatalf("claimed x key should not be advertised as help:\n%s", view)
	}
}

func TestSessionNavigatorHelpToggleDoesNotStealListNavigation(t *testing.T) {
	for _, navKey := range []string{"g", "G", "home", "end", "pgup", "pgdown", "b", "u", "f", "d"} {
		t.Run(navKey, func(t *testing.T) {
			m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
			m.applyHelp(SessionNavigatorHelp{ToggleKeys: []string{navKey, "f1"}})

			updated, _ := sendWindowSize(m, 80, 24)
			om := asOverlay(updated)

			view := ansi.Strip(om.View().Content)
			if strings.Contains(view, navKey+" help") {
				t.Fatalf("claimed list-navigation key should not be advertised as help:\n%s", view)
			}

			if !strings.Contains(view, "f1 help") {
				t.Fatalf("help should fall back to f1 when %s is claimed by list navigation:\n%s", navKey, view)
			}

			updated, _ = sendNavigatorKey(om, navKey)
			om = asOverlay(updated)

			if om.helpExpanded {
				t.Fatalf("%s list navigation should not toggle help", navKey)
			}

			updated, _ = sendF1(om)
			om = asOverlay(updated)

			if !om.helpExpanded {
				t.Fatalf("f1 should toggle help when %s is claimed by list navigation", navKey)
			}
		})
	}
}

func sendNavigatorKey(m tea.Model, keyName string) (tea.Model, tea.Cmd) {
	switch keyName {
	case "home":
		return m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	case "end":
		return m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	case "pgup":
		return m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	case "pgdown":
		return m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	default:
		return sendKey(m, keyName)
	}
}

func TestSessionNavigatorHelpToggleKeepsConfiguredQuestionMarkSearch(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.applyKeys(SessionNavigatorKeys{Search: "?"})

	updated, _ := sendKey(m, "?")
	om := asOverlay(updated)

	if got := om.state; got != stateFilter {
		t.Fatalf("configured ? search state = %v, want %v", got, stateFilter)
	}

	if om.helpExpanded {
		t.Fatal("configured ? search should not toggle help")
	}

	m = newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.applyKeys(SessionNavigatorKeys{Search: "?"})
	updated, _ = sendF1(m)

	om = asOverlay(updated)
	if !om.helpExpanded {
		t.Fatal("f1 should expand help when ? is configured for search")
	}

	updated, _ = sendWindowSize(om, 80, 24)

	view := ansi.Strip(asOverlay(updated).View().Content)
	if !strings.Contains(view, "f1 hide") {
		t.Fatalf("expanded help should advertise f1 when ? is configured:\n%s", view)
	}
}

func TestOverlayExpandedHelpReserveTracksWrappedLines(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.applyHelp(SessionNavigatorHelp{
		ExpandedActions: []string{
			"restart_menu",
			"restart_menu",
			"restart_menu",
			"restart_menu",
			"restart_menu",
			"restart_menu",
			"restart_menu",
			"restart_menu",
		},
		ExpandedByDefault: true,
	})

	updated, _ := sendWindowSize(m, 50, 24)
	om := asOverlay(updated)

	lines := om.expandedNavigatorHelpLines(om.panelInnerWidth())
	if len(lines) < 4 {
		t.Fatalf("test setup produced %d wrapped help lines, want at least 4: %v", len(lines), lines)
	}

	wantHeight := min(len(om.list.Items())+4, om.height-(12+len(lines)))
	if wantHeight < 4 {
		wantHeight = 4
	}

	if got := om.list.Height(); got != wantHeight {
		t.Fatalf("list height = %d, want %d for %d wrapped help lines", got, wantHeight, len(lines))
	}
}

func TestView_ShowsDetailLine(t *testing.T) {
	sessions := overlayTestSessions()
	sessions[0].BaseBranch = "main"
	sessions[0].WorktreePath = "/tmp/test-worktree"
	m := newOverlayModel(sessions, "s1", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 120, 40)
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
	updated, _ := sendWindowSize(m, 120, 40)
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

func TestView_ConfirmDeleteWideDetailLongStatusKeepsPrompt(t *testing.T) {
	sessions := []protocol.SessionInfo{{
		ID:          "braw-delete-status",
		Name:        "braw-delete-status",
		RepoName:    "graith",
		Agent:       "codex",
		Status:      "running",
		SummaryText: strings.Repeat("checking navigator delete confirmation status wrapping ", 40),
		Dirty:       true,
		CreatedAt:   time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
	}}
	m := newOverlayModel(sessions, "braw-delete-status", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 160, 24)
	updated, _ = sendKey(asOverlay(updated), "x")
	view := ansi.Strip(asOverlay(updated).View().Content)

	for _, want := range []string{
		"Session has unsaved work",
		"Delete 'braw-delete-status'? [y/N]",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("delete confirmation should preserve %q with wide details and long status:\n%s", want, view)
		}
	}

	if strings.Contains(view, "Status:") {
		t.Fatalf("delete confirmation should not spend prompt space on the wide status block:\n%s", view)
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

func TestView_FilterModeKeepsLongQueryVisibleAtCompactWidth(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	updated, _ := sendWindowSize(m, 80, 24)
	updated, _ = sendKey(asOverlay(updated), "/")

	query := "abcdefghijklmnopqrstuvwxy0123456789ABCDEFGHIJKLMNOPQRSTUVWX"
	for _, ch := range query {
		updated, _ = sendKey(asOverlay(updated), string(ch))
	}

	view := ansi.Strip(asOverlay(updated).View().Content)
	if !strings.Contains(view, "Filter: ") {
		t.Fatalf("filter mode should show filter prompt:\n%s", view)
	}

	if !strings.Contains(view, "OPQRSTUVWX") {
		t.Fatalf("compact filter mode should keep the query tail visible:\n%s", view)
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
	m.view = viewRepo
	m.rebuildForView()
	// Force cursor onto a group header
	m.list.Select(0)

	cmd := m.fetchPreviewCmd()
	if cmd != nil {
		t.Error("fetchPreviewCmd should return nil when a group header is selected")
	}
}

// --- SessionNavigatorResult construction ---

func TestSessionNavigatorResult_Attach(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	selected := m.list.SelectedItem().(sessionItem)

	updated, _ := sendSpecialKey(m, tea.KeyEnter)
	result := sessionNavigatorResultFromModel(asOverlay(updated))

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

func TestSessionNavigatorResult_Delete_StaysOpen(t *testing.T) {
	deletedID := ""
	deleteFn := func(sid string, children bool) error {
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

	if len(om.allSessions) != len(sessions) {
		t.Errorf("sessions before authoritative refresh = %d, want %d", len(om.allSessions), len(sessions))
	}

	if _, ok := om.list.SelectedItem().(sessionItem); !ok {
		t.Error("selection should remain usable while refresh is pending")
	}
}

func TestSessionNavigatorResultFromModel_Delete(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	selected := m.list.SelectedItem().(sessionItem)
	info := selected.info

	m.selected = &info
	m.state = stateConfirmDelete

	result := sessionNavigatorResultFromModel(m)
	if result.Action != "delete" {
		t.Fatalf("action = %q, want delete", result.Action)
	}

	if result.SessionID != selected.info.ID {
		t.Fatalf("session ID = %q, want %q", result.SessionID, selected.info.ID)
	}
}

func TestSessionNavigatorResult_Quit(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.collapsed["s1"] = true

	updated, _ := sendKey(m, "q")
	result := sessionNavigatorResultFromModel(asOverlay(updated))

	if result == nil {
		t.Fatal("quitting without selection should still return persisted Navigator state")
	}

	if result.Action != "" {
		t.Errorf("action = %q, want empty dismissal action", result.Action)
	}

	if !result.Collapsed["s1"] {
		t.Error("dismissed Navigator result should preserve collapsed state")
	}
}

func TestSessionNavigatorResultFromModel_CreateCopiesLabels(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", nil, nil, nil, nil)
	m.createDone = true
	m.createName = "braw"
	m.createRepoPath = "/tmp/croft"
	m.createAgent = "codex"
	m.createLabels = []string{"canny"}

	result := sessionNavigatorResultFromModel(m)
	if result.Action != "create" {
		t.Fatalf("action = %q, want create", result.Action)
	}

	if result.CreateName != "braw" || result.CreateRepoPath != "/tmp/croft" || result.CreateAgent != "codex" {
		t.Fatalf("create result = %+v, want submitted create fields", result)
	}

	m.createLabels[0] = "mutated"

	if got := strings.Join(result.CreateLabels, ","); got != "canny" {
		t.Fatalf("create labels = %q, want defensive copy canny", got)
	}
}

func TestSessionNavigatorResultFromModel_StoppedCurrent(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "s1", nil, nil, nil, nil)
	m.stoppedCurrent = true

	result := sessionNavigatorResultFromModel(m)
	if result.Action != "stopped-current" {
		t.Fatalf("action = %q, want stopped-current", result.Action)
	}

	if result.SessionID != "s1" {
		t.Fatalf("session ID = %q, want current session s1", result.SessionID)
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

func TestCompactDelegate_RenderCompactLabelsOutsideLabelView(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID:        "braw",
			Name:      "braw",
			RepoName:  "croft",
			Status:    "running",
			Labels:    []string{"strath", "bothy", "canny", "dreich"},
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}

	line := ansi.Strip(renderItem(sessions, "", 1))
	for _, want := range []string{"braw", "strath", "+3"} {
		if !strings.Contains(line, want) {
			t.Errorf("rendered row missing %q:\n%s", want, line)
		}
	}

	for _, absent := range []string{"bothy", "canny", "dreich"} {
		if strings.Contains(line, absent) {
			t.Errorf("rendered row should not include overflow label %q:\n%s", absent, line)
		}
	}
}

func TestCompactDelegate_RenderCompactLabelsInAllView(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID:        "braw",
			Name:      "braw",
			RepoName:  "croft",
			Status:    "running",
			Labels:    []string{"cli", "ui"},
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}

	items := buildViewItems(viewAll, sessions, nil)
	cols := computeColumnWidths(sessions, "")
	cols.name = maxSessionNameWidthFromItems(items, cols.name)
	cols.treeIndent = maxTreeIndentFromItems(items)
	cols.labels = maxCompactSessionLabelWidthFromItems(items)
	d := compactDelegate{cols: cols}
	l := list.New(items, d, 120, 10)
	l.Select(0)

	var buf strings.Builder
	d.Render(&buf, l, 0, items[0])
	line := ansi.Strip(buf.String())

	for _, want := range []string{"braw", "croft", "cli", "ui"} {
		if !strings.Contains(line, want) {
			t.Fatalf("all view row missing %q:\n%s", want, line)
		}
	}
}

func TestCompactDelegate_SuppressesCompactLabelsInLabelView(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID:        "braw",
			Name:      "braw",
			RepoName:  "croft",
			Status:    "running",
			Labels:    []string{"strath", "bothy"},
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}

	items := buildLabelGroupedItems(sessions, nil)
	cols := computeColumnWidths(sessions, "")
	cols.name = maxSessionNameWidthFromItems(items, cols.name)
	cols.treeIndent = maxTreeIndentFromItems(items)
	cols.labels = maxCompactSessionLabelWidthFromItems(items)
	d := compactDelegate{cols: cols}
	l := list.New(items, d, 120, 10)
	l.Select(1)

	var buf strings.Builder
	d.Render(&buf, l, 1, items[1])
	line := ansi.Strip(buf.String())

	if !strings.Contains(line, "braw") || !strings.Contains(line, "croft") {
		t.Fatalf("label view row should show separate session and repo columns:\n%s", line)
	}

	if strings.Contains(line, "strath") || strings.Contains(line, "bothy") {
		t.Fatalf("label view row should not duplicate compact label chips:\n%s", line)
	}
}

func TestCompactDelegate_CompactLabelsRespectListWidth(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID:     "braw",
			Name:   "braw",
			Status: "running",
			Labels: []string{
				"strath-label-for-dreich-weather",
				"bothy-label-for-canny-roof",
				"thrawn",
				"bairn",
				"blether",
			},
			CreatedAt: time.Now().Format(time.RFC3339),
		},
	}

	items := buildGroupedItems(sessions, nil)
	cols := computeColumnWidths(sessions, "")
	cols.name = maxSessionNameWidthFromItems(items, cols.name)
	cols.treeIndent = maxTreeIndentFromItems(items)
	cols.labels = maxCompactSessionLabelWidthFromItems(items)
	d := compactDelegate{cols: cols}
	l := list.New(items, d, 36, 10)

	var buf strings.Builder
	d.Render(&buf, l, 1, items[1])
	line := buf.String()

	if strings.Contains(line, "\n") {
		t.Fatalf("compact labels should keep the row single-line, got:\n%q", line)
	}

	if width := lipgloss.Width(line); width > l.Width() {
		t.Fatalf("compact label row width = %d, want <= %d:\n%s", width, l.Width(), ansi.Strip(line))
	}

	stripped := ansi.Strip(line)
	for _, want := range []string{"braw", "running"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("compact labels should preserve %q in a narrow row:\n%s", want, stripped)
		}
	}
}

func TestRenderCompactSessionLabelsUsesColoredChips(t *testing.T) {
	chips := compactSessionLabelChips([]string{"strath", "bothy", "canny"})
	width := labelChipSequenceWidth(chips)

	rendered := renderCompactSessionLabels(chips, width)
	stripped := ansi.Strip(rendered)

	for _, want := range []string{"strath", "+2"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("compact labels should render %q, got %q", want, stripped)
		}
	}

	if !strings.Contains(rendered, renderLabelChip(chips[0])) {
		t.Fatalf("compact labels should render the first label as a coloured chip, got %q", rendered)
	}

	if got := lipgloss.Width(rendered); got != width {
		t.Fatalf("compact labels width = %d, want %d", got, width)
	}
}

func TestLabelChipColorsAreStableByLabelIdentity(t *testing.T) {
	tests := map[string]struct {
		a string
		b string
	}{
		"trim and case": {
			a: "Strath",
			b: " strath ",
		},
		"micro sign and greek mu": {
			a: "µ",
			b: "μ",
		},
		"long s and ascii s": {
			a: "ſ",
			b: "s",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var (
				a = strings.TrimSpace(test.a)
				b = strings.TrimSpace(test.b)
			)

			if !sessionlabel.Equal(a, b) {
				t.Fatalf("test labels %q and %q should share label identity", test.a, test.b)
			}

			fg, bg := labelChipColors(test.a)

			againFG, againBG := labelChipColors(test.b)
			if fg != againFG || bg != againBG {
				t.Fatalf("label chip colors should be stable for label identity %q/%q", test.a, test.b)
			}
		})
	}
}

func TestLabelChipPaletteForegroundsAreReadable(t *testing.T) {
	backgrounds := append([]color.Color(nil), labelChipPalette...)
	backgrounds = append(backgrounds, colorLabelChipOverflow)

	for i, bg := range backgrounds {
		t.Run(fmt.Sprintf("background_%02d", i), func(t *testing.T) {
			fg := labelChipForeground(bg)
			if ratio := colorContrastRatio(fg, bg); ratio < 4.5 {
				t.Fatalf("label chip contrast ratio = %.2f, want at least 4.5", ratio)
			}
		})
	}
}

func TestAppendDetailLabelChipsUsesColoredLegend(t *testing.T) {
	var b strings.Builder

	appendDetailLabelChips(&b, []string{"strath", "bothy"}, 42)

	rendered := b.String()
	if !strings.Contains(rendered, renderLabelChip(newLabelChip("strath", selectedDetailLabelMaxWidth))) {
		t.Fatalf("detail labels should render the first label as a coloured chip, got %q", rendered)
	}

	stripped := ansi.Strip(rendered)
	for _, want := range []string{"Labels:", "strath", "bothy"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("detail labels should include %q, got %q", want, stripped)
		}
	}
}

func TestAppendDetailLabelChipsCapsWrappedLegend(t *testing.T) {
	var b strings.Builder

	appendDetailLabelChips(&b, []string{
		"strath",
		"bothy",
		"canny",
		"dreich",
		"blether",
		"thrawn",
		"bairn",
		"croft",
		"haar",
	}, 28)

	lines := strings.Split(strings.TrimPrefix(b.String(), "\n"), "\n")
	if len(lines) > selectedDetailLabelMaxLines {
		t.Fatalf("detail label legend line count = %d, want <= %d:\n%s", len(lines), selectedDetailLabelMaxLines, ansi.Strip(b.String()))
	}

	for _, line := range lines {
		if width := lipgloss.Width(line); width > 28 {
			t.Fatalf("detail label legend line width = %d, want <= 28: %q", width, ansi.Strip(line))
		}
	}

	if stripped := ansi.Strip(b.String()); !strings.Contains(stripped, "+") {
		t.Fatalf("capped detail label legend should include overflow chip, got %q", stripped)
	}
}

func TestCompactDelegate_RenderConfigStaleMarkerOnlyWhenActionable(t *testing.T) {
	tests := map[string]struct {
		status string
		want   bool
	}{
		"running stale session shows marker": {
			status: "running",
			want:   true,
		},
		"stopped stale session suppresses marker": {
			status: "stopped",
			want:   false,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			sessions := []protocol.SessionInfo{{
				ID:          "braw",
				Name:        "braw",
				RepoName:    "graith",
				Agent:       "claude",
				Status:      test.status,
				ConfigStale: true,
			}}

			line := ansi.Strip(renderItem(sessions, "", 1))
			if got := strings.Contains(line, "↻"); got != test.want {
				t.Errorf("stale marker present = %v, want %v in %q", got, test.want, line)
			}
		})
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

func TestSelectedRowDefaultStyleContrast(t *testing.T) {
	tests := map[string]struct {
		foreground color.Color
		background color.Color
		minRatio   float64
	}{
		"background stands out from panel": {
			foreground: colorSelectBg,
			background: colorPanel,
			minRatio:   2,
		},
		"default foreground readable on selection": {
			foreground: colorSelectFg,
			background: colorSelectBg,
			minRatio:   4.5,
		},
		"dim foreground readable on selection": {
			foreground: colorSelectDim,
			background: colorSelectBg,
			minRatio:   4.5,
		},
		"red semantic foreground readable on selection": {
			foreground: colorSelectRed,
			background: colorSelectBg,
			minRatio:   4.5,
		},
		"blue semantic foreground readable on selection": {
			foreground: colorSelectBlue,
			background: colorSelectBg,
			minRatio:   4.5,
		},
		"green semantic foreground readable on selection": {
			foreground: colorGreen,
			background: colorSelectBg,
			minRatio:   4.5,
		},
		"gold semantic foreground readable on selection": {
			foreground: colorGold,
			background: colorSelectBg,
			minRatio:   4.5,
		},
		"yellow semantic foreground readable on selection": {
			foreground: colorYellow,
			background: colorSelectBg,
			minRatio:   4.5,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := colorContrastRatio(test.foreground, test.background)
			if got < test.minRatio {
				t.Errorf("contrast ratio = %.2f, want at least %.2f", got, test.minRatio)
			}
		})
	}
}

// TestHighlightSelectedRow guards the core reset-reopen mechanism directly: a
// Navigator row is built from columns that each end in a full SGR reset, which
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

func TestColumnWidths_TotalWidthIncludesCompactLabels(t *testing.T) {
	cw := columnWidths{name: 10, labels: 12, trailing: map[string]int{
		"status": 8, "summary": 15, "git": 5, "pr": 6, "review": 6, "output": 4,
	}}

	got := cw.totalWidth()
	if got != 93 {
		t.Errorf("totalWidth() with compact labels = %d, want 93", got)
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

// --- viewMode cycling ---

func TestViewModeCycling(t *testing.T) {
	if got := strings.Join(viewNames, ","); got != "All,Repo,Starred,Labels,Scenarios,Deleted" {
		t.Fatalf("viewNames = %q, want All / Repo / Starred / Labels / Scenarios / Deleted", got)
	}

	v := viewAll

	v = v.next()
	if v != viewRepo {
		t.Errorf("All.next() = %d, want viewRepo", v)
	}

	v = v.next()
	if v != viewStarred {
		t.Errorf("Repo.next() = %d, want viewStarred", v)
	}

	v = v.next()
	if v != viewLabels {
		t.Errorf("Starred.next() = %d, want viewLabels", v)
	}

	v = v.next()
	if v != viewScenario {
		t.Errorf("Labels.next() = %d, want viewScenario", v)
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
	if om.view != viewRepo {
		t.Errorf("after right: view = %d, want viewRepo", om.view)
	}

	updated, _ = sendKey(updated, "right")

	om = asOverlay(updated)
	if om.view != viewStarred {
		t.Errorf("after 2x right: view = %d, want viewStarred", om.view)
	}

	updated, _ = sendKey(updated, "right")

	om = asOverlay(updated)
	if om.view != viewLabels {
		t.Errorf("after 3x right: view = %d, want viewLabels", om.view)
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

func TestOverlayDeletedFilterRestoresInsteadOfAttaching(t *testing.T) {
	m := newOverlayModel(overlayTestSessions(), "", noopFetchPreview, nil, nil, nil)
	m.deletedSessions = []protocol.SessionInfo{
		{ID: "dreich", Name: "dreich", Status: "stopped", DeletedAt: "2026-07-10T10:00:00Z", DeleteExpiresAt: "2026-07-11T10:00:00Z"},
	}

	var restored string

	m.restoreSession = func(id string) error { restored = id; return nil }

	updated, _ := sendWindowSize(m, 120, 40)
	updated, _ = sendKey(updated, "left")
	updated, _ = sendKey(updated, "/")
	updated, _ = updated.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})

	om := asOverlay(updated)

	view := ansi.Strip(om.View().Content)
	if !strings.Contains(view, "enter restore") {
		t.Fatalf("deleted filter footer should advertise restore:\n%s", view)
	}

	if strings.Contains(view, "enter attach") {
		t.Fatalf("deleted filter footer should not advertise attach:\n%s", view)
	}

	updated, cmd := sendKey(om, "enter")
	om = asOverlay(updated)

	if om.selected != nil {
		t.Fatal("enter in deleted filter must not select/attach")
	}

	if cmd == nil {
		t.Fatal("enter in deleted filter should return restore command")
	}

	if msg := cmd(); msg != nil {
		_, _ = om.Update(msg)
	}

	if restored != "dreich" {
		t.Fatalf("restore hook got %q, want dreich", restored)
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

func TestOverlay_FilterRespectsView(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "braw-api", RepoName: "repo", Status: "running", Starred: true,
			CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "s2", Name: "thrawn-api", RepoName: "repo", Status: "running",
			CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "s3", Name: "thrawn-ui", RepoName: "repo", Status: "running", Starred: true,
			CreatedAt: time.Now().Format(time.RFC3339)},
	}

	var updated tea.Model

	updated, _ = sendWindowSize(newOverlayModel(sessions, "", nil, nil, nil, nil), 120, 40)

	// Switch to Starred.
	updated, _ = sendKey(updated, "right")
	updated, _ = sendKey(updated, "right")

	om := asOverlay(updated)
	if om.view != viewStarred {
		t.Fatalf("view = %d, want viewStarred", om.view)
	}

	// Enter filter mode and type "api"
	updated, _ = sendKey(updated, "/")
	for _, ch := range "api" {
		updated, _ = updated.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	om = asOverlay(updated)
	sessionCount := countSessionItems(om)
	// Only the starred braw-api session matches both filters.
	if sessionCount != 1 {
		t.Errorf("filtered starred view has %d sessions, want 1", sessionCount)
	}
}

func TestOverlay_FilterEscRebuildsView(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "s1", Name: "braw-working", RepoName: "repo", Status: "running", Starred: true,
			CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "s2", Name: "thrawn-working", RepoName: "repo", Status: "running",
			CreatedAt: time.Now().Format(time.RFC3339)},
	}

	var updated tea.Model

	updated, _ = sendWindowSize(newOverlayModel(sessions, "", nil, nil, nil, nil), 120, 40)

	// Switch to Starred.
	updated, _ = sendKey(updated, "right")
	updated, _ = sendKey(updated, "right")

	// Enter filter, type something, then cancel
	updated, _ = sendKey(updated, "/")
	updated, _ = updated.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	updated, _ = sendKey(updated, "esc")

	om := asOverlay(updated)
	// The text filter clears without changing the selected Navigator view.
	if om.view != viewStarred {
		t.Errorf("view = %d after filter cancel, want viewStarred", om.view)
	}

	sessionCount := countSessionItems(om)

	if sessionCount != 1 {
		t.Errorf("after filter cancel: %d sessions, want 1 starred session", sessionCount)
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
		{"draft pending", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 9, State: "draft"}, CI: &protocol.CIInfo{State: "pending"}}, "#9 D ·"},
		// The review decision is now its own column (displayReview); displayPR must
		// NOT append it, so the PR/CI token colour never bleeds onto the review glyph.
		{"review omitted from PR token (approved)", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open", ReviewDecision: "approved"}, CI: &protocol.CIInfo{State: "passing"}}, "#56 ✓"},
		{"review omitted from PR token (changes)", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open", ReviewDecision: "changes_requested"}, CI: &protocol.CIInfo{State: "failing"}}, "#56 ✗"},
		{"review omitted, no CI", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open", ReviewDecision: "review_required"}}, "#56"},
		{"review omitted with conflict", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 56, State: "open", Conflicting: true, ReviewDecision: "changes_requested"}, CI: &protocol.CIInfo{State: "passing"}}, "#56 ⚠"},
		{"merged", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 583, State: "merged", ReviewDecision: "approved"}}, "#583 merged"},
		{"closed", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 584, State: "closed", ReviewDecision: "changes_requested"}}, "#584 closed"},
		{"draft omits review", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 9, State: "draft", ReviewDecision: "review_required"}, CI: &protocol.CIInfo{State: "pending"}}, "#9 D ·"},
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

func TestRenderPRCellHighlightsDraftMarker(t *testing.T) {
	info := protocol.SessionInfo{
		PullRequest: &protocol.PRInfo{Number: 9, State: "draft"},
		CI:          &protocol.CIInfo{State: "pending"},
	}

	const width = 8

	got := renderTUIColumnCell(prTUIColumn(t), info, width)

	wantPlain := pad("#9 D ·", width)
	if stripped := ansi.Strip(got); stripped != wantPlain {
		t.Fatalf("renderPRCell stripped = %q, want %q", stripped, wantPlain)
	}

	if draftPRMarkerStyle().GetUnderline() {
		t.Fatal("draft marker style should not use underline")
	}

	wantMarker := draftPRMarkerStyle().Render(draftPRMarker)
	if !strings.Contains(got, wantMarker) {
		t.Fatalf("draft marker should be amber and bold; rendered cell %q does not contain marker %q", got, wantMarker)
	}

	selected := highlightSelectedRow(got, width)
	if stripped := ansi.Strip(selected); stripped != wantPlain {
		t.Fatalf("selected renderPRCell stripped = %q, want %q", stripped, wantPlain)
	}

	if !strings.Contains(selected, wantMarker) {
		t.Fatalf("selected draft marker should retain draft styling; rendered cell %q does not contain marker %q", selected, wantMarker)
	}
}

func TestRenderPRCellNonDraftKeepsSinglePRStyle(t *testing.T) {
	info := protocol.SessionInfo{
		PullRequest: &protocol.PRInfo{Number: 9, State: "open"},
		CI:          &protocol.CIInfo{State: "passing"},
	}

	const width = 8

	col := prTUIColumn(t)
	got := renderTUIColumnCell(col, info, width)

	wantPlain := pad("#9 ✓", width)
	if stripped := ansi.Strip(got); stripped != wantPlain {
		t.Fatalf("renderPRCell stripped = %q, want %q", stripped, wantPlain)
	}

	wantRendered := col.TUIStyle(info).Render(wantPlain)
	if got != wantRendered {
		t.Fatalf("non-draft PR cell should keep the single PR style:\n got %q\nwant %q", got, wantRendered)
	}

	unexpectedMarker := draftPRMarkerStyle().Render(draftPRMarker)
	if strings.Contains(got, unexpectedMarker) {
		t.Fatalf("non-draft PR cell should not include the draft marker style, got %q", got)
	}
}

func prTUIColumn(t *testing.T) SessionColumn {
	t.Helper()

	for _, c := range tuiColumns() {
		if c.Key == "pr" {
			return c
		}
	}

	t.Fatal("PR TUI column not found")

	return SessionColumn{}
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
	// plain open PR. Overlay rows render the draft marker more strongly, while
	// the status bar keeps this compact suffix (#776).
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

func TestComputeColumnWidthsIncludesTUIHeaders(t *testing.T) {
	cols := computeColumnWidths(nil, "")

	for _, c := range tuiColumns() {
		if got, want := cols.col(c.Key), lipgloss.Width(c.Header); got < want {
			t.Errorf("column %q width = %d, want at least header width %d", c.Key, got, want)
		}
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
	if got := displayPR(s); got != "#9 D ✓" {
		t.Errorf("displayPR draft+passing = %q, want #9 D ✓", got)
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

// TestDisplaySummary_UTF8Safe is the regression test for issue #1313: the old
// code measured display width with lipgloss.Width but truncated with a byte
// slice (text[:summaryWidth-1]), so a summary containing wide/combining/emoji or
// ANSI-decorated text would be cut mid-rune, producing invalid UTF-8 / mojibake.
// Truncation must be cell-width aware and always yield valid UTF-8.
func TestDisplaySummary_UTF8Safe(t *testing.T) {
	cases := map[string]string{
		"emoji":     strings.Repeat("🎉", 80),
		"accented":  strings.Repeat("café ", 40),
		"combining": strings.Repeat("é", 80),
		"cjk":       strings.Repeat("世界", 80),
		"ansi":      "\x1b[31m" + strings.Repeat("hello ", 40) + "\x1b[0m",
	}

	for name, text := range cases {
		got := displaySummary(protocol.SessionInfo{SummaryText: text})

		if !utf8.ValidString(got) {
			t.Errorf("%s: displaySummary produced invalid UTF-8: %q", name, got)
		}

		// The visible cell width (ANSI escapes excluded) must never exceed the
		// configured budget — the whole point of a cell-width-aware truncation.
		if w := ansi.StringWidth(got); w > summaryWidth {
			t.Errorf("%s: rendered width = %d cells, want <= %d; got %q", name, w, summaryWidth, got)
		}
	}
}

// TestDisplaySummary_CJKWidth confirms wide runes are measured in cells, not
// bytes: 40 CJK runes render as 80 cells, so displaySummary must truncate them
// to fit summaryWidth cells and append the ellipsis (issue #1313). The old byte
// slice would have kept far too much content (and split a rune).
func TestDisplaySummary_CJKWidth(t *testing.T) {
	got := displaySummary(protocol.SessionInfo{SummaryText: strings.Repeat("世", maxSummaryWidth)})

	if w := ansi.StringWidth(got); w > maxSummaryWidth {
		t.Errorf("CJK summary width = %d cells, want <= %d", w, maxSummaryWidth)
	}

	if !strings.HasSuffix(got, "…") {
		t.Errorf("wide-rune summary should be truncated with an ellipsis, got %q", got)
	}

	if !utf8.ValidString(got) {
		t.Errorf("CJK summary produced invalid UTF-8: %q", got)
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

func TestBuildScenarioGroupedItemsPreservesNestedCrossRepoTree(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "ben", RepoName: "croft", Status: "running", ScenarioID: "sc", ScenarioName: "strath"},
		{ID: "child", Name: "bairn", ParentID: "root", RepoName: "bothy", Status: "running", ScenarioID: "sc", ScenarioName: "strath"},
		{ID: "grandchild", Name: "wee-bairn", ParentID: "child", RepoName: "glen", Status: "running", ScenarioID: "sc", ScenarioName: "strath"},
	}

	items := buildScenarioGroupedItems(sessions, nil)

	group := sessionItemsForGroup(t, items, "strath")
	if len(group) != 3 {
		t.Fatalf("scenario group has %d sessions, want 3", len(group))
	}

	wantIDs := []string{"root", "child", "grandchild"}

	wantPrefixes := []string{"", "└── ", "    └── "}
	for i := range group {
		if group[i].info.ID != wantIDs[i] || group[i].treePrefix != wantPrefixes[i] {
			t.Errorf("scenario[%d] = %q prefix %q, want %q prefix %q", i, group[i].info.ID, group[i].treePrefix, wantIDs[i], wantPrefixes[i])
		}
	}

	collapsed := buildScenarioGroupedItems(sessions, map[string]bool{"root": true})

	collapsedGroup := sessionItemsForGroup(t, collapsed, "strath")
	if len(collapsedGroup) != 1 || !collapsedGroup[0].collapsed || collapsedGroup[0].descendantCount != 2 {
		t.Fatalf("collapsed scenario group = %+v, want root with two descendants", collapsedGroup)
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

func TestUpdate_DeleteResultRefreshesAuthoritativeState(t *testing.T) {
	sessions := overlayTestSessions()
	m := newOverlayModel(sessions, "", noopFetchPreview, nil, nil, nil)
	m.width, m.height = 120, 40

	updated, _ := m.Update(deleteResultMsg{sessionID: "s1"})
	om := asOverlay(updated)

	if len(om.allSessions) != len(sessions) {
		t.Errorf("allSessions before refresh = %d, want %d", len(om.allSessions), len(sessions))
	}

	updated, _ = om.Update(refreshSessionsMsg{sessions: sessions[1:]})
	if got := len(asOverlay(updated).allSessions); got != len(sessions)-1 {
		t.Errorf("allSessions after refresh = %d, want %d", got, len(sessions)-1)
	}
}

func TestUpdate_DeleteResultRefreshPreservesMiddleSelectionPosition(t *testing.T) {
	sessions := overlayTestSessions()
	m := sizedModel(t, sessions, "")
	m.selectSessionByID("s1")
	requireSelectedSessionID(t, m, "s1")

	updated, _ := m.Update(deleteResultMsg{sessionID: "s1"})
	remaining := []protocol.SessionInfo{sessions[1], sessions[2]}
	updated, _ = asOverlay(updated).Update(refreshSessionsMsg{sessions: remaining})

	requireSelectedSessionID(t, asOverlay(updated), "s2")
}

func TestUpdate_DeleteResultRefreshPreservesFinalSelectionPosition(t *testing.T) {
	sessions := overlayTestSessions()
	m := sizedModel(t, sessions, "")
	m.selectSessionByID("s2")
	requireSelectedSessionID(t, m, "s2")

	if got, want := m.list.Index(), len(m.list.Items())-1; got != want {
		t.Fatalf("selected index = %d, want final item index %d", got, want)
	}

	updated, _ := m.Update(deleteResultMsg{sessionID: "s2"})
	remaining := []protocol.SessionInfo{sessions[0], sessions[2]}
	updated, _ = asOverlay(updated).Update(refreshSessionsMsg{sessions: remaining})

	requireSelectedSessionID(t, asOverlay(updated), "s1")
}

func TestUpdate_DeleteResultRefreshPreservesGroupedSelectionPosition(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "braw", Name: "braw", RepoName: "bothy", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "canny", Name: "canny", RepoName: "bothy", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "dreich", Name: "dreich", RepoName: "croft", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	m := sizedModel(t, sessions, "")
	m.view = viewRepo
	m.rebuildForView()
	m.selectSessionByID("canny")
	requireSelectedSessionID(t, m, "canny")

	updated, _ := m.Update(deleteResultMsg{sessionID: "canny"})
	remaining := []protocol.SessionInfo{sessions[0], sessions[2]}
	updated, _ = asOverlay(updated).Update(refreshSessionsMsg{sessions: remaining})

	requireSelectedSessionID(t, asOverlay(updated), "braw")
}

func TestUpdate_DeleteResultLastSessionRefreshesToEmpty(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "only", Name: "neep", RepoName: "repo", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	m := newOverlayModel(sessions, "", noopFetchPreview, nil, nil, nil)
	m.width, m.height = 120, 40

	updated, cmd := m.Update(deleteResultMsg{sessionID: "only"})
	if cmd == nil {
		t.Fatal("deleting the last session should return a command")
	}

	_ = cmd

	updated, _ = asOverlay(updated).Update(refreshSessionsMsg{sessions: []protocol.SessionInfo{}})

	if got := countSessionItems(asOverlay(updated)); got != 0 {
		t.Errorf("sessions after refresh = %d, want 0", got)
	}

	if _, ok := asOverlay(updated).list.SelectedItem().(sessionItem); ok {
		t.Fatal("empty refresh should not leave a session selected")
	}
}

func TestUpdate_DeleteResultErrorStaysActionable(t *testing.T) {
	m := sizedModel(t, overlayTestSessions(), "")
	m.state = stateConfirmDelete

	updated, _ := m.Update(deleteResultMsg{sessionID: "s1", err: errFake})
	om := asOverlay(updated)

	if om.state != stateConfirmDelete {
		t.Errorf("delete error should keep confirmation visible, got %v", om.state)
	}

	if len(om.allSessions) != 3 {
		t.Errorf("delete error should not remove the session, got %d", len(om.allSessions))
	}

	if !strings.Contains(om.View().Content, errFake.Error()) {
		t.Errorf("delete error should be visible: %q", om.View().Content)
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

// --- View: filtered-view empty states ---

func TestView_StarredEmptyState(t *testing.T) {
	m := sizedModel(t, overlayTestSessions(), "")
	m.view = viewStarred
	m.rebuildForView()

	out := m.View().Content
	if !strings.Contains(out, "No starred sessions") {
		t.Errorf("empty starred view should show its empty message:\n%s", out)
	}
}

func TestView_ScenarioEmptyState(t *testing.T) {
	m := sizedModel(t, nil, "")
	m.view = viewScenario
	m.rebuildForView()

	out := m.View().Content
	if !strings.Contains(out, "No sessions") {
		t.Errorf("empty scenario view should show its empty message:\n%s", out)
	}
}

func TestView_DeletedEmptyState(t *testing.T) {
	m := sizedModel(t, overlayTestSessions(), "")
	m.view = viewDeleted
	m.rebuildForView()

	out := m.View().Content
	if !strings.Contains(out, "No deleted sessions") {
		t.Errorf("empty deleted view should show its empty message:\n%s", out)
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
	sessions := overlayTestSessions()
	sessions[0].ConfigStale = true
	sessions[1].ConfigStale = true // stopped sessions are not counted as outdated

	m := sizedModel(t, sessions, "")
	m.restartSession = func(string) error { return nil }
	m.state = stateRestartMenu

	out := m.View().Content
	for _, want := range []string{"Restart:", "[a]ll (3)", "[o]utdated (1)", "[s]topped (1)"} {
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
			"draft passing adds spaced D and check",
			protocol.SessionInfo{
				PullRequest: &protocol.PRInfo{Number: 8, State: "draft"},
				CI:          &protocol.CIInfo{State: "passing"},
			},
			"#8 D ✓",
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
