package pty

import (
	"strings"
	"testing"
)

type snapshotTestTerminal struct {
	snapshot TerminalSnapshot
}

func (t *snapshotTestTerminal) Write(p []byte) (int, error) { return len(p), nil }
func (t *snapshotTestTerminal) Resize(cols, rows int) error {
	t.snapshot.Cols = cols
	t.snapshot.Rows = rows
	t.snapshot.Cells = make([]Cell, cols*rows)

	for i := range t.snapshot.Cells {
		t.snapshot.Cells[i].Content = " "
	}

	return nil
}
func (t *snapshotTestTerminal) Size() (int, int) { return t.snapshot.Cols, t.snapshot.Rows }
func (t *snapshotTestTerminal) Cursor() (int, int, bool) {
	return t.snapshot.CursorX, t.snapshot.CursorY, t.snapshot.CursorVisible
}
func (t *snapshotTestTerminal) Cell(x, y int) Cell {
	if x < 0 || x >= t.snapshot.Cols || y < 0 || y >= t.snapshot.Rows {
		return Cell{Content: " "}
	}

	return t.snapshot.Cells[y*t.snapshot.Cols+x]
}
func (t *snapshotTestTerminal) Close() error { return nil }
func (t *snapshotTestTerminal) Snapshot() (TerminalSnapshot, error) {
	return cloneTerminalSnapshot(t.snapshot), nil
}

func TestScreenSnapshotDeltaRows(t *testing.T) {
	term := &snapshotTestTerminal{snapshot: testSnapshot(8,
		testCells("braw"),
		testCells("canny"),
		testCells("dreich"),
	)}
	session := &Session{ID: "bothy", screen: term}

	base := session.ScreenSnapshot()
	if base.SnapshotID == 0 || base.Frame == "" || base.Delta {
		t.Fatalf("base snapshot = %+v, want full frame with snapshot id", base)
	}

	term.snapshot = testSnapshot(8,
		testCells("braw"),
		testCells("blether"),
		testCells("dreich"),
	)
	term.snapshot.CursorX = 3
	term.snapshot.CursorY = 1
	term.snapshot.CursorVisible = true

	delta := session.ScreenSnapshotDelta(base.SnapshotID)
	if !delta.Delta {
		t.Fatalf("delta snapshot Delta = false, want true: %+v", delta)
	}

	if delta.Frame != "" {
		t.Fatalf("delta frame = %q, want empty full frame", delta.Frame)
	}

	if got := len(delta.RowDeltas); got != 1 {
		t.Fatalf("delta rows = %d, want 1: %+v", got, delta.RowDeltas)
	}

	if row := delta.RowDeltas[0]; row.Y != 1 || !strings.Contains(row.Frame, "blether") {
		t.Fatalf("delta row = %+v, want row 1 containing blether", row)
	}

	if delta.CursorX != 3 || delta.CursorY != 1 || !delta.CursorVisible {
		t.Fatalf("delta cursor = (%d,%d,%v), want (3,1,true)", delta.CursorX, delta.CursorY, delta.CursorVisible)
	}
}

func TestScreenSnapshotDeltaDetectsStyleChanges(t *testing.T) {
	term := &snapshotTestTerminal{snapshot: testSnapshot(6, testCells("braw"))}
	session := &Session{ID: "canny", screen: term}

	base := session.ScreenSnapshot()

	red := CellStyle{FG: Color{Kind: ColorIndexed, Value: 1}}
	term.snapshot = testSnapshot(6, testStyledCells("braw", red))

	delta := session.ScreenSnapshotDelta(base.SnapshotID)
	if got := len(delta.RowDeltas); got != 1 {
		t.Fatalf("style delta rows = %d, want 1", got)
	}

	if !strings.Contains(delta.RowDeltas[0].Frame, ";31m") {
		t.Fatalf("style delta frame = %q, want red SGR", delta.RowDeltas[0].Frame)
	}
}

func TestScreenSnapshotDeltaWideAndGraphemeRows(t *testing.T) {
	term := &snapshotTestTerminal{snapshot: testSnapshot(5, []Cell{
		{Content: "你"}, {Content: ""}, {Content: "e\u0301"}, {Content: " "}, {Content: " "},
	})}
	session := &Session{ID: "strath", screen: term}

	base := session.ScreenSnapshot()

	term.snapshot = testSnapshot(5, []Cell{
		{Content: "😀"}, {Content: ""}, {Content: "x"}, {Content: " "}, {Content: " "},
	})

	delta := session.ScreenSnapshotDelta(base.SnapshotID)
	if got := len(delta.RowDeltas); got != 1 {
		t.Fatalf("wide delta rows = %d, want 1", got)
	}

	if frame := delta.RowDeltas[0].Frame; !strings.Contains(frame, "😀") || !strings.Contains(frame, "x") {
		t.Fatalf("wide delta frame = %q, want emoji and x", frame)
	}
}

func TestScreenSnapshotDeltaCursorOnly(t *testing.T) {
	term := &snapshotTestTerminal{snapshot: testSnapshot(8, testCells("braw"))}
	session := &Session{ID: "croft", screen: term}

	base := session.ScreenSnapshot()

	term.snapshot.CursorX = 4
	term.snapshot.CursorY = 0
	term.snapshot.CursorVisible = true

	delta := session.ScreenSnapshotDelta(base.SnapshotID)
	if !delta.Delta {
		t.Fatalf("cursor-only update returned full snapshot: %+v", delta)
	}

	if len(delta.RowDeltas) != 0 {
		t.Fatalf("cursor-only delta rows = %+v, want none", delta.RowDeltas)
	}

	if delta.CursorX != 4 || delta.CursorY != 0 || !delta.CursorVisible {
		t.Fatalf("cursor-only cursor = (%d,%d,%v), want (4,0,true)", delta.CursorX, delta.CursorY, delta.CursorVisible)
	}
}

