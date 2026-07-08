import SwiftUI
import GraithClientAPI

/// Read-only session detail for the Task 19 milestone: metadata, lifecycle
/// actions, a non-attaching screen peek, and a log tail. No PTY attach here —
/// that arrives in Task 20 and must not kick a desktop attach (design §C.6).
struct SessionDetailView: View {
    @ObservedObject var connection: HostConnection
    let session: SessionInfo

    @State private var snapshot: ScreenSnapshot?
    @State private var logText: String = ""
    @State private var mode: Mode = .peek
    @State private var loading = false

    enum Mode: String, CaseIterable, Identifiable {
        case peek = "Peek"
        case logs = "Logs"
        var id: String { rawValue }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider()
            Picker("View", selection: $mode) {
                ForEach(Mode.allCases) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)
            .padding(8)

            content
        }
        .navigationTitle(session.name)
        .toolbar { actionsToolbar }
        .task(id: refreshKey) { await reload() }
    }

    private var refreshKey: String { "\(session.id)/\(mode.rawValue)" }

    // MARK: - Header

    private var header: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                StatusDot(session: session)
                Text(session.status.capitalized).font(.subheadline.weight(.semibold))
                if let agentStatus = session.agentStatus {
                    Text("· \(agentStatus)").font(.subheadline).foregroundStyle(.secondary)
                }
                Spacer()
                Text(connection.entry.label).font(.caption).foregroundStyle(.secondary)
            }
            Text(session.branch).font(.caption).foregroundStyle(.secondary).lineLimit(1)
            HStack(spacing: 10) {
                Label(session.agent, systemImage: "cpu")
                if let model = session.model { Text(model) }
                if let pr = session.pullRequest { PRBadge(pr: pr) }
                if let ci = session.ci { CIBadge(ci: ci) }
            }
            .font(.caption)
            .foregroundStyle(.secondary)
            if session.needsApproval {
                Label("Waiting for approval", systemImage: "bell.badge")
                    .font(.caption).foregroundStyle(.orange)
            }
        }
        .padding(12)
    }

    // MARK: - Content

    @ViewBuilder
    private var content: some View {
        ScrollView {
            if loading {
                ProgressView().padding()
            } else {
                switch mode {
                case .peek:
                    TerminalTextView(text: snapshot?.frame ?? "(no screen)")
                case .logs:
                    TerminalTextView(text: logText.isEmpty ? "(no logs)" : logText)
                }
            }
        }
        .refreshable { await reload() }
    }

    // MARK: - Toolbar actions

    @ToolbarContentBuilder
    private var actionsToolbar: some ToolbarContent {
        ToolbarItemGroup {
            if session.isRunning {
                Button { Task { await connection.interrupt(session) } } label: {
                    Label("Interrupt", systemImage: "stop.circle")
                }
                Button { Task { await connection.stop(session) } } label: {
                    Label("Stop", systemImage: "pause.circle")
                }
            } else {
                Button { Task { await connection.resume(session) } } label: {
                    Label("Resume", systemImage: "play.circle")
                }
            }
            Button { Task { await connection.restart(session) } } label: {
                Label("Restart", systemImage: "arrow.clockwise")
            }
        }
    }

    // MARK: - Loading

    private func reload() async {
        loading = true
        defer { loading = false }
        switch mode {
        case .peek:
            snapshot = await connection.screenSnapshot(session)
        case .logs:
            logText = await connection.logs(session)
        }
    }
}

/// A monospaced, selectable text block used for peeks and logs.
struct TerminalTextView: View {
    let text: String
    var body: some View {
        Text(text)
            .font(.system(.footnote, design: .monospaced))
            .textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(12)
    }
}
