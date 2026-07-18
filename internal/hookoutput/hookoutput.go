package hookoutput

import "encoding/json"

type commandPolicyResponse struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// codexCommandPolicyResponse models Codex's current PermissionRequest hook-output
// contract. Codex's PermissionRequestCommandOutputWire carries the decision
// under hookSpecificOutput.decision.behavior ("allow" | "deny") — NOT the legacy
// top-level "decision" field — and uses deny_unknown_fields, so a top-level
// "decision" is rejected and the decision silently dropped (issue #1183).
type codexCommandPolicyResponse struct {
	HookSpecificOutput codexPermissionHookSpecificOutput `json:"hookSpecificOutput"`
}

// codexPermissionHookSpecificOutput carries the PermissionRequest decision.
// Decision retains Codex's optional wire shape, but command-policy responses
// always populate it with an immediate allow or deny.
type codexPermissionHookSpecificOutput struct {
	HookEventName string                   `json:"hookEventName"`
	Decision      *codexPermissionDecision `json:"decision,omitempty"`
}

type codexPermissionDecision struct {
	Behavior string `json:"behavior"`
	Message  string `json:"message,omitempty"`
}

type cursorCommandPolicyResponse struct {
	Permission string `json:"permission"`
	Reason     string `json:"reason,omitempty"`
}

// claudeCommandPolicyResponse models Claude Code's current PreToolUse hook-output
// contract, which carries the permission decision under hookSpecificOutput
// rather than the legacy top-level "decision" field (still accepted by Claude,
// but deprecated). See the PreToolUse decision-control section of
// https://docs.claude.com/en/docs/claude-code/hooks (source:
// claude-code/src/utils/hooks.ts, permissions/permissions.ts).
type claudeCommandPolicyResponse struct {
	HookSpecificOutput claudeHookSpecificOutput `json:"hookSpecificOutput"`
}

// claudeHookSpecificOutput carries the PreToolUse permission decision.
// PermissionDecision is always "allow" or "deny". UpdatedInput, when
// non-nil, rewrites the tool input Claude will run (input rewriting/redaction).
// graith does not populate UpdatedInput yet, but the field is here so the
// struct matches the full contract and can carry it in future.
type claudeHookSpecificOutput struct {
	HookEventName            string          `json:"hookEventName"`
	PermissionDecision       string          `json:"permissionDecision"`
	PermissionDecisionReason string          `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             json.RawMessage `json:"updatedInput,omitempty"`
}

// CommandPolicy returns a deterministic allow-or-deny response in each agent's
// hook schema. Unknown decisions are denied; native prompting is never resumed.
func CommandPolicy(agent, decision, reason string) string {
	switch agent {
	case "claude":
		return marshalString(claudeCommandPolicyResponse{
			HookSpecificOutput: claudeHookSpecificOutput{
				HookEventName:            "PreToolUse",
				PermissionDecision:       claudeDecision(decision),
				PermissionDecisionReason: reason,
			},
		})
	case "codex":
		resp := codexCommandPolicyResponse{
			HookSpecificOutput: codexPermissionHookSpecificOutput{
				HookEventName: "PermissionRequest",
			},
		}
		resp.HookSpecificOutput.Decision = &codexPermissionDecision{Behavior: codexBehavior(decision), Message: reason}

		return marshalString(resp)
	case "cursor":
		return marshalString(cursorCommandPolicyResponse{
			Permission: cursorDecision(decision),
			Reason:     reason,
		})
	default:
		return marshalString(commandPolicyResponse{Decision: denyUnlessAllow(decision), Reason: reason})
	}
}

// marshalString marshals v to a JSON string. The values passed here are fixed
// hook-response structs and string maps that cannot fail to marshal, so a
// (theoretically impossible) error yields an empty string rather than being
// silently discarded.
func marshalString(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return ""
	}

	return string(out)
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
	case "claude", "codex":
		// Both Claude and current Codex deliver model-visible context through
		// hookSpecificOutput.additionalContext; their universal systemMessage is
		// only a user-facing banner. (Codex's *CommandOutputWire types carry
		// additionalContext under hookSpecificOutput — issue #1183.)
		var resp claudeHookOutput

		resp.HookSpecificOutput.HookEventName = event
		resp.HookSpecificOutput.AdditionalContext = context

		return marshalString(resp)
	default:
		return marshalString(map[string]string{"systemMessage": context})
	}
}

// codexBehavior maps graith's internal decision vocabulary onto Codex's
// PermissionRequest behavior ("allow" | "deny"). "block" and "deny" both refuse.
// Every other value is denied so native prompting can never resume.
func codexBehavior(d string) string {
	switch d {
	case "allow":
		return "allow"
	case "block", "deny":
		return "deny"
	default:
		return "deny"
	}
}

// claudeDecision maps graith's internal decision vocabulary onto Claude Code's
// permissionDecision values ("allow" | "deny"). "block" and "deny" both
// refuse, as do unknown or interactive decisions.
func claudeDecision(d string) string {
	switch d {
	case "allow":
		return "allow"
	case "block", "deny":
		return "deny"
	default:
		return "deny"
	}
}

// Cursor uses "permission" field with "allow" and "deny" values.
func cursorDecision(d string) string {
	return denyUnlessAllow(d)
}

func denyUnlessAllow(d string) string {
	if d == "allow" {
		return "allow"
	}
	return "deny"
}
