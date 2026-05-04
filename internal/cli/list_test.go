package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

func TestFindSession(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "abc123", Name: "worker-a"},
		{ID: "def456", Name: "worker-b"},
	}

	t.Run("by name", func(t *testing.T) {
		s := findSession(sessions, "worker-a")
		if s == nil || s.ID != "abc123" {
			t.Errorf("findSession by name: got %v", s)
		}
	})

	t.Run("by id", func(t *testing.T) {
		s := findSession(sessions, "def456")
		if s == nil || s.Name != "worker-b" {
			t.Errorf("findSession by id: got %v", s)
		}
	})

	t.Run("not found", func(t *testing.T) {
		s := findSession(sessions, "nope")
		if s != nil {
			t.Errorf("findSession not found: got %v, want nil", s)
		}
	})
}

func TestDescendantsOf(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "root"},
		{ID: "child1", ParentID: "root", Name: "child1"},
		{ID: "child2", ParentID: "root", Name: "child2"},
		{ID: "grandchild", ParentID: "child1", Name: "grandchild"},
		{ID: "unrelated", Name: "unrelated"},
	}

	desc := descendantsOf(sessions, "root")
	if len(desc) != 3 {
		t.Fatalf("expected 3 descendants, got %d", len(desc))
	}

	ids := make(map[string]bool)
	for _, s := range desc {
		ids[s.ID] = true
	}
	for _, expected := range []string{"child1", "child2", "grandchild"} {
		if !ids[expected] {
			t.Errorf("missing descendant %q", expected)
		}
	}
	if ids["root"] {
		t.Error("root should not be in descendants")
	}
	if ids["unrelated"] {
		t.Error("unrelated should not be in descendants")
	}
}

func TestDescendantsOfOrphans(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "orphan", ParentID: "deleted-parent", Name: "orphan"},
		{ID: "child", ParentID: "orphan", Name: "child"},
	}

	desc := descendantsOf(sessions, "orphan")
	if len(desc) != 1 || desc[0].ID != "child" {
		t.Errorf("expected [child], got %v", desc)
	}
}

func TestPrintTree(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "root", Name: "orchestrator", RepoName: "graith", Agent: "claude", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "child1", ParentID: "root", Name: "worker-a", RepoName: "graith", Agent: "codex", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "child2", ParentID: "root", Name: "worker-b", RepoName: "graith", Agent: "claude", Status: "stopped", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "grand", ParentID: "child1", Name: "review-a", RepoName: "graith", Agent: "codex", Status: "stopped", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "solo", Name: "standalone", RepoName: "graith", Agent: "claude", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printTree(cmd, sessions, time.Now())

	output := buf.String()

	if !bytes.Contains([]byte(output), []byte("orchestrator")) {
		t.Error("missing root session 'orchestrator'")
	}
	if !bytes.Contains([]byte(output), []byte("|-- worker-a")) && !bytes.Contains([]byte(output), []byte("`-- worker-a")) {
		t.Error("missing tree-indented 'worker-a'")
	}
	if !bytes.Contains([]byte(output), []byte("review-a")) {
		t.Error("missing grandchild 'review-a'")
	}
	if !bytes.Contains([]byte(output), []byte("standalone")) {
		t.Error("missing root session 'standalone'")
	}
}

func TestPrintTreeOrphansAsRoots(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "orphan", ParentID: "deleted", Name: "orphan-worker", RepoName: "graith", Agent: "claude", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printTree(cmd, sessions, time.Now())

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("orphan-worker")) {
		t.Error("orphan should render as root")
	}
	if bytes.Contains([]byte(output), []byte("|--")) || bytes.Contains([]byte(output), []byte("`--")) {
		t.Error("orphan should not have tree indentation")
	}
}
