package hookoutput

import "encoding/json"

type approvalResponse struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

type cursorApprovalResponse struct {
	Permission string `json:"permission"`
	Reason     string `json:"reason,omitempty"`
}

// Approval returns JSON for a hook approval decision formatted for the given agent.
// The decision parameter uses graith's internal values ("allow", "block", "deny").
// Each agent maps these to its own hook schema vocabulary.
func Approval(agent, decision, reason string) string {
	switch agent {
	case "cursor":
		resp := cursorApprovalResponse{
			Permission: cursorDecision(decision),
			Reason:     reason,
		}
		out, _ := json.Marshal(resp)

		return string(out)
	default:
		mapped := mapDecision(agent, decision)
		resp := approvalResponse{Decision: mapped, Reason: reason}
		out, _ := json.Marshal(resp)

		return string(out)
	}
}

// AllowAll returns a fail-open approval response for the given agent.
func AllowAll(agent string) string {
	return Approval(agent, "allow", "")
}

// claudeHookOutput is Claude Code's hook stdout envelope for injecting model
// context. The nested hookSpecificOutput.additionalContext becomes a
// hook_additional_context attachment fed to the model; a top-level
// systemMessage (which graith emitted previously) is only a user-facing warning
// banner and never reaches the model. hookEventName is required and must match
// the firing event exactly. See claude-code src/utils/hooks.ts.
type claudeHookOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// InboxContext returns the hook stdout JSON that surfaces unread inbox messages
// to an agent at a lifecycle event (e.g. "SessionStart"). For Claude Code the
// context must be delivered via hookSpecificOutput.additionalContext so it
// actually reaches the model — a plain systemMessage is shown to the human only.
// Other agents keep the systemMessage form they already consume.
func InboxContext(agent, event, context string) string {
	switch agent {
	case "claude":
		var resp claudeHookOutput

		resp.HookSpecificOutput.HookEventName = event
		resp.HookSpecificOutput.AdditionalContext = context

		out, _ := json.Marshal(resp)

		return string(out)
	default:
		out, _ := json.Marshal(map[string]string{"systemMessage": context})

		return string(out)
	}
}

func mapDecision(agent, internal string) string {
	switch agent {
	case "claude":
		return claudeDecision(internal)
	case "codex":
		return codexDecision(internal)
	default:
		return internal
	}
}

// Claude Code expects "approve" or "block" in the top-level decision field.
// See: claude-code/src/types/hooks.ts syncHookResponseSchema
func claudeDecision(d string) string {
	switch d {
	case "allow":
		return "approve"
	case "deny":
		return "block"
	default:
		return d
	}
}

// Codex uses "allow" and "deny" natively; the daemon uses "block" internally.
func codexDecision(d string) string {
	if d == "block" {
		return "deny"
	}

	return d
}

// Cursor uses "permission" field with "allow" and "deny" values.
func cursorDecision(d string) string {
	if d == "block" {
		return "deny"
	}

	return d
}
