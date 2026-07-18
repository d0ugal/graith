package daemon

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestTodoStore(t *testing.T) *TodoStore {
	t.Helper()

	s, err := NewTodoStore(filepath.Join(t.TempDir(), "todos.sqlite"))
	if err != nil {
		t.Fatalf("NewTodoStore: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	return s
}

func mustAdd(t *testing.T, s *TodoStore, scope, title string) TodoItem {
	t.Helper()

	it, err := s.Add(TodoAdd{Scope: scope, Title: title, CreatedBy: "braw"})
	if err != nil {
		t.Fatalf("Add(%q): %v", title, err)
	}

	return it
}

func TestTodoAddAndGet(t *testing.T) {
	s := newTestTodoStore(t)

	it, err := s.Add(TodoAdd{
		Scope: "session:ben", Title: "  Wire the claim  ", Note: "canny",
		Tags: []string{"backend", "backend", " p1 ", ""}, CreatedBy: "braw",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if it.Title != "Wire the claim" {
		t.Errorf("title not trimmed: %q", it.Title)
	}

	if it.Status != TodoStatusTodo || it.Owner != "" || it.Revision != 1 {
		t.Errorf("unexpected initial state: %+v", it)
	}

	if len(it.Tags) != 2 || it.Tags[0] != "backend" || it.Tags[1] != "p1" {
		t.Errorf("tags not normalized: %v", it.Tags)
	}

	got, err := s.Get(it.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.ID != it.ID || got.Note != "canny" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestTodoAddRejectsEmptyTitle(t *testing.T) {
	s := newTestTodoStore(t)

	if _, err := s.Add(TodoAdd{Scope: "session:dreich", Title: "   ", CreatedBy: "x"}); err == nil {
		t.Fatal("expected empty-title rejection")
	}

	if _, err := s.Add(TodoAdd{Scope: "", Title: "fash", CreatedBy: "x"}); err == nil {
		t.Fatal("expected empty-scope rejection")
	}
}

func TestTodoGetNotFound(t *testing.T) {
	s := newTestTodoStore(t)

	if _, err := s.Get("td-missing"); err != ErrTodoNotFound {
		t.Fatalf("want ErrTodoNotFound, got %v", err)
	}
}

func TestTodoSubItemOneLevelAndSameScope(t *testing.T) {
	s := newTestTodoStore(t)

	parent := mustAdd(t, s, "session:ben", "parent")

	child, err := s.Add(TodoAdd{Scope: "session:ben", Title: "child", ParentID: parent.ID, CreatedBy: "x"})
	if err != nil {
		t.Fatalf("add child: %v", err)
	}

	// A grandchild (two levels) must be rejected.
	if _, err := s.Add(TodoAdd{Scope: "session:ben", Title: "grandchild", ParentID: child.ID, CreatedBy: "x"}); err == nil {
		t.Error("expected two-level parentage rejection")
	}

	// A cross-scope sub-item must be rejected.
	if _, err := s.Add(TodoAdd{Scope: "session:other", Title: "cross", ParentID: parent.ID, CreatedBy: "x"}); err == nil {
		t.Error("expected cross-scope sub-item rejection")
	}

	// Unknown parent must be rejected.
	if _, err := s.Add(TodoAdd{Scope: "session:ben", Title: "orphan", ParentID: "td-nope", CreatedBy: "x"}); err == nil {
		t.Error("expected unknown-parent rejection")
	}
}

func TestTodoRemoveCascadesSubItems(t *testing.T) {
	s := newTestTodoStore(t)

	parent := mustAdd(t, s, "session:ben", "parent")

	child, err := s.Add(TodoAdd{Scope: "session:ben", Title: "child", ParentID: parent.ID, CreatedBy: "x"})
	if err != nil {
		t.Fatalf("add child: %v", err)
	}

	if err := s.Remove(parent.ID); err != nil {
		t.Fatalf("remove parent: %v", err)
	}

	if _, err := s.Get(child.ID); err != ErrTodoNotFound {
		t.Fatalf("child should cascade-delete, got %v", err)
	}

	if err := s.Remove("td-missing"); err != ErrTodoNotFound {
		t.Fatalf("remove missing: want ErrTodoNotFound, got %v", err)
	}
}

// TestTodoClaimRaceSingleItem: N goroutines claim one item; exactly one wins.
func TestTodoClaimRaceSingleItem(t *testing.T) {
	s := newTestTodoStore(t)

	it := mustAdd(t, s, "session:ben", "one item")

	const n = 32

	var (
		wins    atomic.Int64
		wg      sync.WaitGroup
		barrier = make(chan struct{})
	)

	for i := 0; i < n; i++ {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			<-barrier

			owner := fmt.Sprintf("worker-%d", i)

			_, ok, err := s.Claim(it.ID, owner, false)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}

			if ok {
				wins.Add(1)
			}
		}(i)
	}

	close(barrier)
	wg.Wait()

	if wins.Load() != 1 {
		t.Fatalf("expected exactly one winner, got %d", wins.Load())
	}

	got, _ := s.Get(it.ID)
	if got.Status != TodoStatusInProgress || got.Owner == "" {
		t.Fatalf("item not claimed after race: %+v", got)
	}
}

// TestTodoClaimNextRace: N goroutines ClaimNext over N items; each item once.
func TestTodoClaimNextRace(t *testing.T) {
	s := newTestTodoStore(t)

	const n = 24
	for i := 0; i < n; i++ {
		mustAdd(t, s, "scenario:strath", "item")
	}

	var (
		mu      sync.Mutex
		claimed = map[string]int{}
		wg      sync.WaitGroup
		barrier = make(chan struct{})
	)

	for i := 0; i < n; i++ {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			<-barrier

			it, ok, err := s.ClaimNext("scenario:strath", fmt.Sprintf("w%d", i), false)
			if err != nil {
				t.Errorf("claimnext: %v", err)
				return
			}

			if ok {
				mu.Lock()
				claimed[it.ID]++
				mu.Unlock()
			}
		}(i)
	}

	close(barrier)
	wg.Wait()

	if len(claimed) != n {
		t.Fatalf("expected %d distinct claimed items, got %d", n, len(claimed))
	}

	for id, c := range claimed {
		if c != 1 {
			t.Fatalf("item %s claimed %d times", id, c)
		}
	}

	// A further ClaimNext on the drained scope reports empty.
	if _, ok, err := s.ClaimNext("scenario:strath", "late", false); err != nil || ok {
		t.Fatalf("drained ClaimNext: ok=%v err=%v", ok, err)
	}
}

func TestTodoClaimNextHonorsAssignment(t *testing.T) {
	s := newTestTodoStore(t)

	const scope = "scenario:strath"

	assigned, err := s.Add(TodoAdd{Scope: scope, Title: "raise the brig", Assignee: "bairn", CreatedBy: "ben"})
	if err != nil {
		t.Fatal(err)
	}

	unassigned := mustAdd(t, s, scope, "stack the peat")

	peerClaim, ok, err := s.ClaimNext(scope, "skelf", false)
	if err != nil || !ok {
		t.Fatalf("peer ClaimNext: ok=%v err=%v", ok, err)
	}

	if peerClaim.ID != unassigned.ID {
		t.Errorf("peer claimed %q, want eligible unassigned item %q", peerClaim.ID, unassigned.ID)
	}

	assigneeClaim, ok, err := s.ClaimNext(scope, "bairn", false)
	if err != nil || !ok {
		t.Fatalf("assignee ClaimNext: ok=%v err=%v", ok, err)
	}

	if assigneeClaim.ID != assigned.ID {
		t.Errorf("assignee claimed %q, want assigned item %q", assigneeClaim.ID, assigned.ID)
	}

	overrideItem, err := s.Add(TodoAdd{Scope: scope, Title: "thatch the bothy", Assignee: "canny", CreatedBy: "ben"})
	if err != nil {
		t.Fatal(err)
	}

	overrideClaim, ok, err := s.ClaimNext(scope, "ben", true)
	if err != nil || !ok {
		t.Fatalf("override ClaimNext: ok=%v err=%v", ok, err)
	}

	if overrideClaim.ID != overrideItem.ID {
		t.Errorf("override claimed %q, want assigned item %q", overrideClaim.ID, overrideItem.ID)
	}
}

func TestTodoClaimHonorsAssignment(t *testing.T) {
	s := newTestTodoStore(t)

	const scope = "scenario:strath"

	assigned, err := s.Add(TodoAdd{Scope: scope, Title: "raise the brig", Assignee: "bairn", CreatedBy: "ben"})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok, err := s.Claim(assigned.ID, "skelf", false); err != nil || ok {
		t.Fatalf("peer claim of assigned item: ok=%v err=%v", ok, err)
	}

	if item, ok, err := s.Claim(assigned.ID, "bairn", false); err != nil || !ok {
		t.Fatalf("assignee claim: ok=%v err=%v", ok, err)
	} else if item.Owner != "bairn" {
		t.Errorf("assignee claim owner = %q", item.Owner)
	}

	overrideItem, err := s.Add(TodoAdd{Scope: scope, Title: "thatch the bothy", Assignee: "canny", CreatedBy: "ben"})
	if err != nil {
		t.Fatal(err)
	}

	if item, ok, err := s.Claim(overrideItem.ID, "ben", true); err != nil || !ok {
		t.Fatalf("override claim: ok=%v err=%v", ok, err)
	} else if item.Owner != "ben" {
		t.Errorf("override claim owner = %q", item.Owner)
	}
}

func TestTodoClaimRequiresOwner(t *testing.T) {
	s := newTestTodoStore(t)

	it := mustAdd(t, s, "session:ben", "x")
	if _, _, err := s.Claim(it.ID, "", false); err == nil {
		t.Fatal("expected empty-owner rejection")
	}
}

func TestTodoTransitions(t *testing.T) {
	s := newTestTodoStore(t)

	it := mustAdd(t, s, "session:ben", "work")

	// done on an unclaimed item must fail (wrong pre-state).
	if _, err := s.Transition(it.ID, TodoStatusDone, "owner", false); err == nil {
		t.Error("expected done-on-unclaimed to fail")
	}

	claimed, ok, err := s.Claim(it.ID, "owner", false)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	// A non-owner without override cannot complete it.
	if _, err := s.Transition(claimed.ID, TodoStatusDone, "intruder", false); err == nil {
		t.Error("expected non-owner done to fail")
	}

	// The owner can complete it.
	done, err := s.Transition(claimed.ID, TodoStatusDone, "owner", false)
	if err != nil {
		t.Fatalf("owner done: %v", err)
	}

	if done.Status != TodoStatusDone {
		t.Errorf("want done, got %q", done.Status)
	}

	// Reopen clears the owner.
	re, err := s.Transition(done.ID, TodoStatusTodo, "owner", true)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	if re.Status != TodoStatusTodo || re.Owner != "" {
		t.Errorf("reopen did not clear owner: %+v", re)
	}
}

// TestTodoClaimNextSameOwnerTiedClock is the regression for the review finding
// that ClaimNext could return the wrong item when the same owner claims twice
// under a fixed clock (tied updated_at). It must return the exact item claimed
// each time, in position order, with no repeats.
func TestTodoClaimNextSameOwnerTiedClock(t *testing.T) {
	s := newTestTodoStore(t)
	s.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) } // frozen: all ties

	a := mustAdd(t, s, "scenario:strath", "first")
	b := mustAdd(t, s, "scenario:strath", "second")
	c := mustAdd(t, s, "scenario:strath", "third")

	var got []string

	for range 3 {
		it, ok, err := s.ClaimNext("scenario:strath", "solo", false)
		if err != nil || !ok {
			t.Fatalf("ClaimNext: ok=%v err=%v", ok, err)
		}

		got = append(got, it.ID)
	}

	want := []string{a.ID, b.ID, c.ID} // position order
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("claim order under tied clock: got %v, want %v", got, want)
		}
	}
}

