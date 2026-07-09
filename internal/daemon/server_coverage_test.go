package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// The unix-socket paths under this environment's t.TempDir() exceed the
// platform's sockaddr length limit, so the socket-based server test cannot
// bind here. These tests cover the Server accept loop, connection tracking,
// and graceful shutdown over a loopback TCP listener (no path-length limit),
// and cover Listen itself via a deliberately short /tmp socket path.

// TestCoverServeAndShutdownTCP drives NewServer/Serve/trackConn/untrackConn and
// a graceful Shutdown over a TCP listener, verifying every accepted connection
// reaches the handler.
func TestCoverServeAndShutdownTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	var handled atomic.Int32

	released := make(chan struct{})

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
		t.Fatal(err)
	}

	conn2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for both connections to be accepted and reach the handler.
	deadline := time.Now().Add(2 * time.Second)
	for handled.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d connections handled, want 2", handled.Load())
		}

		time.Sleep(5 * time.Millisecond)
	}

	// Release the handlers so they return, then shut down gracefully.
	close(released)

	_ = conn1.Close()
	_ = conn2.Close()

	srv.Shutdown()

	if handled.Load() != 2 {
		t.Errorf("handled %d connections, want 2", handled.Load())
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

// TestCoverListenShortPath covers Listen end-to-end over a unix socket using a
// short /tmp path that fits within the sockaddr limit, and exercises the
// stale-socket removal branch by binding the same path twice.
func TestCoverListenShortPath(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "grcov")
	if err != nil {
		t.Skipf("cannot create short tmp dir: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sockPath := filepath.Join(dir, "s.sock")

	l1, err := Listen(sockPath)
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}

	// The socket file now exists; a second Listen must remove the stale file and
	// succeed rather than failing with "address already in use".
	l2, err := Listen(sockPath)
	if err != nil {
		_ = l1.Close()
		t.Fatalf("second Listen (stale removal): %v", err)
	}

	// The permissions must restrict the socket to the owner (0700).
	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket perm = %o, want 700", perm)
	}

	_ = l1.Close()
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
