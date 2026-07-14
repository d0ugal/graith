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

	// Approval gating is opt-in (disabled by default). These tests exercise
	// the hook-generation and approval-queue mechanics, so enable it here.
	cfg := config.Default()
	enabled := true
	cfg.Approvals.Enabled = &enabled

	return NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(dir, "state.json"),
		DataDir:   dir,
	}, slog.Default())
}

func TestGenerateClaudeSettings(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-braw-02"

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

	expectedEvents := []string{
		"SessionStart",
		"SessionEnd",
		"UserPromptSubmit",
		"PreToolUse",
		"PostToolUse",
		"Notification",
		"Stop",
		"PreCompact",
		"PostCompact",
		"SubagentStart",
		"SubagentStop",
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

		// PreToolUse is scoped to exclude read-only tools (fail-closed); every
		// other event stays match-all (empty matcher).
		wantMatcher := ""
		if event == "PreToolUse" {
			wantMatcher = preToolUseMatcher()
		}

		if matchers[0].Matcher != wantMatcher {
			t.Errorf("event %q matcher = %q, want %q", event, matchers[0].Matcher, wantMatcher)
		}

		for _, hook := range matchers[0].Hooks {
			if hook.Type != "command" {
				t.Errorf("event %q type = %q, want %q", event, hook.Type, "command")
			}
		}

		switch event {
		case "PreToolUse":
			if len(matchers[0].Hooks) != 1 {
				t.Errorf("event %q has %d hooks, want 1", event, len(matchers[0].Hooks))
			} else if !strings.Contains(matchers[0].Hooks[0].Command, "approve-request") {
				t.Errorf("event %q command = %q, does not contain approve-request", event, matchers[0].Hooks[0].Command)
			}
		case "SessionStart":
			if len(matchers[0].Hooks) != 2 {
				t.Errorf("event %q has %d hooks, want 2", event, len(matchers[0].Hooks))
			} else {
				if !strings.Contains(matchers[0].Hooks[0].Command, "report-status") {
					t.Errorf("SessionStart hook[0] = %q, want report-status", matchers[0].Hooks[0].Command)
				}

				if !strings.Contains(matchers[0].Hooks[1].Command, "check-inbox") {
					t.Errorf("SessionStart hook[1] = %q, want check-inbox", matchers[0].Hooks[1].Command)
				}
			}
		default:
			if len(matchers[0].Hooks) != 1 {
				t.Errorf("event %q has %d hooks, want 1", event, len(matchers[0].Hooks))
			} else {
				if !strings.Contains(matchers[0].Hooks[0].Command, "report-status") {
					t.Errorf("event %q command = %q, does not contain report-status", event, matchers[0].Hooks[0].Command)
				}

				if !strings.Contains(matchers[0].Hooks[0].Command, "--event "+event) {
					t.Errorf("event %q command = %q, does not contain --event %s", event, matchers[0].Hooks[0].Command, event)
				}
			}
		}
	}

	if len(parsed.Hooks) != len(expectedEvents) {
		t.Errorf("hooks has %d events, want %d", len(parsed.Hooks), len(expectedEvents))
	}
}

func TestInjectClaudeHooks(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-braw-03"

	extraArgs, extraEnv, err := sm.injectClaudeHooks(sessionID, false, nil)
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
	sessionID := "kirk-braw-04"

	_, err := sm.generateClaudeSettings(sessionID, false)
	if err != nil {
		t.Fatalf("generateClaudeSettings() error = %v", err)
	}

	hookDir := sm.hookDir(sessionID)
	if _, err := os.Stat(hookDir); err != nil {
		t.Fatalf("hook dir does not exist before cleanup: %v", err)
	}

	sm.cleanupHooks(sessionID, "claude", "")

	if _, err := os.Stat(hookDir); !os.IsNotExist(err) {
		t.Errorf("hook dir still exists after cleanup: err = %v", err)
	}
}

func TestCleanupHooksNonexistent(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sm.cleanupHooks("haar-session", "claude", "")
}

