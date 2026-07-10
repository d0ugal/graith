package daemon

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func newPRWatchSM() *SessionManager {
	return &SessionManager{prWatch: newPRWatchState()}
}

func allOnConfig() *config.PRWatchConfig {
	return &config.PRWatchConfig{
		Enabled:               true,
		NotifyCIFailures:      true,
		NotifyMergeConflicts:  true,
		NotifyReviewComments:  true,
		NotifyPRComments:      true,
		NotifyReviewDecisions: true,
		NotifyPRLifecycle:     true,
	}
}

func TestDiffAndBuild_MergeConflictTransition(t *testing.T) {
	sm := newPRWatchSM()
	cfg := allOnConfig()
	t1 := prWatchTarget{id: "scunner", branch: "scunner"}

	// Prime: mergeable, passing CI, no comments.
	sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 4, State: "open", HeadRefOid: "sha1", CIState: "passing",
		Mergeable: "MERGEABLE", CommentsOK: true,
	})

	// UNKNOWN must NOT notify (GitHub still computing) and must not reset the cursor.
	if out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 4, State: "open", HeadRefOid: "sha1", CIState: "passing",
		Mergeable: "UNKNOWN", CommentsOK: true,
	}); len(out) != 0 {
		t.Fatalf("UNKNOWN mergeability should not notify, got %v", out)
	}

	// MERGEABLE -> CONFLICTING notifies once, with directive framing.
	out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 4, State: "open", HeadRefOid: "sha1", CIState: "passing",
		Mergeable: "CONFLICTING", CommentsOK: true,
	})
	if len(out) != 1 || !strings.Contains(out[0], "merge conflicts") {
		t.Fatalf("conflict transition should notify, got %v", out)
	}

	if !strings.Contains(out[0], "Rebase") {
		t.Errorf("conflict notice should be directive (rebase), got: %s", out[0])
	}

	// Still conflicting -> no re-notify.
	if out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 4, State: "open", HeadRefOid: "sha1", CIState: "passing",
		Mergeable: "CONFLICTING", CommentsOK: true,
	}); len(out) != 0 {
		t.Fatalf("repeat conflict should not re-notify, got %v", out)
	}
}

// TestDiffAndBuild_ConflictNotMaskedByExhaustedCap reproduces issue #771: once
// the per-SHA notification cap is exhausted by informational notices, a conflict
// detected afterwards must still be delivered (it's a directive signal that
// auto-resumes the agent), not permanently masked on every subsequent poll.
func TestDiffAndBuild_ConflictNotMaskedByExhaustedCap(t *testing.T) {
	sm := newPRWatchSM()
	cfg := allOnConfig()
	cfg.Debounce = "0s"           // drive multiple polls within the test's instant
	cfg.MaxNotificationsPerPR = 1 // one informational notice exhausts the cap
	t1 := prWatchTarget{id: "thrawn", branch: "thrawn"}

	// Prime: mergeable, passing CI, no comments.
	sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 7, State: "open", HeadRefOid: "sha1", CIState: "passing",
		Mergeable: "MERGEABLE", CommentsOK: true,
	})

	// An informational review comment consumes the single cap slot.
	out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 7, State: "open", HeadRefOid: "sha1", CIState: "passing", Mergeable: "MERGEABLE",
		IssueComments: []ghComment{{ID: 1, User: ghUser{Login: "ailsa"}, Body: "nit"}}, CommentsOK: true,
	})
	if len(out) != 1 || !strings.Contains(out[0], "conversation activity") {
		t.Fatalf("PR comment should notify and consume the cap, got %v", out)
	}

	// Cap is now exhausted. A conflict appears on the same head SHA — it must
	// still be delivered rather than rejected with "cap" forever.
	out = sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 7, State: "open", HeadRefOid: "sha1", CIState: "passing",
		Mergeable: "CONFLICTING", CommentsOK: true,
	})
	if len(out) != 1 || !strings.Contains(out[0], "merge conflicts") {
		t.Fatalf("conflict must be delivered despite exhausted cap, got %v", out)
	}

	// And once delivered, the cursor advances so it does not re-spam.
	if out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 7, State: "open", HeadRefOid: "sha1", CIState: "passing",
		Mergeable: "CONFLICTING", CommentsOK: true,
	}); len(out) != 0 {
		t.Fatalf("repeat conflict should not re-notify after delivery, got %v", out)
	}
}

// TestDiffAndBuild_PrimeDirectiveNotRepeatedWhileDegraded guards the priming
// path: while comment fetches keep degrading (CommentsOK=false), cur.primed
// stays false and the priming branch runs every poll. A directive (CI failure /
// conflict) already delivered must NOT re-fire on subsequent polls — since
// directives bypass the per-SHA cap, a missing transition guard would let them
// repeat until the rate-limit. Each is delivered exactly once.
func TestDiffAndBuild_PrimeDirectiveNotRepeatedWhileDegraded(t *testing.T) {
	sm := newPRWatchSM()
	cfg := allOnConfig()
	cfg.Debounce = "0s" // drive multiple polls within the test's instant
	t1 := prWatchTarget{id: "haar", branch: "haar"}

	d := prData{
		Number: 11, State: "open", HeadRefOid: "sha1",
		CIState: "failing", FailingChecks: []string{"build"},
		Mergeable: "CONFLICTING", CommentsOK: false, // degraded — never primes
	}

	// Poll 1: CI failure delivered first (takes priority).
	out := sm.diffAndBuild(cfg, t1, "croft/loch", d)
	if len(out) != 1 || !strings.Contains(out[0], "CI failed") {
		t.Fatalf("poll 1 should deliver CI failure, got %v", out)
	}

	// Poll 2: CI already delivered (no newly-failing check), so the conflict
	// surfaces instead of the CI notice repeating.
	out = sm.diffAndBuild(cfg, t1, "croft/loch", d)
	if len(out) != 1 || !strings.Contains(out[0], "merge conflicts") {
		t.Fatalf("poll 2 should deliver the deferred conflict, got %v", out)
	}

	// Poll 3+: both directives already delivered — nothing repeats despite still
	// being in the priming branch (CommentsOK stays false).
	for i := 0; i < 3; i++ {
		if out := sm.diffAndBuild(cfg, t1, "croft/loch", d); len(out) != 0 {
			t.Fatalf("degraded prime must not repeat delivered directives, got %v", out)
		}
	}
}

