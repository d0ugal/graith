//go:build safehouse_enforce

// Package sandbox enforcement test for the SAFEHOUSE backend — the macOS
// mirror of nono_enforce_test.go. It proves graith's safehouse/Seatbelt
// wrapping actually HOLDS at the kernel level, not just that we build the right
// argv. It is build-tagged (`-tags safehouse_enforce`) and skips cleanly unless
// a real `safehouse` binary is on PATH and Seatbelt can enforce (i.e. macOS),
// so it never hard-fails on a Linux/dev box — it only runs for real on
// macos-latest CI.
//
// Run with:  go test -tags safehouse_enforce ./internal/sandbox/... -v
//
// safehouse has no `why`/`validate` oracle (unlike nono), so this asserts the
// same filesystem boundary the nono test does purely by real sandboxed exec,
// judged by exit status / errno — never by string-matching advisory output:
//   - a NO-ACCESS read is denied
//   - a READ-ONLY read is allowed
//   - a WRITE-DIR write is allowed
package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func mustEnforceSafehouse(t *testing.T) {
	t.Helper()

	av := safehouseBackend{}.Availability("")
	if !av.CanEnforce {
		t.Skipf("safehouse cannot enforce here (%s); skipping enforcement test", av.Detail)
	}
}

func TestSafehouseEnforcesFilesystemBoundary(t *testing.T) {
	mustEnforceSafehouse(t)

	// Fixtures: a read-write worktree, a granted read-only dir, a granted
	// write dir, and a secret dir that is NEVER granted (no-access).
	root := t.TempDir()
	worktree := filepath.Join(root, "bothy")
	readOnly := filepath.Join(root, "glen")
	writeDir := filepath.Join(root, "croft")
	secretDir := filepath.Join(root, "hame", ".ssh")

	for _, d := range []string{worktree, readOnly, writeDir, secretDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// A file the agent is allowed to READ (inside the read-only grant).
	readable := filepath.Join(readOnly, "canny.txt")
	if err := os.WriteFile(readable, []byte("bonnie-braw"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A secret the agent must NOT be able to read (ungranted).
	secret := filepath.Join(secretDir, "id_rsa")
	if err := os.WriteFile(secret, []byte("PRIVATE-KEY-thrawn"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := WrapOpts{
		Backend:     BackendSafehouse,
		WorktreeDir: worktree,
		ReadDirs:    []string{readOnly},
		WriteDirs:   []string{writeDir},
		EnvKeys:     []string{"PATH", "HOME"},
	}

	run := func(command string, args ...string) error {
		cmd, wargs, err := Wrap(command, args, opts)
		if err != nil {
			t.Fatalf("wrap: %v", err)
		}

		_, execErr := exec.Command(cmd, wargs...).CombinedOutput() //nolint:gosec // test-controlled command

		return execErr
	}

	// 1. NO-ACCESS read must be DENIED (non-zero exit / errno).
	if err := run("cat", secret); err == nil {
		t.Errorf("reading ungranted secret %s succeeded under safehouse; want denied", secret)
	}

	// 2. READ-ONLY read must be ALLOWED (zero exit).
	if err := run("cat", readable); err != nil {
		t.Errorf("reading granted read-only file %s failed under safehouse: %v", readable, err)
	}

	// 3. WRITE-DIR write must be ALLOWED, and the file must actually appear.
	target := filepath.Join(writeDir, "wrote.txt")
	if err := run("sh", "-c", "echo dinnae > "+target); err != nil {
		t.Errorf("writing into granted write dir %s failed under safehouse: %v", target, err)
	}

	if _, err := os.Stat(target); err != nil {
		t.Errorf("expected %s to be written into the granted write dir: %v", target, err)
	}
}
