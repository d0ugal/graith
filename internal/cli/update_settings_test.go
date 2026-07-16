package cli

import (
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func TestUpdateSettings_NilConfig(t *testing.T) {
	// A nil config yields the zero value, which the version package treats as
	// enabled with all defaults — the historical behaviour.
	got := updateSettings(nil)
	if got.Disabled {
		t.Error("nil config produced Disabled = true, want false")
	}

	if got.Repository != "" || got.Interval != 0 || got.Timeout != 0 {
		t.Errorf("nil config produced non-zero settings: %+v", got)
	}
}

func TestUpdateSettings_Translation(t *testing.T) {
	cfg := &config.Config{
		Updates: config.UpdatesConfig{
			Enabled:    false,
			Repository: "canny/bothy",
			Interval:   "3h",
			Timeout:    "8s",
		},
	}

	got := updateSettings(cfg)
	if !got.Disabled {
		t.Error("enabled=false did not map to Disabled=true")
	}

	if got.Repository != "canny/bothy" {
		t.Errorf("Repository = %q, want canny/bothy", got.Repository)
	}

	if got.Interval != 3*time.Hour {
		t.Errorf("Interval = %v, want 3h", got.Interval)
	}

	if got.Timeout != 8*time.Second {
		t.Errorf("Timeout = %v, want 8s", got.Timeout)
	}
}

func TestUpdateSettings_EnabledDefaults(t *testing.T) {
	cfg := &config.Config{Updates: config.UpdatesConfig{Enabled: true}}

	got := updateSettings(cfg)
	if got.Disabled {
		t.Error("enabled=true mapped to Disabled=true")
	}

	// Empty duration strings translate to 0 so the version package fills its
	// own defaults.
	if got.Interval != 0 || got.Timeout != 0 {
		t.Errorf("empty durations produced non-zero: interval=%v timeout=%v", got.Interval, got.Timeout)
	}
}
