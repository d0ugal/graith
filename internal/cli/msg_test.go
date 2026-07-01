package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestDescendantsOfDeepTree(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "ben"},
		{ID: "bairn", Name: "bairn", ParentID: "ben"},
		{ID: "canny", Name: "canny", ParentID: "ben"},
		{ID: "wee-bairn", Name: "wee-bairn", ParentID: "bairn"},
		{ID: "wee-canny", Name: "wee-canny", ParentID: "bairn"},
		{ID: "wee-skelf", Name: "wee-skelf", ParentID: "wee-bairn"},
		{ID: "thrawn", Name: "thrawn"},
	}

	descendants := descendantsOf(sessions, "ben")

	found := make(map[string]bool)
	for _, d := range descendants {
		found[d.ID] = true
	}

	for _, expected := range []string{"bairn", "canny", "wee-bairn", "wee-canny", "wee-skelf"} {
		if !found[expected] {
			t.Errorf("missing expected descendant %q", expected)
		}
	}

	if found["ben"] {
		t.Error("ben should not be in descendants")
	}

	if found["thrawn"] {
		t.Error("thrawn session should not be in descendants")
	}

	if len(descendants) != 5 {
		t.Errorf("expected 5 descendants, got %d", len(descendants))
	}
}

func TestDescendantsOfNoChildren(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "ben"},
		{ID: "thrawn", Name: "thrawn", ParentID: "someone-else"},
	}

	descendants := descendantsOf(sessions, "ben")
	if len(descendants) != 0 {
		t.Errorf("expected 0 descendants, got %d", len(descendants))
	}
}

func TestDescendantsOfFlatTree(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "ben"},
		{ID: "bairn", Name: "bairn", ParentID: "ben"},
		{ID: "canny", Name: "canny", ParentID: "ben"},
	}

	descendants := descendantsOf(sessions, "ben")
	if len(descendants) != 2 {
		t.Errorf("expected 2 descendants, got %d", len(descendants))
	}
}

func TestDescendantsOfCycleBackToRoot(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "ben"},
		{ID: "bairn", Name: "bairn", ParentID: "ben"},
		{ID: "wee-bairn", Name: "wee-bairn", ParentID: "bairn"},
	}
	// Simulate a cycle: wee-bairn points back to ben via its bairn
	sessions = append(sessions, protocol.SessionInfo{ID: "whin", Name: "whin", ParentID: "wee-bairn"})
	// Add ben as a "child" of whin to create a cycle
	sessions[0].ParentID = "whin"

	descendants := descendantsOf(sessions, "ben")

	found := make(map[string]bool)
	for _, d := range descendants {
		found[d.ID] = true
	}

	if found["ben"] {
		t.Error("ben should not appear in its own descendants (cycle guard)")
	}

	if !found["bairn"] || !found["wee-bairn"] || !found["whin"] {
		t.Error("all reachable non-root descendants should be included")
	}
}

func TestDescendantsOfSelfParent(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "skelf", Name: "skelf", ParentID: "skelf"},
	}

	descendants := descendantsOf(sessions, "skelf")
	if len(descendants) != 0 {
		t.Errorf("self-parented session should not appear in own descendants, got %d", len(descendants))
	}
}
