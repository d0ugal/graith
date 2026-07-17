package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
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

// TestPRWatchDocumentationDefaultsMatchEmbedded keeps the documented top-level
// timing example in lockstep with the embedded defaults. These values directly
// control polling load and notification anti-flood behavior, and the docs state
// that the example shows the defaults.
func TestPRWatchDocumentationDefaultsMatchEmbedded(t *testing.T) {
	path := filepath.Join("..", "..", "website", "content", "docs", "configuration", "automation.md")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	const fence = "```toml\n[pr_watch]\n"

	doc := string(data)

	start := strings.Index(doc, fence)
	if start < 0 {
		t.Fatalf("%s: missing top-level [pr_watch] TOML example", path)
	}

	start += len("```toml\n")

	end := strings.Index(doc[start:], "\n```")
	if end < 0 {
		t.Fatalf("%s: unterminated top-level [pr_watch] TOML example", path)
	}

	var documented Config
	if err := toml.Unmarshal([]byte(doc[start:start+end]), &documented); err != nil {
		t.Fatalf("parse documented [pr_watch] example: %v", err)
	}

	want := Default().PRWatch
	checks := []struct {
		name, got, want string
	}{
		{"poll_pending", documented.PRWatch.PollPending, want.PollPending},
		{"poll_terminal", documented.PRWatch.PollTerminal, want.PollTerminal},
		{"poll_merged", documented.PRWatch.PollMerged, want.PollMerged},
		{"debounce", documented.PRWatch.Debounce, want.Debounce},
	}

	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("documented pr_watch.%s = %q, embedded default = %q", check.name, check.got, check.want)
		}
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

// TestPRWatchAdvancedAccessorDefaults covers the [pr_watch.advanced] accessor
// fail-safes: a zero-value config (direct struct construction, no TOML) resolves
// every advanced knob to its documented default. These are the runtime fail-safes
// that back the embedded default_config.toml values (asserted separately by
// TestPRWatchAdvancedEmbeddedDefaults).
func TestPRWatchAdvancedAccessorDefaults(t *testing.T) {
	var p PRWatchConfig // zero value: no [pr_watch.advanced] set

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"base_tick", p.BaseTickDuration(), 15 * time.Second},
		{"batch_size", p.BatchSize(), 3},
		{"no_pr_negative_cache", p.NoPRNegativeCacheDuration(), 5 * time.Minute},
		{"comment_body_max_bytes", p.CommentBodyMaxBytes(), 1024},
		{"notification_rate_limit", p.NotificationRateLimit(), 5},
		{"notification_rate_window", p.NotificationRateWindowDuration(), 30 * time.Minute},
		{"untrusted_author_prompt_rate", p.UntrustedAuthorPromptRate(), 5},
		{"untrusted_author_prompt_window", p.UntrustedAuthorPromptWindowDuration(), 30 * time.Minute},
		{"max_prompted_authors", p.MaxPromptedAuthors(), 5000},
		{"kick_cooldown", p.KickCooldownDuration(), 3 * time.Second},
		{"kick_channel_size", p.KickChannelSize(), 64},
		{"kicked_no_pr_backoff", p.KickedNoPRBackoffDuration(), 20 * time.Second},
		{"ref_reconcile_interval", p.RefReconcileIntervalDuration(), 2 * time.Second},
		{"ref_debounce", p.RefDebounceDuration(), 750 * time.Millisecond},
		{"gh_timeout", p.GHTimeoutDuration(), 5 * time.Second},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s default = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestPRWatchAdvancedEmbeddedDefaults is the drift guard (epic #1230 pattern): the
// advanced tuning defaults must live in the embedded default_config.toml, not only
// as Go fallback literals — otherwise `gr config show/diff/reset` would omit them.
// It asserts the RAW fields parsed from the embedded TOML (not just the accessors,
// which pass whether the value comes from TOML or the Go fallback).
func TestPRWatchAdvancedEmbeddedDefaults(t *testing.T) {
	a := Default().PRWatch.Advanced

	strChecks := map[string]struct{ got, want string }{
		"base_tick":                      {a.BaseTick, "15s"},
		"no_pr_negative_cache":           {a.NoPRNegativeCache, "5m"},
		"notification_rate_window":       {a.NotificationRateWindow, "30m"},
		"untrusted_author_prompt_window": {a.UntrustedAuthorPromptWindow, "30m"},
		"kick_cooldown":                  {a.KickCooldown, "3s"},
		"kicked_no_pr_backoff":           {a.KickedNoPRBackoff, "20s"},
		"ref_reconcile_interval":         {a.RefReconcileInterval, "2s"},
		"ref_debounce":                   {a.RefDebounce, "750ms"},
		"gh_timeout":                     {a.GHTimeout, "5s"},
	}
	for name, c := range strChecks {
		if c.got != c.want {
			t.Errorf("Default().PRWatch.Advanced.%s = %q, want %q (missing from default_config.toml?)", name, c.got, c.want)
		}
	}

	intChecks := map[string]struct{ got, want int }{
		"batch_size":                   {a.BatchSize, 3},
		"comment_body_max_bytes":       {a.CommentBodyMaxBytes, 1024},
		"notification_rate_limit":      {a.NotificationRateLimit, 5},
		"untrusted_author_prompt_rate": {a.UntrustedAuthorPromptRate, 5},
		"max_prompted_authors":         {a.MaxPromptedAuthors, 5000},
		"kick_channel_size":            {a.KickChannelSize, 64},
	}
	for name, c := range intChecks {
		if c.got != c.want {
			t.Errorf("Default().PRWatch.Advanced.%s = %d, want %d (missing from default_config.toml?)", name, c.got, c.want)
		}
	}
}

// TestPRWatchAdvancedParsing covers parsing an explicit [pr_watch.advanced] block
// through Load(): set values override, and the accessors reflect the overrides.
func TestPRWatchAdvancedParsing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	body := "[pr_watch]\nenabled = true\n\n[pr_watch.advanced]\n" +
		"base_tick = \"45s\"\nbatch_size = 7\ngh_timeout = \"9s\"\n" +
		"comment_body_max_bytes = 256\nkick_channel_size = 8\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	pw := cfg.PRWatch
	if got := pw.BaseTickDuration(); got != 45*time.Second {
		t.Errorf("BaseTickDuration = %v, want 45s", got)
	}

	if got := pw.BatchSize(); got != 7 {
		t.Errorf("BatchSize = %d, want 7", got)
	}

	if got := pw.GHTimeoutDuration(); got != 9*time.Second {
		t.Errorf("GHTimeoutDuration = %v, want 9s", got)
	}

	if got := pw.CommentBodyMaxBytes(); got != 256 {
		t.Errorf("CommentBodyMaxBytes = %d, want 256", got)
	}

	if got := pw.KickChannelSize(); got != 8 {
		t.Errorf("KickChannelSize = %d, want 8", got)
	}

	// An unset advanced key in the same block still resolves to its default.
	if got := pw.RefDebounceDuration(); got != 750*time.Millisecond {
		t.Errorf("RefDebounceDuration (unset) = %v, want 750ms", got)
	}
}

