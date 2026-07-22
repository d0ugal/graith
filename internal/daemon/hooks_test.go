package daemon

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/pelletier/go-toml/v2"
)

func newTestSessionManagerWithDataDir(t *testing.T) *SessionManager {
	t.Helper()
	dir := t.TempDir()

	cfg := config.Default()

	return NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(dir, "state.json"),
		DataDir:   dir,
	}, slog.Default())
}

func TestCommandPolicyHookCommandBlocksSupervisorSpawnFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-gr")
	cmd := exec.Command("sh", "-c", commandPolicyHookCommand(shellQuote(missing)))
	output, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("hook command error = %v, output = %q, want blocking exit 2", err, output)
	}

	if !strings.Contains(string(output), "failed before returning a decision") {
		t.Fatalf("hook command output = %q, want useful fail-closed diagnostic", output)
	}
}

func TestHookGenerationSnapshotsPolicyTimeoutDuringReload(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	var reloads sync.WaitGroup

	reloads.Add(1)

	go func() {
		defer reloads.Done()

		for i := 0; i < 500; i++ {
			sm.mu.Lock()

			next := *sm.cfg
			if i%2 == 0 {
				next.CommandPolicy.Timeout = "1s"
			} else {
				next.CommandPolicy.Timeout = "2s"
			}

			sm.cfg = &next
			sm.mu.Unlock()
		}
	}()

	for i := 0; i < 500; i++ {
		if _, _, err := sm.injectCodexHooks("canny-reload", false, true); err != nil {
			t.Fatal(err)
		}
	}

	reloads.Wait()
}

func TestGenerateClaudeSettings(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-braw-02"

	settingsPath, err := sm.generateClaudeSettings(sessionID, true, true)
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
				Timeout int    `json:"timeout"`
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

		// PreToolUse is restricted to the shell command-policy scope; every
		// other event stays match-all (empty matcher).
		wantMatcher := ""
		if event == "PreToolUse" {
			wantMatcher = "^Bash$"
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
			} else if !strings.Contains(matchers[0].Hooks[0].Command, "command-policy-check") {
				t.Errorf("event %q command = %q, does not contain command-policy-check", event, matchers[0].Hooks[0].Command)
			} else if matchers[0].Hooks[0].Timeout <= 0 || !strings.Contains(matchers[0].Hooks[0].Command, "exit 2") {
				t.Errorf("event %q hook is not bounded/fail-closed: %+v", event, matchers[0].Hooks[0])
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

	extraArgs, extraEnv, err := sm.injectClaudeHooks(sessionID, true, false)
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

	_, err := sm.generateClaudeSettings(sessionID, true, false)
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

	_, _, err := sm.injectCursorHooks(sessionID, worktree, true, false)
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
		// injectCodexHooks may emit other config overrides; this helper only
		// reconstructs the [hooks] table, so skip the rest.
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

	extraArgs, extraEnv, err := sm.injectCodexHooks(sessionID, true, true)
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

		wantGroups := 1
		if event == "PreToolUse" {
			wantGroups = 2 // lifecycle status plus the Bash-only policy gate
		}

		if len(groups) != wantGroups {
			t.Errorf("event %q has %d matcher groups, want %d", event, len(groups), wantGroups)
		}

		for _, group := range groups {
			if len(group.Hooks) == 0 {
				t.Errorf("event %q has a matcher group with no command handlers", event)
			}

			for _, h := range group.Hooks {
				if h.Type != "command" {
					t.Errorf("event %q handler type = %q, want command", event, h.Type)
				}

				if h.Command == "" {
					t.Errorf("event %q handler has empty command", event)
				}
			}
		}
	}
}

