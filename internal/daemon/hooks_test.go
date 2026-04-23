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

func TestGenerateHookScript(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "test-session-01"

	scriptPath, err := sm.generateHookScript(sessionID)
	if err != nil {
		t.Fatalf("generateHookScript() error = %v", err)
	}

	// Verify the script file exists.
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat script: %v", err)
	}

	// Verify executable permission (0755).
	perm := info.Mode().Perm()
	if perm&0o111 == 0 {
		t.Errorf("script is not executable: mode = %v", perm)
	}

	// Verify content.
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	content := string(data)

	if !strings.HasPrefix(content, "#!/bin/sh") {
		t.Error("script missing shebang")
	}
	if !strings.Contains(content, "GRAITH_BIN") {
		t.Error("script missing GRAITH_BIN reference")
	}
	if !strings.Contains(content, "report-status") {
		t.Error("script missing report-status command")
	}
	if !strings.Contains(content, "exit 0") {
		t.Error("script missing exit 0 fallback")
	}

	// Verify the file is in the correct directory.
	expectedDir := filepath.Join(sm.paths.DataDir, "hooks", sessionID)
	if filepath.Dir(scriptPath) != expectedDir {
		t.Errorf("script dir = %q, want %q", filepath.Dir(scriptPath), expectedDir)
	}
}

func TestGenerateClaudeSettings(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "test-session-02"

	// Create the hook directory first (generateHookScript would normally do this).
	hookDir := sm.hookDir(sessionID)
	if err := os.MkdirAll(hookDir, 0o700); err != nil {
		t.Fatalf("mkdir hook dir: %v", err)
	}
	hookScript := filepath.Join(hookDir, "hook.sh")

	settingsPath, err := sm.generateClaudeSettings(sessionID, hookScript)
	if err != nil {
		t.Fatalf("generateClaudeSettings() error = %v", err)
	}

	// Verify the file exists.
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	// Verify valid JSON.
	var parsed struct {
		Hooks map[string][]struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	// Verify all expected events are present.
	expectedEvents := []string{
		"SessionStart",
		"UserPromptSubmit",
		"PreToolUse",
		"PostToolUse",
		"Notification",
		"Stop",
	}

	for _, event := range expectedEvents {
		entries, ok := parsed.Hooks[event]
		if !ok {
			t.Errorf("missing hook event %q", event)
			continue
		}
		if len(entries) != 1 {
			t.Errorf("event %q has %d entries, want 1", event, len(entries))
			continue
		}
		if entries[0].Type != "command" {
			t.Errorf("event %q type = %q, want %q", event, entries[0].Type, "command")
		}
		if !strings.Contains(entries[0].Command, hookScript) {
			t.Errorf("event %q command = %q, does not contain hook script path %q", event, entries[0].Command, hookScript)
		}
		if !strings.Contains(entries[0].Command, "--event "+event) {
			t.Errorf("event %q command = %q, does not contain --event %s", event, entries[0].Command, event)
		}
	}

	// Verify no unexpected events.
	if len(parsed.Hooks) != len(expectedEvents) {
		t.Errorf("hooks has %d events, want %d", len(parsed.Hooks), len(expectedEvents))
	}
}

func TestInjectClaudeHooks(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "test-session-03"

	extraArgs, extraEnv, err := sm.injectClaudeHooks(sessionID)
	if err != nil {
		t.Fatalf("injectClaudeHooks() error = %v", err)
	}

	// Verify --settings arg is returned.
	if len(extraArgs) != 2 {
		t.Fatalf("extraArgs length = %d, want 2", len(extraArgs))
	}
	if extraArgs[0] != "--settings" {
		t.Errorf("extraArgs[0] = %q, want %q", extraArgs[0], "--settings")
	}
	// The settings path should exist.
	if _, err := os.Stat(extraArgs[1]); err != nil {
		t.Errorf("settings file does not exist: %v", err)
	}

	// Verify GRAITH_BIN env is set.
	grBin, ok := extraEnv["GRAITH_BIN"]
	if !ok {
		t.Fatal("extraEnv missing GRAITH_BIN")
	}
	if grBin == "" {
		t.Error("GRAITH_BIN is empty")
	}
}

func TestCleanupHooks(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "test-session-04"

	// Generate hooks first so there's something to clean up.
	_, err := sm.generateHookScript(sessionID)
	if err != nil {
		t.Fatalf("generateHookScript() error = %v", err)
	}

	hookDir := sm.hookDir(sessionID)
	if _, err := os.Stat(hookDir); err != nil {
		t.Fatalf("hook dir does not exist before cleanup: %v", err)
	}

	// Clean up.
	sm.cleanupHooks(sessionID)

	// Verify directory is gone.
	if _, err := os.Stat(hookDir); !os.IsNotExist(err) {
		t.Errorf("hook dir still exists after cleanup: err = %v", err)
	}
}

func TestCleanupHooksNonexistent(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	// Should not panic when cleaning up a session that never had hooks.
	sm.cleanupHooks("nonexistent-session")
}

func TestResolveGrBin(t *testing.T) {
	result := resolveGrBin()
	if result == "" {
		t.Error("resolveGrBin() returned empty string")
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
