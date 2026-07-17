package hookoutput

import "encoding/json"

// approvalResponse is the legacy top-level decision format. It is still used by
// agents whose hook schema expects a top-level "decision" field (agy).
type approvalResponse struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// codexApprovalResponse models Codex's current PermissionRequest hook-output
// contract. Codex's PermissionRequestCommandOutputWire carries the decision
// under hookSpecificOutput.decision.behavior ("allow" | "deny") — NOT the legacy
// top-level "decision" field — and uses deny_unknown_fields, so a top-level
// "decision" is rejected and the decision silently dropped, defeating the
// approval bridge (issue #1183).
type codexApprovalResponse struct {
	HookSpecificOutput codexPermissionHookSpecificOutput `json:"hookSpecificOutput"`
}

// codexPermissionHookSpecificOutput carries the PermissionRequest decision.
// Decision is a pointer so it can be omitted: when graith defers (or the
// decision is unrecognised) no decision is sent and Codex falls back to its own
// approval flow rather than being forced to allow or deny.
type codexPermissionHookSpecificOutput struct {
	HookEventName string                   `json:"hookEventName"`
	Decision      *codexPermissionDecision `json:"decision,omitempty"`
}

type codexPermissionDecision struct {
	Behavior string `json:"behavior"`
	Message  string `json:"message,omitempty"`
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

// Hook-output dialect names. A dialect selects the JSON schema a hook emits; it
// is derived from the agent's configured hook mechanism (DialectForHookMechanism)
// rather than the literal agent name, so a custom Claude/Codex/Cursor alias emits
// the correct approval/inbox payload for the mechanism it installed (issue #1236).
const (
	DialectClaude = "claude"
	DialectCodex  = "codex"
	DialectCursor = "cursor"
)

// DialectForHookMechanism maps an agent's configured hook mechanism (config's
// HookMechanism* constants) to the hook-output dialect that mechanism speaks.
// An empty or unknown mechanism yields "" — the generic top-level {"decision":…}
// form — so an agent with no recognised hook wiring still produces valid output.
// The mechanism strings are duplicated here as literals to keep hookoutput free
// of a config dependency; config.Validate is the source of truth for the set.
func DialectForHookMechanism(mechanism string) string {
	switch mechanism {
	case "claude_settings":
		return DialectClaude
	case "codex_config":
		return DialectCodex
	case "cursor_project":
		return DialectCursor
	default:
		return ""
	}
}

// Approval returns JSON for a hook approval decision formatted for the given
// dialect. The decision parameter uses graith's internal values ("allow",
// "block", "deny", "defer"). Each dialect maps these to its own hook schema
// vocabulary; an unknown dialect uses the generic top-level "decision" form.
func Approval(dialect, decision, reason string) string {
	switch dialect {
	case DialectClaude:
		return marshalString(claudeApprovalResponse{
			HookSpecificOutput: claudeHookSpecificOutput{
				HookEventName:            "PreToolUse",
				PermissionDecision:       claudeDecision(decision),
				PermissionDecisionReason: reason,
			},
		})
	case DialectCodex:
		resp := codexApprovalResponse{
			HookSpecificOutput: codexPermissionHookSpecificOutput{
				HookEventName: "PermissionRequest",
			},
		}
		if behavior := codexBehavior(decision); behavior != "" {
			resp.HookSpecificOutput.Decision = &codexPermissionDecision{
				Behavior: behavior,
				Message:  reason,
			}
		}

		return marshalString(resp)
	case DialectCursor:
		return marshalString(cursorApprovalResponse{
			Permission: cursorDecision(decision),
			Reason:     reason,
		})
	default:
		return marshalString(approvalResponse{Decision: decision, Reason: reason})
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

// AllowAll returns a fail-open approval response for the given dialect.
func AllowAll(dialect string) string {
	return Approval(dialect, "allow", "")
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
// to an agent at a lifecycle event (e.g. "SessionStart"). For the Claude and
// Codex dialects the context must be delivered via
// hookSpecificOutput.additionalContext so it actually reaches the model — a
// plain systemMessage is shown to the human only. Other dialects keep the
// systemMessage form they already consume. The dialect is derived from the
// agent's hook mechanism (DialectForHookMechanism), so a custom alias emits the
// right shape (issue #1236).
func InboxContext(dialect, event, context string) string {
	switch dialect {
	case DialectClaude, DialectCodex:
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
// "defer"/"ask" (and anything unrecognised) return "" so the caller omits the
// decision entirely and Codex falls back to its own approval flow.
func codexBehavior(d string) string {
	switch d {
	case "allow":
		return "allow"
	case "block", "deny":
		return "deny"
	default:
		return ""
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

// Cursor uses "permission" field with "allow" and "deny" values.
func cursorDecision(d string) string {
	if d == "block" {
		return "deny"
	}

	return d
}
