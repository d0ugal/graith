package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
//
// It derives the lookup name from the basename of os.Args[0] rather than a
// hardcoded "gr", so a dev build launched as "gr-dev" wires hooks to "gr-dev"
// instead of an unrelated production "gr" that happens to be on PATH. Note that
// os.Args[0] is not always the literal command the user typed: in the autostart
// and upgrade paths the daemon is exec'd with argv[0] set to the parent's
// resolved os.Executable() path, so we take its basename. That basename is
// still the right signal for hooks, which are shell scripts run in the user's
// environment and so want the command name that environment exposes. (This
// deliberately differs from resolveExecutable, which upgrades the real binary
// and so derives its name from os.Executable.)
//
// The PATH entry is preferred over os.Executable() because the former is
// typically a stable symlink, whereas os.Executable() resolves through to the
// upgrade-versioned install path (e.g. a Homebrew Cellar dir) that breaks the
// next time the binary is upgraded.
func resolveGrBin() string {
	name := "gr"

	if len(os.Args) > 0 {
		if base := filepath.Base(os.Args[0]); base != "" && base != "." && base != string(filepath.Separator) {
			name = base
		}
	}

	if p, err := exec.LookPath(name); err == nil {
		return p
	}

	if p, err := os.Executable(); err == nil {
		return p
	}

	return name
}

// grBinReadDir returns the directory to grant sandbox read access for the
// resolved gr binary, and whether such a grant applies. It applies only when
// grBin is an absolute path. resolveGrBin's bare-name fallback (reached only if
// both the PATH lookup and os.Executable fail) has no meaningful directory:
// filepath.Dir would yield "." and grant sandbox read on the daemon's cwd, a
// fail-open over-share. Guarding on absoluteness — rather than the stale
// grBin != "gr" sentinel — keeps a non-"gr" invocation name (e.g. "gr-dev")
// from slipping through.
func grBinReadDir(grBin string) (string, bool) {
	if filepath.IsAbs(grBin) {
		return filepath.Dir(grBin), true
	}

	return "", false
}

// preToolUseExemptTools is the explicit set of read-only Claude tools excluded
// from the PreToolUse approval hook. Keep it small and known-safe.
var preToolUseExemptTools = []string{"Read", "Glob", "Grep", "LS", "NotebookRead"}

// preToolUseMatcher builds the Claude hook matcher for the PreToolUse group.
//
// It is an anchored negative lookahead that fires the hook for every tool
// EXCEPT preToolUseExemptTools. This is deliberately an EXCLUSION, not an
// allowlist, so it fails CLOSED: any tool not named here — a new or renamed
// Claude tool, every mutating tool (Bash/Write/Edit/MultiEdit/NotebookEdit/
// WebFetch/WebSearch/Task), and every MCP tool (mcp__*) — still routes to the
// daemon's approve-request round-trip, keeping the approval backend and the
// yolo dangerous-command blocklist in force for anything unrecognised.
// TodoWrite is NOT exempt: it mutates state.
//
// The leading ^ anchor and trailing . are both load-bearing. Claude evaluates
// the matcher as a JS regex with search-anywhere (.test) semantics, not a
// full-string anchored match. The ^ pins the negative lookahead to position 0,
// and the trailing . forces the match to consume the first character there — so
// the only strings that fail to match are exactly the exempt names (the
// lookahead rejects them). Without the anchor the zero-width lookahead would
// succeed at a later offset and fire (fail-open) even for an exempt tool.
//
// SAFETY: this scoping is correct only while approval policy is tool-name
// based. If a backend ever grows path-aware rules (e.g. "deny Read of
// ~/.ssh/**"), excluding Read here would silently bypass them — revisit the
// exclusion set if approval policy becomes path-aware.
//
// No *Claude* hook sends a PreToolUse report-status (Claude's PreToolUse only
// runs approve-request), so scoping loses no Claude "active" signal —
// PostToolUse drives the per-tool active heartbeat. (Codex DOES send a
// pre-tool-use report-status, and HandleHookReport is agent-agnostic, so that
// mapping stays live for Codex.) Don't "restore" a match-all matcher thinking
// status needs it.
func preToolUseMatcher() string {
	return "^(?!(" + strings.Join(preToolUseExemptTools, "|") + ")$)."
}

