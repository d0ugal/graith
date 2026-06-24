package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexReaderRolesAndToolPairing(t *testing.T) {
	lines := []string{
		`{"timestamp":"t0","type":"session_meta","payload":{"id":"sess-9","cwd":"/glen/bothy"}}`,
		`{"timestamp":"t1","type":"turn_context","payload":{"model":"o3"}}`,
		`{"timestamp":"t2","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"text","text":"system bield"}]}}`,
		`{"timestamp":"t3","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bothy"}]}}`,
		`{"timestamp":"t4","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"on it"}]}}`,
		`{"timestamp":"t5","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}","call_id":"c1"}}`,
		`{"timestamp":"t6","type":"event_msg","payload":{"type":"token_count","total":42}}`,
		`{"timestamp":"t7","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"neeps"}}`,
		`{"timestamp":"t8","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
	}
	path := writeLines(t, lines)

	turns, dropped, err := codexReader{}.read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}

	want := []struct {
		role Role
		text string
		tool string
		out  string
	}{
		{RoleContext, "system bield", "", ""},
		{RoleUser, "fix the bothy", "", ""},
		{RoleAssistant, "on it", "", ""},
		{RoleTool, "", "shell", "neeps"},
		{RoleAssistant, "done", "", ""},
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
			} else if turns[i].Tool.Output != w.out {
				t.Errorf("turn %d tool output = %q, want %q", i, turns[i].Tool.Output, w.out)
			}
		}
	}
}

func TestLocateCodexByCwdNewest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	day := filepath.Join(root, "sessions", "2026", "06", "24")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}

	cwd := t.TempDir() // a real dir so EvalSymlinks matches both sides
	older := filepath.Join(day, "rollout-100-aaaa.jsonl")
	newer := filepath.Join(day, "rollout-200-bbbb.jsonl")
	other := filepath.Join(day, "rollout-300-cccc.jsonl")
	meta := func(c string) string {
		return `{"timestamp":"t","type":"session_meta","payload":{"id":"x","cwd":"` + c + `"}}` + "\n"
	}
	if err := os.WriteFile(older, []byte(meta(cwd)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte(meta(cwd)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(meta("/somewhere/else")), 0o600); err != nil {
		t.Fatal(err)
	}
	// Make `newer` the most recently modified matching rollout.
	bumpModTime(t, newer)

	got, err := locateCodex("", cwd)
	if err != nil {
		t.Fatalf("locateCodex: %v", err)
	}
	if got != newer {
		t.Errorf("located %q, want %q", got, newer)
	}
}

func TestCodexRolloutID(t *testing.T) {
	path := writeLines(t, []string{
		`{"type":"session_meta","payload":{"id":"rollout-id-7","cwd":"/x"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
	})
	id, ok := CodexRolloutID(path)
	if !ok || id != "rollout-id-7" {
		t.Errorf("CodexRolloutID = %q, %v; want rollout-id-7, true", id, ok)
	}
}
