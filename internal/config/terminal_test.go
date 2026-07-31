package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTerminal_EmbeddedDefaults asserts the embedded default config reproduces
// exactly the terminal/TUI literals, so a fresh install behaves as documented.
// It asserts the RAW fields parsed from the embedded TOML (not just the
// accessor), so a default silently dropped from the TOML — leaving only the Go
// fallback — fails here (see the "config default fallback defeated by embedded
// TOML" trap).
func TestTerminal_EmbeddedDefaults(t *testing.T) {
	d := Default().Terminal

	if d.RefreshInterval != "2s" {
		t.Errorf("embedded terminal refresh_interval = %q, want %q", d.RefreshInterval, "2s")
	}

	if d.SummaryWidth != TerminalSummaryWidth {
		t.Errorf("embedded terminal summary_width = %d, want %d", d.SummaryWidth, TerminalSummaryWidth)
	}

	if d.HistoryRows != TerminalHistoryRowsDefault {
		t.Errorf("embedded terminal history_rows = %d, want %d", d.HistoryRows, TerminalHistoryRowsDefault)
	}

	if got := d.RefreshIntervalDuration(); got != 2*time.Second {
		t.Errorf("default RefreshIntervalDuration() = %v, want 2s", got)
	}

	if got := d.SummaryWidthValue(); got != TerminalSummaryWidth {
		t.Errorf("default SummaryWidthValue() = %d, want %d", got, TerminalSummaryWidth)
	}

	if got := d.HistoryRowsOrDefault(); got != TerminalHistoryRowsDefault {
		t.Errorf("default HistoryRowsOrDefault() = %d, want %d", got, TerminalHistoryRowsDefault)
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

		if got := d.HistoryRowsOrDefault(); got != TerminalHistoryRowsDefault {
			t.Errorf("history rows empty = %d, want %d", got, TerminalHistoryRowsDefault)
		}
	})

	t.Run("valid values parse", func(t *testing.T) {
		d := TerminalConfig{RefreshInterval: "500ms", SummaryWidth: 64, HistoryRows: 1234}

		if got := d.RefreshIntervalDuration(); got != 500*time.Millisecond {
			t.Errorf("refresh = %v, want 500ms", got)
		}

		if got := d.SummaryWidthValue(); got != 64 {
			t.Errorf("summary = %d, want 64", got)
		}

		if got := d.HistoryRowsOrDefault(); got != 1234 {
			t.Errorf("history rows = %d, want 1234", got)
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

	t.Run("non-positive history_rows falls back", func(t *testing.T) {
		for _, in := range []int{0, -3} {
			if got := (TerminalConfig{HistoryRows: in}).HistoryRowsOrDefault(); got != TerminalHistoryRowsDefault {
				t.Errorf("HistoryRowsOrDefault(%d) = %d, want default", in, got)
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

	if got := cfg.Terminal.HistoryRowsOrDefault(); got != TerminalHistoryRowsDefault {
		t.Errorf("history_rows = %d, want %d (default retained)", got, TerminalHistoryRowsDefault)
	}
}

// TestValidateRejectsBadRefreshInterval confirms an unparseable or non-positive
// [terminal] refresh_interval fails at load rather than silently falling back to
// the accessor default (a zero/negative cadence would busy-loop the refresh
// tick). Integer terminal fields such as summary_width and history_rows
// self-clamp and so are never load-time errors.
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
