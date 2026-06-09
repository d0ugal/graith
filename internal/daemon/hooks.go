package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// hookDir returns the directory for hook files for a session.
func (sm *SessionManager) hookDir(sessionID string) string {
	return filepath.Join(sm.paths.DataDir, "hooks", sessionID)
}

// resolveGrBin finds the gr binary path for hook scripts.
func resolveGrBin() string {
	if p, err := exec.LookPath("gr"); err == nil {
		return p
	}
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "gr"
}

// generateClaudeSettings writes a Claude Code settings JSON file that registers
// hooks for all relevant lifecycle events. Returns the path to the settings file.
func (sm *SessionManager) generateClaudeSettings(sessionID string) (string, error) {
	dir := sm.hookDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create hook dir: %w", err)
	}
	settingsPath := filepath.Join(dir, "settings.json")

	grBin := resolveGrBin()

	events := []string{
		"SessionStart",
		"UserPromptSubmit",
		"PreToolUse",
		"PostToolUse",
		"Notification",
		"Stop",
	}

	type hookHandler struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	type matcherGroup struct {
		Matcher string        `json:"matcher"`
		Hooks   []hookHandler `json:"hooks"`
	}
	type settingsFile struct {
		Hooks map[string][]matcherGroup `json:"hooks"`
	}

	settings := settingsFile{
		Hooks: make(map[string][]matcherGroup),
	}
	for _, event := range events {
		command := fmt.Sprintf("%s report-status --event %s", grBin, event)
		if event == "PreToolUse" {
			command = fmt.Sprintf("%s approve-request", grBin)
		}
		settings.Hooks[event] = []matcherGroup{
			{
				Matcher: "",
				Hooks: []hookHandler{
					{Type: "command", Command: command},
				},
			},
		}
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		return "", fmt.Errorf("write settings: %w", err)
	}
	return settingsPath, nil
}

// injectClaudeHooks generates hook files for a Claude session and returns
// the extra args and env vars to add to the agent launch.
func (sm *SessionManager) injectClaudeHooks(sessionID string) (extraArgs []string, extraEnv map[string]string, err error) {
	settingsPath, err := sm.generateClaudeSettings(sessionID)
	if err != nil {
		return nil, nil, err
	}

	extraArgs = []string{"--settings", settingsPath}
	sm.log.Info("injected claude hooks", "session_id", sessionID, "settings", settingsPath)
	return extraArgs, nil, nil
}

// injectCodexHooks generates per-event hook scripts for a Codex session and
// returns extra env vars (including CODEX_HOOKS_DIR) to add to the agent launch.
func (sm *SessionManager) injectCodexHooks(sessionID string) (extraArgs []string, extraEnv map[string]string, err error) {
	dir := sm.hookDir(sessionID)
	grBin := resolveGrBin()

	events := map[string]string{
		"session-start":      "SessionStart",
		"user-prompt-submit": "UserPromptSubmit",
		"pre-tool-use":       "PreToolUse",
		"post-tool-use":      "PostToolUse",
		"permission-request": "PermissionRequest",
		"stop":               "Stop",
	}

	hooksDir := filepath.Join(dir, "codex-hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create codex hooks dir: %w", err)
	}

	for filename, eventName := range events {
		var script string
		if filename == "permission-request" {
			script = fmt.Sprintf("#!/bin/sh\nexec '%s' approve-request\n", grBin)
		} else {
			script = fmt.Sprintf("#!/bin/sh\nexec '%s' report-status --event %s\n", grBin, eventName)
		}
		path := filepath.Join(hooksDir, filename)
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			return nil, nil, fmt.Errorf("write codex hook %s: %w", filename, err)
		}
	}

	extraEnv = map[string]string{
		"CODEX_HOOKS_DIR": hooksDir,
	}

	sm.log.Info("injected codex hooks", "session_id", sessionID, "hooks_dir", hooksDir)
	return nil, extraEnv, nil
}

// cleanupHooks removes generated hook files for a session.
func (sm *SessionManager) cleanupHooks(sessionID string) {
	dir := sm.hookDir(sessionID)
	if err := os.RemoveAll(dir); err != nil {
		sm.log.Warn("failed to cleanup hooks", "session_id", sessionID, "err", err)
	}
}
