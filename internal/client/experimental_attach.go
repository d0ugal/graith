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
	c.sendExperimentalResize(fd, chrome, refreshCh)

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
				c.sendExperimentalResize(fd, chrome, refreshCh)
			}
		}
	}()

	writeExperimentalAttachSeedWithChrome(stdout, opts.ExperimentalSeed, chrome)

	opts.StatusBar = nil
	opts.ExperimentalSeed = nil
	opts.TerminalOwned = true
	opts.experimentalChrome = chrome
	opts.terminalRefresh = refreshCh

	return c.runPassthroughLoop(ctx, opts, os.Stdin, stdout, nil)
}

func (c *Client) sendExperimentalResize(fd int, chrome *experimentalAttachChrome, refreshCh chan<- struct{}) {
	w, h, err := term.GetSize(fd)
	if err != nil {
		return
	}

	rows := h
	if chrome != nil {
		chrome.updateSize(h, w)

		if rows > 1 {
			rows--
		}
	}

	_ = c.SendControl("resize", protocol.ResizeMsg{
		Cols: uint16(w),    //nolint:gosec // G115: terminal width from term.GetSize is a small non-negative int
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
	if snap == nil || snap.Frame == "" {
		return
	}

	var buf strings.Builder
	buf.WriteString("\x1b[?2026h")
	buf.WriteString("\x1b[?25l")
	buf.WriteString("\x1b[H\x1b[2J")

	cursorYOffset := 0
	if chrome != nil {
		cursorYOffset = chrome.childRowOffset()
		if cursorYOffset > 0 {
			fmt.Fprintf(&buf, "\x1b[%d;1H", cursorYOffset+1)
		}
	}

	buf.WriteString(snap.Frame)

	if chrome != nil {
		chrome.render(&buf)
	}

	fmt.Fprintf(&buf, "\x1b[%d;%dH", snap.CursorY+1+cursorYOffset, snap.CursorX+1)

	if snap.CursorVisible {
		buf.WriteString("\x1b[?25h")
	}

	buf.WriteString("\x1b[?2026l")

	_, _ = w.Write([]byte(buf.String()))
}

type experimentalAttachChrome struct {
	mu       sync.Mutex
	readOnly bool
	info     statusBarInfo
	rows     int
	cols     int
	position string
}

func newExperimentalAttachChrome(info protocol.SessionInfo, readOnly bool, position string, rows, cols int) *experimentalAttachChrome {
	return &experimentalAttachChrome{
		readOnly: readOnly,
		info:     newStatusBarInfo(info, 0, protocol.FleetSummary{}),
		rows:     rows,
		cols:     cols,
		position: position,
	}
}

func (ch *experimentalAttachChrome) updateSize(rows, cols int) {
	ch.mu.Lock()
	ch.rows = rows
	ch.cols = cols
	ch.mu.Unlock()
}

func (ch *experimentalAttachChrome) updateInfo(info statusBarInfo) {
	ch.mu.Lock()
	ch.info = info
	ch.mu.Unlock()
}

func (ch *experimentalAttachChrome) childRowOffset() int {
	ch.mu.Lock()
	position := ch.position
	ch.mu.Unlock()

	if position == "top" {
		return 1
	}

	return 0
}

func (ch *experimentalAttachChrome) renderTo(w io.Writer) {
	var buf strings.Builder

	ch.render(&buf)

	_, _ = w.Write([]byte(buf.String()))
}

func (ch *experimentalAttachChrome) render(buf *strings.Builder) {
	ch.mu.Lock()
	readOnly := ch.readOnly
	info := ch.info
	rows := ch.rows
	cols := ch.cols
	position := ch.position
	ch.mu.Unlock()

	if rows < 1 || cols < 1 {
		return
	}

	row := rows
	if position == "top" {
		row = 1
	}

	line := formatStatusLine(info, cols)
	if readOnly {
		line = formatReadOnlyLine(info, cols)
	}

	fmt.Fprintf(buf, "\x1b[%d;1H%s", row, line)
}
