import SwiftUI

/// A wrapping layout that places subviews left-to-right and wraps to a new row
/// whenever the next subview would overflow the proposed width.
///
/// This keeps large config-driven agent catalogs, or catalogs with long custom
/// names, visible and hittable instead of clipping off the sheet edge.
struct FlowLayout: Layout {
    var spacing: CGFloat = 8
    var lineSpacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout Void) -> CGSize {
        let width = proposal.width ?? .greatestFiniteMagnitude
        let sizes = measuredSizes(subviews, maxWidth: width)
        return Self.arrange(sizes: sizes, in: width, spacing: spacing, lineSpacing: lineSpacing).size
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout Void) {
        let sizes = measuredSizes(subviews, maxWidth: bounds.width)
        let frames = Self.arrange(sizes: sizes, in: bounds.width, spacing: spacing, lineSpacing: lineSpacing).frames
        for (index, subview) in subviews.enumerated() {
            let frame = frames[index]
            subview.place(
                at: CGPoint(x: bounds.minX + frame.minX, y: bounds.minY + frame.minY),
                proposal: ProposedViewSize(width: frame.width, height: frame.height)
            )
        }
    }

    /// Re-measure over-wide subviews at the container width so labels can wrap
    /// within the chip instead of extending past the sheet edge.
    private func measuredSizes(_ subviews: Subviews, maxWidth: CGFloat) -> [CGSize] {
        subviews.map { subview in
            let intrinsic = subview.sizeThatFits(.unspecified)
            guard maxWidth.isFinite, intrinsic.width > maxWidth else { return intrinsic }
            return subview.sizeThatFits(ProposedViewSize(width: maxWidth, height: nil))
        }
    }

    /// Pure geometry for tests: break `sizes` into rows that fit within
    /// `maxWidth`, returning item frames and the total bounding size.
    static func arrange(sizes: [CGSize], in maxWidth: CGFloat,
                        spacing: CGFloat, lineSpacing: CGFloat) -> (frames: [CGRect], size: CGSize) {
        guard maxWidth > 0 else {
            let bounded = sizes.map { CGSize(width: max(0, $0.width), height: max(0, $0.height)) }
            let width = bounded.map(\.width).max() ?? 0
            let height = bounded.enumerated().reduce(CGFloat.zero) { total, item in
                total + item.element.height + (item.offset == bounded.startIndex ? 0 : lineSpacing)
            }
            let frames = bounded.enumerated().map { index, size in
                let y = bounded[..<index].reduce(CGFloat.zero) { total, prior in
                    total + prior.height + lineSpacing
                }
                return CGRect(x: 0, y: y, width: size.width, height: size.height)
            }
            return (frames, CGSize(width: width, height: height))
        }

        var frames: [CGRect] = []
        var cursorX: CGFloat = 0
        var cursorY: CGFloat = 0
        var rowHeight: CGFloat = 0
        var boundingWidth: CGFloat = 0

        for size in sizes {
            let placedWidth = min(size.width, maxWidth)
            if cursorX > 0, cursorX + placedWidth > maxWidth {
                cursorY += rowHeight + lineSpacing
                cursorX = 0
                rowHeight = 0
            }
            frames.append(CGRect(x: cursorX, y: cursorY, width: placedWidth, height: size.height))
            cursorX += placedWidth + spacing
            rowHeight = max(rowHeight, size.height)
            boundingWidth = max(boundingWidth, cursorX - spacing)
        }

        return (frames, CGSize(width: boundingWidth, height: cursorY + rowHeight))
    }
}
