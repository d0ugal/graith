package pty

import (
	"fmt"
	"strings"

	"github.com/hinshun/vt10x"
)

const (
	glyphReverse   = 1 << 0
	glyphUnderline = 1 << 1
	glyphBold      = 1 << 2
	glyphItalic    = 1 << 4
	glyphBlink     = 1 << 5
)

type ScreenCapture struct {
	Frame         string
	CursorX       int
	CursorY       int
	CursorVisible bool
	Cols          int
	Rows          int
}

func (s *Session) ScreenSnapshot() ScreenCapture {
	s.mu.Lock()
	snap := renderFrame(s.screen)
	s.mu.Unlock()

	return snap
}

func (s *Session) ScreenPreview() string {
	s.mu.Lock()
	preview := renderPreview(s.screen)
	s.mu.Unlock()

	return preview
}

func renderFrame(vt vt10x.Terminal) ScreenCapture {
	cols, rows := vt.Size()
	cur := vt.Cursor()
	visible := vt.CursorVisible()

	var buf strings.Builder
	buf.Grow(cols * rows * 8)

	var (
		prevFG   = vt10x.DefaultFG
		prevBG   = vt10x.DefaultBG
		prevMode int16
	)

	for y := 0; y < rows; y++ {
		if y > 0 {
			buf.WriteString("\r\n")
		}

		for x := 0; x < cols; x++ {
			g := vt.Cell(x, y)
			if g.FG != prevFG || g.BG != prevBG || g.Mode != prevMode {
				writeSGR(&buf, g)
				prevFG = g.FG
				prevBG = g.BG
				prevMode = g.Mode
			}

			ch := g.Char
			if ch == 0 {
				ch = ' '
			}

			buf.WriteRune(ch)
		}
	}

	buf.WriteString("\x1b[0m")

	return ScreenCapture{
		Frame:         buf.String(),
		CursorX:       cur.X,
		CursorY:       cur.Y,
		CursorVisible: visible,
		Cols:          cols,
		Rows:          rows,
	}
}

func writeSGR(buf *strings.Builder, g vt10x.Glyph) {
	buf.WriteString("\x1b[0")

	if g.Mode&glyphBold != 0 {
		buf.WriteString(";1")
	}

	if g.Mode&glyphItalic != 0 {
		buf.WriteString(";3")
	}

	if g.Mode&glyphUnderline != 0 {
		buf.WriteString(";4")
	}

	if g.Mode&glyphBlink != 0 {
		buf.WriteString(";5")
	}

	if g.Mode&glyphReverse != 0 {
		buf.WriteString(";7")
	}

	writeColor(buf, g.FG, false)
	writeColor(buf, g.BG, true)
	buf.WriteByte('m')
}

func writeColor(buf *strings.Builder, c vt10x.Color, bg bool) {
	if bg && c == vt10x.DefaultBG {
		return
	}

	if !bg && c == vt10x.DefaultFG {
		return
	}

	switch {
	case c < 8:
		base := 30
		if bg {
			base = 40
		}

		fmt.Fprintf(buf, ";%d", base+int(c))
	case c < 16:
		base := 90
		if bg {
			base = 100
		}

		fmt.Fprintf(buf, ";%d", base+int(c)-8)
	case c < 256:
		if bg {
			fmt.Fprintf(buf, ";48;5;%d", c)
		} else {
			fmt.Fprintf(buf, ";38;5;%d", c)
		}
	default:
		r := (uint32(c) >> 16) & 0xFF
		g := (uint32(c) >> 8) & 0xFF

		b := uint32(c) & 0xFF
		if bg {
			fmt.Fprintf(buf, ";48;2;%d;%d;%d", r, g, b)
		} else {
			fmt.Fprintf(buf, ";38;2;%d;%d;%d", r, g, b)
		}
	}
}

func renderPreview(vt vt10x.Terminal) string {
	cols, rows := vt.Size()

	var result strings.Builder
	result.Grow(cols * rows)

	for y := 0; y < rows; y++ {
		if y > 0 {
			result.WriteByte('\n')
		}

		var line strings.Builder

		for x := 0; x < cols; x++ {
			ch := vt.Cell(x, y).Char
			if ch == 0 {
				ch = ' '
			}

			line.WriteRune(ch)
		}

		result.WriteString(strings.TrimRight(line.String(), " "))
	}

	return result.String()
}
