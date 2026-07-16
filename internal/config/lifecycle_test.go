package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLaunchLifecycleFields_EmbeddedDefaults asserts the embedded default config
// reproduces exactly the launch-watchdog and lifecycle/PTY policy that was
// hard-coded before issue #1243, so a fresh install behaves identically. It
// asserts the RAW fields parsed from the embedded TOML (not just the accessors),
// so a default silently dropped from the TOML — leaving only the Go fallback —
// fails here (see the "config default fallback defeated by embedded TOML" trap).
func TestLaunchLifecycleFields_EmbeddedDefaults(t *testing.T) {
	d := Default()

	t.Run("launch watchdog knobs", func(t *testing.T) {
		if d.Launch.MaxRestarts != 3 {
			t.Errorf("embedded launch.max_restarts = %d, want 3", d.Launch.MaxRestarts)
		}

		if d.Launch.WatchdogInterval != "15s" {
			t.Errorf("embedded launch.watchdog_interval = %q, want %q", d.Launch.WatchdogInterval, "15s")
		}

		if d.Launch.SlotPollInterval != "100ms" {
			t.Errorf("embedded launch.slot_poll_interval = %q, want %q", d.Launch.SlotPollInterval, "100ms")
		}

		if got := d.Launch.MaxRestartsOrDefault(); got != LaunchMaxRestartsDefault {
			t.Errorf("MaxRestartsOrDefault() = %d, want %d", got, LaunchMaxRestartsDefault)
		}

		if got := d.Launch.WatchdogIntervalDuration(); got != 15*time.Second {
			t.Errorf("WatchdogIntervalDuration() = %v, want 15s", got)
		}

		if got := d.Launch.SlotPollIntervalDuration(); got != 100*time.Millisecond {
			t.Errorf("SlotPollIntervalDuration() = %v, want 100ms", got)
		}
	})

	t.Run("lifecycle policy", func(t *testing.T) {
		l := d.Lifecycle

		checks := []struct {
			name string
			raw  string
			want string
		}{
			{"convert_settle_timeout", l.ConvertSettleTimeout, "5s"},
			{"convert_kill_timeout", l.ConvertKillTimeout, "3s"},
			{"convert_force_kill_timeout", l.ConvertForceKillTimeout, "3s"},
			{"mass_exit_window", l.MassExitWindow, "2s"},
			{"process_kill_grace", l.ProcessKillGrace, "5s"},
			{"adopted_timeout", l.AdoptedTimeout, "24h"},
			{"adopted_poll_interval", l.AdoptedPollInterval, "1s"},
			{"input_delay", l.InputDelay, "50ms"},
		}
		for _, c := range checks {
			if c.raw != c.want {
				t.Errorf("embedded lifecycle.%s = %q, want %q", c.name, c.raw, c.want)
			}
		}

		if l.MassExitThreshold != 5 {
			t.Errorf("embedded lifecycle.mass_exit_threshold = %d, want 5", l.MassExitThreshold)
		}

		if l.ScrollbackHydrationBytes != 131072 {
			t.Errorf("embedded lifecycle.scrollback_hydration_bytes = %d, want 131072", l.ScrollbackHydrationBytes)
		}

		if l.DefaultCols != 80 || l.DefaultRows != 24 {
			t.Errorf("embedded lifecycle default geometry = %dx%d, want 80x24", l.DefaultCols, l.DefaultRows)
		}

		if l.MaxLogBytes != 104857600 {
			t.Errorf("embedded lifecycle.max_log_bytes = %d, want 104857600", l.MaxLogBytes)
		}

		// Accessors on the embedded defaults resolve to the pre-#1243 constants.
		if got := l.ConvertSettleTimeoutDuration(); got != ConvertSettleTimeoutDefault {
			t.Errorf("ConvertSettleTimeoutDuration() = %v, want %v", got, ConvertSettleTimeoutDefault)
		}

		if got := l.AdoptedTimeoutDuration(); got != AdoptedTimeoutDefault {
			t.Errorf("AdoptedTimeoutDuration() = %v, want %v", got, AdoptedTimeoutDefault)
		}

		if got := l.MassExitThresholdOrDefault(); got != MassExitThresholdDefault {
			t.Errorf("MassExitThresholdOrDefault() = %d, want %d", got, MassExitThresholdDefault)
		}

		if got := l.DefaultColsOrDefault(); got != DefaultColsDefault {
			t.Errorf("DefaultColsOrDefault() = %d, want %d", got, DefaultColsDefault)
		}

		if got := l.DefaultRowsOrDefault(); got != DefaultRowsDefault {
			t.Errorf("DefaultRowsOrDefault() = %d, want %d", got, DefaultRowsDefault)
		}

		if got := l.MaxLogBytesOrDefault(); got != MaxLogBytesDefault {
			t.Errorf("MaxLogBytesOrDefault() = %d, want %d", got, MaxLogBytesDefault)
		}

		if got := l.ScrollbackHydrationBytesOrDefault(); got != ScrollbackHydrationBytesDefault {
			t.Errorf("ScrollbackHydrationBytesOrDefault() = %d, want %d", got, ScrollbackHydrationBytesDefault)
		}
	})
}

