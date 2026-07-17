package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/atomicfile"
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

// cursorHooksJSON builds the exact bytes graith publishes as a session's
// .cursor/hooks.json for the given yolo / approval policy. It is pure (depends
// only on the resolved gr binary and the two flags) so two --allow-concurrent
// sessions sharing a worktree with the same settings produce byte-identical
// output, which is what makes their ownership safely shareable (issue #1328).
func cursorHooksJSON(yolo, approvalHooksEnabled bool) ([]byte, error) {
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
		return nil, fmt.Errorf("marshal cursor hooks: %w", err)
	}

	return data, nil
}

// injectCursorHooks generates a .cursor/hooks.json in the worktree for a
// Cursor session. Returns no extra args or env — cursor reads hooks from the
// project directory automatically.
//
// Ownership is SHARED and refcounted across concurrent sessions (issue #1328):
// several --allow-concurrent sessions running in the same worktree can co-own a
// single graith-generated hooks.json as long as the artifact each would produce
// is byte-identical. A live co-owner is a session still present in state whose
// per-session ownership marker matches the on-disk artifact; the artifact is
// removed only after the last such owner exits (see removeGeneratedCursorHooks).
// Publication and cleanup make the pathname operation conditional on the exact
// file object that was verified, so a concurrent external replacement is never
// overwritten or deleted (issue #1325).
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

	data, err := cursorHooksJSON(yolo, approvalHooksEnabled)
	if err != nil {
		return nil, nil, err
	}

	newHash := sha256Hex(data)

	// Serialize ownership decisions and publication so two --allow-concurrent
	// sessions sharing this worktree cannot race each other into overwriting or
	// double-publishing the shared artifact (issues #1325, #1328).
	sm.cursorHooksMu.Lock()
	defer sm.cursorHooksMu.Unlock()

	existing, statErr := os.ReadFile(hooksPath)

	switch {
	case statErr == nil:
		existingHash := sha256Hex(existing)

		recorded, markerErr := os.ReadFile(sm.cursorHooksOwnershipPath(sessionID))
		ownedByMe := markerErr == nil &&
			strings.EqualFold(strings.TrimSpace(string(recorded)), existingHash)
		ownedByOther := sm.liveCursorHookCoOwnerExists(worktreePath, existingHash, sessionID)

		if !ownedByMe && !ownedByOther {
			// A pre-existing user file, or a user modification of a graith file. Never
			// claim/overwrite it: a later exact-hash cleanup could then delete the
			// user's data, so refuse the launch instead (issue #1236).
			if markerErr != nil {
				return nil, nil, fmt.Errorf("refusing to overwrite %s: not owned by this graith session; move it aside to use cursor_project hooks: %w", hooksPath, markerErr)
			}

			return nil, nil, fmt.Errorf("refusing to overwrite %s: it was modified since graith wrote it; move it aside to re-enable cursor_project hooks", hooksPath)
		}

		if existingHash == newHash {
			// The identical graith artifact is already on disk. Co-own it: record this
			// session's ownership marker and leave the file untouched. Nothing is
			// published, so there is no pathname race to run (issue #1328). This is
			// also the clean-resume path (our own unmodified file).
			if err := sm.recordCursorHooksOwnership(sessionID, data); err != nil {
				return nil, nil, fmt.Errorf("record cursor hooks ownership: %w", err)
			}

			sm.log.Info("adopted shared cursor hooks", "session_id", sessionID, "hooks_path", hooksPath)

			return nil, nil, nil
		}

		// The on-disk content differs from what this session would generate.
		if ownedByOther {
			// Another live session sharing this worktree published a DIFFERENT hook
			// definition. Shared ownership requires byte-identical artifacts, so fail
			// clearly before launch rather than clobbering the other session (#1328).
			return nil, nil, fmt.Errorf("refusing to launch cursor_project hooks in %s: another concurrent session already published an incompatible hooks.json (differing approval/hook settings); align the sessions' settings or don't share the worktree", worktreePath)
		}

		// Owned only by this session (a solo re-inject with changed config). Replace
		// our own file, conditional on it still being the object we just verified.
		return nil, nil, sm.publishCursorHooks(sessionID, hooksPath, data, existingHash)

	case errors.Is(statErr, os.ErrNotExist):
		// First publication: create the file only if it is still absent, so a file
		// that appears concurrently is preserved rather than overwritten (#1325).
		return nil, nil, sm.publishCursorHooks(sessionID, hooksPath, data, "")

	default:
		return nil, nil, fmt.Errorf("read existing cursor hooks: %w", statErr)
	}
}

// liveCursorHookCoOwnerExists reports whether some session OTHER than excludeID,
// still present in state and sharing worktreePath, currently owns the cursor
// hooks artifact — i.e. its recorded ownership marker matches wantHash (the hash
// of the on-disk .cursor/hooks.json). It is the refcount predicate for shared
// ownership (issue #1328): true authorizes adopting an identical artifact at
// launch and blocks removing it at cleanup while a live owner remains. A stale
// marker for a session no longer in state is never counted, so a crashed owner
// cannot keep the artifact alive forever (stale-owner recovery). The worktree is
// compared on its resolved path so an in-place session and a symlinked view of
// the same directory are recognised as sharing the artifact.
func (sm *SessionManager) liveCursorHookCoOwnerExists(worktreePath, wantHash, excludeID string) bool {
	want := config.ResolvePath(worktreePath)

	type candidate struct {
		id, worktree string
	}

	sm.mu.RLock()

	candidates := make([]candidate, 0, len(sm.state.Sessions))
	for id, s := range sm.state.Sessions {
		if id == excludeID || s == nil {
			continue
		}

		candidates = append(candidates, candidate{id: id, worktree: s.WorktreePath})
	}

	sm.mu.RUnlock()

	for _, c := range candidates {
		if config.ResolvePath(c.worktree) != want {
			continue
		}

		recorded, err := os.ReadFile(sm.cursorHooksOwnershipPath(c.id))
		if err != nil {
			continue
		}

		if strings.EqualFold(strings.TrimSpace(string(recorded)), wantHash) {
			return true
		}
	}

	return false
}

// cursorHooksOwnershipPath is the marker graith writes when it generates a
// cursor hooks.json for a session. It lives in graith's own per-session hook dir
// (not the user's worktree), so it records launch-time ownership independent of
// the session's current config and survives a daemon restart. Its contents are
// the hex SHA-256 of the generated file's bytes.
func (sm *SessionManager) cursorHooksOwnershipPath(sessionID string) string {
	return filepath.Join(sm.hookDir(sessionID), "cursor_hooks_owned")
}

// atomicWriteCursorFile publishes cursor hook files (the ownership marker and the
// generated hooks.json) crash-safely. It is a package var so tests can inject
// deterministic write failures at either path (issue #1236).
var atomicWriteCursorFile = atomicfile.Write

// recordCursorHooksOwnership writes the ownership marker: the hex SHA-256 of the
// exact generated hooks.json bytes, so cleanup can delete only an unmodified
// graith-owned file. The write is atomic and fsync-backed (atomicfile.Write), so
// a failure never truncates or corrupts an existing marker — the previous marker
// is left fully intact (issue #1236).
func (sm *SessionManager) recordCursorHooksOwnership(sessionID string, data []byte) error {
	sum := sha256.Sum256(data)
	if err := atomicWriteCursorFile(sm.cursorHooksOwnershipPath(sessionID), []byte(hex.EncodeToString(sum[:])), 0o600); err != nil {
		return fmt.Errorf("write cursor hooks ownership marker: %w", err)
	}

	return nil
}

// restoreCursorHooksOwnership rolls the ownership marker back to its prior state
// after a failed target publish: it rewrites the previous marker bytes
// (atomically) when one existed, or removes the just-written marker when none
// did. It returns any failure so the caller can surface a combined error rather
// than silently leaving a new marker over an old target (issue #1236).
func (sm *SessionManager) restoreCursorHooksOwnership(sessionID string, prev []byte, hadPrev bool) error {
	markerPath := sm.cursorHooksOwnershipPath(sessionID)

	if hadPrev {
		if err := atomicWriteCursorFile(markerPath, prev, 0o600); err != nil {
			return fmt.Errorf("restore previous marker: %w", err)
		}

		return nil
	}

	if err := os.Remove(markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove new marker: %w", err)
	}

	return nil
}

// sha256Hex returns the lowercase hex SHA-256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// cursorHooksRaceHook, when non-nil, is invoked at the exact check/use boundary
// inside the race-safe publish and cleanup paths — after graith has atomically
// taken possession of (or confirmed the absence of) the current
// .cursor/hooks.json but before it acts on the pathname. Tests set it to replace
// the file at that instant to prove a concurrent replacement is never
// overwritten or deleted (issue #1325). Nil in production.
var cursorHooksRaceHook func()

// cursorHooksSyncDir fsyncs the directory holding a freshly-placed hooks.json so
// the new directory entry is durable. It is a package var so a test can inject a
// post-placement durability failure deterministically (issue #1236). Nil-safe:
// always assigned a real implementation.
var cursorHooksSyncDir = fsyncDir

// fsyncDir fsyncs a directory so a rename/link into it is durable.
func fsyncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}

	err = d.Sync()
	_ = d.Close()

	return err
}

// errCursorHooksRaced signals that .cursor/hooks.json was concurrently created
// or replaced by another process at the pathname boundary, so graith refused to
// overwrite it. The launch fails closed and the concurrent file is preserved.
var errCursorHooksRaced = errors.New("cursor hooks.json was concurrently replaced; refusing to overwrite user content")

// errCursorHooksDurability signals that the new hooks.json was linked into place
// but a post-link directory fsync failed. The new bytes are on disk, so the new
// ownership marker is kept (it matches the target); the durability error is
// surfaced rather than swallowed (mirrors the atomicfile post-rename ambiguity).
var errCursorHooksDurability = errors.New("cursor hooks published but directory fsync failed")

// reserveCursorSidecar exclusively creates a uniquely-named empty file next to
// the hooks.json (via os.CreateTemp, which uses O_EXCL) and returns its path.
// Because the name is guaranteed unique and freshly created, a later os.Rename
// INTO this path (or an atomicfile write to it) overwrites only graith's own
// reservation — never a pre-existing user file or a stale graith sidecar left by
// a crashed run. This is what keeps the quarantine/preserve machinery from
// clobbering anything at a fixed sidecar name (issue #1325). The pattern keeps
// "hooks.json" in the name so the atomicWriteCursorFile test seam (which matches
// that substring) still fires for the staged target write.
func reserveCursorSidecar(dir, kind string) (string, error) {
	f, err := os.CreateTemp(dir, "hooks.json.graith-"+kind+"-*")
	if err != nil {
		return "", fmt.Errorf("reserve cursor %s sidecar: %w", kind, err)
	}

	name := f.Name()
	_ = f.Close()

	return name, nil
}

// publishCursorHooks records this session's ownership marker and then publishes
// data at hooksPath, making the pathname write conditional on the file object
// graith verified so a concurrent external replacement is never overwritten
// (issue #1325).
//
//   - expectHash == "": first publication. hooksPath must still be ABSENT; the
//     bytes are placed with an exclusive hard link, so a file that appeared
//     concurrently is preserved and the launch fails.
//   - expectHash != "": replacing this session's own file. hooksPath must still
//     hash to expectHash at the instant graith claims it; a mismatch (concurrent
//     replacement) is restored and the launch fails.
//
// The marker is recorded BEFORE the target and rolled back on a pre-placement
// failure, so recovery stays fail-closed exactly as the crash-safety contract in
// recordCursorHooksOwnership requires (issue #1236).
func (sm *SessionManager) publishCursorHooks(sessionID, hooksPath string, data []byte, expectHash string) error {
	markerPath := sm.cursorHooksOwnershipPath(sessionID)

	// Reserve the staging path FIRST, exclusively, so it can never collide with a
	// pre-existing user or stale graith sidecar (the later write/rename only ever
	// touches graith's own freshly-created reservation, issue #1325).
	tmpPath, err := reserveCursorSidecar(filepath.Dir(hooksPath), "stage")
	if err != nil {
		return err
	}

	prevMarker, prevErr := os.ReadFile(markerPath)
	hadPrevMarker := prevErr == nil

	if err := sm.recordCursorHooksOwnership(sessionID, data); err != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("record cursor hooks ownership: %w", err)
	}

	rollback := func(cause error) error {
		_ = os.Remove(tmpPath)

		if rbErr := sm.restoreCursorHooksOwnership(sessionID, prevMarker, hadPrevMarker); rbErr != nil {
			return fmt.Errorf("%w (ownership marker rollback also failed: %w)", cause, rbErr)
		}

		return cause
	}

	if err := atomicWriteCursorFile(tmpPath, data, 0o600); err != nil {
		return rollback(fmt.Errorf("stage cursor hooks: %w", err))
	}

	if err := sm.placeCursorHooks(hooksPath, tmpPath, expectHash); err != nil {
		if errors.Is(err, errCursorHooksDurability) {
			// The new bytes are already linked into place; keep the new marker (it
			// matches the on-disk target) and surface the durability error honestly.
			_ = os.Remove(tmpPath)

			return fmt.Errorf("publish cursor hooks (target linked but not durably synced): %w", err)
		}

		return rollback(fmt.Errorf("publish cursor hooks: %w", err))
	}

	_ = os.Remove(tmpPath)

	sm.log.Info("injected cursor hooks", "session_id", sessionID, "hooks_path", hooksPath)

	return nil
}

// placeCursorHooks atomically moves the staged file at tmpPath into hooksPath,
// conditional on the current state of hooksPath. See publishCursorHooks for the
// expectHash contract. It never overwrites or deletes content graith did not
// verify: on any concurrent replacement it preserves the other file (leaving it
// in place, or setting the claimed content aside under a uniquely-reserved
// preserved sidecar) and returns errCursorHooksRaced.
func (sm *SessionManager) placeCursorHooks(hooksPath, tmpPath, expectHash string) error {
	dir := filepath.Dir(hooksPath)

	if expectHash == "" {
		// First publication: the check (absence) and the use (create) are fused by
		// an exclusive hard link, which fails rather than overwriting if a file
		// appeared in the window. Fire the interleaving seam right before it.
		if cursorHooksRaceHook != nil {
			cursorHooksRaceHook()
		}

		if err := os.Link(tmpPath, hooksPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				return errCursorHooksRaced
			}

			return fmt.Errorf("link cursor hooks: %w", err)
		}

		if err := cursorHooksSyncDir(dir); err != nil {
			return fmt.Errorf("%w: %w", errCursorHooksDurability, err)
		}

		return nil
	}

	// Replacing our own file: atomically claim the current object by renaming it
	// aside into a uniquely-reserved sidecar (never a fixed name, so we can't
	// clobber a user/stale sidecar). This is the linearization point — from here
	// the claimed bytes are immutable, so verifying them is authoritative, and
	// hooksPath is momentarily free.
	quarantine, err := reserveCursorSidecar(dir, "claim")
	if err != nil {
		return err
	}

	if err := os.Rename(hooksPath, quarantine); err != nil {
		_ = os.Remove(quarantine)

		if errors.Is(err, os.ErrNotExist) {
			// Our file vanished under us (concurrent removal); treat as a raced
			// replacement rather than blindly recreating over whatever is there now.
			return errCursorHooksRaced
		}

		return fmt.Errorf("claim cursor hooks: %w", err)
	}

	// The classic check/use window: hooksPath is free right now. A test replaces
	// it here to prove graith never clobbers the replacement.
	if cursorHooksRaceHook != nil {
		cursorHooksRaceHook()
	}

	claimed, err := os.ReadFile(quarantine)
	if err != nil {
		_ = restoreCursorClaim(quarantine, hooksPath)

		return fmt.Errorf("verify claimed cursor hooks: %w", err)
	}

	if !strings.EqualFold(sha256Hex(claimed), expectHash) {
		// Concurrently replaced between the ownership check and the claim: restore
		// the claimed content and refuse; never delete or overwrite it (#1325).
		if rErr := restoreCursorClaim(quarantine, hooksPath); rErr != nil {
			return fmt.Errorf("%w (and restore failed: %w)", errCursorHooksRaced, rErr)
		}

		return errCursorHooksRaced
	}

	// Verified ours. hooksPath is free, so an exclusive link still guards against a
	// third writer landing in the window between the claim and here.
	if err := os.Link(tmpPath, hooksPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			// A third party recreated hooksPath after we claimed. Preserve BOTH: leave
			// their file in place and set our verified-old content aside under a unique
			// preserved name (never a fixed name, so no existing sidecar is clobbered).
			if preserved, rErr := reserveCursorSidecar(dir, "preserved"); rErr == nil {
				if rnErr := os.Rename(quarantine, preserved); rnErr == nil {
					sm.log.Warn("cursor hooks concurrently recreated during republish; preserved prior content",
						"hooks_path", hooksPath, "preserved", preserved)
				} else {
					_ = os.Remove(preserved)

					sm.log.Warn("cursor hooks concurrently recreated during republish; failed to set prior content aside",
						"hooks_path", hooksPath, "err", rnErr)
				}
			}

			return errCursorHooksRaced
		}

		_ = restoreCursorClaim(quarantine, hooksPath)

		return fmt.Errorf("link cursor hooks: %w", err)
	}

	_ = os.Remove(quarantine)

	if err := cursorHooksSyncDir(dir); err != nil {
		return fmt.Errorf("%w: %w", errCursorHooksDurability, err)
	}

	return nil
}

// restoreCursorClaim puts a claimed (renamed-aside) hooks file back at its
// original path without ever clobbering a file that appeared there meanwhile. It
// uses an exclusive hard link, so a concurrent newcomer at hooksPath is left
// intact and the claimed content is instead set aside under a uniquely-reserved
// preserved sidecar (never a fixed name, so no existing sidecar is clobbered).
func restoreCursorClaim(quarantine, hooksPath string) error {
	if err := os.Link(quarantine, hooksPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			preserved, rErr := reserveCursorSidecar(filepath.Dir(hooksPath), "preserved")
			if rErr != nil {
				return rErr
			}

			if rErr := os.Rename(quarantine, preserved); rErr != nil {
				_ = os.Remove(preserved)

				return rErr
			}

			return nil
		}

		return err
	}

	return os.Remove(quarantine)
}

// safeRemoveCursorHooks deletes hooksPath only if, at the atomic instant graith
// takes possession of it, its content still hashes to wantHash. It claims the
// file by renaming it aside, verifies the claimed bytes, and deletes the claimed
// copy — so a concurrent external replacement, which lands at the now-free
// pathname, is never deleted. A mismatch restores the claimed content (#1325).
func (sm *SessionManager) safeRemoveCursorHooks(sessionID, hooksPath, wantHash string) {
	quarantine, err := reserveCursorSidecar(filepath.Dir(hooksPath), "rm")
	if err != nil {
		sm.log.Warn("failed to reserve cursor hooks removal sidecar", "session_id", sessionID, "err", err)

		return
	}

	if err := os.Rename(hooksPath, quarantine); err != nil {
		_ = os.Remove(quarantine)

		if !errors.Is(err, os.ErrNotExist) {
			sm.log.Warn("failed to claim cursor hooks for removal", "session_id", sessionID, "err", err)
		}

		return
	}

	// Check/use boundary: a test replaces hooksPath here to prove the replacement
	// (now at the free pathname) is never deleted.
	if cursorHooksRaceHook != nil {
		cursorHooksRaceHook()
	}

	claimed, err := os.ReadFile(quarantine)
	if err != nil {
		_ = restoreCursorClaim(quarantine, hooksPath)

		sm.log.Warn("failed to verify claimed cursor hooks; restored", "session_id", sessionID, "err", err)

		return
	}

	if !strings.EqualFold(sha256Hex(claimed), wantHash) {
		if rErr := restoreCursorClaim(quarantine, hooksPath); rErr != nil {
			sm.log.Warn("cursor hooks changed under removal and restore failed", "session_id", sessionID, "err", rErr)
		} else {
			sm.log.Info("cursor hooks changed under removal; preserved", "session_id", sessionID, "path", hooksPath)
		}

		return
	}

	if err := os.Remove(quarantine); err != nil && !os.IsNotExist(err) {
		sm.log.Warn("failed to remove cursor hooks", "session_id", sessionID, "err", err)
	}
}

type cursorHookCmd struct {
	Command string `json:"command"`
}

// cursorHookCmdsEqual reports whether entries is EXACTLY want: same length and
// each command byte-for-byte equal (order included).
func cursorHookCmdsEqual(entries []cursorHookCmd, want []string) bool {
	if len(entries) != len(want) {
		return false
	}

	for i := range want {
		if entries[i].Command != want[i] {
			return false
		}
	}

	return true
}

// isGraithGeneratedCursorHooks reports whether the file at path is a cursor
// hooks.json graith itself wrote, matched against the EXACT bytes injectCursorHooks
// emits: version 1; hook events exactly {sessionStart, postToolUse, stop} plus an
// optional preToolUse; and each command equal byte-for-byte to graith's (the
// resolved gr binary plus the exact subcommand), not merely containing a
// substring. Any extra/missing key, entry, or differing command fails the match,
// so a user file that embeds graith's command inside a larger command (e.g.
// "echo x; gr report-status ...") or adds events is preserved. Used only on the
// legacy path where no launch-time hash marker exists (issue #1236); the marker
// path uses an exact SHA-256 instead.
func isGraithGeneratedCursorHooks(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}

	var f struct {
		Version int                        `json:"version"`
		Hooks   map[string][]cursorHookCmd `json:"hooks"`
	}

	if json.Unmarshal(data, &f) != nil || f.Version != 1 {
		return false
	}

	quoted := shellQuote(resolveGrBin())
	required := map[string][]string{
		"sessionStart": {quoted + " report-status --event SessionStart", quoted + " check-inbox"},
		"postToolUse":  {quoted + " report-status --event PostToolUse"},
		"stop":         {quoted + " report-status --event Stop"},
	}
	optionalPreToolUse := []string{quoted + " approve-request"}

	for k := range f.Hooks {
		if _, req := required[k]; req {
			continue
		}

		if k == "preToolUse" {
			continue
		}

		return false // an unexpected event key means this isn't graith's file.
	}

	for k, want := range required {
		if !cursorHookCmdsEqual(f.Hooks[k], want) {
			return false
		}
	}

	// preToolUse is optional (present only when approval hooks / yolo are on); if
	// present it must be exactly graith's approve-request entry.
	if pre, ok := f.Hooks["preToolUse"]; ok && !cursorHookCmdsEqual(pre, optionalPreToolUse) {
		return false
	}

	return true
}

