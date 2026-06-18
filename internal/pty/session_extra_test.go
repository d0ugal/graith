package pty

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	n := len(s.writers)
	s.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected 0 writers after detaching matching writer, got %d", n)
	}
}

func TestDetachWriterNonMatchingWriter(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	var writerA, writerB bytes.Buffer
	s.Attach(&writerB)
	s.DetachWriter(&writerA)

	s.mu.RLock()
	n := len(s.writers)
	s.mu.RUnlock()
	if n != 1 {
		t.Errorf("expected 1 writer after detaching non-matching writerA, got %d", n)
	}
}

func TestDetachWriterWhenNoneAttached(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	var writerA bytes.Buffer
	s.DetachWriter(&writerA)

	s.mu.RLock()
	n := len(s.writers)
	s.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected 0 writers, got %d", n)
	}
}

func TestMultipleWriters(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	var writerA, writerB bytes.Buffer
	s.Attach(&writerA)
	s.Attach(&writerB)

	s.mu.RLock()
	n := len(s.writers)
	s.mu.RUnlock()
	if n != 2 {
		t.Errorf("expected 2 writers, got %d", n)
	}

	s.DetachWriter(&writerA)
	s.mu.RLock()
	n = len(s.writers)
	s.mu.RUnlock()
	if n != 1 {
		t.Errorf("expected 1 writer after detaching writerA, got %d", n)
	}
}

func TestDetachClearsAllWriters(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	var writerA, writerB bytes.Buffer
	s.Attach(&writerA)
	s.Attach(&writerB)
	s.Detach()

	s.mu.RLock()
	n := len(s.writers)
	s.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected 0 writers after Detach, got %d", n)
	}
}

func TestMultiWriterFanOut(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")
	s, err := NewSession(SessionOpts{
		ID: "test", Command: "sh", Args: []string{"-c", "read a; echo $a; read b; echo $b; sleep 0.1"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var bufA, bufB syncBuf
	s.Attach(&bufA)
	s.Attach(&bufB)

	if err := s.WriteInput([]byte("fanout test\n")); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(5 * time.Second)
	for !bytes.Contains(bufA.Bytes(), []byte("fanout test")) || !bytes.Contains(bufB.Bytes(), []byte("fanout test")) {
		select {
		case <-deadline:
			t.Fatalf("bufA = %q, bufB = %q; both should contain 'fanout test'", bufA.Bytes(), bufB.Bytes())
		case <-time.After(10 * time.Millisecond):
		}
	}

	s.DetachWriter(&bufA)
	beforeA := len(bufA.Bytes())

	if err := s.WriteInput([]byte("after detach\n")); err != nil {
		t.Fatal(err)
	}

	deadline = time.After(5 * time.Second)
	for !bytes.Contains(bufB.Bytes(), []byte("after detach")) {
		select {
		case <-deadline:
			t.Fatalf("bufB = %q; should contain 'after detach'", bufB.Bytes())
		case <-time.After(10 * time.Millisecond):
		}
	}

	time.Sleep(50 * time.Millisecond)
	if len(bufA.Bytes()) != beforeA {
		t.Error("bufA received data after DetachWriter")
	}

	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for process exit")
	}
}

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}

func TestBuildEnvSetsTERM(t *testing.T) {
	env := envMap(buildEnv(nil))
	if got := env["TERM"]; got != "xterm-256color" {
		t.Errorf("TERM = %q, want xterm-256color", got)
	}
}

func TestBuildEnvOverridesParent(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("GRAITH_TEST_VAR", "parent")

	env := envMap(buildEnv(map[string]string{
		"GRAITH_TEST_VAR": "child",
	}))

	if got := env["TERM"]; got != "xterm-256color" {
		t.Errorf("TERM = %q, want xterm-256color (should override parent)", got)
	}
	if got := env["GRAITH_TEST_VAR"]; got != "child" {
		t.Errorf("GRAITH_TEST_VAR = %q, want child (should override parent)", got)
	}
}

func TestBuildEnvExtraOverridesTERM(t *testing.T) {
	env := envMap(buildEnv(map[string]string{
		"TERM": "screen",
	}))
	if got := env["TERM"]; got != "screen" {
		t.Errorf("TERM = %q, want screen (extra should override default)", got)
	}
}

func TestBuildEnvPreservesParentVars(t *testing.T) {
	t.Setenv("GRAITH_PASSTHROUGH", "keep-me")

	env := envMap(buildEnv(nil))
	if got := env["GRAITH_PASSTHROUGH"]; got != "keep-me" {
		t.Errorf("GRAITH_PASSTHROUGH = %q, want keep-me (parent vars should be preserved)", got)
	}
}

func TestBuildEnvNoDuplicateKeys(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("GRAITH_SESSION_ID", "parent-id")

	env := buildEnv(map[string]string{
		"TERM":              "screen",
		"GRAITH_SESSION_ID": "child-id",
	})
	for _, key := range []string{"TERM", "GRAITH_SESSION_ID"} {
		count := 0
		for _, e := range env {
			if strings.HasPrefix(e, key+"=") {
				count++
			}
		}
		if count != 1 {
			t.Errorf("found %d %s entries, want exactly 1", count, key)
		}
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
