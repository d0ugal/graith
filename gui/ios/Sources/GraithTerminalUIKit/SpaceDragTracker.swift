import CoreGraphics
import GraithClientAPI

/// Pure state machine that turns a drag on the on-screen space key into a run of
/// arrow-key emissions (issue #979). Kept free of UIKit so it can be unit-tested
/// off-device — the UIKit `KeyboardAccessoryView` owns the gesture recognizer and
/// feeds translations in, exactly the "extract the pure logic and cover that"
/// approach the project's testing guidance calls for.
///
/// Behaviour: holding the space key and dragging moves a cursor through the
/// terminal. The dominant drag axis selects the direction (up/down/left/right),
/// and every `threshold` points of travel emits one more arrow key — so a short
/// flick sends a single arrow and a long continuous drag sends a run of them.
/// If any arrow is emitted the drag is "committed": the space character is
/// suppressed on release (`didEmit`), so a plain tap still types a space.
public struct SpaceDragTracker {
    /// Points of travel per emitted arrow key.
    public let threshold: CGFloat

    /// The reference point the next emission is measured from, in the gesture's
    /// translation space (translation is relative to the drag's start). Advances
    /// by `threshold` along the emitted axis on each emission.
    private var anchor: CGPoint = .zero

    /// True once at least one arrow has been emitted this drag, so the caller
    /// knows to suppress the space character on release.
    public private(set) var didEmit = false

    public init(threshold: CGFloat = 22) {
        self.threshold = max(1, threshold)
    }

    /// Begin a fresh drag (call on gesture `.began`).
    public mutating func begin() {
        anchor = .zero
        didEmit = false
    }

    /// Feed the current translation from the drag's start point (gesture
    /// `.changed`). Returns the arrow keys crossed since the last call — none,
    /// one, or several for a fast drag that spanned multiple thresholds.
    public mutating func update(translation: CGPoint) -> [TerminalKey] {
        var keys: [TerminalKey] = []
        // Emit one arrow per `threshold` of travel. Re-evaluate the dominant axis
        // each step so a curving drag follows direction changes; every emission
        // shrinks the remaining offset by `threshold`, so this always terminates.
        while true {
            let dx = translation.x - anchor.x
            let dy = translation.y - anchor.y
            if abs(dx) >= abs(dy) {
                if dx >= threshold {
                    keys.append(.arrowRight)
                    anchor.x += threshold
                } else if dx <= -threshold {
                    keys.append(.arrowLeft)
                    anchor.x -= threshold
                } else {
                    break
                }
            } else {
                if dy >= threshold {
                    keys.append(.arrowDown)
                    anchor.y += threshold
                } else if dy <= -threshold {
                    keys.append(.arrowUp)
                    anchor.y -= threshold
                } else {
                    break
                }
            }
        }
        if !keys.isEmpty { didEmit = true }
        return keys
    }
}
