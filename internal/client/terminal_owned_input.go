package client

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/d0ugal/graith/internal/protocol"
)

const (
	focusInSequence       = "\x1b[I"
	focusOutSequence      = "\x1b[O"
	bracketedPasteStart   = "\x1b[200~"
	bracketedPasteEnd     = "\x1b[201~"
	applicationCursorUp   = "\x1bOA"
	applicationCursorDown = "\x1bOB"
)

type terminalOwnedChildArea struct {
	topOffset int
	cols      int
	rows      int
}

type terminalOwnedInputRouter struct {
	mu                 sync.Mutex
	modes              protocol.TerminalInputModes
	childCols          int
	childRows          int
	chrome             *terminalOwnedAttachChrome
	modeMirror         *terminalOwnedTerminalModeMirror
	localHistoryScroll func(delta int) bool
	readOnly           bool
}

func newTerminalOwnedInputRouter(
	chrome *terminalOwnedAttachChrome,
	modeMirror *terminalOwnedTerminalModeMirror,
	localHistoryScroll func(delta int) bool,
) *terminalOwnedInputRouter {
	return &terminalOwnedInputRouter{
		modes:              normalizeTerminalInputModes(nil),
		chrome:             chrome,
		modeMirror:         modeMirror,
		localHistoryScroll: localHistoryScroll,
	}
}

func (r *terminalOwnedInputRouter) updateSnapshot(snap *protocol.ScreenSnapshotResponseMsg) {
	if r == nil || snap == nil {
		return
	}

	modes := normalizeTerminalInputModes(snap.InputModes)

	r.mu.Lock()
	r.modes = modes
	r.childCols = snap.Cols
	r.childRows = snap.Rows
	captureLocalHistory := r.localHistoryScroll != nil
	r.mu.Unlock()

	if r.modeMirror != nil && !r.readOnly {
		r.modeMirror.apply(modes, captureLocalHistory)
	}
}

func (r *terminalOwnedInputRouter) childMouseTracking() bool {
	if r == nil {
		return false
	}

	r.mu.Lock()
	modes := r.modes
	r.mu.Unlock()

	return canRouteChildMouse(modes)
}

func (r *terminalOwnedInputRouter) keyboardLocked() bool {
	if r == nil {
		return false
	}

	r.mu.Lock()
	locked := r.modes.KeyboardLocked
	r.mu.Unlock()

	return locked
}

func (r *terminalOwnedInputRouter) process(input []byte) []byte {
	if r == nil || len(input) == 0 {
		return input
	}

	modes, area, localHistoryScroll := r.snapshot()

	var (
		out   []byte
		start int
	)

	replace := func(i, seqLen int, replacement []byte) {
		if out == nil {
			out = make([]byte, 0, len(input)+len(replacement))
		}

		out = append(out, input[start:i]...)
		out = append(out, replacement...)
		start = i + seqLen
	}

	for i := 0; i < len(input); i++ {
		if ev, seqLen, ok := parseSGRMouse(input, i); ok {
			replace(i, seqLen, routeTerminalOwnedMouse(ev, modes, area, localHistoryScroll))
			i += seqLen - 1

			continue
		}

		if hasSequence(input, i, focusInSequence) {
			var replacement []byte
			if modes.Focus {
				replacement = []byte(focusInSequence)
			}

			replace(i, len(focusInSequence), replacement)
			i += len(focusInSequence) - 1

			continue
		}

		if hasSequence(input, i, focusOutSequence) {
			var replacement []byte
			if modes.Focus {
				replacement = []byte(focusOutSequence)
			}

			replace(i, len(focusOutSequence), replacement)
			i += len(focusOutSequence) - 1

			continue
		}

		if hasSequence(input, i, bracketedPasteStart) {
			var replacement []byte
			if modes.BracketedPaste {
				replacement = []byte(bracketedPasteStart)
			}

			replace(i, len(bracketedPasteStart), replacement)
			i += len(bracketedPasteStart) - 1

			continue
		}

		if hasSequence(input, i, bracketedPasteEnd) {
			var replacement []byte
			if modes.BracketedPaste {
				replacement = []byte(bracketedPasteEnd)
			}

			replace(i, len(bracketedPasteEnd), replacement)
			i += len(bracketedPasteEnd) - 1
		}
	}

	if out == nil {
		return input
	}

	out = append(out, input[start:]...)

	return out
}

func (r *terminalOwnedInputRouter) snapshot() (
	protocol.TerminalInputModes,
	terminalOwnedChildArea,
	func(delta int) bool,
) {
	r.mu.Lock()
	modes := r.modes
	childCols := r.childCols
	childRows := r.childRows
	chrome := r.chrome
	localHistoryScroll := r.localHistoryScroll
	r.mu.Unlock()

	return modes, terminalOwnedChromeChildArea(chrome, childCols, childRows), localHistoryScroll
}

