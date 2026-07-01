package pty

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
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