func TestCleanupCursorHooks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-cursor"
	worktree := t.TempDir()

	_, _, err := sm.injectCursorHooks(sessionID, worktree, false)
	if err != nil {
		t.Fatalf("injectCursorHooks() error = %v", err)
	}

	hooksPath := filepath.Join(worktree, ".cursor", "hooks.json")
	if _, err := os.Stat(hooksPath); err != nil {
		t.Fatalf("cursor hooks.json not created: %v", err)
	}

	sm.cleanupHooks(sessionID, "cursor", worktree)

	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Errorf("cursor hooks.json still exists after cleanup: err = %v", err)
	}
}

func TestResolveGrBin(t *testing.T) {
	result := resolveGrBin()
	if result == "" {
		t.Error("resolveGrBin() returned empty string")
	}
}

func TestInjectCodexHooks(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-codex-01"

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
		"permission-request",
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

func TestCodexHookScriptContent(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-codex-02"

	_, _, err := sm.injectCodexHooks(sessionID, false)
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

		switch filename {
		case "permission-request":
			if !strings.Contains(content, "approve-request") {
				t.Errorf("codex hook %q does not contain approve-request; content = %q", filename, content)
			}
		case "session-start":
			if !strings.Contains(content, "report-status") {
				t.Errorf("codex hook %q does not contain report-status; content = %q", filename, content)
			}

			if !strings.Contains(content, "check-inbox") {
				t.Errorf("codex hook %q does not contain check-inbox; content = %q", filename, content)
			}
		default:
			if !strings.Contains(content, "--event "+eventName) {
				t.Errorf("codex hook %q does not contain --event %s; content = %q", filename, eventName, content)
			}

			if !strings.Contains(content, "report-status") {
				t.Errorf("codex hook %q does not contain report-status; content = %q", filename, content)
			}
		}
	}
}

func TestInjectHooksSupported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)

	args, env, err := sm.injectHooks("claude", "kirk-claude", "", false, nil)
	if err != nil {
		t.Fatalf("injectHooks(claude) error = %v", err)
	}

	if len(args) == 0 {
		t.Error("injectHooks(claude) returned no args")
	}

	if env != nil {
		t.Errorf("injectHooks(claude) returned unexpected env: %v", env)
	}

	args, env, err = sm.injectHooks("codex", "kirk-codex", "", false, nil)
	if err != nil {
		t.Fatalf("injectHooks(codex) error = %v", err)
	}

	if len(args) != 0 {
		t.Errorf("injectHooks(codex) returned unexpected args: %v", args)
	}

	if _, ok := env["CODEX_HOOKS_DIR"]; !ok {
		t.Error("injectHooks(codex) missing CODEX_HOOKS_DIR")
	}

	worktree := t.TempDir()

	args, env, err = sm.injectHooks("cursor", "kirk-cursor-sup", worktree, false, nil)
	if err != nil {
		t.Fatalf("injectHooks(cursor) error = %v", err)
	}

	if len(args) != 0 {
		t.Errorf("injectHooks(cursor) returned unexpected args: %v", args)
	}

	if env != nil {
		t.Errorf("injectHooks(cursor) returned unexpected env: %v", env)
	}

	hooksPath := filepath.Join(worktree, ".cursor", "hooks.json")
	if _, err := os.Stat(hooksPath); err != nil {
		t.Errorf("cursor hooks.json not created: %v", err)
	}
}

func TestInjectHooksUnsupportedIsNoop(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	for _, agent := range []string{"agy", "opencode", "custom-agent"} {
		args, env, err := sm.injectHooks(agent, "haar-unsupported", "", false, nil)
		if err != nil {
			t.Errorf("injectHooks(%q) unexpected error: %v", agent, err)
		}

		if args != nil {
			t.Errorf("injectHooks(%q) returned non-nil args: %v", agent, args)
		}

		if env != nil {
			t.Errorf("injectHooks(%q) returned non-nil env: %v", agent, env)
		}
	}
}

