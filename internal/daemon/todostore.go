package daemon

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/d0ugal/graith/internal/config"
	_ "modernc.org/sqlite"
)

// Item status values.
const (
	TodoStatusTodo       = "todo"
	TodoStatusInProgress = "in-progress"
	TodoStatusDone       = "done"
	TodoStatusBlocked    = "blocked"
)

// validTodoStatus reports whether s is a known todo status.
func validTodoStatus(s string) bool {
	switch s {
	case TodoStatusTodo, TodoStatusInProgress, TodoStatusDone, TodoStatusBlocked:
		return true
	default:
		return false
	}
}

// todoTitleHardCeiling / todoNoteHardCeiling are the length limits baked into
// the database CHECK constraints (see initTodoSchema). They are the absolute
// ceiling: the configurable [todo] max_title/max_note may tighten below them but
// can never exceed what the database will accept. Kept as named constants so the
// schema literals and the config ceiling stay in lockstep (config.TodoMaxTitleCeiling
// / config.TodoMaxNoteCeiling mirror these).
const (
	todoTitleHardCeiling = 500
	todoNoteHardCeiling  = 2000
)

// ErrTodoNotFound is returned when a todo id does not exist.
var ErrTodoNotFound = errors.New("todo not found")

// TodoItem is a single todo list entry. It is the daemon-internal shape; the
// wire type (protocol.TodoItemInfo) mirrors it.
type TodoItem struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Scope     string   `json:"scope"`
	Owner     string   `json:"owner,omitempty"`
	Assignee  string   `json:"assignee,omitempty"`
	ParentID  string   `json:"parent_id,omitempty"`
	Note      string   `json:"note,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	CreatedBy string   `json:"created_by"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	Revision  int64    `json:"revision"`
	Position  int64    `json:"position"`
}

// TodoAdd carries the inputs for creating a todo item.
type TodoAdd struct {
	Scope     string
	Title     string
	Note      string
	Tags      []string
	ParentID  string
	Assignee  string
	CreatedBy string
}

// TodoFilter narrows a List query. Empty fields are ignored.
type TodoFilter struct {
	Status string
	Tag    string
	Owner  string
}

// TodoStore is a SQLite-backed store for todo items. It is deliberately a
// separate database (todos.sqlite) from the message log so the higher-churn
// item write stream and its retention are isolated. A single daemon owns it, so
// the mutex fully serializes writers; the conditional UPDATEs are additionally
// race-free at the SQL layer (compare-and-set claim).
type TodoStore struct {
	db  *sql.DB
	mu  sync.Mutex
	now func() time.Time
	// maxTitle/maxNote are the config-resolved title/note length limits (issue
	// #1249). They are documented as hot-reloadable, so they are held in atomics
	// and updated live by SetMaxTitle/SetMaxNote on config reload without
	// reopening the database (issue #1291); Add reads titleLimit() before taking
	// s.mu, so the atomics are what make a concurrent reload race-safe. Neither
	// ever exceeds the database CHECK ceiling. listLimit is fixed at open time
	// (restart-only) and only read under s.mu.
	maxTitle  atomic.Int64
	maxNote   atomic.Int64
	listLimit int
}

// TodoStoreSettings carries the config-derived operational limits for the todo
// store. A zero value resolves each field to its built-in default, so tests and
// callers that don't tune these can pass TodoStoreSettings{} (or nothing).
type TodoStoreSettings struct {
	BusyTimeout time.Duration
	MaxTitle    int
	MaxNote     int
	ListLimit   int
}

func (s TodoStoreSettings) resolved() TodoStoreSettings {
	if s.BusyTimeout <= 0 {
		s.BusyTimeout = config.TodoBusyTimeoutDefault
	} else if s.BusyTimeout < config.SQLiteBusyTimeoutResolution {
		// SQLite's busy_timeout pragma has millisecond resolution; a positive
		// sub-millisecond value would render as busy_timeout(0) and disable the
		// wait the claim contract depends on. Config rejects such values, but
		// floor here so no direct caller can produce a zero DSN. See #1322.
		s.BusyTimeout = config.SQLiteBusyTimeoutResolution
	}

	if s.MaxTitle < 1 || s.MaxTitle > todoTitleHardCeiling {
		s.MaxTitle = config.TodoMaxTitleDefault
	}

	if s.MaxNote < 1 || s.MaxNote > todoNoteHardCeiling {
		s.MaxNote = config.TodoMaxNoteDefault
	}

	if s.ListLimit < 1 {
		s.ListLimit = config.TodoListLimitDefault
	}

	return s
}

