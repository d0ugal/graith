import CoreGraphics
import Foundation

/// User-tunable physics for the iOS terminal's touch gestures (issue #1255).
///
/// The interactive terminal surface hard-coded its drag thresholds, momentum /
/// spring physics, auto-repeat timing, and gesture delays across
/// `TerminalScrollController`, `SpaceDragTracker`, and `BaseTerminalUIView`, so
/// they could only be changed by rebuilding the app. This value type gathers the
/// **tunable** knobs into one place with a single set of defaults and lets them
/// be overridden at runtime via `UserDefaults` (see `init(userDefaults:)`), so a
/// user — or a future settings screen — can retune the feel without a rebuild.
///
/// ## Tunable vs. invariant
///
/// Only values that change *feel* (sensitivity, acceleration, deceleration, and
/// timing) live here. Values that are **physical invariants** — correct for the
/// platform rather than a matter of taste — stay as documented constants in the
/// components that own them, because changing them would break correctness or the
/// iOS convention rather than merely retune it:
///
/// - **Rubber-band constant `0.55`** (`TerminalScrollController.rubberBandConstant`)
///   — matches `UIScrollView`'s overscroll curve; a different value would feel
///   foreign to the platform.
/// - **Spring settle epsilons `0.5` pt / `8` pt·s⁻¹** (`TerminalScrollController.tick`)
///   — the "close enough to rest" threshold that ends the bounce; a convergence
///   detail, not a feel knob.
/// - **Frame-time clamp `0.05` s** (`BaseTerminalUIView.tick`) — caps `dt` so a
///   dropped frame can't fling the content; a stability guard.
/// - **Minimum thumb length `36` pt** (`TerminalScrollController.thumb`) — the
///   smallest grabbable scroll indicator (a touch-target floor).
/// - **Two-finger scroll (`min`/`maxNumberOfTouches = 2`)** and the immediate
///   (`minimumPressDuration = 0`) space-key recognizer — the gesture *shapes*
///   the physics animate; retuning them would change what the gestures *are*.
/// - **Display-link rate `60` fps** — hardware frame cadence, not a preference.
public struct TerminalGestureConfig: Equatable, Sendable {

    private enum Shipped {
        static let scrollFriction: CGFloat = 4.5
        static let scrollMomentumCutoff: CGFloat = 24
        static let scrollSpringStiffness: CGFloat = 220
        static let scrollSpringDamping: CGFloat = 26
        static let spaceActivationThreshold: CGFloat = 22
        static let spaceInitialRepeatDelay: Double = 0.5
        static let spaceRepeatInterval: Double = 0.1
        static let spaceDirectionHysteresis: CGFloat = 1.5
        static let selectionLongPressDuration: Double = 0.3
    }

    // The scroll ranges are deliberately paired with the controller's 50 ms
    // maximum integration step. In particular, the spring maxima satisfy the
    // semi-implicit Euler stability bound at that step, while the positive
    // minima guarantee momentum and overscroll keep progressing toward idle.
    private enum Range {
        static let scrollFriction: ClosedRange<CGFloat> = 1...60
        static let scrollMomentumCutoff: ClosedRange<CGFloat> = 1...10_000
        static let scrollSpringStiffness: ClosedRange<CGFloat> = 30...400
        static let scrollSpringDamping: ClosedRange<CGFloat> = 4...29
        static let spaceActivationThreshold: ClosedRange<CGFloat> = 1...10_000
        static let spaceInitialRepeatDelay: ClosedRange<Double> = 0...60
        static let spaceRepeatInterval: ClosedRange<Double> = 0.001...60
        static let spaceDirectionHysteresis: ClosedRange<CGFloat> = 1...100
        static let selectionLongPressDuration: ClosedRange<Double> = 0...60
    }

    // MARK: - Scrollback physics (TerminalScrollController)

