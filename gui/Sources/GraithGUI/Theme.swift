import SwiftUI
import AppKit

enum Theme {
    // Catppuccin Mocha palette — close to Ghostty's default dark theme
    static let base = Color(hex: 0x1e1e2e)
    static let mantle = Color(hex: 0x181825)
    static let crust = Color(hex: 0x11111b)
    static let surface0 = Color(hex: 0x313244)
    static let surface1 = Color(hex: 0x45475a)
    static let overlay0 = Color(hex: 0x6c7086)
    static let text = Color(hex: 0xcdd6f4)
    static let subtext0 = Color(hex: 0xa6adc8)
    static let subtext1 = Color(hex: 0xbac2de)
    static let green = Color(hex: 0xa6e3a1)
    static let red = Color(hex: 0xf38ba8)
    static let yellow = Color(hex: 0xf9e2af)
    static let blue = Color(hex: 0x89b4fa)
    static let mauve = Color(hex: 0xcba6f7)
    static let peach = Color(hex: 0xfab387)
    static let teal = Color(hex: 0x94e2d5)

    static let sidebarWidth: CGFloat = 260

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

extension Color {
    init(hex: UInt32, opacity: Double = 1) {
        let r = Double((hex >> 16) & 0xFF) / 255.0
        let g = Double((hex >> 8) & 0xFF) / 255.0
        let b = Double(hex & 0xFF) / 255.0
        self.init(.sRGB, red: r, green: g, blue: b, opacity: opacity)
    }
}
