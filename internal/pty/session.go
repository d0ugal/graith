package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

type Session struct {
	ID         string
	Cmd        *exec.Cmd
	Ptmx       *os.File
	Scrollback *Scrollback

	mu             sync.RWMutex
	attachedWriter io.Writer
	screen         vt10x.Terminal
	done           chan struct{}
	readDone       chan struct{}
	exitCode       int
	exited         bool
	adoptedPID     int
}

type SessionOpts struct {
	ID         string
	Command    string
	Args       []string
	Dir        string
	Env        map[string]string
	Rows, Cols uint16
	LogPath    string
	MaxLogSize int64
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
		screen:   vt10x.New(vt10x.WithSize(int(opts.Cols), int(opts.Rows))),
		done:     make(chan struct{}),
		readDone: make(chan struct{}),
	}

	go s.readLoop()
	go s.waitLoop()

	return s, nil
}

type AdoptOpts struct {
	ID         string
	Fd         uintptr
	PID        int
	LogPath    string
	MaxLogSize int64
}

func AdoptSession(opts AdoptOpts) (*Session, error) {
	ptmx := os.NewFile(opts.Fd, fmt.Sprintf("ptmx-%s", opts.ID))
	if ptmx == nil {
		return nil, fmt.Errorf("invalid fd %d for session %s", opts.Fd, opts.ID)
	}

	sb, err := NewScrollback(opts.LogPath, opts.MaxLogSize)
	if err != nil {
		return nil, fmt.Errorf("open scrollback: %w", err)
	}

	s := &Session{
		ID:         opts.ID,
		Ptmx:       ptmx,
		Scrollback: sb,
		screen:     vt10x.New(vt10x.WithSize(80, 24)),
		done:       make(chan struct{}),
		readDone:   make(chan struct{}),
		adoptedPID: opts.PID,
	}

	go s.readLoop()
	go s.adoptedWaitLoop()

	return s, nil
}

func (s *Session) adoptedWaitLoop() {
	exitCode := -1

	proc, _ := os.FindProcess(s.adoptedPID)
	ps, waitErr := proc.Wait()
	if waitErr == nil {
		exitCode = ps.ExitCode()
	} else {
		for {
			time.Sleep(time.Second)
			if syscall.Kill(s.adoptedPID, 0) != nil {
				break
			}
		}
	}

	<-s.readDone
	s.mu.Lock()
	s.exited = true
	s.exitCode = exitCode
	s.mu.Unlock()
	close(s.done)
}

func (s *Session) ProcessPID() int {
	if s.Cmd != nil && s.Cmd.Process != nil {
		return s.Cmd.Process.Pid
	}
	return s.adoptedPID
}

func (s *Session) Fd() uintptr {
	return s.Ptmx.Fd()
}

func (s *Session) readLoop() {
	defer close(s.readDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := s.Ptmx.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			_, _ = s.Scrollback.Write(chunk)
			s.mu.Lock()
			_, _ = s.screen.Write(chunk)
			w := s.attachedWriter
			s.mu.Unlock()
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
	// Wait for readLoop to drain remaining PTY output before signalling done.
	<-s.readDone
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
	s.mu.Lock()
	s.screen.Resize(int(cols), int(rows))
	s.mu.Unlock()
	return pty.Setsize(s.Ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

func (s *Session) Attach(w io.Writer) { s.mu.Lock(); s.attachedWriter = w; s.mu.Unlock() }
func (s *Session) Detach()            { s.mu.Lock(); s.attachedWriter = nil; s.mu.Unlock() }

func (s *Session) DetachWriter(w io.Writer) {
	s.mu.Lock()
	if s.attachedWriter == w {
		s.attachedWriter = nil
	}
	s.mu.Unlock()
}
func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Exited() bool          { s.mu.RLock(); defer s.mu.RUnlock(); return s.exited }
func (s *Session) ExitCode() int         { s.mu.RLock(); defer s.mu.RUnlock(); return s.exitCode }

func (s *Session) Kill() error {
	pid := s.ProcessPID()
	if pid == 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGTERM)
}

func (s *Session) ForceKill() error {
	pid := s.ProcessPID()
	if pid == 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}

func (s *Session) Close() {
	_ = s.Ptmx.Close()
	_ = s.Scrollback.Close()
}

func buildEnv(extra map[string]string) []string {
	env := os.Environ()
	env = append(env, "TERM=xterm-256color")
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}
