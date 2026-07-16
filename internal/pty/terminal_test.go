package pty

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// This file is the characterization suite for the terminal-emulation backend
// behind the Terminal interface (issue #1211). Each test pins a concrete
// behavior of the emulator as observed through the neutral Cell/Color model, so
// the backend can be swapped again in future with confidence that previews,
// snapshots, cursor tracking, resize, and race-safety still behave the same.
//
// The suite exercises the emulator only through the exported Terminal
// interface, never the concrete charm-vt type, so it doubles as the contract
// any replacement backend must satisfy.

// newCharTestTerm builds a Terminal and registers cleanup for the drain
// goroutine.
func newCharTestTerm(t *testing.T, cols, rows int) Terminal {
	t.Helper()

	term := newTerminal(cols, rows)

	t.Cleanup(func() { _ = term.Close() })

	return term
}

// write feeds s into the terminal, failing the test on a write error.
func write(t *testing.T, term Terminal, s string) {
	t.Helper()

	if _, err := term.Write([]byte(s)); err != nil {
		t.Fatalf("write %q: %v", s, err)
	}
}

// line returns the plain text of screen row y with wide-grapheme continuation
// columns skipped and trailing blanks trimmed — the same normalization
// renderPreview uses per line.
func line(t *testing.T, term Terminal, y int) string {
	t.Helper()

	cols, _ := term.Size()

	var b strings.Builder

	for x := 0; x < cols; x++ {
		cell := term.Cell(x, y)
		if cell.Content == "" {
			continue
		}

		b.WriteString(cell.Content)
	}

	return strings.TrimRight(b.String(), " ")
}

func TestNormalAndAlternateScreen(t *testing.T) {
	term := newCharTestTerm(t, 20, 3)

	write(t, term, "on the brae")

	if got := line(t, term, 0); got != "on the brae" {
		t.Fatalf("main screen line0 = %q, want %q", got, "on the brae")
	}

	// Enter the alternate screen (DECSET 1049): it starts cleared.
	write(t, term, "\x1b[?1049h")

	if got := line(t, term, 0); got != "" {
		t.Errorf("alt screen line0 = %q, want empty", got)
	}

	write(t, term, "in the bothy")

	if got := line(t, term, 0); got != "in the bothy" {
		t.Errorf("alt screen line0 = %q, want %q", got, "in the bothy")
	}

	// Leave the alternate screen: the main screen content is restored.
	write(t, term, "\x1b[?1049l")

	if got := line(t, term, 0); got != "on the brae" {
		t.Errorf("restored main line0 = %q, want %q", got, "on the brae")
	}
}

func TestCursorMovement(t *testing.T) {
	term := newCharTestTerm(t, 20, 5)

	// CUP: move to row 3, column 5 (1-based) → zero-based (4, 2).
	write(t, term, "\x1b[3;5H")

	x, y, visible := term.Cursor()
	if x != 4 || y != 2 {
		t.Errorf("cursor = (%d, %d), want (4, 2)", x, y)
	}

	if !visible {
		t.Error("cursor should be visible by default")
	}

	// Relative moves: up 1, back 2 → (2, 1).
	write(t, term, "\x1b[A\x1b[2D")

	x, y, _ = term.Cursor()
	if x != 2 || y != 1 {
		t.Errorf("cursor after relative moves = (%d, %d), want (2, 1)", x, y)
	}
}

func TestCursorVisibilityTracking(t *testing.T) {
	term := newCharTestTerm(t, 10, 2)

	if _, _, visible := term.Cursor(); !visible {
		t.Fatal("cursor should start visible")
	}

	write(t, term, "\x1b[?25l") // DECTCEM hide

	if _, _, visible := term.Cursor(); visible {
		t.Error("cursor should be hidden after DECTCEM reset")
	}

	write(t, term, "\x1b[?25h") // show

	if _, _, visible := term.Cursor(); !visible {
		t.Error("cursor should be visible after DECTCEM set")
	}
}

func TestCursorSaveRestore(t *testing.T) {
	term := newCharTestTerm(t, 20, 5)

	write(t, term, "\x1b[3;5H")  // move to (4, 2)
	write(t, term, "\x1b7")      // DECSC save
	write(t, term, "\x1b[1;1HX") // home, write X (advances to (1, 0))
	write(t, term, "\x1b8")      // DECRC restore → (4, 2)

	x, y, _ := term.Cursor()
	if x != 4 || y != 2 {
		t.Errorf("restored cursor = (%d, %d), want (4, 2)", x, y)
	}

	if got := line(t, term, 0); got != "X" {
		t.Errorf("line0 = %q, want %q", got, "X")
	}
}

