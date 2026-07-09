package client

import (
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

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