// A persistent conflict across a push (new head SHA) is intentionally
// suppressed: cur.mergeable is not reset on a new SHA and UNKNOWN is never
// stored, so re-notifying only happens after a confirmed MERGEABLE is observed.
func TestDiffAndBuild_PersistentConflictAcrossPushSuppressed(t *testing.T) {
	sm := newPRWatchSM()
	cfg := allOnConfig()
	cfg.Debounce = "0s" // this test fires two conflict notices; don't debounce them
	t1 := prWatchTarget{id: "bide", branch: "bide"}

	// Prime mergeable, then transition to CONFLICTING (notifies once).
	sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 10, State: "open", HeadRefOid: "sha1", CIState: "passing",
		Mergeable: "MERGEABLE", CommentsOK: true,
	})

	if out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 10, State: "open", HeadRefOid: "sha1", CIState: "passing",
		Mergeable: "CONFLICTING", CommentsOK: true,
	}); len(out) != 1 || !strings.Contains(out[0], "merge conflicts") {
		t.Fatalf("first conflict should notify, got %v", out)
	}

	// Push (new head SHA) while still conflicting. GitHub reports UNKNOWN first
	// (never stored), then CONFLICTING again. Neither re-notifies: without an
	// intervening MERGEABLE the cursor stays CONFLICTING.
	if out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 10, State: "open", HeadRefOid: "sha2", CIState: "passing",
		Mergeable: "UNKNOWN", CommentsOK: true,
	}); len(out) != 0 {
		t.Fatalf("UNKNOWN after push should not notify, got %v", out)
	}

	if out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 10, State: "open", HeadRefOid: "sha2", CIState: "passing",
		Mergeable: "CONFLICTING", CommentsOK: true,
	}); len(out) != 0 {
		t.Fatalf("still-conflicting re-push should be suppressed, got %v", out)
	}

	// Only a confirmed MERGEABLE re-arms the notification.
	sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 10, State: "open", HeadRefOid: "sha2", CIState: "passing",
		Mergeable: "MERGEABLE", CommentsOK: true,
	})

	if out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 10, State: "open", HeadRefOid: "sha2", CIState: "passing",
		Mergeable: "CONFLICTING", CommentsOK: true,
	}); len(out) != 1 || !strings.Contains(out[0], "merge conflicts") {
		t.Fatalf("conflict after confirmed MERGEABLE should re-notify, got %v", out)
	}
}

func TestDiffAndBuild_PrimeConflictNotMaskedByCIFailure(t *testing.T) {
	// On the priming poll a PR is BOTH failing CI and conflicting. CI takes
	// priority and is delivered first; the conflict must NOT be permanently
	// masked — it re-fires from the steady-state path on the next poll.
	sm := newPRWatchSM()
	cfg := allOnConfig()
	cfg.Debounce = "0s" // so the deferred conflict isn't debounced within the test's instant
	t1 := prWatchTarget{id: "bothy", branch: "bothy"}

	out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 8, State: "open", HeadRefOid: "sha1",
		CIState: "failing", FailingChecks: []string{"build"},
		Mergeable: "CONFLICTING", CommentsOK: true,
	})
	if len(out) != 1 || !strings.Contains(out[0], "CI failed") {
		t.Fatalf("prime should deliver the CI failure first, got %v", out)
	}

	// Next poll: CI still failing (already delivered, no re-notify), conflict
	// must now surface since it was deferred, not lost.
	out = sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 8, State: "open", HeadRefOid: "sha1",
		CIState: "failing", FailingChecks: []string{"build"},
		Mergeable: "CONFLICTING", CommentsOK: true,
	})
	if len(out) != 1 || !strings.Contains(out[0], "merge conflicts") {
		t.Fatalf("deferred conflict should fire on the next poll, got %v", out)
	}
}

func TestDiffAndBuild_MergeConflictGatedOff(t *testing.T) {
	sm := newPRWatchSM()
	cfg := &config.PRWatchConfig{Enabled: true, NotifyMergeConflicts: false}
	t1 := prWatchTarget{id: "thrawn", branch: "thrawn"}
	sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 6, State: "open", HeadRefOid: "sha1", CIState: "passing",
		Mergeable: "MERGEABLE", CommentsOK: true,
	})

	if out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 6, State: "open", HeadRefOid: "sha1", CIState: "passing",
		Mergeable: "CONFLICTING", CommentsOK: true,
	}); len(out) != 0 {
		t.Fatalf("conflict with gate off should not notify, got %v", out)
	}
}

