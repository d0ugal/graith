//go:build libghostty && cgo && (darwin || linux)

package pty

import (
	"errors"
	"fmt"
	"strings"

	libghostty "go.mitchellh.com/libghostty"
)

// maxGhosttyCells bounds both native viewport allocation and the retained Go
// snapshots on either side of the helper boundary. 262,144 cells still permits
// unusually large 1024x256 terminals while keeping a single hostile resize
// from turning into several hundred MiB of duplicated cell state.
const maxGhosttyCells = 256 * 1024

var (
	errGhosttyClosed       = errors.New("libghostty-vt terminal is closed")
	errGhosttyBindingPanic = errors.New("go-libghostty operation panicked")
)

// ghosttyTerminal adapts go-libghostty's public Go API to Graith's narrow
// backend-neutral terminal contract. It exists only inside the isolated helper
// process; the daemon never owns a native terminal handle.
type ghosttyTerminal struct {
	terminal    *libghostty.Terminal
	renderState *libghostty.RenderState
	rowIterator *libghostty.RenderStateRowIterator
	rowCells    *libghostty.RenderStateRowCells

	cols  int
	rows  int
	cells []Cell
	dirty bool
}

var _ Terminal = (*ghosttyTerminal)(nil)
var _ terminalSnapshotter = (*ghosttyTerminal)(nil)

func newGhosttyTerminal(cols, rows int) (gt *ghosttyTerminal, err error) {
	defer func() {
		if recover() != nil {
			err = errGhosttyBindingPanic
		}
		if err != nil && gt != nil {
			_ = gt.Close()
			gt = nil
		}
	}()

	cols, rows, err = validateGhosttySize(cols, rows)
	if err != nil {
		return nil, err
	}

	terminal, err := libghostty.NewTerminal(
		libghostty.WithSize(uint16(cols), uint16(rows)),
		// Graith's bounded raw Scrollback is authoritative and is replayed when
		// reconstructing a helper. The native backend only needs the visible
		// viewport; retaining historical native lines multiplies memory by width
		// and helper count without exposing any additional product behavior.
		libghostty.WithMaxScrollback(0),
	)
	if err != nil {
		return nil, fmt.Errorf("create go-libghostty terminal: %w", err)
	}

	gt = &ghosttyTerminal{terminal: terminal, cols: cols, rows: rows, dirty: true}

	if err = terminal.ModeSet(libghostty.ModeGraphemeCluster, true); err != nil {
		return nil, fmt.Errorf("enable go-libghostty grapheme clustering: %w", err)
	}

	// Graith renders text cells only. Disable the image storage and all
	// filesystem/shared-memory image media exposed by the upstream binding.
	zero := uint64(0)
	if err = terminal.SetKittyImageStorageLimit(&zero); err != nil {
		return nil, fmt.Errorf("disable Kitty image storage: %w", err)
	}
	if err = terminal.SetKittyImageMediumFile(false); err != nil {
		return nil, fmt.Errorf("disable Kitty file medium: %w", err)
	}
	if err = terminal.SetKittyImageMediumTempFile(false); err != nil {
		return nil, fmt.Errorf("disable Kitty temporary-file medium: %w", err)
	}
	if err = terminal.SetKittyImageMediumSharedMem(false); err != nil {
		return nil, fmt.Errorf("disable Kitty shared-memory medium: %w", err)
	}

	gt.renderState, err = libghostty.NewRenderState()
	if err != nil {
		return nil, fmt.Errorf("create go-libghostty render state: %w", err)
	}
	gt.rowIterator, err = libghostty.NewRenderStateRowIterator()
	if err != nil {
		return nil, fmt.Errorf("create go-libghostty row iterator: %w", err)
	}
	gt.rowCells, err = libghostty.NewRenderStateRowCells()
	if err != nil {
		return nil, fmt.Errorf("create go-libghostty cell iterator: %w", err)
	}

	return gt, nil
}

func (gt *ghosttyTerminal) Write(p []byte) (n int, err error) {
	defer func() {
		if recover() != nil {
			n = 0
			err = errGhosttyBindingPanic
		}
	}()

	if gt.terminal == nil {
		return 0, errGhosttyClosed
	}
	if len(p) == 0 {
		return 0, nil
	}

	gt.terminal.VTWrite(p)
	gt.dirty = true

	return len(p), nil
}

