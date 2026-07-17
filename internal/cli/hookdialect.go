package cli

import "github.com/d0ugal/graith/internal/hookoutput"

// hookOutputDialect selects the hook-output dialect for the running hook process
// from the agent's configured hook mechanism rather than the literal
// GRAITH_AGENT_TYPE, so a custom Claude/Codex/Cursor alias emits the approval and
// inbox JSON its installed hook expects (issue #1236). A nil config or an agent
// with no recognised hook mechanism yields the generic dialect ("").
func hookOutputDialect(agentType string) string {
	if cfg == nil {
		return ""
	}

	return hookoutput.DialectForHookMechanism(cfg.Agents[agentType].HookMechanism())
}
