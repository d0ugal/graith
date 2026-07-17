package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// sessionIDs pulls the ids out of a plan slice for stable comparison.
func sortedContains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}

	return false
}

func TestReconcileTracker(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	grace := 10 * time.Minute

	issue := func(key string) issueRef { return issueRef{key: key, number: 1} }
	running := func(id, key string) trackerSession {
		return trackerSession{id: id, issueKey: key, status: StatusRunning}
	}
	stopped := func(id, key string) trackerSession {
		return trackerSession{id: id, issueKey: key, status: StatusStopped}
	}

	// stop reconcile (the default reap mode) with the fixed clock.
	stop := func(active []issueRef, existing []trackerSession, obsolete map[string]time.Time, maxc int) (trackerReconcilePlan, map[string]time.Time, int) {
		return reconcileTracker(active, existing, obsolete, grace, maxc, config.TrackerReapStop, now)
	}

	t.Run("spawn a newly active issue", func(t *testing.T) {
		plan, _, capped := stop([]issueRef{issue("gh:croft#1"), issue("gh:croft#2")},
			[]trackerSession{running("braw", "gh:croft#1")}, nil, 0)

		if len(plan.spawn) != 1 || plan.spawn[0].key != "gh:croft#2" {
			t.Fatalf("expected spawn of #2, got %+v", plan.spawn)
		}

		if len(plan.reap) != 0 || len(plan.resume) != 0 || capped != 0 {
			t.Fatalf("unexpected reap/resume/capped: %+v %+v %d", plan.reap, plan.resume, capped)
		}
	})

	t.Run("no respawn while a running session exists", func(t *testing.T) {
		plan, _, _ := stop([]issueRef{issue("gh:croft#1")}, []trackerSession{running("braw", "gh:croft#1")}, nil, 0)
		if len(plan.spawn) != 0 || len(plan.resume) != 0 {
			t.Fatalf("expected no spawn/resume, got %+v %+v", plan.spawn, plan.resume)
		}
	})

	t.Run("resume a stopped session for a re-active issue", func(t *testing.T) {
		plan, _, _ := stop([]issueRef{issue("gh:croft#1")}, []trackerSession{stopped("braw", "gh:croft#1")}, nil, 0)
		if len(plan.resume) != 1 || plan.resume[0] != "braw" {
			t.Fatalf("expected resume of braw, got %+v", plan.resume)
		}

		if len(plan.spawn) != 0 {
			t.Fatalf("expected no spawn (dedup by key), got %+v", plan.spawn)
		}
	})

	t.Run("a cleanly-completed session is not resumed for a still-active issue", func(t *testing.T) {
		done := trackerSession{id: "braw", issueKey: "gh:croft#1", status: StatusStopped, completedCleanly: true}

		plan, _, _ := stop([]issueRef{issue("gh:croft#1")}, []trackerSession{done}, nil, 0)
		if len(plan.resume) != 0 || len(plan.spawn) != 0 {
			t.Fatalf("expected no resume/spawn for a completed session, got %+v %+v", plan.resume, plan.spawn)
		}
	})

	t.Run("a creating session dedups (no double spawn)", func(t *testing.T) {
		creating := trackerSession{id: "braw", issueKey: "gh:croft#1", status: StatusCreating}

		plan, _, _ := stop([]issueRef{issue("gh:croft#1")}, []trackerSession{creating}, nil, 0)
		if len(plan.spawn) != 0 || len(plan.resume) != 0 {
			t.Fatalf("a creating session must dedup, got spawn %+v resume %+v", plan.spawn, plan.resume)
		}
	})

	t.Run("an errored session is resumed (restart mid-create recovery)", func(t *testing.T) {
		errored := trackerSession{id: "braw", issueKey: "gh:croft#1", status: StatusErrored}

		plan, _, _ := stop([]issueRef{issue("gh:croft#1")}, []trackerSession{errored}, nil, 0)
		if len(plan.resume) != 1 || len(plan.spawn) != 0 {
			t.Fatalf("expected resume of errored session (no double spawn), got %+v %+v", plan.resume, plan.spawn)
		}
	})

	t.Run("obsolete issue within grace is held", func(t *testing.T) {
		obsolete := map[string]time.Time{"gh:croft#9": now.Add(-5 * time.Minute)}

		plan, newGrace, _ := stop(nil, []trackerSession{running("dreich", "gh:croft#9")}, obsolete, 0)
		if len(plan.reap) != 0 {
			t.Fatalf("expected no reap within grace, got %+v", plan.reap)
		}

		if _, ok := newGrace["gh:croft#9"]; !ok {
			t.Fatalf("grace timestamp should carry forward, got %+v", newGrace)
		}
	})

	t.Run("obsolete issue past grace is reaped", func(t *testing.T) {
		obsolete := map[string]time.Time{"gh:croft#9": now.Add(-11 * time.Minute)}

		plan, _, _ := stop(nil, []trackerSession{running("dreich", "gh:croft#9")}, obsolete, 0)
		if len(plan.reap) != 1 || plan.reap[0] != "dreich" {
			t.Fatalf("expected reap of dreich, got %+v", plan.reap)
		}
	})

	t.Run("newly obsolete issue starts the grace clock, not reaped", func(t *testing.T) {
		plan, newGrace, _ := stop(nil, []trackerSession{running("dreich", "gh:croft#9")}, nil, 0)
		if len(plan.reap) != 0 {
			t.Fatalf("expected no reap on first observation, got %+v", plan.reap)
		}

		if got := newGrace["gh:croft#9"]; !got.Equal(now) {
			t.Fatalf("grace clock should start at now, got %v", got)
		}
	})

	t.Run("re-active issue clears its grace mark", func(t *testing.T) {
		obsolete := map[string]time.Time{"gh:croft#9": now.Add(-5 * time.Minute)}

		plan, newGrace, _ := stop([]issueRef{issue("gh:croft#9")}, []trackerSession{running("dreich", "gh:croft#9")}, obsolete, 0)
		if len(plan.reap) != 0 {
			t.Fatalf("expected no reap for re-active issue, got %+v", plan.reap)
		}

		if _, ok := newGrace["gh:croft#9"]; ok {
			t.Fatalf("grace mark should be cleared for a re-active issue, got %+v", newGrace)
		}
	})

	t.Run("reap=stop on an already-stopped obsolete session is a no-op", func(t *testing.T) {
		obsolete := map[string]time.Time{"gh:croft#9": now.Add(-11 * time.Minute)}

		plan, _, _ := stop(nil, []trackerSession{stopped("napping", "gh:croft#9")}, obsolete, 0)
		if len(plan.reap) != 0 {
			t.Fatalf("stop mode must not re-reap an already-stopped session, got %+v", plan.reap)
		}
	})

	t.Run("reap=delete reaps a stopped obsolete session", func(t *testing.T) {
		obsolete := map[string]time.Time{"gh:croft#9": now.Add(-11 * time.Minute)}

		plan, _, _ := reconcileTracker(nil, []trackerSession{stopped("napping", "gh:croft#9")}, obsolete, grace, 0, config.TrackerReapDelete, now)
		if len(plan.reap) != 1 || plan.reap[0] != "napping" {
			t.Fatalf("delete mode should reap a stopped obsolete session, got %+v", plan.reap)
		}
	})

	t.Run("reap=none never reaps and keeps the slot occupied", func(t *testing.T) {
		obsolete := map[string]time.Time{"gh:croft#1": now.Add(-11 * time.Minute)}
		// One past-grace obsolete running session under reap=none + a new active
		// issue, cap 1. The obsolete session is NOT reaped and still occupies the
		// only slot, so the new issue must be capped (not spawned over the cap).
		plan, _, capped := reconcileTracker([]issueRef{issue("gh:croft#2")},
			[]trackerSession{running("keep", "gh:croft#1")}, obsolete, grace, 1, config.TrackerReapNone, now)

		if len(plan.reap) != 0 {
			t.Fatalf("reap=none must never reap, got %+v", plan.reap)
		}

		if len(plan.spawn) != 0 || capped != 1 {
			t.Fatalf("reap=none must keep the obsolete session's slot (expected 0 spawn, 1 capped), got spawn %+v capped=%d", plan.spawn, capped)
		}
	})

	t.Run("max_concurrent caps spawns", func(t *testing.T) {
		active := []issueRef{issue("gh:croft#1"), issue("gh:croft#2"), issue("gh:croft#3")}

		plan, _, capped := stop(active, []trackerSession{running("braw", "gh:croft#1")}, nil, 2)
		if len(plan.spawn) != 1 {
			t.Fatalf("expected 1 spawn under cap 2 (1 running), got %+v", plan.spawn)
		}

		if capped != 1 {
			t.Fatalf("expected 1 capped, got %d", capped)
		}
	})

	t.Run("reaping a running session frees a cap slot for a spawn", func(t *testing.T) {
		obsolete := map[string]time.Time{"gh:croft#1": now.Add(-11 * time.Minute)}

		plan, _, capped := stop([]issueRef{issue("gh:croft#2")}, []trackerSession{running("old", "gh:croft#1")}, obsolete, 1)
		if !sortedContains(plan.reap, "old") {
			t.Fatalf("expected reap of old, got %+v", plan.reap)
		}

		if len(plan.spawn) != 1 || capped != 0 {
			t.Fatalf("expected 1 spawn, 0 capped after reap freed a slot, got %+v capped=%d", plan.spawn, capped)
		}
	})

	t.Run("stopped obsolete session held within grace does not occupy a cap slot", func(t *testing.T) {
		// A stopped, held-obsolete session must not block a new spawn.
		obsolete := map[string]time.Time{"gh:croft#1": now.Add(-1 * time.Minute)}

		plan, _, capped := stop([]issueRef{issue("gh:croft#2")}, []trackerSession{stopped("napping", "gh:croft#1")}, obsolete, 1)
		if len(plan.spawn) != 1 || capped != 0 {
			t.Fatalf("expected 1 spawn under cap 1 (stopped session doesn't count), got %+v capped=%d", plan.spawn, capped)
		}
	})
}

