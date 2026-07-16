package pty

// This file defines the narrow terminal-screen surface graith relies on and the
// backend-neutral cell/color model the renderer consumes. The concrete emulator
// (github.com/charmbracelet/x/vt) lives behind the Terminal interface in
// terminal_charm.go so the backend can be swapped without touching render.go or
// session.go (issue #1211).

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
	Resize(cols, rows int)
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

// newTerminal constructs the default Terminal backend at the given size.
func newTerminal(cols, rows int) Terminal {
	return newCharmTerminal(cols, rows)
}
