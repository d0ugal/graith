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
gr msg send <session> "text"          # message another session directly
gr msg send --parent "text"           # message your parent session
gr msg send --children "text"         # message all child sessions
gr msg pub --topic <topic> "text"     # broadcast to a topic
gr msg sub --topic inbox --all --ack  # read and acknowledge inbox messages
` + "```" + `

Note: ` + "`gr msg sub --wait`" + ` blocks until a message arrives. Use ` + "`--all`" + ` to read without blocking.

## Document store

Store and retrieve artifacts that persist across sessions using ` + "`gr store`" + `:

` + "```" + `
gr store put design/api.md --file ./api-design.md   # store a document
gr store put notes/summary.md "# Summary\n\n..."     # store inline content
gr store get design/api.md                           # retrieve a document
gr store list                                        # list all documents
gr store list design/                                # list by prefix
gr store rm design/api.md                            # remove a document
` + "```" + `

Documents are plain files in a per-repo git directory — browsable, greppable, and version-tracked. Use the store for artifacts you want to keep but don` + "`" + `t want to commit: design docs, research notes, build outputs, shared context between agents.

## Environment

- GRAITH_SESSION_ID — your session ID (set automatically)
- GRAITH_SESSION_NAME — your session name
- GRAITH_WORKTREE_PATH — absolute path to your session worktree
- GRAITH_REPO_PATH — absolute path to the source repository

Run ` + "`gr --help`" + ` for the full command list.`

func (sm *SessionManager) injectPrompt(agentName, worktreePath string) (extraArgs []string, err error) {
	method := detectPromptInjection(agentName)
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

	cursorDir := filepath.Join(worktreePath, ".cursor")
	entries, err = os.ReadDir(cursorDir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(cursorDir)
	}
}
