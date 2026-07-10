package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "braw.txt")

	if err := Write(path, []byte("bonnie"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(got) != "bonnie" {
		t.Errorf("content = %q, want %q", got, "bonnie")
	}
}

func TestWriteCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	// Nested path whose parent directories do not yet exist.
	path := filepath.Join(dir, "glen", "wynd", "loch.txt")

	if err := Write(path, []byte("haar"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat written file: %v", err)
	}
}

func TestWriteOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auld.txt")

	if err := Write(path, []byte("auld content"), 0o600); err != nil {
		t.Fatalf("first Write: %v", err)
	}

	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// The full old content must be gone — no leftover bytes from the longer
	// previous write (which a truncate-in-place writer could leave).
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
}

func TestWriteHonorsPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kirk.txt")

	if err := Write(path, []byte("x"), 0o640); err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("perm = %o, want %o", got, 0o640)
	}
}

func TestWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skelf.txt")

	if err := Write(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}

	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1", len(entries))
	}
}
