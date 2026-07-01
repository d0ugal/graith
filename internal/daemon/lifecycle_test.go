package daemon

import (
	"testing"
	"time"
)

func TestTruncateToBytes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		expected string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncate", "hello world", 5, "hello"},
		{"utf8_no_split", "héllo", 3, "hé"},
		{"utf8_boundary", "héllo", 2, "h"},
		{"emoji", "hi 👋 there", 7, "hi 👋"},
		{"empty", "", 10, ""},
		{"zero_max", "hello", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateToBytes(tt.input, tt.max)
			if got != tt.expected {
				t.Errorf("truncateToBytes(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.expected)
			}

			if len(got) > tt.max {
				t.Errorf("result %q exceeds max %d bytes", got, tt.max)
			}
		})
	}
}

func TestFormatStopSummary(t *testing.T) {
	fresh := time.Now()
	stale := time.Now().Add(-1 * time.Hour)
	ttl := 5 * time.Minute
	exitCode := func(n int) *int { return &n }

	tests := []struct {
		name       string
		reason     string
		exitCode   *int
		exitSignal string
		prev       string
		prevSet    *time.Time
		ttl        time.Duration
		expected   string
	}{
		{"user_no_prev", StopReasonUser, nil, "", "", nil, ttl, "Stopped"},
		{"user_with_prev", StopReasonUser, nil, "", "Running tests", &fresh, ttl, "Stopped (was: Running tests)"},
		{"user_stale_prev", StopReasonUser, nil, "", "Running tests", &stale, ttl, "Stopped"},
		{"idle_with_prev", StopReasonIdle, nil, "", "Exploring code", &fresh, ttl, "Stopped after idle (was: Exploring code)"},
		{"shutdown", StopReasonShutdown, nil, "", "Building", &fresh, ttl, "Stopped by shutdown (was: Building)"},
		{"crash_nonzero", StopReasonCrash, exitCode(1), "", "Compiling", &fresh, ttl, "Crashed exit 1 (was: Compiling)"},
		{"crash_zero", StopReasonCrash, exitCode(0), "", "Done", &fresh, ttl, "Exited (was: Done)"},
		{"crash_no_code", StopReasonCrash, nil, "", "", nil, ttl, "Crashed"},
		{"unknown_reason", "something", nil, "", "", nil, ttl, "Stopped"},
		{"nil_prevSetAt", StopReasonUser, nil, "", "Running", nil, ttl, "Stopped"},
		{"crash_sigkill", StopReasonCrash, exitCode(-1), "killed", "", nil, ttl, "Killed by SIGKILL"},
		{"crash_sigterm", StopReasonCrash, exitCode(-1), "terminated", "", nil, ttl, "Killed by SIGTERM"},
		{"crash_sigabrt_darwin", StopReasonCrash, exitCode(-1), "abort trap", "", nil, ttl, "Killed by SIGABRT"},
		{"crash_sigabrt_linux", StopReasonCrash, exitCode(-1), "aborted", "", nil, ttl, "Killed by SIGABRT"},
		{"crash_sigkill_with_prev", StopReasonCrash, exitCode(-1), "killed", "Running tests", &fresh, ttl, "Killed by SIGKILL (was: Running tests)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStopSummary(tt.reason, tt.exitCode, tt.exitSignal, tt.prev, tt.prevSet, tt.ttl)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestTruncateWithContext(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		prev     string
		max      int
		expected string
	}{
		{"fits", "Stopped", "Running tests", 100, "Stopped (was: Running tests)"},
		{"truncates_prev", "Stopped", "A very long previous status message that goes on and on", 40, "Stopped (was: A very long previous s...)"},
		{"no_room_for_prev", "Very long base status text here", "prev", 32, "Very long base status text here"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateWithContext(tt.base, tt.prev, tt.max)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}

			if len(got) > tt.max {
				t.Errorf("result %q exceeds max %d bytes", got, tt.max)
			}
		})
	}
}

func TestApplyLifecycleSummaryLocked(t *testing.T) {
	t.Run("sets fields", func(t *testing.T) {
		s := &SessionState{}
		applyLifecycleSummaryLocked(s, "Created by parent")

		if s.SummaryText != "Created by parent" {
			t.Errorf("SummaryText = %q, want %q", s.SummaryText, "Created by parent")
		}

		if s.SummarySetAt == nil {
			t.Error("SummarySetAt should be set")
		}

		if s.SummaryTTL != 0 {
			t.Errorf("SummaryTTL = %d, want 0", s.SummaryTTL)
		}
	})

	t.Run("skips system sessions", func(t *testing.T) {
		s := &SessionState{SystemKind: SystemKindOrchestrator}
		applyLifecycleSummaryLocked(s, "Created by parent")

		if s.SummaryText != "" {
			t.Errorf("SummaryText = %q, should be empty for system session", s.SummaryText)
		}

		if s.SummarySetAt != nil {
			t.Error("SummarySetAt should be nil for system session")
		}
	})

	t.Run("sanitizes control chars", func(t *testing.T) {
		s := &SessionState{}
		applyLifecycleSummaryLocked(s, "Created\x00by\x01parent")

		if s.SummaryText != "Createdbyparent" {
			t.Errorf("SummaryText = %q, want %q", s.SummaryText, "Createdbyparent")
		}
	})

	t.Run("truncates long text", func(t *testing.T) {
		s := &SessionState{}
		long := "Created by a session with a very long name that exceeds one hundred bytes when written out in full text format"
		applyLifecycleSummaryLocked(s, long)

		if len(s.SummaryText) > 100 {
			t.Errorf("SummaryText length = %d, should be <= 100", len(s.SummaryText))
		}
	})
}

func TestFormatStopSummary_CustomTTL(t *testing.T) {
	thirtyMinAgo := time.Now().Add(-30 * time.Minute)
	shortTTL := 5 * time.Minute
	longTTL := 1 * time.Hour

	got := formatStopSummary(StopReasonUser, nil, "", "Waiting for CI", &thirtyMinAgo, shortTTL)
	if got != "Stopped" {
		t.Errorf("with short TTL, got %q, want %q — prev should be stale", got, "Stopped")
	}

	got = formatStopSummary(StopReasonUser, nil, "", "Waiting for CI", &thirtyMinAgo, longTTL)
	if got != "Stopped (was: Waiting for CI)" {
		t.Errorf("with long TTL, got %q, want %q — prev should be fresh", got, "Stopped (was: Waiting for CI)")
	}
}

func TestFormatSignalSummary(t *testing.T) {
	tests := []struct {
		signal   string
		expected string
	}{
		{"killed", "Killed by SIGKILL"},
		{"terminated", "Killed by SIGTERM"},
		{"abort trap", "Killed by SIGABRT"},
		{"aborted", "Killed by SIGABRT"},
		{"hangup", "Killed by hangup"},
	}
	for _, tt := range tests {
		t.Run(tt.signal, func(t *testing.T) {
			got := formatSignalSummary(tt.signal)
			if got != tt.expected {
				t.Errorf("formatSignalSummary(%q) = %q, want %q", tt.signal, got, tt.expected)
			}
		})
	}
}
