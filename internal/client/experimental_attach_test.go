package client

import (
	"bytes"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestExperimentalChromeFrameLayout(t *testing.T) {
	tests := map[string]struct {
		rows          int
		cols          int
		position      string
		chromeEnabled bool
		wantChildCols int
		wantChildRows int
		wantChromeRow int
		wantChromeOK  bool
		wantOrigin    string
		wantCursorRow int
	}{
		"bottom chrome reserves last row": {
			rows:          24,
			cols:          80,
			position:      "bottom",
			chromeEnabled: true,
			wantChildCols: 80,
			wantChildRows: 23,
			wantChromeRow: 24,
			wantChromeOK:  true,
			wantCursorRow: 3,
		},
		"top chrome reserves first row": {
			rows:          24,
			cols:          80,
			position:      "top",
			chromeEnabled: true,
			wantChildCols: 80,
			wantChildRows: 23,
			wantChromeRow: 1,
			wantChromeOK:  true,
			wantOrigin:    "\x1b[2;1H",
			wantCursorRow: 4,
		},
		"disabled chrome gives child full frame": {
			rows:          24,
			cols:          80,
			position:      "top",
			chromeEnabled: false,
			wantChildCols: 80,
			wantChildRows: 24,
			wantCursorRow: 3,
		},
		"one row suppresses chrome reservation": {
			rows:          1,
			cols:          80,
			position:      "top",
			chromeEnabled: true,
			wantChildCols: 80,
			wantChildRows: 1,
			wantCursorRow: 1,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			frame := newExperimentalChromeFrame(test.rows, test.cols, test.position, test.chromeEnabled)

			gotCols, gotRows := frame.childSize()
			if gotCols != test.wantChildCols || gotRows != test.wantChildRows {
				t.Fatalf("child size = (%d,%d), want (%d,%d)", gotCols, gotRows, test.wantChildCols, test.wantChildRows)
			}

			gotChromeRow, gotChromeOK := frame.chromeLineRow()
			if gotChromeRow != test.wantChromeRow || gotChromeOK != test.wantChromeOK {
				t.Fatalf("chrome row = (%d,%v), want (%d,%v)", gotChromeRow, gotChromeOK, test.wantChromeRow, test.wantChromeOK)
			}

			if gotOrigin := frame.childOriginSequence(); gotOrigin != test.wantOrigin {
				t.Fatalf("child origin sequence = %q, want %q", gotOrigin, test.wantOrigin)
			}

			gotCursorRow, gotCursorCol := frame.childCursorPosition(2, 4)
			if gotCursorRow != test.wantCursorRow || gotCursorCol != 5 {
				t.Fatalf("child cursor position = (%d,%d), want (%d,5)", gotCursorRow, gotCursorCol, test.wantCursorRow)
			}
		})
	}
}

func TestExperimentalChromeFrameOuterToChildCells(t *testing.T) {
	tests := map[string]struct {
		frame        experimentalChromeFrame
		outerCol     int
		outerRow     int
		wantChildCol int
		wantChildRow int
		wantOK       bool
	}{
		"top chrome shifts child row": {
			frame:        newExperimentalChromeFrame(24, 80, "top", true),
			outerCol:     7,
			outerRow:     2,
			wantChildCol: 7,
			wantChildRow: 1,
			wantOK:       true,
		},
		"top chrome owns first row": {
			frame:    newExperimentalChromeFrame(24, 80, "top", true),
			outerCol: 7,
			outerRow: 1,
		},
		"bottom chrome owns last row": {
			frame:    newExperimentalChromeFrame(24, 80, "bottom", true),
			outerCol: 7,
			outerRow: 24,
		},
		"bottom chrome leaves child rows unchanged": {
			frame:        newExperimentalChromeFrame(24, 80, "bottom", true),
			outerCol:     7,
			outerRow:     23,
			wantChildCol: 7,
			wantChildRow: 23,
			wantOK:       true,
		},
		"disabled chrome accepts last row": {
			frame:        newExperimentalChromeFrame(24, 80, "bottom", false),
			outerCol:     7,
			outerRow:     24,
			wantChildCol: 7,
			wantChildRow: 24,
			wantOK:       true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gotCol, gotRow, gotOK := test.frame.outerToChildCell(test.outerCol, test.outerRow)
			if gotCol != test.wantChildCol || gotRow != test.wantChildRow || gotOK != test.wantOK {
				t.Fatalf("outerToChildCell() = (%d,%d,%v), want (%d,%d,%v)", gotCol, gotRow, gotOK, test.wantChildCol, test.wantChildRow, test.wantOK)
			}
		})
	}
}

