import SwiftUI

struct SessionSidebar: View {
    @EnvironmentObject var store: SessionStore
    @Binding var showNewSession: Bool

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
            if store.sessions.isEmpty {
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

    var isSelected: Bool {
        window.selectedSessionID == session.id
    }

    var isInSplitPane: Bool {
        window.isSplit && window.splitSessionID == session.id
    }

    var isHighlighted: Bool {
        isSelected || isInSplitPane
    }

    var body: some View {
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

                    // Agent type badge (compact, inline)
                    Text(session.agent)
                        .font(.system(size: 9, design: .monospaced))
                        .foregroundStyle(agentColor)
                        .padding(.horizontal, 4)
                        .padding(.vertical, 1)
                        .background(agentColor.opacity(0.12))
                        .clipShape(RoundedRectangle(cornerRadius: 3))
                }

                // Status / summary line
                if let statusLine = statusLine {
                    Text(statusLine)
                        .font(.system(.caption2, design: .monospaced))
                        .foregroundStyle(statusLineColor)
                        .lineLimit(1)
                }

                // Metadata subtitle (cost, dirty, ahead)
                if !metadataSubtitle.isEmpty {
                    Text(metadataSubtitle)
                        .font(.system(.caption2, design: .monospaced))
                        .foregroundStyle(Theme.overlay0)
                        .lineLimit(1)
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
            } else {
                Button("Resume") { store.resumeSession(session) }
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
            Button("Delete", role: .destructive) { store.deleteSession(session) }
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
