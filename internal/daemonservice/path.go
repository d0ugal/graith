package daemonservice

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalPathsOverlap reports whether either absolute path contains the
// other after resolving every existing symlink component. Missing leaf
// components are retained beneath their nearest verified ancestor; broken
// symlinks and paths that cannot be inspected fail closed.
func CanonicalPathsOverlap(first, second string) (bool, error) {
	canonicalFirst, err := canonicalPathWithMissingLeaf(first)
	if err != nil {
		return false, err
	}

	canonicalSecond, err := canonicalPathWithMissingLeaf(second)
	if err != nil {
		return false, err
	}

	return pathsOverlap(canonicalFirst, canonicalSecond), nil
}

// CanonicalPathContains reports whether parent contains child after applying
// the same fail-closed canonicalization as CanonicalPathsOverlap.
func CanonicalPathContains(parent, child string) (bool, error) {
	canonicalParent, err := canonicalPathWithMissingLeaf(parent)
	if err != nil {
		return false, err
	}

	canonicalChild, err := canonicalPathWithMissingLeaf(child)
	if err != nil {
		return false, err
	}

	return pathContains(canonicalParent, canonicalChild), nil
}

func canonicalPathWithMissingLeaf(path string) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return "", errors.New("security-sensitive path must be an absolute single-line path")
	}

	current := filepath.Clean(path)

	var suffix []string

	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			parts := append([]string{resolved}, suffix...)

			return filepath.Clean(filepath.Join(parts...)), nil
		}

		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("resolve path %q: %w", path, err)
		}

		if info, lstatErr := os.Lstat(current); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("resolve path %q: broken symlink at %q", path, current)
		} else if lstatErr != nil && !errors.Is(lstatErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect path %q: %w", current, lstatErr)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve path %q: no existing ancestor", path)
		}

		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent
	}
}

func pathsOverlap(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)

	return pathContains(first, second) || pathContains(second, first)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}

	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
