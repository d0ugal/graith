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

// Write atomically writes data to path with the given permissions. The parent
// directory is created (mode 0o700) if it does not exist. The temp file is
// created in the same directory as path so the final rename stays on one
// filesystem (a cross-device rename would fail and is not atomic).
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
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

	if _, err := tmp.Write(data); err != nil {
		return cleanup(err, "write temp")
	}

	if err := tmp.Chmod(perm); err != nil {
		return cleanup(err, "chmod temp")
	}

	if err := tmp.Sync(); err != nil {
		return cleanup(err, "sync temp")
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync dir: %w", err)
	}

	return nil
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
