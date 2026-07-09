import CoreGraphics
import XCTest
import GraithClientAPI
@testable import GraithTerminalUIKit

/// Covers the space-drag → arrow-key state machine (issue #979). This is the
/// pure logic behind the UIKit gesture, extracted so it can be tested without a
/// real `UILongPressGestureRecognizer`.
final class SpaceDragTrackerTests: XCTestCase {

    func testStationaryTapEmitsNothingAndDoesNotCommit() {
        var tracker = SpaceDragTracker(threshold: 22)
        tracker.begin()
        XCTAssertTrue(tracker.update(translation: .zero).isEmpty)
        // Sub-threshold jitter still must not commit — a tap types a space.
        XCTAssertTrue(tracker.update(translation: CGPoint(x: 5, y: 5)).isEmpty)
        XCTAssertFalse(tracker.didEmit, "a tap must leave the drag uncommitted so space is typed")
    }

    func testShortDragEmitsOneArrowAndCommits() {
        var tracker = SpaceDragTracker(threshold: 22)
        tracker.begin()
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 25, y: 0)), [.arrowRight])
        XCTAssertTrue(tracker.didEmit, "emitting an arrow commits the drag (suppresses space)")
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
            var tracker = SpaceDragTracker(threshold: 22)
            tracker.begin()
            XCTAssertEqual(tracker.update(translation: translation), [expected],
                           "translation \(translation) should map to \(expected)")
        }
    }

    func testContinuousDragEmitsRepeatedArrows() {
        var tracker = SpaceDragTracker(threshold: 20)
        tracker.begin()
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 65, y: 0)),
                       [.arrowRight, .arrowRight, .arrowRight],
                       "65pt over a 20pt threshold crosses three thresholds")
    }

    func testIncrementalUpdatesOnlyEmitNewlyCrossedThresholds() {
        var tracker = SpaceDragTracker(threshold: 20)
        tracker.begin()
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 25, y: 0)), [.arrowRight])
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 45, y: 0)), [.arrowRight])
        XCTAssertTrue(tracker.update(translation: CGPoint(x: 50, y: 0)).isEmpty,
                      "a sub-threshold advance from the last emission emits nothing")
    }

    func testReversingDirectionEmitsTheOppositeArrow() {
        var tracker = SpaceDragTracker(threshold: 20)
        tracker.begin()
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 40, y: 0)),
                       [.arrowRight, .arrowRight])
        // Drag back left past the anchor: now two thresholds to the left of the
        // last emission point (40 → 0 is 40pt of travel back).
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 0, y: 0)),
                       [.arrowLeft, .arrowLeft])
    }

    func testDominantAxisWinsPerStep() {
        var tracker = SpaceDragTracker(threshold: 20)
        tracker.begin()
        XCTAssertEqual(tracker.update(translation: CGPoint(x: 5, y: 30)), [.arrowDown],
                       "a mostly-vertical drag reads as vertical")
    }

    func testBeginResetsCommittedState() {
        var tracker = SpaceDragTracker(threshold: 20)
        tracker.begin()
        _ = tracker.update(translation: CGPoint(x: 40, y: 0))
        XCTAssertTrue(tracker.didEmit)
        tracker.begin()
        XCTAssertFalse(tracker.didEmit, "begin() must clear the committed flag for reuse")
        XCTAssertTrue(tracker.update(translation: CGPoint(x: 5, y: 0)).isEmpty,
                      "and reset the anchor so travel is measured from the new start")
    }

    func testThresholdIsClampedToAtLeastOne() {
        var tracker = SpaceDragTracker(threshold: 0)
        tracker.begin()
        // A zero threshold would loop forever; the initializer clamps it to 1.
        let keys = tracker.update(translation: CGPoint(x: 3, y: 0))
        XCTAssertEqual(keys, [.arrowRight, .arrowRight, .arrowRight])
    }
}
