package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Server struct {
	listener net.Listener
	handler  func(ctx context.Context, conn net.Conn)
	wg       sync.WaitGroup
	log      *slog.Logger

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

func Listen(sockPath string) (net.Listener, error) {
	dir := filepath.Dir(sockPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}

	_ = os.Remove(sockPath)

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	if err := os.Chmod(sockPath, 0o700); err != nil { //nolint:gosec // G302: 0700 restricts the control socket to the owner only
		_ = l.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}

	return l, nil
}

func NewServer(l net.Listener, handler func(ctx context.Context, conn net.Conn), log *slog.Logger) *Server {
	return &Server{listener: l, handler: handler, log: log, conns: make(map[net.Conn]struct{})}
}

// trackConn registers an accepted connection and enrolls it in the wait group,
// unless a shutdown has already begun. It returns false when the server is
// shutting down, in which case the caller must not run the handler: the
// wg.Add here is serialized under the same mutex Shutdown takes before it calls
// wg.Wait, so Add can never race with Wait (concurrent Add/Wait is a WaitGroup
// misuse the race detector flags).
func (s *Server) trackConn(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false
	}

	s.conns[c] = struct{}{}
	s.wg.Add(1)

	return true
}

func (s *Server) untrackConn(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()

		_ = s.listener.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return ctx.Err()
			}

			if s.log != nil {
				s.log.Warn("accept error", "err", err)
			}

			continue
		}

		// Enroll the connection under the mutex. If Shutdown has already begun,
		// don't start a handler (and don't wg.Add) — just drop the connection.
		if !s.trackConn(conn) {
			_ = conn.Close()
			return ctx.Err()
		}

		go func(c net.Conn) {
			defer s.wg.Done()
			defer func() { _ = c.Close() }()
			defer s.untrackConn(c)

			s.handler(ctx, c)
		}(conn)
	}
}

func (s *Server) Shutdown() {
	_ = s.listener.Close()

	// Give handlers a short window to finish gracefully.
	deadline := time.Now().Add(5 * time.Second)

	// Mark closed under the mutex before waiting on the group. This is the write
	// half of the barrier with trackConn: once closed is set, no further wg.Add
	// can happen (Serve's trackConn returns false), so the wg.Wait below cannot
	// race a concurrent wg.Add.
	s.mu.Lock()
	s.closed = true

	for c := range s.conns {
		_ = c.SetDeadline(deadline)
	}
	s.mu.Unlock()

	done := make(chan struct{})

	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// Force-close any remaining connections.
		s.mu.Lock()
		for c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	}
}
