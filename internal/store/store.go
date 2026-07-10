package store

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/atomicfile"
)

// Entry represents a document in the store.
type Entry struct {
	Key       string    `json:"key"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Init initialises the document store at storePath. It creates the directory
// if it does not exist and sets up a git repository for versioning. Calling
// Init on an already-initialised store refreshes graith's repository-local Git
// settings so stores created by older versions do not inherit host settings.
func Init(storePath string) error {
	if err := os.MkdirAll(storePath, 0o700); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}

	// Serialize repository creation and config migration with document writes.
	// Git's own config.lock fails immediately under contention, whereas store.lock
	// waits and prevents concurrent agents from racing these idempotent updates.
	return withLock(storePath, func() error {
		// Only initialise a new repository, but always refresh the local settings.
		// In particular, internal store commits must never inherit a developer's
		// global commit.gpgsign=true and contact their SSH/GPG agent.
		if _, err := os.Stat(filepath.Join(storePath, ".git")); err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("inspect git repository: %w", err)
			}

			if err := git(storePath, "init", "--quiet"); err != nil {
				return fmt.Errorf("git init: %w", err)
			}
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

		if err := git(storePath, "config", "commit.gpgsign", "false"); err != nil {
			return fmt.Errorf("git config commit.gpgsign: %w", err)
		}

		return nil
	})
}

// git runs git with the given args in dir.
func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

// ValidateKey returns an error if key is not a valid store key.
//
// A valid key must:
//   - be non-empty
//   - not start with '/' or '-'
//   - not contain "..", ".git", or "." path components
//   - not contain control characters, NUL bytes, or backslashes
//   - not contain git pathspec characters (*, ?, [, :)
//   - not be "store.lock"
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("store key must not be empty")
	}

	if key[0] == '/' {
		return fmt.Errorf("store key must not start with '/'")
	}

	if key[0] == '-' {
		return fmt.Errorf("store key must not start with '-'")
	}

	for _, c := range key {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("store key must not contain control characters")
		}
	}

	if strings.ContainsAny(key, "*?[:") {
		return fmt.Errorf("store key must not contain glob/pathspec characters")
	}

	if strings.Contains(key, "\\") {
		return fmt.Errorf("store key must not contain backslashes")
	}

	for _, component := range strings.Split(key, "/") {
		if component == ".." {
			return fmt.Errorf("store key must not contain '..' path components")
		}

		if strings.EqualFold(component, ".git") {
			return fmt.Errorf("store key must not contain '.git' path components")
		}

		if component == "." {
			return fmt.Errorf("store key must not contain '.' path components")
		}
	}

	if strings.EqualFold(key, "store.lock") {
		return fmt.Errorf("store key must not be 'store.lock'")
	}

	return nil
}

// StorePath returns the on-disk directory for the document store for a given
// repo. The path is <dataDir>/store/<reponame>-<hash> where reponame is the
// base name of repoRoot and hash is a 12-character deterministic hex string
// derived from repoRoot.
func StorePath(dataDir, repoRoot string) string {
	repoName := filepath.Base(repoRoot)
	return filepath.Join(dataDir, "store", repoName+"-"+repoHash(repoRoot))
}

// SharedStorePath returns the on-disk directory for the shared document store.
// The shared store is not scoped to any repo.
func SharedStorePath(dataDir string) string {
	return filepath.Join(dataDir, "store", "shared")
}

// withLock acquires an exclusive flock on a lock file in storePath and runs fn.
func withLock(storePath string, fn func() error) error {
	lockPath := filepath.Join(storePath, "store.lock")

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire store lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

	return fn()
}

// CommitMessage builds a commit message for a store operation.
// The first line is "store: <action> <key>". If GRAITH_SESSION_ID is set,
// trailers are appended after a blank line.
func CommitMessage(action, key string) string {
	first := "store: " + action + " " + key

	sessionID := os.Getenv("GRAITH_SESSION_ID")
	if sessionID == "" {
		return first
	}

	var sb strings.Builder
	sb.WriteString(first)
	sb.WriteString("\n\n")

	sessionName := os.Getenv("GRAITH_SESSION_NAME")
	if sessionName != "" {
		sb.WriteString("session: " + sessionName + " (" + sessionID + ")\n")
	} else {
		sb.WriteString("session: " + sessionID + "\n")
	}

	if agentType := os.Getenv("GRAITH_AGENT_TYPE"); agentType != "" {
		sb.WriteString("agent: " + agentType + "\n")
	}

	// Trim trailing newline for a clean message.
	return strings.TrimRight(sb.String(), "\n")
}

// Put writes body to the store under key and commits it to the git history.
func Put(storePath, key, body string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}

	return withLock(storePath, func() error {
		filePath := filepath.Join(storePath, key)
		if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
			return fmt.Errorf("create parent directories: %w", err)
		}

		if err := atomicfile.Write(filePath, []byte(body), 0o600); err != nil {
			return fmt.Errorf("write file: %w", err)
		}

		if err := git(storePath, "add", "--", key); err != nil {
			return fmt.Errorf("git add: %w", err)
		}

		if git(storePath, "diff", "--quiet", "--cached", "--", key) == nil {
			return nil
		}

		msg := CommitMessage("update", key)
		if err := git(storePath, "commit", "--no-gpg-sign", "-m", msg, "--", key); err != nil {
			return fmt.Errorf("git commit: %w", err)
		}

		return nil
	})
}

// Append appends a line to the file at key and commits it to the git history.
// A trailing newline is added if line does not already end with one.
// The file is created if it does not exist.
func Append(storePath, key, line string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}

	return withLock(storePath, func() error {
		filePath := filepath.Join(storePath, key)
		if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
			return fmt.Errorf("create parent directories: %w", err)
		}

		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}

		// Read-modify-write under the store lock so the append is atomic: a
		// crash mid-write leaves the old file intact rather than a torn line.
		// The store lock (held by withLock) serializes concurrent appends, so
		// no reader/writer sees the intermediate temp file.
		existing, err := os.ReadFile(filePath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read file for append: %w", err)
		}

		if err := atomicfile.Write(filePath, append(existing, line...), 0o600); err != nil {
			return fmt.Errorf("append to file: %w", err)
		}

		if err := git(storePath, "add", "--", key); err != nil {
			return fmt.Errorf("git add: %w", err)
		}

		if git(storePath, "diff", "--quiet", "--cached", "--", key) == nil {
			return nil
		}

		msg := CommitMessage("append", key)
		if err := git(storePath, "commit", "--no-gpg-sign", "-m", msg, "--", key); err != nil {
			return fmt.Errorf("git commit: %w", err)
		}

		return nil
	})
}

// Get retrieves the body stored under key.
func Get(storePath, key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}

	filePath := filepath.Join(storePath, key)

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// List returns all entries in the store, optionally filtered by prefix.
// If the prefix directory does not exist, an empty slice is returned (not an error).
// No locking is required for listing.
func List(storePath, prefix string) ([]Entry, error) {
	searchDir := storePath

	if prefix != "" {
		if err := ValidateKey(strings.TrimSuffix(prefix, "/")); err != nil {
			return nil, err
		}

		searchDir = filepath.Join(storePath, prefix)
	}

	if _, err := os.Stat(searchDir); os.IsNotExist(err) {
		return []Entry{}, nil
	}

	var entries []Entry

	err := filepath.WalkDir(searchDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}

		if d.IsDir() {
			return nil
		}

		if path == filepath.Join(storePath, "store.lock") {
			return nil
		}

		key, err := filepath.Rel(storePath, path)
		if err != nil {
			return fmt.Errorf("compute relative path: %w", err)
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", key, err)
		}

		entries = append(entries, Entry{
			Key:       key,
			UpdatedAt: info.ModTime(),
		})

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk store: %w", err)
	}

	return entries, nil
}

// Remove deletes the document at key from the store and commits the deletion.
// Empty parent directories up to the store root are cleaned up after removal.
func Remove(storePath, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}

	return withLock(storePath, func() error {
		filePath := filepath.Join(storePath, key)
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			return fmt.Errorf("document %q not found", key)
		}

		if err := git(storePath, "rm", "--", key); err != nil {
			return fmt.Errorf("git rm: %w", err)
		}

		msg := CommitMessage("remove", key)
		if err := git(storePath, "commit", "--no-gpg-sign", "-m", msg, "--", key); err != nil {
			return fmt.Errorf("git commit: %w", err)
		}

		dir := filepath.Dir(filePath)
		for dir != storePath {
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) > 0 {
				break
			}

			_ = os.Remove(dir)
			dir = filepath.Dir(dir)
		}

		return nil
	})
}

// StoreInfo describes a discovered store directory.
type StoreInfo struct {
	Name    string  `json:"name"`
	Path    string  `json:"path"`
	Entries []Entry `json:"entries,omitempty"`
}

// ListStores enumerates all store directories under dataDir/store/.
// Each directory is named <reponame>-<hash>.
func ListStores(dataDir string) ([]StoreInfo, error) {
	storeRoot := filepath.Join(dataDir, "store")

	dirs, err := os.ReadDir(storeRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("read store root: %w", err)
	}

	var stores []StoreInfo

	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}

		storePath := filepath.Join(storeRoot, d.Name())
		if _, err := os.Stat(filepath.Join(storePath, ".git")); err != nil {
			continue
		}

		stores = append(stores, StoreInfo{
			Name: d.Name(),
			Path: storePath,
		})
	}

	return stores, nil
}

// repoHash is copied from internal/daemon/daemon.go to produce a deterministic
// 12-character hex hash of an absolute repo path.
func repoHash(repoPath string) string {
	h := uint64(0)
	for _, c := range repoPath {
		h = h*31 + uint64(c) //nolint:gosec // G115: c is a rune from range-over-string, always a non-negative code point
	}

	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(h >> (i * 8)) //nolint:gosec // G115: intentional low-byte truncation for a hash digest
	}

	return hex.EncodeToString(b)[:12]
}
