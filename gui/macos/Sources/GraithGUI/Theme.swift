import SwiftUI
import AppKit
import GraithDesign

/// macOS façade over the shared `GraithDesign` language (gui/shared): the SwiftUI
/// palette + semantic tokens come from the shared module (one source of truth
/// with iOS), and the macOS-only `NSColor`/`NSFont` + sizing live here. Existing
/// `Theme.*` call sites are unchanged.
enum Theme {
    // Catppuccin Mocha palette — sourced from the shared design module.
    static let base = GraithDesign.base
    static let mantle = GraithDesign.mantle
    static let crust = GraithDesign.crust
    static let surface0 = GraithDesign.surface0
    static let surface1 = GraithDesign.surface1
    static let overlay0 = GraithDesign.overlay0
    static let text = GraithDesign.text
    static let subtext0 = GraithDesign.subtext0
    static let subtext1 = GraithDesign.subtext1
    static let green = GraithDesign.green
    static let red = GraithDesign.red
    static let yellow = GraithDesign.yellow
    static let blue = GraithDesign.blue
    static let mauve = GraithDesign.mauve
    static let peach = GraithDesign.peach
    static let teal = GraithDesign.teal

    // Semantic aliases (shared with iOS via GraithDesign).
    static let accent = GraithDesign.accent
    static let success = GraithDesign.success
    static let warning = GraithDesign.warning
    static let danger = GraithDesign.danger

    static let sidebarWidth: CGFloat = GraithDesign.sidebarWidth

    // NSColor versions for terminal views
    static let terminalBg = NSColor(red: 0x1e/255.0, green: 0x1e/255.0, blue: 0x2e/255.0, alpha: 1)
    static let terminalFg = NSColor(red: 0xcd/255.0, green: 0xd6/255.0, blue: 0xf4/255.0, alpha: 1)
    static let terminalCursor = NSColor(red: 0xf5/255.0, green: 0xe0/255.0, blue: 0xdc/255.0, alpha: 1)
    static let defaultFontSize: CGFloat = 13
    static let minFontSize: CGFloat = 8
    static let maxFontSize: CGFloat = 32
    static let terminalFont = NSFont.monospacedSystemFont(ofSize: defaultFontSize, weight: .regular)

    static func terminalFont(ofSize size: CGFloat) -> NSFont {
        NSFont.monospacedSystemFont(ofSize: size, weight: .regular)
    }
}

extension Notification.Name {
    static let terminalFontSizeChanged = Notification.Name("terminalFontSizeChanged")
}
