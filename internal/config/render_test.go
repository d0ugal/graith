package config

import (
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/tools"
	"github.com/pelletier/go-toml/v2"
)

func TestEffectiveTOMLRendersConfig(t *testing.T) {
	data, err := EffectiveTOML(Default())
	if err != nil {
		t.Fatal(err)
	}

	if len(data) == 0 {
		t.Fatal("expected non-empty TOML")
	}

	if !strings.Contains(string(data), "[sandbox]") {
		t.Errorf("expected a [sandbox] table in rendered defaults:\n%s", data)
	}
}

func TestEffectiveTOMLMaterializesRuntimeDefaults(t *testing.T) {
	cfg := Default()
	tools.Configure(cfg.Tools.Resolved(cfg.SourceDir))
	t.Cleanup(tools.Reset)

	data, err := EffectiveTOML(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var rendered Config
	if err := toml.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("rendered TOML does not parse: %v", err)
	}

	if got, want := rendered.Remote.MaxPendingPairings, cfg.Remote.MaxPendingPairingsOrDefault(); got != want {
		t.Errorf("remote.max_pending_pairings = %d, runtime accessor = %d", got, want)
	}

	fallback := cfg.Remote.PairFallbackRate()
	if got := rendered.Remote.PairFallbackCount; got != fallback.Count {
		t.Errorf("remote.pair_fallback_count = %d, runtime accessor = %d", got, fallback.Count)
	}

	for _, check := range []struct {
		name      string
		raw       string
		canonical string
		want      time.Duration
	}{
		{"remote.pending_pairing_ttl", rendered.Remote.PendingPairingTTL, "10m", RemotePendingPairingTTLDefault},
		{"remote.pair_fallback_window", rendered.Remote.PairFallbackWindow, "1m", RemotePairFallbackWindowDefault},
		{"approvals.command_timeout", rendered.Approvals.CommandTimeout, "", cfg.Approvals.CommandTimeoutDuration()},
		{"approvals.localmost_timeout", rendered.Approvals.LocalmostTimeout, "", cfg.Approvals.LocalmostTimeoutDuration()},
	} {
		if check.canonical != "" && check.raw != check.canonical {
			t.Errorf("%s rendered as %q, want canonical spelling %q", check.name, check.raw, check.canonical)
		}

		got, err := ParseDurationWithDays(check.raw)
		if err != nil {
			t.Errorf("%s raw rendered value %q is not a duration: %v", check.name, check.raw, err)
			continue
		}

		if got != check.want {
			t.Errorf("%s = %v, runtime accessor = %v", check.name, got, check.want)
		}
	}

	renderedTools := rendered.Tools.Resolved(rendered.SourceDir)
	for _, check := range []struct {
		name, got, want string
	}{
		{"git", renderedTools.Git, tools.Git()},
		{"gh", renderedTools.GH, tools.GH()},
		{"gcx", renderedTools.GCX, tools.GCX()},
		{"shell", renderedTools.Shell, tools.Shell()},
		{"osascript", renderedTools.OSAScript, tools.OSAScript()},
		{"ps", renderedTools.PS, tools.PS()},
		{"lsof", renderedTools.Lsof, tools.Lsof()},
	} {
		if check.got != check.want {
			t.Errorf("tools.%s = %q, runtime accessor = %q", check.name, check.got, check.want)
		}
	}

	if cfg.Remote.MaxPendingPairings != 0 || cfg.Remote.PendingPairingTTL != "" ||
		cfg.Remote.PairFallbackCount != 0 || cfg.Remote.PairFallbackWindow != "" ||
		cfg.Tools != (ToolsConfig{}) || cfg.Approvals.CommandTimeout != "" ||
		cfg.Approvals.LocalmostTimeout != "" {
		t.Fatal("EffectiveTOML mutated its input config")
	}
}

func TestDiffFromDefaultsIgnoresExplicitRuntimeDefaults(t *testing.T) {
	cfg := Default()
	cfg.Remote.MaxPendingPairings = cfg.Remote.MaxPendingPairingsOrDefault()
	cfg.Remote.PendingPairingTTL = "10m"
	fallback := cfg.Remote.PairFallbackRate()
	cfg.Remote.PairFallbackCount = fallback.Count
	cfg.Remote.PairFallbackWindow = "1m"

	toolDefaults := tools.Defaults()
	cfg.Tools = ToolsConfig{
		Git:       toolDefaults.Git,
		GH:        toolDefaults.GH,
		GCX:       toolDefaults.GCX,
		Shell:     toolDefaults.Shell,
		OSAScript: toolDefaults.OSAScript,
		PS:        toolDefaults.PS,
		Lsof:      toolDefaults.Lsof,
	}
	cfg.Approvals.CommandTimeout = cfg.Approvals.CommandTimeoutDuration().String()
	cfg.Approvals.LocalmostTimeout = cfg.Approvals.LocalmostTimeoutDuration().String()

	diff, err := DiffFromDefaults(cfg, "effective")
	if err != nil {
		t.Fatal(err)
	}

	if diff != "" {
		t.Errorf("explicit runtime defaults should not produce a diff:\n%s", diff)
	}
}