func TestParseGitHubIssues(t *testing.T) {
	out := `[
	  {"number": 643, "title": "tracker poll", "body": "reconcile me", "url": "https://x/643",
	   "labels": [{"name": "in-progress"}, {"name": "M"}]},
	  {"number": 71, "title": "spawn from issue", "body": "", "url": "https://x/71", "labels": []}
	]`

	refs, err := parseGitHubIssues(out, "d0ugal/croft")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(refs) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(refs))
	}

	if refs[0].key != "gh:d0ugal/croft#643" {
		t.Errorf("key = %q, want gh:d0ugal/croft#643", refs[0].key)
	}

	if refs[0].title != "tracker poll" || refs[0].url != "https://x/643" {
		t.Errorf("unexpected fields: %+v", refs[0])
	}

	if len(refs[0].labels) != 2 || refs[0].labels[0] != "in-progress" {
		t.Errorf("labels = %v, want [in-progress M]", refs[0].labels)
	}
}

// TestFetchGitHubIssuesUsesConfiguredTimeout guards issue #1318: the tracker
// must route `gh issue list` through the effective configured gh timeout
// (pr_watch.advanced.gh_timeout), not the 5s package fallback. It also proves the
// value is read per-pass, so a live reload of the setting is observed.
func TestFetchGitHubIssuesUsesConfiguredTimeout(t *testing.T) {
	origAvail := ghAvailable
	origRunner := ghRunner

	t.Cleanup(func() { ghAvailable = origAvail; ghRunner = origRunner })

	ghAvailable = func() bool { return true }

	var observed time.Duration

	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		if dl, ok := ctx.Deadline(); ok {
			observed = time.Until(dl)
		}

		return "[]", nil
	}

	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "remote", "add", "origin", "https://github.com/d0ugal/croft.git")

	sm := newOrchTestSM(t)
	tc := &config.TrackerConfig{Repo: repo}

	// Mirror actionTracker's exact wiring: the effective timeout comes from the
	// current config snapshot each pass.
	fetchWith := func(ghTimeout string) time.Duration {
		cfg := config.Default()
		cfg.PRWatch.Advanced.GHTimeout = ghTimeout

		if _, _, err := sm.fetchTrackerIssues(context.Background(), tc, repo, cfg.PRWatch.GHTimeoutDuration()); err != nil {
			t.Fatalf("fetchTrackerIssues: %v", err)
		}

		return observed
	}

	// A non-default 45s timeout must be observed on the runner's deadline — the
	// old code hard-capped this at the 5s ghTimeout constant.
	if got := fetchWith("45s"); got < 44*time.Second || got > 45*time.Second {
		t.Fatalf("observed deadline = %v, want ~45s (configured, not the 5s fallback)", got)
	}

	// A subsequent pass with a reloaded, shorter timeout is honoured too.
	if got := fetchWith("12s"); got < 11*time.Second || got > 12*time.Second {
		t.Fatalf("reloaded deadline = %v, want ~12s", got)
	}
}

