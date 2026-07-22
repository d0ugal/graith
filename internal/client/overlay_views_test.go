package client

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

func pickerViewSessions() []protocol.SessionInfo {
	return []protocol.SessionInfo{
		{ID: "ben", Name: "ben", SystemKind: "orchestrator", Status: "running"},
		{ID: "bairn", ParentID: "ben", Name: "bairn", RepoName: "croft", Status: "running"},
		{ID: "wee-bairn", ParentID: "bairn", Name: "wee-bairn", RepoName: "bothy", Status: "running"},
		{ID: "thrawn", Name: "thrawn", RepoName: "strath", Status: "stopped"},
	}
}

func sessionItems(items []list.Item) []sessionItem {
	result := make([]sessionItem, 0, len(items))
	for _, item := range items {
		if session, ok := item.(sessionItem); ok {
			result = append(result, session)
		}
	}

	return result
}

func assertSelectedSession(t *testing.T, m *overlayModel, want string) {
	t.Helper()

	item, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		t.Fatalf("selected item = %T, want sessionItem", m.list.SelectedItem())
	}

	if item.info.ID != want {
		t.Fatalf("selected session = %q, want %q", item.info.ID, want)
	}
}

func TestAllViewBuildsOneGlobalCrossRepoTree(t *testing.T) {
	m := newOverlayModel(pickerViewSessions(), "", nil, nil, nil, nil)

	if m.view != viewAll {
		t.Fatalf("initial view = %v, want viewAll", m.view)
	}

	for _, item := range m.list.Items() {
		if _, ok := item.(groupHeader); ok {
			t.Fatal("All view must not contain repository group headers")
		}
	}

	items := sessionItems(m.list.Items())
	if len(items) != 4 {
		t.Fatalf("All session count = %d, want 4", len(items))
	}

	byID := make(map[string]sessionItem, len(items))
	for _, item := range items {
		byID[item.info.ID] = item
	}

	if got := byID["ben"].displayName(); got != "System/ben" {
		t.Errorf("system display name = %q, want System/ben", got)
	}

	if got := byID["bairn"].displayName(); got != "croft/bairn" {
		t.Errorf("cross-repo child display name = %q, want croft/bairn", got)
	}

	if got := byID["wee-bairn"].displayName(); got != "bothy/wee-bairn" {
		t.Errorf("grandchild display name = %q, want bothy/wee-bairn", got)
	}

	if byID["bairn"].treePrefix == "" || byID["wee-bairn"].treePrefix == "" {
		t.Fatalf("cross-repository descendants lost tree edges: bairn=%q wee-bairn=%q", byID["bairn"].treePrefix, byID["wee-bairn"].treePrefix)
	}

	if !byID["ben"].hasChildren || !byID["bairn"].hasChildren {
		t.Fatal("global tree should preserve system and cross-repo parent relationships")
	}
}

func TestRepoViewKeepsRepositoryGroupsAndSplitsCrossRepoEdges(t *testing.T) {
	m := newOverlayModel(pickerViewSessions(), "", nil, nil, nil, nil)
	updated, _ := sendKey(m, "right")
	m = asOverlay(updated)

	if m.view != viewRepo {
		t.Fatalf("view after right = %v, want viewRepo", m.view)
	}

	var headers []string

	for _, item := range m.list.Items() {
		if header, ok := item.(groupHeader); ok {
			headers = append(headers, header.name)
		}
	}

	if got := strings.Join(headers, ","); got != "System,bothy,croft,strath" {
		t.Fatalf("Repo headers = %q, want System,bothy,croft,strath", got)
	}

	for _, item := range sessionItems(m.list.Items()) {
		if item.treePrefix != "" {
			t.Errorf("cross-repo session %q should be a Repo-view root, prefix = %q", item.info.ID, item.treePrefix)
		}

		if strings.Contains(item.displayName(), "/") {
			t.Errorf("Repo-view display name %q should rely on its group header", item.displayName())
		}
	}
}

