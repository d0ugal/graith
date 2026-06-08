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

// generateHookScript writes a shell script that forwards hook events to gr report-status.
// It returns the absolute path to the script.
func (sm *SessionManager) generateHookScript(sessionID string) (string, error) {
	dir := sm.hookDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create hook dir: %w", err)
	}

	scriptPath := filepath.Join(dir, "hook.sh")
	script := `#!/bin/sh
if [ -n "${GRAITH_BIN:-}" ] && [ -x "$GRAITH_BIN" ]; then
  exec "$GRAITH_BIN" report-status "$@" >/dev/null 2>&1
fi
if command -v gr >/dev/null 2>&1; then
  exec gr report-status "$@" >/dev/null 2>&1
fi
exit 0
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		return "", fmt.Errorf("write hook script: %w", err)
	}
	return scriptPath, nil
}

// generateClaudeSettings writes a Claude Code settings JSON file that registers
// hooks for all relevant lifecycle events. Returns the path to the settings file.
func (sm *SessionManager) generateClaudeSettings(sessionID, hookScript string) (string, error) {
	dir := sm.hookDir(sessionID)
	settingsPath := filepath.Join(dir, "settings.json")

	// Each event gets its own hook entry, passing --event <name> to the hook script.
	// The hook script forwards this to gr report-status.
	events := []string{
		"SessionStart",
		"UserPromptSubmit",
		"PreToolUse",
		"PostToolUse",
		"Notification",
		"Stop",
	}

	type hookEntry struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	type settingsFile struct {
		Hooks map[string][]hookEntry `json:"hooks"`
	}

	settings := settingsFile{
		Hooks: make(map[string][]hookEntry),
	}
	for _, event := range events {
		settings.Hooks[event] = []hookEntry{
			{Type: "command", Command: fmt.Sprintf("%s --event %s", hookScript, event)},
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
	hookScript, err := sm.generateHookScript(sessionID)
	if err != nil {
		return nil, nil, err
	}

	settingsPath, err := sm.generateClaudeSettings(sessionID, hookScript)
	if err != nil {
		return nil, nil, err
	}

	extraArgs = []string{"--settings", settingsPath}
	extraEnv = map[string]string{
		"GRAITH_BIN": resolveGrBin(),
	}

	sm.log.Info("injected claude hooks", "session_id", sessionID, "settings", settingsPath)
	return extraArgs, extraEnv, nil
}

// injectCodexHooks generates per-event hook scripts for a Codex session and
// returns extra env vars (including CODEX_HOOKS_DIR) to add to the agent launch.
func (sm *SessionManager) injectCodexHooks(sessionID string) (extraArgs []string, extraEnv map[string]string, err error) {
	hookScript, err := sm.generateHookScript(sessionID)
	if err != nil {
		return nil, nil, err
	}

	// Generate per-event wrapper scripts in a codex-hooks subdirectory.
	dir := sm.hookDir(sessionID)
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
		script := fmt.Sprintf("#!/bin/sh\nexec %s --event %s\n", hookScript, eventName)
		path := filepath.Join(hooksDir, filename)
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			return nil, nil, fmt.Errorf("write codex hook %s: %w", filename, err)
		}
	}

	extraEnv = map[string]string{
		"GRAITH_BIN":      resolveGrBin(),
		"CODEX_HOOKS_DIR": hooksDir,
	}

	sm.log.Info("injected codex hooks", "session_id", sessionID, "hooks_dir", hooksDir)
	return nil, extraEnv, nil // no extra args needed, just env
}

// cleanupHooks removes generated hook files for a session.
func (sm *SessionManager) cleanupHooks(sessionID string) {
	dir := sm.hookDir(sessionID)
	if err := os.RemoveAll(dir); err != nil {
		sm.log.Warn("failed to cleanup hooks", "session_id", sessionID, "err", err)
	}
}
