package pty

import (
	"image/color"
	"io"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	cvt "github.com/charmbracelet/x/vt"
)

// charmTerminal adapts github.com/charmbracelet/x/vt's Emulator to the Terminal
// interface. The Emulator answers device queries (DSR, DA, DECRQM, …) by
// writing to an unbuffered internal pipe; graith is a pure observer — the real
// PTY already replied to those queries — so we drain and discard the responses
// in a goroutine, otherwise the pipe blocks Write and deadlocks Session.readLoop
// (issue #1211).
type charmTerminal struct {
	emu *cvt.Emulator

	// pw is the emulator's response-pipe writer (the same *io.PipeWriter
	// InputPipe returns). Close closes it to unblock the drain goroutine's
	// blocked Read — deliberately instead of Emulator.Close, whose unsynchronized
	// write to the emulator's `closed` flag races the drain goroutine's Read of
	// it (an upstream data race; issue #1211).
	pw        *io.PipeWriter
	drainDone chan struct{}

	// mu guards cursorVisible, which the emulator mutates from its
	// CursorVisibility callback (invoked synchronously inside Write) and Cursor
	// reads. Session already serializes Write against Cursor with its own lock,
	// but guarding here keeps the adapter correct on its own terms.
	mu            sync.Mutex
	cursorVisible bool

	closeOnce sync.Once
}

// newCharmTerminal builds a charm-vt-backed Terminal at cols×rows. Dimensions
// below 1 are clamped so a zero-size PTY (seen briefly on some launch paths)
// can't panic the emulator.
func newCharmTerminal(cols, rows int) *charmTerminal {
	cols, rows = clampSize(cols, rows)

	ct := &charmTerminal{
		emu:           cvt.NewEmulator(cols, rows),
		cursorVisible: true,
		drainDone:     make(chan struct{}),
	}

	if pw, ok := ct.emu.InputPipe().(*io.PipeWriter); ok {
		ct.pw = pw
	}

	ct.emu.SetCallbacks(cvt.Callbacks{
		CursorVisibility: func(visible bool) {
			ct.mu.Lock()
			ct.cursorVisible = visible
			ct.mu.Unlock()
		},
	})

	go func() {
		defer close(ct.drainDone)

		_, _ = io.Copy(io.Discard, ct.emu)
	}()

	return ct
}

func (ct *charmTerminal) Write(p []byte) (int, error) {
	return ct.emu.Write(p)
}

func (ct *charmTerminal) Resize(cols, rows int) {
	cols, rows = clampSize(cols, rows)
	ct.emu.Resize(cols, rows)
}

func (ct *charmTerminal) Size() (int, int) {
	return ct.emu.Width(), ct.emu.Height()
}

func (ct *charmTerminal) Cursor() (int, int, bool) {
	pos := ct.emu.CursorPosition()

	ct.mu.Lock()
	visible := ct.cursorVisible
	ct.mu.Unlock()

	return pos.X, pos.Y, visible
}

func (ct *charmTerminal) Cell(x, y int) Cell {
	c := ct.emu.CellAt(x, y)
	if c == nil {
		return Cell{Content: " "}
	}

	return convertCell(c)
}

func (ct *charmTerminal) Close() error {
	ct.closeOnce.Do(func() {
		// Closing the response pipe's write end makes the drain goroutine's
		// blocked Read return io.EOF; wait for it to exit so no goroutine
		// leaks and no reader survives the emulator.
		if ct.pw != nil {
			_ = ct.pw.CloseWithError(io.EOF)
		}

		<-ct.drainDone
	})

	return nil
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

// convertCell maps an ultraviolet cell to graith's backend-neutral Cell.
func convertCell(c *uv.Cell) Cell {
	return Cell{
		Content: c.Content,
		Style: CellStyle{
			FG:            convertColor(c.Style.Fg),
			BG:            convertColor(c.Style.Bg),
			Bold:          c.Style.Attrs&uv.AttrBold != 0,
			Faint:         c.Style.Attrs&uv.AttrFaint != 0,
			Italic:        c.Style.Attrs&uv.AttrItalic != 0,
			Blink:         c.Style.Attrs&uv.AttrBlink != 0,
			Reverse:       c.Style.Attrs&uv.AttrReverse != 0,
			Strikethrough: c.Style.Attrs&uv.AttrStrikethrough != 0,
			Underline:     c.Style.Underline != uv.UnderlineNone,
		},
	}
}

// convertColor maps a color.Color from the emulator to graith's neutral Color.
// The emulator emits ansi.BasicColor (0-15), ansi.IndexedColor (0-255),
// color.RGBA / ansi.TrueColor (24-bit), or nil (the default color).
func convertColor(c color.Color) Color {
	if c == nil {
		return Color{Kind: ColorDefault}
	}

	switch v := c.(type) {
	case ansi.BasicColor:
		return Color{Kind: ColorIndexed, Value: uint32(v)}
	case ansi.IndexedColor:
		return Color{Kind: ColorIndexed, Value: uint32(v)}
	default:
		// TrueColor, color.RGBA, and any other concrete color: read the 8-bit
		// RGB components off the 16-bit-per-channel color.Color contract.
		r, g, b, _ := c.RGBA()

		return Color{
			Kind:  ColorRGB,
			Value: ((r >> 8) << 16) | ((g >> 8) << 8) | (b >> 8),
		}
	}
}
