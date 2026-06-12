package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/d0ugal/graith/internal/config"
)

// shellQuote wraps s in single quotes for use in shell scripts,
// escaping any embedded single quotes with the '\” idiom.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

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
// All supported hooks are generated including PreToolUse (approve-request) and
// SessionStart (check-inbox). Only called when agent hooks are enabled.
func (sm *SessionManager) generateClaudeSettings(sessionID string) (string, error) {
	dir := sm.hookDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create hook dir: %w", err)
	}
	settingsPath := filepath.Join(dir, "settings.json")

	quoted := shellQuote(resolveGrBin())

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
		var handlers []hookHandler

		switch event {
		case "PreToolUse":
			handlers = []hookHandler{
				{Type: "command", Command: fmt.Sprintf("%s approve-request", quoted)},
			}
		case "SessionStart":
			handlers = []hookHandler{
				{Type: "command", Command: fmt.Sprintf("%s report-status --event %s", quoted, event)},
				{Type: "command", Command: fmt.Sprintf("%s check-inbox", quoted)},
			}
		default:
			handlers = []hookHandler{
				{Type: "command", Command: fmt.Sprintf("%s report-status --event %s", quoted, event)},
			}
		}

		settings.Hooks[event] = []matcherGroup{
			{
				Matcher: "",
				Hooks:   handlers,
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

// generateMCPConfig writes an MCP config JSON file (compatible with Claude
// Code's --mcp-config flag) that maps each server to its gr mcp-proxy command.
// Returns the path to the config file.
func (sm *SessionManager) generateMCPConfig(sessionID string, mcpServers []config.MCPServerConfig) (string, error) {
	dir := sm.hookDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create hook dir: %w", err)
	}
	mcpConfigPath := filepath.Join(dir, "mcp.json")

	grBin := resolveGrBin()
	type mcpEntry struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	type mcpConfigFile struct {
		MCPServers map[string]mcpEntry `json:"mcpServers"`
	}

	cfg := mcpConfigFile{
		MCPServers: make(map[string]mcpEntry, len(mcpServers)),
	}
	for _, s := range mcpServers {
		cfg.MCPServers[s.Name] = mcpEntry{
			Command: grBin,
			Args:    []string{"mcp-proxy", s.Name},
		}
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal mcp config: %w", err)
	}
	if err := os.WriteFile(mcpConfigPath, data, 0o600); err != nil {
		return "", fmt.Errorf("write mcp config: %w", err)
	}
	return mcpConfigPath, nil
}

// injectClaudeHooks generates hook files for a Claude session and returns
// the extra args and env vars to add to the agent launch.
func (sm *SessionManager) injectClaudeHooks(sessionID string, mcpServers []config.MCPServerConfig) (extraArgs []string, extraEnv map[string]string, err error) {
	settingsPath, err := sm.generateClaudeSettings(sessionID)
	if err != nil {
		return nil, nil, err
	}

	extraArgs = []string{"--settings", settingsPath}

	if len(mcpServers) > 0 {
		mcpConfigPath, err := sm.generateMCPConfig(sessionID, mcpServers)
		if err != nil {
			return nil, nil, err
		}
		extraArgs = append(extraArgs, "--mcp-config", mcpConfigPath)
		sm.log.Info("injected claude hooks", "session_id", sessionID, "settings", settingsPath, "mcp_config", mcpConfigPath, "mcp_servers", len(mcpServers))
	} else {
		sm.log.Info("injected claude hooks", "session_id", sessionID, "settings", settingsPath, "mcp_servers", 0)
	}

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

	quoted := shellQuote(grBin)
	for filename, eventName := range events {
		var script string
		switch filename {
		case "permission-request":
			script = fmt.Sprintf("#!/bin/sh\nexec %s approve-request\n", quoted)
		case "session-start":
			script = fmt.Sprintf("#!/bin/sh\n%s report-status --event %s\nexec %s check-inbox\n", quoted, eventName, quoted)
		default:
			script = fmt.Sprintf("#!/bin/sh\nexec %s report-status --event %s\n", quoted, eventName)
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

// graithMCPServer returns the auto-injected graith MCP server config.
// The graith server is unsandboxed because it needs to connect to the
// daemon socket via client.Connect().
func graithMCPServer() config.MCPServerConfig {
	noSandbox := false
	return config.MCPServerConfig{
		Name:    "graith",
		Command: resolveGrBin(),
		Args:    []string{"mcp"},
		Sandbox: &noSandbox,
	}
}

// resolveMCPServers returns the merged MCP server list for the given agent,
// including auto-injected graith MCP server.
func (sm *SessionManager) resolveMCPServers(agentName string) []config.MCPServerConfig {
	global := append([]config.MCPServerConfig{graithMCPServer()}, sm.cfg.MCPServers...)
	var overrides map[string]config.MCPServerConfig
	if agent, ok := sm.cfg.Agents[agentName]; ok {
		overrides = agent.MCPServers
	}
	return config.MergeMCPServers(global, overrides)
}

// injectHooks dispatches hook injection to the agent-specific implementation.
// Returns an error if the agent type does not support hooks.
func (sm *SessionManager) injectHooks(agentName, sessionID string) (extraArgs []string, extraEnv map[string]string, err error) {
	switch agentName {
	case "claude":
		return sm.injectClaudeHooks(sessionID, sm.resolveMCPServers(agentName))
	case "codex":
		return sm.injectCodexHooks(sessionID)
	default:
		return nil, nil, fmt.Errorf("agent %q does not support hooks", agentName)
	}
}

// cleanupHooks removes generated hook files for a session.
func (sm *SessionManager) cleanupHooks(sessionID string) {
	dir := sm.hookDir(sessionID)
	if err := os.RemoveAll(dir); err != nil {
		sm.log.Warn("failed to cleanup hooks", "session_id", sessionID, "err", err)
	}
}