func TestDiffAndBuild_PrimeThenNoReNotify(t *testing.T) {
	sm := newPRWatchSM()
	cfg := allOnConfig()
	t1 := prWatchTarget{id: "bide", name: "bide", branch: "bide"}
	d := prData{
		Number: 7, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true,
		IssueComments: []ghComment{{ID: 100, User: ghUser{Login: "ailsa"}, Body: "old"}},
	}
	// First poll primes the baseline — existing comment must NOT notify.
	if out := sm.diffAndBuild(cfg, t1, "croft/loch", d); len(out) != 0 {
		t.Fatalf("prime poll should not notify, got %v", out)
	}
	// Same state again — no notification.
	if out := sm.diffAndBuild(cfg, t1, "croft/loch", d); len(out) != 0 {
		t.Fatalf("unchanged poll should not notify, got %v", out)
	}
}

func TestDiffAndBuild_PrimeNotifiesCurrentCIFailure(t *testing.T) {
	// A restart that primes against an already-failing CI must still wake the agent.
	sm := newPRWatchSM()
	cfg := allOnConfig()
	t1 := prWatchTarget{id: "haar", branch: "haar"}
	d := prData{Number: 9, State: "open", HeadRefOid: "sha1", CIState: "failing", FailingChecks: []string{"build"}, CommentsOK: true}

	out := sm.diffAndBuild(cfg, t1, "croft/loch", d)
	if len(out) != 1 || !strings.Contains(out[0], "CI failed") {
		t.Fatalf("prime against failing CI should notify, got %v", out)
	}
}

func TestDiffAndBuild_PrimeMechanicalNoticesDedupWhileCommentsDegraded(t *testing.T) {
	// Regression for #772: if the comment fetch is persistently degraded
	// (CommentsOK == false) the session never primes, so every poll re-enters the
	// unprimed branch. The mechanical notices (failing CI, conflict) must still
	// fire exactly once each — deduped against cur.failing / cur.mergeable —
	// rather than re-firing on every poll.
	sm := newPRWatchSM()
	cfg := allOnConfig()
	cfg.Debounce = "0s" // so a deferred/second notice isn't debounced within the test
	t1 := prWatchTarget{id: "dreich", branch: "dreich"}

	d := prData{
		Number: 42, State: "open", HeadRefOid: "sha1",
		CIState: "failing", FailingChecks: []string{"build"},
		Mergeable: "CONFLICTING", CommentsOK: false,
	}

	// Poll 1: CI failure fires first (never primes because CommentsOK == false).
	out := sm.diffAndBuild(cfg, t1, "croft/loch", d)
	if len(out) != 1 || !strings.Contains(out[0], "CI failed") {
		t.Fatalf("first unprimed poll should deliver the CI failure, got %v", out)
	}

	// Poll 2: CI already delivered (no re-fire); the deferred conflict now surfaces.
	out = sm.diffAndBuild(cfg, t1, "croft/loch", d)
	if len(out) != 1 || !strings.Contains(out[0], "merge conflicts") {
		t.Fatalf("second unprimed poll should deliver the deferred conflict, got %v", out)
	}

	// Poll 3+: both mechanical notices already delivered — still unprimed, but no
	// notice should re-fire. Before the fix these re-fired on every poll.
	for i := 0; i < 3; i++ {
		if out := sm.diffAndBuild(cfg, t1, "croft/loch", d); len(out) != 0 {
			t.Fatalf("unprimed poll %d should not re-fire mechanical notices, got %v", i+3, out)
		}
	}
}

func TestDiffAndBuild_PrimeCIReFailsAfterRecoveryWhileCommentsDegraded(t *testing.T) {
	// While the comment fetch stays degraded (never primes), a same-SHA CI
	// failing -> passing -> failing sequence must re-notify on the re-failure:
	// the unprimed CI dedup set (cur.failing) has to be cleared when CI goes
	// green, mirroring the steady-state reset. Otherwise the re-failure is
	// silently deduped and a stopped agent is stranded on a red build.
	sm := newPRWatchSM()
	cfg := allOnConfig()
	cfg.Debounce = "0s"
	t1 := prWatchTarget{id: "fash", branch: "fash"}

	failing := prData{
		Number: 43, State: "open", HeadRefOid: "sha1",
		CIState: "failing", FailingChecks: []string{"build"}, CommentsOK: false,
	}
	passing := prData{
		Number: 43, State: "open", HeadRefOid: "sha1",
		CIState: "passing", CommentsOK: false,
	}

	// Poll 1: failing -> notify.
	if out := sm.diffAndBuild(cfg, t1, "croft/loch", failing); len(out) != 1 || !strings.Contains(out[0], "CI failed") {
		t.Fatalf("first failing poll should notify, got %v", out)
	}
	// Poll 2: passing -> no notice, but clears the dedup set.
	if out := sm.diffAndBuild(cfg, t1, "croft/loch", passing); len(out) != 0 {
		t.Fatalf("passing poll should not notify while unprimed, got %v", out)
	}
	// Poll 3: same SHA fails again -> must re-notify (not deduped).
	if out := sm.diffAndBuild(cfg, t1, "croft/loch", failing); len(out) != 1 || !strings.Contains(out[0], "CI failed") {
		t.Fatalf("re-failure on the same SHA should re-notify, got %v", out)
	}
}

func TestDiffAndBuild_CITransitionAndDedup(t *testing.T) {
	sm := newPRWatchSM()
	cfg := allOnConfig()
	t1 := prWatchTarget{id: "thrawn", branch: "thrawn"}

	// Prime: passing.
	sm.diffAndBuild(cfg, t1, "croft/loch", prData{Number: 1, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true})

	// Transition to failing → notify once.
	out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{Number: 1, State: "open", HeadRefOid: "sha1", CIState: "failing", FailingChecks: []string{"build"}, CommentsOK: true})
	if len(out) != 1 {
		t.Fatalf("pass→fail should notify once, got %v", out)
	}
	// Same failure again → debounced/dedup, no new notify.
	out = sm.diffAndBuild(cfg, t1, "croft/loch", prData{Number: 1, State: "open", HeadRefOid: "sha1", CIState: "failing", FailingChecks: []string{"build"}, CommentsOK: true})
	if len(out) != 0 {
		t.Fatalf("repeat of same failure should not re-notify, got %v", out)
	}
}