func (gt *ghosttyTerminal) Resize(cols, rows int) (err error) {
	defer func() {
		if recover() != nil {
			err = errGhosttyBindingPanic
		}
	}()

	if gt.terminal == nil {
		return errGhosttyClosed
	}
	cols, rows, err = validateGhosttySize(cols, rows)
	if err != nil {
		return err
	}
	if err := gt.terminal.Resize(uint16(cols), uint16(rows), 8, 16); err != nil {
		return fmt.Errorf("resize go-libghostty terminal: %w", err)
	}

	gt.cols = cols
	gt.rows = rows
	gt.dirty = true

	return nil
}

func (gt *ghosttyTerminal) Size() (int, int) {
	return gt.cols, gt.rows
}

func (gt *ghosttyTerminal) Cursor() (int, int, bool) {
	x, y, visible, err := gt.cursor()
	if err != nil {
		return 0, 0, false
	}

	return x, y, visible
}

func (gt *ghosttyTerminal) cursor() (int, int, bool, error) {
	if gt.terminal == nil {
		return 0, 0, false, errGhosttyClosed
	}

	x, err := gt.terminal.CursorX()
	if err != nil {
		return 0, 0, false, fmt.Errorf("read go-libghostty cursor x: %w", err)
	}
	y, err := gt.terminal.CursorY()
	if err != nil {
		return 0, 0, false, fmt.Errorf("read go-libghostty cursor y: %w", err)
	}
	visible, err := gt.terminal.CursorVisible()
	if err != nil {
		return 0, 0, false, fmt.Errorf("read go-libghostty cursor visibility: %w", err)
	}

	return int(x), int(y), visible, nil
}

func (gt *ghosttyTerminal) Cell(x, y int) Cell {
	if x < 0 || x >= gt.cols || y < 0 || y >= gt.rows {
		return Cell{Content: " "}
	}
	if err := gt.refreshCells(); err != nil {
		return Cell{Content: " "}
	}

	return gt.cells[y*gt.cols+x]
}

func (gt *ghosttyTerminal) Snapshot() (snapshot TerminalSnapshot, err error) {
	defer func() {
		if recover() != nil {
			snapshot = TerminalSnapshot{}
			err = errGhosttyBindingPanic
		}
	}()

	if err := gt.refreshCells(); err != nil {
		return TerminalSnapshot{}, err
	}
	cursorX, cursorY, cursorVisible, err := gt.cursor()
	if err != nil {
		return TerminalSnapshot{}, err
	}
	cells := make([]Cell, len(gt.cells))
	copy(cells, gt.cells)

	return TerminalSnapshot{
		Cells:         cells,
		CursorX:       cursorX,
		CursorY:       cursorY,
		CursorVisible: cursorVisible,
		Cols:          gt.cols,
		Rows:          gt.rows,
	}, nil
}

func (gt *ghosttyTerminal) Close() (err error) {
	defer func() {
		if recover() != nil {
			err = errGhosttyBindingPanic
		}
	}()

	if gt.rowCells != nil {
		gt.rowCells.Close()
		gt.rowCells = nil
	}
	if gt.rowIterator != nil {
		gt.rowIterator.Close()
		gt.rowIterator = nil
	}
	if gt.renderState != nil {
		gt.renderState.Close()
		gt.renderState = nil
	}
	if gt.terminal != nil {
		gt.terminal.Close()
		gt.terminal = nil
	}
	gt.cells = nil

	return nil
}

