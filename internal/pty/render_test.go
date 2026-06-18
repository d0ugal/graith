package pty

import (
	"strings"
	"testing"

	"github.com/hinshun/vt10x"
)

func TestRenderFramePlainText(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(10, 3))
	vt.Write([]byte("braw!"))

	frame := renderFrame(vt)
	if frame.Cols != 10 || frame.Rows != 3 {
		t.Errorf("Size = (%d, %d), want (10, 3)", frame.Cols, frame.Rows)
	}
	if !strings.Contains(frame.Frame, "braw!") {
		t.Errorf("Frame should contain 'braw!', got %q", frame.Frame)
	}
	if !strings.HasSuffix(frame.Frame, "\x1b[0m") {
		t.Error("Frame should end with SGR reset")
	}
}

func TestRenderFrameColors(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(20, 3))
	vt.Write([]byte("\x1b[31mred\x1b[0m normal"))

	frame := renderFrame(vt)
	if !strings.Contains(frame.Frame, ";31m") {
		t.Errorf("Frame should contain red FG SGR, got %q", frame.Frame)
	}
	if !strings.Contains(frame.Frame, "red") {
		t.Errorf("Frame should contain 'red', got %q", frame.Frame)
	}
	if !strings.Contains(frame.Frame, "normal") {
		t.Errorf("Frame should contain 'normal', got %q", frame.Frame)
	}
}

func TestRenderFrameBold(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(20, 2))
	vt.Write([]byte("\x1b[1mbold\x1b[0m"))

	frame := renderFrame(vt)
	if !strings.Contains(frame.Frame, ";1m") {
		t.Errorf("Frame should contain bold SGR, got %q", frame.Frame)
	}
}

func TestRenderFrame256Color(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(20, 2))
	vt.Write([]byte("\x1b[38;5;208morange\x1b[0m"))

	frame := renderFrame(vt)
	if !strings.Contains(frame.Frame, ";38;5;208m") {
		t.Errorf("Frame should contain 256-color SGR, got %q", frame.Frame)
	}
}

func TestRenderFrameCursorPosition(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(20, 5))
	vt.Write([]byte("line1\nline2"))

	frame := renderFrame(vt)
	if frame.CursorY < 1 {
		t.Errorf("CursorY = %d, want >= 1 after newline", frame.CursorY)
	}
}

func TestRenderFrameRows(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(10, 4))
	vt.Write([]byte("abc"))

	frame := renderFrame(vt)
	rows := strings.Split(frame.Frame, "\r\n")
	if len(rows) != 4 {
		t.Errorf("Expected 4 rows separated by \\r\\n, got %d", len(rows))
	}
}

func TestRenderPreviewPlainText(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(10, 3))
	vt.Write([]byte("braw!"))

	preview := renderPreview(vt)
	if !strings.Contains(preview, "braw!") {
		t.Errorf("Preview should contain 'braw!', got %q", preview)
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
		ID: "braw", Command: "echo", Args: []string{"hi"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	snap := s.ScreenSnapshot()
	if snap.Cols != 80 || snap.Rows != 24 {
		t.Errorf("Snapshot size = (%d, %d), want (80, 24)", snap.Cols, snap.Rows)
	}
}

func TestScreenPreviewUsesLock(t *testing.T) {
	logPath := strings.Join([]string{t.TempDir(), "test.log"}, "/")
	s, err := NewSession(SessionOpts{
		ID: "braw", Command: "echo", Args: []string{"hi"},
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
