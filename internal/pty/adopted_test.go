package pty

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

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
	defer sb.Close()

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
	cmd.Wait()

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
	defer sb.Close()

	// Start a short-lived process to get a real PID.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid

	startTime, stErr := ProcessStartTime(pid)
	if stErr != nil {
		cmd.Process.Kill()
		cmd.Wait()
		t.Skipf("cannot get process start time: %v", stErr)
	}

	// Kill the original process.
	cmd.Process.Kill()
	cmd.Wait()

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
		cmd.Process.Kill()
		cmd.Wait()
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
	defer sb.Close()

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
