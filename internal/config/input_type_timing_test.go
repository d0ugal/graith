package config

import (
	"strings"
	"testing"
	"time"
)

// TestInputTypeTiming_EmbeddedDefaults asserts the embedded default config
// reproduces exactly the gr type PTY-injection timing that was hard-coded in
// internal/daemon (typeIdleTimeout=10s, typeMaxWait=2m) before issue #1317 made
// the policy configurable, so a fresh install behaves identically. It checks the
// RAW string fields parsed from the embedded TOML — not just the accessors —
// because an accessor would silently fall back to its Go constant if the
// embedded key were blanked, masking drift (see the "config default fallback
// defeated by embedded TOML" trap).
func TestInputTypeTiming_EmbeddedDefaults(t *testing.T) {
	in := Default().Input

	if in.TypeIdleTimeout != "10s" {
		t.Errorf("embedded input.type_idle_timeout = %q, want %q", in.TypeIdleTimeout, "10s")
	}

	if in.TypeMaxWait != "2m" {
		t.Errorf("embedded input.type_max_wait = %q, want %q", in.TypeMaxWait, "2m")
	}

	if got := in.TypeIdleTimeoutDuration(); got != 10*time.Second {
		t.Errorf("default TypeIdleTimeoutDuration() = %v, want 10s", got)
	}

	if got := in.TypeMaxWaitDuration(); got != 2*time.Minute {
		t.Errorf("default TypeMaxWaitDuration() = %v, want 2m", got)
	}
}

// TestInputTypeTiming_Accessors covers the accessor fallback in isolation. A
// loaded config never reaches these branches with a bad value (Validate rejects
// non-empty invalid/zero/negative durations — see TestInputTypeTiming_Validation);
// the fallback is defensive for directly-constructed InputConfig values, so it
// must still resolve to the default rather than a zero wait.
func TestInputTypeTiming_Accessors(t *testing.T) {
	cases := []struct {
		name     string
		idle     string
		maxWait  string
		wantIdle time.Duration
		wantMax  time.Duration
	}{
		{"empty uses defaults", "", "", 10 * time.Second, 2 * time.Minute},
		{"valid non-default", "3s", "45s", 3 * time.Second, 45 * time.Second},
		{"unparseable falls back (defensive)", "haar", "dreich", 10 * time.Second, 2 * time.Minute},
		{"zero falls back (defensive)", "0", "0s", 10 * time.Second, 2 * time.Minute},
		{"negative falls back (defensive)", "-5s", "-1m", 10 * time.Second, 2 * time.Minute},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := InputConfig{TypeIdleTimeout: c.idle, TypeMaxWait: c.maxWait}
			if got := in.TypeIdleTimeoutDuration(); got != c.wantIdle {
				t.Errorf("TypeIdleTimeoutDuration() = %v, want %v", got, c.wantIdle)
			}

			if got := in.TypeMaxWaitDuration(); got != c.wantMax {
				t.Errorf("TypeMaxWaitDuration() = %v, want %v", got, c.wantMax)
			}
		})
	}
}

// TestInputTypeTiming_Validation asserts the user-facing contract: unset uses
// the default, a valid positive duration passes, and a non-empty invalid, zero,
// or negative value is rejected at load/reload rather than silently falling back
// (issue #1317, aligning with the other bounded-wait fields).
func TestInputTypeTiming_Validation(t *testing.T) {
	valid := Default()
	valid.Input.TypeIdleTimeout = "5s"
	valid.Input.TypeMaxWait = "1m"

	if err := valid.Validate(); err != nil {
		t.Fatalf("valid input timing rejected: %v", err)
	}

	rejects := []struct {
		name    string
		field   string
		set     func(*Config)
		wantSub string
	}{
		{"unparseable idle", "type_idle_timeout", func(c *Config) { c.Input.TypeIdleTimeout = "not-a-duration" }, "input.type_idle_timeout"},
		{"zero idle", "type_idle_timeout", func(c *Config) { c.Input.TypeIdleTimeout = "0" }, "input.type_idle_timeout"},
		{"negative idle", "type_idle_timeout", func(c *Config) { c.Input.TypeIdleTimeout = "-5s" }, "input.type_idle_timeout"},
		{"zero max wait", "type_max_wait", func(c *Config) { c.Input.TypeMaxWait = "0s" }, "input.type_max_wait"},
		{"negative max wait", "type_max_wait", func(c *Config) { c.Input.TypeMaxWait = "-1m" }, "input.type_max_wait"},
	}

	for _, rc := range rejects {
		t.Run(rc.name, func(t *testing.T) {
			cfg := Default()
			rc.set(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected validation error for %s", rc.name)
			}

			if !strings.Contains(err.Error(), rc.wantSub) {
				t.Errorf("error %q does not mention %s", err, rc.wantSub)
			}
		})
	}
}
