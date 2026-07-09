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
// filesystem boundary purely by real sandboxed exec, judged by exit status /
// errno — never by string-matching advisory output:
//   - a NO-ACCESS read is denied
//   - a READ-ONLY read is allowed
//   - a WRITE-DIR write is allowed
//
// IMPORTANT: safehouse's policy is PATH-scoped, not content-based — unlike
// nono's default profile it has no `deny_credentials`-style rule, so a file is
// denied only because its path is outside every grant. It also (like nono with
// /tmp) implicitly allows the system temp dir so the agent can function, which
// means a "secret" placed under t.TempDir() (macOS $TMPDIR, /var/folders/...)
// is readable. The no-access fixture therefore lives OUTSIDE any temp dir, in a
// freshly created dir under $HOME, which safehouse does not grant.
package sandbox

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func mustEnforceSafehouse(t *testing.T) {
	t.Helper()

	av := safehouseBackend{}.Availability("", Requirements{})
	if !av.CanEnforce {
		t.Skipf("safehouse cannot enforce here (%s); skipping enforcement test", av.Detail)
	}
}

func TestSafehouseEnforcesFilesystemBoundary(t *testing.T) {
	mustEnforceSafehouse(t)

	// Granted fixtures live under t.TempDir(): a read-write worktree, a granted
	// read-only dir, and a granted write dir.
	root := t.TempDir()
	worktree := filepath.Join(root, "bothy")
	readOnly := filepath.Join(root, "glen")
	writeDir := filepath.Join(root, "croft")

	for _, d := range []string{worktree, readOnly, writeDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// The NO-ACCESS secret must live OUTSIDE the temp tree — safehouse
	// implicitly allows the system temp dir, so a secret under t.TempDir()
	// would be readable. Put it in a fresh dir under $HOME, which is not
	// granted, and clean it up.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}

	secretDir, err := os.MkdirTemp(home, "graith-noaccess-*")
	if err != nil {
		t.Fatalf("create no-access dir under home: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(secretDir) })

	secret := filepath.Join(secretDir, "id_rsa")
	if err := os.WriteFile(secret, []byte("PRIVATE-KEY-thrawn"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A file the agent IS allowed to read (inside the read-only grant).
	readable := filepath.Join(readOnly, "canny.txt")
	if err := os.WriteFile(readable, []byte("bonnie-braw"), 0o600); err != nil {
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

// TestSafehouseEnforcesUnixSocketConnect is the kernel-level regression test for
// the daemon-socket bug: a sandboxed agent could not connect() to the daemon
// socket, so `gr msg`/`gr status` failed. It proves (1) safehouse's default
// profile DENIES connecting to an arbitrary Unix socket even when the socket
// file is readable — i.e. a file grant is not enough — and (2) a UnixSockets
// grant (emitted as an --append-profile network-outbound rule) actually makes
// the connect succeed. Without this test the earlier file-grant "fix" passed
// argv assertions but did nothing at the kernel level.
func TestSafehouseEnforcesUnixSocketConnect(t *testing.T) {
	mustEnforceSafehouse(t)

	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; skipping socket-connect enforcement test")
	}

	// The socket path must stay under the ~104-char AF_UNIX limit and must be a
	// real (symlink-resolved) path so it matches Seatbelt's canonical path rules.
	// A short /tmp base resolves to /private/tmp, which safehouse allows for file
	// access — so path resolution is fine and the ONLY thing under test is the
	// network-outbound connect gate. We still grant the dir read explicitly so
	// the negative case fails on connect, not on file access.
	base, err := os.MkdirTemp("/tmp", "gsk")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(base) })

	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	worktree := filepath.Join(base, "bothy")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}

	sockPath := filepath.Join(base, "d.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen on %s: %v", sockPath, err)
	}

	defer func() { _ = ln.Close() }()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}

			_ = c.Close()
		}
	}()

	// Connects to the socket; exits 0 on success, non-zero on EPERM.
	script := "import socket,sys; s=socket.socket(socket.AF_UNIX); s.settimeout(5); s.connect(sys.argv[1])"

	runConnect := func(sockets []string) error {
		opts := WrapOpts{
			Backend:               BackendSafehouse,
			WorktreeDir:           worktree,
			ReadDirs:              []string{base}, // file access to the socket; isolates the connect gate
			UnixSockets:           sockets,
			SafehouseFragmentPath: filepath.Join(base, "frag.sb"),
			EnvKeys:               []string{"PATH", "HOME"},
		}

		cmd, wargs, err := Wrap(py, []string{"-c", script, sockPath}, opts)
		if err != nil {
			t.Fatalf("wrap: %v", err)
		}

		_, execErr := exec.Command(cmd, wargs...).CombinedOutput() //nolint:gosec // test-controlled command

		return execErr
	}

	// 1. WITHOUT a UnixSockets grant, connect must be DENIED (the whole bug:
	//    file access alone does not permit connect under Seatbelt).
	if err := runConnect(nil); err == nil {
		t.Errorf("connect succeeded WITHOUT a UnixSockets grant; safehouse should deny Unix-socket connect by default")
	}

	// 2. WITH the grant, connect must SUCCEED.
	if err := runConnect([]string{sockPath}); err != nil {
		t.Errorf("connect failed WITH a UnixSockets grant (the fix does not work): %v", err)
	}
}
