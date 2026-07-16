package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestEmbeddedDefaultsCarryPolicyValues is the drift guard for issue #1237: every
// user-facing policy default must live in the embedded default_config.toml, not
// only as a Go fallback literal. Each check asserts the RAW field parsed from the
// embedded TOML (not just the accessor), so a default that is silently dropped
// from the TOML — leaving only the Go fallback — fails here. That keeps
// `gr config show`, `gr config diff`, and `gr config reset` describing the full
// effective configuration.
func TestEmbeddedDefaultsCarryPolicyValues(t *testing.T) {
	d := Default()

	t.Run("status ttl", func(t *testing.T) {
		if d.Status.TTL != "5m" {
			t.Errorf("Default().Status.TTL = %q, want %q (missing from default_config.toml?)", d.Status.TTL, "5m")
		}

		if got := d.Status.TTLDuration(); got != 5*time.Minute {
			t.Errorf("Default().Status.TTLDuration() = %v, want 5m", got)
		}
	})

	t.Run("todo", func(t *testing.T) {
		if d.Todo.EmitEvents != TodoEmitScenario {
			t.Errorf("Default().Todo.EmitEvents = %q, want %q", d.Todo.EmitEvents, TodoEmitScenario)
		}

		if d.Todo.ClaimLease != "30m" {
			t.Errorf("Default().Todo.ClaimLease = %q, want %q", d.Todo.ClaimLease, "30m")
		}

		if got := d.Todo.ClaimLeaseDuration(); got != DefaultTodoClaimLease {
			t.Errorf("Default().Todo.ClaimLeaseDuration() = %v, want %v", got, DefaultTodoClaimLease)
		}

		if d.Todo.Retention != "0" {
			t.Errorf("Default().Todo.Retention = %q, want %q", d.Todo.Retention, "0")
		}

		if got := d.Todo.RetentionDuration(); got != 0 {
			t.Errorf("Default().Todo.RetentionDuration() = %v, want 0 (keep forever)", got)
		}
	})

	t.Run("messages limits", func(t *testing.T) {
		m := d.Messages
		if m.ConversationPageSize != MessagesConversationPageSizeDefault {
			t.Errorf("Default().Messages.ConversationPageSize = %d, want %d", m.ConversationPageSize, MessagesConversationPageSizeDefault)
		}

		if m.ConversationMaxLimit != MessagesConversationMaxLimitDefault {
			t.Errorf("Default().Messages.ConversationMaxLimit = %d, want %d", m.ConversationMaxLimit, MessagesConversationMaxLimitDefault)
		}

		if m.JailListLimit != MessagesJailListLimitDefault {
			t.Errorf("Default().Messages.JailListLimit = %d, want %d", m.JailListLimit, MessagesJailListLimitDefault)
		}

		if m.SubscriberBuffer != MessagesSubscriberBufferDefault {
			t.Errorf("Default().Messages.SubscriberBuffer = %d, want %d", m.SubscriberBuffer, MessagesSubscriberBufferDefault)
		}

		if m.BusyTimeout != "5s" {
			t.Errorf("Default().Messages.BusyTimeout = %q, want %q", m.BusyTimeout, "5s")
		}

		if got := m.BusyTimeoutDuration(); got != MessagesBusyTimeoutDefault {
			t.Errorf("Default().Messages.BusyTimeoutDuration() = %v, want %v", got, MessagesBusyTimeoutDefault)
		}
	})

	t.Run("todo limits", func(t *testing.T) {
		tc := d.Todo
		if tc.MaxTitle != TodoMaxTitleDefault {
			t.Errorf("Default().Todo.MaxTitle = %d, want %d", tc.MaxTitle, TodoMaxTitleDefault)
		}

		if tc.MaxNote != TodoMaxNoteDefault {
			t.Errorf("Default().Todo.MaxNote = %d, want %d", tc.MaxNote, TodoMaxNoteDefault)
		}

		if tc.ListLimit != TodoListLimitDefault {
			t.Errorf("Default().Todo.ListLimit = %d, want %d", tc.ListLimit, TodoListLimitDefault)
		}

		if tc.SweepInterval != "1m" {
			t.Errorf("Default().Todo.SweepInterval = %q, want %q", tc.SweepInterval, "1m")
		}

		if got := tc.SweepIntervalDuration(); got != TodoSweepIntervalDefault {
			t.Errorf("Default().Todo.SweepIntervalDuration() = %v, want %v", got, TodoSweepIntervalDefault)
		}

		if tc.BusyTimeout != "5s" {
			t.Errorf("Default().Todo.BusyTimeout = %q, want %q", tc.BusyTimeout, "5s")
		}

		if got := tc.BusyTimeoutDuration(); got != TodoBusyTimeoutDefault {
			t.Errorf("Default().Todo.BusyTimeoutDuration() = %v, want %v", got, TodoBusyTimeoutDefault)
		}
	})

	t.Run("notifications", func(t *testing.T) {
		if d.Notifications.Backend != "macos" {
			t.Errorf("Default().Notifications.Backend = %q, want %q", d.Notifications.Backend, "macos")
		}

		if d.Notifications.MaxPerHour != DefaultNotifyMaxPerHour {
			t.Errorf("Default().Notifications.MaxPerHour = %d, want %d", d.Notifications.MaxPerHour, DefaultNotifyMaxPerHour)
		}

		tm := d.Notifications.Timing
		if tm.CoalesceWindow != "30s" {
			t.Errorf("Default().Notifications.Timing.CoalesceWindow = %q, want %q", tm.CoalesceWindow, "30s")
		}

		if tm.DispatchTimeout != "15s" {
			t.Errorf("Default().Notifications.Timing.DispatchTimeout = %q, want %q", tm.DispatchTimeout, "15s")
		}
	})

	t.Run("config reload debounce", func(t *testing.T) {
		if d.ConfigReload.ReloadDebounce != "200ms" {
			t.Errorf("Default().ConfigReload.ReloadDebounce = %q, want %q", d.ConfigReload.ReloadDebounce, "200ms")
		}

		if got := d.ConfigReload.ReloadDebounceDuration(); got != ConfigReloadDebounceDefault {
			t.Errorf("Default().ConfigReload.ReloadDebounceDuration() = %v, want %v", got, ConfigReloadDebounceDefault)
		}
	})

	t.Run("terminal presentation", func(t *testing.T) {
		term := d.Terminal
		if term.RefreshInterval != "2s" {
			t.Errorf("Default().Terminal.RefreshInterval = %q, want %q", term.RefreshInterval, "2s")
		}

		if term.SummaryWidth != TerminalSummaryWidth {
			t.Errorf("Default().Terminal.SummaryWidth = %d, want %d", term.SummaryWidth, TerminalSummaryWidth)
		}

		if got := term.RefreshIntervalDuration(); got != TerminalRefreshIntervalDefault {
			t.Errorf("Default().Terminal.RefreshIntervalDuration() = %v, want %v", got, TerminalRefreshIntervalDefault)
		}
	})
}