func TestDiffAndBuild_GatesCIvsComments(t *testing.T) {
	// notify_ci_failures on, both comment gates off → CI notifies, comment does not.
	sm := newPRWatchSM()
	cfg := &config.PRWatchConfig{Enabled: true, NotifyCIFailures: true, NotifyReviewComments: false, NotifyPRComments: false}
	t1 := prWatchTarget{id: "canny", branch: "canny"}

	// Prime passing with no comments.
	sm.diffAndBuild(cfg, t1, "croft/loch", prData{Number: 2, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true})

	// A conversation comment arrives but notify_pr_comments is off → no notify.
	out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 2, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true,
		IssueComments: []ghComment{{ID: 500, User: ghUser{Login: "hamish"}, Body: "a nit"}},
	})
	if len(out) != 0 {
		t.Fatalf("PR comment with gate off should not notify, got %v", out)
	}
}

func TestDiffAndBuild_ReviewCommentAwarenessFraming(t *testing.T) {
	sm := newPRWatchSM()
	cfg := allOnConfig()
	t1 := prWatchTarget{id: "wynd", branch: "wynd"}
	sm.diffAndBuild(cfg, t1, "croft/loch", prData{Number: 3, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true})

	out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 3, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true,
		ReviewComments: []ghComment{{ID: 9001, User: ghUser{Login: "coderabbitai[bot]"}, Body: "consider X", Path: "a.go", Line: 4}},
	})
	if len(out) != 1 {
		t.Fatalf("new review comment should notify once, got %v", out)
	}
	// Awareness framing: must NOT be an imperative; must hand the decision to the agent.
	body := out[0]
	if strings.Contains(strings.ToLower(body), "fix the failures") {
		t.Error("review-comment notice must not use CI's imperative framing")
	}

	if !strings.Contains(body, "Consider whether") {
		t.Errorf("review-comment notice should use awareness framing, got: %s", body)
	}
}

func TestDiffAndBuild_PRCommentAwarenessFraming(t *testing.T) {
	// A regular conversation comment (issues/{n}/comments) must notify under
	// notify_pr_comments with awareness framing and a body that clearly marks it
	// as a conversation comment, distinct from an inline review comment.
	sm := newPRWatchSM()
	cfg := allOnConfig()
	t1 := prWatchTarget{id: "blether", branch: "blether"}
	sm.diffAndBuild(cfg, t1, "croft/loch", prData{Number: 3, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true})

	out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 3, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true,
		IssueComments: []ghComment{{ID: 42, User: ghUser{Login: "hamish"}, Body: "ship it"}},
	})
	if len(out) != 1 {
		t.Fatalf("new PR conversation comment should notify once, got %v", out)
	}

	body := out[0]
	if !strings.Contains(body, "conversation") {
		t.Errorf("PR-comment notice should identify itself as a conversation comment, got: %s", body)
	}

	if strings.Contains(body, "inline code-review") {
		t.Errorf("PR-comment notice must not be labelled as an inline review comment, got: %s", body)
	}

	if !strings.Contains(body, "Consider whether") {
		t.Errorf("PR-comment notice should use awareness framing, got: %s", body)
	}
}

// TestDiffAndBuild_CommentGatesIndependent asserts that the two comment gates
// are truly independent: with only notify_review_comments on, an inline review
// comment notifies but a conversation comment does not, and vice versa. Each
// gate touches only its own cursor — the enabled surface advances on delivery,
// the disabled surface's cursor is kept current (baselined) so enabling it
// later doesn't dump backlog.
func TestDiffAndBuild_CommentGatesIndependent(t *testing.T) {
	cases := []struct {
		name         string
		id           string
		reviewOn     bool
		prOn         bool
		wantContains string // marker for the surface that should notify
	}{
		{"review-only", "canny", true, false, "inline code-review"},
		{"pr-only", "ken", false, true, "conversation"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sm := newPRWatchSM()
			cfg := &config.PRWatchConfig{Enabled: true, NotifyReviewComments: tc.reviewOn, NotifyPRComments: tc.prOn, Debounce: "0s"}
			t1 := prWatchTarget{id: tc.id, branch: tc.id}
			sm.diffAndBuild(cfg, t1, "croft/loch", prData{Number: 1, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true})

			// Both surfaces have a new comment; only the enabled gate should fire.
			out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
				Number: 1, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true,
				IssueComments:  []ghComment{{ID: 10, User: ghUser{Login: "hamish"}, Body: "ship it"}},
				ReviewComments: []ghComment{{ID: 20, User: ghUser{Login: "ailsa"}, Body: "nit", Path: "a.go", Line: 4}},
			})
			if len(out) != 1 || !strings.Contains(out[0], tc.wantContains) {
				t.Fatalf("only the %s comment should notify, got %v", tc.wantContains, out)
			}
		})
	}
}

