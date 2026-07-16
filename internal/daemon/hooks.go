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
	return sm.generateClaudeSettingsFromConfig(sm.Config(), sessionID, yolo)
}

func (sm *SessionManager) generateClaudeSettingsFromConfig(cfgSnapshot *config.Config, sessionID string, yolo bool) (string, error) {
	dir := sm.hookDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create hook dir: %w", err)
	}

	settingsPath := filepath.Join(dir, "settings.json")

	quoted := shellQuote(resolveGrBin())

	// A yolo session always installs the PreToolUse approval hook so its tool
	// calls route through the daemon's auto-approve backend (and any future
	// dangerous-command blocklist), even when global approval gating is off.
	hookEnabled := cfgSnapshot.Approvals.HookEnabled() || yolo

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
				{Type: "command", Command: quoted + " approve-request"},
			}
		case "SessionStart":
			handlers = []hookHandler{
				{Type: "command", Command: fmt.Sprintf("%s report-status --event %s", quoted, event)},
				{Type: "command", Command: quoted + " check-inbox"},
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
func (sm *SessionManager) injectClaudeHooks(sessionID string, yolo bool, agent config.Agent) (extraArgs []string, extraEnv map[string]string, err error) {
	return sm.injectClaudeHooksFromConfig(sm.Config(), sessionID, yolo, agent)
}

func (sm *SessionManager) injectClaudeHooksFromConfig(cfgSnapshot *config.Config, sessionID string, yolo bool, agent config.Agent) (extraArgs []string, extraEnv map[string]string, err error) {
	settingsPath, err := sm.generateClaudeSettingsFromConfig(cfgSnapshot, sessionID, yolo)
	if err != nil {
		return nil, nil, err
	}

	// The generated settings file is dynamic; only the flag spelling comes from
	// config (agents.<name>.hooks.settings_args, {path} bound). (issue #1236)
	args, err := config.ExpandSliceWith(agent.HookSettingsArgsOrDefault(), map[string]string{"path": settingsPath})
	if err != nil {
		return nil, nil, fmt.Errorf("expand hook settings args: %w", err)
	}

	sm.log.Info("injected claude hooks", "session_id", sessionID, "settings", settingsPath)

	return args, nil, nil
}

// injectMCPConfig wires a session's daemon-managed MCP servers into the agent
// launch and returns the extra args. It is independent of lifecycle-hook
// injection (injectHooks): MCP availability must not ride on whether generated
// hooks are installed, so a headless session — which skips hook generation — can
// still be given its MCP servers (issue #1135).
//
// Claude consumes a `--mcp-config` file; Codex takes per-session
// `-c mcp_servers.<name>.…` overrides (issue #1184). Other agents get no args.
func (sm *SessionManager) injectMCPConfig(agentName, sessionID string, mcpServers []config.MCPServerConfig) (extraArgs []string, err error) {
	return sm.injectMCPConfigFromConfig(sm.Config(), agentName, sessionID, mcpServers)
}

func (sm *SessionManager) injectMCPConfigFromConfig(cfgSnapshot *config.Config, agentName, sessionID string, mcpServers []config.MCPServerConfig) (extraArgs []string, err error) {
	if len(mcpServers) == 0 {
		return nil, nil
	}

	agent := cfgSnapshot.Agents[agentName]

	switch agent.MCPMechanism() {
	case config.MCPMechanismClaudeConfig:
		mcpConfigPath, err := sm.generateMCPConfig(sessionID, mcpServers)
		if err != nil {
			return nil, err
		}

		// The config file is generated dynamically; only the flag spelling comes
		// from config (agents.<name>.mcp.config_args, {path} bound). (issue #1236)
		args, err := config.ExpandSliceWith(agent.MCPConfigArgsOrDefault(), map[string]string{"path": mcpConfigPath})
		if err != nil {
			return nil, fmt.Errorf("expand mcp config args: %w", err)
		}

		sm.log.Info("injected mcp config", "session_id", sessionID, "mcp_config", mcpConfigPath, "mcp_servers", len(mcpServers))

		return args, nil
	case config.MCPMechanismCodexConfig:
		args, skipped, err := codexMCPServerArgs(mcpServers, agent.MCPServerArgsOrDefault())
		if err != nil {
			return nil, err
		}

		if len(skipped) > 0 {
			sm.log.Warn("skipped codex mcp servers with names not representable as codex config keys",
				"session_id", sessionID, "servers", skipped)
		}

		sm.log.Info("injected mcp config", "session_id", sessionID, "mcp_servers", len(mcpServers)-len(skipped))

		return args, nil
	default:
		return nil, nil
	}
}

// codexBareKeyRe matches MCP server names that can be represented as a TOML
// bare key inside a Codex `-c mcp_servers.<name>.…` override path. Codex's
// `-c key=value` override parser splits the dotted key path on `.` and does
// NOT honour quoting or backslash-escaping of a segment (verified against
// Codex CLI 0.144.5: `mcp_servers."foo.bar".command`, `…'foo.bar'…`, and
// `…foo\.bar…` all still split at the dot). So a name containing a `.` would
// nest under the wrong table and, worse, fail Codex config loading outright —
// preventing the whole session from starting. Any name outside this charset is
// skipped rather than emitted, so one ill-named server can't break the launch.
// The auto-injected `graith` server and typical names are always representable;
// this only affects a user who names a server with a dot or other special
// character. (The Claude path handles any name because it uses the name as a
// JSON map key, so this restriction is Codex-specific.)
var codexBareKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// codexMCPServerArgs builds the per-session `-c` config overrides that point
// each daemon-managed MCP server at `gr mcp-proxy <name>` for a Codex session.
// It returns the args plus the names of any servers skipped because their name
// can't be represented as a Codex override key (see codexBareKeyRe).
//
// It deliberately overrides only `command` and `args` (mirroring the Claude
// --mcp-config which sets the same two fields). Using `-c` overrides rather
// than writing a full config file leaves any user-supplied per-server Codex
// controls — `startup_timeout_sec`, `tool_timeout_sec`, `enabled`,
// enabled/disabled tools, per-tool approval mode — intact and merged, rather
// than flattening every server to graith's command/args/env shape.
//
// Values are JSON-encoded, which is also valid TOML for a string
// (`"…"`) and a string array (`["…","…"]`), the two value kinds Codex's
// `-c key=value` override parser accepts here.
func codexMCPServerArgs(mcpServers []config.MCPServerConfig, serverTmpl []string) (args, skipped []string, err error) {
	if len(mcpServers) == 0 {
		return nil, nil, nil
	}

	grBin := resolveGrBin()

	cmdVal, err := json.Marshal(grBin)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal mcp command: %w", err)
	}

	args = make([]string, 0, len(mcpServers)*len(serverTmpl))

	for _, s := range mcpServers {
		// Security: a name that isn't a representable Codex config key is skipped
		// in Go, not emitted, so one ill-named server can't break the launch. This
		// stays in code regardless of the configured argv spelling. (issue #1236)
		if !codexBareKeyRe.MatchString(s.Name) {
			skipped = append(skipped, s.Name)
			continue
		}

		proxyArgs, err := json.Marshal([]string{"mcp-proxy", s.Name})
		if err != nil {
			return nil, nil, fmt.Errorf("marshal mcp args for %q: %w", s.Name, err)
		}

		// The command/args values are Go-built and JSON-encoded (valid TOML); only
		// the -c override spelling comes from config (agents.<name>.mcp.server_args).
		serverArgs, err := config.ExpandSliceWith(serverTmpl, map[string]string{
			"mcp_name":    s.Name,
			"mcp_command": string(cmdVal),
			"mcp_args":    string(proxyArgs),
		})
		if err != nil {
			return nil, nil, fmt.Errorf("expand mcp server args for %q: %w", s.Name, err)
		}

		args = append(args, serverArgs...)
	}

	return args, skipped, nil
}

