//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

import (
	"fmt"
	"testing"
)

func BenchmarkGhosttySnapshot(b *testing.B) {
	for _, size := range []struct{ cols, rows int }{{80, 24}, {120, 40}, {240, 72}} {
		for _, mode := range []string{"cursor", "row", "full"} {
			b.Run(fmt.Sprintf("%dx%d/%s", size.cols, size.rows, mode), func(b *testing.B) {
				term, err := newGhosttyTerminal(size.cols, size.rows)
				if err != nil {
					b.Fatal(err)
				}

				b.Cleanup(func() {
					if err := term.Close(); err != nil {
						b.Error(err)
					}
				})

				workload := syntheticTerminalWorkload(size.cols * size.rows)
				if _, err = term.Write(workload); err != nil {
					b.Fatal(err)
				}

				if _, err = term.Snapshot(); err != nil {
					b.Fatal(err)
				}

				updates := [][]byte{[]byte("\x1b[Hbraw"), []byte("\x1b[Hcanny")}
				if mode == "cursor" {
					updates = [][]byte{[]byte("\x1b[H"), []byte("\x1b[2;2H")}
				}

				if mode == "full" {
					updates = [][]byte{workload, workload}
				}

				b.ReportAllocs()
				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					if _, err = term.Write(updates[i%2]); err != nil {
						b.Fatal(err)
					}

					if _, err = term.Snapshot(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
