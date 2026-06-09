package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func newTestSessionManagerWithDataDir(t *testing.T) *SessionManager {
	t.Helper()
	dir := t.TempDir()
	return NewSessionManager(config.Default(), config.Paths{
		StateFile: filepath.Join(dir, "state.json"),
		DataDir:   dir,
	}, slog.Default())
}

func TestGenerateClaudeSettings(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "test-session-02"

	settingsPath, err := sm.generateClaudeSettings(sessionID, true)
	if err != nil {
		t.Fatalf("generateClaudeSettings() error = %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var parsed struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	expectedEvents := []string{
		"SessionStart",
		"UserPromptSubmit",
		"PreToolUse",
		"PostToolUse",
		"Notification",
		"Stop",
	}

	for _, event := range expectedEvents {
		matchers, ok := parsed.Hooks[event]
		if !ok {
			t.Errorf("missing hook event %q", event)
			continue
		}
		if len(matchers) != 1 {
			t.Errorf("event %q has %d matcher groups, want 1", event, len(matchers))
			continue
		}
		if matchers[0].Matcher != "" {
			t.Errorf("event %q matcher = %q, want empty (match-all)", event, matchers[0].Matcher)
		}
		if len(matchers[0].Hooks) != 1 {
			t.Errorf("event %q has %d hooks, want 1", event, len(matchers[0].Hooks))
			continue
		}
		hook := matchers[0].Hooks[0]
		if hook.Type != "command" {
			t.Errorf("event %q type = %q, want %q", event, hook.Type, "command")
		}
		if event == "PreToolUse" {
			if !strings.Contains(hook.Command, "approve-request") {
				t.Errorf("event %q command = %q, does not contain approve-request", event, hook.Command)
			}
		} else {
			if !strings.Contains(hook.Command, "report-status") {
				t.Errorf("event %q command = %q, does not contain report-status", event, hook.Command)
			}
			if !strings.Contains(hook.Command, "--event "+event) {
				t.Errorf("event %q command = %q, does not contain --event %s", event, hook.Command, event)
			}
		}
	}

	if len(parsed.Hooks) != len(expectedEvents) {
		t.Errorf("hooks has %d events, want %d", len(parsed.Hooks), len(expectedEvents))
	}
}

func TestGenerateClaudeSettingsNoApprovals(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "test-session-no-approvals"

	settingsPath, err := sm.generateClaudeSettings(sessionID, false)
	if err != nil {
		t.Fatalf("generateClaudeSettings() error = %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var parsed struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	if _, ok := parsed.Hooks["PreToolUse"]; ok {
		t.Error("PreToolUse should not be present when approvals=false")
	}

	for event := range parsed.Hooks {
		hook := parsed.Hooks[event][0].Hooks[0]
		if strings.Contains(hook.Command, "approve-request") {
			t.Errorf("event %q should not use approve-request when approvals=false", event)
		}
	}
}

func TestInjectClaudeHooks(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "test-session-03"

	extraArgs, extraEnv, err := sm.injectClaudeHooks(sessionID, false)
	if err != nil {
		t.Fatalf("injectClaudeHooks() error = %v", err)
	}

	if len(extraArgs) != 2 {
		t.Fatalf("extraArgs length = %d, want 2", len(extraArgs))
	}
	if extraArgs[0] != "--settings" {
		t.Errorf("extraArgs[0] = %q, want %q", extraArgs[0], "--settings")
	}
	if _, err := os.Stat(extraArgs[1]); err != nil {
		t.Errorf("settings file does not exist: %v", err)
	}

	if extraEnv != nil {
		t.Errorf("extraEnv = %v, want nil", extraEnv)
	}
}

func TestCleanupHooks(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "test-session-04"

	// Generate settings so there's something to clean up.
	_, err := sm.generateClaudeSettings(sessionID, false)
	if err != nil {
		t.Fatalf("generateClaudeSettings() error = %v", err)
	}

	hookDir := sm.hookDir(sessionID)
	if _, err := os.Stat(hookDir); err != nil {
		t.Fatalf("hook dir does not exist before cleanup: %v", err)
	}

	sm.cleanupHooks(sessionID)

	if _, err := os.Stat(hookDir); !os.IsNotExist(err) {
		t.Errorf("hook dir still exists after cleanup: err = %v", err)
	}
}

func TestCleanupHooksNonexistent(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sm.cleanupHooks("nonexistent-session")
}

func TestResolveGrBin(t *testing.T) {
	result := resolveGrBin()
	if result == "" {
		t.Error("resolveGrBin() returned empty string")
	}
}

func TestInjectCodexHooks(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "test-session-codex-01"

	extraArgs, extraEnv, err := sm.injectCodexHooks(sessionID, false)
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	if len(extraArgs) != 0 {
		t.Errorf("extraArgs length = %d, want 0", len(extraArgs))
	}

	hooksDir, ok := extraEnv["CODEX_HOOKS_DIR"]
	if !ok {
		t.Fatal("extraEnv missing CODEX_HOOKS_DIR")
	}
	if hooksDir == "" {
		t.Fatal("CODEX_HOOKS_DIR is empty")
	}

	if _, ok := extraEnv["GRAITH_BIN"]; ok {
		t.Error("extraEnv should not contain GRAITH_BIN")
	}

	info, err := os.Stat(hooksDir)
	if err != nil {
		t.Fatalf("codex hooks dir does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("CODEX_HOOKS_DIR is not a directory")
	}

	expectedScripts := []string{
		"session-start",
		"user-prompt-submit",
		"pre-tool-use",
		"post-tool-use",
		"stop",
	}

	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		t.Fatalf("read codex hooks dir: %v", err)
	}
	if len(entries) != len(expectedScripts) {
		t.Errorf("codex hooks dir has %d entries, want %d", len(entries), len(expectedScripts))
	}

	for _, name := range expectedScripts {
		path := filepath.Join(hooksDir, name)
		fi, err := os.Stat(path)
		if err != nil {
			t.Errorf("missing codex hook script %q: %v", name, err)
			continue
		}
		perm := fi.Mode().Perm()
		if perm&0o111 == 0 {
			t.Errorf("codex hook script %q is not executable: mode = %v", name, perm)
		}
	}
}

func TestInjectCodexHooksWithApprovals(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "test-session-codex-approvals"

	_, extraEnv, err := sm.injectCodexHooks(sessionID, true)
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	hooksDir := extraEnv["CODEX_HOOKS_DIR"]
	if _, err := os.Stat(filepath.Join(hooksDir, "permission-request")); err != nil {
		t.Error("permission-request hook should exist when approvals=true")
	}
}

func TestCodexHookScriptContent(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "test-session-codex-02"

	_, _, err := sm.injectCodexHooks(sessionID, true)
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	hooksDir := filepath.Join(sm.hookDir(sessionID), "codex-hooks")

	events := map[string]string{
		"session-start":      "SessionStart",
		"user-prompt-submit": "UserPromptSubmit",
		"pre-tool-use":       "PreToolUse",
		"post-tool-use":      "PostToolUse",
		"permission-request": "PermissionRequest",
		"stop":               "Stop",
	}

	for filename, eventName := range events {
		path := filepath.Join(hooksDir, filename)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read codex hook %q: %v", filename, err)
			continue
		}
		content := string(data)

		if !strings.HasPrefix(content, "#!/bin/sh") {
			t.Errorf("codex hook %q missing shebang", filename)
		}
		if filename == "permission-request" {
			if !strings.Contains(content, "approve-request") {
				t.Errorf("codex hook %q does not contain approve-request; content = %q", filename, content)
			}
		} else {
			if !strings.Contains(content, "--event "+eventName) {
				t.Errorf("codex hook %q does not contain --event %s; content = %q", filename, eventName, content)
			}
			if !strings.Contains(content, "report-status") {
				t.Errorf("codex hook %q does not contain report-status; content = %q", filename, content)
			}
		}
	}
}

func TestHookDir(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	dir := sm.hookDir("sess123")
	expected := filepath.Join(sm.paths.DataDir, "hooks", "sess123")
	if dir != expected {
		t.Errorf("hookDir() = %q, want %q", dir, expected)
	}
}
