package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdatesConfig_DefaultsEnabled(t *testing.T) {
	// The embedded default config must keep the update check on (opt-out) so a
	// fresh install behaves exactly as before the [updates] block existed.
	cfg := Default()
	if !cfg.Updates.Enabled {
		t.Fatal("default updates.enabled = false, want true")
	}

	if cfg.Updates.Repository != "d0ugal/graith" {
		t.Errorf("default updates.repository = %q, want d0ugal/graith", cfg.Updates.Repository)
	}

	if got := cfg.Updates.IntervalDuration(); got != time.Hour {
		t.Errorf("default updates interval = %v, want 1h", got)
	}

	if got := cfg.Updates.TimeoutDuration(); got != 5*time.Second {
		t.Errorf("default updates timeout = %v, want 5s", got)
	}
}

func TestUpdatesConfig_DurationAccessors(t *testing.T) {
	tests := []struct {
		name         string
		interval     string
		timeout      string
		wantInterval time.Duration
		wantTimeout  time.Duration
	}{
		// Empty and invalid both return 0 so the version package applies its own
		// default, rather than this layer guessing one.
		{"empty falls back to zero", "", "", 0, 0},
		{"explicit values", "2h", "10s", 2 * time.Hour, 10 * time.Second},
		{"days supported", "1d", "1m", 24 * time.Hour, time.Minute},
		{"invalid falls back to zero", "thrawn", "dreich", 0, 0},
		{"non-positive falls back to zero", "0", "0s", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := UpdatesConfig{Interval: tt.interval, Timeout: tt.timeout}
			if got := u.IntervalDuration(); got != tt.wantInterval {
				t.Errorf("IntervalDuration() = %v, want %v", got, tt.wantInterval)
			}

			if got := u.TimeoutDuration(); got != tt.wantTimeout {
				t.Errorf("TimeoutDuration() = %v, want %v", got, tt.wantTimeout)
			}
		})
	}
}

func TestUpdatesConfig_LoadDisables(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(cfgPath, []byte(`
[updates]
enabled = false
repository = "canny/bothy"
interval = "6h"
timeout = "2s"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Updates.Enabled {
		t.Error("updates.enabled = true, want false")
	}

	if cfg.Updates.Repository != "canny/bothy" {
		t.Errorf("updates.repository = %q, want canny/bothy", cfg.Updates.Repository)
	}

	if got := cfg.Updates.IntervalDuration(); got != 6*time.Hour {
		t.Errorf("interval = %v, want 6h", got)
	}

	if got := cfg.Updates.TimeoutDuration(); got != 2*time.Second {
		t.Errorf("timeout = %v, want 2s", got)
	}
}

func TestUpdatesConfig_OmittedBlockKeepsDefault(t *testing.T) {
	// A user config that never mentions [updates] must inherit the embedded
	// enabled=true default rather than the bool zero value.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(cfgPath, []byte("default_agent = \"claude\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if !cfg.Updates.Enabled {
		t.Error("updates.enabled = false after omitted block, want true (inherited default)")
	}
}

func TestUpdatesConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		updates UpdatesConfig
		wantErr string
	}{
		{"defaults pass", Default().Updates, ""},
		{"disabled with junk still validated", UpdatesConfig{Interval: "thrawn"}, "updates.interval"},
		{"invalid interval", UpdatesConfig{Enabled: true, Interval: "thrawn"}, "updates.interval"},
		{"non-positive interval", UpdatesConfig{Enabled: true, Interval: "0"}, "updates.interval"},
		{"invalid timeout", UpdatesConfig{Enabled: true, Timeout: "dreich"}, "updates.timeout"},
		{"repository missing owner", UpdatesConfig{Enabled: true, Repository: "/graith"}, "updates.repository"},
		{"repository missing name", UpdatesConfig{Enabled: true, Repository: "d0ugal/"}, "updates.repository"},
		{"repository extra segment", UpdatesConfig{Enabled: true, Repository: "d0ugal/graith/x"}, "updates.repository"},
		{"repository no slash", UpdatesConfig{Enabled: true, Repository: "graith"}, "updates.repository"},
		{"valid custom block", UpdatesConfig{Enabled: true, Repository: "canny/bothy", Interval: "3h", Timeout: "8s"}, ""},
		{"empty durations allowed", UpdatesConfig{Enabled: true, Repository: "canny/bothy"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Updates = tt.updates

			err := cfg.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && err != nil && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