// codexHookEvent describes one Codex lifecycle hook: the Codex event name and
// the shell commands graith runs for it, in order.
type codexHookEvent struct {
	// event is the Codex hook event name. It must match a key the current Codex
	// CLI recognises in its HookEventsToml schema (PascalCase, e.g. SessionStart).
	event string
	// commands are the shell command strings run for the event, in order.
	commands []string
	// approval marks the PermissionRequest hook, which bridges to the daemon's
	// approval backend and is only installed when the approval gate (or yolo) is on.
	approval bool
}

// injectCodexHooks builds the Codex session-config overrides that register
// graith's lifecycle hooks and returns them as extra launch args.
//
// Current Codex no longer reads a CODEX_HOOKS_DIR env var (issue #1183). It
// discovers hooks from configuration-layer folders (hooks.json) and inline
// [hooks] config, and accepts hooks per-invocation as trusted session config via
// repeatable `-c hooks.<Event>=<toml>` overrides. Each override's value is
// parsed by Codex as TOML (codex-rs/utils/cli config_override), so graith emits
// a TOML array of matcher groups — [{hooks=[{type="command",command="..."}]}] —
// with no matcher (match-all). `--dangerously-bypass-hook-trust` skips Codex's
// interactive hook-trust review, safe here because graith generated and vetted
// these hooks and no human is watching the TUI to approve them.
//
// MCP-server wiring is NOT handled here — it rides the separate injectMCPConfig
// path (issue #1135), so a headless codex session gets MCP without hooks.
func (sm *SessionManager) injectCodexHooks(sessionID string, yolo bool, agent config.Agent) (extraArgs []string, extraEnv map[string]string, err error) {
	return sm.injectCodexHooksFromConfig(sm.Config(), sessionID, yolo, agent)
}

