import SwiftUI
import GraithClientAPI
import GraithDesign

/// The aggregated multi-host sidebar: host → repo → session tree (design §C.4).
/// Session IDs are per-daemon, so selection is namespaced by `SessionRef`.
struct SessionSidebar: View {
    @ObservedObject var model: AppModel

    var body: some View {
        List(selection: $model.selection) {
            ForEach(model.connections) { conn in
                HostSection(connection: conn)
            }
        }
        .scrollContentBackground(.hidden)
        .background(GraithDesign.sidebarBackground)
        .font(GraithDesign.mono(.callout))
        .refreshable {
            await model.connectAll()
        }
    }
}

/// One host's section: connection state header + repo groups.
private struct HostSection: View {
    @ObservedObject var connection: HostConnection

    var body: some View {
        Section {
            switch connection.state {
            case .connecting:
                Label("Connecting…", systemImage: "hourglass")
                    .foregroundStyle(.secondary)
            case .failed(let msg):
                Label(msg, systemImage: "exclamationmark.triangle")
                    .foregroundStyle(.red)
                    .font(.footnote)
            case .idle, .connected:
                ForEach(repoGroups, id: \.repo) { group in
                    RepoGroup(repo: group.repo, sessions: group.sessions, connection: connection)
                }
            }
        } header: {
            HStack {
                Image(systemName: "server.rack")
                Text(connection.entry.label)
                Spacer()
                ConnectionDot(state: connection.state)
            }
        }
    }

    private var repoGroups: [(repo: String, sessions: [SessionInfo])] {
        let grouped = Dictionary(grouping: connection.sessions) { $0.repoName.isEmpty ? "—" : $0.repoName }
        return grouped
            .map { (repo: $0.key, sessions: $0.value.sorted { $0.name < $1.name }) }
            .sorted { $0.repo < $1.repo }
    }
}

private struct RepoGroup: View {
    let repo: String
    let sessions: [SessionInfo]
    @ObservedObject var connection: HostConnection

    var body: some View {
        DisclosureGroup {
            ForEach(sessions) { session in
                SessionRow(session: session, connection: connection)
                    .tag(SessionRef(hostID: connection.id, sessionID: session.id))
            }
        } label: {
            HStack {
                Image(systemName: "folder")
                Text(repo).font(.subheadline.weight(.medium))
                Spacer()
                Text("\(sessions.count)").foregroundStyle(.secondary).font(.caption)
            }
        }
    }
}

/// A single session row: status dot, name, agent, and PR/CI + attention badges.
/// Carries the per-session actions (swipe-to-delete + context menu, issue #899).
struct SessionRow: View {
    let session: SessionInfo
    @ObservedObject var connection: HostConnection

    // Action sheets / confirmation.
    @State private var showRename = false
    @State private var renameText = ""
    @State private var showFork = false
    @State private var forkName = ""
    @State private var showMigrate = false
    @State private var showDeleteConfirm = false

    private let migrateAgents = ["claude", "codex", "agy", "opencode"]

    var body: some View {
        HStack(spacing: 8) {
            StatusDot(session: session)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(session.name).font(.body)
                    if session.starred == true { Image(systemName: "star.fill").font(.caption2).foregroundStyle(.yellow) }
                    if session.isYolo { Image(systemName: "bolt.fill").font(.caption2).foregroundStyle(.orange) }
                    if session.sandboxed == true { Image(systemName: "shield.lefthalf.filled").font(.caption2).foregroundStyle(.secondary) }
                    if session.isScenarioMember { Image(systemName: "square.stack.3d.up.fill").font(.caption2).foregroundStyle(.purple) }
                    if session.configStale == true { Image(systemName: "exclamationmark.triangle.fill").font(.caption2).foregroundStyle(.yellow) }
                }
                if let summary = session.summaryText, !summary.isEmpty {
                    Text(summary)
                        .font(.caption)
                        .foregroundStyle(session.summaryFaded == true ? .tertiary : .secondary)
                        .lineLimit(1)
                }
                HStack(spacing: 6) {
                    Text(session.agent).font(.caption2).foregroundStyle(.secondary)
                    if let pr = session.pullRequest { PRBadge(pr: pr) }
                    if let ci = session.ci { CIBadge(ci: ci) }
                }
            }
            Spacer()
            if session.needsApproval {
                Image(systemName: "bell.badge.fill").foregroundStyle(.orange)
            }
        }
        .padding(.vertical, 2)
        // Swipe left → Delete (red). Confirms when running, immediate when
        // stopped (issue #899).
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            Button(role: .destructive) { requestDelete() } label: {
                Label("Delete", systemImage: "trash")
            }
        }
        // Swipe right → toggle star.
        .swipeActions(edge: .leading, allowsFullSwipe: true) {
            Button { Task { await connection.toggleStar(session) } } label: {
                Label(session.starred == true ? "Unstar" : "Star",
                      systemImage: session.starred == true ? "star.slash" : "star")
            }
            .tint(.yellow)
        }
        .contextMenu { contextMenuItems }
        .confirmationDialog(
            "Delete session \u{201c}\(session.name)\u{201d}?",
            isPresented: $showDeleteConfirm,
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive) { Task { await connection.delete(session) } }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This session is running. Deleting it stops the agent and removes its worktree.")
        }
        .sheet(isPresented: $showRename) {
            SessionTextPromptSheet(
                title: "Rename Session", fieldLabel: "New name",
                placeholder: session.name, confirmLabel: "Rename", text: $renameText
            ) { newName in Task { await connection.rename(session, to: newName) } }
        }
        .sheet(isPresented: $showFork) {
            SessionTextPromptSheet(
                title: "Fork Session", fieldLabel: "New session name",
                placeholder: "\(session.name)-fork", confirmLabel: "Fork", text: $forkName
            ) { name in Task { await connection.fork(session, name: name) } }
        }
        .sheet(isPresented: $showMigrate) {
            MigrateSheet(sessionName: session.name, agents: migrateAgents, currentAgent: session.agent) { agent in
                Task { await connection.migrate(session, agent: agent) }
            }
        }
    }

    @ViewBuilder
    private var contextMenuItems: some View {
        if session.isStopped {
            Button { Task { await connection.resume(session) } } label: { Label("Resume", systemImage: "play") }
        } else {
            Button { Task { await connection.stop(session) } } label: { Label("Stop", systemImage: "stop") }
            Button { Task { await connection.restart(session) } } label: { Label("Restart", systemImage: "arrow.clockwise") }
            Button { Task { await connection.interrupt(session) } } label: { Label("Interrupt (Ctrl-C)", systemImage: "hand.raised") }
        }
        Divider()
        Button { renameText = session.name; showRename = true } label: { Label("Rename…", systemImage: "pencil") }
        Button { Task { await connection.toggleStar(session) } } label: {
            Label(session.starred == true ? "Unstar" : "Star",
                  systemImage: session.starred == true ? "star.slash" : "star")
        }
        Button { forkName = "\(session.name)-fork"; showFork = true } label: { Label("Fork…", systemImage: "arrow.triangle.branch") }
        Button { showMigrate = true } label: { Label("Migrate…", systemImage: "arrow.left.arrow.right") }
        Divider()
        Button(role: .destructive) { requestDelete() } label: { Label("Delete", systemImage: "trash") }
    }

    /// Confirm when running, delete immediately when stopped.
    private func requestDelete() {
        if session.isRunning {
            showDeleteConfirm = true
        } else {
            Task { await connection.delete(session) }
        }
    }
}

