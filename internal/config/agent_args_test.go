package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// TestAgentAddDirArgsFor covers the config-driven add-directory adapter (#1236):
// {dir} is expanded once per directory, an agent with no add_dir_args emits
// nothing (its CLI has no such flag), and empty directory entries are skipped.
func TestAgentAddDirArgsFor(t *testing.T) {
	withFlag := Agent{AddDirArgs: []string{"--add-dir", "{dir}"}}

	tests := []struct {
		name string
		a    Agent
		dirs []string
		want []string
	}{
		{
			name: "no add_dir_args yields nil even with dirs",
			a:    Agent{},
			dirs: []string{"/glen/bothy/bairn"},
			want: nil,
		},
		{
			name: "no dirs yields nil",
			a:    withFlag,
			dirs: nil,
			want: nil,
		},
		{
			name: "single dir expands {dir}",
			a:    withFlag,
			dirs: []string{"/glen/bothy/bairn"},
			want: []string{"--add-dir", "/glen/bothy/bairn"},
		},
		{
			name: "multiple dirs preserve order",
			a:    withFlag,
			dirs: []string{"/glen/bothy/bairn", "/glen/bothy/whin"},
			want: []string{"--add-dir", "/glen/bothy/bairn", "--add-dir", "/glen/bothy/whin"},
		},
		{
			name: "empty dir entries are skipped",
			a:    withFlag,
			dirs: []string{"", "/glen/bothy/bairn", ""},
			want: []string{"--add-dir", "/glen/bothy/bairn"},
		},
		{
			name: "all-empty dirs yield nil not empty slice",
			a:    withFlag,
			dirs: []string{"", ""},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.a.AddDirArgsFor(TemplateVars{}, tt.dirs)
			if err != nil {
				t.Fatalf("AddDirArgsFor: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AddDirArgsFor(%v) = %v, want %v", tt.dirs, got, tt.want)
			}
		})
	}
}

// TestAgentOptionArgsFor covers the conditional option-arg adapter (#1236): a
// group fires only when its `when` variable resolves non-empty, an empty `when`
// fires unconditionally, a boolean (web_search) gates on "true"/"", and groups
// preserve declaration order.
func TestAgentOptionArgsFor(t *testing.T) {
	agent := Agent{OptionArgs: []AgentOptionArg{
		{When: "model", Args: []string{"--model", "{model}"}},
		{When: "reasoning_effort", Args: []string{"-c", "model_reasoning_effort={reasoning_effort}"}},
		{When: "web_search", Args: []string{"--search"}},
		{When: "", Args: []string{"--always"}},
	}}

	tests := []struct {
		name string
		vars TemplateVars
		want []string
	}{
		{
			name: "only the unconditional group fires when nothing is set",
			vars: TemplateVars{},
			want: []string{"--always"},
		},
		{
			name: "model gate fires and expands {model}",
			vars: TemplateVars{Model: "gpt-5.1-codex"},
			want: []string{"--model", "gpt-5.1-codex", "--always"},
		},
		{
			name: "boolean web_search fires only when true",
			vars: TemplateVars{WebSearch: true},
			want: []string{"--search", "--always"},
		},
		{
			name: "all gates in declaration order",
			vars: TemplateVars{Model: "m", ReasoningEffort: "high", WebSearch: true},
			want: []string{"--model", "m", "-c", "model_reasoning_effort=high", "--search", "--always"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := agent.OptionArgsFor(tt.vars)
			if err != nil {
				t.Fatalf("OptionArgsFor: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("OptionArgsFor(%+v) = %v, want %v", tt.vars, got, tt.want)
			}
		})
	}
}

func TestAgentOptionArgsForNoGroupsIsNil(t *testing.T) {
	got, err := (Agent{}).OptionArgsFor(TemplateVars{Model: "m"})
	if err != nil {
		t.Fatalf("OptionArgsFor: %v", err)
	}

	if got != nil {
		t.Errorf("OptionArgsFor with no groups = %v, want nil", got)
	}
}

// TestAgentOptionArgsForUnknownVarErrors ensures a group args template that
// references an undefined variable fails loudly rather than silently emitting a
// literal placeholder.
func TestAgentOptionArgsForUnknownVarErrors(t *testing.T) {
	agent := Agent{OptionArgs: []AgentOptionArg{
		{When: "model", Args: []string{"--model", "{bogus}"}},
	}}

	_, err := agent.OptionArgsFor(TemplateVars{Model: "m"})
	if err == nil {
		t.Fatal("OptionArgsFor with unknown template var = nil error, want error")
	}
}

func TestIsTemplateVar(t *testing.T) {
	known := []string{"model", "dir", "profile", "reasoning_effort", "service_tier", "web_search", "worktree_path"}
	for _, v := range known {
		if !IsTemplateVar(v) {
			t.Errorf("IsTemplateVar(%q) = false, want true", v)
		}
	}

	for _, v := range []string{"reasoning", "bogus", "", "Model"} {
		if IsTemplateVar(v) {
			t.Errorf("IsTemplateVar(%q) = true, want false", v)
		}
	}
}

// TestValidateOptionArgs covers the config-load guards for option_args (#1236):
// a group with no args and a `when` gate naming an unknown variable are both
// rejected.
func TestValidateOptionArgs(t *testing.T) {
	tests := []struct {
		name      string
		opt       AgentOptionArg
		wantSubst string
	}{
		{
			name:      "empty args rejected",
			opt:       AgentOptionArg{When: "model", Args: nil},
			wantSubst: "args must not be empty",
		},
		{
			name:      "unknown when rejected",
			opt:       AgentOptionArg{When: "reasoning", Args: []string{"-c", "x"}},
			wantSubst: "not a known template variable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			braw := cfg.Agents["codex"]
			braw.OptionArgs = []AgentOptionArg{tt.opt}
			cfg.Agents["codex"] = braw

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantSubst) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantSubst)
			}
		})
	}
}

