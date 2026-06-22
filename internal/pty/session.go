package pty

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
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

	mu               sync.RWMutex
	writeMu          sync.Mutex
	writers          []io.Writer
	screen           vt10x.Terminal
	done             chan struct{}
	readDone         chan struct{}
	exitCode         int
	exitSignal       syscall.Signal
	peakRSSBytes     int64
	exited           bool
	adoptedPID       int
	adoptedStartTime int64
	lastOutputAt     time.Time
	lastUserInputAt  time.Time
	userInputCond    *sync.Cond
	adoptedAt        time.Time
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
	s.userInputCond = sync.NewCond(&sync.Mutex{})

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

	cols, rows := 80, 24
	if ws, err := pty.GetsizeFull(ptmx); err == nil && ws.Cols > 0 && ws.Rows > 0 {
		cols, rows = int(ws.Cols), int(ws.Rows)
	}

	startTime, stErr := ProcessStartTime(opts.PID)
	if stErr != nil {
		slog.Warn("could not capture process start time for adopted session; PID reuse detection degraded",
			"session", opts.ID, "pid", opts.PID, "error", stErr)
	}

	s := &Session{
		ID:               opts.ID,
		Ptmx:             ptmx,
		Scrollback:       sb,
		screen:           vt10x.New(vt10x.WithSize(cols, rows)),
		done:             make(chan struct{}),
		readDone:         make(chan struct{}),
		adoptedPID:       opts.PID,
		adoptedStartTime: startTime,
		adoptedAt:        time.Now(),
	}
	s.userInputCond = sync.NewCond(&sync.Mutex{})

	if tail, err := sb.TailBytes(128 * 1024); err == nil && len(tail) > 0 {
		s.screen.Write(tail)
	}

	go s.readLoop()
	go s.adoptedWaitLoop()

	return s, nil
}

const adoptedPollTimeout = 24 * time.Hour

func (s *Session) adoptedWaitLoop() {
	exitCode := -1

	// proc.Wait only works when we're the parent (rare for adopted
	// processes). Run it in the background so it doesn't block the poll
	// loop that handles the common case.
	waitDone := make(chan int, 1)
	go func() {
		proc, _ := os.FindProcess(s.adoptedPID)
		if ps, err := proc.Wait(); err == nil {
			waitDone <- ps.ExitCode()
		}
	}()

	deadlineReached := false
	deadline := time.After(adoptedPollTimeout)
	poll := time.NewTicker(time.Second)
	defer poll.Stop()

	for {
		select {
		case code := <-waitDone:
			exitCode = code
			goto done
		case <-deadline:
			deadlineReached = true
		case <-poll.C:
		}
		if err := syscall.Kill(s.adoptedPID, 0); err != nil {
			break
		}
		if s.adoptedStartTime != 0 {
			cur, err := ProcessStartTime(s.adoptedPID)
			if err != nil || cur != s.adoptedStartTime {
				break
			}
		}
		// Safety timeout only applies when we have no start time to
		// compare — the start time check already handles PID reuse, so
		// we don't need to force-exit live sessions.
		if deadlineReached && s.adoptedStartTime == 0 {
			slog.Warn("adopted wait loop deadline reached without start time identity",
				"session", s.ID, "pid", s.adoptedPID)
			break
		}
	}

done:
	select {
	case code := <-waitDone:
		exitCode = code
	default:
	}
	<-s.readDone
	s.mu.Lock()
	s.exited = true
	s.exitCode = exitCode
	s.mu.Unlock()
	if s.userInputCond != nil {
		s.userInputCond.Broadcast()
	}
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
			s.lastOutputAt = time.Now()
			writers := make([]io.Writer, len(s.writers))
			copy(writers, s.writers)
			s.mu.Unlock()
			for _, w := range writers {
				if w != nil {
					_, _ = w.Write(chunk)
				}
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
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				s.exitSignal = ws.Signal()
			}
			s.peakRSSBytes = extractPeakRSS(exitErr.ProcessState)
		} else {
			s.exitCode = -1
		}
	} else if s.Cmd.ProcessState != nil {
		s.peakRSSBytes = extractPeakRSS(s.Cmd.ProcessState)
	}
	s.mu.Unlock()
	if s.userInputCond != nil {
		s.userInputCond.Broadcast()
	}
	close(s.done)
}

func extractPeakRSS(ps *os.ProcessState) int64 {
	if ps == nil {
		return 0
	}
	if ru, ok := ps.SysUsage().(*syscall.Rusage); ok && ru != nil {
		rss := ru.Maxrss
		if runtime.GOOS != "darwin" {
			rss *= 1024 // Linux reports KB; macOS already reports bytes.
		}
		return rss
	}
	return 0
}

func (s *Session) WriteInput(data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeInputLocked(data)
}

