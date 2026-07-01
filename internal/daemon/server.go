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

	mu    sync.Mutex
	conns map[net.Conn]struct{}
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

	if err := os.Chmod(sockPath, 0o700); err != nil {
		l.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}

	return l, nil
}

func NewServer(l net.Listener, handler func(ctx context.Context, conn net.Conn), log *slog.Logger) *Server {
	return &Server{listener: l, handler: handler, log: log, conns: make(map[net.Conn]struct{})}
}

func (s *Server) trackConn(c net.Conn) {
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) untrackConn(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.listener.Close()
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

		s.trackConn(conn)

		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer c.Close()
			defer s.untrackConn(c)

			s.handler(ctx, c)
		}(conn)
	}
}

func (s *Server) Shutdown() {
	s.listener.Close()

	// Give handlers a short window to finish gracefully.
	deadline := time.Now().Add(5 * time.Second)

	s.mu.Lock()
	for c := range s.conns {
		c.SetDeadline(deadline)
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
			c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	}
}
