import SwiftUI
import GraithRemoteKit

struct SessionSidebar: View {
    @EnvironmentObject var store: SessionStore
    @Binding var showNewSession: Bool
    @State private var showAddHost = false

    var body: some View {
        VStack(spacing: 0) {
            // Header
            HStack {
                Text("GRAITH")
                    .font(.system(.headline, design: .monospaced))
                    .fontWeight(.bold)
                    .foregroundStyle(Theme.subtext0)
                Spacer()

                // Session count
                Text("\(store.sessions.count)")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(Theme.surface0)
                    .clipShape(Capsule())

                // Add host button
                Button(action: { showAddHost = true }) {
                    Image(systemName: "network")
                        .font(.system(size: 12, weight: .medium))
                        .foregroundStyle(Theme.subtext0)
                        .frame(width: 22, height: 22)
                        .background(Theme.surface0)
                        .clipShape(RoundedRectangle(cornerRadius: 4))
                }
                .buttonStyle(.plain)
                .help("Add a remote host over Tailscale")

                // New session button
                Button(action: { showNewSession = true }) {
                    Image(systemName: "plus")
                        .font(.system(size: 12, weight: .medium))
                        .foregroundStyle(Theme.subtext0)
                        .frame(width: 22, height: 22)
                        .background(Theme.surface0)
                        .clipShape(RoundedRectangle(cornerRadius: 4))
                }
                .buttonStyle(.plain)
                .help("New session (Cmd+N)")
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            .padding(.top, 8)

            Divider()
                .background(Theme.surface0)

            // Fleet summary bar
            if !store.sessions.isEmpty {
                FleetSummaryBar(sessions: store.sessions)
            }

            // Session list
            if store.hasRemoteHosts {
                // Multi-host: group by host, then repo, so every daemon (and its
                // connection state) is visible in one sidebar.
                ScrollView {
                    LazyVStack(spacing: 0) {
                        ForEach(store.sessionsByHost, id: \.host.id) { entry in
                            HostSection(host: entry.host, groups: entry.groups)
                        }
                    }
                    .padding(.vertical, 4)
                }
            } else if store.sessions.isEmpty {
                Spacer()
                VStack(spacing: 8) {
                    Image(systemName: "terminal")
                        .font(.system(size: 32))
                        .foregroundStyle(Theme.overlay0)
                    Text("No sessions")
                        .font(.system(.body, design: .monospaced))
                        .foregroundStyle(Theme.overlay0)
                    Button("Create session") { showNewSession = true }
                        .buttonStyle(.plain)
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(Theme.blue)
                }
                Spacer()
            } else {
                ScrollView {
                    LazyVStack(spacing: 0) {
                        ForEach(store.sessionsByRepo, id: \.repo) { group in
                            RepoSection(repo: group.repo, sessions: group.sessions)
                        }
                    }
                    .padding(.vertical, 4)
                }
            }

            // Footer with error state
            if let error = store.error {
                Divider().background(Theme.surface0)
                HStack(spacing: 6) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundStyle(Theme.yellow)
                        .font(.system(size: 10))
                    Text(error)
                        .font(.system(.caption2, design: .monospaced))
                        .foregroundStyle(Theme.yellow)
                        .lineLimit(1)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
            }
        }
        .background(Theme.mantle)
        .sheet(isPresented: $showAddHost) {
            AddHostSheet()
        }
    }
}

/// A collapsible per-host section in the multi-host sidebar: a host header
/// (label, connection state, session count, forget button) over its repo groups.
struct HostSection: View {
    let host: Host
    let groups: [(repo: String, sessions: [Session])]
    @EnvironmentObject var store: SessionStore

