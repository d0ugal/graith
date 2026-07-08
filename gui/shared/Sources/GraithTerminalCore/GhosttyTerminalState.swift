import Foundation
// Re-export the C shim so consumers of GraithTerminalCore (the macOS + iOS
// apps) get the ghostty types used across this module's public API (GhosttyKey,
// GhosttyMods, GhosttySelection, …) without importing CGhosttyVT themselves.
@_exported import CGhosttyVT

/// Platform-neutral wrapper around `libghostty-vt`: VT parsing, grid/render
/// state, key + mouse encoding, scrollback, and selection.
///
/// This type has no AppKit/UIKit/SwiftUI dependencies (only Foundation +
/// `CGhosttyVT`), so it is shared verbatim by the macOS and iOS terminal
/// views — the load-bearing "reusable core" the design identifies.
public final class GhosttyTerminalState {
    private var terminal: GhosttyTerminal?
    private var renderState: GhosttyRenderState?
    private var rowIterator: GhosttyRenderStateRowIterator?
    private var rowCells: GhosttyRenderStateRowCells?
    private var keyEncoder: GhosttyKeyEncoder?
    private var keyEvent: GhosttyKeyEvent?
    private var mouseEncoder: GhosttyMouseEncoder?
    private var mouseEvent: GhosttyMouseEvent?
    private var anyButtonPressed = false

    // Live grid geometry. Mutable because `resize()` reflows the underlying
    // libghostty grid — a frozen `let` here would leave the renderer laying out
    // cells at the initial width after any resize.
    public private(set) var cols: UInt16
    public private(set) var rows: UInt16

    public init(cols: UInt16, rows: UInt16, maxScrollback: Int = 10000) {
        self.cols = cols
        self.rows = rows

        let opts = GhosttyTerminalOptions(
            cols: cols,
            rows: rows,
            max_scrollback: maxScrollback
        )

        var term: GhosttyTerminal?
        let result = ghostty_terminal_new(nil, &term, opts)
        if result == GHOSTTY_SUCCESS {
            self.terminal = term
        }

        var rs: GhosttyRenderState?
        let rsResult = ghostty_render_state_new(nil, &rs)
        if rsResult == GHOSTTY_SUCCESS {
            self.renderState = rs
        }

        var ri: GhosttyRenderStateRowIterator?
        let riResult = ghostty_render_state_row_iterator_new(nil, &ri)
        if riResult == GHOSTTY_SUCCESS {
            self.rowIterator = ri
        }

        var rc: GhosttyRenderStateRowCells?
        let rcResult = ghostty_render_state_row_cells_new(nil, &rc)
        if rcResult == GHOSTTY_SUCCESS {
            self.rowCells = rc
        }

        var ke: GhosttyKeyEncoder?
        if ghostty_key_encoder_new(nil, &ke) == GHOSTTY_SUCCESS {
            self.keyEncoder = ke
        }

        var kev: GhosttyKeyEvent?
        if ghostty_key_event_new(nil, &kev) == GHOSTTY_SUCCESS {
            self.keyEvent = kev
        }

        var me: GhosttyMouseEncoder?
        if ghostty_mouse_encoder_new(nil, &me) == GHOSTTY_SUCCESS {
            self.mouseEncoder = me
        }

        var mev: GhosttyMouseEvent?
        if ghostty_mouse_event_new(nil, &mev) == GHOSTTY_SUCCESS {
            self.mouseEvent = mev
        }
    }

    deinit {
        if let mev = mouseEvent { ghostty_mouse_event_free(mev) }
        if let me = mouseEncoder { ghostty_mouse_encoder_free(me) }
        if let kev = keyEvent { ghostty_key_event_free(kev) }
        if let ke = keyEncoder { ghostty_key_encoder_free(ke) }
        if let rc = rowCells { ghostty_render_state_row_cells_free(rc) }
        if let ri = rowIterator { ghostty_render_state_row_iterator_free(ri) }
        if let rs = renderState { ghostty_render_state_free(rs) }
        if let t = terminal { ghostty_terminal_free(t) }
    }

    public func write(_ data: Data) {
        guard let terminal else { return }
        data.withUnsafeBytes { buf in
            if let ptr = buf.baseAddress?.assumingMemoryBound(to: UInt8.self) {
                ghostty_terminal_vt_write(terminal, ptr, buf.count)
            }
        }
    }

