package client

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

func dashboardTestSessions() []protocol.SessionInfo {
	return []protocol.SessionInfo{
		{
			ID:        "s1",
			Name:      "fix-bug",
			RepoName:  "myrepo",
			Agent:     "claude",
			Status:    "running",
			Branch:    "graith/fix-bug/s1",
			CreatedAt: time.Now().Add(-30 * time.Minute).Format(time.RFC3339),
		},
		{
			ID:        "s2",
			Name:      "add-feature",
			RepoName:  "myrepo",
			Agent:     "codex",
			Status:    "stopped",
			Branch:    "graith/add-feature/s2",
			CreatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
		},
	}
}

func updateDash(m DashboardModel, key string) DashboardModel {
	result, _ := m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
	return result.(DashboardModel)
}

func updateDashKey(m DashboardModel, k rune) (DashboardModel, tea.Cmd) {
	result, cmd := m.Update(tea.KeyPressMsg{Code: k})
	return result.(DashboardModel), cmd
}

func TestDashboardNavigation(t *testing.T) {
	sessions := dashboardTestSessions()
	m := NewDashboardModel(sessions, nil)
	m.width = 120
	m.height = 40

	if m.cursor != 0 {
		t.Errorf("initial cursor = %d, want 0", m.cursor)
	}

	dm := updateDash(m, "j")
	if dm.cursor != 1 {
		t.Errorf("after j: cursor = %d, want 1", dm.cursor)
	}

	dm = updateDash(dm, "j")
	if dm.cursor != 1 {
		t.Errorf("after j at end: cursor = %d, want 1", dm.cursor)
	}

	dm = updateDash(dm, "k")
	if dm.cursor != 0 {
		t.Errorf("after k: cursor = %d, want 0", dm.cursor)
	}

	dm = updateDash(dm, "k")
	if dm.cursor != 0 {
		t.Errorf("after k at start: cursor = %d, want 0", dm.cursor)
	}
}

func TestDashboardAttach(t *testing.T) {
	sessions := dashboardTestSessions()
	m := NewDashboardModel(sessions, nil)
	m.width = 120
	m.height = 40

	dm, cmd := updateDashKey(m, tea.KeyEnter)
	if dm.result == nil {
		t.Fatal("expected result after enter")
	}
	if dm.result.Action != "attach" {
		t.Errorf("action = %q, want %q", dm.result.Action, "attach")
	}
	if dm.result.SessionID != "s1" {
		t.Errorf("session_id = %q, want %q", dm.result.SessionID, "s1")
	}
	if cmd == nil {
		t.Error("expected tea.Quit cmd")
	}
}

func TestDashboardDeleteConfirm(t *testing.T) {
	sessions := dashboardTestSessions()
	m := NewDashboardModel(sessions, nil)
	m.width = 120
	m.height = 40

	dm := updateDash(m, "x")
	if dm.state != dashStateConfirmDelete {
		t.Errorf("state = %d, want dashStateConfirmDelete", dm.state)
	}

	dm = updateDash(dm, "n")
	if dm.state != dashStateNormal {
		t.Errorf("state after n = %d, want dashStateNormal", dm.state)
	}

	dm = updateDash(dm, "x")
	dm = updateDash(dm, "y")
	if dm.result == nil {
		t.Fatal("expected result after y confirm")
	}
	if dm.result.Action != "delete" {
		t.Errorf("action = %q, want %q", dm.result.Action, "delete")
	}
	if dm.result.SessionID != "s1" {
		t.Errorf("session_id = %q, want %q", dm.result.SessionID, "s1")
	}
}

func TestDashboardStopConfirm(t *testing.T) {
	sessions := dashboardTestSessions()
	m := NewDashboardModel(sessions, nil)
	m.width = 120
	m.height = 40

	dm := updateDash(m, "s")
	if dm.state != dashStateConfirmStop {
		t.Errorf("state = %d, want dashStateConfirmStop", dm.state)
	}

	dm = updateDash(dm, "y")
	if dm.result == nil {
		t.Fatal("expected result after y confirm")
	}
	if dm.result.Action != "stop" {
		t.Errorf("action = %q, want %q", dm.result.Action, "stop")
	}
}

func TestDashboardStopOnlyStopping(t *testing.T) {
	sessions := dashboardTestSessions()
	m := NewDashboardModel(sessions, nil)
	m.width = 120
	m.height = 40

	dm := updateDash(m, "j")

	dm = updateDash(dm, "s")
	if dm.state != dashStateNormal {
		t.Errorf("state = %d, want dashStateNormal (can't stop already stopped)", dm.state)
	}
}