// TestValidateOptionArgsEmptyWhenAllowed confirms an empty `when` (emit
// unconditionally) passes validation.
func TestValidateOptionArgsEmptyWhenAllowed(t *testing.T) {
	cfg := Default()
	braw := cfg.Agents["codex"]
	braw.OptionArgs = []AgentOptionArg{{When: "", Args: []string{"--always"}}}
	cfg.Agents["codex"] = braw

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with empty when = %v, want nil", err)
	}
}

func TestValidateAgentInfoCommands(t *testing.T) {
	tests := map[string]struct {
		info      AgentInfoCommands
		wantSubst string
	}{
		"empty key rejected": {
			info:      AgentInfoCommands{"": AgentInfoCommand{Args: []string{"--version"}}},
			wantSubst: "key must not be empty",
		},
		"whitespace key rejected": {
			info:      AgentInfoCommands{" model ": AgentInfoCommand{Args: []string{"--list-models"}}},
			wantSubst: "key must not have leading or trailing whitespace",
		},
		"empty args rejected": {
			info:      AgentInfoCommands{"version": AgentInfoCommand{}},
			wantSubst: "args must not be empty",
		},
		"unknown format rejected": {
			info:      AgentInfoCommands{"model": AgentInfoCommand{Args: []string{"--models"}, Format: "jsonpath"}},
			wantSubst: "must be one of",
		},
		"bad cache ttl rejected": {
			info:      AgentInfoCommands{"model": AgentInfoCommand{Args: []string{"--models"}, CacheTTL: "dreich"}},
			wantSubst: "cache_ttl",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			braw := cfg.Agents["codex"]
			braw.Info = test.info
			cfg.Agents["codex"] = braw

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.wantSubst) {
				t.Fatalf("Validate() = %v, want error containing %q", err, test.wantSubst)
			}
		})
	}
}

