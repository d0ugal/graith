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
