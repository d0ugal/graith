package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/store"
)

// errNoTodoStore is returned when a todo operation is attempted before the store
// is wired (e.g. in a unit test using a bare SessionManager).
var errNoTodoStore = errors.New("todo store not available")

// RunTodoSweepLoop periodically reclaims stranded claims (the lease) and sweeps
// aged-out done items (retention). Both windows come from [todo] config; a zero
// window disables that sweep. The sweep cadence ([todo] sweep_interval) is read
// once here, so a change to it takes effect on the next daemon (re)start; the
// lease/retention windows it applies are re-read each tick, so they are
// reloadable. Runs until ctx is cancelled.
func (sm *SessionManager) RunTodoSweepLoop(ctx context.Context) {
	if sm.todos == nil {
		return
	}

	ticker := time.NewTicker(sm.Config().Todo.SweepIntervalDuration())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg := sm.Config()

			if n, err := sm.todos.ReopenStale(cfg.Todo.ClaimLeaseDuration()); err != nil {
				sm.log.Error("todo lease sweep failed", "err", err)
			} else if n > 0 {
				sm.log.Info("reclaimed stale todo claims", "count", n)
			}

			if n, err := sm.todos.SweepDone(cfg.Todo.RetentionDuration()); err != nil {
				sm.log.Error("todo retention sweep failed", "err", err)
			} else if n > 0 {
				sm.log.Info("swept aged-out done todos", "count", n)
			}
		}
	}
}

// reopenTodosForSession reopens todo items claimed by a session that has just
// stopped, so its in-flight work is not stranded (issue #591). Best-effort.
func (sm *SessionManager) reopenTodosForSession(id string) {
	if sm.todos == nil || id == "" {
		return
	}

	if n, err := sm.todos.ReopenOwnedBy(id); err != nil {
		sm.log.Error("failed to reopen todos for stopped session", "id", id, "err", err)
	} else if n > 0 {
		sm.log.Info("reopened stranded todos on session stop", "id", id, "count", n)
	}
}

// subtreeRootLocked walks a session's ParentID chain up to the topmost
// non-system ancestor and returns its id — the anchor for a "session:" scope.
// A system ancestor (e.g. the orchestrator) is not crossed, so sibling subtrees
// stay separate. Must be called with sm.mu at least RLocked.
func (sm *SessionManager) subtreeRootLocked(id string) string {
	cur := id

	for {
		s, ok := sm.state.Sessions[cur]
		if !ok || s.ParentID == "" {
			return cur
		}

		parent, ok := sm.state.Sessions[s.ParentID]
		if !ok || parent.SystemKind != "" {
			return cur
		}

		cur = s.ParentID
	}
}

// todoInSubtreeLocked reports whether caller belongs to the subtree anchored at
// root — i.e. caller's own anchor is root. This deliberately mirrors the
// anchoring rule (subtreeRootLocked, which stops at a system ancestor) rather
// than a raw isDescendantOf walk, so a member cannot reach a *system* ancestor's
// personal list via --session (a descendant of the orchestrator anchors to
// itself, not to the orchestrator). Must be called with sm.mu at least RLocked.
func (sm *SessionManager) todoInSubtreeLocked(caller, root string) bool {
	if caller == "" {
		return false
	}

	return sm.subtreeRootLocked(caller) == root
}

// findScenarioByNameLocked / findScenarioByIDLocked look up a scenario.
func (sm *SessionManager) findScenarioByNameLocked(name string) *ScenarioState {
	for _, s := range sm.state.Scenarios {
		if s.Name == name {
			return s
		}
	}

	return nil
}

func (sm *SessionManager) findScenarioByIDLocked(id string) *ScenarioState {
	for _, s := range sm.state.Scenarios {
		if s.ID == id {
			return s
		}
	}

	return nil
}

