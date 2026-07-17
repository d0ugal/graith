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
)

type Session struct {
	ID         string
	Cmd        *exec.Cmd
	Ptmx       *os.File
	Scrollback *Scrollback

	mu               sync.RWMutex
	writeMu          sync.Mutex
	writers          []io.Writer
	screen           Terminal
	done             chan struct{}
	readDone         chan struct{}
	exitCode         int
	exitSignal       syscall.Signal
	peakRSSBytes     int64
	bytesRead        int64
	exited           bool
	adoptedPID       int
	adoptedStartTime int64
	lastOutputAt     time.Time
	lastUserInputAt  time.Time
	// inputDelay is the pause between typed text and the submit CR in
	// WriteInputAndSubmit. Resolved at construction or adoption (SessionOpts /
	// AdoptOpts InputDelay, or the typeInputDelay default) and updateable via
	// SetInputDelay on a config hot-reload (issue #1294). Read and written only
	// under writeMu, so both the live update and the WriteInputAndSubmit read stay
	// race-free.
	inputDelay time.Duration
	// adoptedPollTimeout / adoptedPollInterval bound the adopted-PTY babysit loop.
	// Resolved at adoption (AdoptOpts values or the package defaults); immutable
	// after. Unused for a freshly-launched (non-adopted) session.
	adoptedPollTimeout  time.Duration
	adoptedPollInterval time.Duration
	userInputCond       *sync.Cond
	adoptedAt           time.Time
	createdAt           time.Time
	// log routes this session's diagnostics to the daemon's logger. It is set
	// once at construction and only read, so it needs no lock. Never nil (falls
	// back to slog.Default()).
	log *slog.Logger
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
	// InputDelay is the pause WriteInputAndSubmit inserts between the typed text
	// and the submit carriage return. Non-positive falls back to typeInputDelay,
	// the built-in default (the daemon passes the [lifecycle] input_delay policy).
	InputDelay time.Duration
	// Logger routes this session's PTY/scrollback diagnostics to the daemon's
	// logger. Nil falls back to slog.Default(). See the Session.log field.
	Logger *slog.Logger
}

