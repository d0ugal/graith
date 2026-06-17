import AppKit
import SwiftUI
import CGhosttyVT

final class GhosttyTerminalNSView: BaseTerminalNSView {
    private var cellFont: NSFont

    override init(sessionName: String, grPath: String, fontSize: CGFloat = Theme.defaultFontSize) {
        self.cellFont = Theme.terminalFont(ofSize: fontSize)
        super.init(sessionName: sessionName, grPath: grPath, fontSize: fontSize)
    }

    override func setupRendering(fontSize: CGFloat) {
        calculateCellMetrics()
    }

    override func updateRendering(fontSize: CGFloat) {
        cellFont = Theme.terminalFont(ofSize: fontSize)
        calculateCellMetrics()
    }

    override func handleDirtyFrame() {
        setNeedsDisplay(bounds)
    }

    // MARK: - Cell Metrics

    private func calculateCellMetrics() {
        let ctFont = cellFont as CTFont
        cellHeight = ceil(CTFontGetAscent(ctFont) + CTFontGetDescent(ctFont) + CTFontGetLeading(ctFont))
        cellDescent = ceil(CTFontGetDescent(ctFont))

        let attrs: [NSAttributedString.Key: Any] = [.font: cellFont]
        let size = ("M" as NSString).size(withAttributes: attrs)
        cellWidth = ceil(size.width)
        if cellHeight == 0 { cellHeight = ceil(size.height) }
    }

    // MARK: - Drawing

