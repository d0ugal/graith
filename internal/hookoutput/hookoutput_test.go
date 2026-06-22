package hookoutput

import "testing"

func TestApproval(t *testing.T) {
	tests := []struct {
		agent    string
		decision string
		reason   string
		want     string
	}{
		{"claude", "allow", "", `{"decision":"approve"}`},
		{"claude", "block", "", `{"decision":"block"}`},
		{"claude", "deny", "", `{"decision":"block"}`},
		{"claude", "allow", "braw-approved", `{"decision":"approve","reason":"braw-approved"}`},
		{"claude", "block", "neep-forbidden", `{"decision":"block","reason":"neep-forbidden"}`},
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
	if got := AllowAll("claude"); got != `{"decision":"approve"}` {
		t.Errorf("AllowAll(claude) = %s, want approve", got)
	}
	if got := AllowAll("codex"); got != `{"decision":"allow"}` {
		t.Errorf("AllowAll(codex) = %s, want allow", got)
	}
}
