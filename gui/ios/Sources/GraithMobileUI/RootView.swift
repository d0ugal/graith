import SwiftUI
import GraithSessionKit
import GraithRemoteKit
import GraithDesign

/// The app's top-level universal shell: a `NavigationSplitView` with the
/// multi-host session sidebar and a session detail pane, plus the
/// not-connected-to-tailnet banner, an approvals entry point, and add-host.
public struct RootView: View {
    @ObservedObject var model: FleetModel
    @State private var showingAddHost = false
    @State private var showingApprovals = false
    @State private var showingNewSession = false
    @State private var showingDeleted = false
    @State private var showingScenarios = false
    @State private var showingStore = false
    @State private var showingInspector = false

    public init(model: FleetModel) {
        self.model = model
    }

    public var body: some View {
        NavigationSplitView {
            sidebar
        } detail: {
            detail
        }
        // Match the desktop: fixed dark, Catppuccin, accent-tinted controls.
        .preferredColorScheme(.dark)
        .tint(GraithDesign.accent)
        .task {
            model.reachability?.start()
            await model.connectAll()
        }
        .sheet(isPresented: $showingAddHost) {
            PairingView(model: model)
        }
        .sheet(isPresented: $showingApprovals) {
            ApprovalsView(model: model)
        }
        .sheet(isPresented: $showingNewSession) {
            NewSessionView(model: model)
        }
        .sheet(isPresented: $showingDeleted) {
            DeletedSessionsView(model: model)
        }
        .sheet(isPresented: $showingScenarios) {
            ScenariosView(model: model)
        }
        .sheet(isPresented: $showingStore) {
            StoreBrowserView(model: model)
        }
        .sheet(isPresented: $showingInspector) {
            HostInspectorView(model: model)
        }
    }

    // MARK: - Sidebar

    private var sidebar: some View {
        VStack(spacing: 0) {
            if let reachState = model.reachability?.state, reachState != .onTailnet {
                TailnetBanner(state: reachState)
            }
            SessionSidebar(model: model)
        }
        .background(GraithDesign.sidebarBackground)
        .toolbar {
            ToolbarItem(placement: .principal) {
                GraithWordmark(size: 15)
            }
            ToolbarItemGroup {
                Button {
                    showingApprovals = true
                } label: {
                    Label("Approvals", systemImage: "checkmark.shield")
                }
                .badgeCompat(model.totalPendingApprovals)

                Button {
                    showingNewSession = true
                } label: {
                    Label("New Session", systemImage: "plus")
                }
                .disabled(model.connections.isEmpty)

                Button {
                    showingAddHost = true
                } label: {
                    Label("Add Host", systemImage: "externaldrive.badge.plus")
                }

                Button {
                    showingScenarios = true
                } label: {
                    Label("Scenarios", systemImage: "square.stack.3d.up")
                }
                .badgeCompat(model.hostedScenarios.count)
                .disabled(model.connections.isEmpty)

                Button {
                    showingDeleted = true
                } label: {
                    Label("Recently Deleted", systemImage: "trash")
                }
                .disabled(model.connections.isEmpty)

                Button {
                    showingStore = true
                } label: {
                    Label("Document Store", systemImage: "doc.on.doc")
                }
                .disabled(model.connections.isEmpty)

                Button {
                    showingInspector = true
                } label: {
                    Label("Host Diagnostics", systemImage: "stethoscope")
                }
                .disabled(model.connections.isEmpty)
            }
        }
    }

    // MARK: - Detail

    private var detail: some View {
        detailContent
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(GraithDesign.background)
    }

    @ViewBuilder
    private var detailContent: some View {
        if let ref = model.selection,
           let conn = model.connection(for: ref),
           let session = conn.sessions.first(where: { $0.id == ref.sessionID }) {
            SessionDetailView(connection: conn, session: session)
                .id(ref.id)
        } else if model.connections.isEmpty {
            EmptyHostsView(addHost: { showingAddHost = true })
        } else {
            ContentUnavailableCompat(
                title: "No session selected",
                systemImage: "sidebar.left",
                description: "Pick a session from the sidebar."
            )
        }
    }
}

/// Banner shown when the device isn't confirmed on the tailnet (design §C.5).
struct TailnetBanner: View {
    let state: TailnetReachability.State

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: icon)
                .foregroundStyle(GraithDesign.warning)
            Text(message)
                .font(GraithDesign.mono(.footnote))
                .foregroundStyle(GraithDesign.foreground)
            Spacer()
        }
        .padding(8)
        .frame(maxWidth: .infinity)
        .background(GraithDesign.warning.opacity(0.15))
    }

    private var icon: String {
        switch state {
        case .offline: return "wifi.slash"
        default: return "network.slash"
        }
    }

    private var message: String {
        switch state {
        case .offline: return "No network connection."
        case .notOnTailnet: return "Not connected to the tailnet — open the Tailscale app to connect."
        case .unknown: return "Checking tailnet connection…"
        case .onTailnet: return "Connected."
        }
    }
}

/// Shown in the detail pane when no hosts are paired yet.
struct EmptyHostsView: View {
    let addHost: () -> Void

    var body: some View {
        GraithEmptyState(
            systemImage: "externaldrive.badge.plus",
            title: "No daemons paired",
            subtitle: "Pair a graith daemon on your tailnet to see and drive its sessions.",
            actionTitle: "Add Host",
            action: addHost
        )
    }
}
