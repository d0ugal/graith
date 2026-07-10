package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestServerAcceptsConnections(t *testing.T) {
	sockPath := filepath.Join(shortSocketDir(t), "test.sock")

	l, err := Listen(sockPath)
	if err != nil {
		if bindUnavailable(err) {
			t.Skipf("unix socket bind unavailable in this environment: %v", err)
		}

		t.Fatal(err)
	}

	var count atomic.Int32

	handler := func(ctx context.Context, conn net.Conn) {
		count.Add(1)

		buf := make([]byte, 16)
		_, _ = conn.Read(buf)
	}

	srv := NewServer(l, handler, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Serve(ctx) }()

	conn1, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = conn1.Write([]byte("hi"))
	_ = conn1.Close()

	conn2, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = conn2.Write([]byte("hi"))
	_ = conn2.Close()

	time.Sleep(100 * time.Millisecond)
	srv.Shutdown()

	if count.Load() != 2 {
		t.Errorf("handled %d connections, want 2", count.Load())
	}
}

// The unix-socket paths under this environment's t.TempDir() exceed the
// platform's sockaddr length limit, and some sandboxes deny AF_UNIX bind
// outright. These tests cover the Server accept loop, connection tracking, and
// graceful shutdown over a loopback TCP listener (no path-length limit, no bind
// restriction), and cover Listen itself via a short /tmp socket path — skipping
// only when the sandbox denies the bind, while still failing on real errors.

// bindUnavailable reports whether a Listen error is a sandbox/permission denial
// (as opposed to a genuine code regression), so socket tests can skip rather
// than hard-fail where AF_UNIX bind is disallowed.
func bindUnavailable(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
}

// shortSocketDir returns a temp directory under /tmp for unix-socket tests.
// t.TempDir() honors the graith-set TMPDIR (a deeply-nested per-repo path), so
// a socket built under it can exceed the ~104-byte sockaddr limit on macOS and
// fail bind with EINVAL. /tmp keeps the path short. Cleaned up automatically.
func shortSocketDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "gr")
	if err != nil {
		t.Skipf("cannot create short tmp dir: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}

// TestCoverServeAndShutdownTCP drives NewServer/Serve/trackConn/untrackConn and
// a graceful Shutdown over a TCP listener. It asserts that accepted connections
// are actually tracked while live and untracked once their handlers return, so
// it would catch trackConn/untrackConn no longer maintaining s.conns.
func TestCoverServeAndShutdownTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	var handled atomic.Int32

	var releaseOnce sync.Once

	released := make(chan struct{})
	release := func() { releaseOnce.Do(func() { close(released) }) }

	handler := func(ctx context.Context, conn net.Conn) {
		handled.Add(1)

		// Block until released so the connection is tracked while live, then
		// exit so Shutdown's graceful wait completes.
		<-released

		buf := make([]byte, 8)
		_, _ = conn.Read(buf)
	}

	srv := NewServer(l, handler, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Serve(ctx) }()

	addr := l.Addr().String()

	conn1, err := net.Dial("tcp", addr)
	if err != nil {
		release()
		t.Fatal(err)
	}

	conn2, err := net.Dial("tcp", addr)
	if err != nil {
		release()
		t.Fatal(err)
	}

	// Ensure blocked handlers and client conns never leak if the test fails.
	t.Cleanup(func() {
		release()

		_ = conn1.Close()
		_ = conn2.Close()
	})

	// Wait for both connections to be accepted and reach the handler.
	deadline := time.Now().Add(2 * time.Second)
	for handled.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d connections handled, want 2", handled.Load())
		}

		time.Sleep(5 * time.Millisecond)
	}

	// Both connections are still live (handlers blocked), so they must be
	// tracked. This directly exercises trackConn.
	srv.mu.Lock()
	tracked := len(srv.conns)
	srv.mu.Unlock()

	if tracked != 2 {
		t.Errorf("tracked %d live connections, want 2", tracked)
	}

	// Release the handlers so they return, then shut down gracefully.
	release()

	_ = conn1.Close()
	_ = conn2.Close()

	srv.Shutdown()

	// After Shutdown returns, every handler goroutine has finished, so untrackConn
	// must have removed all connections.
	srv.mu.Lock()
	remaining := len(srv.conns)
	srv.mu.Unlock()

	if remaining != 0 {
		t.Errorf("after shutdown %d connections still tracked, want 0", remaining)
	}
}

// TestCoverServeContextCancelReturns verifies Serve unblocks and returns the
// context error once the context is cancelled (the accept loop's ErrClosed
// path).
func TestCoverServeContextCancelReturns(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(l, func(ctx context.Context, conn net.Conn) {}, nil)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected a non-nil error from Serve after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}
}

// TestCoverListenStaleSocketRemoval covers Listen over a unix socket using a
// short /tmp path (within the sockaddr limit), verifies the 0700 permission,
// and exercises the stale-socket removal branch: after the first listener is
// closed, a leftover file is planted at the same path and a second Listen must
// remove it and bind successfully. It skips (rather than fails) where the
// sandbox denies AF_UNIX bind.
func TestCoverListenStaleSocketRemoval(t *testing.T) {
	sockPath := filepath.Join(shortSocketDir(t), "s.sock")

	l1, err := Listen(sockPath)
	if err != nil {
		if bindUnavailable(err) {
			t.Skipf("unix socket bind unavailable in this environment: %v", err)
		}

		t.Fatalf("first Listen: %v", err)
	}

	// The permissions must restrict the socket to the owner (0700).
	info, err := os.Stat(sockPath)
	if err != nil {
		_ = l1.Close()

		t.Fatalf("stat socket: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket perm = %o, want 700", perm)
	}

	// Closing the listener unlinks its socket file; plant a stale file in its
	// place so the next Listen must os.Remove it before binding.
	_ = l1.Close()

	if err := os.WriteFile(sockPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	l2, err := Listen(sockPath)
	if err != nil {
		t.Fatalf("second Listen (stale removal): %v", err)
	}

	_ = l2.Close()
}

// TestCoverListenBadDir verifies Listen fails closed when its socket directory
// cannot be created (a non-directory component in the path).
func TestCoverListenBadDir(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "grcov")
	if err != nil {
		t.Skipf("cannot create short tmp dir: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Create a regular file, then try to use it as a parent directory.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Listen(filepath.Join(blocker, "s.sock")); err == nil {
		t.Fatal("expected Listen to fail when the socket dir cannot be created")
	}
}
