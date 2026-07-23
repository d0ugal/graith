//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

type terminalGraphemeExpectation struct {
	cells   []string
	cursorX int
	preview string
}

type terminalAlternateExpectation struct {
	active   string
	restored string
}

type terminalBackendExpectations struct {
	issue1430Error   error
	resizePreviews   []string
	alternateScreens map[int]terminalAlternateExpectation
	graphemes        map[string]terminalGraphemeExpectation
	fragmented       map[string]terminalGraphemeExpectation
}

func commonTerminalBackendExpectations() terminalBackendExpectations {
	return terminalBackendExpectations{
		resizePreviews: []string{
			"canny brae bide\n\n\n",
			"ae b\nide",
			"ae bide\n\n",
			"ae bi\nde",
			"ae bide\n\n\n",
		},
		alternateScreens: map[int]terminalAlternateExpectation{
			47:   {active: "    bothy", restored: "brae"},
			1047: {active: "    bothy", restored: "brae"},
			1049: {active: "    bothy", restored: "brae"},
		},
		graphemes: map[string]terminalGraphemeExpectation{
			"combining":          {cells: []string{"e\u0301", "b"}, cursorX: 2, preview: "e\u0301b"},
			"zwj":                {cells: []string{"👩‍💻", "", "b"}, cursorX: 3, preview: "👩‍💻b"},
			"variation_selector": {cells: []string{"♥️", "", "b"}, cursorX: 3, preview: "♥️b"},
			"regional_indicator": {cells: []string{"🇬🇧", "", "b"}, cursorX: 3, preview: "🇬🇧b"},
			"wide":               {cells: []string{"你", "", "b"}, cursorX: 3, preview: "你b"},
		},
	}
}

// TestTerminalBackendCompatibilityCorpus drives the process-isolated native
// backend with the reduced compatibility corpus.
// No captured output, paths, or identifiers belong in this corpus.
func TestTerminalBackendCompatibilityCorpus(t *testing.T) {
	factory := selectedTerminalBackendFactory()
	t.Run(factory.name, func(t *testing.T) {
		t.Run("scrollback_hydration", func(t *testing.T) {
			testTerminalScrollbackHydration(t, factory)
		})
		t.Run("repeated_grow_shrink_resize", func(t *testing.T) {
			testTerminalRepeatedResize(t, factory)
		})
		t.Run("cursor_save_restore_and_visibility", func(t *testing.T) {
			testTerminalCursorState(t, factory)
		})
		t.Run("scroll_region", func(t *testing.T) {
			testTerminalScrollRegion(t, factory)
		})
		t.Run("alternate_screen_modes", func(t *testing.T) {
			testTerminalAlternateScreens(t, factory)
		})
		t.Run("erase_tabs_and_wrap", func(t *testing.T) {
			testTerminalEraseTabsAndWrap(t, factory)
		})
		t.Run("fragmented_control_strings_and_queries", func(t *testing.T) {
			testTerminalFragmentedControls(t, factory)
		})
		t.Run("graphemes_and_width", func(t *testing.T) {
			testTerminalGraphemes(t, factory)
		})
		t.Run("ris_and_grapheme_mode", func(t *testing.T) {
			testTerminalRISGraphemes(t, factory)
		})
		t.Run("styles_and_colors", func(t *testing.T) {
			testTerminalStylesAndColors(t, factory)
		})
		t.Run("issue_1430_reduced_regression", func(t *testing.T) {
			testTerminalIssue1430(t, factory)
		})
	})
}

func testTerminalScrollbackHydration(t *testing.T, factory terminalBackendFactory) {
	t.Helper()

	term := newTerminalBackendTestTerm(t, factory, 36, 5)
	fixture := syntheticTerminalWorkload(128 * 1024)

	n, err := term.Write(fixture)

	if err != nil || n != len(fixture) {
		t.Fatalf("hydrate terminal = (%d,%v), want (%d,nil)", n, err, len(fixture))
	}

	if got := renderPreview(term); !strings.Contains(got, "canny braw brae") {
		t.Fatalf("hydrated preview missing final synthetic rows: %q", got)
	}

	if err := term.Resize(52, 8); err != nil {
		t.Fatal(err)
	}

	if cols, rows := term.Size(); cols != 52 || rows != 8 {
		t.Errorf("size after hydrated grow = (%d,%d), want (52,8)", cols, rows)
	}

	if got := renderPreview(term); !strings.Contains(got, "canny braw brae") {
		t.Errorf("grow lost hydrated tail: %q", got)
	}
}

