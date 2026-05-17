package daemon

import (
	"testing"
)

func TestCollectDescendantsIncludesRoot(t *testing.T) {
	sm := &SessionManager{
		state: &State{
			Sessions: map[string]*SessionState{
				"root":       {ID: "root", Status: StatusRunning},
				"child1":     {ID: "child1", ParentID: "root", Status: StatusRunning},
				"child2":     {ID: "child2", ParentID: "root", Status: StatusStopped},
				"grandchild": {ID: "grandchild", ParentID: "child1", Status: StatusRunning},
			},
		},
	}

	all := sm.collectDescendants("root")

	found := make(map[string]bool)
	for _, id := range all {
		found[id] = true
	}
	if !found["root"] {
		t.Error("collectDescendants should include root")
	}
	if !found["child1"] || !found["child2"] || !found["grandchild"] {
		t.Error("collectDescendants should include all descendants")
	}
	rootIdx := -1
	grandchildIdx := -1
	for i, id := range all {
		if id == "root" {
			rootIdx = i
		}
		if id == "grandchild" {
			grandchildIdx = i
		}
	}
	if grandchildIdx > rootIdx {
		t.Error("grandchild should come before root (leaf-first)")
	}
}

func TestFilterExcludeRoot(t *testing.T) {
	ids := []string{"grandchild", "child1", "child2", "root"}

	filtered := filterExcludeRoot(ids, "root")

	for _, id := range filtered {
		if id == "root" {
			t.Error("filterExcludeRoot should remove root")
		}
	}
	if len(filtered) != 3 {
		t.Errorf("expected 3 items, got %d", len(filtered))
	}
}