    public func write(_ string: String) {
        guard let data = string.data(using: .utf8) else { return }
        write(data)
    }

    public func resize(cols: UInt16, rows: UInt16, cellWidth: UInt32 = 8, cellHeight: UInt32 = 16) {
        guard let terminal else { return }
        ghostty_terminal_resize(terminal, cols, rows, cellWidth, cellHeight)
        self.cols = cols
        self.rows = rows
    }

    public func updateRenderState() -> GhosttyRenderStateDirty {
        guard let terminal, let renderState else { return GHOSTTY_RENDER_STATE_DIRTY_FALSE }
        ghostty_render_state_update(renderState, terminal)

        var dirty = GHOSTTY_RENDER_STATE_DIRTY_FALSE
        ghostty_render_state_get(renderState, GHOSTTY_RENDER_STATE_DATA_DIRTY, &dirty)
        return dirty
    }

    public func getColors() -> GhosttyRenderStateColors {
        var colors = GhosttyRenderStateColors()
        colors.size = MemoryLayout<GhosttyRenderStateColors>.size
        if let renderState {
            ghostty_render_state_colors_get(renderState, &colors)
        }
        return colors
    }

    public func getCursorInfo() -> (visible: Bool, x: UInt16, y: UInt16, style: GhosttyRenderStateCursorVisualStyle)? {
        guard let renderState else { return nil }

        var visible = false
        ghostty_render_state_get(renderState, GHOSTTY_RENDER_STATE_DATA_CURSOR_VISIBLE, &visible)
        guard visible else { return nil }

        var inViewport = false
        ghostty_render_state_get(renderState, GHOSTTY_RENDER_STATE_DATA_CURSOR_VIEWPORT_HAS_VALUE, &inViewport)
        guard inViewport else { return nil }

        var cx: UInt16 = 0
        var cy: UInt16 = 0
        var style = GHOSTTY_RENDER_STATE_CURSOR_VISUAL_STYLE_BLOCK
        ghostty_render_state_get(renderState, GHOSTTY_RENDER_STATE_DATA_CURSOR_VIEWPORT_X, &cx)
        ghostty_render_state_get(renderState, GHOSTTY_RENDER_STATE_DATA_CURSOR_VIEWPORT_Y, &cy)
        ghostty_render_state_get(renderState, GHOSTTY_RENDER_STATE_DATA_CURSOR_VISUAL_STYLE, &style)

        return (visible: true, x: cx, y: cy, style: style)
    }

    public struct CellInfo {
        public let codepoints: [UInt32]
        public let fgColor: GhosttyColorRgb?
        public let bgColor: GhosttyColorRgb?
        public let bold: Bool
        public let italic: Bool
        public let underline: Bool
        public let strikethrough: Bool
        public let isEmpty: Bool
    }