// generateClaudeSettings writes a Claude Code settings JSON file that registers
// hooks for all relevant lifecycle events. Returns the path to the settings file.
// All supported hooks are generated including PreToolUse (approve-request) and
// SessionStart (check-inbox). Only called when agent hooks are enabled.
func (sm *SessionManager) generateClaudeSettings(sessionID string, yolo bool) (string, error) {
	dir := sm.hookDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create hook dir: %w", err)
	}

	settingsPath := filepath.Join(dir, "settings.json")

	quoted := shellQuote(resolveGrBin())

	// A yolo session always installs the PreToolUse approval hook so its tool
	// calls route through the daemon's auto-approve backend (and any future
	// dangerous-command blocklist), even when global approval gating is off.
	hookEnabled := sm.cfg.Approvals.HookEnabled() || yolo

	events := []string{
		"SessionStart",
		"SessionEnd",
		"UserPromptSubmit",
		"PreToolUse",
		"PostToolUse",
		"Notification",
		"Stop",
		// Context-pressure signals (issue #1073, tier 1): Claude fires these
		// around a compaction with a `trigger` (manual|auto).
		"PreCompact",
		"PostCompact",
		// Sub-agent lifecycle (issue #1073, tier 2): Claude spawns its own
		// sub-agents and reports agent_id/agent_type.
		"SubagentStart",
		"SubagentStop",
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

		// Default to a match-all (empty) matcher; PreToolUse narrows it to
		// exclude the known read-only tools (fail-closed — see preToolUseMatcher).
		matcher := ""

		switch event {
		case "PreToolUse":
			if !hookEnabled {
				continue
			}

			matcher = preToolUseMatcher()
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
				Matcher: matcher,
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

// injectClaudeHooks generates the Claude Code settings (lifecycle hook) file for
// a session and returns the extra args to add to the agent launch.
//
// MCP `--mcp-config` generation is deliberately NOT bundled here — it lives in
// injectMCPConfig so MCP availability can be decided independently of whether
// generated hooks are installed. A headless session skips generated hooks (the
// typed stream is its status/approval feed) but still needs its MCP servers, so
// the two concerns must not ride the same branch. See issue #1135.
func (sm *SessionManager) injectClaudeHooks(sessionID string, yolo bool) (extraArgs []string, extraEnv map[string]string, err error) {
	settingsPath, err := sm.generateClaudeSettings(sessionID, yolo)
	if err != nil {
		return nil, nil, err
	}

	sm.log.Info("injected claude hooks", "session_id", sessionID, "settings", settingsPath)

	return []string{"--settings", settingsPath}, nil, nil
}

// injectMCPConfig generates the MCP config file for a session and returns the
// `--mcp-config` args to add to the agent launch. It is independent of
// lifecycle-hook injection (injectHooks): MCP availability must not ride on
// whether generated hooks are installed, so a headless session — which skips
// hook generation — can still be given its MCP servers.
//
// Only Claude consumes `--mcp-config`; other agents get no args (any MCP wiring
// they have is handled elsewhere), matching the pre-decoupling behaviour where
// generateMCPConfig was only ever reached via the Claude hook path.
func (sm *SessionManager) injectMCPConfig(agentName, sessionID string, mcpServers []config.MCPServerConfig) (extraArgs []string, err error) {
	if len(mcpServers) == 0 || agentName != "claude" {
		return nil, nil
	}

	mcpConfigPath, err := sm.generateMCPConfig(sessionID, mcpServers)
	if err != nil {
		return nil, err
	}

	sm.log.Info("injected mcp config", "session_id", sessionID, "mcp_config", mcpConfigPath, "mcp_servers", len(mcpServers))

	return []string{"--mcp-config", mcpConfigPath}, nil
}

// injectCodexHooks generates per-event hook scripts for a Codex session and
// returns extra env vars (including CODEX_HOOKS_DIR) to add to the agent launch.
func (sm *SessionManager) injectCodexHooks(sessionID string, yolo bool) (extraArgs []string, extraEnv map[string]string, err error) {
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

	hookEnabled := sm.cfg.Approvals.HookEnabled() || yolo

	hooksDir := filepath.Join(dir, "codex-hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create codex hooks dir: %w", err)
	}

	quoted := shellQuote(grBin)

	for filename, eventName := range events {
		if filename == "permission-request" && !hookEnabled {
			continue
		}

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
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // G306: script/binary must be executable
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
	sandboxed := false

	return config.MCPServerConfig{
		Name:    "graith",
		Command: resolveGrBin(),
		Args:    []string{"mcp"},
		Sandbox: &sandboxed,
	}
}

func (sm *SessionManager) resolveMCPServers(agentName string) []config.MCPServerConfig {
	return sm.resolveMCPServersFromConfig(sm.cfg, agentName)
}

func (sm *SessionManager) resolveMCPServersFromConfig(cfg *config.Config, agentName string) []config.MCPServerConfig {
	global := append([]config.MCPServerConfig{graithMCPServer()}, cfg.MCPServers...)

	var overrides map[string]config.MCPServerConfig
	if agent, ok := cfg.Agents[agentName]; ok {
		overrides = agent.MCPServers
	}

	return config.MergeMCPServers(global, overrides)
}

var nonAlnumRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// cursorProjectKey encodes an absolute path into the key cursor uses under
// ~/.cursor/projects/. The encoding replaces runs of non-alphanumeric
// characters with a single hyphen and trims leading/trailing hyphens.
func cursorProjectKey(absPath string) string {
	return strings.Trim(nonAlnumRe.ReplaceAllString(absPath, "-"), "-")
}

// preTrustCursorWorkspace creates the .workspace-trusted sentinel file that
// cursor checks before showing the "Workspace Trust Required" prompt. Without
// this, concurrent cursor sessions race to write ~/.cursor/cli-config.json
// during the trust flow, causing ENOENT or JSON corruption crashes.
//
// The sentinel is written with the same JSON format cursor uses
// (trustedAt + workspacePath). O_EXCL avoids overwriting files cursor
// created itself.
func preTrustCursorWorkspace(worktreePath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	key := cursorProjectKey(worktreePath)

	dir := filepath.Join(home, ".cursor", "projects", key)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create cursor project dir: %w", err)
	}

	sentinel := filepath.Join(dir, ".workspace-trusted")

	f, err := os.OpenFile(sentinel, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}

		return fmt.Errorf("create workspace-trusted: %w", err)
	}
	defer func() { _ = f.Close() }()

	trust := struct {
		TrustedAt     string `json:"trustedAt"`
		WorkspacePath string `json:"workspacePath"`
	}{
		TrustedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		WorkspacePath: worktreePath,
	}

	data, err := json.Marshal(trust)
	if err != nil {
		return fmt.Errorf("marshal workspace-trusted: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write workspace-trusted: %w", err)
	}

	return nil
}