func normalizeTerminalInputModes(m *protocol.TerminalInputModes) protocol.TerminalInputModes {
	out := protocol.TerminalInputModes{
		MouseTracking: protocol.TerminalMouseTrackingNone,
		MouseFormat:   protocol.TerminalMouseFormatX10,
	}
	if m == nil {
		return out
	}

	out = *m
	switch out.MouseTracking {
	case protocol.TerminalMouseTrackingX10,
		protocol.TerminalMouseTrackingNormal,
		protocol.TerminalMouseTrackingButton,
		protocol.TerminalMouseTrackingAny:
	default:
		out.MouseTracking = protocol.TerminalMouseTrackingNone
	}

	switch out.MouseFormat {
	case protocol.TerminalMouseFormatUTF8,
		protocol.TerminalMouseFormatSGR,
		protocol.TerminalMouseFormatURxvt,
		protocol.TerminalMouseFormatSGRPixels:
	default:
		out.MouseFormat = protocol.TerminalMouseFormatX10
	}

	return out
}

func terminalOwnedChromeChildArea(chrome *terminalOwnedAttachChrome, childCols, childRows int) terminalOwnedChildArea {
	area := terminalOwnedChildArea{cols: childCols, rows: childRows}
	if chrome == nil {
		return area
	}

	_, _, frame := chrome.snapshot()
	frameCols, frameRows := frame.childSize()

	if top := frame.topRows(); top > 0 {
		area.topOffset = top
	}

	if area.rows <= 0 || frameRows > 0 && area.rows > frameRows {
		area.rows = frameRows
	}

	if area.cols <= 0 || frameCols > 0 && area.cols > frameCols {
		area.cols = frameCols
	}

	if area.rows < 0 {
		area.rows = 0
	}

	if area.cols < 0 {
		area.cols = 0
	}

	return area
}

func routeTerminalOwnedMouse(
	ev sgrMouseEvent,
	modes protocol.TerminalInputModes,
	area terminalOwnedChildArea,
	localHistoryScroll func(delta int) bool,
) []byte {
	translated, ok := translateMouseToChild(ev, area)
	if !ok {
		return nil
	}

	if ev.isWheel() && modes.MouseTracking == protocol.TerminalMouseTrackingNone {
		if modes.AlternateScreen && modes.AlternateScroll {
			return alternateScrollSequence(translated, modes)
		}

		if delta := translated.wheelDelta(); delta != 0 && localHistoryScroll != nil {
			_ = localHistoryScroll(delta)
		}

		return nil
	}

	if modes.MouseTracking == protocol.TerminalMouseTrackingNone || !shouldForwardMouse(ev, modes.MouseTracking) {
		return nil
	}

	if modes.MouseFormat == protocol.TerminalMouseFormatSGRPixels {
		return nil
	}

	return encodeMouseForChild(translated, modes)
}

func canRouteChildMouse(modes protocol.TerminalInputModes) bool {
	return modes.MouseTracking != "" &&
		modes.MouseTracking != protocol.TerminalMouseTrackingNone &&
		modes.MouseFormat != protocol.TerminalMouseFormatSGRPixels
}

func (ev sgrMouseEvent) isWheel() bool {
	return !ev.release && ev.button&mouseWheelBit != 0
}

func (ev sgrMouseEvent) wheelDelta() int {
	switch ev.button & 0x3 {
	case 0:
		return -1
	case 1:
		return 1
	default:
		return 0
	}
}

func shouldForwardMouse(ev sgrMouseEvent, tracking string) bool {
	switch tracking {
	case protocol.TerminalMouseTrackingAny:
		return true
	case protocol.TerminalMouseTrackingButton:
		return true
	case protocol.TerminalMouseTrackingNormal:
		return ev.release || ev.button&mouseMotionBit == 0
	case protocol.TerminalMouseTrackingX10:
		return !ev.release && ev.button&mouseMotionBit == 0
	default:
		return false
	}
}

func translateMouseToChild(ev sgrMouseEvent, area terminalOwnedChildArea) (sgrMouseEvent, bool) {
	if ev.col < 1 || ev.row < 1 {
		if !ev.isDragOrRelease() {
			return sgrMouseEvent{}, false
		}
	}

	ev.row -= area.topOffset

	if ev.isDragOrRelease() {
		return clampMouseToChild(ev, area), true
	}

	if ev.row < 1 {
		return sgrMouseEvent{}, false
	}

	if area.cols > 0 && ev.col > area.cols {
		return sgrMouseEvent{}, false
	}

	if area.rows > 0 && ev.row > area.rows {
		return sgrMouseEvent{}, false
	}

	return ev, true
}

func (ev sgrMouseEvent) isDragOrRelease() bool {
	return ev.release || ev.button&mouseMotionBit != 0
}

func clampMouseToChild(ev sgrMouseEvent, area terminalOwnedChildArea) sgrMouseEvent {
	if ev.col < 1 {
		ev.col = 1
	}

	if ev.row < 1 {
		ev.row = 1
	}

	if area.cols > 0 && ev.col > area.cols {
		ev.col = area.cols
	}

	if area.rows > 0 && ev.row > area.rows {
		ev.row = area.rows
	}

	return ev
}