// TestLaunchAccessors_Fallbacks covers the empty/valid/invalid/non-positive
// paths of the new [launch] knobs.
func TestLaunchAccessors_Fallbacks(t *testing.T) {
	t.Run("empty falls back", func(t *testing.T) {
		var l LaunchConfig

		if got := l.MaxRestartsOrDefault(); got != LaunchMaxRestartsDefault {
			t.Errorf("max_restarts empty = %d, want %d", got, LaunchMaxRestartsDefault)
		}

		if got := l.WatchdogIntervalDuration(); got != LaunchWatchdogIntervalDefault {
			t.Errorf("watchdog empty = %v, want %v", got, LaunchWatchdogIntervalDefault)
		}

		if got := l.SlotPollIntervalDuration(); got != LaunchSlotPollIntervalDefault {
			t.Errorf("slot poll empty = %v, want %v", got, LaunchSlotPollIntervalDefault)
		}
	})

	t.Run("valid values honoured", func(t *testing.T) {
		l := LaunchConfig{MaxRestarts: 7, WatchdogInterval: "30s", SlotPollInterval: "40ms"}

		if got := l.MaxRestartsOrDefault(); got != 7 {
			t.Errorf("max_restarts = %d, want 7", got)
		}

		if got := l.WatchdogIntervalDuration(); got != 30*time.Second {
			t.Errorf("watchdog = %v, want 30s", got)
		}

		if got := l.SlotPollIntervalDuration(); got != 40*time.Millisecond {
			t.Errorf("slot poll = %v, want 40ms", got)
		}
	})

	t.Run("invalid and non-positive fall back", func(t *testing.T) {
		l := LaunchConfig{MaxRestarts: 0, WatchdogInterval: "dreich", SlotPollInterval: "0"}

		if got := l.MaxRestartsOrDefault(); got != LaunchMaxRestartsDefault {
			t.Errorf("max_restarts 0 = %d, want default (disable via startup_timeout)", got)
		}

		if got := l.WatchdogIntervalDuration(); got != LaunchWatchdogIntervalDefault {
			t.Errorf("watchdog invalid = %v, want default", got)
		}

		if got := l.SlotPollIntervalDuration(); got != LaunchSlotPollIntervalDefault {
			t.Errorf("slot poll 0 = %v, want default (a zero cadence would busy-loop)", got)
		}
	})
}

