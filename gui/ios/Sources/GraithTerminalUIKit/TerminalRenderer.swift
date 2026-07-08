#if canImport(UIKit)
import UIKit

/// The rendering seam between `BaseTerminalUIView` (input + layout, this module)
/// and the shared Metal renderer (`GraithTerminalCore`, macOS track). The
/// concrete renderer is a `CAMetalLayer`-backed object that reads the shared
/// `GhosttyTerminalState`; the input view only needs cell metrics (for resize
/// math + gesture hit-testing) and a redraw signal. Wiring the real renderer is
/// an integration step (see NEEDS-IOS-VALIDATION.md).
public protocol TerminalRenderer: AnyObject {
    /// The pixel size of one cell at the current font/scale.
    var cellSize: CGSize { get }
    /// The layer to host in the view hierarchy.
    var layer: CALayer { get }
    /// Lay the render surface out to `bounds` (points) at `scale`.
    func layout(bounds: CGRect, scale: CGFloat)
    /// Draw the current terminal state if dirty (called from the display link).
    func renderIfNeeded()
    /// Force a redraw on the next tick (e.g. after new output or a resize).
    func setNeedsRender()
}
#endif