// TestDiffAndBuild_BothGatesSamePollDefersNotDrops covers the new interaction
// introduced by the split: with both gates on and new comments on both surfaces
// in one poll, the review comment (evaluated first) consumes the single debounce
// slot and the conversation comment is debounced. The deferred comment must not
// be dropped — its cursor stays un-advanced, so a later poll delivers it.
func TestDiffAndBuild_BothGatesSamePollDefersNotDrops(t *testing.T) {
	sm := newPRWatchSM()
	cfg := allOnConfig() // default 2m debounce
	t1 := prWatchTarget{id: "kirk", branch: "kirk"}
	sm.diffAndBuild(cfg, t1, "croft/loch", prData{Number: 1, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true})

	both := prData{
		Number: 1, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true,
		ReviewComments: []ghComment{{ID: 20, User: ghUser{Login: "ailsa"}, Body: "nit", Path: "a.go", Line: 4}},
		IssueComments:  []ghComment{{ID: 30, User: ghUser{Login: "hamish"}, Body: "ship it"}},
	}

	out := sm.diffAndBuild(cfg, t1, "croft/loch", both)
	if len(out) != 1 || !strings.Contains(out[0], "inline code-review") {
		t.Fatalf("first poll should deliver the review comment, got %v", out)
	}

	// The delivered review cursor advances; the debounced conversation cursor must
	// stay un-advanced (deferred, not baselined away).
	sm.prWatch.mu.Lock()
	cur := sm.prWatch.cursors[t1.id]
	reviewCursor, issueCursor := cur.lastReviewCommentID, cur.lastIssueCommentID
	sm.prWatch.mu.Unlock()

	if reviewCursor != 20 {
		t.Errorf("delivered review cursor should advance to 20, got %d", reviewCursor)
	}

	if issueCursor != 0 {
		t.Errorf("debounced conversation cursor must stay un-advanced, got %d", issueCursor)
	}

	// Clear the debounce and re-poll: the deferred conversation comment now fires.
	sm.prWatch.mu.Lock()
	delete(sm.prWatch.lastSent, t1.id)
	sm.prWatch.mu.Unlock()

	out = sm.diffAndBuild(cfg, t1, "croft/loch", both)
	if len(out) != 1 || !strings.Contains(out[0], "conversation") {
		t.Fatalf("second poll should deliver the deferred conversation comment, got %v", out)
	}
}

func TestCIFailureBodyIsDirective(t *testing.T) {
	body := ciFailureBody(prWatchTarget{branch: "thrawn"}, "croft/loch",
		prData{Number: 12, CIState: "failing", FailingChecks: []string{"build", "lint"}})
	if !strings.Contains(body, "CI failed") || !strings.Contains(body, "Fix the failures") {
		t.Errorf("CI body should be directive, got: %s", body)
	}

	if !strings.Contains(body, "build") || !strings.Contains(body, "lint") {
		t.Errorf("CI body should list failing checks, got: %s", body)
	}
}

func TestGateRateLimit(t *testing.T) {
	sm := newPRWatchSM()
	cfg := &config.PRWatchConfig{Enabled: true, Debounce: "0s", MaxNotificationsPerPR: 100}
	cur := &prWatchCursor{failing: map[string]bool{}}
	allowed := 0

	for i := 0; i < 10; i++ {
		if _, ok := sm.gate(cfg, "fash", cur, false); ok {
			allowed++
		}
	}

	if allowed != 5 {
		t.Errorf("rate-limit should allow 5 per window, allowed %d", allowed)
	}
}

func TestDiffAndBuild_PrimeDefersOnCommentFetchFailure(t *testing.T) {
	// If the comment fetch degraded on the priming poll, we must NOT baseline the
	// comment cursor at 0 and must not mark primed — otherwise the next good poll
	// dumps the whole backlog as "new".
	sm := newPRWatchSM()
	cfg := allOnConfig()
	t1 := prWatchTarget{id: "dreich", branch: "dreich"}
	existing := []ghComment{{ID: 100, User: ghUser{Login: "ailsa"}, Body: "old"}}

	if out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 5, State: "open", HeadRefOid: "sha1", CIState: "passing",
		IssueComments: existing, CommentsOK: false,
	}); len(out) != 0 {
		t.Fatalf("degraded prime should not notify, got %v", out)
	}

	if out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 5, State: "open", HeadRefOid: "sha1", CIState: "passing",
		IssueComments: existing, CommentsOK: true,
	}); len(out) != 0 {
		t.Fatalf("backlog must not be dumped after a degraded prime, got %v", out)
	}
}

func TestPrunePRWatchState(t *testing.T) {
	sm := newPRWatchSM()
	sm.state = &State{Sessions: map[string]*SessionState{"braw": {ID: "braw"}}}
	sm.prWatch.cursors["braw"] = &prWatchCursor{failing: map[string]bool{}}
	sm.prWatch.cursors["thrawn"] = &prWatchCursor{failing: map[string]bool{}}
	sm.prWatch.nextPoll["thrawn"] = time.Now()
	sm.prWatch.lastSent["thrawn"] = time.Now()

	sm.prunePRWatchState()

	if _, ok := sm.prWatch.cursors["braw"]; !ok {
		t.Error("live session cursor should be retained")
	}

	if _, ok := sm.prWatch.cursors["thrawn"]; ok {
		t.Error("dead session cursor should be pruned")
	}

	if _, ok := sm.prWatch.nextPoll["thrawn"]; ok {
		t.Error("dead session nextPoll should be pruned")
	}
}

func TestGatePerSHACap(t *testing.T) {
	sm := newPRWatchSM()
	cfg := &config.PRWatchConfig{Enabled: true, Debounce: "0s", MaxNotificationsPerPR: 2}
	cur := &prWatchCursor{failing: map[string]bool{}}
	allowed := 0

	for i := 0; i < 5; i++ {
		if _, ok := sm.gate(cfg, "fash", cur, false); ok {
			allowed++
		}
	}

	if allowed != 2 {
		t.Errorf("per-SHA cap should allow 2, allowed %d", allowed)
	}
}