// TestEmbeddedDefaultAgentPolicies guards that the built-in agents carry their
// prompt-delivery, workspace-trust, and idle-timeout policy defaults explicitly
// in the embedded TOML rather than relying on the Go fallbacks, so the effective
// values are visible in `gr config show` (issue #1237).
func TestEmbeddedDefaultAgentPolicies(t *testing.T) {
	d := Default()

	wantInjection := map[string]string{
		"claude":   PromptInjectionAppendSystemPrompt,
		"codex":    PromptInjectionDeveloperInstructions,
		"cursor":   PromptInjectionCursorRules,
		"opencode": PromptInjectionNone,
		"agy":      PromptInjectionNone,
	}

	for name, wantMethod := range wantInjection {
		agent, ok := d.Agents[name]
		if !ok {
			t.Errorf("Default().Agents[%q] missing", name)
			continue
		}

		if agent.IdleTimeout != "1h" {
			t.Errorf("agent %q: IdleTimeout = %q, want %q", name, agent.IdleTimeout, "1h")
		}

		if got := agent.IdleTimeoutDuration(); got != time.Hour {
			t.Errorf("agent %q: IdleTimeoutDuration() = %v, want 1h", name, got)
		}

		if agent.InjectPrompt == nil || !*agent.InjectPrompt {
			t.Errorf("agent %q: InjectPrompt = %v, want explicit true", name, agent.InjectPrompt)
		}

		if agent.PreTrustWorkspace == nil || !*agent.PreTrustWorkspace {
			t.Errorf("agent %q: PreTrustWorkspace = %v, want explicit true", name, agent.PreTrustWorkspace)
		}

		if agent.PromptInjection != wantMethod {
			t.Errorf("agent %q: PromptInjection = %q, want %q", name, agent.PromptInjection, wantMethod)
		}
	}
}

// TestEmbeddedDefaultsCarryAgentAdapters is the drift guard for issue #1236: the
// agent-specific CLI adapters (add-dir flag, headless prefix, and the conditional
// codex option groups) must live in the embedded default_config.toml, not as
// hard-coded Go. Each check asserts the RAW field parsed from the embedded TOML,
// so a default silently dropped from the file fails here.
func TestEmbeddedDefaultsCarryAgentAdapters(t *testing.T) {
	d := Default()

	t.Run("add_dir_args on claude/codex/cursor only", func(t *testing.T) {
		want := []string{"--add-dir", "{dir}"}
		for _, name := range []string{"claude", "codex", "cursor"} {
			if got := d.Agents[name].AddDirArgs; !reflect.DeepEqual(got, want) {
				t.Errorf("agent %q: AddDirArgs = %v, want %v", name, got, want)
			}
		}

		for _, name := range []string{"opencode", "agy"} {
			if got := d.Agents[name].AddDirArgs; len(got) != 0 {
				t.Errorf("agent %q: AddDirArgs = %v, want none", name, got)
			}
		}
	})

	t.Run("claude headless_args control channel", func(t *testing.T) {
		want := []string{
			"-p",
			"--output-format", "stream-json",
			"--input-format", "stream-json",
			"--verbose",
			"--permission-prompt-tool", "stdio",
		}
		if got := d.Agents["claude"].HeadlessArgs; !reflect.DeepEqual(got, want) {
			t.Errorf("claude HeadlessArgs = %v, want %v", got, want)
		}
	})

	t.Run("codex option_args cover the #1186 adapter", func(t *testing.T) {
		want := []AgentOptionArg{
			{When: "model", Args: []string{"--model", "{model}"}},
			{When: "profile", Args: []string{"--profile", "{profile}"}},
			{When: "reasoning_effort", Args: []string{"-c", "model_reasoning_effort={reasoning_effort}"}},
			{When: "service_tier", Args: []string{"-c", "service_tier={service_tier}"}},
			{When: "web_search", Args: []string{"--search"}},
			{When: "approval_policy", Args: []string{"--ask-for-approval", "{approval_policy}"}},
		}
		if got := d.Agents["codex"].OptionArgs; !reflect.DeepEqual(got, want) {
			t.Errorf("codex OptionArgs = %v, want %v", got, want)
		}
	})
}

