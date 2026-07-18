package pty

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// terminalBackendFactory lets the same reduced synthetic corpus and benchmarks
// exercise the rollback backend and an explicitly tagged native candidate.
type terminalBackendFactory struct {
	name                      string
	new                       func(cols, rows int) (Terminal, error)
	expectsContained1430Panic bool
	combiningContent          string
	alternateScreenHomes      bool
	shrinkFirstLine           string
}

func terminalBackendFactories() []terminalBackendFactory {
	backends := []terminalBackendFactory{
		{
			name: "charm",
			new: func(cols, rows int) (Terminal, error) {
				return newCharmTerminal(cols, rows), nil
			},
			expectsContained1430Panic: true,
			combiningContent:          "e",
			alternateScreenHomes:      true,
			shrinkFirstLine:           "keep",
		},
	}

	return append(backends, nativeTerminalBackendFactories()...)
}

func newTerminalBackendTestTerm(
	t testing.TB,
	factory terminalBackendFactory,
	cols, rows int,
) Terminal {
	t.Helper()

	term, err := factory.new(cols, rows)
	if err != nil {
		t.Fatalf("new %s terminal: %v", factory.name, err)
	}

	t.Cleanup(func() {
		if err := term.Close(); err != nil {
			t.Errorf("close %s terminal: %v", factory.name, err)
		}
	})

	return term
}

// TestTerminalBackendCompatibilityCorpus runs both backends through the same
// generic inputs. The corpus is deliberately reduced and synthetic: it
// contains no captured user/agent output, local paths, or session identifiers.
func TestTerminalBackendCompatibilityCorpus(t *testing.T) {
	for _, factory := range terminalBackendFactories() {
		t.Run(factory.name, func(t *testing.T) {
			t.Run("graphemes_styles_colors_and_cursor", func(t *testing.T) {
				term := newTerminalBackendTestTerm(t, factory, 24, 4)
				write(t, term, "\x1b[2J\x1b[H")
				write(t, term, "\x1b[1;2;3;4;5;7;9;38;5;208;48;2;10;20;30mZ\x1b[0m")
				write(t, term, "\r\ne\u0301你😀")
				write(t, term, "\x1b[?25l")

				styled := term.Cell(0, 0).Style
				if styled.FG != (Color{Kind: ColorIndexed, Value: 208}) ||
					styled.BG != (Color{Kind: ColorRGB, Value: 0x0A141E}) {
					t.Errorf("styled cell colors = FG %+v BG %+v", styled.FG, styled.BG)
				}

				if !styled.Bold || !styled.Faint || !styled.Italic || !styled.Underline ||
					!styled.Blink || !styled.Reverse || !styled.Strikethrough {
					t.Errorf("styled cell attributes incomplete: %+v", styled)
				}

				// Charm currently drops the combining mark while Ghostty
				// clusters it with the base codepoint, matching Terminal's
				// documented grapheme semantics. Keep the difference explicit
				// while driving both backends with the same input.
				wantCells := map[int]string{
					0: factory.combiningContent,
					1: "你",
					2: "",
					3: "😀",
					4: "",
				}
				for x, want := range wantCells {
					if got := term.Cell(x, 1).Content; got != want {
						t.Errorf("cell (%d,1) = %q, want %q", x, got, want)
					}
				}

				x, y, visible := term.Cursor()
				if x != 5 || y != 1 || visible {
					t.Errorf("cursor = (%d,%d,%t), want (5,1,false)", x, y, visible)
				}
			})

			t.Run("scrollback_hydration_and_resize", func(t *testing.T) {
				term := newTerminalBackendTestTerm(t, factory, 36, 5)
				fixture := syntheticTerminalWorkload(128 * 1024)
				write(t, term, string(fixture))

				before := renderPreview(term)
				if !strings.Contains(before, "canny synthetic line") {
					t.Fatalf("hydrated preview missing final synthetic rows: %q", before)
				}

				if err := term.Resize(52, 8); err != nil {
					t.Fatal(err)
				}

				if cols, rows := term.Size(); cols != 52 || rows != 8 {
					t.Errorf("size after grow = (%d,%d), want (52,8)", cols, rows)
				}

				if after := renderPreview(term); !strings.Contains(after, "canny synthetic line") {
					t.Errorf("resize lost hydrated content: %q", after)
				}
			})

			t.Run("shrink_reflow", func(t *testing.T) {
				term := newTerminalBackendTestTerm(t, factory, 20, 2)
				write(t, term, "keep me canny")

				if err := term.Resize(4, 2); err != nil {
					t.Fatal(err)
				}

				if got := line(t, term, 0); got != factory.shrinkFirstLine {
					t.Errorf("first line after shrink = %q, want %q", got, factory.shrinkFirstLine)
				}
			})

			t.Run("alternate_screen_and_device_queries", func(t *testing.T) {
				term := newTerminalBackendTestTerm(t, factory, 30, 3)
				write(t, term, "on the brae")
				write(t, term, "\x1b[?1049h\x1b[6n\x1b[c\x1b[5n")
				write(t, term, "in the bothy")

				wantAlternate := "           in the bothy"
				if factory.alternateScreenHomes {
					wantAlternate = "in the bothy"
				}

				if got := line(t, term, 0); got != wantAlternate {
					t.Errorf("alternate screen = %q, want %q", got, wantAlternate)
				}

				write(t, term, "\x1b[?1049l")

				if got := line(t, term, 0); got != "on the brae" {
					t.Errorf("restored main screen = %q", got)
				}
			})

			t.Run("issue_1430_reduced_regression", func(t *testing.T) {
				term := newTerminalBackendTestTerm(t, factory, 80, 24)

				n, err := term.Write(terminalParserPanicFixture(t))
				if factory.expectsContained1430Panic {
					if n != 0 || err != errTerminalParserPanic {
						t.Fatalf("contained result = (%d,%v), want (0,%v)", n, err, errTerminalParserPanic)
					}

					return
				}

				if n != len(terminalParserPanicFixture(t)) || err != nil {
					t.Fatalf("write regression fixture = (%d,%v)", n, err)
				}

				write(t, term, "canny after malformed region")
			})
		})
	}
}

