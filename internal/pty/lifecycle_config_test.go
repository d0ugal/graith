package pty

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	if s.inputDelay != typeInputDelay {
		t.Fatalf("inputDelay = %v, want the typeInputDelay default %v", s.inputDelay, typeInputDelay)
	}
}

// TestSetInputDelayLiveUpdate proves SetInputDelay changes the pause the next
// WriteInputAndSubmit observes, so a config hot-reload takes effect on a live
// PTY without reconstructing it (issue #1294). A large delay set after
// construction must dominate the elapsed submit time.
func TestSetInputDelayLiveUpdate(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")

	s, err := NewSession(SessionOpts{
		ID: "thrawn", Command: "sh", Args: []string{"-c", "sleep 30"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
		InputDelay: time.Millisecond, // start small
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.SetInputDelay(300 * time.Millisecond)

	start := time.Now()

	if err := s.WriteInputAndSubmit([]byte("bonnie")); err != nil {
		t.Fatal(err)
	}

	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("WriteInputAndSubmit took %v after SetInputDelay(300ms), want >= ~300ms", elapsed)
	}
}

// TestSetInputDelayNonPositiveFallsBack proves a non-positive SetInputDelay
// value restores the built-in default rather than writing text and CR back to
// back, preserving the per-session construction default (issue #1294).
func TestSetInputDelayNonPositiveFallsBack(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")

	s, err := NewSession(SessionOpts{
		ID: "strath", Command: "sh", Args: []string{"-c", "sleep 30"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
		InputDelay: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.SetInputDelay(0)

	if s.inputDelay != typeInputDelay {
		t.Fatalf("inputDelay = %v after SetInputDelay(0), want typeInputDelay %v", s.inputDelay, typeInputDelay)
	}

	s.SetInputDelay(-1 * time.Second)

	if s.inputDelay != typeInputDelay {
		t.Fatalf("inputDelay = %v after SetInputDelay(-1s), want typeInputDelay %v", s.inputDelay, typeInputDelay)
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

// TestAdoptOptsInputDelay proves an adopted session honours AdoptOpts.InputDelay
// instead of silently reverting to the built-in typeInputDelay default, so a
// configured [lifecycle] input_delay survives a daemon upgrade (issue #1294).
func TestAdoptOptsInputDelay(t *testing.T) {
	adopt := func(t *testing.T, delay time.Duration) *Session {
		t.Helper()

		logPath := filepath.Join(t.TempDir(), "scrollback.log")

		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}

		s, err := AdoptSession(AdoptOpts{
			ID: "bothy", Fd: r.Fd(), PID: cmd.Process.Pid, LogPath: logPath,
			MaxLogSize: 1024 * 1024, HydrationBytes: 0,
			PollInterval: 10 * time.Millisecond, InputDelay: delay,
		})
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
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
		})

		return s
	}

	t.Run("configured delay is honoured", func(t *testing.T) {
		s := adopt(t, 175*time.Millisecond)
		if got := s.InputDelay(); got != 175*time.Millisecond {
			t.Errorf("adopted InputDelay = %v, want the configured 175ms", got)
		}
	})

	t.Run("non-positive falls back to the package default", func(t *testing.T) {
		s := adopt(t, 0)
		if got := s.InputDelay(); got != typeInputDelay {
			t.Errorf("adopted InputDelay = %v, want the %v default", got, typeInputDelay)
		}
	})
}
