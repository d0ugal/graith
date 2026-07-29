package client

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/d0ugal/graith/internal/protocol"
	"golang.org/x/term"
)

func (c *Client) runExperimentalPassthrough(ctx context.Context, opts PassthroughOpts) PassthroughResult {
	fd := int(os.Stdin.Fd())

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return ResultQuit
	}
	defer func() { _ = term.Restore(fd, oldState) }()

	stdout := &syncWriter{w: os.Stdout}

	_, _ = stdout.Write([]byte(experimentalAttachEnterSequence()))
	defer func() {
		_, _ = stdout.Write([]byte(experimentalAttachExitSequence()))
	}()

	cols, rows := int(fallbackCols), int(fallbackRows)
	if w, h, err := term.GetSize(fd); err == nil {
		cols, rows = w, h
	}

	var chrome *experimentalAttachChrome

	if (opts.StatusBar != nil || opts.ReadOnly) && opts.Info != nil {
		position := "bottom"
		if opts.StatusBar != nil {
			position = opts.StatusBar.Position
		}

		chrome = newExperimentalAttachChrome(*opts.Info, opts.ReadOnly, position, rows, cols)
	}

	refreshCh := make(chan struct{}, 1)
	viewport := &experimentalAttachViewport{}

	c.sendExperimentalResize(fd, chrome, refreshCh, viewport)

	resizeCtx, stopResize := context.WithCancel(ctx)
	defer stopResize()

	sigCh := make(chan os.Signal, 1)

	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	go func() {
		for {
			select {
			case <-resizeCtx.Done():
				return
			case <-sigCh:
				c.sendExperimentalResize(fd, chrome, refreshCh, viewport)
			}
		}
	}()

	writeExperimentalAttachSeedWithChrome(stdout, opts.ExperimentalSeed, chrome)
	seedSnapshotID := opts.ExperimentalSeed.Snapshot.SnapshotID

	opts.StatusBar = nil
	opts.ExperimentalSeed = nil
	opts.TerminalOwned = true
	opts.experimentalChrome = chrome
	opts.terminalRefresh = refreshCh
	opts.experimentalSnapshotID = seedSnapshotID
	opts.experimentalViewport = viewport

	return c.runPassthroughLoop(ctx, opts, os.Stdin, stdout, nil)
}

func (c *Client) sendExperimentalResize(fd int, chrome *experimentalAttachChrome, refreshCh chan<- struct{}, viewport *experimentalAttachViewport) {
	w, h, err := term.GetSize(fd)
	if err != nil {
		return
	}

	cols, rows := w, h
	if chrome != nil {
		chrome.updateSize(h, w)
		cols, rows = chrome.childSize()
	}

	if viewport != nil {
		viewport.update(w, rows)
	}

	_ = c.SendControl("resize", protocol.ResizeMsg{
		Cols: uint16(cols), //nolint:gosec // G115: terminal width from term.GetSize is a small non-negative int
		Rows: uint16(rows), //nolint:gosec // G115: terminal height from term.GetSize is a small non-negative int
	})

	signalExperimentalRefresh(refreshCh)
}

