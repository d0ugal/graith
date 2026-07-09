import CoreGraphics
import XCTest
import GraithClientAPI
@testable import GraithTerminalUIKit

/// Covers the space-drag → arrow-key state machine (issue #979). This is the
/// pure logic behind the UIKit gesture, extracted so it can be tested without a
/// real `UILongPressGestureRecognizer`. The model is a held hardware arrow key:
/// one press per direction, a delay, then auto-repeat while held.
final class SpaceDragTrackerTests: XCTestCase {

    func testStationaryTapEmitsNothingAndDoesNotCommit() {
        var tracker = SpaceDragTracker(activationThreshold: 22)
        tracker.begin()
        XCTAssertTrue(tracker.update(translation: .zero, time: 0).isEmpty)
        // Sub-threshold jitter still must not commit — a tap types a space.
        XCTAssertTrue(tracker.update(translation: CGPoint(x: 5, y: 5), time: 0.1).isEmpty)
        XCTAssertFalse(tracker.didEmit, "a tap must leave the drag uncommitted so space is typed")
    }

    func testDragEmitsExactlyOneArrowAndCommits() {
        var tracker = SpaceDragTracker(activationThreshold: 22)
        tracker.begin()
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 25, y: 0), time: 0), [.arrowRight])
        XCTAssertTrue(tracker.didEmit, "emitting an arrow commits the drag (suppresses space)")
        // Dragging further in the same direction does NOT emit more arrows — the
        // model is a key press, not a scroll wheel.
        XCTAssertTrue(tracker.update(translation: CGPoint(x: 200, y: 0), time: 0.05).isEmpty,
                      "extra travel in the held direction must not emit more arrows")
    }

    func testEachDirectionMapsToTheExpectedArrow() {
        // Screen coordinates: +y is downward.
        let cases: [(CGPoint, TerminalKey)] = [
            (CGPoint(x: 30, y: 0), .arrowRight),
            (CGPoint(x: -30, y: 0), .arrowLeft),
            (CGPoint(x: 0, y: 30), .arrowDown),
            (CGPoint(x: 0, y: -30), .arrowUp),
        ]
        for (translation, expected) in cases {
            var tracker = SpaceDragTracker(activationThreshold: 22)
            tracker.begin()
            XCTAssertEqual(tracker.update(translation: translation, time: 0), [expected],
                           "translation \(translation) should map to \(expected)")
        }
    }

    func testHoldingRepeatsAfterInitialDelayThenAtInterval() {
        var tracker = SpaceDragTracker(activationThreshold: 22,
                                       initialRepeatDelay: 0.5,
                                       repeatInterval: 0.1)
        tracker.begin()
        let held = CGPoint(x: 30, y: 0)
        XCTAssertEqual(tracker.update(translation: held, time: 0.0), [.arrowRight], "initial press")
        XCTAssertTrue(tracker.update(translation: held, time: 0.4).isEmpty,
                      "no repeat before the initial delay elapses")
        XCTAssertEqual(tracker.update(translation: held, time: 0.5), [.arrowRight],
                       "first repeat fires at the initial delay")
        XCTAssertTrue(tracker.update(translation: held, time: 0.55).isEmpty,
                      "no repeat before the faster interval elapses")
        XCTAssertEqual(tracker.update(translation: held, time: 0.65), [.arrowRight],
                       "subsequent repeats fire at the repeat interval")
    }

    func testChangingDirectionEmitsOneArrowAndRestartsTheDelay() {
        var tracker = SpaceDragTracker(activationThreshold: 22,
                                       initialRepeatDelay: 0.5,
                                       repeatInterval: 0.1)
        tracker.begin()
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 30, y: 0), time: 0.0), [.arrowRight])
        // Change direction well before any repeat would have fired: one immediate
        // arrow the new way.
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 0, y: 30), time: 0.05), [.arrowDown],
                       "a direction change presses the new arrow immediately")
        // The repeat delay restarts for the new direction.
        XCTAssertTrue(tracker.update(translation: CGPoint(x: 0, y: 30), time: 0.4).isEmpty,
                      "changing direction restarts the initial repeat delay")
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 0, y: 30), time: 0.55), [.arrowDown])
    }

    func testReturningToCentreStopsRepeatAndReleasesTheKey() {
        var tracker = SpaceDragTracker(activationThreshold: 22,
                                       initialRepeatDelay: 0.5,
                                       repeatInterval: 0.1)
        tracker.begin()
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 30, y: 0), time: 0.0), [.arrowRight])
        // Finger drifts back within the threshold: key released, nothing repeats.
        XCTAssertTrue(tracker.update(translation: CGPoint(x: 5, y: 0), time: 1.0).isEmpty,
                      "returning within the activation threshold releases the key")
        // Pushing back out is a fresh press.
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 30, y: 0), time: 1.05), [.arrowRight],
                       "re-crossing the threshold is a new press")
    }

    func testHysteresisKeepsAHeldAxisAgainstDiagonalWobble() {
        var tracker = SpaceDragTracker(activationThreshold: 22, directionHysteresis: 1.5)
        tracker.begin()
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 30, y: 0), time: 0.0), [.arrowRight])
        // Vertical now nominally dominates (29 < 30) but not past the hysteresis
        // margin, so the held horizontal axis stays and no spurious arrow fires.
        XCTAssertTrue(tracker.update(translation: CGPoint(x: 29, y: 30), time: 0.01).isEmpty,
                      "a near-diagonal wobble must not thrash off the held axis")
    }

    func testHysteresisStillYieldsToAClearAxisChange() {
        var tracker = SpaceDragTracker(activationThreshold: 22, directionHysteresis: 1.5)
        tracker.begin()
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 30, y: 0), time: 0.0), [.arrowRight])
        // Vertical clearly dominates (40 vs 5), well past the margin: switch axes.
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 5, y: 40), time: 0.01), [.arrowDown],
                       "a decisive axis change still flips direction")
    }

    func testDominantAxisWins() {
        var tracker = SpaceDragTracker(activationThreshold: 22)
        tracker.begin()
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 5, y: 30), time: 0), [.arrowDown],
                       "a mostly-vertical drag reads as vertical")
    }

    func testBeginResetsState() {
        var tracker = SpaceDragTracker(activationThreshold: 22)
        tracker.begin()
        _ = tracker.update(translation: CGPoint(x: 40, y: 0), time: 0)
        XCTAssertTrue(tracker.didEmit)
        tracker.begin()
        XCTAssertFalse(tracker.didEmit, "begin() must clear the committed flag for reuse")
        XCTAssertTrue(tracker.update(translation: CGPoint(x: 5, y: 0), time: 0).isEmpty,
                      "and reset so sub-threshold travel from the new start emits nothing")
    }

    func testActivationThresholdIsClampedToAtLeastOne() {
        var tracker = SpaceDragTracker(activationThreshold: 0)
        XCTAssertEqual(tracker.activationThreshold, 1, "a zero threshold is clamped to 1")
        tracker.begin()
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 1, y: 0), time: 0), [.arrowRight])
    }

    func testRepeatIntervalIsClampedAwayFromZero() {
        // A zero interval would fire on every tick; the initializer floors it.
        let tracker = SpaceDragTracker(repeatInterval: 0)
        XCTAssertGreaterThan(tracker.repeatInterval, 0)
    }
}