func TestScreenSnapshotDeltaRowClearing(t *testing.T) {
	term := &snapshotTestTerminal{snapshot: testSnapshot(7, testCells("bairn"))}
	session := &Session{ID: "bairn", screen: term}

	base := session.ScreenSnapshot()

	term.snapshot = testSnapshot(7, nil)

	delta := session.ScreenSnapshotDelta(base.SnapshotID)
	if got := len(delta.RowDeltas); got != 1 {
		t.Fatalf("clear delta rows = %d, want 1", got)
	}

	if frame := delta.RowDeltas[0].Frame; !strings.Contains(frame, strings.Repeat(" ", 7)) {
		t.Fatalf("clear delta frame = %q, want blank row payload", frame)
	}
}

func TestScreenSnapshotDeltaFallsBackToFullOnResize(t *testing.T) {
	term := &snapshotTestTerminal{snapshot: testSnapshot(4, testCells("braw"))}
	session := &Session{ID: "thrawn", screen: term}

	base := session.ScreenSnapshot()

	term.snapshot = testSnapshot(6, testCells("braw"))

	delta := session.ScreenSnapshotDelta(base.SnapshotID)
	if delta.Delta {
		t.Fatalf("resize delta returned Delta=true, want full snapshot: %+v", delta)
	}

	if delta.Frame == "" || delta.Cols != 6 {
		t.Fatalf("resize fallback = %+v, want full frame at new width", delta)
	}
}

func TestScreenSnapshotDeltaAlternateScreenVisibleRows(t *testing.T) {
	term := &snapshotTestTerminal{snapshot: testSnapshot(8, testCells("main"))}
	session := &Session{ID: "alt-braw", screen: term}

	base := session.ScreenSnapshot()

	// The backend-neutral delta contract receives the currently visible
	// terminal snapshot; an alternate-screen switch is visible here as changed
	// rows from the active buffer.
	term.snapshot = testSnapshot(8, testCells("alt"))

	delta := session.ScreenSnapshotDelta(base.SnapshotID)
	if got := len(delta.RowDeltas); got != 1 {
		t.Fatalf("alternate-screen delta rows = %d, want 1", got)
	}

	if frame := delta.RowDeltas[0].Frame; !strings.Contains(frame, "alt") || strings.Contains(frame, "main") {
		t.Fatalf("alternate-screen delta frame = %q, want visible alternate row only", frame)
	}
}

func TestScreenSnapshotDeltaFallsBackToFullOnStaleBase(t *testing.T) {
	term := &snapshotTestTerminal{snapshot: testSnapshot(8, testCells("base"))}
	session := &Session{ID: "stale-braw", screen: term}

	base := session.ScreenSnapshot()

	for i := 0; i < screenSnapshotCacheLimit; i++ {
		content := strings.Repeat(string(rune('a'+i)), 8)
		term.snapshot = testSnapshot(8, testCells(content))
		_ = session.ScreenSnapshot()
	}

	term.snapshot = testSnapshot(8, testCells("latest"))

	delta := session.ScreenSnapshotDelta(base.SnapshotID)
	if delta.Delta {
		t.Fatalf("stale base returned delta snapshot: %+v", delta)
	}

	if delta.Frame == "" || !strings.Contains(delta.Frame, "latest") {
		t.Fatalf("stale base fallback = %+v, want full latest frame", delta)
	}
}

func TestScreenSnapshotDeltaTransferSizeMeasurement(t *testing.T) {
	rows := make([][]Cell, 24)
	for y := range rows {
		rows[y] = testCells(strings.Repeat("a", 80))
	}

	term := &snapshotTestTerminal{snapshot: testSnapshot(80, rows...)}
	session := &Session{ID: "measure-braw", screen: term}

	base := session.ScreenSnapshot()

	rows[12] = testCells(strings.Repeat("b", 80))
	term.snapshot = testSnapshot(80, rows...)

	delta := session.ScreenSnapshotDelta(base.SnapshotID)
	full := renderSnapshotFrame(term.snapshot)

	deltaBytes := 0
	for _, row := range delta.RowDeltas {
		deltaBytes += len(row.Frame)
	}

	t.Logf("80x24 one-row update: full_frame_bytes=%d row_delta_bytes=%d changed_rows=%d",
		len(full.Frame), deltaBytes, len(delta.RowDeltas))

	if len(delta.RowDeltas) != 1 {
		t.Fatalf("changed rows = %d, want 1", len(delta.RowDeltas))
	}

	if deltaBytes >= len(full.Frame) {
		t.Fatalf("row delta bytes = %d, want less than full frame bytes %d", deltaBytes, len(full.Frame))
	}
}

func testSnapshot(cols int, rows ...[]Cell) TerminalSnapshot {
	if len(rows) == 0 {
		rows = [][]Cell{nil}
	}

	cells := make([]Cell, cols*len(rows))
	for i := range cells {
		cells[i].Content = " "
	}

	for y, row := range rows {
		copy(cells[y*cols:(y+1)*cols], row)
	}

	return TerminalSnapshot{
		Cells:         cells,
		CursorVisible: false,
		Cols:          cols,
		Rows:          len(rows),
	}
}

func testCells(s string) []Cell {
	return testStyledCells(s, CellStyle{})
}

func testStyledCells(s string, style CellStyle) []Cell {
	cells := make([]Cell, 0, len(s))
	for _, r := range s {
		cells = append(cells, Cell{Content: string(r), Style: style})
	}

	return cells
}
