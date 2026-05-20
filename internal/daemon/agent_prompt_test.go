package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectPromptInjection(t *testing.T) {
	tests := []struct {
		command string
		want    promptInjectionMethod
	}{
		{"claude", promptInjectionAppendSystemPrompt},
		{"/usr/local/bin/claude", promptInjectionAppendSystemPrompt},
		{"agent", promptInjectionCursorRules},
		{"/opt/homebrew/bin/agent", promptInjectionCursorRules},
		{"codex", promptInjectionNone},
		{"opencode", promptInjectionNone},
		{"agy", promptInjectionNone},
		{"", promptInjectionNone},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := detectPromptInjection(tt.command)
			if got != tt.want {
				t.Errorf("detectPromptInjection(%q) = %d, want %d", tt.command, got, tt.want)
			}
		})
	}
}

func TestInjectPrompt_Claude(t *testing.T) {
	sm := &SessionManager{}
	args, err := sm.injectPrompt("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d", len(args))
	}
	if args[0] != "--append-system-prompt" {
		t.Errorf("args[0] = %q, want --append-system-prompt", args[0])
	}
	if !strings.Contains(args[1], "gr status") {
		t.Error("prompt should mention gr status")
	}
}

func TestInjectPrompt_Cursor(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{}
	args, err := sm.injectPrompt("agent", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Errorf("expected no extra args for cursor, got %d", len(args))
	}

	rulePath := filepath.Join(dir, ".cursor", "rules", "graith.mdc")
	data, err := os.ReadFile(rulePath)
	if err != nil {
		t.Fatalf("rule file not created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "alwaysApply: true") {
		t.Error("rule should have alwaysApply: true frontmatter")
	}
	if !strings.Contains(content, "gr status") {
		t.Error("rule should mention gr status")
	}
}

func TestInjectPrompt_Unknown(t *testing.T) {
	sm := &SessionManager{}
	args, err := sm.injectPrompt("codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Errorf("expected no args for unknown agent, got %d", len(args))
	}
}

func TestInjectPrompt_CursorEmptyWorktree(t *testing.T) {
	sm := &SessionManager{}
	args, err := sm.injectPrompt("agent", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %d", len(args))
	}
}

func TestCleanupCursorRule(t *testing.T) {
	dir := t.TempDir()

	rulesDir := filepath.Join(dir, ".cursor", "rules")
	if err := os.MkdirAll(rulesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rulePath := filepath.Join(rulesDir, "graith.mdc")
	if err := os.WriteFile(rulePath, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanupCursorRule(dir)

	if _, err := os.Stat(rulePath); !os.IsNotExist(err) {
		t.Error("rule file should be removed after cleanup")
	}
	if _, err := os.Stat(rulesDir); !os.IsNotExist(err) {
		t.Error("empty rules dir should be removed after cleanup")
	}
}

func TestCleanupCursorRule_PreservesOtherRules(t *testing.T) {
	dir := t.TempDir()

	rulesDir := filepath.Join(dir, ".cursor", "rules")
	if err := os.MkdirAll(rulesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "graith.mdc"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "other.mdc"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanupCursorRule(dir)

	if _, err := os.Stat(filepath.Join(rulesDir, "graith.mdc")); !os.IsNotExist(err) {
		t.Error("graith.mdc should be removed")
	}
	if _, err := os.Stat(filepath.Join(rulesDir, "other.mdc")); err != nil {
		t.Error("other.mdc should be preserved")
	}
	if _, err := os.Stat(rulesDir); err != nil {
		t.Error("rules dir should be preserved when other files exist")
	}
}

func TestPromptInjectionEnabled(t *testing.T) {
	tests := []struct {
		name string
		val  *bool
		want bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Import cycle prevention: test the logic directly
			enabled := true
			if tt.val != nil {
				enabled = *tt.val
			}
			if enabled != tt.want {
				t.Errorf("got %v, want %v", enabled, tt.want)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }
