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
	"slices"
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

// commandPolicyHookCommand turns failure to start or run the bounded policy
// supervisor into the exit-2 blocking contract shared by Claude and Codex
// PreToolUse hooks. The supervisor itself emits a valid native deny for worker
// crashes, malformed output, signals, and its shorter internal timeout.
func commandPolicyHookCommand(quotedGr string) string {
	return quotedGr + " command-policy-check || { printf '%s\\n' " +
		"'graith command policy hook failed before returning a decision' >&2; exit 2; }"
}

// The agent hook runner must outlive the policy supervisor's own deadline. The
// config caps policy evaluation at 60 seconds; ten seconds covers the worker's
// transport slack and process startup/teardown without leaving a human-facing
// prompt or an unbounded hook.
func commandPolicyHookTimeout(timeout time.Duration) int {
	const runnerSlack = 10 * time.Second
	return int((timeout + runnerSlack + time.Second - 1) / time.Second)
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

// generateClaudeSettings writes a Claude Code settings JSON file that registers
// lifecycle hooks and, independently, the optional Bash command-policy hook.
func (sm *SessionManager) generateClaudeSettings(sessionID string, lifecycle, policy bool) (string, error) {
	return sm.generateClaudeSettingsWithTimeout(sessionID, lifecycle, policy, sm.Config().CommandPolicy.TimeoutDuration())
}

func (sm *SessionManager) generateClaudeSettingsWithTimeout(sessionID string, lifecycle, policy bool, policyTimeout time.Duration) (string, error) {
	dir := sm.hookDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create hook dir: %w", err)
	}

	settingsPath := filepath.Join(dir, "settings.json")

	quoted := shellQuote(resolveGrBin())

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
		Timeout int    `json:"timeout,omitempty"`
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

		// Default to a match-all (empty) matcher. Command policy is scoped to the
		// verified shell transport by an exact Bash matcher.
		matcher := ""

		switch event {
		case "PreToolUse":
			if !policy {
				continue
			}

			matcher = "^Bash$"
			handlers = []hookHandler{
				{
					Type: "command", Command: commandPolicyHookCommand(quoted),
					Timeout: commandPolicyHookTimeout(policyTimeout),
				},
			}
		case "SessionStart":
			if !lifecycle {
				continue
			}

			handlers = []hookHandler{
				{Type: "command", Command: fmt.Sprintf("%s report-status --event %s", quoted, event)},
				{Type: "command", Command: quoted + " check-inbox"},
			}
		default:
			if !lifecycle {
				continue
			}

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
// typed stream is its status/lifecycle feed) but still needs its MCP servers, so
// the two concerns must not ride the same branch. See issue #1135.
func (sm *SessionManager) injectClaudeHooks(sessionID string, lifecycle, policy bool) (extraArgs []string, extraEnv map[string]string, err error) {
	return sm.injectClaudeHooksWithTimeout(sessionID, lifecycle, policy, sm.Config().CommandPolicy.TimeoutDuration())
}

func (sm *SessionManager) injectClaudeHooksWithTimeout(sessionID string, lifecycle, policy bool, policyTimeout time.Duration) (extraArgs []string, extraEnv map[string]string, err error) {
	settingsPath, err := sm.generateClaudeSettingsWithTimeout(sessionID, lifecycle, policy, policyTimeout)
	if err != nil {
		return nil, nil, err
	}

	sm.log.Info("injected claude hooks", "session_id", sessionID, "settings", settingsPath)

	return []string{"--settings", settingsPath}, nil, nil
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
	if len(mcpServers) == 0 {
		return nil, nil
	}

	switch agentName {
	case "claude":
		mcpConfigPath, err := sm.generateMCPConfig(sessionID, mcpServers)
		if err != nil {
			return nil, err
		}

		sm.log.Info("injected mcp config", "session_id", sessionID, "mcp_config", mcpConfigPath, "mcp_servers", len(mcpServers))

		return []string{"--mcp-config", mcpConfigPath}, nil
	case "codex":
		args, skipped, err := codexMCPServerArgs(mcpServers)
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
// enabled/disabled tools, per-tool execution mode — intact and merged, rather
// than flattening every server to graith's command/args/env shape.
//
// Values are JSON-encoded, which is also valid TOML for a string
// (`"…"`) and a string array (`["…","…"]`), the two value kinds Codex's
// `-c key=value` override parser accepts here.
func codexMCPServerArgs(mcpServers []config.MCPServerConfig) (args, skipped []string, err error) {
	if len(mcpServers) == 0 {
		return nil, nil, nil
	}

	grBin := resolveGrBin()

	cmdVal, err := json.Marshal(grBin)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal mcp command: %w", err)
	}

	args = make([]string, 0, len(mcpServers)*4)

	for _, s := range mcpServers {
		if !codexBareKeyRe.MatchString(s.Name) {
			skipped = append(skipped, s.Name)
			continue
		}

		proxyArgs, err := json.Marshal([]string{"mcp-proxy", s.Name})
		if err != nil {
			return nil, nil, fmt.Errorf("marshal mcp args for %q: %w", s.Name, err)
		}

		args = append(args,
			"-c", fmt.Sprintf("mcp_servers.%s.command=%s", s.Name, cmdVal),
			"-c", fmt.Sprintf("mcp_servers.%s.args=%s", s.Name, proxyArgs),
		)
	}

	return args, skipped, nil
}

// codexHookEvent describes one Codex hook event and its independently matched
// command groups.
type codexHookEvent struct {
	// event is the Codex hook event name. It must match a key the current Codex
	// CLI recognises in its HookEventsToml schema (PascalCase, e.g. SessionStart).
	event  string
	groups []codexHookGroup
}

type codexHookGroup struct {
	matcher  string
	commands []string
	timeout  int
}

// injectCodexHooks builds the Codex session-config overrides that register
// graith's lifecycle hooks and returns them as extra launch args.
//
// Current Codex no longer reads a CODEX_HOOKS_DIR env var (issue #1183). It
// discovers hooks from configuration-layer folders (hooks.json) and inline
// [hooks] config, and accepts hooks per-invocation as trusted session config via
// repeatable `-c hooks.<Event>=<toml>` overrides. Each override's value is
// parsed by Codex as TOML (codex-rs/utils/cli config_override), so graith emits
// a TOML array of matcher groups. Lifecycle groups have no matcher (match all),
// while command policy matches Bash only. `--dangerously-bypass-hook-trust`
// skips Codex's hook-trust review because graith generated and vetted these
// hooks; it does not disable the agent's separate native tool approvals.
//
// MCP-server wiring is NOT handled here — it rides the separate injectMCPConfig
// path (issue #1135), so a headless codex session gets MCP without hooks.
func (sm *SessionManager) injectCodexHooks(sessionID string, lifecycle, policy bool) (extraArgs []string, extraEnv map[string]string, err error) {
	return sm.injectCodexHooksWithTimeout(sessionID, lifecycle, policy, sm.Config().CommandPolicy.TimeoutDuration())
}

func (sm *SessionManager) injectCodexHooksWithTimeout(sessionID string, lifecycle, policy bool, policyTimeout time.Duration) (extraArgs []string, extraEnv map[string]string, err error) {
	grBin := shellQuote(resolveGrBin())

	var events []codexHookEvent
	if lifecycle {
		events = append(events,
			codexHookEvent{event: "SessionStart", groups: []codexHookGroup{{commands: []string{
				grBin + " report-status --event SessionStart",
				grBin + " check-inbox",
			}}}},
			codexHookEvent{event: "UserPromptSubmit", groups: []codexHookGroup{{commands: []string{
				grBin + " report-status --event UserPromptSubmit",
			}}}},
			codexHookEvent{event: "PostToolUse", groups: []codexHookGroup{{commands: []string{
				grBin + " report-status --event PostToolUse",
			}}}},
			codexHookEvent{event: "Stop", groups: []codexHookGroup{{commands: []string{
				grBin + " report-status --event Stop",
			}}}},
		)
	}

	var preToolGroups []codexHookGroup
	if lifecycle {
		preToolGroups = append(preToolGroups, codexHookGroup{commands: []string{
			grBin + " report-status --event PreToolUse",
		}})
	}

	if policy {
		// PreToolUse runs independently of Codex's native approval policy, so
		// command policy remains enforceable whether the agent prompts or not.
		preToolGroups = append(preToolGroups, codexHookGroup{
			matcher:  "^Bash$",
			commands: []string{commandPolicyHookCommand(grBin)},
			timeout:  commandPolicyHookTimeout(policyTimeout),
		})
	}

	if len(preToolGroups) > 0 {
		events = append(events, codexHookEvent{event: "PreToolUse", groups: preToolGroups})
	}

	for _, e := range events {
		value := codexHookOverrideGroups(e.groups)
		extraArgs = append(extraArgs, "-c", fmt.Sprintf("hooks.%s=%s", e.event, value))
	}

	// NOTE: --dangerously-bypass-hook-trust is process-wide, not scoped to the
	// -c overrides above: it also runs any OTHER enabled-but-untrusted hook
	// sources in the session (a repo-local .codex/hooks.json, user config hooks,
	// plugin hooks) without Codex's trust review. graith relies on this to run
	// its own generated hooks unattended; the containment boundary for anything
	// else those sources might do is the graith sandbox (see [sandbox]), the same
	// boundary that already confines the agent itself. Codex has no way today to
	// trust only the session-config hooks without bypassing trust globally.
	extraArgs = append(extraArgs, "--dangerously-bypass-hook-trust")

	sm.log.Info("injected codex hooks", "session_id", sessionID, "events", len(events))

	return extraArgs, nil, nil
}

// codexHookOverrideValue builds the inline-TOML value for a `hooks.<Event>`
// config override: a single match-all matcher group whose command handlers run
// the given shell commands in order. The shape mirrors Codex's HookEventsToml
// matcher-group schema ([{hooks=[{type="command",command="..."}]}]).
func codexHookOverrideValue(commands []string) string {
	return codexHookOverrideGroups([]codexHookGroup{{commands: commands}})
}

func codexHookOverrideGroups(groups []codexHookGroup) string {
	encodedGroups := make([]string, len(groups))
	for i, group := range groups {
		handlers := make([]string, len(group.commands))
		for j, command := range group.commands {
			timeout := ""
			if group.timeout > 0 {
				timeout = fmt.Sprintf(`,timeout=%d`, group.timeout)
			}

			handlers[j] = fmt.Sprintf(`{type="command",command=%s%s}`, tomlBasicString(command), timeout)
		}

		matcher := ""
		if group.matcher != "" {
			matcher = fmt.Sprintf(`matcher=%s,`, tomlBasicString(group.matcher))
		}

		encodedGroups[i] = fmt.Sprintf(`{%shooks=[%s]}`, matcher, strings.Join(handlers, ","))
	}

	return fmt.Sprintf(`[%s]`, strings.Join(encodedGroups, ","))
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
func (sm *SessionManager) injectCursorHooks(sessionID, worktreePath string, lifecycle, policy bool) (extraArgs []string, extraEnv map[string]string, err error) {
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

	hooks := hooksFile{Version: 1, Hooks: map[string][]hookEntry{}}
	if lifecycle {
		hooks.Hooks = map[string][]hookEntry{
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
		}
	}

	if policy {
		hooks.Hooks["preToolUse"] = []hookEntry{
			{Command: quoted + " command-policy-check"},
		}
	}

	data, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal cursor hooks: %w", err)
	}

	sm.cursorHooksMu.Lock()
	defer sm.cursorHooksMu.Unlock()

	existing, statErr := os.ReadFile(hooksPath)
	desiredHash := sha256Hex(data)

	var existingHash string
	if statErr == nil {
		existingHash = sha256Hex(existing)

		owners, scanErr := sm.cursorHooksOwnersFor(worktreePath, sessionID)

		matching := matchingCursorHooksOwners(owners, existingHash)
		if len(matching) == 0 {
			if scanErr != nil {
				return nil, nil, fmt.Errorf("refusing to overwrite %s: ownership metadata is unreadable; move it aside to use Cursor hooks: %w", hooksPath, scanErr)
			}

			return nil, nil, fmt.Errorf("refusing to overwrite %s: not owned by graith; move it aside to use Cursor hooks", hooksPath)
		}

		if existingHash == desiredHash {
			if err := sm.recordCursorHooksOwnership(sessionID, worktreePath, data); err != nil {
				return nil, nil, fmt.Errorf("record shared cursor hooks ownership: %w", err)
			}

			sm.retireStaleCursorHooksOwners(matching, sessionID)

			return nil, nil, nil
		}

		if scanErr != nil {
			return nil, nil, fmt.Errorf("refusing to replace %s: ownership metadata is unreadable: %w", hooksPath, scanErr)
		}

		if other := otherPersistedCursorHooksOwners(owners, sessionID); len(other) > 0 {
			return nil, nil, fmt.Errorf("refusing to replace %s: existing graith hooks are shared by session(s) %s, but this session requires a different hook definition", hooksPath, strings.Join(other, ", "))
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("read existing cursor hooks: %w", statErr)
	}

	if err := sm.publishCursorHooks(hooksPath, data, existingHash); err != nil {
		return nil, nil, fmt.Errorf("publish cursor hooks: %w", err)
	}

	// Record ownership only after the target has been published. If this write
	// fails, cleanup has no matching marker and therefore preserves the target;
	// it can leak a graith-generated file, but can never turn a failed publish
	// into authority to remove user content.
	if err := sm.recordCursorHooksOwnership(sessionID, worktreePath, data); err != nil {
		return nil, nil, fmt.Errorf("record cursor hooks ownership: %w", err)
	}

	if statErr == nil {
		owners, _ := sm.cursorHooksOwnersFor(worktreePath, sessionID)
		sm.retireStaleCursorHooksOwners(owners, sessionID)
	}

	sm.log.Info("injected cursor hooks", "session_id", sessionID, "hooks_path", hooksPath)

	return nil, nil, nil
}

// cursorHooksOwnershipPath is the marker graith writes when it generates or
// joins a Cursor hooks.json for a session. It lives in graith's per-session data
// dir, outside the user's worktree.
func (sm *SessionManager) cursorHooksOwnershipPath(sessionID string) string {
	return filepath.Join(sm.hookDir(sessionID), "cursor_hooks_owned")
}

const cursorHooksOwnershipVersion = 1

type cursorHooksOwnership struct {
	Version      int    `json:"version"`
	WorktreePath string `json:"worktree_path"`
	SHA256       string `json:"sha256"`
}

type cursorHooksOwner struct {
	sessionID  string
	markerPath string
	ownership  cursorHooksOwnership
	persisted  bool
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func canonicalCursorHooksWorktree(worktreePath string) string {
	if worktreePath == "" {
		return ""
	}

	return filepath.Clean(config.ResolvePath(worktreePath))
}

func validSHA256Hex(value string) bool {
	decoded, err := hex.DecodeString(value)

	return err == nil && len(decoded) == sha256.Size
}

func decodeCursorHooksOwnership(data []byte, legacyWorktreePath string) (cursorHooksOwnership, error) {
	value := strings.TrimSpace(string(data))
	if validSHA256Hex(value) {
		if legacyWorktreePath == "" {
			return cursorHooksOwnership{}, errors.New("legacy cursor hooks ownership marker has no persisted worktree")
		}

		return cursorHooksOwnership{
			Version:      cursorHooksOwnershipVersion,
			WorktreePath: canonicalCursorHooksWorktree(legacyWorktreePath),
			SHA256:       strings.ToLower(value),
		}, nil
	}

	var ownership cursorHooksOwnership
	if err := json.Unmarshal(data, &ownership); err != nil {
		return cursorHooksOwnership{}, fmt.Errorf("decode cursor hooks ownership marker: %w", err)
	}

	if ownership.Version != cursorHooksOwnershipVersion {
		return cursorHooksOwnership{}, fmt.Errorf("unsupported cursor hooks ownership marker version %d", ownership.Version)
	}

	if ownership.WorktreePath == "" {
		return cursorHooksOwnership{}, errors.New("cursor hooks ownership marker has no worktree path")
	}

	if !validSHA256Hex(ownership.SHA256) {
		return cursorHooksOwnership{}, errors.New("cursor hooks ownership marker has an invalid SHA-256")
	}

	ownership.WorktreePath = canonicalCursorHooksWorktree(ownership.WorktreePath)
	ownership.SHA256 = strings.ToLower(ownership.SHA256)

	return ownership, nil
}

func (sm *SessionManager) recordCursorHooksOwnership(sessionID, worktreePath string, data []byte) error {
	ownership := cursorHooksOwnership{
		Version:      cursorHooksOwnershipVersion,
		WorktreePath: canonicalCursorHooksWorktree(worktreePath),
		SHA256:       sha256Hex(data),
	}

	marker, err := json.Marshal(ownership)
	if err != nil {
		return fmt.Errorf("marshal cursor hooks ownership marker: %w", err)
	}

	if err := atomicfile.Write(sm.cursorHooksOwnershipPath(sessionID), marker, 0o600); err != nil {
		return fmt.Errorf("write cursor hooks ownership marker: %w", err)
	}

	return nil
}

// cursorHooksOwnersFor returns markers bound to worktreePath. A marker is
// persisted only when state.json still contains a Cursor session for that same
// worktree. The current session is treated as persisted while its failed-launch
// cleanup runs because Create removes its reservation immediately afterwards.
//
// Legacy bare-hash markers can be bound only through persisted session state.
// Structured markers retain their worktree after their session disappears, so
// an unchanged generated artifact can be recovered without treating arbitrary
// byte-identical files in other worktrees as owned.
func (sm *SessionManager) cursorHooksOwnersFor(worktreePath, currentSessionID string) ([]cursorHooksOwner, error) {
	target := canonicalCursorHooksWorktree(worktreePath)

	sm.mu.RLock()

	persistedPaths := make(map[string]string)

	for id, session := range sm.state.Sessions {
		if session.Agent == "cursor" && session.WorktreePath != "" {
			persistedPaths[id] = session.WorktreePath
		}
	}

	sm.mu.RUnlock()

	hooksRoot := filepath.Join(sm.paths.DataDir, "hooks")

	entries, err := os.ReadDir(hooksRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("read cursor hooks ownership directory: %w", err)
	}

	var (
		owners   []cursorHooksOwner
		scanErrs []error
	)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		ownerID := entry.Name()
		markerPath := sm.cursorHooksOwnershipPath(ownerID)

		marker, readErr := os.ReadFile(markerPath)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}

			if fallback := persistedPaths[ownerID]; ownerID == currentSessionID || canonicalCursorHooksWorktree(fallback) == target {
				scanErrs = append(scanErrs, fmt.Errorf("read ownership marker for session %s: %w", ownerID, readErr))
			}

			continue
		}

		fallback := persistedPaths[ownerID]
		if ownerID == currentSessionID && fallback == "" {
			fallback = worktreePath
		}

		ownership, decodeErr := decodeCursorHooksOwnership(marker, fallback)
		if decodeErr != nil {
			// A malformed marker for a persisted session on this worktree is
			// relevant uncertainty and must make replacement/cleanup fail closed.
			if canonicalCursorHooksWorktree(fallback) == target {
				scanErrs = append(scanErrs, fmt.Errorf("ownership marker for session %s: %w", ownerID, decodeErr))
			}

			continue
		}

		if ownership.WorktreePath != target {
			continue
		}

		persistedPath, inState := persistedPaths[ownerID]

		persisted := inState && canonicalCursorHooksWorktree(persistedPath) == target
		if ownerID == currentSessionID {
			persisted = true
		}

		owners = append(owners, cursorHooksOwner{
			sessionID: ownerID, markerPath: markerPath,
			ownership: ownership, persisted: persisted,
		})
	}

	return owners, errors.Join(scanErrs...)
}

func matchingCursorHooksOwners(owners []cursorHooksOwner, hash string) []cursorHooksOwner {
	var matching []cursorHooksOwner

	for _, owner := range owners {
		if strings.EqualFold(owner.ownership.SHA256, hash) {
			matching = append(matching, owner)
		}
	}

	return matching
}

func otherPersistedCursorHooksOwners(owners []cursorHooksOwner, sessionID string) []string {
	var sessionIDs []string

	for _, owner := range owners {
		if owner.persisted && owner.sessionID != sessionID {
			sessionIDs = append(sessionIDs, owner.sessionID)
		}
	}

	slices.Sort(sessionIDs)

	return sessionIDs
}

func removeCursorHooksOwnershipMarker(markerPath string) error {
	if err := os.Remove(markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := syncCursorHooksDir(filepath.Dir(markerPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sync ownership marker directory: %w", err)
	}

	return nil
}

func (sm *SessionManager) retireStaleCursorHooksOwners(owners []cursorHooksOwner, currentSessionID string) {
	for _, owner := range owners {
		if owner.persisted || owner.sessionID == currentSessionID {
			continue
		}

		if err := removeCursorHooksOwnershipMarker(owner.markerPath); err != nil {
			sm.log.Warn("failed to retire stale cursor hooks owner", "session_id", owner.sessionID, "err", err)
		}
	}
}

type cursorHooksRacePoint uint8

const (
	cursorHooksBeforeCreate cursorHooksRacePoint = iota
	cursorHooksBeforeClaim
	cursorHooksAfterClaim
)

// cursorHooksRaceHook is a deterministic test seam at the pathname boundaries
// guarded below. Production leaves it nil.
var cursorHooksRaceHook func(cursorHooksRacePoint)

var errCursorHooksRaced = errors.New("cursor hooks.json was concurrently replaced; refusing to overwrite user content")

// linkCursorHooksFile is the preferred no-replace publication primitive. Some
// writable filesystems do not support hard links; publishCursorFileNoReplace
// falls back to O_EXCL creation while preserving the same no-overwrite rule.
// It is a package var so tests can deterministically exercise that fallback.
var linkCursorHooksFile = os.Link

// stageCursorHooks creates a fully written, synced file next to hooksPath. The
// staged inode can then be linked into place with no-replace semantics.
func stageCursorHooks(hooksPath string, data []byte) (path string, err error) {
	f, err := os.CreateTemp(filepath.Dir(hooksPath), "hooks.json.graith-stage-*")
	if err != nil {
		return "", fmt.Errorf("create cursor hooks stage: %w", err)
	}

	path = f.Name()

	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(path)
		}
	}()

	if err = f.Chmod(0o600); err != nil {
		return "", fmt.Errorf("chmod cursor hooks stage: %w", err)
	}

	n, writeErr := f.Write(data)
	if writeErr != nil {
		return "", fmt.Errorf("write cursor hooks stage: %w", writeErr)
	}

	if n != len(data) {
		return "", fmt.Errorf("write cursor hooks stage: short write (%d of %d bytes)", n, len(data))
	}

	if err = f.Sync(); err != nil {
		return "", fmt.Errorf("sync cursor hooks stage: %w", err)
	}

	if err = f.Close(); err != nil {
		return "", fmt.Errorf("close cursor hooks stage: %w", err)
	}

	return path, nil
}

// reserveCursorQuarantine creates a private, uniquely named directory next to
// hooks.json and returns a nonexistent child pathname inside it. Renaming the
// public path to that child never relies on platform-specific replacement
// semantics and cannot overwrite a pre-existing user file or stale sidecar.
func reserveCursorQuarantine(dir, kind string) (string, error) {
	quarantineDir, err := os.MkdirTemp(dir, "hooks.json.graith-"+kind+"-*")
	if err != nil {
		return "", fmt.Errorf("reserve cursor %s quarantine: %w", kind, err)
	}

	return filepath.Join(quarantineDir, "hooks.json"), nil
}

func runCursorHooksRaceHook(point cursorHooksRacePoint) {
	if cursorHooksRaceHook != nil {
		cursorHooksRaceHook(point)
	}
}

func syncCursorHooksDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}

	err = dir.Sync()
	_ = dir.Close()

	return err
}

// publishCursorFileNoReplace places data at hooksPath only when the pathname is
// absent. A hard link publishes the already-synced stage atomically. On a
// filesystem without hard-link support, O_EXCL still makes creation conditional
// on absence; failures after creation leave a markerless graith file in place
// rather than removing by pathname and risking a concurrent replacement.
func publishCursorFileNoReplace(stage, hooksPath string, data []byte) error {
	linkErr := linkCursorHooksFile(stage, hooksPath)
	if linkErr == nil || errors.Is(linkErr, os.ErrExist) {
		return linkErr
	}

	f, createErr := os.OpenFile(hooksPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if createErr != nil {
		return fmt.Errorf("link cursor hooks: %w (exclusive-create fallback failed: %w)", linkErr, createErr)
	}

	fail := func(verb string, cause error) error {
		_ = f.Close()

		return fmt.Errorf("link cursor hooks: %w (%s fallback failed: %w)", linkErr, verb, cause)
	}

	n, writeErr := f.Write(data)
	if writeErr != nil {
		return fail("write", writeErr)
	}

	if n != len(data) {
		return fail("write", fmt.Errorf("short write (%d of %d bytes)", n, len(data)))
	}

	if err := f.Sync(); err != nil {
		return fail("sync", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("link cursor hooks: %w (close fallback failed: %w)", linkErr, err)
	}

	return nil
}

// publishCursorHooks publishes data without ever overwriting hooksPath. A first
// publish uses an exclusive hard link. A replacement first moves the current
// pathname aside and verifies the moved object before linking the staged file,
// so the hash check and pathname mutation apply to the same file object.
func (sm *SessionManager) publishCursorHooks(hooksPath string, data []byte, expectedHash string) error {
	stage, err := stageCursorHooks(hooksPath, data)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(stage) }()

	if expectedHash == "" {
		runCursorHooksRaceHook(cursorHooksBeforeCreate)

		if err := publishCursorFileNoReplace(stage, hooksPath, data); err != nil {
			if errors.Is(err, os.ErrExist) {
				return errCursorHooksRaced
			}

			return err
		}

		if err := syncCursorHooksDir(filepath.Dir(hooksPath)); err != nil {
			return fmt.Errorf("sync published cursor hooks: %w", err)
		}

		return nil
	}

	claimed, err := claimCursorHooks(hooksPath)
	if err != nil {
		return err
	}

	claimedData, err := readClaimedCursorHooks(claimed)
	if err != nil {
		if restoreErr := restoreCursorClaim(claimed, hooksPath); restoreErr != nil {
			return fmt.Errorf("verify claimed cursor hooks: %w (restore failed: %w)", err, restoreErr)
		}

		return fmt.Errorf("verify claimed cursor hooks: %w", err)
	}

	if !strings.EqualFold(sha256Hex(claimedData), expectedHash) {
		if restoreErr := restoreCursorClaim(claimed, hooksPath); restoreErr != nil {
			return fmt.Errorf("%w (restore failed: %w)", errCursorHooksRaced, restoreErr)
		}

		return errCursorHooksRaced
	}

	runCursorHooksRaceHook(cursorHooksAfterClaim)

	if err := publishCursorFileNoReplace(stage, hooksPath, data); err != nil {
		if errors.Is(err, os.ErrExist) {
			// The claimed file is the old graith-owned artifact. A newcomer at the
			// public path is left untouched; the old private link can be discarded.
			_ = removeCursorClaim(claimed)

			return errCursorHooksRaced
		}

		if restoreErr := restoreCursorClaim(claimed, hooksPath); restoreErr != nil {
			return fmt.Errorf("link cursor hooks: %w (restore failed: %w)", err, restoreErr)
		}

		return err
	}

	_ = removeCursorClaim(claimed)

	if err := syncCursorHooksDir(filepath.Dir(hooksPath)); err != nil {
		return fmt.Errorf("sync published cursor hooks: %w", err)
	}

	return nil
}

// claimCursorHooks atomically moves the current pathname into a uniquely
// reserved quarantine. The moved object is then stable for verification.
func claimCursorHooks(hooksPath string) (string, error) {
	quarantine, err := reserveCursorQuarantine(filepath.Dir(hooksPath), "claim")
	if err != nil {
		return "", err
	}

	runCursorHooksRaceHook(cursorHooksBeforeClaim)

	if err := os.Rename(hooksPath, quarantine); err != nil {
		// Remove only the directory graith created, and only if it is still empty.
		// If another process somehow populated the unpredictable child path, its
		// content is preserved.
		_ = os.Remove(filepath.Dir(quarantine))

		if errors.Is(err, os.ErrNotExist) {
			return "", errCursorHooksRaced
		}

		return "", fmt.Errorf("claim cursor hooks: %w", err)
	}

	return quarantine, nil
}

func readClaimedCursorHooks(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() {
		return nil, errors.New("claimed cursor hooks is not a regular file")
	}

	return os.ReadFile(path)
}

// restoreCursorClaim restores a quarantined file without overwriting anything
// that appeared at hooksPath meanwhile. If the public path is occupied, the
// quarantined file stays at its unique sidecar path so both files are preserved.
func restoreCursorClaim(quarantine, hooksPath string) error {
	if err := os.Link(quarantine, hooksPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("cursor hooks path is occupied; claimed content preserved at %s", quarantine)
		}

		return fmt.Errorf("restore cursor hooks from %s: %w", quarantine, err)
	}

	if err := os.Remove(quarantine); err != nil {
		return fmt.Errorf("remove restored cursor hooks sidecar: %w", err)
	}

	_ = os.Remove(filepath.Dir(quarantine))

	return nil
}

func removeCursorClaim(quarantine string) error {
	err := os.Remove(quarantine)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(filepath.Dir(quarantine))
	}

	return err
}

// removeGeneratedCursorHooks releases one session's ownership. It removes only
// the exact regular file object whose bytes match the marker, and only when no
// other persisted owner remains. Markerless, unreadable, modified, or
// concurrently replaced files are preserved.
func (sm *SessionManager) removeGeneratedCursorHooks(sessionID, worktreePath string) {
	if worktreePath == "" {
		return
	}

	sm.cursorHooksMu.Lock()
	defer sm.cursorHooksMu.Unlock()

	markerPath := sm.cursorHooksOwnershipPath(sessionID)

	recorded, err := os.ReadFile(markerPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			sm.log.Warn("cursor hooks ownership marker unreadable; leaving hooks in place", "session_id", sessionID, "err", err)
		}

		return
	}

	ownership, err := decodeCursorHooksOwnership(recorded, worktreePath)
	if err != nil {
		sm.log.Warn("cursor hooks ownership marker invalid; leaving hooks in place", "session_id", sessionID, "err", err)

		return
	}

	if ownership.WorktreePath != canonicalCursorHooksWorktree(worktreePath) {
		sm.log.Warn("cursor hooks ownership marker names a different worktree; leaving hooks in place", "session_id", sessionID, "marker_worktree", ownership.WorktreePath, "worktree", worktreePath)

		return
	}

	owners, scanErr := sm.cursorHooksOwnersFor(worktreePath, sessionID)
	if scanErr != nil {
		sm.log.Warn("cursor hooks ownership metadata unreadable; leaving hooks in place", "session_id", sessionID, "err", scanErr)

		return
	}

	if other := otherPersistedCursorHooksOwners(owners, sessionID); len(other) > 0 {
		if err := removeCursorHooksOwnershipMarker(markerPath); err != nil {
			sm.log.Warn("failed to release shared cursor hooks ownership", "session_id", sessionID, "remaining_owners", other, "err", err)
		}

		return
	}

	hooksPath := filepath.Join(worktreePath, ".cursor", "hooks.json")

	claimed, err := claimCursorHooks(hooksPath)
	if err != nil {
		if !errors.Is(err, errCursorHooksRaced) {
			sm.log.Warn("failed to claim cursor hooks for cleanup", "session_id", sessionID, "err", err)
		}

		return
	}

	runCursorHooksRaceHook(cursorHooksAfterClaim)

	claimedData, err := readClaimedCursorHooks(claimed)
	if err != nil || !strings.EqualFold(ownership.SHA256, sha256Hex(claimedData)) {
		if restoreErr := restoreCursorClaim(claimed, hooksPath); restoreErr != nil {
			sm.log.Warn("cursor hooks ownership changed during cleanup; content preserved in quarantine", "session_id", sessionID, "err", restoreErr)
		} else {
			sm.log.Info("leaving modified cursor hooks in place", "session_id", sessionID, "path", hooksPath)
		}

		return
	}

	// Make the public-path removal durable before dropping marker authority. If
	// the daemon crashes after this point, hooks.json is absent; a later user file
	// at the public path cannot be mistaken for the object being cleaned up.
	if err := syncCursorHooksDir(filepath.Dir(hooksPath)); err != nil {
		if restoreErr := restoreCursorClaim(claimed, hooksPath); restoreErr != nil {
			sm.log.Warn("failed to sync cursor hooks claim; content preserved in quarantine", "session_id", sessionID, "err", errors.Join(err, restoreErr))
		} else {
			sm.log.Warn("failed to sync cursor hooks claim; restored hooks", "session_id", sessionID, "err", err)
		}

		return
	}

	// Remove this marker and every stale structured marker for the same
	// worktree before deleting the quarantined artifact. A failure restores the
	// artifact without overwriting anything that appeared at the public path.
	markers := []string{markerPath}
	for _, owner := range owners {
		if !owner.persisted && owner.markerPath != markerPath {
			markers = append(markers, owner.markerPath)
		}
	}

	for _, path := range markers {
		if err := removeCursorHooksOwnershipMarker(path); err != nil {
			if restoreErr := restoreCursorClaim(claimed, hooksPath); restoreErr != nil {
				sm.log.Warn("failed to remove cursor hooks ownership marker; content preserved in quarantine", "session_id", sessionID, "err", errors.Join(err, restoreErr))
			} else {
				sm.log.Warn("failed to remove cursor hooks ownership marker; restored hooks", "session_id", sessionID, "err", err)
			}

			return
		}
	}

	if err := removeCursorClaim(claimed); err != nil && !errors.Is(err, os.ErrNotExist) {
		sm.log.Warn("failed to remove cursor hooks", "session_id", sessionID, "err", err)

		return
	}

	if err := syncCursorHooksDir(filepath.Dir(hooksPath)); err != nil {
		sm.log.Warn("failed to sync cursor hooks cleanup", "session_id", sessionID, "err", err)
	}
}

