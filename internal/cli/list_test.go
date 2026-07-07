package cli

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/output"
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
		{ID: "ben", Name: "ben"},
		{ID: "bairn", ParentID: "ben", Name: "bairn"},
		{ID: "canny", ParentID: "ben", Name: "canny"},
		{ID: "wee-bairn", ParentID: "bairn", Name: "wee-bairn"},
		{ID: "thrawn", Name: "thrawn"},
	}

	desc := descendantsOf(sessions, "ben")
	if len(desc) != 3 {
		t.Fatalf("expected 3 descendants, got %d", len(desc))
	}

	ids := make(map[string]bool)
	for _, s := range desc {
		ids[s.ID] = true
	}

	for _, expected := range []string{"bairn", "canny", "wee-bairn"} {
		if !ids[expected] {
			t.Errorf("missing descendant %q", expected)
		}
	}

	if ids["ben"] {
		t.Error("ben should not be in descendants")
	}

	if ids["thrawn"] {
		t.Error("thrawn should not be in descendants")
	}
}

func TestDescendantsOfOrphans(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "auld", ParentID: "deleted-parent", Name: "auld"},
		{ID: "bairn", ParentID: "auld", Name: "bairn"},
	}

	desc := descendantsOf(sessions, "auld")
	if len(desc) != 1 || desc[0].ID != "bairn" {
		t.Errorf("expected [bairn], got %v", desc)
	}
}

func TestPrintTree(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "hame", RepoName: "croft", Agent: "claude", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "bairn", ParentID: "ben", Name: "bairn", RepoName: "croft", Agent: "codex", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "canny", ParentID: "ben", Name: "canny", RepoName: "croft", Agent: "claude", Status: "stopped", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "wee-bairn", ParentID: "bairn", Name: "wee-bairn", RepoName: "croft", Agent: "codex", Status: "stopped", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "thrawn", Name: "thrawn", RepoName: "croft", Agent: "claude", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}

	var buf bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printTree(cmd, sessions, time.Now())

	output := buf.String()

	if !bytes.Contains([]byte(output), []byte("hame")) {
		t.Error("missing root session 'hame'")
	}

	if !bytes.Contains([]byte(output), []byte("|-- bairn")) && !bytes.Contains([]byte(output), []byte("`-- bairn")) {
		t.Error("missing tree-indented 'bairn'")
	}

	if !bytes.Contains([]byte(output), []byte("wee-bairn")) {
		t.Error("missing grandchild 'wee-bairn'")
	}

	if !bytes.Contains([]byte(output), []byte("thrawn")) {
		t.Error("missing root session 'thrawn'")
	}
}

func TestPrintQuietNames(t *testing.T) {
	origJSON := jsonOutput

	defer func() { jsonOutput = origJSON }()

	jsonOutput = false

	sessions := []protocol.SessionInfo{
		{ID: "abc", Name: "thrawn", RepoName: "croft"},
		{ID: "def", Name: "braw", RepoName: "croft"},
		{ID: "ghi", Name: "canny", RepoName: "bothy"},
	}

	var buf bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := printQuiet(cmd, sessions); err != nil {
		t.Fatalf("printQuiet: %v", err)
	}

	// Sorted by repo, then name: bothy/canny, croft/braw, croft/thrawn.
	want := "canny\nbraw\nthrawn\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

func TestPrintQuietJSONIDs(t *testing.T) {
	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	var buf bytes.Buffer

	jsonOutput = true
	out = output.NewWithWriter(true, &buf)

	sessions := []protocol.SessionInfo{
		{ID: "abc", Name: "thrawn", RepoName: "croft"},
		{ID: "def", Name: "braw", RepoName: "croft"},
		{ID: "ghi", Name: "canny", RepoName: "bothy"},
	}

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := printQuiet(cmd, sessions); err != nil {
		t.Fatalf("printQuiet: %v", err)
	}

	var ids []string
	if err := json.Unmarshal(buf.Bytes(), &ids); err != nil {
		t.Fatalf("output is not a JSON array: %v (%q)", err, buf.String())
	}

	// Sorted by repo, then name: canny(ghi), braw(def), thrawn(abc).
	want := []string{"ghi", "def", "abc"}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}

	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v, want %v", ids, want)
		}
	}
}

func TestPrintQuietDoesNotMutateInput(t *testing.T) {
	origJSON := jsonOutput

	defer func() { jsonOutput = origJSON }()

	jsonOutput = false

	sessions := []protocol.SessionInfo{
		{ID: "abc", Name: "thrawn", RepoName: "croft"},
		{ID: "def", Name: "braw", RepoName: "croft"},
	}

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	if err := printQuiet(cmd, sessions); err != nil {
		t.Fatalf("printQuiet: %v", err)
	}

	if sessions[0].Name != "thrawn" || sessions[1].Name != "braw" {
		t.Errorf("printQuiet mutated caller's slice order: %v", sessions)
	}
}

func TestPrintTreeOrphansAsRoots(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "auld", ParentID: "deleted", Name: "auld-whin", RepoName: "croft", Agent: "claude", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}

	var buf bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printTree(cmd, sessions, time.Now())

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("auld-whin")) {
		t.Error("orphan should render as root")
	}

	if bytes.Contains([]byte(output), []byte("|--")) || bytes.Contains([]byte(output), []byte("`--")) {
		t.Error("orphan should not have tree indentation")
	}
}
