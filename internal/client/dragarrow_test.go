package client

import (
	"testing"
)

func TestParseSGRMouse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		pos     int
		want    sgrMouseEvent
		wantLen int
		wantOK  bool
	}{
		{
			name:    "left press",
			input:   "\x1b[<0;10;5M",
			want:    sgrMouseEvent{button: 0, col: 10, row: 5, release: false},
			wantLen: len("\x1b[<0;10;5M"),
			wantOK:  true,
		},
		{
			name:    "left drag motion",
			input:   "\x1b[<32;12;8M",
			want:    sgrMouseEvent{button: 32, col: 12, row: 8, release: false},
			wantLen: len("\x1b[<32;12;8M"),
			wantOK:  true,
		},
		{
			name:    "release lowercase m",
			input:   "\x1b[<0;3;4m",
			want:    sgrMouseEvent{button: 0, col: 3, row: 4, release: true},
			wantLen: len("\x1b[<0;3;4m"),
			wantOK:  true,
		},
		{
			name:    "wheel up",
			input:   "\x1b[<64;1;1M",
			want:    sgrMouseEvent{button: 64, col: 1, row: 1, release: false},
			wantLen: len("\x1b[<64;1;1M"),
			wantOK:  true,
		},
		{name: "not an escape", input: "abc", wantOK: false},
		{name: "not SGR mouse (arrow key)", input: "\x1b[A", wantOK: false},
		{name: "missing terminator", input: "\x1b[<0;1;1", wantOK: false},
		{name: "bad terminator", input: "\x1b[<0;1;1X", wantOK: false},
		{name: "missing field", input: "\x1b[<0;1M", wantOK: false},
		{name: "empty field", input: "\x1b[<;1;1M", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, gotLen, ok := parseSGRMouse([]byte(tt.input), tt.pos)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}

			if !tt.wantOK {
				return
			}

			if ev != tt.want {
				t.Errorf("event = %+v, want %+v", ev, tt.want)
			}

			if gotLen != tt.wantLen {
				t.Errorf("len = %d, want %d", gotLen, tt.wantLen)
			}
		})
	}
}

func TestParseSGRMouseAtOffset(t *testing.T) {
	input := []byte("xy\x1b[<0;7;9M")

	ev, gotLen, ok := parseSGRMouse(input, 2)
	if !ok {
		t.Fatal("expected parse to succeed at offset 2")
	}

	if ev != (sgrMouseEvent{button: 0, col: 7, row: 9}) {
		t.Errorf("event = %+v", ev)
	}

	if gotLen != len("\x1b[<0;7;9M") {
		t.Errorf("len = %d", gotLen)
	}
}

func TestSGRMouseEventClassification(t *testing.T) {
	tests := []struct {
		name      string
		ev        sgrMouseEvent
		wantPress bool
		wantDrag  bool
	}{
		{"left press", sgrMouseEvent{button: 0}, true, false},
		{"left drag", sgrMouseEvent{button: mouseMotionBit}, false, true},
		{"release is neither", sgrMouseEvent{button: 0, release: true}, false, false},
		{"wheel not press", sgrMouseEvent{button: mouseWheelBit}, false, false},
		{"wheel motion not drag", sgrMouseEvent{button: mouseWheelBit | mouseMotionBit}, false, false},
		{"right press not left", sgrMouseEvent{button: 2}, false, false},
		{"right drag not left", sgrMouseEvent{button: mouseMotionBit | 2}, false, false},
		{"no-button motion not drag", sgrMouseEvent{button: mouseMotionBit | 3}, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ev.isLeftPress(); got != tt.wantPress {
				t.Errorf("isLeftPress = %v, want %v", got, tt.wantPress)
			}

			if got := tt.ev.isLeftDrag(); got != tt.wantDrag {
				t.Errorf("isLeftDrag = %v, want %v", got, tt.wantDrag)
			}
		})
	}
}

func TestNewDragArrowStateThresholdDefault(t *testing.T) {
	if got := newDragArrowState(0).threshold; got != defaultDragArrowThreshold {
		t.Errorf("threshold for 0 = %d, want %d", got, defaultDragArrowThreshold)
	}

	if got := newDragArrowState(-5).threshold; got != defaultDragArrowThreshold {
		t.Errorf("threshold for -5 = %d, want %d", got, defaultDragArrowThreshold)
	}

	if got := newDragArrowState(4).threshold; got != 4 {
		t.Errorf("threshold for 4 = %d, want 4", got)
	}
}

