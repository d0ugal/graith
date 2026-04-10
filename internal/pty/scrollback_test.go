package pty

import (
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
