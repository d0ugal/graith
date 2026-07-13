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
// but deprecated). See claude-code/src/utils/hooks.ts and
// permissions/permissions.ts.
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