func TestEraseOperations(t *testing.T) {
	t.Run("erase to end of line", func(t *testing.T) {
		term := newCharTestTerm(t, 20, 2)
		write(t, term, "dreich haar day")
		write(t, term, "\x1b[1;7H") // to column 7 (the space before "haar")
		write(t, term, "\x1b[K")    // EL: erase to end of line

		if got := line(t, term, 0); got != "dreich" {
			t.Errorf("line0 = %q, want %q", got, "dreich")
		}
	})

	t.Run("erase entire display", func(t *testing.T) {
		term := newCharTestTerm(t, 20, 2)
		write(t, term, "scunner\r\nfash")
		write(t, term, "\x1b[2J") // ED: erase whole display

		if got := line(t, term, 0); got != "" {
			t.Errorf("line0 = %q, want empty", got)
		}

		if got := line(t, term, 1); got != "" {
			t.Errorf("line1 = %q, want empty", got)
		}
	})
}

func TestScrollingRegion(t *testing.T) {
	term := newCharTestTerm(t, 10, 5)
	write(t, term, "L1\r\nL2\r\nL3\r\nL4\r\nL5")

	// DECSTBM: restrict scrolling to rows 2-4, then scroll up from the bottom
	// of the region. L1 (outside the region) stays put; L2 is scrolled out.
	write(t, term, "\x1b[2;4r")
	write(t, term, "\x1b[4;1H") // bottom row of the region
	write(t, term, "\n")        // linefeed scrolls the region

	if got := line(t, term, 0); got != "L1" {
		t.Errorf("row0 (outside region) = %q, want %q", got, "L1")
	}

	if got := line(t, term, 1); got != "L3" {
		t.Errorf("row1 = %q, want %q (region scrolled)", got, "L3")
	}

	if got := line(t, term, 4); got != "L5" {
		t.Errorf("row4 (outside region) = %q, want %q", got, "L5")
	}
}

func TestInsertAndDeleteCharacter(t *testing.T) {
	t.Run("replace mode is the default", func(t *testing.T) {
		term := newCharTestTerm(t, 20, 2)
		write(t, term, "abcdef")
		write(t, term, "\x1b[1;1HXY") // overwrite from home

		if got := line(t, term, 0); got != "XYcdef" {
			t.Errorf("line0 = %q, want %q", got, "XYcdef")
		}
	})

	t.Run("insert character shifts right", func(t *testing.T) {
		term := newCharTestTerm(t, 20, 2)
		write(t, term, "abcdef")
		write(t, term, "\x1b[1;1H\x1b[2@") // ICH: insert 2 blanks at home

		if got := line(t, term, 0); got != "  abcdef" {
			t.Errorf("line0 = %q, want %q", got, "  abcdef")
		}
	})

	t.Run("delete character shifts left", func(t *testing.T) {
		term := newCharTestTerm(t, 20, 2)
		write(t, term, "abcdef")
		write(t, term, "\x1b[1;1H\x1b[2P") // DCH: delete 2 chars at home

		if got := line(t, term, 0); got != "cdef" {
			t.Errorf("line0 = %q, want %q", got, "cdef")
		}
	})
}

func TestResizePreservesAndTruncates(t *testing.T) {
	term := newCharTestTerm(t, 20, 2)
	write(t, term, "keep me canny")

	term.Resize(40, 4)

	if cols, rows := term.Size(); cols != 40 || rows != 4 {
		t.Errorf("size after grow = (%d, %d), want (40, 4)", cols, rows)
	}

	if got := line(t, term, 0); got != "keep me canny" {
		t.Errorf("content lost on grow: line0 = %q", got)
	}

	term.Resize(4, 2)

	if cols, rows := term.Size(); cols != 4 || rows != 2 {
		t.Errorf("size after shrink = (%d, %d), want (4, 2)", cols, rows)
	}

	if got := line(t, term, 0); got != "keep" {
		t.Errorf("content on shrink: line0 = %q, want %q", got, "keep")
	}
}

func TestResizeClampsToMinimum(t *testing.T) {
	term := newCharTestTerm(t, 10, 3)

	// A zero/negative size must not panic; it clamps to at least 1×1.
	term.Resize(0, 0)

	cols, rows := term.Size()
	if cols < 1 || rows < 1 {
		t.Errorf("clamped size = (%d, %d), want both >= 1", cols, rows)
	}
}

