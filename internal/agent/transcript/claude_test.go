package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLines writes newline-joined lines to a temp file and returns its path.
func writeLines(t *testing.T, lines []string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestClaudeReaderWalksChainAndPairsTools(t *testing.T) {
	lines := []string{
		`{"type":"file-history-snapshot","uuid":"meta1"}`, // metadata, skipped
		`{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"fix the bothy"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm","signature":"sig"},{"type":"text","text":"Looking now."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"neeps\ntatties"}]}}`,
		`{"type":"assistant","uuid":"a2","parentUuid":"u2","message":{"role":"assistant","content":[{"type":"text","text":"Done, braw."}]}}`,
		`{"type":"user","uuid":"side1","parentUuid":"u1","isSidechain":true,"message":{"role":"user","content":"sidechain noise"}}`,
		`{not valid json`, // partial trailing line
	}
	path := writeLines(t, lines)

	turns, dropped, err := claudeReader{}.read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}

	want := []struct {
		role Role
		text string
		tool string
	}{
		{RoleUser, "fix the bothy", ""},
		{RoleAssistant, "Looking now.", ""},
		{RoleTool, "", "Bash"},
		{RoleAssistant, "Done, braw.", ""},
	}
	if len(turns) != len(want) {
		t.Fatalf("got %d turns, want %d: %+v", len(turns), len(want), turns)
	}

	for i, w := range want {
		if turns[i].Role != w.role {
			t.Errorf("turn %d role = %s, want %s", i, turns[i].Role, w.role)
		}

		if w.text != "" && turns[i].Text != w.text {
			t.Errorf("turn %d text = %q, want %q", i, turns[i].Text, w.text)
		}

		if w.tool != "" {
			if turns[i].Tool == nil || turns[i].Tool.Name != w.tool {
				t.Errorf("turn %d tool = %+v, want %s", i, turns[i].Tool, w.tool)
			}
		}
	}
	// Thinking dropped, sidechain excluded, tool output paired.
	if turns[2].Tool.Output != "neeps\ntatties" {
		t.Errorf("tool output = %q, want paired result", turns[2].Tool.Output)
	}

	for _, tr := range turns {
		if strings.Contains(tr.Text, "sidechain") {
			t.Error("sidechain turn leaked into chain")
		}
	}
}

func TestClaudeReaderEmptyYieldsNoTurns(t *testing.T) {
	path := writeLines(t, []string{`{"type":"summary","uuid":"s1"}`})

	turns, _, err := claudeReader{}.read(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(turns) != 0 {
		t.Errorf("got %d turns, want 0", len(turns))
	}
}

func TestLocateClaudeBySessionIDGlob(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	// The project dir name is irrelevant — locate globs by session id.
	projDir := filepath.Join(root, "projects", "-Users-someone--graith-worktrees-glen")
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatal(err)
	}

	sid := "abc123-de45-67f8-9012-3456789abcde"

	want := filepath.Join(projDir, sid+".jsonl")
	if err := os.WriteFile(want, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := locateClaude(sid)
	if err != nil {
		t.Fatalf("locateClaude: %v", err)
	}

	if got != want {
		t.Errorf("located %q, want %q", got, want)
	}

	if _, err := locateClaude("missing-session"); err == nil {
		t.Error("expected error locating missing session")
	}
}