func signalExperimentalRefresh(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func experimentalAttachEnterSequence() string {
	return "" +
		"\x1b[?1049h" + // enter alternate screen buffer
		"\x1b[r" + // reset any inherited scroll region before repainting
		"\x1b[0m" + // reset any inherited style before clearing
		"\x1b[?25l" + // hide cursor while repainting
		"\x1b[H\x1b[2J" // clear screen, cursor home
}

func experimentalAttachExitSequence() string {
	return "" +
		"\x1b[r" + // reset scroll region
		"\x1b[?1049l" + // leave alternate screen buffer
		"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l" + // disable mouse tracking
		"\x1b[?1004l" + // disable focus event reporting
		"\x1b[?2004l" + // disable bracketed paste
		"\x1b[?1l\x1b>" + // leave application cursor keys and keypad mode
		"\x1b[<u" + // pop Kitty keyboard protocol
		"\x1b[0m" + // reset text attributes
		"\x1b[?25h" // show cursor
}

func writeExperimentalAttachSeed(w io.Writer, seed *protocol.ExperimentalAttachSeedMsg) {
	writeExperimentalAttachSeedWithChrome(w, seed, nil)
}

func writeExperimentalAttachSeedWithChrome(w io.Writer, seed *protocol.ExperimentalAttachSeedMsg, chrome *experimentalAttachChrome) {
	if seed == nil {
		return
	}

	writeExperimentalScreenSnapshotWithChrome(w, &seed.Snapshot, chrome)
}

func writeExperimentalScreenSnapshot(w io.Writer, snap *protocol.ScreenSnapshotResponseMsg) {
	writeExperimentalScreenSnapshotWithChrome(w, snap, nil)
}

func writeExperimentalScreenSnapshotWithChrome(w io.Writer, snap *protocol.ScreenSnapshotResponseMsg, chrome *experimentalAttachChrome) {
	if snap == nil || (snap.Frame == "" && !snap.Delta) {
		return
	}

	var buf strings.Builder
	buf.WriteString("\x1b[?2026h")
	buf.WriteString("\x1b[r")
	buf.WriteString("\x1b[0m")
	buf.WriteString("\x1b[?25l")

	var (
		chromeInfo     statusBarInfo
		chromeReadOnly bool
		frame          experimentalChromeFrame
		hasChrome      bool
	)

	if chrome != nil {
		chromeInfo, chromeReadOnly, frame = chrome.snapshot()
		hasChrome = true

		chrome.updateChildCursor(snap)
	}

	if snap.Delta {
		screenRows := snap.Rows
		cursorYOffset := 0

		if hasChrome {
			if _, childRows := frame.childSize(); childRows > 0 {
				screenRows = min(screenRows, childRows)
			}

			cursorYOffset = frame.topRows()
		}

		if hasChrome {
			buf.WriteString("\x1b[?7l")
		}

		writeExperimentalScreenRows(&buf, snap.RowDeltas, screenRows, cursorYOffset)

		if hasChrome {
			buf.WriteString("\x1b[?7h")
		}
	} else {
		buf.WriteString("\x1b[H\x1b[2J")

		if hasChrome {
			buf.WriteString(frame.childOriginSequence())
		}

		childFrame := snap.Frame
		if hasChrome {
			childFrame = frame.clipChildFrame(childFrame)

			buf.WriteString("\x1b[?7l")
		}

		buf.WriteString(childFrame)

		if hasChrome {
			buf.WriteString("\x1b[?7h")
		}
	}

	if hasChrome {
		renderExperimentalChromeLine(&buf, chromeInfo, chromeReadOnly, frame)
	}

	cursorRow, cursorCol := snap.CursorY+1, snap.CursorX+1
	if hasChrome {
		cursorRow, cursorCol = frame.childCursorPosition(snap.CursorY, snap.CursorX)
	}

	fmt.Fprintf(&buf, "\x1b[%d;%dH", cursorRow, cursorCol)

	if snap.CursorVisible {
		buf.WriteString("\x1b[?25h")
	}

	buf.WriteString("\x1b[?2026l")

	_, _ = w.Write([]byte(buf.String()))
}

func writeExperimentalScreenRows(buf *strings.Builder, rows []protocol.ScreenSnapshotRowMsg, screenRows int, cursorYOffset int) {
	for _, row := range rows {
		if row.Y < 0 || row.Y >= screenRows {
			continue
		}

		fmt.Fprintf(buf, "\x1b[%d;1H\x1b[0m\x1b[2K%s", row.Y+1+cursorYOffset, row.Frame)
	}
}

type experimentalAttachViewport struct {
	mu   sync.Mutex
	cols int
	rows int
}

func (v *experimentalAttachViewport) update(cols, rows int) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.cols = cols
	v.rows = rows
}

func (v *experimentalAttachViewport) size() (int, int, bool) {
	if v == nil {
		return 0, 0, false
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	return v.cols, v.rows, v.cols > 0 && v.rows > 0
}

type experimentalAttachChrome struct {
	mu         sync.Mutex
	readOnly   bool
	info       statusBarInfo
	frame      experimentalChromeFrame
	cursor     experimentalAttachCursor
	mouseState experimentalChromeMouseState
}

type experimentalAttachCursor struct {
	x       int
	y       int
	visible bool
	known   bool
}

func newExperimentalAttachChrome(info protocol.SessionInfo, readOnly bool, position string, rows, cols int) *experimentalAttachChrome {
	return &experimentalAttachChrome{
		readOnly: readOnly,
		info:     newStatusBarInfo(info, 0, protocol.FleetSummary{}),
		frame:    newExperimentalChromeFrame(rows, cols, position, true),
	}
}

func (ch *experimentalAttachChrome) updateSize(rows, cols int) {
	ch.mu.Lock()
	ch.frame = ch.frame.resize(rows, cols)
	ch.mu.Unlock()
}

func (ch *experimentalAttachChrome) updateInfo(info statusBarInfo) {
	ch.mu.Lock()
	ch.info = info
	ch.mu.Unlock()
}

func (ch *experimentalAttachChrome) updateChildCursor(snap *protocol.ScreenSnapshotResponseMsg) {
	ch.mu.Lock()
	ch.cursor = experimentalAttachCursor{
		x:       snap.CursorX,
		y:       snap.CursorY,
		visible: snap.CursorVisible,
		known:   true,
	}
	ch.mu.Unlock()
}

func (ch *experimentalAttachChrome) childSize() (int, int) {
	ch.mu.Lock()
	frame := ch.frame
	ch.mu.Unlock()

	return frame.childSize()
}

func (ch *experimentalAttachChrome) snapshot() (statusBarInfo, bool, experimentalChromeFrame) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	return ch.info, ch.readOnly, ch.frame
}

