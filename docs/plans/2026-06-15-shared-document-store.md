# Shared Document Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a shared document store (`gr store`) that lets agents persist artifacts outside of git worktrees, so documents survive worktree deletion and are accessible to all sessions for the same repo.

**Architecture:** SQLite-backed key/value store in the daemon's data directory, scoped per-repo (keyed by the canonical repo path). Documents have a key (slash-separated path like `design/api.md`), a body (arbitrary text), and metadata (author session, creation/update time, content type). The store is accessed via `gr store put/get/list/rm` CLI commands that route through the existing control protocol — no new wire channels needed.

**Tech Stack:** Go, SQLite (modernc.org/sqlite, same driver as msgstore), Cobra CLI, graith's existing framed control protocol.

---

## Design Decisions

### Scoping: per-repo

Documents are scoped by canonical repo path. When an agent in a session for `~/Code/graith` writes `design/api.md`, only sessions for that same repo can see it. The repo path is resolved from the session's `RepoPath` field (already tracked in state.json). For CLI calls outside a session, the user provides `--repo` or the CWD's git root is used.

### Keys: slash-separated paths

Keys are slash-separated strings (e.g. `notes/findings.md`, `design/api-spec`). No leading slash. The `/` is purely conventional — the store is flat, not hierarchical — but `gr store list` supports prefix filtering so `gr store list design/` works naturally.

### No TTL / No auto-cleanup

Unlike messages, documents are meant to persist. No automatic expiration. Users explicitly remove with `gr store rm`.

### Storage: single SQLite database

Reuse the same SQLite patterns as `msgstore.go`: WAL mode, busy timeout, `modernc.org/sqlite` driver. New database file at `<data-dir>/docstore.sqlite`. One table, repo scoping via a column.

---

## File Structure

| File | Responsibility |
|------|---------------|
| `internal/daemon/docstore.go` | SQLite backend: schema, CRUD operations |
| `internal/daemon/docstore_test.go` | Unit tests for the store backend |
| `internal/protocol/messages.go` | New message types (store_put, store_get, etc.) — append to existing file |
| `internal/daemon/handler.go` | New `case` branches in the handler switch — append to existing file |
| `internal/daemon/daemon.go` | Wire up `DocStore` in `Run()` — modify existing file |
| `internal/cli/store.go` | CLI commands: `gr store put/get/list/rm` |

---

## Task 1: DocStore SQLite Backend

**Files:**
- Create: `internal/daemon/docstore.go`
- Create: `internal/daemon/docstore_test.go`

This is the core storage layer. It follows the same patterns as `internal/daemon/msgstore.go`: `sql.Open` with WAL mode, schema init in a helper, mutex-protected operations, `Close()` for cleanup.

### Schema

```sql
CREATE TABLE IF NOT EXISTS documents (
    repo     TEXT NOT NULL,
    key      TEXT NOT NULL,
    body     TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT '',
    author_id   TEXT NOT NULL DEFAULT '',
    author_name TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (repo, key)
);
CREATE INDEX IF NOT EXISTS idx_documents_repo ON documents(repo);
```

### Go struct

```go
type Document struct {
    Repo        string `json:"repo"`
    Key         string `json:"key"`
    Body        string `json:"body"`
    ContentType string `json:"content_type,omitempty"`
    AuthorID    string `json:"author_id,omitempty"`
    AuthorName  string `json:"author_name,omitempty"`
    CreatedAt   string `json:"created_at"`
    UpdatedAt   string `json:"updated_at"`
}
```

### DocStore interface

```go
type DocStore struct {
    db *sql.DB
    mu sync.Mutex
}

func NewDocStore(dbPath string) (*DocStore, error)
func (s *DocStore) Close() error
func (s *DocStore) Put(repo, key, body, contentType, authorID, authorName string) error
func (s *DocStore) Get(repo, key string) (*Document, error)
func (s *DocStore) List(repo, prefix string) ([]Document, error)
func (s *DocStore) Delete(repo, key string) error
```