// TestConfigReloadDebounceDuration exercises the accessor's empty/invalid/valid
// paths, mirroring the fail-safe pattern of the other duration accessors.
func TestConfigReloadDebounceDuration(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"empty uses default", "", ConfigReloadDebounceDefault},
		{"unparseable uses default", "haar", ConfigReloadDebounceDefault},
		{"zero uses default", "0", ConfigReloadDebounceDefault},
		{"negative-ish invalid uses default", "-5s", ConfigReloadDebounceDefault},
		{"explicit value honoured", "500ms", 500 * time.Millisecond},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (ConfigReload{ReloadDebounce: c.in}).ReloadDebounceDuration(); got != c.want {
				t.Errorf("ReloadDebounceDuration(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestValidateRejectsBadStatusTTL confirms an unparseable [status] ttl fails at
// load rather than silently falling back to the 5m accessor default.
func TestValidateRejectsBadStatusTTL(t *testing.T) {
	cfg := Default()
	cfg.Status.TTL = "dreich"

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected Validate() to reject an unparseable status.ttl")
	}
}

// TestValidateRejectsBadReloadDebounce confirms an unparseable or non-positive
// [config] reload_debounce fails at load (a zero/negative debounce would
// busy-loop the config watcher).
func TestValidateRejectsBadReloadDebounce(t *testing.T) {
	for _, bad := range []string{"blether", "0", "-1s"} {
		cfg := Default()
		cfg.ConfigReload.ReloadDebounce = bad

		if err := cfg.Validate(); err == nil {
			t.Errorf("expected Validate() to reject reload_debounce = %q", bad)
		}
	}
}

// TestLoadPreservesPartialConfigMerge confirms a partial user config keeps the
// embedded policy defaults it does not mention, and overrides only what it sets
// (issue #1237 acceptance: preserve compatibility for partial config files).
func TestLoadPreservesPartialConfigMerge(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// A user config that touches only the status TTL and one agent's inject flag.
	toml := `
[status]
ttl = "10m"

[agents.claude]
inject_prompt = false
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Status.TTLDuration(); got != 10*time.Minute {
		t.Errorf("status ttl = %v, want 10m (user override)", got)
	}

	// Unmentioned embedded defaults survive the merge.
	if got := cfg.Todo.ClaimLeaseDuration(); got != DefaultTodoClaimLease {
		t.Errorf("todo claim_lease = %v, want %v (default preserved)", got, DefaultTodoClaimLease)
	}

	if got := cfg.ConfigReload.ReloadDebounceDuration(); got != ConfigReloadDebounceDefault {
		t.Errorf("reload_debounce = %v, want %v (default preserved)", got, ConfigReloadDebounceDefault)
	}

	claude := cfg.Agents["claude"]
	if claude.PromptInjectionEnabled() {
		t.Error("claude inject_prompt override to false did not take effect")
	}

	// The agent's other embedded policy defaults survive the partial override.
	if claude.PromptInjection != PromptInjectionAppendSystemPrompt {
		t.Errorf("claude prompt_injection = %q, want %q (default preserved)", claude.PromptInjection, PromptInjectionAppendSystemPrompt)
	}

	if got := claude.IdleTimeoutDuration(); got != time.Hour {
		t.Errorf("claude idle_timeout = %v, want 1h (default preserved)", got)
	}

	// The embedded agent adapters (#1236) survive a partial override that never
	// mentions them, so an existing config keeps the add-dir / codex-option
	// behaviour without re-declaring it.
	if got := claude.AddDirArgs; !reflect.DeepEqual(got, []string{"--add-dir", "{dir}"}) {
		t.Errorf("claude add_dir_args = %v, want default preserved", got)
	}

	if got := cfg.Agents["codex"].OptionArgs; len(got) == 0 {
		t.Error("codex option_args dropped by partial-config merge, want default preserved")
	}
}
