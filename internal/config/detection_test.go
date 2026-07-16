package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDetectionConfig_EmbeddedDefaults asserts the embedded default config
// reproduces exactly the timing policy that was hard-coded before issue #1241,
// so a fresh install behaves identically. Verified through Default() (which
// parses the embedded TOML), not just the accessor fallbacks, because the
// embedded values — not the Go constants — are what a real daemon reads.
func TestDetectionConfig_EmbeddedDefaults(t *testing.T) {
	d := Default().Detection

	checks := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"scan_interval", d.ScanIntervalDuration(), 500 * time.Millisecond},
		{"fetch_interval", d.FetchIntervalDuration(), 5 * time.Minute},
		{"fetch_timeout", d.FetchTimeoutDuration(), 30 * time.Second},
		{"silent_threshold", d.SilentThresholdDuration(), 20 * time.Second},
		{"adopted_grace", d.AdoptedGraceDuration(), 60 * time.Second},
		{"recent_output_window", d.RecentOutputWindowDuration(), 3 * time.Second},
		{"hook_start_window", d.HookStartWindowDuration(), 5 * time.Second},
		{"hook_activity_window", d.HookActivityWindowDuration(), 30 * time.Second},
		{"hook_terminal_window", d.HookTerminalWindowDuration(), 30 * time.Minute},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("default detection %s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestDetectionConfig_Accessors covers the empty/valid/invalid/non-positive
// paths for every accessor. Cadences, timeouts, and hook windows treat a
// non-positive value as "use the default" (they have no sensible zero); the two
// disable-able fallback windows (adopted_grace, recent_output_window) honour an
// explicit "0".
func TestDetectionConfig_Accessors(t *testing.T) {
	t.Run("empty falls back to defaults", func(t *testing.T) {
		var d DetectionConfig // all zero-value strings

		checks := []struct {
			name string
			got  time.Duration
			want time.Duration
		}{
			{"scan", d.ScanIntervalDuration(), DetectionScanIntervalDefault},
			{"fetch interval", d.FetchIntervalDuration(), DetectionFetchIntervalDefault},
			{"fetch timeout", d.FetchTimeoutDuration(), DetectionFetchTimeoutDefault},
			{"silent", d.SilentThresholdDuration(), DetectionSilentThresholdDefault},
			{"adopted", d.AdoptedGraceDuration(), DetectionAdoptedGraceDefault},
			{"recent", d.RecentOutputWindowDuration(), DetectionRecentOutputWindowDefault},
			{"hook start", d.HookStartWindowDuration(), DetectionHookStartWindowDefault},
			{"hook activity", d.HookActivityWindowDuration(), DetectionHookActivityWindowDefault},
			{"hook terminal", d.HookTerminalWindowDuration(), DetectionHookTerminalWindowDefault},
		}
		for _, c := range checks {
			if c.got != c.want {
				t.Errorf("%s empty = %v, want default %v", c.name, c.got, c.want)
			}
		}
	})

	t.Run("valid values parse", func(t *testing.T) {
		d := DetectionConfig{
			ScanInterval:       "1s",
			FetchInterval:      "10m",
			FetchTimeout:       "45s",
			SilentThreshold:    "1m",
			AdoptedGrace:       "2m",
			RecentOutputWindow: "5s",
			HookStartWindow:    "2s",
			HookActivityWindow: "1m",
			HookTerminalWindow: "1h",
		}

		if got := d.ScanIntervalDuration(); got != time.Second {
			t.Errorf("scan = %v, want 1s", got)
		}

		if got := d.FetchIntervalDuration(); got != 10*time.Minute {
			t.Errorf("fetch interval = %v, want 10m", got)
		}

		if got := d.FetchTimeoutDuration(); got != 45*time.Second {
			t.Errorf("fetch timeout = %v, want 45s", got)
		}

		if got := d.SilentThresholdDuration(); got != time.Minute {
			t.Errorf("silent = %v, want 1m", got)
		}

		if got := d.AdoptedGraceDuration(); got != 2*time.Minute {
			t.Errorf("adopted = %v, want 2m", got)
		}

		if got := d.RecentOutputWindowDuration(); got != 5*time.Second {
			t.Errorf("recent = %v, want 5s", got)
		}

		if got := d.HookStartWindowDuration(); got != 2*time.Second {
			t.Errorf("hook start = %v, want 2s", got)
		}

		if got := d.HookActivityWindowDuration(); got != time.Minute {
			t.Errorf("hook activity = %v, want 1m", got)
		}

		if got := d.HookTerminalWindowDuration(); got != time.Hour {
			t.Errorf("hook terminal = %v, want 1h", got)
		}
	})

	t.Run("invalid falls back to defaults", func(t *testing.T) {
		d := DetectionConfig{
			ScanInterval:       "dreich",
			AdoptedGrace:       "thrawn",
			RecentOutputWindow: "blether",
		}
		if got := d.ScanIntervalDuration(); got != DetectionScanIntervalDefault {
			t.Errorf("scan invalid = %v, want default", got)
		}

		if got := d.AdoptedGraceDuration(); got != DetectionAdoptedGraceDefault {
			t.Errorf("adopted invalid = %v, want default", got)
		}

		if got := d.RecentOutputWindowDuration(); got != DetectionRecentOutputWindowDefault {
			t.Errorf("recent invalid = %v, want default", got)
		}
	})

	t.Run("non-positive: cadences keep default, fallback windows disable", func(t *testing.T) {
		d := DetectionConfig{
			ScanInterval:       "0",
			FetchInterval:      "0s",
			FetchTimeout:       "-5s",
			SilentThreshold:    "0",
			HookStartWindow:    "0",
			HookActivityWindow: "-1m",
			HookTerminalWindow: "0",
			// These two can be legitimately disabled with "0".
			AdoptedGrace:       "0",
			RecentOutputWindow: "0",
		}

		if got := d.ScanIntervalDuration(); got != DetectionScanIntervalDefault {
			t.Errorf("scan 0 = %v, want default (a zero cadence would busy-loop)", got)
		}

		if got := d.FetchIntervalDuration(); got != DetectionFetchIntervalDefault {
			t.Errorf("fetch interval 0 = %v, want default", got)
		}

		if got := d.FetchTimeoutDuration(); got != DetectionFetchTimeoutDefault {
			t.Errorf("fetch timeout negative = %v, want default", got)
		}

		if got := d.SilentThresholdDuration(); got != DetectionSilentThresholdDefault {
			t.Errorf("silent 0 = %v, want default", got)
		}

		if got := d.HookStartWindowDuration(); got != DetectionHookStartWindowDefault {
			t.Errorf("hook start 0 = %v, want default", got)
		}

		if got := d.HookActivityWindowDuration(); got != DetectionHookActivityWindowDefault {
			t.Errorf("hook activity negative = %v, want default", got)
		}

		if got := d.HookTerminalWindowDuration(); got != DetectionHookTerminalWindowDefault {
			t.Errorf("hook terminal 0 = %v, want default", got)
		}

		if got := d.AdoptedGraceDuration(); got != 0 {
			t.Errorf("adopted 0 = %v, want 0 (disabled)", got)
		}

		if got := d.RecentOutputWindowDuration(); got != 0 {
			t.Errorf("recent 0 = %v, want 0 (disabled)", got)
		}
	})
}

// TestDetectionConfig_LoadOverride confirms a user config overrides individual
// detection keys through Load() while omitted keys keep the embedded defaults —
// the merge behaviour that matters when a fork tunes only one value.
func TestDetectionConfig_LoadOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(cfgPath, []byte(`
[detection]
scan_interval = "2s"
hook_terminal_window = "10m"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Detection.ScanIntervalDuration(); got != 2*time.Second {
		t.Errorf("scan_interval = %v, want 2s (overridden)", got)
	}

	if got := cfg.Detection.HookTerminalWindowDuration(); got != 10*time.Minute {
		t.Errorf("hook_terminal_window = %v, want 10m (overridden)", got)
	}

	// Omitted keys must retain the embedded defaults, not collapse to the Go
	// fallback via an emptied string.
	if got := cfg.Detection.FetchIntervalDuration(); got != 5*time.Minute {
		t.Errorf("fetch_interval = %v, want 5m (default retained)", got)
	}

	if got := cfg.Detection.SilentThresholdDuration(); got != 20*time.Second {
		t.Errorf("silent_threshold = %v, want 20s (default retained)", got)
	}
}
