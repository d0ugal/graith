package config

import (
	"fmt"
	"strings"

	"github.com/aymanbagabas/go-udiff"
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
			s.Env = maskValues(s.Env)
			c.MCPServers[i] = s
		}
	}

	if len(cfg.Agents) > 0 {
		c.Agents = make(map[string]Agent, len(cfg.Agents))
		for name, a := range cfg.Agents {
			a.Env = maskValues(a.Env)
			c.Agents[name] = a
		}
	}

	return &c
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

// resolveRenderedDefaults materializes accessor-backed sentinel defaults before
// effective output is marshaled. Runtime callers defensively accept zero/empty
// pairing-policy fields as "use the default"; config show/diff must display the
// policy that actually runs rather than those internal sentinels.
func resolveRenderedDefaults(cfg *Config) *Config {
	c := *cfg

	if c.Remote.MaxPendingPairings == 0 {
		c.Remote.MaxPendingPairings = RemoteMaxPendingPairingsDefault
	}
	if strings.TrimSpace(c.Remote.PendingPairingTTL) == "" {
		c.Remote.PendingPairingTTL = "10m"
	}
	if c.Remote.PairFallbackCount == 0 {
		c.Remote.PairFallbackCount = RemotePairFallbackCountDefault
	}
	if strings.TrimSpace(c.Remote.PairFallbackWindow) == "" {
		c.Remote.PairFallbackWindow = "1m"
	}

	return &c
}

// DiffFromDefaults returns a unified diff (built-in defaults → cfg) of the two
// TOML renderings. toLabel names the "to" side in the diff header (e.g. the
// config file path, or "effective"). An empty return means cfg is byte-for-byte
// identical to the built-in defaults.
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
