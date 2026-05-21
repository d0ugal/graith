package daemon

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Document is a key/value artifact stored in the document store.
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

// DocStore is a SQLite-backed persistent document store.
type DocStore struct {
	db *sql.DB
	mu sync.Mutex
}

// NewDocStore opens (or creates) the SQLite database at dbPath and initialises
// the schema.
func NewDocStore(dbPath string) (*DocStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("create documents db dir: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open documents db: %w", err)
	}

	if err := initDocSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	return &DocStore{db: db}, nil
}

func initDocSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS documents (
			repo         TEXT NOT NULL,
			key          TEXT NOT NULL,
			body         TEXT NOT NULL,
			content_type TEXT NOT NULL CHECK(content_type <> ''),
			author_id    TEXT NOT NULL DEFAULT '',
			author_name  TEXT NOT NULL DEFAULT '',
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL,
			PRIMARY KEY (repo, key)
		);
	`)
	if err != nil {
		return fmt.Errorf("init documents schema: %w", err)
	}
	return nil
}

// Close closes the underlying database connection.
func (s *DocStore) Close() error {
	return s.db.Close()
}

// Put inserts or updates a document. If the key already exists the body,
// content_type, author fields and updated_at are updated but created_at is
// preserved.
func (s *DocStore) Put(repo, key, body, contentType, authorID, authorName string) error {
	if key == "" {
		return errors.New("document key must not be empty")
	}
	if strings.HasPrefix(key, "/") {
		return errors.New("document key must not have a leading slash")
	}
	if contentType == "" {
		return errors.New("document content_type must not be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`
		INSERT INTO documents (repo, key, body, content_type, author_id, author_name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo, key) DO UPDATE SET
			body         = excluded.body,
			content_type = excluded.content_type,
			author_id    = excluded.author_id,
			author_name  = excluded.author_name,
			updated_at   = excluded.updated_at
	`, repo, key, body, contentType, authorID, authorName, now, now)
	if err != nil {
		return fmt.Errorf("put document: %w", err)
	}
	return nil
}

// Get retrieves a document by repo and key. Returns nil, nil when the document
// does not exist.
func (s *DocStore) Get(repo, key string) (*Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var d Document
	err := s.db.QueryRow(`
		SELECT repo, key, body, content_type, author_id, author_name, created_at, updated_at
		FROM documents
		WHERE repo = ? AND key = ?
	`, repo, key).Scan(&d.Repo, &d.Key, &d.Body, &d.ContentType, &d.AuthorID, &d.AuthorName, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}
	return &d, nil
}

// List returns metadata (no body) for all documents in repo whose key starts
// with prefix. An empty prefix returns all documents in the repo.
func (s *DocStore) List(repo, prefix string) ([]Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var (
		rows *sql.Rows
		err  error
	)
	if prefix == "" {
		rows, err = s.db.Query(`
			SELECT repo, key, content_type, author_id, author_name, created_at, updated_at
			FROM documents
			WHERE repo = ?
			ORDER BY key ASC
		`, repo)
	} else {
		end := prefixEnd(prefix)
		rows, err = s.db.Query(`
			SELECT repo, key, content_type, author_id, author_name, created_at, updated_at
			FROM documents
			WHERE repo = ? AND key >= ? AND key < ?
			ORDER BY key ASC
		`, repo, prefix, end)
	}
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	defer rows.Close()

	var docs []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.Repo, &d.Key, &d.ContentType, &d.AuthorID, &d.AuthorName, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

// Delete removes a document. It is a no-op if the document does not exist.
func (s *DocStore) Delete(repo, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM documents WHERE repo = ? AND key = ?`, repo, key)
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
}

// prefixEnd returns the smallest string that is lexicographically greater than
// all strings with the given prefix. It increments the last byte of the
// prefix; if the last byte overflows, it is removed and the process repeats.
// Returns an empty string if the prefix is empty or consists entirely of 0xff
// bytes (in which case the caller should use an unbounded scan).
func prefixEnd(prefix string) string {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xff {
			b[i]++
			return string(b[:i+1])
		}
	}
	// All bytes were 0xff — no upper bound exists; return the maximum possible
	// string for the type (not reachable in practice for normal key names).
	return "\xff"
}
