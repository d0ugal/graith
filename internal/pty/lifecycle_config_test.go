//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	creackpty "github.com/creack/pty"
)

// TestSessionOptsInputDelayHonoured proves SessionOpts.InputDelay drives the
// pause WriteInputAndSubmit inserts between the typed text and the submit CR
// (issue #1243). A large configured delay must dominate the elapsed time.
func TestSessionOptsInputDelayHonoured(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")

	s, err := NewSession(SessionOpts{
		ID: "braw", Command: "sh", Args: []string{"-c", "sleep 30"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
		InputDelay: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	start := time.Now()

	if err := s.WriteInputAndSubmit([]byte("bonnie")); err != nil {
		t.Fatal(err)
	}

	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("WriteInputAndSubmit took %v, want >= ~300ms (configured InputDelay not applied)", elapsed)
	}
}

// TestSetInputDelayUpdatesLiveSubmit proves SetInputDelay changes the pause a
// live session applies on its next WriteInputAndSubmit, so a reloaded
// [lifecycle] input_delay takes effect without a restart (issue #1294). The
// session starts with a tiny delay; after the setter raises it, the next submit
// must be dominated by the new, larger delay.
func TestSetInputDelayUpdatesLiveSubmit(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")

	s, err := NewSession(SessionOpts{
		ID: "thrawn", Command: "sh", Args: []string{"-c", "sleep 30"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
		InputDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Baseline: the tiny construction-time delay must not dominate.
	start := time.Now()

	if err := s.WriteInputAndSubmit([]byte("croft")); err != nil {
		t.Fatal(err)
	}

	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("baseline submit took %v with a 1ms delay, want well under 150ms", elapsed)
	}

	s.SetInputDelay(300 * time.Millisecond)

	start = time.Now()

	if err := s.WriteInputAndSubmit([]byte("bothy")); err != nil {
		t.Fatal(err)
	}

	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("submit after SetInputDelay took %v, want >= ~300ms (updated delay not applied)", elapsed)
	}
}

// TestSetInputDelayNonPositiveRestoresDefault proves a non-positive delay resets
// the pause to the built-in typeInputDelay default, mirroring construction so a
// reload that clears the policy can't leave a live session with a stale delay.
func TestSetInputDelayNonPositiveRestoresDefault(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")

	s, err := NewSession(SessionOpts{
		ID: "strath", Command: "sh", Args: []string{"-c", "sleep 30"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
		InputDelay: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.SetInputDelay(0)

	if got := time.Duration(s.inputDelay.Load()); got != typeInputDelay {
		t.Fatalf("inputDelay after SetInputDelay(0) = %v, want the typeInputDelay default %v", got, typeInputDelay)
	}
}

// TestSessionOptsInputDelayDefault proves an unset (zero) InputDelay falls back
// to the built-in typeInputDelay rather than writing text and CR back to back.
func TestSessionOptsInputDelayDefault(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")

	s, err := NewSession(SessionOpts{
		ID: "canny", Command: "sh", Args: []string{"-c", "sleep 30"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
		// InputDelay unset.
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if got := time.Duration(s.inputDelay.Load()); got != typeInputDelay {
		t.Fatalf("inputDelay = %v, want the typeInputDelay default %v", got, typeInputDelay)
	}
}

// TestAdoptOptsHydrationDisabled proves AdoptOpts.HydrationBytes == 0 skips
// replaying the scrollback tail into the adopted session's screen, while a
// positive value hydrates it (issue #1243).
func TestAdoptOptsHydration(t *testing.T) {
	seed := []byte("dreich scrollback tail content\r\n")

	// adoptedPreview seeds a scrollback file, adopts a stand-in fd with the given
	// hydration size, and returns the screen preview. Hydration happens
	// synchronously inside AdoptSession, so the write end of the stand-in pipe is
	// closed immediately to let the read loop reach EOF (otherwise Close would
	// block waiting on it).
	adoptedPreview := func(t *testing.T, hydrate int) string {
		t.Helper()

		logPath := filepath.Join(t.TempDir(), "scrollback.log")

		sb, err := NewScrollback(logPath, 1024*1024)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := sb.Write(seed); err != nil {
			t.Fatal(err)
		}

		_ = sb.Close()

		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		// A pipe fd stands in for the ptmx; GetsizeFull fails on it, exercising the
		// default-geometry fallback path too.
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}

		s, err := AdoptSession(AdoptOpts{
			ID: "adopt", Fd: r.Fd(), PID: cmd.Process.Pid, LogPath: logPath,
			MaxLogSize: 1024 * 1024, HydrationBytes: hydrate,
			PollInterval: 10 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}

		preview := strings.TrimSpace(s.ScreenPreview())

		// Unblock the read loop, reap the process, and tear the session down.
		_ = w.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()

		select {
		case <-s.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("adopted session did not exit after teardown")
		}

		s.Close()
		_ = r.Close()

		return preview
	}

	t.Run("disabled leaves screen empty", func(t *testing.T) {
		if got := adoptedPreview(t, 0); got != "" {
			t.Errorf("screen preview = %q, want empty (hydration disabled)", got)
		}
	})

	t.Run("enabled replays the tail", func(t *testing.T) {
		if got := adoptedPreview(t, 128*1024); !strings.Contains(got, "dreich") {
			t.Errorf("screen preview = %q, want the hydrated scrollback tail", got)
		}
	})
}

func TestAdoptSessionPreservesPTYAfterTerminalHydrationFailure(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "scrollback.log")
	if err := os.WriteFile(logPath, terminalParserPanicFixture(t), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sleep", "30")

	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	transferredFD, err := syscall.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	// Make transferredFD the pipe's sole read endpoint. Native helper teardown
	// may reuse its descriptor number, so Fstat(transferredFD) cannot prove that
	// AdoptSession released ownership. An EPIPE write after teardown can.
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := AdoptSession(AdoptOpts{
		ID: "thrawn-adopt", Fd: uintptr(transferredFD), PID: cmd.Process.Pid, LogPath: logPath,
		MaxLogSize: 1024 * 1024, DefaultRows: 24, DefaultCols: 80,
		HydrationBytes: 128 * 1024,
	})
	if err != nil {
		t.Fatalf("AdoptSession rejected a live PTY for derived-screen failure: %v", err)
	}

	if s == nil {
		t.Fatal("AdoptSession returned nil session")
	}

	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("adopted process was not preserved: %v", err)
	}

	if _, err := w.Write([]byte("canny live output\n")); err != nil {
		t.Fatalf("transferred PTY was not serviceable after hydration failure: %v", err)
	}

	s.Close()

	if _, err := w.Write([]byte("canny ownership probe")); !errors.Is(err, syscall.EPIPE) {
		t.Errorf("write after adopted endpoint teardown = %v, want EPIPE", err)
	}
}

func TestAdoptSessionDrainsRawPTYBeforeScreenFactoryReturns(t *testing.T) {
	cmd := exec.Command("sleep", "30")

	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	cleanupProcess := true

	t.Cleanup(func() {
		if cleanupProcess {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	startTime, err := ProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	fd, err := syscall.Dup(int(readEnd.Fd()))
	_ = readEnd.Close()

	if err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "canny-prompt-drain.log")
	factoryStarted := make(chan struct{})
	releaseFactory := make(chan struct{})

	type result struct {
		session *Session
		err     error
	}

	resultCh := make(chan result, 1)

	go func() {
		session, adoptErr := AdoptSession(AdoptOpts{
			ID: "canny-prompt-drain", Fd: uintptr(fd), PID: cmd.Process.Pid,
			ExpectedPIDStartTime: startTime,
			LogPath:              logPath,
			HydrationBytes:       1024 * 1024,
			screenFactory: func(cols, rows int) (Terminal, error) {
				close(factoryStarted)
				<-releaseFactory

				return &terminalChunkRecorder{}, nil
			},
		})
		resultCh <- result{session: session, err: adoptErr}
	}()

	select {
	case <-factoryStarted:
	case <-time.After(time.Second):
		t.Fatal("screen factory did not start")
	}

	marker := []byte("dreich prompt bytes while helper starts")
	if _, err := writeEnd.Write(marker); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)

	for {
		data, readErr := os.ReadFile(logPath)
		if readErr == nil && bytes.Contains(data, marker) {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("raw PTY reader did not drain before helper return: %q, err=%v", data, readErr)
		}

		time.Sleep(10 * time.Millisecond)
	}

	close(releaseFactory)

	adopted := <-resultCh
	if adopted.err != nil {
		t.Fatal(adopted.err)
	}

	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}

	_ = adopted.session.ForceKill()
	select {
	case <-adopted.session.Done():
		cleanupProcess = false
	case <-time.After(5 * time.Second):
		t.Fatal("adopted session did not stop")
	}

	adopted.session.Close()
}

func TestAdoptSessionOneShotFactoryFailureReplaysRetainedScreen(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "dreich-replay.log")

	want := "canny retained screen"
	if err := os.WriteFile(logPath, []byte("\x1b[2J\x1b[H"+want), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = w.Close() })

	fd, err := syscall.Dup(int(r.Fd()))
	_ = r.Close()

	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32

	s, err := AdoptSession(AdoptOpts{
		ID: "canny-replay", Fd: uintptr(fd), PID: cmd.Process.Pid,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
		DefaultRows: 24, DefaultCols: 80, HydrationBytes: 128 * 1024,
		screenFactory: func(cols, rows int) (Terminal, error) {
			if calls.Add(1) == 1 {
				return nil, errors.New("injected one-shot helper failure")
			}

			return newTerminal(cols, rows)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(s.Close)

	if got := s.ScreenPreview(); !strings.Contains(got, want) {
		t.Fatalf("recovered preview = %q, want retained screen", got)
	}

	if got := calls.Load(); got != 2 {
		t.Fatalf("factory calls = %d, want failed construction plus hydrated replacement", got)
	}

	s.setSize = func(*os.File, *creackpty.Winsize) error { return nil }
	if err := s.Resize(25, 81); err != nil {
		t.Fatalf("resize recovered screen: %v", err)
	}

	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("recovery lost adopted process: %v", err)
	}

	if _, err := w.Write([]byte("braw live output\n")); err != nil {
		t.Fatalf("recovery lost adopted descriptor: %v", err)
	}
}

func TestDegradedScreenRecoveryBackoffPreservesAndReplaysRawOutput(t *testing.T) {
	sb, err := NewScrollback(filepath.Join(t.TempDir(), "thrawn-backoff.log"), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = sb.Close() })

	now := time.Unix(1_700_000_000, 0)
	available := false
	attempts := 0
	s := &Session{
		ID:                   "thrawn-backoff",
		Scrollback:           sb,
		screen:               newUnavailableTerminal(80, 24),
		screenHydrationBytes: 1024 * 1024,
		screenRecoveryNow:    func() time.Time { return now },
		log:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
		screenFactory: func(cols, rows int) (Terminal, error) {
			attempts++

			if !available {
				return nil, errors.New("injected persistent helper failure")
			}

			return newTerminal(cols, rows)
		},
	}
	t.Cleanup(s.Close)

	if got := s.ScreenPreview(); got != "" {
		t.Fatalf("preview during outage = %q, want empty", got)
	}

	if attempts != 1 {
		t.Fatalf("initial attempts = %d, want 1", attempts)
	}

	var raw bytes.Buffer

	for i := 0; i < 128; i++ {
		chunk := []byte(fmt.Sprintf("canny-%03d\n", i))
		raw.Write(chunk)

		if _, err := sb.Write(chunk); err != nil {
			t.Fatal(err)
		}

		s.mu.Lock()
		_ = s.writeScreenLocked(chunk)
		s.mu.Unlock()
		_ = s.ScreenPreview()
	}

	if attempts != 1 {
		t.Fatalf("attempts during recovery backoff = %d, want 1", attempts)
	}

	tail, err := sb.TailBytes(int64(raw.Len()))
	if err != nil || !bytes.Equal(tail, raw.Bytes()) {
		t.Fatalf("raw scrollback during outage was not preserved: len=%d err=%v", len(tail), err)
	}

	available = true
	now = now.Add(minScreenRecoveryBackoff)
	preview := s.ScreenPreview()

	if attempts != 2 {
		t.Fatalf("attempts after backoff = %d, want one successful retry", attempts)
	}

	if !strings.Contains(preview, "canny-127") {
		t.Fatalf("recovered preview did not hydrate retained output: %q", preview)
	}
}

func TestAdoptSessionKthScreenFailurePreservesAllPTYsAndRecovers(t *testing.T) {
	const sessionCount = 3

	var (
		factoryCalls     atomic.Int32
		backendAvailable atomic.Bool
	)
	backendAvailable.Store(true)

	factory := func(cols, rows int) (Terminal, error) {
		call := factoryCalls.Add(1)
		if call == 2 || !backendAvailable.Load() {
			return nil, errors.New("injected terminal construction failure")
		}

		return newTerminal(cols, rows)
	}

	type adoptedFixture struct {
		session *Session
		writer  *os.File
		cmd     *exec.Cmd
	}

	fixtures := make([]adoptedFixture, 0, sessionCount)
	for i := range sessionCount {
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}

		fd, err := syscall.Dup(int(r.Fd()))
		_ = r.Close()

		if err != nil {
			t.Fatal(err)
		}

		session, err := AdoptSession(AdoptOpts{
			ID: fmt.Sprintf("canny-%d", i), Fd: uintptr(fd), PID: cmd.Process.Pid,
			LogPath:     filepath.Join(t.TempDir(), fmt.Sprintf("canny-%d.log", i)),
			DefaultRows: 24, DefaultCols: 80, HydrationBytes: 0,
			screenFactory: factory,
		})
		if err != nil {
			t.Fatalf("adopt session %d: %v", i, err)
		}

		fixtures = append(fixtures, adoptedFixture{session: session, writer: w, cmd: cmd})
	}

	t.Cleanup(func() {
		for _, fixture := range fixtures {
			fixture.session.Close()
			_ = fixture.writer.Close()
			_ = fixture.cmd.Process.Kill()
			_ = fixture.cmd.Wait()
		}
	})

	backendAvailable.Store(false)

	if _, err := fixtures[1].writer.Write([]byte("canny raw recovery marker\n")); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)
	backendAvailable.Store(true)

	if _, err := fixtures[1].writer.Write([]byte("\x1b[2J\x1b[Hdreich recovery trigger")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = fixtures[1].session.ScreenPreview()
		fixtures[1].session.mu.RLock()
		_, degraded := fixtures[1].session.screen.(*unavailableTerminal)
		fixtures[1].session.mu.RUnlock()

		if !degraded {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	fixtures[1].session.mu.RLock()
	_, degraded := fixtures[1].session.screen.(*unavailableTerminal)
	fixtures[1].session.mu.RUnlock()

	if degraded {
		t.Fatal("degraded screen did not recover when the factory became available")
	}

	tail, err := fixtures[1].session.Scrollback.TailBytes(1024)
	if err != nil || !strings.Contains(string(tail), "canny raw recovery marker") ||
		!strings.Contains(string(tail), "dreich recovery trigger") {
		t.Fatalf("authoritative raw scrollback was not preserved: %q, err=%v", tail, err)
	}

	for i, fixture := range fixtures {
		if err := fixture.cmd.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("session %d process was lost: %v", i, err)
		}
	}
}

func TestAdoptSessionRejectsProcessIdentityAndClosesOwnedDescriptor(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer closePTYTestResource(t, writeEnd)

	ownedFD, err := syscall.Dup(int(readEnd.Fd()))
	if err != nil {
		t.Fatal(err)
	}

	if err := readEnd.Close(); err != nil {
		t.Fatal(err)
	}

	startTime, err := ProcessStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}

	_, err = AdoptSession(AdoptOpts{
		ID: "thrawn", Fd: uintptr(ownedFD), PID: os.Getpid(),
		ExpectedPIDStartTime: startTime + 1,
		LogPath:              filepath.Join(t.TempDir(), "thrawn.log"),
	})
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("AdoptSession error = %v, want identity rejection", err)
	}

	if _, err := writeEnd.Write([]byte("canny")); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("write error = %v, want EPIPE proving adopted descriptor closed", err)
	}
}

func TestAdoptSessionClosesTransferredFDWhenScrollbackOpenFails(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	transferredFD, err := syscall.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}

	session, err := AdoptSession(AdoptOpts{
		ID: "dreich-scrollback", Fd: uintptr(transferredFD), PID: 4242,
		LogPath:    filepath.Join(t.TempDir(), "missing", "scrollback.log"),
		MaxLogSize: 1024,
	})
	if err == nil || session != nil {
		t.Fatalf("AdoptSession = (%v, %v), want scrollback error and nil session", session, err)
	}

	var stat syscall.Stat_t
	if err := syscall.Fstat(transferredFD, &stat); !errors.Is(err, syscall.EBADF) {
		t.Errorf("transferred PTY fd remains usable after scrollback failure: %v", err)
	}
}

func TestAdoptSessionClosesTransferredDescriptorsWhenScrollbackFDIsInvalid(t *testing.T) {
	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer closePTYTestResource(t, ptyWrite)

	ptyFD, err := syscall.Dup(int(ptyRead.Fd()))
	if err != nil {
		t.Fatal(err)
	}

	closePTYTestResource(t, ptyRead)

	scrollbackRead, scrollbackWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer closePTYTestResource(t, scrollbackWrite)

	scrollbackFD, err := syscall.Dup(int(scrollbackRead.Fd()))
	if err != nil {
		t.Fatal(err)
	}

	closePTYTestResource(t, scrollbackRead)

	session, err := AdoptSession(AdoptOpts{
		ID: "canny-invalid-scrollback", Fd: uintptr(ptyFD), ScrollbackFd: uintptr(scrollbackFD), PID: 4242,
		LogPath: filepath.Join(t.TempDir(), "canny-invalid-scrollback.log"),
	})
	if err == nil || session != nil {
		t.Fatalf("AdoptSession = (%v, %v), want transferred scrollback error", session, err)
	}

	for name, fd := range map[string]int{"PTY": ptyFD, "scrollback": scrollbackFD} {
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); !errors.Is(err, syscall.EBADF) {
			t.Errorf("transferred %s fd remains usable after scrollback rejection: %v", name, err)
		}
	}
}