// resolveTodoScope maps a client TodoScope onto a scope string and verifies the
// caller may reach it. It takes sm.mu.RLock itself.
func (sm *SessionManager) resolveTodoScope(ac authContext, sc protocol.TodoScope) (string, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	switch {
	case sc.Scenario != "":
		scenario := sm.findScenarioByNameLocked(sc.Scenario)
		if scenario == nil {
			return "", fmt.Errorf("scenario %q not found", sc.Scenario)
		}

		if !sm.todoScenarioMemberLocked(ac, scenario) {
			return "", fmt.Errorf("not authorized for scenario %q's todo list", sc.Scenario)
		}

		return "scenario:" + scenario.ID, nil

	case sc.Session != "":
		root := sm.subtreeRootLocked(sc.Session)
		if !ac.isHuman() && !sm.todoInSubtreeLocked(ac.sessionID, root) {
			return "", errors.New("not authorized for that session's task list")
		}

		return "session:" + root, nil

	default:
		if ac.sessionID == "" {
			return "", errors.New("no session context: specify --session or --scenario")
		}

		return "session:" + sm.subtreeRootLocked(ac.sessionID), nil
	}
}

func (sm *SessionManager) todoScenarioMemberLocked(ac authContext, s *ScenarioState) bool {
	if ac.isHuman() {
		return true
	}

	if ac.sessionID == "" {
		return false
	}

	if ac.sessionID == s.OrchestratorID {
		return true
	}

	for _, id := range s.SessionIDs {
		if id == ac.sessionID {
			return true
		}
	}

	return false
}

// todoAccess describes a caller's rights over a specific item's scope.
type todoAccess struct {
	inScope  bool
	owner    bool
	override bool // scope override authority (subtree root / scenario orchestrator)
	human    bool
}

// accessForItem resolves a caller's access to an item. It takes sm.mu.RLock.
func (sm *SessionManager) accessForItem(ac authContext, item TodoItem) todoAccess {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	acc := todoAccess{human: ac.isHuman()}
	if acc.human {
		acc.inScope, acc.override = true, true
	}

	acc.owner = ac.sessionID != "" && ac.sessionID == item.Owner

	switch {
	case strings.HasPrefix(item.Scope, "session:"):
		root := strings.TrimPrefix(item.Scope, "session:")
		if sm.todoInSubtreeLocked(ac.sessionID, root) {
			acc.inScope = true
		}

		if ac.sessionID != "" && ac.sessionID == root {
			acc.override = true
		}

	case strings.HasPrefix(item.Scope, "scenario:"):
		id := strings.TrimPrefix(item.Scope, "scenario:")
		if sc := sm.findScenarioByIDLocked(id); sc != nil {
			if sm.todoScenarioMemberLocked(ac, sc) {
				acc.inScope = true
			}

			if ac.sessionID != "" && ac.sessionID == sc.OrchestratorID {
				acc.override = true
			}
		}
	}

	return acc
}

// --- Operations (called from the handler) ---

// TodoAddOp creates an item in the resolved scope.
func (sm *SessionManager) TodoAddOp(ac authContext, m protocol.TodoAddMsg) (protocol.TodoItemInfo, error) {
	if sm.todos == nil {
		return protocol.TodoItemInfo{}, errNoTodoStore
	}

	scope, err := sm.resolveTodoScope(ac, m.Scope)
	if err != nil {
		return protocol.TodoItemInfo{}, err
	}

	// Assigning to someone else at creation is an override-authority action, the
	// same rule TodoAssignOp enforces — otherwise a plain member could plant
	// assigned items on a sibling and skew its derived progress. Self-assignment
	// is always allowed.
	if m.Assignee != "" && m.Assignee != ac.sessionID {
		if !sm.accessForItem(ac, TodoItem{Scope: scope}).override {
			return protocol.TodoItemInfo{}, errors.New("only the scope's override authority or the human may assign an item to another session")
		}
	}

	createdBy := ac.sessionID
	if createdBy == "" {
		createdBy = "human"
	}

	item, err := sm.todos.Add(TodoAdd{
		Scope: scope, Title: m.Title, Note: m.Note, Tags: m.Tags,
		ParentID: m.ParentID, Assignee: m.Assignee, CreatedBy: createdBy,
	})
	if err != nil {
		return protocol.TodoItemInfo{}, err
	}

	sm.emitTodoEvent(scope, "added", item)

	return todoItemToWire(item), nil
}

