package pty

import (
	"errors"
	"io"
)

// This file defines the narrow terminal-screen surface graith relies on and the
// backend-neutral cell/color model the renderer consumes. Concrete emulators
// live behind the Terminal interface so backends can be selected without
// touching render.go or session.go (issue #1211).

// ColorKind identifies how a Color's Value should be interpreted.
type ColorKind uint8

const (
	// ColorDefault is the terminal's default foreground/background — the
	// renderer emits no explicit SGR color for it.
	ColorDefault ColorKind = iota
	// ColorIndexed is an ANSI palette index: 0-7 basic, 8-15 bright, 16-255
	// the 256-color cube/greyscale.
	ColorIndexed
	// ColorRGB is a 24-bit true color packed as 0xRRGGBB in Value.
	ColorRGB
)

// Color is a backend-neutral terminal cell color. The zero value is the
// terminal default color, which keeps a freshly rendered cell equal to the
// unstyled default (see render.go's run-length SGR suppression).
type Color struct {
	Kind  ColorKind
	Value uint32
}

// CellStyle is the visual style of a rendered cell, split out from Cell so the
// renderer can compare adjacent cells' styles with a single struct equality and
// only re-emit an SGR sequence when the style changes. All fields are
// comparable, and the zero value is the terminal's default style.
type CellStyle struct {
	FG            Color
	BG            Color
	Bold          bool
	Faint         bool
	Italic        bool
	Underline     bool
	Blink         bool
	Reverse       bool
	Strikethrough bool
}

// Cell is a single rendered terminal cell. Content is the cell's grapheme
// cluster (a single rune most of the time, but possibly a wide character,
// emoji, or base+combining sequence). An empty Content marks the trailing
// column of a wide (double-width) cell and renders as nothing, since the wide
// grapheme in the preceding column already occupies the space.
type Cell struct {
	Content string
	Style   CellStyle
}

// TerminalSnapshot is one coherent read of a terminal's visible viewport.
// Cells are stored in row-major order and always contain Cols*Rows entries.
// Keeping this bulk form behind the narrow Terminal interface avoids one FFI
// or helper-process round trip per cell.
type TerminalSnapshot struct {
	Cells         []Cell
	CursorX       int
	CursorY       int
	CursorVisible bool
	Cols          int
	Rows          int
}

// Terminal is the terminal-screen emulation surface graith needs: feed it raw
// PTY output with Write, then read back the rendered screen for previews and
// snapshots. It deliberately hides the concrete emulator (issue #1211).
//
// Implementations are not required to be safe for concurrent use; callers
// serialize Write against the Size/Cursor/Cell readers (Session does this with
// its mutex).
type Terminal interface {
	// Write feeds raw PTY output bytes into the emulator. It never blocks on
	// terminal query responses (the implementation drains them).
	Write(p []byte) (int, error)
	// Resize changes the screen dimensions to cols columns by rows rows.
	Resize(cols, rows int) error
	// Size returns the current dimensions as (columns, rows).
	Size() (cols, rows int)
	// Cursor returns the cursor column, row (both zero-based), and whether it
	// is currently visible.
	Cursor() (x, y int, visible bool)
	// Cell returns the rendered cell at (x, y). Out-of-range coordinates return
	// a blank cell.
	Cell(x, y int) Cell
	// Close releases resources held by the emulator (e.g. its response-drain
	// goroutine). It is safe to call more than once.
	Close() error
}

// terminalWriteChunkBytes keeps replay writes below the strictest built-in
// backend request limit. Hydration can be configured above that limit, so it
// must be streamed without weakening the per-request allocation bound.
const terminalWriteChunkBytes = 512 * 1024

func writeTerminalChunks(term Terminal, p []byte) error {
	for len(p) > 0 {
		chunk := p[:min(len(p), terminalWriteChunkBytes)]

		n, err := term.Write(chunk)
		if err != nil {
			return err
		}

		if n != len(chunk) {
			return io.ErrShortWrite
		}

		p = p[n:]
	}

	return nil
}

// terminalSnapshotter is implemented by backends that can extract a coherent
// viewport more efficiently than repeated Cursor and Cell calls. All built-in
// backends implement it; the fallback keeps small test doubles source-compatible
// with Terminal.
type terminalSnapshotter interface {
	Snapshot() (TerminalSnapshot, error)
}

// HelperProcessIdentity identifies a native terminal helper across an exec.
// StartTime is mandatory so a non-zombie PID is never signalled after reuse.
type HelperProcessIdentity struct {
	PID       int
	StartTime int64
}

var errTerminalGenerationFrozen = errors.New("terminal helper generation is frozen")
var errTerminalUnavailable = errors.New("terminal screen is temporarily unavailable")

// unavailableTerminal preserves the PTY/session ownership boundary when a
// derived screen backend cannot be constructed during exec adoption. Writes
// deliberately fail so Session retries reconstruction from authoritative raw
// scrollback on subsequent output, snapshot, or resize. Size still reports the
// last requested geometry so a failed reconstruction never loses it.
type unavailableTerminal struct {
	cols int
	rows int
}

func newUnavailableTerminal(cols, rows int) Terminal {
	return &unavailableTerminal{cols: max(cols, 1), rows: max(rows, 1)}
}

func (t *unavailableTerminal) Write([]byte) (int, error) { return 0, errTerminalUnavailable }
func (t *unavailableTerminal) Resize(cols, rows int) error {
	t.cols = max(cols, 1)
	t.rows = max(rows, 1)

	return errTerminalUnavailable
}
func (t *unavailableTerminal) Size() (int, int)         { return t.cols, t.rows }
func (t *unavailableTerminal) Cursor() (int, int, bool) { return 0, 0, false }
func (t *unavailableTerminal) Cell(int, int) Cell       { return Cell{} }
func (t *unavailableTerminal) Close() error             { return nil }
func (t *unavailableTerminal) Snapshot() (TerminalSnapshot, error) {
	return TerminalSnapshot{}, errTerminalUnavailable
}

func snapshotTerminal(term Terminal) (TerminalSnapshot, error) {
	if snapshotter, ok := term.(terminalSnapshotter); ok {
		return snapshotter.Snapshot()
	}

	cols, rows := term.Size()
	cursorX, cursorY, cursorVisible := term.Cursor()

	cells := make([]Cell, cols*rows)
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			cells[y*cols+x] = term.Cell(x, y)
		}
	}

	return TerminalSnapshot{
		Cells:         cells,
		CursorX:       cursorX,
		CursorY:       cursorY,
		CursorVisible: cursorVisible,
		Cols:          cols,
		Rows:          rows,
	}, nil
}

func clampSize(cols, rows int) (int, int) {
	if cols < 1 {
		cols = 1
	}

	if rows < 1 {
		rows = 1
	}

	return cols, rows
}
