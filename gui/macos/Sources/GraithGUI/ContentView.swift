import SwiftUI
import AppKit

enum SessionUserActivity {
    static let activityType = "com.graith.session"
    static let sessionURLKey = "sessionURL"

    static func configure(_ activity: NSUserActivity, for session: Session) {
        let sessionURL = "graith://local/\(session.id)"

        activity.title = session.name
        activity.userInfo = [
            "sessionID": session.id,
            "sessionName": session.name,
            "repoName": session.repoName,
            sessionURLKey: sessionURL,
        ]
        activity.targetContentIdentifier = sessionURL
        activity.isEligibleForHandoff = true
    }
}

struct ContentView: View {
    @EnvironmentObject var store: SessionStore
    /// Per-window selection/split state (see WindowState.swift). Each window
    /// gets its own instance so multiple windows can show different sessions.
    @StateObject private var window = WindowState()

    @State private var sidebarWidth: CGFloat = Theme.sidebarWidth
    /// Proportion of the terminal area given to the primary (left) pane (0..1).
    @State private var splitFraction: CGFloat = 0.5

    // Window state restoration: SwiftUI persists these per-scene and restores
    // them on relaunch, so each reopened window recovers what it was showing.
    @SceneStorage("selectedSessionID") private var restoredSelectedID: String = ""
    @SceneStorage("splitSessionID") private var restoredSplitID: String = ""
    @SceneStorage("isSplit") private var restoredIsSplit: Bool = false

    var body: some View {
        ResizableSplitView(sidebarWidth: $sidebarWidth) {
            SessionSidebar(showNewSession: $window.showNewSession)
        } trailing: {
            terminalArea
        }
        .background(Theme.crust)
        .environmentObject(window)
        .focusedSceneValue(\.windowState, window)
        .focusedSceneValue(\.sessionStore, store)
        .preferredColorScheme(.dark)
        .sheet(isPresented: $window.showNewSession) {
            NewSessionSheet()
        }
        .onChange(of: store.sessions) { _, sessions in
            window.prune(against: sessions)
        }
        .onChange(of: window.selectedSessionID) { _, id in
            restoredSelectedID = id ?? ""
        }
        .onChange(of: window.splitSessionID) { _, id in
            restoredSplitID = id ?? ""
        }
        .onChange(of: window.isSplit) { _, value in
            restoredIsSplit = value
        }
        .onAppear { restoreWindowState() }
        .onOpenURL { url in handleURL(url) }
        .onReceive(NotificationCenter.default.publisher(for: .openSessionURL)) { note in
            if let url = note.object as? URL { handleURL(url) }
        }
        .userActivity(SessionUserActivity.activityType, isActive: window.selectedSessionID != nil) { activity in
            advertiseCurrentSession(activity)
        }
    }

    @ViewBuilder
    var terminalArea: some View {
        if window.isSplit {
            splitTerminalArea
        } else if let session = window.selectedSession(in: store.sessions) {
            terminalView(for: session, pane: .primary)
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
                    session: window.selectedSession(in: store.sessions),
                    pane: .primary,
                    isFocused: window.focusedPane == .primary
                )
                .frame(width: geo.size.width * splitFraction)

                // Divider between panes
                TerminalSplitDivider(
                    splitFraction: $splitFraction,
                    totalWidth: geo.size.width
                )

                // Secondary pane (right)
                splitPaneContent(
                    session: window.splitSession(in: store.sessions),
                    pane: .secondary,
                    isFocused: window.focusedPane == .secondary
                )
            }
        }
    }

    @ViewBuilder
    func splitPaneContent(session: Session?, pane: WindowState.SplitPane, isFocused: Bool) -> some View {
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
                terminalView(for: session, pane: pane)
            } else {
                TerminalPlaceholder()
            }
        }
        .contentShape(Rectangle())
        .onTapGesture {
            window.focusedPane = pane
        }
    }

    @ViewBuilder
    func terminalView(for session: Session, pane: WindowState.SplitPane) -> some View {
        TerminalContainer(session: session, pane: pane)
            .id(session.id)
    }

    // MARK: - Window state restoration

    private func restoreWindowState() {
        if window.selectedSessionID == nil, !restoredSelectedID.isEmpty {
            window.selectedSessionID = restoredSelectedID
        }
        if window.splitSessionID == nil, !restoredSplitID.isEmpty {
            window.splitSessionID = restoredSplitID
        }
        if restoredIsSplit { window.isSplit = true }
    }

    // MARK: - URL scheme (graith://host/session)

    private func handleURL(_ url: URL) {
        guard url.scheme == "graith" else { return }
        // graith://<host>/<session-name-or-id>. v1 is local-only, so the host
        // component is advisory; we match the session by name or id.
        let target = url.lastPathComponent
        guard !target.isEmpty, target != "/" else { return }
        if let match = store.sessions.first(where: { $0.id == target || $0.name == target }) {
            window.selectSession(match)
        }
    }

    // MARK: - Handoff (NSUserActivity)

    private func advertiseCurrentSession(_ activity: NSUserActivity) {
        guard let session = window.selectedSession(in: store.sessions) else { return }
        SessionUserActivity.configure(activity, for: session)
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