// TodoListOp returns items in the resolved scope (or all scopes when
// m.Scope.All is set by a human/orchestrator).
func (sm *SessionManager) TodoListOp(ac authContext, m protocol.TodoListMsg) ([]protocol.TodoItemInfo, error) {
	if sm.todos == nil {
		return nil, errNoTodoStore
	}

	filter := TodoFilter{Status: m.Status, Tag: m.Tag}

	var (
		items []TodoItem
		err   error
	)

	if m.Scope.All {
		if !ac.isHuman() && ac.role != roleOrchestrator {
			return nil, errors.New("--all is restricted to the human or orchestrator")
		}

		items, err = sm.todos.ListAll(filter)
	} else {
		scope, rerr := sm.resolveTodoScope(ac, m.Scope)
		if rerr != nil {
			return nil, rerr
		}

		items, err = sm.todos.List(scope, filter)
	}

	if err != nil {
		return nil, err
	}

	out := make([]protocol.TodoItemInfo, 0, len(items))
	for _, it := range items {
		out = append(out, todoItemToWire(it))
	}

	return out, nil
}

// TodoClaimOp claims a specific item (m.ID) or the next unclaimed item in scope
// (m.ID empty). The owner is always the calling session, server-derived.
func (sm *SessionManager) TodoClaimOp(ac authContext, m protocol.TodoClaimMsg) (protocol.TodoClaimResponse, error) {
	if sm.todos == nil {
		return protocol.TodoClaimResponse{}, errNoTodoStore
	}

	owner := ac.sessionID
	if owner == "" {
		return protocol.TodoClaimResponse{}, errors.New("claiming a todo requires a session identity")
	}

	if m.ID != "" {
		item, err := sm.todos.Get(m.ID)
		if err != nil {
			return protocol.TodoClaimResponse{}, err
		}

		if !sm.accessForItem(ac, item).inScope {
			return protocol.TodoClaimResponse{}, errors.New("not authorized to claim that item")
		}

		claimed, ok, err := sm.todos.Claim(m.ID, owner)
		if err != nil {
			return protocol.TodoClaimResponse{}, err
		}

		if !ok {
			return protocol.TodoClaimResponse{Claimed: false}, nil
		}

		sm.emitTodoEvent(claimed.Scope, "claimed", claimed)

		return protocol.TodoClaimResponse{Claimed: true, Item: todoItemToWire(claimed)}, nil
	}

	scope, err := sm.resolveTodoScope(ac, m.Scope)
	if err != nil {
		return protocol.TodoClaimResponse{}, err
	}

	claimed, ok, err := sm.todos.ClaimNext(scope, owner)
	if err != nil {
		return protocol.TodoClaimResponse{}, err
	}

	if !ok {
		return protocol.TodoClaimResponse{Claimed: false}, nil
	}

	sm.emitTodoEvent(claimed.Scope, "claimed", claimed)

	return protocol.TodoClaimResponse{Claimed: true, Item: todoItemToWire(claimed)}, nil
}

// TodoTransitionOp performs a guarded status change (done / blocked / todo).
func (sm *SessionManager) TodoTransitionOp(ac authContext, m protocol.TodoTransitionMsg) (protocol.TodoItemInfo, error) {
	if sm.todos == nil {
		return protocol.TodoItemInfo{}, errNoTodoStore
	}

	item, err := sm.todos.Get(m.ID)
	if err != nil {
		return protocol.TodoItemInfo{}, err
	}

	acc := sm.accessForItem(ac, item)
	if !acc.owner && !acc.override {
		return protocol.TodoItemInfo{}, errors.New("only the owner, the scope's override authority, or the human may transition this item")
	}

	// Apply the guarded transition FIRST; only persist the block note once the
	// transition succeeds, so a rejected transition never leaves a half-written
	// note (the note-then-transition order could persist a note against an
	// unchanged status).
	updated, err := sm.todos.Transition(m.ID, m.Status, ac.sessionID, acc.override)
	if err != nil {
		return protocol.TodoItemInfo{}, err
	}

	if m.Note != "" && m.Status == TodoStatusBlocked {
		note := m.Note
		if updated, err = sm.todos.UpdateFields(m.ID, nil, &note, nil, nil); err != nil {
			return protocol.TodoItemInfo{}, err
		}
	}

	sm.emitTodoEvent(updated.Scope, m.Status, updated)

	return todoItemToWire(updated), nil
}