// removeGeneratedCursorHooks releases this session's ownership of the
// .cursor/hooks.json graith generated and removes the artifact only when this
// session is the LAST live owner (issue #1328). The deletion target is always
// derived from the session's persisted worktree (a trusted value), never from a
// stored path, so a tampered marker cannot redirect the delete. It fails CLOSED:
// it deletes only when it can positively confirm graith ownership, and preserves
// the file on any ambiguity (issue #1236). The actual deletion is race-safe: the
// file is claimed and re-verified before removal so a concurrent external
// replacement is never deleted (issue #1325).
//
//   - marker present, current bytes match it, another live co-owner remains:
//     keep the artifact for the surviving owner (refcounted).
//   - marker present, current bytes match it, no other live owner: last owner —
//     remove the artifact race-safely.
//   - marker present but current bytes differ: a user modification/replacement —
//     preserve.
//   - marker genuinely absent (os.ErrNotExist, e.g. a session predating the
//     marker): legacy best-effort — remove race-safely only when the current
//     config still selects cursor_project AND the file still fingerprints as
//     graith-generated.
//   - marker unreadable for any other reason (permission/I/O): preserve; never
//     blind-delete when ownership cannot be determined.
//
// Callers must invoke this before removing the per-session hook dir that holds
// the marker.
func (sm *SessionManager) removeGeneratedCursorHooks(sessionID, agentName, worktreePath string) {
	if worktreePath == "" {
		return
	}

	sm.cursorHooksMu.Lock()
	defer sm.cursorHooksMu.Unlock()

	hooksPath := filepath.Join(worktreePath, ".cursor", "hooks.json")

	recorded, err := os.ReadFile(sm.cursorHooksOwnershipPath(sessionID))
	switch {
	case err == nil:
		current, cerr := os.ReadFile(hooksPath)
		if cerr != nil {
			return // already gone (or unreadable) — never blind-delete.
		}

		curHash := sha256Hex(current)
		if !strings.EqualFold(strings.TrimSpace(string(recorded)), curHash) {
			sm.log.Info("leaving modified cursor hooks in place",
				"session_id", sessionID, "path", hooksPath)

			return
		}

		// We own the current content. If another live session sharing this worktree
		// also owns it, keep the artifact for them (refcounted shared ownership;
		// issue #1328). Only the last live owner removes it.
		if sm.liveCursorHookCoOwnerExists(worktreePath, curHash, sessionID) {
			sm.log.Info("cursor hooks still owned by another concurrent session; leaving in place",
				"session_id", sessionID, "path", hooksPath)

			return
		}

		sm.safeRemoveCursorHooks(sessionID, hooksPath, curHash)
	case errors.Is(err, os.ErrNotExist):
		// Legacy path: no launch-time marker. Only remove a file that is both
		// selected by current config and fingerprints as graith-generated.
		if sm.Config().Agents[agentName].HookMechanism() != config.HookMechanismCursorProject {
			return
		}

		if !isGraithGeneratedCursorHooks(hooksPath) {
			sm.log.Info("leaving non-graith cursor hooks in place",
				"session_id", sessionID, "path", hooksPath)

			return
		}

		current, cerr := os.ReadFile(hooksPath)
		if cerr != nil {
			return // already gone (or unreadable) — never blind-delete.
		}

		sm.safeRemoveCursorHooks(sessionID, hooksPath, sha256Hex(current))
	default:
		// Ownership marker unreadable (permission/I/O): fail closed, preserve.
		sm.log.Warn("cursor hooks ownership marker unreadable; leaving hooks in place",
			"session_id", sessionID, "err", err)
	}
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

// cleanupHooks removes generated hook files for a session. Any cursor
// project hooks.json graith generated is removed via the artifact path recorded
// at launch, so a config reload that changed or dropped the mechanism cannot
// strand it and a user-authored hooks.json is never deleted (issue #1236). The
// recorded artifact is read before the per-session hook dir (which holds the
// record) is removed.
func (sm *SessionManager) cleanupHooks(sessionID, agentName, worktreePath string) {
	sm.removeGeneratedCursorHooks(sessionID, agentName, worktreePath)

	dir := sm.hookDir(sessionID)
	if err := os.RemoveAll(dir); err != nil {
		sm.log.Warn("failed to cleanup hooks", "session_id", sessionID, "err", err)
	}

	cleanupCursorRule(worktreePath)
}
