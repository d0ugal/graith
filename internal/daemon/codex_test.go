package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/d0ugal/graith/internal/config"
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
				ApprovalPolicy:  "never",
			},
			want: []string{
				"--model", "gpt-5.1-codex",
				"--profile", "braw",
				"-c", "model_reasoning_effort=high",
				"-c", "service_tier=flex",
				"--search",
				"--ask-for-approval", "never",
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

	in := &config.CodexOptions{ApprovalPolicy: "never"}
	if got := codexOptsFromMsg(in); got != *in {
		t.Errorf("codexOptsFromMsg(%+v) = %+v, want %+v", in, got, *in)
	}
}

// TestCreateRejectsCodexOptionsForNonCodexAgent locks the guard that a typed
// option can't be silently dropped against an agent whose option_args don't
// declare it — claude declares none, so a profile is rejected (issue #1236).
func TestCreateRejectsCodexOptionsForNonCodexAgent(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.Create(CreateOpts{
		Name:      "thrawn-mix",
		AgentName: "claude",
		Codex:     config.CodexOptions{Profile: "braw"},
		Rows:      24,
		Cols:      80,
	})
	assertErrContains(t, err, "does not support option")
	// The error must name the unsupported variable so a value is never silently
	// dropped (issue #1236).
	assertErrContains(t, err, "profile")
}

// TestOptionsForAgent locks the capability-driven filter used by fork/migrate:
// an option survives only when the target agent's option_args declare it, so a
// non-codex target never persists an orphan option block while a codex (or a
// custom alias that declares the same groups) keeps them.
func TestOptionsForAgent(t *testing.T) {
	codex := config.Default().Agents["codex"]
	claude := config.Default().Agents["claude"]
	opts := &config.CodexOptions{Profile: "braw"}

	if got := optionsForAgent(codex, opts); got == nil || got.Profile != "braw" {
		t.Errorf("optionsForAgent(codex, …) = %v, want profile preserved", got)
	}

	if got := optionsForAgent(claude, opts); got != nil {
		t.Errorf("optionsForAgent(claude, …) = %v, want nil (claude declares no option_args)", got)
	}

	if got := optionsForAgent(codex, nil); got != nil {
		t.Errorf("optionsForAgent(codex, nil) = %v, want nil", got)
	}

	// A custom alias that declares only the reasoning_effort group keeps that
	// option and drops the profile it can't consume.
	alias := config.Agent{OptionArgs: []config.AgentOptionArg{
		{When: "reasoning_effort", Args: []string{"-c", "model_reasoning_effort={reasoning_effort}"}},
	}}

	got := optionsForAgent(alias, &config.CodexOptions{Profile: "braw", ReasoningEffort: "high"})
	if got == nil || got.ReasoningEffort != "high" || got.Profile != "" {
		t.Errorf("optionsForAgent(alias, …) = %v, want only reasoning_effort kept", got)
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

// newCodexRecorderManager builds a SessionManager whose "codex" agent is a shell
// script that records its launch argv to recordPath (mirrors newRecorderManager
// but keyed on codex). The option_args groups carry over from the embedded
// default so the model/typed-option adapter still fires (issue #1236); only the
// launch command is swapped for the recorder script.
func newCodexRecorderManager(t *testing.T, repoDir string) (*SessionManager, string) {
	t.Helper()

	return newAliasRecorderManager(t, repoDir, "codex")
}

// newAliasRecorderManager builds a SessionManager whose agent named agentName is
// a recorder script that carries codex's typed-option option_args groups. When
// agentName is not "codex" this proves a custom alias — not the literal name —
// consumes the declared typed options (issue #1236).
func newAliasRecorderManager(t *testing.T, repoDir, agentName string) (*SessionManager, string) {
	t.Helper()

	dir := t.TempDir()
	recordPath := filepath.Join(dir, "argv.txt")

	// Bound codex's async id-capture scan to an empty dir so it never touches the
	// real ~/.codex.
	t.Setenv("CODEX_HOME", t.TempDir())

	script := `printf '%s\n' "$0" "$@" > "$GRAITH_ARGS_RECORD"; exec cat`

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents[agentName] = config.Agent{
		Command:    "sh",
		Args:       []string{"-c", script},
		ResumeArgs: []string{"-c", script},
		ForkArgs:   []string{"-c", script},
		Env:        map[string]string{"GRAITH_ARGS_RECORD": recordPath},
		OptionArgs: cfg.Agents["codex"].OptionArgs,
		AddDirArgs: cfg.Agents["codex"].AddDirArgs,
	}
	cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		DataDir:    dir,
		LogDir:     dir,
		RuntimeDir: dir,
		TmpDir:     filepath.Join(dir, "tmp"),
	}, slog.Default())

	return sm, recordPath
}

// TestCustomAliasTypedOptionsAcrossLifecycle is the end-to-end regression for
// issue #1236: a custom agent alias (not named "codex") that declares the typed
// option_args groups must have its profile/reasoning-effort options accepted at
// create and turned into real flags on create, resume, AND fork — the paths where
// a name-based guard would otherwise reject or silently drop them.
func TestCustomAliasTypedOptionsAcrossLifecycle(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}

	repoDir := initTempGitRepo(t)
	sm, recordPath := newAliasRecorderManager(t, repoDir, "wrapx")

	created, err := sm.Create(CreateOpts{
		Name: "canny", AgentName: "wrapx", RepoPath: repoDir, BaseBranch: "main",
		Model: "gpt-5.1-codex",
		Codex: config.CodexOptions{Profile: "braw", ReasoningEffort: "high"},
		Rows:  24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	id := created.ID

	t.Cleanup(func() { stopAndClosePTY(sm, id) })

	assertAliasOptions := func() {
		t.Helper()

		argv := waitForRecordedArgv(t, recordPath, "--profile")
		assertContiguousPair(t, argv, "--profile", "braw")
		assertContiguousPair(t, argv, "-c", "model_reasoning_effort=high")
	}

	assertAliasOptions()

	// Resume (restart) must replay the persisted typed options.
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

	assertAliasOptions()

	// A same-agent fork must carry the source's typed options through.
	if err := os.Remove(recordPath); err != nil {
		t.Fatalf("remove record before fork: %v", err)
	}

	forked, err := sm.Fork("bairn", id, 24, 80)
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, forked.ID) })

	assertAliasOptions()
}
