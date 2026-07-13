package hookoutput

import "testing"

func TestApproval(t *testing.T) {
	tests := []struct {
		agent    string
		decision string
		reason   string
		want     string
	}{
		{"claude", "allow", "", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`},
		{"claude", "block", "", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}`},
		{"claude", "deny", "", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}`},
		{"claude", "defer", "", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask"}}`},
		{"claude", "ask", "", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask"}}`},
		{"claude", "haar", "", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"haar"}}`},
		{"claude", "allow", "braw-approved", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":"braw-approved"}}`},
		{"claude", "block", "neep-forbidden", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"neep-forbidden"}}`},
		{"codex", "allow", "", `{"decision":"allow"}`},
		{"codex", "deny", "", `{"decision":"deny"}`},
		{"codex", "block", "", `{"decision":"deny"}`},
		{"cursor", "allow", "", `{"permission":"allow"}`},
		{"cursor", "deny", "", `{"permission":"deny"}`},
		{"cursor", "block", "", `{"permission":"deny"}`},
		{"agy", "allow", "", `{"decision":"allow"}`},
	}
	for _, tt := range tests {
		t.Run(tt.agent+"/"+tt.decision, func(t *testing.T) {
			got := Approval(tt.agent, tt.decision, tt.reason)
			if got != tt.want {
				t.Errorf("Approval(%q, %q, %q) = %s, want %s", tt.agent, tt.decision, tt.reason, got, tt.want)
			}
		})
	}
}

func TestAllowAll(t *testing.T) {
	want := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`
	if got := AllowAll("claude"); got != want {
		t.Errorf("AllowAll(claude) = %s, want %s", got, want)
	}

	if got := AllowAll("codex"); got != `{"decision":"allow"}` {
		t.Errorf("AllowAll(codex) = %s, want allow", got)
	}
}
