package hookoutput

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandPolicyNeverDefersToNativePrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		agent, decision, want string
	}{
		{"claude", "allow", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`},
		{"claude", "ask", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}`},
		{"claude", "unknown", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}`},
		{"codex", "allow", `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`},
		{"codex", "defer", `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny"}}}`},
		{"cursor", "allow", `{"permission":"allow"}`},
		{"cursor", "ask", `{"permission":"deny"}`},
	}
	for _, tt := range tests {
		t.Run(tt.agent+"/"+tt.decision, func(t *testing.T) {
			t.Parallel()
			if got := CommandPolicy(tt.agent, tt.decision, ""); got != tt.want {
				t.Fatalf("CommandPolicy() = %s, want %s", got, tt.want)
			}
		})
	}
}

// inbox context must go through hookSpecificOutput.additionalContext (which
// reaches the model), not a top-level systemMessage (which is user-facing only).
func TestInboxContextClaude(t *testing.T) {
	body := "You have 1 unread message(s). From braw: hello"

	got := InboxContext("claude", "SessionStart", body)

	// Decode into the documented Claude hook contract shape.
	var parsed struct {
		SystemMessage      string `json:"systemMessage"`
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("InboxContext(claude) produced invalid JSON %q: %v", got, err)
	}

	if parsed.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", parsed.HookSpecificOutput.HookEventName)
	}

	if parsed.HookSpecificOutput.AdditionalContext != body {
		t.Errorf("additionalContext = %q, want %q", parsed.HookSpecificOutput.AdditionalContext, body)
	}

	// The buggy behaviour emitted the body as systemMessage; guard against a
	// regression to that (systemMessage never reaches the model).
	if parsed.SystemMessage != "" {
		t.Errorf("systemMessage = %q, want empty (context must use additionalContext)", parsed.SystemMessage)
	}

	if strings.Contains(got, `"systemMessage"`) {
		t.Errorf("output %q must not carry the message body as systemMessage", got)
	}
}

// TestInboxContextCodex verifies Codex inbox context reaches the model via
// hookSpecificOutput.additionalContext, not a user-facing systemMessage — the
// same requirement as Claude (#1072), since Codex's SessionStart output wire
// carries additionalContext under hookSpecificOutput (#1183).
func TestInboxContextCodex(t *testing.T) {
	body := "You have 1 unread message(s). From braw: hello"

	got := InboxContext("codex", "SessionStart", body)

	var parsed struct {
		SystemMessage      string `json:"systemMessage"`
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("InboxContext(codex) produced invalid JSON %q: %v", got, err)
	}

	if parsed.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", parsed.HookSpecificOutput.HookEventName)
	}

	if parsed.HookSpecificOutput.AdditionalContext != body {
		t.Errorf("additionalContext = %q, want %q", parsed.HookSpecificOutput.AdditionalContext, body)
	}

	if parsed.SystemMessage != "" || strings.Contains(got, `"systemMessage"`) {
		t.Errorf("output %q must not carry the body as systemMessage (never reaches the model)", got)
	}
}

// TestInboxContextOtherAgents checks agents without a model-visible context
// channel keep the systemMessage form they already consume.
func TestInboxContextOtherAgents(t *testing.T) {
	body := "unread message from canny"

	for _, agent := range []string{"cursor", "agy", ""} {
		got := InboxContext(agent, "SessionStart", body)

		var parsed struct {
			SystemMessage string `json:"systemMessage"`
		}
		if err := json.Unmarshal([]byte(got), &parsed); err != nil {
			t.Fatalf("InboxContext(%q) produced invalid JSON %q: %v", agent, got, err)
		}

		if parsed.SystemMessage != body {
			t.Errorf("InboxContext(%q) systemMessage = %q, want %q", agent, parsed.SystemMessage, body)
		}

		if strings.Contains(got, "hookSpecificOutput") {
			t.Errorf("InboxContext(%q) = %q, should not emit hookSpecificOutput", agent, got)
		}
	}
}
