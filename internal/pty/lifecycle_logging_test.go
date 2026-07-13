package pty

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionPgidEqualsPID documents that Pgid tracks the process group graith
// signals: because sessions start with Setsid, the child is a group leader and
// its PGID equals its PID (issue #1104).
func TestSessionPgidEqualsPID(t *testing.T) {
	s, err := NewSession(SessionOpts{
		ID: "braw", Command: "sleep", Args: []string{"100"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: filepath.Join(t.TempDir(), "test.log"), MaxLogSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = s.Kill()
		s.Close()
	}()

	if s.ProcessPID() <= 0 {
		t.Fatalf("ProcessPID() = %d, want > 0", s.ProcessPID())
	}

	if s.Pgid() != s.ProcessPID() {
		t.Errorf("Pgid() = %d, want == ProcessPID() %d", s.Pgid(), s.ProcessPID())
	}
}

// TestFirstOutputLogsSinceLaunch is the regression test for the launch→first-
// output half of gap #6: the "pty first output" line carries the duration from
// spawn to first byte.
func TestFirstOutputLogsSinceLaunch(t *testing.T) {
	buf := &syncBuf{}
	log := slog.New(slog.NewJSONHandler(buf, nil))

	s, err := NewSession(SessionOpts{
		ID: "braw", Command: "sh", Args: []string{"-c", "echo braw graith; sleep 0.1"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: filepath.Join(t.TempDir(), "test.log"), MaxLogSize: 1024 * 1024,
		Logger: log,
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

	var found bool

	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}

		if rec["msg"] != "pty first output" {
			continue
		}

		found = true

		if _, ok := rec["since_launch_ms"]; !ok {
			t.Error("\"pty first output\" record missing since_launch_ms")
		}
	}

	if !found {
		t.Fatalf("no \"pty first output\" record emitted; log = %s", buf.Bytes())
	}
}