// TestLifecycleAccessors_Fallbacks covers the empty/valid/invalid/non-positive
// paths of every [lifecycle] accessor. Durations use positive-or-default (a zero
// wait would escalate instantly or busy-loop); the count/geometry/byte fields use
// their documented < N thresholds.
func TestLifecycleAccessors_Fallbacks(t *testing.T) {
	t.Run("empty falls back", func(t *testing.T) {
		var l LifecycleConfig

		if got := l.ConvertSettleTimeoutDuration(); got != ConvertSettleTimeoutDefault {
			t.Errorf("convert settle empty = %v, want %v", got, ConvertSettleTimeoutDefault)
		}

		if got := l.MassExitWindowDuration(); got != MassExitWindowDefault {
			t.Errorf("mass exit window empty = %v, want %v", got, MassExitWindowDefault)
		}

		if got := l.MassExitThresholdOrDefault(); got != MassExitThresholdDefault {
			t.Errorf("mass exit threshold empty = %d, want %d", got, MassExitThresholdDefault)
		}

		if got := l.ProcessKillGraceDuration(); got != ProcessKillGraceDefault {
			t.Errorf("process kill grace empty = %v, want %v", got, ProcessKillGraceDefault)
		}

		if got := l.AdoptedPollIntervalDuration(); got != AdoptedPollIntervalDefault {
			t.Errorf("adopted poll empty = %v, want %v", got, AdoptedPollIntervalDefault)
		}

		if got := l.InputDelayDuration(); got != InputDelayDefault {
			t.Errorf("input delay empty = %v, want %v", got, InputDelayDefault)
		}

		// NB: the byte/count fields default only when < 0 (or < 1). A zero-value
		// struct's 0 is the honoured disable/unlimited value, exercised in the
		// "disabled semantics" subtest below — not a fallback.
	})

	t.Run("valid values honoured", func(t *testing.T) {
		l := LifecycleConfig{
			ConvertSettleTimeout:     "9s",
			MassExitWindow:           "10s",
			MassExitThreshold:        12,
			ProcessKillGrace:         "1m",
			AdoptedTimeout:           "48h",
			AdoptedPollInterval:      "2s",
			InputDelay:               "80ms",
			ScrollbackHydrationBytes: 4096,
			DefaultCols:              120,
			DefaultRows:              40,
			MaxLogBytes:              1 << 20,
		}

		if got := l.ConvertSettleTimeoutDuration(); got != 9*time.Second {
			t.Errorf("convert settle = %v, want 9s", got)
		}

		if got := l.MassExitWindowDuration(); got != 10*time.Second {
			t.Errorf("mass exit window = %v, want 10s", got)
		}

		if got := l.MassExitThresholdOrDefault(); got != 12 {
			t.Errorf("mass exit threshold = %d, want 12", got)
		}

		if got := l.ProcessKillGraceDuration(); got != time.Minute {
			t.Errorf("process kill grace = %v, want 1m", got)
		}

		if got := l.AdoptedTimeoutDuration(); got != 48*time.Hour {
			t.Errorf("adopted timeout = %v, want 48h", got)
		}

		if got := l.AdoptedPollIntervalDuration(); got != 2*time.Second {
			t.Errorf("adopted poll = %v, want 2s", got)
		}

		if got := l.InputDelayDuration(); got != 80*time.Millisecond {
			t.Errorf("input delay = %v, want 80ms", got)
		}

		if got := l.ScrollbackHydrationBytesOrDefault(); got != 4096 {
			t.Errorf("hydration = %d, want 4096", got)
		}

		if got := l.DefaultColsOrDefault(); got != 120 {
			t.Errorf("cols = %d, want 120", got)
		}

		if got := l.DefaultRowsOrDefault(); got != 40 {
			t.Errorf("rows = %d, want 40", got)
		}

		if got := l.MaxLogBytesOrDefault(); got != 1<<20 {
			t.Errorf("max log = %d, want %d", got, 1<<20)
		}
	})

	t.Run("disabled semantics: hydration 0 disables, max log 0 unlimited", func(t *testing.T) {
		l := LifecycleConfig{ScrollbackHydrationBytes: 0, MaxLogBytes: 0}

		if got := l.ScrollbackHydrationBytesOrDefault(); got != 0 {
			t.Errorf("hydration 0 = %d, want 0 (disabled)", got)
		}

		if got := l.MaxLogBytesOrDefault(); got != 0 {
			t.Errorf("max log 0 = %d, want 0 (unlimited)", got)
		}
	})

	t.Run("negative counts/bytes fall back to default", func(t *testing.T) {
		l := LifecycleConfig{ScrollbackHydrationBytes: -1, MaxLogBytes: -1, MassExitThreshold: -1, DefaultCols: -1, DefaultRows: -1}

		if got := l.ScrollbackHydrationBytesOrDefault(); got != ScrollbackHydrationBytesDefault {
			t.Errorf("hydration -1 = %d, want default", got)
		}

		if got := l.MaxLogBytesOrDefault(); got != MaxLogBytesDefault {
			t.Errorf("max log -1 = %d, want default", got)
		}

		if got := l.MassExitThresholdOrDefault(); got != MassExitThresholdDefault {
			t.Errorf("threshold -1 = %d, want default", got)
		}

		if got := l.DefaultColsOrDefault(); got != DefaultColsDefault {
			t.Errorf("cols -1 = %d, want default", got)
		}

		if got := l.DefaultRowsOrDefault(); got != DefaultRowsDefault {
			t.Errorf("rows -1 = %d, want default", got)
		}
	})

	t.Run("non-positive durations fall back", func(t *testing.T) {
		l := LifecycleConfig{ConvertKillTimeout: "0", InputDelay: "0", AdoptedPollInterval: "-1s"}

		if got := l.ConvertKillTimeoutDuration(); got != ConvertKillTimeoutDefault {
			t.Errorf("convert kill 0 = %v, want default", got)
		}

		if got := l.InputDelayDuration(); got != InputDelayDefault {
			t.Errorf("input delay 0 = %v, want default (a zero pause defeats the paste guard)", got)
		}

		if got := l.AdoptedPollIntervalDuration(); got != AdoptedPollIntervalDefault {
			t.Errorf("adopted poll -1s = %v, want default", got)
		}
	})
}