func TestTranslateSGRMouseForExperimentalFrame(t *testing.T) {
	tests := map[string]struct {
		frame experimentalChromeFrame
		input string
		want  string
	}{
		"top chrome shifts child mouse row": {
			frame: newExperimentalChromeFrame(24, 80, "top", true),
			input: "a\x1b[<0;10;5Mb",
			want:  "a\x1b[<0;10;4Mb",
		},
		"top chrome drops owned row": {
			frame: newExperimentalChromeFrame(24, 80, "top", true),
			input: "a\x1b[<0;10;1Mb",
			want:  "ab",
		},
		"bottom chrome drops owned row": {
			frame: newExperimentalChromeFrame(24, 80, "bottom", true),
			input: "a\x1b[<0;10;24Mb",
			want:  "ab",
		},
		"top chrome release without child press drops owned row": {
			frame: newExperimentalChromeFrame(24, 80, "top", true),
			input: "a\x1b[<0;10;1mb",
			want:  "ab",
		},
		"bottom chrome release without child press drops owned row": {
			frame: newExperimentalChromeFrame(24, 80, "bottom", true),
			input: "a\x1b[<0;10;24mb",
			want:  "ab",
		},
		"bottom chrome leaves child mouse row unchanged": {
			frame: newExperimentalChromeFrame(24, 80, "bottom", true),
			input: "a\x1b[<0;10;23Mb",
			want:  "a\x1b[<0;10;23Mb",
		},
		"release terminator is preserved": {
			frame: newExperimentalChromeFrame(24, 80, "top", true),
			input: "a\x1b[<0;10;5mb",
			want:  "a\x1b[<0;10;4mb",
		},
		"disabled chrome leaves last row report unchanged": {
			frame: newExperimentalChromeFrame(24, 80, "bottom", false),
			input: "a\x1b[<0;10;24Mb",
			want:  "a\x1b[<0;10;24Mb",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := string(translateSGRMouseForExperimentalFrame([]byte(test.input), test.frame)); got != test.want {
				t.Fatalf("translateSGRMouseForExperimentalFrame() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExperimentalChromeMouseReleaseClearsDragArrow(t *testing.T) {
	chrome := newExperimentalAttachChrome(protocol.SessionInfo{
		Name:   "drag-braw",
		Agent:  "codex",
		Status: "running",
	}, false, "bottom", 24, 80)
	dragArrow := newDragArrowState(2)

	translated := append([]byte{}, chrome.translateMouseInput([]byte("\x1b[<0;10;23M"))...)

	translated = append(translated, chrome.translateMouseInput([]byte("\x1b[<0;10;24m"))...)
	if got, want := string(translated), "\x1b[<0;10;23M\x1b[<0;10;23m"; got != want {
		t.Fatalf("translated mouse reports = %q, want %q", got, want)
	}

	if out := dragArrow.process(translated); len(out) != 0 {
		t.Fatalf("press/release should not emit arrow bytes, got %q", string(out))
	}

	if dragArrow.active {
		t.Fatal("release clamped from chrome row did not clear drag-arrow state")
	}
}

func TestExperimentalChromeMouseReleaseFromOwnedRowRequiresChildPress(t *testing.T) {
	tests := map[string]struct {
		position string
		pressRow int
	}{
		"top chrome": {
			position: "top",
			pressRow: 1,
		},
		"bottom chrome": {
			position: "bottom",
			pressRow: 24,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			chrome := newExperimentalAttachChrome(protocol.SessionInfo{
				Name:   "mouse-braw",
				Agent:  "codex",
				Status: "running",
			}, false, test.position, 24, 80)

			press := chrome.translateMouseInput(formatSGRMouse(sgrMouseEvent{
				button: 0,
				col:    10,
				row:    test.pressRow,
			}))
			release := chrome.translateMouseInput(formatSGRMouse(sgrMouseEvent{
				button:  0,
				col:     10,
				row:     test.pressRow,
				release: true,
			}))

			if len(press) != 0 || len(release) != 0 {
				t.Fatalf("chrome-only press/release leaked to child: press=%q release=%q", string(press), string(release))
			}
		})
	}
}

func TestExperimentalAttachEnterSequenceResetsScrollRegion(t *testing.T) {
	out := experimentalAttachEnterSequence()
	if !strings.Contains(out, "\x1b[?1049h\x1b[r") {
		t.Fatalf("enter sequence should reset scroll region after entering alternate screen, got %q", out)
	}
}

func TestWriteExperimentalScreenSnapshotPreservesBottomChromeChildCursor(t *testing.T) {
	var buf bytes.Buffer

	chrome := newExperimentalAttachChrome(protocol.SessionInfo{
		Name:   "bottom-braw",
		Agent:  "codex",
		Status: "running",
	}, false, "bottom", 24, 80)

	writeExperimentalScreenSnapshotWithChrome(&buf, &protocol.ScreenSnapshotResponseMsg{
		SessionID:     "canny",
		Frame:         "hello bothy",
		CursorX:       4,
		CursorY:       2,
		CursorVisible: true,
		Cols:          80,
		Rows:          23,
	}, chrome)

	out := buf.String()
	if !strings.Contains(out, "\x1b[24;1H") || !strings.Contains(out, "bottom-braw") {
		t.Fatalf("bottom experimental status chrome missing from output %q", out)
	}

	if strings.Contains(out, "\x1b[2;1Hhello bothy") {
		t.Fatalf("bottom chrome should not shift the child frame: %q", out)
	}

	if !strings.Contains(out, "\x1b[3;5H") {
		t.Fatalf("bottom chrome should preserve child cursor row, got %q", out)
	}
}

func TestWriteExperimentalScreenSnapshotSuppressesChromeWhenFrameTooShort(t *testing.T) {
	var buf bytes.Buffer

	chrome := newExperimentalAttachChrome(protocol.SessionInfo{
		Name:   "short-braw",
		Agent:  "codex",
		Status: "running",
	}, true, "top", 1, 80)

	writeExperimentalScreenSnapshotWithChrome(&buf, &protocol.ScreenSnapshotResponseMsg{
		SessionID:     "canny",
		Frame:         "hello bothy",
		CursorVisible: true,
		Cols:          80,
		Rows:          1,
	}, chrome)

	out := buf.String()
	if strings.Contains(out, "short-braw") || strings.Contains(out, "READ-ONLY") {
		t.Fatalf("one-row terminal should not render chrome over child content: %q", out)
	}

	if strings.Contains(out, "\x1b[2;1H") {
		t.Fatalf("one-row terminal should not shift child content below chrome: %q", out)
	}
}

func TestWriteExperimentalScreenSnapshotClipsChildFrameToReservedViewport(t *testing.T) {
	tests := map[string]struct {
		position        string
		wantChromeRow   string
		wantCursorRow   string
		wantChildOrigin string
	}{
		"bottom chrome clips before reserved row": {
			position:      "bottom",
			wantChromeRow: "\x1b[3;1H",
			wantCursorRow: "\x1b[2;3H",
		},
		"top chrome clips after shifted child viewport": {
			position:        "top",
			wantChromeRow:   "\x1b[1;1H",
			wantCursorRow:   "\x1b[3;3H",
			wantChildOrigin: "\x1b[2;1H",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer

			chrome := newExperimentalAttachChrome(protocol.SessionInfo{
				Name:   "clip-braw",
				Agent:  "codex",
				Status: "running",
			}, false, test.position, 3, 80)

			writeExperimentalScreenSnapshotWithChrome(&buf, &protocol.ScreenSnapshotResponseMsg{
				SessionID:     "canny",
				Frame:         "row1\r\nrow2\r\nrow3\x1b[0m",
				CursorX:       2,
				CursorY:       2,
				CursorVisible: true,
				Cols:          80,
				Rows:          3,
			}, chrome)

			out := buf.String()
			if !strings.Contains(out, "row1\r\nrow2\x1b[0m") {
				t.Fatalf("child frame was not clipped to two rows: %q", out)
			}

			if strings.Contains(out, "row3") {
				t.Fatalf("child frame wrote through reserved chrome row: %q", out)
			}

			if !strings.Contains(out, test.wantChromeRow) || !strings.Contains(out, "clip-braw") {
				t.Fatalf("chrome line missing from clipped repaint: %q", out)
			}

			if test.wantChildOrigin != "" && !strings.Contains(out, test.wantChildOrigin) {
				t.Fatalf("top chrome did not shift child origin: %q", out)
			}

			if !strings.Contains(out, test.wantCursorRow) {
				t.Fatalf("cursor was not clamped inside child viewport, got %q", out)
			}
		})
	}
}
