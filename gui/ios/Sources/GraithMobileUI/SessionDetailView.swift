import SwiftUI
import GraithClientAPI
import GraithTerminalUIKit
import GraithMobileRealTerminal
import GraithTerminalCore

/// Session detail: a live interactive terminal (real libghostty render) plus
/// lifecycle actions. The terminal attaches over the host's PTY stream (Task 20).
struct SessionDetailView: View {
    @ObservedObject var connection: HostConnection
    let session: SessionInfo

    var body: some View {
        // Full-bleed content (terminal fills the screen); the lifecycle actions
        // and session metadata live in the hamburger menu, so the terminal isn't
        // squeezed by a big header.
        SessionTerminalPane(connection: connection, session: session)
            .navigationTitle(session.name)
            .compactInlineTitle()
            .toolbar { ToolbarItem(placement: .primaryAction) { sessionMenu } }
    }

    // MARK: - Hamburger menu

    private var sessionMenu: some View {
        Menu {
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
