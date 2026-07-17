package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// TestScrollbackFetchLinesAlwaysDelegates proves attach scroll mode always sends
// the daemon-default sentinel (0) rather than the client's config snapshot.
// Attach is a long-lived process, so a positive cfg.Limits.LogLines captured at
// connect time would be re-sent on every scroll entry / reconnect and bypass a
// daemon hot reload of log_lines. Sending 0 keeps the daemon's *current* default
// authoritative on every reconnect (issue #1320 round-4).
func TestScrollbackFetchLinesAlwaysDelegates(t *testing.T) {
	oldCfg := cfg

	t.Cleanup(func() { cfg = oldCfg })

	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{
			// The bug: a positive snapshot must NOT be re-sent; the daemon's
			// hot-reloaded default must win, so 0 is sent.
			name: "raised snapshot still delegates",
			cfg:  &config.Config{Limits: config.LimitsConfig{LogLines: 4321}},
		},
		{
			name: "lowered snapshot still delegates",
			cfg:  &config.Config{Limits: config.LimitsConfig{LogLines: 50}},
		},
		{
			name: "unset delegates",
			cfg:  &config.Config{Limits: config.LimitsConfig{LogLines: 0}},
		},
		{
			name: "negative snapshot still delegates",
			cfg:  &config.Config{Limits: config.LimitsConfig{LogLines: -17}},
		},
		{
			name: "nil config delegates",
			cfg:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg = tc.cfg

			if got := scrollbackFetchLines(); got != 0 {
				t.Fatalf("scrollbackFetchLines() = %d, want 0 (daemon-default sentinel on every reconnect)", got)
			}
		})
	}
}

// TestScrollbackFetchLinesMatchesDaemonDelegation proves the client's delegation
// sentinel (0) resolves, on the daemon side, to the daemon's current
// LogLinesOrDefault — so a hot-reloaded log_lines takes effect and the client
// never duplicates the default literal.
func TestScrollbackFetchLinesMatchesDaemonDelegation(t *testing.T) {
	oldCfg := cfg

	t.Cleanup(func() { cfg = oldCfg })

	// Even with a stale positive client snapshot, the sentinel is sent...
	cfg = &config.Config{Limits: config.LimitsConfig{LogLines: 999}}

	if got := scrollbackFetchLines(); got != 0 {
		t.Fatalf("scrollbackFetchLines() = %d, want 0 (delegation sentinel)", got)
	}

	// ...and the daemon (whose config generation is authoritative) maps a <= 0
	// count onto its own current default rather than 2,000 or the client value.
	daemonCfg := &config.Config{Limits: config.LimitsConfig{LogLines: 0}}
	if got := daemonCfg.Limits.LogLinesOrDefault(); got != config.LimitsLogLinesDefault {
		t.Fatalf("LogLinesOrDefault() = %d, want %d", got, config.LimitsLogLinesDefault)
	}
}