func alternateScrollSequence(ev sgrMouseEvent, modes protocol.TerminalInputModes) []byte {
	switch ev.wheelDelta() {
	case -1:
		if modes.ApplicationCursorKeys {
			return []byte(applicationCursorUp)
		}

		return arrowUp
	case 1:
		if modes.ApplicationCursorKeys {
			return []byte(applicationCursorDown)
		}

		return arrowDown
	default:
		return nil
	}
}

func encodeMouseForChild(ev sgrMouseEvent, modes protocol.TerminalInputModes) []byte {
	switch modes.MouseFormat {
	case protocol.TerminalMouseFormatSGR:
		term := 'M'
		if ev.release {
			term = 'm'
		}

		return []byte(fmt.Sprintf("\x1b[<%d;%d;%d%c", ev.button, ev.col, ev.row, term))
	case protocol.TerminalMouseFormatURxvt:
		return []byte(fmt.Sprintf("\x1b[%d;%d;%dM", legacyMouseButtonCode(ev)+32, ev.col, ev.row))
	case protocol.TerminalMouseFormatUTF8:
		return encodeLegacyMouse(ev, true)
	default:
		return encodeLegacyMouse(ev, false)
	}
}

func legacyMouseButtonCode(ev sgrMouseEvent) int {
	if ev.release {
		return 3
	}

	return ev.button
}

func encodeLegacyMouse(ev sgrMouseEvent, utf8 bool) []byte {
	button := legacyMouseButtonCode(ev) + 32
	col := ev.col + 32
	row := ev.row + 32

	if button < 0 || col < 0 || row < 0 {
		return nil
	}

	if !utf8 {
		if button > 255 || col > 255 || row > 255 {
			return nil
		}

		return []byte{'\x1b', '[', 'M', byte(button), byte(col), byte(row)}
	}

	var b strings.Builder
	b.WriteString("\x1b[M")

	if !writeMouseUTF8Rune(&b, button) {
		return nil
	}

	if !writeMouseUTF8Rune(&b, col) {
		return nil
	}

	if !writeMouseUTF8Rune(&b, row) {
		return nil
	}

	return []byte(b.String())
}

func writeMouseUTF8Rune(b *strings.Builder, value int) bool {
	if value < 0 || value > utf8.MaxRune {
		return false
	}

	// #nosec G115 -- value is range-checked against utf8.MaxRune above.
	b.WriteRune(rune(value))

	return true
}

func hasSequence(input []byte, pos int, seq string) bool {
	return pos+len(seq) <= len(input) && string(input[pos:pos+len(seq)]) == seq
}

type terminalOwnedTerminalModeMirror struct {
	w     io.Writer
	state terminalOwnedOuterModes
}

type terminalOwnedOuterModes struct {
	mouseEnabled  bool
	mouseTracking string
	focus         bool
	paste         bool
	cursorKeys    bool
	keypad        bool
}

func newTerminalOwnedTerminalModeMirror(w io.Writer) *terminalOwnedTerminalModeMirror {
	return &terminalOwnedTerminalModeMirror{w: w}
}

func (m *terminalOwnedTerminalModeMirror) apply(modes protocol.TerminalInputModes, captureLocalHistory bool) {
	if m == nil || m.w == nil {
		return
	}

	want := terminalOwnedOuterModes{
		mouseTracking: protocol.TerminalMouseTrackingNormal,
		focus:         modes.Focus,
		paste:         modes.BracketedPaste,
		cursorKeys:    modes.ApplicationCursorKeys,
		keypad:        modes.ApplicationKeypad,
	}

	if canRouteChildMouse(modes) {
		want.mouseEnabled = true
		want.mouseTracking = modes.MouseTracking
	} else if captureLocalHistory || (modes.AlternateScreen && modes.AlternateScroll) {
		want.mouseEnabled = true
	}

	if want == m.state {
		return
	}

	var b strings.Builder
	if want.mouseEnabled != m.state.mouseEnabled || want.mouseTracking != m.state.mouseTracking {
		b.WriteString("\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l")

		if want.mouseEnabled {
			b.WriteString("\x1b[?1006h")

			switch want.mouseTracking {
			case protocol.TerminalMouseTrackingAny:
				b.WriteString("\x1b[?1003h")
			case protocol.TerminalMouseTrackingButton:
				b.WriteString("\x1b[?1002h")
			default:
				b.WriteString("\x1b[?1000h")
			}
		}
	}

	writeModeBool(&b, "\x1b[?1004h", "\x1b[?1004l", want.focus, m.state.focus)
	writeModeBool(&b, "\x1b[?2004h", "\x1b[?2004l", want.paste, m.state.paste)
	writeModeBool(&b, "\x1b[?1h", "\x1b[?1l", want.cursorKeys, m.state.cursorKeys)
	writeModeBool(&b, "\x1b=", "\x1b>", want.keypad, m.state.keypad)

	if b.Len() > 0 {
		_, _ = m.w.Write([]byte(b.String()))
	}

	m.state = want
}

func writeModeBool(buf *strings.Builder, enable, disable string, want, got bool) {
	if want == got {
		return
	}

	if want {
		buf.WriteString(enable)
		return
	}

	buf.WriteString(disable)
}
