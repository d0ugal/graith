package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestPrintPathPlain(t *testing.T) {
	var buf bytes.Buffer
	o := output.New(false)
	session := &protocol.SessionInfo{
		ID:           "abc123",
		Name:         "braw",
		WorktreePath: "/tmp/graith/braw",
	}

	err := printPath(&buf, o, session, "braw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := buf.String()
	if got != "/tmp/graith/braw" {
		t.Errorf("got %q, want %q", got, "/tmp/graith/braw")
	}
	if strings.HasSuffix(got, "\n") {
		t.Error("plain output should not end with newline")
	}
}

func TestPrintPathJSON(t *testing.T) {
	var buf bytes.Buffer
	o := output.NewWithWriter(true, &buf)
	session := &protocol.SessionInfo{
		ID:           "abc123",
		Name:         "braw",
		WorktreePath: "/tmp/graith/braw",
	}

	err := printPath(&bytes.Buffer{}, o, session, "braw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	for _, key := range []string{"session_id", "name", "worktree_path"} {
		if _, ok := result[key]; !ok {
			t.Errorf("missing key %q in JSON output", key)
		}
	}
	if result["session_id"] != "abc123" {
		t.Errorf("session_id = %q, want %q", result["session_id"], "abc123")
	}
	if result["worktree_path"] != "/tmp/graith/braw" {
		t.Errorf("worktree_path = %q, want %q", result["worktree_path"], "/tmp/graith/braw")
	}
}

func TestPrintPathEmptyWorktree(t *testing.T) {
	var buf bytes.Buffer
	o := output.New(false)
	session := &protocol.SessionInfo{
		ID:   "abc123",
		Name: "braw",
	}

	err := printPath(&buf, o, session, "braw")
	if err == nil {
		t.Fatal("expected error for empty worktree path")
	}
	if !strings.Contains(err.Error(), "no worktree path") {
		t.Errorf("error = %q, want it to mention 'no worktree path'", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output, got %q", buf.String())
	}
}