    override func draw(_ dirtyRect: NSRect) {
        guard let ctx = NSGraphicsContext.current?.cgContext else { return }
        guard let terminalState else { return }

        let colors = terminalState.getColors()
        let defaultFG = nsColor(colors.foreground)
        let defaultBG = nsColor(colors.background)

        ctx.setFillColor(defaultBG.cgColor)
        ctx.fill(bounds)

        let hasSelection = terminalState.currentSelection != nil
        let searchMatches = searchState?.matches ?? []
        let currentMatchIdx = (searchState?.currentMatch ?? 0) - 1

        terminalState.iterateRows { [self] rowIndex, _, cells in
            let y = bounds.height - CGFloat(rowIndex + 1) * cellHeight

            for (colIndex, cell) in cells.enumerated() {
                let x = CGFloat(colIndex) * cellWidth
                let selected = hasSelection && terminalState.selectionContains(
                    col: UInt16(colIndex), row: UInt32(rowIndex))

                var isCurrentMatch = false
                var isMatchHighlight = false
                for (i, match) in searchMatches.enumerated() {
                    if rowIndex == match.row && colIndex >= match.col && colIndex < match.col + match.length {
                        isMatchHighlight = true
                        if i == currentMatchIdx { isCurrentMatch = true }
                        break
                    }
                }

                if cell.isEmpty && !selected && !isMatchHighlight { continue }

                if isCurrentMatch {
                    ctx.setFillColor(red: 0.98, green: 0.73, blue: 0.18, alpha: 0.8)
                    ctx.fill(CGRect(x: x, y: y, width: cellWidth, height: cellHeight))
                } else if isMatchHighlight {
                    ctx.setFillColor(red: 0.98, green: 0.73, blue: 0.18, alpha: 0.4)
                    ctx.fill(CGRect(x: x, y: y, width: cellWidth, height: cellHeight))
                } else if selected {
                    ctx.setFillColor(red: 0.35, green: 0.45, blue: 0.65, alpha: 1)
                    ctx.fill(CGRect(x: x, y: y, width: cellWidth, height: cellHeight))
                } else if let bg = cell.bgColor {
                    ctx.setFillColor(red: CGFloat(bg.r) / 255.0,
                                     green: CGFloat(bg.g) / 255.0,
                                     blue: CGFloat(bg.b) / 255.0, alpha: 1)
                    ctx.fill(CGRect(x: x, y: y, width: cellWidth, height: cellHeight))
                }

                if cell.isEmpty { continue }

                let str = String(cell.codepoints.compactMap { UnicodeScalar($0) }.map { Character($0) })
                guard !str.isEmpty else { continue }

                let fgNSColor: NSColor
                if selected {
                    fgNSColor = NSColor.white
                } else if let fg = cell.fgColor {
                    fgNSColor = NSColor(red: CGFloat(fg.r) / 255.0,
                                        green: CGFloat(fg.g) / 255.0,
                                        blue: CGFloat(fg.b) / 255.0, alpha: 1)
                } else {
                    fgNSColor = defaultFG
                }

                var font = cellFont
                if cell.bold {
                    font = NSFontManager.shared.convert(font, toHaveTrait: .boldFontMask)
                }
                if cell.italic {
                    font = NSFontManager.shared.convert(font, toHaveTrait: .italicFontMask)
                }

                var attrs: [NSAttributedString.Key: Any] = [
                    .font: font,
                    .foregroundColor: fgNSColor,
                ]
                if cell.underline {
                    attrs[.underlineStyle] = NSUnderlineStyle.single.rawValue
                }
                if cell.strikethrough {
                    attrs[.strikethroughStyle] = NSUnderlineStyle.single.rawValue
                }

                let attrStr = NSAttributedString(string: str, attributes: attrs)
                let line = CTLineCreateWithAttributedString(attrStr)
                ctx.textPosition = CGPoint(x: x, y: y + cellDescent)
                CTLineDraw(line, ctx)
            }
        }

        if let cursor = terminalState.getCursorInfo() {
            let cursorX = CGFloat(cursor.x) * cellWidth
            let cursorY = bounds.height - CGFloat(cursor.y + 1) * cellHeight
            let cursorColor = colors.cursor_has_value
                ? nsColor(colors.cursor)
                : NSColor(red: 0.96, green: 0.88, blue: 0.86, alpha: 1)

            switch cursor.style {
            case GHOSTTY_RENDER_STATE_CURSOR_VISUAL_STYLE_BLOCK:
                ctx.setFillColor(cursorColor.withAlphaComponent(0.5).cgColor)
                ctx.fill(CGRect(x: cursorX, y: cursorY, width: cellWidth, height: cellHeight))
            case GHOSTTY_RENDER_STATE_CURSOR_VISUAL_STYLE_BAR:
                ctx.setFillColor(cursorColor.cgColor)
                ctx.fill(CGRect(x: cursorX, y: cursorY, width: 2, height: cellHeight))
            case GHOSTTY_RENDER_STATE_CURSOR_VISUAL_STYLE_UNDERLINE:
                ctx.setFillColor(cursorColor.cgColor)
                ctx.fill(CGRect(x: cursorX, y: cursorY, width: cellWidth, height: 2))
            case GHOSTTY_RENDER_STATE_CURSOR_VISUAL_STYLE_BLOCK_HOLLOW:
                ctx.setStrokeColor(cursorColor.cgColor)
                ctx.setLineWidth(1)
                ctx.stroke(CGRect(x: cursorX + 0.5, y: cursorY + 0.5,
                                  width: cellWidth - 1, height: cellHeight - 1))
            default:
                break
            }
        }
    }

    private func nsColor(_ c: GhosttyColorRgb) -> NSColor {
        NSColor(red: CGFloat(c.r) / 255.0, green: CGFloat(c.g) / 255.0,
                blue: CGFloat(c.b) / 255.0, alpha: 1)
    }
}

// MARK: - SwiftUI Wrapper

struct GhosttyTerminalPane: NSViewRepresentable {
    let sessionName: String
    let grPath: String
    var fontSize: CGFloat = Theme.defaultFontSize
    var onExit: ((Int32?) -> Void)?
    var searchState: TerminalSearchState?

    func makeNSView(context: Context) -> GhosttyTerminalNSView {
        let view = GhosttyTerminalNSView(sessionName: sessionName, grPath: grPath, fontSize: fontSize)
        view.onProcessExit = { code in
            DispatchQueue.main.async { onExit?(code) }
        }
        if let searchState {
            view.configureSearch(searchState)
        }
        return view
    }

    func updateNSView(_ nsView: GhosttyTerminalNSView, context: Context) {}
}
