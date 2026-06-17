import SwiftUI
import AppKit

@main
struct GraithApp: App {
    @StateObject private var store = SessionStore()

    init() {
        // SPM executables aren't .app bundles, so macOS treats them as
        // background processes. Force regular activation so the window
        // comes to front and appears in the Dock.
        NSApplication.shared.setActivationPolicy(.regular)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(store)
                .frame(minWidth: 800, minHeight: 500)
        }
        .defaultSize(width: 1200, height: 800)
        .windowStyle(.hiddenTitleBar)
        .commands {
            CommandGroup(replacing: .newItem) {
                Button("New Session") {
                    NotificationCenter.default.post(name: .showNewSession, object: nil)
                }
                .keyboardShortcut("n")
            }

            CommandMenu("Session") {
                Button("Next Session") {
                    NotificationCenter.default.post(name: .nextSession, object: nil)
                }
                .keyboardShortcut("]")

                Button("Previous Session") {
                    NotificationCenter.default.post(name: .prevSession, object: nil)
                }
                .keyboardShortcut("[")

                Divider()

                Button("Refresh") { store.refresh() }
                    .keyboardShortcut("r")
            }

            CommandMenu("Terminal") {
                Button("Increase Font Size") {
                    store.increaseFontSize()
                }
                .keyboardShortcut("=", modifiers: .command)

                Button("Decrease Font Size") {
                    store.decreaseFontSize()
                }
                .keyboardShortcut("-", modifiers: .command)

                Button("Reset Font Size") {
                    store.resetFontSize()
                }
                .keyboardShortcut("0", modifiers: .command)

                Divider()

                Button("Find...") {
                    NotificationCenter.default.post(name: .toggleFind, object: nil)
                }
                .keyboardShortcut("f")

                Button("Find Next") {
                    NotificationCenter.default.post(name: .findNext, object: nil)
                }
                .keyboardShortcut("g")

                Button("Find Previous") {
                    NotificationCenter.default.post(name: .findPrevious, object: nil)
                }
                .keyboardShortcut("g", modifiers: [.command, .shift])
            }

            CommandMenu("View") {
                Button("Split Right") {
                    NotificationCenter.default.post(name: .splitRight, object: nil)
                }
                .keyboardShortcut("d")

                Button("Close Split") {
                    NotificationCenter.default.post(name: .closeSplit, object: nil)
                }
                .keyboardShortcut("d", modifiers: [.command, .shift])

                Divider()

                ForEach(SessionStore.RendererType.allCases, id: \.self) { type in
                    Button {
                        store.renderer = type
                    } label: {
                        HStack {
                            Image(systemName: store.renderer == type ? "checkmark" : "")
                                .frame(width: 16)
                            Text(type.rawValue)
                        }
                    }
                }
            }
        }
    }
}

extension Notification.Name {
    static let showNewSession = Notification.Name("showNewSession")
    static let nextSession = Notification.Name("nextSession")
    static let prevSession = Notification.Name("prevSession")
    static let splitRight = Notification.Name("splitRight")
    static let closeSplit = Notification.Name("closeSplit")
    static let toggleFind = Notification.Name("toggleFind")
    static let findNext = Notification.Name("findNext")
    static let findPrevious = Notification.Name("findPrevious")
}
