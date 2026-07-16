package client

import (
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

// savePresentation snapshots the package-level presentation vars and restores
// them after the test, so a test that calls ConfigurePresentation can't leak its
// overrides into other tests (these are process-global by design, mirroring the
// [connection] timeout vars).
func savePresentation(t *testing.T) {
	t.Helper()

	ri, fc, fr, sw := refreshInterval, fallbackCols, fallbackRows, summaryWidth

	t.Cleanup(func() {
		refreshInterval, fallbackCols, fallbackRows, summaryWidth = ri, fc, fr, sw
	})
}

func TestConfigurePresentation_OverridesAndIgnoresNonPositive(t *testing.T) {
	savePresentation(t)

	ConfigurePresentation(PresentationPrefs{
		RefreshInterval: 750 * time.Millisecond,
		DefaultCols:     120,
		DefaultRows:     40,
		SummaryWidth:    64,
	})

	if refreshInterval != 750*time.Millisecond {
		t.Errorf("refreshInterval = %v, want 750ms", refreshInterval)
	}

	if fallbackCols != 120 || fallbackRows != 40 {
		t.Errorf("fallback geometry = %dx%d, want 120x40", fallbackCols, fallbackRows)
	}

	if summaryWidth != 64 {
		t.Errorf("summaryWidth = %d, want 64", summaryWidth)
	}

	if c, r := FallbackGeometry(); c != 120 || r != 40 {
		t.Errorf("FallbackGeometry() = %dx%d, want 120x40", c, r)
	}

	// A non-positive field must be ignored (not zero out an established value),
	// so a partially populated struct can't silently disable a preference.
	ConfigurePresentation(PresentationPrefs{})

	if refreshInterval != 750*time.Millisecond {
		t.Errorf("refreshInterval after zero struct = %v, want 750ms (unchanged)", refreshInterval)
	}

	if fallbackCols != 120 || fallbackRows != 40 {
		t.Errorf("geometry after zero struct = %dx%d, want 120x40 (unchanged)", fallbackCols, fallbackRows)
	}

	if summaryWidth != 64 {
		t.Errorf("summaryWidth after zero struct = %d, want 64 (unchanged)", summaryWidth)
	}
}

// TestDisplaySummaryHonoursConfiguredWidth confirms the configured summary width
// actually drives the picker's truncation, not just the default constant.
func TestDisplaySummaryHonoursConfiguredWidth(t *testing.T) {
	savePresentation(t)

	ConfigurePresentation(PresentationPrefs{SummaryWidth: 10})

	long := protocol.SessionInfo{SummaryText: strings.Repeat("x", 40)}
	got := displaySummary(long)

	if n := len([]rune(got)); n != 10 {
		t.Errorf("truncated summary = %d runes, want 10 (configured width)", n)
	}

	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated summary %q should end with an ellipsis", got)
	}

	// A summary already within the width is returned unchanged.
	short := protocol.SessionInfo{SummaryText: "brief"}
	if got := displaySummary(short); got != "brief" {
		t.Errorf("short summary = %q, want %q", got, "brief")
	}
}
