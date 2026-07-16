import CoreGraphics
import XCTest
import GraithSessionKit
@testable import GraithTerminalUIKit

/// Covers the pure scroll physics/state behind the iOS two-finger scrollback
/// gesture (issue #984): point→row accounting, momentum, rubber-band bounce, and
/// the indicator thumb — extracted so it tests without a real gesture recognizer.
final class TerminalScrollControllerTests: XCTestCase {

    private func makeController() -> TerminalScrollController {
        TerminalScrollController(cellHeight: 16)
    }

    // MARK: - Drag → rows

    func testDragDownScrollsUpIntoHistory() {
        var c = makeController()
        c.beginDrag()
        // Finger down (+y) reveals older output ⇒ negative viewport rows.
        XCTAssertEqual(c.drag(translationDelta: 32), -2, "32pt / 16pt cell ⇒ 2 rows up")
    }

    func testDragUpScrollsDownTowardLive() {
        var c = makeController()
        c.beginDrag()
        XCTAssertEqual(c.drag(translationDelta: -48), 3, "48pt up ⇒ 3 rows toward the live bottom")
    }

    func testFractionalTravelAccumulatesAcrossCalls() {
        var c = makeController()
        c.beginDrag()
        XCTAssertEqual(c.drag(translationDelta: -8), 0, "half a cell emits no row yet")
        XCTAssertEqual(c.drag(translationDelta: -8), 1, "the second half completes one row")
    }

    func testBeginDragClearsRemainder() {
        var c = makeController()
        c.beginDrag()
        _ = c.drag(translationDelta: -8)   // 0.5 row banked
        c.beginDrag()
        XCTAssertEqual(c.drag(translationDelta: -8), 0, "begin() resets the fractional remainder")
    }

    // MARK: - Overscroll + rubber-band

    func testAbsorbOverscrollAccumulatesRawPoints() {
        var c = makeController()
        c.absorbOverscroll(rows: 3)   // refused at the bottom
        XCTAssertEqual(c.overscroll, 48, accuracy: 0.001, "3 rows × 16pt of past-bottom overscroll")
    }

    func testReverseDragUnwindsOverscrollBeforeScrolling() {
        var c = makeController()
        c.beginDrag()
        c.absorbOverscroll(rows: 6)    // +96pt past the bottom
        // Dragging the finger down opposes a past-bottom pull: it should unwind
        // the overscroll first and scroll the core zero rows.
        let rows = c.drag(translationDelta: 50)
        XCTAssertEqual(rows, 0, "the whole drag is spent unwinding the bounce")
        XCTAssertEqual(c.overscroll, 46, accuracy: 0.001, "96 − 50 of overscroll remains")
    }

    func testContentTranslationIsSignedAndSaturates() {
        var c = makeController()
        let vh: CGFloat = 800
        XCTAssertEqual(c.contentTranslation(viewportHeight: vh), 0, "no overscroll ⇒ no translation")

        c.absorbOverscroll(rows: -10)  // past the top (negative)
        let up = c.contentTranslation(viewportHeight: vh)
        XCTAssertGreaterThan(up, 0, "past-top pull moves content down (+)")
        XCTAssertLessThan(up, 160, "rubber-band damps below the raw 160pt pull")

        var d = makeController()
        d.absorbOverscroll(rows: 10)   // past the bottom (positive)
        XCTAssertLessThan(d.contentTranslation(viewportHeight: vh), 0, "past-bottom pull moves content up (−)")
    }

    func testRubberBandNeverExceedsViewport() {
        var c = makeController()
        c.absorbOverscroll(rows: 10_000)   // absurd pull
        let t = abs(c.contentTranslation(viewportHeight: 800))
        XCTAssertLessThan(t, 800, "the banded translation stays below the viewport height")
    }

    // MARK: - Momentum