    private var sessionCount: Int { groups.reduce(0) { $0 + $1.sessions.count } }
    private var errorText: String? { store.hostErrors[host.id] }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: host.kind == .local ? "desktopcomputer" : "network")
                    .font(.system(size: 11))
                    .foregroundStyle(host.kind == .local ? Theme.blue : Theme.teal)
                Circle()
                    .fill(errorText == nil ? Theme.green : Theme.overlay0)
                    .frame(width: 6, height: 6)
                Text(host.label.uppercased())
                    .font(.system(.caption, design: .monospaced))
                    .fontWeight(.semibold)
                    .foregroundStyle(Theme.subtext0)
                    .lineLimit(1)
                Spacer()
                Text("\(sessionCount)")
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
                if host.kind != .local {
                    Button(action: { store.removeHost(host) }) {
                        Image(systemName: "minus.circle")
                            .font(.system(size: 11))
                            .foregroundStyle(Theme.overlay0)
                    }
                    .buttonStyle(.plain)
                    .help("Forget this host")
                }
            }
            .padding(.horizontal, 14)
            .padding(.top, 12)
            .padding(.bottom, 4)
            .background(Theme.crust)

            if let errorText {
                Text(errorText)
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(Theme.yellow)
                    .lineLimit(1)
                    .padding(.horizontal, 16)
                    .padding(.bottom, 4)
            } else if sessionCount == 0 {
                Text(host.isPaired ? "No sessions" : "Pairing…")
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
                    .padding(.horizontal, 16)
                    .padding(.bottom, 4)
            }

            ForEach(groups, id: \.repo) { group in
                RepoSection(repo: group.repo, sessions: group.sessions)
            }
        }
    }
}

/// Compact bar showing fleet-level counts
struct FleetSummaryBar: View {
    let sessions: [Session]

    var running: Int { sessions.filter { $0.isRunning }.count }
    var needsAttention: Int {
        sessions.filter { $0.needsApproval || $0.isErrored ||
            ($0.isStopped && ($0.dirty ?? false || ($0.unpushedCount ?? 0) > 0))
        }.count
    }

    var body: some View {
        HStack(spacing: 12) {
            HStack(spacing: 4) {
                Circle().fill(Theme.green).frame(width: 6, height: 6)
                Text("\(running) active")
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
            }
            if needsAttention > 0 {
                HStack(spacing: 4) {
                    Circle().fill(Theme.yellow).frame(width: 6, height: 6)
                    Text("\(needsAttention) need attention")
                        .font(.system(.caption2, design: .monospaced))
                        .foregroundStyle(Theme.yellow)
                }
            }
            Spacer()
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 6)
        .background(Theme.crust)
    }
}

struct RepoSection: View {
    let repo: String
    let sessions: [Session]
    @EnvironmentObject var store: SessionStore

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(repo.uppercased())
                .font(.system(.caption2, design: .monospaced))
                .fontWeight(.semibold)
                .foregroundStyle(Theme.overlay0)
                .padding(.horizontal, 16)
                .padding(.top, 12)
                .padding(.bottom, 4)

            let roots = store.roots(in: sessions)
            ForEach(roots) { session in
                SessionTreeNode(session: session, allSessions: sessions, depth: 0)
            }
        }
    }
}

struct SessionTreeNode: View {
    let session: Session
    let allSessions: [Session]
    let depth: Int
    @EnvironmentObject var store: SessionStore

    var children: [Session] {
        store.children(of: session.id, in: allSessions)
    }

    var hasChildren: Bool { !children.isEmpty }
    var isCollapsed: Bool { store.collapsedSessions.contains(session.id) }

    var body: some View {
        VStack(spacing: 0) {
            SessionRow(
                session: session,
                depth: depth,
                hasChildren: hasChildren,
                isCollapsed: isCollapsed,
                descendantCount: hasChildren && isCollapsed
                    ? store.descendantCount(of: session.id, in: allSessions) : 0
            )
            if !isCollapsed {
                ForEach(children) { child in
                    SessionTreeNode(session: child, allSessions: allSessions, depth: depth + 1)
                }
            }
        }
    }
}

struct SessionRow: View {
    let session: Session
    let depth: Int
    var hasChildren: Bool = false
    var isCollapsed: Bool = false
    var descendantCount: Int = 0
    @EnvironmentObject var store: SessionStore
    @EnvironmentObject var window: WindowState