- `Put` is an upsert — INSERT OR REPLACE. Sets `created_at` on insert, `updated_at` always. **`contentType` is required** — the caller must provide a non-empty value (e.g. `text/markdown`, `application/json`). The daemon rejects puts with empty content type.
- `Get` returns `nil, nil` when not found (no error for missing key).
- `List` returns documents matching the repo and optional key prefix. Returns metadata only (no body) to keep list responses small. Body is fetched via `Get`.
- `Delete` is a no-op if the key doesn't exist.

### Steps

- [ ] **Step 1: Write test for NewDocStore and Close**

```go
// internal/daemon/docstore_test.go
package daemon

import (
    "path/filepath"
    "testing"
)

func TestDocStoreOpenClose(t *testing.T) {
    dbPath := filepath.Join(t.TempDir(), "test.sqlite")
    ds, err := NewDocStore(dbPath)
    if err != nil {
        t.Fatalf("NewDocStore: %v", err)
    }
    if err := ds.Close(); err != nil {
        t.Fatalf("Close: %v", err)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestDocStoreOpenClose -v`
Expected: compilation error — `NewDocStore` not defined.

- [ ] **Step 3: Implement NewDocStore with schema init**

```go
// internal/daemon/docstore.go
package daemon

import (
    "database/sql"
    "fmt"
    "os"
    "path/filepath"
    "sync"
    "time"

    _ "modernc.org/sqlite"
)

type Document struct {
    Repo        string `json:"repo"`
    Key         string `json:"key"`
    Body        string `json:"body"`
    ContentType string `json:"content_type,omitempty"`
    AuthorID    string `json:"author_id,omitempty"`
    AuthorName  string `json:"author_name,omitempty"`
    CreatedAt   string `json:"created_at"`
    UpdatedAt   string `json:"updated_at"`
}

type DocStore struct {
    db *sql.DB
    mu sync.Mutex
}

func NewDocStore(dbPath string) (*DocStore, error) {
    if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
        return nil, fmt.Errorf("create docstore db dir: %w", err)
    }
    db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
    if err != nil {
        return nil, fmt.Errorf("open docstore db: %w", err)
    }
    if err := initDocStoreSchema(db); err != nil {
        db.Close()
        return nil, err
    }
    return &DocStore{db: db}, nil
}

func initDocStoreSchema(db *sql.DB) error {
    _, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS documents (
            repo         TEXT NOT NULL,
            key          TEXT NOT NULL,
            body         TEXT NOT NULL,
            content_type TEXT NOT NULL DEFAULT '',
            author_id    TEXT NOT NULL DEFAULT '',
            author_name  TEXT NOT NULL DEFAULT '',
            created_at   TEXT NOT NULL,
            updated_at   TEXT NOT NULL,
            PRIMARY KEY (repo, key)
        );
        CREATE INDEX IF NOT EXISTS idx_documents_repo ON documents(repo);
    `)
    return err
}

