package config

import (
	"os"
	"path/filepath"
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

// TestPRWatchCommentCompat covers the backward-compatibility bridge: an older
// config that enabled notify_review_comments (which used to gate BOTH comment
// surfaces) but predates notify_pr_comments must keep receiving conversation
// comments, while any config that mentions notify_pr_comments is left as-is.
func TestPRWatchCommentCompat(t *testing.T) {
	load := func(t *testing.T, body string) PRWatchConfig {
		t.Helper()

		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")

		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}

		return cfg.PRWatch
	}

	t.Run("legacy review-comments-on implies pr-comments", func(t *testing.T) {
		pw := load(t, "[pr_watch]\nenabled = true\nnotify_review_comments = true\n")
		if !pw.NotifyPRComments {
			t.Error("notify_review_comments=true with notify_pr_comments absent should imply pr comments")
		}
	})

	t.Run("explicit pr-comments false is respected", func(t *testing.T) {
		pw := load(t, "[pr_watch]\nenabled = true\nnotify_review_comments = true\nnotify_pr_comments = false\n")
		if pw.NotifyPRComments {
			t.Error("explicit notify_pr_comments=false must not be overridden by compat")
		}
	})

	t.Run("review-comments off does not enable pr-comments", func(t *testing.T) {
		pw := load(t, "[pr_watch]\nenabled = true\nnotify_ci_failures = true\n")
		if pw.NotifyPRComments {
			t.Error("compat must not fire when notify_review_comments is off")
		}
	})
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