func (ch *experimentalAttachChrome) renderTo(w io.Writer) {
	var buf strings.Builder

	ch.renderStatusRefresh(&buf)

	_, _ = w.Write([]byte(buf.String()))
}

func (ch *experimentalAttachChrome) translateMouseInput(input []byte) []byte {
	ch.mu.Lock()
	frame := ch.frame
	mouseState := ch.mouseState
	out, mouseState := translateSGRMouseForExperimentalFrameWithState(input, frame, mouseState)
	ch.mouseState = mouseState
	ch.mu.Unlock()

	return out
}

func (ch *experimentalAttachChrome) renderStatusRefresh(buf *strings.Builder) {
	ch.mu.Lock()
	readOnly := ch.readOnly
	info := ch.info
	frame := ch.frame
	cursor := ch.cursor
	ch.mu.Unlock()

	// Return before the sync/hide prologue so a degenerate size cannot leave the
	// terminal inside an unclosed synchronized update.
	if _, ok := frame.chromeLineRow(); !ok {
		return
	}

	if cursor.known {
		buf.WriteString("\x1b[?2026h")
		buf.WriteString("\x1b[?25l")
	}

	renderExperimentalChromeLine(buf, info, readOnly, frame)

	if cursor.known {
		cursorRow, cursorCol := frame.childCursorPosition(cursor.y, cursor.x)
		writeExperimentalAttachCursorRestore(buf, cursorRow, cursorCol, cursor.visible)
		buf.WriteString("\x1b[?2026l")
	}
}

func renderExperimentalChromeLine(buf *strings.Builder, info statusBarInfo, readOnly bool, frame experimentalChromeFrame) {
	row, ok := frame.chromeLineRow()
	if !ok {
		return
	}

	line := formatStatusLine(info, frame.cols)
	if readOnly {
		line = formatReadOnlyLine(info, frame.cols)
	}

	fmt.Fprintf(buf, "\x1b[%d;1H%s", row, line)
}

type experimentalChromePlacement int

const (
	experimentalChromeBottom experimentalChromePlacement = iota
	experimentalChromeTop
)

type experimentalChromeFrame struct {
	rows          int
	cols          int
	placement     experimentalChromePlacement
	chromeEnabled bool
}

func newExperimentalChromeFrame(rows, cols int, position string, chromeEnabled bool) experimentalChromeFrame {
	placement := experimentalChromeBottom
	if position == "top" {
		placement = experimentalChromeTop
	}

	return experimentalChromeFrame{
		rows:          max(rows, 0),
		cols:          max(cols, 0),
		placement:     placement,
		chromeEnabled: chromeEnabled,
	}
}

func (f experimentalChromeFrame) resize(rows, cols int) experimentalChromeFrame {
	f.rows = max(rows, 0)
	f.cols = max(cols, 0)

	return f
}

func (f experimentalChromeFrame) reservedRows() int {
	if !f.chromeEnabled || f.rows <= 1 || f.cols < 1 {
		return 0
	}

	return 1
}

func (f experimentalChromeFrame) topRows() int {
	if f.placement == experimentalChromeTop {
		return f.reservedRows()
	}

	return 0
}

func (f experimentalChromeFrame) childSize() (int, int) {
	return f.cols, max(f.rows-f.reservedRows(), 0)
}

func (f experimentalChromeFrame) childOriginSequence() string {
	if top := f.topRows(); top > 0 {
		return fmt.Sprintf("\x1b[%d;1H", top+1)
	}

	return ""
}

func (f experimentalChromeFrame) childCursorPosition(childY, childX int) (int, int) {
	childCols, childRows := f.childSize()
	y := max(childY, 0)
	x := max(childX, 0)

	if childRows > 0 {
		y = min(y, childRows-1)
	}

	if childCols > 0 {
		x = min(x, childCols-1)
	}

	return y + 1 + f.topRows(), x + 1
}

func (f experimentalChromeFrame) chromeLineRow() (int, bool) {
	if f.reservedRows() == 0 || f.cols < 1 {
		return 0, false
	}

	if f.placement == experimentalChromeTop {
		return 1, true
	}

	return f.rows, true
}

