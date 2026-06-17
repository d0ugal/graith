import AppKit
import SwiftUI
import MetalKit
import CGhosttyVT

final class MetalTerminalNSView: BaseTerminalNSView {
    private var metalDevice: MTLDevice!
    private var metalView: MTKView!
    private var renderer: MetalTerminalRenderer!

    override func setupRendering(fontSize: CGFloat) {
        guard let device = MTLCreateSystemDefaultDevice() else {
            fatalError("Metal is not available")
        }
        self.metalDevice = device

        renderer = MetalTerminalRenderer(device: device, font: Theme.terminalFont(ofSize: fontSize))
        cellWidth = renderer.cellWidth
        cellHeight = renderer.cellHeight
        cellDescent = renderer.cellDescent

        metalView = MTKView(frame: .zero, device: device)
        metalView.delegate = renderer
        metalView.isPaused = true
        metalView.enableSetNeedsDisplay = true
        metalView.colorPixelFormat = .bgra8Unorm
        metalView.autoresizingMask = [.width, .height]

        addSubview(metalView)
    }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        updateBackingScale()
    }

    override func viewDidChangeBackingProperties() {
        super.viewDidChangeBackingProperties()
        updateBackingScale()
    }

    private func updateBackingScale() {
        let scale = window?.backingScaleFactor ?? NSScreen.main?.backingScaleFactor ?? 2.0
        renderer?.setBackingScaleFactor(scale)
    }

    override func updateRendering(fontSize: CGFloat) {
        let scale = window?.backingScaleFactor ?? NSScreen.main?.backingScaleFactor ?? 2.0
        renderer = MetalTerminalRenderer(device: metalDevice, font: Theme.terminalFont(ofSize: fontSize))
        renderer.setBackingScaleFactor(scale)
        cellWidth = renderer.cellWidth
        cellHeight = renderer.cellHeight
        cellDescent = renderer.cellDescent
        metalView.delegate = renderer
        renderer.mtkView(metalView, drawableSizeWillChange: metalView.drawableSize)
    }

    override func handleDirtyFrame() {
        rebuildMetalState()
        metalView.needsDisplay = true
    }

    override func onFrameResized() {
        metalView.frame = bounds
    }

    private func rebuildMetalState() {
        guard let terminalState else { return }

        let colors = terminalState.getColors()
        let metalColors = MetalTerminalRenderer.TerminalColors(from: colors)

        let hasSelection = terminalState.currentSelection != nil
        var selectedCells = Set<Int>()
        var allCells: [[GhosttyTerminalState.CellInfo]] = []

        let searchMatches = searchState?.matches ?? []
        var highlightedCells = Set<Int>()
        var currentMatchCells = Set<Int>()
        let currentMatchIdx = (searchState?.currentMatch ?? 0) - 1
        for (i, match) in searchMatches.enumerated() {
            for col in match.col..<(match.col + match.length) {
                let idx = match.row * Int(gridCols) + col
                highlightedCells.insert(idx)
                if i == currentMatchIdx {
                    currentMatchCells.insert(idx)
                }
            }
        }

        terminalState.iterateRows { rowIndex, _, cells in
            allCells.append(cells)
            if hasSelection {
                for colIndex in 0..<cells.count {
                    if terminalState.selectionContains(col: UInt16(colIndex), row: UInt32(rowIndex)) {
                        selectedCells.insert(rowIndex * Int(self.gridCols) + colIndex)
                    }
                }
            }
        }

        var cursorInfo: MetalTerminalRenderer.CursorInfo?
        if let cursor = terminalState.getCursorInfo() {
            cursorInfo = MetalTerminalRenderer.CursorInfo(
                x: Int(cursor.x), y: Int(cursor.y),
                ghosttyStyle: cursor.style.rawValue,
                colors: metalColors
            )
        }

        renderer.update(
            cells: allCells,
            rows: allCells.count,
            cols: Int(gridCols),
            colors: metalColors,
            cursor: cursorInfo,
            selectedCells: hasSelection ? selectedCells : nil,
            highlightedCells: highlightedCells.isEmpty ? nil : highlightedCells,
            currentMatchCells: currentMatchCells.isEmpty ? nil : currentMatchCells
        )
    }
}

// MARK: - SwiftUI Wrapper

struct MetalTerminalPane: NSViewRepresentable {
    let sessionName: String
    let grPath: String
    var fontSize: CGFloat = Theme.defaultFontSize
    var onExit: ((Int32?) -> Void)?
    var searchState: TerminalSearchState?

    func makeNSView(context: Context) -> MetalTerminalNSView {
        let view = MetalTerminalNSView(sessionName: sessionName, grPath: grPath, fontSize: fontSize)
        view.onProcessExit = { code in
            DispatchQueue.main.async { onExit?(code) }
        }
        if let searchState {
            view.configureSearch(searchState)
        }
        return view
    }

    func updateNSView(_ nsView: MetalTerminalNSView, context: Context) {}
}
