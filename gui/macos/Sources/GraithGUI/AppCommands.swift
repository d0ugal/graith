import SwiftUI
import AppKit

// MARK: - Focused values

/// The key window's per-window state, published by each `ContentView` via
/// `.focusedSceneValue`. Menu commands read it so the menu bar drives the
/// frontmost window only — the idiomatic macOS behaviour.
private struct WindowStateKey: FocusedValueKey {
    typealias Value = WindowState
}

private struct SessionStoreKey: FocusedValueKey {
    typealias Value = SessionStore
}

extension FocusedValues {
    var windowState: WindowState? {
        get { self[WindowStateKey.self] }
        set { self[WindowStateKey.self] = newValue }
    }
    var sessionStore: SessionStore? {
        get { self[SessionStoreKey.self] }
        set { self[SessionStoreKey.self] = newValue }
    }
}

// MARK: - Commands

/// The full macOS menu bar. Split out of `GraithApp` so the app scene stays
/// declarative. Window-specific items target the key window's `WindowState`
/// via `@FocusedValue`; app-global items (font size, renderer, refresh) act on
/// the shared `SessionStore`. Terminal actions (Copy/Paste/Clear) are dispatched
/// down the responder chain so they reach the focused terminal view.
struct GraithCommands: Commands {
    @ObservedObject var store: SessionStore
    @FocusedValue(\.windowState) private var windowState: WindowState?
    @Environment(\.openWindow) private var openWindow

    /// Sessions in the same stable order the sidebar and ⌘1–9 use.
    private var orderedSessions: [Session] {
        store.sessions.sorted { $0.name < $1.name }
    }

    private func send(_ selector: Selector) {
        NSApp.sendAction(selector, to: nil, from: nil)
    }

    var body: some Commands {
        // MARK: File
        CommandGroup(replacing: .newItem) {
            Button("New Session") { windowState?.showNewSession = true }
                .keyboardShortcut("n")
                .disabled(windowState == nil)

            Button("New Window") { openWindow(id: GraithApp.mainWindowID) }
                .keyboardShortcut("n", modifiers: [.command, .shift])
        }

        // MARK: Edit — route to the focused terminal via the responder chain.
        CommandGroup(replacing: .pasteboard) {
            Button("Copy") { send(#selector(BaseTerminalNSView.copy(_:))) }
                .keyboardShortcut("c")
            Button("Paste") { send(#selector(BaseTerminalNSView.paste(_:))) }
                .keyboardShortcut("v")
            // selectAll(_:) also exists on NSResponder; name the superclass one
            // explicitly so the selector isn't ambiguous.
            Button("Select All") { send(#selector(NSResponder.selectAll(_:))) }
                .keyboardShortcut("a")
        }

        // MARK: About
        CommandGroup(replacing: .appInfo) {
            Button("About GraithGUI") { AboutPanel.show() }
        }

        // MARK: Session
        CommandMenu("Session") {
            Button("Next Session") {
                windowState?.selectAdjacentSession(offset: 1, in: store.sessions)
            }
            .keyboardShortcut("]", modifiers: [.command, .shift])
            .disabled(windowState == nil)

            Button("Previous Session") {
                windowState?.selectAdjacentSession(offset: -1, in: store.sessions)
            }
            .keyboardShortcut("[", modifiers: [.command, .shift])
            .disabled(windowState == nil)

            Divider()

            // ⌘1–9 jump straight to a session by position.
            ForEach(Array(orderedSessions.prefix(9).enumerated()), id: \.element.id) { index, session in
                Button(session.name) {
                    windowState?.selectSessionByIndex(index, in: store.sessions)
                }
                .keyboardShortcut(KeyEquivalent(Character("\(index + 1)")), modifiers: .command)
                .disabled(windowState == nil)
            }

            Divider()

            Button("Refresh") { store.refresh() }
                .keyboardShortcut("r")
        }

        // MARK: View
        CommandMenu("View") {
            Button(windowState?.isSplit == true ? "Close Split" : "Split Right") {
                windowState?.toggleSplit(in: store.sessions)
            }
            .keyboardShortcut("d")
            .disabled(windowState == nil)

            Divider()

            Picker("Renderer", selection: Binding(
                get: { store.renderer },
                set: { store.renderer = $0 }
            )) {
                ForEach(SessionStore.RendererType.allCases, id: \.self) { type in
                    Text(type.rawValue).tag(type)
                }
            }
        }

        // MARK: Terminal
        CommandMenu("Terminal") {
            Button("Increase Font Size") { store.increaseFontSize() }
                .keyboardShortcut("=", modifiers: .command)
            Button("Decrease Font Size") { store.decreaseFontSize() }
                .keyboardShortcut("-", modifiers: .command)
            Button("Reset Font Size") { store.resetFontSize() }
                .keyboardShortcut("0", modifiers: .command)

            Divider()

            Button("Clear") { send(#selector(BaseTerminalNSView.clearTerminal(_:))) }
                .keyboardShortcut("k")

            Divider()

            // Route to the key window's focused terminal via WindowState, not a
            // global NotificationCenter broadcast — otherwise ⌘F/⌘G would toggle
            // the search bar in every terminal of every window at once.
            Button("Find…") { windowState?.dispatchFind(.toggle) }
                .keyboardShortcut("f")
                .disabled(windowState == nil)

            Button("Find Next") { windowState?.dispatchFind(.next) }
                .keyboardShortcut("g")
                .disabled(windowState == nil)

            Button("Find Previous") { windowState?.dispatchFind(.previous) }
                .keyboardShortcut("g", modifiers: [.command, .shift])
                .disabled(windowState == nil)
        }
    }
}
