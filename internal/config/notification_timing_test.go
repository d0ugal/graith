package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNotificationTiming_EmbeddedDefaults asserts the embedded default config
// reproduces exactly the timing policy that was hard-coded before issue #1245,
// so a fresh install behaves identically. It asserts the RAW string fields
// parsed from the embedded TOML, not just the accessor results: an accessor
// would silently fall back to its Go constant if the embedded key were blanked,
// masking the drift (see the "config default fallback defeated by embedded
// TOML" trap). The accessors are then checked to confirm both agree.
func TestNotificationTiming_EmbeddedDefaults(t *testing.T) {
	tm := Default().Notifications.Timing

	rawChecks := []struct {
		name string
		got  string
		want string
	}{
		{"coalesce_window", tm.CoalesceWindow, "30s"},
		{"dispatch_timeout", tm.DispatchTimeout, "15s"},
		{"inbox_idle_timeout", tm.InboxIdleTimeout, "10s"},
		{"inbox_max_wait", tm.InboxMaxWait, "2m"},
		{"inbox_cooldown", tm.InboxCooldown, "30s"},
		{"inbox_detached_delay", tm.InboxDetachedDelay, "5s"},
	}
	for _, c := range rawChecks {
		if c.got != c.want {
			t.Errorf("embedded notifications.timing %s = %q, want %q", c.name, c.got, c.want)
		}
	}

	durChecks := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"coalesce_window", tm.CoalesceWindowDuration(), 30 * time.Second},
		{"dispatch_timeout", tm.DispatchTimeoutDuration(), 15 * time.Second},
		{"inbox_idle_timeout", tm.InboxIdleTimeoutDuration(), 10 * time.Second},
		{"inbox_max_wait", tm.InboxMaxWaitDuration(), 2 * time.Minute},
		{"inbox_cooldown", tm.InboxCooldownDuration(), 30 * time.Second},
		{"inbox_detached_delay", tm.InboxDetachedDelayDuration(), 5 * time.Second},
	}
	for _, c := range durChecks {
		if c.got != c.want {
			t.Errorf("default notifications.timing %s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestNotificationTiming_Accessors covers the empty/valid/invalid/non-positive
// paths for every accessor. Timeouts and user-idle windows treat a non-positive
// value as "use the default" (they have no sensible zero); the three
// disable-able knobs (coalesce_window, inbox_cooldown, inbox_detached_delay)
// honour an explicit "0".
func TestNotificationTiming_Accessors(t *testing.T) {
	t.Run("empty falls back to defaults", func(t *testing.T) {
		var tm NotificationTiming // all zero-value strings

		checks := []struct {
			name string
			got  time.Duration
			want time.Duration
		}{
			{"coalesce", tm.CoalesceWindowDuration(), NotifyCoalesceWindowDefault},
			{"dispatch", tm.DispatchTimeoutDuration(), NotifyDispatchTimeoutDefault},
			{"idle", tm.InboxIdleTimeoutDuration(), NotifyInboxIdleTimeoutDefault},
			{"max wait", tm.InboxMaxWaitDuration(), NotifyInboxMaxWaitDefault},
			{"cooldown", tm.InboxCooldownDuration(), NotifyInboxCooldownDefault},
			{"detached", tm.InboxDetachedDelayDuration(), NotifyInboxDetachedDelayDefault},
		}
		for _, c := range checks {
			if c.got != c.want {
				t.Errorf("%s empty = %v, want default %v", c.name, c.got, c.want)
			}
		}
	})

	t.Run("valid values parse", func(t *testing.T) {
		tm := NotificationTiming{
			CoalesceWindow:     "45s",
			DispatchTimeout:    "20s",
			InboxIdleTimeout:   "3s",
			InboxMaxWait:       "5m",
			InboxCooldown:      "1m",
			InboxDetachedDelay: "2s",
		}

		if got := tm.CoalesceWindowDuration(); got != 45*time.Second {
			t.Errorf("coalesce = %v, want 45s", got)
		}

		if got := tm.DispatchTimeoutDuration(); got != 20*time.Second {
			t.Errorf("dispatch = %v, want 20s", got)
		}

		if got := tm.InboxIdleTimeoutDuration(); got != 3*time.Second {
			t.Errorf("idle = %v, want 3s", got)
		}

		if got := tm.InboxMaxWaitDuration(); got != 5*time.Minute {
			t.Errorf("max wait = %v, want 5m", got)
		}

		if got := tm.InboxCooldownDuration(); got != time.Minute {
			t.Errorf("cooldown = %v, want 1m", got)
		}

		if got := tm.InboxDetachedDelayDuration(); got != 2*time.Second {
			t.Errorf("detached = %v, want 2s", got)
		}
	})

	t.Run("invalid falls back to defaults", func(t *testing.T) {
		tm := NotificationTiming{
			CoalesceWindow:     "dreich",
			DispatchTimeout:    "thrawn",
			InboxIdleTimeout:   "blether",
			InboxMaxWait:       "canny",
			InboxCooldown:      "bothy",
			InboxDetachedDelay: "croft",
		}

		if got := tm.CoalesceWindowDuration(); got != NotifyCoalesceWindowDefault {
			t.Errorf("coalesce invalid = %v, want default", got)
		}

		if got := tm.DispatchTimeoutDuration(); got != NotifyDispatchTimeoutDefault {
			t.Errorf("dispatch invalid = %v, want default", got)
		}

		if got := tm.InboxIdleTimeoutDuration(); got != NotifyInboxIdleTimeoutDefault {
			t.Errorf("idle invalid = %v, want default", got)
		}

		if got := tm.InboxMaxWaitDuration(); got != NotifyInboxMaxWaitDefault {
			t.Errorf("max wait invalid = %v, want default", got)
		}

		if got := tm.InboxCooldownDuration(); got != NotifyInboxCooldownDefault {
			t.Errorf("cooldown invalid = %v, want default", got)
		}

		if got := tm.InboxDetachedDelayDuration(); got != NotifyInboxDetachedDelayDefault {
			t.Errorf("detached invalid = %v, want default", got)
		}
	})

	t.Run("non-positive: timeouts keep default, disable-able knobs honour 0", func(t *testing.T) {
		tm := NotificationTiming{
			DispatchTimeout:  "0",
			InboxIdleTimeout: "-5s",
			InboxMaxWait:     "0s",
			// These three can be legitimately disabled with "0".
			CoalesceWindow:     "0",
			InboxCooldown:      "0",
			InboxDetachedDelay: "0",
		}

		if got := tm.DispatchTimeoutDuration(); got != NotifyDispatchTimeoutDefault {
			t.Errorf("dispatch 0 = %v, want default (a zero timeout fails instantly)", got)
		}

		if got := tm.InboxIdleTimeoutDuration(); got != NotifyInboxIdleTimeoutDefault {
			t.Errorf("idle negative = %v, want default", got)
		}

		if got := tm.InboxMaxWaitDuration(); got != NotifyInboxMaxWaitDefault {
			t.Errorf("max wait 0 = %v, want default", got)
		}

		if got := tm.CoalesceWindowDuration(); got != 0 {
			t.Errorf("coalesce 0 = %v, want 0 (disabled)", got)
		}

		if got := tm.InboxCooldownDuration(); got != 0 {
			t.Errorf("cooldown 0 = %v, want 0 (disabled)", got)
		}

		if got := tm.InboxDetachedDelayDuration(); got != 0 {
			t.Errorf("detached 0 = %v, want 0 (immediate)", got)
		}
	})
}

// TestNotificationTiming_PartialConfigMerge confirms a user config that sets one
// timing knob keeps the embedded defaults for the others (issue #1245: preserve
// compatibility for partial config files).
func TestNotificationTiming_PartialConfigMerge(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	toml := `
[notifications.timing]
coalesce_window = "90s"
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	tm := cfg.Notifications.Timing

	if got := tm.CoalesceWindowDuration(); got != 90*time.Second {
		t.Errorf("coalesce_window = %v, want 90s (user override)", got)
	}

	// Unmentioned embedded defaults survive the merge.
	if got := tm.DispatchTimeoutDuration(); got != NotifyDispatchTimeoutDefault {
		t.Errorf("dispatch_timeout = %v, want %v (default preserved)", got, NotifyDispatchTimeoutDefault)
	}

	if got := tm.InboxCooldownDuration(); got != NotifyInboxCooldownDefault {
		t.Errorf("inbox_cooldown = %v, want %v (default preserved)", got, NotifyInboxCooldownDefault)
	}
}