func (sm *SessionManager) injectCodexHooksFromConfig(cfgSnapshot *config.Config, sessionID string, yolo bool, agent config.Agent) (extraArgs []string, extraEnv map[string]string, err error) {
	grBin := shellQuote(resolveGrBin())
	hookEnabled := cfgSnapshot.Approvals.HookEnabled() || yolo

	eventTmpl := agent.HookEventArgsOrDefault()

	events := []codexHookEvent{
		{event: "SessionStart", commands: []string{
			grBin + " report-status --event SessionStart",
			grBin + " check-inbox",
		}},
		{event: "UserPromptSubmit", commands: []string{
			grBin + " report-status --event UserPromptSubmit",
		}},
		{event: "PreToolUse", commands: []string{
			grBin + " report-status --event PreToolUse",
		}},
		{event: "PostToolUse", commands: []string{
			grBin + " report-status --event PostToolUse",
		}},
		{event: "PermissionRequest", approval: true, commands: []string{
			grBin + " approve-request",
		}},
		{event: "Stop", commands: []string{
			grBin + " report-status --event Stop",
		}},
	}

	installed := 0

	for _, e := range events {
		if e.approval && !hookEnabled {
			continue
		}

		value := codexHookOverrideValue(e.commands)

		// The hook value is Go-built inline TOML; only the -c override spelling
		// comes from config (agents.<name>.hooks.event_args). (issue #1236)
		evArgs, err := config.ExpandSliceWith(eventTmpl, map[string]string{"hook_event": e.event, "hook_value": value})
		if err != nil {
			return nil, nil, fmt.Errorf("expand hook event args: %w", err)
		}

		extraArgs = append(extraArgs, evArgs...)
		installed++
	}

	// NOTE: --dangerously-bypass-hook-trust (agents.<name>.hooks.trust_args) is
	// process-wide, not scoped to the -c overrides above: it also runs any OTHER
	// enabled-but-untrusted hook sources in the session (a repo-local
	// .codex/hooks.json, user config hooks, plugin hooks) without Codex's trust
	// review. graith relies on this to run its own generated hooks unattended; the
	// containment boundary for anything else those sources might do is the graith
	// sandbox (see [sandbox]), the same boundary that already confines the agent
	// itself. Codex has no way today to trust only the session-config hooks without
	// bypassing trust globally.
	extraArgs = append(extraArgs, agent.HookTrustArgsOrDefault()...)

	sm.log.Info("injected codex hooks", "session_id", sessionID, "events", installed)

	return extraArgs, nil, nil
}

