package cli

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/hookoutput"
	"github.com/d0ugal/graith/internal/protocol"
)

// withCfg temporarily installs c as the package config for a test, restoring the
// previous value afterwards. The hook commands read the global cfg, so the
// dialect-selection tests must set it explicitly.
func withCfg(t *testing.T, c *config.Config) {
	t.Helper()

	prev := cfg
	cfg = c

	t.Cleanup(func() { cfg = prev })
}

// customAliasConfig returns a config whose custom aliases mirror each built-in
// hook mechanism, so the dialect must be derived from the mechanism rather than
// the literal agent name.
func customAliasConfig() *config.Config {
	c := config.Default()
	c.Agents["dreich-claude"] = config.Agent{Command: "claude-wrapper", Hooks: &config.AgentHookConfig{Mechanism: config.HookMechanismClaudeSettings}}
	c.Agents["dreich-codex"] = config.Agent{Command: "codex-wrapper", Hooks: &config.AgentHookConfig{Mechanism: config.HookMechanismCodexConfig}}
	c.Agents["dreich-cursor"] = config.Agent{Command: "cursor-wrapper", Hooks: &config.AgentHookConfig{Mechanism: config.HookMechanismCursorProject}}
	c.Agents["dreich-plain"] = config.Agent{Command: "plain-wrapper"} // no hooks

	return c
}

// TestHookOutputDialectFromMechanism is the end-to-end regression for issue
// #1236: the running hook process picks its output dialect from the agent's
// configured hook mechanism, so a custom alias emits the correct schema even
// though its name is not "claude"/"codex"/"cursor". A nil config or an agent
// without hooks falls back to the generic dialect.
func TestHookOutputDialectFromMechanism(t *testing.T) {
	withCfg(t, customAliasConfig())

	tests := []struct {
		agent string
		want  string
	}{
		{"dreich-claude", hookoutput.DialectClaude},
		{"dreich-codex", hookoutput.DialectCodex},
		{"dreich-cursor", hookoutput.DialectCursor},
		{"dreich-plain", ""},
		{"unknown-agent", ""},
	}
	for _, tt := range tests {
		if got := hookOutputDialect(tt.agent); got != tt.want {
			t.Errorf("hookOutputDialect(%q) = %q, want %q", tt.agent, got, tt.want)
		}
	}

	prev := cfg
	cfg = nil

	if got := hookOutputDialect("dreich-claude"); got != "" {
		t.Errorf("hookOutputDialect with nil cfg = %q, want \"\"", got)
	}

	cfg = prev
}

// TestCustomAliasApprovalStdout exercises the actual approval hook stdout (not
// just the injected argv): a custom Claude alias must emit Claude's
// hookSpecificOutput schema, a custom Codex alias must emit the PermissionRequest
// schema, and a custom Cursor alias the {"permission":…} schema — all keyed off
// the configured mechanism, not the alias name (issue #1236).
func TestCustomAliasApprovalStdout(t *testing.T) {
	withCfg(t, customAliasConfig())

	tests := []struct {
		agent     string
		wantSubst string
		notSubst  string
	}{
		{"dreich-claude", `"permissionDecision":"deny"`, `{"decision"`},
		{"dreich-codex", `"hookEventName":"PermissionRequest"`, `"permissionDecision"`},
		{"dreich-cursor", `"permission":"deny"`, `"hookSpecificOutput"`},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			dialect := hookOutputDialect(tt.agent)
			c := &fakeApprovalControlConn{resp: approvalEnvelope(t, "block", "canny policy")}
			out := submitApprovalRequest(c, dialect, protocol.ApprovalRequestMsg{RequestID: "canny"})

			if !strings.Contains(out, tt.wantSubst) {
				t.Errorf("approval stdout for %q = %q, want substring %q", tt.agent, out, tt.wantSubst)
			}

			if strings.Contains(out, tt.notSubst) {
				t.Errorf("approval stdout for %q = %q, should not contain %q", tt.agent, out, tt.notSubst)
			}
		})
	}
}

// TestCustomAliasInboxStdout exercises the actual inbox hook stdout: a custom
// Claude/Codex alias must deliver context through hookSpecificOutput.additional
// Context (model-visible), while a custom Cursor alias keeps the systemMessage
// form — again derived from the mechanism, not the alias name (issue #1236).
func TestCustomAliasInboxStdout(t *testing.T) {
	withCfg(t, customAliasConfig())

	const body = "You have 1 unread message(s). From braw: hello"

	modelVisible := []string{"dreich-claude", "dreich-codex"}
	for _, agent := range modelVisible {
		out := hookoutput.InboxContext(hookOutputDialect(agent), "SessionStart", body)
		if !strings.Contains(out, `"additionalContext"`) {
			t.Errorf("inbox stdout for %q = %q, want additionalContext", agent, out)
		}

		if strings.Contains(out, `"systemMessage"`) {
			t.Errorf("inbox stdout for %q = %q, should not carry systemMessage", agent, out)
		}
	}

	out := hookoutput.InboxContext(hookOutputDialect("dreich-cursor"), "SessionStart", body)
	if !strings.Contains(out, `"systemMessage"`) || strings.Contains(out, "hookSpecificOutput") {
		t.Errorf("inbox stdout for dreich-cursor = %q, want systemMessage form", out)
	}
}
