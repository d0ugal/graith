package config

import (
	"testing"
	"time"
)

func TestLaunchMaxConcurrentOrDefault(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"unset uses default", 0, LaunchMaxConcurrentDefault},
		{"negative uses default", -3, LaunchMaxConcurrentDefault},
		{"explicit one", 1, 1},
		{"explicit larger", 8, 8},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := LaunchConfig{MaxConcurrent: tc.in}
			if got := l.MaxConcurrentOrDefault(); got != tc.want {
				t.Errorf("MaxConcurrentOrDefault(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestLaunchStartupTimeoutDuration(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"empty uses default", "", LaunchStartupTimeoutDefault},
		{"explicit disable", "0", 0},
		{"explicit minutes", "2m", 2 * time.Minute},
		{"days suffix", "1d", 24 * time.Hour},
		{"garbage uses default", "haar", LaunchStartupTimeoutDefault},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := LaunchConfig{StartupTimeout: tc.in}
			if got := l.StartupTimeoutDuration(); got != tc.want {
				t.Errorf("StartupTimeoutDuration(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestLaunchSettleTimeoutDuration(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"empty uses default", "", LaunchSettleTimeoutDefault},
		{"explicit zero releases immediately", "0", 0},
		{"explicit seconds", "3s", 3 * time.Second},
		{"garbage uses default", "dreich", LaunchSettleTimeoutDefault},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := LaunchConfig{SettleTimeout: tc.in}
			if got := l.SettleTimeoutDuration(); got != tc.want {
				t.Errorf("SettleTimeoutDuration(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestDefaultLaunchConfig verifies the embedded default_config.toml carries the
// expected [launch] values so a fresh install throttles bursts out of the box.
func TestDefaultLaunchConfig(t *testing.T) {
	cfg := Default()

	if got := cfg.Launch.MaxConcurrentOrDefault(); got != LaunchMaxConcurrentDefault {
		t.Errorf("default max_concurrent = %d, want %d", got, LaunchMaxConcurrentDefault)
	}

	if got := cfg.Launch.StartupTimeoutDuration(); got != LaunchStartupTimeoutDefault {
		t.Errorf("default startup_timeout = %s, want %s", got, LaunchStartupTimeoutDefault)
	}

	if got := cfg.Launch.SettleTimeoutDuration(); got != LaunchSettleTimeoutDefault {
		t.Errorf("default settle_timeout = %s, want %s", got, LaunchSettleTimeoutDefault)
	}
}