func TestTodoTransitionBlockedToDone(t *testing.T) {
	s := newTestTodoStore(t)

	it := mustAdd(t, s, "session:ben", "work")
	if _, _, err := s.Claim(it.ID, "owner", false); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if _, err := s.Transition(it.ID, TodoStatusBlocked, "owner", false); err != nil {
		t.Fatalf("block: %v", err)
	}

	// A blocked item can be completed directly, without reopen/re-claim.
	done, err := s.Transition(it.ID, TodoStatusDone, "owner", false)
	if err != nil {
		t.Fatalf("blocked -> done: %v", err)
	}

	if done.Status != TodoStatusDone {
		t.Errorf("want done, got %q", done.Status)
	}
}

// TestTodoSweepDoneKeepsParentWithUnfinishedChild is the regression for the
// retention finding: aging out a done parent must not cascade-delete an
// unfinished child.
func TestTodoSweepDoneKeepsParentWithUnfinishedChild(t *testing.T) {
	s := newTestTodoStore(t)

	base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	parent := mustAdd(t, s, "session:ben", "parent")

	child, err := s.Add(TodoAdd{Scope: "session:ben", Title: "child", ParentID: parent.ID, CreatedBy: "x"})
	if err != nil {
		t.Fatalf("add child: %v", err)
	}

	// Complete the parent, leave the child as todo.
	if _, _, err := s.Claim(parent.ID, "o", false); err != nil {
		t.Fatalf("claim parent: %v", err)
	}

	if _, err := s.Transition(parent.ID, TodoStatusDone, "o", false); err != nil {
		t.Fatalf("done parent: %v", err)
	}

	s.now = func() time.Time { return base.Add(48 * time.Hour) }

	if _, err := s.SweepDone(24 * time.Hour); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	// The done parent must survive because its child is unfinished.
	if _, err := s.Get(parent.ID); err != nil {
		t.Errorf("done parent with unfinished child was swept: %v", err)
	}

	if _, err := s.Get(child.ID); err != nil {
		t.Errorf("unfinished child was cascade-deleted: %v", err)
	}
}