func (f experimentalChromeFrame) outerToChildCell(col, row int) (int, int, bool) {
	if col < 1 || row < 1 || col > f.cols || row > f.rows {
		return 0, 0, false
	}

	top := f.topRows()

	childRows := max(f.rows-f.reservedRows(), 0)
	if row <= top || row > top+childRows {
		return 0, 0, false
	}

	return col, row - top, true
}

func (f experimentalChromeFrame) translateMouseEvent(ev sgrMouseEvent, allowChromeRelease bool) (sgrMouseEvent, bool) {
	col, row, ok := f.outerToChildCell(ev.col, ev.row)
	if !ok && ev.release && allowChromeRelease {
		col, row, ok = f.chromeReleaseCell(ev.col, ev.row)
	}

	if !ok {
		return sgrMouseEvent{}, false
	}

	ev.col = col
	ev.row = row

	return ev, true
}

func (f experimentalChromeFrame) chromeReleaseCell(col, row int) (int, int, bool) {
	if col < 1 || row < 1 || col > f.cols || row > f.rows {
		return 0, 0, false
	}

	top := f.topRows()

	_, childRows := f.childSize()
	if childRows < 1 {
		return 0, 0, false
	}

	switch {
	case top > 0 && row <= top:
		return col, 1, true
	case row > top+childRows:
		return col, childRows, true
	default:
		return 0, 0, false
	}
}

func (f experimentalChromeFrame) clipChildFrame(frame string) string {
	if f.reservedRows() == 0 {
		return frame
	}

	// Screen snapshots are produced by pty.renderFrame, which separates full
	// rows with CRLF. Clip at those row boundaries so a stale oversized snapshot
	// cannot paint or scroll into the Graith-owned chrome row.
	_, childRows := f.childSize()

	row := 1

	for i := 0; i < len(frame)-1; i++ {
		if frame[i] != '\r' || frame[i+1] != '\n' {
			continue
		}

		row++
		if row > childRows {
			return frame[:i] + "\x1b[0m"
		}

		i++
	}

	return frame
}

func translateSGRMouseForExperimentalFrame(input []byte, frame experimentalChromeFrame) []byte {
	out, _ := translateSGRMouseForExperimentalFrameWithState(input, frame, experimentalChromeMouseState{})

	return out
}

func translateSGRMouseForExperimentalFrameWithState(input []byte, frame experimentalChromeFrame, mouseState experimentalChromeMouseState) ([]byte, experimentalChromeMouseState) {
	var out []byte

	copied := 0

	for i := 0; i < len(input); i++ {
		if input[i] != '\x1b' {
			continue
		}

		ev, seqLen, ok := parseSGRMouse(input, i)
		if !ok {
			continue
		}

		_, _, inChild := frame.outerToChildCell(ev.col, ev.row)
		translated, keep := frame.translateMouseEvent(ev, mouseState.allowChromeRelease())

		mouseState = mouseState.update(ev, inChild)
		if keep && translated == ev {
			continue
		}

		if out == nil {
			out = make([]byte, 0, len(input))
		}

		out = append(out, input[copied:i]...)
		if keep {
			out = append(out, formatSGRMouse(translated)...)
		}

		i += seqLen - 1
		copied = i + 1
	}

	if out == nil {
		return input, mouseState
	}

	return append(out, input[copied:]...), mouseState
}

func formatSGRMouse(ev sgrMouseEvent) []byte {
	term := 'M'
	if ev.release {
		term = 'm'
	}

	return fmt.Appendf(nil, "\x1b[<%d;%d;%d%c", ev.button, ev.col, ev.row, term)
}

type experimentalChromeMouseState struct {
	buttonDownInChild bool
}

func (s experimentalChromeMouseState) allowChromeRelease() bool {
	return s.buttonDownInChild
}

func (s experimentalChromeMouseState) update(ev sgrMouseEvent, inChild bool) experimentalChromeMouseState {
	if isMouseButtonPress(ev) {
		s.buttonDownInChild = inChild
	}

	if ev.release {
		s.buttonDownInChild = false
	}

	return s
}

func isMouseButtonPress(ev sgrMouseEvent) bool {
	return !ev.release && ev.button&mouseMotionBit == 0 && ev.button&mouseWheelBit == 0
}

func writeExperimentalAttachCursorRestore(buf *strings.Builder, row, col int, visible bool) {
	fmt.Fprintf(buf, "\x1b[%d;%dH", row, col)

	if visible {
		buf.WriteString("\x1b[?25h")
	}
}
