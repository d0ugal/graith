package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
	known := []string{"model", "dir", "profile", "reasoning_effort", "service_tier", "approval_policy", "web_search", "worktree_path"}
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
// a group with no args, a `when` gate naming an unknown variable, and a `when`
// gate on `dir` (a valid template variable that is never bound at the option_args
// expansion site, so the group would silently never fire) are all rejected.
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
			wantSubst: "not a variable bound when option_args expand",
		},
		{
			name:      "when dir rejected (never bound at option_args expansion)",
			opt:       AgentOptionArg{When: "dir", Args: []string{"--flag"}},
			wantSubst: "not a variable bound when option_args expand",
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

	if len(orig.Agents["codex"].OptionArgs) == 0 {
		t.Fatal("expected the embedded codex agent to define option_args")
	}

	// The #1236 hook/MCP/prompt/empty-id adapters must also survive the render
	// path so `gr config show`/`diff` don't silently drop them.
	if !reflect.DeepEqual(got.Agents["claude"].Hooks, orig.Agents["claude"].Hooks) {
		t.Errorf("claude hooks did not round-trip: %+v", got.Agents["claude"].Hooks)
	}

	if !reflect.DeepEqual(got.Agents["claude"].MCP, orig.Agents["claude"].MCP) {
		t.Errorf("claude mcp did not round-trip: %+v", got.Agents["claude"].MCP)
	}

	if !reflect.DeepEqual(got.Agents["codex"].Hooks, orig.Agents["codex"].Hooks) {
		t.Errorf("codex hooks did not round-trip: %+v", got.Agents["codex"].Hooks)
	}

	if !reflect.DeepEqual(got.Agents["codex"].MCP, orig.Agents["codex"].MCP) {
		t.Errorf("codex mcp did not round-trip: %+v", got.Agents["codex"].MCP)
	}

	if !reflect.DeepEqual(got.Agents["codex"].PromptInjectionArgs, orig.Agents["codex"].PromptInjectionArgs) {
		t.Errorf("codex prompt_injection_args did not round-trip: %v", got.Agents["codex"].PromptInjectionArgs)
	}

	if !reflect.DeepEqual(got.Agents["codex"].EmptyIDResumeArgs, orig.Agents["codex"].EmptyIDResumeArgs) {
		t.Errorf("codex empty_id_resume_args did not round-trip: %v", got.Agents["codex"].EmptyIDResumeArgs)
	}
}

// TestValidateAgentAdapters covers the #1236 config-load guards: unknown hook/MCP
// mechanisms and argv templates using an unsupported placeholder are rejected.
func TestValidateAgentAdapters(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(a *Agent)
		wantSubst string
	}{
		{
			name:      "base args context rejects add-dir placeholder",
			mutate:    func(a *Agent) { a.Args = []string{"--dir", "{dir}"} },
			wantSubst: "agents.codex.args",
		},
		{
			name:      "resume args unknown placeholder rejected",
			mutate:    func(a *Agent) { a.ResumeArgs = []string{"--resume", "{bogus}"} },
			wantSubst: "agents.codex.resume_args",
		},
		{
			name:      "fork args unknown placeholder rejected",
			mutate:    func(a *Agent) { a.ForkArgs = []string{"fork", "{bogus}"} },
			wantSubst: "agents.codex.fork_args",
		},
		{
			name:      "empty-id resume args unknown placeholder rejected",
			mutate:    func(a *Agent) { a.EmptyIDResumeArgs = []string{"resume", "{bogus}"} },
			wantSubst: "agents.codex.empty_id_resume_args",
		},
		{
			name:      "option args unknown placeholder rejected",
			mutate:    func(a *Agent) { a.OptionArgs = []AgentOptionArg{{Args: []string{"--model", "{bogus}"}}} },
			wantSubst: "agents.codex.option_args[0].args",
		},
		{
			name:      "option args context rejects add-dir placeholder",
			mutate:    func(a *Agent) { a.OptionArgs = []AgentOptionArg{{Args: []string{"--dir", "{dir}"}}} },
			wantSubst: "agents.codex.option_args[0].args",
		},
		{
			name:      "add-dir args unknown placeholder rejected",
			mutate:    func(a *Agent) { a.AddDirArgs = []string{"--add-dir", "{bogus}"} },
			wantSubst: "agents.codex.add_dir_args",
		},
		{
			name:      "add-dir args context rejects option placeholder",
			mutate:    func(a *Agent) { a.AddDirArgs = []string{"--profile", "{profile}"} },
			wantSubst: "agents.codex.add_dir_args",
		},
		{
			name:      "headless args unknown placeholder rejected",
			mutate:    func(a *Agent) { a.HeadlessArgs = []string{"--protocol", "{bogus}"} },
			wantSubst: "agents.codex.headless_args",
		},
		{
			name:      "unknown hook mechanism rejected",
			mutate:    func(a *Agent) { a.Hooks = &AgentHookConfig{Mechanism: "telepathy"} },
			wantSubst: "hooks.mechanism",
		},
		{
			name:      "unknown mcp mechanism rejected",
			mutate:    func(a *Agent) { a.MCP = &AgentMCPConfig{Mechanism: "smoke-signal"} },
			wantSubst: "mcp.mechanism",
		},
		{
			name: "hook settings_args unsupported placeholder rejected",
			mutate: func(a *Agent) {
				a.Hooks = &AgentHookConfig{Mechanism: HookMechanismClaudeSettings, SettingsArgs: []string{"--settings", "{bogus}"}}
			},
			wantSubst: "unsupported template variable",
		},
		{
			name: "codex server_args unsupported placeholder rejected",
			mutate: func(a *Agent) {
				a.MCP = &AgentMCPConfig{Mechanism: MCPMechanismCodexConfig, ServerArgs: []string{"-c", "mcp_servers.{model}.command={mcp_command}"}}
			},
			wantSubst: "unsupported template variable",
		},
		{
			name:      "prompt_injection_args unsupported placeholder rejected",
			mutate:    func(a *Agent) { a.PromptInjectionArgs = []string{"--sys", "{dir}"} },
			wantSubst: "unsupported template variable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			braw := cfg.Agents["codex"]
			tt.mutate(&braw)
			cfg.Agents["codex"] = braw

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantSubst) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantSubst)
			}
		})
	}
}