func TestHookDir(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	dir := sm.hookDir("braw123")

	expected := filepath.Join(sm.paths.DataDir, "hooks", "braw123")
	if dir != expected {
		t.Errorf("hookDir() = %q, want %q", dir, expected)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/usr/bin/gr", "'/usr/bin/gr'"},
		{"/Users/o'malley/bin/gr", "'/Users/o'\\''malley/bin/gr'"},
		{"it's a 'test'", "'it'\\''s a '\\''test'\\'''"},
		{"simple", "'simple'"},
		{"", "''"},
		{"/path with spaces/gr", "'/path with spaces/gr'"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCodexHookScriptsEscapeSingleQuotes(t *testing.T) {
	// Create a fake gr binary in a directory whose name contains a single quote.
	fakeDir := filepath.Join(t.TempDir(), "o'malley", "bin")
	if err := os.MkdirAll(fakeDir, 0o750); err != nil {
		t.Fatalf("create fake dir: %v", err)
	}

	fakeBin := filepath.Join(fakeDir, "gr")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil { //nolint:gosec // G306: script/binary must be executable
		t.Fatalf("write fake binary: %v", err)
	}

	// Override PATH so resolveGrBin finds our fake binary.
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-codex-quote"

	_, _, err := sm.injectCodexHooks(sessionID, false)
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	hooksDir := filepath.Join(sm.hookDir(sessionID), "codex-hooks")
	expectedQuoted := shellQuote(fakeBin)

	scripts := []string{"permission-request", "session-start", "stop"}
	for _, name := range scripts {
		path := filepath.Join(hooksDir, name)

		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read codex hook %q: %v", name, err)
			continue
		}

		content := string(data)
		if !strings.Contains(content, expectedQuoted) {
			t.Errorf("codex hook %q does not contain properly escaped path %q; content = %q", name, expectedQuoted, content)
		}
	}
}

func TestGenerateMCPConfig(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-mcp-01"

	servers := []config.MCPServerConfig{
		{Name: "graith", Command: "/usr/bin/gr", Args: []string{"mcp"}},
		{Name: "chrome", Command: "npx", Args: []string{"chrome-mcp"}, Env: map[string]string{"DISPLAY": ":0"}},
	}

	mcpConfigPath, err := sm.generateMCPConfig(sessionID, servers)
	if err != nil {
		t.Fatalf("generateMCPConfig() error = %v", err)
	}

	data, err := os.ReadFile(mcpConfigPath)
	if err != nil {
		t.Fatalf("read mcp config: %v", err)
	}

	var parsed struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal mcp config: %v", err)
	}

	if len(parsed.MCPServers) != 2 {
		t.Fatalf("mcpServers has %d entries, want 2", len(parsed.MCPServers))
	}

	grBin := resolveGrBin()

	graith, ok := parsed.MCPServers["graith"]
	if !ok {
		t.Fatal("mcpServers missing graith")
	}

	if graith.Command != grBin {
		t.Errorf("graith command = %q, want %q", graith.Command, grBin)
	}

	if len(graith.Args) != 2 || graith.Args[0] != "mcp-proxy" || graith.Args[1] != "graith" {
		t.Errorf("graith args = %v, want [mcp-proxy graith]", graith.Args)
	}

	chrome, ok := parsed.MCPServers["chrome"]
	if !ok {
		t.Fatal("mcpServers missing chrome")
	}

	if chrome.Command != grBin {
		t.Errorf("chrome command = %q, want %q", chrome.Command, grBin)
	}

	if len(chrome.Args) != 2 || chrome.Args[0] != "mcp-proxy" || chrome.Args[1] != "chrome" {
		t.Errorf("chrome args = %v, want [mcp-proxy chrome]", chrome.Args)
	}
}

func TestInjectClaudeHooksWithMCPServers(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-mcp-inject"

	servers := []config.MCPServerConfig{
		{Name: "graith", Command: "/usr/bin/gr", Args: []string{"mcp"}},
		{Name: "chrome", Command: "npx", Args: []string{"chrome-mcp"}},
	}

	args, env, err := sm.injectClaudeHooks(sessionID, false, servers)
	if err != nil {
		t.Fatalf("injectClaudeHooks() error = %v", err)
	}

	if env != nil {
		t.Errorf("unexpected env: %v", env)
	}

	if len(args) != 4 {
		t.Fatalf("args = %v, want 4 elements [--settings path --mcp-config path]", args)
	}

	if args[0] != "--settings" {
		t.Errorf("args[0] = %q, want --settings", args[0])
	}

	if args[2] != "--mcp-config" {
		t.Errorf("args[2] = %q, want --mcp-config", args[2])
	}

	mcpConfigPath := args[3]

	data, err := os.ReadFile(mcpConfigPath)
	if err != nil {
		t.Fatalf("read mcp config at %q: %v", mcpConfigPath, err)
	}

	if !strings.Contains(string(data), "mcpServers") {
		t.Error("mcp config file should contain mcpServers")
	}
}

func TestGenerateClaudeSettingsNoMCPServers(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-mcp-02"

	settingsPath, err := sm.generateClaudeSettings(sessionID, false)
	if err != nil {
		t.Fatalf("generateClaudeSettings() error = %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	if strings.Contains(string(data), "mcpServers") {
		t.Error("settings should not contain mcpServers when none provided")
	}
}

func TestResolveMCPServers(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	t.Run("auto-injects graith", func(t *testing.T) {
		servers := sm.resolveMCPServers("claude")
		if len(servers) == 0 {
			t.Fatal("expected at least graith server")
		}

		if servers[0].Name != "graith" {
			t.Errorf("first server = %q, want graith", servers[0].Name)
		}

		if len(servers[0].Args) != 1 || servers[0].Args[0] != "mcp" {
			t.Errorf("graith args = %v, want [mcp]", servers[0].Args)
		}
	})

	t.Run("includes global servers", func(t *testing.T) {
		sm.cfg.MCPServers = []config.MCPServerConfig{
			{Name: "chrome", Command: "npx", Args: []string{"chrome-mcp"}},
		}

		servers := sm.resolveMCPServers("claude")
		if len(servers) != 2 {
			t.Fatalf("got %d servers, want 2 (graith + chrome)", len(servers))
		}

		if servers[1].Name != "chrome" {
			t.Errorf("second server = %q, want chrome", servers[1].Name)
		}
	})

	t.Run("applies per-agent overrides", func(t *testing.T) {
		sm.cfg.MCPServers = []config.MCPServerConfig{
			{Name: "chrome", Command: "npx", Args: []string{"chrome-mcp", "--port", "9222"}},
		}
		sm.cfg.Agents = map[string]config.Agent{
			"claude": {
				MCPServers: map[string]config.MCPServerConfig{
					"chrome": {Args: []string{"chrome-mcp", "--port", "9333"}},
				},
			},
		}
		servers := sm.resolveMCPServers("claude")
		found := false

		for _, s := range servers {
			if s.Name == "chrome" {
				found = true

				if s.Args[2] != "9333" {
					t.Errorf("chrome args = %v, want port 9333", s.Args)
				}
			}
		}

		if !found {
			t.Error("chrome server not found after merge")
		}
	})

	t.Run("can disable graith per-agent", func(t *testing.T) {
		sm2 := newTestSessionManagerWithDataDir(t)
		sm2.cfg.MCPServers = nil
		sm2.cfg.Agents = map[string]config.Agent{
			"claude": {
				MCPServers: map[string]config.MCPServerConfig{
					"graith": {Disabled: true},
				},
			},
		}

		servers := sm2.resolveMCPServers("claude")
		for _, s := range servers {
			if s.Name == "graith" {
				t.Error("graith should be disabled but was found")
			}
		}
	})

	t.Run("can disable graith globally", func(t *testing.T) {
		sm2 := newTestSessionManagerWithDataDir(t)
		sm2.cfg.MCPServers = []config.MCPServerConfig{
			{Name: "graith", Disabled: true},
		}

		servers := sm2.resolveMCPServers("claude")
		for _, s := range servers {
			if s.Name == "graith" {
				t.Error("graith should be disabled via global config but was found")
			}
		}
	})

	t.Run("global graith override uses user command", func(t *testing.T) {
		sm2 := newTestSessionManagerWithDataDir(t)
		sm2.cfg.MCPServers = []config.MCPServerConfig{
			{Name: "graith", Command: "/custom/gr", Args: []string{"mcp", "--verbose"}},
		}
		servers := sm2.resolveMCPServers("claude")

		var found bool

		for _, s := range servers {
			if s.Name == "graith" {
				found = true

				if s.Command != "/custom/gr" {
					t.Errorf("graith command = %q, want /custom/gr", s.Command)
				}

				if len(s.Args) != 2 || s.Args[1] != "--verbose" {
					t.Errorf("graith args = %v, want [mcp --verbose]", s.Args)
				}
			}
		}

		if !found {
			t.Error("graith server not found")
		}

		count := 0

		for _, s := range servers {
			if s.Name == "graith" {
				count++
			}
		}

		if count != 1 {
			t.Errorf("got %d graith entries, want exactly 1", count)
		}
	})
}

func TestInjectCursorHooks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-cursor-01"
	worktree := t.TempDir()

	extraArgs, extraEnv, err := sm.injectCursorHooks(sessionID, worktree, false)
	if err != nil {
		t.Fatalf("injectCursorHooks() error = %v", err)
	}

	if len(extraArgs) != 0 {
		t.Errorf("extraArgs length = %d, want 0", len(extraArgs))
	}

	if extraEnv != nil {
		t.Errorf("extraEnv = %v, want nil", extraEnv)
	}

	hooksPath := filepath.Join(worktree, ".cursor", "hooks.json")

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read cursor hooks: %v", err)
	}

	var parsed struct {
		Version int `json:"version"`
		Hooks   map[string][]struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal cursor hooks: %v", err)
	}

	if parsed.Version != 1 {
		t.Errorf("version = %d, want 1", parsed.Version)
	}

	expectedEvents := []string{"sessionStart", "preToolUse", "postToolUse", "stop"}
	for _, event := range expectedEvents {
		hooks, ok := parsed.Hooks[event]
		if !ok {
			t.Errorf("missing hook event %q", event)
			continue
		}

		if len(hooks) == 0 {
			t.Errorf("event %q has no hooks", event)
		}
	}

	if len(parsed.Hooks) != len(expectedEvents) {
		t.Errorf("hooks has %d events, want %d", len(parsed.Hooks), len(expectedEvents))
	}
}

func TestInjectCursorHooksContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-cursor-02"
	worktree := t.TempDir()

	_, _, err := sm.injectCursorHooks(sessionID, worktree, false)
	if err != nil {
		t.Fatalf("injectCursorHooks() error = %v", err)
	}

	hooksPath := filepath.Join(worktree, ".cursor", "hooks.json")

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read cursor hooks: %v", err)
	}

	var parsed struct {
		Hooks map[string][]struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal cursor hooks: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "report-status") {
		t.Error("cursor hooks missing report-status command")
	}

	if !strings.Contains(content, "approve-request") {
		t.Error("cursor hooks missing approve-request command")
	}

	if !strings.Contains(content, "check-inbox") {
		t.Error("cursor hooks missing check-inbox command")
	}
}

func TestCursorProjectKey(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/Users/dougalmatthews/Code/graith", "Users-dougalmatthews-Code-graith"},
		{"/Users/dougalmatthews/.graith/worktrees/graith/af4385950142/7146d968", "Users-dougalmatthews-graith-worktrees-graith-af4385950142-7146d968"},
		{"/Users/dougalmatthews/Library/Application Support/graith/worktrees/graith/e52613751b29/250dfbe5", "Users-dougalmatthews-Library-Application-Support-graith-worktrees-graith-e52613751b29-250dfbe5"},
	}
	for _, tt := range tests {
		got := cursorProjectKey(tt.path)
		if got != tt.want {
			t.Errorf("cursorProjectKey(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestPreTrustCursorWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	worktree := "/fake/worktree/path"
	if err := preTrustCursorWorkspace(worktree); err != nil {
		t.Fatalf("preTrustCursorWorkspace() error = %v", err)
	}

	key := cursorProjectKey(worktree)
	sentinel := filepath.Join(home, ".cursor", "projects", key, ".workspace-trusted")

	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel file not created: %v", err)
	}

	var trust struct {
		TrustedAt     string `json:"trustedAt"`
		WorkspacePath string `json:"workspacePath"`
	}
	if err := json.Unmarshal(data, &trust); err != nil {
		t.Fatalf("sentinel is not valid JSON: %v", err)
	}

	if trust.WorkspacePath != worktree {
		t.Errorf("workspacePath = %q, want %q", trust.WorkspacePath, worktree)
	}

	if trust.TrustedAt == "" {
		t.Error("trustedAt is empty")
	}
}

func TestPreTrustCursorWorkspaceDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sm := newTestSessionManagerWithDataDir(t)
	disabled := false
	sm.cfg.Agents = map[string]config.Agent{
		"cursor": {PreTrustWorkspace: &disabled},
	}

	worktree := t.TempDir()

	_, _, err := sm.injectCursorHooks("haar-no-trust", worktree, false)
	if err != nil {
		t.Fatalf("injectCursorHooks() error = %v", err)
	}

	key := cursorProjectKey(worktree)

	sentinel := filepath.Join(home, ".cursor", "projects", key, ".workspace-trusted")
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("sentinel file should not exist when pre_trust_workspace=false, err = %v", err)
	}
}

func TestPreTrustCursorWorkspaceIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	worktree := "/fake/worktree/path"
	if err := preTrustCursorWorkspace(worktree); err != nil {
		t.Fatalf("first call error = %v", err)
	}

	if err := preTrustCursorWorkspace(worktree); err != nil {
		t.Fatalf("second call error = %v", err)
	}
}

func TestInjectCursorHooksEmptyWorktree(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	args, env, err := sm.injectCursorHooks("haar-no-worktree", "", false)
	if err != nil {
		t.Fatalf("injectCursorHooks() error = %v", err)
	}

	if args != nil {
		t.Errorf("expected nil args, got %v", args)
	}

	if env != nil {
		t.Errorf("expected nil env, got %v", env)
	}
}

func TestClaudeSettingsEscapeSingleQuotes(t *testing.T) {
	fakeDir := filepath.Join(t.TempDir(), "o'malley", "bin")
	if err := os.MkdirAll(fakeDir, 0o750); err != nil {
		t.Fatalf("create fake dir: %v", err)
	}

	fakeBin := filepath.Join(fakeDir, "gr")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil { //nolint:gosec // G306: script/binary must be executable
		t.Fatalf("write fake binary: %v", err)
	}

	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-claude-quote"

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
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	expectedQuoted := shellQuote(fakeBin)

	for event, matchers := range parsed.Hooks {
		for _, m := range matchers {
			for _, h := range m.Hooks {
				if !strings.HasPrefix(h.Command, expectedQuoted+" ") {
					t.Errorf("event %q command does not start with quoted path %q; got %q", event, expectedQuoted, h.Command)
				}
			}
		}
	}
}

