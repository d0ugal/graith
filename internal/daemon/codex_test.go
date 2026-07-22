package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
)

// TestOptionArgs locks the #1186 fix as re-expressed through config-driven
// option_args (#1236): a codex session's model (and typed options) must be
// turned into real CLI flags by the built-in [agents.codex] option_args groups.
// The regression is the first case — before #1186 a model set on a codex session
// produced no `--model` flag and the session silently ran on Codex's default
// model. The default codex/claude agents are loaded from the embedded config so
// this test also proves default_config.toml carries the right groups.
func TestOptionArgs(t *testing.T) {
	def := config.Default()
	codex := def.Agents["codex"]
	claude := def.Agents["claude"]

	tests := []struct {
		name  string
		agent config.Agent
		model string
		opts  *config.CodexOptions
		want  []string
	}{
		{
			name:  "codex model becomes --model (regression #1186)",
			agent: codex,
			model: "gpt-5.1-codex",
			want:  []string{"--model", "gpt-5.1-codex"},
		},
		{
			name:  "codex with no model or options yields nil",
			agent: codex,
			want:  nil,
		},
		{
			name:  "non-codex agent (claude) has no option_args, so no flags",
			agent: claude,
			model: "opus",
			opts:  &config.CodexOptions{Profile: "braw", WebSearch: true},
			want:  nil,
		},
		{
			name:  "all options in stable order",
			agent: codex,
			model: "gpt-5.1-codex",
			opts: &config.CodexOptions{
				Profile:         "braw",
				ReasoningEffort: "high",
				ServiceTier:     "flex",
				WebSearch:       true,
			},
			want: []string{
				"--model", "gpt-5.1-codex",
				"--profile", "braw",
				"-c", "model_reasoning_effort=high",
				"-c", "service_tier=flex",
				"--search",
			},
		},
		{
			name:  "options without a model omit --model",
			agent: codex,
			opts:  &config.CodexOptions{ReasoningEffort: "low"},
			want:  []string{"-c", "model_reasoning_effort=low"},
		},
		{
			name:  "web search false emits no --search",
			agent: codex,
			opts:  &config.CodexOptions{WebSearch: false, Profile: "canny"},
			want:  []string{"--profile", "canny"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := config.TemplateVars{Model: tt.model}

			got, err := optionArgs(tt.agent, vars, tt.opts)
			if err != nil {
				t.Fatalf("optionArgs: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("optionArgs(model=%q, %+v) = %v, want %v", tt.model, tt.opts, got, tt.want)
			}
		})
	}
}

func TestCodexStatePtr(t *testing.T) {
	if got := codexStatePtr(config.CodexOptions{}); got != nil {
		t.Errorf("codexStatePtr(zero) = %v, want nil", got)
	}

	in := config.CodexOptions{Profile: "braw"}

	got := codexStatePtr(in)
	if got == nil || *got != in {
		t.Fatalf("codexStatePtr(%+v) = %v, want pointer to equal value", in, got)
	}

	// Must be an independent copy, not an alias of the caller's value.
	got.Profile = "thrawn"
	if in.Profile != "braw" {
		t.Error("codexStatePtr aliased the caller's struct")
	}
}

func TestCloneCodexOptions(t *testing.T) {
	if got := cloneCodexOptions(nil); got != nil {
		t.Errorf("cloneCodexOptions(nil) = %v, want nil", got)
	}

	src := &config.CodexOptions{Profile: "braw", WebSearch: true}

	got := cloneCodexOptions(src)
	if got == src {
		t.Fatal("cloneCodexOptions returned the same pointer (aliased)")
	}

	if *got != *src {
		t.Fatalf("cloneCodexOptions(%+v) = %+v, want equal value", *src, *got)
	}

	got.Profile = "thrawn"
	if src.Profile != "braw" {
		t.Error("cloneCodexOptions aliased the source struct's data")
	}
}

func TestCodexOptsFromMsg(t *testing.T) {
	if got := codexOptsFromMsg(nil); !got.IsZero() {
		t.Errorf("codexOptsFromMsg(nil) = %+v, want zero", got)
	}

	in := &protocol.CodexOptions{
		Profile: "canny", ReasoningEffort: "high", ServiceTier: "fast", WebSearch: true,
	}

	want := config.CodexOptions{
		Profile: "canny", ReasoningEffort: "high", ServiceTier: "fast", WebSearch: true,
	}
	if got := codexOptsFromMsg(in); got != want {
		t.Errorf("codexOptsFromMsg(%+v) = %+v, want %+v", in, got, want)
	}
}

// TestCreateRejectsCodexOptionsForNonCodexAgent locks the guard that Codex-only
// options can't be silently dropped against another agent.
func TestCreateRejectsCodexOptionsForNonCodexAgent(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.Create(CreateOpts{
		Name:      "thrawn-mix",
		AgentName: "claude",
		Codex:     config.CodexOptions{Profile: "braw"},
		Rows:      24,
		Cols:      80,
	})
	assertErrContains(t, err, "require --agent codex")
}

// TestCodexOptsForAgent locks the codex-only invariant used by fork/migrate:
// options survive only when the target agent is codex, so a non-codex session
// never persists an orphan codex block.
func TestCodexOptsForAgent(t *testing.T) {
	opts := &config.CodexOptions{Profile: "braw"}

	if got := codexOptsForAgent("codex", opts); got != opts {
		t.Errorf("codexOptsForAgent(codex, …) = %v, want the same options", got)
	}

	if got := codexOptsForAgent("claude", opts); got != nil {
		t.Errorf("codexOptsForAgent(claude, …) = %v, want nil", got)
	}

	if got := codexOptsForAgent("codex", nil); got != nil {
		t.Errorf("codexOptsForAgent(codex, nil) = %v, want nil", got)
	}
}

// TestCloneSessionStateCopiesCodex locks that a session snapshot doesn't alias
// the daemon-owned Codex pointer — mutating the clone must not touch the live
// state (the same discipline as Includes / CI.FailingChecks).
func TestCloneSessionStateCopiesCodex(t *testing.T) {
	live := &SessionState{
		ID:    "braw",
		Codex: &config.CodexOptions{Profile: "canny"},
	}

	clone := cloneSessionState(live)
	if clone.Codex == live.Codex {
		t.Fatal("cloneSessionState aliased the Codex pointer")
	}

	clone.Codex.Profile = "thrawn"
	if live.Codex.Profile != "canny" {
		t.Error("mutating the clone's Codex changed the live session state")
	}
}

// TestCodexModelPassedOnLaunchAndResume is the end-to-end regression lock for
// #1186: a codex session created with a model must have `--model <model>`
// appended to the launched argv — and, crucially, again on resume (restart),
// which is exactly the path that silently dropped the model before the fix.
func TestCodexModelPassedOnLaunchAndResume(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}

	repoDir := initTempGitRepo(t)
	sm, recordPath := newCodexRecorderManager(t, repoDir)

	const model = "gpt-5.1-codex"

	created, err := sm.Create(CreateOpts{
		Name: "canny", AgentName: "codex", RepoPath: repoDir, BaseBranch: "main",
		Model: model, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	id := created.ID

	t.Cleanup(func() { stopAndClosePTY(sm, id) })

	argv := waitForRecordedArgv(t, recordPath, "--model")
	assertContiguousPair(t, argv, "--model", model)

	// Resume (restart) is where the model was dropped before the fix.
	if err := os.Remove(recordPath); err != nil {
		t.Fatalf("remove record before resume: %v", err)
	}

	if err := sm.Stop(id); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	waitForStatus(t, sm, id, StatusStopped)

	if _, err := sm.Restart(id, 24, 80); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	argv = waitForRecordedArgv(t, recordPath, "--model")
	assertContiguousPair(t, argv, "--model", model)
}

// TestCodexMCPProxyEnvAliasesAcrossLifecycle is the daemon-level regression
// for Codex MCP proxies losing the owning session's identity. Codex clears the
// environment of stdio MCP children unless variable names are listed in the
// server's env_vars config, so create, fork, and resume must each inject one
// fresh alias set alongside the command/args overrides.
func TestCodexMCPProxyEnvAliasesAcrossLifecycle(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	t.Setenv("GRAITH_MCP_FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF_TOKEN", "stale-parent-token")
	t.Setenv("GRAITH_MCP_FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF_PROFILE", "stale-parent-profile")
	t.Setenv("GRAITH_MCP_UNRELATED", "stale-parent-context")
	t.Setenv("GRAITH_INHERITED_CONTEXT", "bide-wi-me")

	repoDir := initTempGitRepo(t)
	sm, recordPath := newCodexRecorderManager(t, repoDir)

	created, err := sm.Create(CreateOpts{
		Name: "canny", AgentName: "codex", RepoPath: repoDir, BaseBranch: "main",
		AgentHooks: true, SkipModelValidation: true, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

	createdAliases := waitForCodexMCPProxyEnvOverride(t, sm, created.ID, recordPath)

	if err := os.Remove(recordPath); err != nil {
		t.Fatalf("remove record before fork: %v", err)
	}

	forked, err := sm.Fork("bairn", created.ID, 24, 80)
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, forked.ID) })

	forkedAliases := waitForCodexMCPProxyEnvOverride(t, sm, forked.ID, recordPath)
	if reflect.DeepEqual(createdAliases, forkedAliases) {
		t.Fatal("fork reused the source launch's MCP identity aliases")
	}

	if err := os.Remove(recordPath); err != nil {
		t.Fatalf("remove record before resume: %v", err)
	}

	if err := sm.Stop(created.ID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	waitForStatus(t, sm, created.ID, StatusStopped)

	if _, err := sm.Restart(created.ID, 24, 80); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	resumedAliases := waitForCodexMCPProxyEnvOverride(t, sm, created.ID, recordPath)
	if reflect.DeepEqual(createdAliases, resumedAliases) {
		t.Fatal("restart reused the original launch's MCP identity aliases")
	}

	stateData, err := os.ReadFile(sm.paths.StateFile)
	if err != nil {
		t.Fatalf("read persisted state: %v", err)
	}
	if strings.Contains(string(stateData), mcpProxyIdentityEnvPrefix) {
		t.Fatal("persisted session state contains an MCP identity alias")
	}
}

func waitForCodexMCPProxyEnvOverride(t *testing.T, sm *SessionManager, sessionID, recordPath string) []string {
	t.Helper()

	// Wait on a fixed override emitted after the randomized alias arguments, then
	// inspect the complete recorded argv for the per-launch names.
	argv := waitForRecordedArgv(t, recordPath, `mcp_servers.graith.environment_id="local"`)
	assertContiguousPair(t, argv, "-c", `mcp_servers.graith.environment_id="local"`)

	var aliases []string
	for _, arg := range argv {
		const prefix = "mcp_servers.graith.env_vars="
		if strings.HasPrefix(arg, prefix) {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(arg, prefix)), &aliases); err != nil {
				t.Fatalf("decode MCP identity alias names: %v", err)
			}
			break
		}
	}
	if len(aliases) != 4 {
		t.Fatalf("MCP identity aliases = %v, want exactly four", aliases)
	}

	sm.mu.RLock()
	driver := sm.sessions[sessionID]
	sm.mu.RUnlock()

	ptySession, ok := driver.(*grpty.Session)
	if !ok {
		t.Fatalf("session %q driver = %T, want *pty.Session", sessionID, driver)
	}

	env := make(map[string]string, len(ptySession.Cmd.Env))
	for _, entry := range ptySession.Cmd.Env {
		if name, value, ok := strings.Cut(entry, "="); ok {
			if _, duplicate := env[name]; duplicate {
				t.Fatalf("session %q environment contains duplicate key %q", sessionID, name)
			}
			env[name] = value
		}
	}
	if got := env["GRAITH_INHERITED_CONTEXT"]; got != "bide-wi-me" {
		t.Fatalf("session %q unrelated inherited context = %q, want bide-wi-me", sessionID, got)
	}

	wantValues := []string{sessionID, env["GRAITH_TOKEN"], sm.paths.Profile, sm.paths.SocketPath}
	aliasNameRe := regexp.MustCompile(`^GRAITH_MCP_[A-F0-9]{32}_(SESSION_ID|TOKEN|PROFILE|SOCKET_PATH)$`)
	for i, alias := range aliases {
		if !aliasNameRe.MatchString(alias) {
			t.Fatalf("invalid MCP identity alias name %q", alias)
		}
		if got, ok := env[alias]; !ok || got != wantValues[i] {
			t.Fatalf("session %q MCP identity alias %q is absent or has the wrong value", sessionID, alias)
		}
	}

	var reserved int
	for name := range env {
		if strings.HasPrefix(name, mcpProxyIdentityEnvPrefix) {
			reserved++
		}
	}
	if reserved != 4 {
		t.Fatalf("session %q has %d reserved MCP aliases, want exactly four", sessionID, reserved)
	}

	return aliases
}

