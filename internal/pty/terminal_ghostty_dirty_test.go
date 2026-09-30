//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

import (
	"reflect"
	"testing"

	libghostty "go.mitchellh.com/libghostty"
)

func TestGhosttyIncrementalSnapshotMatchesFull(t *testing.T) {
	tests := map[string][]string{
		"cursor and modes":              {"\x1b[H", "\x1b[3;8H", "\x1b[?25l", "\x1b[?1000h\x1b[?2004h", "\x1b[?25h"},
		"styles and palette":            {"\x1b[H\x1b[1;31;44mbraw\x1b[0m", "\x1b]4;1;rgb:12/34/56\x07", "\x1b[2;3H\x1b[48;2;9;8;7m  \x1b[0m", "\x1b]10;rgb:aa/bb/cc\x07"},
		"wide and grapheme":             {"\x1b[H你e\u0301😀", "\x1b[1;2Hb", "\x1b[2;19H你", "\x1b[3;3He", "\u0301"},
		"erase and scroll":              {"\x1b[2;3H\x1b[K", "\x1b[3;2H\x1b[1K", "\x1b[6;1H\n\n", "\x1b[2;5r\x1b[2;1H\x1bM", "\x1b[2J"},
		"alternate screen and reset":    {"\x1b[?1049hbothy", "\x1b[?1049l", "\x1b[?47hbairn", "\x1b[?47l", "\x1bc"},
		"insert delete lines and cells": {"\x1b[2;1H\x1b[L", "\x1b[M", "\x1b[3;4H\x1b[2@", "\x1b[3P", "\x1b[2X"},
	}
	for name, updates := range tests {
		t.Run(name, func(t *testing.T) {
			cached, full := newDirtyTestTerm(t), newDirtyTestTerm(t)
			for i, data := range append([]string{string(syntheticTerminalWorkload(256))}, updates...) {
				for _, term := range []*ghosttyTerminal{cached, full} {
					if _, err := term.Write([]byte(data)); err != nil {
						t.Fatal(err)
					}
				}

				full.cells = nil

				got, err := cached.Snapshot()
				if err != nil {
					t.Fatal(err)
				}

				want, err := full.Snapshot()
				if err != nil {
					t.Fatal(err)
				}

				if !reflect.DeepEqual(got, want) {
					t.Fatalf("snapshot differs after update %d (%q)", i, data)
				}
			}
		})
	}
}

func TestGhosttyIncrementalSnapshotResize(t *testing.T) {
	cached, full := newDirtyTestTerm(t), newDirtyTestTerm(t)
	for _, size := range [][2]int{{20, 6}, {30, 4}, {10, 12}, {40, 10}, {10, 3}, {20, 6}} {
		for _, term := range []*ghosttyTerminal{cached, full} {
			if err := term.Resize(size[0], size[1]); err != nil {
				t.Fatal(err)
			}

			if _, err := term.Write(syntheticTerminalWorkload(100)); err != nil {
				t.Fatal(err)
			}
		}

		full.cells = nil

		got, err := cached.Snapshot()
		if err != nil {
			t.Fatal(err)
		}

		want, err := full.Snapshot()
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("snapshot differs at %dx%d", size[0], size[1])
		}
	}
}

func TestGhosttyCursorSnapshotAvoidsCellExtraction(t *testing.T) {
	term := newDirtyTestTerm(t)
	update := []byte("\x1b[H")

	if _, err := term.Snapshot(); err != nil {
		t.Fatal(err)
	}

	measure := func(full bool) float64 {
		return testing.AllocsPerRun(5, func() {
			if _, err := term.Write(update); err != nil {
				t.Fatal(err)
			}

			if full {
				term.cells = nil
			}

			if _, err := term.Snapshot(); err != nil {
				t.Fatal(err)
			}
		})
	}

	cached, full := measure(false), measure(true)
	if cached >= full/4 {
		t.Fatalf("cursor-only snapshot allocated %.0f times vs %.0f for full extraction; want less than a quarter", cached, full)
	}
}

func TestGhosttyFullInvalidationDiscardsCachedCells(t *testing.T) {
	term := newDirtyTestTerm(t)
	if _, err := term.Write([]byte("braw")); err != nil {
		t.Fatal(err)
	}

	want, err := term.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a full native invalidation while all row flags are clean and
	// geometry is unchanged. No corrupted cached cell may survive it.
	term.cells[len(term.cells)-1].Content = "dreich"
	if err := term.renderState.SetDirty(libghostty.RenderStateDirtyFull); err != nil {
		t.Fatal(err)
	}

	term.dirty = true

	got, err := term.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatal("full invalidation retained a stale cached cell")
	}
}

func newDirtyTestTerm(t *testing.T) *ghosttyTerminal {
	t.Helper()

	term, err := newGhosttyTerminal(20, 6)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := term.Close(); err != nil {
			t.Error(err)
		}
	})

	return term
}