// todoStoreDSN builds the sqlite DSN for the todo database. It is a pure helper
// so the busy_timeout rendering can be tested at its boundaries; it shares
// sqliteBusyTimeoutMillis so a positive sub-millisecond wait can never render as
// busy_timeout(0) and silently disable the claim contract's lock wait. See #1322.
func todoStoreDSN(dbPath string, busyTimeout time.Duration) string {
	return fmt.Sprintf("%s?_pragma=journal_mode(wal)&_pragma=busy_timeout(%d)&_pragma=foreign_keys(on)",
		dbPath, sqliteBusyTimeoutMillis(busyTimeout))
}

// NewTodoStore opens (creating if needed) the todo database at dbPath. An
// optional TodoStoreSettings tunes the SQLite busy timeout and the title/note/
// list operational limits; omit it (or pass a zero value) to use the built-in
// defaults. Only the first settings value is used.
func NewTodoStore(dbPath string, settings ...TodoStoreSettings) (*TodoStore, error) {
	var st TodoStoreSettings
	if len(settings) > 0 {
		st = settings[0]
	}

	st = st.resolved()

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("create todos db dir: %w", err)
	}

	// The busy_timeout is load-bearing: the claim contract ("loser gets zero
	// rows") relies on a contended writer waiting rather than erroring with
	// SQLITE_BUSY. foreign_keys(on) makes the parent ON DELETE CASCADE fire.
	dsn := todoStoreDSN(dbPath, st.BusyTimeout)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open todos db: %w", err)
	}

	if err := initTodoSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	store := &TodoStore{
		db:        db,
		now:       time.Now,
		listLimit: st.ListLimit,
	}
	store.maxTitle.Store(int64(st.MaxTitle))
	store.maxNote.Store(int64(st.MaxNote))

	return store, nil
}

// SetMaxTitle updates the todo title length limit live on config reload (issue
// #1291) without reopening the database. A non-positive value or one above the
// database CHECK ceiling resolves to the default, mirroring
// TodoStoreSettings.resolved so a reload matches open-time semantics and can
// never accept a title the database would reject. Safe to call concurrently
// with Add/UpdateFields (the field is atomic).
func (s *TodoStore) SetMaxTitle(limit int) {
	if limit < 1 || limit > todoTitleHardCeiling {
		limit = config.TodoMaxTitleDefault
	}

	s.maxTitle.Store(int64(limit))
}

// SetMaxNote updates the todo note length limit live on config reload, with the
// same clamp-to-default semantics as SetMaxTitle (issue #1291).
func (s *TodoStore) SetMaxNote(limit int) {
	if limit < 1 || limit > todoNoteHardCeiling {
		limit = config.TodoMaxNoteDefault
	}

	s.maxNote.Store(int64(limit))
}

// titleLimit / noteLimit / listCap return the store's effective operational
// limits, falling back to the defaults if the store was constructed without
// settings (e.g. a bare struct literal in a test).
func (s *TodoStore) titleLimit() int {
	if v := int(s.maxTitle.Load()); v >= 1 {
		return v
	}

	return config.TodoMaxTitleDefault
}

func (s *TodoStore) noteLimit() int {
	if v := int(s.maxNote.Load()); v >= 1 {
		return v
	}

	return config.TodoMaxNoteDefault
}

func (s *TodoStore) listCap() int {
	if s.listLimit < 1 {
		return config.TodoListLimitDefault
	}

	return s.listLimit
}

func initTodoSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS todos (
			id          TEXT PRIMARY KEY,
			title       TEXT NOT NULL CHECK (length(title) <= 500),
			status      TEXT NOT NULL DEFAULT 'todo'
			            CHECK (status IN ('todo','in-progress','done','blocked')),
			scope       TEXT NOT NULL,
			owner       TEXT NOT NULL DEFAULT '',
			assignee    TEXT NOT NULL DEFAULT '',
			parent_id   TEXT REFERENCES todos(id) ON DELETE CASCADE,
			note        TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 2000),
			created_by  TEXT NOT NULL,
			created_at  TEXT NOT NULL,
			updated_at  TEXT NOT NULL,
			revision    INTEGER NOT NULL DEFAULT 1,
			position    INTEGER NOT NULL DEFAULT 0,
			CHECK ((status = 'todo' AND owner = '') OR (status <> 'todo' AND owner <> ''))
		);
		CREATE INDEX IF NOT EXISTS idx_todos_scope  ON todos(scope, status, position, id);
		CREATE INDEX IF NOT EXISTS idx_todos_owner  ON todos(owner) WHERE owner <> '';
		CREATE INDEX IF NOT EXISTS idx_todos_parent ON todos(parent_id) WHERE parent_id IS NOT NULL;

		CREATE TABLE IF NOT EXISTS todo_tags (
			todo_id TEXT NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
			tag     TEXT NOT NULL,
			PRIMARY KEY (todo_id, tag)
		);
		CREATE INDEX IF NOT EXISTS idx_todo_tags_tag ON todo_tags(tag);
	`)
	if err != nil {
		return fmt.Errorf("init todos schema: %w", err)
	}

	return nil
}

// Close closes the underlying database.
func (s *TodoStore) Close() error {
	if s.db == nil {
		return nil
	}

	return s.db.Close()
}

func (s *TodoStore) nowStr() string {
	return s.now().UTC().Format(time.RFC3339Nano)
}

// Add creates a new todo item. It validates the title, note, and parent
// (one-level only, same scope), assigns a position after the current maximum in
// the scope, and returns the created item.
func (s *TodoStore) Add(in TodoAdd) (TodoItem, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return TodoItem{}, errors.New("todo title must not be empty")
	}

	if len(title) > s.titleLimit() {
		return TodoItem{}, fmt.Errorf("todo title too long (max %d)", s.titleLimit())
	}

	if len(in.Note) > s.noteLimit() {
		return TodoItem{}, fmt.Errorf("todo note too long (max %d)", s.noteLimit())
	}

	if in.Scope == "" {
		return TodoItem{}, errors.New("todo scope must not be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return TodoItem{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if in.ParentID != "" {
		var parentScope, grandparent sql.NullString

		err := tx.QueryRow(`SELECT scope, parent_id FROM todos WHERE id = ?`, in.ParentID).
			Scan(&parentScope, &grandparent)
		if errors.Is(err, sql.ErrNoRows) {
			return TodoItem{}, fmt.Errorf("parent todo %q not found", in.ParentID)
		}

		if err != nil {
			return TodoItem{}, fmt.Errorf("look up parent: %w", err)
		}

		if grandparent.Valid && grandparent.String != "" {
			return TodoItem{}, fmt.Errorf("todo hierarchy is one level deep: %q is itself a sub-item", in.ParentID)
		}

		if parentScope.String != in.Scope {
			return TodoItem{}, errors.New("sub-item must share its parent's scope")
		}
	}

	var maxPos sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(position) FROM todos WHERE scope = ?`, in.Scope).Scan(&maxPos); err != nil {
		return TodoItem{}, fmt.Errorf("compute position: %w", err)
	}

	now := s.nowStr()
	item := TodoItem{
		ID:        "td-" + generateMsgID(),
		Title:     title,
		Status:    TodoStatusTodo,
		Scope:     in.Scope,
		Assignee:  in.Assignee,
		ParentID:  in.ParentID,
		Note:      in.Note,
		Tags:      normalizeTags(in.Tags),
		CreatedBy: in.CreatedBy,
		CreatedAt: now,
		UpdatedAt: now,
		Revision:  1,
		Position:  maxPos.Int64 + 1,
	}

	if _, err := tx.Exec(
		`INSERT INTO todos (id, title, status, scope, owner, assignee, parent_id, note,
		 created_by, created_at, updated_at, revision, position)
		 VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, 1, ?)`,
		item.ID, item.Title, item.Status, item.Scope, item.Assignee,
		nullStr(item.ParentID), item.Note, item.CreatedBy, item.CreatedAt, item.UpdatedAt, item.Position,
	); err != nil {
		return TodoItem{}, fmt.Errorf("insert todo: %w", err)
	}

	for _, tag := range item.Tags {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO todo_tags (todo_id, tag) VALUES (?, ?)`, item.ID, tag); err != nil {
			return TodoItem{}, fmt.Errorf("insert tag: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return TodoItem{}, fmt.Errorf("commit: %w", err)
	}

	return item, nil
}

// Get returns the item with the given id.
func (s *TodoStore) Get(id string) (TodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.getLocked(s.db, id)
}

type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

func (s *TodoStore) getLocked(q rowQuerier, id string) (TodoItem, error) {
	var it TodoItem

	var parent sql.NullString

	err := q.QueryRow(
		`SELECT id, title, status, scope, owner, assignee, parent_id, note,
		 created_by, created_at, updated_at, revision, position
		 FROM todos WHERE id = ?`, id,
	).Scan(&it.ID, &it.Title, &it.Status, &it.Scope, &it.Owner, &it.Assignee,
		&parent, &it.Note, &it.CreatedBy, &it.CreatedAt, &it.UpdatedAt, &it.Revision, &it.Position)
	if errors.Is(err, sql.ErrNoRows) {
		return TodoItem{}, ErrTodoNotFound
	}

	if err != nil {
		return TodoItem{}, fmt.Errorf("get todo: %w", err)
	}

	it.ParentID = parent.String

	tags, err := loadTags(q, it.ID)
	if err != nil {
		return TodoItem{}, err
	}

	it.Tags = tags

	return it, nil
}

// List returns items in a scope, ordered by position then id, filtered by the
// (optional) filter fields.
func (s *TodoStore) List(scope string, f TodoFilter) ([]TodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `SELECT id, title, status, scope, owner, assignee, parent_id, note,
		created_by, created_at, updated_at, revision, position FROM todos WHERE scope = ?`

	args := []any{scope}

	if f.Status != "" {
		query += " AND status = ?"

		args = append(args, f.Status)
	}

	if f.Owner != "" {
		query += " AND owner = ?"

		args = append(args, f.Owner)
	}

	if f.Tag != "" {
		query += " AND id IN (SELECT todo_id FROM todo_tags WHERE tag = ?)"

		args = append(args, f.Tag)
	}

	query += fmt.Sprintf(" ORDER BY position ASC, id ASC LIMIT %d", s.listCap())

	return s.queryTodos(query, args...)
}

// queryTodos runs a SELECT that returns the standard todo column set, scans the
// rows, and hydrates each item's tags.
func (s *TodoStore) queryTodos(query string, args ...any) ([]TodoItem, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query todos: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []TodoItem

	for rows.Next() {
		var it TodoItem

		var parent sql.NullString

		if err := rows.Scan(&it.ID, &it.Title, &it.Status, &it.Scope, &it.Owner, &it.Assignee,
			&parent, &it.Note, &it.CreatedBy, &it.CreatedAt, &it.UpdatedAt, &it.Revision, &it.Position); err != nil {
			return nil, fmt.Errorf("scan todo: %w", err)
		}

		it.ParentID = parent.String
		out = append(out, it)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate todos: %w", err)
	}

	for i := range out {
		tags, err := loadTags(s.db, out[i].ID)
		if err != nil {
			return nil, err
		}

		out[i].Tags = tags
	}

	return out, nil
}

// ListAll returns items across every scope (human/orchestrator "--all" view),
// ordered by scope then position.
func (s *TodoStore) ListAll(f TodoFilter) ([]TodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `SELECT id, title, status, scope, owner, assignee, parent_id, note,
		created_by, created_at, updated_at, revision, position FROM todos WHERE 1=1`

	var args []any

	if f.Status != "" {
		query += " AND status = ?"

		args = append(args, f.Status)
	}

	if f.Owner != "" {
		query += " AND owner = ?"

		args = append(args, f.Owner)
	}

	if f.Tag != "" {
		query += " AND id IN (SELECT todo_id FROM todo_tags WHERE tag = ?)"

		args = append(args, f.Tag)
	}

	query += fmt.Sprintf(" ORDER BY scope ASC, position ASC, id ASC LIMIT %d", s.listCap())

	return s.queryTodos(query, args...)
}

// Claim atomically claims a specific unclaimed item for owner. It reports
// whether the claim succeeded (false = already claimed / not claimable). owner
// must be non-empty and is set server-side by the caller, never trusted from a
// client payload.
func (s *TodoStore) Claim(id, owner string) (TodoItem, bool, error) {
	if owner == "" {
		return TodoItem{}, false, errors.New("claim requires an owner")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`UPDATE todos SET status = ?, owner = ?, revision = revision + 1, updated_at = ?
		 WHERE id = ? AND status = ? AND owner = ''`,
		TodoStatusInProgress, owner, s.nowStr(), id, TodoStatusTodo,
	)
	if err != nil {
		return TodoItem{}, false, fmt.Errorf("claim todo: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return TodoItem{}, false, fmt.Errorf("claim rows affected: %w", err)
	}

	if n == 0 {
		return TodoItem{}, false, nil
	}

	it, err := s.getLocked(s.db, id)

	return it, true, err
}

// ClaimNext atomically claims the lowest-position unclaimed item in scope and
// returns that exact item. It selects a candidate, then flips it with a guarded
// UPDATE keyed by that id; if a (hypothetical, given the store mutex) concurrent
// claimant took it the UPDATE affects zero rows and we advance to the next
// candidate. This returns the precise row claimed — not one inferred by recency
// — so it is correct even when the same owner claims repeatedly under a coarse
// or mocked clock. Only genuine emptiness returns ok=false.
func (s *TodoStore) ClaimNext(scope, owner string) (TodoItem, bool, error) {
	if owner == "" {
		return TodoItem{}, false, errors.New("claim requires an owner")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for {
		var id string

		err := s.db.QueryRow(
			`SELECT id FROM todos WHERE scope = ? AND status = ? AND owner = ''
			 ORDER BY position ASC, id ASC LIMIT 1`,
			scope, TodoStatusTodo,
		).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return TodoItem{}, false, nil
		}

		if err != nil {
			return TodoItem{}, false, fmt.Errorf("select claim candidate: %w", err)
		}

		res, err := s.db.Exec(
			`UPDATE todos SET status = ?, owner = ?, revision = revision + 1, updated_at = ?
			 WHERE id = ? AND status = ? AND owner = ''`,
			TodoStatusInProgress, owner, s.nowStr(), id, TodoStatusTodo,
		)
		if err != nil {
			return TodoItem{}, false, fmt.Errorf("claim next todo: %w", err)
		}

		n, err := res.RowsAffected()
		if err != nil {
			return TodoItem{}, false, fmt.Errorf("claim next rows affected: %w", err)
		}

		if n == 0 {
			// The candidate was taken between select and update (only possible if
			// the store mutex is bypassed); try the next one.
			continue
		}

		it, err := s.getLocked(s.db, id)

		return it, true, err
	}
}

// Transition applies a guarded status change. actor is the caller; override is
// true when the caller is the scope's override authority or the human (allowing
// them to transition an item they do not own). The conditional WHERE enforces
// the pre-state so an out-of-order or unauthorized transition affects no rows.
func (s *TodoStore) Transition(id, newStatus, actor string, override bool) (TodoItem, error) {
	if !validTodoStatus(newStatus) {
		return TodoItem{}, fmt.Errorf("invalid status %q", newStatus)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowStr()

	var (
		res sql.Result
		err error
	)

	// ownerClause restricts a non-override actor to the item's owner.
	ownerClause := ""
	ownerArgs := []any{}

	if !override {
		ownerClause = " AND owner = ?"
		ownerArgs = []any{actor}
	}

	switch newStatus {
	case TodoStatusDone:
		// Done may be reached from in-progress OR blocked (a resolved blocker is
		// completed directly, without a reopen/re-claim round-trip).
		args := append([]any{newStatus, now, id, TodoStatusInProgress, TodoStatusBlocked}, ownerArgs...)
		res, err = s.db.Exec(
			`UPDATE todos SET status = ?, revision = revision + 1, updated_at = ?
			 WHERE id = ? AND status IN (?, ?)`+ownerClause, args...)
	case TodoStatusBlocked:
		// Blocking applies to an in-progress item.
		args := append([]any{newStatus, now, id, TodoStatusInProgress}, ownerArgs...)
		res, err = s.db.Exec(
			`UPDATE todos SET status = ?, revision = revision + 1, updated_at = ?
			 WHERE id = ? AND status = ?`+ownerClause, args...)
	case TodoStatusTodo:
		// Reopen: clear the owner. Any non-todo state may be reopened.
		args := append([]any{TodoStatusTodo, now, id, TodoStatusTodo}, ownerArgs...)
		res, err = s.db.Exec(
			`UPDATE todos SET status = ?, owner = '', revision = revision + 1, updated_at = ?
			 WHERE id = ? AND status <> ?`+ownerClause, args...)
	case TodoStatusInProgress:
		return TodoItem{}, errors.New("use Claim to move an item to in-progress")
	}

	if err != nil {
		return TodoItem{}, fmt.Errorf("transition todo: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return TodoItem{}, fmt.Errorf("transition rows affected: %w", err)
	}

	if n == 0 {
		// Either the item is gone, in the wrong pre-state, or the actor is not
		// authorized. Disambiguate not-found for a clearer error.
		if _, gerr := s.getLocked(s.db, id); errors.Is(gerr, ErrTodoNotFound) {
			return TodoItem{}, ErrTodoNotFound
		}

		return TodoItem{}, fmt.Errorf("todo %q cannot move to %q from its current state (or not permitted)", id, newStatus)
	}

	return s.getLocked(s.db, id)
}

// UpdateFields edits mutable presentation fields. It never touches status,
// scope, or owner. A nil pointer leaves that field unchanged.
func (s *TodoStore) UpdateFields(id string, title, note *string, tags *[]string, position *int64) (TodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return TodoItem{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := s.getLocked(tx, id); err != nil {
		return TodoItem{}, err
	}

	if title != nil {
		t := strings.TrimSpace(*title)
		if t == "" {
			return TodoItem{}, errors.New("todo title must not be empty")
		}

		if len(t) > s.titleLimit() {
			return TodoItem{}, fmt.Errorf("todo title too long (max %d)", s.titleLimit())
		}

		if _, err := tx.Exec(`UPDATE todos SET title = ? WHERE id = ?`, t, id); err != nil {
			return TodoItem{}, fmt.Errorf("update title: %w", err)
		}
	}

	if note != nil {
		if len(*note) > s.noteLimit() {
			return TodoItem{}, fmt.Errorf("todo note too long (max %d)", s.noteLimit())
		}

		if _, err := tx.Exec(`UPDATE todos SET note = ? WHERE id = ?`, *note, id); err != nil {
			return TodoItem{}, fmt.Errorf("update note: %w", err)
		}
	}

	if position != nil {
		if _, err := tx.Exec(`UPDATE todos SET position = ? WHERE id = ?`, *position, id); err != nil {
			return TodoItem{}, fmt.Errorf("update position: %w", err)
		}
	}

	if tags != nil {
		if _, err := tx.Exec(`DELETE FROM todo_tags WHERE todo_id = ?`, id); err != nil {
			return TodoItem{}, fmt.Errorf("clear tags: %w", err)
		}

		for _, tag := range normalizeTags(*tags) {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO todo_tags (todo_id, tag) VALUES (?, ?)`, id, tag); err != nil {
				return TodoItem{}, fmt.Errorf("insert tag: %w", err)
			}
		}
	}

	if _, err := tx.Exec(`UPDATE todos SET revision = revision + 1, updated_at = ? WHERE id = ?`, s.nowStr(), id); err != nil {
		return TodoItem{}, fmt.Errorf("bump revision: %w", err)
	}

	it, err := s.getLocked(tx, id)
	if err != nil {
		return TodoItem{}, err
	}

	if err := tx.Commit(); err != nil {
		return TodoItem{}, fmt.Errorf("commit: %w", err)
	}

	return it, nil
}

