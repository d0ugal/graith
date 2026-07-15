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

// hookDerivedStatus returns the on-the-fly "Using <tool>" status a hook report
// yields, or "" when the report carries no tool name or is no longer
// authoritative at now. This mirrors the fallback in toSessionInfo so a session
// whose only visible status was hook-derived (never stored in SummaryText)
// keeps that context — e.g. in a stop summary's "(was: …)" suffix.
func hookDerivedStatus(hr hookReport, now time.Time) string {
	if hr.ToolName == "" || !now.Before(hr.AuthoritativeUntil) {
		return ""
	}

	return "Using " + hr.ToolName
}

// prevStopSummaryLocked resolves the previous visible status (and the time it
// was set) to thread into a session's stop summary as "(was: …)" context. It
// prefers the stored SummaryText, but a session whose only visible status was
// hook-derived (e.g. "Using Bash", computed on the fly and never stored) has an
// empty SummaryText — so it falls back to the current hook report, preserving
// that context on stop (issue #1034). Because the hook report is freshness-
// checked here, the fallback's set-at time is now, so the stop-summary TTL gate
// always admits it. Caller must hold sm.mu.
func (sm *SessionManager) prevStopSummaryLocked(s *SessionState, id string) (string, *time.Time) {
	if s.SummaryText != "" {
		return s.SummaryText, s.SummarySetAt
	}

	if hr, ok := sm.hookReports[id]; ok {
		now := time.Now()
		if derived := hookDerivedStatus(hr, now); derived != "" {
			return derived, &now
		}
	}

	return "", s.SummarySetAt
}

func formatStopSummary(reason string, exitCode *int, exitSignal string, prev string, prevSetAt *time.Time, ttl time.Duration) string {
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
		case exitSignal != "":
			base = formatSignalSummary(exitSignal)
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

func formatSignalSummary(sig string) string {
	switch sig {
	case "killed":
		return "Killed by SIGKILL"
	case "terminated":
		return "Killed by SIGTERM"
	case "abort trap", "aborted":
		return "Killed by SIGABRT"
	default:
		return fmt.Sprintf("Killed by %s", sig)
	}
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
