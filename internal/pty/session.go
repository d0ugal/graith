package pty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type Session struct {
	ID         string
	Cmd        *exec.Cmd
	Ptmx       *os.File
	Scrollback *Scrollback

	mu        sync.RWMutex
	writeMu   sync.Mutex
	upgradeMu sync.Mutex
	// screenRecoveryMu admits one off-lock helper reconstruction at a time.
	// The potentially blocking factory must never hold mu, which raw PTY
	// drainage needs for each authoritative scrollback append.
	screenRecoveryMu sync.Mutex
	// upgradePause coordinates a bounded safe point in readLoop while writeMu
	// separately drains and bars input. ack closes only when no PTY read is in
	// flight and every returned byte has reached scrollback and the screen.
	upgradePause        bool
	upgradeAck          chan struct{}
	upgradeResume       chan struct{}
	upgradeInputBlocked atomic.Bool
	writers             []io.Writer
	screen              Terminal
	closed              bool
	closeOnce           sync.Once
	// screenFactory is fixed at construction and provides a deterministic seam
	// for proving recovery behavior without weakening the production selector.
	screenFactory func(cols, rows int) (Terminal, error)
	// screenHydrationBytes bounds how much persistent output is replayed when
	// the terminal helper fails. The raw scrollback remains authoritative; the
	// helper owns only reconstructable derived state.
	screenHydrationBytes int
	// screenInitializing keeps raw PTY drainage independent from potentially
	// slow derived-screen construction during upgrade adoption. While true,
	// readLoop appends authoritative bytes without starting a competing helper;
	// the completed helper is hydrated from that exact tail before publication.
	screenInitializing bool
	// scrollbackErr records that a PTY chunk could not be durably appended. A
	// preserve upgrade must refuse at the reader safe point rather than claim a
	// lossless handoff after raw output has diverged from the authoritative log.
	scrollbackErr error
	// screenRecoveryPending records parser failure while helper generation is
	// frozen for exec. Raw output remains in scrollback and is replayed if exec
	// fails and the old daemon thaws.
	screenRecoveryPending bool
	screenRecoveryCols    int
	screenRecoveryRows    int
	// screenRecoveryGeneration changes whenever raw output or requested geometry
	// changes while an off-lock recovery is in flight. A replacement may publish
	// only against the exact generation whose scrollback tail it hydrated.
	screenRecoveryGeneration uint64
	// screenRecoveryNext bounds repeated helper launches when the backend is
	// unavailable. Raw PTY bytes continue into scrollback while reconstruction
	// attempts back off per session.
	screenRecoveryNext  time.Time
	screenRecoveryDelay time.Duration
	// screenRecoveryNow is a package-test seam; production uses time.Now.
	screenRecoveryNow func() time.Time
	// setSize is a test seam for the PTY ownership window. Production uses
	// pty.Setsize when it is nil.
	setSize func(*os.File, *pty.Winsize) error
	// afterScrollbackAppend is a deterministic test seam. It runs while mu is
	// held, proving snapshots cannot interleave between durable append and
	// applying the same chunk to the derived screen.
	afterScrollbackAppend func()

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
	// inputDelay is the pause (in nanoseconds) between typed text and the submit
	// CR in WriteInputAndSubmit. Seeded at construction from SessionOpts.InputDelay
	// (or the typeInputDelay default) and updated live by SetInputDelay when the
	// [lifecycle] input_delay policy is reloaded (issue #1294). Stored atomically
	// so a reload never races an in-flight input write: WriteInputAndSubmit reads
	// it once, without the writeMu, and a concurrent SetInputDelay simply takes
	// effect on the next submit.
	inputDelay atomic.Int64
	// adoptedPollTimeout / adoptedPollInterval bound the adopted-PTY babysit loop.
	// Resolved at adoption (AdoptOpts values or the package defaults); immutable
	// after. Unused for a freshly-launched (non-adopted) session.
	adoptedPollTimeout  time.Duration
	adoptedPollInterval time.Duration
	adoptedWaitOnce     sync.Once
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
	return newSessionWithTerminalFactory(opts, newTerminal)
}

