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
	// Every notify_* sub-option defaults ON: enabling pr_watch is a single
	// switch that turns on all notification classes; users disable selectively.
	if !pw.NotifyCIFailures {
		t.Error("notify_ci_failures should default true")
	}

	if !pw.NotifyMergeConflicts {
		t.Error("notify_merge_conflicts should default true")
	}

	if !pw.NotifyReviewComments {
		t.Error("notify_review_comments should default true")
	}

	if !pw.NotifyPRComments {
		t.Error("notify_pr_comments should default true")
	}

	if !pw.NotifyReviewDecisions {
		t.Error("notify_review_decisions should default true")
	}

	if !pw.NotifyPRLifecycle {
		t.Error("notify_pr_lifecycle should default true")
	}

	if !pw.NotifyCIRecovery {
		t.Error("notify_ci_recovery should default true")
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

	t.Run("explicit disables are respected", func(t *testing.T) {
		// Every notify_* class defaults on; a user selectively disabling some
		// keeps those off (and the compat bridge does not re-enable pr-comments
		// when review-comments is explicitly off).
		pw := load(t, "[pr_watch]\nenabled = true\nnotify_review_comments = false\nnotify_pr_comments = false\nnotify_ci_recovery = false\n")
		if pw.NotifyReviewComments {
			t.Error("explicit notify_review_comments=false must be respected")
		}

		if pw.NotifyPRComments {
			t.Error("explicit notify_pr_comments=false must be respected")
		}

		if pw.NotifyCIRecovery {
			t.Error("explicit notify_ci_recovery=false must be respected")
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

// TestTrustedAssociationSet covers the author-trust association accessor: the
// default set when a nil (unset) list, upper-case normalisation of a
// hand-written config, whitespace trimming, and that a present-but-empty list
// is honoured as trust-no-association (fail closed) rather than widened to the
// default.
func TestTrustedAssociationSet(t *testing.T) {
	t.Run("unset defaults to owner/member/collaborator", func(t *testing.T) {
		var pw PRWatchConfig

		set := pw.TrustedAssociationSet()

		for _, want := range []string{"OWNER", "MEMBER", "COLLABORATOR"} {
			if !set[want] {
				t.Errorf("default set should trust %q", want)
			}
		}

		// CONTRIBUTOR is deliberately excluded (drive-by / bot association).
		if set["CONTRIBUTOR"] {
			t.Error("CONTRIBUTOR must not be trusted by default")
		}

		if len(set) != 3 {
			t.Errorf("default set should have exactly 3 entries, got %d", len(set))
		}
	})

	t.Run("configured values are upper-cased and trimmed", func(t *testing.T) {
		pw := PRWatchConfig{TrustedAuthorAssociations: []string{" member ", "Owner"}}
		set := pw.TrustedAssociationSet()

		if !set["MEMBER"] || !set["OWNER"] {
			t.Errorf("configured associations should normalise to upper-case, got %v", set)
		}

		if set["COLLABORATOR"] {
			t.Error("a configured set must not include unlisted defaults")
		}
	})

	t.Run("nil (unset) falls back to default", func(t *testing.T) {
		// A nil slice is the Go zero value / unset key: it must resolve to the
		// default trusted tier.
		pw := PRWatchConfig{}
		set := pw.TrustedAssociationSet()

		if !set["OWNER"] || !set["MEMBER"] || !set["COLLABORATOR"] || len(set) != 3 {
			t.Errorf("nil associations should fall back to the default set, got %v", set)
		}
	})

	t.Run("present-but-empty is honoured as trust-no-association (fail closed)", func(t *testing.T) {
		// trusted_author_associations = [] is an explicit "allowlist-only" request
		// and must NOT be silently widened back to the default (issue #1039).
		pw := PRWatchConfig{TrustedAuthorAssociations: []string{}}
		set := pw.TrustedAssociationSet()

		if len(set) != 0 {
			t.Errorf("an explicit empty list must trust no association, got %v", set)
		}
	})

	t.Run("whitespace-only entries collapse to trust-no-association", func(t *testing.T) {
		// A present list whose entries are all blank is malformed input; it fails
		// closed (empty set), not open to the default.
		pw := PRWatchConfig{TrustedAuthorAssociations: []string{"", "  "}}
		set := pw.TrustedAssociationSet()

		if len(set) != 0 {
			t.Errorf("blank entries must not fall back to the default set, got %v", set)
		}
	})
}

// TestPRWatchAuthorTrustParsing covers parsing the three author-trust keys from
// TOML and the association default/normalisation on load.
func TestPRWatchAuthorTrustParsing(t *testing.T) {
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

	t.Run("keys parse", func(t *testing.T) {
		pw := load(t, "[pr_watch]\nenabled = true\n"+
			"comment_author_allowlist = [\"github-actions[bot]\", \"coderabbitai[bot]\"]\n"+
			"trusted_author_associations = [\"owner\", \"member\"]\n"+
			"notify_untrusted_authors = false\n")

		if len(pw.CommentAuthorAllowlist) != 2 || pw.CommentAuthorAllowlist[0] != "github-actions[bot]" {
			t.Errorf("comment_author_allowlist not parsed, got %v", pw.CommentAuthorAllowlist)
		}

		if pw.NotifyUntrustedAuthors {
			t.Error("notify_untrusted_authors=false should parse as false")
		}

		set := pw.TrustedAssociationSet()
		if !set["OWNER"] || !set["MEMBER"] {
			t.Errorf("trusted_author_associations should normalise to upper-case, got %v", set)
		}

		if set["COLLABORATOR"] {
			t.Error("an explicit list must not include the unlisted default COLLABORATOR")
		}
	})

	t.Run("defaults when unset", func(t *testing.T) {
		// Master switch on, author-trust keys absent: allowlist empty, association
		// default applies, notify_untrusted_authors follows the embedded default.
		pw := load(t, "[pr_watch]\nenabled = true\n")

		if len(pw.CommentAuthorAllowlist) != 0 {
			t.Errorf("comment_author_allowlist should default empty, got %v", pw.CommentAuthorAllowlist)
		}

		set := pw.TrustedAssociationSet()
		if !set["OWNER"] || !set["MEMBER"] || !set["COLLABORATOR"] {
			t.Errorf("unset associations should default to owner/member/collaborator, got %v", set)
		}
	})

	t.Run("explicit empty list is honoured as allowlist-only (fail closed)", func(t *testing.T) {
		// Writing `trusted_author_associations = []` is an operator asking to trust
		// NO association and rely on the allowlist alone. It must not be silently
		// widened back to the default three (issue #1039 review finding).
		pw := load(t, "[pr_watch]\nenabled = true\n"+
			"comment_author_allowlist = [\"canny-bot[bot]\"]\n"+
			"trusted_author_associations = []\n")

		set := pw.TrustedAssociationSet()
		if len(set) != 0 {
			t.Errorf("explicit empty list must trust no association through Load, got %v", set)
		}
	})
}

// TestPRWatchAuthorTrustDefaults asserts the embedded default_config.toml ships
// the author-trust gate on: notify_untrusted_authors true and the default
// trusted associations.
func TestPRWatchAuthorTrustDefaults(t *testing.T) {
	pw := Default().PRWatch

	if !pw.NotifyUntrustedAuthors {
		t.Error("notify_untrusted_authors should default true in the embedded config")
	}

	set := pw.TrustedAssociationSet()
	if !set["OWNER"] || !set["MEMBER"] || !set["COLLABORATOR"] || set["CONTRIBUTOR"] {
		t.Errorf("default trusted associations wrong: %v", set)
	}

	if len(pw.CommentAuthorAllowlist) != 0 {
		t.Errorf("comment_author_allowlist should default empty, got %v", pw.CommentAuthorAllowlist)
	}
}