func (gt *ghosttyTerminal) refreshCells() error {
	if gt.terminal == nil || gt.renderState == nil || gt.rowIterator == nil || gt.rowCells == nil {
		return errGhosttyClosed
	}
	if !gt.dirty {
		return nil
	}
	if err := gt.renderState.Update(gt.terminal); err != nil {
		return fmt.Errorf("update go-libghostty render state: %w", err)
	}
	if err := gt.renderState.RowIterator(gt.rowIterator); err != nil {
		return fmt.Errorf("read go-libghostty rows: %w", err)
	}

	count := gt.cols * gt.rows
	if cap(gt.cells) < count {
		gt.cells = make([]Cell, count)
	} else {
		gt.cells = gt.cells[:count]
		clear(gt.cells)
	}
	for i := range gt.cells {
		gt.cells[i].Content = " "
	}

	for y := 0; y < gt.rows && gt.rowIterator.Next(); y++ {
		if err := gt.rowIterator.Cells(gt.rowCells); err != nil {
			return fmt.Errorf("read go-libghostty row %d cells: %w", y, err)
		}
		for x := 0; x < gt.cols && gt.rowCells.Next(); x++ {
			cell, err := gt.convertCell()
			if err != nil {
				return fmt.Errorf("read go-libghostty cell %d,%d: %w", x, y, err)
			}
			gt.cells[y*gt.cols+x] = cell
		}
	}

	gt.dirty = false

	return nil
}

func (gt *ghosttyTerminal) convertCell() (Cell, error) {
	raw, err := gt.rowCells.Raw()
	if err != nil {
		return Cell{}, err
	}
	style, err := gt.rowCells.Style()
	if err != nil {
		return Cell{}, err
	}
	graphemes, err := gt.rowCells.Graphemes()
	if err != nil {
		return Cell{}, err
	}

	content := ghosttyGraphemes(graphemes)
	wide, err := raw.Wide()
	if err != nil {
		return Cell{}, err
	}
	if len(graphemes) == 0 &&
		(wide == libghostty.CellWideSpacerTail || wide == libghostty.CellWideSpacerHead) {
		content = ""
	}

	cell := Cell{
		Content: content,
		Style: CellStyle{
			FG:            ghosttyStyleColor(style.FgColor()),
			BG:            ghosttyStyleColor(style.BgColor()),
			Bold:          style.Bold(),
			Faint:         style.Faint(),
			Italic:        style.Italic(),
			Underline:     style.Underline() != libghostty.UnderlineNone,
			Blink:         style.Blink(),
			Reverse:       style.Inverse(),
			Strikethrough: style.Strikethrough(),
		},
	}

	// Background-only cells encode their palette/RGB identity in the raw cell
	// rather than Style. Preserve that distinction for Graith's ANSI renderer.
	if cell.Style.BG.Kind == ColorDefault {
		tag, err := raw.ContentTag()
		if err != nil {
			return Cell{}, err
		}
		switch tag {
		case libghostty.CellContentBgColorPalette:
			palette, err := raw.ColorPalette()
			if err != nil {
				return Cell{}, err
			}
			cell.Style.BG = Color{Kind: ColorIndexed, Value: uint32(palette)}
		case libghostty.CellContentBgColorRGB:
			rgb, err := raw.ColorRGB()
			if err != nil {
				return Cell{}, err
			}
			cell.Style.BG = ghosttyRGB(rgb)
		}
	}

	return cell, nil
}

func ghosttyGraphemes(codepoints []uint32) string {
	if len(codepoints) == 0 {
		return " "
	}
	if len(codepoints) == 1 {
		return string(rune(codepoints[0]))
	}

	var content strings.Builder
	for _, codepoint := range codepoints {
		content.WriteRune(rune(codepoint))
	}

	return content.String()
}

func ghosttyStyleColor(color libghostty.StyleColor) Color {
	switch color.Tag {
	case libghostty.StyleColorPalette:
		return Color{Kind: ColorIndexed, Value: uint32(color.Palette)}
	case libghostty.StyleColorRGB:
		return ghosttyRGB(color.RGB)
	default:
		return Color{Kind: ColorDefault}
	}
}

func ghosttyRGB(color libghostty.ColorRGB) Color {
	return Color{
		Kind: ColorRGB,
		Value: uint32(color.R)<<16 |
			uint32(color.G)<<8 |
			uint32(color.B),
	}
}

func validateGhosttySize(cols, rows int) (int, int, error) {
	cols, rows = clampSize(cols, rows)
	if cols > int(^uint16(0)) || rows > int(^uint16(0)) || cols > maxGhosttyCells/rows {
		return 0, 0, fmt.Errorf("libghostty terminal size %dx%d exceeds safety limit", cols, rows)
	}

	return cols, rows, nil
}
