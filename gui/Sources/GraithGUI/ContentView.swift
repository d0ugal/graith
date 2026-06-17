import SwiftUI
import AppKit

struct ContentView: View {
    @EnvironmentObject var store: SessionStore
    @State private var sidebarWidth: CGFloat = Theme.sidebarWidth
    @State private var showNewSession = false
    /// Proportion of the terminal area given to the primary (left) pane (0..1).
    @State private var splitFraction: CGFloat = 0.5
    @State private var shortcutMonitor: Any?

    var body: some View {
        ResizableSplitView(sidebarWidth: $sidebarWidth) {
            SessionSidebar(showNewSession: $showNewSession)
        } trailing: {
            terminalArea
        }
        .background(Theme.crust)
        .preferredColorScheme(.dark)
        .sheet(isPresented: $showNewSession) {
            NewSessionSheet()
        }
        .onReceive(NotificationCenter.default.publisher(for: .showNewSession)) { _ in
            showNewSession = true
        }
        .onReceive(NotificationCenter.default.publisher(for: .nextSession)) { _ in
            store.selectAdjacentSession(offset: 1)
        }
        .onReceive(NotificationCenter.default.publisher(for: .prevSession)) { _ in
            store.selectAdjacentSession(offset: -1)
        }
        .onReceive(NotificationCenter.default.publisher(for: .splitRight)) { _ in
            store.splitRight()
        }
        .onReceive(NotificationCenter.default.publisher(for: .closeSplit)) { _ in
            store.closeSplit()
        }
        .onAppear { installSessionSwitchShortcuts() }
        .onDisappear {
            if let monitor = shortcutMonitor {
                NSEvent.removeMonitor(monitor)
                shortcutMonitor = nil
            }
        }
    }

    @ViewBuilder
    var terminalArea: some View {
        if store.isSplit {
            splitTerminalArea
        } else if let session = store.selectedSession {
            terminalView(for: session)
        } else {
            TerminalPlaceholder()
        }
    }

    /// Two terminals side by side with a draggable divider.
    var splitTerminalArea: some View {
        GeometryReader { geo in
            HStack(spacing: 0) {
                // Primary pane (left)
                splitPaneContent(
                    session: store.selectedSession,
                    pane: .primary,
                    isFocused: store.focusedPane == .primary
                )
                .frame(width: geo.size.width * splitFraction)

                // Divider between panes
                TerminalSplitDivider(
                    splitFraction: $splitFraction,
                    totalWidth: geo.size.width
                )

                // Secondary pane (right)
                splitPaneContent(
                    session: store.splitSession,
                    pane: .secondary,
                    isFocused: store.focusedPane == .secondary
                )
            }
        }
    }

    @ViewBuilder
    func splitPaneContent(session: Session?, pane: SessionStore.SplitPane, isFocused: Bool) -> some View {
        VStack(spacing: 0) {
            // Pane header showing session name
            HStack(spacing: 6) {
                Circle()
                    .fill(isFocused ? Theme.green : Theme.surface1)
                    .frame(width: 6, height: 6)
                if let session = session {
                    Text(session.name)
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(isFocused ? Theme.subtext1 : Theme.overlay0)
                        .lineLimit(1)
                } else {
                    Text("No session")
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(Theme.overlay0)
                }
                Spacer()
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(Theme.crust)

            // Terminal content
            if let session = session {
                terminalView(for: session)
            } else {
                TerminalPlaceholder()
            }
        }
        .contentShape(Rectangle())
        .onTapGesture {
            store.focusedPane = pane
        }
    }

    @ViewBuilder
    func terminalView(for session: Session) -> some View {
        TerminalContainer(session: session, grPath: store.grPath)
            .id(session.id)
    }

    func installSessionSwitchShortcuts() {
        guard shortcutMonitor == nil else { return }
        // Cmd+1-9 to jump to session by index (not handled by menu commands)
        shortcutMonitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { event in
            guard event.modifierFlags.contains(.command),
                  !event.modifierFlags.contains(.shift),
                  let char = event.charactersIgnoringModifiers,
                  let digit = Int(char), digit >= 1 && digit <= 9 else {
                return event
            }
            store.selectSessionByIndex(digit - 1)
            return nil
        }
    }
}

// MARK: - Split Divider

/// Draggable divider between two terminal panes in split view.
struct TerminalSplitDivider: View {
    @Binding var splitFraction: CGFloat
    let totalWidth: CGFloat
    @State private var isDragging = false
    @State private var dragStartFraction: CGFloat?

    var body: some View {
        Rectangle()
            .fill(isDragging ? Theme.surface1 : Theme.surface0)
            .frame(width: isDragging ? 3 : 1)
            .contentShape(Rectangle().inset(by: -4))
            .onHover { hovering in
                if hovering {
                    NSCursor.resizeLeftRight.push()
                } else {
                    NSCursor.pop()
                }
            }
            .gesture(
                DragGesture(minimumDistance: 1)
                    .onChanged { value in
                        isDragging = true
                        if dragStartFraction == nil { dragStartFraction = splitFraction }
                        let startPx = (dragStartFraction ?? splitFraction) * totalWidth
                        let newPx = startPx + value.translation.width
                        let newFraction = newPx / totalWidth
                        splitFraction = min(max(newFraction, 0.2), 0.8)
                    }
                    .onEnded { _ in
                        isDragging = false
                        dragStartFraction = nil
                    }
            )
            .onTapGesture(count: 2) {
                withAnimation(.easeInOut(duration: 0.2)) {
                    splitFraction = 0.5
                }
            }
    }
}

/// Placeholder shown when no session is selected
struct TerminalPlaceholder: View {
    var body: some View {
        VStack(spacing: 16) {
            Image(systemName: "rectangle.split.2x1")
                .font(.system(size: 48))
                .foregroundStyle(Theme.surface1)

            Text("Select a session")
                .font(.system(.title3, design: .monospaced))
                .foregroundStyle(Theme.overlay0)

            VStack(spacing: 4) {
                Text("Click a session in the sidebar to attach")
                    .font(.system(.body, design: .monospaced))
                    .foregroundStyle(Theme.surface1)
                Text("Cmd+N to create a new session")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.surface1)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.base)
    }
}