// injectHooks dispatches lifecycle-hook injection to the agent-specific
// implementation. It does NOT handle MCP config — that is injectMCPConfig's job,
// kept separate so MCP can be injected without hooks (see issue #1135). Returns
// nil for agents that don't support hooks.
func (sm *SessionManager) injectHooks(agentName, sessionID, worktreePath string, lifecycle, policy bool, policyTimeout time.Duration) (extraArgs []string, extraEnv map[string]string, err error) {
	switch agentName {
	case "claude":
		return sm.injectClaudeHooksWithTimeout(sessionID, lifecycle, policy, policyTimeout)
	case "codex":
		return sm.injectCodexHooksWithTimeout(sessionID, lifecycle, policy, policyTimeout)
	case "cursor":
		return sm.injectCursorHooks(sessionID, worktreePath, lifecycle, policy)
	default:
		sm.log.Info("agent does not support hooks, skipping", "agent", agentName, "session_id", sessionID)
		return nil, nil, nil
	}
}

// cleanupHooks removes generated hook files for a session. For Cursor sessions,
// it removes .cursor/hooks.json only after proving ownership of the exact file
// object at the pathname, then removes the graith rule from the worktree.
func (sm *SessionManager) cleanupHooks(sessionID, agentName, worktreePath string) {
	if agentName == "cursor" {
		sm.removeGeneratedCursorHooks(sessionID, worktreePath)
	}

	dir := sm.hookDir(sessionID)
	if err := os.RemoveAll(dir); err != nil {
		sm.log.Warn("failed to cleanup hooks", "session_id", sessionID, "err", err)
	}

	cleanupCursorRule(worktreePath)
}
