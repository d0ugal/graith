#if canImport(UIKit)
import UIKit
import GraithSessionKit

/// A `UITextPosition` backed by an integer offset into the composition buffer.
final class IndexedPosition: UITextPosition {
    let index: Int
    init(index: Int) { self.index = index }
}

/// A `UITextRange` over two `IndexedPosition`s.
final class IndexedRange: UITextRange {
    let range: Range<Int>
    init(range: Range<Int>) { self.range = range }
    override var start: UITextPosition { IndexedPosition(index: range.lowerBound) }
    override var end: UITextPosition { IndexedPosition(index: range.upperBound) }
    override var isEmpty: Bool { range.isEmpty }
}

// MARK: - UITextInput (IME marked-text)

// A terminal has no persistent editable document — text is sent to the remote
// PTY and echoed back. We expose only the IME **composition buffer**
// (`markedText`) as the editable document so multi-stage input (Japanese,
// Chinese, dead keys, emoji) composes correctly, and commit it to the terminal
// when the IME unmarks/commits (design §C.3: IME marked text is required).
extension BaseTerminalUIView: UITextInput {

    // The composition buffer is the whole visible document.
    private var buffer: String {
        get { markedText }
        set { markedText = newValue }
    }

    public func text(in range: UITextRange) -> String? {
        guard let r = range as? IndexedRange else { return nil }
        let clamped = clamp(r.range)
        let chars = Array(buffer)
        return String(chars[clamped])
    }

    public func replace(_ range: UITextRange, withText text: String) {
        guard let r = range as? IndexedRange else { return }
        var chars = Array(buffer)
        let clamped = clamp(r.range)
        chars.replaceSubrange(clamped, with: Array(text))
        buffer = String(chars)
    }

    public var selectedTextRange: UITextRange? {
        get { IndexedRange(range: buffer.count..<buffer.count) }
        set { /* single caret at end of composition; nothing to store */ }
    }

    // Satisfies UITextInput's markedTextRange requirement (UITextRange?),
    // bridging the internal IndexedRange? backing store.
    public var markedTextRange: UITextRange? { markedRange }

    public var markedTextStyle: [NSAttributedString.Key: Any]? {
        get { nil }
        set {}
    }

    /// The IME is composing: stash the composing text (shown inline by the OS)
    /// but do NOT send to the terminal until it commits.
    public func setMarkedText(_ markedText: String?, selectedRange: NSRange) {
        inputDelegate?.textWillChange(self)
        self.markedText = markedText ?? ""
        self.markedRange = IndexedRange(range: 0..<self.markedText.count)
        inputDelegate?.textDidChange(self)
    }

    /// The IME committed: send the composed text to the terminal and clear.
    public func unmarkText() {
        let committed = markedText
        markedText = ""
        markedRange = nil
        if !committed.isEmpty {
            insertText(committed)
        }
    }

    public var beginningOfDocument: UITextPosition { IndexedPosition(index: 0) }
    public var endOfDocument: UITextPosition { IndexedPosition(index: buffer.count) }

    public func textRange(from fromPosition: UITextPosition, to toPosition: UITextPosition) -> UITextRange? {
        guard let f = fromPosition as? IndexedPosition, let t = toPosition as? IndexedPosition else { return nil }
        return IndexedRange(range: min(f.index, t.index)..<max(f.index, t.index))
    }

    public func position(from position: UITextPosition, offset: Int) -> UITextPosition? {
        guard let p = position as? IndexedPosition else { return nil }
        let idx = p.index + offset
        guard idx >= 0, idx <= buffer.count else { return nil }
        return IndexedPosition(index: idx)
    }

    public func position(from position: UITextPosition, in direction: UITextLayoutDirection, offset: Int) -> UITextPosition? {
        // Horizontal movement only in the composition buffer.
        let delta = (direction == .left || direction == .up) ? -offset : offset
        return self.position(from: position, offset: delta)
    }

    public func compare(_ position: UITextPosition, to other: UITextPosition) -> ComparisonResult {
        guard let a = position as? IndexedPosition, let b = other as? IndexedPosition else { return .orderedSame }
        if a.index < b.index { return .orderedAscending }
        if a.index > b.index { return .orderedDescending }
        return .orderedSame
    }

    public func offset(from: UITextPosition, to toPosition: UITextPosition) -> Int {
        guard let a = from as? IndexedPosition, let b = toPosition as? IndexedPosition else { return 0 }
        return b.index - a.index
    }

    public func position(within range: UITextRange, farthestIn direction: UITextLayoutDirection) -> UITextPosition? {
        guard let r = range as? IndexedRange else { return nil }
        return (direction == .left || direction == .up) ? IndexedPosition(index: r.range.lowerBound)
                                                        : IndexedPosition(index: r.range.upperBound)
    }

    public func characterRange(byExtending position: UITextPosition, in direction: UITextLayoutDirection) -> UITextRange? {
        guard let p = position as? IndexedPosition else { return nil }
        return IndexedRange(range: 0..<p.index)
    }

    // MARK: Writing direction

    public func baseWritingDirection(for position: UITextPosition, in direction: UITextStorageDirection) -> NSWritingDirection {
        .leftToRight
    }
    public func setBaseWritingDirection(_ writingDirection: NSWritingDirection, for range: UITextRange) {}

    // MARK: Geometry (the terminal draws its own cursor; provide sane stubs)

    public func firstRect(for range: UITextRange) -> CGRect { .zero }
    public func caretRect(for position: UITextPosition) -> CGRect { .zero }
    public func selectionRects(for range: UITextRange) -> [UITextSelectionRect] { [] }
    public func closestPosition(to point: CGPoint) -> UITextPosition? { endOfDocument }
    public func closestPosition(to point: CGPoint, within range: UITextRange) -> UITextPosition? { endOfDocument }
    public func characterRange(at point: CGPoint) -> UITextRange? { nil }

    // MARK: Helpers

    private func clamp(_ range: Range<Int>) -> Range<Int> {
        let lower = max(0, min(range.lowerBound, buffer.count))
        let upper = max(lower, min(range.upperBound, buffer.count))
        return lower..<upper
    }
}
#endif