    public func iterateRows(_ callback: (_ row: Int, _ dirty: Bool, _ cells: [CellInfo]) -> Void) {
        guard let renderState, var rowIterator = rowIterator, var rowCells = rowCells else { return }

        ghostty_render_state_get(renderState, GHOSTTY_RENDER_STATE_DATA_ROW_ITERATOR, &rowIterator)
        self.rowIterator = rowIterator

        var rowIndex = 0
        while ghostty_render_state_row_iterator_next(rowIterator) {
            var rowDirty = false
            ghostty_render_state_row_get(rowIterator, GHOSTTY_RENDER_STATE_ROW_DATA_DIRTY, &rowDirty)

            ghostty_render_state_row_get(rowIterator, GHOSTTY_RENDER_STATE_ROW_DATA_CELLS, &rowCells)
            self.rowCells = rowCells

            var cells: [CellInfo] = []
            while ghostty_render_state_row_cells_next(rowCells) {
                var graphemeLen: UInt32 = 0
                ghostty_render_state_row_cells_get(
                    rowCells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_GRAPHEMES_LEN, &graphemeLen)

                if graphemeLen == 0 {
                    cells.append(CellInfo(codepoints: [], fgColor: nil, bgColor: nil,
                                          bold: false, italic: false, underline: false,
                                          strikethrough: false, isEmpty: true))
                    continue
                }

                var fgColor = GhosttyColorRgb()
                let fgResult = ghostty_render_state_row_cells_get(
                    rowCells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_FG_COLOR, &fgColor)

                var bgColor = GhosttyColorRgb()
                let bgResult = ghostty_render_state_row_cells_get(
                    rowCells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_BG_COLOR, &bgColor)

                var hasStyling = false
                ghostty_render_state_row_cells_get(
                    rowCells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_HAS_STYLING, &hasStyling)

                var bold = false, italic = false, underline = false, strikethrough = false
                if hasStyling {
                    var style = GhosttyStyle()
                    style.size = MemoryLayout<GhosttyStyle>.size
                    ghostty_render_state_row_cells_get(
                        rowCells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_STYLE, &style)
                    bold = style.bold
                    italic = style.italic
                    underline = style.underline != 0
                    strikethrough = style.strikethrough
                }

                var codepoints = [UInt32](repeating: 0, count: Int(graphemeLen))
                ghostty_render_state_row_cells_get(
                    rowCells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_GRAPHEMES_BUF, &codepoints)

                cells.append(CellInfo(
                    codepoints: codepoints,
                    fgColor: fgResult == GHOSTTY_SUCCESS ? fgColor : nil,
                    bgColor: bgResult == GHOSTTY_SUCCESS ? bgColor : nil,
                    bold: bold, italic: italic, underline: underline,
                    strikethrough: strikethrough, isEmpty: false
                ))
            }

            callback(rowIndex, rowDirty, cells)

            var clean = false
            ghostty_render_state_row_set(rowIterator, GHOSTTY_RENDER_STATE_ROW_OPTION_DIRTY, &clean)

            rowIndex += 1
        }

        var cleanState = GHOSTTY_RENDER_STATE_DIRTY_FALSE
        ghostty_render_state_set(renderState, GHOSTTY_RENDER_STATE_OPTION_DIRTY, &cleanState)
    }

    // MARK: - Key Encoding

    public func syncEncoderOptions() {
        guard let terminal, let keyEncoder else { return }
        ghostty_key_encoder_setopt_from_terminal(keyEncoder, terminal)
        var optionAsAlt = GHOSTTY_OPTION_AS_ALT_TRUE
        ghostty_key_encoder_setopt(keyEncoder, GHOSTTY_KEY_ENCODER_OPT_MACOS_OPTION_AS_ALT, &optionAsAlt)
    }

    public func encodeKey(action: GhosttyKeyAction, key: GhosttyKey, mods: GhosttyMods, text: String?) -> Data? {
        guard let keyEncoder, let keyEvent else { return nil }

        syncEncoderOptions()

        ghostty_key_event_set_action(keyEvent, action)
        ghostty_key_event_set_key(keyEvent, key)
        ghostty_key_event_set_mods(keyEvent, mods)

        func doEncode() -> Data? {
            var buf = [CChar](repeating: 0, count: 128)
            var written: Int = 0
            let result = ghostty_key_encoder_encode(keyEncoder, keyEvent, &buf, buf.count, &written)
            if result == GHOSTTY_SUCCESS && written > 0 {
                return Data(bytes: buf, count: written)
            }
            return nil
        }

        if let text, !text.isEmpty {
            return text.withCString { cstr in
                ghostty_key_event_set_utf8(keyEvent, cstr, text.utf8.count)
                return doEncode()
            }
        } else {
            ghostty_key_event_set_utf8(keyEvent, nil, 0)
            return doEncode()
        }
    }

    // MARK: - Mouse Encoding

