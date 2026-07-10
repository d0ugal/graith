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