// TodoUpdateOp edits mutable presentation fields.
func (sm *SessionManager) TodoUpdateOp(ac authContext, m protocol.TodoUpdateMsg) (protocol.TodoItemInfo, error) {
	if sm.todos == nil {
		return protocol.TodoItemInfo{}, errNoTodoStore
	}

	item, err := sm.todos.Get(m.ID)
	if err != nil {
		return protocol.TodoItemInfo{}, err
	}

	acc := sm.accessForItem(ac, item)
	if !acc.owner && !acc.override {
		return protocol.TodoItemInfo{}, errors.New("only the owner, the scope's override authority, or the human may edit this item")
	}

	updated, err := sm.todos.UpdateFields(m.ID, m.Title, m.Note, m.Tags, m.Position)
	if err != nil {
		return protocol.TodoItemInfo{}, err
	}

	sm.emitTodoEvent(updated.Scope, "updated", updated)

	return todoItemToWire(updated), nil
}

// TodoAssignOp sets an item's assignee (override authority / human only).
func (sm *SessionManager) TodoAssignOp(ac authContext, m protocol.TodoAssignMsg) (protocol.TodoItemInfo, error) {
	if sm.todos == nil {
		return protocol.TodoItemInfo{}, errNoTodoStore
	}

	item, err := sm.todos.Get(m.ID)
	if err != nil {
		return protocol.TodoItemInfo{}, err
	}

	if acc := sm.accessForItem(ac, item); !acc.override {
		return protocol.TodoItemInfo{}, errors.New("only the scope's override authority or the human may assign this item")
	}

	updated, err := sm.todos.Assign(m.ID, m.Assignee)
	if err != nil {
		return protocol.TodoItemInfo{}, err
	}

	sm.emitTodoEvent(updated.Scope, "assigned", updated)

	return todoItemToWire(updated), nil
}

// TodoRemoveOp deletes an item (and its sub-items).
func (sm *SessionManager) TodoRemoveOp(ac authContext, m protocol.TodoRemoveMsg) error {
	if sm.todos == nil {
		return errNoTodoStore
	}

	item, err := sm.todos.Get(m.ID)
	if err != nil {
		return err
	}

	acc := sm.accessForItem(ac, item)
	if !acc.owner && !acc.override {
		return errors.New("only the owner, the scope's override authority, or the human may remove this item")
	}

	if err := sm.todos.Remove(m.ID); err != nil {
		return err
	}

	sm.emitTodoEvent(item.Scope, "removed", item)

	return nil
}

// TodoExportOp writes the scope's items to the document store and returns the key.
func (sm *SessionManager) TodoExportOp(ac authContext, m protocol.TodoExportMsg) (string, error) {
	if sm.todos == nil {
		return "", errNoTodoStore
	}

	scope, err := sm.resolveTodoScope(ac, m.Scope)
	if err != nil {
		return "", err
	}

	items, err := sm.todos.List(scope, TodoFilter{})
	if err != nil {
		return "", err
	}

	body, key := renderTodoExport(scope, m.Format, items)

	storePath := store.SharedStorePath(sm.paths.DataDir)
	if err := store.Init(storePath); err != nil {
		return "", fmt.Errorf("init store: %w", err)
	}

	if err := store.Put(storePath, key, body); err != nil {
		return "", fmt.Errorf("write export: %w", err)
	}

	return "shared:" + key, nil
}

