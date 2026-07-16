package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/pelletier/go-toml/v2"
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

	extraArgs, extraEnv, err := sm.injectClaudeHooks(sessionID, false, sm.Config().Agents["claude"])
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

// TestResolveGrBinUsesInvocationName is a regression test for a dev build
// wiring hooks to an unrelated production "gr" on PATH instead of itself. A
// daemon launched as "gr-dev" must resolve its own name, not the hardcoded
// "gr", even when a different "gr" binary is present on PATH.
func TestResolveGrBinUsesInvocationName(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}

	// Plant both a production "gr" and the dev "gr-dev" on PATH. The dev daemon
	// must pick gr-dev — the bug picked gr.
	grBin := filepath.Join(binDir, "gr")
	devBin := filepath.Join(binDir, "gr-dev")

	for _, p := range []string{grBin, devBin} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // G306: must be executable
			t.Fatalf("write %q: %v", p, err)
		}
	}

	t.Setenv("PATH", binDir)

	// Simulate being launched as gr-dev.
	origArgs := os.Args

	t.Cleanup(func() { os.Args = origArgs })

	os.Args = []string{devBin, "daemon", "start"}

	if got := resolveGrBin(); got != devBin {
		t.Errorf("resolveGrBin() = %q, want %q (must resolve its own invocation name, not the planted %q)", got, devBin, grBin)
	}
}

// TestResolveGrBinEdgeCases exercises the basename guard and the fallback chain.
// Degenerate os.Args[0] values (empty, ".", "/", empty argv) must collapse to
// the default "gr", a bare invocation name must be looked up on PATH, and a name
// absent from PATH must fall through to os.Executable().
func TestResolveGrBinEdgeCases(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}

	grBin := filepath.Join(binDir, "gr")
	devBin := filepath.Join(binDir, "gr-dev")

	for _, p := range []string{grBin, devBin} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // G306: must be executable
			t.Fatalf("write %q: %v", p, err)
		}
	}

	t.Setenv("PATH", binDir)

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	origArgs := os.Args

	t.Cleanup(func() { os.Args = origArgs })

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"empty argv", []string{}, grBin},                   // guard: len(os.Args)==0 -> "gr"
		{"empty string", []string{""}, grBin},               // Base("")=="." -> rejected -> "gr"
		{"dot", []string{"."}, grBin},                       // Base(".")=="." -> rejected -> "gr"
		{"separator", []string{"/"}, grBin},                 // Base("/")=="/" -> rejected -> "gr"
		{"bare name", []string{"gr-dev"}, devBin},           // basename looked up on PATH
		{"absolute path", []string{devBin}, devBin},         // directory stripped, basename on PATH
		{"not on PATH", []string{"gr-nae-sic-thing"}, self}, // LookPath miss -> os.Executable()
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.Args = tc.args
			if got := resolveGrBin(); got != tc.want {
				t.Errorf("resolveGrBin() with os.Args=%q = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// TestGrBinReadDir is a regression test for the sandbox read-dir grant. The
// former guard (grBin != "gr") admitted any non-"gr" name — so a bare "gr-dev"
// fallback would grant read on filepath.Dir("gr-dev") == "." (the cwd). The
// grant must apply only to absolute resolved paths.
func TestGrBinReadDir(t *testing.T) {
	cases := []struct {
		name    string
		grBin   string
		wantDir string
		wantOK  bool
	}{
		{"absolute path", "/usr/local/bin/gr", "/usr/local/bin", true},
		{"absolute dev path", "/opt/braw/gr-dev", "/opt/braw", true},
		{"bare gr fallback", "gr", "", false},
		{"bare dev-name fallback", "gr-dev", "", false},
		{"dot", ".", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, ok := grBinReadDir(tc.grBin)
			if ok != tc.wantOK || dir != tc.wantDir {
				t.Errorf("grBinReadDir(%q) = (%q, %v), want (%q, %v)", tc.grBin, dir, ok, tc.wantDir, tc.wantOK)
			}
		})
	}
}

// codexHandler mirrors a Codex hook command handler (HookHandlerConfig::Command).
type codexHandler struct {
	Type           string `toml:"type"`
	Command        string `toml:"command"`
	CommandWindows string `toml:"commandWindows"`
	Timeout        int    `toml:"timeout"`
	StatusMessage  string `toml:"statusMessage"`
}

// codexMatcherGroup mirrors a Codex hook matcher group (MatcherGroup).
type codexMatcherGroup struct {
	Matcher *string        `toml:"matcher"`
	Hooks   []codexHandler `toml:"hooks"`
}

// parseCodexHookOverrides emulates how Codex applies `-c hooks.<Event>=<toml>`
// overrides: each pair sets a dotted TOML key, so joining them into a single
// document and decoding it with the same TOML dialect Codex uses reconstructs
// the effective [hooks] table. It fails the test if the args aren't well-formed
// (odd -c pairing, non-hooks.* key, or TOML that won't parse) — that is the
// "real Codex contract" assertion: the generated overrides must be valid TOML
// matching Codex's HookEventsToml matcher-group schema.
func parseCodexHookOverrides(t *testing.T, extraArgs []string) (map[string][]codexMatcherGroup, bool) {
	t.Helper()

	var (
		docLines []string
		bypass   bool
	)

	for i := 0; i < len(extraArgs); i++ {
		if extraArgs[i] == "--dangerously-bypass-hook-trust" {
			bypass = true
			continue
		}

		if extraArgs[i] != "-c" {
			t.Fatalf("unexpected codex hook arg %q (want -c or --dangerously-bypass-hook-trust)", extraArgs[i])
		}

		i++
		if i >= len(extraArgs) {
			t.Fatal("trailing -c with no value")
		}

		kv := extraArgs[i]
		// injectCodexHooks also emits mcp_servers.* overrides (#1184); this
		// helper only reconstructs the [hooks] table, so skip the rest.
		if !strings.HasPrefix(kv, "hooks.") {
			continue
		}
		// Codex splits on the first '=' only; the value is parsed as TOML.
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			t.Fatalf("codex -c override %q has no '='", kv)
		}

		docLines = append(docLines, kv[:eq]+" = "+kv[eq+1:])
	}

	var doc struct {
		Hooks map[string][]codexMatcherGroup `toml:"hooks"`
	}
	if err := toml.Unmarshal([]byte(strings.Join(docLines, "\n")), &doc); err != nil {
		t.Fatalf("generated codex hook overrides are not valid TOML: %v\n%s", err, strings.Join(docLines, "\n"))
	}

	return doc.Hooks, bypass
}

