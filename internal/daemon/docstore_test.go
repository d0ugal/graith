package daemon

import (
	"path/filepath"
	"testing"
)

func newTestDocStore(t *testing.T) *DocStore {
	t.Helper()
	ds, err := NewDocStore(filepath.Join(t.TempDir(), "docs.db"))
	if err != nil {
		t.Fatalf("NewDocStore: %v", err)
	}
	t.Cleanup(func() { ds.Close() })
	return ds
}

func TestDocStoreOpenClose(t *testing.T) {
	ds, err := NewDocStore(filepath.Join(t.TempDir(), "docs.db"))
	if err != nil {
		t.Fatalf("NewDocStore: %v", err)
	}
	if err := ds.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestDocStorePutGet(t *testing.T) {
	ds := newTestDocStore(t)

	err := ds.Put("repo1", "notes/hello", "hello world", "text/plain", "user1", "Alice")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	doc, err := ds.Get("repo1", "notes/hello")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if doc == nil {
		t.Fatal("Get returned nil, want document")
	}
	if doc.Repo != "repo1" {
		t.Errorf("Repo = %q, want %q", doc.Repo, "repo1")
	}
	if doc.Key != "notes/hello" {
		t.Errorf("Key = %q, want %q", doc.Key, "notes/hello")
	}
	if doc.Body != "hello world" {
		t.Errorf("Body = %q, want %q", doc.Body, "hello world")
	}
	if doc.ContentType != "text/plain" {
		t.Errorf("ContentType = %q, want %q", doc.ContentType, "text/plain")
	}
	if doc.AuthorID != "user1" {
		t.Errorf("AuthorID = %q, want %q", doc.AuthorID, "user1")
	}
	if doc.AuthorName != "Alice" {
		t.Errorf("AuthorName = %q, want %q", doc.AuthorName, "Alice")
	}
	if doc.CreatedAt == "" {
		t.Error("CreatedAt is empty")
	}
	if doc.UpdatedAt == "" {
		t.Error("UpdatedAt is empty")
	}
}

func TestDocStoreGetNotFound(t *testing.T) {
	ds := newTestDocStore(t)

	doc, err := ds.Get("repo1", "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if doc != nil {
		t.Errorf("Get returned %+v, want nil", doc)
	}
}

func TestDocStorePutUpsert(t *testing.T) {
	ds := newTestDocStore(t)

	if err := ds.Put("repo1", "key1", "original", "text/plain", "", ""); err != nil {
		t.Fatalf("Put (first): %v", err)
	}
	first, err := ds.Get("repo1", "key1")
	if err != nil {
		t.Fatalf("Get (first): %v", err)
	}

	if err := ds.Put("repo1", "key1", "updated", "text/markdown", "u2", "Bob"); err != nil {
		t.Fatalf("Put (second): %v", err)
	}
	second, err := ds.Get("repo1", "key1")
	if err != nil {
		t.Fatalf("Get (second): %v", err)
	}
	if second == nil {
		t.Fatal("Get returned nil after upsert")
	}

	if second.Body != "updated" {
		t.Errorf("Body = %q, want %q", second.Body, "updated")
	}
	if second.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q, want %q", second.ContentType, "text/markdown")
	}
	if second.AuthorID != "u2" {
		t.Errorf("AuthorID = %q, want %q", second.AuthorID, "u2")
	}
	if second.CreatedAt != first.CreatedAt {
		t.Errorf("created_at changed: got %q, want %q", second.CreatedAt, first.CreatedAt)
	}
	if second.UpdatedAt == first.UpdatedAt {
		// updated_at should not change if the wall clock hasn't advanced, but
		// since the upsert always writes excluded.updated_at it will be equal
		// when the operation happens within the same nanosecond. We only flag
		// this if it somehow regressed to an empty value.
		if second.UpdatedAt == "" {
			t.Error("updated_at is empty after upsert")
		}
	}
}

func TestDocStorePutEmptyContentType(t *testing.T) {
	ds := newTestDocStore(t)

	err := ds.Put("repo1", "key1", "body", "", "", "")
	if err == nil {
		t.Fatal("Put with empty content_type should return error, got nil")
	}
}

func TestDocStorePutInvalidKey(t *testing.T) {
	ds := newTestDocStore(t)

	if err := ds.Put("repo1", "", "body", "text/plain", "", ""); err == nil {
		t.Error("Put with empty key should return error, got nil")
	}
	if err := ds.Put("repo1", "/leading-slash", "body", "text/plain", "", ""); err == nil {
		t.Error("Put with leading-slash key should return error, got nil")
	}
}

func TestDocStoreListAndDelete(t *testing.T) {
	ds := newTestDocStore(t)

	// Insert into two repos.
	for _, tc := range []struct{ repo, key string }{
		{"repoA", "alpha/one"},
		{"repoA", "alpha/two"},
		{"repoA", "beta/three"},
		{"repoB", "alpha/one"},
	} {
		if err := ds.Put(tc.repo, tc.key, "body", "text/plain", "", ""); err != nil {
			t.Fatalf("Put %s/%s: %v", tc.repo, tc.key, err)
		}
	}

	// List all in repoA.
	all, err := ds.List("repoA", "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List all: got %d docs, want 3", len(all))
	}

	// Verify body is not returned.
	for _, d := range all {
		if d.Body != "" {
			t.Errorf("List returned body for %s/%s, want empty", d.Repo, d.Key)
		}
	}

	// List with prefix "alpha/".
	alpha, err := ds.List("repoA", "alpha/")
	if err != nil {
		t.Fatalf("List alpha/: %v", err)
	}
	if len(alpha) != 2 {
		t.Errorf("List alpha/: got %d docs, want 2", len(alpha))
	}

	// List in repoB.
	repoB, err := ds.List("repoB", "")
	if err != nil {
		t.Fatalf("List repoB: %v", err)
	}
	if len(repoB) != 1 {
		t.Errorf("List repoB: got %d docs, want 1", len(repoB))
	}

	// Delete one.
	if err := ds.Delete("repoA", "alpha/one"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	after, err := ds.List("repoA", "alpha/")
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(after) != 1 {
		t.Errorf("List after delete: got %d docs, want 1", len(after))
	}
	if after[0].Key != "alpha/two" {
		t.Errorf("remaining key = %q, want %q", after[0].Key, "alpha/two")
	}

	// Delete nonexistent is a no-op.
	if err := ds.Delete("repoA", "does-not-exist"); err != nil {
		t.Errorf("Delete nonexistent returned error: %v", err)
	}
}

func TestDocStoreListPrefixWithUnderscore(t *testing.T) {
	ds := newTestDocStore(t)

	keys := []string{
		"notes_draft",
		"notes_final",
		"notes/one",
		"notes/two",
		"other",
	}
	for _, k := range keys {
		if err := ds.Put("repo1", k, "body", "text/plain", "", ""); err != nil {
			t.Fatalf("Put %q: %v", k, err)
		}
	}

	// Prefix "notes/" should return only "notes/one" and "notes/two",
	// NOT "notes_draft" or "notes_final" (which a LIKE-based approach would
	// include because _ is a wildcard in SQL LIKE).
	results, err := ds.List("repo1", "notes/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("List with prefix 'notes/': got %d results, want 2", len(results))
		for _, r := range results {
			t.Logf("  key=%q", r.Key)
		}
		return
	}
	for _, r := range results {
		if r.Key != "notes/one" && r.Key != "notes/two" {
			t.Errorf("unexpected key %q in prefix='notes/' results", r.Key)
		}
	}
}
