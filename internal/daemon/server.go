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
)

type Server struct {
	listener net.Listener
	handler  func(ctx context.Context, conn net.Conn)
	wg       sync.WaitGroup
	log      *slog.Logger
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
	return &Server{listener: l, handler: handler, log: log}
}

func (s *Server) Serve(ctx context.Context) error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if s.log != nil {
				s.log.Warn("accept error", "err", err)
			}
			continue
		}

		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer c.Close()
			s.handler(ctx, c)
		}(conn)
	}
}

func (s *Server) Shutdown() {
	s.listener.Close()
	s.wg.Wait()
}
