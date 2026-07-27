package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	serverShutdownGrace     = 5 * time.Second
	serverShutdownForceWait = time.Second
)

type Server struct {
	listener          net.Listener
	listenerCloseOnce sync.Once
	handler           func(ctx context.Context, conn net.Conn)
	wg                sync.WaitGroup
	log               *slog.Logger

	mu     sync.Mutex
	conns  map[net.Conn]serverConnInfo
	closed bool
}

type serverConnInfo struct {
	acceptedAt time.Time
	localAddr  string
	remoteAddr string
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
	return &Server{listener: l, handler: handler, log: log, conns: make(map[net.Conn]serverConnInfo)}
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

	s.conns[c] = serverConnInfo{
		acceptedAt: time.Now(),
		localAddr:  addrString(c.LocalAddr()),
		remoteAddr: addrString(c.RemoteAddr()),
	}
	s.wg.Add(1)

	return true
}

func (s *Server) untrackConn(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func addrString(addr net.Addr) string {
	if addr == nil {
		return ""
	}

	return addr.String()
}

func (s *Server) activeHandlerSnapshot(now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	handlers := make([]string, 0, len(s.conns))
	for _, info := range s.conns {
		age := now.Sub(info.acceptedAt)
		if age < 0 {
			age = 0
		}

		handlers = append(handlers, fmt.Sprintf("local=%s remote=%s age=%s", info.localAddr, info.remoteAddr, age.Truncate(time.Millisecond)))
	}

	sort.Strings(handlers)

	return handlers
}

func (s *Server) closeListenerPreservingSocket() {
	s.listenerCloseOnce.Do(func() {
		if unixListener, ok := s.listener.(*net.UnixListener); ok {
			unixListener.SetUnlinkOnClose(false)
		}

		_ = s.listener.Close()
	})
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()

		s.closeListenerPreservingSocket()
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
	s.shutdown(serverShutdownGrace, serverShutdownForceWait)
}

func (s *Server) shutdown(grace, forceWait time.Duration) {
	s.closeListenerPreservingSocket()

	// Give handlers a short window to finish gracefully.
	deadline := time.Now().Add(grace)

	// Mark closed under the mutex before waiting on the group. This is the write
	// half of the barrier with trackConn: once closed is set, no further wg.Add
	// can happen (Serve's trackConn returns false), so the wg.Wait below cannot
	// race a concurrent wg.Add. The waits below are deliberately bounded: a
	// handler can be parked in a non-context-aware child wait even after its
	// connection is closed.
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

	if waitForServerDrain(done, grace) {
		return
	}

	handlers := s.activeHandlerSnapshot(time.Now())
	if s.log != nil {
		s.log.Warn("server graceful shutdown timed out; force-closing active handlers",
			"active_handlers", len(handlers),
			"handlers", handlers,
			"grace", grace,
		)
	}

	// Force-close any remaining connections.
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()

	if waitForServerDrain(done, forceWait) {
		return
	}

	handlers = s.activeHandlerSnapshot(time.Now())
	if s.log != nil {
		s.log.Warn("server handler drain still blocked after force close",
			"active_handlers", len(handlers),
			"handlers", handlers,
			"force_wait", forceWait,
		)
	}
}

func waitForServerDrain(done <-chan struct{}, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