func (s *DocStore) Close() error {
    return s.db.Close()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestDocStoreOpenClose -v`
Expected: PASS

- [ ] **Step 5: Write test for Put and Get**

```go
func TestDocStorePutGet(t *testing.T) {
    dbPath := filepath.Join(t.TempDir(), "test.sqlite")
    ds, err := NewDocStore(dbPath)
    if err != nil {
        t.Fatalf("NewDocStore: %v", err)
    }
    defer ds.Close()

    err = ds.Put("/home/user/repo", "design/api.md", "# API Design", "text/markdown", "sess-1", "my-session")
    if err != nil {
        t.Fatalf("Put: %v", err)
    }

    doc, err := ds.Get("/home/user/repo", "design/api.md")
    if err != nil {
        t.Fatalf("Get: %v", err)
    }
    if doc == nil {
        t.Fatal("Get returned nil")
    }
    if doc.Body != "# API Design" {
        t.Errorf("Body = %q, want %q", doc.Body, "# API Design")
    }
    if doc.ContentType != "text/markdown" {
        t.Errorf("ContentType = %q, want %q", doc.ContentType, "text/markdown")
    }
    if doc.AuthorName != "my-session" {
        t.Errorf("AuthorName = %q, want %q", doc.AuthorName, "my-session")
    }
}

func TestDocStoreGetNotFound(t *testing.T) {
    dbPath := filepath.Join(t.TempDir(), "test.sqlite")
    ds, err := NewDocStore(dbPath)
    if err != nil {
        t.Fatalf("NewDocStore: %v", err)
    }
    defer ds.Close()

    doc, err := ds.Get("/home/user/repo", "nonexistent")
    if err != nil {
        t.Fatalf("Get: %v", err)
    }
    if doc != nil {
        t.Errorf("expected nil for missing key, got %+v", doc)
    }
}

func TestDocStorePutUpsert(t *testing.T) {
    dbPath := filepath.Join(t.TempDir(), "test.sqlite")
    ds, err := NewDocStore(dbPath)
    if err != nil {
        t.Fatalf("NewDocStore: %v", err)
    }
    defer ds.Close()

    ds.Put("/repo", "key", "v1", "", "s1", "")
    ds.Put("/repo", "key", "v2", "", "s2", "")

    doc, _ := ds.Get("/repo", "key")
    if doc.Body != "v2" {
        t.Errorf("Body after upsert = %q, want %q", doc.Body, "v2")
    }
}
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run TestDocStore -v`
Expected: compilation error — `Put`, `Get` not defined.

- [ ] **Step 7: Implement Put and Get**

```go
func (s *DocStore) Put(repo, key, body, contentType, authorID, authorName string) error {
    s.mu.Lock()
    defer s.mu.Unlock()

    now := time.Now().UTC().Format(time.RFC3339Nano)
    _, err := s.db.Exec(`
        INSERT INTO documents (repo, key, body, content_type, author_id, author_name, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(repo, key) DO UPDATE SET
            body = excluded.body,
            content_type = excluded.content_type,
            author_id = excluded.author_id,
            author_name = excluded.author_name,
            updated_at = excluded.updated_at
    `, repo, key, body, contentType, authorID, authorName, now, now)
    return err
}

func (s *DocStore) Get(repo, key string) (*Document, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    var doc Document
    err := s.db.QueryRow(`
        SELECT repo, key, body, content_type, author_id, author_name, created_at, updated_at
        FROM documents WHERE repo = ? AND key = ?
    `, repo, key).Scan(&doc.Repo, &doc.Key, &doc.Body, &doc.ContentType,
        &doc.AuthorID, &doc.AuthorName, &doc.CreatedAt, &doc.UpdatedAt)
    if err == sql.ErrNoRows {
        return nil, nil
    }
    if err != nil {
        return nil, err
    }
    return &doc, nil
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestDocStore -v`
Expected: PASS

- [ ] **Step 9: Write test for List and Delete**

```go
func TestDocStoreListAndDelete(t *testing.T) {
    dbPath := filepath.Join(t.TempDir(), "test.sqlite")
    ds, err := NewDocStore(dbPath)
    if err != nil {
        t.Fatalf("NewDocStore: %v", err)
    }
    defer ds.Close()

    ds.Put("/repo", "design/api.md", "api doc", "", "", "")
    ds.Put("/repo", "design/schema.md", "schema doc", "", "", "")
    ds.Put("/repo", "notes/todo.md", "todo", "", "", "")
    ds.Put("/other", "design/other.md", "other repo", "", "", "")

    // List all for /repo
    docs, err := ds.List("/repo", "")
    if err != nil {
        t.Fatalf("List: %v", err)
    }
    if len(docs) != 3 {
        t.Errorf("List all: got %d, want 3", len(docs))
    }

    // List with prefix
    docs, err = ds.List("/repo", "design/")
    if err != nil {
        t.Fatalf("List prefix: %v", err)
    }
    if len(docs) != 2 {
        t.Errorf("List design/: got %d, want 2", len(docs))
    }

    // List returns empty body (metadata only)
    for _, d := range docs {
        if d.Body != "" {
            t.Errorf("List should not include body, got %q", d.Body)
        }
    }

    // Delete
    err = ds.Delete("/repo", "design/api.md")
    if err != nil {
        t.Fatalf("Delete: %v", err)
    }
    doc, _ := ds.Get("/repo", "design/api.md")
    if doc != nil {
        t.Error("expected nil after delete")
    }

    // Delete nonexistent is a no-op
    err = ds.Delete("/repo", "nonexistent")
    if err != nil {
        t.Fatalf("Delete nonexistent: %v", err)
    }
}
```

- [ ] **Step 10: Implement List and Delete**

```go
func (s *DocStore) List(repo, prefix string) ([]Document, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    var rows *sql.Rows
    var err error
    if prefix == "" {
        rows, err = s.db.Query(`
            SELECT repo, key, content_type, author_id, author_name, created_at, updated_at
            FROM documents WHERE repo = ? ORDER BY key
        `, repo)
    } else {
        rows, err = s.db.Query(`
            SELECT repo, key, content_type, author_id, author_name, created_at, updated_at
            FROM documents WHERE repo = ? AND key LIKE ? ORDER BY key
        `, repo, prefix+"%")
    }
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var docs []Document
    for rows.Next() {
        var d Document
        if err := rows.Scan(&d.Repo, &d.Key, &d.ContentType, &d.AuthorID, &d.AuthorName, &d.CreatedAt, &d.UpdatedAt); err != nil {
            return nil, err
        }
        docs = append(docs, d)
    }
    return docs, rows.Err()
}

func (s *DocStore) Delete(repo, key string) error {
    s.mu.Lock()
    defer s.mu.Unlock()

    _, err := s.db.Exec(`DELETE FROM documents WHERE repo = ? AND key = ?`, repo, key)
    return err
}
```

- [ ] **Step 11: Run all DocStore tests**

Run: `go test ./internal/daemon/ -run TestDocStore -v`
Expected: all PASS

- [ ] **Step 12: Commit**

```bash
git add internal/daemon/docstore.go internal/daemon/docstore_test.go
git commit -m "feat: add DocStore SQLite backend for shared document storage"
```

---

## Task 2: Protocol Messages

**Files:**
- Modify: `internal/protocol/messages.go` — append new message structs

Add the control message types for store operations. These follow the same pattern as `MsgPubMsg`, `MsgSubMsg`, etc.

- [ ] **Step 1: Add store protocol messages to messages.go**

Append these structs to `internal/protocol/messages.go`, after the existing `MsgTopicsMsg`:

```go
// Document store messages (client -> daemon)

type StorePutMsg struct {
    Repo        string `json:"repo"`
    Key         string `json:"key"`
    Body        string `json:"body"`
    ContentType string `json:"content_type,omitempty"`
    AuthorID    string `json:"author_id,omitempty"`
    AuthorName  string `json:"author_name,omitempty"`
}

type StoreGetMsg struct {
    Repo string `json:"repo"`
    Key  string `json:"key"`
}

type StoreListMsg struct {
    Repo   string `json:"repo"`
    Prefix string `json:"prefix,omitempty"`
}

type StoreDeleteMsg struct {
    Repo string `json:"repo"`
    Key  string `json:"key"`
}

// Document store responses (daemon -> client)

type StoreDocumentMsg struct {
    Document *Document `json:"document,omitempty"`
    Found    bool      `json:"found"`
}

type StoreListResponseMsg struct {
    Documents []Document `json:"documents"`
}
```

Note: `StoreDocumentMsg` references `daemon.Document`. Since protocol shouldn't import daemon, we'll define a slimmed-down `protocol.StoreDocument` struct instead:

```go
type StoreDocument struct {
    Repo        string `json:"repo"`
    Key         string `json:"key"`
    Body        string `json:"body,omitempty"`
    ContentType string `json:"content_type,omitempty"`
    AuthorID    string `json:"author_id,omitempty"`
    AuthorName  string `json:"author_name,omitempty"`
    CreatedAt   string `json:"created_at"`
    UpdatedAt   string `json:"updated_at"`
}

type StoreGetResponseMsg struct {
    Document *StoreDocument `json:"document,omitempty"`
    Found    bool           `json:"found"`
}

type StoreListResponseMsg struct {
    Documents []StoreDocument `json:"documents"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/protocol/`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add internal/protocol/messages.go
git commit -m "feat: add store protocol message types"
```

---

## Task 3: Daemon Handler Integration

**Files:**
- Modify: `internal/daemon/handler.go` — add `case` branches for store messages
- Modify: `internal/daemon/daemon.go` — wire `DocStore` into `SessionManager` and `Run()`
- Modify: `internal/config/paths.go` — add `DocStoreDB` path

### Steps

- [ ] **Step 1: Add DocStoreDB to Paths**

In `internal/config/paths.go`, add `DocStoreDB string` to the `Paths` struct:

```go
type Paths struct {
    // ... existing fields ...
    MessagesDB string
    DocStoreDB string  // add after MessagesDB
}
```

Set it in `ResolvePaths()`:

```go
DocStoreDB: filepath.Join(dataDir, "docstore.sqlite"),
```

And in `WithDataDir()`:

```go
p.DocStoreDB = filepath.Join(dataDir, "docstore.sqlite")
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: success

- [ ] **Step 3: Wire DocStore into daemon startup**

In `internal/daemon/daemon.go`, add a `docStore` field to `SessionManager`:

Find where `messages *MsgStore` is declared as a field on SessionManager and add `docStore *DocStore` next to it.

In the `Run()` function, after the MsgStore creation block (`sm.messages = msgStore`), add:

```go
docStore, err := NewDocStore(paths.DocStoreDB)
if err != nil {
    return fmt.Errorf("open document store: %w", err)
}
defer func() { _ = docStore.Close() }()
sm.docStore = docStore
```

- [ ] **Step 4: Add handler cases**

In `internal/daemon/handler.go`, inside the `switch msg.Type` block, add these cases (after the existing `msg_topics` case or similar):

```go
case "store_put":
    var m protocol.StorePutMsg
    if err := protocol.DecodePayload(msg, &m); err != nil {
        sendControl("error", protocol.ErrorMsg{Message: "invalid store_put message"})
        continue
    }
    if m.Repo == "" || m.Key == "" || m.ContentType == "" {
        sendControl("error", protocol.ErrorMsg{Message: "repo, key, and content_type are required"})
        continue
    }
    if err := sm.docStore.Put(m.Repo, m.Key, m.Body, m.ContentType, m.AuthorID, m.AuthorName); err != nil {
        sendControl("error", protocol.ErrorMsg{Message: err.Error()})
    } else {
        sendControl("store_ok", struct{}{})
    }

case "store_get":
    var m protocol.StoreGetMsg
    if err := protocol.DecodePayload(msg, &m); err != nil {
        sendControl("error", protocol.ErrorMsg{Message: "invalid store_get message"})
        continue
    }
    doc, err := sm.docStore.Get(m.Repo, m.Key)
    if err != nil {
        sendControl("error", protocol.ErrorMsg{Message: err.Error()})
        continue
    }
    if doc == nil {
        sendControl("store_get_response", protocol.StoreGetResponseMsg{Found: false})
    } else {
        sendControl("store_get_response", protocol.StoreGetResponseMsg{
            Found: true,
            Document: &protocol.StoreDocument{
                Repo:        doc.Repo,
                Key:         doc.Key,
                Body:        doc.Body,
                ContentType: doc.ContentType,
                AuthorID:    doc.AuthorID,
                AuthorName:  doc.AuthorName,
                CreatedAt:   doc.CreatedAt,
                UpdatedAt:   doc.UpdatedAt,
            },
        })
    }

case "store_list":
    var m protocol.StoreListMsg
    if err := protocol.DecodePayload(msg, &m); err != nil {
        sendControl("error", protocol.ErrorMsg{Message: "invalid store_list message"})
        continue
    }
    docs, err := sm.docStore.List(m.Repo, m.Prefix)
    if err != nil {
        sendControl("error", protocol.ErrorMsg{Message: err.Error()})
        continue
    }
    result := make([]protocol.StoreDocument, len(docs))
    for i, d := range docs {
        result[i] = protocol.StoreDocument{
            Repo:        d.Repo,
            Key:         d.Key,
            ContentType: d.ContentType,
            AuthorID:    d.AuthorID,
            AuthorName:  d.AuthorName,
            CreatedAt:   d.CreatedAt,
            UpdatedAt:   d.UpdatedAt,
        }
    }
    sendControl("store_list_response", protocol.StoreListResponseMsg{Documents: result})

case "store_delete":
    var m protocol.StoreDeleteMsg
    if err := protocol.DecodePayload(msg, &m); err != nil {
        sendControl("error", protocol.ErrorMsg{Message: "invalid store_delete message"})
        continue
    }
    if err := sm.docStore.Delete(m.Repo, m.Key); err != nil {
        sendControl("error", protocol.ErrorMsg{Message: err.Error()})
    } else {
        sendControl("store_ok", struct{}{})
    }
```

- [ ] **Step 5: Verify it compiles**

Run: `go build ./...`
Expected: success

- [ ] **Step 6: Commit**

```bash
git add internal/config/paths.go internal/daemon/daemon.go internal/daemon/handler.go
git commit -m "feat: wire DocStore into daemon handler and startup"
```

---

## Task 4: CLI Commands

**Files:**
- Create: `internal/cli/store.go`

This follows the same pattern as `internal/cli/msg.go`: a parent `storeCmd` with subcommands. Each subcommand connects to the daemon, sends a control message, reads the response.

### Repo resolution

The CLI needs to determine the repo path for scoping. Resolution order:
1. `--repo` flag (explicit)
2. `GRAITH_SESSION_ID` env → look up session's `RepoPath` via `list` message
3. CWD's git root (via `git rev-parse --show-toplevel`)

This is implemented as a `resolveRepo` helper.

### Steps

- [ ] **Step 1: Create `internal/cli/store.go` with the parent command and repo resolution**

```go
package cli

import (
    "encoding/json"
    "fmt"
    "os"
    "os/exec"
    "strings"
    "text/tabwriter"

    "github.com/d0ugal/graith/internal/client"
    "github.com/d0ugal/graith/internal/protocol"
    "github.com/spf13/cobra"
)

var storeCmd = &cobra.Command{
    Use:     "store",
    Aliases: []string{"s"},
    Short:   "Shared document store",
}

var storeRepoFlag string

func resolveRepoPath(c *client.Client) (string, error) {
    if storeRepoFlag != "" {
        return storeRepoFlag, nil
    }

    sessionID := os.Getenv("GRAITH_SESSION_ID")
    if sessionID != "" {
        c.SendControl("list", struct{}{})
        resp, err := c.ReadControlResponse()
        if err != nil {
            return "", err
        }
        var list protocol.SessionListMsg
        if err := protocol.DecodePayload(resp, &list); err != nil {
            return "", err
        }
        for _, s := range list.Sessions {
            if s.ID == sessionID {
                return s.RepoPath, nil
            }
        }
    }

    out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
    if err != nil {
        return "", fmt.Errorf("cannot determine repo: use --repo or run from a git directory")
    }
    return strings.TrimSpace(string(out)), nil
}
```

- [ ] **Step 2: Add `gr store put` subcommand**

```go
var (
    storePutContentType string
    storePutFile        string
)

func expandContentType(ct string) (string, error) {
    switch ct {
    case "md", "markdown":
        return "text/markdown", nil
    case "json":
        return "application/json", nil
    case "text":
        return "text/plain", nil
    case "":
        return "", fmt.Errorf("--type is required (md, json, text, or a MIME type)")
    default:
        if !strings.Contains(ct, "/") {
            return "", fmt.Errorf("unknown content type shorthand %q; use md, json, text, or a full MIME type", ct)
        }
        return ct, nil
    }
}

var storePutCmd = &cobra.Command{
    Use:   "put <key> [body]",
    Short: "Store a document",
    Long:  "Store a document by key. Body can be an argument, --file, or stdin.\n\n--type is required: md, json, text, or a full MIME type.",
    Args:  cobra.RangeArgs(1, 2),
    RunE: func(cmd *cobra.Command, args []string) error {
        contentType, err := expandContentType(storePutContentType)
        if err != nil {
            return err
        }

        c, err := client.Connect(cfg, paths, cfgFile)
        if err != nil {
            return err
        }
        defer c.Close()

        repo, err := resolveRepoPath(c)
        if err != nil {
            return err
        }

        key := args[0]
        bodyArgs := args[1:]
        body, err := resolveBody(bodyArgs, storePutFile)
        if err != nil {
            return err
        }

        senderID, senderName := detectSender()

        c.SendControl("store_put", protocol.StorePutMsg{
            Repo:        repo,
            Key:         key,
            Body:        body,
            ContentType: contentType,
            AuthorID:    senderID,
            AuthorName:  senderName,
        })

        resp, err := c.ReadControlResponse()
        if err != nil {
            return err
        }
        if resp.Type == "error" {
            var e protocol.ErrorMsg
            protocol.DecodePayload(resp, &e)
            return fmt.Errorf("%s", e.Message)
        }

        if jsonOutput {
            return out.JSON(struct {
                Key  string `json:"key"`
                Repo string `json:"repo"`
            }{key, repo})
        }
        out.Print("Stored %s\n", key)
        return nil
    },
}
```

- [ ] **Step 3: Add `gr store get` subcommand**

```go
var storeGetCmd = &cobra.Command{
    Use:   "get <key>",
    Short: "Retrieve a document",
    Args:  cobra.ExactArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        c, err := client.Connect(cfg, paths, cfgFile)
        if err != nil {
            return err
        }
        defer c.Close()

        repo, err := resolveRepoPath(c)
        if err != nil {
            return err
        }

        c.SendControl("store_get", protocol.StoreGetMsg{
            Repo: repo,
            Key:  args[0],
        })

        resp, err := c.ReadControlResponse()
        if err != nil {
            return err
        }
        if resp.Type == "error" {
            var e protocol.ErrorMsg
            protocol.DecodePayload(resp, &e)
            return fmt.Errorf("%s", e.Message)
        }

        var result protocol.StoreGetResponseMsg
        if err := protocol.DecodePayload(resp, &result); err != nil {
            return err
        }

        if !result.Found {
            return fmt.Errorf("document %q not found", args[0])
        }

        if jsonOutput {
            return out.JSON(result.Document)
        }
        fmt.Print(result.Document.Body)
        return nil
    },
}
```

- [ ] **Step 4: Add `gr store list` subcommand**

```go
var storeListCmd = &cobra.Command{
    Use:   "list [prefix]",
    Short: "List documents",
    Aliases: []string{"ls"},
    Args:  cobra.MaximumNArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        c, err := client.Connect(cfg, paths, cfgFile)
        if err != nil {
            return err
        }
        defer c.Close()

        repo, err := resolveRepoPath(c)
        if err != nil {
            return err
        }

        prefix := ""
        if len(args) > 0 {
            prefix = args[0]
        }

        c.SendControl("store_list", protocol.StoreListMsg{
            Repo:   repo,
            Prefix: prefix,
        })

        resp, err := c.ReadControlResponse()
        if err != nil {
            return err
        }
        if resp.Type == "error" {
            var e protocol.ErrorMsg
            protocol.DecodePayload(resp, &e)
            return fmt.Errorf("%s", e.Message)
        }

        var result protocol.StoreListResponseMsg
        if err := protocol.DecodePayload(resp, &result); err != nil {
            return err
        }

        if jsonOutput {
            return out.JSON(result)
        }

        if len(result.Documents) == 0 {
            out.Print("No documents found\n")
            return nil
        }

        w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
        fmt.Fprintf(w, "KEY\tTYPE\tAUTHOR\tUPDATED\n")
        for _, d := range result.Documents {
            author := d.AuthorName
            if author == "" {
                author = d.AuthorID
            }
            ct := d.ContentType
            if ct == "" {
                ct = "-"
            }
            fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.Key, ct, author, d.UpdatedAt)
        }
        w.Flush()
        return nil
    },
}
```

- [ ] **Step 5: Add `gr store rm` subcommand**

```go
var storeRmCmd = &cobra.Command{
    Use:   "rm <key>",
    Short: "Remove a document",
    Args:  cobra.ExactArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        c, err := client.Connect(cfg, paths, cfgFile)
        if err != nil {
            return err
        }
        defer c.Close()

        repo, err := resolveRepoPath(c)
        if err != nil {
            return err
        }

        c.SendControl("store_delete", protocol.StoreDeleteMsg{
            Repo: repo,
            Key:  args[0],
        })

        resp, err := c.ReadControlResponse()
        if err != nil {
            return err
        }
        if resp.Type == "error" {
            var e protocol.ErrorMsg
            protocol.DecodePayload(resp, &e)
            return fmt.Errorf("%s", e.Message)
        }

        if jsonOutput {
            return out.JSON(struct {
                Key     string `json:"key"`
                Deleted bool   `json:"deleted"`
            }{args[0], true})
        }
        out.Print("Removed %s\n", args[0])
        return nil
    },
}
```

- [ ] **Step 6: Add init() to wire up commands and flags**

```go
func init() {
    rootCmd.AddCommand(storeCmd)
    storeCmd.PersistentFlags().StringVar(&storeRepoFlag, "repo", "", "repo path (default: auto-detect)")

    storeCmd.AddCommand(storePutCmd)
    storePutCmd.Flags().StringVar(&storePutContentType, "type", "", "content type (e.g. text/markdown)")
    storePutCmd.Flags().StringVarP(&storePutFile, "file", "f", "", "read body from file")

    storeCmd.AddCommand(storeGetCmd)
    storeCmd.AddCommand(storeListCmd)
    storeCmd.AddCommand(storeRmCmd)
}
```

- [ ] **Step 7: Verify it compiles**

Run: `go build ./...`
Expected: success

- [ ] **Step 8: Commit**

```bash
git add internal/cli/store.go
git commit -m "feat: add gr store put/get/list/rm CLI commands"
```

---

## Task 5: Integration Smoke Test

**Files:**
- Create: `internal/daemon/docstore_integration_test.go` (or add to an existing integration test file if there is one)

This tests the full round-trip: client → daemon → SQLite → daemon → client. Follow the pattern of existing integration tests in `internal/integration/`.

- [ ] **Step 1: Check how integration tests are structured**

Look at `internal/integration/` for the test harness pattern — how they start a daemon, connect a client, and exercise the control protocol. Follow that pattern.

- [ ] **Step 2: Write an integration test for the store round-trip**

The test should:
1. Start a test daemon (using the existing integration test helper)
2. Send `store_put` with a key and body
3. Send `store_get` and verify the document comes back
4. Send `store_list` and verify the document appears
5. Send `store_delete` and verify `store_get` returns `found: false`

- [ ] **Step 3: Run the integration test**

Run: `go test ./internal/integration/ -run TestDocStore -v`
Expected: PASS

- [ ] **Step 4: Run full test suite with race detector**

Run: `go test -race ./...`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/integration/
git commit -m "test: add integration test for document store"
```

---

## Task 6: Documentation

**Files:**
- Modify: `CLAUDE.md` — add store docs to the relevant sections

### Steps

- [ ] **Step 1: Add store documentation to CLAUDE.md**

Add a section under "Inter-agent messaging" (or as a peer section) documenting the store:

```markdown
### Shared document store

Sessions can persist documents that survive worktree deletion:

\`\`\`bash
# Store a document (scoped to the current repo)
gr store put design/api.md "# API Design\n\nEndpoints: ..."
gr store put design/api.md --file ./api-design.md
echo "content" | gr store put notes/finding.md

# Retrieve a document
gr store get design/api.md

# List documents (optional prefix filter)
gr store list
gr store list design/

# Remove a document
gr store rm design/api.md

# Explicit repo scoping (when not in a session)
gr store list --repo ~/Code/graith
\`\`\`

Documents are scoped per-repo — sessions for `~/Code/graith` share one
namespace, sessions for `~/Code/other` share another. Keys are
slash-separated paths (e.g. `design/api.md`, `research/findings`).

Use `gr store` for artifacts you want to keep but don't want to commit:
design docs, research notes, build outputs, shared context between agents.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: add gr store documentation to CLAUDE.md"
```

---

## Task 7: Format and Final Verification

- [ ] **Step 1: Run gofmt on all modified files**

Run: `gofmt -w internal/daemon/docstore.go internal/cli/store.go internal/config/paths.go internal/protocol/messages.go`

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: no issues

- [ ] **Step 3: Run full test suite**

Run: `go test -race ./...`
Expected: all PASS

- [ ] **Step 4: Build the binary**

Run: `make build`
Expected: produces `./gr` binary

- [ ] **Step 5: Commit any formatting fixes**

```bash
git add -A
git commit -m "chore: gofmt and final cleanup"
```