func newSessionWithTerminalFactory(
	opts SessionOpts,
	factory func(cols, rows int) (Terminal, error),
) (*Session, error) {
	if factory == nil {
		factory = newTerminal
	}

	cmd := exec.Command(opts.Command, opts.Args...)
	cmd.Dir = opts.Dir
	cmd.Env = buildEnv(opts.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	size := &pty.Winsize{Rows: opts.Rows, Cols: opts.Cols}

	// Stamp the launch instant *before* the process starts so the launch→first-
	// output and launch→active durations (issue #1104) include terminal, PTY, and
	// scrollback setup — otherwise a fast agent can emit output before the epoch
	// is recorded, under-reporting the true startup time.
	launchedAt := time.Now()

	// Reserve the derived terminal model and open persistent scrollback before
	// starting the user command. In particular, native-helper capacity failure
	// must not let a rejected command execute side effects. No fallible setup
	// remains after StartWithSize succeeds.
	screen, err := factory(int(opts.Cols), int(opts.Rows))
	if err != nil {
		return nil, fmt.Errorf("terminal screen: %w", err)
	}

	sb, err := NewScrollback(opts.LogPath, opts.MaxLogSize)
	if err != nil {
		_ = screen.Close()

		return nil, fmt.Errorf("scrollback: %w", err)
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	sb.SetLogger(log)

	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		_ = sb.Close()
		_ = screen.Close()

		return nil, fmt.Errorf("start pty: %w", err)
	}

	inputDelay := opts.InputDelay
	if inputDelay <= 0 {
		inputDelay = typeInputDelay
	}

	s := &Session{
		ID: opts.ID, Cmd: cmd, Ptmx: ptmx, Scrollback: sb,
		screen:               screen,
		screenFactory:        factory,
		screenHydrationBytes: defaultScreenHydrationBytes,
		done:                 make(chan struct{}),
		readDone:             make(chan struct{}),
		createdAt:            launchedAt,
		log:                  log,
	}
	s.inputDelay.Store(int64(inputDelay))
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
	ID           string
	Fd           uintptr
	ScrollbackFd uintptr
	PID          int
	// ExpectedPIDStartTime binds adoption to the process identity captured by
	// the old daemon. Zero is accepted only for a legacy helper-free manifest.
	ExpectedPIDStartTime int64
	LogPath              string
	MaxLogSize           int64
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
	// Logger routes this session's diagnostics to the daemon's logger. Nil
	// falls back to slog.Default().
	Logger *slog.Logger
	// DegradedScreen skips synchronous derived-screen construction while still
	// adopting the PTY and starting raw-output drainage. Later screen access or
	// output retries the ordinary backend through screenFactory.
	DegradedScreen bool
	// DeferWait leaves exact-child reaping dormant until StartAdoptedWaiter.
	// Upgrade adoption uses this to finish all rejection-prone manager checks
	// before any waiter can consume the leader status needed by cleanup.
	DeferWait bool
	// screenFactory is a deterministic package-test seam. Production leaves it
	// nil and always selects the build-tagged terminal backend.
	screenFactory func(cols, rows int) (Terminal, error)
}

func AdoptSession(opts AdoptOpts) (*Session, error) {
	ptmx := os.NewFile(opts.Fd, "ptmx-"+opts.ID)
	if ptmx == nil {
		return nil, fmt.Errorf("invalid fd %d for session %s", opts.Fd, opts.ID)
	}

	owned := true
	defer func() {
		if owned {
			_ = ptmx.Close()
		}
	}()

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	var (
		sb  *Scrollback
		err error
	)
	if opts.ScrollbackFd > 0 {
		sb, err = AdoptScrollback(opts.ScrollbackFd, opts.LogPath, opts.MaxLogSize)
	} else {
		// Legacy and package-local callers may not have a transferred writer.
		// Current daemon handoffs require one structurally before this point.
		sb, err = NewScrollback(opts.LogPath, opts.MaxLogSize)
	}

	if err != nil {
		_ = ptmx.Close()

		return nil, fmt.Errorf("adopt scrollback: %w", err)
	}

	sb.SetLogger(log)

	startTime, stErr := ProcessStartTime(opts.PID)
	if opts.ExpectedPIDStartTime > 0 && (stErr != nil || startTime != opts.ExpectedPIDStartTime) {
		drainErr := drainTransferredPTY(ptmx, sb)
		_ = ptmx.Close()
		_ = sb.Close()

		return nil, errors.Join(errors.New("adopted process identity does not match handoff"), drainErr)
	}

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

	factory := opts.screenFactory
	if factory == nil {
		factory = newTerminal
	}

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

	s := &Session{
		ID:                  opts.ID,
		Ptmx:                ptmx,
		Scrollback:          sb,
		screen:              newUnavailableTerminal(cols, rows),
		screenFactory:       factory,
		screenInitializing:  true,
		done:                make(chan struct{}),
		readDone:            make(chan struct{}),
		adoptedPID:          opts.PID,
		adoptedStartTime:    startTime,
		adoptedAt:           time.Now(),
		createdAt:           time.Now(),
		log:                 log,
		adoptedPollTimeout:  pollTimeout,
		adoptedPollInterval: pollInterval,
	}
	s.userInputCond = sync.NewCond(&sync.Mutex{})

	// HydrationBytes < 0 means "use the built-in default"; 0 disables hydration.
	hydrate := opts.HydrationBytes
	if hydrate < 0 {
		hydrate = defaultScreenHydrationBytes
	}

	s.screenHydrationBytes = hydrate
	go s.readLoop()

	if opts.DegradedScreen {
		s.mu.Lock()
		// Keep screenInitializing set until the daemon-owned post-commit recovery
		// task installs a derived screen. Raw PTY bytes continue straight into
		// scrollback without letting a slow factory block readLoop.
		s.screenRecoveryPending = true
		s.screenRecoveryCols = cols
		s.screenRecoveryRows = rows
		s.mu.Unlock()

		if !opts.DeferWait {
			s.StartAdoptedWaiter()
		}

		owned = false

		return s, nil
	}

	screen, screenErr := factory(cols, rows)

	s.mu.Lock()
	s.screenInitializing = false

	if screenErr != nil {
		log.Warn("terminal screen unavailable during adoption; preserving PTY with degraded screen",
			"session", opts.ID, "error", screenErr)
		// The first ordinary screen access gets one immediate recovery attempt.
		// Backoff begins only if that retry also fails; otherwise a transient
		// constructor failure can make the adopted screen appear blank for an
		// arbitrary scheduler-dependent interval.
	} else {
		if hydrate > 0 {
			if tail, tailErr := sb.TailBytes(int64(hydrate)); tailErr == nil && len(tail) > 0 {
				if err := writeTerminalChunks(screen, tail); err != nil {
					_ = screen.Close()

					log.Warn("terminal hydration failed during adoption; preserving PTY with empty screen",
						"session", opts.ID, "error", err)

					screen, screenErr = factory(cols, rows)
				}
			}
		}

		if screenErr == nil {
			_ = s.screen.Close()
			s.screen = screen
		} else {
			s.noteScreenRecoveryFailureLocked()
		}
	}
	s.mu.Unlock()

	if !opts.DeferWait {
		s.StartAdoptedWaiter()
	}

	owned = false

	return s, nil
}

// StartAdoptedWaiter starts the adopted process waiter exactly once.
func (s *Session) StartAdoptedWaiter() {
	s.adoptedWaitOnce.Do(func() { go s.adoptedWaitLoop() })
}

// RejectAdoption terminates an adopted exact child and waits for readLoop to
// finish its final PTY drain before closing either transferred descriptor. It
// must be called before StartAdoptedWaiter: retaining the daemon's sole waiter
// keeps the PID reserved between the identity check and process-group signal.
func (s *Session) RejectAdoption(ctx context.Context) error {
	if s.adoptedPID <= 1 || s.adoptedStartTime <= 0 {
		return errors.New("adopted process identity is unavailable")
	}

	startTime, err := ProcessStartTime(s.adoptedPID)
	if err != nil || startTime != s.adoptedStartTime {
		return errors.New("adopted process identity changed before rejection")
	}

	if err := syscall.Kill(-s.adoptedPID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return errors.New("terminate rejected adopted process group")
	}

	s.StartAdoptedWaiter()

	select {
	case <-s.Done():
		s.Close()
		return nil
	case <-ctx.Done():
		return errors.New("rejected adopted process final drain exceeded deadline")
	}
}

const maxTransferredPTYDrainBytes = 1024 * 1024

// drainTransferredPTY preserves output already queued in a transferred PTY
// when its recorded process identity is gone or reused. It never waits for new
// bytes and never signals the observed PID; the bounded raw bytes go only to
// authoritative scrollback and are never included in diagnostics.
func drainTransferredPTY(ptmx *os.File, scrollback *Scrollback) error {
	if ptmx == nil || scrollback == nil {
		return nil
	}

	fd := int(ptmx.Fd())

	flags, err := unix.FcntlInt(ptmx.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return errors.New("inspect transferred PTY for final drain")
	}

	if _, err := unix.FcntlInt(ptmx.Fd(), unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
		return errors.New("make transferred PTY final drain nonblocking")
	}

	buf := make([]byte, 32*1024)

	total := 0
	for total < maxTransferredPTYDrainBytes {
		limit := min(len(buf), maxTransferredPTYDrainBytes-total)

		n, readErr := unix.Read(fd, buf[:limit])
		if n > 0 {
			written, writeErr := scrollback.Write(buf[:n])
			if writeErr != nil || written != n {
				return errors.New("persist transferred PTY final drain")
			}

			total += n
		}

		switch {
		case readErr == nil && n > 0:
			continue
		case errors.Is(readErr, unix.EINTR):
			continue
		case readErr == nil, errors.Is(readErr, unix.EAGAIN), errors.Is(readErr, unix.EWOULDBLOCK),
			errors.Is(readErr, unix.EIO):
			return nil
		default:
			return errors.New("read transferred PTY final drain")
		}
	}

	return errors.New("transferred PTY final drain exceeded byte limit")
}

// DrainTransferredPTY performs the bounded final drain for a daemon handoff
// rejection before the exact inherited descriptors are closed.
func DrainTransferredPTY(ptmx *os.File, scrollback *Scrollback) error {
	return drainTransferredPTY(ptmx, scrollback)
}

const defaultScreenHydrationBytes = 128 * 1024

const (
	minScreenRecoveryBackoff    = 100 * time.Millisecond
	maxScreenRecoveryBackoff    = 5 * time.Second
	screenRecoveryBatchAttempts = 3
	screenRecoveryLockPoll      = 5 * time.Millisecond
)

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

// DuplicateFD returns a close-on-exec duplicate of the PTY master. The
// duplicate is owned by the caller. Close serializes the original descriptor
// close with this operation, so a successful return cannot duplicate an
// already-closed descriptor or a descriptor number reused by another goroutine.
func (s *Session) DuplicateFD() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed || s.Ptmx == nil {
		return -1, errors.New("PTY session is closed")
	}

	fd, err := unix.FcntlInt(s.Ptmx.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return -1, fmt.Errorf("duplicate PTY descriptor: %w", err)
	}

	return fd, nil
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
		if !s.upgradeReadSafePoint() {
			return
		}

		s.mu.RLock()

		if s.closed || s.Ptmx == nil {
			s.mu.RUnlock()
			return
		}

		fd := int(s.Ptmx.Fd())
		if fd < 0 || fd > math.MaxInt32 {
			s.mu.RUnlock()

			return
		}

		pollFD := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}}
		_, pollErr := unix.Poll(pollFD, 100)

		s.mu.RUnlock()

		if pollErr != nil && !errors.Is(pollErr, unix.EINTR) {
			return
		}

		if pollErr != nil || pollFD[0].Revents == 0 {
			continue
		}
		// A pause may have arrived while Poll waited. Acknowledge it before
		// consuming bytes, then re-poll after rollback.
		if !s.upgradeReadSafePoint() {
			return
		}

		s.mu.RLock()

		if s.closed || s.Ptmx == nil || int(s.Ptmx.Fd()) != fd {
			s.mu.RUnlock()
			return
		}

		n, err := s.Ptmx.Read(buf)
		s.mu.RUnlock()

		if n > 0 {
			chunk := buf[:n]

			s.mu.Lock()

			written, appendErr := s.Scrollback.Write(chunk)
			if appendErr != nil || written != len(chunk) {
				s.scrollbackErr = errors.New("scrollback append failed")
			}

			if s.afterScrollbackAppend != nil {
				s.afterScrollbackAppend()
			}

			screenErr := s.writeScreenLocked(chunk)
			s.lastOutputAt = time.Now()
			first := s.bytesRead == 0
			s.bytesRead += int64(n)
			writers := make([]io.Writer, len(s.writers))
			copy(writers, s.writers)
			s.mu.Unlock()

			if appendErr != nil || written != len(chunk) {
				s.log.Error("scrollback append failed; preserve upgrade disabled", "session", s.ID)
			}

			if screenErr != nil {
				s.log.Warn("terminal parser failed; screen reset",
					"session", s.ID, "error", screenErr)
			}

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

func (s *Session) upgradeReadSafePoint() bool {
	s.upgradeMu.Lock()
	if !s.upgradePause {
		s.upgradeMu.Unlock()
		return true
	}

	ack := s.upgradeAck
	resume := s.upgradeResume

	select {
	case <-ack:
	default:
		close(ack)
	}
	s.upgradeMu.Unlock()

	select {
	case <-resume:
		return true
	case <-s.readDone:
		return false
	}
}

// QuiesceIOForUpgrade drains input and pauses the PTY reader at a lossless safe
// point. The returned release must be called on every path where syscall.Exec
// does not replace the process image.
func (s *Session) QuiesceIOForUpgrade(ctx context.Context) (func(), error) {
	// Close input admission before waiting for the current writer. Writers use
	// TryLock and observe this flag, so none can strand behind the lock across
	// syscall.Exec.
	s.upgradeInputBlocked.Store(true)

	for !s.writeMu.TryLock() {
		select {
		case <-ctx.Done():
			s.upgradeInputBlocked.Store(false)
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}

	s.upgradeMu.Lock()
	if s.upgradePause {
		s.upgradeMu.Unlock()
		s.writeMu.Unlock()

		return nil, errors.New("session I/O is already quiesced")
	}

	s.upgradePause = true
	s.upgradeAck = make(chan struct{})
	s.upgradeResume = make(chan struct{})
	ack := s.upgradeAck
	s.upgradeMu.Unlock()

	released := false
	release := func() {
		doUnlock := false

		s.upgradeMu.Lock()
		if !released {
			released = true
			doUnlock = true
			s.upgradePause = false
			close(s.upgradeResume)
		}
		s.upgradeMu.Unlock()

		if doUnlock {
			s.writeMu.Unlock()
			s.upgradeInputBlocked.Store(false)
		}
	}

	select {
	case <-ack:
		s.mu.RLock()
		scrollbackErr := s.scrollbackErr
		s.mu.RUnlock()

		if scrollbackErr != nil {
			release()

			return nil, scrollbackErr
		}

		return release, nil
	case <-s.readDone:
		return release, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

// writeScreenLocked parses one PTY output chunk. If the helper reports a
// failure or exits, replace it immediately and reconstruct the screen from the
// bounded persistent scrollback tail. The caller has already appended chunk to
// Scrollback and must hold s.mu.
func (s *Session) writeScreenLocked(chunk []byte) error {
	if s.screenInitializing {
		s.screenRecoveryGeneration++
		return nil
	}

	n, err := s.screen.Write(chunk)
	if err == nil && n != len(chunk) {
		err = io.ErrShortWrite
	}

	if err != nil {
		recoveryErr := s.replaceScreenLocked()

		return errors.Join(err, recoveryErr)
	}

	return nil
}

// replaceScreenLocked reconstructs the derived terminal model from persistent
// scrollback. It swaps only after construction and hydration both succeed, so
// callers never lose the last model merely because replacement initialization
// failed. The caller must hold s.mu.
func (s *Session) replaceScreenLocked() error {
	cols, rows := s.screen.Size()

	return s.replaceScreenAtSizeLocked(cols, rows, false)
}

func (s *Session) replaceScreenAtSizeLocked(cols, rows int, explicitResize bool) (returnErr error) {
	if !s.screenRecoveryNext.IsZero() && s.screenRecoveryTime().Before(s.screenRecoveryNext) {
		return errTerminalUnavailable
	}

	defer func() {
		if errors.Is(returnErr, errTerminalGenerationFrozen) {
			if explicitResize || !s.screenRecoveryPending {
				s.screenRecoveryCols = cols
				s.screenRecoveryRows = rows
			}

			s.screenRecoveryPending = true
			s.screenInitializing = true
			s.screenRecoveryGeneration++

			return
		}

		if returnErr != nil {
			s.noteScreenRecoveryFailureLocked()
		} else {
			s.screenRecoveryNext = time.Time{}
			s.screenRecoveryDelay = 0
		}
	}()

	if s.closed {
		return os.ErrClosed
	}

	factory := s.screenFactory
	if factory == nil {
		factory = newTerminal
	}

	replacement, err := factory(cols, rows)
	if err != nil {
		return fmt.Errorf("create replacement terminal: %w", err)
	}

	if s.screenHydrationBytes > 0 && s.Scrollback != nil {
		tail, tailErr := s.Scrollback.TailBytes(int64(s.screenHydrationBytes))
		if tailErr != nil {
			_ = replacement.Close()

			return fmt.Errorf("read terminal recovery scrollback: %w", tailErr)
		}

		if len(tail) > 0 {
			if writeErr := writeTerminalChunks(replacement, tail); writeErr != nil {
				_ = replacement.Close()

				s.log.Warn("terminal recovery hydration failed; using empty screen",
					"session", s.ID, "error", writeErr)
				// The retained tail may contain the exact input that killed the
				// previous parser. Keep the daemon and future output usable by
				// falling back to an empty replacement rather than replaying the
				// same poison sequence indefinitely.
				replacement, err = factory(cols, rows)
				if err != nil {
					return errors.Join(
						fmt.Errorf("hydrate replacement terminal: %w", writeErr),
						fmt.Errorf("create empty replacement terminal: %w", err),
					)
				}
			}
		}
	}

	failed := s.screen
	s.screen = replacement
	s.screenRecoveryPending = false
	s.screenRecoveryCols = 0
	s.screenRecoveryRows = 0

	if failed != nil {
		_ = failed.Close()
	}

	return nil
}

func (s *Session) screenRecoveryTime() time.Time {
	if s.screenRecoveryNow != nil {
		return s.screenRecoveryNow()
	}

	return time.Now()
}

func (s *Session) noteScreenRecoveryFailureLocked() {
	delay := s.screenRecoveryDelay
	if delay <= 0 {
		delay = minScreenRecoveryBackoff
	} else {
		delay = min(delay*2, maxScreenRecoveryBackoff)
	}

	s.screenRecoveryDelay = delay
	s.screenRecoveryNext = s.screenRecoveryTime().Add(delay)
}

var errScreenRecoveryGenerationChanged = errors.New("terminal recovery input changed")

// RecoverTerminalAfterUpgrade reconstructs a screen that failed while helper
// generation was frozen. Its daemon-owned retry loop yields between bounded,
// backed-off batches so a transient helper failure cannot leave an adopted
// session permanently blank.
func (s *Session) RecoverTerminalAfterUpgrade() error {
	return s.RecoverTerminalAfterUpgradeContext(context.Background())
}

// RecoverTerminalAfterUpgradeContext is the daemon-owned, cancellation-aware
// recovery path. Slow helper requests remain outside the session mutex so raw
// PTY drainage is never coupled to reconstruction.
//
//nolint:contextcheck // terminal factories have their own bounded start/RPC deadlines; every retry and hydration stage checks ctx
func (s *Session) RecoverTerminalAfterUpgradeContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	attempts := 0

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := s.recoverTerminalAfterUpgradeAttempt(ctx)
		if err == nil {
			return nil
		}

		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}

		delay, delayErr := s.screenRecoveryRetryDelay(ctx, err)
		if delayErr != nil {
			return errors.Join(err, delayErr)
		}

		attempts++
		if attempts >= screenRecoveryBatchAttempts {
			// Yield between bounded batches, but keep this daemon-owned job alive.
			// Continuous output can invalidate any one candidate; eventual quiet
			// must still publish without requiring a client resize or snapshot.
			attempts = 0
			delay = max(delay, minScreenRecoveryBackoff)
		}

		if delay <= 0 {
			continue
		}

		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		}
	}
}

