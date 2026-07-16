package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTerminal_EmbeddedDefaults asserts the embedded default config reproduces
// exactly the terminal/TUI presentation literals that were hard-coded before
// issue #1254, so a fresh install behaves identically. It asserts the RAW fields
// parsed from the embedded TOML (not just the accessor), so a default silently
// dropped from the TOML — leaving only the Go fallback — fails here (see the
// "config default fallback defeated by embedded TOML" trap).
func TestTerminal_EmbeddedDefaults(t *testing.T) {
	d := Default().Terminal

	if d.RefreshInterval != "2s" {
		t.Errorf("embedded terminal refresh_interval = %q, want %q", d.RefreshInterval, "2s")
	}

	if d.SummaryWidth != TerminalSummaryWidth {
		t.Errorf("embedded terminal summary_width = %d, want %d", d.SummaryWidth, TerminalSummaryWidth)
	}

	if got := d.RefreshIntervalDuration(); got != 2*time.Second {
		t.Errorf("default RefreshIntervalDuration() = %v, want 2s", got)
	}

	if got := d.SummaryWidthValue(); got != TerminalSummaryWidth {
		t.Errorf("default SummaryWidthValue() = %d, want %d", got, TerminalSummaryWidth)
	}
}

// TestTerminal_Accessors covers the empty/valid/invalid/non-positive paths. The
// summary width treats a non-positive value as "use the default"; the refresh
// interval does the same because a zero cadence would busy-loop the refresh tick.
func TestTerminal_Accessors(t *testing.T) {
	t.Run("empty falls back to defaults", func(t *testing.T) {
		var d TerminalConfig

		if got := d.RefreshIntervalDuration(); got != TerminalRefreshIntervalDefault {
			t.Errorf("refresh empty = %v, want %v", got, TerminalRefreshIntervalDefault)
		}

		if got := d.SummaryWidthValue(); got != TerminalSummaryWidth {
			t.Errorf("summary empty = %d, want %d", got, TerminalSummaryWidth)
		}
	})

	t.Run("valid values parse", func(t *testing.T) {
		d := TerminalConfig{RefreshInterval: "500ms", SummaryWidth: 64}

		if got := d.RefreshIntervalDuration(); got != 500*time.Millisecond {
			t.Errorf("refresh = %v, want 500ms", got)
		}

		if got := d.SummaryWidthValue(); got != 64 {
			t.Errorf("summary = %d, want 64", got)
		}
	})

	t.Run("invalid and non-positive refresh_interval falls back", func(t *testing.T) {
		for _, in := range []string{"dreich", "0", "-2s"} {
			if got := (TerminalConfig{RefreshInterval: in}).RefreshIntervalDuration(); got != TerminalRefreshIntervalDefault {
				t.Errorf("RefreshIntervalDuration(%q) = %v, want default (a zero/invalid cadence would busy-loop)", in, got)
			}
		}
	})

	t.Run("non-positive summary_width falls back", func(t *testing.T) {
		for _, in := range []int{0, -3} {
			if got := (TerminalConfig{SummaryWidth: in}).SummaryWidthValue(); got != TerminalSummaryWidth {
				t.Errorf("SummaryWidthValue(%d) = %d, want default", in, got)
			}
		}
	})
}

// TestTerminal_LoadOverride confirms a user config overrides individual keys
// through Load() while omitted keys keep the embedded defaults — the merge
// behaviour that matters when a fork tunes only one value.
func TestTerminal_LoadOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(cfgPath, []byte(`
[terminal]
refresh_interval = "5s"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Terminal.RefreshIntervalDuration(); got != 5*time.Second {
		t.Errorf("refresh_interval = %v, want 5s (overridden)", got)
	}

	// Omitted keys must retain the embedded defaults, not collapse to a Go
	// fallback via an emptied value.
	if got := cfg.Terminal.SummaryWidthValue(); got != TerminalSummaryWidth {
		t.Errorf("summary_width = %d, want %d (default retained)", got, TerminalSummaryWidth)
	}
}

// TestValidateRejectsBadRefreshInterval confirms an unparseable or non-positive
// [terminal] refresh_interval fails at load rather than silently falling back to
// the accessor default (a zero/negative cadence would busy-loop the refresh
// tick). The integer summary_width field self-clamps and so is never a load-time
// error.
func TestValidateRejectsBadRefreshInterval(t *testing.T) {
	for _, bad := range []string{"blether", "0", "-1s"} {
		cfg := Default()
		cfg.Terminal.RefreshInterval = bad

		if err := cfg.Validate(); err == nil {
			t.Errorf("expected Validate() to reject terminal.refresh_interval = %q", bad)
		}
	}

	// A negative summary_width must NOT fail validation — the accessor clamps it
	// to the default at read time.
	cfg := Default()
	cfg.Terminal.SummaryWidth = -1

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() rejected self-clamping terminal.summary_width: %v", err)
	}
}
