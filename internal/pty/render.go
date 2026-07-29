package pty

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

type ScreenRow struct {
	Y     int
	Frame string
}

const screenSnapshotCacheLimit = 8

type screenSnapshotCacheEntry struct {
	id       uint64
	snapshot TerminalSnapshot
}

type ScreenCapture struct {
	Frame         string
	RowDeltas     []ScreenRow
	Delta         bool
	DeltaFrom     uint64
	SnapshotID    uint64
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

	snap := s.screenSnapshotLocked()
	s.mu.Unlock()

	return snap
}

func (s *Session) ScreenSnapshotDelta(deltaFrom uint64) ScreenCapture {
	s.mu.Lock()
	if s.closed || s.screenInitializing {
		s.mu.Unlock()

		return ScreenCapture{}
	}

	snap := s.screenSnapshotDeltaLocked(deltaFrom)
	s.mu.Unlock()

	return snap
}

// AttachWithScreenSnapshot captures the current terminal screen and registers a
// live-output writer under one mutex acquisition. It lets attach clients seed a
// local view without losing bytes written concurrently with attach.
func (s *Session) AttachWithScreenSnapshot(w io.Writer) ScreenCapture {
	s.mu.Lock()
	if s.closed || s.screenInitializing {
		s.writers = append(s.writers, w)
		s.mu.Unlock()

		return ScreenCapture{}
	}

	snap := s.screenSnapshotLocked()
	s.writers = append(s.writers, w)
	s.mu.Unlock()

	return snap
}

func (s *Session) screenSnapshotLocked() ScreenCapture {
	return s.screenSnapshotDeltaLocked(0)
}

func (s *Session) screenSnapshotDeltaLocked(deltaFrom uint64) ScreenCapture {
	snapshot, err := snapshotTerminal(s.screen)
	if err != nil {
		recoveryErr := s.replaceScreenLocked()
		s.log.Warn("terminal snapshot failed; screen reconstructed",
			"session", s.ID, "error", err, "recovery_error", recoveryErr)

		if recoveryErr == nil {
			snapshot, _ = snapshotTerminal(s.screen)
		}
	}

	if len(snapshot.Cells) == 0 || snapshot.Cols <= 0 || snapshot.Rows <= 0 {
		return ScreenCapture{}
	}

	return s.screenCaptureFromSnapshotLocked(snapshot, deltaFrom)
}

func (s *Session) screenCaptureFromSnapshotLocked(snapshot TerminalSnapshot, deltaFrom uint64) ScreenCapture {
	s.screenSnapshotSeq++
	snapshotID := s.screenSnapshotSeq

	var (
		base    TerminalSnapshot
		hasBase bool
	)
	if deltaFrom != 0 {
		base, hasBase = s.lookupScreenSnapshotLocked(deltaFrom)
		hasBase = hasBase && sameSnapshotGeometry(snapshot, base)
	}

	stored := cloneTerminalSnapshot(snapshot)
	s.storeScreenSnapshotLocked(snapshotID, stored)

	var capture ScreenCapture
	if hasBase {
		capture = renderSnapshotDelta(snapshot, base)
		capture.DeltaFrom = deltaFrom
	} else {
		capture = renderSnapshotFrame(snapshot)
	}

	capture.SnapshotID = snapshotID

	return capture
}

func (s *Session) lookupScreenSnapshotLocked(id uint64) (TerminalSnapshot, bool) {
	for _, entry := range s.screenSnapshotCache {
		if entry.id == id {
			return entry.snapshot, true
		}
	}

	return TerminalSnapshot{}, false
}

func (s *Session) storeScreenSnapshotLocked(id uint64, snapshot TerminalSnapshot) {
	s.screenSnapshotCache = append(s.screenSnapshotCache, screenSnapshotCacheEntry{
		id:       id,
		snapshot: snapshot,
	})
	if len(s.screenSnapshotCache) > screenSnapshotCacheLimit {
		copy(s.screenSnapshotCache, s.screenSnapshotCache[len(s.screenSnapshotCache)-screenSnapshotCacheLimit:])
		s.screenSnapshotCache = s.screenSnapshotCache[:screenSnapshotCacheLimit]
	}
}

func sameSnapshotGeometry(a, b TerminalSnapshot) bool {
	return a.Cols == b.Cols && a.Rows == b.Rows &&
		len(a.Cells) == a.Cols*a.Rows && len(b.Cells) == b.Cols*b.Rows
}

func cloneTerminalSnapshot(snapshot TerminalSnapshot) TerminalSnapshot {
	cells := make([]Cell, len(snapshot.Cells))
	copy(cells, snapshot.Cells)
	snapshot.Cells = cells

	return snapshot
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

	return renderSnapshotFrame(snapshot), nil
}

func renderSnapshotFrame(snapshot TerminalSnapshot) ScreenCapture {
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
			writeStyledCell(&buf, cell, &prevStyle)
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
	}
}

func renderSnapshotDelta(snapshot, base TerminalSnapshot) ScreenCapture {
	capture := ScreenCapture{
		Delta:         true,
		CursorX:       snapshot.CursorX,
		CursorY:       snapshot.CursorY,
		CursorVisible: snapshot.CursorVisible,
		Cols:          snapshot.Cols,
		Rows:          snapshot.Rows,
	}

	for y := 0; y < snapshot.Rows; y++ {
		rowStart := y * snapshot.Cols
		rowEnd := rowStart + snapshot.Cols

		if slices.Equal(snapshot.Cells[rowStart:rowEnd], base.Cells[rowStart:rowEnd]) {
			continue
		}

		capture.RowDeltas = append(capture.RowDeltas, ScreenRow{
			Y:     y,
			Frame: renderSnapshotRow(snapshot, y),
		})
	}

	return capture
}

func renderSnapshotRow(snapshot TerminalSnapshot, y int) string {
	var buf strings.Builder
	buf.Grow(snapshot.Cols * 8)

	var prevStyle CellStyle

	rowStart := y * snapshot.Cols
	rowEnd := rowStart + snapshot.Cols

	for _, cell := range snapshot.Cells[rowStart:rowEnd] {
		writeStyledCell(&buf, cell, &prevStyle)
	}

	buf.WriteString("\x1b[0m")

	return buf.String()
}

func writeStyledCell(buf *strings.Builder, cell Cell, prevStyle *CellStyle) {
	if cell.Style != *prevStyle {
		writeSGR(buf, cell.Style)
		*prevStyle = cell.Style
	}

	// An empty Content is the trailing column of a wide grapheme; the wide
	// character in the preceding column already fills the space, so emit nothing
	// here.
	if cell.Content == "" {
		return
	}

	buf.WriteString(cell.Content)
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
