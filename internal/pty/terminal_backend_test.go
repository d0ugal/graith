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
	name         string
	new          func(cols, rows int) (Terminal, error)
	expectations terminalBackendExpectations
}

func terminalBackendFactories() []terminalBackendFactory {
	backends := []terminalBackendFactory{
		{
			name: "charm",
			new: func(cols, rows int) (Terminal, error) {
				return newCharmTerminal(cols, rows), nil
			},
			expectations: charmTerminalBackendExpectations(),
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
	return writeTerminalChunks(term, fixture)
}

func syntheticTerminalWorkload(minBytes int) []byte {
	var fixture strings.Builder
	fixture.Grow(minBytes + 128)

	for i := 0; fixture.Len() < minBytes; i++ {
		fmt.Fprintf(
			&fixture,
			"\x1b[3%dm%06d canny braw brae e\u0301 你 😀\x1b[0m\r\n",
			i%8,
			i,
		)
	}

	return []byte(fixture.String())
}