func TestParseGitHubIssuesEdgeCases(t *testing.T) {
	if refs, err := parseGitHubIssues("", "croft"); err != nil || refs != nil {
		t.Fatalf("empty output should be (nil, nil), got %+v %v", refs, err)
	}

	if refs, err := parseGitHubIssues("   ", "croft"); err != nil || refs != nil {
		t.Fatalf("whitespace output should be (nil, nil), got %+v %v", refs, err)
	}

	if _, err := parseGitHubIssues("not json", "croft"); err == nil {
		t.Fatal("expected parse error for garbage input")
	}
}

func TestFilterIssuesByLabels(t *testing.T) {
	refs := []issueRef{
		{key: "a", labels: []string{"thrawn", "M"}},
		{key: "b", labels: []string{"S"}},
		{key: "c", labels: nil},
	}

	// No labels => all pass.
	if got := filterIssuesByLabels(refs, nil); len(got) != 3 {
		t.Fatalf("empty filter should pass all, got %d", len(got))
	}

	got := filterIssuesByLabels(refs, []string{"thrawn"})
	if len(got) != 1 || got[0].key != "a" {
		t.Fatalf("expected only 'a' to match thrawn, got %+v", got)
	}

	got = filterIssuesByLabels(refs, []string{"S", "M"})
	if len(got) != 2 {
		t.Fatalf("expected a and b to match {S,M}, got %+v", got)
	}
}