// TestDragDirectionMapping covers drag-delta -> direction -> arrow-key mapping,
// the movement threshold, and the one-arrow-per-threshold debounce.
func TestDragDirectionMapping(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		startCol  int
		startRow  int
		dragCol   int
		dragRow   int
		want      string
	}{
		{"below threshold emits nothing", 2, 10, 5, 11, 6, ""},
		{"one cell down at threshold", 2, 10, 5, 10, 7, "\x1b[B"},
		{"drag up", 2, 10, 5, 10, 3, "\x1b[A"},
		{"drag right", 2, 10, 5, 12, 5, "\x1b[C"},
		{"drag left", 2, 10, 5, 8, 5, "\x1b[D"},
		{"long drag down = repeated arrows", 2, 10, 5, 10, 9, "\x1b[B\x1b[B"},
		{"long drag right = repeated arrows", 2, 10, 5, 16, 5, "\x1b[C\x1b[C\x1b[C"},
		{"diagonal vertical dominant", 2, 10, 5, 12, 10, "\x1b[B\x1b[B"},
		{"diagonal horizontal dominant", 2, 5, 5, 12, 6, "\x1b[C\x1b[C\x1b[C"},
		{"tie favors vertical", 2, 5, 5, 8, 8, "\x1b[B"},
		{"threshold 1 is sensitive", 1, 5, 5, 6, 5, "\x1b[C"},
		{"threshold 3 needs more travel", 3, 5, 5, 7, 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDragArrowState(tt.threshold)

			if out := d.feed(sgrMouseEvent{button: 0, col: tt.startCol, row: tt.startRow}); out != nil {
				t.Fatalf("press should emit nothing, got %q", out)
			}

			out := d.feed(sgrMouseEvent{button: mouseMotionBit, col: tt.dragCol, row: tt.dragRow})
			if string(out) != tt.want {
				t.Errorf("drag emitted %q, want %q", out, tt.want)
			}
		})
	}
}

// TestDragIncrementalDebounce verifies that a continuous drag emits one arrow
// each time the accumulated movement crosses another threshold boundary.
func TestDragIncrementalDebounce(t *testing.T) {
	d := newDragArrowState(2)
	d.feed(sgrMouseEvent{button: 0, col: 10, row: 10})

	// Move down one cell: below threshold, nothing yet.
	if out := d.feed(sgrMouseEvent{button: mouseMotionBit, col: 10, row: 11}); len(out) != 0 {
		t.Fatalf("1-cell move emitted %q, want none", out)
	}

	// Cross the threshold: one Down.
	if out := d.feed(sgrMouseEvent{button: mouseMotionBit, col: 10, row: 12}); string(out) != "\x1b[B" {
		t.Fatalf("2-cell move emitted %q, want one Down", out)
	}

	// Another cell: still below the *next* threshold.
	if out := d.feed(sgrMouseEvent{button: mouseMotionBit, col: 10, row: 13}); len(out) != 0 {
		t.Fatalf("3-cell move emitted %q, want none", out)
	}

	// Cross again: second Down.
	if out := d.feed(sgrMouseEvent{button: mouseMotionBit, col: 10, row: 14}); string(out) != "\x1b[B" {
		t.Fatalf("4-cell move emitted %q, want one Down", out)
	}
}

func TestDragReleaseClearsGesture(t *testing.T) {
	d := newDragArrowState(2)
	d.feed(sgrMouseEvent{button: 0, col: 10, row: 10})

	if !d.active {
		t.Fatal("gesture should be active after press")
	}

	d.feed(sgrMouseEvent{button: 0, col: 10, row: 12, release: true})

	if d.active {
		t.Fatal("gesture should be inactive after release")
	}

	// A drag after release (without a fresh press) must not emit — handles()
	// gates it out.
	if d.handles(sgrMouseEvent{button: mouseMotionBit, col: 10, row: 20}) {
		t.Error("drag after release should not be handled")
	}
}

