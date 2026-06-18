package config

import "testing"

func TestExpand(t *testing.T) {
	vars := TemplateVars{Username: "braw-lad", AgentSessionID: "abc-123", SessionName: "braw-fix", SessionID: "a3f2b1c9", WorktreePath: "/tmp/bothy", Model: "claude-opus-4"}
	tests := []struct{ input, want string }{
		{"{username}/graith", "braw-lad/graith"},
		{"--session-id {agent_session_id}", "--session-id abc-123"},
		{"no vars here", "no vars here"},
		{"{session_name}-{session_id}", "braw-fix-a3f2b1c9"},
		{"--model {model}", "--model claude-opus-4"},
	}
	for _, tt := range tests {
		got, err := Expand(tt.input, vars)
		if err != nil {
			t.Errorf("Expand(%q) error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Expand(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExpandUnknownVar(t *testing.T) {
	vars := TemplateVars{}
	_, err := Expand("{nonexistent}", vars)
	if err == nil {
		t.Error("Expand with unknown var should return error")
	}
}

func TestExpandSlice(t *testing.T) {
	vars := TemplateVars{Username: "braw-lad", AgentSessionID: "abc"}
	got, err := ExpandSlice([]string{"--resume", "{agent_session_id}"}, vars)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "--resume" || got[1] != "abc" {
		t.Errorf("ExpandSlice = %v, want [--resume abc]", got)
	}
}