    /// Width of the swipe-to-delete action revealed behind the row.
    private let swipeActionWidth: CGFloat = 72

    // Swipe-to-delete state. `revealed` is the committed (snapped) state; the
    // live finger drag is tracked in `dragOffset` and auto-resets when the
    // gesture ends. `offset` composes the two.
    @State private var revealed = false
    @GestureState private var dragOffset: CGFloat = 0

    // Action sheets / confirmation.
    @State private var showRename = false
    @State private var renameText = ""
    @State private var showFork = false
    @State private var forkName = ""
    @State private var showMigrate = false
    @State private var migrateAgent = "claude"
    @State private var showDeleteConfirm = false

    private let migrateAgents = ["claude", "codex", "agy", "opencode"]

    var isSelected: Bool {
        window.selectedSessionID == session.id
    }

    var isInSplitPane: Bool {
        window.isSplit && window.splitSessionID == session.id
    }

    var isHighlighted: Bool {
        isSelected || isInSplitPane
    }

    /// Composed swipe offset: the committed reveal plus the live drag, clamped
    /// so the row can only slide left far enough to expose the delete action.
    private var offset: CGFloat {
        let base: CGFloat = revealed ? -swipeActionWidth : 0
        return max(-swipeActionWidth, min(0, base + dragOffset))
    }

    var body: some View {
        ZStack(alignment: .trailing) {
            // Red delete action revealed as the row slides left.
            if offset < 0 {
                Button(action: { requestDelete() }) {
                    VStack(spacing: 2) {
                        Image(systemName: "trash.fill")
                            .font(.system(size: 13, weight: .semibold))
                        Text("Delete")
                            .font(.system(size: 9, weight: .semibold, design: .monospaced))
                    }
                    .foregroundStyle(.white)
                    .frame(width: swipeActionWidth)
                    .frame(maxHeight: .infinity)
                    .background(Theme.red)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }

            rowContent
                .background(Theme.mantle)
                .offset(x: offset)
                .gesture(swipeGesture)
        }
        .sheet(isPresented: $showRename) {
            SessionTextPromptSheet(
                title: "Rename Session",
                fieldLabel: "New name",
                placeholder: session.name,
                initialText: session.name,
                confirmLabel: "Rename",
                text: $renameText
            ) { store.renameSession(session, to: $0) }
        }
        .sheet(isPresented: $showFork) {
            SessionTextPromptSheet(
                title: "Fork Session",
                fieldLabel: "New session name",
                placeholder: "\(session.name)-fork",
                initialText: "\(session.name)-fork",
                confirmLabel: "Fork",
                text: $forkName
            ) { store.forkSession(session, name: $0) }
        }
        .sheet(isPresented: $showMigrate) {
            MigrateSheet(
                sessionName: session.name,
                agents: migrateAgents,
                currentAgent: session.agent,
                selectedAgent: $migrateAgent
            ) { store.migrateSession(session, agent: $0) }
        }
        .confirmationDialog(
            "Delete session \u{201c}\(session.name)\u{201d}?",
            isPresented: $showDeleteConfirm,
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive) {
                closeSwipe()
                store.deleteSession(session)
            }
            Button("Cancel", role: .cancel) { closeSwipe() }
        } message: {
            Text("This session is running. Deleting it stops the agent and removes its worktree.")
        }
    }

    /// Snap the swipe action closed.
    private func closeSwipe() {
        withAnimation(.easeOut(duration: 0.18)) { revealed = false }
    }

    /// Delete flow shared by the swipe action and context menu: confirm when the
    /// session is running, delete immediately when stopped.
    private func requestDelete() {
        if session.isRunning {
            showDeleteConfirm = true
        } else {
            closeSwipe()
            store.deleteSession(session)
        }
    }