func TestDashboardResumeOnlyStopped(t *testing.T) {
	sessions := dashboardTestSessions()
	m := NewDashboardModel(sessions, nil)
	m.width = 120
	m.height = 40

	dm := updateDash(m, "r")
	if dm.result != nil {
		t.Error("resume should not work on running session")
	}

	dm = updateDash(m, "j")

	dm = updateDash(dm, "r")
	if dm.result == nil {
		t.Fatal("expected result after resume")
	}
	if dm.result.Action != "resume" {
		t.Errorf("action = %q, want %q", dm.result.Action, "resume")
	}
}

func TestDashboardViewRendersContent(t *testing.T) {
	sessions := dashboardTestSessions()
	m := NewDashboardModel(sessions, nil)
	m.width = 120
	m.height = 40

	view := m.View().Content
	if view == "" {
		t.Error("view should not be empty")
	}

	checks := []string{"graith dashboard", "fix-bug", "add-feature", "attach", "stop", "delete", "resume", "quit"}
	for _, check := range checks {
		if !strings.Contains(view, check) {
			t.Errorf("view should contain %q", check)
		}
	}
}

func TestDashboardRefresh(t *testing.T) {
	sessions := dashboardTestSessions()
	m := NewDashboardModel(sessions, nil)
	m.width = 120
	m.height = 40

	newSessions := append(dashboardTestSessions(), protocol.SessionInfo{
		ID:        "s3",
		Name:      "new-work",
		RepoName:  "myrepo",
		Agent:     "claude",
		Status:    "running",
		CreatedAt: time.Now().Format(time.RFC3339),
	})

	result, _ := m.Update(refreshMsg{sessions: newSessions})
	dm := result.(DashboardModel)
	if len(dm.sessions) != 3 {
		t.Errorf("sessions count = %d, want 3", len(dm.sessions))
	}
}

func TestDashboardRefreshNilPreservesState(t *testing.T) {
	sessions := dashboardTestSessions()
	m := NewDashboardModel(sessions, nil)
	m.width = 120
	m.height = 40

	result, _ := m.Update(refreshMsg{sessions: nil})
	dm := result.(DashboardModel)
	if len(dm.sessions) != 2 {
		t.Errorf("sessions count = %d, want 2 (preserved on nil refresh)", len(dm.sessions))
	}
}

func TestDashboardEmptySessions(t *testing.T) {
	m := NewDashboardModel(nil, nil)
	m.width = 120
	m.height = 40

	view := m.View().Content
	if !strings.Contains(view, "No sessions") {
		t.Error("empty dashboard should show 'No sessions' message")
	}
}

func TestDashboardCursorPreservedOnRefresh(t *testing.T) {
	sessions := dashboardTestSessions()
	m := NewDashboardModel(sessions, nil)
	m.width = 120
	m.height = 40

	dm := updateDash(m, "j")
	if dm.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", dm.cursor)
	}

	result, _ := dm.Update(refreshMsg{sessions: dashboardTestSessions()})
	dm = result.(DashboardModel)
	if dm.cursor != 1 {
		t.Errorf("cursor after refresh = %d, want 1 (preserved)", dm.cursor)
	}
	if dm.selectedSessionID() != "s2" {
		t.Errorf("selected session = %q, want %q", dm.selectedSessionID(), "s2")
	}
}

func TestDashboardNarrowTerminal(t *testing.T) {
	sessions := dashboardTestSessions()
	m := NewDashboardModel(sessions, nil)
	m.width = 3
	m.height = 10

	view := m.View().Content
	if view == "" {
		t.Error("view should not be empty even with narrow terminal")
	}
}

func TestDashboardViewportScrolling(t *testing.T) {
	var sessions []protocol.SessionInfo
	for i := range 20 {
		sessions = append(sessions, protocol.SessionInfo{
			ID:        fmt.Sprintf("s%d", i),
			Name:      fmt.Sprintf("session-%d", i),
			RepoName:  "myrepo",
			Agent:     "claude",
			Status:    "running",
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
		})
	}

	m := NewDashboardModel(sessions, nil)
	m.width = 120
	m.height = 15

	view := m.View().Content
	if !strings.Contains(view, "session-0") {
		t.Error("first session should be visible initially")
	}
	if !strings.Contains(view, "more below") {
		t.Error("should show 'more below' indicator when sessions overflow")
	}

	// Navigate to the bottom
	dm := m
	for range 19 {
		dm = updateDash(dm, "j")
	}
	if dm.cursor != 19 {
		t.Fatalf("cursor = %d, want 19", dm.cursor)
	}

	view = dm.View().Content
	if !strings.Contains(view, "session-19") {
		t.Error("last session should be visible after scrolling down")
	}
	if !strings.Contains(view, "more above") {
		t.Error("should show 'more above' indicator when scrolled down")
	}
}
