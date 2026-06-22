package daemon

import (
	"testing"
)

func TestCollectDescendantsIncludesRoot(t *testing.T) {
	sm := &SessionManager{
		state: &State{
			Sessions: map[string]*SessionState{
				"brae":      {ID: "brae", Status: StatusRunning},
				"bairn1":    {ID: "bairn1", ParentID: "brae", Status: StatusRunning},
				"bairn2":    {ID: "bairn2", ParentID: "brae", Status: StatusStopped},
				"wee-bairn": {ID: "wee-bairn", ParentID: "bairn1", Status: StatusRunning},
			},
		},
	}

	all := sm.collectDescendants("brae")

	found := make(map[string]bool)
	for _, id := range all {
		found[id] = true
	}
	if !found["brae"] {
		t.Error("collectDescendants should include root")
	}
	if !found["bairn1"] || !found["bairn2"] || !found["wee-bairn"] {
		t.Error("collectDescendants should include all descendants")
	}
	rootIdx := -1
	grandchildIdx := -1
	for i, id := range all {
		if id == "brae" {
			rootIdx = i
		}
		if id == "wee-bairn" {
			grandchildIdx = i
		}
	}
	if grandchildIdx > rootIdx {
		t.Error("grandchild should come before root (leaf-first)")
	}
}

func TestFilterExcludeRoot(t *testing.T) {
	ids := []string{"wee-bairn", "bairn1", "bairn2", "brae"}

	filtered := filterExcludeRoot(ids, "brae")

	for _, id := range filtered {
		if id == "brae" {
			t.Error("filterExcludeRoot should remove root")
		}
	}
	if len(filtered) != 3 {
		t.Errorf("expected 3 items, got %d", len(filtered))
	}
}
