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
		sb.Write([]byte(fmt.Sprintf("line %04d\n", i)))
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