// newCodexRecorderManager builds a SessionManager whose "codex" agent is a shell
// script that records its launch argv to recordPath (mirrors newRecorderManager
// but keyed on codex). The option_args groups carry over from the embedded
// default so the model/typed-option adapter still fires (issue #1236); only the
// launch command is swapped for the recorder script.
func newCodexRecorderManager(t *testing.T, repoDir string) (*SessionManager, string) {
	t.Helper()

	dir := t.TempDir()
	recordPath := filepath.Join(dir, "argv.txt")

	// Bound codex's async id-capture scan to an empty dir so it never touches the
	// real ~/.codex.
	t.Setenv("CODEX_HOME", t.TempDir())

	script := `printf '%s\n' "$0" "$@" > "$GRAITH_ARGS_RECORD"; exec cat`

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["codex"] = config.Agent{
		NonInteractiveArgs: []string{},
		Command:            "sh",
		Args:               []string{"-c", script},
		ResumeArgs:         []string{"-c", script},
		ForkArgs:           []string{"-c", script},
		Env:                map[string]string{"GRAITH_ARGS_RECORD": recordPath},
		OptionArgs:         cfg.Agents["codex"].OptionArgs,
		AddDirArgs:         cfg.Agents["codex"].AddDirArgs,
	}
	cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		DataDir:    dir,
		LogDir:     dir,
		RuntimeDir: dir,
		SocketPath: filepath.Join(dir, "graith.sock"),
		TmpDir:     filepath.Join(dir, "tmp"),
	}, slog.Default())
	sm.sandboxResolver = func(string) (bool, error) { return false, nil }

	return sm, recordPath
}
