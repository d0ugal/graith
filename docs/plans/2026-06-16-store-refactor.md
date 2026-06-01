# Store Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the SQLite-backed document store with a flat-file git-backed store, removing daemon dependency for store operations.

**Architecture:** New `internal/store` package handles all file I/O and git operations directly. CLI commands call into this package instead of sending control messages through the daemon. Each project repo gets its own git-managed store directory under `~/.local/share/graith/store/<reponame>-<hash>/`.

**Tech Stack:** Go stdlib (`os`, `os/exec`, `filepath`, `syscall`), git CLI, `flock` for concurrency.

**Spec:** `docs/superpowers/specs/2026-06-16-store-refactor-design.md`

---

### Task 1: Create `internal/store` package — key validation and path helpers

**Files:**
- Create: `internal/store/store.go`
- Create: `internal/store/store_test.go`

- [ ] **Step 1: Write failing tests for `ValidateKey`**

```go
package store

import "testing"

func TestValidateKey(t *testing.T) {
	valid := []string{
		"design/api.md",
		"notes.txt",
		"tribunal/2026-06-15.md",
		"a/b/c/d.json",
		"single",
	}
	for _, k := range valid {
		if err := ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q) = %v, want nil", k, err)
		}
	}

	invalid := []struct {
		key    string
		substr string
	}{
		{"", "empty"},
		{"/leading", "leading slash"},
		{"path/../escape", ".."},
		{"../up", ".."},
		{"-dashstart.md", "starts with"},
		{"has\x00null", "control"},
		{"has\nnewline", "control"},
		{"has\ttab", "control"},
	}
	for _, tc := range invalid {
		err := ValidateKey(tc.key)
		if err == nil {
			t.Errorf("ValidateKey(%q) = nil, want error containing %q", tc.key, tc.substr)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestValidateKey -v`
Expected: FAIL — `ValidateKey` not defined

- [ ] **Step 3: Implement `ValidateKey`**

```go
package store

import (
	"fmt"
	"strings"
	"unicode"
)

func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("key must not be empty")
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("key must not have a leading slash")
	}
	if strings.HasPrefix(key, "-") {
		return fmt.Errorf("key must not start with a dash (starts with '-')")
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == ".." {
			return fmt.Errorf("key must not contain '..' path components")
		}
	}
	for _, r := range key {
		if r == 0 || unicode.IsControl(r) {
			return fmt.Errorf("key must not contain control characters")
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestValidateKey -v`
Expected: PASS

- [ ] **Step 5: Write failing tests for `StorePath`**

Add to `store_test.go`:

```go
func TestStorePath(t *testing.T) {
	path := StorePath("/data", "/home/user/Code/graith")
	if path == "" {
		t.Fatal("StorePath returned empty string")
	}
	if !strings.HasPrefix(path, "/data/store/graith-") {
		t.Errorf("StorePath = %q, want prefix /data/store/graith-", path)
	}

	// Same repo root always produces the same path.
	path2 := StorePath("/data", "/home/user/Code/graith")
	if path != path2 {
		t.Errorf("StorePath not deterministic: %q != %q", path, path2)
	}

	// Different repo root produces different path.
	path3 := StorePath("/data", "/home/user/Code/other")
	if path == path3 {
		t.Errorf("Different repos should produce different paths")
	}
}
```

Add `"strings"` to the test import if not already present.

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestStorePath -v`
Expected: FAIL — `StorePath` not defined

- [ ] **Step 7: Implement `StorePath`**

Add to `store.go`. The hash function is copied from `internal/daemon/daemon.go:repoHash` — it must produce the same values so the naming is consistent with existing worktree directory naming conventions.

```go
import (
	"encoding/hex"
	"path/filepath"
)

func repoHash(repoPath string) string {
	h := uint64(0)
	for _, c := range repoPath {
		h = h*31 + uint64(c)
	}
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(h >> (i * 8))
	}
	return hex.EncodeToString(b)[:12]
}

func StorePath(dataDir, repoRoot string) string {
	repoName := filepath.Base(repoRoot)
	return filepath.Join(dataDir, "store", repoName+"-"+repoHash(repoRoot))
}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestStorePath -v`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat: add store package with key validation and path helpers"
```

---

### Task 2: Add `Init` and git repo management

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

- [ ] **Step 1: Write failing test for `Init`**

Add to `store_test.go`:

```go
import "os"

func TestInit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")

	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// .git directory should exist.
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatalf(".git not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal(".git is not a directory")
	}

	// Calling Init again should be idempotent.
	if err := Init(dir); err != nil {
		t.Fatalf("Init (idempotent): %v", err)
	}
}
```

