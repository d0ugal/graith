//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

import "strings"

const (
	// For v1, invalid/omitted requests receive the largest history the backend
	// supports; explicit larger requests clamp to the same cap below.
	defaultTerminalHistoryRows = 2000
	maxTerminalHistoryRows     = 2000
	maxTerminalHistoryCells    = 256 * 1024
)

func resolveTerminalHistoryRows(requested, cols int) int {
	if requested <= 0 {
		requested = defaultTerminalHistoryRows
	}

	requested = min(requested, maxTerminalHistoryRows)
	cols = max(cols, 1)

	cellBound := maxTerminalHistoryCells / cols
	if cellBound < 1 {
		cellBound = 1
	}

	return min(requested, cellBound)
}

func renderHistoryLineFrame(cells []Cell, trimTrailingBlanks bool) string {
	if trimTrailingBlanks {
		cells = trimHistoryTrailingBlanks(cells)
	}

	var buf strings.Builder
	buf.Grow(len(cells) * 8)

	var prevStyle CellStyle

	writeStyledCells(&buf, cells, &prevStyle)
	buf.WriteString("\x1b[0m")

	return buf.String()
}

func trimHistoryTrailingBlanks(cells []Cell) []Cell {
	last := len(cells)
	for last > 0 && historyBlankCell(cells[last-1]) {
		last--
	}

	return cells[:last]
}

func historyBlankCell(cell Cell) bool {
	return cell.Content == " " && cell.Style == (CellStyle{})
}
