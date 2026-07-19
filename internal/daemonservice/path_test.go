package daemonservice

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalPathsOverlapResolvesExistingAndMissingSymlinkChildren(t *testing.T) {
	root := t.TempDir()

	control := filepath.Join(root, "services", "control")
	if err := os.MkdirAll(control, 0o700); err != nil {
		t.Fatal(err)
	}

	alias := filepath.Join(root, "croft")
	if err := os.Symlink(control, alias); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{alias, filepath.Join(alias, "bootstrap", "start-default.json"), filepath.Dir(control), string(filepath.Separator)} {
		overlaps, err := CanonicalPathsOverlap(path, control)
		if err != nil {
			t.Fatal(err)
		}

		if !overlaps {
			t.Errorf("%q did not overlap protected root %q", path, control)
		}
	}

	unrelated, err := CanonicalPathsOverlap(filepath.Join(root, "bothy"), control)
	if err != nil {
		t.Fatal(err)
	}

	if unrelated {
		t.Fatal("unrelated path overlapped the protected root")
	}
}

func TestCanonicalPathsOverlapRejectsBrokenSymlink(t *testing.T) {
	root := t.TempDir()

	broken := filepath.Join(root, "dreich")
	if err := os.Symlink(filepath.Join(root, "missing"), broken); err != nil {
		t.Fatal(err)
	}

	if _, err := CanonicalPathsOverlap(filepath.Join(broken, "bootstrap"), filepath.Join(root, "control")); err == nil {
		t.Fatal("broken symlink was accepted for a security-sensitive path")
	}
}

func TestCanonicalPathContainsIsDirectional(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "services", "control")

	contains, err := CanonicalPathContains(root, child)
	if err != nil || !contains {
		t.Fatalf("root contains child = (%t, %v)", contains, err)
	}

	contains, err = CanonicalPathContains(child, root)
	if err != nil || contains {
		t.Fatalf("child contains root = (%t, %v)", contains, err)
	}
}