    /// Exponential momentum decay rate (1/s): higher stops a flick sooner.
    public var scrollFriction: CGFloat
    /// Momentum halts once |velocity| drops below this (points/s).
    public var scrollMomentumCutoff: CGFloat
    /// Overscroll spring constant (points/s² per point): higher snaps back harder.
    public var scrollSpringStiffness: CGFloat
    /// Overscroll spring damping (points/s² per point/s): higher settles flatter.
    public var scrollSpringDamping: CGFloat

    // MARK: - Space-key drag → arrow keys (SpaceDragTracker)

    /// Points of travel before a space-key drag registers as a direction.
    public var spaceActivationThreshold: CGFloat
    /// Seconds a direction is held before the first auto-repeat fires.
    public var spaceInitialRepeatDelay: Double
    /// Seconds between auto-repeats once repeating has started.
    public var spaceRepeatInterval: Double
    /// How far the off-axis component must beat the held axis before the arrow
    /// direction flips (≥1; 1 disables the hysteresis).
    public var spaceDirectionHysteresis: CGFloat

    // MARK: - Selection (BaseTerminalUIView)

    /// Seconds a single finger must be held before text selection begins.
    public var selectionLongPressDuration: Double

    /// The built-in defaults — the values the app previously hard-coded. These
    /// are the feel the terminal ships with when nothing overrides them.
    public static let `default` = TerminalGestureConfig(
        scrollFriction: Shipped.scrollFriction,
        scrollMomentumCutoff: Shipped.scrollMomentumCutoff,
        scrollSpringStiffness: Shipped.scrollSpringStiffness,
        scrollSpringDamping: Shipped.scrollSpringDamping,
        spaceActivationThreshold: Shipped.spaceActivationThreshold,
        spaceInitialRepeatDelay: Shipped.spaceInitialRepeatDelay,
        spaceRepeatInterval: Shipped.spaceRepeatInterval,
        spaceDirectionHysteresis: Shipped.spaceDirectionHysteresis,
        selectionLongPressDuration: Shipped.selectionLongPressDuration
    )

    /// Memberwise init with finite, stability-safe clamping so an out-of-range
    /// value (from user defaults or a caller) can't wedge the physics. A
    /// non-finite value falls back to the shipped value instead of leaking NaN
    /// through comparisons. Bounds mirror the per-component clamps so
    /// `TerminalGestureConfig` is self-consistent regardless of source.
    public init(scrollFriction: CGFloat = TerminalGestureConfig.default.scrollFriction,
                scrollMomentumCutoff: CGFloat = TerminalGestureConfig.default.scrollMomentumCutoff,
                scrollSpringStiffness: CGFloat = TerminalGestureConfig.default.scrollSpringStiffness,
                scrollSpringDamping: CGFloat = TerminalGestureConfig.default.scrollSpringDamping,
                spaceActivationThreshold: CGFloat = TerminalGestureConfig.default.spaceActivationThreshold,
                spaceInitialRepeatDelay: Double = TerminalGestureConfig.default.spaceInitialRepeatDelay,
                spaceRepeatInterval: Double = TerminalGestureConfig.default.spaceRepeatInterval,
                spaceDirectionHysteresis: CGFloat = TerminalGestureConfig.default.spaceDirectionHysteresis,
                selectionLongPressDuration: Double = TerminalGestureConfig.default.selectionLongPressDuration) {
        self.scrollFriction = Self.normalize(scrollFriction, to: Range.scrollFriction,
                                             fallback: Shipped.scrollFriction)
        self.scrollMomentumCutoff = Self.normalize(scrollMomentumCutoff, to: Range.scrollMomentumCutoff,
                                                   fallback: Shipped.scrollMomentumCutoff)
        self.scrollSpringStiffness = Self.normalize(scrollSpringStiffness, to: Range.scrollSpringStiffness,
                                                    fallback: Shipped.scrollSpringStiffness)
        self.scrollSpringDamping = Self.normalize(scrollSpringDamping, to: Range.scrollSpringDamping,
                                                  fallback: Shipped.scrollSpringDamping)
        self.spaceActivationThreshold = Self.normalize(spaceActivationThreshold, to: Range.spaceActivationThreshold,
                                                       fallback: Shipped.spaceActivationThreshold)
        self.spaceInitialRepeatDelay = Self.normalize(spaceInitialRepeatDelay, to: Range.spaceInitialRepeatDelay,
                                                      fallback: Shipped.spaceInitialRepeatDelay)
        self.spaceRepeatInterval = Self.normalize(spaceRepeatInterval, to: Range.spaceRepeatInterval,
                                                  fallback: Shipped.spaceRepeatInterval)
        self.spaceDirectionHysteresis = Self.normalize(spaceDirectionHysteresis, to: Range.spaceDirectionHysteresis,
                                                       fallback: Shipped.spaceDirectionHysteresis)
        self.selectionLongPressDuration = Self.normalize(selectionLongPressDuration, to: Range.selectionLongPressDuration,
                                                         fallback: Shipped.selectionLongPressDuration)
    }