func TestAgentInfoCacheTTL(t *testing.T) {
	if got := (AgentInfoConfig{}).CacheTTLDuration(); got != AgentInfoCacheTTLDefault {
		t.Fatalf("empty cache TTL = %v, want %v", got, AgentInfoCacheTTLDefault)
	}

	if got := (AgentInfoConfig{CacheTTL: "0"}).CacheTTLDuration(); got != 0 {
		t.Fatalf("zero cache TTL = %v, want disabled", got)
	}

	if got := (AgentInfoConfig{CacheTTL: "15m"}).CacheTTLDuration(); got != 15*time.Minute {
		t.Fatalf("custom cache TTL = %v, want 15m", got)
	}
}

// TestDefaultAgentArgsRoundTrip proves the new adapter fields survive a
// marshal→unmarshal cycle — the path `gr config show`/`diff` take — so the
// codex option_args array-of-tables and the add_dir_args/headless_args slices
// are not silently dropped from a rendered config (#1236).
func TestDefaultAgentArgsRoundTrip(t *testing.T) {
	orig := Default()

	blob, err := toml.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal Default(): %v", err)
	}

	var got Config
	if err := toml.Unmarshal(blob, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(got.Agents["claude"].AddDirArgs, orig.Agents["claude"].AddDirArgs) {
		t.Errorf("claude add_dir_args did not round-trip: %v", got.Agents["claude"].AddDirArgs)
	}

	if !reflect.DeepEqual(got.Agents["claude"].HeadlessArgs, orig.Agents["claude"].HeadlessArgs) {
		t.Errorf("claude headless_args did not round-trip: %v", got.Agents["claude"].HeadlessArgs)
	}

	if !reflect.DeepEqual(got.Agents["codex"].OptionArgs, orig.Agents["codex"].OptionArgs) {
		t.Errorf("codex option_args did not round-trip: %v", got.Agents["codex"].OptionArgs)
	}

	for _, agentName := range []string{"claude", "codex", "cursor"} {
		gotInfo, err := got.Agents[agentName].Info.Commands()
		if err != nil {
			t.Fatalf("%s round-tripped info commands: %v", agentName, err)
		}

		wantInfo, err := orig.Agents[agentName].Info.Commands()
		if err != nil {
			t.Fatalf("%s default info commands: %v", agentName, err)
		}

		if !reflect.DeepEqual(gotInfo, wantInfo) {
			t.Errorf("%s info commands did not round-trip: %v", agentName, gotInfo)
		}
	}

	if len(orig.Agents["codex"].OptionArgs) == 0 {
		t.Fatal("expected the embedded codex agent to define option_args")
	}

	gotCursorModel, ok, err := orig.Agents["cursor"].Info.Command("model")
	if err != nil || !ok {
		t.Fatalf("cursor model info command missing: ok=%v err=%v", ok, err)
	}

	if got := gotCursorModel; !reflect.DeepEqual(got.Args, []string{"--list-models"}) || got.Format != AgentInfoFormatModelList {
		t.Fatalf("cursor model info command = %+v, want args [--list-models] with model_list format", got)
	}

	for _, agentName := range []string{"claude", "codex"} {
		got, ok, err := orig.Agents[agentName].Info.Command("version")
		if err != nil || !ok {
			t.Fatalf("%s version info command missing: ok=%v err=%v", agentName, ok, err)
		}

		if !reflect.DeepEqual(got.Args, []string{"--version"}) {
			t.Fatalf("%s version info command = %v, want [--version]", agentName, got.Args)
		}
	}
}

