//go:build libghostty && cgo && (darwin || linux)

package pty

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGhosttySnapshotProtocolRoundTrip(t *testing.T) {
	want := TerminalSnapshot{
		Cells: []Cell{
			{Content: "e\u0301", Style: CellStyle{
				FG:   Color{Kind: ColorIndexed, Value: 208},
				BG:   Color{Kind: ColorRGB, Value: 0x0a141e},
				Bold: true, Faint: true, Italic: true, Underline: true,
				Blink: true, Reverse: true, Strikethrough: true,
			}},
			{Content: ""},
		},
		CursorX:       1,
		CursorY:       0,
		CursorVisible: true,
		Cols:          2,
		Rows:          1,
	}

	payload, err := encodeGhosttySnapshot(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeGhosttySnapshot(payload)
	if err != nil {
		t.Fatal(err)
	}

	if got.Cols != want.Cols || got.Rows != want.Rows ||
		got.CursorX != want.CursorX || got.CursorY != want.CursorY ||
		got.CursorVisible != want.CursorVisible {
		t.Fatalf("snapshot metadata = %+v, want %+v", got, want)
	}
	if len(got.Cells) != len(want.Cells) {
		t.Fatalf("snapshot cells = %d, want %d", len(got.Cells), len(want.Cells))
	}
	for i := range want.Cells {
		if got.Cells[i] != want.Cells[i] {
			t.Errorf("cell %d = %+v, want %+v", i, got.Cells[i], want.Cells[i])
		}
	}
}

func TestGhosttySnapshotProtocolRejectsMalformedFrames(t *testing.T) {
	valid, err := encodeGhosttySnapshot(TerminalSnapshot{
		Cells: []Cell{{Content: "braw"}}, Cols: 1, Rows: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string][]byte{
		"empty":               nil,
		"truncated fixed":     valid[:12],
		"cell count":          append([]byte(nil), valid...),
		"content length":      append([]byte(nil), valid...),
		"trailing payload":    append(append([]byte(nil), valid...), 0),
		"invalid color kind":  append([]byte(nil), valid...),
		"invalid cursor bool": append([]byte(nil), valid...),
		"unknown style flag":  append([]byte(nil), valid...),
		"invalid utf8":        append([]byte(nil), valid...),
	}
	tests["cell count"][12] = 2
	tests["content length"][13] = 0xff
	tests["invalid color kind"][17] = 0xff
	tests["invalid cursor bool"][8] = 2
	tests["unknown style flag"][19] = 0x80
	tests["invalid utf8"][len(valid)-1] = 0xff

	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeGhosttySnapshot(payload); !errors.Is(err, errGhosttyHelperProtocol) {
				t.Fatalf("decode error = %v, want protocol violation", err)
			}
		})
	}
}

func TestGhosttyProcessTerminalLifecycle(t *testing.T) {
	term, err := newGhosttyProcessTerminal(20, 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = term.Close() })

	if term.cmd == nil || term.cmd.Process == nil || term.cmd.Process.Pid == 0 {
		t.Fatal("helper process was not started")
	}
	if _, err := term.Write([]byte("braw e\u0301 你")); err != nil {
		t.Fatal(err)
	}

	snapshot, err := term.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Cells[5].Content; got != "e\u0301" {
		t.Errorf("combined grapheme = %q, want %q", got, "e\u0301")
	}
	if err := term.Resize(30, 4); err != nil {
		t.Fatal(err)
	}
	if cols, rows := term.Size(); cols != 30 || rows != 4 {
		t.Errorf("size = (%d,%d), want (30,4)", cols, rows)
	}

	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Write([]byte("thrawn")); !errors.Is(err, errGhosttyHelperClosed) {
		t.Fatalf("write after close = %v, want helper closed", err)
	}
}

func TestGhosttyHelperCrashReconstructsFromScrollback(t *testing.T) {
	scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "bothy.log"), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	term, err := newGhosttyProcessTerminal(40, 4)
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{
		ID:                   "canny-helper",
		Scrollback:           scrollback,
		screen:               term,
		screenHydrationBytes: defaultScreenHydrationBytes,
		log:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	t.Cleanup(func() {
		_ = session.screen.Close()
		_ = scrollback.Close()
	})

	initial := []byte("braw before crash\r\n")
	if _, err := scrollback.Write(initial); err != nil {
		t.Fatal(err)
	}
	if err := session.writeScreenLocked(initial); err != nil {
		t.Fatal(err)
	}

	if err := term.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-term.waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not exit after kill")
	}

	afterCrash := []byte("canny after crash")
	if _, err := scrollback.Write(afterCrash); err != nil {
		t.Fatal(err)
	}
	if err := session.writeScreenLocked(afterCrash); err == nil {
		t.Fatal("write after helper crash returned nil error")
	}

	preview, err := renderPreviewErr(session.screen)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview, "braw before crash") || !strings.Contains(preview, "canny after crash") {
		t.Fatalf("reconstructed preview = %q", preview)
	}
	if session.screen == term {
		t.Fatal("crashed helper was not replaced")
	}
}

func TestGhosttyTerminalSizeLimit(t *testing.T) {
	if _, err := newGhosttyProcessTerminal(65535, 65535); err == nil {
		t.Fatal("oversized terminal returned nil error")
	}
}