// RecoverTerminalSessionsAfterUpgrade owns a concurrent recovery generation.
// It joins every started session operation before returning, so cancellation
// cannot strand helper factories or candidate terminals in detached goroutines.
//
//nolint:contextcheck // nil retains the legacy background fallback; non-nil caller cancellation is passed unchanged to every worker
func RecoverTerminalSessionsAfterUpgrade(ctx context.Context, sessions []*Session) []error {
	if ctx == nil {
		ctx = context.Background()
	}

	results := make([]error, len(sessions))

	var recoveries sync.WaitGroup

	for i, session := range sessions {
		if err := ctx.Err(); err != nil {
			results[i] = err
			continue
		}

		if session == nil {
			continue
		}

		recoveries.Add(1)
		go func() {
			defer recoveries.Done()

			results[i] = session.RecoverTerminalAfterUpgradeContext(ctx)
		}()
	}

	recoveries.Wait()

	return results
}

func (s *Session) recoverTerminalAfterUpgradeAttempt(ctx context.Context) error {
	if err := lockWithContext(ctx, s.screenRecoveryMu.TryLock); err != nil {
		return err
	}
	defer s.screenRecoveryMu.Unlock()

	if err := s.lockScreenRecoveryState(ctx); err != nil {
		return err
	}

	if s.closed || !s.screenRecoveryPending {
		s.mu.Unlock()
		return nil
	}

	if !s.screenRecoveryNext.IsZero() && s.screenRecoveryTime().Before(s.screenRecoveryNext) {
		s.mu.Unlock()
		return errTerminalUnavailable
	}

	generation := s.screenRecoveryGeneration

	cols, rows := s.screenRecoveryCols, s.screenRecoveryRows
	if cols <= 0 || rows <= 0 {
		cols, rows = s.screen.Size()
	}

	factory := s.screenFactory
	if factory == nil {
		factory = newTerminal
	}

	hydrationBytes := s.screenHydrationBytes
	scrollback := s.Scrollback

	log := s.log
	if log == nil {
		log = slog.Default()
	}
	s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	replacement, err := factory(cols, rows)
	if err != nil {
		s.noteAsyncScreenRecoveryFailure(ctx)
		return fmt.Errorf("create replacement terminal: %w", err)
	}

	closeReplacement := func(cause error) error {
		return errors.Join(cause, replacement.Close())
	}
	if err := ctx.Err(); err != nil {
		return closeReplacement(err)
	}

	if hydrationBytes > 0 && scrollback != nil {
		tail, tailErr := scrollback.TailBytes(int64(hydrationBytes))
		if tailErr != nil {
			result := closeReplacement(fmt.Errorf("read terminal recovery scrollback: %w", tailErr))

			s.noteAsyncScreenRecoveryFailure(ctx)

			return result
		}

		if err := ctx.Err(); err != nil {
			return closeReplacement(err)
		}

		if len(tail) > 0 {
			if writeErr := writeTerminalChunksContext(ctx, replacement, tail); writeErr != nil {
				_ = replacement.Close()

				if ctx.Err() != nil {
					return errors.Join(writeErr, ctx.Err())
				}

				log.Warn("terminal recovery hydration failed; using empty screen",
					"session", s.ID, "error", writeErr)

				if err := ctx.Err(); err != nil {
					return err
				}

				replacement, err = factory(cols, rows)
				if err != nil {
					s.noteAsyncScreenRecoveryFailure(ctx)

					return errors.Join(
						fmt.Errorf("hydrate replacement terminal: %w", writeErr),
						fmt.Errorf("create empty replacement terminal: %w", err),
					)
				}

				if err := ctx.Err(); err != nil {
					return errors.Join(err, replacement.Close())
				}
			}
		}
	}

	if err := s.lockScreenRecoveryState(ctx); err != nil {
		return errors.Join(err, replacement.Close())
	}

	if s.closed || !s.screenRecoveryPending {
		s.mu.Unlock()

		_ = replacement.Close()

		return nil
	}

	if generation != s.screenRecoveryGeneration ||
		cols != s.screenRecoveryCols || rows != s.screenRecoveryRows {
		s.mu.Unlock()

		_ = replacement.Close()

		return errScreenRecoveryGenerationChanged
	}

	failed := s.screen
	s.screen = replacement
	s.screenRecoveryPending = false
	s.screenRecoveryCols = 0
	s.screenRecoveryRows = 0
	s.screenRecoveryNext = time.Time{}
	s.screenRecoveryDelay = 0
	s.screenInitializing = false
	s.mu.Unlock()

	if failed != nil {
		_ = failed.Close()
	}

	return nil
}