// emitTodoEvent publishes a compact state-change event to todo:<scope>, subject
// to the [todo] emit_events mode. Best-effort: a publish failure is logged, not
// fatal, and the table remains the source of truth.
func (sm *SessionManager) emitTodoEvent(scope, event string, item TodoItem) {
	if sm.messages == nil {
		return
	}

	mode := sm.Config().Todo.EmitMode()

	switch mode {
	case "off":
		return
	case "scenario":
		if !strings.HasPrefix(scope, "scenario:") {
			return
		}
	}

	payload, err := json.Marshal(struct {
		Event    string `json:"event"`
		ID       string `json:"id"`
		Scope    string `json:"scope"`
		Status   string `json:"status"`
		Owner    string `json:"owner,omitempty"`
		Revision int64  `json:"revision"`
	}{event, item.ID, scope, item.Status, item.Owner, item.Revision})
	if err != nil {
		return
	}

	if _, err := sm.messages.Publish(PublishOpts{
		Stream:     "todo:" + scope,
		SenderID:   systemSenderID,
		SenderName: systemSenderName,
		Body:       string(payload),
	}); err != nil {
		sm.log.Error("failed to emit todo event", "scope", scope, "event", event, "err", err)
	}
}

// anchorScopesFor resolves each session's subtree-anchor scope string under the
// read lock, returning a parallel slice.
func (sm *SessionManager) anchorScopesFor(infos []protocol.SessionInfo) []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	scopes := make([]string, len(infos))
	for i, info := range infos {
		scopes[i] = "session:" + sm.subtreeRootLocked(info.ID)
	}

	return scopes
}

// fillTodoCounts populates each SessionInfo's TodoDone/TodoTotal from its
// subtree-anchored todo list. Best-effort: a store error leaves the counts at
// zero. No-op when the store is unavailable.
func (sm *SessionManager) fillTodoCounts(infos []protocol.SessionInfo) {
	if sm.todos == nil || len(infos) == 0 {
		return
	}

	// Resolve each session's anchor scope under the lock, then query outside it.
	scopes := sm.anchorScopesFor(infos)

	cache := make(map[string][2]int)

	for i := range infos {
		scope := scopes[i]

		c, ok := cache[scope]
		if !ok {
			done, total, err := sm.todos.Counts(scope)
			if err != nil {
				continue
			}

			c = [2]int{done, total}
			cache[scope] = c
		}

		infos[i].TodoDone = c[0]
		infos[i].TodoTotal = c[1]
	}
}

// todoItemToWire converts a store item to its wire representation.
func todoItemToWire(it TodoItem) protocol.TodoItemInfo {
	return protocol.TodoItemInfo{
		ID: it.ID, Title: it.Title, Status: it.Status, Scope: it.Scope,
		Owner: it.Owner, Assignee: it.Assignee, ParentID: it.ParentID, Note: it.Note,
		Tags: it.Tags, CreatedBy: it.CreatedBy, CreatedAt: it.CreatedAt,
		UpdatedAt: it.UpdatedAt, Revision: it.Revision, Position: it.Position,
	}
}

// renderTodoExport builds the export body and store key for a scope.
func renderTodoExport(scope, format string, items []TodoItem) (body, key string) {
	safe := strings.NewReplacer(":", "-", "/", "-").Replace(scope)

	if format == "json" {
		data, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			data = []byte("[]")
		}

		return string(data), "todos/" + safe + ".json"
	}

	var b strings.Builder

	fmt.Fprintf(&b, "# Todo export: %s\n\n", scope)

	if len(items) == 0 {
		b.WriteString("_(no items)_\n")
	}

	for _, it := range items {
		mark := " "
		if it.Status == TodoStatusDone {
			mark = "x"
		}

		prefix := "- "
		if it.ParentID != "" {
			prefix = "  - "
		}

		fmt.Fprintf(&b, "%s[%s] %s _(%s", prefix, mark, it.Title, it.Status)

		if it.Owner != "" {
			fmt.Fprintf(&b, ", owner %s", it.Owner)
		}

		if len(it.Tags) > 0 {
			fmt.Fprintf(&b, ", tags %s", strings.Join(it.Tags, ","))
		}

		b.WriteString(")_\n")
	}

	return b.String(), "todos/" + safe + ".md"
}