    private var swipeGesture: some Gesture {
        DragGesture(minimumDistance: 14)
            .updating($dragOffset) { value, state, _ in
                // Track horizontal drags only; ignore predominantly-vertical
                // ones so the enclosing scroll view keeps working.
                if abs(value.translation.width) > abs(value.translation.height) {
                    state = value.translation.width
                }
            }
            .onEnded { value in
                guard abs(value.translation.width) > abs(value.translation.height) else { return }
                let projected = (revealed ? -swipeActionWidth : 0) + value.translation.width
                withAnimation(.easeOut(duration: 0.18)) {
                    revealed = projected < -swipeActionWidth / 2
                }
            }
    }

    private var rowContent: some View {
        HStack(spacing: 8) {
            // Tree indentation
            if depth > 0 {
                HStack(spacing: 0) {
                    ForEach(0..<depth, id: \.self) { _ in
                        Rectangle()
                            .fill(Theme.surface0)
                            .frame(width: 1)
                            .padding(.horizontal, 6)
                    }
                }
                .frame(width: CGFloat(depth) * 14)
            }

            // Collapse/expand indicator
            if hasChildren {
                Button(action: { store.toggleCollapsed(session.id) }) {
                    HStack(spacing: 2) {
                        Text(isCollapsed ? "▸" : "▾")
                            .font(.system(size: 10, design: .monospaced))
                            .foregroundStyle(Theme.overlay0)
                        if isCollapsed {
                            Text("\(descendantCount)")
                                .font(.system(size: 9, design: .monospaced))
                                .foregroundStyle(Theme.overlay0)
                        }
                    }
                }
                .buttonStyle(.plain)
                .frame(width: isCollapsed ? 28 : 14, alignment: .leading)
            }

            // Status dot with pulse for active agents
            ZStack {
                if session.isRunning && session.agentStatus == "active" {
                    Circle()
                        .fill(statusColor.opacity(0.3))
                        .frame(width: 12, height: 12)
                }
                Circle()
                    .fill(statusColor)
                    .frame(width: 7, height: 7)
            }
            .frame(width: 12, height: 12)

            // Session info
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    if session.starred ?? false {
                        Image(systemName: "star.fill")
                            .foregroundStyle(Theme.yellow)
                            .font(.system(size: 8))
                    }
                    Text(session.name)
                        .font(.system(.body, design: .monospaced))
                        .foregroundStyle(isHighlighted ? Theme.text : Theme.subtext1)
                        .lineLimit(1)
                        // Keep the name from being squeezed by the trailing
                        // agent badge + mode glyphs in a narrow sidebar.
                        .layoutPriority(1)

                    // Agent type badge (compact, inline)
                    Text(session.agent)
                        .font(.system(size: 9, design: .monospaced))
                        .foregroundStyle(agentColor)
                        .padding(.horizontal, 4)
                        .padding(.vertical, 1)
                        .background(agentColor.opacity(0.12))
                        .clipShape(RoundedRectangle(cornerRadius: 3))

                    // Mode/membership indicators (issue #901): YOLO, sandboxed,
                    // scenario membership, and a config-stale warning.
                    if session.isYolo {
                        Image(systemName: "bolt.fill")
                            .foregroundStyle(Theme.peach)
                            .font(.system(size: 8))
                            .help("YOLO mode \u{2014} approvals bypassed")
                    }
                    if session.isSandboxed {
                        Image(systemName: "shield.lefthalf.filled")
                            .foregroundStyle(Theme.teal)
                            .font(.system(size: 8))
                            .help("Sandboxed")
                    }
                    if session.isScenarioMember {
                        Image(systemName: "square.stack.3d.up.fill")
                            .foregroundStyle(Theme.mauve)
                            .font(.system(size: 8))
                            .help("Scenario: \(session.scenarioName ?? "member")")
                    }
                    if session.isConfigStale {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .foregroundStyle(Theme.yellow)
                            .font(.system(size: 8))
                            .help("Config changed since launch \u{2014} restart to apply")
                    }
                }

                // Status / summary line
                if let statusLine = statusLine {
                    Text(statusLine)
                        .font(.system(.caption2, design: .monospaced))
                        .foregroundStyle(statusLineColor)
                        .lineLimit(1)
                }

                // Metadata line: PR/CI badges (issue #901) plus dirty/ahead.
                let showsCI = shouldShowCI(pr: session.pullRequest, ci: session.ci)
                if session.pullRequest != nil || showsCI || !metadataSubtitle.isEmpty {
                    HStack(spacing: 6) {
                        if let pr = session.pullRequest { PRBadge(pr: pr) }
                        if let ci = session.ci, showsCI { CIBadge(ci: ci) }
                        if !metadataSubtitle.isEmpty {
                            Text(metadataSubtitle)
                                .font(.system(.caption2, design: .monospaced))
                                .foregroundStyle(Theme.overlay0)
                                .lineLimit(1)
                        }
                    }
                }
            }

