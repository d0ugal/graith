import SwiftUI
import GraithSessionKit
import GraithDesign

/// The aggregated multi-host sidebar: host → repo → session tree (design §C.4).
/// Session IDs are per-daemon, so selection is namespaced by `SessionRef`.
struct SessionSidebar: View {
    @ObservedObject var model: FleetModel

    var body: some View {
        List(selection: $model.selection) {
            // View-mode + quick filters (#906). Search is the navigation-bar
            // search field below; the mode/starred/repo controls live inline so
            // they're reachable without a toolbar overflow menu.
            SidebarFilterControls(model: model)

            ScenarioSidebarSection(model: model)
            ForEach(model.connections) { conn in
                HostSection(connection: conn, criteria: model.filterCriteria)
            }
        }
        .scrollContentBackground(.hidden)
        .background(GraithDesign.sidebarBackground)
        .font(GraithDesign.mono(.callout))
        .searchable(text: $model.searchQuery, placement: .automatic, prompt: "Filter sessions")
        .refreshable {
            await model.connectAll()
        }
    }
}

/// Inline view-mode segmented picker plus starred and repo quick filters (#906).
/// All state lives on the shared `FleetModel`.
private struct SidebarFilterControls: View {
    @ObservedObject var model: FleetModel

    var body: some View {
        Section {
            Picker("View", selection: $model.viewMode) {
                ForEach(SidebarViewMode.allCases) { mode in
                    Text(mode.displayName).tag(mode)
                }
            }
            .pickerStyle(.segmented)
            .listRowBackground(Color.clear)

            HStack {
                Toggle(isOn: $model.starredOnly) {
                    Label("Starred only", systemImage: "star")
                }
                .toggleStyle(.button)
                .buttonStyle(.bordered)
                .tint(model.starredOnly ? .yellow : .secondary)

                Spacer()

                Menu {
                    Button("All repos") { model.repoFilter = nil }
                    Divider()
                    ForEach(model.availableRepos, id: \.self) { repo in
                        Button {
                            model.repoFilter = repo
                        } label: {
                            if model.repoFilter == repo {
                                Label(repo, systemImage: "checkmark")
                            } else {
                                Text(repo)
                            }
                        }
                    }
                } label: {
                    Label(model.repoFilter ?? "All repos", systemImage: "folder")
                        .lineLimit(1)
                }
                .disabled(model.availableRepos.isEmpty)

                if model.isFilterActive {
                    Button {
                        model.clearFilters()
                    } label: {
                        Label("Clear", systemImage: "xmark.circle")
                    }
                    .tint(.secondary)
                }
            }
            .font(.footnote)
            .listRowBackground(Color.clear)
        }
    }
}

/// One host's section: connection state header + repo groups.
private struct HostSection: View {
    @ObservedObject var connection: HostConnection
    let criteria: SidebarFilter.Criteria

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
                // Only say "No sessions match" when the host actually has
                // sessions the filter hid — a genuinely empty host renders its
                // existing empty section rather than a misleading filter hint.
                if repoGroups.isEmpty && criteria.isActive && !connection.sessions.isEmpty {
                    Label("No sessions match", systemImage: "line.3.horizontal.decrease.circle")
                        .foregroundStyle(.secondary)
                        .font(.footnote)
                } else {
                    ForEach(repoGroups, id: \.repo) { group in
                        RepoGroup(repo: group.repo, sessions: group.sessions, connection: connection)
                    }
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
        let filtered = SidebarFilter.apply(connection.sessions, criteria)
        let grouped = Dictionary(grouping: filtered) { $0.repoName.isEmpty ? "—" : $0.repoName }
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
    @State private var showSetStatus = false
    @State private var statusText = ""
    @State private var showDeleteConfirm = false
    @State private var showPurgeConfirm = false

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
                    // The daemon keeps the last-known CI after a PR merges/closes
                    // (it stops polling), so suppress the badge for those terminal
                    // states to avoid showing a stale result (shared with macOS via
                    // GraithSessionKit's shouldShowCI, #1173).
                    if let ci = session.ci, shouldShowCI(pr: session.pullRequest, ci: ci) {
                        CIBadge(ci: ci)
                    }
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
        .confirmationDialog(
            "Permanently delete \u{201c}\(session.name)\u{201d}?",
            isPresented: $showPurgeConfirm,
            titleVisibility: .visible
        ) {
            Button("Delete Permanently", role: .destructive) { Task { await connection.purge(session) } }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This bypasses the recovery window — the agent is stopped and the worktree, branch, and history are removed immediately. This cannot be undone.")
        }
        .sheet(isPresented: $showSetStatus) {
            SessionTextPromptSheet(
                title: "Set Status", fieldLabel: "Status summary",
                placeholder: "what this session is doing…", confirmLabel: "Set", text: $statusText
            ) { text in Task { await connection.setStatus(session, text: text) } }
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
        Button { statusText = session.summaryText ?? ""; showSetStatus = true } label: {
            Label("Set Status…", systemImage: "text.bubble")
        }
        if let summary = session.summaryText, !summary.isEmpty {
            Button { Task { await connection.setStatus(session, text: "", clear: true) } } label: {
                Label("Clear Status", systemImage: "text.badge.xmark")
            }
        }
        Divider()
        Button(role: .destructive) { requestDelete() } label: { Label("Delete", systemImage: "trash") }
        Button(role: .destructive) { showPurgeConfirm = true } label: { Label("Delete Permanently…", systemImage: "trash.slash") }
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
        default: break
        }
        // A merge conflict is the actionable signal — surface it even on a
        // draft (matches the terminal overlay and the macOS badge).
        if pr.conflicting == true { return .orange }
        return pr.state == "draft" ? .secondary : .blue
    }
}

/// Compact CI status badge: a coloured glyph plus the passed/total check count
/// ("16/22") while CI runs/fails, falling back to the bare glyph when no count
/// is available; passing shows only the ✓ (#1173). Style/count logic is shared
/// with macOS via `GraithSessionKit` (`ciBadgeStyle`, `CIInfo.badgeCountText`).
struct CIBadge: View {
    let ci: CIInfo
    var body: some View {
        HStack(spacing: 2) {
            Image(systemName: icon)
            if let count = ci.badgeCountText {
                Text(count).monospaced()
            }
        }
        .font(.caption2)
        .foregroundStyle(color)
    }
    private var icon: String {
        switch ciBadgeStyle(for: ci) {
        case .passing: return "checkmark.circle.fill"
        case .failing: return "xmark.circle.fill"
        case .pending: return "clock.fill"
        }
    }
    private var color: Color {
        switch ciBadgeStyle(for: ci) {
        case .passing: return .green
        case .failing: return .red
        case .pending: return .yellow
        }
    }
}
