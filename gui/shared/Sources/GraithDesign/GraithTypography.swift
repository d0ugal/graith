import SwiftUI

public extension GraithDesign {
    /// The product typeface is monospaced everywhere (terminal-native feel).
    /// Use for any text style: `GraithDesign.mono(.title3)`.
    static func mono(_ style: Font.TextStyle = .body, weight: Font.Weight = .regular) -> Font {
        .system(style, design: .monospaced).weight(weight)
    }

    /// Fixed-size monospaced font (e.g. for the wordmark or metrics).
    static func mono(size: CGFloat, weight: Font.Weight = .regular) -> Font {
        .system(size: size, weight: weight, design: .monospaced)
    }
}

/// The graith wordmark: `GRAITH`, uppercase, letter-spaced, monospaced — matching
/// the desktop sidebar header. Size and color are configurable so it works as a
/// small sidebar label or a large title.
public struct GraithWordmark: View {
    private let size: CGFloat
    private let color: Color
    private let weight: Font.Weight

    public init(size: CGFloat = 13, weight: Font.Weight = .bold, color: Color = GraithDesign.foreground) {
        self.size = size
        self.weight = weight
        self.color = color
    }

    public var body: some View {
        Text("GRAITH")
            .font(GraithDesign.mono(size: size, weight: weight))
            .tracking(size * 0.18)
            .foregroundStyle(color)
            .accessibilityLabel("graith")
    }
}
