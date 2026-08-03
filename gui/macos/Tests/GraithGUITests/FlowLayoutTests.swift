import XCTest
import CoreGraphics
@testable import GraithGUI

final class FlowLayoutTests: XCTestCase {
    private let spacing: CGFloat = 8
    private let lineSpacing: CGFloat = 8

    private func chip(_ width: CGFloat, _ height: CGFloat = 24) -> CGSize {
        CGSize(width: width, height: height)
    }

    private func arrange(_ sizes: [CGSize], width: CGFloat) -> (frames: [CGRect], size: CGSize) {
        FlowLayout.arrange(sizes: sizes, in: width, spacing: spacing, lineSpacing: lineSpacing)
    }

    func testShortCatalogStaysOnOneRow() {
        let result = arrange([chip(60), chip(60), chip(60)], width: 480)
        XCTAssertEqual(Set(result.frames.map(\.minY)).count, 1)
        XCTAssertEqual(result.frames.count, 3)
    }

    func testLargeCatalogWrapsAndStaysReachable() {
        let result = arrange((0..<24).map { _ in chip(160) }, width: 480)

        XCTAssertEqual(result.frames.count, 24)
        XCTAssertGreaterThan(Set(result.frames.map(\.minY)).count, 1)

        for frame in result.frames {
            XCTAssertLessThanOrEqual(frame.maxX, 480.001, "chip \(frame) overflows the container width")
        }

        let lastRowBottom = result.frames.map(\.maxY).max()!
        XCTAssertEqual(result.size.height, lastRowBottom, accuracy: 0.001)
    }

    func testOverwideSingleChipIsClampedToContainer() {
        let result = arrange([chip(900)], width: 480)
        XCTAssertEqual(result.frames.count, 1)
        XCTAssertEqual(result.frames[0].minX, 0, accuracy: 0.001)
        XCTAssertEqual(result.frames[0].width, 480, accuracy: 0.001)
        XCTAssertLessThanOrEqual(result.frames[0].maxX, 480.001)
    }

    func testZeroWidthProposalKeepsIntrinsicWidths() {
        let result = arrange([chip(120), chip(80)], width: 0)

        XCTAssertEqual(result.frames.count, 2)
        XCTAssertEqual(result.frames[0].width, 120, accuracy: 0.001)
        XCTAssertEqual(result.frames[1].width, 80, accuracy: 0.001)
        XCTAssertGreaterThan(result.size.width, 0)
    }

    func testOverwideChipAmongNormalStaysInBounds() {
        let result = arrange([chip(60), chip(900), chip(60)], width: 480)
        XCTAssertEqual(result.frames.count, 3)
        for frame in result.frames {
            XCTAssertLessThanOrEqual(frame.maxX, 480.001, "chip \(frame) overflows the container width")
        }
    }

    func testItemsPackInOrderWithSpacing() {
        let result = arrange([chip(60), chip(60)], width: 480)
        XCTAssertEqual(result.frames[0].minX, 0, accuracy: 0.001)
        XCTAssertEqual(result.frames[1].minX, 60 + spacing, accuracy: 0.001)
        XCTAssertEqual(result.frames[0].minY, result.frames[1].minY, accuracy: 0.001)
    }
}