func TestGenerateClaudeSettingsApprovalsDisabled(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	disabled := false
	sm.cfg.Approvals.Enabled = &disabled

	settingsPath, err := sm.generateClaudeSettings("thrawn-no-approve", false)
	if err != nil {
		t.Fatalf("generateClaudeSettings() error = %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	if _, ok := parsed.Hooks["PreToolUse"]; ok {
		t.Error("PreToolUse hook present when approvals disabled, want omitted")
	}

	// The other lifecycle hooks must still be installed.
	for _, event := range []string{"SessionStart", "SessionEnd", "UserPromptSubmit", "PostToolUse", "Notification", "Stop"} {
		if _, ok := parsed.Hooks[event]; !ok {
			t.Errorf("event %q missing when approvals disabled, want present", event)
		}
	}

	if strings.Contains(string(data), "approve-request") {
		t.Error("settings contain approve-request when approvals disabled")
	}
}

// TestGenerateClaudeSettingsYoloInstallsPreToolUse verifies that a yolo session
// installs the PreToolUse approve-request hook even when global approval gating
// is disabled, so its tool calls route through the daemon's auto-approve path.
func TestGenerateClaudeSettingsYoloInstallsPreToolUse(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	disabled := false
	sm.cfg.Approvals.Enabled = &disabled

	settingsPath, err := sm.generateClaudeSettings("bonnie-yolo", true)
	if err != nil {
		t.Fatalf("generateClaudeSettings() error = %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	if _, ok := parsed.Hooks["PreToolUse"]; !ok {
		t.Error("PreToolUse hook omitted for yolo session, want installed")
	}

	if !strings.Contains(string(data), "approve-request") {
		t.Error("yolo settings missing approve-request command")
	}
}

// TestInjectCodexHooksYoloInstallsPermissionRequest verifies a yolo codex
// session installs the permission-request (approve-request) hook script even
// when global approval gating is off.
func TestInjectCodexHooksYoloInstallsPermissionRequest(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	disabled := false
	sm.cfg.Approvals.Enabled = &disabled

	_, extraEnv, err := sm.injectCodexHooks("bonnie-codex-yolo", true)
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	hooksDir := extraEnv["CODEX_HOOKS_DIR"]

	script, err := os.ReadFile(filepath.Join(hooksDir, "permission-request"))
	if err != nil {
		t.Fatalf("permission-request script missing for yolo session: %v", err)
	}

	if !strings.Contains(string(script), "approve-request") {
		t.Error("yolo permission-request script missing approve-request command")
	}
}

// TestInjectCursorHooksYoloInstallsPreToolUse verifies a yolo cursor session
// installs the preToolUse approve-request hook even when gating is off.
func TestInjectCursorHooksYoloInstallsPreToolUse(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	disabled := false
	sm.cfg.Approvals.Enabled = &disabled

	worktree := t.TempDir()

	if _, _, err := sm.injectCursorHooks("bonnie-cursor-yolo", worktree, true); err != nil {
		t.Fatalf("injectCursorHooks() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(worktree, ".cursor", "hooks.json"))
	if err != nil {
		t.Fatalf("read cursor hooks: %v", err)
	}

	if !strings.Contains(string(data), "preToolUse") || !strings.Contains(string(data), "approve-request") {
		t.Errorf("yolo cursor hooks missing preToolUse approve-request: %s", data)
	}
}

func TestInjectCodexHooksApprovalsDisabled(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	disabled := false
	sm.cfg.Approvals.Enabled = &disabled

	_, extraEnv, err := sm.injectCodexHooks("thrawn-codex-no-approve", false)
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	hooksDir := extraEnv["CODEX_HOOKS_DIR"]

	if _, err := os.Stat(filepath.Join(hooksDir, "permission-request")); !os.IsNotExist(err) {
		t.Errorf("permission-request script present when approvals disabled, err = %v", err)
	}

	// Other lifecycle scripts must still exist.
	for _, name := range []string{"session-start", "user-prompt-submit", "pre-tool-use", "post-tool-use", "stop"} {
		if _, err := os.Stat(filepath.Join(hooksDir, name)); err != nil {
			t.Errorf("script %q missing when approvals disabled: %v", name, err)
		}
	}
}

func TestInjectCursorHooksApprovalsDisabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	disabled := false
	sm.cfg.Approvals.Enabled = &disabled
	worktree := t.TempDir()

	_, _, err := sm.injectCursorHooks("thrawn-cursor-no-approve", worktree, false)
	if err != nil {
		t.Fatalf("injectCursorHooks() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(worktree, ".cursor", "hooks.json"))
	if err != nil {
		t.Fatalf("read cursor hooks: %v", err)
	}

	var parsed struct {
		Hooks map[string][]struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal cursor hooks: %v", err)
	}

	if _, ok := parsed.Hooks["preToolUse"]; ok {
		t.Error("preToolUse hook present when approvals disabled, want omitted")
	}

	for _, event := range []string{"sessionStart", "postToolUse", "stop"} {
		if _, ok := parsed.Hooks[event]; !ok {
			t.Errorf("event %q missing when approvals disabled, want present", event)
		}
	}

	if strings.Contains(string(data), "approve-request") {
		t.Error("cursor hooks contain approve-request when approvals disabled")
	}
}

// TestPreToolUseMatcher verifies the PreToolUse approval hook is scoped to
// exclude a known read-only set (fail-closed): the matcher is an anchored
// negative lookahead over exactly that set, and every other tool — mutating,
// MCP, or unknown/new — still routes to the daemon.
func TestPreToolUseMatcher(t *testing.T) {
	// Exact string: guards the anchor (^), the trailing "." and the exact
	// exempt set all at once. Dropping the anchor or widening the set would be
	// a fail-open regression, so pin the literal.
	want := `^(?!(Read|Glob|Grep|LS|NotebookRead)$).`
	if got := preToolUseMatcher(); got != want {
		t.Fatalf("preToolUseMatcher() = %q, want %q", got, want)
	}

	// The matcher semantic is "fire for every tool NOT exactly in the exempt
	// set". Membership in preToolUseExemptTools is that semantic; this table
	// documents which tools skip the round-trip and, crucially, that mutating,
	// MCP, and unknown tools do not.
	inExempt := func(name string) bool {
		for _, e := range preToolUseExemptTools {
			if e == name {
				return true
			}
		}

		return false
	}

	cases := []struct {
		tool         string
		wantExcluded bool
	}{
		// Read-only set: excluded (hook skipped).
		{"Read", true},
		{"Glob", true},
		{"Grep", true},
		{"LS", true},
		{"NotebookRead", true},
		// Mutating tools: still route.
		{"Bash", false},
		{"Write", false},
		{"Edit", false},
		{"MultiEdit", false},
		{"NotebookEdit", false},
		{"WebFetch", false},
		{"WebSearch", false},
		{"Task", false},
		// TodoWrite mutates state — explicitly NOT exempt.
		{"TodoWrite", false},
		// MCP tools always route (fail-closed).
		{"mcp__memory__create", false},
		{"mcp__chrome-devtools__click", false},
		// Unknown / renamed tools route (fail-closed).
		{"SomeFutureTool", false},
		{"ReadFile", false}, // superstring of Read must not be excluded
	}

	for _, tc := range cases {
		if got := inExempt(tc.tool); got != tc.wantExcluded {
			t.Errorf("tool %q excluded = %v, want %v", tc.tool, got, tc.wantExcluded)
		}
	}
}

// TestGenerateClaudeSettingsPreToolUseScoped verifies the generated settings
// file carries the scoped matcher on the PreToolUse group.
func TestGenerateClaudeSettingsPreToolUseScoped(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	settingsPath, err := sm.generateClaudeSettings("canny-scope", false)
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
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	pre, ok := parsed.Hooks["PreToolUse"]
	if !ok || len(pre) != 1 {
		t.Fatalf("PreToolUse group = %v, want exactly one", pre)
	}

	if pre[0].Matcher != preToolUseMatcher() {
		t.Errorf("PreToolUse matcher = %q, want %q", pre[0].Matcher, preToolUseMatcher())
	}

	// Every other event stays match-all (empty matcher).
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Notification", "Stop"} {
		g, ok := parsed.Hooks[event]
		if !ok || len(g) != 1 {
			t.Fatalf("event %q group = %v, want exactly one", event, g)
		}

		if g[0].Matcher != "" {
			t.Errorf("event %q matcher = %q, want empty", event, g[0].Matcher)
		}
	}
}
