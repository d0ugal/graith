//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestDegradedAdoptionDoesNotEnterBlockedScreenFactory(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	reaped := false

	t.Cleanup(func() {
		if !reaped {
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

	factoryEntered := make(chan struct{})
	releaseFactory := make(chan struct{})

	var factoryOnce sync.Once

	logPath := filepath.Join(t.TempDir(), "dreich-blocked-factory.log")

	session, err := AdoptSession(AdoptOpts{
		ID: "dreich-blocked-factory", Fd: uintptr(fd), PID: cmd.Process.Pid,
		ExpectedPIDStartTime: startTime,
		LogPath:              logPath,
		HydrationBytes:       0,
		DegradedScreen:       true,
		DeferWait:            true,
		screenFactory: func(cols, rows int) (Terminal, error) {
			factoryOnce.Do(func() { close(factoryEntered) })
			<-releaseFactory

			return &terminalChunkRecorder{}, nil
		},
	})
	if err != nil {
		_ = writeEnd.Close()

		t.Fatal(err)
	}

	t.Cleanup(session.Close)

	select {
	case <-factoryEntered:
		t.Fatal("raw-first adoption entered the blocked screen factory")
	case <-time.After(100 * time.Millisecond):
	}

	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- session.RecoverTerminalAfterUpgrade() }()

	select {
	case <-factoryEntered:
	case <-time.After(time.Second):
		t.Fatal("owned recovery did not enter the screen factory")
	}

	marker := []byte("canny raw bytes during blocked recovery")
	if _, err := writeEnd.Write(marker); err != nil {
		t.Fatal(err)
	}

	drainDeadline := time.Now().Add(time.Second)

	for {
		data, readErr := os.ReadFile(logPath)
		if readErr == nil && bytes.Contains(data, marker) {
			break
		}

		if time.Now().After(drainDeadline) {
			t.Fatalf("blocked screen recovery stalled raw PTY drainage: %q, err=%v", data, readErr)
		}

		time.Sleep(10 * time.Millisecond)
	}

	select {
	case err := <-recoveryDone:
		t.Fatalf("blocked recovery returned early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseFactory)

	if err := <-recoveryDone; err != nil {
		t.Fatal(err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	_ = writeEnd.Close()

	session.StartAdoptedWaiter()

	select {
	case <-session.Done():
		reaped = true
	case <-time.After(5 * time.Second):
		t.Fatal("adopted waiter did not reap after recovery")
	}
}

func TestAdoptSessionDefersExactWaiterUntilPublished(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	reaped := false

	t.Cleanup(func() {
		if !reaped {
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

	s, err := AdoptSession(AdoptOpts{
		ID: "canny-deferred-wait", Fd: uintptr(fd), PID: cmd.Process.Pid,
		ExpectedPIDStartTime: startTime,
		LogPath:              filepath.Join(t.TempDir(), "canny-deferred-wait.log"),
		HydrationBytes:       0,
		PollInterval:         10 * time.Millisecond,
		DegradedScreen:       true,
		DeferWait:            true,
	})
	if err != nil {
		_ = writeEnd.Close()

		t.Fatal(err)
	}

	t.Cleanup(s.Close)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-s.Done():
		t.Fatal("deferred waiter reaped before manager publication")
	case <-time.After(50 * time.Millisecond):
	}

	s.StartAdoptedWaiter()

	select {
	case <-s.Done():
		reaped = true
	case <-time.After(5 * time.Second):
		t.Fatal("adopted waiter did not reap after publication")
	}
}

func TestAdoptedWaitLoopExitsOnProcessDeath(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	pid := cmd.Process.Pid

	logPath := filepath.Join(t.TempDir(), "test.log")

	sb, err := NewScrollback(logPath, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	startTime, _ := ProcessStartTime(pid)

	s := &Session{
		ID:               "braw-adopted",
		Scrollback:       sb,
		done:             make(chan struct{}),
		readDone:         make(chan struct{}),
		adoptedPID:       pid,
		adoptedStartTime: startTime,
	}
	// readDone must close for adoptedWaitLoop to complete.
	close(s.readDone)

	go s.adoptedWaitLoop()

	// Kill the process and verify the loop terminates.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}

	_ = cmd.Wait()

	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("adoptedWaitLoop did not exit after process was killed")
	}

	if !s.Exited() {
		t.Error("expected session to be marked as exited")
	}
}

func TestAdoptedWaitLoopExitsWhenPIDGone(t *testing.T) {
	t.Parallel()
	logPath := filepath.Join(t.TempDir(), "test.log")

	sb, err := NewScrollback(logPath, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	// Start a short-lived process to get a real PID.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	pid := cmd.Process.Pid

	startTime, stErr := ProcessStartTime(pid)
	if stErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()

		t.Skipf("cannot get process start time: %v", stErr)
	}

	// Kill the original process.
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	// Simulate PID reuse: adoptedStartTime is from the original process,
	// but the PID no longer exists (kill(pid,0) will fail), so the loop
	// should exit immediately. This also covers the case where a new
	// process reuses the PID but has a different start time.
	s := &Session{
		ID:               "auld-reuse",
		Scrollback:       sb,
		done:             make(chan struct{}),
		readDone:         make(chan struct{}),
		adoptedPID:       pid,
		adoptedStartTime: startTime,
	}
	close(s.readDone)

	go s.adoptedWaitLoop()

	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("adoptedWaitLoop did not exit when PID no longer exists")
	}

	if !s.Exited() {
		t.Error("expected session to be marked as exited")
	}
}

func TestAdoptedWaitLoopStartTimeMismatchBreaks(t *testing.T) {
	t.Parallel()

	// Start a long-lived process.
	cmd := exec.Command("sleep", "120")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	pid := cmd.Process.Pid

	_, stErr := ProcessStartTime(pid)
	if stErr != nil {
		t.Skipf("cannot get process start time: %v", stErr)
	}

	logPath := filepath.Join(t.TempDir(), "test.log")

	sb, err := NewScrollback(logPath, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	// Use a fake start time that doesn't match the real process. This
	// simulates PID reuse: the PID is alive (kill returns nil) but the
	// start time differs, so the loop should detect the mismatch and exit.
	s := &Session{
		ID:               "thrawn-mismatch",
		Scrollback:       sb,
		done:             make(chan struct{}),
		readDone:         make(chan struct{}),
		adoptedPID:       pid,
		adoptedStartTime: 1, // will never match a real start time
	}
	close(s.readDone)

	go s.adoptedWaitLoop()

	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("adoptedWaitLoop did not exit on start time mismatch")
	}

	if !s.Exited() {
		t.Error("expected session to be marked as exited")
	}
}