func TestInjectCodexHooks(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-codex-01"

	extraArgs, extraEnv, err := sm.injectCodexHooks(sessionID, false, sm.Config().Agents["codex"])
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	// Codex no longer reads CODEX_HOOKS_DIR (issue #1183); hooks ride in as args.
	if extraEnv != nil {
		t.Errorf("extraEnv = %v, want nil (CODEX_HOOKS_DIR removed)", extraEnv)
	}

	hooks, bypass := parseCodexHookOverrides(t, extraArgs)

	if !bypass {
		t.Error("codex hook args missing --dangerously-bypass-hook-trust")
	}

	expectedEvents := []string{
		"SessionStart",
		"UserPromptSubmit",
		"PreToolUse",
		"PostToolUse",
		"PermissionRequest",
		"Stop",
	}

	if len(hooks) != len(expectedEvents) {
		t.Errorf("hooks has %d events, want %d: %v", len(hooks), len(expectedEvents), hooks)
	}

	for _, event := range expectedEvents {
		groups, ok := hooks[event]
		if !ok {
			t.Errorf("missing codex hook event %q", event)
			continue
		}

		if len(groups) != 1 {
			t.Errorf("event %q has %d matcher groups, want 1", event, len(groups))
			continue
		}

		if len(groups[0].Hooks) == 0 {
			t.Errorf("event %q has no command handlers", event)
			continue
		}

		for _, h := range groups[0].Hooks {
			if h.Type != "command" {
				t.Errorf("event %q handler type = %q, want command", event, h.Type)
			}

			if h.Command == "" {
				t.Errorf("event %q handler has empty command", event)
			}
		}
	}
}

