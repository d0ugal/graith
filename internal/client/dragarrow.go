package client

// Touch/hold-and-drag arrow-key support.
//
// When enabled, a press-and-hold of the left mouse button followed by a drag
// is translated into discrete arrow-key presses forwarded to the focused pane:
// dragging up/down/left/right emits Up/Down/Left/Right. This mirrors the
// gesture used by touch/mobile terminals where there is no physical arrow-key
// row and holding then dragging nudges the cursor.
//
// The translation is a pure pre-pass over the raw stdin byte stream (mirroring
// processKittyPrefix): recognized left-button SGR mouse sequences are consumed
// and replaced with the arrow-key escape sequences they map to; every other
// byte — including mouse-wheel scroll events and other buttons — is passed
// through untouched so existing mouse behavior is preserved.

// Arrow-key escape sequences (application-normal cursor keys). These contain no
// control bytes, so they flow through the prefix-key scanner unchanged.
var (
	arrowUp    = []byte("\x1b[A")
	arrowDown  = []byte("\x1b[B")
	arrowRight = []byte("\x1b[C")
	arrowLeft  = []byte("\x1b[D")
)

// SGR mouse button-code bits. The low two bits identify the button (0=left);
// bit 5 (32) marks a motion/drag report; bit 6 (64) marks a wheel event.
const (
	mouseButtonMask = 0b11
	mouseMotionBit  = 32
	mouseWheelBit   = 64
)

const defaultDragArrowThreshold = 2

// sgrMouseEvent is a parsed SGR (1006) mouse report: ESC [ < b ; col ; row (M|m).
type sgrMouseEvent struct {
	button  int
	col     int
	row     int
	release bool // true for the 'm' (button release) terminator
}

// parseSGRMouse parses an SGR mouse sequence at input[pos:]. It returns the
// decoded event, the byte length of the sequence, and whether parsing
// succeeded. The sequence form is: ESC [ < <button> ; <col> ; <row> (M|m).
func parseSGRMouse(input []byte, pos int) (sgrMouseEvent, int, bool) {
	if pos+3 >= len(input) || input[pos] != '\x1b' || input[pos+1] != '[' || input[pos+2] != '<' {
		return sgrMouseEvent{}, 0, false
	}

	i := pos + 3

	button, i, ok := parseUint(input, i)
	if !ok || i >= len(input) || input[i] != ';' {
		return sgrMouseEvent{}, 0, false
	}

	i++ // consume ';'

	col, i, ok := parseUint(input, i)
	if !ok || i >= len(input) || input[i] != ';' {
		return sgrMouseEvent{}, 0, false
	}

	i++ // consume ';'

	row, i, ok := parseUint(input, i)
	if !ok || i >= len(input) {
		return sgrMouseEvent{}, 0, false
	}

	term := input[i]
	if term != 'M' && term != 'm' {
		return sgrMouseEvent{}, 0, false
	}

	return sgrMouseEvent{
		button:  button,
		col:     col,
		row:     row,
		release: term == 'm',
	}, i - pos + 1, true
}

// parseUint reads a run of decimal digits starting at input[i], returning the
// value, the index past the digits, and whether at least one digit was read.
func parseUint(input []byte, i int) (int, int, bool) {
	start := i
	val := 0

	for i < len(input) && input[i] >= '0' && input[i] <= '9' {
		val = val*10 + int(input[i]-'0')
		i++
	}

	if i == start {
		return 0, i, false
	}

	return val, i, true
}

// dragArrowState is the pure gesture state machine. It is fed SGR mouse events
// and returns the arrow-key bytes the gesture should emit. It tracks an anchor
// position that advances by the threshold each time an arrow is emitted, so one
// continuous drag produces one discrete arrow press per threshold cells moved.
type dragArrowState struct {
	threshold int
	active    bool
	anchorCol int
	anchorRow int
}

func newDragArrowState(threshold int) *dragArrowState {
	if threshold < 1 {
		threshold = defaultDragArrowThreshold
	}

	return &dragArrowState{threshold: threshold}
}

