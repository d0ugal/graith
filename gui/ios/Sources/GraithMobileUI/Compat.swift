import SwiftUI
#if os(iOS)
import UIKit
#elseif os(macOS)
import AppKit
#endif

// Small cross-platform / back-deployment shims so the shell builds on iOS 16+
// and macOS 14+ without `#available` scattered through the views.

/// A minimal stand-in for `ContentUnavailableView` (iOS 17+/macOS 14+ only).
struct ContentUnavailableCompat: View {
    let title: String
    let systemImage: String
    let description: String

    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: systemImage)
                .font(.largeTitle)
                .foregroundStyle(.secondary)
            Text(title)
                .font(.headline)
            Text(description)
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

extension View {
    /// Overlay a small count bubble on a toolbar item (a portable `.badge`).
    @ViewBuilder
    func badgeCompat(_ count: Int) -> some View {
        if count > 0 {
            overlay(alignment: .topTrailing) {
                Text("\(count)")
                    .font(.system(size: 10, weight: .bold))
                    .foregroundStyle(.white)
                    .padding(3)
                    .background(Circle().fill(.red))
                    .offset(x: 8, y: -8)
            }
        } else {
            self
        }
    }
}

/// Copy text to the platform pasteboard.
enum Clipboard {
    static func copy(_ text: String) {
        #if os(iOS)
        UIPasteboard.general.string = text
        #elseif os(macOS)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        #endif
    }
}
