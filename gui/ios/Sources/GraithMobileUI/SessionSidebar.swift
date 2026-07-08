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
                    RepoGroup(repo: group.repo, sessions: group.sessions, hostID: connection.id)
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
    let hostID: String

    var body: some View {
        DisclosureGroup {
            ForEach(sessions) { session in
                SessionRow(session: session)
                    .tag(SessionRef(hostID: hostID, sessionID: session.id))
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
struct SessionRow: View {
    let session: SessionInfo

    var body: some View {
        HStack(spacing: 8) {
            StatusDot(session: session)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(session.name).font(.body)
                    if session.starred == true { Image(systemName: "star.fill").font(.caption2).foregroundStyle(.yellow) }
                    if session.isYolo { Image(systemName: "bolt.fill").font(.caption2).foregroundStyle(.orange) }
                    if session.sandboxed == true { Image(systemName: "shield.lefthalf.filled").font(.caption2).foregroundStyle(.secondary) }
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
