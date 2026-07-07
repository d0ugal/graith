package pty

import (
	"bytes"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.buf.Write(p)
}

func (s *syncBuf) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]byte(nil), s.buf.Bytes()...)
}

func TestSessionEcho(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")
	// Use sh -c with a brief sleep so the PTY slave stays open long enough
	// for the master to read the output. On macOS, bare "echo" can exit
	// before the master-side read drains the buffer, causing EIO and lost data.
	s, err := NewSession(SessionOpts{
		ID: "braw", Command: "sh", Args: []string{"-c", "echo braw graith; sleep 0.1"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for process exit")
	}

	if !s.Exited() {
		t.Error("expected process to have exited")
	}

	if s.ExitCode() != 0 {
		t.Errorf("exit code = %d, want 0", s.ExitCode())
	}

	tail, err := s.Scrollback.Tail(0)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(tail, []byte("braw graith")) {
		t.Errorf("scrollback = %q, want to contain 'braw graith'", tail)
	}
}

func TestSessionInterrupt(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")

	// A bare `sleep` runs as the foreground process on the PTY, so the interrupt
	// byte (0x03) is turned into SIGINT by the line discipline and kills it.
	s, err := NewSession(SessionOpts{
		ID: "braw", Command: "sh", Args: []string{"-c", "sleep 30"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Interrupt(1, 0); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: interrupt did not terminate the process")
	}

	if !s.Exited() {
		t.Error("expected process to have exited after interrupt")
	}
}

func TestSessionInterruptCountAndDelay(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")

	// Ignore SIGINT so the process survives every press — this lets us observe
	// that Interrupt sends `count` times and sleeps `delay` between each.
	s, err := NewSession(SessionOpts{
		ID: "canny", Command: "sh", Args: []string{"-c", "trap '' INT; sleep 30"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Give the trap time to install before interrupting.
	time.Sleep(200 * time.Millisecond)

	start := time.Now()

	if err := s.Interrupt(3, 100*time.Millisecond); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// 3 presses with a 100ms gap means at least 2 gaps (~200ms) of sleeping.
	if elapsed := time.Since(start); elapsed < 180*time.Millisecond {
		t.Errorf("Interrupt returned after %v, want >= ~200ms for the inter-press delays", elapsed)
	}

	if s.Exited() {
		t.Error("process ignoring SIGINT should still be running")
	}
}

func TestSessionAttachDetach(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")

	s, err := NewSession(SessionOpts{
		ID: "braw", Command: "sh", Args: []string{"-c", "read line; echo $line"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var buf syncBuf
	s.Attach(&buf)

	if err := s.WriteInput([]byte("bonnie output\n")); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(5 * time.Second)

	for !bytes.Contains(buf.Bytes(), []byte("bonnie output")) {
		select {
		case <-deadline:
			t.Fatalf("bonnie output = %q", buf.Bytes())
		case <-time.After(10 * time.Millisecond):
		}
	}

	s.Detach()

	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for process exit")
	}
}