// typeInputDelay is the pause between writing text and the submit key in
// WriteInputAndSubmit. TUI frameworks treat text+CR in a single read as a
// paste (inserting a newline) rather than "type then press Enter". Separating
// the writes lets the TUI drain the text before the CR arrives.
const typeInputDelay = 50 * time.Millisecond

// WriteInputAndSubmit writes text followed by a carriage return, with a brief
// pause between the two so that TUI frameworks treat them as separate events.
// The entire operation holds writeMu to prevent interleaving from other sources.
func (s *Session) WriteInputAndSubmit(data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if len(data) > 0 {
		if err := s.writeInputLocked(data); err != nil {
			return err
		}
		time.Sleep(typeInputDelay)
	}
	return s.writeInputLocked([]byte("\r"))
}

func (s *Session) writeInputLocked(data []byte) error {
	s.mu.RLock()
	exited := s.exited
	s.mu.RUnlock()
	if exited {
		return fmt.Errorf("session process has exited")
	}

	n, err := s.Ptmx.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

// NotifyUserInput records that the attached user just typed something.
// Call this from the passthrough data path, not from gr type.
func (s *Session) NotifyUserInput() {
	s.userInputCond.L.Lock()
	s.lastUserInputAt = time.Now()
	s.userInputCond.L.Unlock()
	s.userInputCond.Broadcast()
}

// WaitForUserIdle blocks until at least idleTimeout has elapsed since the
// last user keystroke, or until maxWait has elapsed (whichever comes first).
// Returns true if the idle condition was met, false if maxWait expired.
func (s *Session) WaitForUserIdle(idleTimeout, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	s.userInputCond.L.Lock()
	defer s.userInputCond.L.Unlock()

	for {
		s.mu.RLock()
		exited := s.exited
		s.mu.RUnlock()
		if exited {
			return true
		}
		idle := time.Since(s.lastUserInputAt)
		if idle >= idleTimeout {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}

		// Wake up when the next idle-timeout window could be satisfied,
		// or when new user input arrives (Broadcast from NotifyUserInput).
		wakeIn := idleTimeout - idle
		if dl := time.Until(deadline); dl < wakeIn {
			wakeIn = dl
		}
		timer := time.AfterFunc(wakeIn, func() {
			s.userInputCond.L.Lock()
			s.userInputCond.Broadcast()
			s.userInputCond.L.Unlock()
		})
		s.userInputCond.Wait()
		timer.Stop()
	}
}

func (s *Session) Resize(rows, cols uint16) error {
	s.mu.Lock()
	s.screen.Resize(int(cols), int(rows))
	s.mu.Unlock()
	return pty.Setsize(s.Ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

// Poke sends SIGWINCH to the session's process group. This interrupts
// blocked reads and forces TUI frameworks to re-check stdin, ensuring
// recently written input is consumed. The child process was started
// with Setsid, so its PID equals its process group ID.
func (s *Session) Poke() {
	if pid := s.ProcessPID(); pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGWINCH)
	}
}

func (s *Session) Attach(w io.Writer) {
	s.mu.Lock()
	s.writers = append(s.writers, w)
	s.mu.Unlock()
}

func (s *Session) Detach() {
	s.mu.Lock()
	s.writers = nil
	s.mu.Unlock()
}

func (s *Session) DetachWriter(w io.Writer) {
	s.mu.Lock()
	for i, wr := range s.writers {
		if wr == w {
			s.writers = append(s.writers[:i], s.writers[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
}
func (s *Session) Done() <-chan struct{}   { return s.done }
func (s *Session) LastOutputAt() time.Time { s.mu.RLock(); defer s.mu.RUnlock(); return s.lastOutputAt }

// RecentlyAdopted returns true if the session was adopted (daemon restart)
// within the last duration and has not yet received fresh PTY output.
func (s *Session) RecentlyAdopted(grace time.Duration) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.adoptedAt.IsZero() && s.lastOutputAt.IsZero() && time.Since(s.adoptedAt) < grace
}
func (s *Session) Exited() bool  { s.mu.RLock(); defer s.mu.RUnlock(); return s.exited }
func (s *Session) ExitCode() int { s.mu.RLock(); defer s.mu.RUnlock(); return s.exitCode }
func (s *Session) ExitSignal() syscall.Signal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.exitSignal
}
func (s *Session) PeakRSSBytes() int64 { s.mu.RLock(); defer s.mu.RUnlock(); return s.peakRSSBytes }

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
	<-s.readDone
	_ = s.Scrollback.Close()
}

func buildEnv(extra map[string]string) []string {
	overrides := make(map[string]string, len(extra)+1)
	overrides["TERM"] = "xterm-256color"
	for k, v := range extra {
		overrides[k] = v
	}

	parent := os.Environ()
	env := make([]string, 0, len(overrides)+len(parent))
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	for _, e := range parent {
		if k, _, ok := strings.Cut(e, "="); ok {
			if _, overridden := overrides[k]; overridden {
				continue
			}
		}
		env = append(env, e)
	}
	return env
}