    public func isMouseTrackingActive() -> Bool {
        guard let terminal else { return false }
        var tracking = false
        ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_MOUSE_TRACKING, &tracking)
        return tracking
    }

    public func updateMouseEncoderFromTerminal() {
        guard let terminal, let mouseEncoder else { return }
        ghostty_mouse_encoder_setopt_from_terminal(mouseEncoder, terminal)
    }

    public func setMouseEncoderSize(screenWidth: UInt32, screenHeight: UInt32,
                                    cellWidth: UInt32, cellHeight: UInt32) {
        guard let mouseEncoder else { return }
        var size = GhosttyMouseEncoderSize()
        size.size = MemoryLayout<GhosttyMouseEncoderSize>.size
        size.screen_width = screenWidth
        size.screen_height = screenHeight
        size.cell_width = cellWidth
        size.cell_height = cellHeight
        size.padding_top = 0
        size.padding_bottom = 0
        size.padding_left = 0
        size.padding_right = 0
        ghostty_mouse_encoder_setopt(mouseEncoder, GHOSTTY_MOUSE_ENCODER_OPT_SIZE, &size)
    }

    public func encodeMouse(action: GhosttyMouseAction, button: GhosttyMouseButton?,
                            mods: GhosttyMods, x: Float, y: Float) -> Data? {
        guard let mouseEncoder, let mouseEvent else { return nil }

        updateMouseEncoderFromTerminal()

        ghostty_mouse_event_set_action(mouseEvent, action)
        if let button {
            ghostty_mouse_event_set_button(mouseEvent, button)
        } else {
            ghostty_mouse_event_clear_button(mouseEvent)
        }
        ghostty_mouse_event_set_mods(mouseEvent, mods)
        ghostty_mouse_event_set_position(mouseEvent, GhosttyMousePosition(x: x, y: y))

        if action == GHOSTTY_MOUSE_ACTION_PRESS {
            anyButtonPressed = true
            var pressed = true
            ghostty_mouse_encoder_setopt(mouseEncoder, GHOSTTY_MOUSE_ENCODER_OPT_ANY_BUTTON_PRESSED, &pressed)
        } else if action == GHOSTTY_MOUSE_ACTION_RELEASE {
            anyButtonPressed = false
            var pressed = false
            ghostty_mouse_encoder_setopt(mouseEncoder, GHOSTTY_MOUSE_ENCODER_OPT_ANY_BUTTON_PRESSED, &pressed)
        }

        var buf = [CChar](repeating: 0, count: 128)
        var written: Int = 0
        let result = ghostty_mouse_encoder_encode(mouseEncoder, mouseEvent, &buf, buf.count, &written)

        if result == GHOSTTY_SUCCESS && written > 0 {
            return Data(bytes: buf, count: written)
        }
        return nil
    }

    // MARK: - Scrollback

    public func scrollViewport(delta: Int) {
        guard let terminal else { return }
        var behavior = GhosttyTerminalScrollViewport()
        behavior.tag = GHOSTTY_SCROLL_VIEWPORT_DELTA
        behavior.value.delta = delta
        ghostty_terminal_scroll_viewport(terminal, behavior)
    }

    public func scrollToTop() {
        guard let terminal else { return }
        var behavior = GhosttyTerminalScrollViewport()
        behavior.tag = GHOSTTY_SCROLL_VIEWPORT_TOP
        ghostty_terminal_scroll_viewport(terminal, behavior)
    }

    public func scrollToBottom() {
        guard let terminal else { return }
        var behavior = GhosttyTerminalScrollViewport()
        behavior.tag = GHOSTTY_SCROLL_VIEWPORT_BOTTOM
        ghostty_terminal_scroll_viewport(terminal, behavior)
    }

    public func isViewportAtBottom() -> Bool {
        guard let terminal else { return true }
        var active = true
        ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_VIEWPORT_ACTIVE, &active)
        return active
    }

    // MARK: - Selection

    private var selectionGesture: GhosttySelectionGesture?
    private var pressEvent: GhosttySelectionGestureEvent?
    private var dragEvent: GhosttySelectionGestureEvent?
    private var releaseEvent: GhosttySelectionGestureEvent?
    public private(set) var currentSelection: GhosttySelection?

    public func initSelection() {
        guard terminal != nil else { return }
        var gesture: GhosttySelectionGesture?
        if ghostty_selection_gesture_new(nil, &gesture) == GHOSTTY_SUCCESS {
            self.selectionGesture = gesture
        }
        var pe: GhosttySelectionGestureEvent?
        if ghostty_selection_gesture_event_new(nil, &pe, GHOSTTY_SELECTION_GESTURE_EVENT_TYPE_PRESS) == GHOSTTY_SUCCESS {
            self.pressEvent = pe
        }
        var de: GhosttySelectionGestureEvent?
        if ghostty_selection_gesture_event_new(nil, &de, GHOSTTY_SELECTION_GESTURE_EVENT_TYPE_DRAG) == GHOSTTY_SUCCESS {
            self.dragEvent = de
        }
        var re: GhosttySelectionGestureEvent?
        if ghostty_selection_gesture_event_new(nil, &re, GHOSTTY_SELECTION_GESTURE_EVENT_TYPE_RELEASE) == GHOSTTY_SUCCESS {
            self.releaseEvent = re
        }
    }

    public func freeSelection() {
        if let re = releaseEvent { ghostty_selection_gesture_event_free(re) }
        releaseEvent = nil
        if let de = dragEvent { ghostty_selection_gesture_event_free(de) }
        dragEvent = nil
        if let pe = pressEvent { ghostty_selection_gesture_event_free(pe) }
        pressEvent = nil
        if let gesture = selectionGesture {
            ghostty_selection_gesture_free(gesture, terminal)
        }
        selectionGesture = nil
        currentSelection = nil
    }

    public func gridRefAtViewport(x: UInt16, y: UInt32) -> GhosttyGridRef? {
        guard let terminal else { return nil }
        var point = GhosttyPoint()
        point.tag = GHOSTTY_POINT_TAG_VIEWPORT
        point.value.coordinate = GhosttyPointCoordinate(x: x, y: y)
        var ref = GhosttyGridRef()
        ref.size = MemoryLayout<GhosttyGridRef>.size
        if ghostty_terminal_grid_ref(terminal, point, &ref) == GHOSTTY_SUCCESS {
            return ref
        }
        return nil
    }

    public func selectionPress(col: UInt16, row: UInt32, surfaceX: Double, surfaceY: Double, timeNs: UInt64) {
        guard let terminal, let gesture = selectionGesture, let event = pressEvent else { return }

        guard var ref = gridRefAtViewport(x: col, y: row) else { return }

        ghostty_selection_gesture_event_set(event, GHOSTTY_SELECTION_GESTURE_EVENT_OPT_REF, &ref)
        var pos = GhosttySurfacePosition(x: surfaceX, y: surfaceY)
        ghostty_selection_gesture_event_set(event, GHOSTTY_SELECTION_GESTURE_EVENT_OPT_POSITION, &pos)
        var time = timeNs
        ghostty_selection_gesture_event_set(event, GHOSTTY_SELECTION_GESTURE_EVENT_OPT_TIME_NS, &time)
        var repeatDist: Double = 4.0
        ghostty_selection_gesture_event_set(event, GHOSTTY_SELECTION_GESTURE_EVENT_OPT_REPEAT_DISTANCE, &repeatDist)
        var repeatInterval: UInt64 = 500_000_000
        ghostty_selection_gesture_event_set(event, GHOSTTY_SELECTION_GESTURE_EVENT_OPT_REPEAT_INTERVAL_NS, &repeatInterval)

        var sel = GhosttySelection()
        sel.size = MemoryLayout<GhosttySelection>.size
        let result = ghostty_selection_gesture_event(gesture, terminal, event, &sel)
        if result == GHOSTTY_SUCCESS {
            currentSelection = sel
            ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_SELECTION, &sel)
        }
    }

    public func selectionDrag(col: UInt16, row: UInt32, surfaceX: Double, surfaceY: Double,
                              columns: UInt32, cellWidth: UInt32, screenHeight: UInt32) {
        guard let terminal, let gesture = selectionGesture, let event = dragEvent else { return }

        guard var ref = gridRefAtViewport(x: col, y: row) else { return }

        ghostty_selection_gesture_event_set(event, GHOSTTY_SELECTION_GESTURE_EVENT_OPT_REF, &ref)
        var pos = GhosttySurfacePosition(x: surfaceX, y: surfaceY)
        ghostty_selection_gesture_event_set(event, GHOSTTY_SELECTION_GESTURE_EVENT_OPT_POSITION, &pos)
        var geo = GhosttySelectionGestureGeometry(
            columns: columns, cell_width: cellWidth,
            padding_left: 0, screen_height: screenHeight
        )
        ghostty_selection_gesture_event_set(event, GHOSTTY_SELECTION_GESTURE_EVENT_OPT_GEOMETRY, &geo)

        var sel = GhosttySelection()
        sel.size = MemoryLayout<GhosttySelection>.size
        let result = ghostty_selection_gesture_event(gesture, terminal, event, &sel)
        if result == GHOSTTY_SUCCESS {
            currentSelection = sel
            ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_SELECTION, &sel)
        }
    }

    public func selectionRelease(col: UInt16, row: UInt32) {
        guard let terminal, let gesture = selectionGesture, let event = releaseEvent else { return }

        if var ref = gridRefAtViewport(x: col, y: row) {
            ghostty_selection_gesture_event_set(event, GHOSTTY_SELECTION_GESTURE_EVENT_OPT_REF, &ref)
        } else {
            ghostty_selection_gesture_event_set(event, GHOSTTY_SELECTION_GESTURE_EVENT_OPT_REF, nil)
        }

        ghostty_selection_gesture_event(gesture, terminal, event, nil)
    }

    public func clearSelection() {
        guard let terminal else { return }
        ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_SELECTION, nil)
        currentSelection = nil
    }

    public func getSelectedText() -> String? {
        guard let terminal else { return nil }

        var opts = GhosttyTerminalSelectionFormatOptions()
        opts.size = MemoryLayout<GhosttyTerminalSelectionFormatOptions>.size
        opts.emit = GHOSTTY_FORMATTER_FORMAT_PLAIN
        opts.unwrap = true
        opts.trim = true
        opts.selection = nil

        var needed: Int = 0
        let sizeResult = ghostty_terminal_selection_format_buf(terminal, opts, nil, 0, &needed)
        guard sizeResult == GHOSTTY_OUT_OF_SPACE && needed > 0 else { return nil }

        var buf = [UInt8](repeating: 0, count: needed)
        var written: Int = 0
        let result = ghostty_terminal_selection_format_buf(terminal, opts, &buf, buf.count, &written)
        guard result == GHOSTTY_SUCCESS && written > 0 else { return nil }

        return String(bytes: buf[0..<written], encoding: .utf8)
    }

    public func selectionContains(col: UInt16, row: UInt32) -> Bool {
        guard let terminal, let sel = currentSelection else { return false }
        var point = GhosttyPoint()
        point.tag = GHOSTTY_POINT_TAG_VIEWPORT
        point.value.coordinate = GhosttyPointCoordinate(x: col, y: row)
        var mutableSel = sel
        var contains = false
        ghostty_terminal_selection_contains(terminal, &mutableSel, point, &contains)
        return contains
    }

    // MARK: - Mode Queries

    public func isBracketedPasteEnabled() -> Bool {
        guard let terminal else { return false }
        var enabled: Bool = false
        let bracketedPasteMode: GhosttyMode = ghostty_mode_new(2004, false)
        let result = ghostty_terminal_mode_get(terminal, bracketedPasteMode, &enabled)
        return result == GHOSTTY_SUCCESS && enabled
    }

    // MARK: - Hyperlink Detection

    public func hyperlinkAtViewport(col: UInt16, row: UInt32) -> String? {
        guard let ref = gridRefAtViewport(x: col, y: row) else { return nil }
        var mutableRef = ref

        var needed: Int = 0
        let sizeResult = ghostty_grid_ref_hyperlink_uri(&mutableRef, nil, 0, &needed)
        guard sizeResult == GHOSTTY_OUT_OF_SPACE && needed > 0 else { return nil }

        var buf = [UInt8](repeating: 0, count: needed)
        var written: Int = 0
        let result = ghostty_grid_ref_hyperlink_uri(&mutableRef, &buf, buf.count, &written)
        guard result == GHOSTTY_SUCCESS && written > 0 else { return nil }

        return String(bytes: buf[0..<written], encoding: .utf8)
    }

    /// Extract all visible text from the terminal, one string per row.
    public func getVisibleText() -> [String] {
        var lines: [String] = []
        iterateRows { _, _, cells in
            let line = cells.map { cell -> String in
                if cell.isEmpty { return " " }
                let str = String(cell.codepoints.compactMap { UnicodeScalar($0) }.map { Character($0) })
                return str.isEmpty ? " " : str
            }.joined()
            lines.append(line)
        }
        return lines
    }

    public func setColors(fg: (UInt8, UInt8, UInt8), bg: (UInt8, UInt8, UInt8), cursor: (UInt8, UInt8, UInt8)) {
        guard let terminal else { return }

        var fgColor = GhosttyColorRgb(r: fg.0, g: fg.1, b: fg.2)
        ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_COLOR_FOREGROUND, &fgColor)

        var bgColor = GhosttyColorRgb(r: bg.0, g: bg.1, b: bg.2)
        ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_COLOR_BACKGROUND, &bgColor)

        var cursorColor = GhosttyColorRgb(r: cursor.0, g: cursor.1, b: cursor.2)
        ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_COLOR_CURSOR, &cursorColor)
    }
}