func TestTodoTransitionOverride(t *testing.T) {
	s := newTestTodoStore(t)

	it := mustAdd(t, s, "session:ben", "work")

	if _, _, err := s.Claim(it.ID, "worker", false); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Override (orchestrator/human) can complete another's item.
	done, err := s.Transition(it.ID, TodoStatusDone, "orchestrator", true)
	if err != nil {
		t.Fatalf("override done: %v", err)
	}

	if done.Status != TodoStatusDone {
		t.Errorf("want done, got %q", done.Status)
	}
}

func TestTodoTransitionNotFound(t *testing.T) {
	s := newTestTodoStore(t)

	if _, err := s.Transition("td-missing", TodoStatusDone, "x", true); err != ErrTodoNotFound {
		t.Fatalf("want ErrTodoNotFound, got %v", err)
	}

	if _, err := s.Transition("x", "bogus", "y", true); err == nil {
		t.Fatal("expected invalid-status rejection")
	}
}

func TestTodoUpdateFields(t *testing.T) {
	s := newTestTodoStore(t)

	it := mustAdd(t, s, "session:ben", "old")

	newTitle := "new title"
	newTags := []string{"z", "a"}

	up, err := s.UpdateFields(it.ID, &newTitle, nil, &newTags, nil, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if up.Title != "new title" {
		t.Errorf("title not updated: %q", up.Title)
	}

	if len(up.Tags) != 2 || up.Tags[0] != "a" || up.Tags[1] != "z" {
		t.Errorf("tags not updated/sorted: %v", up.Tags)
	}

	if up.Revision <= it.Revision {
		t.Errorf("revision not bumped: %d <= %d", up.Revision, it.Revision)
	}

	empty := "   "
	if _, err := s.UpdateFields(it.ID, &empty, nil, nil, nil, nil); err == nil {
		t.Error("expected empty-title rejection on update")
	}
}

func TestTodoListFilters(t *testing.T) {
	s := newTestTodoStore(t)

	a, _ := s.Add(TodoAdd{Scope: "session:ben", Title: "a", Tags: []string{"x"}, CreatedBy: "u"})
	mustAdd(t, s, "session:ben", "b")
	mustAdd(t, s, "session:other", "c") // different scope

	if _, _, err := s.Claim(a.ID, "owner", false); err != nil {
		t.Fatalf("claim: %v", err)
	}

	all, err := s.List("session:ben", TodoFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(all) != 2 {
		t.Fatalf("want 2 in scope, got %d", len(all))
	}

	byStatus, _ := s.List("session:ben", TodoFilter{Status: TodoStatusInProgress})
	if len(byStatus) != 1 || byStatus[0].ID != a.ID {
		t.Errorf("status filter wrong: %+v", byStatus)
	}

	byTag, _ := s.List("session:ben", TodoFilter{Tag: "x"})
	if len(byTag) != 1 || byTag[0].ID != a.ID {
		t.Errorf("tag filter wrong: %+v", byTag)
	}

	byOwner, _ := s.List("session:ben", TodoFilter{Owner: "owner"})
	if len(byOwner) != 1 {
		t.Errorf("owner filter wrong: %+v", byOwner)
	}
}

func TestTodoCounts(t *testing.T) {
	s := newTestTodoStore(t)

	for i := 0; i < 3; i++ {
		mustAdd(t, s, "session:ben", "t")
	}

	items, _ := s.List("session:ben", TodoFilter{})
	if _, _, err := s.Claim(items[0].ID, "o", false); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if _, err := s.Transition(items[0].ID, TodoStatusDone, "o", false); err != nil {
		t.Fatalf("done: %v", err)
	}

	done, total, err := s.Counts("session:ben")
	if err != nil {
		t.Fatalf("counts: %v", err)
	}

	if done != 1 || total != 3 {
		t.Errorf("counts wrong: done=%d total=%d", done, total)
	}
}

func TestTodoReopenOwnedBy(t *testing.T) {
	s := newTestTodoStore(t)

	a := mustAdd(t, s, "session:ben", "a")
	b := mustAdd(t, s, "session:ben", "b")

	if _, _, err := s.Claim(a.ID, "gone", false); err != nil {
		t.Fatalf("claim a: %v", err)
	}

	if _, _, err := s.Claim(b.ID, "other", false); err != nil {
		t.Fatalf("claim b: %v", err)
	}

	n, err := s.ReopenOwnedBy("gone")
	if err != nil {
		t.Fatalf("reopen owned: %v", err)
	}

	if n != 1 {
		t.Fatalf("want 1 reopened, got %d", n)
	}

	ga, _ := s.Get(a.ID)
	if ga.Status != TodoStatusTodo || ga.Owner != "" {
		t.Errorf("a not reopened: %+v", ga)
	}

	gb, _ := s.Get(b.ID)
	if gb.Status != TodoStatusInProgress {
		t.Errorf("b should be untouched: %+v", gb)
	}

	if got, _ := s.ReopenOwnedBy(""); got != 0 {
		t.Errorf("empty owner should reopen nothing, got %d", got)
	}
}

func TestTodoReopenStaleLease(t *testing.T) {
	s := newTestTodoStore(t)

	base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	it := mustAdd(t, s, "session:ben", "leased")
	if _, _, err := s.Claim(it.ID, "worker", false); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Not yet stale.
	s.now = func() time.Time { return base.Add(20 * time.Minute) }
	if n, _ := s.ReopenStale(30 * time.Minute); n != 0 {
		t.Fatalf("premature reopen: %d", n)
	}

	// Past the lease.
	s.now = func() time.Time { return base.Add(40 * time.Minute) }

	n, err := s.ReopenStale(30 * time.Minute)
	if err != nil {
		t.Fatalf("reopen stale: %v", err)
	}

	if n != 1 {
		t.Fatalf("want 1 stale reopened, got %d", n)
	}

	got, _ := s.Get(it.ID)
	if got.Status != TodoStatusTodo {
		t.Errorf("stale item not reopened: %+v", got)
	}

	// Disabled lease reopens nothing.
	if got, _ := s.ReopenStale(0); got != 0 {
		t.Errorf("disabled lease reopened %d", got)
	}
}

func TestTodoSweepDone(t *testing.T) {
	s := newTestTodoStore(t)

	base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	it := mustAdd(t, s, "session:ben", "finish me")
	if _, _, err := s.Claim(it.ID, "o", false); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if _, err := s.Transition(it.ID, TodoStatusDone, "o", false); err != nil {
		t.Fatalf("done: %v", err)
	}

	// Still fresh.
	s.now = func() time.Time { return base.Add(time.Hour) }
	if n, _ := s.SweepDone(24 * time.Hour); n != 0 {
		t.Fatalf("premature sweep: %d", n)
	}

	// Aged out.
	s.now = func() time.Time { return base.Add(48 * time.Hour) }

	n, err := s.SweepDone(24 * time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if n != 1 {
		t.Fatalf("want 1 swept, got %d", n)
	}

	if _, err := s.Get(it.ID); err != ErrTodoNotFound {
		t.Errorf("swept item still present: %v", err)
	}

	if got, _ := s.SweepDone(0); got != 0 {
		t.Errorf("disabled sweep removed %d", got)
	}
}

func TestTodoSweepDoneProtectsActivePolicyScope(t *testing.T) {
	s := newTestTodoStore(t)
	base := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	protected := mustAdd(t, s, "scenario:croft", "review croft")
	unprotected := mustAdd(t, s, "scenario:bothy", "review bothy")
	claimAndDone(t, s, protected.ID, "braw")
	claimAndDone(t, s, unprotected.ID, "canny")

	s.now = func() time.Time { return base.Add(48 * time.Hour) }

	n, err := s.SweepDoneExceptScopes(24*time.Hour, []string{"scenario:croft"})
	if err != nil {
		t.Fatal(err)
	}

	if n != 1 {
		t.Fatalf("swept %d items, want only the unprotected contract", n)
	}

	if _, err := s.Get(protected.ID); err != nil {
		t.Fatalf("active-policy contract disappeared: %v", err)
	}

	if _, err := s.Get(unprotected.ID); !errors.Is(err, ErrTodoNotFound) {
		t.Fatalf("unprotected contract survived: %v", err)
	}
}

func TestTodoAssigneeProgress(t *testing.T) {
	s := newTestTodoStore(t)

	scope := "scenario:strath"

	a1, _ := s.Add(TodoAdd{Scope: scope, Title: "a1", Assignee: "backend", CreatedBy: "orch"})
	s.mustAssignedAdd(t, scope, "a2", "backend")
	s.mustAssignedAdd(t, scope, "f1", "frontend")
	mustAdd(t, s, scope, "unassigned")

	// Complete backend's a1.
	if _, _, err := s.Claim(a1.ID, "backend", false); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if _, err := s.Transition(a1.ID, TodoStatusDone, "backend", false); err != nil {
		t.Fatalf("done: %v", err)
	}

	prog, err := s.AssigneeProgress(scope)
	if err != nil {
		t.Fatalf("assignee progress: %v", err)
	}

	if prog["backend"] != [2]int{1, 2} {
		t.Errorf("backend progress wrong: %v", prog["backend"])
	}

	if prog["frontend"] != [2]int{0, 1} {
		t.Errorf("frontend progress wrong: %v", prog["frontend"])
	}

	if _, ok := prog[""]; ok {
		t.Error("unassigned items must not appear in progress")
	}
}

func (s *TodoStore) mustAssignedAdd(t *testing.T, scope, title, assignee string) {
	t.Helper()

	if _, err := s.Add(TodoAdd{Scope: scope, Title: title, Assignee: assignee, CreatedBy: "orch"}); err != nil {
		t.Fatalf("add assigned %q: %v", title, err)
	}
}

func claimAndDone(t *testing.T, s *TodoStore, id, owner string) TodoItem {
	t.Helper()

	if _, ok, err := s.Claim(id, owner, false); err != nil || !ok {
		t.Fatalf("Claim(%s): ok=%v err=%v", id, ok, err)
	}

	item, err := s.Transition(id, TodoStatusDone, owner, false)
	if err != nil {
		t.Fatalf("Transition done(%s): %v", id, err)
	}

	return item
}

func TestTodoDependenciesBlockUntilAllDone(t *testing.T) {
	s := newTestTodoStore(t)
	scope := "scenario:strath"
	a := mustAdd(t, s, scope, "raise the brig")
	b := mustAdd(t, s, scope, "mend the dyke")

	downstream, err := s.Add(TodoAdd{
		Scope: scope, Title: "inspect the croft", DependsOn: []string{b.ID, a.ID, a.ID}, CreatedBy: "canny",
	})
	if err != nil {
		t.Fatalf("Add dependent: %v", err)
	}

	if downstream.Status != TodoStatusBlocked || downstream.Owner != "" {
		t.Fatalf("dependent initial state = %+v, want ownerless blocked", downstream)
	}

	if len(downstream.DependsOn) != 2 || len(downstream.BlockedBy) != 2 {
		t.Fatalf("dependency hydration = depends %v blocked %v", downstream.DependsOn, downstream.BlockedBy)
	}

	if _, ok, err := s.Claim(downstream.ID, "thrawn", false); err != nil || ok {
		t.Fatalf("blocked claim: ok=%v err=%v", ok, err)
	}

	claimAndDone(t, s, a.ID, "braw")

	stillBlocked, err := s.Get(downstream.ID)
	if err != nil {
		t.Fatal(err)
	}

	if stillBlocked.Status != TodoStatusBlocked || len(stillBlocked.BlockedBy) != 1 || stillBlocked.BlockedBy[0] != b.ID {
		t.Fatalf("after first dependency: %+v", stillBlocked)
	}

	if _, ok, err := s.Claim(b.ID, "braw", false); err != nil || !ok {
		t.Fatalf("claim final dependency: ok=%v err=%v", ok, err)
	}

	result, err := s.TransitionCascade(b.ID, TodoStatusDone, "", "braw", false)
	if err != nil {
		t.Fatalf("complete final dependency: %v", err)
	}

	if len(result.Unblocked) != 1 || result.Unblocked[0].ID != downstream.ID {
		t.Fatalf("unblocked = %+v", result.Unblocked)
	}

	if result.Unblocked[0].Status != TodoStatusTodo || result.Unblocked[0].Revision != downstream.Revision+1 {
		t.Fatalf("unblocked state = %+v", result.Unblocked[0])
	}

	if _, ok, err := s.Claim(downstream.ID, "thrawn", false); err != nil || !ok {
		t.Fatalf("ready claim: ok=%v err=%v", ok, err)
	}
}

func TestTodoDependencyCompletionConcurrentFinalEdges(t *testing.T) {
	s := newTestTodoStore(t)
	scope := "session:bothy"
	a := mustAdd(t, s, scope, "first upstream")
	b := mustAdd(t, s, scope, "second upstream")

	downstream, err := s.Add(TodoAdd{Scope: scope, Title: "synthesis", DependsOn: []string{a.ID, b.ID}, CreatedBy: "braw"})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok, err := s.Claim(a.ID, "bairn-a", false); err != nil || !ok {
		t.Fatalf("claim a: ok=%v err=%v", ok, err)
	}

	if _, ok, err := s.Claim(b.ID, "bairn-b", false); err != nil || !ok {
		t.Fatalf("claim b: ok=%v err=%v", ok, err)
	}

	barrier := make(chan struct{})
	results := make(chan TodoTransitionResult, 2)
	errs := make(chan error, 2)

	var wg sync.WaitGroup
	for _, tc := range []struct{ id, owner string }{{a.ID, "bairn-a"}, {b.ID, "bairn-b"}} {
		wg.Add(1)
		go func(id, owner string) {
			defer wg.Done()

			<-barrier

			result, err := s.TransitionCascade(id, TodoStatusDone, "", owner, false)
			results <- result

			errs <- err
		}(tc.id, tc.owner)
	}

	close(barrier)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent completion: %v", err)
		}
	}

	unblocked := 0
	for result := range results {
		unblocked += len(result.Unblocked)
	}

	if unblocked != 1 {
		t.Fatalf("cascade reported %d unblocks, want exactly 1", unblocked)
	}

	got, err := s.Get(downstream.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != TodoStatusTodo || got.Revision != downstream.Revision+1 {
		t.Fatalf("downstream after race = %+v", got)
	}
}

func TestTodoDependencyUpdateRejectsCycleAndCrossScopeAtomically(t *testing.T) {
	s := newTestTodoStore(t)
	a := mustAdd(t, s, "session:croft", "auld work")
	b := mustAdd(t, s, "session:croft", "braw work")
	other := mustAdd(t, s, "session:bothy", "distant work")

	aDeps := []string{b.ID}
	if _, err := s.UpdateFields(a.ID, nil, nil, nil, nil, &aDeps); err != nil {
		t.Fatalf("set first edge: %v", err)
	}

	bDeps := []string{a.ID}
	if _, err := s.UpdateFields(b.ID, nil, nil, nil, nil, &bDeps); err == nil {
		t.Fatal("expected cycle rejection")
	}

	gotB, err := s.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(gotB.DependsOn) != 0 {
		t.Fatalf("cycle failure changed old graph: %+v", gotB)
	}

	title := "must roll back too"

	crossScope := []string{other.ID}
	if _, err := s.UpdateFields(b.ID, &title, nil, nil, nil, &crossScope); err == nil {
		t.Fatal("expected cross-scope rejection")
	}

	gotB, _ = s.Get(b.ID)
	if gotB.Title != "braw work" || len(gotB.DependsOn) != 0 {
		t.Fatalf("partial update survived failed dependency validation: %+v", gotB)
	}
}

func TestTodoDependencyReopenAndReclaimSemantics(t *testing.T) {
	s := newTestTodoStore(t)
	scope := "session:glen"
	upstream := mustAdd(t, s, scope, "prepare stones")
	claimAndDone(t, s, upstream.ID, "mason")

	downstream, err := s.Add(TodoAdd{Scope: scope, Title: "build wall", DependsOn: []string{upstream.ID}, CreatedBy: "braw"})
	if err != nil {
		t.Fatal(err)
	}

	if downstream.Status != TodoStatusTodo {
		t.Fatalf("dependency already done: downstream = %+v", downstream)
	}

	reopened, err := s.TransitionCascade(upstream.ID, TodoStatusTodo, "", "mason", true)
	if err != nil {
		t.Fatalf("reopen dependency: %v", err)
	}

	if len(reopened.DependencyBlocked) != 1 || reopened.DependencyBlocked[0].ID != downstream.ID {
		t.Fatalf("newly blocked = %+v", reopened.DependencyBlocked)
	}

	claimAndDone(t, s, upstream.ID, "mason")

	if _, ok, err := s.Claim(downstream.ID, "builder", false); err != nil || !ok {
		t.Fatalf("claim downstream: ok=%v err=%v", ok, err)
	}

	if _, err := s.Transition(upstream.ID, TodoStatusTodo, "mason", true); err != nil {
		t.Fatalf("reopen dependency behind started work: %v", err)
	}

	started, _ := s.Get(downstream.ID)
	if started.Status != TodoStatusInProgress || started.Owner != "builder" {
		t.Fatalf("reopen unwound started dependent: %+v", started)
	}

	if n, err := s.ReopenOwnedBy("builder"); err != nil || n != 1 {
		t.Fatalf("reclaim dependent: n=%d err=%v", n, err)
	}

	reclaimed, _ := s.Get(downstream.ID)
	if reclaimed.Status != TodoStatusBlocked || reclaimed.Owner != "" || len(reclaimed.BlockedBy) != 1 {
		t.Fatalf("reclaimed work should wait on reopened dependency: %+v", reclaimed)
	}
}

func TestTodoDependencyRemovalAndRetentionProtectReferencedItems(t *testing.T) {
	s := newTestTodoStore(t)
	base := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	dependency := mustAdd(t, s, "session:croft", "foundation")
	claimAndDone(t, s, dependency.ID, "mason")

	dependent, err := s.Add(TodoAdd{Scope: dependency.Scope, Title: "roof", DependsOn: []string{dependency.ID}, CreatedBy: "braw"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Remove(dependency.ID); err == nil {
		t.Fatal("expected referenced dependency removal to be rejected")
	}

	s.now = func() time.Time { return base.Add(48 * time.Hour) }
	if n, err := s.SweepDone(24 * time.Hour); err != nil || n != 0 {
		t.Fatalf("retention removed referenced dependency: n=%d err=%v", n, err)
	}

	if _, err := s.Get(dependency.ID); err != nil {
		t.Fatalf("referenced dependency disappeared: %v", err)
	}

	emptyDeps := []string{}
	if _, err := s.UpdateFields(dependent.ID, nil, nil, nil, nil, &emptyDeps); err != nil {
		t.Fatalf("clear dependencies: %v", err)
	}

	if err := s.Remove(dependency.ID); err != nil {
		t.Fatalf("remove after clearing edge: %v", err)
	}
}

func TestTodoDependencyRemovalAllowsEdgesInsideParentDeletionSet(t *testing.T) {
	s := newTestTodoStore(t)
	parent := mustAdd(t, s, "session:bothy", "raise frame")

	child, err := s.Add(TodoAdd{
		Scope: parent.Scope, Title: "fit roof", ParentID: parent.ID,
		DependsOn: []string{parent.ID}, CreatedBy: "mason",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Remove(parent.ID); err != nil {
		t.Fatalf("remove parent and internal dependency edge: %v", err)
	}

	if _, err := s.Get(child.ID); err != ErrTodoNotFound {
		t.Fatalf("child survived parent removal: %v", err)
	}
}

func TestTodoDependencyRetentionSkipsParentOfReferencedChild(t *testing.T) {
	s := newTestTodoStore(t)
	base := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	parent := mustAdd(t, s, "session:croft", "raise frame")

	child, err := s.Add(TodoAdd{
		Scope: parent.Scope, Title: "fit roof", ParentID: parent.ID, CreatedBy: "mason",
	})
	if err != nil {
		t.Fatal(err)
	}

	claimAndDone(t, s, child.ID, "mason")
	claimAndDone(t, s, parent.ID, "mason")

	if _, err := s.Add(TodoAdd{
		Scope: parent.Scope, Title: "inspect roof", DependsOn: []string{child.ID}, CreatedBy: "inspector",
	}); err != nil {
		t.Fatal(err)
	}

	unrelated := mustAdd(t, s, parent.Scope, "clear yard")
	claimAndDone(t, s, unrelated.ID, "mason")

	s.now = func() time.Time { return base.Add(48 * time.Hour) }

	n, err := s.SweepDone(24 * time.Hour)
	if err != nil {
		t.Fatalf("sweep with referenced child: %v", err)
	}

	if n != 1 {
		t.Fatalf("swept %d items, want unrelated item only", n)
	}

	for _, id := range []string{parent.ID, child.ID} {
		if _, err := s.Get(id); err != nil {
			t.Fatalf("protected tree item %s disappeared: %v", id, err)
		}
	}

	if _, err := s.Get(unrelated.ID); err != ErrTodoNotFound {
		t.Fatalf("unrelated aged item survived: %v", err)
	}
}

func TestScenarioSeedIdentitySurvivesAssigneeChanges(t *testing.T) {
	s := newTestTodoStore(t)
	scope := "scenario:strath"

	braw, err := s.Add(TodoAdd{Scope: scope, Title: "build", Assignee: "braw-id", CreatedBy: scope})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Add(TodoAdd{Scope: scope, Title: "inspect", Assignee: "canny-id", CreatedBy: scope}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Assign(braw.ID, "canny-id"); err != nil {
		t.Fatal(err)
	}

	items, err := s.ScenarioSeedItems(scope)
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 2 || items["braw-id"].ID != braw.ID || items["braw-id"].Assignee != "canny-id" {
		t.Fatalf("stable seed mapping = %+v", items)
	}
}

func TestTodoDependencyUpdateReportsTransactionalReadiness(t *testing.T) {
	s := newTestTodoStore(t)
	dependency := mustAdd(t, s, "session:glen", "lay stones")
	dependent := mustAdd(t, s, dependency.Scope, "inspect wall")

	deps := []string{dependency.ID}

	blocked, err := s.UpdateFieldsCascade(dependent.ID, nil, nil, nil, nil, &deps)
	if err != nil {
		t.Fatal(err)
	}

	if !blocked.DependencyBlocked || blocked.Unblocked || blocked.Item.Status != TodoStatusBlocked {
		t.Fatalf("blocked update result = %+v", blocked)
	}

	emptyDeps := []string{}

	ready, err := s.UpdateFieldsCascade(dependent.ID, nil, nil, nil, nil, &emptyDeps)
	if err != nil {
		t.Fatal(err)
	}

	if !ready.Unblocked || ready.DependencyBlocked || ready.Item.Status != TodoStatusTodo {
		t.Fatalf("unblocked update result = %+v", ready)
	}
}

func TestTodoDependencyGraphPersistsAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "todos.sqlite")

	s, err := NewTodoStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	dependency := mustAdd(t, s, "scenario:strath", "gather")

	dependent, err := s.Add(TodoAdd{
		Scope: dependency.Scope, Title: "synthesise", DependsOn: []string{dependency.ID}, CreatedBy: "braw",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewTodoStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	got, err := reopened.Get(dependent.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != TodoStatusBlocked || len(got.DependsOn) != 1 || got.DependsOn[0] != dependency.ID {
		t.Fatalf("dependency graph after restart = %+v", got)
	}
}

func TestTodoDependencySchemaMigrationPreservesExistingItems(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "todos.sqlite")

	db, err := sql.Open("sqlite", todoStoreDSN(dbPath, time.Second))
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`
		CREATE TABLE todos (
			id TEXT PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL, scope TEXT NOT NULL,
			owner TEXT NOT NULL DEFAULT '', assignee TEXT NOT NULL DEFAULT '', parent_id TEXT,
			note TEXT NOT NULL DEFAULT '', created_by TEXT NOT NULL, created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 1, position INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE todo_tags (todo_id TEXT NOT NULL, tag TEXT NOT NULL, PRIMARY KEY (todo_id, tag));
		INSERT INTO todos (id, title, status, scope, created_by, created_at, updated_at)
		VALUES ('td-auld', 'auld task', 'todo', 'session:croft', 'braw', '2026-07-17T09:00:00Z', '2026-07-17T09:00:00Z');
		INSERT INTO todos (id, title, status, scope, assignee, created_by, created_at, updated_at)
		VALUES ('td-seed', 'seed task', 'todo', 'scenario:strath', 'braw-id', 'scenario:strath', '2026-07-17T09:00:00Z', '2026-07-17T09:00:00Z');
	`)
	if err != nil {
		_ = db.Close()

		t.Fatalf("create pre-dependency schema: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := NewTodoStore(dbPath)
	if err != nil {
		t.Fatalf("migrate store: %v", err)
	}
	defer func() { _ = s.Close() }()

	old, err := s.Get("td-auld")
	if err != nil || old.Status != TodoStatusTodo || len(old.DependsOn) != 0 {
		t.Fatalf("old item after migration = %+v err=%v", old, err)
	}

	seeds, err := s.ScenarioSeedItems("scenario:strath")
	if err != nil {
		t.Fatal(err)
	}

	if seeds["braw-id"].ID != "td-seed" {
		t.Fatalf("migrated seed identity = %+v", seeds)
	}

	dependency := mustAdd(t, s, old.Scope, "new dependency")

	deps := []string{dependency.ID}
	if _, err := s.UpdateFields(old.ID, nil, nil, nil, nil, &deps); err != nil {
		t.Fatalf("write graph after migration: %v", err)
	}
}

func TestTodoAddBatchRollsBackCyclicGraph(t *testing.T) {
	s := newTestTodoStore(t)

	_, err := s.AddBatch([]TodoBatchAdd{
		{Key: "braw", Item: TodoAdd{Scope: "scenario:croft", Title: "braw", CreatedBy: "orch"}, DependsOnKeys: []string{"canny"}},
		{Key: "canny", Item: TodoAdd{Scope: "scenario:croft", Title: "canny", CreatedBy: "orch"}, DependsOnKeys: []string{"braw"}},
	})
	if err == nil {
		t.Fatal("expected batch cycle rejection")
	}

	items, listErr := s.List("scenario:croft", TodoFilter{})
	if listErr != nil {
		t.Fatal(listErr)
	}

	if len(items) != 0 {
		t.Fatalf("failed batch left partial rows: %+v", items)
	}
}

func TestTodoDependencyEffectiveStatusFilters(t *testing.T) {
	s := newTestTodoStore(t)
	dependency := mustAdd(t, s, "session:croft", "dependency")

	dependent, err := s.Add(TodoAdd{Scope: dependency.Scope, Title: "dependent", DependsOn: []string{dependency.ID}, CreatedBy: "braw"})
	if err != nil {
		t.Fatal(err)
	}

	blocked, err := s.List(dependency.Scope, TodoFilter{Status: TodoStatusBlocked})
	if err != nil || len(blocked) != 1 || blocked[0].ID != dependent.ID {
		t.Fatalf("blocked filter = %+v err=%v", blocked, err)
	}

	ready, err := s.List(dependency.Scope, TodoFilter{Status: TodoStatusTodo})
	if err != nil || len(ready) != 1 || ready[0].ID != dependency.ID {
		t.Fatalf("todo filter = %+v err=%v", ready, err)
	}
}
