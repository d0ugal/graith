package store

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Init initialises the document store at storePath. It creates the directory
// if it does not exist and sets up a bare git repository for versioning.
// Calling Init on an already-initialised store is a no-op.
func Init(storePath string) error {
	if err := os.MkdirAll(storePath, 0o700); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}

	// Idempotent: skip git init if .git already exists.
	if _, err := os.Stat(filepath.Join(storePath, ".git")); err == nil {
		return nil
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

// git runs git with the given args in dir.
func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}

// ValidateKey returns an error if key is not a valid store key.
//
// A valid key must:
//   - be non-empty
//   - not start with '/'
//   - not start with '-'
//   - not contain any ".." path component
//   - not contain control characters or NUL bytes
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
	for _, component := range strings.Split(key, "/") {
		if component == ".." {
			return fmt.Errorf("store key must not contain '..' path components")
		}
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

// withLock acquires an exclusive flock on a lock file in storePath and runs fn.
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

// Get retrieves the body stored under key.
func Get(storePath, key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}

	filePath := filepath.Join(storePath, key)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	return string(data), nil
}

// repoHash is copied from internal/daemon/daemon.go to produce a deterministic
// 12-character hex hash of an absolute repo path.
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