func BenchmarkTerminalBackends(b *testing.B) {
	parseWorkload := syntheticTerminalWorkload(64 * 1024)
	hydrationWorkload := syntheticTerminalWorkload(4 * 1024 * 1024)

	for _, factory := range terminalBackendFactories() {
		b.Run(factory.name, func(b *testing.B) {
			b.Run("start_close", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					term, err := factory.new(120, 40)
					if err != nil {
						b.Fatal(err)
					}

					if err := term.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})

			b.Run("parse", func(b *testing.B) {
				term := newTerminalBackendTestTerm(b, factory, 120, 40)
				b.ReportAllocs()
				b.SetBytes(int64(len(parseWorkload)))
				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					if _, err := term.Write(parseWorkload); err != nil {
						b.Fatal(err)
					}
				}
			})

			b.Run("snapshot_120x40", func(b *testing.B) {
				term := newTerminalBackendTestTerm(b, factory, 120, 40)
				if _, err := term.Write(parseWorkload); err != nil {
					b.Fatal(err)
				}

				var checksum uint64

				b.ReportAllocs()
				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					if _, err := term.Write([]byte("\x1b[H")); err != nil {
						b.Fatal(err)
					}

					snapshot, err := snapshotTerminal(term)
					if err != nil {
						b.Fatal(err)
					}

					for _, cell := range snapshot.Cells {
						checksum += uint64(len(cell.Content)) + uint64(cell.Style.FG.Value)
					}
				}

				if checksum == 0 {
					b.Fatal("empty extraction checksum")
				}
			})

			b.Run("resize", func(b *testing.B) {
				term := newTerminalBackendTestTerm(b, factory, 80, 24)
				if _, err := term.Write(parseWorkload); err != nil {
					b.Fatal(err)
				}

				b.ReportAllocs()
				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					if i%2 == 0 {
						if err := term.Resize(120, 40); err != nil {
							b.Fatal(err)
						}
					} else {
						if err := term.Resize(80, 24); err != nil {
							b.Fatal(err)
						}
					}
				}
			})

			b.Run("reconstruct_4MiB", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(hydrationWorkload)))
				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					term, err := factory.new(120, 40)
					if err != nil {
						b.Fatal(err)
					}

					if err := writeTerminalFixture(term, hydrationWorkload); err != nil {
						_ = term.Close()

						b.Fatal(err)
					}

					if _, err := snapshotTerminal(term); err != nil {
						_ = term.Close()

						b.Fatal(err)
					}

					if err := term.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// TestTerminalBackendPeakMemoryWorkload is an opt-in process-level workload for
// /usr/bin/time (or an equivalent RSS observer). Go's benchmark allocation
// counters cannot see native allocations, so peak RSS must be measured around
// a separate test process for each backend.
func TestTerminalBackendPeakMemoryWorkload(t *testing.T) {
	name := os.Getenv("GRAITH_TERMINAL_MEMORY_BACKEND")
	if name == "" {
		t.Skip("set GRAITH_TERMINAL_MEMORY_BACKEND to run the process-level RSS workload")
	}

	var selected *terminalBackendFactory

	for _, factory := range terminalBackendFactories() {
		if factory.name == name {
			factory := factory
			selected = &factory

			break
		}
	}

	if selected == nil {
		t.Fatalf("unknown terminal backend %q", name)
	}

	term := newTerminalBackendTestTerm(t, *selected, 120, 40)

	fixture := syntheticTerminalWorkload(4 * 1024 * 1024)
	if err := writeTerminalFixture(term, fixture); err != nil {
		t.Fatal(err)
	}

	snapshot, err := snapshotTerminal(term)
	if err != nil {
		t.Fatal(err)
	}

	var checksum uint64
	for _, cell := range snapshot.Cells {
		checksum += uint64(len(cell.Content)) + uint64(cell.Style.FG.Value)
	}

	if err := term.Close(); err != nil {
		t.Fatal(err)
	}

	if observer, ok := term.(interface{ PeakRSSBytes() int64 }); ok {
		t.Logf("helper_peak_rss_bytes=%d", observer.PeakRSSBytes())
	}

	t.Logf("backend=%s fixture_bytes=%d checksum=%d", name, len(fixture), checksum)
}

func writeTerminalFixture(term Terminal, fixture []byte) error {
	const chunkBytes = 512 * 1024

	for len(fixture) > 0 {
		n := min(len(fixture), chunkBytes)
		if _, err := term.Write(fixture[:n]); err != nil {
			return err
		}

		fixture = fixture[n:]
	}

	return nil
}

func syntheticTerminalWorkload(minBytes int) []byte {
	var fixture strings.Builder
	fixture.Grow(minBytes + 128)

	for i := 0; fixture.Len() < minBytes; i++ {
		fmt.Fprintf(
			&fixture,
			"\x1b[3%dm%06d canny synthetic line on the brae with e\u0301, 你, and 😀\x1b[0m\r\n",
			i%8,
			i,
		)
	}

	return []byte(fixture.String())
}
