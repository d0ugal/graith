package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestDescendantsOfDeepTree(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "root"},
		{ID: "child1", Name: "child1", ParentID: "root"},
		{ID: "child2", Name: "child2", ParentID: "root"},
		{ID: "grandchild1", Name: "grandchild1", ParentID: "child1"},
		{ID: "grandchild2", Name: "grandchild2", ParentID: "child1"},
		{ID: "greatgrandchild", Name: "greatgrandchild", ParentID: "grandchild1"},
		{ID: "unrelated", Name: "unrelated"},
	}

	descendants := descendantsOf(sessions, "root")

	found := make(map[string]bool)
	for _, d := range descendants {
		found[d.ID] = true
	}

	for _, expected := range []string{"child1", "child2", "grandchild1", "grandchild2", "greatgrandchild"} {
		if !found[expected] {
			t.Errorf("missing expected descendant %q", expected)
		}
	}
	if found["root"] {
		t.Error("root should not be in descendants")
	}
	if found["unrelated"] {
		t.Error("unrelated session should not be in descendants")
	}
	if len(descendants) != 5 {
		t.Errorf("expected 5 descendants, got %d", len(descendants))
	}
}

func TestDescendantsOfNoChildren(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "root"},
		{ID: "other", Name: "other", ParentID: "someone-else"},
	}

	descendants := descendantsOf(sessions, "root")
	if len(descendants) != 0 {
		t.Errorf("expected 0 descendants, got %d", len(descendants))
	}
}

func TestDescendantsOfFlatTree(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "root"},
		{ID: "child1", Name: "child1", ParentID: "root"},
		{ID: "child2", Name: "child2", ParentID: "root"},
	}

	descendants := descendantsOf(sessions, "root")
	if len(descendants) != 2 {
		t.Errorf("expected 2 descendants, got %d", len(descendants))
	}
}

func TestDescendantsOfCycleBackToRoot(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "root"},
		{ID: "child", Name: "child", ParentID: "root"},
		{ID: "grandchild", Name: "grandchild", ParentID: "child"},
	}
	// Simulate a cycle: grandchild points back to root via its child
	sessions = append(sessions, protocol.SessionInfo{ID: "cycler", Name: "cycler", ParentID: "grandchild"})
	// Add root as a "child" of cycler to create a cycle
	sessions[0].ParentID = "cycler"

	descendants := descendantsOf(sessions, "root")

	found := make(map[string]bool)
	for _, d := range descendants {
		found[d.ID] = true
	}

	if found["root"] {
		t.Error("root should not appear in its own descendants (cycle guard)")
	}
	if !found["child"] || !found["grandchild"] || !found["cycler"] {
		t.Error("all reachable non-root descendants should be included")
	}
}

func TestDescendantsOfSelfParent(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "self", Name: "self", ParentID: "self"},
	}

	descendants := descendantsOf(sessions, "self")
	if len(descendants) != 0 {
		t.Errorf("self-parented session should not appear in own descendants, got %d", len(descendants))
	}
}