func TestTrackerIssueVars(t *testing.T) {
	iss := issueRef{key: "gh:croft#5", number: 5, title: "bonnie", body: "the body", url: "https://x/5", labels: []string{"whin", "neep"}}
	vars := trackerIssueVars("braw-tracker", iss)

	prompt, err := config.ExpandTrigger("#{issue_number} {issue_title}: {issue_body} ({issue_url}) [{issue_labels}]", vars)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}

	want := "#5 bonnie: the body (https://x/5) [whin, neep]"
	if prompt != want {
		t.Errorf("prompt = %q, want %q", prompt, want)
	}
}

func TestTrackerSessionsFilter(t *testing.T) {
	sm := newTriggerTestSM(t)

	deletedAt := time.Now()
	exit0 := 0
	exit1 := 1
	sm.state.Sessions = map[string]*SessionState{
		// Owned, running.
		"braw": {ID: "braw", TriggerID: "issues", TrackerIssue: "gh:croft#1", Status: StatusRunning},
		// Owned, stopped by user (not a clean completion).
		"bide": {ID: "bide", TriggerID: "issues", TrackerIssue: "gh:croft#2", Status: StatusStopped, StopReason: StopReasonUser},
		// Owned, self-exited cleanly (completed).
		"done": {ID: "done", TriggerID: "issues", TrackerIssue: "gh:croft#6", Status: StatusStopped, StopReason: StopReasonCrash, ExitCode: &exit0},
		// Owned, self-exited non-zero (crashed — not completed).
		"crash": {ID: "crash", TriggerID: "issues", TrackerIssue: "gh:croft#7", Status: StatusStopped, StopReason: StopReasonCrash, ExitCode: &exit1},
		// Owned but soft-deleted — excluded.
		"gone": {ID: "gone", TriggerID: "issues", TrackerIssue: "gh:croft#3", Status: StatusStopped, DeletedAt: &deletedAt},
		// Owned, creating — INCLUDED (dedup reservation).
		"new": {ID: "new", TriggerID: "issues", TrackerIssue: "gh:croft#4", Status: StatusCreating},
		// Different trigger — excluded.
		"other": {ID: "other", TriggerID: "elsewhere", TrackerIssue: "gh:croft#5", Status: StatusRunning},
		// Same trigger, no tracker tag (a plain session action) — excluded.
		"plain": {ID: "plain", TriggerID: "issues", Status: StatusRunning},
	}

	got := sm.trackerSessions("issues")
	if len(got) != 5 {
		t.Fatalf("expected 5 tracker sessions, got %d: %+v", len(got), got)
	}

	byKey := map[string]trackerSession{}
	for _, s := range got {
		byKey[s.issueKey] = s
	}

	if s := byKey["gh:croft#1"]; !s.running() {
		t.Errorf("expected running braw, got %+v", s)
	}

	if s := byKey["gh:croft#2"]; s.running() || s.completedCleanly {
		t.Errorf("expected user-stopped bide (not completed), got %+v", s)
	}

	if s := byKey["gh:croft#6"]; !s.completedCleanly {
		t.Errorf("expected completedCleanly for done, got %+v", s)
	}

	if s := byKey["gh:croft#7"]; s.completedCleanly {
		t.Errorf("a non-zero self-exit is not a clean completion, got %+v", s)
	}

	if s := byKey["gh:croft#4"]; !s.creating() {
		t.Errorf("expected creating new, got %+v", s)
	}
}