// codexHookOverrideValue builds the inline-TOML value for a `hooks.<Event>`
// config override: a single match-all matcher group whose command handlers run
// the given shell commands in order. The shape mirrors Codex's HookEventsToml
// matcher-group schema ([{hooks=[{type="command",command="..."}]}]).
func codexHookOverrideValue(commands []string) string {
	handlers := make([]string, len(commands))
	for i, c := range commands {
		handlers[i] = fmt.Sprintf(`{type="command",command=%s}`, tomlBasicString(c))
	}

	return fmt.Sprintf(`[{hooks=[%s]}]`, strings.Join(handlers, ","))
}

// tomlBasicString encodes s as a TOML basic (double-quoted) string, escaping the
// characters TOML requires so the shell command survives being embedded in an
// inline-TOML config-override value and parsed back out by Codex.
func tomlBasicString(s string) string {
	var b strings.Builder

	b.WriteByte('"')

	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			// TOML basic strings forbid unescaped C0 controls and U+007F (DEL).
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}

	b.WriteByte('"')

	return b.String()
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
	return sm.resolveMCPServersFromConfig(sm.Config(), agentName)
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
func (sm *SessionManager) injectCursorHooks(
	sessionID, worktreePath string,
	yolo bool,
	agent config.Agent,
	approvalHooksEnabled bool,
) (extraArgs []string, extraEnv map[string]string, err error) {
	if worktreePath == "" {
		return nil, nil, nil
	}

	// The caller supplies the already-selected agent and approval policy from one
	// config snapshot. A custom cursor_project adapter must never inherit the
	// built-in cursor entry's trust policy (issues #1236, #1287).
	if agent.PreTrustWorkspaceEnabled() {
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
				{Command: quoted + " report-status --event SessionStart"},
				{Command: quoted + " check-inbox"},
			},
			"postToolUse": {
				{Command: quoted + " report-status --event PostToolUse"},
			},
			"stop": {
				{Command: quoted + " report-status --event Stop"},
			},
		},
	}

	if approvalHooksEnabled || yolo {
		hooks.Hooks["preToolUse"] = []hookEntry{
			{Command: quoted + " approve-request"},
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
	return sm.injectHooksFromConfig(sm.Config(), agentName, sessionID, worktreePath, yolo)
}

func (sm *SessionManager) injectHooksFromConfig(cfgSnapshot *config.Config, agentName, sessionID, worktreePath string, yolo bool) (extraArgs []string, extraEnv map[string]string, err error) {
	agent := cfgSnapshot.Agents[agentName]

	switch agent.HookMechanism() {
	case config.HookMechanismClaudeSettings:
		return sm.injectClaudeHooksFromConfig(cfgSnapshot, sessionID, yolo, agent)
	case config.HookMechanismCodexConfig:
		return sm.injectCodexHooksFromConfig(cfgSnapshot, sessionID, yolo, agent)
	case config.HookMechanismCursorProject:
		return sm.injectCursorHooks(sessionID, worktreePath, yolo, agent, cfgSnapshot.Approvals.HookEnabled())
	default:
		sm.log.Info("agent does not support hooks, skipping", "agent", agentName, "session_id", sessionID)
		return nil, nil, nil
	}
}

// cleanupHooks removes generated hook files for a session.
// For any agent configured with cursor_project hooks, also removes
// .cursor/hooks.json. Dispatch by mechanism rather than the literal agent name
// so custom adapters clean up the artifact they created.
func (sm *SessionManager) cleanupHooks(sessionID, agentName, worktreePath string) {
	dir := sm.hookDir(sessionID)
	if err := os.RemoveAll(dir); err != nil {
		sm.log.Warn("failed to cleanup hooks", "session_id", sessionID, "err", err)
	}

	agent := sm.Config().Agents[agentName]
	if agent.HookMechanism() == config.HookMechanismCursorProject && worktreePath != "" {
		hooksPath := filepath.Join(worktreePath, ".cursor", "hooks.json")
		_ = os.Remove(hooksPath)
	}

	cleanupCursorRule(worktreePath)
}
