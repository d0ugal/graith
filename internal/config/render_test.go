package config

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/d0ugal/graith/internal/tools"
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

func TestEffectiveTOMLResolvesPairingDefaults(t *testing.T) {
	cfg := Default()
	cfg.Remote.MaxPendingPairings = 0
	cfg.Remote.PendingPairingTTL = ""
	cfg.Remote.PairFallbackCount = 0
	cfg.Remote.PairFallbackWindow = ""

	data, err := EffectiveTOML(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var rendered Config
	if err := toml.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("rendered TOML does not parse: %v", err)
	}

	if rendered.Remote.MaxPendingPairings != RemoteMaxPendingPairingsDefault ||
		rendered.Remote.PendingPairingTTL != "10m" ||
		rendered.Remote.PairFallbackCount != RemotePairFallbackCountDefault ||
		rendered.Remote.PairFallbackWindow != "1m" {
		t.Errorf("rendered pairing policy still contains sentinels: %+v", rendered.Remote)
	}

	if cfg.Remote.MaxPendingPairings != 0 || cfg.Remote.PendingPairingTTL != "" ||
		cfg.Remote.PairFallbackCount != 0 || cfg.Remote.PairFallbackWindow != "" {
		t.Fatal("EffectiveTOML mutated its input")
	}
}

// TestEffectiveTOMLRendersToolAndApprovalDefaults is the #1311 render-vs-accessor
// regression: a fresh config leaves [tools] executables and the approval backend
// timeouts empty, but runtime resolution supplies git/gh/sh/... and 5s. The
// rendered "effective" config must therefore materialize exactly what the runtime
// accessors return, so config show / the GUI viewer reflect what actually runs.
func TestEffectiveTOMLRendersToolAndApprovalDefaults(t *testing.T) {
	cfg := Default()

	// The daemon Configures the process resolver from the same config at startup;
	// pin it here so the assertions compare render output against the live runtime
	// accessors rather than re-deriving the defaults independently.
	tools.Configure(cfg.Tools.Resolved())
	t.Cleanup(tools.Reset)

	data, err := EffectiveTOML(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var rendered Config
	if err := toml.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("rendered TOML does not parse: %v", err)
	}

	toolChecks := []struct {
		name     string
		rendered string
		accessor string
	}{
		{"git", rendered.Tools.Git, tools.Git()},
		{"gh", rendered.Tools.GH, tools.GH()},
		{"shell", rendered.Tools.Shell, tools.Shell()},
		{"osascript", rendered.Tools.OSAScript, tools.OSAScript()},
		{"ps", rendered.Tools.PS, tools.PS()},
		{"lsof", rendered.Tools.Lsof, tools.Lsof()},
	}
	for _, c := range toolChecks {
		if c.rendered == "" {
			t.Errorf("tools.%s rendered empty; effective output must show the runtime default", c.name)
		}

		if c.rendered != c.accessor {
			t.Errorf("tools.%s rendered %q, runtime accessor returns %q", c.name, c.rendered, c.accessor)
		}
	}

	// The approval backend timeouts must round-trip to the same duration the
	// runtime accessors resolve (5s), not the empty-string zero.
	if got, want := rendered.Approvals.CommandTimeoutDuration(), cfg.Approvals.CommandTimeoutDuration(); got != want {
		t.Errorf("rendered approvals.command_timeout = %v, runtime accessor = %v", got, want)
	}

	if rendered.Approvals.CommandTimeout == "" {
		t.Error("approvals.command_timeout rendered empty; effective output must show the 5s default")
	}

	if got, want := rendered.Approvals.LocalmostTimeoutDuration(), cfg.Approvals.LocalmostTimeoutDuration(); got != want {
		t.Errorf("rendered approvals.localmost_timeout = %v, runtime accessor = %v", got, want)
	}

	if rendered.Approvals.LocalmostTimeout == "" {
		t.Error("approvals.localmost_timeout rendered empty; effective output must show the 5s default")
	}

	// EffectiveTOML must not mutate its input.
	if cfg.Tools.Git != "" || cfg.Approvals.CommandTimeout != "" || cfg.Approvals.LocalmostTimeout != "" {
		t.Fatal("EffectiveTOML mutated its input config")
	}
}

func TestDiffFromDefaultsTreatsPairingSentinelsAsEffectiveDefaults(t *testing.T) {
	cfg := Default()
	cfg.Remote.MaxPendingPairings = 0
	cfg.Remote.PendingPairingTTL = ""
	cfg.Remote.PairFallbackCount = 0
	cfg.Remote.PairFallbackWindow = ""

	diff, err := DiffFromDefaults(cfg, "effective")
	if err != nil {
		t.Fatal(err)
	}

	if diff != "" {
		t.Errorf("effective sentinels should match defaults, got diff:\n%s", diff)
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
	const envVal = "braw-fixture-val-42"

	cfg := Default()
	cfg.MCPServers = []MCPServerConfig{{
		Name: "blether",
		Env:  map[string]string{"GITHUB_TOKEN": envVal},
	}}
	cfg.Agents = map[string]Agent{
		"canny": {Command: "claude", Env: map[string]string{"ANTHROPIC_API_KEY": envVal}},
	}

	red := RedactSecrets(cfg)

	if got := red.MCPServers[0].Env["GITHUB_TOKEN"]; got != RedactedMask {
		t.Errorf("MCP env value not masked: got %q, want %q", got, RedactedMask)
	}

	if got := red.Agents["canny"].Env["ANTHROPIC_API_KEY"]; got != RedactedMask {
		t.Errorf("agent env value not masked: got %q, want %q", got, RedactedMask)
	}

	// The original config must be untouched (redaction works on a copy).
	if got := cfg.MCPServers[0].Env["GITHUB_TOKEN"]; got != envVal {
		t.Errorf("original MCP env mutated: got %q, want %q", got, envVal)
	}

	if got := cfg.Agents["canny"].Env["ANTHROPIC_API_KEY"]; got != envVal {
		t.Errorf("original agent env mutated: got %q, want %q", got, envVal)
	}

	// A rendered redacted config must not contain the value anywhere.
	data, err := EffectiveTOML(red)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(data), envVal) {
		t.Errorf("redacted TOML still leaked the value:\n%s", data)
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
