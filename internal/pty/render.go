package pty

import (
	"fmt"
	"strings"
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
	if s.closed || s.screenInitializing {
		s.mu.Unlock()

		return ScreenCapture{}
	}

	snap, err := renderFrameErr(s.screen)
	if err != nil {
		recoveryErr := s.replaceScreenLocked()
		s.log.Warn("terminal snapshot failed; screen reconstructed",
			"session", s.ID, "error", err, "recovery_error", recoveryErr)

		if recoveryErr == nil {
			snap, _ = renderFrameErr(s.screen)
		}
	}
	s.mu.Unlock()

	return snap
}

func (s *Session) ScreenPreview() string {
	s.mu.Lock()
	if s.closed || s.screenInitializing {
		s.mu.Unlock()

		return ""
	}

	preview, err := renderPreviewErr(s.screen)
	if err != nil {
		recoveryErr := s.replaceScreenLocked()
		s.log.Warn("terminal preview failed; screen reconstructed",
			"session", s.ID, "error", err, "recovery_error", recoveryErr)

		if recoveryErr == nil {
			preview, _ = renderPreviewErr(s.screen)
		}
	}
	s.mu.Unlock()

	return preview
}

// renderFrame produces an ANSI-styled snapshot of the terminal screen. Rows are
// separated by "\r\n" and the frame ends with an SGR reset, so a client can
// write it straight to a raw terminal to restore the screen. SGR sequences are
// emitted only when a cell's style differs from the previous cell's — the
// initial "previous" style is the zero CellStyle (terminal default), so a
// leading run of default-styled cells emits no SGR at all.
func renderFrame(vt Terminal) ScreenCapture {
	frame, _ := renderFrameErr(vt)

	return frame
}

func renderFrameErr(vt Terminal) (ScreenCapture, error) {
	snapshot, err := snapshotTerminal(vt)
	if err != nil {
		return ScreenCapture{}, err
	}

	cols, rows := snapshot.Cols, snapshot.Rows

	var buf strings.Builder
	buf.Grow(cols * rows * 8)

	var prevStyle CellStyle

	for y := 0; y < rows; y++ {
		if y > 0 {
			buf.WriteString("\r\n")
		}

		for x := 0; x < cols; x++ {
			cell := snapshot.Cells[y*cols+x]
			if cell.Style != prevStyle {
				writeSGR(&buf, cell.Style)
				prevStyle = cell.Style
			}

			// An empty Content is the trailing column of a wide grapheme; the
			// wide character in the preceding column already fills the space, so
			// emit nothing here.
			if cell.Content == "" {
				continue
			}

			buf.WriteString(cell.Content)
		}
	}

	buf.WriteString("\x1b[0m")

	return ScreenCapture{
		Frame:         buf.String(),
		CursorX:       snapshot.CursorX,
		CursorY:       snapshot.CursorY,
		CursorVisible: snapshot.CursorVisible,
		Cols:          cols,
		Rows:          rows,
	}, nil
}

func writeSGR(buf *strings.Builder, style CellStyle) {
	buf.WriteString("\x1b[0")

	if style.Bold {
		buf.WriteString(";1")
	}

	if style.Faint {
		buf.WriteString(";2")
	}

	if style.Italic {
		buf.WriteString(";3")
	}

	if style.Underline {
		buf.WriteString(";4")
	}

	if style.Blink {
		buf.WriteString(";5")
	}

	if style.Reverse {
		buf.WriteString(";7")
	}

	if style.Strikethrough {
		buf.WriteString(";9")
	}

	writeColor(buf, style.FG, false)
	writeColor(buf, style.BG, true)
	buf.WriteByte('m')
}

func writeColor(buf *strings.Builder, c Color, bg bool) {
	switch c.Kind {
	case ColorDefault:
		return
	case ColorRGB:
		r := (c.Value >> 16) & 0xFF
		g := (c.Value >> 8) & 0xFF
		b := c.Value & 0xFF

		if bg {
			fmt.Fprintf(buf, ";48;2;%d;%d;%d", r, g, b)
		} else {
			fmt.Fprintf(buf, ";38;2;%d;%d;%d", r, g, b)
		}
	case ColorIndexed:
		writeIndexedColor(buf, c.Value, bg)
	}
}

func writeIndexedColor(buf *strings.Builder, v uint32, bg bool) {
	switch {
	case v < 8:
		base := 30
		if bg {
			base = 40
		}

		fmt.Fprintf(buf, ";%d", base+int(v))
	case v < 16:
		base := 90
		if bg {
			base = 100
		}

		fmt.Fprintf(buf, ";%d", base+int(v)-8)
	default:
		if bg {
			fmt.Fprintf(buf, ";48;5;%d", v)
		} else {
			fmt.Fprintf(buf, ";38;5;%d", v)
		}
	}
}

// renderPreview produces a plain-text (no ANSI) snapshot of the screen. Rows are
// separated by "\n" with trailing spaces trimmed, for the session-picker
// preview.
func renderPreview(vt Terminal) string {
	preview, _ := renderPreviewErr(vt)

	return preview
}

func renderPreviewErr(vt Terminal) (string, error) {
	snapshot, err := snapshotTerminal(vt)
	if err != nil {
		return "", err
	}

	cols, rows := snapshot.Cols, snapshot.Rows

	var result strings.Builder
	result.Grow(cols * rows)

	for y := 0; y < rows; y++ {
		if y > 0 {
			result.WriteByte('\n')
		}

		var line strings.Builder

		for x := 0; x < cols; x++ {
			cell := snapshot.Cells[y*cols+x]
			// Skip wide-grapheme continuation columns (empty Content).
			if cell.Content == "" {
				continue
			}

			line.WriteString(cell.Content)
		}

		result.WriteString(strings.TrimRight(line.String(), " "))
	}

	return result.String(), nil
}