// TestLifecycleValidation rejects unparseable, non-positive, and out-of-range
// values at load, rather than silently falling back to the accessor default.
func TestLifecycleValidation(t *testing.T) {
	bad := []struct {
		name  string
		apply func(*Config)
	}{
		{"launch.watchdog_interval unparseable", func(c *Config) { c.Launch.WatchdogInterval = "haar" }},
		{"launch.watchdog_interval zero", func(c *Config) { c.Launch.WatchdogInterval = "0" }},
		{"launch.slot_poll_interval negative", func(c *Config) { c.Launch.SlotPollInterval = "-1s" }},
		{"convert_settle_timeout unparseable", func(c *Config) { c.Lifecycle.ConvertSettleTimeout = "blether" }},
		{"convert_kill_timeout zero", func(c *Config) { c.Lifecycle.ConvertKillTimeout = "0" }},
		{"mass_exit_window zero", func(c *Config) { c.Lifecycle.MassExitWindow = "0" }},
		{"process_kill_grace unparseable", func(c *Config) { c.Lifecycle.ProcessKillGrace = "thrawn" }},
		{"process_kill_grace negative", func(c *Config) { c.Lifecycle.ProcessKillGrace = "-1s" }},
		{"adopted_timeout zero", func(c *Config) { c.Lifecycle.AdoptedTimeout = "0" }},
		{"adopted_poll_interval zero", func(c *Config) { c.Lifecycle.AdoptedPollInterval = "0" }},
		{"input_delay zero", func(c *Config) { c.Lifecycle.InputDelay = "0" }},
		{"input_delay negative", func(c *Config) { c.Lifecycle.InputDelay = "-1ms" }},
		{"default_cols too large", func(c *Config) { c.Lifecycle.DefaultCols = 70000 }},
		{"default_rows too large", func(c *Config) { c.Lifecycle.DefaultRows = 70000 }},
	}

	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.apply(cfg)

			if err := cfg.Validate(); err == nil {
				t.Errorf("expected Validate() to reject %s", tc.name)
			}
		})
	}
}

// TestLifecycleLoadOverride confirms a user config overrides individual keys
// through Load() while omitted keys keep the embedded defaults — the merge
// behaviour that matters when a fork tunes only one value.
func TestLifecycleLoadOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(cfgPath, []byte(`
[launch]
watchdog_interval = "45s"

[lifecycle]
convert_settle_timeout = "8s"
default_cols = 132
max_log_bytes = 0
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Launch.WatchdogIntervalDuration(); got != 45*time.Second {
		t.Errorf("watchdog_interval = %v, want 45s (overridden)", got)
	}

	// Omitted launch knob keeps the embedded default.
	if got := cfg.Launch.SlotPollIntervalDuration(); got != 100*time.Millisecond {
		t.Errorf("slot_poll_interval = %v, want 100ms (default retained)", got)
	}

	if got := cfg.Lifecycle.ConvertSettleTimeoutDuration(); got != 8*time.Second {
		t.Errorf("convert_settle_timeout = %v, want 8s (overridden)", got)
	}

	if got := cfg.Lifecycle.DefaultColsOrDefault(); got != 132 {
		t.Errorf("default_cols = %d, want 132 (overridden)", got)
	}

	// Explicit "0" for the log cap means unlimited, and must survive the merge
	// rather than collapsing back to the embedded 100 MiB default.
	if got := cfg.Lifecycle.MaxLogBytesOrDefault(); got != 0 {
		t.Errorf("max_log_bytes = %d, want 0 (unlimited, overridden)", got)
	}

	// Omitted lifecycle keys keep the embedded defaults.
	if got := cfg.Lifecycle.ConvertKillTimeoutDuration(); got != 3*time.Second {
		t.Errorf("convert_kill_timeout = %v, want 3s (default retained)", got)
	}

	if got := cfg.Lifecycle.DefaultRowsOrDefault(); got != 24 {
		t.Errorf("default_rows = %d, want 24 (default retained)", got)
	}
}
