import Foundation

// The seam between the iOS UIKit input layer (`GraithTerminalUIKit`,
// `BaseTerminalUIView`) and the shared VT core (`GhosttyTerminalState`, macOS
// track). The input view produces logical key strokes and text; the core turns
// them into the bytes to send on channel 0x01 and consumes the bytes that
// arrive. Modelling this as a protocol keeps the input view testable without
// linking `libghostty-vt` (which cannot be built on this host).
//
// `GhosttyTerminalState` already has methods of exactly this shape
// (`write`, `resize`, `encodeKey`, `scrollViewport`, selection gestures); the
// integration step is a thin `extension GhosttyTerminalState: TerminalCoreDriving`
// in the app target that maps `TerminalKey`/`TerminalModifiers` onto the ghostty
// C enums.

/// A logical modifier set, mapped to ghostty `GhosttyMods` in the adapter.
public struct TerminalModifiers: OptionSet, Sendable, Hashable {
    public let rawValue: Int
    public init(rawValue: Int) { self.rawValue = rawValue }

    public static let shift = TerminalModifiers(rawValue: 1 << 0)
    public static let control = TerminalModifiers(rawValue: 1 << 1)
    /// The Option/Alt key. On the on-screen row this is the "alt" sticky.
    public static let option = TerminalModifiers(rawValue: 1 << 2)
    /// The Command/Meta key (hardware keyboards on iPad).
    public static let command = TerminalModifiers(rawValue: 1 << 3)
}

/// A logical key. The subset a terminal input layer cares about; printable
/// characters travel as `.character` carrying their text so the core's key
/// encoder can apply legacy / kitty / modifyOtherKeys rules.
public enum TerminalKey: Sendable, Hashable {
    case character(String) // printable text (IME-committed or a single glyph)
    case enter
    case tab
    case backspace
    case escape
    case delete
    case arrowUp
    case arrowDown
    case arrowLeft
    case arrowRight
    case home
    case end
    case pageUp
    case pageDown
    case insert
    case function(Int) // F1…F12
}

public enum TerminalKeyAction: Sendable, Hashable {
    case press
    case release
    case repeatKey
}

/// One logical key event to encode.
public struct TerminalKeyStroke: Sendable, Hashable {
    public var key: TerminalKey
    public var modifiers: TerminalModifiers
    public var action: TerminalKeyAction

    public init(key: TerminalKey, modifiers: TerminalModifiers = [], action: TerminalKeyAction = .press) {
        self.key = key
        self.modifiers = modifiers
        self.action = action
    }
}

/// A cell coordinate in the visible viewport (0-based).
public struct ViewportCell: Sendable, Hashable {
    public var col: UInt16
    public var row: UInt32
    public init(col: UInt16, row: UInt32) {
        self.col = col
        self.row = row
    }
}

/// Scrollback geometry for drawing a scroll-position indicator and detecting
/// boundaries (issue #984). `total` is the scrollable height in rows, `offset`
/// the viewport's distance in rows from the top of that area, and `len` the
/// visible viewport height in rows. When `total <= len` there is nothing to
/// scroll (no history).
public struct ScrollMetrics: Sendable, Equatable {
    public var total: Int
    public var offset: Int
    public var len: Int
    public init(total: Int, offset: Int, len: Int) {
        self.total = total
        self.offset = offset
        self.len = len
    }

    /// True when the viewport sits at the live bottom (no room to scroll down).
    public var isAtBottom: Bool { offset >= max(0, total - len) }
    /// True when there is scrollback history to reveal.
    public var hasHistory: Bool { total > len }
}

/// What `BaseTerminalUIView` needs from the VT core. Implemented for real by
/// `GhosttyTerminalState` (via an adapter extension) and by
/// `GraithMobileMock.MockTerminalCore` for tests / previews.
public protocol TerminalCoreDriving: AnyObject {
    var cols: UInt16 { get }
    var rows: UInt16 { get }

    /// Feed channel 0x01 bytes from the daemon into the VT parser.
    func feedOutput(_ data: Data)

    /// Encode a logical key stroke to the bytes to send on channel 0x01.
    /// Returns nil when the stroke produces no output.
    func encode(_ stroke: TerminalKeyStroke) -> Data?

    /// Resize the local grid. The caller separately sends a `resize` control
    /// message so the remote PTY matches.
    func resize(cols: UInt16, rows: UInt16, cellWidth: UInt32, cellHeight: UInt32)

    // Scrollback.
    func scrollViewport(byRows delta: Int)
    func scrollToBottom()
    var isViewportAtBottom: Bool { get }

    /// Current scrollback geometry (for the scroll-position indicator and
    /// boundary detection). May be relatively expensive — read it only when
    /// updating the indicator, not on every frame.
    func scrollMetrics() -> ScrollMetrics

    /// Whether the running program has requested mouse tracking (e.g. a TUI like
    /// `claude`, vim, htop, tmux). When true, scroll gestures must be forwarded
    /// as mouse-wheel events (`encodeScrollWheel`) so the program scrolls its own
    /// content, rather than moving the local scrollback viewport.
    var isMouseTrackingActive: Bool { get }

    /// Encode `ticks` mouse-wheel events for a mouse-tracking program at the
    /// given surface pixel position. `ticks` is signed: positive scrolls the
    /// content down (toward newer output / wheel-down), negative scrolls up.
    /// Returns one encoded byte-chunk per tick, to send on channel 0x01.
    func encodeScrollWheel(ticks: Int, surfaceX: Double, surfaceY: Double,
                           screenWidth: UInt32, screenHeight: UInt32,
                           cellWidth: UInt32, cellHeight: UInt32) -> [Data]

    // Selection (touch gestures on iOS).
    func beginSelection(at cell: ViewportCell, surfaceX: Double, surfaceY: Double, timeNs: UInt64)
    func dragSelection(to cell: ViewportCell, surfaceX: Double, surfaceY: Double,
                       columns: UInt32, cellWidth: UInt32, screenHeight: UInt32)
    func endSelection(at cell: ViewportCell)
    func clearSelection()
    func selectedText() -> String?

    /// Whether the terminal has bracketed-paste mode enabled (affects paste framing).
    var isBracketedPasteEnabled: Bool { get }
}

public extension TerminalCoreDriving {
    func resize(cols: UInt16, rows: UInt16) {
        resize(cols: cols, rows: rows, cellWidth: 8, cellHeight: 16)
    }
}