func testTerminalRepeatedResize(t *testing.T, factory terminalBackendFactory) {
	t.Helper()

	term := newTerminalBackendTestTerm(t, factory, 20, 2)
	write(t, term, "canny brae bide")

	sizes := [][2]int{{40, 4}, {4, 2}, {9, 3}, {5, 2}, {12, 4}}
	for i, size := range sizes {
		if err := term.Resize(size[0], size[1]); err != nil {
			t.Fatalf("resize to %dx%d: %v", size[0], size[1], err)
		}

		if cols, rows := term.Size(); cols != size[0] || rows != size[1] {
			t.Errorf("size step %d = (%d,%d), want (%d,%d)", i, cols, rows, size[0], size[1])
		}

		if got, want := renderPreview(term), factory.expectations.resizePreviews[i]; got != want {
			t.Errorf("preview after resize step %d = %q, want %q", i, got, want)
		}
	}
}

func testTerminalCursorState(t *testing.T, factory terminalBackendFactory) {
	t.Helper()

	term := newTerminalBackendTestTerm(t, factory, 20, 5)
	write(t, term, "\x1b[3;5H\x1b7\x1b[1;1Hbraw\x1b8")

	if x, y, visible := term.Cursor(); x != 4 || y != 2 || !visible {
		t.Errorf("restored cursor = (%d,%d,%t), want (4,2,true)", x, y, visible)
	}

	if got := line(t, term, 0); got != "braw" {
		t.Errorf("cursor fixture line = %q, want braw", got)
	}

	write(t, term, "\x1b[?25l")

	if _, _, visible := term.Cursor(); visible {
		t.Error("cursor remained visible after DECTCEM reset")
	}

	write(t, term, "\x1b[?25h")

	if x, y, visible := term.Cursor(); x != 4 || y != 2 || !visible {
		t.Errorf("shown cursor = (%d,%d,%t), want (4,2,true)", x, y, visible)
	}
}

func testTerminalScrollRegion(t *testing.T, factory terminalBackendFactory) {
	t.Helper()

	term := newTerminalBackendTestTerm(t, factory, 10, 5)
	write(t, term, "brae\r\ncroft\r\nhaar\r\nfash\r\nbide")
	write(t, term, "\x1b[2;4r\x1b[4;1H\n")

	want := []string{"brae", "haar", "fash", "", "bide"}
	for y, wantLine := range want {
		if got := line(t, term, y); got != wantLine {
			t.Errorf("scroll-region row %d = %q, want %q", y, got, wantLine)
		}
	}
}

func testTerminalAlternateScreens(t *testing.T, factory terminalBackendFactory) {
	t.Helper()

	for _, mode := range []int{47, 1047, 1049} {
		t.Run(fmt.Sprintf("mode_%d", mode), func(t *testing.T) {
			term := newTerminalBackendTestTerm(t, factory, 24, 3)
			write(t, term, "brae")
			write(t, term, fmt.Sprintf("\x1b[?%dh", mode))
			write(t, term, "bothy")

			want := factory.expectations.alternateScreens[mode]
			if got := line(t, term, 0); got != want.active {
				t.Errorf("active alternate line = %q, want %q", got, want.active)
			}

			write(t, term, fmt.Sprintf("\x1b[?%dl", mode))

			if got := line(t, term, 0); got != want.restored {
				t.Errorf("restored main line = %q, want %q", got, want.restored)
			}
		})
	}
}

func testTerminalEraseTabsAndWrap(t *testing.T, factory terminalBackendFactory) {
	t.Helper()

	t.Run("erase", func(t *testing.T) {
		term := newTerminalBackendTestTerm(t, factory, 12, 3)
		write(t, term, "dreich haar\r\ncanny bothy")
		write(t, term, "\x1b[1;8H\x1b[K\x1b[2;6H\x1b[1K")

		if got := line(t, term, 0); got != "dreich" {
			t.Errorf("erase-to-end line = %q, want dreich", got)
		}

		if got := line(t, term, 1); got != "      bothy" {
			t.Errorf("erase-to-start line = %q, want leading blanks plus bothy", got)
		}

		write(t, term, "\x1b[2J")

		for y := 0; y < 3; y++ {
			if got := line(t, term, y); got != "" {
				t.Errorf("row %d after display erase = %q, want empty", y, got)
			}
		}
	})

	t.Run("tab", func(t *testing.T) {
		term := newTerminalBackendTestTerm(t, factory, 16, 2)
		write(t, term, "brae\tbide")

		if got := line(t, term, 0); got != "brae    bide" {
			t.Errorf("tab-expanded line = %q, want %q", got, "brae    bide")
		}
	})

	t.Run("wrap", func(t *testing.T) {
		term := newTerminalBackendTestTerm(t, factory, 8, 3)
		write(t, term, "cannybrae")

		if got := line(t, term, 0); got != "cannybra" {
			t.Errorf("wrapped row 0 = %q, want cannybra", got)
		}

		if got := line(t, term, 1); got != "e" {
			t.Errorf("wrapped row 1 = %q, want e", got)
		}

		if x, y, _ := term.Cursor(); x != 1 || y != 1 {
			t.Errorf("wrapped cursor = (%d,%d), want (1,1)", x, y)
		}
	})
}

