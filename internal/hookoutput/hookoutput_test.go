package hookoutput

import (
	"encoding/json"
	"strings"
	"testing"
)

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
		// Codex's PermissionRequest output nests the decision under
		// hookSpecificOutput.decision.behavior (issue #1183) — the legacy
		// top-level {"decision":...} is rejected by Codex's deny_unknown_fields.
		{"codex", "allow", "", `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`},
		{"codex", "deny", "", `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny"}}}`},
		{"codex", "block", "", `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny"}}}`},
		{"codex", "block", "neep-forbidden", `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"neep-forbidden"}}}`},
		// defer/unknown omit the decision so Codex runs its own approval flow.
		{"codex", "defer", "", `{"hookSpecificOutput":{"hookEventName":"PermissionRequest"}}`},
		{"codex", "haar", "", `{"hookSpecificOutput":{"hookEventName":"PermissionRequest"}}`},
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

	codexWant := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`
	if got := AllowAll("codex"); got != codexWant {
		t.Errorf("AllowAll(codex) = %s, want %s", got, codexWant)
	}
}

// TestInboxContextClaude is the regression test for issue #1072: Claude Code
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

// TestDialectForHookMechanism locks the mechanism→dialect mapping (issue #1236):
// the hook-output schema is chosen from the agent's configured hook mechanism, so
// a custom alias installing a known mechanism emits the right dialect. An empty or
// unknown mechanism falls back to the generic dialect ("").
func TestDialectForHookMechanism(t *testing.T) {
	tests := []struct {
		mechanism string
		want      string
	}{
		{"claude_settings", DialectClaude},
		{"codex_config", DialectCodex},
		{"cursor_project", DialectCursor},
		{"", ""},
		{"something_else", ""},
	}
	for _, tt := range tests {
		if got := DialectForHookMechanism(tt.mechanism); got != tt.want {
			t.Errorf("DialectForHookMechanism(%q) = %q, want %q", tt.mechanism, got, tt.want)
		}
	}
}
