import CoreGraphics
import CoreText
import Foundation

#if os(macOS)
import AppKit
/// The platform's native font type (`NSFont` on macOS, `UIFont` on iOS).
public typealias PlatformFont = NSFont
#elseif os(iOS)
import UIKit
public typealias PlatformFont = UIFont
#endif

/// Cross-platform font helpers for the shared terminal renderer.
///
/// The renderer works entirely in CoreText (`CTFont`) internally so it needs
/// no AppKit/UIKit beyond the initial ``PlatformFont``. This is what lets
/// `MetalTerminalRenderer` compile unchanged on both macOS and iOS.
public enum TerminalFontProvider {
    /// A regular-weight monospaced system font at `size` points.
    public static func monospaced(ofSize size: CGFloat) -> PlatformFont {
        PlatformFont.monospacedSystemFont(ofSize: size, weight: .regular)
    }

    /// Bridge a ``PlatformFont`` to a `CTFont`. `NSFont`/`UIFont` both expose
    /// `fontName`/`pointSize`, so this avoids the platform-specific toll-free
    /// bridging differences between the two.
    public static func ctFont(from font: PlatformFont) -> CTFont {
        CTFontCreateWithName(font.fontName as CFString, font.pointSize, nil)
    }

    /// Derive a bold/italic variant of a `CTFont` via symbolic traits.
    public static func variant(of base: CTFont, bold: Bool, italic: Bool) -> CTFont {
        var traits: CTFontSymbolicTraits = []
        if bold { traits.insert(.traitBold) }
        if italic { traits.insert(.traitItalic) }
        if traits.isEmpty { return base }
        let size = CTFontGetSize(base)
        return CTFontCreateCopyWithSymbolicTraits(base, size, nil, traits, traits) ?? base
    }
}
