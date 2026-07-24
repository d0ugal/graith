package daemon

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// TodoTransitionResult is the atomic result of a status mutation. Unblocked
// and DependencyBlocked contain unclaimed direct dependents whose effective
// readiness changed in the same transaction.
type TodoTransitionResult struct {
	Item              TodoItem
	Unblocked         []TodoItem
	DependencyBlocked []TodoItem
}

// TodoUpdateResult reports an item's readiness change using snapshots taken
// inside the same transaction that replaced its dependency edges.
type TodoUpdateResult struct {
	Item              TodoItem
	Unblocked         bool
	DependencyBlocked bool
}

// hydrateTodoDependencies loads both the declared edge set and its currently
// unsatisfied subset. Dependency waiting is an effective blocked state: the
// stored row remains todo (and ownerless), avoiding overlap with a manually
// blocked, owned item.
func hydrateTodoDependencies(q rowQuerier, item *TodoItem) error {
	rows, err := q.Query(
		`SELECT d.dependency_id, dependency.status
		 FROM todo_dependencies d
		 JOIN todos dependency ON dependency.id = d.dependency_id
		 WHERE d.todo_id = ? ORDER BY d.dependency_id`, item.ID)
	if err != nil {
		return fmt.Errorf("load todo dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return fmt.Errorf("scan todo dependency: %w", err)
		}

		item.DependsOn = append(item.DependsOn, id)
		if status != TodoStatusDone {
			item.BlockedBy = append(item.BlockedBy, id)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate todo dependencies: %w", err)
	}

	if item.Status == TodoStatusTodo && len(item.BlockedBy) > 0 {
		item.Status = TodoStatusBlocked
	}

	return nil
}

// appendTodoStatusFilter adds a predicate for effective rather than merely
// stored status. Dependency-waiting rows therefore appear in --status blocked
// and are excluded from --status todo.
func appendTodoStatusFilter(query string, args []any, status string) (string, []any) {
	switch status {
	case TodoStatusTodo:
		query += ` AND status = ? AND NOT EXISTS (
			SELECT 1 FROM todo_dependencies d
			JOIN todos dependency ON dependency.id = d.dependency_id
			WHERE d.todo_id = todos.id AND dependency.status <> ?
		)`

		args = append(args, TodoStatusTodo, TodoStatusDone)
	case TodoStatusBlocked:
		query += ` AND (status = ? OR (status = ? AND EXISTS (
			SELECT 1 FROM todo_dependencies d
			JOIN todos dependency ON dependency.id = d.dependency_id
			WHERE d.todo_id = todos.id AND dependency.status <> ?
		)))`

		args = append(args, TodoStatusBlocked, TodoStatusTodo, TodoStatusDone)
	default:
		query += " AND status = ?"

		args = append(args, status)
	}

	return query, args
}

func normalizeTodoDependencies(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	seen := make(map[string]bool, len(ids))

	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, errors.New("todo dependency id must not be empty")
		}

		if seen[id] {
			continue
		}

		seen[id] = true
		out = append(out, id)
	}

	sort.Strings(out)

	return out, nil
}

// replaceTodoDependencies validates and replaces an item's complete edge set
// inside the caller's transaction. Validation happens before deletion so any
// failure preserves the previous graph when the transaction rolls back.
func replaceTodoDependencies(tx *sql.Tx, todoID, scope string, ids []string) error {
	deps, err := normalizeTodoDependencies(ids)
	if err != nil {
		return err
	}

	for _, dependencyID := range deps {
		if dependencyID == todoID {
			return fmt.Errorf("todo %q cannot depend on itself", todoID)
		}

		var dependencyScope string

		err := tx.QueryRow(`SELECT scope FROM todos WHERE id = ?`, dependencyID).Scan(&dependencyScope)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("dependency todo %q not found", dependencyID)
		}

		if err != nil {
			return fmt.Errorf("look up dependency %q: %w", dependencyID, err)
		}

		if dependencyScope != scope {
			return fmt.Errorf("dependency todo %q is in a different scope", dependencyID)
		}

		reaches, err := todoDependencyReaches(tx, dependencyID, todoID)
		if err != nil {
			return err
		}

		if reaches {
			return fmt.Errorf("todo dependency cycle: %q already depends on %q", dependencyID, todoID)
		}
	}

	if _, err := tx.Exec(`DELETE FROM todo_dependencies WHERE todo_id = ?`, todoID); err != nil {
		return fmt.Errorf("clear todo dependencies: %w", err)
	}

	for _, dependencyID := range deps {
		if _, err := tx.Exec(
			`INSERT INTO todo_dependencies (todo_id, dependency_id) VALUES (?, ?)`,
			todoID, dependencyID); err != nil {
			return fmt.Errorf("insert todo dependency %q: %w", dependencyID, err)
		}
	}

	return nil
}

