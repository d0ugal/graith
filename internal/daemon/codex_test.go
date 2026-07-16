package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// TestCodexExtraArgs locks the #1186 fix: a codex session's model (and typed
// options) must be turned into real CLI flags. The regression is the first case
// — before the fix, a model set on a codex session produced no `--model` flag
// and the session silently ran on Codex's default model.
func TestCodexExtraArgs(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		model string
		opts  *config.CodexOptions
		want  []string
	}{
		{
			name:  "codex model becomes --model (regression #1186)",
			agent: "codex",
			model: "gpt-5.1-codex",
			want:  []string{"--model", "gpt-5.1-codex"},
		},
		{
			name:  "codex with no model or options yields nil",
			agent: "codex",
			want:  nil,
		},
		{
			name:  "non-codex agent never gets flags",
			agent: "claude",
			model: "opus",
			opts:  &config.CodexOptions{Profile: "braw", WebSearch: true},
			want:  nil,
		},
		{
			name:  "all options in stable order",
			agent: "codex",
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
			agent: "codex",
			opts:  &config.CodexOptions{ReasoningEffort: "low"},
			want:  []string{"-c", "model_reasoning_effort=low"},
		},
		{
			name:  "web search false emits no --search",
			agent: "codex",
			opts:  &config.CodexOptions{WebSearch: false, Profile: "canny"},
			want:  []string{"--profile", "canny"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codexExtraArgs(tt.agent, tt.model, tt.opts)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("codexExtraArgs(%q, %q, %+v) = %v, want %v", tt.agent, tt.model, tt.opts, got, tt.want)
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

// newCodexRecorderManager builds a SessionManager whose "codex" agent is a shell
// script that records its launch argv to recordPath (mirrors newRecorderManager
// but keyed on codex, since codexExtraArgs only fires for that agent).
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
		Command:    "sh",
		Args:       []string{"-c", script},
		ResumeArgs: []string{"-c", script},
		ForkArgs:   []string{"-c", script},
		Env:        map[string]string{"GRAITH_ARGS_RECORD": recordPath},
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
