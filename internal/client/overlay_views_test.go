package client

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
)

func navigatorViewSessions() []protocol.SessionInfo {
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

func TestLiveViewsPinOrchestratorAndDeletedDoesNot(t *testing.T) {
	sessions := navigatorViewSessions()

	for name, view := range map[string]viewMode{
		"all":      viewAll,
		"repo":     viewRepo,
		"starred":  viewStarred,
		"labels":   viewLabels,
		"scenario": viewScenario,
	} {
		t.Run(name, func(t *testing.T) {
			items := buildViewItems(view, sessions, nil)
			if len(items) == 0 {
				t.Fatal("view is empty")
			}

			pinned, ok := items[0].(sessionItem)
			if !ok || !pinned.pinned || pinned.info.ID != "ben" {
				t.Fatalf("first item = %#v, want pinned orchestrator", items[0])
			}

			for _, item := range items[1:] {
				if si, ok := item.(sessionItem); ok && si.info.ID == "ben" {
					t.Fatal("orchestrator rendered again in ordinary list")
				}
			}
		})
	}

	deleted := buildViewItems(viewDeleted, sessions, nil)
	for _, item := range deleted {
		if si, ok := item.(sessionItem); ok && si.pinned {
			t.Fatal("Deleted view must not pin the orchestrator")
		}
	}
}

func TestFilterSessionsKeepsOrchestratorReachable(t *testing.T) {
	filtered := filterSessions(navigatorViewSessions(), "missing-session")
	if len(filtered) != 1 || filtered[0].SystemKind != "orchestrator" {
		t.Fatalf("filtered sessions = %#v, want only orchestrator", filtered)
	}
}

func TestPinnedOrchestratorOmitsSystemRepositoryCell(t *testing.T) {
	items := buildViewItems(viewRepo, navigatorViewSessions(), nil)
	cols := computeColumnWidths(navigatorViewSessions(), "")
	d := compactDelegate{cols: cols}
	l := list.New(items, d, 120, 10)

	var buf strings.Builder
	d.Render(&buf, l, 0, items[0])
	line := ansi.Strip(buf.String())

	if strings.Contains(line, "System") {
		t.Fatalf("pinned orchestrator should not render System repository label: %q", line)
	}

	if !strings.Contains(line, "◆") || !strings.Contains(line, "ben") {
		t.Fatalf("pinned row lacks distinct treatment: %q", line)
	}
}

func TestStarResultPreservesSelectionWithPinnedOrchestrator(t *testing.T) {
	m := newOverlayModel(navigatorViewSessions(), "", nil, nil, nil, nil)
	m.selectSessionByID("bairn")

	updated, _ := m.Update(starResultMsg{sessionID: "bairn", starred: true})
	assertSelectedSession(t, asOverlay(updated), "bairn")
}

func TestAllViewBuildsOneGlobalCrossRepoTree(t *testing.T) {
	m := newOverlayModel(navigatorViewSessions(), "", nil, nil, nil, nil)

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

	if got := byID["ben"].displayName(); got != "ben" {
		t.Errorf("system display name = %q, want ben", got)
	}

	if got := byID["bairn"].displayName(); got != "bairn" {
		t.Errorf("cross-repo child display name = %q, want bairn", got)
	}

	if got := byID["wee-bairn"].displayName(); got != "wee-bairn" {
		t.Errorf("grandchild display name = %q, want wee-bairn", got)
	}

	if byID["wee-bairn"].treePrefix == "" {
		t.Fatalf("nested descendants lost tree edges: wee-bairn=%q", byID["wee-bairn"].treePrefix)
	}
}

func TestRepoViewKeepsRepositoryGroupsAndSplitsCrossRepoEdges(t *testing.T) {
	m := newOverlayModel(navigatorViewSessions(), "", nil, nil, nil, nil)
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

	if got := strings.Join(headers, ","); got != "bothy,croft,strath" {
		t.Fatalf("Repo headers = %q, want bothy,croft,strath", got)
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

func TestNonRepoViewsKeepSessionAndRepositoryNamesSeparate(t *testing.T) {
	sessions := []protocol.SessionInfo{{
		ID:       "dreich",
		Name:     "session-with-a-distinctly-long-assigned-name",
		RepoName: "repository-with-a-distinctly-long-name",
		Status:   "running",
	}}

	for name, view := range map[string]viewMode{
		"all":      viewAll,
		"starred":  viewStarred,
		"labels":   viewLabels,
		"scenario": viewScenario,
		"deleted":  viewDeleted,
	} {
		t.Run(name, func(t *testing.T) {
			items := buildViewItems(view, sessions, nil)

			var session sessionItem

			for _, item := range items {
				if candidate, ok := item.(sessionItem); ok {
					session = candidate
					break
				}
			}

			if session.info.ID == "" {
				t.Fatal("view did not contain a session")
			}

			if got := session.displayName(); got != sessions[0].Name {
				t.Fatalf("display name = %q, want %q", got, sessions[0].Name)
			}

			if strings.Contains(session.displayName(), "/") {
				t.Fatalf("display name %q contains repository prefix", session.displayName())
			}
		})
	}

	cols := computeColumnWidths(sessions, "")
	if cols.repo < lipgloss.Width(sessions[0].RepoName) {
		t.Fatalf("repository column width = %d, want at least %d", cols.repo, lipgloss.Width(sessions[0].RepoName))
	}
}

func TestAllRepoSwitchPreservesSelectionAndCollapseState(t *testing.T) {
	sessions := navigatorViewSessions()
	m := newOverlayModel(sessions, "", nil, nil, nil, nil)
	m.selectSessionByID("bairn")

	updated, _ := sendKey(m, "right")
	m = asOverlay(updated)
	assertSelectedSession(t, m, "bairn")

	updated, _ = sendKey(m, "left")
	m = asOverlay(updated)
	assertSelectedSession(t, m, "bairn")

	m.selectSessionByID("bairn")
	updated, _ = sendKey(m, " ")

	m = asOverlay(updated)
	if !m.collapsed["bairn"] {
		t.Fatal("ordinary parent should be collapsed in All")
	}

	updated, _ = sendKey(m, "right")

	m = asOverlay(updated)
	if !m.collapsed["bairn"] {
		t.Fatal("collapse state should survive switching to Repo")
	}

	m.selectSessionByID("bairn") // visible as a root in its repository group
	updated, _ = sendKey(m, "left")
	m = asOverlay(updated)
	assertSelectedSession(t, m, "bairn") // hidden child falls back to visible parent
}

func TestAllViewRefreshPreservesSelectionAndCollapse(t *testing.T) {
	sessions := navigatorViewSessions()
	collapsed := map[string]bool{"bairn": true}
	m := newOverlayModel(sessions, "ben", nil, nil, collapsed, nil)

	refreshed := append([]protocol.SessionInfo{}, sessions...)
	refreshed = append(refreshed, protocol.SessionInfo{
		ID: "dreich", ParentID: "ben", Name: "dreich", RepoName: "dreich", Status: "running",
	})

	updated, _ := m.Update(refreshSessionsMsg{sessions: refreshed})
	m = asOverlay(updated)
	assertSelectedSession(t, m, "ben")

	root := m.list.SelectedItem().(sessionItem)
	if !root.pinned {
		t.Fatalf("selected root = %#v, want pinned orchestrator", root)
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

	wantNameWidth := lipgloss.Width("bairn")
	if m.cols.name < wantNameWidth {
		t.Errorf("All name width = %d, want at least %d", m.cols.name, wantNameWidth)
	}

	updated, _ := sendKey(m, "right")

	m = asOverlay(updated)
	if m.cols.repo != 0 {
		t.Errorf("Repo view repository column width = %d, want hidden", m.cols.repo)
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

func TestSessionNavigatorStateRestoresViewAndSelection(t *testing.T) {
	sessions := append(navigatorViewSessions(), protocol.SessionInfo{ID: "labelled", Name: "labelled", Labels: []string{"braw"}, Starred: true})

	for _, view := range []SessionNavigatorView{SessionNavigatorViewAll, SessionNavigatorViewRepo, SessionNavigatorViewStarred, SessionNavigatorViewLabels, SessionNavigatorViewScenario, SessionNavigatorViewDeleted} {
		t.Run(viewNames[view], func(t *testing.T) {
			state := SessionNavigatorState{View: view}
			if view != SessionNavigatorViewDeleted {
				state.SessionID = "labelled"
			}

			if view == SessionNavigatorViewLabels {
				state.LabelGroup = "braw"
			}

			m := newOverlayModel(sessions, "", nil, nil, nil, nil)
			m.restoreSessionNavigatorState(state)

			if m.view != viewMode(view) {
				t.Fatalf("view = %v, want %v", m.view, view)
			}

			if view != SessionNavigatorViewDeleted {
				item, ok := m.list.SelectedItem().(sessionItem)
				if !ok || item.info.ID != "labelled" {
					t.Fatalf("selected item = %#v, want session labelled", m.list.SelectedItem())
				}

				if view == SessionNavigatorViewLabels && item.labelGroup != "braw" {
					t.Fatalf("label group = %q, want braw", item.labelGroup)
				}
			}
		})
	}
}

func TestSessionNavigatorStateFallsBackWhenSelectionDisappears(t *testing.T) {
	m := newOverlayModel(navigatorViewSessions(), "", nil, nil, nil, nil)
	m.restoreSessionNavigatorState(SessionNavigatorState{View: SessionNavigatorViewLabels, SessionID: "dreich", LabelGroup: "missing"})

	if m.view != viewLabels {
		t.Fatalf("view = %v, want labels", m.view)
	}

	if _, ok := m.list.SelectedItem().(groupHeader); ok {
		t.Fatal("fallback selected a label header")
	}
}

func TestNewOverlayModelStartsWithDefaultSessionNavigatorState(t *testing.T) {
	m := newOverlayModel(navigatorViewSessions(), "", nil, nil, nil, nil)
	if m.view != viewAll {
		t.Fatalf("initial view = %v, want All", m.view)
	}
}