func (s *Session) screenRecoveryRetryDelay(ctx context.Context, recoveryErr error) (time.Duration, error) {
	if errors.Is(recoveryErr, errScreenRecoveryGenerationChanged) {
		return 0, nil
	}

	if err := s.lockScreenRecoveryState(ctx); err != nil {
		return 0, err
	}
	defer s.mu.Unlock()

	if s.closed || !s.screenRecoveryPending {
		return 0, nil
	}

	now := s.screenRecoveryTime()
	if s.screenRecoveryNext.After(now) {
		return s.screenRecoveryNext.Sub(now), nil
	}

	return 0, nil
}

func (s *Session) noteAsyncScreenRecoveryFailure(ctx context.Context) {
	if s.lockScreenRecoveryState(ctx) != nil {
		return
	}

	if !s.closed && s.screenRecoveryPending {
		s.noteScreenRecoveryFailureLocked()
	}
	s.mu.Unlock()
}

func (s *Session) lockScreenRecoveryState(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// An ordinary writer lock participates in RWMutex fairness; polling TryLock
	// can starve forever behind readLoop's short recurring poll read locks.
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}

	return nil
}

func lockWithContext(ctx context.Context, tryLock func() bool) error {
	for {
		if tryLock() {
			return nil
		}

		timer := time.NewTimer(screenRecoveryLockPoll)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

func writeTerminalChunksContext(ctx context.Context, term Terminal, p []byte) error {
	for len(p) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}

		chunk := p[:min(len(p), terminalWriteChunkBytes)]

		n, err := term.Write(chunk)
		if err != nil {
			return err
		}

		if n != len(chunk) {
			return io.ErrShortWrite
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		p = p[n:]
	}

	return nil
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
		rss := int64(ru.Maxrss) //nolint:unconvert // Maxrss is int32 on Linux/386 and int64 on Darwin/amd64.
		if runtime.GOOS != "darwin" {
			rss *= 1024 // Linux reports KB; macOS already reports bytes.
		}

		return rss
	}

	return 0
}