// isLeftPress reports whether ev is a plain left-button press (no motion, no
// wheel) — the start of a drag gesture.
func (ev sgrMouseEvent) isLeftPress() bool {
	return !ev.release &&
		ev.button&mouseButtonMask == 0 &&
		ev.button&mouseMotionBit == 0 &&
		ev.button&mouseWheelBit == 0
}

// isLeftDrag reports whether ev is a motion report with the left button held.
func (ev sgrMouseEvent) isLeftDrag() bool {
	return !ev.release &&
		ev.button&mouseMotionBit != 0 &&
		ev.button&mouseWheelBit == 0 &&
		ev.button&mouseButtonMask == 0
}

// handles reports whether this event is part of the drag-arrow gesture and
// should therefore be consumed (swallowed) rather than forwarded to the pane.
// A left press, a left drag while active, and a release while active are all
// consumed; everything else (wheel scroll, other buttons, motion without an
// active gesture) is left untouched.
func (d *dragArrowState) handles(ev sgrMouseEvent) bool {
	if ev.isLeftPress() {
		return true
	}

	if !d.active {
		return false
	}

	return ev.release || ev.isLeftDrag()
}

// feed advances the state machine with one mouse event and returns any arrow-key
// bytes to emit. The caller should only pass events for which handles() is true.
func (d *dragArrowState) feed(ev sgrMouseEvent) []byte {
	switch {
	case ev.isLeftPress():
		d.active = true
		d.anchorCol = ev.col
		d.anchorRow = ev.row

		return nil

	case ev.release:
		d.active = false

		return nil

	case ev.isLeftDrag():
		return d.emitFor(ev.col, ev.row)

	default:
		return nil
	}
}

// emitFor computes the discrete arrow presses for a drag to (col, row). Only the
// dominant axis of the movement contributes (vertical breaks ties), so a drag
// registers as a single up/down/left/right direction rather than a mix; the
// minor axis is dropped by re-anchoring it. One arrow is emitted per threshold
// cells moved, so a long fast drag produces several arrows.
func (d *dragArrowState) emitFor(col, row int) []byte {
	dx := col - d.anchorCol
	dy := row - d.anchorRow

	absX, absY := abs(dx), abs(dy)
	if absX < d.threshold && absY < d.threshold {
		return nil
	}

	var (
		out   []byte
		steps int
		seq   []byte
	)

	if absY >= absX {
		// Vertical dominant.
		steps = absY / d.threshold

		if dy < 0 {
			seq = arrowUp
			d.anchorRow -= steps * d.threshold
		} else {
			seq = arrowDown
			d.anchorRow += steps * d.threshold
		}

		d.anchorCol = col // drop minor-axis drift
	} else {
		// Horizontal dominant.
		steps = absX / d.threshold

		if dx < 0 {
			seq = arrowLeft
			d.anchorCol -= steps * d.threshold
		} else {
			seq = arrowRight
			d.anchorCol += steps * d.threshold
		}

		d.anchorRow = row // drop minor-axis drift
	}

	for k := 0; k < steps; k++ {
		out = append(out, seq...)
	}

	return out
}

// process scans input for left-button SGR mouse sequences that make up the
// drag-arrow gesture, replacing each consumed sequence with the arrow-key bytes
// it maps to (often none) and leaving all other bytes untouched. It mirrors the
// copy-on-first-match strategy of processKittyPrefix to avoid allocating when
// there is nothing to translate.
func (d *dragArrowState) process(input []byte) []byte {
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

		if !d.handles(ev) {
			// Any other mouse event (wheel scroll, another button, no-button
			// motion) ends an in-progress gesture so a stale anchor can't leak
			// into a later drag. The event itself passes through untouched.
			d.active = false

			continue
		}

		if out == nil {
			out = make([]byte, 0, len(input))
		}

		out = append(out, input[copied:i]...)
		out = append(out, d.feed(ev)...)

		i += seqLen - 1
		copied = i + 1
	}

	if out == nil {
		return input
	}

	return append(out, input[copied:]...)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}

	return n
}