func TestCodexHookOverrideContent(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)

	extraArgs, _, err := sm.injectCodexHooks("kirk-codex-02", true, true)
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

	// PreToolUse performs the bounded synchronous command-policy check even when
	// Codex approval policy is "never". The policy group must match Bash only.
	var policyGroup *codexMatcherGroup

	preToolGroups := hooks["PreToolUse"]
	for i := range preToolGroups {
		group := &preToolGroups[i]
		if group.Matcher != nil && *group.Matcher == "^Bash$" {
			policyGroup = group
			break
		}
	}

	if policyGroup == nil || len(policyGroup.Hooks) != 1 || !strings.Contains(policyGroup.Hooks[0].Command, "command-policy-check") {
		t.Errorf("PreToolUse policy group = %+v, want Bash-only command-policy-check", policyGroup)
	} else if policyGroup.Hooks[0].Timeout <= 0 || !strings.Contains(policyGroup.Hooks[0].Command, "exit 2") {
		t.Errorf("PreToolUse policy hook is not bounded/fail-closed: %+v", policyGroup.Hooks[0])
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

	extraArgs, extraEnv, err := sm.injectCodexHooks("thrawn-codex-1183", true, false)
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

func TestInjectHooksSupported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)

	args, env, err := sm.injectHooks("claude", "kirk-claude", "", true, false, sm.Config().CommandPolicy.TimeoutDuration())
	if err != nil {
		t.Fatalf("injectHooks(claude) error = %v", err)
	}

	if len(args) == 0 {
		t.Error("injectHooks(claude) returned no args")
	}

	if env != nil {
		t.Errorf("injectHooks(claude) returned unexpected env: %v", env)
	}

	args, env, err = sm.injectHooks("codex", "kirk-codex", "", true, false, sm.Config().CommandPolicy.TimeoutDuration())
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

	args, env, err = sm.injectHooks("cursor", "kirk-cursor-sup", worktree, true, false, sm.Config().CommandPolicy.TimeoutDuration())
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
		args, env, err := sm.injectHooks(agent, "haar-unsupported", "", true, false, sm.Config().CommandPolicy.TimeoutDuration())
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

	extraArgs, _, err := sm.injectCodexHooks("kirk-codex-quote", true, false)
	if err != nil {
		t.Fatalf("injectCodexHooks() error = %v", err)
	}

	// The command handlers must shell-quote the gr path so the shell Codex runs
	// them under doesn't word-split a path with spaces or mis-handle the quote.
	hooks, _ := parseCodexHookOverrides(t, extraArgs)
	expectedQuoted := shellQuote(fakeBin)

	for _, event := range []string{"PreToolUse", "SessionStart", "Stop"} {
		for _, g := range hooks[event] {
			for _, h := range g.Hooks {
				if !strings.HasPrefix(h.Command, expectedQuoted+" ") {
					t.Errorf("event %q command %q does not start with quoted path %q", event, h.Command, expectedQuoted)
				}
			}
		}
	}
}

func TestInjectClaudeHooksEmitsOnlySettings(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-settings"

	args, env, err := sm.injectClaudeHooks(sessionID, true, false)
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
			t.Errorf("injectClaudeHooks emitted an obsolete integration argument; args = %v", args)
		}
	}
}

func TestInjectCursorHooks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	sessionID := "kirk-cursor-01"
	worktree := t.TempDir()

	extraArgs, extraEnv, err := sm.injectCursorHooks(sessionID, worktree, true, true)
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

	_, _, err := sm.injectCursorHooks(sessionID, worktree, true, true)
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

	if !strings.Contains(content, "command-policy-check") {
		t.Error("cursor hooks missing command-policy-check command")
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

	_, _, err := sm.injectCursorHooks("haar-no-trust", worktree, true, false)
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

	args, env, err := sm.injectCursorHooks("haar-no-worktree", "", true, false)
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

	settingsPath, err := sm.generateClaudeSettings(sessionID, true, false)
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

func TestCommandPolicyHooksAreIndependentAndShellScoped(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		sm := newTestSessionManagerWithDataDir(t)

		path, err := sm.generateClaudeSettings("canny-policy", false, true)
		if err != nil {
			t.Fatal(err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		var parsed struct {
			Hooks map[string][]struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatal(err)
		}

		if len(parsed.Hooks) != 1 || len(parsed.Hooks["PreToolUse"]) != 1 {
			t.Fatalf("hooks = %+v", parsed.Hooks)
		}

		group := parsed.Hooks["PreToolUse"][0]
		if group.Matcher != "^Bash$" || len(group.Hooks) != 1 || !strings.Contains(group.Hooks[0].Command, "command-policy-check") {
			t.Fatalf("PreToolUse policy hook = %+v", group)
		}
	})

	t.Run("codex", func(t *testing.T) {
		sm := newTestSessionManagerWithDataDir(t)

		args, _, err := sm.injectCodexHooks("canny-policy", false, true)
		if err != nil {
			t.Fatal(err)
		}

		hooks, _ := parseCodexHookOverrides(t, args)
		if _, ok := hooks["SessionStart"]; ok {
			t.Fatal("lifecycle hook installed when lifecycle=false")
		}

		groups, ok := hooks["PreToolUse"]
		if !ok || len(groups) != 1 || groups[0].Matcher == nil || *groups[0].Matcher != "^Bash$" ||
			!strings.Contains(groups[0].Hooks[0].Command, "command-policy-check") {
			t.Fatalf("PreToolUse hook = %+v", groups)
		}
	})

	t.Run("cursor", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		sm := newTestSessionManagerWithDataDir(t)

		worktree := t.TempDir()
		if _, _, err := sm.injectCursorHooks("canny-policy", worktree, false, true); err != nil {
			t.Fatal(err)
		}

		data, err := os.ReadFile(filepath.Join(worktree, ".cursor", "hooks.json"))
		if err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(string(data), "preToolUse") || !strings.Contains(string(data), "command-policy-check") {
			t.Fatalf("cursor policy hooks = %s", data)
		}

		if strings.Contains(string(data), "sessionStart") {
			t.Fatal("lifecycle hook installed when lifecycle=false")
		}
	})
}