func TestTrackerObsoleteRoundTrip(t *testing.T) {
	sm := newTriggerTestSM(t)

	fresh := sm.trackerObsoleteSnapshot("issues")
	if len(fresh) != 0 {
		t.Fatalf("fresh snapshot should be empty, got %+v", fresh)
	}

	ts := time.Now().Truncate(time.Second)
	sm.setTrackerObsolete("issues", map[string]time.Time{"gh:croft#9": ts})
	// A second trigger's entries must not leak into the first's namespace.
	sm.setTrackerObsolete("other", map[string]time.Time{"gh:croft#9": ts.Add(time.Hour)})

	snap := sm.trackerObsoleteSnapshot("issues")
	if len(snap) != 1 || !snap["gh:croft#9"].Equal(ts) {
		t.Fatalf("round-trip mismatch: %+v", snap)
	}

	// Replacing with an empty map prunes the trigger's entries.
	sm.setTrackerObsolete("issues", nil)

	if snap := sm.trackerObsoleteSnapshot("issues"); len(snap) != 0 {
		t.Fatalf("expected pruned snapshot, got %+v", snap)
	}

	// The other trigger's entry is untouched.
	if snap := sm.trackerObsoleteSnapshot("other"); len(snap) != 1 {
		t.Fatalf("other trigger's grace should survive, got %+v", snap)
	}
}

