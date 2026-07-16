package daemon

import (
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

// TestCreateRejectsInvalidCodexOption locks that an out-of-range enumerated
// option fails session creation with a clear message.
func TestCreateRejectsInvalidCodexOption(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.Create(CreateOpts{
		Name:      "dreich-effort",
		AgentName: "codex",
		Codex:     config.CodexOptions{ReasoningEffort: "haar"},
		Rows:      24,
		Cols:      80,
	})
	assertErrContains(t, err, "invalid codex reasoning effort")
}
