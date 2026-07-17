// Package atomicfile provides a crash-safe file write primitive: write to a
// temp file in the target directory, fsync it, atomically rename it into place,
// then fsync the parent directory. A crash at any point leaves either the old
// file fully intact or the new file fully written — never a truncated or
// partially-written file.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// The temp-write / fsync / rename steps are indirected through package vars so
// tests can inject a failure at each stage and assert the crash-safety
// guarantee (the prior file survives, no temp file is stranded) holds for every
// failure point — not just the read-only-directory case a real filesystem can
// reproduce. Production always uses the real syscalls below.
var (
	writeTemp  = func(f *os.File, data []byte) (int, error) { return f.Write(data) }
	syncTemp   = func(f *os.File) error { return f.Sync() }
	renameTemp = os.Rename
)

// Write atomically writes data to path with the given permissions. The parent
// directory is created (mode 0o700) if it does not exist. The temp file is
// created in the same directory as path so the final rename stays on one
// filesystem (a cross-device rename would fail and is not atomic).
//
// When Write has to create parent directories, it fsyncs each one's parent
// after the write so the whole new path is durable — not just the leaf. Without
// this, a crash could lose a freshly-created directory even though the file's
// own data was synced.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	// Record the deepest already-existing ancestor before creating the tree, so
	// only the directories this call actually creates get their parents fsynced.
	base := deepestExisting(dir)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}

	tmpPath := tmp.Name()

	// From here on, remove the temp file on any error so a failed write never
	// leaves a stray .tmp-* file behind.
	cleanup := func(cause error, verb string) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)

		return fmt.Errorf("%s: %w", verb, cause)
	}

	if _, err := writeTemp(tmp, data); err != nil {
		return cleanup(err, "write temp")
	}

	if err := tmp.Chmod(perm); err != nil {
		return cleanup(err, "chmod temp")
	}

	if err := syncTemp(tmp); err != nil {
		return cleanup(err, "sync temp")
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}

	if err := renameTemp(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync dir: %w", err)
	}

	// Make the entries for any directories we created durable. Each created
	// directory's entry lives in its parent, so fsync parent(dir) up to and
	// including base (base pre-existed, so its own entry was already durable).
	for p := dir; p != base; {
		p = filepath.Dir(p)
		if err := syncDir(p); err != nil {
			return fmt.Errorf("sync parent dir: %w", err)
		}
	}

	return nil
}

// deepestExisting returns the closest ancestor of dir (or dir itself) that
// already exists on disk. It is used to bound the parent-directory fsync loop
// to only the directories Write creates.
func deepestExisting(dir string) string {
	for {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root without finding an existing dir; it
			// exists in practice, so stop here.
			return dir
		}

		dir = parent
	}
}

// syncDir fsyncs a directory so that a rename into it is durable. Without this,
// a crash after rename but before the directory entry is flushed could lose the
// new file even though its data was synced.
func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}

	err = d.Sync()
	_ = d.Close()

	return err
}