Add `"path/filepath"` and `"os"` to test imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestInit -v`
Expected: FAIL — `Init` not defined

- [ ] **Step 3: Implement `Init`**

Add to `store.go`:

```go
import "os/exec"

func Init(storePath string) error {
	if err := os.MkdirAll(storePath, 0o700); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}

	gitDir := filepath.Join(storePath, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return nil // already initialized
	}

	if err := git(storePath, "init", "--quiet"); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	if err := git(storePath, "config", "user.name", "graith"); err != nil {
		return fmt.Errorf("git config user.name: %w", err)
	}
	if err := git(storePath, "config", "user.email", "graith@localhost"); err != nil {
		return fmt.Errorf("git config user.email: %w", err)
	}
	if err := git(storePath, "config", "core.autocrlf", "false"); err != nil {
		return fmt.Errorf("git config core.autocrlf: %w", err)
	}
	return nil
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestInit -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat: add store Init with git repo setup"
```

---

### Task 3: Add `Put` and `Get` with file locking

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

- [ ] **Step 1: Write failing test for `Put` and `Get`**

Add to `store_test.go`:

```go
func newTestStore(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return dir
}

func TestPutGet(t *testing.T) {
	dir := newTestStore(t)

	if err := Put(dir, "design/api.md", "# API Design\n"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	body, err := Get(dir, "design/api.md")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if body != "# API Design\n" {
		t.Errorf("Get body = %q, want %q", body, "# API Design\n")
	}

	// File should exist on disk.
	diskPath := filepath.Join(dir, "design", "api.md")
	if _, err := os.Stat(diskPath); err != nil {
		t.Errorf("file not on disk: %v", err)
	}
}

func TestPutOverwrite(t *testing.T) {
	dir := newTestStore(t)

	if err := Put(dir, "notes.txt", "original"); err != nil {
		t.Fatalf("Put (first): %v", err)
	}
	if err := Put(dir, "notes.txt", "updated"); err != nil {
		t.Fatalf("Put (second): %v", err)
	}

	body, err := Get(dir, "notes.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if body != "updated" {
		t.Errorf("Get body = %q, want %q", body, "updated")
	}
}

func TestGetNotFound(t *testing.T) {
	dir := newTestStore(t)

	_, err := Get(dir, "nonexistent.md")
	if err == nil {
		t.Fatal("Get nonexistent should return error")
	}
}

func TestPutInvalidKey(t *testing.T) {
	dir := newTestStore(t)

	if err := Put(dir, "../escape", "bad"); err == nil {
		t.Error("Put with path traversal key should fail")
	}
	if err := Put(dir, "", "bad"); err == nil {
		t.Error("Put with empty key should fail")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/ -run "TestPut|TestGet" -v`
Expected: FAIL — `Put` and `Get` not defined

- [ ] **Step 3: Implement `Put`, `Get`, and the file lock helper**

Add to `store.go`:

```go
import "syscall"

func withLock(storePath string, fn func() error) error {
	lockPath := filepath.Join(storePath, "store.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire store lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	return fn()
}

func Put(storePath, key, body string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}

	return withLock(storePath, func() error {
		filePath := filepath.Join(storePath, key)
		if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
			return fmt.Errorf("create parent dirs: %w", err)
		}
		if err := os.WriteFile(filePath, []byte(body), 0o600); err != nil {
			return fmt.Errorf("write file: %w", err)
		}
		if err := git(storePath, "add", "--", key); err != nil {
			return fmt.Errorf("git add: %w", err)
		}
		msg := CommitMessage("update", key)
		if err := git(storePath, "commit", "-m", msg, "--", key); err != nil {
			return fmt.Errorf("git commit: %w", err)
		}
		return nil
	})
}

func Get(storePath, key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}

	data, err := os.ReadFile(filepath.Join(storePath, key))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", key, err)
	}
	return string(data), nil
}
```

- [ ] **Step 4: Write `CommitMessage` helper**

Add to `store.go`:

```go
func CommitMessage(action, key string) string {
	subject := fmt.Sprintf("store: %s %s", action, key)

	sessionID := os.Getenv("GRAITH_SESSION_ID")
	sessionName := os.Getenv("GRAITH_SESSION_NAME")
	agentType := os.Getenv("GRAITH_AGENT_TYPE")

	if sessionID == "" && sessionName == "" {
		return subject
	}

	trailer := "\n"
	if sessionName != "" {
		trailer += fmt.Sprintf("\nsession: %s (%s)", sessionName, sessionID)
	} else if sessionID != "" {
		trailer += fmt.Sprintf("\nsession: %s", sessionID)
	}
	if agentType != "" {
		trailer += fmt.Sprintf("\nagent: %s", agentType)
	}
	return subject + trailer
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/store/ -run "TestPut|TestGet" -v`
Expected: PASS

- [ ] **Step 6: Write test for `CommitMessage`**

Add to `store_test.go`:

```go
func TestCommitMessage(t *testing.T) {
	// Clear env for clean test.
	t.Setenv("GRAITH_SESSION_ID", "")
	t.Setenv("GRAITH_SESSION_NAME", "")
	t.Setenv("GRAITH_AGENT_TYPE", "")

	msg := CommitMessage("update", "design/api.md")
	if msg != "store: update design/api.md" {
		t.Errorf("no-session message = %q", msg)
	}

	t.Setenv("GRAITH_SESSION_ID", "abc123")
	t.Setenv("GRAITH_SESSION_NAME", "fix-overlay")
	t.Setenv("GRAITH_AGENT_TYPE", "claude")

	msg = CommitMessage("remove", "old.md")
	if !strings.Contains(msg, "store: remove old.md") {
		t.Errorf("message missing subject: %q", msg)
	}
	if !strings.Contains(msg, "session: fix-overlay (abc123)") {
		t.Errorf("message missing session trailer: %q", msg)
	}
	if !strings.Contains(msg, "agent: claude") {
		t.Errorf("message missing agent trailer: %q", msg)
	}
}
```

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestCommitMessage -v`
Expected: PASS

- [ ] **Step 8: Verify git history is created by Put**

Add to `store_test.go`:

```go
func TestPutCreatesGitCommit(t *testing.T) {
	dir := newTestStore(t)

	t.Setenv("GRAITH_SESSION_ID", "")
	t.Setenv("GRAITH_SESSION_NAME", "")
	t.Setenv("GRAITH_AGENT_TYPE", "")

	if err := Put(dir, "test.md", "content"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Check git log has a commit.
	cmd := exec.Command("git", "log", "--oneline", "-1")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if !strings.Contains(string(out), "store: update test.md") {
		t.Errorf("git log = %q, want commit message containing 'store: update test.md'", string(out))
	}
}
```

Add `"os/exec"` to test imports.

- [ ] **Step 9: Run all store tests**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat: add store Put, Get, CommitMessage with file locking"
```

---

### Task 4: Add `List` and `Remove`

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

- [ ] **Step 1: Write failing tests for `List`**

Add to `store_test.go`:

```go
import "time"

func TestList(t *testing.T) {
	dir := newTestStore(t)

	keys := []string{
		"alpha/one.md",
		"alpha/two.md",
		"beta/three.md",
	}
	for _, k := range keys {
		if err := Put(dir, k, "body"); err != nil {
			t.Fatalf("Put %q: %v", k, err)
		}
	}

	// List all.
	all, err := List(dir, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List all: got %d, want 3", len(all))
	}

	// List with prefix.
	alpha, err := List(dir, "alpha/")
	if err != nil {
		t.Fatalf("List alpha/: %v", err)
	}
	if len(alpha) != 2 {
		t.Errorf("List alpha/: got %d, want 2", len(alpha))
	}
	for _, e := range alpha {
		if !strings.HasPrefix(e.Key, "alpha/") {
			t.Errorf("unexpected key %q in alpha/ results", e.Key)
		}
		if e.UpdatedAt.IsZero() {
			t.Errorf("UpdatedAt is zero for %q", e.Key)
		}
	}

	// List with nonexistent prefix returns empty.
	empty, err := List(dir, "nonexistent/")
	if err != nil {
		t.Fatalf("List nonexistent/: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("List nonexistent/: got %d, want 0", len(empty))
	}
}

func TestListEmptyStore(t *testing.T) {
	dir := newTestStore(t)

	entries, err := List(dir, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List empty store: got %d, want 0", len(entries))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/ -run TestList -v`
Expected: FAIL — `List` and `Entry` not defined

- [ ] **Step 3: Implement `List`**

Add to `store.go`:

```go
import "time"

type Entry struct {
	Key       string    `json:"key"`
	UpdatedAt time.Time `json:"updated_at"`
}

func List(storePath, prefix string) ([]Entry, error) {
	var entries []Entry

	searchDir := storePath
	if prefix != "" {
		searchDir = filepath.Join(storePath, prefix)
	}

	if _, err := os.Stat(searchDir); os.IsNotExist(err) {
		return entries, nil
	}

	err := filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip the lock file.
		if info.Name() == "store.lock" {
			return nil
		}

		rel, err := filepath.Rel(storePath, path)
		if err != nil {
			return err
		}

		entries = append(entries, Entry{
			Key:       rel,
			UpdatedAt: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list store: %w", err)
	}

	return entries, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/ -run TestList -v`
Expected: PASS

- [ ] **Step 5: Write failing tests for `Remove`**

Add to `store_test.go`:

```go
func TestRemove(t *testing.T) {
	dir := newTestStore(t)

	if err := Put(dir, "alpha/one.md", "body"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := Put(dir, "alpha/two.md", "body"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := Remove(dir, "alpha/one.md"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// File should be gone.
	_, err := Get(dir, "alpha/one.md")
	if err == nil {
		t.Error("Get after Remove should fail")
	}

	// Other file still exists.
	body, err := Get(dir, "alpha/two.md")
	if err != nil {
		t.Fatalf("Get sibling: %v", err)
	}
	if body != "body" {
		t.Errorf("sibling body = %q", body)
	}
}

func TestRemoveCleansEmptyParents(t *testing.T) {
	dir := newTestStore(t)

	if err := Put(dir, "a/b/c/deep.md", "body"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := Remove(dir, "a/b/c/deep.md"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Parent dirs a/b/c, a/b, a should be cleaned up.
	if _, err := os.Stat(filepath.Join(dir, "a")); !os.IsNotExist(err) {
		t.Error("empty parent dir 'a' should be removed")
	}
}

func TestRemoveNonexistent(t *testing.T) {
	dir := newTestStore(t)

	err := Remove(dir, "doesnotexist.md")
	if err == nil {
		t.Error("Remove nonexistent should return error")
	}
}
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/store/ -run TestRemove -v`
Expected: FAIL — `Remove` not defined

- [ ] **Step 7: Implement `Remove`**

Add to `store.go`:

```go
func Remove(storePath, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}

	filePath := filepath.Join(storePath, key)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("document %q not found", key)
	}

	return withLock(storePath, func() error {
		msg := CommitMessage("remove", key)
		if err := git(storePath, "rm", "--", key); err != nil {
			return fmt.Errorf("git rm: %w", err)
		}
		if err := git(storePath, "commit", "-m", msg); err != nil {
			return fmt.Errorf("git commit: %w", err)
		}

		// Clean up empty parent directories up to the store root.
		dir := filepath.Dir(filePath)
		for dir != storePath {
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) > 0 {
				break
			}
			os.Remove(dir)
			dir = filepath.Dir(dir)
		}

		return nil
	})
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/store/ -run TestRemove -v`
Expected: PASS

- [ ] **Step 9: Run all store tests**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat: add store List and Remove with empty parent cleanup"
```

---

### Task 5: Rewrite `cli/store.go` — remove daemon dependency

**Files:**
- Modify: `internal/cli/store.go`

This task rewrites the entire file. The new version calls into `internal/store` directly instead of connecting to the daemon. The `resolveRepoPath` function is rewritten to use `GRAITH_WORKTREE_PATH` or CWD git root (no daemon query). The `expandContentType` function and `--type` flag are removed.

- [ ] **Step 1: Rewrite `cli/store.go`**

Replace the entire file with:

```go
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/store"
	"github.com/spf13/cobra"
)

var storeRepoFlag string

var storeCmd = &cobra.Command{
	Use:     "store",
	Aliases: []string{"s"},
	Short:   "Shared document store",
}

func resolveStoreRepoPath() (string, error) {
	if storeRepoFlag != "" {
		return config.ResolvePath(storeRepoFlag), nil
	}

	if wp := os.Getenv("GRAITH_WORKTREE_PATH"); wp != "" {
		cmd := exec.Command("git", "rev-parse", "--show-toplevel")
		cmd.Dir = wp
		out, err := cmd.Output()
		if err == nil {
			return config.ResolvePath(strings.TrimSpace(string(out))), nil
		}
	}

	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("could not detect repo path: use --repo or run from inside a git repository")
	}
	return config.ResolvePath(strings.TrimSpace(string(out))), nil
}

func resolveCurrentRepo() string {
	if wp := os.Getenv("GRAITH_WORKTREE_PATH"); wp != "" {
		cmd := exec.Command("git", "rev-parse", "--show-toplevel")
		cmd.Dir = wp
		out, err := cmd.Output()
		if err == nil {
			return config.ResolvePath(strings.TrimSpace(string(out)))
		}
	}

	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return config.ResolvePath(strings.TrimSpace(string(out)))
}

func checkWritePermission(repo string) error {
	current := resolveCurrentRepo()
	if current == "" {
		return nil // no current repo detected; --repo is explicit choice
	}
	if storeRepoFlag != "" && repo != current {
		return fmt.Errorf("write operations are restricted to the current repo's store (current: %s, requested: %s)", current, repo)
	}
	return nil
}

// --- gr store put ---

var storePutFile string

var storePutCmd = &cobra.Command{
	Use:   "put <key> [body]",
	Short: "Put a document into the store",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		bodyArgs := args[1:]

		body, err := resolveBody(bodyArgs, storePutFile)
		if err != nil {
			return err
		}

		repo, err := resolveStoreRepoPath()
		if err != nil {
			return err
		}
		if err := checkWritePermission(repo); err != nil {
			return err
		}

		storePath := store.StorePath(paths.DataDir, repo)
		if err := store.Init(storePath); err != nil {
			return err
		}
		if err := store.Put(storePath, key, body); err != nil {
			return err
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

// --- gr store get ---

var storeGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Get a document from the store",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]

		repo, err := resolveStoreRepoPath()
		if err != nil {
			return err
		}

		storePath := store.StorePath(paths.DataDir, repo)
		body, err := store.Get(storePath, key)
		if err != nil {
			return err
		}

		fmt.Print(body)
		return nil
	},
}

// --- gr store list ---

var storeListCmd = &cobra.Command{
	Use:     "list [prefix]",
	Aliases: []string{"ls"},
	Short:   "List documents in the store",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var prefix string
		if len(args) > 0 {
			prefix = args[0]
		}

		repo, err := resolveStoreRepoPath()
		if err != nil {
			return err
		}

		storePath := store.StorePath(paths.DataDir, repo)
		entries, err := store.List(storePath, prefix)
		if err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(entries)
		}

		if len(entries) == 0 {
			out.Print("No documents found\n")
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "KEY\tUPDATED")
		for _, e := range entries {
			fmt.Fprintf(tw, "%s\t%s\n", e.Key, e.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
		tw.Flush()
		return nil
	},
}

// --- gr store rm ---

var storeRmCmd = &cobra.Command{
	Use:   "rm <key>",
	Short: "Remove a document from the store",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]

		repo, err := resolveStoreRepoPath()
		if err != nil {
			return err
		}
		if err := checkWritePermission(repo); err != nil {
			return err
		}

		storePath := store.StorePath(paths.DataDir, repo)
		if err := store.Remove(storePath, key); err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(struct {
				Key     string `json:"key"`
				Deleted bool   `json:"deleted"`
			}{key, true})
		}
		out.Print("Removed %s\n", key)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(storeCmd)
	storeCmd.PersistentFlags().StringVar(&storeRepoFlag, "repo", "", "repo path (default: auto-detect)")

	storeCmd.AddCommand(storePutCmd)
	storePutCmd.Flags().StringVarP(&storePutFile, "file", "f", "", "read body from file")

	storeCmd.AddCommand(storeGetCmd)
	storeCmd.AddCommand(storeListCmd)
	storeCmd.AddCommand(storeRmCmd)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./cmd/graith`
Expected: builds successfully

- [ ] **Step 3: Commit**

```bash
git add internal/cli/store.go
git commit -m "feat: rewrite store CLI to use flat files instead of daemon"
```

---

### Task 6: Remove SQLite store from daemon

**Files:**
- Delete: `internal/daemon/docstore.go`
- Delete: `internal/daemon/docstore_test.go`
- Modify: `internal/daemon/daemon.go` (remove `docStore` field, `SetDocStore`, and init in `Run`)
- Modify: `internal/daemon/handler.go` (remove `store_put/get/list/delete` cases)
- Modify: `internal/protocol/messages.go` (remove store message types)
- Modify: `internal/config/paths.go` (remove `DocStoreDB`)

- [ ] **Step 1: Delete `docstore.go` and `docstore_test.go`**

```bash
rm internal/daemon/docstore.go internal/daemon/docstore_test.go
```

- [ ] **Step 2: Remove `docStore` field and `SetDocStore` from `daemon.go`**

In `internal/daemon/daemon.go`:
- Remove line `docStore         *DocStore` from the `SessionManager` struct (around line 65)
- Remove the `SetDocStore` method (around lines 89-91)
- Remove the docstore initialization block in `Run` (around lines 2741-2746):
  ```go
  docStore, err := NewDocStore(paths.DocStoreDB)
  // ... through ...
  sm.docStore = docStore
  ```

- [ ] **Step 3: Remove store handler cases from `handler.go`**

In `internal/daemon/handler.go`, remove the four cases (around lines 517-629):
- `case "store_put":`
- `case "store_get":`
- `case "store_list":`
- `case "store_delete":`

Remove each entire case block including all its code up to the next `case`.

- [ ] **Step 4: Remove store message types from `protocol/messages.go`**

In `internal/protocol/messages.go`, remove (around lines 382-428):
- The comment `// Document store messages (client -> daemon)`
- `StorePutMsg` struct
- `StoreGetMsg` struct
- `StoreListMsg` struct
- `StoreDeleteMsg` struct
- The comment `// Document store responses (daemon -> client)`
- `StoreDocument` struct
- `StoreGetResponseMsg` struct
- `StoreListResponseMsg` struct

- [ ] **Step 5: Remove `DocStoreDB` from `config/paths.go`**

In `internal/config/paths.go`:
- Remove `DocStoreDB string` from the `Paths` struct (line 29)
- Remove `DocStoreDB: filepath.Join(dataDir, "docstore.sqlite"),` from `ResolvePaths` (line 82)
- Remove `p.DocStoreDB = filepath.Join(dataDir, "docstore.sqlite")` from `WithDataDir` (line 102)

- [ ] **Step 6: Verify it compiles**

Run: `go build ./cmd/graith`
Expected: builds successfully. If there are compilation errors from references to deleted types, fix them.

- [ ] **Step 7: Run all tests**

Run: `go test ./... 2>&1 | tail -20`
Expected: all tests pass (the deleted docstore tests are gone; new store tests pass)

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor: remove SQLite document store from daemon

Remove docstore.go, handler cases, protocol message types, and
config path. Store operations now go through internal/store directly."
```

---

### Task 7: Update CLAUDE.md documentation

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update the store section in CLAUDE.md**

The `### Shared document store` section needs to be updated to reflect that:
- `--type` flag is removed
- Store operations no longer go through the daemon
- Files are plain files on disk, browsable in IDE
- Git history is available

Update the examples to remove `--type` flags:

```bash
# Before:
gr store put design/api.md --type md --file ./api-design.md
# After:
gr store put design/api.md --file ./api-design.md
gr store put design/api.md "# API Design\n\nEndpoints: ..."
```

Also update the `### Project layout` table — add the `store/` package entry and remove any `docstore` references.

Update the key files table: replace the `daemon/docstore.go` row with `store/store.go | Flat-file git-backed document store`.

- [ ] **Step 2: Verify no stale references**

Run: `grep -n "content_type\|--type\|DocStore\|docstore\|docStore" CLAUDE.md`
Expected: no matches (all references to the old store should be gone)

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md for flat-file store refactor"
```

---

### Task 8: Final verification

**Files:** None (verification only)

- [ ] **Step 1: Run all tests with race detector**

Run: `go test -race ./...`
Expected: PASS

- [ ] **Step 2: Run lint checks**

Run: `gofmt -l ./internal/store/ ./internal/cli/store.go`
Expected: no output (all files formatted)

Run: `go vet ./...`
Expected: no issues

- [ ] **Step 3: Build the binary**

Run: `go build -o ./gr ./cmd/graith`
Expected: builds successfully

- [ ] **Step 4: Smoke test the CLI**

```bash
# Create a temp repo to test with
cd $(mktemp -d) && git init && touch README && git add . && git commit -m "init"
export TEST_REPO=$(pwd)

# Put a document
./gr store put --repo "$TEST_REPO" design/api.md "# API Design"

# Get it back
./gr store get --repo "$TEST_REPO" design/api.md

# List
./gr store list --repo "$TEST_REPO"

# Remove
./gr store rm --repo "$TEST_REPO" design/api.md

# Verify it's gone
./gr store list --repo "$TEST_REPO"
```

Expected: put succeeds, get returns "# API Design", list shows the file, rm succeeds, final list shows empty.

- [ ] **Step 5: Verify git history in store**

```bash
STORE_DIR=$(ls -d ~/.local/share/graith/store/*)
cd "$STORE_DIR" && git log --oneline
```

Expected: commits with messages like `store: update design/api.md` and `store: remove design/api.md`.
