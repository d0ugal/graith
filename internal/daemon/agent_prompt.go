package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type promptInjectionMethod int

const (
	promptInjectionNone                  promptInjectionMethod = iota
	promptInjectionAppendSystemPrompt                          // --append-system-prompt (Claude)
	promptInjectionCursorRules                                 // .cursor/rules/graith.mdc (Cursor)
	promptInjectionDeveloperInstructions                       // -c developer_instructions=... (Codex)
)

func detectPromptInjection(agentName string) promptInjectionMethod {
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
	return promptInjectionArgs(agentName, sm.Config().AgentPrompt, worktreePath)
}

// promptInjectionArgs adapts an already-assembled prompt to the injection
// mechanism the named agent actually supports, returning the launch args (and
// performing any side effects such as writing a Cursor rule file). It is the
// single agent-aware seam shared by ordinary sessions (injectPrompt) and the
// orchestrator (buildOrchestratorPrompt) so that a non-Claude agent never gets
// launched with Claude's --append-system-prompt flag. An empty prompt or an
// agent with no supported injection method yields no args.
func promptInjectionArgs(agentName, prompt, worktreePath string) (extraArgs []string, err error) {
	if prompt == "" {
		return nil, nil
	}

	method := detectPromptInjection(agentName)
	switch method {
	case promptInjectionAppendSystemPrompt:
		return []string{"--append-system-prompt", prompt}, nil
	case promptInjectionCursorRules:
		return nil, writeCursorRule(worktreePath, prompt)
	case promptInjectionDeveloperInstructions:
		return codexDeveloperInstructionsArgs(prompt), nil
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
	// json.Marshal of a string never fails; the error check satisfies the
	// linter and strconv.Quote is an unreachable, equivalent fallback.
	encoded, err := json.Marshal(prompt)
	if err != nil {
		encoded = []byte(strconv.Quote(prompt))
	}

	return []string{"-c", "developer_instructions=" + string(encoded)}
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