// injectCursorHooks generates a .cursor/hooks.json in the worktree for a
// Cursor session. Returns no extra args or env — cursor reads hooks from the
// project directory automatically.
func (sm *SessionManager) injectCursorHooks(sessionID, worktreePath string, yolo bool) (extraArgs []string, extraEnv map[string]string, err error) {
	if worktreePath == "" {
		return nil, nil, nil
	}

	if agent, ok := sm.cfg.Agents["cursor"]; !ok || agent.PreTrustWorkspaceEnabled() {
		if err := preTrustCursorWorkspace(worktreePath); err != nil {
			sm.log.Warn("failed to pre-trust cursor workspace", "session_id", sessionID, "err", err)
		}
	}

	cursorDir := filepath.Join(worktreePath, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create .cursor dir: %w", err)
	}

	hooksPath := filepath.Join(cursorDir, "hooks.json")

	quoted := shellQuote(resolveGrBin())

	type hookEntry struct {
		Command string `json:"command"`
	}

	type hooksFile struct {
		Version int                    `json:"version"`
		Hooks   map[string][]hookEntry `json:"hooks"`
	}

	hooks := hooksFile{
		Version: 1,
		Hooks: map[string][]hookEntry{
			"sessionStart": {
				{Command: fmt.Sprintf("%s report-status --event SessionStart", quoted)},
				{Command: fmt.Sprintf("%s check-inbox", quoted)},
			},
			"postToolUse": {
				{Command: fmt.Sprintf("%s report-status --event PostToolUse", quoted)},
			},
			"stop": {
				{Command: fmt.Sprintf("%s report-status --event Stop", quoted)},
			},
		},
	}

	if sm.cfg.Approvals.HookEnabled() || yolo {
		hooks.Hooks["preToolUse"] = []hookEntry{
			{Command: fmt.Sprintf("%s approve-request", quoted)},
		}
	}

	data, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal cursor hooks: %w", err)
	}

	if err := os.WriteFile(hooksPath, data, 0o600); err != nil {
		return nil, nil, fmt.Errorf("write cursor hooks: %w", err)
	}

	sm.log.Info("injected cursor hooks", "session_id", sessionID, "hooks_path", hooksPath)

	return nil, nil, nil
}

// injectHooks dispatches lifecycle-hook injection to the agent-specific
// implementation. It does NOT handle MCP config — that is injectMCPConfig's job,
// kept separate so MCP can be injected without hooks (see issue #1135). Returns
// nil for agents that don't support hooks.
func (sm *SessionManager) injectHooks(agentName, sessionID, worktreePath string, yolo bool) (extraArgs []string, extraEnv map[string]string, err error) {
	switch agentName {
	case "claude":
		return sm.injectClaudeHooks(sessionID, yolo)
	case "codex":
		return sm.injectCodexHooks(sessionID, yolo)
	case "cursor":
		return sm.injectCursorHooks(sessionID, worktreePath, yolo)
	default:
		sm.log.Info("agent does not support hooks, skipping", "agent", agentName, "session_id", sessionID)
		return nil, nil, nil
	}
}

// cleanupHooks removes generated hook files for a session.
// For cursor sessions, also removes .cursor/hooks.json and the graith rule
// from the worktree since they're not in the data dir.
func (sm *SessionManager) cleanupHooks(sessionID, agentName, worktreePath string) {
	dir := sm.hookDir(sessionID)
	if err := os.RemoveAll(dir); err != nil {
		sm.log.Warn("failed to cleanup hooks", "session_id", sessionID, "err", err)
	}

	if agentName == "cursor" && worktreePath != "" {
		hooksPath := filepath.Join(worktreePath, ".cursor", "hooks.json")
		_ = os.Remove(hooksPath)
	}

	cleanupCursorRule(worktreePath)
}