func TestDragHandlesGating(t *testing.T) {
	d := newDragArrowState(2)

	// Idle: only a left press is handled.
	if !d.handles(sgrMouseEvent{button: 0}) {
		t.Error("idle: left press should be handled")
	}

	if d.handles(sgrMouseEvent{button: mouseMotionBit, col: 1, row: 1}) {
		t.Error("idle: left drag should not be handled")
	}

	if d.handles(sgrMouseEvent{button: mouseWheelBit}) {
		t.Error("wheel should never be handled")
	}

	// Active: drag and release are handled.
	d.feed(sgrMouseEvent{button: 0, col: 5, row: 5})

	if !d.handles(sgrMouseEvent{button: mouseMotionBit, col: 5, row: 8}) {
		t.Error("active: left drag should be handled")
	}

	if !d.handles(sgrMouseEvent{button: 0, release: true}) {
		t.Error("active: release should be handled")
	}
}

func TestProcessTranslatesDrag(t *testing.T) {
	d := newDragArrowState(2)

	// Press at (10,5) then drag to (10,9): press swallowed, drag -> two Downs.
	input := []byte("\x1b[<0;10;5M\x1b[<32;10;9M")

	got := string(d.process(input))
	if got != "\x1b[B\x1b[B" {
		t.Errorf("process = %q, want two Down arrows", got)
	}
}

func TestProcessPreservesSurroundingBytes(t *testing.T) {
	d := newDragArrowState(2)
	// A press is swallowed but the surrounding literal bytes survive.
	input := []byte("ab\x1b[<0;3;3Mcd")

	if got := string(d.process(input)); got != "abcd" {
		t.Errorf("process = %q, want %q", got, "abcd")
	}
}

func TestProcessPassesThroughWheelScroll(t *testing.T) {
	d := newDragArrowState(2)
	// Mouse-wheel scroll must never be intercepted.
	input := "\x1b[<64;5;5M\x1b[<65;5;5M"

	if got := string(d.process([]byte(input))); got != input {
		t.Errorf("wheel scroll changed: got %q, want %q", got, input)
	}
}

func TestProcessPassesThroughNonMouse(t *testing.T) {
	d := newDragArrowState(2)

	for _, in := range []string{"hello world", "\x1b[A\x1b[B", "\x1b[<0;1;1", "plain text\n"} {
		if got := string(d.process([]byte(in))); got != in {
			t.Errorf("process(%q) = %q, want unchanged", in, got)
		}
	}
}

// TestProcessClearsStaleAnchorOnOtherEvent ensures an interrupting mouse event
// ends the gesture so a later drag can't resume from a stale anchor.
func TestProcessClearsStaleAnchorOnOtherEvent(t *testing.T) {
	d := newDragArrowState(2)

	// Start a gesture.
	d.process([]byte("\x1b[<0;10;10M"))

	if !d.active {
		t.Fatal("gesture should be active after press")
	}

	// A wheel scroll interrupts and clears the gesture, passing through.
	if got := string(d.process([]byte("\x1b[<64;10;10M"))); got != "\x1b[<64;10;10M" {
		t.Errorf("wheel event = %q, want passthrough", got)
	}

	if d.active {
		t.Fatal("interrupting event should clear the active gesture")
	}

	// A subsequent drag without a fresh press emits nothing and passes through.
	if got := string(d.process([]byte("\x1b[<32;10;20M"))); got != "\x1b[<32;10;20M" {
		t.Errorf("stale drag = %q, want passthrough (no arrows)", got)
	}
}

func TestProcessMultipleGesturesInOneBuffer(t *testing.T) {
	d := newDragArrowState(2)
	// press, drag right (two Rights), release, then press + drag up (one Up).
	input := "\x1b[<0;5;5M" + "\x1b[<32;9;5M" + "\x1b[<0;9;5m" +
		"\x1b[<0;9;5M" + "\x1b[<32;9;3M"

	want := "\x1b[C\x1b[C" + "\x1b[A"
	if got := string(d.process([]byte(input))); got != want {
		t.Errorf("process = %q, want %q", got, want)
	}
}

func TestAbs(t *testing.T) {
	if abs(-3) != 3 || abs(3) != 3 || abs(0) != 0 {
		t.Error("abs failed")
	}
}
