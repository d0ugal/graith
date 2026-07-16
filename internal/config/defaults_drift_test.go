package config

import (
	"os"
	"path/filepath"
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

	t.Run("notifications", func(t *testing.T) {
		if d.Notifications.Backend != "macos" {
			t.Errorf("Default().Notifications.Backend = %q, want %q", d.Notifications.Backend, "macos")
		}

		if d.Notifications.MaxPerHour != DefaultNotifyMaxPerHour {
			t.Errorf("Default().Notifications.MaxPerHour = %d, want %d", d.Notifications.MaxPerHour, DefaultNotifyMaxPerHour)
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
}