// A wrapper can speak graith's verified stream-json control contract without
// needing any extra CLI prefix. Headless capability must not imply non-empty
// headless_args; the wrapper's base args may be sufficient.
func TestValidateHeadlessCapableAllowsEmptyHeadlessArgs(t *testing.T) {
	cfg := Default()
	enabled := true
	cfg.Agents["bothy-wrapper"] = Agent{
		Command:         "bothy-wrapper",
		HeadlessCapable: &enabled,
		HeadlessArgs:    []string{},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with empty headless_args = %v, want nil", err)
	}
}

func TestLoadRejectsBadLaunchTemplatePlaceholders(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantField string
	}{
		{
			name: "option args",
			body: `
[agents.bothy]
command = "bothy"
[[agents.bothy.option_args]]
args = ["--model", "{bogus}"]
`,
			wantField: "agents.bothy.option_args[0].args",
		},
		{
			name: "add dir args",
			body: `
[agents.bothy]
command = "bothy"
add_dir_args = ["--add-dir", "{bogus}"]
`,
			wantField: "agents.bothy.add_dir_args",
		},
		{
			name: "headless args",
			body: `
[agents.bothy]
command = "bothy"
headless_capable = true
headless_args = ["--stream", "{bogus}"]
`,
			wantField: "agents.bothy.headless_args",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantField) {
				t.Fatalf("Load() = %v, want error containing %q", err, tt.wantField)
			}
		})
	}
}

// TestValidateAgentAdaptersAcceptsSupportedPlaceholders confirms the built-in
// templates (and their allowed placeholders) pass validation.
func TestValidateAgentAdaptersAcceptsSupportedPlaceholders(t *testing.T) {
	cfg := Default()
	braw := cfg.Agents["codex"]
	braw.Hooks = &AgentHookConfig{
		Mechanism: HookMechanismCodexConfig,
		EventArgs: []string{"-c", "hooks.{hook_event}={hook_value}"},
		TrustArgs: []string{"--dangerously-bypass-hook-trust"},
	}
	braw.MCP = &AgentMCPConfig{
		Mechanism:  MCPMechanismCodexConfig,
		ServerArgs: []string{"-c", "mcp_servers.{mcp_name}.command={mcp_command}", "-c", "mcp_servers.{mcp_name}.args={mcp_args}"},
	}
	cfg.Agents["codex"] = braw

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with supported placeholders = %v, want nil", err)
	}
}

// TestExpandSliceWith covers the adapter-argv expander: known {tokens} are
// substituted, a substituted value that itself contains braces is not
// re-expanded, and an unknown token errors.
func TestExpandSliceWith(t *testing.T) {
	got, err := ExpandSliceWith([]string{"-c", "hooks.{event}={value}"}, map[string]string{
		"event": "SessionStart",
		"value": `[{hooks=[{command="{gr} report"}]}]`,
	})
	if err != nil {
		t.Fatalf("ExpandSliceWith: %v", err)
	}

	want := []string{"-c", `hooks.SessionStart=[{hooks=[{command="{gr} report"}]}]`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExpandSliceWith = %v, want %v (a braces-bearing value must not be re-expanded)", got, want)
	}

	if _, err := ExpandSliceWith([]string{"{nope}"}, map[string]string{"event": "x"}); err == nil {
		t.Error("ExpandSliceWith with unknown token = nil error, want error")
	}

	if got, _ := ExpandSliceWith(nil, nil); got != nil {
		t.Errorf("ExpandSliceWith(nil) = %v, want nil", got)
	}
}