func TestAgentInfoConfigParsingAndMerge(t *testing.T) {
	var cfg Config
	if err := toml.Unmarshal([]byte(`
[agents.cursor]
command = "agent"

[agents.cursor.info]
model = ["--list-models"]
version = ["-v"]

[agents.cursor.info.catalog]
args = ["models"]
format = "lines"
cache_ttl = "10m"
`), &cfg); err != nil {
		t.Fatalf("unmarshal agent info config: %v", err)
	}

	version, ok, err := cfg.Agents["cursor"].Info.Command("version")
	if err != nil || !ok {
		t.Fatalf("parsed cursor version missing: ok=%v err=%v", ok, err)
	}

	if got := version.Args; !reflect.DeepEqual(got, []string{"-v"}) {
		t.Fatalf("parsed cursor version info = %v, want [-v]", got)
	}

	catalog, ok, err := cfg.Agents["cursor"].Info.Command("catalog")
	if err != nil || !ok {
		t.Fatalf("parsed cursor catalog missing: ok=%v err=%v", ok, err)
	}

	if !reflect.DeepEqual(catalog.Args, []string{"models"}) || catalog.Format != AgentInfoFormatLines || catalog.CacheTTL != "10m" {
		t.Fatalf("parsed cursor catalog info = %+v, want table form", catalog)
	}

	merged := mergeAgent(Agent{Info: AgentInfoCommands{"model": AgentInfoCommand{Args: []string{"--old"}}}}, Agent{
		Info: AgentInfoCommands{"version": AgentInfoCommand{Args: []string{"-v"}}},
	})

	if _, ok := merged.Info["model"]; ok {
		t.Fatalf("user info map should replace default info map, got %v", merged.Info)
	}

	mergedVersion, ok, err := merged.Info.Command("version")
	if err != nil || !ok {
		t.Fatalf("merged version info missing: ok=%v err=%v", ok, err)
	}

	if got := mergedVersion.Args; !reflect.DeepEqual(got, []string{"-v"}) {
		t.Fatalf("merged version info = %v, want [-v]", got)
	}
}

// TestDefaultAgentsKeepNativeSafeguards is the regression guard for issue #1467
// and the wider dangerous-default hardening. Every bundled agent must default to
// an empty non_interactive_args so it keeps its own approval TUI (and, for
// Codex, its own sandbox) out of the box. Enabling unattended mode — which
// disables those native safeguards — is now an explicit user opt-in, so graith
// never disables an agent's controls without an operator-established boundary.
func TestDefaultAgentsKeepNativeSafeguards(t *testing.T) {
	t.Parallel()

	cfg := Default()

	// Flags that surrender an agent's native approval and/or sandbox controls.
	// None may appear in a bundled default (issue #1467 called out the Codex
	// three explicitly; the rest are the sibling unattended flags).
	dangerous := []string{
		"--dangerously-skip-permissions",
		"--ask-for-approval",
		"--sandbox",
		"danger-full-access",
		"--dangerously-bypass-approvals-and-sandbox",
		"--force",
		"--auto",
	}

	for _, name := range []string{"claude", "codex", "cursor", "agy", "opencode"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			agent, ok := cfg.Agents[name]
			if !ok {
				t.Fatalf("default agent %q is missing", name)
			}

			if len(agent.NonInteractiveArgs) != 0 {
				t.Fatalf("agent %q non_interactive_args = %v, want empty (native safeguards preserved)", name, agent.NonInteractiveArgs)
			}

			for _, got := range agent.NonInteractiveArgs {
				for _, bad := range dangerous {
					if got == bad {
						t.Fatalf("agent %q bundled default surrenders native controls via %q", name, bad)
					}
				}
			}
		})
	}
}

// TestDefaultSandboxOffByDefault guards the config-only posture change: graith
// does not assume a sandbox backend (nono/safehouse) is installed, so the OS
// sandbox is opt-in. Combined with the native-safeguard defaults above, an
// out-of-the-box session relies on the agent's own controls until the operator
// enables a backend.
func TestDefaultSandboxOffByDefault(t *testing.T) {
	t.Parallel()

	cfg := Default()

	if cfg.Sandbox.Enabled {
		t.Fatalf("Default().Sandbox.Enabled = true, want false (backend must not be assumed)")
	}

	if cfg.Sandbox.Backend != "" {
		t.Fatalf("Default().Sandbox.Backend = %q, want empty (no assumed backend)", cfg.Sandbox.Backend)
	}
}
