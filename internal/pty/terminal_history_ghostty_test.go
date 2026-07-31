//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

import "testing"

func TestResolveTerminalHistoryRowsClampsRowsAndCells(t *testing.T) {
	tests := map[string]struct {
		requested int
		cols      int
		want      int
	}{
		"default rows": {
			requested: 0,
			cols:      80,
			want:      defaultTerminalHistoryRows,
		},
		"negative uses default": {
			requested: -5,
			cols:      80,
			want:      defaultTerminalHistoryRows,
		},
		"positive below caps": {
			requested: 1200,
			cols:      80,
			want:      1200,
		},
		"row cap": {
			requested: 5000,
			cols:      80,
			want:      maxTerminalHistoryRows,
		},
		"cell cap": {
			requested: maxTerminalHistoryRows,
			cols:      200,
			want:      maxTerminalHistoryCells / 200,
		},
		"non-positive columns still retain history": {
			requested: 42,
			cols:      0,
			want:      42,
		},
		"very wide terminal keeps one row": {
			requested: 42,
			cols:      maxTerminalHistoryCells * 2,
			want:      1,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := resolveTerminalHistoryRows(test.requested, test.cols); got != test.want {
				t.Fatalf("resolveTerminalHistoryRows(%d, %d) = %d, want %d", test.requested, test.cols, got, test.want)
			}
		})
	}
}
