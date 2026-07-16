package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTokenAccounting_EmbeddedDefaults asserts the embedded default config
// reproduces exactly the token-loop timing/batch policy that was hard-coded
// before issue #1244, so a fresh install behaves identically. It asserts the RAW
// fields parsed from the embedded TOML (not just the accessor), so a default
// silently dropped from the TOML — leaving only the Go fallback — fails here
// (see the "config default fallback defeated by embedded TOML" trap).
func TestTokenAccounting_EmbeddedDefaults(t *testing.T) {
	d := Default().TokenAccounting

	if d.PollInterval != "30s" {
		t.Errorf("embedded token_accounting poll_interval = %q, want %q", d.PollInterval, "30s")
	}

	if d.StartupDelay != "5s" {
		t.Errorf("embedded token_accounting startup_delay = %q, want %q", d.StartupDelay, "5s")
	}

	if d.BatchSize != 8 {
		t.Errorf("embedded token_accounting batch_size = %d, want %d", d.BatchSize, 8)
	}

	if got := d.PollIntervalDuration(); got != 30*time.Second {
		t.Errorf("default PollIntervalDuration() = %v, want 30s", got)
	}

	if got := d.StartupDelayDuration(); got != 5*time.Second {
		t.Errorf("default StartupDelayDuration() = %v, want 5s", got)
	}

	if got := d.BatchSizeOrDefault(); got != 8 {
		t.Errorf("default BatchSizeOrDefault() = %d, want 8", got)
	}
}

// TestTokenAccounting_Accessors covers the empty/valid/invalid/non-positive
// paths. poll_interval treats a non-positive value as "use the default" (a zero
// cadence would busy-loop); startup_delay honours an explicit "0" (poll
// immediately); batch_size < 1 uses the default.
func TestTokenAccounting_Accessors(t *testing.T) {
	t.Run("empty falls back to defaults", func(t *testing.T) {
		var d TokenAccounting

		if got := d.PollIntervalDuration(); got != TokenPollIntervalDefault {
			t.Errorf("poll empty = %v, want %v", got, TokenPollIntervalDefault)
		}

		if got := d.StartupDelayDuration(); got != TokenStartupDelayDefault {
			t.Errorf("startup empty = %v, want %v", got, TokenStartupDelayDefault)
		}

		if got := d.BatchSizeOrDefault(); got != TokenBatchSizeDefault {
			t.Errorf("batch empty = %d, want %d", got, TokenBatchSizeDefault)
		}
	})

	t.Run("valid values parse", func(t *testing.T) {
		d := TokenAccounting{PollInterval: "1m", StartupDelay: "2s", BatchSize: 20}

		if got := d.PollIntervalDuration(); got != time.Minute {
			t.Errorf("poll = %v, want 1m", got)
		}

		if got := d.StartupDelayDuration(); got != 2*time.Second {
			t.Errorf("startup = %v, want 2s", got)
		}

		if got := d.BatchSizeOrDefault(); got != 20 {
			t.Errorf("batch = %d, want 20", got)
		}
	})

	t.Run("invalid falls back to defaults", func(t *testing.T) {
		d := TokenAccounting{PollInterval: "dreich", StartupDelay: "thrawn"}

		if got := d.PollIntervalDuration(); got != TokenPollIntervalDefault {
			t.Errorf("poll invalid = %v, want default", got)
		}

		if got := d.StartupDelayDuration(); got != TokenStartupDelayDefault {
			t.Errorf("startup invalid = %v, want default", got)
		}
	})

	t.Run("non-positive: poll keeps default, startup and batch honour zero", func(t *testing.T) {
		d := TokenAccounting{PollInterval: "0", StartupDelay: "0", BatchSize: 0}

		if got := d.PollIntervalDuration(); got != TokenPollIntervalDefault {
			t.Errorf("poll 0 = %v, want default (a zero cadence would busy-loop)", got)
		}

		if got := d.StartupDelayDuration(); got != 0 {
			t.Errorf("startup 0 = %v, want 0 (poll immediately)", got)
		}

		if got := d.BatchSizeOrDefault(); got != TokenBatchSizeDefault {
			t.Errorf("batch 0 = %d, want default", got)
		}

		if got := (TokenAccounting{BatchSize: -3}).BatchSizeOrDefault(); got != TokenBatchSizeDefault {
			t.Errorf("batch negative = %d, want default", got)
		}
	})
}