// TestGateDirectiveBypassesCap asserts that directive notices (CI failure, merge
// conflict) are not blocked by the per-SHA cap and do not consume it, while
// informational notices remain capped. Regression test for issue #771.
func TestGateDirectiveBypassesCap(t *testing.T) {
	sm := newPRWatchSM()
	cfg := &config.PRWatchConfig{Enabled: true, Debounce: "0s", MaxNotificationsPerPR: 1}
	cur := &prWatchCursor{failing: map[string]bool{}}

	// Exhaust the cap with one informational notice.
	if _, ok := sm.gate(cfg, "thrawn", cur, false); !ok {
		t.Fatal("first informational notice should pass")
	}

	if _, ok := sm.gate(cfg, "thrawn", cur, false); ok {
		t.Fatal("second informational notice should be capped")
	}

	// A directive notice must still get through despite the exhausted cap...
	if reason, ok := sm.gate(cfg, "thrawn", cur, true); !ok {
		t.Fatalf("directive notice must bypass the cap, got reason %q", reason)
	}

	// ...and must not have consumed the cap (notifyCount unchanged by directives).
	if cur.notifyCount != 1 {
		t.Errorf("directive notice must not increment notifyCount, got %d", cur.notifyCount)
	}
}

func newPRWatchCovSM() *SessionManager {
	return &SessionManager{
		state:   NewState(),
		prWatch: newPRWatchState(),
		cfg:     &config.Config{},
		log:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

func TestPollIntervalFor_Cov(t *testing.T) {
	cfg := &config.PRWatchConfig{
		PollTerminal: "10m",
		PollPending:  "1m",
		PollMerged:   "1h",
	}

	if got := pollIntervalFor(cfg, "merged", ""); got != time.Hour {
		t.Errorf("merged should use PollMerged, got %v", got)
	}

	if got := pollIntervalFor(cfg, "closed", "passing"); got != time.Hour {
		t.Errorf("closed should use PollMerged, got %v", got)
	}

	if got := pollIntervalFor(cfg, "open", "pending"); got != time.Minute {
		t.Errorf("open+pending should use PollPending, got %v", got)
	}

	if got := pollIntervalFor(cfg, "open", "passing"); got != 10*time.Minute {
		t.Errorf("open+passing should use PollTerminal, got %v", got)
	}
}

func TestSchedulePoll_Cov(t *testing.T) {
	sm := newPRWatchCovSM()

	before := time.Now()

	sm.schedulePoll("bide", time.Hour)

	after := time.Now()

	sm.prWatch.mu.Lock()
	next, ok := sm.prWatch.nextPoll["bide"]
	sm.prWatch.mu.Unlock()

	if !ok {
		t.Fatal("schedulePoll should record a nextPoll time")
	}

	// nextPoll must be now+1h, bracketed by the before/after snapshots — a bug
	// that used a different multiple of the interval would fall outside this window.
	if next.Before(before.Add(time.Hour)) || next.After(after.Add(time.Hour)) {
		t.Errorf("nextPoll should be ~1h out, got %v (window %v..%v)", next, before.Add(time.Hour), after.Add(time.Hour))
	}
}

func TestWriteAndClearPRState_Cov(t *testing.T) {
	sm := newPRWatchCovSM()
	sm.state.Sessions["braw"] = &SessionState{ID: "braw"}

	sm.writePRState("braw", prData{
		Number: 7, State: "open", URL: "https://example/pr/7",
		ReviewDecision: "approved", HeadRefOid: "sha1", Mergeable: "MERGEABLE",
		CIState: "failing", FailingChecks: []string{"build"},
	})

	s := sm.state.Sessions["braw"]
	if s.PullRequest.Number != 7 || s.PullRequest.State != "open" || s.PullRequest.Mergeable != "MERGEABLE" {
		t.Errorf("PR state not written: %+v", s.PullRequest)
	}

	if s.CI.State != "failing" || len(s.CI.FailingChecks) != 1 {
		t.Errorf("CI state not written: %+v", s.CI)
	}

	// An empty CIState must NOT clobber the last-known CI badge.
	sm.writePRState("braw", prData{Number: 7, State: "open", CIState: ""})

	if sm.state.Sessions["braw"].CI.State != "failing" {
		t.Errorf("empty CIState should preserve last-known CI, got %q", sm.state.Sessions["braw"].CI.State)
	}

	// Unknown session is a no-op (no panic).
	sm.writePRState("ghost", prData{Number: 1})

	// clearPRState resets both.
	sm.clearPRState("braw")

	if sm.state.Sessions["braw"].PullRequest.Number != 0 || sm.state.Sessions["braw"].CI.State != "" {
		t.Errorf("clearPRState should reset PR and CI, got %+v / %+v",
			sm.state.Sessions["braw"].PullRequest, sm.state.Sessions["braw"].CI)
	}

	sm.clearPRState("ghost") // no-op, no panic
}

func TestReviewDecisionBody_Cov(t *testing.T) {
	tgt := prWatchTarget{branch: "canny"}

	approved := reviewDecisionBody(tgt, prData{Number: 1, ReviewDecision: "approved"})
	if !strings.Contains(approved, "approved") || !strings.Contains(approved, "No action needed") {
		t.Errorf("approved body wrong: %s", approved)
	}

	changes := reviewDecisionBody(tgt, prData{Number: 2, ReviewDecision: "changes_requested"})
	if !strings.Contains(changes, "requested changes") || !strings.Contains(changes, "You decide") {
		t.Errorf("changes_requested body wrong: %s", changes)
	}

	other := reviewDecisionBody(tgt, prData{Number: 3, ReviewDecision: "review_required"})
	if !strings.Contains(other, "review status changed") || !strings.Contains(other, "review_required") {
		t.Errorf("default review body wrong: %s", other)
	}
}

func TestTruncate_Cov(t *testing.T) {
	if got := truncate("  bide  ", 10); got != "bide" {
		t.Errorf("truncate should trim whitespace, got %q", got)
	}

	if got := truncate("abcdefgh", 4); got != "abcd…" {
		t.Errorf("truncate should cut to n and add ellipsis, got %q", got)
	}

	if got := truncate("short", 100); got != "short" {
		t.Errorf("truncate under limit should be unchanged, got %q", got)
	}
}

func TestCommentsAfter_Cov(t *testing.T) {
	comments := []ghComment{
		{ID: 3}, {ID: 1}, {ID: 5}, {ID: 2},
	}

	got := commentsAfter(comments, 2)
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 5 {
		t.Errorf("commentsAfter(>2) should return sorted [3,5], got %+v", got)
	}

	// Callers only care about the count; nil vs empty is not part of the contract.
	if got := commentsAfter(comments, 100); len(got) != 0 {
		t.Errorf("commentsAfter with a cursor past all IDs should be empty, got %+v", got)
	}
}

func TestMaxInt64_Cov(t *testing.T) {
	if got := maxInt64(3, 7); got != 7 {
		t.Errorf("maxInt64(3,7) = %d, want 7", got)
	}

	if got := maxInt64(9, 2); got != 9 {
		t.Errorf("maxInt64(9,2) = %d, want 9", got)
	}
}

func TestPRInfoAndCIInfo_Cov(t *testing.T) {
	if prInfo(PRStatus{Number: 0}) != nil {
		t.Error("prInfo with number 0 should be nil")
	}

	info := prInfo(PRStatus{Number: 5, State: "open", Mergeable: "CONFLICTING", ReviewDecision: "approved"})
	if info == nil || info.Number != 5 || !info.Conflicting || info.ReviewDecision != "approved" {
		t.Errorf("prInfo wrong: %+v", info)
	}

	nonConflict := prInfo(PRStatus{Number: 6, Mergeable: "MERGEABLE"})
	if nonConflict == nil || nonConflict.Conflicting {
		t.Errorf("MERGEABLE should not be marked conflicting: %+v", nonConflict)
	}

	if ciInfo(CIStatus{State: ""}) != nil {
		t.Error("ciInfo with empty state should be nil")
	}

	ci := ciInfo(CIStatus{State: "passing", FailingChecks: nil})
	if ci == nil || ci.State != "passing" {
		t.Errorf("ciInfo wrong: %+v", ci)
	}
}

// TestPollSession_NonGitHubBacksOff verifies pollSession negative-caches a
// worktree whose origin is not a GitHub remote.
func TestPollSession_NonGitHubBacksOff_Cov(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	_ = bareDir

	// The clone's origin points at a local bare repo — not github.com — so
	// repoSlug fails and pollSession applies the no-PR negative cache.
	sm := newPRWatchCovSM()
	cfg := &config.PRWatchConfig{Enabled: true}
	tgt := prWatchTarget{id: "haar", branch: "main", worktreePath: cloneDir}

	sm.pollSession(context.Background(), cfg, tgt)

	sm.prWatch.mu.Lock()
	next, ok := sm.prWatch.nextPoll["haar"]
	sm.prWatch.mu.Unlock()

	if !ok {
		t.Fatal("non-GitHub remote should schedule a back-off poll")
	}

	if next.Before(time.Now().Add(prNoPRNegCache - time.Minute)) {
		t.Errorf("non-GitHub remote should get the long negative-cache back-off, got %v", next)
	}
}

// TestPollSession_FoundPRWritesState drives pollSession end-to-end with a mocked
// gh, against a real GitHub-style remote, and asserts it writes display state and
// schedules the next poll.
func TestPollSession_FoundPRWritesState_Cov(t *testing.T) {
	tmp := t.TempDir()
	cloneDir := tmp + "/clone"

	gitRun(t, "", "init", "--initial-branch=main", cloneDir)
	gitRun(t, cloneDir, "remote", "add", "origin", "git@github.com:croft/loch.git")

	orig := ghRunner
	defer func() { ghRunner = orig }()

	calls := 0
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		calls++
		if calls == 1 { // gh pr list
			return `[{"number":9,"state":"OPEN","isDraft":false,"url":"https://github.com/croft/loch/pull/9","headRefOid":"sha9","mergeable":"MERGEABLE"}]`, nil
		}

		return `[]`, nil // checks + comments empty
	}

	sm := newPRWatchCovSM()
	sm.state.Sessions["bonnie"] = &SessionState{ID: "bonnie"}
	cfg := &config.PRWatchConfig{Enabled: true, PollTerminal: "10m", PollPending: "1m", PollMerged: "1h"}
	tgt := prWatchTarget{id: "bonnie", branch: "bide", worktreePath: cloneDir}

	sm.pollSession(context.Background(), cfg, tgt)

	if sm.state.Sessions["bonnie"].PullRequest.Number != 9 {
		t.Errorf("pollSession should write PR #9, got %+v", sm.state.Sessions["bonnie"].PullRequest)
	}

	sm.prWatch.mu.Lock()
	_, ok := sm.prWatch.nextPoll["bonnie"]
	sm.prWatch.mu.Unlock()

	if !ok {
		t.Error("pollSession should schedule the next poll")
	}
}

// TestPollSession_NoPRClearsState covers the found=false branch: state is
// cleared and the branch is negative-cached.
func TestPollSession_NoPRClearsState_Cov(t *testing.T) {
	tmp := t.TempDir()
	cloneDir := tmp + "/clone"

	gitRun(t, "", "init", "--initial-branch=main", cloneDir)
	gitRun(t, cloneDir, "remote", "add", "origin", "https://github.com/croft/loch.git")

	orig := ghRunner
	defer func() { ghRunner = orig }()

	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		return `[]`, nil // no PR
	}

	sm := newPRWatchCovSM()
	sm.state.Sessions["ken"] = &SessionState{
		ID:          "ken",
		PullRequest: PRStatus{Number: 3},
		CI:          CIStatus{State: "passing"},
	}
	cfg := &config.PRWatchConfig{Enabled: true}
	tgt := prWatchTarget{id: "ken", branch: "bide", worktreePath: cloneDir}

	before := time.Now()

	sm.pollSession(context.Background(), cfg, tgt)

	s := sm.state.Sessions["ken"]
	if s.PullRequest.Number != 0 {
		t.Errorf("no-PR poll should clear PR state, got %+v", s.PullRequest)
	}

	if s.CI.State != "" {
		t.Errorf("no-PR poll should clear CI state, got %+v", s.CI)
	}

	// The branch should be negative-cached (long back-off), not re-polled soon.
	sm.prWatch.mu.Lock()
	next, ok := sm.prWatch.nextPoll["ken"]
	sm.prWatch.mu.Unlock()

	if !ok {
		t.Fatal("no-PR poll should schedule a back-off poll")
	}

	if next.Before(before.Add(prNoPRNegCache - time.Minute)) {
		t.Errorf("no-PR poll should apply the long negative-cache back-off, got %v", next)
	}
}