            Spacer()

            // Right-edge indicators
            if window.isSplit {
                // Show which pane(s) this session appears in
                if isSelected && isInSplitPane {
                    HStack(spacing: 2) {
                        PaneBadge(label: "L")
                        PaneBadge(label: "R")
                    }
                } else if isSelected {
                    PaneBadge(label: "L")
                } else if isInSplitPane {
                    PaneBadge(label: "R")
                }
            }

            if session.isStopped {
                Text("stopped")
                    .font(.system(size: 9, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
            } else if session.isErrored {
                Text("error")
                    .font(.system(size: 9, design: .monospaced))
                    .foregroundStyle(Theme.red)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(rowBackground)
        .contentShape(Rectangle())
        .onTapGesture {
            window.selectSession(session)
        }
        .contextMenu {
            if session.isRunning {
                Button("Stop") { store.stopSession(session) }
                Button("Restart") { store.restartSession(session) }
                Button("Interrupt (Ctrl-C)") { store.interruptSession(session) }
            } else {
                Button("Resume") { store.resumeSession(session) }
            }
            Divider()
            Button("Rename…") {
                renameText = session.name
                showRename = true
            }
            Button((session.starred ?? false) ? "Unstar" : "Star") {
                store.toggleStar(session)
            }
            Button("Fork…") {
                forkName = "\(session.name)-fork"
                showFork = true
            }
            Button("Migrate…") {
                migrateAgent = session.agent
                showMigrate = true
            }
            Divider()
            Button("Copy name") {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(session.name, forType: .string)
            }
            Button("Copy ID") {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(session.id, forType: .string)
            }
            Divider()
            Button("Delete", role: .destructive) { requestDelete() }
        }
    }

    var rowBackground: Color {
        if isSelected && window.focusedPane == .primary && window.isSplit {
            return Theme.surface0
        }
        if isInSplitPane && window.focusedPane == .secondary && window.isSplit {
            return Theme.surface0
        }
        if isHighlighted {
            return Theme.surface0.opacity(0.6)
        }
        return .clear
    }

    var statusColor: Color {
        if session.needsApproval { return Theme.yellow }
        switch session.status {
        case "running": return Theme.green
        case "errored": return Theme.red
        case "stopped": return Theme.overlay0
        case "creating": return Theme.blue
        default: return Theme.overlay0
        }
    }

    var agentColor: Color {
        switch session.agent {
        case "claude": return Theme.mauve
        case "codex": return Theme.blue
        case "agy": return Theme.peach
        case "opencode": return Theme.teal
        default: return Theme.subtext0
        }
    }

    /// The primary status/summary line shown under the session name.
    var statusLine: String? {
        // Show summary text (the `gr status` message) when available
        if let summary = session.summaryText, !summary.isEmpty {
            return summary
        }
        // Fall back to agent status for running sessions
        if session.isRunning {
            if session.needsApproval {
                return "Needs approval"
            }
            if let agentSt = session.agentStatus, !agentSt.isEmpty {
                if agentSt == "active", let tool = session.toolName, !tool.isEmpty {
                    return tool
                }
                return agentSt
            }
        }
        return nil
    }

    /// Color for the status line text — faded summaries get dimmer treatment.
    var statusLineColor: Color {
        if session.needsApproval { return Theme.yellow }
        if session.summaryFaded ?? false { return Theme.overlay0 }
        if session.summaryText != nil { return Theme.subtext0 }
        return Theme.overlay0
    }

    /// Secondary metadata: cost, dirty flag, unpushed count.
    var metadataSubtitle: String {
        var parts: [String] = []

        // cost_usd is not on the wire model; the daemon does not report it.
        if session.dirty ?? false {
            parts.append("dirty")
        }
        if let n = session.unpushedCount, n > 0 {
            parts.append("\(n) ahead")
        }

        return parts.joined(separator: " \u{b7} ")
    }
}

/// A small modal prompting for a single line of text (used for Rename and
/// Fork). Styled to match `NewSessionSheet`.
struct SessionTextPromptSheet: View {
    let title: String
    let fieldLabel: String
    let placeholder: String
    let initialText: String
    let confirmLabel: String
    @Binding var text: String
    let onConfirm: (String) -> Void

    @Environment(\.dismiss) private var dismiss

    private var trimmed: String {
        text.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text(title)
                    .font(.system(.title3, design: .monospaced))
                    .fontWeight(.semibold)
                    .foregroundStyle(Theme.text)
                Spacer()
            }
            .padding(20)

            Divider().background(Theme.surface0)

            VStack(alignment: .leading, spacing: 8) {
                Text(fieldLabel)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.subtext0)
                TextField(placeholder, text: $text)
                    .textFieldStyle(.plain)
                    .font(.system(.body, design: .monospaced))
                    .padding(8)
                    .background(Theme.crust)
                    .clipShape(RoundedRectangle(cornerRadius: 6))
                    .onSubmit(confirm)
            }
            .padding(20)

            Divider().background(Theme.surface0)

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button(confirmLabel, action: confirm)
                    .keyboardShortcut(.defaultAction)
                    .disabled(trimmed.isEmpty)
            }
            .padding(20)
        }
        .frame(width: 360)
        .background(Theme.mantle)
        .onAppear { if text.isEmpty { text = initialText } }
    }

    private func confirm() {
        guard !trimmed.isEmpty else { return }
        onConfirm(trimmed)
        dismiss()
    }
}

