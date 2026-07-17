import XCTest
import CoreGraphics
@testable import GraithGUI

// Regression for #1234: a large / long-named agent catalog used to be laid out
// in a single fixed HStack that clipped chips off the right edge with no way to
// reach them. FlowLayout wraps instead. These exercise the pure geometry
// (`FlowLayout.arrange`) — the `Layout` protocol's `Subviews` proxies can't be
// built headlessly, so the wrapping/reachability logic is tested directly.

final class FlowLayoutTests: XCTestCase {
    private let spacing: CGFloat = 8
    private let lineSpacing: CGFloat = 8

    private func chip(_ width: CGFloat, _ height: CGFloat = 24) -> CGSize {
        CGSize(width: width, height: height)
    }

    private func arrange(_ sizes: [CGSize], width: CGFloat) -> (frames: [CGRect], size: CGSize) {
        FlowLayout.arrange(sizes: sizes, in: width, spacing: spacing, lineSpacing: lineSpacing)
    }

    /// A handful of narrow chips that fit stay on one row.
    func testShortCatalogStaysOnOneRow() {
        let sizes = [chip(60), chip(60), chip(60)]
        let result = arrange(sizes, width: 480)
        let rows = Set(result.frames.map(\.minY))
        XCTAssertEqual(rows.count, 1, "three narrow chips fit on a single row")
        XCTAssertEqual(result.frames.count, 3, "every chip is placed")
    }

    /// The core regression: many long custom agent names wrap onto multiple
    /// rows, every chip is placed, none clips past the container width, and the
    /// reported content height exactly spans every row so the bounded scroll
    /// region in MigrateSheet (and the ScrollView in NewSessionSheet) can reach
    /// the last chip.
    func testLargeCatalogWrapsAndStaysReachable() {
        // 24 agents with long custom names, each far too wide to share few rows.
        let sizes = (0..<24).map { _ in chip(160) }
        let width: CGFloat = 480
        let result = arrange(sizes, width: width)

        XCTAssertEqual(result.frames.count, 24, "no chip is dropped")

        let rows = Set(result.frames.map(\.minY))
        XCTAssertGreaterThan(rows.count, 1, "a large catalog wraps to multiple rows (not one clipped HStack)")

        // Container bounds: every chip's right edge stays within the container,
        // so none is compressed/clipped off-screen with no way to reach it.
        for frame in result.frames {
            XCTAssertLessThanOrEqual(frame.maxX, width + 0.001, "chip \(frame) overflows the container width")
        }

        // The reported content height spans exactly to the bottom of the last
        // row — the extent a bounded/scrolling container needs to reach every
        // chip (rather than merely "at least N rows tall").
        let lastRowBottom = result.frames.map(\.maxY).max()!
        XCTAssertEqual(result.size.height, lastRowBottom, accuracy: 0.001,
                       "content height equals the bottom of the last row")
    }

    /// A single chip wider than the container is still placed (one item minimum
    /// per row) *and clamped to the container width* — its label wraps within
    /// bounds instead of extending past the edge, so it never clips or becomes
    /// unreachable.
    func testOverwideSingleChipIsClampedToContainer() {
        let width: CGFloat = 480
        let result = arrange([chip(900)], width: width)
        XCTAssertEqual(result.frames.count, 1)
        XCTAssertEqual(result.frames[0].minX, 0, accuracy: 0.001, "an over-wide chip anchors at the row start")
        XCTAssertEqual(result.frames[0].width, width, accuracy: 0.001, "an over-wide chip is clamped to the container width")
        XCTAssertLessThanOrEqual(result.frames[0].maxX, width + 0.001, "clamped chip stays within bounds")
    }

    /// An over-wide chip amongst normal ones still never spills past the
    /// container, and following chips continue to pack in bounds.
    func testOverwideChipAmongNormalStaysInBounds() {
        let width: CGFloat = 480
        let result = arrange([chip(60), chip(900), chip(60)], width: width)
        XCTAssertEqual(result.frames.count, 3)
        for frame in result.frames {
            XCTAssertLessThanOrEqual(frame.maxX, width + 0.001, "chip \(frame) overflows the container width")
        }
    }

    /// Items pack left-to-right in order: the first chip anchors at x=0 and each
    /// subsequent chip on the same row sits one `spacing` further right.
    func testItemsPackInOrderWithSpacing() {
        let result = arrange([chip(60), chip(60)], width: 480)
        XCTAssertEqual(result.frames[0].minX, 0, accuracy: 0.001)
        XCTAssertEqual(result.frames[1].minX, 60 + spacing, accuracy: 0.001)
        XCTAssertEqual(result.frames[0].minY, result.frames[1].minY, accuracy: 0.001, "both fit on one row")
    }
}