    func testFlingStartsMomentumThatDecaysToIdle() {
        var c = makeController()
        c.beginDrag()
        c.endDrag(velocityY: -1200)   // fling up ⇒ scroll toward the live bottom
        XCTAssertEqual(c.phase, .momentum)

        var total = 0
        for _ in 0..<600 where c.isSettling {   // up to ~10s of frames
            total += c.tick(dt: 1.0 / 60.0)
        }
        XCTAssertEqual(c.phase, .idle, "momentum settles")
        XCTAssertGreaterThan(total, 0, "a fling toward the bottom moves positive rows")
    }

    func testSlowReleaseDoesNotStartMomentum() {
        var c = makeController()
        c.beginDrag()
        c.endDrag(velocityY: 5)   // below cutoff
        XCTAssertEqual(c.phase, .idle)
        XCTAssertFalse(c.isSettling)
    }

    func testMomentumHittingBoundaryConvertsToSpring() {
        var c = makeController()
        c.beginDrag()
        c.endDrag(velocityY: -1200)
        XCTAssertEqual(c.phase, .momentum)
        _ = c.tick(dt: 1.0 / 60.0)
        // Simulate the view reporting that the core refused the rows (boundary).
        c.absorbOverscroll(rows: 4)
        _ = c.tick(dt: 1.0 / 60.0)
        XCTAssertEqual(c.phase, .springing, "a boundary during momentum becomes a bounce")
    }

    // MARK: - Spring

    func testSpringPullsOverscrollBackToZero() {
        var c = makeController()
        c.beginDrag()
        c.absorbOverscroll(rows: 6)   // +96pt
        c.endDrag(velocityY: 0)
        XCTAssertEqual(c.phase, .springing)

        for _ in 0..<600 where c.isSettling {
            _ = c.tick(dt: 1.0 / 60.0)
        }
        XCTAssertEqual(c.phase, .idle, "the spring settles")
        XCTAssertEqual(c.overscroll, 0, accuracy: 0.001, "overscroll returns to zero")
    }

    func testStopResetsEverything() {
        var c = makeController()
        c.beginDrag()
        c.absorbOverscroll(rows: 6)
        c.endDrag(velocityY: -1000)
        c.stop()
        XCTAssertEqual(c.phase, .idle)
        XCTAssertEqual(c.overscroll, 0)
        XCTAssertFalse(c.isSettling)
    }

    func testCellHeightClampedToAtLeastOne() {
        var c = TerminalScrollController(cellHeight: 0)
        c.beginDrag()
        // A zero cell height would divide-by-zero; the initializer clamps to 1.
        XCTAssertEqual(c.drag(translationDelta: -3), 3, "3pt / 1pt cell ⇒ 3 rows")
    }

    func testDirectInitNormalizesInvalidPhysics() {
        let c = TerminalScrollController(
            friction: 0,
            momentumCutoff: -.infinity,
            springStiffness: .nan,
            springDamping: -1)
        XCTAssertEqual(c.friction, 1)
        XCTAssertEqual(c.momentumCutoff, TerminalGestureConfig.default.scrollMomentumCutoff)
        XCTAssertEqual(c.springStiffness, TerminalGestureConfig.default.scrollSpringStiffness)
        XCTAssertEqual(c.springDamping, 4)
    }

    func testInvalidPhysicsOverridesConvergeWithinBound() {
        let invalidValues: [(String, CGFloat)] = [
            ("zero", 0),
            ("negative", -10),
            ("nan", .nan),
            ("positive infinity", .infinity),
            ("negative infinity", -.infinity),
        ]

        for (name, value) in invalidValues {
            let config = TerminalGestureConfig(
                scrollFriction: value,
                scrollMomentumCutoff: value,
                scrollSpringStiffness: value,
                scrollSpringDamping: value)

            var momentum = TerminalScrollController(config: config)
            momentum.beginDrag()
            momentum.endDrag(velocityY: -1200)
            for _ in 0..<600 where momentum.isSettling {
                _ = momentum.tick(dt: 1.0 / 60.0)
            }
            XCTAssertFalse(momentum.isSettling, "\(name) physics left momentum settling")
            XCTAssertEqual(momentum.phase, .idle, "\(name) physics did not reach idle from momentum")

            var spring = TerminalScrollController(config: config)
            spring.beginDrag()
            spring.absorbOverscroll(rows: 6)
            spring.endDrag(velocityY: 0)
            for _ in 0..<600 where spring.isSettling {
                _ = spring.tick(dt: 1.0 / 60.0)
            }
            XCTAssertFalse(spring.isSettling, "\(name) physics left spring settling")
            XCTAssertEqual(spring.phase, .idle, "\(name) physics did not reach idle from spring")
        }
    }

