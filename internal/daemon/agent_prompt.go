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

func detectPromptInjection(command string) promptInjectionMethod {
	base := filepath.Base(command)
	switch base {
	case "claude":
		return promptInjectionAppendSystemPrompt
	case "agent":
		return promptInjectionCursorRules
	default:
		return promptInjectionNone
	}
}

const graithPrompt = `You are running inside a graith session — a terminal multiplexer for AI coding agents.

## Status updates

Run ` + "`gr status \"message\"`" + ` to set your status visible to the user and other agents in the session picker. Update at key milestones:

` + "```" + `
gr status "Exploring code"
gr status "Running tests"
gr status "Waiting for CI"
gr status "Done"
` + "```" + `

## Inter-agent messaging

` + "```" + `
gr msg send <session> "text"       # message another session directly
gr msg pub --topic <topic> "text"  # broadcast to a topic
gr msg sub --topic inbox:$GRAITH_SESSION_ID --wait  # wait for a direct message
` + "```" + `

## Environment

- GRAITH_SESSION_ID — your session ID (set automatically)
- GRAITH_SESSION_NAME — your session name
- GRAITH_WORKTREE_PATH — your working directory

Run ` + "`gr --help`" + ` for the full command list.`

func (sm *SessionManager) injectPrompt(command, worktreePath string) (extraArgs []string, err error) {
	method := detectPromptInjection(command)
	switch method {
	case promptInjectionAppendSystemPrompt:
		return []string{"--append-system-prompt", graithPrompt}, nil
	case promptInjectionCursorRules:
		return nil, writeCursorRule(worktreePath)
	default:
		return nil, nil
	}
}

func writeCursorRule(worktreePath string) error {
	if worktreePath == "" {
		return nil
	}
	rulesDir := filepath.Join(worktreePath, ".cursor", "rules")
	if err := os.MkdirAll(rulesDir, 0o700); err != nil {
		return fmt.Errorf("create .cursor/rules dir: %w", err)
	}

	rule := cursorRuleContent(graithPrompt)
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
}