// Assign sets or clears the assignee (responsible member) of an item.
func (s *TodoStore) Assign(id, assignee string) (TodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`UPDATE todos SET assignee = ?, revision = revision + 1, updated_at = ? WHERE id = ?`,
		assignee, s.nowStr(), id)
	if err != nil {
		return TodoItem{}, fmt.Errorf("assign todo: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return TodoItem{}, fmt.Errorf("assign rows affected: %w", err)
	}

	if n == 0 {
		return TodoItem{}, ErrTodoNotFound
	}

	return s.getLocked(s.db, id)
}

// Remove deletes an item (and, via ON DELETE CASCADE, its sub-items and tags).
func (s *TodoStore) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(`DELETE FROM todos WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("remove todo: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove rows affected: %w", err)
	}

	if n == 0 {
		return ErrTodoNotFound
	}

	return nil
}

// Counts returns (done, total) for a scope, counting only top-level items
// (sub-items roll up under their parent in the UI, but for the session badge we
// count every item so progress reflects real work).
func (s *TodoStore) Counts(scope string) (done, total int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	err = s.db.QueryRow(
		`SELECT
		   COALESCE(SUM(CASE WHEN status = 'done' THEN 1 ELSE 0 END), 0),
		   COUNT(*)
		 FROM todos WHERE scope = ?`, scope,
	).Scan(&done, &total)
	if err != nil {
		return 0, 0, fmt.Errorf("count todos: %w", err)
	}

	return done, total, nil
}

// ReopenOwnedBy reopens (status=todo, owner cleared) every in-progress item
// owned by ownerID. Used when the owning session stops so its claims are not
// stranded. Returns the number of items reopened.
func (s *TodoStore) ReopenOwnedBy(ownerID string) (int, error) {
	if ownerID == "" {
		return 0, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`UPDATE todos SET status = ?, owner = '', revision = revision + 1, updated_at = ?
		 WHERE owner = ? AND status = ?`,
		TodoStatusTodo, s.nowStr(), ownerID, TodoStatusInProgress)
	if err != nil {
		return 0, fmt.Errorf("reopen owned todos: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reopen owned rows affected: %w", err)
	}

	return int(n), nil
}

// ReopenStale reopens in-progress items whose updated_at is older than the
// lease window (a claimant that went quiet). lease <= 0 disables the sweep.
func (s *TodoStore) ReopenStale(lease time.Duration) (int, error) {
	if lease <= 0 {
		return 0, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.now().Add(-lease).UTC().Format(time.RFC3339Nano)

	res, err := s.db.Exec(
		`UPDATE todos SET status = ?, owner = '', revision = revision + 1, updated_at = ?
		 WHERE status = ? AND updated_at < ?`,
		TodoStatusTodo, s.nowStr(), TodoStatusInProgress, cutoff)
	if err != nil {
		return 0, fmt.Errorf("reopen stale todos: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reopen stale rows affected: %w", err)
	}

	return int(n), nil
}

// SweepDone deletes done items older than maxAge. maxAge <= 0 disables it. A
// done parent with an unfinished child is NOT swept — deleting it would cascade
// away the child's live work; it becomes eligible once its descendants are done
// (and themselves aged out).
func (s *TodoStore) SweepDone(maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.now().Add(-maxAge).UTC().Format(time.RFC3339Nano)

	res, err := s.db.Exec(
		`DELETE FROM todos WHERE status = ? AND updated_at < ?
		 AND id NOT IN (SELECT parent_id FROM todos WHERE parent_id IS NOT NULL AND status <> ?)`,
		TodoStatusDone, cutoff, TodoStatusDone)
	if err != nil {
		return 0, fmt.Errorf("sweep done todos: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sweep done rows affected: %w", err)
	}

	return int(n), nil
}

// AssigneeProgress reports, per assignee in a scope, how many assigned items are
// done vs total. Only items with a non-empty assignee are counted. Used to
// derive scenario member completion.
func (s *TodoStore) AssigneeProgress(scope string) (map[string][2]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT assignee,
		   SUM(CASE WHEN status = 'done' THEN 1 ELSE 0 END),
		   COUNT(*)
		 FROM todos WHERE scope = ? AND assignee <> '' GROUP BY assignee`, scope)
	if err != nil {
		return nil, fmt.Errorf("assignee progress: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string][2]int)

	for rows.Next() {
		var (
			who         string
			done, total int
		)

		if err := rows.Scan(&who, &done, &total); err != nil {
			return nil, fmt.Errorf("scan assignee progress: %w", err)
		}

		out[who] = [2]int{done, total}
	}

	return out, rows.Err()
}

func loadTags(q rowQuerier, id string) ([]string, error) {
	rows, err := q.Query(`SELECT tag FROM todo_tags WHERE todo_id = ? ORDER BY tag`, id)
	if err != nil {
		return nil, fmt.Errorf("load tags: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tags []string

	for rows.Next() {
		var t string

		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}

		tags = append(tags, t)
	}

	return tags, rows.Err()
}

// normalizeTags trims, de-dupes, drops empties, and sorts a tag list.
func normalizeTags(in []string) []string {
	if len(in) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(in))

	var out []string

	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}

		if _, ok := seen[t]; ok {
			continue
		}

		seen[t] = struct{}{}

		out = append(out, t)
	}

	sort.Strings(out)

	return out
}
