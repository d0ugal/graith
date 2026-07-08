import SwiftUI

/// The shared graith design language (#628), consumed by both the macOS app
/// (`gui/macos`) and the iOS app (`gui/ios`) so the two feel like one product.
/// The desktop app is the north star: **Catppuccin Mocha** dark palette,
/// **monospace** typography, the letter-spaced **GRAITH** wordmark, and quiet,
/// considered empty states.
///
/// Pure SwiftUI (no libghostty/protocol deps) so any UI target can link it.
public enum GraithDesign {
    // MARK: - Catppuccin Mocha palette (close to Ghostty's default dark theme)

    public static let base = Color(graithHex: 0x1e1e2e)
    public static let mantle = Color(graithHex: 0x181825)
    public static let crust = Color(graithHex: 0x11111b)
    public static let surface0 = Color(graithHex: 0x313244)
    public static let surface1 = Color(graithHex: 0x45475a)
    public static let overlay0 = Color(graithHex: 0x6c7086)
    public static let text = Color(graithHex: 0xcdd6f4)
    public static let subtext0 = Color(graithHex: 0xa6adc8)
    public static let subtext1 = Color(graithHex: 0xbac2de)
    public static let green = Color(graithHex: 0xa6e3a1)
    public static let red = Color(graithHex: 0xf38ba8)
    public static let yellow = Color(graithHex: 0xf9e2af)
    public static let blue = Color(graithHex: 0x89b4fa)
    public static let mauve = Color(graithHex: 0xcba6f7)
    public static let peach = Color(graithHex: 0xfab387)
    public static let teal = Color(graithHex: 0x94e2d5)

    // MARK: - Semantic tokens (reference these, not raw palette names)

    /// Interactive / selected affordances.
    public static let accent = blue
    /// Running / healthy.
    public static let success = green
    /// Needs attention / approvals.
    public static let warning = yellow
    /// Errored / destructive.
    public static let danger = red

    /// The window/detail background.
    public static let background = base
    /// The sidebar background (one step darker than `background`).
    public static let sidebarBackground = crust
    /// Primary foreground text.
    public static let foreground = text
    /// Secondary / dimmed text.
    public static let secondaryForeground = subtext0
    /// Faint text and empty-state glyphs.
    public static let faint = overlay0

    // MARK: - Spacing

    public static let sidebarWidth: CGFloat = 260
}

public extension Color {
    /// Build a `Color` from a `0xRRGGBB` literal (sRGB).
    init(graithHex hex: UInt32, opacity: Double = 1) {
        let r = Double((hex >> 16) & 0xFF) / 255.0
        let g = Double((hex >> 8) & 0xFF) / 255.0
        let b = Double(hex & 0xFF) / 255.0
        self.init(.sRGB, red: r, green: g, blue: b, opacity: opacity)
    }
}
