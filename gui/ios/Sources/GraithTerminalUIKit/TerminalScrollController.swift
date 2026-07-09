import Foundation
import CoreGraphics
import GraithClientAPI

/// Pure scroll physics + state for the iOS terminal surface (issue #984). UIKit-
/// free so it unit-tests off-device, following the `SpaceDragTracker` precedent
/// ("extract the pure logic and cover that"). `BaseTerminalUIView` owns the
/// `UIPanGestureRecognizer` and the display link and feeds this controller
/// translations, velocities, and frame ticks; the controller owns the fractional
/// point→row accounting, the momentum decay, and the rubber-band overscroll
/// bounce, and computes the scroll-indicator thumb.
///
/// ## Sign conventions
///
/// - Finger translation / velocity use UIKit coordinates: **+y is downward**.
/// - A viewport **row delta** matches `TerminalCoreDriving.scrollViewport(byRows:)`:
///   **negative scrolls up into history, positive scrolls down toward the live
///   bottom** (libghostty's "up is negative"). Dragging a finger *down* reveals
///   older output, so `rows = -translation / cellHeight`.
/// - `overscroll` is signed raw points past a boundary: **positive = pulled past
///   the live bottom, negative = pulled past the top of history**.
public struct TerminalScrollController {

    // MARK: - Tunables

    /// Point height of one terminal row; keep in sync with the renderer's cell
    /// height as it changes (font / scale). Clamped to at least 1 to divide by.
    public var cellHeight: CGFloat {
        didSet { if cellHeight < 1 { cellHeight = 1 } }
    }

    /// Exponential momentum decay rate (1/s): velocity ×= e^(-friction·dt).
    public let friction: CGFloat
    /// Momentum stops once |velocity| drops below this (points/s).
    public let momentumCutoff: CGFloat
    /// Spring constant pulling overscroll back to zero (points/s² per point).
    public let springStiffness: CGFloat
    /// Spring damping (points/s² per point/s).
    public let springDamping: CGFloat
    /// Rubber-band tightness; UIScrollView uses 0.55.
    public let rubberBandConstant: CGFloat

    // MARK: - State

    public enum Phase: Equatable { case idle, dragging, momentum, springing }
    public private(set) var phase: Phase = .idle

    /// Raw (un-banded) signed overscroll distance in points. Display code should
    /// go through `contentTranslation(viewportHeight:)`, which applies the
    /// rubber-band curve.
    public private(set) var overscroll: CGFloat = 0

    private var rowRemainder: CGFloat = 0
    /// Momentum velocity in viewport space (points/s, + = toward the live bottom).
    private var velocity: CGFloat = 0
    private var springVelocity: CGFloat = 0

    public init(cellHeight: CGFloat = 16,
                friction: CGFloat = 4.5,
                momentumCutoff: CGFloat = 24,
                springStiffness: CGFloat = 220,
                springDamping: CGFloat = 26,
                rubberBandConstant: CGFloat = 0.55) {
        self.cellHeight = max(1, cellHeight)
        self.friction = friction
        self.momentumCutoff = momentumCutoff
        self.springStiffness = springStiffness
        self.springDamping = springDamping
        self.rubberBandConstant = rubberBandConstant
    }

    /// True while a momentum or spring animation is running and the caller should
    /// keep ticking the display link.
    public var isSettling: Bool { phase == .momentum || phase == .springing }

    // MARK: - Drag

    /// Start a fresh drag (gesture `.began`). Cancels any in-flight momentum but
    /// keeps existing overscroll so a bounce-in-progress can be grabbed.
    public mutating func beginDrag() {
        phase = .dragging
        velocity = 0
        springVelocity = 0
        rowRemainder = 0
    }

    /// Feed an incremental finger translation (points; +y downward). Any active
    /// overscroll is unwound first so a reverse drag "grabs" the bounce, then the
    /// remaining travel is converted to an integer viewport row delta. Rows the
    /// core can't absorb (a scrollback boundary) must be reported back via
    /// `absorbOverscroll(rows:)`.
    public mutating func drag(translationDelta dy: CGFloat) -> Int {
        // Dragging the finger down (dy > 0) scrolls up into history (negative).
        var p = -dy
        // Unwind existing overscroll when the drag opposes it.
        if overscroll != 0, p != 0, (overscroll > 0) != (p > 0) {
            let consumable = min(abs(p), abs(overscroll))
            let signed = consumable * (p > 0 ? 1 : -1)
            overscroll += signed   // toward zero
            p -= signed
        }
        rowRemainder += p / cellHeight
        let whole = Int(rowRemainder)   // truncates toward zero
        rowRemainder -= CGFloat(whole)
        return whole
    }