func testTerminalFragmentedControls(t *testing.T, factory terminalBackendFactory) {
	t.Helper()

	term := newTerminalBackendTestTerm(t, factory, 40, 2)
	write(t, term, "braw\x1b[")

	if got := line(t, term, 0); got != "braw" {
		t.Errorf("incomplete CSI changed line: %q", got)
	}

	write(t, term, "31mcroft")

	if got := term.Cell(4, 0).Style.FG; got != (Color{Kind: ColorIndexed, Value: 1}) {
		t.Errorf("fragmented CSI color = %+v, want palette red", got)
	}

	write(t, term, "\x1b]0;dreich")

	if got := line(t, term, 0); got != "brawcroft" {
		t.Errorf("incomplete OSC changed line: %q", got)
	}

	write(t, term, "\x07")
	write(t, term, "\x1bP1;2|thrawn")

	if got := line(t, term, 0); got != "brawcroft" {
		t.Errorf("incomplete DCS changed line: %q", got)
	}

	write(t, term, "\x1b\\")

	// Cancel fragmented control strings, ignore an unknown oversized CSI,
	// and drain a burst of device queries without changing the visible model.
	for _, chunk := range []string{
		"\x1b[999;999;", "\x18",
		"\x1b]2;haar", "\x18",
		"\x1bPqblether", "\x18",
		"\x1b[999;999z",
	} {
		write(t, term, chunk)
	}

	for i := 0; i < 32; i++ {
		write(t, term, "\x1b[6n\x1b[c\x1b[5n")
	}

	write(t, term, "\x1b[0m bothy")

	if got := line(t, term, 0); got != "brawcroft bothy" {
		t.Errorf("line after malformed controls and queries = %q, want %q", got, "brawcroft bothy")
	}
}

func testTerminalGraphemes(t *testing.T, factory terminalBackendFactory) {
	t.Helper()

	cases := []struct {
		name  string
		input string
	}{
		{name: "combining", input: "e\u0301b"},
		{name: "zwj", input: "👩‍💻b"},
		{name: "variation_selector", input: "♥️b"},
		{name: "regional_indicator", input: "🇬🇧b"},
		{name: "wide", input: "你b"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, fragmented := range []bool{false, true} {
				name := "whole_write"
				if fragmented {
					name = "byte_fragmented"
				}

				t.Run(name, func(t *testing.T) {
					term := newTerminalBackendTestTerm(t, factory, 12, 2)

					if fragmented {
						for _, b := range []byte(tc.input) {
							write(t, term, string([]byte{b}))
						}
					} else {
						write(t, term, tc.input)
					}

					want := factory.expectations.graphemes[tc.name]
					if fragmented {
						if fragmentedWant, ok := factory.expectations.fragmented[tc.name]; ok {
							want = fragmentedWant
						}
					}

					assertLeadingCellContents(t, term, want.cells)

					if got := renderPreview(term); got != want.preview+"\n" {
						t.Errorf("rendered preview = %q, want %q", got, want.preview+"\n")
					}

					if x, y, visible := term.Cursor(); x != want.cursorX || y != 0 || !visible {
						t.Errorf("cursor = (%d,%d,%t), want (%d,0,true)", x, y, visible, want.cursorX)
					}
				})
			}
		})
	}
}

func testTerminalRISGraphemes(t *testing.T, factory terminalBackendFactory) {
	t.Helper()

	graphemes := []struct {
		name  string
		input string
	}{
		{name: "zwj", input: "👩‍💻b"},
		{name: "variation_selector", input: "♥️b"},
		{name: "regional_indicator", input: "🇬🇧b"},
	}
	deliveries := []struct {
		name   string
		chunks []string
	}{
		{name: "same_write", chunks: []string{"\x1bc"}},
		{name: "split_escape_final", chunks: []string{"\x1b", "c"}},
	}

	for _, delivery := range deliveries {
		t.Run(delivery.name, func(t *testing.T) {
			for _, grapheme := range graphemes {
				t.Run(grapheme.name, func(t *testing.T) {
					term := newTerminalBackendTestTerm(t, factory, 12, 2)
					// Applications may explicitly reset mode 2027, but RIS then
					// restores each backend's grapheme behavior.
					write(t, term, "\x1b[?2027l")

					for i, chunk := range delivery.chunks {
						if i == len(delivery.chunks)-1 {
							chunk += grapheme.input
						}

						write(t, term, chunk)
					}

					want := factory.expectations.graphemes[grapheme.name]
					assertLeadingCellContents(t, term, want.cells)

					if got := renderPreview(term); got != want.preview+"\n" {
						t.Errorf("rendered preview = %q, want %q", got, want.preview+"\n")
					}

					if x, y, visible := term.Cursor(); x != want.cursorX || y != 0 || !visible {
						t.Errorf("cursor = (%d,%d,%t), want (%d,0,true)", x, y, visible, want.cursorX)
					}
				})
			}
		})
	}
}

