import SwiftUI
import GraithClientAPI
import GraithTerminalUIKit
import GraithMobileRealTerminal
import GraithTerminalCore

/// Session detail: a live interactive terminal (real libghostty render), plus a
/// non-attaching screen peek and a log tail, and lifecycle actions. The terminal
/// tab attaches over the host's PTY stream (Task 20); peek/logs don't attach
/// (design §C.6).
struct SessionDetailView: View {
    @ObservedObject var connection: HostConnection
    let session: SessionInfo

    @State private var snapshot: ScreenSnapshot?
    @State private var logText: String = ""
    @State private var mode: Mode = .terminal
    @State private var loading = false

    enum Mode: String, CaseIterable, Identifiable {
        case terminal = "Terminal"
        case peek = "Peek"
        case logs = "Logs"
        var id: String { rawValue }
    }

    var body: some View {
        // Full-bleed content (terminal fills the screen); everything else — the
        // view switcher, lifecycle actions, and session metadata — lives in the
        // hamburger menu, so the terminal isn't squeezed by a big header.
        content
            .navigationTitle(session.name)
            .compactInlineTitle()
            .toolbar { ToolbarItem(placement: .primaryAction) { sessionMenu } }
            .task(id: refreshKey) { await reload() }
    }

    private var refreshKey: String { "\(session.id)/\(mode.rawValue)" }

    // MARK: - Hamburger menu

    private var sessionMenu: some View {
        Menu {
            Picker("View", selection: $mode) {
                ForEach(Mode.allCases) { Text($0.rawValue).tag($0) }
            }
            Divider()
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
            Divider()
            Section {
                Label(session.status.capitalized, systemImage: "circle.fill")
                Label(session.agent, systemImage: "cpu")
                Label(connection.entry.label, systemImage: "server.rack")
                if session.needsApproval {
                    Label("Waiting for approval", systemImage: "bell.badge")
                }
            } header: {
                Text(session.branch)
            }
        } label: {
            Image(systemName: "line.3.horizontal")
        }
    }

    // MARK: - Content

    @ViewBuilder
    private var content: some View {
        switch mode {
        case .terminal:
            SessionTerminalPane(connection: connection, session: session)
        case .peek, .logs:
            ScrollView {
                if loading {
                    ProgressView().padding()
                } else if mode == .peek {
                    TerminalTextView(text: snapshot?.frame ?? "(no screen)")
                } else {
                    TerminalTextView(text: logText.isEmpty ? "(no logs)" : logText)
                }
            }
            .refreshable { await reload() }
        }
    }


    // MARK: - Loading

    private func reload() async {
        guard mode != .terminal else { return } // the terminal pane self-manages its attach
        loading = true
        defer { loading = false }
        switch mode {
        case .peek:
            snapshot = await connection.screenSnapshot(session)
        case .logs:
            logText = await connection.logs(session)
        case .terminal:
            break
        }
    }
}

/// The live interactive terminal: a real libghostty render surface (Metal) via
/// `GhosttyMetalRenderer`, driven by `GhosttyTerminalDriver` over the host's
/// attach stream. The app hands one `GhosttyTerminalState` to both the driver
/// (writes PTY output) and the renderer (reads it each frame).
struct SessionTerminalPane: View {
    @StateObject private var viewModel: TerminalAttachViewModel
    private let state: GhosttyTerminalState

    init(connection: HostConnection, session: SessionInfo) {
        let st = GhosttyTerminalState(cols: 80, rows: 24)
        let vm = TerminalAttachViewModel(
            hostID: connection.id,
            sessionID: session.id,
            core: GhosttyTerminalDriver(core: st),
            client: connection.hostClient,
            registry: .shared
        )
        _viewModel = StateObject(wrappedValue: vm)
        self.state = st
    }

    var body: some View {
        #if canImport(UIKit)
        TerminalAttachView(viewModel: viewModel, makeRenderer: { GhosttyMetalRenderer(state: state) })
        #else
        TerminalAttachView(viewModel: viewModel)
        #endif
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
