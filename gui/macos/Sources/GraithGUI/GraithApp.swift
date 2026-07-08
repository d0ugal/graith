import SwiftUI
import AppKit

@main
struct GraithApp: App {
    /// Stable identifier for the main window group, so `openWindow(id:)` from
    /// the "New Window" command spawns another one.
    static let mainWindowID = "graith-main"

    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    /// App-global state shared by every window: one daemon connection, one
    /// session list, one approvals stream, one host registry. Per-window state
    /// (selection, split) lives in `WindowState`, owned by each `ContentView`.
    @StateObject private var store: SessionStore
    @StateObject private var hosts = HostRegistry()
    @StateObject private var approvals: ApprovalMonitor

    init() {
        let store = SessionStore()
        _store = StateObject(wrappedValue: store)
        _approvals = StateObject(wrappedValue: ApprovalMonitor(store: store))
    }

    var body: some Scene {
        WindowGroup(id: Self.mainWindowID) {
            ContentView()
                .environmentObject(store)
                .environmentObject(hosts)
                .environmentObject(approvals)
                .frame(minWidth: 800, minHeight: 500)
        }
        .defaultSize(width: 1200, height: 800)
        .windowStyle(.hiddenTitleBar)
        .commands {
            GraithCommands(store: store)
        }

        Settings {
            SettingsView()
                .environmentObject(store)
                .environmentObject(hosts)
        }
    }
}
