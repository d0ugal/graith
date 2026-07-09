import Foundation
import GraithClientAPI
import GraithTerminalCore

// The real `TerminalCoreDriving`, backed by the shared VT core
// `GhosttyTerminalState` (libghostty-vt). This is the production replacement for
// `GraithMobileMock.MockTerminalCore` behind the `BaseTerminalUIView` seam.
//
// It is a thin **wrapper** rather than the `extension GhosttyTerminalState:
// TerminalCoreDriving` the adapter table sketched, because two boundary members
// are properties (`isViewportAtBottom`, `isBracketedPasteEnabled`) whose names
// collide with `GhosttyTerminalState`'s same-named methods — Swift forbids a
// property and method sharing a base name on one type, so an extension can't
// conform. The wrapper forwards to those methods with no such clash.
//
// This target is deliberately NOT linked by any executable in this package:
// GraithTerminalCore → CGhosttyVT → libghostty-vt.a is macOS-only + unpinned
// (Task 13), so wiring it into the live app must wait for the pinned
// .xcframework. As a library it still type-checks against the real core here.
public final class GhosttyTerminalDriver: TerminalCoreDriving {
    private let core: GhosttyTerminalState

    public init(core: GhosttyTerminalState) {
        self.core = core
        core.initSelection()
    }

    public convenience init(cols: UInt16, rows: UInt16, maxScrollback: Int = 10000) {
        self.init(core: GhosttyTerminalState(cols: cols, rows: rows, maxScrollback: maxScrollback))
    }

    deinit {
        core.freeSelection()
    }

    // MARK: - Geometry

    public var cols: UInt16 { core.cols }
    public var rows: UInt16 { core.rows }

    // MARK: - I/O

    public func feedOutput(_ data: Data) {
        core.write(data)
    }

    public func encode(_ stroke: TerminalKeyStroke) -> Data? {
        let (key, text) = Self.mapKey(stroke.key)
        return core.encodeKey(
            action: Self.mapAction(stroke.action),
            key: key,
            mods: Self.mapMods(stroke.modifiers),
            text: text
        )
    }

    public func resize(cols: UInt16, rows: UInt16, cellWidth: UInt32, cellHeight: UInt32) {
        core.resize(cols: cols, rows: rows, cellWidth: cellWidth, cellHeight: cellHeight)
    }

    // MARK: - Scrollback

    public func scrollViewport(byRows delta: Int) {
        core.scrollViewport(delta: delta)
    }

    public func scrollToBottom() {
        core.scrollToBottom()
    }

    public var isViewportAtBottom: Bool {
        core.isViewportAtBottom()
    }

    public func scrollMetrics() -> ScrollMetrics {
        let sb = core.scrollbar()
        return ScrollMetrics(total: sb.total, offset: sb.offset, len: sb.len)
    }

    public var isMouseTrackingActive: Bool {
        core.isMouseTrackingActive()
    }

    public func encodeScrollWheel(ticks: Int, surfaceX: Double, surfaceY: Double,
                                  screenWidth: UInt32, screenHeight: UInt32,
                                  cellWidth: UInt32, cellHeight: UInt32) -> [Data] {
        guard ticks != 0 else { return [] }
        // Wheel-down (button 5) scrolls content down/forward; wheel-up (button 4)
        // scrolls back. Mirrors the macOS BaseTerminalNSView.handleMouseScroll
        // mapping (delta < 0 ⇒ button five).
        let button: GhosttyMouseButton = ticks > 0 ? GHOSTTY_MOUSE_BUTTON_FIVE : GHOSTTY_MOUSE_BUTTON_FOUR
        core.setMouseEncoderSize(screenWidth: screenWidth, screenHeight: screenHeight,
                                 cellWidth: cellWidth, cellHeight: cellHeight)
        var out: [Data] = []
        for _ in 0..<abs(ticks) {
            if let encoded = core.encodeMouse(
                action: GHOSTTY_MOUSE_ACTION_PRESS,
                button: button, mods: 0,
                x: Float(surfaceX), y: Float(surfaceY)
            ) {
                out.append(encoded)
            }
        }
        return out
    }

    // MARK: - Selection

    public func beginSelection(at cell: ViewportCell, surfaceX: Double, surfaceY: Double, timeNs: UInt64) {
        core.selectionPress(col: cell.col, row: cell.row, surfaceX: surfaceX, surfaceY: surfaceY, timeNs: timeNs)
    }