func testTerminalStylesAndColors(t *testing.T, factory terminalBackendFactory) {
	t.Helper()

	t.Run("all_rendered_styles", func(t *testing.T) {
		term := newTerminalBackendTestTerm(t, factory, 12, 2)
		write(t, term, "\x1b[1;2;3;4;5;7;9mbraw\x1b[0m")

		want := CellStyle{
			Bold: true, Faint: true, Italic: true, Underline: true,
			Blink: true, Reverse: true, Strikethrough: true,
		}
		if got := term.Cell(0, 0).Style; got != want {
			t.Errorf("all-style cell = %+v, want %+v", got, want)
		}

		if frame := renderFrame(term).Frame; !strings.Contains(frame, "\x1b[0;1;2;3;4;5;7;9m") {
			t.Errorf("rendered frame omitted a style: %q", frame)
		}
	})

	colors := []struct {
		name  string
		input string
		want  CellStyle
		frame string
	}{
		{name: "default", input: "braw"},
		{
			name:  "palette",
			input: "\x1b[38;5;208;48;5;33mbraw",
			want: CellStyle{
				FG: Color{Kind: ColorIndexed, Value: 208},
				BG: Color{Kind: ColorIndexed, Value: 33},
			},
			frame: ";38;5;208;48;5;33m",
		},
		{
			name:  "rgb",
			input: "\x1b[38;2;10;20;30;48;2;40;50;60mbraw",
			want: CellStyle{
				FG: Color{Kind: ColorRGB, Value: 0x0A141E},
				BG: Color{Kind: ColorRGB, Value: 0x28323C},
			},
			frame: ";38;2;10;20;30;48;2;40;50;60m",
		},
		{
			name:  "background_only_palette",
			input: "\x1b[48;5;33m ",
			want:  CellStyle{BG: Color{Kind: ColorIndexed, Value: 33}},
			frame: ";48;5;33m",
		},
		{
			name:  "background_only_rgb",
			input: "\x1b[48;2;10;20;30m ",
			want:  CellStyle{BG: Color{Kind: ColorRGB, Value: 0x0A141E}},
			frame: ";48;2;10;20;30m",
		},
	}

	for _, tc := range colors {
		t.Run(tc.name, func(t *testing.T) {
			term := newTerminalBackendTestTerm(t, factory, 12, 2)
			write(t, term, tc.input)

			if got := term.Cell(0, 0).Style; got != tc.want {
				t.Errorf("cell style = %+v, want %+v", got, tc.want)
			}

			if tc.frame != "" && !strings.Contains(renderFrame(term).Frame, tc.frame) {
				t.Errorf("frame omitted color sequence %q", tc.frame)
			}
		})
	}

	t.Run("reset_to_default", func(t *testing.T) {
		term := newTerminalBackendTestTerm(t, factory, 12, 2)
		write(t, term, "\x1b[31;44mbraw\x1b[0mcanny")

		if got := term.Cell(4, 0).Style; got != (CellStyle{}) {
			t.Errorf("reset cell style = %+v, want default", got)
		}
	})
}

func testTerminalIssue1430(t *testing.T, factory terminalBackendFactory) {
	t.Helper()

	term := newTerminalBackendTestTerm(t, factory, 80, 24)
	fixture := terminalParserPanicFixture(t)

	n, err := term.Write(fixture)

	if wantErr := factory.expectations.issue1430Error; wantErr != nil {
		if n != 0 || !errors.Is(err, wantErr) {
			t.Fatalf("contained result = (%d,%v), want (0,%v)", n, err, wantErr)
		}

		return
	}

	if n != len(fixture) || err != nil {
		t.Fatalf("write regression fixture = (%d,%v), want (%d,nil)", n, err, len(fixture))
	}

	write(t, term, "canny thrawn")

	if got := renderPreview(term); !strings.Contains(got, "canny thrawn") {
		t.Errorf("native backend unusable after reduced regression: %q", got)
	}
}

func assertLeadingCellContents(t *testing.T, term Terminal, want []string) {
	t.Helper()

	for x, wantContent := range want {
		if got := term.Cell(x, 0).Content; got != wantContent {
			t.Errorf("cell (%d,0) = %q, want %q", x, got, wantContent)
		}
	}
}