// TestResourceMonitor_EmbeddedDefaults asserts the embedded default config
// reproduces the sampling policy that was hard-coded before issue #1244.
func TestResourceMonitor_EmbeddedDefaults(t *testing.T) {
	d := Default().ResourceMonitor

	if d.SampleInterval != "30s" {
		t.Errorf("embedded resource_monitor sample_interval = %q, want %q", d.SampleInterval, "30s")
	}

	if d.SampleHistory != 5 {
		t.Errorf("embedded resource_monitor sample_history = %d, want %d", d.SampleHistory, 5)
	}

	if got := d.SampleIntervalDuration(); got != 30*time.Second {
		t.Errorf("default SampleIntervalDuration() = %v, want 30s", got)
	}

	if got := d.SampleHistoryOrDefault(); got != 5 {
		t.Errorf("default SampleHistoryOrDefault() = %d, want 5", got)
	}
}

// TestResourceMonitor_Accessors covers the empty/valid/invalid/non-positive
// paths. sample_interval treats a non-positive value as "use the default";
// sample_history < 1 uses the default.
func TestResourceMonitor_Accessors(t *testing.T) {
	t.Run("empty falls back to defaults", func(t *testing.T) {
		var d ResourceMonitor

		if got := d.SampleIntervalDuration(); got != ResourceSampleIntervalDefault {
			t.Errorf("interval empty = %v, want %v", got, ResourceSampleIntervalDefault)
		}

		if got := d.SampleHistoryOrDefault(); got != ResourceSampleHistoryDefault {
			t.Errorf("history empty = %d, want %d", got, ResourceSampleHistoryDefault)
		}
	})

	t.Run("valid values parse", func(t *testing.T) {
		d := ResourceMonitor{SampleInterval: "1m", SampleHistory: 12}

		if got := d.SampleIntervalDuration(); got != time.Minute {
			t.Errorf("interval = %v, want 1m", got)
		}

		if got := d.SampleHistoryOrDefault(); got != 12 {
			t.Errorf("history = %d, want 12", got)
		}
	})

	t.Run("invalid and non-positive fall back to defaults", func(t *testing.T) {
		d := ResourceMonitor{SampleInterval: "blether", SampleHistory: 0}

		if got := d.SampleIntervalDuration(); got != ResourceSampleIntervalDefault {
			t.Errorf("interval invalid = %v, want default", got)
		}

		if got := (ResourceMonitor{SampleInterval: "0"}).SampleIntervalDuration(); got != ResourceSampleIntervalDefault {
			t.Errorf("interval 0 = %v, want default (a zero cadence would busy-loop)", got)
		}

		if got := d.SampleHistoryOrDefault(); got != ResourceSampleHistoryDefault {
			t.Errorf("history 0 = %d, want default", got)
		}

		if got := (ResourceMonitor{SampleHistory: -2}).SampleHistoryOrDefault(); got != ResourceSampleHistoryDefault {
			t.Errorf("history negative = %d, want default", got)
		}
	})
}

// TestTokenResource_LoadOverride confirms a user config overrides individual
// keys through Load() while omitted keys keep the embedded defaults — the merge
// behaviour that matters when a fork tunes only one value.
func TestTokenResource_LoadOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(cfgPath, []byte(`
[token_accounting]
poll_interval = "2m"

[resource_monitor]
sample_history = 10
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.TokenAccounting.PollIntervalDuration(); got != 2*time.Minute {
		t.Errorf("poll_interval = %v, want 2m (overridden)", got)
	}

	// Omitted keys must retain the embedded defaults, not collapse to a Go
	// fallback via an emptied string.
	if got := cfg.TokenAccounting.StartupDelayDuration(); got != 5*time.Second {
		t.Errorf("startup_delay = %v, want 5s (default retained)", got)
	}

	if got := cfg.TokenAccounting.BatchSizeOrDefault(); got != 8 {
		t.Errorf("batch_size = %d, want 8 (default retained)", got)
	}

	if got := cfg.ResourceMonitor.SampleHistoryOrDefault(); got != 10 {
		t.Errorf("sample_history = %d, want 10 (overridden)", got)
	}

	if got := cfg.ResourceMonitor.SampleIntervalDuration(); got != 30*time.Second {
		t.Errorf("sample_interval = %v, want 30s (default retained)", got)
	}
}