func TestPRWatchKickChannelSizeSafety(t *testing.T) {
	t.Run("maximum accepted", func(t *testing.T) {
		cfg := Default()
		cfg.PRWatch.Advanced.KickChannelSize = PRWatchKickChannelSizeMax

		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() rejected exact maximum: %v", err)
		}

		if got := cfg.PRWatch.KickChannelSize(); got != PRWatchKickChannelSizeMax {
			t.Errorf("KickChannelSize() = %d, want maximum %d", got, PRWatchKickChannelSizeMax)
		}
	})

	t.Run("maximum plus one rejected and capped", func(t *testing.T) {
		cfg := Default()
		cfg.PRWatch.Advanced.KickChannelSize = PRWatchKickChannelSizeMax + 1

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "pr_watch.advanced.kick_channel_size") {
			t.Fatalf("Validate() = %v, want field-specific maximum error", err)
		}

		if got := cfg.PRWatch.KickChannelSize(); got != PRWatchKickChannelSizeMax {
			t.Errorf("defensive KickChannelSize() = %d, want cap %d", got, PRWatchKickChannelSizeMax)
		}
	})
}

// TestPRWatchTickerCadencesRejectNonPositive proves the two advanced cadences
// that feed time.NewTicker fall back to their defaults for "0", "0s", and
// negative durations, so a valid-parsing but non-positive config can never make
// the daemon construct a non-positive ticker (issue #1285).
func TestPRWatchTickerCadencesRejectNonPositive(t *testing.T) {
	for _, bad := range []string{"0", "0s", "-1s", "-5m"} {
		p := PRWatchConfig{Advanced: PRWatchAdvancedConfig{
			BaseTick:             bad,
			RefReconcileInterval: bad,
		}}

		if got := p.BaseTickDuration(); got != 15*time.Second {
			t.Errorf("BaseTickDuration(%q) = %v, want 15s default", bad, got)
		}

		if got := p.RefReconcileIntervalDuration(); got != 2*time.Second {
			t.Errorf("RefReconcileIntervalDuration(%q) = %v, want 2s default", bad, got)
		}

		if p.BaseTickDuration() <= 0 || p.RefReconcileIntervalDuration() <= 0 {
			t.Errorf("cadence for %q resolved non-positive; time.NewTicker would panic", bad)
		}
	}
}

