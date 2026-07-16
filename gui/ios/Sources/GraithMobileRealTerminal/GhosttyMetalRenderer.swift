#if canImport(UIKit)
import UIKit
import MetalKit
import GraithSessionKit
import GraithTerminalCore
import GraithTerminalUIKit

/// Concrete iOS `TerminalRenderer`: an `MTKView` backed by the shared
/// `MetalTerminalRenderer`, reading a `GhosttyTerminalState`. This is a direct
/// port of the macOS `MetalTerminalNSView` (gui/macos), which drives the same
/// renderer — kept in lock-step so both platforms render identically.
///
/// The app constructs one `GhosttyTerminalState` and hands it to *both* the
/// `GhosttyTerminalDriver` (which writes PTY output into it) and this renderer
/// (which reads it each frame), so `BaseTerminalUIView`'s input/layout seam and
/// the render surface share one terminal.
public final class GhosttyMetalRenderer: NSObject, TerminalRenderer {
    private let state: GhosttyTerminalState
    private let device: MTLDevice
    private let metalView: MTKView
    private let renderer: MetalTerminalRenderer
    private var dirty = true
    // Placeholder until layout(bounds:scale:) supplies the view's real scale.
    private var scale: CGFloat = 2

    public init(state: GhosttyTerminalState, fontSize: CGFloat = PresentationPreferences.default.terminalFontSize) {
        self.state = state
        guard let dev = MTLCreateSystemDefaultDevice() else {
            fatalError("graith: Metal is not available on this device")
        }
        self.device = dev
        self.renderer = MetalTerminalRenderer(
            device: dev,
            font: TerminalFontProvider.monospaced(ofSize: fontSize)
        )
        self.metalView = MTKView(frame: .zero, device: dev)
        super.init()
        metalView.delegate = renderer
        metalView.isPaused = true
        metalView.enableSetNeedsDisplay = true
        metalView.colorPixelFormat = .bgra8Unorm
        metalView.isOpaque = true
    }

    // MARK: - TerminalRenderer

    public var layer: CALayer { metalView.layer }

    /// One cell in points. `renderer.cellWidth/Height` are already point metrics
    /// (the input view divides point bounds by this for cols/rows, then
    /// multiplies by scale for the pixel cell size sent to the PTY) — so do NOT
    /// divide by scale (that made the grid ~scale× too many columns). [codex]
    public var cellSize: CGSize {
        CGSize(width: renderer.cellWidth, height: renderer.cellHeight)
    }

    public func layout(bounds: CGRect, scale: CGFloat) {
        self.scale = scale
        metalView.frame = bounds
        // Match the drawable to the device's backing scale so glyphs are crisp
        // (a stale/placeholder scale renders the text blocky).
        metalView.contentScaleFactor = scale
        renderer.setBackingScaleFactor(scale)
        renderer.mtkView(metalView, drawableSizeWillChange: metalView.drawableSize)
        dirty = true
        renderIfNeeded()
    }

    public func setNeedsRender() { dirty = true }

    public func renderIfNeeded() {
        // Called from the view's 60 Hz display link. Repaint every tick so
        // streamed PTY output shows live (not only after a keystroke) —
        // `updateRenderState` advances libghostty's snapshot to the latest write.
        // (Coarse but correct; can be gated on a dirty signal once the streamed
        // dirty-tracking is confirmed reliable on-device.)
        _ = state.updateRenderState()
        rebuild()
        metalView.setNeedsDisplay()
        dirty = false
    }

    // MARK: - State -> renderer (ported from MetalTerminalNSView.rebuildMetalState)

    private func rebuild() {
        let colors = state.getColors()
        let metalColors = MetalTerminalRenderer.TerminalColors(from: colors)
        let gridCols = Int(state.cols)
        let hasSelection = state.currentSelection != nil

        var selectedCells = Set<Int>()
        var allCells: [[GhosttyTerminalState.CellInfo]] = []
        state.iterateRows { rowIndex, _, cells in
            allCells.append(cells)
            if hasSelection {
                for colIndex in 0..<cells.count
                where state.selectionContains(col: UInt16(colIndex), row: UInt32(rowIndex)) {
                    selectedCells.insert(rowIndex * gridCols + colIndex)
                }
            }
        }

        var cursorInfo: MetalTerminalRenderer.CursorInfo?
        if let cursor = state.getCursorInfo() {
            cursorInfo = MetalTerminalRenderer.CursorInfo(
                x: Int(cursor.x), y: Int(cursor.y),
                ghosttyStyle: cursor.style.rawValue,
                colors: metalColors
            )
        }

        renderer.update(
            cells: allCells,
            rows: allCells.count,
            cols: gridCols,
            colors: metalColors,
            cursor: cursorInfo,
            selectedCells: hasSelection ? selectedCells : nil,
            highlightedCells: nil,
            currentMatchCells: nil
        )
    }
}
#endif
