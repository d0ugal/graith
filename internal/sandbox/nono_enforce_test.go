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
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// nonWritableTempRoot returns a fresh temp dir that is NOT under a nono
// default-writable prefix (/tmp or $TMPDIR). t.TempDir() lives under $TMPDIR on
// most hosts, which would make buildNonoProfile's re-deny guard fire for a
// read-only source placed there — masking whether --workdir alone establishes
// the read-only guarantee (issue #786). Real shared worktrees live under the
// repo dir / ~/.local/share/graith, never /tmp, so we build the fixture under
// $HOME to reproduce that faithfully.
func nonWritableTempRoot(t *testing.T) string {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil || home == "" || underDefaultWritable(home) {
		t.Skipf("no home dir outside default-writable prefixes to isolate --workdir (home=%q, err=%v)", home, err)
	}

	dir, err := os.MkdirTemp(home, "graith-nono-786-")
	if err != nil {
		t.Fatalf("mkdir temp under home: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}

func mustEnforce(t *testing.T) {
	t.Helper()

	av := nonoBackend{}.Availability("", Requirements{})
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

// TestNonoEnforcesSharedWorktreeReadOnly reproduces the --share-worktree layout
// (issue #786): the source worktree is added read-only, the scratch dir is the
// read-write workdir, and the process is launched with its cwd set to the source
// worktree (as the daemon does via the PTY). It proves nono pins the workdir to
// the scratch dir — writes to the source worktree fail while writes to scratch
// succeed — even though the cwd is the source. Without the explicit --workdir in
// nono.Wrap, nono would resolve the workdir from the cwd and make the source
// writable.
func TestNonoEnforcesSharedWorktreeReadOnly(t *testing.T) {
	mustEnforce(t)

	// Build the fixture OUTSIDE /tmp/$TMPDIR so buildNonoProfile's re-deny guard
	// does NOT fire for the source (see nonWritableTempRoot). This is essential:
	// if the source were under a default-writable prefix it would land in
	// filesystem.deny as well as filesystem.read, giving a SECOND mechanism that
	// keeps it read-only — the test would then pass even on the pre-fix build and
	// prove nothing about --workdir. Keeping source out of the deny list means the
	// only thing steering writes away from it is the pinned --workdir (issue #786).
	root := nonWritableTempRoot(t)
	source := filepath.Join(root, "bothy")    // read-only source worktree
	scratch := filepath.Join(root, "scratch") // read-write workdir

	for _, d := range []string{source, scratch} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// A pre-existing file in the source so the agent has code to read.
	srcFile := filepath.Join(source, "code.go")
	if err := os.WriteFile(srcFile, []byte("package bonnie\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	profilePath := filepath.Join(root, "profile.json")
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: scratch,          // scratch is the read-write workdir
		ReadDirs:    []string{source}, // source is read-only
		EnvKeys:     []string{"PATH", "HOME"},
		ProfilePath: profilePath,
	}

	// Reading the source worktree must succeed (it is granted read-only).
	cmd, args, err := Wrap("cat", []string{srcFile}, opts)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	// Isolation guard: prove the source is NOT re-denied, so this test exercises
	// the --workdir pin rather than the /tmp re-deny path. If this fails, the
	// fixture leaked under a default-writable prefix and the enforcement result
	// below would be untrustworthy.
	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}

	var prof nonoProfile
	if err := json.Unmarshal(data, &prof); err != nil {
		t.Fatalf("profile is not valid JSON: %v", err)
	}

	if slices.Contains(prof.Filesystem.Deny, source) {
		t.Fatalf("source %q is in filesystem.deny; test would pass via re-deny, not --workdir — fixture must live outside /tmp/$TMPDIR", source)
	}

	rc := exec.Command(cmd, args...) //nolint:gosec // test-controlled command
	rc.Dir = source
	if out, err := rc.CombinedOutput(); err != nil {
		t.Errorf("reading granted source file failed: %v; output=%q", err, out)
	}

	// Writing into the source worktree must FAIL even though it is the cwd —
	// this is the read-only guarantee the shared-worktree model depends on.
	blocked := filepath.Join(source, "tamper.txt")
	cmd, args, err = Wrap("sh", []string{"-c", "echo thrawn > " + blocked}, opts)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	wc := exec.Command(cmd, args...) //nolint:gosec // test-controlled command
	wc.Dir = source
	if out, err := wc.CombinedOutput(); err == nil {
		t.Errorf("writing into the read-only source worktree succeeded under sandbox; output=%q", out)
	}

	if _, err := os.Stat(blocked); err == nil {
		t.Errorf("file %s was written into the read-only source worktree", blocked)
	}

	// Writing into the scratch workdir must succeed.
	allowed := filepath.Join(scratch, "canny.txt")
	cmd, args, err = Wrap("sh", []string{"-c", "echo bonnie > " + allowed}, opts)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	sc := exec.Command(cmd, args...) //nolint:gosec // test-controlled command
	sc.Dir = source
	if out, err := sc.CombinedOutput(); err != nil {
		t.Errorf("writing into the scratch workdir failed: %v; output=%q", err, out)
	}

	if _, err := os.Stat(allowed); err != nil {
		t.Errorf("expected %s to be written inside the scratch workdir: %v", allowed, err)
	}
}
