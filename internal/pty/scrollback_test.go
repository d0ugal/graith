package pty

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestScrollbackWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")
	sb, err := NewScrollback(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()
	sb.Write([]byte("hello world"))
	data, _ := os.ReadFile(path)
	if string(data) != "hello world" {
		t.Errorf("log = %q", data)
	}
}

func TestScrollbackTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")
	sb, err := NewScrollback(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()
	sb.Write([]byte("line1\nline2\nline3\n"))
	tail, err := sb.Tail(2)
	if err != nil {
		t.Fatal(err)
	}
	if string(tail) != "line2\nline3\n" {
		t.Errorf("tail = %q", tail)
	}
}

func TestScrollbackTailLargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")
	sb, err := NewScrollback(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	for i := range 2000 {
		fmt.Fprintf(sb, "line %04d\n", i)
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
	defer sb.Close()

	sb.Write([]byte("abcdefghij"))

	t.Run("returns all when maxBytes exceeds size", func(t *testing.T) {
		tail, err := sb.TailBytes(100)
		if err != nil {
			t.Fatal(err)
		}
		if string(tail) != "abcdefghij" {
			t.Errorf("tail = %q, want %q", tail, "abcdefghij")
		}
	})

	t.Run("returns last N bytes", func(t *testing.T) {
		tail, err := sb.TailBytes(5)
		if err != nil {
			t.Fatal(err)
		}
		if string(tail) != "fghij" {
			t.Errorf("tail = %q, want %q", tail, "fghij")
		}
	})

	t.Run("zero maxBytes returns all", func(t *testing.T) {
		tail, err := sb.TailBytes(0)
		if err != nil {
			t.Fatal(err)
		}
		if string(tail) != "abcdefghij" {
			t.Errorf("tail = %q, want %q", tail, "abcdefghij")
		}
	})
}

func TestScrollbackStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")
	sb, err := NewScrollback(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	written, maxSize, saturated := sb.Stats()
	if written != 0 || maxSize != 100 || saturated {
		t.Errorf("initial stats: written=%d, maxSize=%d, saturated=%v", written, maxSize, saturated)
	}

	sb.Write([]byte("hello"))
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
	defer sb.Close()

	sb.Write([]byte("0123456789"))
	sb.Write([]byte("overflow"))

	written, maxSize, saturated := sb.Stats()
	if written != 10 || maxSize != 10 || !saturated {
		t.Errorf("saturated stats: written=%d, maxSize=%d, saturated=%v", written, maxSize, saturated)
	}
}

func TestScrollbackStopsAtMaxSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.log")
	sb, err := NewScrollback(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	sb.Write([]byte("0123456789"))
	sb.Write([]byte("overflow"))

	data, _ := os.ReadFile(path)
	if string(data) != "0123456789" {
		t.Errorf("expected only first 10 bytes, got %q", data)
	}
	if !sb.saturated {
		t.Error("expected saturated=true after exceeding maxSize")
	}
}
