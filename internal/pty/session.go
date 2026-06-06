package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

type Session struct {
	ID         string
	Cmd        *exec.Cmd
	Ptmx       *os.File
	Scrollback *Scrollback

	mu             sync.RWMutex
	attachedWriter io.Writer
	done           chan struct{}
	exitCode       int
	exited         bool
}

type SessionOpts struct {
	ID          string
	Command     string
	Args        []string
	Dir         string
	Env         map[string]string
	Rows, Cols  uint16
	LogPath     string
	MaxLogSize  int64
}

func NewSession(opts SessionOpts) (*Session, error) {
	cmd := exec.Command(opts.Command, opts.Args...)
	cmd.Dir = opts.Dir
	cmd.Env = buildEnv(opts.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	size := &pty.Winsize{Rows: opts.Rows, Cols: opts.Cols}
	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	sb, err := NewScrollback(opts.LogPath, opts.MaxLogSize)
	if err != nil {
		ptmx.Close()
		cmd.Process.Kill()
		return nil, fmt.Errorf("scrollback: %w", err)
	}

	s := &Session{
		ID: opts.ID, Cmd: cmd, Ptmx: ptmx, Scrollback: sb,
		done: make(chan struct{}),
	}

	go s.readLoop()
	go s.waitLoop()

	return s, nil
}

func (s *Session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.Ptmx.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			s.Scrollback.Write(chunk)
			s.mu.RLock()
			w := s.attachedWriter
			s.mu.RUnlock()
			if w != nil {
				w.Write(chunk)
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) waitLoop() {
	err := s.Cmd.Wait()
	s.mu.Lock()
	s.exited = true
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			s.exitCode = exitErr.ExitCode()
		} else {
			s.exitCode = -1
		}
	}
	s.mu.Unlock()
	close(s.done)
}

func (s *Session) WriteInput(data []byte) error {
	_, err := s.Ptmx.Write(data)
	return err
}

func (s *Session) Resize(rows, cols uint16) error {
	return pty.Setsize(s.Ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

func (s *Session) Attach(w io.Writer)    { s.mu.Lock(); s.attachedWriter = w; s.mu.Unlock() }
func (s *Session) Detach()               { s.mu.Lock(); s.attachedWriter = nil; s.mu.Unlock() }
func (s *Session) Done() <-chan struct{}  { return s.done }
func (s *Session) Exited() bool          { s.mu.RLock(); defer s.mu.RUnlock(); return s.exited }
func (s *Session) ExitCode() int         { s.mu.RLock(); defer s.mu.RUnlock(); return s.exitCode }

func (s *Session) Kill() error {
	if s.Cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-s.Cmd.Process.Pid, syscall.SIGTERM)
}

func (s *Session) ForceKill() error {
	if s.Cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-s.Cmd.Process.Pid, syscall.SIGKILL)
}

func (s *Session) Close() {
	s.Ptmx.Close()
	s.Scrollback.Close()
}

func buildEnv(extra map[string]string) []string {
	env := os.Environ()
	env = append(env, "TERM=xterm-256color")
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}