func (s *Session) WriteInput(data []byte) error {
	if err := s.lockInputWriter(); err != nil {
		return err
	}
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

// WriteInputAndSubmit writes text followed by a carriage return, with a brief
// pause between the two so that TUI frameworks treat them as separate events.
// The entire operation holds writeMu to prevent interleaving from other sources.
func (s *Session) WriteInputAndSubmit(data []byte) error {
	if err := s.lockInputWriter(); err != nil {
		return err
	}
	defer s.writeMu.Unlock()

	if len(data) > 0 {
		if err := s.writeInputLocked(data); err != nil {
			return err
		}

		delay := time.Duration(s.inputDelay.Load())
		if delay <= 0 {
			delay = typeInputDelay
		}

		time.Sleep(delay)
	}

	return s.writeInputLocked([]byte("\r"))
}

// SetInputDelay updates the pause WriteInputAndSubmit inserts between the typed
// text and the submit carriage return, so a reloaded [lifecycle] input_delay
// policy takes effect on a live session's next type operation without a restart
// (issue #1294). A non-positive delay restores the built-in typeInputDelay
// default, mirroring construction. The store is atomic and takes no lock, so it
// never blocks on (or races) an in-flight WriteInputAndSubmit: a write already
// past its Load runs to completion with the value it read, and the next submit
// observes the new delay.
func (s *Session) SetInputDelay(delay time.Duration) {
	if delay <= 0 {
		delay = typeInputDelay
	}

	s.inputDelay.Store(int64(delay))
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

	if err := s.lockInputWriter(); err != nil {
		return err
	}
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

var errSessionIOQuiesced = errors.New("session input is temporarily unavailable during daemon upgrade")

func (s *Session) lockInputWriter() error {
	for !s.writeMu.TryLock() {
		if s.upgradeInputBlocked.Load() {
			return errSessionIOQuiesced
		}

		time.Sleep(time.Millisecond)
	}
	if s.upgradeInputBlocked.Load() {
		s.writeMu.Unlock()
		return errSessionIOQuiesced
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
	return s.WaitForUserIdleContext(context.Background(), idleTimeout, maxWait)
}

// WaitForUserIdleContext is the daemon-owned variant used by notification
// tasks. Cancellation wakes the condition wait so an upgrade/shutdown can join
// the complete task tree rather than waiting for the two-minute idle bound.
func (s *Session) WaitForUserIdleContext(ctx context.Context, idleTimeout, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)

	stopCancelWake := context.AfterFunc(ctx, func() {
		s.userInputCond.L.Lock()
		s.userInputCond.Broadcast()
		s.userInputCond.L.Unlock()
	})
	defer stopCancelWake()

	s.userInputCond.L.Lock()
	defer s.userInputCond.L.Unlock()

	for {
		if ctx.Err() != nil {
			return false
		}

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
	defer s.mu.Unlock()

	if s.closed {
		return os.ErrClosed
	}

	var screenErr error

	if s.screenInitializing {
		// Recovery owns derived-screen construction. Resize only advances the
		// desired generation and the kernel PTY geometry while initialization is
		// in flight; it must never invoke a competing factory under s.mu.
		s.screenRecoveryPending = true
		s.screenRecoveryCols = int(cols)
		s.screenRecoveryRows = int(rows)
		s.screenRecoveryGeneration++
	} else {
		screenErr = s.screen.Resize(int(cols), int(rows))
		if screenErr != nil {
			screenErr = errors.Join(screenErr, s.replaceScreenAtSizeLocked(int(cols), int(rows), true))
		}
	}

	setSize := s.setSize
	if setSize == nil {
		setSize = pty.Setsize
	}

	ptyErr := setSize(s.Ptmx, &pty.Winsize{Rows: rows, Cols: cols})

	return errors.Join(screenErr, ptyErr)
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
	s.closeOnce.Do(func() {
		s.mu.Lock()

		s.closed = true
		if s.Ptmx != nil {
			_ = s.Ptmx.Close()
		}
		s.mu.Unlock()

		if s.readDone != nil {
			<-s.readDone
		}

		// Do not hold s.mu while waiting for readLoop: it may need the mutex to
		// finish its last chunk. Once readDone closes, take the same serialization
		// lock used by preview, snapshot, resize, and screen writes before teardown.
		s.mu.Lock()
		if s.screen != nil {
			_ = s.screen.Close()
		}
		s.mu.Unlock()

		if s.Scrollback != nil {
			_ = s.Scrollback.Close()
		}
	})
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
