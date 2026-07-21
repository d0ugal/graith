package pty

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestScrollbackWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")

	sb, err := NewScrollback(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	_, _ = sb.Write([]byte("braw neep"))

	data, _ := os.ReadFile(path)
	if string(data) != "braw neep" {
		t.Errorf("log = %q", data)
	}
}

func TestScrollbackTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")

	sb, err := NewScrollback(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	_, _ = sb.Write([]byte("glen\nwynd\nloch\n"))

	tail, err := sb.Tail(2)
	if err != nil {
		t.Fatal(err)
	}

	if string(tail) != "wynd\nloch\n" {
		t.Errorf("tail = %q", tail)
	}
}

func TestScrollbackTailLargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")

	sb, err := NewScrollback(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	for i := range 2000 {
		_, _ = fmt.Fprintf(sb, "line %04d\n", i)
	}

	tail, err := sb.Tail(3)
	if err != nil {
		t.Fatal(err)
	}

	want := "line 1997\nline 1998\nline 1999\n"
	if string(tail) != want {
		t.Errorf("tail = %q, want %q", tail, want)
	}
}

func TestScrollbackTailBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")

	sb, err := NewScrollback(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	_, _ = sb.Write([]byte("brawcannye"))

	t.Run("returns all when maxBytes exceeds size", func(t *testing.T) {
		tail, err := sb.TailBytes(100)
		if err != nil {
			t.Fatal(err)
		}

		if string(tail) != "brawcannye" {
			t.Errorf("tail = %q, want %q", tail, "brawcannye")
		}
	})

	t.Run("returns last N bytes", func(t *testing.T) {
		tail, err := sb.TailBytes(5)
		if err != nil {
			t.Fatal(err)
		}

		if string(tail) != "annye" {
			t.Errorf("tail = %q, want %q", tail, "annye")
		}
	})

	t.Run("zero maxBytes returns all", func(t *testing.T) {
		tail, err := sb.TailBytes(0)
		if err != nil {
			t.Fatal(err)
		}

		if string(tail) != "brawcannye" {
			t.Errorf("tail = %q, want %q", tail, "brawcannye")
		}
	})
}

func TestScrollbackStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")

	sb, err := NewScrollback(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	written, maxSize, saturated := sb.Stats()
	if written != 0 || maxSize != 100 || saturated {
		t.Errorf("initial stats: written=%d, maxSize=%d, saturated=%v", written, maxSize, saturated)
	}

	_, _ = sb.Write([]byte("neep!"))

	written, maxSize, saturated = sb.Stats()
	if written != 5 || maxSize != 100 || saturated {
		t.Errorf("after write: written=%d, maxSize=%d, saturated=%v", written, maxSize, saturated)
	}
}

func TestScrollbackStatsSaturated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")

	sb, err := NewScrollback(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	_, _ = sb.Write([]byte("bonnieloch"))
	_, _ = sb.Write([]byte("thrawn"))

	written, maxSize, saturated := sb.Stats()
	if written != 10 || maxSize != 10 || !saturated {
		t.Errorf("saturated stats: written=%d, maxSize=%d, saturated=%v", written, maxSize, saturated)
	}
}

func TestScrollbackConcurrentReadersAndWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")

	sb, err := NewScrollback(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	_, _ = sb.Write([]byte("braw\n"))

	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()

		for range 200 {
			_, _ = sb.Write([]byte("kirk\n"))
		}
	}()

	go func() {
		defer wg.Done()

		for range 200 {
			if _, err := sb.Tail(5); err != nil {
				t.Errorf("Tail error: %v", err)
				return
			}
		}
	}()

	go func() {
		defer wg.Done()

		for range 200 {
			if _, err := sb.TailBytes(64); err != nil {
				t.Errorf("TailBytes error: %v", err)
				return
			}
		}
	}()

	go func() {
		defer wg.Done()

		for range 200 {
			sb.Stats()
		}
	}()

	wg.Wait()
}

func TestScrollbackStopsAtMaxSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")

	sb, err := NewScrollback(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	_, _ = sb.Write([]byte("bonnieloch"))
	_, _ = sb.Write([]byte("thrawn"))

	data, _ := os.ReadFile(path)
	if string(data) != "bonnieloch" {
		t.Errorf("expected only first 10 bytes, got %q", data)
	}

	if !sb.saturated {
		t.Error("expected saturated=true after exceeding maxSize")
	}
}

func TestNewScrollbackRejectsUnsafeExistingPaths(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "symlink", setup: func(t *testing.T, path string) {
			t.Helper()

			target := filepath.Join(filepath.Dir(path), "canny-target")
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}

			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fifo", setup: func(t *testing.T, path string) {
			t.Helper()

			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wide_mode", setup: func(t *testing.T, path string) {
			t.Helper()

			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}

			if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: deliberately makes the fixture unsafe
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bothy.log")
			tt.setup(t, path)

			if scrollback, err := NewScrollback(path, 1024); err == nil {
				closePTYTestResource(t, scrollback)
				t.Fatal("unsafe scrollback path was accepted")
			}
		})
	}
}

func TestTailFileRejectsUnsafeReplacementWithoutBlocking(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "symlink", setup: func(t *testing.T, path string) {
			t.Helper()

			target := filepath.Join(filepath.Dir(path), "croft-target")
			if err := os.WriteFile(target, []byte("canny"), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fifo", setup: func(t *testing.T, path string) {
			t.Helper()

			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wide_mode", setup: func(t *testing.T, path string) {
			t.Helper()

			if err := os.WriteFile(path, []byte("canny"), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: deliberately makes the fixture unsafe
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dreich.log")
			tt.setup(t, path)

			if _, err := TailFile(path, 10); err == nil {
				t.Fatal("unsafe stopped-session log was accepted")
			}
		})
	}
}

func TestAdoptedScrollbackReadsExactInodeAndPreservesReplacementOnRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "strath.log")

	scrollback, err := NewScrollback(path, 1024)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := scrollback.Write([]byte("canny-owned\n")); err != nil {
		t.Fatal(err)
	}

	fd, err := scrollback.DuplicateFD()
	if err != nil {
		t.Fatal(err)
	}

	if err := scrollback.Close(); err != nil {
		t.Fatal(err)
	}

	adopted, err := AdoptScrollback(uintptr(fd), path, 1024)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = adopted.Close() })

	ownedPath := filepath.Join(dir, "strath-owned.log")
	if err := os.Rename(path, ownedPath); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("foreign replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tail, err := adopted.Tail(1)
	if err != nil {
		t.Fatal(err)
	}

	if string(tail) != "canny-owned\n" {
		t.Fatalf("adopted tail = %q, want exact inherited inode", tail)
	}

	if err := adopted.Remove(); err == nil {
		t.Fatal("Remove accepted a replacement pathname")
	}

	if got, err := os.ReadFile(path); err != nil || string(got) != "foreign replacement\n" {
		t.Fatalf("replacement after Remove = %q, %v", got, err)
	}

	if _, err := os.Stat(ownedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
