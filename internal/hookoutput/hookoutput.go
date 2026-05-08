package hookoutput

import "encoding/json"

type approvalResponse struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// Approval returns JSON for a hook approval decision formatted for the given agent.
// The decision parameter uses graith's internal values ("allow", "block", "deny").
// Each agent maps these to its own hook schema vocabulary.
func Approval(agent, decision, reason string) string {
	mapped := mapDecision(agent, decision)
	resp := approvalResponse{Decision: mapped, Reason: reason}
	out, _ := json.Marshal(resp)
	return string(out)
}

// AllowAll returns a fail-open approval response for the given agent.
func AllowAll(agent string) string {
	return Approval(agent, "allow", "")
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