func TestDiffFromDefaultsEmptyForDefaults(t *testing.T) {
	diff, err := DiffFromDefaults(Default(), "effective")
	if err != nil {
		t.Fatal(err)
	}

	if diff != "" {
		t.Errorf("expected empty diff for the default config, got:\n%s", diff)
	}
}

func TestDiffFromDefaultsShowsCustomisation(t *testing.T) {
	cfg := Default()
	cfg.Sandbox.Enabled = !cfg.Sandbox.Enabled

	diff, err := DiffFromDefaults(cfg, "loch")
	if err != nil {
		t.Fatal(err)
	}

	if diff == "" {
		t.Fatal("expected a non-empty diff after customising the config")
	}

	if !strings.Contains(diff, "enabled") {
		t.Errorf("diff should mention the changed sandbox.enabled key:\n%s", diff)
	}

	// The "to" label appears in the unified-diff header.
	if !strings.Contains(diff, "loch") {
		t.Errorf("diff header should carry the toLabel %q:\n%s", "loch", diff)
	}
}

func TestRedactSecretsMasksEnvValues(t *testing.T) {
	const (
		envVal       = "braw-fixture-val-42"
		nestedEnvVal = "dreich-nested-val-73"
	)

	cfg := Default()
	cfg.MCPServers = []MCPServerConfig{{
		Name: "blether",
		Env:  map[string]string{"GITHUB_TOKEN": envVal},
	}}
	cfg.Agents = map[string]Agent{
		"canny": {
			Command: "claude",
			Env:     map[string]string{"ANTHROPIC_API_KEY": envVal},
			MCPServers: map[string]MCPServerConfig{
				"croft": {Command: "npx", Env: map[string]string{"CROFT_TOKEN": nestedEnvVal}},
			},
		},
	}

	red := RedactSecrets(cfg)

	if got := red.MCPServers[0].Env["GITHUB_TOKEN"]; got != RedactedMask {
		t.Errorf("MCP env value not masked: got %q, want %q", got, RedactedMask)
	}

	if got := red.Agents["canny"].Env["ANTHROPIC_API_KEY"]; got != RedactedMask {
		t.Errorf("agent env value not masked: got %q, want %q", got, RedactedMask)
	}

	if got := red.Agents["canny"].MCPServers["croft"].Env["CROFT_TOKEN"]; got != RedactedMask {
		t.Errorf("nested MCP env value not masked: got %q, want %q", got, RedactedMask)
	}

	// The original config must be untouched (redaction works on a copy).
	if got := cfg.MCPServers[0].Env["GITHUB_TOKEN"]; got != envVal {
		t.Errorf("original MCP env mutated: got %q, want %q", got, envVal)
	}

	if got := cfg.Agents["canny"].Env["ANTHROPIC_API_KEY"]; got != envVal {
		t.Errorf("original agent env mutated: got %q, want %q", got, envVal)
	}

	if got := cfg.Agents["canny"].MCPServers["croft"].Env["CROFT_TOKEN"]; got != nestedEnvVal {
		t.Errorf("original nested MCP env mutated: got %q, want %q", got, nestedEnvVal)
	}

	// A rendered redacted config must not contain the value anywhere.
	data, err := EffectiveTOML(red)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(data), envVal) {
		t.Errorf("redacted TOML still leaked the value:\n%s", data)
	}

	if strings.Contains(string(data), nestedEnvVal) {
		t.Errorf("redacted TOML still leaked the nested value:\n%s", data)
	}
}

func TestRedactSecretsHandlesEmpty(t *testing.T) {
	// A config with no MCP servers / agent env maps must round-trip without
	// panicking and without inventing entries.
	cfg := Default()
	cfg.MCPServers = nil

	red := RedactSecrets(cfg)
	if len(red.MCPServers) != 0 {
		t.Errorf("expected no MCP servers, got %d", len(red.MCPServers))
	}
}