func todoDependencyReaches(tx *sql.Tx, startID, targetID string) (bool, error) {
	var reaches bool

	err := tx.QueryRow(`
		WITH RECURSIVE reachable(id) AS (
			SELECT ?
			UNION
			SELECT d.dependency_id
			FROM todo_dependencies d JOIN reachable r ON d.todo_id = r.id
		)
		SELECT EXISTS(SELECT 1 FROM reachable WHERE id = ?)`, startID, targetID).Scan(&reaches)
	if err != nil {
		return false, fmt.Errorf("check todo dependency cycle: %w", err)
	}

	return reaches, nil
}

// AddBatch atomically inserts a keyed collection of todo items and all of its
// dependency edges. It is intentionally graph-focused: scenario seed items are
// top-level, so parented batch entries are rejected.
func (s *TodoStore) AddBatch(entries []TodoBatchAdd) (map[string]TodoItem, error) {
	if len(entries) == 0 {
		return map[string]TodoItem{}, nil
	}

	titles := make([]string, len(entries))

	seenKeys := make(map[string]bool, len(entries))
	for i, entry := range entries {
		if entry.Key == "" {
			return nil, fmt.Errorf("todo batch entry %d has an empty key", i)
		}

		if seenKeys[entry.Key] {
			return nil, fmt.Errorf("duplicate todo batch key %q", entry.Key)
		}

		seenKeys[entry.Key] = true

		if entry.Item.ParentID != "" {
			return nil, fmt.Errorf("todo batch entry %q must be top-level", entry.Key)
		}

		title, err := s.validateAddInput(entry.Item)
		if err != nil {
			return nil, fmt.Errorf("todo batch entry %q: %w", entry.Key, err)
		}

		titles[i] = title
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin todo batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	positions := make(map[string]int64)

	byKey := make(map[string]TodoItem, len(entries))
	for i, entry := range entries {
		position, ok := positions[entry.Item.Scope]
		if !ok {
			var maxPos sql.NullInt64
			if err := tx.QueryRow(`SELECT MAX(position) FROM todos WHERE scope = ?`, entry.Item.Scope).Scan(&maxPos); err != nil {
				return nil, fmt.Errorf("compute batch position: %w", err)
			}

			position = maxPos.Int64
		}

		position++
		positions[entry.Item.Scope] = position

		now := s.nowStr()
		item := TodoItem{
			ID: "td-" + generateMsgID(), Title: titles[i], Status: TodoStatusTodo,
			Scope: entry.Item.Scope, Assignee: entry.Item.Assignee, Note: entry.Item.Note,
			Tags: normalizeTags(entry.Item.Tags), CreatedBy: entry.Item.CreatedBy,
			CreatedAt: now, UpdatedAt: now, Revision: 1, Position: position,
		}

		if _, err := tx.Exec(
			`INSERT INTO todos (id, title, status, scope, owner, assignee, parent_id, note,
			 created_by, created_at, updated_at, revision, position)
			 VALUES (?, ?, ?, ?, '', ?, NULL, ?, ?, ?, ?, 1, ?)`,
			item.ID, item.Title, item.Status, item.Scope, item.Assignee, item.Note,
			item.CreatedBy, item.CreatedAt, item.UpdatedAt, item.Position); err != nil {
			return nil, fmt.Errorf("insert todo batch entry %q: %w", entry.Key, err)
		}

		if err := recordScenarioSeed(tx, item); err != nil {
			return nil, fmt.Errorf("record todo batch seed %q: %w", entry.Key, err)
		}

		for _, tag := range item.Tags {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO todo_tags (todo_id, tag) VALUES (?, ?)`, item.ID, tag); err != nil {
				return nil, fmt.Errorf("insert todo batch tag: %w", err)
			}
		}

		byKey[entry.Key] = item
	}

	for _, entry := range entries {
		item := byKey[entry.Key]

		deps := append([]string(nil), entry.Item.DependsOn...)
		for _, key := range entry.DependsOnKeys {
			dependency, ok := byKey[key]
			if !ok {
				return nil, fmt.Errorf("todo batch entry %q depends on unknown key %q", entry.Key, key)
			}

			deps = append(deps, dependency.ID)
		}

		if err := replaceTodoDependencies(tx, item.ID, item.Scope, deps); err != nil {
			return nil, fmt.Errorf("todo batch entry %q: %w", entry.Key, err)
		}
	}

	for key, item := range byKey {
		hydrated, err := s.getLocked(tx, item.ID)
		if err != nil {
			return nil, err
		}

		byKey[key] = hydrated
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit todo batch: %w", err)
	}

	return byKey, nil
}

func recordScenarioSeed(tx *sql.Tx, item TodoItem) error {
	if item.CreatedBy != item.Scope || item.ParentID != "" || item.Assignee == "" {
		return nil
	}

	if _, err := tx.Exec(
		`INSERT INTO todo_scenario_seeds (todo_id, scope, original_assignee) VALUES (?, ?, ?)`,
		item.ID, item.Scope, item.Assignee); err != nil {
		return fmt.Errorf("record scenario todo seed: %w", err)
	}

	return nil
}

// ScenarioSeedItemIDs returns the original scenario-seeded top-level todo ID
// for each member session. The immutable seed association survives later
// assignee changes.
func (s *TodoStore) ScenarioSeedItemIDs(scope string) (map[string]string, error) {
	items, err := s.ScenarioSeedItems(scope)
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(items))
	for assignee, item := range items {
		out[assignee] = item.ID
	}

	return out, nil
}

// ScenarioCurrentSeedItemIDs returns the scenario-seeded top-level todo ID
// for each member's current assignee. Unlike ScenarioSeedItemIDs, this uses
// mutable assignment because it is used to validate contracts whose progress
// is tracked by AssigneeProgress.
func (s *TodoStore) ScenarioCurrentSeedItemIDs(scope string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT todo.assignee, todo.id
		 FROM todo_scenario_seeds seed
		 JOIN todos todo ON todo.id = seed.todo_id
		 WHERE seed.scope = ? AND todo.scope = ? AND todo.assignee <> ''
		 ORDER BY todo.assignee, todo.position, todo.id`, scope, scope)
	if err != nil {
		return nil, fmt.Errorf("query currently assigned scenario seed todos: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ids := make(map[string]string)

	for rows.Next() {
		var assignee, id string
		if err := rows.Scan(&assignee, &id); err != nil {
			return nil, fmt.Errorf("scan currently assigned scenario seed todo: %w", err)
		}

		if previous := ids[assignee]; previous != "" {
			return nil, fmt.Errorf("scenario assignee %q has multiple currently assigned seeded todo items (%s, %s)", assignee, previous, id)
		}

		ids[assignee] = id
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate currently assigned scenario seed todos: %w", err)
	}

	return ids, nil
}

// ScenarioSeedItems returns the original scenario-seeded top-level todo for
// each member session, including its effective dependency state. The immutable
// seed association is independent of the item's mutable current assignee.
func (s *TodoStore) ScenarioSeedItems(scope string) (map[string]TodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT seed.original_assignee, todo.id
		 FROM todo_scenario_seeds seed
		 JOIN todos todo ON todo.id = seed.todo_id
		 WHERE seed.scope = ? AND todo.scope = ?
		 ORDER BY seed.original_assignee, todo.position, todo.id`, scope, scope)
	if err != nil {
		return nil, fmt.Errorf("query scenario seed todos: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ids := make(map[string]string)

	for rows.Next() {
		var assignee, id string
		if err := rows.Scan(&assignee, &id); err != nil {
			return nil, fmt.Errorf("scan scenario seed todo: %w", err)
		}

		if previous := ids[assignee]; previous != "" {
			return nil, fmt.Errorf("scenario assignee %q has multiple seeded todo items (%s, %s)", assignee, previous, id)
		}

		ids[assignee] = id
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scenario seed todos: %w", err)
	}

	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close scenario seed rows: %w", err)
	}

	out := make(map[string]TodoItem, len(ids))
	for assignee, id := range ids {
		item, err := s.getLocked(s.db, id)
		if err != nil {
			return nil, err
		}

		out[assignee] = item
	}

	return out, nil
}

func directDependentsChangingReadiness(tx *sql.Tx, dependencyID string, unblocking bool) ([]string, error) {
	query := `SELECT d.todo_id
		FROM todo_dependencies d JOIN todos dependent ON dependent.id = d.todo_id
		WHERE d.dependency_id = ? AND dependent.status = ?`
	args := []any{dependencyID, TodoStatusTodo}

	if unblocking {
		query += ` AND NOT EXISTS (
			SELECT 1 FROM todo_dependencies other
			JOIN todos other_dependency ON other_dependency.id = other.dependency_id
			WHERE other.todo_id = dependent.id AND other_dependency.status <> ?
		)`

		args = append(args, TodoStatusDone)
	} else {
		query += ` AND NOT EXISTS (
			SELECT 1 FROM todo_dependencies other
			JOIN todos other_dependency ON other_dependency.id = other.dependency_id
			WHERE other.todo_id = dependent.id AND other.dependency_id <> ?
			AND other_dependency.status <> ?
		)`

		args = append(args, dependencyID, TodoStatusDone)
	}

	query += " ORDER BY d.todo_id"

	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query dependent readiness: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan dependent readiness: %w", err)
		}

		ids = append(ids, id)
	}

	return ids, rows.Err()
}

func touchTodoIDs(tx *sql.Tx, ids []string, now string) error {
	for _, id := range ids {
		if _, err := tx.Exec(
			`UPDATE todos SET revision = revision + 1, updated_at = ? WHERE id = ?`,
			now, id); err != nil {
			return fmt.Errorf("touch dependent todo %q: %w", id, err)
		}
	}

	return nil
}

func (s *TodoStore) loadTodoIDs(tx *sql.Tx, ids []string) ([]TodoItem, error) {
	items := make([]TodoItem, 0, len(ids))
	for _, id := range ids {
		item, err := s.getLocked(tx, id)
		if err != nil {
			return nil, err
		}

		items = append(items, item)
	}

	return items, nil
}
