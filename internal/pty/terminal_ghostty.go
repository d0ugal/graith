//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

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

	cols        int
	rows        int
	historyRows int
	cells       []Cell
	dirty       bool

	pendingPtyReplies []byte
}

var _ Terminal = (*ghosttyTerminal)(nil)
var _ terminalSnapshotter = (*ghosttyTerminal)(nil)
var _ terminalInputModer = (*ghosttyTerminal)(nil)
var _ terminalHistorySnapshotter = (*ghosttyTerminal)(nil)

func newGhosttyTerminal(cols, rows int) (gt *ghosttyTerminal, err error) {
	// The visible screen model keeps native scrollback disabled. Enabling it
	// changes resize reflow semantics; the process helper uses a separate
	// bounded mirror terminal when attach history is requested.
	return newGhosttyTerminalWithScrollback(cols, rows, 0)
}

func newGhosttyTerminalWithHistory(cols, rows, historyRows int) (gt *ghosttyTerminal, err error) {
	return newGhosttyTerminalWithScrollback(cols, rows, resolveTerminalHistoryRows(historyRows, cols))
}

func newGhosttyTerminalWithScrollback(cols, rows, historyRows int) (gt *ghosttyTerminal, err error) {
	defer func() {
		if recover() != nil {
			err = errGhosttyBindingPanic
		}

		if err != nil && gt != nil {
			_ = gt.Close()
			gt = nil
		}
	}()

	cols, rows, cols16, rows16, err := validateGhosttySize16(cols, rows)
	if err != nil {
		return nil, err
	}

	gt = &ghosttyTerminal{
		cols:  cols,
		rows:  rows,
		dirty: true,
	}

	options := []libghostty.TerminalOption{
		libghostty.WithSize(cols16, rows16),
		libghostty.WithModeDefault(libghostty.ModeGraphemeCluster, true),
		libghostty.WithWritePty(func(_ *libghostty.Terminal, data []byte) {
			gt.pendingPtyReplies = append(gt.pendingPtyReplies, data...)
		}),
		libghostty.WithClipboardWrite(func(_ *libghostty.Terminal, _ libghostty.ClipboardWrite) libghostty.ClipboardWriteResult {
			return libghostty.ClipboardWriteDenied
		}),
		libghostty.WithTitleChanged(func(_ *libghostty.Terminal) {}),
		libghostty.WithEnquiry(func(_ *libghostty.Terminal) []byte {
			return nil
		}),
		libghostty.WithXtversion(func(_ *libghostty.Terminal) string {
			return "graith"
		}),
		libghostty.WithSizeReport(func(_ *libghostty.Terminal) (libghostty.SizeReportSize, bool) {
			return libghostty.SizeReportSize{
				Rows:       uint16(gt.rows), //nolint:gosec // G115: rows were validated as uint16 for libghostty
				Columns:    uint16(gt.cols), //nolint:gosec // G115: cols were validated as uint16 for libghostty
				CellWidth:  8,
				CellHeight: 16,
			}, true
		}),
		libghostty.WithColorScheme(func(_ *libghostty.Terminal) (libghostty.ColorScheme, bool) {
			return libghostty.ColorSchemeDark, true
		}),
		libghostty.WithDeviceAttributes(func(_ *libghostty.Terminal) (libghostty.DeviceAttributes, bool) {
			attrs := libghostty.DeviceAttributes{
				Primary: libghostty.DeviceAttributesPrimary{
					ConformanceLevel: libghostty.DAConformanceVT220,
					NumFeatures:      1,
				},
				Secondary: libghostty.DeviceAttributesSecondary{
					DeviceType:      libghostty.DADeviceTypeVT220,
					FirmwareVersion: 0,
					ROMCartridge:    0,
				},
				Tertiary: libghostty.DeviceAttributesTertiary{
					UnitID: 0,
				},
			}
			attrs.Primary.Features[0] = libghostty.DAFeatureANSIColor

			return attrs, true
		}),
	}
	if historyRows > 0 {
		options = append(options, libghostty.WithMaxScrollbackLines(uint(historyRows)))
	} else {
		// Graith's bounded raw Scrollback is authoritative and is replayed when
		// reconstructing a helper. The native backend only needs the visible
		// viewport; retaining historical native lines multiplies memory by width
		// and helper count without exposing any additional product behavior.
		options = append(options,
			libghostty.WithMaxScrollbackBytes(0),
			libghostty.WithMaxScrollbackLines(0),
		)
	}

	terminal, err := libghostty.NewTerminal(options...)
	if err != nil {
		return nil, fmt.Errorf("create go-libghostty terminal: %w", err)
	}

	gt.terminal = terminal
	gt.historyRows = historyRows

	// Graith renders text cells only. Disable the image storage and all
	// filesystem/shared-memory image media exposed by the upstream binding.
	zero := uint64(0)
	if err = terminal.SetKittyImageStorageLimit(&zero); err != nil {
		return nil, fmt.Errorf("disable Kitty image storage: %w", err)
	}

	if err = terminal.SetKittyImageMediumFile(false); err != nil {
		return nil, fmt.Errorf("disable Kitty file medium: %w", err)
	}

	if err = terminal.SetKittyImageMediumTempFile(nil); err != nil {
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

func (gt *ghosttyTerminal) DrainPtyReplies() []byte {
	out := append([]byte(nil), gt.pendingPtyReplies...)
	gt.pendingPtyReplies = nil

	return out
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

	cols, rows, cols16, rows16, err := validateGhosttySize16(cols, rows)
	if err != nil {
		return err
	}

	if err := gt.terminal.Resize(cols16, rows16, 8, 16); err != nil {
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
		InputModes:    terminalInputModes(gt),
	}, nil
}

func (gt *ghosttyTerminal) InputModes() (TerminalInputModes, error) {
	if gt.terminal == nil {
		return TerminalInputModes{}, errGhosttyClosed
	}

	mode := func(m libghostty.Mode) (bool, error) {
		v, err := gt.terminal.Mode(m)
		if err != nil {
			return false, fmt.Errorf("read go-libghostty mode %d: %w", m.Value(), err)
		}

		return v, nil
	}

	x10Mouse, err := mode(libghostty.ModeX10Mouse)
	if err != nil {
		return TerminalInputModes{}, err
	}

	normalMouse, err := mode(libghostty.ModeNormalMouse)
	if err != nil {
		return TerminalInputModes{}, err
	}

	buttonMouse, err := mode(libghostty.ModeButtonMouse)
	if err != nil {
		return TerminalInputModes{}, err
	}

	anyMouse, err := mode(libghostty.ModeAnyMouse)
	if err != nil {
		return TerminalInputModes{}, err
	}

	utf8Mouse, err := mode(libghostty.ModeUTF8Mouse)
	if err != nil {
		return TerminalInputModes{}, err
	}

	sgrMouse, err := mode(libghostty.ModeSGRMouse)
	if err != nil {
		return TerminalInputModes{}, err
	}

	urxvtMouse, err := mode(libghostty.ModeURxvtMouse)
	if err != nil {
		return TerminalInputModes{}, err
	}

	sgrPixelsMouse, err := mode(libghostty.ModeSGRPixelsMouse)
	if err != nil {
		return TerminalInputModes{}, err
	}

	altScreenLegacy, err := mode(libghostty.ModeAltScreenLegacy)
	if err != nil {
		return TerminalInputModes{}, err
	}

	altScreen, err := mode(libghostty.ModeAltScreen)
	if err != nil {
		return TerminalInputModes{}, err
	}

	altScreenSave, err := mode(libghostty.ModeAltScreenSave)
	if err != nil {
		return TerminalInputModes{}, err
	}

	modes := TerminalInputModes{
		MouseTracking:   TerminalMouseTrackingNone,
		MouseFormat:     TerminalMouseFormatX10,
		AlternateScreen: altScreenLegacy || altScreen || altScreenSave,
	}

	switch {
	case anyMouse:
		modes.MouseTracking = TerminalMouseTrackingAny
	case buttonMouse:
		modes.MouseTracking = TerminalMouseTrackingButton
	case normalMouse:
		modes.MouseTracking = TerminalMouseTrackingNormal
	case x10Mouse:
		modes.MouseTracking = TerminalMouseTrackingX10
	}

	switch {
	case sgrPixelsMouse:
		modes.MouseFormat = TerminalMouseFormatSGRPixels
	case urxvtMouse:
		modes.MouseFormat = TerminalMouseFormatURxvt
	case sgrMouse:
		modes.MouseFormat = TerminalMouseFormatSGR
	case utf8Mouse:
		modes.MouseFormat = TerminalMouseFormatUTF8
	}

	if modes.Focus, err = mode(libghostty.ModeFocusEvent); err != nil {
		return TerminalInputModes{}, err
	}

	if modes.BracketedPaste, err = mode(libghostty.ModeBracketedPaste); err != nil {
		return TerminalInputModes{}, err
	}

	if modes.KeyboardLocked, err = mode(libghostty.ModeKAM); err != nil {
		return TerminalInputModes{}, err
	}

	if modes.ApplicationCursorKeys, err = mode(libghostty.ModeDECCKM); err != nil {
		return TerminalInputModes{}, err
	}

	if modes.ApplicationKeypad, err = mode(libghostty.ModeKeypadKeys); err != nil {
		return TerminalInputModes{}, err
	}

	if modes.AlternateScroll, err = mode(libghostty.ModeAltScroll); err != nil {
		return TerminalInputModes{}, err
	}

	return modes, nil
}

func (gt *ghosttyTerminal) SnapshotWithHistory() (TerminalSnapshot, error) {
	snapshot, err := gt.Snapshot()
	if err != nil {
		return TerminalSnapshot{}, err
	}

	history, err := gt.HistorySnapshot(gt.historyRows)
	if err != nil {
		return TerminalSnapshot{}, err
	}

	snapshot.History = history

	return snapshot, nil
}

func (gt *ghosttyTerminal) HistorySnapshot(limit int) (history TerminalHistory, err error) {
	defer func() {
		if recover() != nil {
			history = TerminalHistory{}
			err = errGhosttyBindingPanic
		}
	}()

	if gt.terminal == nil {
		return TerminalHistory{}, errGhosttyClosed
	}

	screen, err := gt.terminal.ActiveScreen()
	if err != nil {
		return TerminalHistory{}, fmt.Errorf("read go-libghostty active screen: %w", err)
	}

	activeScreen := TerminalScreenPrimary
	if screen == libghostty.ScreenAlternate {
		activeScreen = TerminalScreenAlternate
	}

	history = TerminalHistory{
		MaxLines:     gt.historyRows,
		ActiveScreen: activeScreen,
	}

	if activeScreen != TerminalScreenPrimary {
		return history, nil
	}

	rows, err := gt.terminal.ScrollbackRows()
	if err != nil {
		return TerminalHistory{}, fmt.Errorf("read go-libghostty scrollback rows: %w", err)
	}

	if rows == 0 {
		return history, nil
	}

	if limit <= 0 || limit > gt.historyRows {
		limit = gt.historyRows
	}

	start := 0
	if int(rows) > limit {
		start = int(rows) - limit
		history.Truncated = true
	}

	if gt.historyRows > 0 && int(rows) >= gt.historyRows {
		history.Truncated = true
	}

	history.Lines = make([]TerminalHistoryLine, 0, int(rows)-start)
	for y := start; y < int(rows); y++ {
		line, err := gt.historyLine(y)
		if err != nil {
			return TerminalHistory{}, err
		}

		history.Lines = append(history.Lines, line)
	}

	return history, nil
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

	return convertGhosttyCell(raw, style, graphemes)
}

func (gt *ghosttyTerminal) historyLine(y int) (TerminalHistoryLine, error) {
	if y < 0 || y > int(^uint32(0)) {
		return TerminalHistoryLine{}, errGhosttyBindingPanic
	}

	cells := make([]Cell, gt.cols)

	rowRef, err := gt.terminal.GridRef(libghostty.Point{
		Tag: libghostty.PointTagHistory,
		X:   0,
		Y:   uint32(y),
	})
	if err != nil {
		return TerminalHistoryLine{}, fmt.Errorf("read go-libghostty history row %d: %w", y, err)
	}

	row, err := rowRef.Row()
	if err != nil {
		return TerminalHistoryLine{}, fmt.Errorf("read go-libghostty history row metadata %d: %w", y, err)
	}

	wrapped, err := row.Wrap()
	if err != nil {
		return TerminalHistoryLine{}, fmt.Errorf("read go-libghostty history row wrap %d: %w", y, err)
	}

	wrapContinuation, err := row.WrapContinuation()
	if err != nil {
		return TerminalHistoryLine{}, fmt.Errorf("read go-libghostty history row wrap continuation %d: %w", y, err)
	}

	for x := 0; x < gt.cols; x++ {
		ref, err := gt.terminal.GridRef(libghostty.Point{
			Tag: libghostty.PointTagHistory,
			X:   uint16(x),
			Y:   uint32(y),
		})
		if err != nil {
			return TerminalHistoryLine{}, fmt.Errorf("read go-libghostty history cell %d,%d: %w", x, y, err)
		}

		raw, err := ref.Cell()
		if err != nil {
			return TerminalHistoryLine{}, fmt.Errorf("read go-libghostty history raw cell %d,%d: %w", x, y, err)
		}

		style, err := ref.Style()
		if err != nil {
			return TerminalHistoryLine{}, fmt.Errorf("read go-libghostty history style %d,%d: %w", x, y, err)
		}

		graphemes, err := ref.Graphemes()
		if err != nil {
			return TerminalHistoryLine{}, fmt.Errorf("read go-libghostty history graphemes %d,%d: %w", x, y, err)
		}

		cell, err := convertGhosttyCell(raw, style, graphemes)
		if err != nil {
			return TerminalHistoryLine{}, fmt.Errorf("convert go-libghostty history cell %d,%d: %w", x, y, err)
		}

		cells[x] = cell
	}

	return TerminalHistoryLine{
		Frame:            renderHistoryLineFrame(cells, !wrapped),
		Width:            gt.cols,
		Wrapped:          wrapped,
		WrapContinuation: wrapContinuation,
	}, nil
}

func convertGhosttyCell(raw *libghostty.Cell, style *libghostty.Style, graphemes []uint32) (Cell, error) {
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
		return string(ghosttyRune(codepoints[0]))
	}

	var content strings.Builder
	for _, codepoint := range codepoints {
		content.WriteRune(ghosttyRune(codepoint))
	}

	return content.String()
}

func ghosttyRune(codepoint uint32) rune {
	if codepoint > utf8.MaxRune {
		return utf8.RuneError
	}

	r := rune(codepoint)
	if !utf8.ValidRune(r) {
		return utf8.RuneError
	}

	return r
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
	cols, rows, _, _, err := validateGhosttySize16(cols, rows)

	return cols, rows, err
}

func validateGhosttySize16(cols, rows int) (int, int, uint16, uint16, error) {
	cols, rows = clampSize(cols, rows)
	if cols > int(^uint16(0)) || rows > int(^uint16(0)) || cols > maxGhosttyCells/rows {
		return 0, 0, 0, 0, fmt.Errorf("libghostty terminal size %dx%d exceeds safety limit", cols, rows)
	}

	return cols, rows, uint16(cols), uint16(rows), nil //nolint:gosec // G115: bounds above keep both dimensions within uint16.
}