func TestPRWatchTargets_Cov(t *testing.T) {
	tmp := t.TempDir()
	cloneDir := tmp + "/clone"

	gitRun(t, "", "init", "--initial-branch=main", cloneDir)
	gitRun(t, cloneDir, "remote", "add", "origin", "git@github.com:croft/loch.git")
	// Put the worktree HEAD on the recorded branch so reconcileBranch (which
	// compares live HEAD against SessionState.Branch, #1008) is a no-op here and
	// this test stays focused on eligibility filtering.
	gitRun(t, cloneDir, "checkout", "-b", "canny-feature")

	sm := newPRWatchCovSM()

	// Eligible: running, has repo, recorded branch.
	sm.state.Sessions["braw"] = &SessionState{
		ID: "braw", Name: "braw", Status: StatusRunning,
		RepoPath: cloneDir, WorktreePath: cloneDir, Branch: "canny-feature",
	}
	// Ineligible: no repo path.
	sm.state.Sessions["norepo"] = &SessionState{ID: "norepo", Status: StatusRunning}
	// Ineligible: in-place.
	sm.state.Sessions["inplace"] = &SessionState{ID: "inplace", Status: StatusRunning, RepoPath: cloneDir, InPlace: true}
	// Ineligible: shared worktree.
	sm.state.Sessions["shared"] = &SessionState{ID: "shared", Status: StatusRunning, RepoPath: cloneDir, SharedWorktree: true}
	// Ineligible: errored status.
	sm.state.Sessions["errored"] = &SessionState{ID: "errored", Status: StatusErrored, RepoPath: cloneDir}

	targets := sm.prWatchTargets()

	if len(targets) != 1 || targets[0].id != "braw" || targets[0].branch != "canny-feature" {
		t.Fatalf("expected only 'braw' eligible with recorded branch, got %+v", targets)
	}
}