func TestAllRepoSwitchPreservesSelectionAndCollapseState(t *testing.T) {
	sessions := pickerViewSessions()
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	m.selectSessionByID("bairn")

	updated, _ := sendKey(m, "right")
	m = asOverlay(updated)
	assertSelectedSession(t, m, "bairn")

	updated, _ = sendKey(m, "left")
	m = asOverlay(updated)
	assertSelectedSession(t, m, "bairn")

	m.selectSessionByID("ben")
	updated, _ = sendKey(m, " ")

	m = asOverlay(updated)
	if !m.collapsed["ben"] {
		t.Fatal("system parent should be collapsed in All")
	}

	updated, _ = sendKey(m, "right")

	m = asOverlay(updated)
	if !m.collapsed["ben"] {
		t.Fatal("collapse state should survive switching to Repo")
	}

	m.selectSessionByID("bairn") // visible as a root in its repository group
	updated, _ = sendKey(m, "left")
	m = asOverlay(updated)
	assertSelectedSession(t, m, "ben") // hidden child falls back to visible parent
}

func TestAllViewRefreshPreservesSelectionAndCollapse(t *testing.T) {
	sessions := pickerViewSessions()
	collapsed := map[string]bool{"ben": true}
	m := newOverlayModel(sessions, "ben", nil, nil, collapsed, nil)

	refreshed := append([]protocol.SessionInfo{}, sessions...)
	refreshed = append(refreshed, protocol.SessionInfo{
		ID: "dreich", ParentID: "ben", Name: "dreich", RepoName: "dreich", Status: "running",
	})

	updated, _ := m.Update(refreshSessionsMsg{sessions: refreshed})
	m = asOverlay(updated)
	assertSelectedSession(t, m, "ben")

	root := m.list.SelectedItem().(sessionItem)
	if !root.collapsed || root.descendantCount != 3 {
		t.Fatalf("refreshed root collapsed=%v descendants=%d, want true/3", root.collapsed, root.descendantCount)
	}
}

func TestAllViewSearchPromotesMatchingChildAndResizesColumns(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "ben", RepoName: "croft", Status: "running"},
		{ID: "bairn", ParentID: "ben", Name: "bairn", RepoName: "a-very-long-bothy", Status: "running"},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	m.filterInput.SetValue("bairn")
	m.rebuildForView()

	items := sessionItems(m.list.Items())
	if len(items) != 1 || items[0].info.ID != "bairn" {
		t.Fatalf("filtered All items = %+v, want child only", items)
	}

	if items[0].treePrefix != "" {
		t.Errorf("child with filtered-out parent should be a root, prefix = %q", items[0].treePrefix)
	}

	wantNameWidth := lipgloss.Width("a-very-long-bothy/bairn")
	if m.cols.name < wantNameWidth {
		t.Errorf("All name width = %d, want at least %d", m.cols.name, wantNameWidth)
	}

	updated, _ := sendKey(m, "right")

	m = asOverlay(updated)
	if m.cols.name >= wantNameWidth {
		t.Errorf("Repo name width = %d, should be recomputed without repeated repo identity", m.cols.name)
	}
}

func TestAllViewRendersOrphansAndCyclesExactlyOnce(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "thrawn", ParentID: "absent", Name: "thrawn", RepoName: "croft", Status: "running"},
		{ID: "braw", ParentID: "canny", Name: "braw", RepoName: "bothy", Status: "running"},
		{ID: "canny", ParentID: "braw", Name: "canny", RepoName: "strath", Status: "running"},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)

	seen := make(map[string]int)
	for _, item := range sessionItems(m.list.Items()) {
		seen[item.info.ID]++
	}

	for _, session := range sessions {
		if seen[session.ID] != 1 {
			t.Errorf("session %q rendered %d times, want once", session.ID, seen[session.ID])
		}
	}
}