func TestSGRAttributes(t *testing.T) {
	cases := []struct {
		name string
		seq  string
		want func(CellStyle) bool
	}{
		{"bold", "\x1b[1m", func(s CellStyle) bool { return s.Bold }},
		{"faint", "\x1b[2m", func(s CellStyle) bool { return s.Faint }},
		{"italic", "\x1b[3m", func(s CellStyle) bool { return s.Italic }},
		{"underline", "\x1b[4m", func(s CellStyle) bool { return s.Underline }},
		{"blink", "\x1b[5m", func(s CellStyle) bool { return s.Blink }},
		{"reverse", "\x1b[7m", func(s CellStyle) bool { return s.Reverse }},
		{"strikethrough", "\x1b[9m", func(s CellStyle) bool { return s.Strikethrough }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term := newCharTestTerm(t, 10, 2)
			write(t, term, tc.seq+"Z")

			if !tc.want(term.Cell(0, 0).Style) {
				t.Errorf("%s attribute not set on cell: %+v", tc.name, term.Cell(0, 0).Style)
			}
		})
	}
}

func TestSGRColors(t *testing.T) {
	cases := []struct {
		name string
		seq  string
		want Color
	}{
		{"basic red fg", "\x1b[31m", Color{Kind: ColorIndexed, Value: 1}},
		{"bright red fg", "\x1b[91m", Color{Kind: ColorIndexed, Value: 9}},
		{"256 orange fg", "\x1b[38;5;208m", Color{Kind: ColorIndexed, Value: 208}},
		{"truecolor fg", "\x1b[38;2;10;20;30m", Color{Kind: ColorRGB, Value: 0x0A141E}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term := newCharTestTerm(t, 10, 2)
			write(t, term, tc.seq+"Z")

			if got := term.Cell(0, 0).Style.FG; got != tc.want {
				t.Errorf("FG = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSGRBackgroundColor(t *testing.T) {
	term := newCharTestTerm(t, 10, 2)
	write(t, term, "\x1b[44mZ")

	if got := term.Cell(0, 0).Style.BG; got != (Color{Kind: ColorIndexed, Value: 4}) {
		t.Errorf("BG = %+v, want basic blue", got)
	}
}

func TestSGRResetClearsStyle(t *testing.T) {
	term := newCharTestTerm(t, 10, 2)
	write(t, term, "\x1b[1;31mA\x1b[0mB")

	if styled := term.Cell(0, 0).Style; !styled.Bold || styled.FG.Kind != ColorIndexed {
		t.Errorf("cell A should be bold+colored, got %+v", styled)
	}

	if reset := term.Cell(1, 0).Style; reset != (CellStyle{}) {
		t.Errorf("cell B should be default after reset, got %+v", reset)
	}
}

func TestUTF8SplitWrites(t *testing.T) {
	term := newCharTestTerm(t, 20, 2)

	// A multi-byte UTF-8 string split mid-rune across writes must reassemble.
	full := []byte("café €uro")
	_, _ = term.Write(full[:2])  // "ca"
	_, _ = term.Write(full[2:4]) // "f" + first byte of "é"
	_, _ = term.Write(full[4:])  // rest

	if got := line(t, term, 0); got != "café €uro" {
		t.Errorf("reassembled line0 = %q, want %q", got, "café €uro")
	}
}

func TestWideCharacters(t *testing.T) {
	term := newCharTestTerm(t, 20, 2)
	write(t, term, "A你B") // 你 is double-width

	if got := term.Cell(0, 0).Content; got != "A" {
		t.Errorf("cell0 = %q, want %q", got, "A")
	}

	if got := term.Cell(1, 0).Content; got != "你" {
		t.Errorf("cell1 = %q, want %q", got, "你")
	}

	// The column after a wide grapheme is an empty continuation cell.
	if got := term.Cell(2, 0).Content; got != "" {
		t.Errorf("cell2 (continuation) = %q, want empty", got)
	}

	if got := term.Cell(3, 0).Content; got != "B" {
		t.Errorf("cell3 = %q, want %q", got, "B")
	}

	// The preview skips the continuation cell, so display width is preserved.
	if got := line(t, term, 0); got != "A你B" {
		t.Errorf("line0 = %q, want %q", got, "A你B")
	}
}

func TestEmojiCharacter(t *testing.T) {
	term := newCharTestTerm(t, 20, 2)
	write(t, term, "😀x")

	if got := term.Cell(0, 0).Content; got != "😀" {
		t.Errorf("cell0 = %q, want emoji", got)
	}

	if got := term.Cell(1, 0).Content; got != "" {
		t.Errorf("cell1 (continuation) = %q, want empty", got)
	}

	if got := term.Cell(2, 0).Content; got != "x" {
		t.Errorf("cell2 = %q, want %q", got, "x")
	}
}

// TestRenderFrameSGREmission pins the exact SGR codes renderFrame emits for
// each color class and attribute, across both foreground and background — the
// snapshot format a client replays to restore the screen. It exercises the
// bright, 256, and true-color background branches the plain-text tests don't.
func TestRenderFrameSGREmission(t *testing.T) {
	cases := []struct {
		name string
		seq  string
		want string
	}{
		{"basic fg", "\x1b[31m", ";31"},
		{"basic bg", "\x1b[41m", ";41"},
		{"bright fg", "\x1b[91m", ";91"},
		{"bright bg", "\x1b[101m", ";101"},
		{"256 fg", "\x1b[38;5;208m", ";38;5;208"},
		{"256 bg", "\x1b[48;5;208m", ";48;5;208"},
		{"truecolor fg", "\x1b[38;2;10;20;30m", ";38;2;10;20;30"},
		{"truecolor bg", "\x1b[48;2;10;20;30m", ";48;2;10;20;30"},
		{"faint", "\x1b[2m", ";2"},
		{"italic", "\x1b[3m", ";3"},
		{"underline", "\x1b[4m", ";4"},
		{"blink", "\x1b[5m", ";5"},
		{"reverse", "\x1b[7m", ";7"},
		{"strikethrough", "\x1b[9m", ";9"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term := newCharTestTerm(t, 10, 2)
			write(t, term, tc.seq+"Z")

			frame := renderFrame(term)
			if !strings.Contains(frame.Frame, tc.want+"m") && !strings.Contains(frame.Frame, tc.want+";") {
				t.Errorf("frame missing %q for %s: %q", tc.want, tc.name, frame.Frame)
			}
		})
	}
}

// TestRenderFrameDefaultColorsEmitNoSGR confirms a screen of purely
// default-styled cells emits no color/attribute SGR before the final reset —
// the run-length suppression that keeps snapshots compact.
func TestRenderFrameDefaultColorsEmitNoSGR(t *testing.T) {
	term := newCharTestTerm(t, 10, 2)
	write(t, term, "plain")

	frame := renderFrame(term)
	// The only SGR in the frame should be the trailing reset.
	if strings.Count(frame.Frame, "\x1b[") != 1 {
		t.Errorf("expected a single (reset) SGR, got %q", frame.Frame)
	}

	if !strings.HasSuffix(frame.Frame, "\x1b[0m") {
		t.Errorf("frame should end with reset, got %q", frame.Frame)
	}
}

func TestIncompleteEscapeSequence(t *testing.T) {
	term := newCharTestTerm(t, 20, 2)

	// An escape sequence split across writes must be buffered and applied once
	// the terminator arrives — the "B" ends up red.
	write(t, term, "A\x1b[")
	write(t, term, "31mB")

	if got := line(t, term, 0); got != "AB" {
		t.Errorf("line0 = %q, want %q", got, "AB")
	}

	if got := term.Cell(1, 0).Style.FG; got != (Color{Kind: ColorIndexed, Value: 1}) {
		t.Errorf("cell B FG = %+v, want red once sequence completed", got)
	}
}

func TestTrailingEscapeDoesNotCorrupt(t *testing.T) {
	term := newCharTestTerm(t, 20, 2)

	// A lone trailing ESC (no terminator) must not eat printable text or panic.
	write(t, term, "haar\x1b")

	if got := line(t, term, 0); got != "haar" {
		t.Errorf("line0 = %q, want %q", got, "haar")
	}
}

func TestControlCharacters(t *testing.T) {
	t.Run("tab", func(t *testing.T) {
		term := newCharTestTerm(t, 20, 2)
		write(t, term, "a\tb")

		if got := line(t, term, 0); got != "a       b" {
			t.Errorf("line0 = %q, want tab-expanded", got)
		}
	})

	t.Run("backspace", func(t *testing.T) {
		term := newCharTestTerm(t, 20, 2)
		write(t, term, "abc\b\bX")

		if got := line(t, term, 0); got != "aXc" {
			t.Errorf("line0 = %q, want %q", got, "aXc")
		}
	})

	t.Run("carriage return", func(t *testing.T) {
		term := newCharTestTerm(t, 20, 2)
		write(t, term, "hello\rHE")

		if got := line(t, term, 0); got != "HEllo" {
			t.Errorf("line0 = %q, want %q", got, "HEllo")
		}
	})
}

// TestRepresentativeTUIOutput feeds a synthetic sequence in the shape of an
// agent TUI: alternate screen, cursor hide, colored/attributed status lines,
// device queries (which must be drained, not block), a box-drawing frame, and a
// final teardown. It asserts the rendered screen is sensible and that graith's
// snapshot/preview renderers produce clean output. No real session data is used.
func TestRepresentativeTUIOutput(t *testing.T) {
	term := newCharTestTerm(t, 40, 6)

	var b strings.Builder
	b.WriteString("\x1b[?1049h")                         // enter alt screen
	b.WriteString("\x1b[?25l")                           // hide cursor
	b.WriteString("\x1b[6n\x1b[c")                       // DSR + DA queries (must be drained)
	b.WriteString("\x1b[2J\x1b[H")                       // clear + home
	b.WriteString("\x1b[1;36m╭────────────╮\x1b[0m\r\n") // bold cyan box top
	b.WriteString("\x1b[36m│\x1b[0m \x1b[1mbraw agent\x1b[0m \x1b[36m│\x1b[0m\r\n")
	b.WriteString("\x1b[36m╰────────────╯\x1b[0m\r\n")
	b.WriteString("\x1b[38;2;120;200;80mgreen truecolor status\x1b[0m")

	write(t, term, b.String())

	if _, _, visible := term.Cursor(); visible {
		t.Error("cursor should be hidden in the TUI")
	}

	if got := line(t, term, 0); !strings.Contains(got, "╭") {
		t.Errorf("row0 = %q, want box-drawing top", got)
	}

	if got := line(t, term, 1); !strings.Contains(got, "braw agent") {
		t.Errorf("row1 = %q, want title", got)
	}

	if got := line(t, term, 3); !strings.Contains(got, "green truecolor status") {
		t.Errorf("row3 = %q, want status line", got)
	}

	// The snapshot must carry SGR color and end with a reset; the preview must
	// be free of escape sequences.
	frame := renderFrame(term)
	if !strings.Contains(frame.Frame, ";38;2;120;200;80m") {
		t.Error("snapshot frame should carry the truecolor SGR")
	}

	if !strings.HasSuffix(frame.Frame, "\x1b[0m") {
		t.Error("snapshot frame should end with SGR reset")
	}

	if frame.CursorVisible {
		t.Error("snapshot should record the cursor as hidden")
	}

	preview := renderPreview(term)
	if strings.Contains(preview, "\x1b") {
		t.Errorf("preview must not contain escape sequences: %q", preview)
	}

	if !strings.Contains(preview, "braw agent") {
		t.Errorf("preview should contain the title: %q", preview)
	}
}

// TestDeviceQueriesDoNotBlockWrite is the regression test for the io.Pipe
// deadlock (issue #1211): the emulator answers device queries by writing to an
// internal pipe, and if nothing drains it Write blocks forever. A burst of
// queries must complete promptly.
func TestDeviceQueriesDoNotBlockWrite(t *testing.T) {
	term := newCharTestTerm(t, 80, 24)

	done := make(chan struct{})

	go func() {
		defer close(done)

		for i := 0; i < 2000; i++ {
			// DSR cursor report + primary DA + DSR status — each triggers a
			// response written to the emulator's pipe.
			write(t, term, "\x1b[6n\x1b[c\x1b[5nblether\r\n")
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Write blocked on the emulator response pipe (deadlock)")
	}
}

// TestConcurrentWriteAndSnapshot exercises the interleaving the Session mutex
// protects in production: writes and screen reads must be race-free when
// serialized by an external lock. Run under -race this catches unguarded shared
// state in the adapter.
func TestConcurrentWriteAndSnapshot(t *testing.T) {
	term := newCharTestTerm(t, 40, 10)

	var mu sync.Mutex

	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()

		for i := 0; i < 500; i++ {
			mu.Lock()
			write(t, term, "\x1b[1;32mbide\x1b[0m\r\n")
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()

		for i := 0; i < 500; i++ {
			mu.Lock()
			_ = renderFrame(term)
			_ = renderPreview(term)
			_, _, _ = term.Cursor()
			mu.Unlock()
		}
	}()

	wg.Wait()
}