/// A minimal single-field prompt sheet (Rename / Fork).
struct SessionTextPromptSheet: View {
    let title: String
    let fieldLabel: String
    let placeholder: String
    let confirmLabel: String
    @Binding var text: String
    let onConfirm: (String) -> Void

    @Environment(\.dismiss) private var dismiss

    private var trimmed: String { text.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        NavigationStack {
            Form {
                Section(fieldLabel) {
                    TextField(placeholder, text: $text)
                        .autocorrectionDisabled()
                        .onSubmit(confirm)
                }
            }
            .navigationTitle(title)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(confirmLabel, action: confirm).disabled(trimmed.isEmpty)
                }
            }
        }
    }

    private func confirm() {
        guard !trimmed.isEmpty else { return }
        onConfirm(trimmed)
        dismiss()
    }
}

/// A picker sheet for choosing a new agent to migrate a session to.
struct MigrateSheet: View {
    let sessionName: String
    let agents: [String]
    let currentAgent: String
    let onConfirm: (String) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var selectedAgent: String

    init(sessionName: String, agents: [String], currentAgent: String, onConfirm: @escaping (String) -> Void) {
        self.sessionName = sessionName
        self.agents = agents
        self.currentAgent = currentAgent
        self.onConfirm = onConfirm
        _selectedAgent = State(initialValue: currentAgent)
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Swap \u{201c}\(sessionName)\u{201d} to a different agent") {
                    Picker("Agent", selection: $selectedAgent) {
                        ForEach(agents, id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.inline)
                    .labelsHidden()
                }
            }
            .navigationTitle("Migrate Session")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Migrate") {
                        onConfirm(selectedAgent)
                        dismiss()
                    }
                    .disabled(selectedAgent == currentAgent)
                }
            }
        }
    }
}

struct StatusDot: View {
    let session: SessionInfo
    var body: some View {
        Circle()
            .fill(color)
            .frame(width: 9, height: 9)
    }
    private var color: Color {
        if session.isErrored { return .red }
        if session.needsApproval { return .orange }
        if session.isStopped { return .gray }
        if session.agentStatus == "active" { return .green }
        return .blue
    }
}

struct ConnectionDot: View {
    let state: HostConnection.ConnectionState
    var body: some View {
        Circle().fill(color).frame(width: 8, height: 8)
    }
    private var color: Color {
        switch state {
        case .connected: return .green
        case .connecting: return .yellow
        case .failed: return .red
        case .idle: return .gray
        }
    }
}

struct PRBadge: View {
    let pr: PRInfo
    var body: some View {
        HStack(spacing: 2) {
            Image(systemName: "arrow.triangle.pull")
            Text("#\(pr.number)")
        }
        .font(.caption2)
        .foregroundStyle(color)
    }
    private var color: Color {
        switch pr.state {
        case "merged": return .purple
        case "closed": return .red
        case "draft": return .secondary
        default: return pr.conflicting == true ? .orange : .blue
        }
    }
}

struct CIBadge: View {
    let ci: CIInfo
    var body: some View {
        Image(systemName: icon).font(.caption2).foregroundStyle(color)
    }
    private var icon: String {
        switch ci.state {
        case "passing": return "checkmark.circle.fill"
        case "failing": return "xmark.circle.fill"
        default: return "clock.fill"
        }
    }
    private var color: Color {
        switch ci.state {
        case "passing": return .green
        case "failing": return .red
        default: return .yellow
        }
    }
}