// TestRunPRWatchLoop_CancelledCtxReturns ensures the loop exits promptly when
// its context is cancelled.
func TestRunPRWatchLoop_CancelledCtxReturns_Cov(t *testing.T) {
	sm := newPRWatchCovSM()
	sm.cfg = &config.Config{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})

	go func() {
		sm.RunPRWatchLoop(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RunPRWatchLoop did not return on cancelled context")
	}
}

// TestRunPRWatchTick_DisabledAndBatch drives runPRWatchTick against several due
// targets and asserts the per-tick batch cap bounds how many are polled.
func TestRunPRWatchTick_BatchCap_Cov(t *testing.T) {
	tmp := t.TempDir()
	cloneDir := tmp + "/clone"

	gitRun(t, "", "init", "--initial-branch=main", cloneDir)
	// Non-GitHub remote → each polled session just negative-caches (no network).
	gitRun(t, cloneDir, "remote", "add", "origin", "git@example.com:croft/loch.git")

	sm := newPRWatchCovSM()
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		sm.state.Sessions[id] = &SessionState{
			ID: id, Name: id, Status: StatusRunning,
			RepoPath: cloneDir, WorktreePath: cloneDir, Branch: "canny-feature",
		}
	}

	cfg := &config.PRWatchConfig{Enabled: true}
	sm.runPRWatchTick(context.Background(), cfg)

	// At most prWatchBatchCap sessions should have been polled (scheduled).
	sm.prWatch.mu.Lock()
	polled := len(sm.prWatch.nextPoll)
	sm.prWatch.mu.Unlock()

	if polled != prWatchBatchCap {
		t.Errorf("runPRWatchTick should poll at most %d per tick, polled %d", prWatchBatchCap, polled)
	}
}
