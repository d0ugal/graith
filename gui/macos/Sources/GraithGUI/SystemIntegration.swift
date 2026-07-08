import SwiftUI
import AppKit

extension Notification.Name {
    /// Posted with a `graith://host/session` URL (object: URL) when the app is
    /// asked to open one, either via `.onOpenURL` (bundled) or the AppleEvent
    /// GetURL handler below (works even for the unbundled `swift run` binary).
    static let openSessionURL = Notification.Name("openSessionURL")
}

/// App-level AppKit integration that SwiftUI doesn't cover:
///
/// - native window tabbing (`allowsAutomaticWindowTabbing`),
/// - the `graith://` URL scheme via an AppleEvent GetURL handler (LaunchServices
///   won't route URLs to an unbundled SPM binary, so we register the handler
///   ourselves and rebroadcast as a notification),
/// - keeping the app a foreground/regular app so the window and Dock tile show.
final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        // Force regular activation: an SPM executable isn't a .app bundle, so
        // macOS would otherwise treat it as a background (accessory) process.
        NSApplication.shared.setActivationPolicy(.regular)
        NSApplication.shared.activate(ignoringOtherApps: true)

        // Opt into native window tabbing (Window ▸ Merge All Windows, the tab
        // bar, ⌘⇧\ etc.).
        NSWindow.allowsAutomaticWindowTabbing = true

        // Register for graith:// URLs.
        NSAppleEventManager.shared().setEventHandler(
            self,
            andSelector: #selector(handleGetURL(_:withReplyEvent:)),
            forEventClass: AEEventClass(kInternetEventClass),
            andEventID: AEEventID(kAEGetURL)
        )
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        false
    }

    @objc func handleGetURL(_ event: NSAppleEventDescriptor, withReplyEvent: NSAppleEventDescriptor) {
        guard let string = event.paramDescriptor(forKeyword: AEKeyword(keyDirectObject))?.stringValue,
              let url = URL(string: string) else { return }
        NotificationCenter.default.post(name: .openSessionURL, object: url)
    }
}

/// A themed About panel using AppKit's standard panel with custom credits, so
/// it matches the Catppuccin look rather than a bare default.
enum AboutPanel {
    static func show() {
        NSApplication.shared.activate(ignoringOtherApps: true)

        let credits = NSAttributedString(
            string: "A native macOS front end for graith — the terminal "
                + "multiplexer for AI coding agents.\n\nConnects to the local "
                + "daemon over its Unix socket and to remote daemons over "
                + "Tailscale.",
            attributes: [
                .font: NSFont.monospacedSystemFont(ofSize: 11, weight: .regular),
                .foregroundColor: NSColor.secondaryLabelColor,
            ]
        )

        NSApplication.shared.orderFrontStandardAboutPanel(options: [
            .applicationName: "GraithGUI",
            .credits: credits,
            .init(rawValue: "Copyright"): "graith",
        ])
    }
}
