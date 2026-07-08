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