func TestTrackerSessionName(t *testing.T) {
	if got := trackerSessionName("issues", 643); got != "issues-643" {
		t.Errorf("name = %q, want issues-643", got)
	}

	// A long trigger name truncates the PREFIX, never the numeric suffix, so two
	// different issues can't collapse to the same session name.
	long := "a-very-long-trigger-name-that-exceeds-the-limit"

	n5 := trackerSessionName(long, 5)
	n12 := trackerSessionName(long, 12)

	if len(n5) > 40 || len(n12) > 40 {
		t.Errorf("names exceed 40: %q (%d), %q (%d)", n5, len(n5), n12, len(n12))
	}

	if !strings.HasSuffix(n5, "-5") || !strings.HasSuffix(n12, "-12") {
		t.Errorf("numeric suffix dropped: %q, %q", n5, n12)
	}

	if n5 == n12 {
		t.Errorf("different issues produced identical names: %q", n5)
	}
}

func TestReapTrackerSession(t *testing.T) {
	newSM := func(retention string, s *SessionState) *SessionManager {
		sm := newTriggerTestSM(t)
		sm.cfg.Delete.Retention = retention
		sm.state.Sessions = map[string]*SessionState{s.ID: s}

		return sm
	}

	t.Run("none is a no-op", func(t *testing.T) {
		sm := newSM("24h", &SessionState{ID: "braw", Status: StatusRunning})
		if sm.reapTrackerSession(context.Background(), "braw", config.TrackerReapNone) {
			t.Error("reap=none should never act")
		}
	})

	t.Run("delete skipped when retention disabled", func(t *testing.T) {
		sm := newSM("0", &SessionState{ID: "braw", TriggerID: "issues", TrackerIssue: "gh:croft#1", Status: StatusStopped})
		if sm.reapTrackerSession(context.Background(), "braw", config.TrackerReapDelete) {
			t.Error("reap=delete must be skipped when retention=0 (never a hard purge)")
		}

		if sm.state.Sessions["braw"].IsSoftDeleted() {
			t.Error("session must not be soft-deleted with retention=0")
		}
	})

	t.Run("delete refuses a starred session", func(t *testing.T) {
		sm := newSM("24h", &SessionState{ID: "braw", TriggerID: "issues", TrackerIssue: "gh:croft#1", Status: StatusStopped, Starred: true})
		if sm.reapTrackerSession(context.Background(), "braw", config.TrackerReapDelete) {
			t.Error("reap=delete must not touch a starred session")
		}
	})

	t.Run("delete soft-deletes a stopped session", func(t *testing.T) {
		sm := newSM("24h", &SessionState{ID: "braw", TriggerID: "issues", TrackerIssue: "gh:croft#1", Status: StatusStopped})
		if !sm.reapTrackerSession(context.Background(), "braw", config.TrackerReapDelete) {
			t.Fatal("reap=delete should soft-delete a stopped session")
		}

		if !sm.state.Sessions["braw"].IsSoftDeleted() {
			t.Error("session should be soft-deleted")
		}
	})

	t.Run("stop is a no-op on an already-stopped session", func(t *testing.T) {
		sm := newSM("24h", &SessionState{ID: "braw", Status: StatusStopped})
		if sm.reapTrackerSession(context.Background(), "braw", config.TrackerReapStop) {
			t.Error("reap=stop on a stopped session should be a no-op")
		}
	})
}

func TestReapStopEligible(t *testing.T) {
	sm := newTriggerTestSM(t)
	sm.state.Sessions = map[string]*SessionState{
		"run":     {ID: "run", Status: StatusRunning},
		"stopped": {ID: "stopped", Status: StatusStopped},
		"starred": {ID: "starred", Status: StatusRunning, Starred: true},
		"system":  {ID: "system", Status: StatusRunning, SystemKind: "orchestrator"},
	}

	if !sm.reapStopEligible("run") {
		t.Error("a plain running session should be stop-eligible")
	}

	for _, id := range []string{"stopped", "starred", "system", "missing"} {
		if sm.reapStopEligible(id) {
			t.Errorf("%s should not be stop-eligible", id)
		}
	}

	if !sm.reapProtected("starred") || !sm.reapProtected("system") {
		t.Error("starred/system should be reap-protected")
	}

	if sm.reapProtected("run") {
		t.Error("a plain session should not be reap-protected")
	}
}