func NewSession(opts SessionOpts) (*Session, error) {
	cmd := exec.Command(opts.Command, opts.Args...)
	cmd.Dir = opts.Dir
	cmd.Env = buildEnv(opts.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	size := &pty.Winsize{Rows: opts.Rows, Cols: opts.Cols}

	// Stamp the launch instant *before* the process starts so the launch→first-
	// output and launch→active durations (issue #1104) include PTY and
	// scrollback setup — otherwise a fast agent can emit output before the epoch
	// is recorded, under-reporting the true startup time.
	launchedAt := time.Now()

	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	sb, err := NewScrollback(opts.LogPath, opts.MaxLogSize)
	if err != nil {
		// The process is already running; kill it so a scrollback-open failure
		// doesn't leak an unmanaged agent. No "session spawned"/"session exited"
		// record will ever be emitted for this pid, so log the rollback kill here
		// (issue #1104).
		rollbackLog := opts.Logger
		if rollbackLog == nil {
			rollbackLog = slog.Default()
		}

		pid := 0
		if cmd.Process != nil {
			pid = cmd.Process.Pid
		}

		rollbackLog.Info("stopping session",
			"id", opts.ID, "reason", "rollback", "initiator", "pty-scrollback-failure",
			"pid", pid, "pgid", pid, "err", err)

		_ = ptmx.Close()
		_ = cmd.Process.Kill()

		return nil, fmt.Errorf("scrollback: %w", err)
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	sb.SetLogger(log)

	inputDelay := opts.InputDelay
	if inputDelay <= 0 {
		inputDelay = typeInputDelay
	}

	s := &Session{
		ID: opts.ID, Cmd: cmd, Ptmx: ptmx, Scrollback: sb,
		screen:     newTerminal(int(opts.Cols), int(opts.Rows)),
		done:       make(chan struct{}),
		readDone:   make(chan struct{}),
		createdAt:  launchedAt,
		log:        log,
		inputDelay: inputDelay,
	}
	s.userInputCond = sync.NewCond(&sync.Mutex{})

	// The inherited scrollback size distinguishes a fresh log from one reopened
	// (append) on resume/restart — the append case is the restart path in #1087.
	written, _, _ := sb.Stats()
	log.Debug("scrollback writer initialized", "session", opts.ID,
		"path", opts.LogPath, "existing_bytes", written, "max_size", opts.MaxLogSize)

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
	// DefaultRows / DefaultCols are the geometry used when the adopted ptmx size
	// can't be read. Non-positive falls back to the built-in 24x80 (the daemon
	// passes the [lifecycle] default_rows/default_cols policy).
	DefaultRows, DefaultCols uint16
	// HydrationBytes is how much of the scrollback tail is replayed into the
	// adopted session's virtual screen. Negative falls back to the built-in
	// default; 0 disables hydration (the daemon passes the [lifecycle]
	// scrollback_hydration_bytes policy).
	HydrationBytes int
	// PollTimeout / PollInterval bound the adopted-PTY babysit loop. Non-positive
	// falls back to the built-in defaults (adoptedPollTimeout / one second).
	PollTimeout  time.Duration
	PollInterval time.Duration
	// InputDelay is the pause WriteInputAndSubmit inserts between the typed text
	// and the submit carriage return. Non-positive falls back to typeInputDelay,
	// matching NewSession, so an adopted session honours the configured
	// [lifecycle] input_delay instead of silently reverting to the package
	// default across a daemon upgrade (issue #1294).
	InputDelay time.Duration
	// Logger routes this session's diagnostics to the daemon's logger. Nil
	// falls back to slog.Default().
	Logger *slog.Logger
}

func AdoptSession(opts AdoptOpts) (*Session, error) {
	ptmx := os.NewFile(opts.Fd, "ptmx-"+opts.ID)
	if ptmx == nil {
		return nil, fmt.Errorf("invalid fd %d for session %s", opts.Fd, opts.ID)
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	sb, err := NewScrollback(opts.LogPath, opts.MaxLogSize)
	if err != nil {
		return nil, fmt.Errorf("open scrollback: %w", err)
	}

	sb.SetLogger(log)

	defCols := opts.DefaultCols
	if defCols == 0 {
		defCols = 80
	}

	defRows := opts.DefaultRows
	if defRows == 0 {
		defRows = 24
	}

	cols, rows := int(defCols), int(defRows)
	if ws, err := pty.GetsizeFull(ptmx); err == nil && ws.Cols > 0 && ws.Rows > 0 {
		cols, rows = int(ws.Cols), int(ws.Rows)
	}

	startTime, stErr := ProcessStartTime(opts.PID)
	if stErr != nil {
		log.Warn("could not capture process start time for adopted session; PID reuse detection degraded",
			"session", opts.ID, "pid", opts.PID, "error", stErr)
	}

	pollTimeout := opts.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = adoptedPollTimeout
	}

	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = time.Second
	}

	inputDelay := opts.InputDelay
	if inputDelay <= 0 {
		inputDelay = typeInputDelay
	}

	s := &Session{
		ID:                  opts.ID,
		Ptmx:                ptmx,
		Scrollback:          sb,
		screen:              newTerminal(cols, rows),
		done:                make(chan struct{}),
		readDone:            make(chan struct{}),
		adoptedPID:          opts.PID,
		adoptedStartTime:    startTime,
		adoptedAt:           time.Now(),
		createdAt:           time.Now(),
		log:                 log,
		inputDelay:          inputDelay,
		adoptedPollTimeout:  pollTimeout,
		adoptedPollInterval: pollInterval,
	}
	s.userInputCond = sync.NewCond(&sync.Mutex{})

	// HydrationBytes < 0 means "use the built-in default"; 0 disables hydration.
	hydrate := opts.HydrationBytes
	if hydrate < 0 {
		hydrate = 128 * 1024
	}

	if hydrate > 0 {
		if tail, err := sb.TailBytes(int64(hydrate)); err == nil && len(tail) > 0 {
			_, _ = s.screen.Write(tail)
		}
	}

	go s.readLoop()
	go s.adoptedWaitLoop()

	return s, nil
}

// adoptedPollTimeout is the built-in safety deadline for the adopted-PTY babysit
// loop when AdoptOpts.PollTimeout is unset. The daemon overrides it via the
// [lifecycle] adopted_timeout policy.
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

	timeout := s.adoptedPollTimeout
	if timeout <= 0 {
		timeout = adoptedPollTimeout
	}

	interval := s.adoptedPollInterval
	if interval <= 0 {
		interval = time.Second
	}

	deadlineReached := false
	deadline := time.After(timeout)

	poll := time.NewTicker(interval)
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

// Pgid returns the process-group id graith signals on Kill/ForceKill. For
// graith-spawned sessions (started with Setsid, see NewSession) the child is a
// session/group leader and its PGID equals its PID. For adopted sessions graith
// did not set Setsid, so PGID may differ from the reported pid — but Kill/
// ForceKill signal -pid regardless, so this remains an honest report of "the
// group graith signals". Logging pid+pgid together (issue #1104) makes an
// OS-level signal trace (e.g. `kill -0 -<pgid>`) possible from the daemon log
// alone. Returns 0 when the pid is unknown.
func (s *Session) Pgid() int {
	return s.ProcessPID()
}

func (s *Session) Fd() uintptr {
	return s.Ptmx.Fd()
}

// ScrollbackFile returns the session's scrollback buffer. It exists so a
// SessionDriver interface can expose the scrollback via a method (interfaces
// can't have fields), while the exported Scrollback field stays for direct
// concrete use within this package.
func (s *Session) ScrollbackFile() *Scrollback {
	return s.Scrollback
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
			first := s.bytesRead == 0
			s.bytesRead += int64(n)
			writers := make([]io.Writer, len(s.writers))
			copy(writers, s.writers)
			s.mu.Unlock()

			// Record the first byte of PTY output. Its absence is the signature
			// of the blank-screen-on-restart bug (issue #1087): the agent process
			// is alive but never rendered anything, so scrollback stays empty and
			// attach shows nothing. Logging the first output makes "did the agent
			// ever produce anything?" answerable from the daemon log. The
			// launch→first-output duration (issue #1104) distinguishes a slow
			// starter from a stuck one: a large gap means the agent booted but took
			// a long time to render, not that it hung.
			if first {
				s.mu.RLock()
				sinceLaunch := time.Since(s.createdAt)
				s.mu.RUnlock()

				s.log.Info("pty first output",
					"session", s.ID, "bytes", n,
					"since_launch_ms", sinceLaunch.Milliseconds())
			}

			for _, w := range writers {
				if w != nil {
					_, _ = w.Write(chunk)
				}
			}
		}

		if err != nil {
			s.mu.RLock()
			total := s.bytesRead
			adopted := !s.adoptedAt.IsZero()
			s.mu.RUnlock()

			// Record total output at loop end. Zero output is the silent-session
			// signature (issue #1087), but a loop can also end at zero for benign
			// reasons — a session stopped/killed before it rendered, an adopted
			// PTY that stayed quiet after a daemon upgrade, or a command agent
			// that emits nothing — so this stays at Info (the actionable
			// diagnostic is the daemon's 20s silent-session Warn, not this).
			s.log.Info("pty read loop ended",
				"session", s.ID, "total_bytes", total, "adopted", adopted, "error", err)

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

// typeInputDelay is the built-in default pause between writing text and the
// submit key in WriteInputAndSubmit, used when SessionOpts.InputDelay is unset.
// TUI frameworks treat text+CR in a single read as a paste (inserting a newline)
// rather than "type then press Enter". Separating the writes lets the TUI drain
// the text before the CR arrives. The daemon overrides it via the [lifecycle]
// input_delay policy.
const typeInputDelay = 50 * time.Millisecond

// SetInputDelay updates the submit pause used by WriteInputAndSubmit so a live
// PTY observes a reloaded [lifecycle] input_delay on its next submit, matching
// the documented read-at-each-use contract (issue #1294). A non-positive value
// falls back to the built-in default, preserving the per-session construction
// default. It holds writeMu — the same lock WriteInputAndSubmit reads the delay
// under — so the update can't race an in-flight submit.
func (s *Session) SetInputDelay(d time.Duration) {
	if d <= 0 {
		d = typeInputDelay
	}

	s.writeMu.Lock()
	s.inputDelay = d
	s.writeMu.Unlock()
}

// InputDelay returns the submit pause WriteInputAndSubmit currently uses. It
// reads under writeMu so it never races a concurrent SetInputDelay or submit.
func (s *Session) InputDelay() time.Duration {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	return s.inputDelay
}

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

		delay := s.inputDelay
		if delay <= 0 {
			delay = typeInputDelay
		}

		time.Sleep(delay)
	}

	return s.writeInputLocked([]byte("\r"))
}

