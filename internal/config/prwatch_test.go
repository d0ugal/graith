package config

import (
	"testing"
	"time"
)

func TestPRWatchDefaults(t *testing.T) {
	cfg := Default()
	pw := cfg.PRWatch

	// Master switch off by default (new behaviour-changing feature opts in).
	if pw.Enabled {
		t.Error("pr_watch should default disabled")
	}
	// CI failures default ON (machine verdict, safe); human-intent default OFF.
	if !pw.NotifyCIFailures {
		t.Error("notify_ci_failures should default true")
	}

	if !pw.NotifyMergeConflicts {
		t.Error("notify_merge_conflicts should default true (directive)")
	}

	if pw.NotifyReviewComments {
		t.Error("notify_review_comments should default false")
	}

	if pw.NotifyPRComments {
		t.Error("notify_pr_comments should default false")
	}

	if pw.NotifyReviewDecisions {
		t.Error("notify_review_decisions should default false")
	}

	if !pw.NotifyPRLifecycle {
		t.Error("notify_pr_lifecycle should default true")
	}
}

func TestPRWatchDurations(t *testing.T) {
	// Empty values fall back to sane defaults.
	var empty PRWatchConfig
	if got := empty.PollPendingDuration(); got != 30*time.Second {
		t.Errorf("PollPendingDuration default = %v, want 30s", got)
	}

	if got := empty.PollTerminalDuration(); got != 5*time.Minute {
		t.Errorf("PollTerminalDuration default = %v, want 5m", got)
	}

	if got := empty.PollMergedDuration(); got != 15*time.Minute {
		t.Errorf("PollMergedDuration default = %v, want 15m", got)
	}

	if got := empty.DebounceDuration(); got != 2*time.Minute {
		t.Errorf("DebounceDuration default = %v, want 2m", got)
	}

	if got := empty.MaxNotifications(); got != 10 {
		t.Errorf("MaxNotifications default = %d, want 10", got)
	}

	// Explicit values parse.
	pw := PRWatchConfig{PollPending: "45s", Debounce: "90s", MaxNotificationsPerPR: 3}
	if got := pw.PollPendingDuration(); got != 45*time.Second {
		t.Errorf("PollPendingDuration = %v, want 45s", got)
	}

	if got := pw.DebounceDuration(); got != 90*time.Second {
		t.Errorf("DebounceDuration = %v, want 90s", got)
	}

	if got := pw.MaxNotifications(); got != 3 {
		t.Errorf("MaxNotifications = %d, want 3", got)
	}
}
