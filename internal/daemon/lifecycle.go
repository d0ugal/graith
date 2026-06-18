package daemon

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// applyLifecycleSummaryLocked sets lifecycle status on a session.
// Caller must hold the lock guarding s. Sanitizes and truncates to fit
// the 100-byte limit. Does not call saveState() — caller must persist.
func applyLifecycleSummaryLocked(s *SessionState, text string) {
	if s.SystemKind != "" {
		return
	}
	text = sanitizeSummaryText(text)
	text = truncateToBytes(text, 100)
	now := time.Now()
	s.SummaryText = text
	s.SummarySetAt = &now
	s.SummaryTTL = 0
}

func formatStopSummary(reason string, exitCode *int, prev string, prevSetAt *time.Time, ttl time.Duration) string {
	var base string
	switch reason {
	case StopReasonUser:
		base = "Stopped"
	case StopReasonIdle:
		base = "Stopped after idle"
	case StopReasonShutdown:
		base = "Stopped by shutdown"
	case StopReasonCrash:
		switch {
		case exitCode != nil && *exitCode == 0:
			base = "Exited"
		case exitCode != nil:
			base = fmt.Sprintf("Crashed exit %d", *exitCode)
		default:
			base = "Crashed"
		}
	default:
		base = "Stopped"
	}

	if prev == "" || prevSetAt == nil || time.Since(*prevSetAt) > ttl {
		return base
	}
	return truncateWithContext(base, prev, 100)
}

func truncateWithContext(base, prev string, maxBytes int) string {
	suffix := " (was: " + prev + ")"
	full := base + suffix
	if len(full) <= maxBytes {
		return full
	}
	overhead := len(base) + len(" (was: ...)")
	avail := maxBytes - overhead
	if avail <= 0 {
		return base
	}
	return base + " (was: " + truncateToBytes(prev, avail) + "...)"
}

// truncateToBytes truncates s to at most maxBytes without splitting
// multi-byte UTF-8 sequences. It truncates at rune boundaries.
func truncateToBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk rune by rune, stopping when the next rune would exceed maxBytes.
	pos := 0
	for pos < len(s) {
		_, size := utf8.DecodeRuneInString(s[pos:])
		if pos+size > maxBytes {
			break
		}
		pos += size
	}
	return s[:pos]
}
