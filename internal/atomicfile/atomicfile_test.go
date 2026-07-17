package atomicfile

import (
	"errors"
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

// The write/sync/rename fault-injection tests below drive the crash-safety
// guarantee at each publication step: whichever step fails, the prior file must
// survive intact and no temp file may be stranded. They override the package
// seams, so they must not run in parallel with each other.

func withFailingStep(t *testing.T, step string) {
	t.Helper()

	origWrite, origSync, origRename := writeTemp, syncTemp, renameTemp

	t.Cleanup(func() { writeTemp, syncTemp, renameTemp = origWrite, origSync, origRename })

	boom := errors.New("injected failure")

	switch step {
	case "write":
		writeTemp = func(_ *os.File, _ []byte) (int, error) { return 0, boom }
	case "sync":
		syncTemp = func(_ *os.File) error { return boom }
	case "rename":
		renameTemp = func(_, _ string) error { return boom }
	default:
		t.Fatalf("unknown step %q", step)
	}
}

func TestWritePreservesPriorFileOnInjectedFailure(t *testing.T) {
	for _, step := range []string{"write", "sync", "rename"} {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "croft.txt")

			if err := Write(path, []byte("auld and durable"), 0o600); err != nil {
				t.Fatalf("seed Write: %v", err)
			}

			withFailingStep(t, step)

			if err := Write(path, []byte("new but doomed"), 0o600); err == nil {
				t.Fatalf("%s: expected Write to fail", step)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: ReadFile: %v", step, err)
			}

			if string(got) != "auld and durable" {
				t.Errorf("%s: prior file corrupted: got %q", step, got)
			}

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("%s: ReadDir: %v", step, err)
			}

			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".tmp-") {
					t.Errorf("%s: stranded temp file %s", step, e.Name())
				}
			}
		})
	}
}

func TestWriteFailsCleanlyOnInjectedFailureForNewFile(t *testing.T) {
	for _, step := range []string{"write", "sync", "rename"} {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "bothy.txt")

			withFailingStep(t, step)

			if err := Write(path, []byte("first write"), 0o600); err == nil {
				t.Fatalf("%s: expected Write to fail", step)
			}

			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("%s: target should not exist after a failed initial write, stat err = %v", step, err)
			}

			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".tmp-") {
					t.Errorf("%s: stranded temp file %s", step, e.Name())
				}
			}
		})
	}
}
