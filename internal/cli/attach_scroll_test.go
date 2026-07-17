package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// TestScrollbackFetchLinesResolvesFromLimits proves attach scroll mode requests
// the configured [limits] log_lines count instead of the old hard-coded 2,000,
// and that unset/invalid values delegate to the daemon default by sending 0
// rather than the client duplicating the default literal (issue #1320).
func TestScrollbackFetchLinesResolvesFromLimits(t *testing.T) {
	oldCfg := cfg

	t.Cleanup(func() { cfg = oldCfg })

	tests := []struct {
		name string
		cfg  *config.Config
		want int
	}{
		{
			// Non-default regression: a raised limit must be requested verbatim.
			name: "custom raised value requested verbatim",
			cfg:  &config.Config{Limits: config.LimitsConfig{LogLines: 4321}},
			want: 4321,
		},
		{
			// A lowered limit (memory/privacy) is honoured too, no longer stuck at 2000.
			name: "custom lowered value requested verbatim",
			cfg:  &config.Config{Limits: config.LimitsConfig{LogLines: 50}},
			want: 50,
		},
		{
			// Unset: send 0 so the daemon applies LogLinesOrDefault (delegation).
			name: "unset delegates to daemon default",
			cfg:  &config.Config{Limits: config.LimitsConfig{LogLines: 0}},
			want: 0,
		},
		{
			// Defensive: an invalid negative value must not be sent as a negative
			// line count; it collapses to 0 so the daemon applies its default.
			name: "negative value collapses to delegation",
			cfg:  &config.Config{Limits: config.LimitsConfig{LogLines: -17}},
			want: 0,
		},
		{
			name: "nil config delegates to daemon default",
			cfg:  nil,
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg = tc.cfg

			if got := scrollbackFetchLines(); got != tc.want {
				t.Fatalf("scrollbackFetchLines() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestScrollbackFetchLinesMatchesDaemonDelegation proves the client's delegation
// sentinel (0) resolves, on the daemon side, to the same LogLinesOrDefault value
// the configured count would use — so the client never duplicates the default.
func TestScrollbackFetchLinesMatchesDaemonDelegation(t *testing.T) {
	oldCfg := cfg

	t.Cleanup(func() { cfg = oldCfg })

	cfg = &config.Config{Limits: config.LimitsConfig{LogLines: 0}}

	if got := scrollbackFetchLines(); got != 0 {
		t.Fatalf("scrollbackFetchLines() = %d, want 0 (delegation sentinel)", got)
	}

	// The daemon's logs handler treats a <= 0 count as LogLinesOrDefault, so the
	// effective scroll-mode fetch equals the shared default rather than 2,000.
	if got := cfg.Limits.LogLinesOrDefault(); got != config.LimitsLogLinesDefault {
		t.Fatalf("LogLinesOrDefault() = %d, want %d", got, config.LimitsLogLinesDefault)
	}
}