// TestPRWatchTopLevelDurationSafety covers the top-level [pr_watch] poll cadences
// and the anti-flood debounce (issue #1304). Directly-constructed bad configs
// resolve to positive defaults (defensive accessor fallback); loaded configs
// reject the same unsafe values with a field-specific error, and the positive 1ns
// boundary is accepted. None of these fields has a zero-disable contract, so a
// non-positive value must fail closed rather than build a non-positive ticker or
// silently drop the per-session cooldown.
func TestPRWatchTopLevelDurationSafety(t *testing.T) {
	tests := []struct {
		name string
		set  func(*PRWatchConfig, string)
		get  func(PRWatchConfig) time.Duration
		def  time.Duration
	}{
		{"poll_pending", func(p *PRWatchConfig, v string) { p.PollPending = v }, PRWatchConfig.PollPendingDuration, 30 * time.Second},
		{"poll_terminal", func(p *PRWatchConfig, v string) { p.PollTerminal = v }, PRWatchConfig.PollTerminalDuration, 5 * time.Minute},
		{"poll_merged", func(p *PRWatchConfig, v string) { p.PollMerged = v }, PRWatchConfig.PollMergedDuration, 15 * time.Minute},
		{"debounce", func(p *PRWatchConfig, v string) { p.Debounce = v }, PRWatchConfig.DebounceDuration, 2 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, bad := range []string{"0s", "-1ns", "haar"} {
				var direct PRWatchConfig
				tt.set(&direct, bad)

				if got := tt.get(direct); got != tt.def {
					t.Errorf("accessor(%q) = %v, want defensive default %v", bad, got, tt.def)
				}

				if tt.get(direct) <= 0 {
					t.Errorf("accessor(%q) resolved non-positive; a ticker/debounce would be invalid", bad)
				}

				cfg := Default()
				tt.set(&cfg.PRWatch, bad)

				err := cfg.Validate()
				if err == nil || !strings.Contains(err.Error(), "pr_watch."+tt.name) {
					t.Errorf("Validate(%q) = %v, want field-specific pr_watch.%s error", bad, err, tt.name)
				}
			}

			cfg := Default()
			tt.set(&cfg.PRWatch, "1ns")

			if err := cfg.Validate(); err != nil {
				t.Fatalf("positive 1ns boundary rejected: %v", err)
			}

			if got := tt.get(cfg.PRWatch); got != time.Nanosecond {
				t.Errorf("positive boundary accessor = %v, want 1ns", got)
			}
		})
	}
}

// TestPRWatchAdvancedDurationSafety covers every advanced duration accessor and
// its load-time boundary validation. Directly-constructed bad configs resolve to
// positive defaults; loaded configs reject the same unsafe values.
func TestPRWatchAdvancedDurationSafety(t *testing.T) {
	tests := []struct {
		name string
		set  func(*PRWatchAdvancedConfig, string)
		get  func(PRWatchConfig) time.Duration
		def  time.Duration
	}{
		{"base_tick", func(a *PRWatchAdvancedConfig, v string) { a.BaseTick = v }, PRWatchConfig.BaseTickDuration, 15 * time.Second},
		{"no_pr_negative_cache", func(a *PRWatchAdvancedConfig, v string) { a.NoPRNegativeCache = v }, PRWatchConfig.NoPRNegativeCacheDuration, 5 * time.Minute},
		{"notification_rate_window", func(a *PRWatchAdvancedConfig, v string) { a.NotificationRateWindow = v }, PRWatchConfig.NotificationRateWindowDuration, 30 * time.Minute},
		{"untrusted_author_prompt_window", func(a *PRWatchAdvancedConfig, v string) { a.UntrustedAuthorPromptWindow = v }, PRWatchConfig.UntrustedAuthorPromptWindowDuration, 30 * time.Minute},
		{"kick_cooldown", func(a *PRWatchAdvancedConfig, v string) { a.KickCooldown = v }, PRWatchConfig.KickCooldownDuration, 3 * time.Second},
		{"kicked_no_pr_backoff", func(a *PRWatchAdvancedConfig, v string) { a.KickedNoPRBackoff = v }, PRWatchConfig.KickedNoPRBackoffDuration, 20 * time.Second},
		{"ref_reconcile_interval", func(a *PRWatchAdvancedConfig, v string) { a.RefReconcileInterval = v }, PRWatchConfig.RefReconcileIntervalDuration, 2 * time.Second},
		{"ref_debounce", func(a *PRWatchAdvancedConfig, v string) { a.RefDebounce = v }, PRWatchConfig.RefDebounceDuration, 750 * time.Millisecond},
		{"gh_timeout", func(a *PRWatchAdvancedConfig, v string) { a.GHTimeout = v }, PRWatchConfig.GHTimeoutDuration, 5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, bad := range []string{"0s", "-1ns"} {
				var direct PRWatchConfig
				tt.set(&direct.Advanced, bad)

				if got := tt.get(direct); got != tt.def {
					t.Errorf("accessor(%q) = %v, want defensive default %v", bad, got, tt.def)
				}

				cfg := Default()
				tt.set(&cfg.PRWatch.Advanced, bad)

				err := cfg.Validate()
				if err == nil || !strings.Contains(err.Error(), "pr_watch.advanced."+tt.name) {
					t.Errorf("Validate(%q) = %v, want field-specific error", bad, err)
				}
			}

			cfg := Default()
			tt.set(&cfg.PRWatch.Advanced, "1ns")

			if err := cfg.Validate(); err != nil {
				t.Fatalf("positive boundary rejected: %v", err)
			}

			if got := tt.get(cfg.PRWatch); got != time.Nanosecond {
				t.Errorf("positive boundary accessor = %v, want 1ns", got)
			}
		})
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
