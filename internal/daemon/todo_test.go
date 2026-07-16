package daemon

import (
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

// newTodoSM builds a SessionManager with a todo store wired, ready for the
// Op-level (authContext) tests below.
func newTodoSM(t *testing.T) *SessionManager {
	t.Helper()

	sm := newTestSessionManager(t)
	sm.todos = newTestTodoStore(t)

	return sm
}

// putSession registers a session in the manager state under the lock.
func putTodoSession(sm *SessionManager, id, parentID, systemKind string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.state.Sessions[id] = &SessionState{
		ID:         id,
		Name:       id,
		Status:     StatusRunning,
		ParentID:   parentID,
		SystemKind: systemKind,
	}
}

// listForSession returns the caller's default (own-subtree) todo list.
func listForSession(t *testing.T, sm *SessionManager, ac authContext) []protocol.TodoItemInfo {
	t.Helper()

	items, err := sm.TodoListOp(ac, protocol.TodoListMsg{})
	if err != nil {
		t.Fatalf("TodoListOp: %v", err)
	}

	return items
}

// TestTodoOpScopeAnchorsToSubtreeRoot verifies that a child session's todos land
// in the same "session:<root>" scope as its parent, that a system-kind ancestor
// is not crossed, and that a caller with neither a session nor a scope hint is
// rejected.
func TestTodoOpScopeAnchorsToSubtreeRoot(t *testing.T) {
	sm := newTodoSM(t)

	// ben (root) -> bairn (child). Both non-system.
	putTodoSession(sm, "ben", "", "")
	putTodoSession(sm, "bairn", "ben", "")

	parentAC := authContext{role: roleSession, sessionID: "ben", authenticated: true}
	childAC := authContext{role: roleSession, sessionID: "bairn", authenticated: true}

	if _, err := sm.TodoAddOp(parentAC, protocol.TodoAddMsg{Title: "parent task"}); err != nil {
		t.Fatalf("parent add: %v", err)
	}

	if _, err := sm.TodoAddOp(childAC, protocol.TodoAddMsg{Title: "child task"}); err != nil {
		t.Fatalf("child add: %v", err)
	}

	// Both anchored to session:ben — the parent sees both.
	got := listForSession(t, sm, parentAC)
	if len(got) != 2 {
		t.Fatalf("want 2 items in shared subtree scope, got %d: %+v", len(got), got)
	}

	for _, it := range got {
		if it.Scope != "session:ben" {
			t.Errorf("item %q anchored to %q, want session:ben", it.Title, it.Scope)
		}
	}

	// The child sees the same list (subtree root resolves to ben).
	if childGot := listForSession(t, sm, childAC); len(childGot) != 2 {
		t.Fatalf("child view: want 2, got %d", len(childGot))
	}

	// A system-kind ancestor (orchestrator) is NOT crossed: a session parented
	// to the orchestrator anchors to itself, not to the orchestrator.
	putTodoSession(sm, "orch", "", SystemKindOrchestrator)
	putTodoSession(sm, "canny", "orch", "")

	sm.mu.RLock()
	root := sm.subtreeRootLocked("canny")
	sm.mu.RUnlock()

	if root != "canny" {
		t.Errorf("subtree root crossed a system ancestor: got %q, want canny", root)
	}

	// No session and no scope hint is an error.
	if _, err := sm.TodoAddOp(authContext{}, protocol.TodoAddMsg{Title: "dreich"}); err == nil {
		t.Fatal("expected error for no-session, no-scope add")
	}
}

// TestTodoOpAddListRoundTrip covers the basic Add + List round-trip through the
// Ops with a session identity.
func TestTodoOpAddListRoundTrip(t *testing.T) {
	sm := newTodoSM(t)
	putTodoSession(sm, "braw", "", "")

	ac := authContext{role: roleSession, sessionID: "braw", authenticated: true}

	added, err := sm.TodoAddOp(ac, protocol.TodoAddMsg{Title: "wire the claim", Tags: []string{"p1"}})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	if added.Scope != "session:braw" || added.Status != TodoStatusTodo {
		t.Errorf("unexpected added item: %+v", added)
	}

	got := listForSession(t, sm, ac)
	if len(got) != 1 || got[0].ID != added.ID || got[0].Title != "wire the claim" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

// TestTodoOpClaimAuthorization verifies an in-scope session may claim, an
// out-of-scope session is rejected, and the owner is always the caller.
func TestTodoOpClaimAuthorization(t *testing.T) {
	sm := newTodoSM(t)

	putTodoSession(sm, "ben", "", "")      // subtree root
	putTodoSession(sm, "bairn", "ben", "") // in-scope descendant
	putTodoSession(sm, "thrawn", "", "")   // unrelated, out of scope

	ownerAC := authContext{role: roleSession, sessionID: "ben", authenticated: true}
	inScopeAC := authContext{role: roleSession, sessionID: "bairn", authenticated: true}
	strangerAC := authContext{role: roleSession, sessionID: "thrawn", authenticated: true}

	item, err := sm.TodoAddOp(ownerAC, protocol.TodoAddMsg{Title: "claimable"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	// An out-of-scope session cannot claim it.
	if _, err := sm.TodoClaimOp(strangerAC, protocol.TodoClaimMsg{ID: item.ID}); err == nil {
		t.Fatal("expected out-of-scope claim rejection")
	} else {
		assertErrContains(t, err, "not authorized")
	}

	// An in-scope descendant can claim it, and the owner becomes the caller.
	resp, err := sm.TodoClaimOp(inScopeAC, protocol.TodoClaimMsg{ID: item.ID})
	if err != nil {
		t.Fatalf("in-scope claim: %v", err)
	}

	if !resp.Claimed {
		t.Fatal("expected claim to succeed")
	}

	if resp.Item.Owner != "bairn" {
		t.Errorf("owner should be the caller (bairn), got %q", resp.Item.Owner)
	}

	if resp.Item.Status != TodoStatusInProgress {
		t.Errorf("claimed item status = %q, want in-progress", resp.Item.Status)
	}
}

// TestTodoOpTransitionAuthorization verifies the owner, the subtree-root
// override authority, and the human may transition; a non-owner sibling may not.
func TestTodoOpTransitionAuthorization(t *testing.T) {
	sm := newTodoSM(t)

	putTodoSession(sm, "ben", "", "")      // subtree root (override)
	putTodoSession(sm, "bairn", "ben", "") // owner
	putTodoSession(sm, "skelf", "ben", "") // sibling, not owner/override

	rootAC := authContext{role: roleSession, sessionID: "ben", authenticated: true}
	ownerAC := authContext{role: roleSession, sessionID: "bairn", authenticated: true}
	siblingAC := authContext{role: roleSession, sessionID: "skelf", authenticated: true}
	humanAC := authContext{role: roleLocalHuman}

	// Helper: fresh claimed-by-bairn item.
	newClaimed := func() protocol.TodoItemInfo {
		it, err := sm.TodoAddOp(rootAC, protocol.TodoAddMsg{Title: "work"})
		if err != nil {
			t.Fatalf("add: %v", err)
		}

		resp, err := sm.TodoClaimOp(ownerAC, protocol.TodoClaimMsg{ID: it.ID})
		if err != nil || !resp.Claimed {
			t.Fatalf("claim: ok=%v err=%v", resp.Claimed, err)
		}

		return resp.Item
	}

	// A non-owner, non-override sibling cannot mark it done.
	it := newClaimed()
	if _, err := sm.TodoTransitionOp(siblingAC, protocol.TodoTransitionMsg{ID: it.ID, Status: TodoStatusDone}); err == nil {
		t.Error("expected sibling transition rejection")
	} else {
		assertErrContains(t, err, "only the owner")
	}

	// The owner can mark it done.
	if done, err := sm.TodoTransitionOp(ownerAC, protocol.TodoTransitionMsg{ID: it.ID, Status: TodoStatusDone}); err != nil {
		t.Errorf("owner done: %v", err)
	} else if done.Status != TodoStatusDone {
		t.Errorf("owner done status = %q", done.Status)
	}

	// The subtree root (override authority) can mark another's item done.
	it2 := newClaimed()
	if done, err := sm.TodoTransitionOp(rootAC, protocol.TodoTransitionMsg{ID: it2.ID, Status: TodoStatusDone}); err != nil {
		t.Errorf("override done: %v", err)
	} else if done.Status != TodoStatusDone {
		t.Errorf("override done status = %q", done.Status)
	}

	// The human can mark another's item done.
	it3 := newClaimed()
	if done, err := sm.TodoTransitionOp(humanAC, protocol.TodoTransitionMsg{ID: it3.ID, Status: TodoStatusDone}); err != nil {
		t.Errorf("human done: %v", err)
	} else if done.Status != TodoStatusDone {
		t.Errorf("human done status = %q", done.Status)
	}
}

// TestTodoOpScenarioScope verifies membership gating for scenario-scoped todos
// and that the orchestrator is the override authority.
func TestTodoOpScenarioScope(t *testing.T) {
	sm := newTodoSM(t)

	putTodoSession(sm, "ben-orch", "", SystemKindOrchestrator)
	putTodoSession(sm, "braw-member", "", "")
	putTodoSession(sm, "thrawn-outsider", "", "")

	sm.mu.Lock()
	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID:             "sc-strath",
		Name:           "strath",
		OrchestratorID: "ben-orch",
		SessionIDs:     []string{"braw-member"},
	}
	sm.mu.Unlock()

	scope := protocol.TodoScope{Scenario: "strath"}
	memberAC := authContext{role: roleSession, sessionID: "braw-member", authenticated: true}
	orchAC := authContext{role: roleOrchestrator, sessionID: "ben-orch", authenticated: true}
	outsiderAC := authContext{role: roleSession, sessionID: "thrawn-outsider", authenticated: true}

	// A member can add into the scenario scope.
	item, err := sm.TodoAddOp(memberAC, protocol.TodoAddMsg{Scope: scope, Title: "scenario task"})
	if err != nil {
		t.Fatalf("member add: %v", err)
	}

	if item.Scope != "scenario:sc-strath" {
		t.Errorf("scenario item scope = %q", item.Scope)
	}

	// A non-member is rejected.
	if _, err := sm.TodoAddOp(outsiderAC, protocol.TodoAddMsg{Scope: scope, Title: "fash"}); err == nil {
		t.Error("expected non-member add rejection")
	} else {
		assertErrContains(t, err, "not authorized")
	}

	// A member can claim within the scenario scope.
	resp, err := sm.TodoClaimOp(memberAC, protocol.TodoClaimMsg{Scope: scope})
	if err != nil || !resp.Claimed {
		t.Fatalf("member claim: ok=%v err=%v", resp.Claimed, err)
	}

	// The orchestrator is the override authority: it can complete the member's item.
	done, err := sm.TodoTransitionOp(orchAC, protocol.TodoTransitionMsg{ID: item.ID, Status: TodoStatusDone})
	if err != nil {
		t.Fatalf("orchestrator override done: %v", err)
	}

	if done.Status != TodoStatusDone {
		t.Errorf("override done status = %q", done.Status)
	}

	// An unknown scenario name is an error.
	if _, err := sm.TodoAddOp(memberAC, protocol.TodoAddMsg{Scope: protocol.TodoScope{Scenario: "haar"}, Title: "x"}); err == nil {
		t.Error("expected unknown-scenario rejection")
	}
}

// TestTodoOpAssignAuthorization verifies only the override authority (or human)
// may assign, and a plain member is rejected.
func TestTodoOpAssignAuthorization(t *testing.T) {
	sm := newTodoSM(t)

	putTodoSession(sm, "ben-orch", "", SystemKindOrchestrator)
	putTodoSession(sm, "braw-member", "", "")

	sm.mu.Lock()
	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID:             "sc-strath",
		Name:           "strath",
		OrchestratorID: "ben-orch",
		SessionIDs:     []string{"braw-member"},
	}
	sm.mu.Unlock()

	scope := protocol.TodoScope{Scenario: "strath"}
	memberAC := authContext{role: roleSession, sessionID: "braw-member", authenticated: true}
	orchAC := authContext{role: roleOrchestrator, sessionID: "ben-orch", authenticated: true}
	humanAC := authContext{role: roleLocalHuman}

	item, err := sm.TodoAddOp(memberAC, protocol.TodoAddMsg{Scope: scope, Title: "assign me"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	// A plain member cannot assign.
	if _, err := sm.TodoAssignOp(memberAC, protocol.TodoAssignMsg{ID: item.ID, Assignee: "braw-member"}); err == nil {
		t.Error("expected member assign rejection")
	} else {
		assertErrContains(t, err, "override authority")
	}

	// The orchestrator (override) can assign.
	up, err := sm.TodoAssignOp(orchAC, protocol.TodoAssignMsg{ID: item.ID, Assignee: "braw-member"})
	if err != nil {
		t.Fatalf("orchestrator assign: %v", err)
	}

	if up.Assignee != "braw-member" {
		t.Errorf("assignee = %q, want braw-member", up.Assignee)
	}

	// The human can (re)assign.
	if _, err := sm.TodoAssignOp(humanAC, protocol.TodoAssignMsg{ID: item.ID, Assignee: ""}); err != nil {
		t.Errorf("human assign: %v", err)
	}
}

// TestTodoOpRemoveAuthorization verifies owner/override/human may remove, but a
// stranger cannot.
func TestTodoOpRemoveAuthorization(t *testing.T) {
	sm := newTodoSM(t)

	putTodoSession(sm, "ben", "", "")      // subtree root (override)
	putTodoSession(sm, "bairn", "ben", "") // owner
	putTodoSession(sm, "thrawn", "", "")   // stranger

	rootAC := authContext{role: roleSession, sessionID: "ben", authenticated: true}
	ownerAC := authContext{role: roleSession, sessionID: "bairn", authenticated: true}
	strangerAC := authContext{role: roleSession, sessionID: "thrawn", authenticated: true}

	item, err := sm.TodoAddOp(rootAC, protocol.TodoAddMsg{Title: "removable"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	if _, err := sm.TodoClaimOp(ownerAC, protocol.TodoClaimMsg{ID: item.ID}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// A stranger cannot remove it.
	if err := sm.TodoRemoveOp(strangerAC, protocol.TodoRemoveMsg{ID: item.ID}); err == nil {
		t.Error("expected stranger remove rejection")
	} else {
		assertErrContains(t, err, "only the owner")
	}

	// The owner can remove it.
	if err := sm.TodoRemoveOp(ownerAC, protocol.TodoRemoveMsg{ID: item.ID}); err != nil {
		t.Fatalf("owner remove: %v", err)
	}

	if _, err := sm.todos.Get(item.ID); err != ErrTodoNotFound {
		t.Errorf("item should be gone, got %v", err)
	}
}

// TestTodoOpFillCounts verifies fillTodoCounts populates TodoDone/TodoTotal from
// a session's subtree scope.
func TestTodoOpFillCounts(t *testing.T) {
	sm := newTodoSM(t)

	putTodoSession(sm, "ben", "", "")

	ac := authContext{role: roleSession, sessionID: "ben", authenticated: true}

	const total = 4

	var ids []string

	for i := 0; i < total; i++ {
		it, err := sm.TodoAddOp(ac, protocol.TodoAddMsg{Title: "task"})
		if err != nil {
			t.Fatalf("add: %v", err)
		}

		ids = append(ids, it.ID)
	}

	// Complete two of them.
	for _, id := range ids[:2] {
		if _, err := sm.TodoClaimOp(ac, protocol.TodoClaimMsg{ID: id}); err != nil {
			t.Fatalf("claim %s: %v", id, err)
		}

		if _, err := sm.TodoTransitionOp(ac, protocol.TodoTransitionMsg{ID: id, Status: TodoStatusDone}); err != nil {
			t.Fatalf("done %s: %v", id, err)
		}
	}

	infos := []protocol.SessionInfo{{ID: "ben"}}
	sm.fillTodoCounts(infos)

	if infos[0].TodoTotal != total {
		t.Errorf("TodoTotal = %d, want %d", infos[0].TodoTotal, total)
	}

	if infos[0].TodoDone != 2 {
		t.Errorf("TodoDone = %d, want 2", infos[0].TodoDone)
	}
}

// TestTodoOpReopenForSession verifies that reopenTodosForSession returns a
// stopped session's claimed item to the pool.
func TestTodoOpReopenForSession(t *testing.T) {
	sm := newTodoSM(t)

	putTodoSession(sm, "ben", "", "")

	ac := authContext{role: roleSession, sessionID: "ben", authenticated: true}

	item, err := sm.TodoAddOp(ac, protocol.TodoAddMsg{Title: "in flight"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	if _, err := sm.TodoClaimOp(ac, protocol.TodoClaimMsg{ID: item.ID}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	sm.reopenTodosForSession("ben")

	got, err := sm.todos.Get(item.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Status != TodoStatusTodo || got.Owner != "" {
		t.Errorf("item not reopened: status=%q owner=%q", got.Status, got.Owner)
	}
}

// TestTodoOpNilStoreGuard verifies the Ops fail cleanly when no store is wired.
func TestTodoOpNilStoreGuard(t *testing.T) {
	sm := newTestSessionManager(t) // no sm.todos

	ac := authContext{role: roleSession, sessionID: "ben", authenticated: true}

	if _, err := sm.TodoAddOp(ac, protocol.TodoAddMsg{Title: "x"}); err == nil {
		t.Error("TodoAddOp: expected error with nil store")
	}

	if _, err := sm.TodoListOp(ac, protocol.TodoListMsg{}); err == nil {
		t.Error("TodoListOp: expected error with nil store")
	}

	if _, err := sm.TodoClaimOp(ac, protocol.TodoClaimMsg{ID: "td-x"}); err == nil {
		t.Error("TodoClaimOp: expected error with nil store")
	}

	if _, err := sm.TodoTransitionOp(ac, protocol.TodoTransitionMsg{ID: "td-x", Status: TodoStatusDone}); err == nil {
		t.Error("TodoTransitionOp: expected error with nil store")
	}

	if _, err := sm.TodoAssignOp(ac, protocol.TodoAssignMsg{ID: "td-x"}); err == nil {
		t.Error("TodoAssignOp: expected error with nil store")
	}

	if err := sm.TodoRemoveOp(ac, protocol.TodoRemoveMsg{ID: "td-x"}); err == nil {
		t.Error("TodoRemoveOp: expected error with nil store")
	}
}

// TestTodoOpAddAssigneeAuthorization is the regression for the add-time assignee
// bypass: a plain member cannot plant an assigned item on a sibling, but may
// self-assign, and the orchestrator (override) may assign to anyone.
func TestTodoOpAddAssigneeAuthorization(t *testing.T) {
	sm := newTodoSM(t)

	sm.mu.Lock()
	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID: "sc-strath", Name: "strath", OrchestratorID: "orch",
		SessionIDs: []string{"braw", "bonnie"},
	}
	sm.mu.Unlock()
	putTodoSession(sm, "braw", "", "")
	putTodoSession(sm, "bonnie", "", "")
	putTodoSession(sm, "orch", "", "")

	member := authContext{role: roleSession, sessionID: "braw", authenticated: true}
	scope := protocol.TodoScope{Scenario: "strath"}

	// Plant an item on a sibling → rejected.
	if _, err := sm.TodoAddOp(member, protocol.TodoAddMsg{Scope: scope, Title: "fash", Assignee: "bonnie"}); err == nil {
		t.Error("member assigning to a sibling at add time should be rejected")
	}

	// Self-assign → allowed.
	if _, err := sm.TodoAddOp(member, protocol.TodoAddMsg{Scope: scope, Title: "mine", Assignee: "braw"}); err != nil {
		t.Errorf("member self-assign should be allowed: %v", err)
	}

	// Orchestrator (override) may assign to a member.
	orch := authContext{role: roleOrchestrator, sessionID: "orch", authenticated: true}
	if _, err := sm.TodoAddOp(orch, protocol.TodoAddMsg{Scope: scope, Title: "assigned", Assignee: "bonnie"}); err != nil {
		t.Errorf("orchestrator assign should be allowed: %v", err)
	}
}

// TestTodoOpScopeBoundaryNotCrossed is the regression for the scope-crossing
// finding: a session parented to a SYSTEM ancestor (the orchestrator) must not
// reach that ancestor's personal subtree list via --session, even though a raw
// descendant walk would say it is a descendant.
func TestTodoOpScopeBoundaryNotCrossed(t *testing.T) {
	sm := newTodoSM(t)

	putTodoSession(sm, "orch", "", "orchestrator") // system ancestor
	putTodoSession(sm, "bairn", "orch", "")        // child of the orchestrator

	// The orchestrator files a private item in its own subtree.
	orch := authContext{role: roleOrchestrator, sessionID: "orch", authenticated: true}
	if _, err := sm.TodoAddOp(orch, protocol.TodoAddMsg{Title: "orch-private"}); err != nil {
		t.Fatalf("orchestrator add: %v", err)
	}

	// The child must NOT be able to target the orchestrator's list via --session.
	child := authContext{role: roleSession, sessionID: "bairn", authenticated: true}
	if _, err := sm.TodoListOp(child, protocol.TodoListMsg{Scope: protocol.TodoScope{Session: "orch"}}); err == nil {
		t.Error("child crossing the system boundary to the orchestrator's list should be rejected")
	}

	// The child's own subtree anchors to itself (isolated).
	items := listForSession(t, sm, child)
	if len(items) != 0 {
		t.Errorf("child's own list should be empty, got %d items", len(items))
	}
}