func TestAllAndRepoEmptyStatesAndHelp(t *testing.T) {
	m := newOverlayModel(nil, "", nil, nil, nil, nil)
	m.width, m.height = 160, 40

	all := m.View().Content
	if !strings.Contains(all, "No sessions") {
		t.Error("All empty state should say No sessions")
	}

	if strings.Contains(all, "tab group") {
		t.Error("All help should not advertise group navigation")
	}

	updated, _ := sendKey(m, "right")

	m = asOverlay(updated)

	repo := m.View().Content
	if !strings.Contains(repo, "No sessions") {
		t.Error("Repo empty state should say No sessions")
	}

	if !strings.Contains(repo, "tab group") {
		t.Error("Repo help should advertise group navigation")
	}
}

func TestAllAndRepoCollapseAll(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "ben", RepoName: "croft", Status: "running"},
		{ID: "bairn", ParentID: "ben", Name: "bairn", RepoName: "croft", Status: "running"},
	}
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)

	updated, _ := sendKey(m, "C")

	m = asOverlay(updated)
	if !m.collapsed["ben"] {
		t.Fatal("C should collapse parents in All")
	}

	updated, _ = sendKey(m, "right")
	m = asOverlay(updated)
	updated, _ = sendKey(m, "C")

	m = asOverlay(updated)
	if m.collapsed["ben"] {
		t.Fatal("C should expand parents in Repo when all are collapsed")
	}
}

func TestAllRepoViewOrder(t *testing.T) {
	want := []viewMode{viewAll, viewRepo, viewStarred, viewLabels, viewScenario, viewDeleted}

	view := viewAll
	for i, expected := range want {
		if view != expected {
			t.Fatalf("view step %d = %v, want %v", i, view, expected)
		}

		view = view.next()
	}

	if view != viewAll {
		t.Fatalf("view cycle ended at %v, want viewAll", view)
	}

	if got := viewAll.prev(); got != viewDeleted {
		t.Fatalf("viewAll.prev() = %v, want viewDeleted", got)
	}
}

func TestPickerStateRestoresViewAndSelection(t *testing.T) {
	sessions := append(pickerViewSessions(), protocol.SessionInfo{ID: "labelled", Name: "labelled", Labels: []string{"braw"}, Starred: true})

	for _, view := range []PickerView{PickerViewAll, PickerViewRepo, PickerViewStarred, PickerViewLabels, PickerViewScenario, PickerViewDeleted} {
		t.Run(viewNames[view], func(t *testing.T) {
			state := PickerState{View: view}
			if view != PickerViewDeleted {
				state.SessionID = "labelled"
			}

			if view == PickerViewLabels {
				state.LabelGroup = "braw"
			}

			m := newOverlayModel(sessions, "", nil, nil, nil, nil)
			m.restorePickerState(state)

			if m.view != viewMode(view) {
				t.Fatalf("view = %v, want %v", m.view, view)
			}

			if view != PickerViewDeleted {
				item, ok := m.list.SelectedItem().(sessionItem)
				if !ok || item.info.ID != "labelled" {
					t.Fatalf("selected item = %#v, want session labelled", m.list.SelectedItem())
				}

				if view == PickerViewLabels && item.labelGroup != "braw" {
					t.Fatalf("label group = %q, want braw", item.labelGroup)
				}
			}
		})
	}
}

func TestPickerStateFallsBackWhenSelectionDisappears(t *testing.T) {
	m := newOverlayModel(pickerViewSessions(), "", nil, nil, nil, nil)
	m.restorePickerState(PickerState{View: PickerViewLabels, SessionID: "dreich", LabelGroup: "missing"})

	if m.view != viewLabels {
		t.Fatalf("view = %v, want labels", m.view)
	}

	if _, ok := m.list.SelectedItem().(groupHeader); ok {
		t.Fatal("fallback selected a label header")
	}
}

func TestNewOverlayModelStartsWithDefaultPickerState(t *testing.T) {
	m := newOverlayModel(pickerViewSessions(), "", nil, nil, nil, nil)
	if m.view != viewAll {
		t.Fatalf("initial view = %v, want All", m.view)
	}
}
