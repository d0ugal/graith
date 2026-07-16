package daemon

import (
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

	t.Run("spawn a newly active issue", func(t *testing.T) {
		active := []issueRef{issue("gh:croft#1"), issue("gh:croft#2")}
		existing := []trackerSession{{id: "braw", issueKey: "gh:croft#1", running: true}}

		plan, _, capped := reconcileTracker(active, existing, nil, grace, 0, now)

		if len(plan.spawn) != 1 || plan.spawn[0].key != "gh:croft#2" {
			t.Fatalf("expected spawn of #2, got %+v", plan.spawn)
		}

		if len(plan.reap) != 0 || len(plan.resume) != 0 || capped != 0 {
			t.Fatalf("unexpected reap/resume/capped: %+v %+v %d", plan.reap, plan.resume, capped)
		}
	})

	t.Run("no respawn while a running session exists", func(t *testing.T) {
		active := []issueRef{issue("gh:croft#1")}
		existing := []trackerSession{{id: "braw", issueKey: "gh:croft#1", running: true}}

		plan, _, _ := reconcileTracker(active, existing, nil, grace, 0, now)
		if len(plan.spawn) != 0 || len(plan.resume) != 0 {
			t.Fatalf("expected no spawn/resume, got %+v %+v", plan.spawn, plan.resume)
		}
	})

	t.Run("resume a stopped session for a re-active issue", func(t *testing.T) {
		active := []issueRef{issue("gh:croft#1")}
		existing := []trackerSession{{id: "braw", issueKey: "gh:croft#1", running: false}}

		plan, _, _ := reconcileTracker(active, existing, nil, grace, 0, now)
		if len(plan.resume) != 1 || plan.resume[0] != "braw" {
			t.Fatalf("expected resume of braw, got %+v", plan.resume)
		}

		if len(plan.spawn) != 0 {
			t.Fatalf("expected no spawn (dedup by key), got %+v", plan.spawn)
		}
	})

	t.Run("obsolete issue within grace is held", func(t *testing.T) {
		active := []issueRef{}
		existing := []trackerSession{{id: "dreich", issueKey: "gh:croft#9", running: true}}
		obsolete := map[string]time.Time{"gh:croft#9": now.Add(-5 * time.Minute)}

		plan, newGrace, _ := reconcileTracker(active, existing, obsolete, grace, 0, now)
		if len(plan.reap) != 0 {
			t.Fatalf("expected no reap within grace, got %+v", plan.reap)
		}

		if _, ok := newGrace["gh:croft#9"]; !ok {
			t.Fatalf("grace timestamp should carry forward, got %+v", newGrace)
		}
	})

	t.Run("obsolete issue past grace is reaped", func(t *testing.T) {
		active := []issueRef{}
		existing := []trackerSession{{id: "dreich", issueKey: "gh:croft#9", running: true}}
		obsolete := map[string]time.Time{"gh:croft#9": now.Add(-11 * time.Minute)}

		plan, _, _ := reconcileTracker(active, existing, obsolete, grace, 0, now)
		if len(plan.reap) != 1 || plan.reap[0] != "dreich" {
			t.Fatalf("expected reap of dreich, got %+v", plan.reap)
		}
	})

	t.Run("newly obsolete issue starts the grace clock, not reaped", func(t *testing.T) {
		active := []issueRef{}
		existing := []trackerSession{{id: "dreich", issueKey: "gh:croft#9", running: true}}

		plan, newGrace, _ := reconcileTracker(active, existing, nil, grace, 0, now)
		if len(plan.reap) != 0 {
			t.Fatalf("expected no reap on first observation, got %+v", plan.reap)
		}

		if got := newGrace["gh:croft#9"]; !got.Equal(now) {
			t.Fatalf("grace clock should start at now, got %v", got)
		}
	})

	t.Run("re-active issue clears its grace mark", func(t *testing.T) {
		active := []issueRef{issue("gh:croft#9")}
		existing := []trackerSession{{id: "dreich", issueKey: "gh:croft#9", running: true}}
		obsolete := map[string]time.Time{"gh:croft#9": now.Add(-5 * time.Minute)}

		plan, newGrace, _ := reconcileTracker(active, existing, obsolete, grace, 0, now)
		if len(plan.reap) != 0 {
			t.Fatalf("expected no reap for re-active issue, got %+v", plan.reap)
		}

		if _, ok := newGrace["gh:croft#9"]; ok {
			t.Fatalf("grace mark should be cleared for a re-active issue, got %+v", newGrace)
		}
	})

	t.Run("max_concurrent caps spawns", func(t *testing.T) {
		active := []issueRef{issue("gh:croft#1"), issue("gh:croft#2"), issue("gh:croft#3")}
		existing := []trackerSession{{id: "braw", issueKey: "gh:croft#1", running: true}}

		plan, _, capped := reconcileTracker(active, existing, nil, grace, 2, now)
		if len(plan.spawn) != 1 {
			t.Fatalf("expected 1 spawn under cap 2 (1 running), got %+v", plan.spawn)
		}

		if capped != 1 {
			t.Fatalf("expected 1 capped, got %d", capped)
		}
	})

	t.Run("reaping a running session frees a cap slot for a spawn", func(t *testing.T) {
		active := []issueRef{issue("gh:croft#2")}
		existing := []trackerSession{{id: "old", issueKey: "gh:croft#1", running: true}}
		obsolete := map[string]time.Time{"gh:croft#1": now.Add(-11 * time.Minute)}

		plan, _, capped := reconcileTracker(active, existing, obsolete, grace, 1, now)
		if !sortedContains(plan.reap, "old") {
			t.Fatalf("expected reap of old, got %+v", plan.reap)
		}

		if len(plan.spawn) != 1 || capped != 0 {
			t.Fatalf("expected 1 spawn, 0 capped after reap freed a slot, got %+v capped=%d", plan.spawn, capped)
		}
	})

	t.Run("stopped obsolete session held within grace does not occupy a cap slot", func(t *testing.T) {
		// A stopped, held-obsolete session (running=false) must not block a new spawn.
		active := []issueRef{issue("gh:croft#2")}
		existing := []trackerSession{{id: "napping", issueKey: "gh:croft#1", running: false}}
		obsolete := map[string]time.Time{"gh:croft#1": now.Add(-1 * time.Minute)}

		plan, _, capped := reconcileTracker(active, existing, obsolete, grace, 1, now)
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
	sm.state.Sessions = map[string]*SessionState{
		// Owned, running.
		"braw": {ID: "braw", TriggerID: "issues", TrackerIssue: "gh:croft#1", Status: StatusRunning},
		// Owned, stopped.
		"bide": {ID: "bide", TriggerID: "issues", TrackerIssue: "gh:croft#2", Status: StatusStopped},
		// Owned but soft-deleted — excluded.
		"gone": {ID: "gone", TriggerID: "issues", TrackerIssue: "gh:croft#3", Status: StatusStopped, DeletedAt: &deletedAt},
		// Owned but still creating — excluded.
		"new": {ID: "new", TriggerID: "issues", TrackerIssue: "gh:croft#4", Status: StatusCreating},
		// Different trigger — excluded.
		"other": {ID: "other", TriggerID: "elsewhere", TrackerIssue: "gh:croft#5", Status: StatusRunning},
		// Same trigger, no tracker tag (a plain session action) — excluded.
		"plain": {ID: "plain", TriggerID: "issues", Status: StatusRunning},
	}

	got := sm.trackerSessions("issues")
	if len(got) != 2 {
		t.Fatalf("expected 2 tracker sessions, got %d: %+v", len(got), got)
	}

	byKey := map[string]trackerSession{}
	for _, s := range got {
		byKey[s.issueKey] = s
	}

	if s, ok := byKey["gh:croft#1"]; !ok || !s.running {
		t.Errorf("expected running braw, got %+v", s)
	}

	if s, ok := byKey["gh:croft#2"]; !ok || s.running {
		t.Errorf("expected stopped bide, got %+v", s)
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

	long := trackerSessionName("a-very-long-trigger-name-that-exceeds-the-limit", 1)
	if len(long) != 40 {
		t.Errorf("long name len = %d, want 40", len(long))
	}
}
