import Foundation
import GraithClientAPI

/// A mock `TerminalCoreDriving` for exercising `BaseTerminalUIView` without
/// `libghostty-vt`. It records fed output and encodes strokes to a simple,
/// deterministic byte mapping (NOT the real ghostty encoder — good enough to
/// assert the input plumbing calls `encode` with the right strokes).
public final class MockTerminalCore: TerminalCoreDriving, @unchecked Sendable {
    public private(set) var cols: UInt16
    public private(set) var rows: UInt16
    public private(set) var fedOutput = Data()
    public private(set) var encodedStrokes: [TerminalKeyStroke] = []
    public private(set) var lastResize: (cols: UInt16, rows: UInt16, cw: UInt32, ch: UInt32)?
    public private(set) var scrollDeltas: [Int] = []
    public var isViewportAtBottom = true
    public var isBracketedPasteEnabled = false
    private var selection: String?

    public init(cols: UInt16 = 80, rows: UInt16 = 24) {
        self.cols = cols
        self.rows = rows
    }

    public func feedOutput(_ data: Data) { fedOutput.append(data) }

    public func encode(_ stroke: TerminalKeyStroke) -> Data? {
        encodedStrokes.append(stroke)
        switch stroke.key {
        case .character(let s):
            if stroke.modifiers.contains(.control), let first = s.lowercased().unicodeScalars.first {
                // Ctrl-letter → C0 control byte (deterministic test mapping).
                let code = Int(first.value) - Int(UnicodeScalar("a").value) + 1
                if code >= 1, code <= 26 { return Data([UInt8(code)]) }
            }
            return Data(s.utf8)
        case .enter: return Data([0x0D])
        case .tab: return Data([0x09])
        case .backspace: return Data([0x7F])
        case .escape: return Data([0x1B])
        case .delete: return Data("\u{1B}[3~".utf8)
        case .arrowUp: return Data("\u{1B}[A".utf8)
        case .arrowDown: return Data("\u{1B}[B".utf8)
        case .arrowRight: return Data("\u{1B}[C".utf8)
        case .arrowLeft: return Data("\u{1B}[D".utf8)
        case .home: return Data("\u{1B}[H".utf8)
        case .end: return Data("\u{1B}[F".utf8)
        case .pageUp: return Data("\u{1B}[5~".utf8)
        case .pageDown: return Data("\u{1B}[6~".utf8)
        case .insert: return Data("\u{1B}[2~".utf8)
        case .function(let n): return Data("\u{1B}[\(n)F".utf8)
        }
    }

    public func resize(cols: UInt16, rows: UInt16, cellWidth: UInt32, cellHeight: UInt32) {
        self.cols = cols
        self.rows = rows
        lastResize = (cols, rows, cellWidth, cellHeight)
    }

    public func scrollViewport(byRows delta: Int) {
        scrollDeltas.append(delta)
        if delta > 0 { isViewportAtBottom = false }
    }

    public func scrollToBottom() { isViewportAtBottom = true }

    public func beginSelection(at cell: ViewportCell, surfaceX: Double, surfaceY: Double, timeNs: UInt64) {
        selection = "sel@\(cell.col),\(cell.row)"
    }

    public func dragSelection(to cell: ViewportCell, surfaceX: Double, surfaceY: Double,
                              columns: UInt32, cellWidth: UInt32, screenHeight: UInt32) {
        selection = "sel..\(cell.col),\(cell.row)"
    }

    public func endSelection(at cell: ViewportCell) {}
    public func clearSelection() { selection = nil }
    public func selectedText() -> String? { selection }
}
