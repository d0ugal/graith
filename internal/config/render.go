package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/aymanbagabas/go-udiff"
	"github.com/d0ugal/graith/internal/tools"
	"github.com/pelletier/go-toml/v2"
)

// RedactedMask is the placeholder substituted for secret-bearing values when
// a config is rendered for a caller that must not see raw secrets.
const RedactedMask = "***"

// RedactSecrets returns a copy of cfg with secret-bearing values masked: the
// per-server and per-agent `env` maps, whose values routinely hold tokens and
// API keys inline in config.toml. Map keys are preserved (so the shape stays
// visible); only the values are replaced with RedactedMask. cfg is not mutated.
//
// The daemon renders this — not the raw config — over the control protocol, so
// a remote paired human, or a local session reading via the socket, sees the
// configuration structure without its secrets. `gr config show`/`diff` read the
// file directly (not through the daemon) and are deliberately unaffected.
func RedactSecrets(cfg *Config) *Config {
	c := *cfg

	if len(cfg.MCPServers) > 0 {
		c.MCPServers = make([]MCPServerConfig, len(cfg.MCPServers))
		for i, s := range cfg.MCPServers {
			c.MCPServers[i] = redactMCPServer(s)
		}
	}

	if len(cfg.Agents) > 0 {
		c.Agents = make(map[string]Agent, len(cfg.Agents))
		for name, a := range cfg.Agents {
			a.Env = maskValues(a.Env)
			if len(a.MCPServers) > 0 {
				a.MCPServers = make(map[string]MCPServerConfig, len(a.MCPServers))
				for serverName, server := range cfg.Agents[name].MCPServers {
					a.MCPServers[serverName] = redactMCPServer(server)
				}
			}

			c.Agents[name] = a
		}
	}

	return &c
}

// redactMCPServer returns a shallow copy of server with a separately allocated,
// masked env map. It is shared by global servers and per-agent overrides so
// every MCP config shape follows the same redaction boundary.
func redactMCPServer(server MCPServerConfig) MCPServerConfig {
	server.Env = maskValues(server.Env)

	return server
}

// maskValues returns a new map with every value replaced by RedactedMask,
// preserving keys. Returns the input unchanged when it is empty/nil.
func maskValues(m map[string]string) map[string]string {
	if len(m) == 0 {
		return m
	}

	out := make(map[string]string, len(m))
	for k := range m {
		out[k] = RedactedMask
	}

	return out
}

// EffectiveTOML renders cfg as TOML — the effective, fully-merged configuration
// (built-in defaults overlaid with the user's file). This is what `gr config
// show` prints and what the GUI's config viewer displays.
func EffectiveTOML(cfg *Config) ([]byte, error) {
	data, err := toml.Marshal(resolveRenderedDefaults(cfg))
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}

	return data, nil
}

// resolveRenderedDefaults materializes accessor-backed sentinel defaults on a
// copy so effective output describes the values runtime callers actually use.
// The defensive accessor fallbacks remain authoritative for runtime behavior;
// this function only makes their defaults visible to config show/diff and the
// GUI config viewer.
func resolveRenderedDefaults(cfg *Config) *Config {
	c := *cfg

	if c.Remote.MaxPendingPairings == 0 {
		c.Remote.MaxPendingPairings = c.Remote.MaxPendingPairingsOrDefault()
	}

	if strings.TrimSpace(c.Remote.PendingPairingTTL) == "" {
		c.Remote.PendingPairingTTL = canonicalDuration(c.Remote.PendingPairingTTLDuration())
	}

	fallback := c.Remote.PairFallbackRate()
	if c.Remote.PairFallbackCount == 0 {
		c.Remote.PairFallbackCount = fallback.Count
	}

	if strings.TrimSpace(c.Remote.PairFallbackWindow) == "" {
		c.Remote.PairFallbackWindow = canonicalDuration(fallback.Per)
	}

	toolDefaults := tools.Defaults()
	fillToolDefault(&c.Tools.Git, toolDefaults.Git)
	fillToolDefault(&c.Tools.GH, toolDefaults.GH)
	fillToolDefault(&c.Tools.GCX, toolDefaults.GCX)
	fillToolDefault(&c.Tools.Shell, toolDefaults.Shell)
	fillToolDefault(&c.Tools.OSAScript, toolDefaults.OSAScript)
	fillToolDefault(&c.Tools.PS, toolDefaults.PS)
	fillToolDefault(&c.Tools.Lsof, toolDefaults.Lsof)

	if strings.TrimSpace(c.Approvals.CommandTimeout) == "" {
		c.Approvals.CommandTimeout = c.Approvals.CommandTimeoutDuration().String()
	}

	if strings.TrimSpace(c.Approvals.LocalmostTimeout) == "" {
		c.Approvals.LocalmostTimeout = c.Approvals.LocalmostTimeoutDuration().String()
	}

	return &c
}

// canonicalDuration omits trailing zero components from time.Duration's
// spelling so whole-minute and whole-hour defaults remain concise in TOML.
func canonicalDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}

	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}

	return s
}

// fillToolDefault mirrors tools' empty-string merge semantics. Whitespace is
// not treated as unset because runtime resolution preserves it and validation
// rejects it as an invalid explicit executable.
func fillToolDefault(field *string, fallback string) {
	if *field == "" {
		*field = fallback
	}
}

// DiffFromDefaults returns a unified diff (built-in defaults → cfg) of the two
// TOML renderings. toLabel names the "to" side in the diff header (e.g. the
// config file path, or "effective"). An empty return means cfg's effective
// rendering is byte-for-byte identical to the built-in defaults' rendering.
func DiffFromDefaults(cfg *Config, toLabel string) (string, error) {
	defaultBytes, err := EffectiveTOML(Default())
	if err != nil {
		return "", fmt.Errorf("render defaults: %w", err)
	}

	cfgBytes, err := EffectiveTOML(cfg)
	if err != nil {
		return "", fmt.Errorf("render config: %w", err)
	}

	return udiff.Unified("defaults", toLabel, string(defaultBytes), string(cfgBytes)), nil
}