// TestAgentAdapterAccessorsFallBack confirms an agent that selects a mechanism
// without spelling out the argv template still gets the built-in default (the
// #1237 fail-safe pattern), so a custom agent need only set the mechanism.
func TestAgentAdapterAccessorsFallBack(t *testing.T) {
	a := Agent{Hooks: &AgentHookConfig{Mechanism: HookMechanismClaudeSettings}}
	if got, want := a.HookSettingsArgsOrDefault(), []string{"--settings", "{path}"}; !reflect.DeepEqual(got, want) {
		t.Errorf("HookSettingsArgsOrDefault fallback = %v, want %v", got, want)
	}

	c := Agent{MCP: &AgentMCPConfig{Mechanism: MCPMechanismCodexConfig}}
	if got := c.MCPServerArgsOrDefault(); len(got) != 4 || got[0] != "-c" {
		t.Errorf("MCPServerArgsOrDefault fallback = %v, want the built-in codex template", got)
	}

	// An explicit empty TrustArgs opts out; only nil falls back.
	optOut := Agent{Hooks: &AgentHookConfig{Mechanism: HookMechanismCodexConfig, TrustArgs: []string{}}}
	if got := optOut.HookTrustArgsOrDefault(); len(got) != 0 {
		t.Errorf("HookTrustArgsOrDefault with explicit empty = %v, want empty (opt-out honoured)", got)
	}
}

// TestConsumesOptionVar covers the capability check that lets a custom alias
// consume typed options (#1236): an agent consumes a typed-option variable when
// any option_args group gates on it (When) or expands it ({var}); an agent with
// no matching group does not.
func TestConsumesOptionVar(t *testing.T) {
	agent := Agent{OptionArgs: []AgentOptionArg{
		{When: "reasoning_effort", Args: []string{"-c", "model_reasoning_effort={reasoning_effort}"}},
		{Args: []string{"--profile", "{profile}"}}, // expands profile without gating on it
	}}

	for _, v := range []string{"reasoning_effort", "profile"} {
		if !agent.ConsumesOptionVar(v) {
			t.Errorf("ConsumesOptionVar(%q) = false, want true", v)
		}
	}

	for _, v := range []string{"service_tier", "web_search", "approval_policy"} {
		if agent.ConsumesOptionVar(v) {
			t.Errorf("ConsumesOptionVar(%q) = true, want false", v)
		}
	}

	if (Agent{}).ConsumesOptionVar("profile") {
		t.Error("an agent with no option_args consumes nothing")
	}
}

// TestCodexOptionsSetVars locks the set-option enumeration used to validate typed
// options against an agent's capability (#1236).
func TestCodexOptionsSetVars(t *testing.T) {
	got := CodexOptions{
		Profile:         "braw",
		ReasoningEffort: "high",
		ServiceTier:     "flex",
		WebSearch:       true,
		ApprovalPolicy:  "never",
	}.SetVars()

	want := []string{"profile", "reasoning_effort", "service_tier", "web_search", "approval_policy"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SetVars() = %v, want %v", got, want)
	}

	if got := (CodexOptions{}).SetVars(); got != nil {
		t.Errorf("SetVars() on zero options = %v, want nil", got)
	}
}

// TestUnsupportedOptionVars covers the reject/keep decision (#1236): the codex
// agent (declaring every typed-option group) supports all of them; a plain claude
// agent (no option_args) supports none; a partial alias supports only what it
// declares.
func TestUnsupportedOptionVars(t *testing.T) {
	def := Default()
	opts := CodexOptions{Profile: "braw", ReasoningEffort: "high"}

	if got := def.Agents["codex"].UnsupportedOptionVars(opts); len(got) != 0 {
		t.Errorf("codex UnsupportedOptionVars = %v, want none", got)
	}

	if got := def.Agents["claude"].UnsupportedOptionVars(opts); !reflect.DeepEqual(got, []string{"profile", "reasoning_effort"}) {
		t.Errorf("claude UnsupportedOptionVars = %v, want [profile reasoning_effort]", got)
	}

	alias := Agent{OptionArgs: []AgentOptionArg{
		{When: "reasoning_effort", Args: []string{"-c", "x={reasoning_effort}"}},
	}}
	if got := alias.UnsupportedOptionVars(opts); !reflect.DeepEqual(got, []string{"profile"}) {
		t.Errorf("alias UnsupportedOptionVars = %v, want [profile]", got)
	}
}