    /// Report rows the core refused at a boundary, turning them into raw
    /// overscroll (positive = past the bottom, negative = past the top).
    public mutating func absorbOverscroll(rows: Int) {
        guard rows != 0 else { return }
        overscroll += CGFloat(rows) * cellHeight
    }

    /// End a drag (gesture `.ended` / `.cancelled`). A pulled-out overscroll
    /// springs back; otherwise a fast flick starts momentum. `velocityY` is the
    /// gesture's finger velocity (points/s, +y downward).
    public mutating func endDrag(velocityY: CGFloat) {
        if overscroll != 0 {
            phase = .springing
            springVelocity = -velocityY   // carry the fling into the bounce
        } else if abs(velocityY) > momentumCutoff {
            phase = .momentum
            velocity = -velocityY
            rowRemainder = 0
        } else {
            phase = .idle
        }
    }

    // MARK: - Physics loop

    /// Advance momentum / spring by `dt` seconds and return the integer viewport
    /// row delta to apply this frame (0 while springing or idle). During momentum
    /// the caller applies the rows and, if a boundary is hit, reports the refused
    /// rows via `absorbOverscroll(rows:)`; the next tick converts the fling into a
    /// spring bounce. Read `contentTranslation(viewportHeight:)` each tick for the
    /// visual offset, and stop ticking once `isSettling` is false.
    public mutating func tick(dt: CGFloat) -> Int {
        guard dt > 0 else { return 0 }
        switch phase {
        case .momentum:
            if overscroll != 0 {
                // Hit a boundary last frame — hand the remaining speed to a spring.
                springVelocity = velocity
                velocity = 0
                phase = .springing
                return 0
            }
            velocity *= CGFloat(exp(-Double(friction) * Double(dt)))
            if abs(velocity) < momentumCutoff {
                velocity = 0
                phase = .idle
                return 0
            }
            rowRemainder += (velocity * dt) / cellHeight
            let whole = Int(rowRemainder)
            rowRemainder -= CGFloat(whole)
            return whole
        case .springing:
            let accel = -springStiffness * overscroll - springDamping * springVelocity
            springVelocity += accel * dt
            overscroll += springVelocity * dt
            if abs(overscroll) < 0.5, abs(springVelocity) < 8 {
                overscroll = 0
                springVelocity = 0
                phase = .idle
            }
            return 0
        case .idle, .dragging:
            return 0
        }
    }

    /// Cancel all motion and overscroll (e.g. on a jump-to-bottom tap or when a
    /// new gesture pre-empts the animation).
    public mutating func stop() {
        phase = .idle
        velocity = 0
        springVelocity = 0
        overscroll = 0
        rowRemainder = 0
    }

    // MARK: - Visual

    /// The point translation to apply to the terminal content layer for the
    /// current overscroll, with the rubber-band curve applied so the pull
    /// saturates. Positive moves content down (past-top pull); negative moves it
    /// up (past-bottom pull). `viewportHeight` is the visible height in points.
    public func contentTranslation(viewportHeight: CGFloat) -> CGFloat {
        guard overscroll != 0, viewportHeight > 0 else { return 0 }
        let x = abs(overscroll)
        // Apple's rubber-band: b(x) = (x·d·c) / (d + c·x).
        let banded = (x * viewportHeight * rubberBandConstant) / (viewportHeight + rubberBandConstant * x)
        return overscroll < 0 ? banded : -banded
    }

    // MARK: - Scroll indicator

    /// Geometry for a vertical scroll-indicator thumb inside a track of
    /// `trackLength` points, or `nil` when there is no history to scroll. Returns
    /// the thumb's `offset` from the top of the track and its `length`, both in
    /// points, clamped so a tiny viewport still shows a grabbable thumb.
    public static func thumb(metrics: ScrollMetrics,
                             trackLength: CGFloat,
                             minThumb: CGFloat = 36) -> (offset: CGFloat, length: CGFloat)? {
        guard metrics.hasHistory, trackLength > 0, metrics.total > 0 else { return nil }
        let visibleFrac = CGFloat(metrics.len) / CGFloat(metrics.total)
        let length = max(min(minThumb, trackLength), min(trackLength, trackLength * visibleFrac))
        let maxOffset = CGFloat(max(0, metrics.total - metrics.len))
        let posFrac = maxOffset > 0 ? min(1, max(0, CGFloat(metrics.offset) / maxOffset)) : 0
        let offset = (trackLength - length) * posFrac
        return (offset, length)
    }
}
