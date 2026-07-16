package daemon

import (
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

			_, ok, err := s.Claim(it.ID, owner)
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

			it, ok, err := s.ClaimNext("scenario:strath", fmt.Sprintf("w%d", i))
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
	if _, ok, err := s.ClaimNext("scenario:strath", "late"); err != nil || ok {
		t.Fatalf("drained ClaimNext: ok=%v err=%v", ok, err)
	}
}

func TestTodoClaimRequiresOwner(t *testing.T) {
	s := newTestTodoStore(t)

	it := mustAdd(t, s, "session:ben", "x")
	if _, _, err := s.Claim(it.ID, ""); err == nil {
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

	claimed, ok, err := s.Claim(it.ID, "owner")
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
		it, ok, err := s.ClaimNext("scenario:strath", "solo")
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
	if _, _, err := s.Claim(it.ID, "owner"); err != nil {
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
	if _, _, err := s.Claim(parent.ID, "o"); err != nil {
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

	if _, _, err := s.Claim(it.ID, "worker"); err != nil {
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

	up, err := s.UpdateFields(it.ID, &newTitle, nil, &newTags, nil)
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
	if _, err := s.UpdateFields(it.ID, &empty, nil, nil, nil); err == nil {
		t.Error("expected empty-title rejection on update")
	}
}

func TestTodoListFilters(t *testing.T) {
	s := newTestTodoStore(t)

	a, _ := s.Add(TodoAdd{Scope: "session:ben", Title: "a", Tags: []string{"x"}, CreatedBy: "u"})
	mustAdd(t, s, "session:ben", "b")
	mustAdd(t, s, "session:other", "c") // different scope

	if _, _, err := s.Claim(a.ID, "owner"); err != nil {
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
	if _, _, err := s.Claim(items[0].ID, "o"); err != nil {
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

	if _, _, err := s.Claim(a.ID, "gone"); err != nil {
		t.Fatalf("claim a: %v", err)
	}

	if _, _, err := s.Claim(b.ID, "other"); err != nil {
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
	if _, _, err := s.Claim(it.ID, "worker"); err != nil {
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
	if _, _, err := s.Claim(it.ID, "o"); err != nil {
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

func TestTodoAssigneeProgress(t *testing.T) {
	s := newTestTodoStore(t)

	scope := "scenario:strath"

	a1, _ := s.Add(TodoAdd{Scope: scope, Title: "a1", Assignee: "backend", CreatedBy: "orch"})
	s.mustAssignedAdd(t, scope, "a2", "backend")
	s.mustAssignedAdd(t, scope, "f1", "frontend")
	mustAdd(t, s, scope, "unassigned")

	// Complete backend's a1.
	if _, _, err := s.Claim(a1.ID, "backend"); err != nil {
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