// interruptByte is the ETX control code (Ctrl-C), which TUI agents treat as an
// interrupt request.
const interruptByte = 0x03

// Interrupt sends the interrupt byte (Ctrl-C, 0x03) to the PTY count times,
// pausing delay between successive sends. Some agent TUIs (notably Claude)
// ignore a single Ctrl-C and need two rapid presses to actually interrupt, so
// the count and delay are configurable per agent (see issue #620). A count
// below 1 sends once. The whole operation holds writeMu so the presses aren't
// interleaved with other input.
func (s *Session) Interrupt(count int, delay time.Duration) error {
	if count < 1 {
		count = 1
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	for i := 0; i < count; i++ {
		if i > 0 && delay > 0 {
			time.Sleep(delay)
		}

		if err := s.writeInputLocked([]byte{interruptByte}); err != nil {
			// A press after the first can fail because the process already
			// exited — which is the interrupt working, not an error. Only the
			// very first press failing (nothing delivered) is a real failure.
			if i > 0 {
				return nil
			}

			return err
		}
	}

	return nil
}

func (s *Session) writeInputLocked(data []byte) error {
	s.mu.RLock()
	exited := s.exited
	s.mu.RUnlock()

	if exited {
		return errors.New("session process has exited")
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

// BytesRead returns the total number of PTY output bytes read this session
// lifetime. Zero on a running session that has been up for a while is the
// signature of a silent agent (issue #1087) — alive but rendering nothing.
func (s *Session) BytesRead() int64 { s.mu.RLock(); defer s.mu.RUnlock(); return s.bytesRead }

// CreatedAt returns when this PTY session object was constructed (spawned or
// adopted). Used to age a silent session for the zero-output diagnostic.
func (s *Session) CreatedAt() time.Time { s.mu.RLock(); defer s.mu.RUnlock(); return s.createdAt }

// WasAdopted reports whether this session was adopted from a prior daemon (a
// daemon upgrade re-attaching to a surviving agent) rather than freshly
// spawned. An adopted PTY starts at zero bytes read even though the agent may
// have rendered before the upgrade, so the silent-session diagnostic can't
// treat its zero-output as "never rendered" (issue #1087).
func (s *Session) WasAdopted() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.adoptedAt.IsZero() }

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
	// readDone is closed once readLoop returns, so no goroutine writes to the
	// screen after this point; closing it stops the emulator's response-drain
	// goroutine.
	_ = s.screen.Close()
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
