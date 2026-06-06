package pty

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProcessPIDWithCmd(t *testing.T) {
	cmd := &exec.Cmd{}
	cmd.Process = &os.Process{Pid: 99}
	s := &Session{Cmd: cmd, done: make(chan struct{})}
	if got := s.ProcessPID(); got != 99 {
		t.Errorf("ProcessPID() = %d, want 99", got)
	}
}

func TestProcessPIDWithAdoptedPID(t *testing.T) {
	s := &Session{adoptedPID: 42, done: make(chan struct{})}
	if got := s.ProcessPID(); got != 42 {
		t.Errorf("ProcessPID() = %d, want 42", got)
	}
}

func TestProcessPIDWithNeither(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	if got := s.ProcessPID(); got != 0 {
		t.Errorf("ProcessPID() = %d, want 0", got)
	}
}

func TestProcessPIDCmdTakesPrecedence(t *testing.T) {
	// When both Cmd.Process and adoptedPID are set, Cmd.Process.Pid wins.
	cmd := &exec.Cmd{}
	cmd.Process = &os.Process{Pid: 77}
	s := &Session{Cmd: cmd, adoptedPID: 42, done: make(chan struct{})}
	if got := s.ProcessPID(); got != 77 {
		t.Errorf("ProcessPID() = %d, want 77 (Cmd should take precedence)", got)
	}
}

func TestDetachWriterMatchingWriter(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	var writerA bytes.Buffer
	s.Attach(&writerA)
	s.DetachWriter(&writerA)

	s.mu.RLock()
	w := s.attachedWriter
	s.mu.RUnlock()
	if w != nil {
		t.Error("expected attachedWriter to be nil after detaching matching writer")
	}
}

func TestDetachWriterNonMatchingWriter(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	var writerA, writerB bytes.Buffer
	s.Attach(&writerB)
	s.DetachWriter(&writerA) // detach A, but B is attached

	s.mu.RLock()
	w := s.attachedWriter
	s.mu.RUnlock()
	if w != &writerB {
		t.Error("expected attachedWriter to remain as writerB after detaching non-matching writerA")
	}
}

func TestDetachWriterWhenNoneAttached(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	var writerA bytes.Buffer
	// No writer attached; DetachWriter should be a no-op without panic.
	s.DetachWriter(&writerA)

	s.mu.RLock()
	w := s.attachedWriter
	s.mu.RUnlock()
	if w != nil {
		t.Error("expected attachedWriter to remain nil")
	}
}

func TestScrollbackRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remove-test.log")
	sb, err := NewScrollback(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	sb.Write([]byte("some data to be removed"))

	// Verify the file exists before removal.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("scrollback file should exist before removal")
	}

	if err := sb.Remove(); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}

	// Verify the file is gone after removal.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("scrollback file should not exist after Remove()")
	}
}