func TestCodexHookOverrideContent(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	extraArgs, _, err := sm.injectCodexHooks("kirk-codex-02", false, sm.Config().Agents["codex"])
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	hooks, _ := parseCodexHookOverrides(t, extraArgs)

	joined := func(event string) string {
		var b strings.Builder

		for _, g := range hooks[event] {
			for _, h := range g.Hooks {
				b.WriteString(h.Command)
				b.WriteByte('\n')
			}
		}

		return b.String()
	}

	// PermissionRequest bridges to the approval backend.
	if got := joined("PermissionRequest"); !strings.Contains(got, "approve-request") {
		t.Errorf("PermissionRequest command = %q, want approve-request", got)
	}

	// SessionStart reports status and then checks the inbox.
	if got := joined("SessionStart"); !strings.Contains(got, "report-status") || !strings.Contains(got, "check-inbox") {
		t.Errorf("SessionStart commands = %q, want report-status + check-inbox", got)
	}

	// The remaining lifecycle events report status tagged with their event name.
	for event := range map[string]struct{}{"UserPromptSubmit": {}, "PreToolUse": {}, "PostToolUse": {}, "Stop": {}} {
		got := joined(event)
		if !strings.Contains(got, "report-status") {
			t.Errorf("event %q command = %q, want report-status", event, got)
		}

		if !strings.Contains(got, "--event "+event) {
			t.Errorf("event %q command = %q, want --event %s", event, got, event)
		}
	}
}

// TestInjectCodexHooksNoHooksDirEnv is the #1183 regression test: current Codex
// no longer reads CODEX_HOOKS_DIR, so graith must NOT rely on it. Hooks must be
// delivered as `-c hooks.<Event>=` session-config overrides plus the trust
// bypass, and no CODEX_HOOKS_DIR env var may be emitted. This fails against the
// old env-var implementation and passes with the config-override fix.
func TestInjectCodexHooksNoHooksDirEnv(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	extraArgs, extraEnv, err := sm.injectCodexHooks("thrawn-codex-1183", false, sm.Config().Agents["codex"])
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	if _, ok := extraEnv["CODEX_HOOKS_DIR"]; ok {
		t.Error("injectCodexHooks still emits CODEX_HOOKS_DIR — current Codex ignores it (issue #1183)")
	}

	var hasOverride, hasBypass bool

	for i, a := range extraArgs {
		if a == "-c" && i+1 < len(extraArgs) && strings.HasPrefix(extraArgs[i+1], "hooks.") {
			hasOverride = true
		}

		if a == "--dangerously-bypass-hook-trust" {
			hasBypass = true
		}
	}

	if !hasOverride {
		t.Errorf("injectCodexHooks did not emit any -c hooks.* override: %v", extraArgs)
	}

	if !hasBypass {
		t.Errorf("injectCodexHooks did not emit --dangerously-bypass-hook-trust: %v", extraArgs)
	}
}

// TestCodexHookOverrideValue locks the exact inline-TOML the encoder emits for a
// hooks.<Event> override so a format regression is caught, and confirms it
// round-trips back to Codex's matcher-group schema via the shared TOML dialect.
func TestCodexHookOverrideValue(t *testing.T) {
	value := codexHookOverrideValue([]string{"'/bin/gr' report-status --event Stop"})

	const want = `[{hooks=[{type="command",command="'/bin/gr' report-status --event Stop"}]}]`
	if value != want {
		t.Errorf("codexHookOverrideValue = %q, want %q", value, want)
	}

	var doc struct {
		Hooks map[string][]codexMatcherGroup `toml:"hooks"`
	}
	if err := toml.Unmarshal([]byte("hooks.Stop = "+value), &doc); err != nil {
		t.Fatalf("override value is not valid TOML: %v", err)
	}

	groups := doc.Hooks["Stop"]
	if len(groups) != 1 || len(groups[0].Hooks) != 1 {
		t.Fatalf("decoded shape = %+v, want one group with one handler", groups)
	}

	if groups[0].Matcher != nil {
		t.Errorf("matcher = %v, want nil (match-all)", *groups[0].Matcher)
	}

	if h := groups[0].Hooks[0]; h.Type != "command" || h.Command != "'/bin/gr' report-status --event Stop" {
		t.Errorf("handler = %+v, want command handler", h)
	}
}

