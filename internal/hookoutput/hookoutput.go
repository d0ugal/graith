package hookoutput

import "encoding/json"

// approvalResponse is the legacy top-level decision format. It is still used by
// agents whose hook schema expects a top-level "decision" field (codex, agy).
type approvalResponse struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

type cursorApprovalResponse struct {
	Permission string `json:"permission"`
	Reason     string `json:"reason,omitempty"`
}

// claudeApprovalResponse models Claude Code's current PreToolUse hook-output
// contract, which carries the permission decision under hookSpecificOutput
// rather than the legacy top-level "decision" field (still accepted by Claude,
// but deprecated). See the PreToolUse decision-control section of
// https://docs.claude.com/en/docs/claude-code/hooks (source:
// claude-code/src/utils/hooks.ts, permissions/permissions.ts).
type claudeApprovalResponse struct {
	HookSpecificOutput claudeHookSpecificOutput `json:"hookSpecificOutput"`
}

// claudeHookSpecificOutput carries the PreToolUse permission decision.
// PermissionDecision is one of "allow", "deny", or "ask". UpdatedInput, when
// non-nil, rewrites the tool input Claude will run (input rewriting/redaction).
// graith does not populate UpdatedInput yet, but the field is here so the
// struct matches the full contract and can carry it in future.
type claudeHookSpecificOutput struct {
	HookEventName            string          `json:"hookEventName"`
	PermissionDecision       string          `json:"permissionDecision"`
	PermissionDecisionReason string          `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             json.RawMessage `json:"updatedInput,omitempty"`
}

// Approval returns JSON for a hook approval decision formatted for the given agent.
// The decision parameter uses graith's internal values ("allow", "block", "deny",
// "defer"). Each agent maps these to its own hook schema vocabulary.
func Approval(agent, decision, reason string) string {
	switch agent {
	case "claude":
		resp := claudeApprovalResponse{
			HookSpecificOutput: claudeHookSpecificOutput{
				HookEventName:            "PreToolUse",
				PermissionDecision:       claudeDecision(decision),
				PermissionDecisionReason: reason,
			},
		}
		out, _ := json.Marshal(resp)

		return string(out)
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
	case "codex":
		return codexDecision(internal)
	default:
		return internal
	}
}

// claudeDecision maps graith's internal decision vocabulary onto Claude Code's
// permissionDecision values ("allow" | "deny" | "ask"). "block" and "deny" both
// refuse; "defer" (and "ask") hand the decision back to Claude's own prompt.
func claudeDecision(d string) string {
	switch d {
	case "allow":
		return "allow"
	case "block", "deny":
		return "deny"
	case "defer", "ask":
		return "ask"
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
