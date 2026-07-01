package daemon

import (
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
	// notify_ci_failures on, notify_review_comments off → CI notifies, comment does not.
	sm := newPRWatchSM()
	cfg := &config.PRWatchConfig{Enabled: true, NotifyCIFailures: true, NotifyReviewComments: false}
	t1 := prWatchTarget{id: "canny", branch: "canny"}

	// Prime passing with no comments.
	sm.diffAndBuild(cfg, t1, "croft/loch", prData{Number: 2, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true})

	// New comment arrives but comments are gated off → no notify.
	out := sm.diffAndBuild(cfg, t1, "croft/loch", prData{
		Number: 2, State: "open", HeadRefOid: "sha1", CIState: "passing", CommentsOK: true,
		IssueComments: []ghComment{{ID: 500, User: ghUser{Login: "hamish"}, Body: "a nit"}},
	})
	if len(out) != 0 {
		t.Fatalf("review comment with gate off should not notify, got %v", out)
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
		if _, ok := sm.gate(cfg, "fash", cur); ok {
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
		if _, ok := sm.gate(cfg, "fash", cur); ok {
			allowed++
		}
	}

	if allowed != 2 {
		t.Errorf("per-SHA cap should allow 2, allowed %d", allowed)
	}
}