func TestTOMLBasicString(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`'/usr/bin/gr' check-inbox`, `"'/usr/bin/gr' check-inbox"`},
		{`a"b`, `"a\"b"`},
		{`a\b`, `"a\\b"`},
		{"a\tb", `"a\tb"`},
		{"a\nb", `"a\nb"`},
		{"a\x01b", `"a\u0001b"`},
		{"a\x7fb", `"a\u007Fb"`},
	}
	for _, tc := range cases {
		if got := tomlBasicString(tc.in); got != tc.want {
			t.Errorf("tomlBasicString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCodexMCPServerArgs verifies the helper emits a `-c` override pair per
// server pointing command/args at `gr mcp-proxy <name>`, JSON-encoded so the
// values are valid TOML, and in stable slice order.
func TestCodexMCPServerArgs(t *testing.T) {
	if args, skipped, err := codexMCPServerArgs(nil, (config.Agent{}).MCPServerArgsOrDefault()); err != nil || args != nil || skipped != nil {
		t.Fatalf("codexMCPServerArgs(nil, (config.Agent{}).MCPServerArgsOrDefault()) = (%v, %v, %v), want (nil, nil, nil)", args, skipped, err)
	}

	servers := []config.MCPServerConfig{
		{Name: "graith", Command: "/usr/bin/gr", Args: []string{"mcp"}},
		{Name: "chrome-devtools", Command: "npx", Args: []string{"chrome-mcp"}, Env: map[string]string{"DISPLAY": ":0"}},
	}

	args, skipped, err := codexMCPServerArgs(servers, (config.Agent{}).MCPServerArgsOrDefault())
	if err != nil {
		t.Fatalf("codexMCPServerArgs() error = %v", err)
	}

	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want none for bare-key names", skipped)
	}

	grBin := resolveGrBin()

	cmdVal, err := json.Marshal(grBin)
	if err != nil {
		t.Fatalf("marshal grBin: %v", err)
	}

	want := []string{
		"-c", "mcp_servers.graith.command=" + string(cmdVal),
		"-c", `mcp_servers.graith.args=["mcp-proxy","graith"]`,
		"-c", "mcp_servers.chrome-devtools.command=" + string(cmdVal),
		"-c", `mcp_servers.chrome-devtools.args=["mcp-proxy","chrome-devtools"]`,
	}

	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}

	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

// TestCodexMCPServerArgsSkipsUnrepresentableNames is a regression test for the
// tribunal finding (all three judges): a server name containing a `.` (or any
// non-TOML-bare-key char) breaks Codex's dotted `-c` key path and, unquoted,
// fails Codex config loading — taking the whole session down. Such names must
// be skipped (and reported) rather than emitted, while valid names still pass.
func TestCodexMCPServerArgsSkipsUnrepresentableNames(t *testing.T) {
	servers := []config.MCPServerConfig{
		{Name: "graith", Command: "/usr/bin/gr", Args: []string{"mcp"}},
		{Name: "foo.bar", Command: "npx", Args: []string{"dotted"}},
		{Name: "has space", Command: "npx", Args: []string{"spaced"}},
		{Name: "bad\"quote", Command: "npx", Args: []string{"quoted"}},
		{Name: "under_score-ok", Command: "npx", Args: []string{"fine"}},
	}

	args, skipped, err := codexMCPServerArgs(servers, (config.Agent{}).MCPServerArgsOrDefault())
	if err != nil {
		t.Fatalf("codexMCPServerArgs() error = %v", err)
	}

	wantSkipped := []string{"foo.bar", "has space", `bad"quote`}
	if len(skipped) != len(wantSkipped) {
		t.Fatalf("skipped = %v, want %v", skipped, wantSkipped)
	}

	for i := range wantSkipped {
		if skipped[i] != wantSkipped[i] {
			t.Errorf("skipped[%d] = %q, want %q", i, skipped[i], wantSkipped[i])
		}
	}

	// Only the two representable names emit overrides: 2 servers × 4 args each.
	if len(args) != 8 {
		t.Fatalf("args = %v, want 8 (graith + under_score-ok)", args)
	}

	joined := strings.Join(args, " ")
	for _, bad := range []string{"foo.bar", "has space", `bad"quote`} {
		if strings.Contains(joined, "mcp_servers."+bad) {
			t.Errorf("args unexpectedly contain skipped name %q: %v", bad, args)
		}
	}

	for _, ok := range []string{"graith", "under_score-ok"} {
		if !strings.Contains(joined, "mcp_servers."+ok+".command=") {
			t.Errorf("args missing representable server %q: %v", ok, args)
		}
	}
}

func TestInjectHooksSupported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)

	args, env, err := sm.injectHooks("claude", "kirk-claude", "", false)
	if err != nil {
		t.Fatalf("injectHooks(claude) error = %v", err)
	}

	if len(args) == 0 {
		t.Error("injectHooks(claude) returned no args")
	}

	if env != nil {
		t.Errorf("injectHooks(claude) returned unexpected env: %v", env)
	}

	args, env, err = sm.injectHooks("codex", "kirk-codex", "", false)
	if err != nil {
		t.Fatalf("injectHooks(codex) error = %v", err)
	}

	if len(args) == 0 {
		t.Error("injectHooks(codex) returned no args")
	}

	if env != nil {
		t.Errorf("injectHooks(codex) returned unexpected env: %v", env)
	}

	hooks, bypass := parseCodexHookOverrides(t, args)
	if !bypass {
		t.Error("injectHooks(codex) missing --dangerously-bypass-hook-trust")
	}

	if _, ok := hooks["SessionStart"]; !ok {
		t.Error("injectHooks(codex) missing SessionStart hook override")
	}

	worktree := t.TempDir()

	args, env, err = sm.injectHooks("cursor", "kirk-cursor-sup", worktree, false)
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

// TestInjectHooksCustomAgentAdoptsMechanism is the #1236 opt-in: a custom agent
// (whose name matches no built-in) adopts a supported hook mechanism and its own
// argv spelling from config alone, with the generated file path bound to {path}.
func TestInjectHooksCustomAgentAdoptsMechanism(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sm.cfg.Agents["thrawn"] = config.Agent{
		Command: "thrawn",
		Hooks: &config.AgentHookConfig{
			Mechanism:    config.HookMechanismClaudeSettings,
			SettingsArgs: []string{"--config-file", "{path}"},
		},
	}

	args, _, err := sm.injectHooks("thrawn", "bairn-thrawn", "", false)
	if err != nil {
		t.Fatalf("injectHooks(custom) error = %v", err)
	}

	if len(args) != 2 || args[0] != "--config-file" || !strings.HasSuffix(args[1], "settings.json") {
		t.Errorf("custom agent hook args = %v, want [--config-file <dir>/settings.json]", args)
	}
}

// TestInjectMCPConfigCustomAgentAdoptsMechanism is the #1236 opt-in for MCP: a
// custom agent adopts the codex-style per-server override mechanism with its own
// argv spelling, with the Go-built server values bound to the template.
func TestInjectMCPConfigCustomAgentAdoptsMechanism(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sm.cfg.Agents["thrawn"] = config.Agent{
		Command: "thrawn",
		MCP: &config.AgentMCPConfig{
			Mechanism:  config.MCPMechanismCodexConfig,
			ServerArgs: []string{"--mcp-server", "{mcp_name}"},
		},
	}

	servers := []config.MCPServerConfig{{Name: "graith"}, {Name: "bothy"}}

	args, err := sm.injectMCPConfig("thrawn", "bairn-mcp", servers)
	if err != nil {
		t.Fatalf("injectMCPConfig(custom) error = %v", err)
	}

	if want := []string{"--mcp-server", "graith", "--mcp-server", "bothy"}; !reflect.DeepEqual(args, want) {
		t.Errorf("custom agent mcp args = %v, want %v", args, want)
	}
}

// TestInjectHooksNoMechanismIsNoop confirms an agent that declares no hook
// mechanism (custom or built-in without a hooks block) gets nothing — the
// config-driven replacement for the former name-based default.
func TestInjectHooksNoMechanismIsNoop(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sm.cfg.Agents["thrawn"] = config.Agent{Command: "thrawn"}

	args, env, err := sm.injectHooks("thrawn", "bairn-none", "", false)
	if err != nil || args != nil || env != nil {
		t.Errorf("injectHooks(no mechanism) = (%v, %v, %v), want (nil, nil, nil)", args, env, err)
	}
}

func TestInjectHooksUnsupportedIsNoop(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	for _, agent := range []string{"agy", "opencode", "custom-agent"} {
		args, env, err := sm.injectHooks(agent, "haar-unsupported", "", false)
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

func TestCodexHookCommandsEscapeSingleQuotes(t *testing.T) {
	// Create a fake gr binary in a directory whose name contains a single quote.
	fakeDir := filepath.Join(t.TempDir(), "o'malley", "bin")
	if err := os.MkdirAll(fakeDir, 0o750); err != nil {
		t.Fatalf("create fake dir: %v", err)
	}

	fakeBin := filepath.Join(fakeDir, "gr")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil { //nolint:gosec // G306: script/binary must be executable
		t.Fatalf("write fake binary: %v", err)
	}

	// Override PATH so resolveGrBin finds our fake binary, and simulate being
	// launched as "gr" so it looks up that name (resolveGrBin uses os.Args[0]).
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	origArgs := os.Args

	t.Cleanup(func() { os.Args = origArgs })

	os.Args = []string{fakeBin, "daemon", "start"}

	sm := newTestSessionManagerWithDataDir(t)

	extraArgs, _, err := sm.injectCodexHooks("kirk-codex-quote", false, sm.Config().Agents["codex"])
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	// The command handlers must shell-quote the gr path so the shell Codex runs
	// them under doesn't word-split a path with spaces or mis-handle the quote.
	hooks, _ := parseCodexHookOverrides(t, extraArgs)
	expectedQuoted := shellQuote(fakeBin)

	for _, event := range []string{"PermissionRequest", "SessionStart", "Stop"} {
		for _, g := range hooks[event] {
			for _, h := range g.Hooks {
				if !strings.HasPrefix(h.Command, expectedQuoted+" ") {
					t.Errorf("event %q command %q does not start with quoted path %q", event, h.Command, expectedQuoted)
				}
			}
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

// TestInjectClaudeHooksExcludesMCP proves the #1135 decoupling: injectClaudeHooks
// only ever emits the --settings (hook) arg, never --mcp-config, regardless of
// what MCP servers are configured. MCP config is injectMCPConfig's job now.
func TestInjectClaudeHooksExcludesMCP(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-mcp-inject"

	args, env, err := sm.injectClaudeHooks(sessionID, false, sm.Config().Agents["claude"])
	if err != nil {
		t.Fatalf("injectClaudeHooks() error = %v", err)
	}

	if env != nil {
		t.Errorf("unexpected env: %v", env)
	}

	if len(args) != 2 {
		t.Fatalf("args = %v, want 2 elements [--settings path]", args)
	}

	if args[0] != "--settings" {
		t.Errorf("args[0] = %q, want --settings", args[0])
	}

	for _, a := range args {
		if a == "--mcp-config" {
			t.Errorf("injectClaudeHooks must not emit --mcp-config; args = %v", args)
		}
	}
}

// TestInjectMCPConfig proves MCP config injection is independent of the hook
// path: it produces --mcp-config args for Claude given servers, and a readable
// config file, without touching the settings/hook file (#1135).
func TestInjectMCPConfig(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-mcp-inject"

	servers := []config.MCPServerConfig{
		{Name: "graith", Command: "/usr/bin/gr", Args: []string{"mcp"}},
		{Name: "chrome", Command: "npx", Args: []string{"chrome-mcp"}},
	}

	args, err := sm.injectMCPConfig("claude", sessionID, servers)
	if err != nil {
		t.Fatalf("injectMCPConfig() error = %v", err)
	}

	if len(args) != 2 {
		t.Fatalf("args = %v, want 2 elements [--mcp-config path]", args)
	}

	if args[0] != "--mcp-config" {
		t.Errorf("args[0] = %q, want --mcp-config", args[0])
	}

	data, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatalf("read mcp config at %q: %v", args[1], err)
	}

	if !strings.Contains(string(data), "mcpServers") {
		t.Error("mcp config file should contain mcpServers")
	}
}

// TestInjectMCPConfigNoServers verifies that no servers means no args and no
// generated file — nothing to inject.
func TestInjectMCPConfigNoServers(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	args, err := sm.injectMCPConfig("claude", "kirk-mcp-empty", nil)
	if err != nil {
		t.Fatalf("injectMCPConfig() error = %v", err)
	}

	if args != nil {
		t.Errorf("args = %v, want nil for no servers", args)
	}
}

// TestInjectMCPConfigCodex verifies Codex gets its MCP servers as per-session
// `-c mcp_servers.<name>.…` overrides via injectMCPConfig (issue #1184, now on
// the decoupled MCP path — #1135).
func TestInjectMCPConfigCodex(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	servers := []config.MCPServerConfig{
		{Name: "graith", Command: "/usr/bin/gr", Args: []string{"mcp"}},
		{Name: "chrome", Command: "npx", Args: []string{"chrome-mcp"}},
	}

	args, err := sm.injectMCPConfig("codex", "kirk-codex-mcp", servers)
	if err != nil {
		t.Fatalf("injectMCPConfig(codex) error = %v", err)
	}

	joined := strings.Join(args, " ")
	for _, name := range []string{"graith", "chrome"} {
		if !strings.Contains(joined, "mcp_servers."+name+".command=") {
			t.Errorf("args missing command override for %q: %v", name, args)
		}

		if !strings.Contains(joined, `mcp_servers.`+name+`.args=["mcp-proxy","`+name+`"]`) {
			t.Errorf("args missing args override for %q: %v", name, args)
		}
	}
}

// TestInjectMCPConfigNonClaude verifies agents without an MCP wiring path get no
// args even when servers are configured (Claude uses --mcp-config, Codex uses
// -c overrides; the rest get nothing).
func TestInjectMCPConfigNonClaude(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	servers := []config.MCPServerConfig{
		{Name: "graith", Command: "/usr/bin/gr", Args: []string{"mcp"}},
	}

	for _, agent := range []string{"cursor", "opencode"} {
		args, err := sm.injectMCPConfig(agent, "haar-"+agent, servers)
		if err != nil {
			t.Errorf("injectMCPConfig(%q) error = %v", agent, err)
		}

		if args != nil {
			t.Errorf("injectMCPConfig(%q) = %v, want nil", agent, args)
		}
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
	// Simulate being launched as "gr" so resolveGrBin (which uses os.Args[0])
	// looks up that name and finds the planted binary above.
	origArgs := os.Args

	t.Cleanup(func() { os.Args = origArgs })

	os.Args = []string{fakeBin, "daemon", "start"}

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

	extraArgs, _, err := sm.injectCodexHooks("bonnie-codex-yolo", true, sm.Config().Agents["codex"])
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	hooks, _ := parseCodexHookOverrides(t, extraArgs)

	groups, ok := hooks["PermissionRequest"]
	if !ok {
		t.Fatal("PermissionRequest hook missing for yolo session")
	}

	var found bool

	for _, g := range groups {
		for _, h := range g.Hooks {
			if strings.Contains(h.Command, "approve-request") {
				found = true
			}
		}
	}

	if !found {
		t.Error("yolo PermissionRequest hook missing approve-request command")
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

	extraArgs, _, err := sm.injectCodexHooks("thrawn-codex-no-approve", false, sm.Config().Agents["codex"])
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	hooks, _ := parseCodexHookOverrides(t, extraArgs)

	if _, ok := hooks["PermissionRequest"]; ok {
		t.Error("PermissionRequest hook present when approvals disabled, want omitted")
	}

	// Other lifecycle hooks must still be installed.
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		if _, ok := hooks[event]; !ok {
			t.Errorf("event %q missing when approvals disabled, want present", event)
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
