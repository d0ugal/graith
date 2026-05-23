package store

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

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
