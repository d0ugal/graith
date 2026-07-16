package config

import (
	"testing"
	"time"
)

func TestDeleteRetentionDuration(t *testing.T) {
	tests := []struct {
		name      string
		retention string
		want      time.Duration
	}{
		{"unset defaults to 24h", "", 24 * time.Hour},
		{"zero disables soft delete", "0", 0},
		{"zero seconds disables soft delete", "0s", 0},
		{"explicit hours", "48h", 48 * time.Hour},
		{"days", "7d", 7 * 24 * time.Hour},
		{"days and hours", "7d12h", 7*24*time.Hour + 12*time.Hour},
		// A typo must fail SAFE to the default, never silently disable recovery.
		{"unparseable falls back to 24h", "haar", 24 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Delete{Retention: tt.retention}
			if got := d.RetentionDuration(); got != tt.want {
				t.Errorf("RetentionDuration(%q) = %v, want %v", tt.retention, got, tt.want)
			}
		})
	}
}

// TestDeleteRetentionUnsetVsZero pins the crucial distinction: an unset field
// defaults to the 24h recovery window, while an explicit "0" disables soft
// delete. They must not collapse to the same value.
func TestDeleteRetentionUnsetVsZero(t *testing.T) {
	unset := Delete{}.RetentionDuration()
	zero := Delete{Retention: "0"}.RetentionDuration()

	if unset <= 0 {
		t.Errorf("unset retention = %v, want a positive default (soft delete on)", unset)
	}

	if zero != 0 {
		t.Errorf("retention \"0\" = %v, want 0 (soft delete disabled)", zero)
	}
}

func TestConfigValidateRetention(t *testing.T) {
	t.Run("valid retention passes", func(t *testing.T) {
		c := Default()
		c.Delete.Retention = "48h"

		if err := c.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil for a valid retention", err)
		}
	})

	t.Run("unparseable retention fails loudly", func(t *testing.T) {
		c := Default()
		c.Delete.Retention = "haar"

		err := c.Validate()
		if err == nil {
			t.Fatal("Validate() = nil, want an error for an unparseable retention")
		}
	})

	t.Run("zero retention is valid", func(t *testing.T) {
		c := Default()
		c.Delete.Retention = "0"

		if err := c.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil for retention=0", err)
		}
	})
}

// TestDefaultConfigRetention verifies the embedded default_config.toml documents
// the 24h window (the accessor fallback and the embedded value must agree).
func TestDefaultConfigRetention(t *testing.T) {
	if got := Default().Delete.RetentionDuration(); got != 24*time.Hour {
		t.Errorf("Default().Delete.RetentionDuration() = %v, want 24h", got)
	}
}

func TestDeletePurgeCadenceDurations(t *testing.T) {
	tests := []struct {
		name         string
		startupDelay string
		interval     string
		wantStartup  time.Duration
		wantInterval time.Duration
	}{
		{"unset defaults", "", "", DefaultPurgeStartupDelay, DefaultPurgeInterval},
		{"explicit values", "5s", "2m", 5 * time.Second, 2 * time.Minute},
		{"days and hours", "1d", "1d", 24 * time.Hour, 24 * time.Hour},
		// A typo or non-positive value must fail SAFE to the default rather than
		// busy-spin the timer or defeat the coarse cadence.
		{"unparseable falls back", "haar", "dreich", DefaultPurgeStartupDelay, DefaultPurgeInterval},
		{"zero falls back", "0", "0", DefaultPurgeStartupDelay, DefaultPurgeInterval},
		{"negative falls back", "-5s", "-2m", DefaultPurgeStartupDelay, DefaultPurgeInterval},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Delete{PurgeStartupDelay: tt.startupDelay, PurgeInterval: tt.interval}

			if got := d.PurgeStartupDelayDuration(); got != tt.wantStartup {
				t.Errorf("PurgeStartupDelayDuration(%q) = %v, want %v", tt.startupDelay, got, tt.wantStartup)
			}

			if got := d.PurgeIntervalDuration(); got != tt.wantInterval {
				t.Errorf("PurgeIntervalDuration(%q) = %v, want %v", tt.interval, got, tt.wantInterval)
			}
		})
	}
}

func TestGCOrphanMinAgeDuration(t *testing.T) {
	tests := []struct {
		name   string
		minAge string
		want   time.Duration
	}{
		{"unset defaults to 5m", "", DefaultGCOrphanMinAge},
		{"explicit", "10m", 10 * time.Minute},
		// "0" is honoured — an explicit opt-out of the age floor.
		{"zero opts out of the floor", "0", 0},
		// A typo or negative value must not widen GC to newly-created dirs.
		{"unparseable falls back", "thrawn", DefaultGCOrphanMinAge},
		{"negative falls back", "-1m", DefaultGCOrphanMinAge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := GCConfig{OrphanMinAge: tt.minAge}
			if got := g.OrphanMinAgeDuration(); got != tt.want {
				t.Errorf("OrphanMinAgeDuration(%q) = %v, want %v", tt.minAge, got, tt.want)
			}
		})
	}
}

func TestConfigValidatePurgeAndGC(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid cadence passes", func(c *Config) {
			c.Delete.PurgeStartupDelay = "1m"
			c.Delete.PurgeInterval = "30m"
			c.GC.OrphanMinAge = "10m"
		}, false},
		{"unparseable startup delay fails", func(c *Config) { c.Delete.PurgeStartupDelay = "haar" }, true},
		{"zero startup delay fails", func(c *Config) { c.Delete.PurgeStartupDelay = "0" }, true},
		{"unparseable interval fails", func(c *Config) { c.Delete.PurgeInterval = "dreich" }, true},
		{"zero interval fails", func(c *Config) { c.Delete.PurgeInterval = "0" }, true},
		{"unparseable orphan age fails", func(c *Config) { c.GC.OrphanMinAge = "thrawn" }, true},
		{"negative orphan age fails", func(c *Config) { c.GC.OrphanMinAge = "-1m" }, true},
		// "0" orphan age is a valid explicit opt-out.
		{"zero orphan age passes", func(c *Config) { c.GC.OrphanMinAge = "0" }, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.mutate(c)

			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("Validate() = nil, want an error")
			}

			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

// TestDefaultConfigPurgeAndGC verifies the embedded default_config.toml values
// agree with the accessor fallbacks (guards against the trap where an embedded
// key silently defeats the Go default — see the config-default memory).
func TestDefaultConfigPurgeAndGC(t *testing.T) {
	c := Default()

	if got := c.Delete.PurgeStartupDelayDuration(); got != DefaultPurgeStartupDelay {
		t.Errorf("Default purge_startup_delay = %v, want %v", got, DefaultPurgeStartupDelay)
	}

	if got := c.Delete.PurgeIntervalDuration(); got != DefaultPurgeInterval {
		t.Errorf("Default purge_interval = %v, want %v", got, DefaultPurgeInterval)
	}

	if got := c.GC.OrphanMinAgeDuration(); got != DefaultGCOrphanMinAge {
		t.Errorf("Default gc.orphan_min_age = %v, want %v", got, DefaultGCOrphanMinAge)
	}
}
