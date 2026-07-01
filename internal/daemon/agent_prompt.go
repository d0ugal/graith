package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type promptInjectionMethod int

const (
	promptInjectionNone               promptInjectionMethod = iota
	promptInjectionAppendSystemPrompt                       // --append-system-prompt (Claude)
	promptInjectionCursorRules                              // .cursor/rules/graith.mdc (Cursor)
)

func detectPromptInjection(agentName string) promptInjectionMethod {
	switch agentName {
	case "claude":
		return promptInjectionAppendSystemPrompt
	case "cursor":
		return promptInjectionCursorRules
	default:
		return promptInjectionNone
	}
}

func (sm *SessionManager) injectPrompt(agentName, worktreePath string) (extraArgs []string, err error) {
	prompt := sm.Config().AgentPrompt
	if prompt == "" {
		return nil, nil
	}

	method := detectPromptInjection(agentName)
	switch method {
	case promptInjectionAppendSystemPrompt:
		return []string{"--append-system-prompt", prompt}, nil
	case promptInjectionCursorRules:
		return nil, writeCursorRule(worktreePath, prompt)
	default:
		return nil, nil
	}
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