/// A small modal to pick a new agent for `migrate`.
struct MigrateSheet: View {
    let sessionName: String
    let agents: [String]
    let currentAgent: String
    @Binding var selectedAgent: String
    let onConfirm: (String) -> Void

    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("Migrate Session")
                    .font(.system(.title3, design: .monospaced))
                    .fontWeight(.semibold)
                    .foregroundStyle(Theme.text)
                Spacer()
            }
            .padding(20)

            Divider().background(Theme.surface0)

            VStack(alignment: .leading, spacing: 8) {
                Text("Swap \u{201c}\(sessionName)\u{201d} to a different agent")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.subtext0)
                HStack(spacing: 8) {
                    ForEach(agents, id: \.self) { a in
                        AgentChip(name: a, isSelected: selectedAgent == a) {
                            selectedAgent = a
                        }
                    }
                    Spacer()
                }
            }
            .padding(20)

            Divider().background(Theme.surface0)

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button("Migrate") {
                    onConfirm(selectedAgent)
                    dismiss()
                }
                .keyboardShortcut(.defaultAction)
                .disabled(selectedAgent == currentAgent)
            }
            .padding(20)
        }
        .frame(width: 360)
        .background(Theme.mantle)
    }
}

/// Small badge showing which split pane a session is in (L or R).
struct PaneBadge: View {
    let label: String

    var body: some View {
        Text(label)
            .font(.system(size: 8, weight: .bold, design: .monospaced))
            .foregroundStyle(Theme.overlay0)
            .frame(width: 14, height: 14)
            .background(Theme.surface0)
            .clipShape(RoundedRectangle(cornerRadius: 2))
    }
}
