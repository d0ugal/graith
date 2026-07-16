package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/d0ugal/graith/internal/config"
)

type promptInjectionMethod int

const (
	promptInjectionNone                  promptInjectionMethod = iota
	promptInjectionAppendSystemPrompt                          // --append-system-prompt (Claude)
	promptInjectionCursorRules                                 // .cursor/rules/graith.mdc (Cursor)
	promptInjectionDeveloperInstructions                       // -c developer_instructions=... (Codex)
)

// detectPromptInjection resolves the injection mechanism for an agent. An
// explicit [agents.<name>].prompt_injection value wins so a custom agent can
// declare its mechanism (or opt out with "none"); an empty configured value
// falls back to name-based detection so the built-in claude, cursor, and codex
// agents keep working without config. See issue #1232.
func detectPromptInjection(agentName, configured string) promptInjectionMethod {
	switch configured {
	case config.PromptInjectionAppendSystemPrompt:
		return promptInjectionAppendSystemPrompt
	case config.PromptInjectionCursorRules:
		return promptInjectionCursorRules
	case config.PromptInjectionDeveloperInstructions:
		return promptInjectionDeveloperInstructions
	case config.PromptInjectionNone:
		return promptInjectionNone
	}

	switch agentName {
	case "claude":
		return promptInjectionAppendSystemPrompt
	case "cursor":
		return promptInjectionCursorRules
	case "codex":
		return promptInjectionDeveloperInstructions
	default:
		return promptInjectionNone
	}
}

func (sm *SessionManager) injectPrompt(agentName, worktreePath string) (extraArgs []string, err error) {
	cfg := sm.Config()
	return promptInjectionArgs(agentName, cfg.Agents[agentName], cfg.AgentPrompt, worktreePath)
}

// promptInjectionArgs adapts an already-assembled prompt to the injection
// mechanism the agent actually supports, returning the launch args (and
// performing any side effects such as writing a Cursor rule file). It is the
// single agent-aware seam shared by ordinary sessions (injectPrompt) and the
// orchestrator (buildOrchestratorPrompt) so that a non-Claude agent never gets
// launched with Claude's --append-system-prompt flag. The configured argument
// is the agent's [agents.<name>].prompt_injection override ("" for name-based
// detection). An empty prompt or an agent with no supported injection method
// yields no args.
func promptInjectionArgs(agentName string, agent config.Agent, prompt, worktreePath string) (extraArgs []string, err error) {
	if prompt == "" {
		return nil, nil
	}

	method := detectPromptInjection(agentName, agent.PromptInjection)
	switch method {
	case promptInjectionAppendSystemPrompt:
		// The prompt is passed verbatim; only the flag spelling comes from config
		// (agents.<name>.prompt_injection_args, {prompt} bound). (issue #1236)
		if len(agent.PromptInjectionArgs) == 0 {
			return []string{"--append-system-prompt", prompt}, nil
		}

		return config.ExpandSliceWith(agent.PromptInjectionArgs, map[string]string{"prompt": prompt})
	case promptInjectionCursorRules:
		return nil, writeCursorRule(worktreePath, prompt)
	case promptInjectionDeveloperInstructions:
		// The value is JSON-encoded in Go (a valid TOML basic string); only the -c
		// override spelling comes from config. (issue #1236)
		if len(agent.PromptInjectionArgs) == 0 {
			return codexDeveloperInstructionsArgs(prompt), nil
		}

		return config.ExpandSliceWith(agent.PromptInjectionArgs, map[string]string{"prompt": codexDeveloperInstructionsValue(prompt)})
	default:
		return nil, nil
	}
}

// codexDeveloperInstructionsArgs builds the Codex config-override args that
// inject the graith operating instructions as Codex's `developer_instructions`
// (a top-level config key surfaced to the model as a separate developer
// message). Codex parses the `-c key=value` value as TOML, falling back to a
// bare string; we JSON-encode the prompt so a multi-line body — or one that
// would otherwise parse as a TOML scalar (e.g. a lone number or `[...]`) — is
// always carried verbatim as a quoted string. A JSON-encoded string is also a
// valid TOML basic string, so Codex's TOML parse decodes it back to the exact
// prompt.
func codexDeveloperInstructionsArgs(prompt string) []string {
	return []string{"-c", "developer_instructions=" + codexDeveloperInstructionsValue(prompt)}
}

// codexDeveloperInstructionsValue JSON-encodes prompt into the string value used
// after `developer_instructions=`. A JSON-encoded string is also a valid TOML
// basic string, so Codex's `-c key=value` TOML parse decodes it back verbatim —
// carrying a multi-line body or one that would otherwise parse as a TOML scalar.
func codexDeveloperInstructionsValue(prompt string) string {
	// json.Marshal of a string never fails; the error check satisfies the
	// linter and strconv.Quote is an unreachable, equivalent fallback.
	encoded, err := json.Marshal(prompt)
	if err != nil {
		encoded = []byte(strconv.Quote(prompt))
	}

	return string(encoded)
}

func writeCursorRule(worktreePath, prompt string) error {
	if worktreePath == "" {
		return nil
	}

	rulesDir := filepath.Join(worktreePath, ".cursor", "rules")
	if err := os.MkdirAll(rulesDir, 0o700); err != nil {
		return fmt.Errorf("create .cursor/rules dir: %w", err)
	}

	rule := cursorRuleContent(prompt)

	path := filepath.Join(rulesDir, "graith.mdc")
	if err := os.WriteFile(path, []byte(rule), 0o600); err != nil {
		return fmt.Errorf("write cursor rule: %w", err)
	}

	return nil
}

func cursorRuleContent(prompt string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("description: graith session context (auto-generated, do not edit)\n")
	b.WriteString("globs: \n")
	b.WriteString("alwaysApply: true\n")
	b.WriteString("---\n\n")
	b.WriteString(prompt)
	b.WriteString("\n")

	return b.String()
}

func cleanupCursorRule(worktreePath string) {
	if worktreePath == "" {
		return
	}

	rulePath := filepath.Join(worktreePath, ".cursor", "rules", "graith.mdc")
	_ = os.Remove(rulePath)

	rulesDir := filepath.Join(worktreePath, ".cursor", "rules")

	entries, err := os.ReadDir(rulesDir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(rulesDir)
	}

	cursorDir := filepath.Join(worktreePath, ".cursor")

	entries, err = os.ReadDir(cursorDir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(cursorDir)
	}
}