    private static func normalize<T: BinaryFloatingPoint>(_ value: T,
                                                           to range: ClosedRange<T>,
                                                           fallback: T) -> T {
        guard value.isFinite else { return fallback }
        return min(range.upperBound, max(range.lowerBound, value))
    }

    // MARK: - UserDefaults

    /// The `UserDefaults` keys each tunable is read from. Namespaced under
    /// `graith.gesture.` so a settings screen (or `defaults write`) can override
    /// them without colliding with other app state.
    public enum Key {
        public static let scrollFriction = "graith.gesture.scrollFriction"
        public static let scrollMomentumCutoff = "graith.gesture.scrollMomentumCutoff"
        public static let scrollSpringStiffness = "graith.gesture.scrollSpringStiffness"
        public static let scrollSpringDamping = "graith.gesture.scrollSpringDamping"
        public static let spaceActivationThreshold = "graith.gesture.spaceActivationThreshold"
        public static let spaceInitialRepeatDelay = "graith.gesture.spaceInitialRepeatDelay"
        public static let spaceRepeatInterval = "graith.gesture.spaceRepeatInterval"
        public static let spaceDirectionHysteresis = "graith.gesture.spaceDirectionHysteresis"
        public static let selectionLongPressDuration = "graith.gesture.selectionLongPressDuration"
    }

    /// Load the config from `UserDefaults`, falling back to `.default` for any key
    /// that is absent. Present-but-out-of-range values are clamped by the
    /// memberwise init. A key is treated as set only when actually present, so
    /// `.default` is used rather than reading a spurious `0` for a missing key.
    public init(userDefaults defaults: UserDefaults) {
        let base = TerminalGestureConfig.default
        func value(_ key: String, _ fallback: CGFloat) -> CGFloat {
            defaults.object(forKey: key) == nil ? fallback : CGFloat(defaults.double(forKey: key))
        }
        func time(_ key: String, _ fallback: Double) -> Double {
            defaults.object(forKey: key) == nil ? fallback : defaults.double(forKey: key)
        }
        self.init(
            scrollFriction: value(Key.scrollFriction, base.scrollFriction),
            scrollMomentumCutoff: value(Key.scrollMomentumCutoff, base.scrollMomentumCutoff),
            scrollSpringStiffness: value(Key.scrollSpringStiffness, base.scrollSpringStiffness),
            scrollSpringDamping: value(Key.scrollSpringDamping, base.scrollSpringDamping),
            spaceActivationThreshold: value(Key.spaceActivationThreshold, base.spaceActivationThreshold),
            spaceInitialRepeatDelay: time(Key.spaceInitialRepeatDelay, base.spaceInitialRepeatDelay),
            spaceRepeatInterval: time(Key.spaceRepeatInterval, base.spaceRepeatInterval),
            spaceDirectionHysteresis: value(Key.spaceDirectionHysteresis, base.spaceDirectionHysteresis),
            selectionLongPressDuration: time(Key.selectionLongPressDuration, base.selectionLongPressDuration)
        )
    }
}
