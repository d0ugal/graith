package pty

import (
	"strings"
	"testing"

	"github.com/hinshun/vt10x"
)

func TestRenderFramePlainText(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(10, 3))
	vt.Write([]byte("hello"))

	cap := renderFrame(vt)
	if cap.Cols != 10 || cap.Rows != 3 {
		t.Errorf("Size = (%d, %d), want (10, 3)", cap.Cols, cap.Rows)
	}
	if !strings.Contains(cap.Frame, "hello") {
		t.Errorf("Frame should contain 'hello', got %q", cap.Frame)
	}
	if !strings.HasSuffix(cap.Frame, "\x1b[0m") {
		t.Error("Frame should end with SGR reset")
	}
}

func TestRenderFrameColors(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(20, 3))
	vt.Write([]byte("\x1b[31mred\x1b[0m normal"))

	cap := renderFrame(vt)
	if !strings.Contains(cap.Frame, ";31m") {
		t.Errorf("Frame should contain red FG SGR, got %q", cap.Frame)
	}
	if !strings.Contains(cap.Frame, "red") {
		t.Errorf("Frame should contain 'red', got %q", cap.Frame)
	}
	if !strings.Contains(cap.Frame, "normal") {
		t.Errorf("Frame should contain 'normal', got %q", cap.Frame)
	}
}

func TestRenderFrameBold(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(20, 2))
	vt.Write([]byte("\x1b[1mbold\x1b[0m"))

	cap := renderFrame(vt)
	if !strings.Contains(cap.Frame, ";1m") {
		t.Errorf("Frame should contain bold SGR, got %q", cap.Frame)
	}
}

func TestRenderFrame256Color(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(20, 2))
	vt.Write([]byte("\x1b[38;5;208morange\x1b[0m"))

	cap := renderFrame(vt)
	if !strings.Contains(cap.Frame, ";38;5;208m") {
		t.Errorf("Frame should contain 256-color SGR, got %q", cap.Frame)
	}
}

func TestRenderFrameCursorPosition(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(20, 5))
	vt.Write([]byte("line1\nline2"))

	cap := renderFrame(vt)
	if cap.CursorY < 1 {
		t.Errorf("CursorY = %d, want >= 1 after newline", cap.CursorY)
	}
}

func TestRenderFrameRows(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(10, 4))
	vt.Write([]byte("abc"))

	cap := renderFrame(vt)
	rows := strings.Split(cap.Frame, "\r\n")
	if len(rows) != 4 {
		t.Errorf("Expected 4 rows separated by \\r\\n, got %d", len(rows))
	}
}

func TestRenderPreviewPlainText(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(10, 3))
	vt.Write([]byte("hello"))

	preview := renderPreview(vt)
	if !strings.Contains(preview, "hello") {
		t.Errorf("Preview should contain 'hello', got %q", preview)
	}
	if strings.Contains(preview, "\x1b") {
		t.Error("Preview should not contain escape sequences")
	}
}

func TestRenderPreviewStripsColors(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(20, 3))
	vt.Write([]byte("\x1b[31mred text\x1b[0m"))

	preview := renderPreview(vt)
	if !strings.Contains(preview, "red text") {
		t.Errorf("Preview should contain 'red text', got %q", preview)
	}
	if strings.Contains(preview, "\x1b") {
		t.Error("Preview should not contain escape sequences")
	}
}

func TestRenderPreviewTrimsTrailingSpaces(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(20, 2))
	vt.Write([]byte("hi"))

	preview := renderPreview(vt)
	lines := strings.Split(preview, "\n")
	if strings.HasSuffix(lines[0], " ") {
		t.Error("Preview lines should have trailing spaces trimmed")
	}
}

func TestScreenSnapshotUsesLock(t *testing.T) {
	logPath := strings.Join([]string{t.TempDir(), "test.log"}, "/")
	s, err := NewSession(SessionOpts{
		ID: "test", Command: "echo", Args: []string{"hi"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	cap := s.ScreenSnapshot()
	if cap.Cols != 80 || cap.Rows != 24 {
		t.Errorf("Snapshot size = (%d, %d), want (80, 24)", cap.Cols, cap.Rows)
	}
}

func TestScreenPreviewUsesLock(t *testing.T) {
	logPath := strings.Join([]string{t.TempDir(), "test.log"}, "/")
	s, err := NewSession(SessionOpts{
		ID: "test", Command: "echo", Args: []string{"hi"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	preview := s.ScreenPreview()
	if preview == "" {
		t.Error("Preview should not be empty")
	}
}
