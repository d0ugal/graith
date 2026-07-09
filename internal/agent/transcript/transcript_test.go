package transcript

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeCodexSessions lays out a CODEX_HOME with a sessions dir and points
// CODEX_HOME at it, returning the sessions directory. Fixture strings use old
// Scots words per AGENTS.md.
func writeCodexSessions(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)

	day := filepath.Join(root, "sessions", "2026", "07", "09")
	if err := os.MkdirAll(day, 0o750); err != nil {
		t.Fatal(err)
	}

	return day
}

// writeRollout writes a rollout-*.jsonl file into dir with a session_meta line
// (id + cwd) followed by any extra JSONL lines, and returns its path.
func writeRollout(t *testing.T, dir, name, id, cwd string, extra ...string) string {
	t.Helper()

	// Marshal the session_meta line rather than concatenating strings so a
	// cwd/id containing JSON-special characters (e.g. an unusual TMPDIR) can't
	// produce invalid JSON.
	meta, err := json.Marshal(map[string]any{
		"type":    "session_meta",
		"payload": map[string]string{"id": id, "cwd": cwd},
	})
	if err != nil {
		t.Fatal(err)
	}

	lines := []string{string(meta)}
	lines = append(lines, extra...)

	path := filepath.Join(dir, name)

	data := []byte("")
	for _, l := range lines {
		data = append(data, []byte(l+"\n")...)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestSupportedAgentsCov(t *testing.T) {
	cases := []struct {
		agent string
		want  bool
	}{
		{AgentClaude, true},
		{AgentCodex, true},
		{"dreich-unknown", false},
		{"", false},
	}

	for _, tc := range cases {
		if got := Supported(tc.agent); got != tc.want {
			t.Errorf("Supported(%q) = %v, want %v", tc.agent, got, tc.want)
		}
	}
}

func TestReaderForUnsupportedCov(t *testing.T) {
	r, err := readerFor("thrawn-agent")
	if r != nil {
		t.Errorf("reader = %v, want nil", r)
	}

	if !errors.Is(err, ErrUnsupportedAgent) {
		t.Errorf("err = %v, want ErrUnsupportedAgent", err)
	}
}

func TestReadCodexEndToEndCov(t *testing.T) {
	day := writeCodexSessions(t)
	cwd := t.TempDir() // real dir so EvalSymlinks matches both sides

	extra := []string{
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"mend the bothy"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"aye, on it"}]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"neeps"}}`,
	}
	writeRollout(t, day, "rollout-1-braw.jsonl", "sess-braw", cwd, extra...)

	conv, err := Read(AgentCodex, "", cwd)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if conv.SrcAgent != AgentCodex {
		t.Errorf("SrcAgent = %q, want %q", conv.SrcAgent, AgentCodex)
	}

	if conv.DroppedLines != 0 {
		t.Errorf("DroppedLines = %d, want 0", conv.DroppedLines)
	}

	if len(conv.Turns) != 3 {
		t.Fatalf("got %d turns, want 3: %+v", len(conv.Turns), conv.Turns)
	}

	for i, turn := range conv.Turns {
		if turn.SrcAgent != AgentCodex {
			t.Errorf("turn %d SrcAgent = %q, want %q", i, turn.SrcAgent, AgentCodex)
		}
	}

	// Assert every turn's role and content, not just the paired tool output —
	// otherwise mis-classified user/assistant roles or text would slip through.
	if conv.Turns[0].Role != RoleUser || conv.Turns[0].Text != "mend the bothy" {
		t.Errorf("turn 0 = {%s, %q}, want {user, mend the bothy}", conv.Turns[0].Role, conv.Turns[0].Text)
	}

	if conv.Turns[1].Role != RoleAssistant || conv.Turns[1].Text != "aye, on it" {
		t.Errorf("turn 1 = {%s, %q}, want {assistant, aye, on it}", conv.Turns[1].Role, conv.Turns[1].Text)
	}

	tool := conv.Turns[2].Tool
	if conv.Turns[2].Role != RoleTool || tool == nil || tool.Name != "shell" || tool.Args != "{}" || tool.Output != "neeps" {
		t.Errorf("turn 2 tool = %+v, want {shell, {}, neeps}", tool)
	}
}

func TestReadUnsupportedAgentCov(t *testing.T) {
	_, err := Read("scunner-agent", "id", "/tmp/glen")
	if !errors.Is(err, ErrUnsupportedAgent) {
		t.Errorf("err = %v, want ErrUnsupportedAgent", err)
	}
}

func TestReadNoTurnsCov(t *testing.T) {
	day := writeCodexSessions(t)
	cwd := t.TempDir()
	// A rollout with only session_meta / non-content records yields no turns.
	writeRollout(t, day, "rollout-1-dreich.jsonl", "sess-dreich", cwd,
		`{"type":"turn_context","payload":{"model":"o3"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","total":9}}`,
	)

	_, err := Read(AgentCodex, "", cwd)
	if !errors.Is(err, ErrNoTurns) {
		t.Errorf("err = %v, want ErrNoTurns", err)
	}
}

func TestReadLocateFailureCodexCov(t *testing.T) {
	writeCodexSessions(t)
	// No rollout matches this cwd, so locate fails.
	_, err := Read(AgentCodex, "", t.TempDir())
	if err == nil {
		t.Fatal("expected locate error for unmatched cwd")
	}

	if errors.Is(err, ErrNoTurns) {
		t.Errorf("err = %v, want a locate error not ErrNoTurns", err)
	}
}

func TestReadLocateFailureClaudeEmptyIDCov(t *testing.T) {
	// Empty agent session id makes the claude locate branch fail fast at the
	// guard, exercising locate()'s claude case and readerFor's claude branch.
	_, err := Read(AgentClaude, "", t.TempDir())
	if err == nil {
		t.Fatal("expected claude locate error for empty session id")
	}
}

func TestReadLocateFailureClaudeNoTranscriptCov(t *testing.T) {
	// A non-empty id with an empty config dir reaches the glob and finds no
	// matching transcript, exercising locateClaude's not-found path.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	_, err := Read(AgentClaude, "sess-nae-such", t.TempDir())
	if err == nil {
		t.Fatal("expected claude locate error when no transcript matches the id")
	}
}

func TestPairToolOutputsIdentityCov(t *testing.T) {
	in := []Turn{
		{Role: RoleUser, Text: "speir"},
		{Role: RoleAssistant, Text: "ken"},
	}

	out := pairToolOutputs(in)
	if len(out) != len(in) {
		t.Fatalf("got %d turns, want %d", len(out), len(in))
	}

	for i := range in {
		if out[i] != in[i] {
			t.Errorf("turn %d changed: %+v != %+v", i, out[i], in[i])
		}
	}
}
