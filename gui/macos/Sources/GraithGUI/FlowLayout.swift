import SwiftUI

/// A wrapping (flow) layout: places subviews left-to-right and wraps to a new
/// row whenever the next subview would overflow the proposed width.
///
/// Replaces the fixed `HStack` that clipped long or numerous agent chips off
/// the right edge (#1234). A config-driven agent catalog with many custom
/// names — or a few very long ones — now wraps onto as many rows as it needs,
/// so every chip stays visible and hittable in `NewSessionSheet` and the
/// `MigrateSheet` picker instead of being compressed/truncated past the sheet
/// edge with no way to reach it.
struct FlowLayout: Layout {
    /// Horizontal gap between chips on a row.
    var spacing: CGFloat = 8
    /// Vertical gap between wrapped rows.
    var lineSpacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout Void) -> CGSize {
        // An unspecified/infinite proposed width means "as wide as you like":
        // fall back to the widest single item so the intrinsic size is sane.
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

    /// Each subview's size, re-measured capped to `maxWidth` when its intrinsic
    /// width would overflow a whole row. Re-measuring at the capped width lets a
    /// too-long label *wrap* within the container (the chip grows taller) rather
    /// than extending past the edge and clipping — so an over-wide agent name
    /// stays fully readable and reachable (#1234).
    private func measuredSizes(_ subviews: Subviews, maxWidth: CGFloat) -> [CGSize] {
        subviews.map { subview in
            let intrinsic = subview.sizeThatFits(.unspecified)
            guard maxWidth.isFinite, intrinsic.width > maxWidth else { return intrinsic }
            return subview.sizeThatFits(ProposedViewSize(width: maxWidth, height: nil))
        }
    }

    /// Pure geometry: break `sizes` into rows that fit within `maxWidth`,
    /// returning each item's frame (origin relative to the container's
    /// top-left) and the total bounding size.
    ///
    /// Exposed `static` and dependency-free so the wrapping behaviour can be
    /// unit-tested directly: `Layout`'s `Subviews` proxies can't be constructed
    /// in a headless XCTest, so the geometry that decides reachability lives
    /// here where a large catalog can be exercised without rendering.
    ///
    /// Each item's placed width is clamped to `maxWidth`, so a single item
    /// wider than the container is anchored at the row start *and constrained
    /// to the container width* (its label wraps) rather than extending past the
    /// edge — no chip ever spills out of bounds or becomes unreachable.
    static func arrange(sizes: [CGSize], in maxWidth: CGFloat,
                        spacing: CGFloat, lineSpacing: CGFloat) -> (frames: [CGRect], size: CGSize) {
        var frames: [CGRect] = []
        var cursorX: CGFloat = 0
        var cursorY: CGFloat = 0
        var rowHeight: CGFloat = 0
        var boundingWidth: CGFloat = 0

        for size in sizes {
            // Never let a placed item exceed the container: an over-wide item is
            // pinned to the container width so its right edge stays in bounds.
            let placedWidth = min(size.width, maxWidth)
            // Wrap when this item would overflow the current row — but never on
            // the first item of a row, so a too-wide item still gets a frame.
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
