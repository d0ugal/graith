import CoreGraphics
import GraithSessionKit

/// Pure state machine that turns a drag on the on-screen space key into arrow-key
/// emissions (issue #979). Kept free of UIKit so it can be unit-tested off-device
/// — the UIKit `KeyboardAccessoryView` owns the gesture recognizer plus a repeat
/// timer and feeds translations + timestamps in, exactly the "extract the pure
/// logic and cover that" approach the project's testing guidance calls for.
///
/// Behaviour models a physical arrow key rather than a scroll wheel: holding the
/// space key and dragging in a direction sends **one** arrow immediately. Keep
/// holding in that direction and, after `initialRepeatDelay`, it starts repeating
/// every `repeatInterval` — the same press → pause → repeat cadence as holding a
/// key on a hardware keyboard. Changing direction sends one arrow the new way and
/// restarts the repeat delay. Releasing (via `begin()` on the next drag) resets
/// everything. If any arrow is emitted the drag is "committed" (`didEmit`), so the
/// caller suppresses the space character on release; a plain tap still types a
/// space.
public struct SpaceDragTracker {
    /// Points of travel from the drag origin before a direction registers. Keeps a
    /// tap or tiny jitter from being read as navigation.
    public let activationThreshold: CGFloat

    /// Seconds a direction must be held before the first auto-repeat fires.
    public let initialRepeatDelay: Double

    /// Seconds between auto-repeats once repeating has started.
    public let repeatInterval: Double

    /// How much the off-axis component must beat the currently-held axis before
    /// the direction switches axes (horizontal ⇄ vertical). Keeps a finger held
    /// near a 45° diagonal from thrashing between two arrows on tiny wobbles. A
    /// value of 1 disables the hysteresis (any dominance switches).
    public let directionHysteresis: CGFloat

    /// The direction currently "held" (nil when the finger is within the
    /// activation threshold of the origin, i.e. no key pressed).
    private var heldDirection: TerminalKey?

    /// Timestamp of the most recent emission in the held direction.
    private var lastEmit: Double = 0

    /// True until the first auto-repeat fires, so the initial (longer) delay is
    /// used once and the faster interval thereafter.
    private var awaitingFirstRepeat = true

    /// True once at least one arrow has been emitted this drag, so the caller
    /// knows to suppress the space character on release.
    public private(set) var didEmit = false

    public init(activationThreshold: CGFloat = 22,
                initialRepeatDelay: Double = 0.5,
                repeatInterval: Double = 0.1,
                directionHysteresis: CGFloat = 1.5) {
        self.activationThreshold = max(1, activationThreshold)
        self.initialRepeatDelay = max(0, initialRepeatDelay)
        self.repeatInterval = max(0.001, repeatInterval)
        self.directionHysteresis = max(1, directionHysteresis)
    }

    /// Begin a fresh drag (call on gesture `.began`).
    public mutating func begin() {
        heldDirection = nil
        lastEmit = 0
        awaitingFirstRepeat = true
        didEmit = false
    }

    /// Feed the current translation from the drag's start point plus the current
    /// time (seconds). Call on gesture `.changed` and on each repeat-timer tick.
    /// Returns at most one arrow key: the initial press when a direction is first
    /// entered or changed, or an auto-repeat once the direction has been held past
    /// the delay. Returns none while below the activation threshold or between
    /// repeats.
    public mutating func update(translation: CGPoint, time: Double) -> [TerminalKey] {
        let target = direction(for: translation)

        // Direction changed (including entering a direction from neutral): a fresh
        // press. Emit one arrow now and restart the repeat delay.
        if target != heldDirection {
            heldDirection = target
            guard let key = target else { return [] }
            lastEmit = time
            awaitingFirstRepeat = true
            didEmit = true
            return [key]
        }

        // Same direction still held: auto-repeat once enough time has elapsed.
        guard let key = heldDirection else { return [] }
        let delay = awaitingFirstRepeat ? initialRepeatDelay : repeatInterval
        if time - lastEmit >= delay {
            lastEmit = time
            awaitingFirstRepeat = false
            didEmit = true
            return [key]
        }
        return []
    }

    /// The arrow implied by a translation, or nil if the finger is still within
    /// the activation threshold of the origin. The dominant axis wins; because the
    /// larger component decides direction, whenever either component clears the
    /// threshold the dominant one has too. When a direction is already held, the
    /// off-axis component must beat the held axis by `directionHysteresis` before
    /// the axis flips, so a finger wobbling near the diagonal stays put instead of
    /// thrashing between two arrows.
    private func direction(for translation: CGPoint) -> TerminalKey? {
        let ax = abs(translation.x)
        let ay = abs(translation.y)
        if ax < activationThreshold && ay < activationThreshold { return nil }

        let horizontal: Bool
        switch heldDirection {
        case .arrowLeft, .arrowRight:
            // Held horizontal: flip to vertical only if vertical clearly dominates.
            horizontal = ay <= ax * directionHysteresis
        case .arrowUp, .arrowDown:
            // Held vertical: flip to horizontal only if horizontal clearly dominates.
            horizontal = ax > ay * directionHysteresis
        default:
            // No axis held yet: plain dominant-axis pick, ties go horizontal.
            horizontal = ax >= ay
        }

        if horizontal {
            return translation.x >= 0 ? .arrowRight : .arrowLeft
        } else {
            return translation.y >= 0 ? .arrowDown : .arrowUp
        }
    }
}
