package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/pelletier/go-toml/v2"
)

func TestDetectPromptInjection(t *testing.T) {
	tests := []struct {
		agentName string
		want      promptInjectionMethod
	}{
		{"claude", promptInjectionAppendSystemPrompt},
		{"cursor", promptInjectionCursorRules},
		{"codex", promptInjectionDeveloperInstructions},
		{"opencode", promptInjectionNone},
		{"agy", promptInjectionNone},
		{"", promptInjectionNone},
	}
	for _, tt := range tests {
		t.Run(tt.agentName, func(t *testing.T) {
			got := detectPromptInjection(tt.agentName)
			if got != tt.want {
				t.Errorf("detectPromptInjection(%q) = %d, want %d", tt.agentName, got, tt.want)
			}
		})
	}
}

func testSessionManager() *SessionManager {
	return &SessionManager{cfg: config.Default()}
}

func TestInjectPrompt_Claude(t *testing.T) {
	sm := testSessionManager()

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
	sm := testSessionManager()

	args, err := sm.injectPrompt("cursor", dir)
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
	sm := testSessionManager()

	// "opencode" has no prompt-injection method (agy/opencode both fall through).
	args, err := sm.injectPrompt("opencode", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(args) != 0 {
		t.Errorf("expected no args for unknown agent, got %d", len(args))
	}
}

// TestInjectPrompt_Codex is the regression test for #1185: Codex sessions were
// silently getting no graith operating instructions because detectPromptInjection
// returned promptInjectionNone. They must now receive the prompt via a
// `-c developer_instructions=<json>` config override.
func TestInjectPrompt_Codex(t *testing.T) {
	sm := testSessionManager()

	args, err := sm.injectPrompt("codex", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(args), args)
	}

	if args[0] != "-c" {
		t.Errorf("args[0] = %q, want -c", args[0])
	}

	const prefix = "developer_instructions="
	if !strings.HasPrefix(args[1], prefix) {
		t.Fatalf("args[1] = %q, want prefix %q", args[1], prefix)
	}

	// The value must be a JSON-encoded string that decodes back to a prompt
	// mentioning graith's commands (verbatim, incl. newlines).
	encoded := strings.TrimPrefix(args[1], prefix)

	var decoded string
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("value after %q is not valid JSON: %v (%q)", prefix, err, encoded)
	}

	if !strings.Contains(decoded, "gr status") {
		t.Error("decoded developer_instructions should mention gr status")
	}

	if decoded != sm.Config().AgentPrompt {
		t.Error("decoded developer_instructions should equal the configured agent prompt verbatim")
	}
}

// TestCodexDeveloperInstructionsArgs_RoundTrip guards the JSON-encoding choice
// against Codex's actual `-c key=value` parser: Codex parses the value as TOML
// (falling back to a bare string), so it decodes the value the way this test's
// TOML decode does. A prompt that would otherwise be misparsed as a TOML scalar
// (a lone number/bool, an array-looking line) or that embeds quotes/newlines
// must survive verbatim. We decode the emitted value BOTH as JSON (proving it's
// a well-formed encoding) and as a TOML basic string via the same parser family
// Codex uses (proving Codex reconstructs the exact prompt).
func TestCodexDeveloperInstructionsArgs_RoundTrip(t *testing.T) {
	prompts := []string{
		"42",
		"true",
		"[not, an, array]",
		"line one\nline two\twith tab",
		`he said "braw" and 'canny'`,
		"trailing space and =equals= signs",
		config.Default().AgentPrompt, // the real multi-line prompt
	}

	for _, prompt := range prompts {
		args := codexDeveloperInstructionsArgs(prompt)
		if len(args) != 2 || args[0] != "-c" {
			t.Fatalf("codexDeveloperInstructionsArgs(%q) = %v", prompt, args)
		}

		if !strings.HasPrefix(args[1], `developer_instructions="`) {
			t.Errorf("value should be a quoted string: %q", args[1])
		}

		encoded := strings.TrimPrefix(args[1], "developer_instructions=")

		var jsonDecoded string
		if err := json.Unmarshal([]byte(encoded), &jsonDecoded); err != nil {
			t.Fatalf("value for %q is not valid JSON: %v", prompt, err)
		}

		if jsonDecoded != prompt {
			t.Errorf("JSON round-trip mismatch: got %q, want %q", jsonDecoded, prompt)
		}

		// Codex wraps the value as `_x_ = <value>` and parses it as TOML. Mirror
		// that here: a JSON string literal must also be a valid TOML basic string
		// decoding back to the original prompt.
		var tomlDoc struct {
			Value string `toml:"developer_instructions"`
		}
		if err := toml.Unmarshal([]byte(args[1]), &tomlDoc); err != nil {
			t.Fatalf("value for %q does not parse as TOML: %v", prompt, err)
		}

		if tomlDoc.Value != prompt {
			t.Errorf("TOML round-trip mismatch: got %q, want %q", tomlDoc.Value, prompt)
		}
	}
}

func TestInjectPrompt_CursorEmptyWorktree(t *testing.T) {
	sm := testSessionManager()

	args, err := sm.injectPrompt("cursor", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(args) != 0 {
		t.Errorf("expected no args, got %d", len(args))
	}
}

func TestInjectPrompt_EmptyPrompt(t *testing.T) {
	sm := &SessionManager{cfg: &config.Config{AgentPrompt: ""}}

	args, err := sm.injectPrompt("claude", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(args) != 0 {
		t.Errorf("expected no args for empty prompt, got %d", len(args))
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
			agent := config.Agent{InjectPrompt: tt.val}
			if got := agent.PromptInjectionEnabled(); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCleanupCursorRule_RemovesEmptyCursorDir(t *testing.T) {
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

	cursorDir := filepath.Join(dir, ".cursor")
	if _, err := os.Stat(cursorDir); !os.IsNotExist(err) {
		t.Error("empty .cursor dir should be removed after cleanup")
	}
}

func TestCleanupCursorRule_PreservesCursorDirWithOtherFiles(t *testing.T) {
	dir := t.TempDir()

	cursorDir := filepath.Join(dir, ".cursor")

	rulesDir := filepath.Join(cursorDir, "rules")
	if err := os.MkdirAll(rulesDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(rulesDir, "graith.mdc"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cursorDir, "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanupCursorRule(dir)

	if _, err := os.Stat(cursorDir); err != nil {
		t.Error(".cursor dir should be preserved when other files exist")
	}
}

func boolPtr(b bool) *bool { return &b }