    func testNonFiniteTickFailsSafeToIdle() {
        var c = makeController()
        c.beginDrag()
        c.endDrag(velocityY: -1200)
        _ = c.tick(dt: .nan)
        XCTAssertEqual(c.phase, .idle)
        XCTAssertFalse(c.isSettling)
    }

    // MARK: - Indicator thumb

    func testThumbNilWithoutHistory() {
        let m = ScrollMetrics(total: 24, offset: 0, len: 24)
        XCTAssertNil(TerminalScrollController.thumb(metrics: m, trackLength: 200),
                     "a viewport with no scrollback has no thumb")
    }

    func testThumbNilForZeroTrack() {
        let m = ScrollMetrics(total: 100, offset: 0, len: 20)
        XCTAssertNil(TerminalScrollController.thumb(metrics: m, trackLength: 0))
    }

    func testThumbGeometryTracksOffset() {
        let m = ScrollMetrics(total: 100, offset: 0, len: 20)
        let top = TerminalScrollController.thumb(metrics: m, trackLength: 200, minThumb: 10)
        XCTAssertEqual(top?.length ?? 0, 40, accuracy: 0.001, "20/100 of a 200pt track")
        XCTAssertEqual(top?.offset ?? -1, 0, accuracy: 0.001, "at the top of history ⇒ thumb at top")

        var bottom = m; bottom.offset = 80   // total-len ⇒ live bottom
        let b = TerminalScrollController.thumb(metrics: bottom, trackLength: 200, minThumb: 10)
        XCTAssertEqual(b?.offset ?? -1, 160, accuracy: 0.001, "at the bottom ⇒ thumb at track end")

        var mid = m; mid.offset = 40
        let mth = TerminalScrollController.thumb(metrics: mid, trackLength: 200, minThumb: 10)
        XCTAssertEqual(mth?.offset ?? -1, 80, accuracy: 0.001, "halfway ⇒ thumb centred over its range")
    }

    func testThumbRespectsMinimumSize() {
        let m = ScrollMetrics(total: 10_000, offset: 0, len: 20)
        let thumb = TerminalScrollController.thumb(metrics: m, trackLength: 200, minThumb: 36)
        XCTAssertEqual(thumb?.length ?? 0, 36, accuracy: 0.001, "a tiny fraction still shows a grabbable thumb")
    }

    func testConfigInitMapsTunablesAndKeepsRubberBandInvariant() {
        // The config-based convenience init (#1255) threads the user-tunable
        // scroll physics through, while the rubber-band constant stays at its
        // platform-matching default (a documented invariant, not from config).
        let config = TerminalGestureConfig(
            scrollFriction: 6,
            scrollMomentumCutoff: 40,
            scrollSpringStiffness: 300,
            scrollSpringDamping: 30)
        let c = TerminalScrollController(config: config, cellHeight: 20)
        XCTAssertEqual(c.friction, 6)
        XCTAssertEqual(c.momentumCutoff, 40)
        XCTAssertEqual(c.springStiffness, 300)
        XCTAssertEqual(c.springDamping, 30)
        XCTAssertEqual(c.cellHeight, 20)
        XCTAssertEqual(c.rubberBandConstant, 0.55, accuracy: 0.0001,
                       "rubber-band constant is an invariant, not sourced from config")
    }
}
