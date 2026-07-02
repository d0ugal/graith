//go:build nono_enforce && linux

// Package sandbox enforcement test. This proves the nono sandbox actually
// HOLDS — argv-shape tests only prove we build the right command line. It is
// build-tagged (`-tags nono_enforce`) and Linux-only, and skips cleanly unless
// a real `nono` binary is installed on a kernel that can enforce Landlock.
//
// Run with:  go test -tags nono_enforce ./internal/sandbox/ -run TestNonoEnforces
//
// It uses nono's own oracles where possible:
//   - `nono profile validate` to prove the generated profile is well-formed
//   - `nono why --path P --op read|write` to prove the policy decision
//   - a real sandboxed exec to prove end-to-end enforcement (errno EACCES,
//     never by parsing nono's advisory stdout, which is misleading on denials)
package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mustEnforce(t *testing.T) {
	t.Helper()

	av := nonoBackend{}.Availability("")
	if !av.CanEnforce {
		t.Skipf("nono cannot enforce here (%s); skipping enforcement test", av.Detail)
	}
}

func TestNonoProfileValidates(t *testing.T) {
	mustEnforce(t)

	root := t.TempDir()
	worktree := filepath.Join(root, "bothy")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}

	profilePath := filepath.Join(root, "profile.json")
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: worktree,
		ReadDirs:    []string{filepath.Join(root, "glen")},
		WriteDirs:   []string{filepath.Join(root, "croft")},
		EnvKeys:     []string{"PATH", "HOME"},
		ProfilePath: profilePath,
	}

	if _, _, err := Wrap("cat", nil, opts); err != nil {
		t.Fatalf("wrap: %v", err)
	}

	out, err := exec.Command("nono", "profile", "validate", profilePath).CombinedOutput()
	if err != nil {
		t.Fatalf("nono profile validate failed: %v\n%s", err, out)
	}
}

func TestNonoWhyPolicyDecisions(t *testing.T) {
	mustEnforce(t)

	root := t.TempDir()
	worktree := filepath.Join(root, "bothy")
	readOnly := filepath.Join(root, "glen")
	secret := filepath.Join(root, "hame", ".ssh")

	for _, d := range []string{worktree, readOnly, secret} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	profilePath := filepath.Join(root, "profile.json")
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: worktree,
		ReadDirs:    []string{readOnly},
		EnvKeys:     []string{"PATH", "HOME"},
		ProfilePath: profilePath,
	}

	if _, _, err := Wrap("cat", nil, opts); err != nil {
		t.Fatalf("wrap: %v", err)
	}

	why := func(path, op string) string {
		out, _ := exec.Command("nono", "why", "--profile", profilePath, "--path", path, "--op", op).CombinedOutput()

		return strings.ToUpper(string(out))
	}

	if got := why(filepath.Join(worktree, "f"), "write"); !strings.Contains(got, "ALLOW") {
		t.Errorf("worktree write should be ALLOWED, got: %s", got)
	}

	if got := why(filepath.Join(readOnly, "f"), "read"); !strings.Contains(got, "ALLOW") {
		t.Errorf("read_dir read should be ALLOWED, got: %s", got)
	}

	if got := why(filepath.Join(readOnly, "f"), "write"); !strings.Contains(got, "DENY") {
		t.Errorf("read_dir write should be DENIED, got: %s", got)
	}

	if got := why(filepath.Join(secret, "id_rsa"), "read"); !strings.Contains(got, "DENY") {
		t.Errorf("ungranted secret read should be DENIED, got: %s", got)
	}
}

func TestNonoEnforcesFilesystemBoundary(t *testing.T) {
	mustEnforce(t)

	root := t.TempDir()
	worktree := filepath.Join(root, "bothy")
	secretDir := filepath.Join(root, "hame", ".ssh")

	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}

	secret := filepath.Join(secretDir, "id_rsa")
	if err := os.WriteFile(secret, []byte("PRIVATE-KEY-braw"), 0o600); err != nil {
		t.Fatal(err)
	}

	profilePath := filepath.Join(root, "profile.json")
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: worktree,
		EnvKeys:     []string{"PATH", "HOME"},
		ProfilePath: profilePath,
	}

	// Denied path: reading the ungranted secret must fail (errno EACCES). We
	// judge by exit status, NOT by parsing nono's stdout advisory.
	cmd, args, err := Wrap("cat", []string{secret}, opts)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	if out, err := exec.Command(cmd, args...).CombinedOutput(); err == nil { //nolint:gosec // test-controlled command
		t.Errorf("reading ungranted secret %s succeeded under sandbox; output=%q", secret, out)
	}

	// Allowed path: writing inside the worktree must succeed.
	target := filepath.Join(worktree, "canny.txt")
	cmd, args, err = Wrap("sh", []string{"-c", "echo bonnie > " + target}, opts)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	if out, err := exec.Command(cmd, args...).CombinedOutput(); err != nil { //nolint:gosec // test-controlled command
		t.Errorf("writing inside granted worktree failed: %v; output=%q", err, out)
	}

	if _, err := os.Stat(target); err != nil {
		t.Errorf("expected %s to be written inside the worktree: %v", target, err)
	}
}