    public func dragSelection(to cell: ViewportCell, surfaceX: Double, surfaceY: Double,
                              columns: UInt32, cellWidth: UInt32, screenHeight: UInt32) {
        core.selectionDrag(col: cell.col, row: cell.row, surfaceX: surfaceX, surfaceY: surfaceY,
                           columns: columns, cellWidth: cellWidth, screenHeight: screenHeight)
    }

    public func endSelection(at cell: ViewportCell) {
        core.selectionRelease(col: cell.col, row: cell.row)
    }

    public func clearSelection() {
        core.clearSelection()
    }

    public func selectedText() -> String? {
        core.getSelectedText()
    }

    public var isBracketedPasteEnabled: Bool {
        core.isBracketedPasteEnabled()
    }

    // MARK: - Key mapping (TerminalKey/Modifiers → ghostty C enums)

    /// Map a logical key to a ghostty key plus optional UTF-8 text. Printable
    /// characters travel as text with an unidentified key so the core's encoder
    /// applies legacy / kitty / modifyOtherKeys rules itself.
    static func mapKey(_ key: TerminalKey) -> (GhosttyKey, String?) {
        switch key {
        case let .character(text): return (GHOSTTY_KEY_UNIDENTIFIED, text)
        case .enter:               return (GHOSTTY_KEY_ENTER, nil)
        case .tab:                 return (GHOSTTY_KEY_TAB, nil)
        case .backspace:           return (GHOSTTY_KEY_BACKSPACE, nil)
        case .escape:              return (GHOSTTY_KEY_ESCAPE, nil)
        case .delete:              return (GHOSTTY_KEY_DELETE, nil)
        case .arrowUp:             return (GHOSTTY_KEY_ARROW_UP, nil)
        case .arrowDown:           return (GHOSTTY_KEY_ARROW_DOWN, nil)
        case .arrowLeft:           return (GHOSTTY_KEY_ARROW_LEFT, nil)
        case .arrowRight:          return (GHOSTTY_KEY_ARROW_RIGHT, nil)
        case .home:                return (GHOSTTY_KEY_HOME, nil)
        case .end:                 return (GHOSTTY_KEY_END, nil)
        case .pageUp:              return (GHOSTTY_KEY_PAGE_UP, nil)
        case .pageDown:            return (GHOSTTY_KEY_PAGE_DOWN, nil)
        case .insert:              return (GHOSTTY_KEY_INSERT, nil)
        case let .function(n):     return (Self.functionKey(n), nil)
        }
    }

    static func functionKey(_ n: Int) -> GhosttyKey {
        switch n {
        case 1:  return GHOSTTY_KEY_F1
        case 2:  return GHOSTTY_KEY_F2
        case 3:  return GHOSTTY_KEY_F3
        case 4:  return GHOSTTY_KEY_F4
        case 5:  return GHOSTTY_KEY_F5
        case 6:  return GHOSTTY_KEY_F6
        case 7:  return GHOSTTY_KEY_F7
        case 8:  return GHOSTTY_KEY_F8
        case 9:  return GHOSTTY_KEY_F9
        case 10: return GHOSTTY_KEY_F10
        case 11: return GHOSTTY_KEY_F11
        case 12: return GHOSTTY_KEY_F12
        default: return GHOSTTY_KEY_UNIDENTIFIED
        }
    }

    static func mapMods(_ mods: TerminalModifiers) -> GhosttyMods {
        var m: GhosttyMods = 0
        if mods.contains(.shift) { m |= GhosttyMods(GHOSTTY_MODS_SHIFT) }
        if mods.contains(.control) { m |= GhosttyMods(GHOSTTY_MODS_CTRL) }
        if mods.contains(.option) { m |= GhosttyMods(GHOSTTY_MODS_ALT) }
        if mods.contains(.command) { m |= GhosttyMods(GHOSTTY_MODS_SUPER) }
        return m
    }

    static func mapAction(_ action: TerminalKeyAction) -> GhosttyKeyAction {
        switch action {
        case .press:     return GHOSTTY_KEY_ACTION_PRESS
        case .release:   return GHOSTTY_KEY_ACTION_RELEASE
        case .repeatKey: return GHOSTTY_KEY_ACTION_REPEAT
        }
    }
}
