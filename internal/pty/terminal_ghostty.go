//go:build libghostty && cgo

package pty

/*
#cgo CFLAGS: -DGHOSTTY_STATIC -I${SRCDIR}/../../gui/shared/Sources/CGhosttyVT/include

#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include <ghostty/vt.h>

// This spike deliberately uses the same public C ABI and pinned headers as the
// native clients. The small C-side snapshot cache keeps a full screen
// extraction to one cgo crossing instead of crossing once per cell.
typedef struct {
    size_t content_offset;
    size_t content_len;
    uint32_t fg_value;
    uint32_t bg_value;
    uint8_t fg_kind;
    uint8_t bg_kind;
    uint8_t wide;
    uint8_t bold;
    uint8_t faint;
    uint8_t italic;
    uint8_t underline;
    uint8_t blink;
    uint8_t reverse;
    uint8_t strikethrough;
} GraithGhosttyCell;

typedef struct {
    GhosttyTerminal terminal;
    GhosttyRenderState render_state;
    GhosttyRenderStateRowIterator row_iterator;
    GhosttyRenderStateRowCells row_cells;
    GraithGhosttyCell* cells;
    size_t cells_cap;
    size_t cells_len;
    uint8_t* content;
    size_t content_cap;
    size_t content_len;
} GraithGhosttyTerminal;

static void graith_ghostty_terminal_free(GraithGhosttyTerminal* terminal);

static GraithGhosttyTerminal* graith_ghostty_terminal_new(
    uint16_t cols,
    uint16_t rows
) {
    GraithGhosttyTerminal* result = calloc(1, sizeof(GraithGhosttyTerminal));
    if (result == NULL) return NULL;

    GhosttyTerminalOptions options = {
        .cols = cols,
        .rows = rows,
        .max_scrollback = 10000,
    };

    if (ghostty_terminal_new(NULL, &result->terminal, options) != GHOSTTY_SUCCESS ||
        ghostty_render_state_new(NULL, &result->render_state) != GHOSTTY_SUCCESS ||
        ghostty_render_state_row_iterator_new(NULL, &result->row_iterator) != GHOSTTY_SUCCESS ||
        ghostty_render_state_row_cells_new(NULL, &result->row_cells) != GHOSTTY_SUCCESS) {
        graith_ghostty_terminal_free(result);
        return NULL;
    }

    return result;
}

static void graith_ghostty_terminal_free(GraithGhosttyTerminal* terminal) {
    if (terminal == NULL) return;

    ghostty_render_state_row_cells_free(terminal->row_cells);
    ghostty_render_state_row_iterator_free(terminal->row_iterator);
    ghostty_render_state_free(terminal->render_state);
    ghostty_terminal_free(terminal->terminal);
    free(terminal->content);
    free(terminal->cells);
    free(terminal);
}

static void graith_ghostty_terminal_write(
    GraithGhosttyTerminal* terminal,
    const uint8_t* data,
    size_t len
) {
    if (terminal == NULL || terminal->terminal == NULL || len == 0) return;
    ghostty_terminal_vt_write(terminal->terminal, data, len);
}

static bool graith_ghostty_terminal_resize(
    GraithGhosttyTerminal* terminal,
    uint16_t cols,
    uint16_t rows
) {
    if (terminal == NULL || terminal->terminal == NULL) return false;
    return ghostty_terminal_resize(terminal->terminal, cols, rows, 8, 16) == GHOSTTY_SUCCESS;
}

static bool graith_ghostty_terminal_cursor(
    GraithGhosttyTerminal* terminal,
    uint16_t* x,
    uint16_t* y,
    bool* visible
) {
    if (terminal == NULL || terminal->terminal == NULL) return false;

    return ghostty_terminal_get(terminal->terminal, GHOSTTY_TERMINAL_DATA_CURSOR_X, x) == GHOSTTY_SUCCESS &&
        ghostty_terminal_get(terminal->terminal, GHOSTTY_TERMINAL_DATA_CURSOR_Y, y) == GHOSTTY_SUCCESS &&
        ghostty_terminal_get(terminal->terminal, GHOSTTY_TERMINAL_DATA_CURSOR_VISIBLE, visible) == GHOSTTY_SUCCESS;
}

static uint32_t graith_ghostty_rgb(GhosttyColorRgb color) {
    return ((uint32_t)color.r << 16) | ((uint32_t)color.g << 8) | (uint32_t)color.b;
}

static void graith_ghostty_style_color(
    GhosttyStyleColor color,
    uint8_t* kind,
    uint32_t* value
) {
    switch (color.tag) {
    case GHOSTTY_STYLE_COLOR_PALETTE:
        *kind = 1;
        *value = color.value.palette;
        break;
    case GHOSTTY_STYLE_COLOR_RGB:
        *kind = 2;
        *value = graith_ghostty_rgb(color.value.rgb);
        break;
    default:
        *kind = 0;
        *value = 0;
        break;
    }
}

static bool graith_ghostty_reserve_cells(
    GraithGhosttyTerminal* terminal,
    size_t required
) {
    if (required <= terminal->cells_cap) return true;
    if (required > SIZE_MAX / sizeof(GraithGhosttyCell)) return false;

    GraithGhosttyCell* cells = realloc(
        terminal->cells,
        required * sizeof(GraithGhosttyCell)
    );
    if (cells == NULL) return false;

    terminal->cells = cells;
    terminal->cells_cap = required;
    return true;
}

static bool graith_ghostty_reserve_content(
    GraithGhosttyTerminal* terminal,
    size_t required
) {
    if (required <= terminal->content_cap) return true;

    size_t cap = terminal->content_cap == 0 ? 4096 : terminal->content_cap;
    while (cap < required) {
        if (cap > SIZE_MAX / 2) {
            cap = required;
            break;
        }
        cap *= 2;
    }

    uint8_t* content = realloc(terminal->content, cap);
    if (content == NULL) return false;

    terminal->content = content;
    terminal->content_cap = cap;
    return true;
}

// Returns 0 on success, 1 on allocation failure, and 2 on an unexpected C API
// result. It never includes terminal data in the result, keeping failures safe
// to surface in daemon diagnostics.
static int graith_ghostty_terminal_snapshot(
    GraithGhosttyTerminal* terminal,
    uint16_t cols,
    uint16_t rows
) {
    if (terminal == NULL || terminal->terminal == NULL) return 2;

    size_t cell_count = (size_t)cols * (size_t)rows;
    if (!graith_ghostty_reserve_cells(terminal, cell_count)) return 1;

    memset(terminal->cells, 0, cell_count * sizeof(GraithGhosttyCell));
    terminal->cells_len = cell_count;
    terminal->content_len = 0;

    if (ghostty_render_state_update(terminal->render_state, terminal->terminal) != GHOSTTY_SUCCESS ||
        ghostty_render_state_get(
            terminal->render_state,
            GHOSTTY_RENDER_STATE_DATA_ROW_ITERATOR,
            &terminal->row_iterator
        ) != GHOSTTY_SUCCESS) {
        terminal->cells_len = 0;
        return 2;
    }

    uint16_t y = 0;
    while (y < rows && ghostty_render_state_row_iterator_next(terminal->row_iterator)) {
        if (ghostty_render_state_row_get(
                terminal->row_iterator,
                GHOSTTY_RENDER_STATE_ROW_DATA_CELLS,
                &terminal->row_cells
            ) != GHOSTTY_SUCCESS) {
            terminal->cells_len = 0;
            return 2;
        }

        uint16_t x = 0;
        while (x < cols && ghostty_render_state_row_cells_next(terminal->row_cells)) {
            GraithGhosttyCell* out = &terminal->cells[(size_t)y * cols + x];
            GhosttyCell raw = 0;
            GhosttyStyle style = { .size = sizeof(GhosttyStyle) };

            if (ghostty_render_state_row_cells_get(
                    terminal->row_cells,
                    GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_RAW,
                    &raw
                ) != GHOSTTY_SUCCESS ||
                ghostty_render_state_row_cells_get(
                    terminal->row_cells,
                    GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_STYLE,
                    &style
                ) != GHOSTTY_SUCCESS) {
                terminal->cells_len = 0;
                return 2;
            }

            GhosttyCellWide wide = GHOSTTY_CELL_WIDE_NARROW;
            if (ghostty_cell_get(raw, GHOSTTY_CELL_DATA_WIDE, &wide) != GHOSTTY_SUCCESS) {
                terminal->cells_len = 0;
                return 2;
            }
            out->wide = (uint8_t)wide;

            graith_ghostty_style_color(style.fg_color, &out->fg_kind, &out->fg_value);
            graith_ghostty_style_color(style.bg_color, &out->bg_kind, &out->bg_value);

            // A background-only cell stores its color in the raw cell content,
            // not in GhosttyStyle. Preserve its palette/RGB identity too.
            if (out->bg_kind == 0) {
                GhosttyCellContentTag tag = GHOSTTY_CELL_CONTENT_CODEPOINT;
                if (ghostty_cell_get(raw, GHOSTTY_CELL_DATA_CONTENT_TAG, &tag) != GHOSTTY_SUCCESS) {
                    terminal->cells_len = 0;
                    return 2;
                }

                if (tag == GHOSTTY_CELL_CONTENT_BG_COLOR_PALETTE) {
                    GhosttyColorPaletteIndex palette = 0;
                    if (ghostty_cell_get(raw, GHOSTTY_CELL_DATA_COLOR_PALETTE, &palette) != GHOSTTY_SUCCESS) {
                        terminal->cells_len = 0;
                        return 2;
                    }
                    out->bg_kind = 1;
                    out->bg_value = palette;
                } else if (tag == GHOSTTY_CELL_CONTENT_BG_COLOR_RGB) {
                    GhosttyColorRgb rgb = {0};
                    if (ghostty_cell_get(raw, GHOSTTY_CELL_DATA_COLOR_RGB, &rgb) != GHOSTTY_SUCCESS) {
                        terminal->cells_len = 0;
                        return 2;
                    }
                    out->bg_kind = 2;
                    out->bg_value = graith_ghostty_rgb(rgb);
                }
            }

            out->bold = style.bold;
            out->faint = style.faint;
            out->italic = style.italic;
            out->underline = style.underline != GHOSTTY_SGR_UNDERLINE_NONE;
            out->blink = style.blink;
            out->reverse = style.inverse;
            out->strikethrough = style.strikethrough;

            GhosttyBuffer query = {0};
            GhosttyResult content_result = ghostty_render_state_row_cells_get(
                terminal->row_cells,
                GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_GRAPHEMES_UTF8,
                &query
            );
            if (content_result == GHOSTTY_OUT_OF_SPACE && query.len > 0) {
                if (query.len > SIZE_MAX - terminal->content_len ||
                    !graith_ghostty_reserve_content(terminal, terminal->content_len + query.len)) {
                    terminal->cells_len = 0;
                    return 1;
                }

                out->content_offset = terminal->content_len;
                GhosttyBuffer content = {
                    .ptr = terminal->content + terminal->content_len,
                    .cap = query.len,
                    .len = 0,
                };
                if (ghostty_render_state_row_cells_get(
                        terminal->row_cells,
                        GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_GRAPHEMES_UTF8,
                        &content
                    ) != GHOSTTY_SUCCESS) {
                    terminal->cells_len = 0;
                    return 2;
                }

                out->content_len = content.len;
                terminal->content_len += content.len;
            } else if (content_result != GHOSTTY_SUCCESS) {
                terminal->cells_len = 0;
                return 2;
            }

            x++;
        }
        y++;
    }

    return 0;
}

static const GraithGhosttyCell* graith_ghostty_terminal_cells(
    GraithGhosttyTerminal* terminal
) {
    return terminal == NULL ? NULL : terminal->cells;
}

static size_t graith_ghostty_terminal_cells_len(GraithGhosttyTerminal* terminal) {
    return terminal == NULL ? 0 : terminal->cells_len;
}

static const uint8_t* graith_ghostty_terminal_content(
    GraithGhosttyTerminal* terminal
) {
    return terminal == NULL ? NULL : terminal->content;
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

var (
	errGhosttyClosed      = errors.New("libghostty-vt terminal is closed")
	errGhosttyInit        = errors.New("libghostty-vt terminal initialization failed")
	errGhosttySnapshot    = errors.New("libghostty-vt render-state snapshot failed")
	errGhosttySnapshotOOM = errors.New("libghostty-vt render-state snapshot allocation failed")
)

// ghosttyTerminal is an experimental libghostty-vt adapter. It is omitted from
// normal builds and can only be compiled with both cgo and the libghostty build
// tag. Linking is intentionally supplied by the caller's external linker
// flags, so the default daemon and release build do not acquire a native
// dependency.
type ghosttyTerminal struct {
	handle *C.GraithGhosttyTerminal
	cols   int
	rows   int
	cells  []Cell
	dirty  bool
}

var _ Terminal = (*ghosttyTerminal)(nil)

func newGhosttyTerminal(cols, rows int) (*ghosttyTerminal, error) {
	cols, rows = clampGhosttySize(cols, rows)
	handle := C.graith_ghostty_terminal_new(C.uint16_t(cols), C.uint16_t(rows))
	if handle == nil {
		return nil, errGhosttyInit
	}

	return &ghosttyTerminal{
		handle: handle,
		cols:   cols,
		rows:   rows,
		dirty:  true,
	}, nil
}

func (gt *ghosttyTerminal) Write(p []byte) (int, error) {
	if gt.handle == nil {
		return 0, errGhosttyClosed
	}
	if len(p) == 0 {
		return 0, nil
	}

	C.graith_ghostty_terminal_write(
		gt.handle,
		(*C.uint8_t)(unsafe.Pointer(&p[0])),
		C.size_t(len(p)),
	)
	gt.dirty = true

	return len(p), nil
}

func (gt *ghosttyTerminal) Resize(cols, rows int) {
	if gt.handle == nil {
		return
	}

	cols, rows = clampGhosttySize(cols, rows)
	if !bool(C.graith_ghostty_terminal_resize(
		gt.handle,
		C.uint16_t(cols),
		C.uint16_t(rows),
	)) {
		return
	}

	gt.cols = cols
	gt.rows = rows
	gt.dirty = true
}

func (gt *ghosttyTerminal) Size() (int, int) {
	return gt.cols, gt.rows
}

func (gt *ghosttyTerminal) Cursor() (int, int, bool) {
	if gt.handle == nil {
		return 0, 0, false
	}

	var x, y C.uint16_t
	var visible C.bool
	if !bool(C.graith_ghostty_terminal_cursor(gt.handle, &x, &y, &visible)) {
		return 0, 0, false
	}

	return int(x), int(y), bool(visible)
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

func (gt *ghosttyTerminal) Close() error {
	if gt.handle != nil {
		C.graith_ghostty_terminal_free(gt.handle)
		gt.handle = nil
		gt.cells = nil
	}

	return nil
}

func (gt *ghosttyTerminal) refreshCells() error {
	if gt.handle == nil {
		return errGhosttyClosed
	}
	if !gt.dirty {
		return nil
	}

	switch result := int(C.graith_ghostty_terminal_snapshot(
		gt.handle,
		C.uint16_t(gt.cols),
		C.uint16_t(gt.rows),
	)); result {
	case 0:
	case 1:
		return errGhosttySnapshotOOM
	default:
		return errGhosttySnapshot
	}

	count := int(C.graith_ghostty_terminal_cells_len(gt.handle))
	if count != gt.cols*gt.rows {
		return errGhosttySnapshot
	}

	if cap(gt.cells) < count {
		gt.cells = make([]Cell, count)
	} else {
		gt.cells = gt.cells[:count]
	}

	nativeCells := unsafe.Slice(C.graith_ghostty_terminal_cells(gt.handle), count)
	contentBase := C.graith_ghostty_terminal_content(gt.handle)
	for i := range nativeCells {
		native := &nativeCells[i]
		content := " "
		if native.content_len > 0 {
			start := unsafe.Add(unsafe.Pointer(contentBase), uintptr(native.content_offset))
			content = string(unsafe.Slice((*byte)(start), int(native.content_len)))
		} else if native.wide == C.uint8_t(C.GHOSTTY_CELL_WIDE_SPACER_TAIL) ||
			native.wide == C.uint8_t(C.GHOSTTY_CELL_WIDE_SPACER_HEAD) {
			content = ""
		}

		gt.cells[i] = Cell{
			Content: content,
			Style: CellStyle{
				FG:            ghosttyColor(native.fg_kind, native.fg_value),
				BG:            ghosttyColor(native.bg_kind, native.bg_value),
				Bold:          native.bold != 0,
				Faint:         native.faint != 0,
				Italic:        native.italic != 0,
				Underline:     native.underline != 0,
				Blink:         native.blink != 0,
				Reverse:       native.reverse != 0,
				Strikethrough: native.strikethrough != 0,
			},
		}
	}

	gt.dirty = false
	return nil
}

func ghosttyColor(kind C.uint8_t, value C.uint32_t) Color {
	switch kind {
	case 1:
		return Color{Kind: ColorIndexed, Value: uint32(value)}
	case 2:
		return Color{Kind: ColorRGB, Value: uint32(value)}
	default:
		return Color{Kind: ColorDefault}
	}
}

func clampGhosttySize(cols, rows int) (int, int) {
	cols, rows = clampSize(cols, rows)
	if cols > int(^uint16(0)) {
		cols = int(^uint16(0))
	}
	if rows > int(^uint16(0)) {
		rows = int(^uint16(0))
	}

	return cols, rows
}
