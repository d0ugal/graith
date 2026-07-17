import SwiftUI
import GraithProtocol
import GraithRemoteKit
import GraithSessionKit

/// The ⌘, preferences window. SwiftUI's `Settings` scene wires up the menu item
/// and a standard preferences window automatically; this is its content. Styled
/// with the shared `Theme` so it matches the Catppuccin dark look of the app.
struct SettingsView: View {
    var body: some View {
        TabView {
            AppearanceSettings()
                .tabItem { Label("Appearance", systemImage: "paintpalette") }
            GeneralSettings()
                .tabItem { Label("General", systemImage: "gearshape") }
            HostsSettings()
                .tabItem { Label("Hosts", systemImage: "server.rack") }
            ConfigSettings()
                .tabItem { Label("Config", systemImage: "doc.text") }
            DiagnosticsSettings()
                .tabItem { Label("Diagnostics", systemImage: "stethoscope") }
            LocalDaemonSettings()
                .tabItem { Label("Advanced", systemImage: "wrench.and.screwdriver") }
        }
        .frame(width: 620, height: 460)
        .preferredColorScheme(.dark)
    }
}

// MARK: - Local daemon

struct LocalDaemonSettings: View {
    @AppStorage(GraithLocalSocket.profileOverrideKey) private var profileOverride = ""
    @AppStorage(GraithLocalSocket.configPathOverrideKey) private var configPathOverride = ""
    @AppStorage(GraithLocalSocket.socketPathOverrideKey) private var socketPathOverride = ""

    private var resolution: GraithLocalSocket.Resolution {
        GraithLocalSocket.resolve(
            profileOverride: profileOverride,
            configPathOverride: configPathOverride,
            socketPathOverride: socketPathOverride
        )
    }

    var body: some View {
        Form {
            Section {
                TextField("Profile override", text: $profileOverride)
                    .textFieldStyle(.roundedBorder)
                if let profileError = resolution.profileError {
                    Label(profileError, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption)
                        .foregroundStyle(Theme.red)
                }
                TextField("Config file override", text: $configPathOverride)
                    .textFieldStyle(.roundedBorder)
                TextField("Socket path override", text: $socketPathOverride)
                    .textFieldStyle(.roundedBorder)
            } header: {
                Text("Local daemon")
            } footer: {
                Text("Leave fields empty to discover them automatically. Tilde-prefixed paths are supported.")
                    .font(.caption)
                    .foregroundStyle(Theme.overlay0)
            }

            Section {
                LabeledContent("Profile") {
                    Text(resolution.profile.isEmpty ? "default" : resolution.profile)
                        .font(.system(.caption, design: .monospaced))
                        .textSelection(.enabled)
                }
                LabeledContent("Config") {
                    Text(resolution.configPath)
                        .font(.system(.caption, design: .monospaced))
                        .textSelection(.enabled)
                        .lineLimit(2)
                }
                LabeledContent("Socket") {
                    Text(resolution.socketPath)
                        .font(.system(.caption, design: .monospaced))
                        .textSelection(.enabled)
                        .lineLimit(2)
                }
            } header: {
                Text("Effective paths")
            } footer: {
                Text("Restart the macOS app after changing an override so the local connection is rebuilt.")
                    .font(.caption)
                    .foregroundStyle(Theme.yellow)
            }
        }
        .formStyle(.grouped)
    }
}

// MARK: - Appearance

struct AppearanceSettings: View {
    @EnvironmentObject var store: SessionStore

    var body: some View {
        Form {
            Section {
                Slider(
                    value: Binding(
                        get: { store.fontSize },
                        set: { store.setFontSize($0) }
                    ),
                    in: Theme.minFontSize...Theme.maxFontSize,
                    step: 1
                ) {
                    Text("Terminal font size")
                } minimumValueLabel: {
                    Text("\(Int(Theme.minFontSize))")
                } maximumValueLabel: {
                    Text("\(Int(Theme.maxFontSize))")
                }
                LabeledContent("Current") {
                    Text("\(Int(store.fontSize)) pt")
                        .font(.system(.body, design: .monospaced))
                        .foregroundStyle(Theme.subtext0)
                }

                Picker("Renderer", selection: Binding(
                    get: { store.renderer },
                    set: { store.renderer = $0 }
                )) {
                    ForEach(SessionStore.RendererType.allCases, id: \.self) { type in
                        Text(type.rawValue).tag(type)
                    }
                }
            }

            Section {
                LabeledContent("Theme") {
                    Text("Catppuccin Mocha (dark)")
                        .font(.system(.body, design: .monospaced))
                        .foregroundStyle(Theme.subtext0)
                }
            } footer: {
                Text("GraithGUI uses a fixed dark terminal theme to match the daemon UI.")
                    .font(.caption)
                    .foregroundStyle(Theme.overlay0)
            }
        }
        .formStyle(.grouped)
    }
}

// MARK: - General

struct GeneralSettings: View {
    @EnvironmentObject var store: SessionStore
    /// Empty means "follow this host's daemon default". Unlike an AppStorage
    /// literal, it cannot mask a non-Claude default on a fresh profile.
    @State private var selectedPreference = ""
    @State private var catalogState: AgentCatalogState = .loading

    var body: some View {
        Form {
            Section {
                switch catalogState {
                case .loading:
                    ProgressView("Loading agent catalog…")
                case let .available(catalog):
                    Picker("Default agent", selection: Binding(
                        get: { selectedPreference },
                        set: {
                            selectedPreference = $0
                            AgentPreference.store($0.isEmpty ? nil : $0)
                        }
                    )) {
                        Text("Daemon default (\(catalog.resolvedDefault))").tag("")
                        ForEach(catalog.names, id: \.self) { Text($0).tag($0) }
                    }
                case let .unavailable(reason):
                    LabeledContent("Default agent") {
                        Text("Managed by daemon")
                            .foregroundStyle(Theme.yellow)
                    }
                    Text("Agent catalog unavailable: \(reason)")
                        .font(.caption)
                        .foregroundStyle(Theme.overlay0)
                }
            } footer: {
                Text("Each host supplies its own catalog and default. A local override is used only when that host offers it.")
                    .font(.caption)
                    .foregroundStyle(Theme.overlay0)
            }
        }
        .formStyle(.grouped)
        .task {
            let loaded = await store.fetchAgentCatalog()
            catalogState = loaded
            // Read-only against the stored preference: a host that doesn't offer
            // the agent shows the daemon-default row without erasing a choice
            // still valid on another host (#1234). Only an explicit pick (the
            // Picker's setter) mutates the persisted preference.
            selectedPreference = AgentPreference.selection(
                explicit: AgentPreference.explicitAgent(),
                catalog: loaded.catalog)
        }
    }
}

// MARK: - Hosts

struct HostsSettings: View {
    @EnvironmentObject var store: SessionStore
    @EnvironmentObject var hosts: HostRegistry
    @State private var showAddHost = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            List {
                ForEach(hosts.hosts) { host in
                    HStack(spacing: 10) {
                        Image(systemName: host.kind == .local ? "desktopcomputer" : "network")
                            .foregroundStyle(host.kind == .local ? Theme.accent : Theme.success)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(host.label)
                                .font(.system(.body, design: .monospaced))
                            Text(subtitle(for: host))
                                .font(.system(.caption2, design: .monospaced))
                                .foregroundStyle(Theme.overlay0)
                        }
                        Spacer()
                        if host.kind != .local {
                            Button(role: .destructive) {
                                Task { await store.removeHost(host) }
                            } label: {
                                Image(systemName: "minus.circle")
                            }
                            .buttonStyle(.borderless)
                            .help("Forget this host (revokes trust on this Mac)")
                        }
                    }
                    .padding(.vertical, 2)
                }
            }
            HStack {
                Button {
                    showAddHost = true
                } label: {
                    Label("Add Host…", systemImage: "plus")
                }
                Spacer()
            }
            .padding(12)
            Text("Remote hosts are reached over Tailscale. Adding one runs a one-time "
                 + "device pairing — approve it on the host with `gr pair approve`.")
                .font(.caption)
                .foregroundStyle(Theme.overlay0)
                .padding(.horizontal, 12)
                .padding(.bottom, 12)
        }
        .sheet(isPresented: $showAddHost) {
            AddHostSheet()
        }
    }

    private func subtitle(for host: Host) -> String {
        if host.kind == .local {
            return "Local daemon · Unix socket"
        }
        let endpoint = "\(host.magicDNSName ?? "?"):\(host.port)"
        if let err = store.hostErrors[host.id] {
            return "Remote · \(endpoint) · \(err)"
        }
        return host.isPaired ? "Remote · \(endpoint)" : "Remote · \(endpoint) · pairing…"
    }
}

// MARK: - Host picker (shared by Config + Diagnostics)

/// A small host selector shown when more than one host is connected. When only
/// the local daemon is present it renders nothing (and the caller uses "local").
private struct HostPicker: View {
    @EnvironmentObject var store: SessionStore
    @Binding var hostID: String

    var body: some View {
        if store.connections.count > 1 {
            Picker("Host", selection: $hostID) {
                ForEach(store.connections) { conn in
                    Text(conn.entry.label).tag(conn.id)
                }
            }
            .pickerStyle(.menu)
        }
    }
}

// MARK: - Config viewer (#904)

/// Read-only viewer for the daemon's effective configuration and how it differs
/// from the built-in defaults. Fetched over the control protocol so it works
/// against a remote host too (the app never reads the daemon host's filesystem).
struct ConfigSettings: View {
    @EnvironmentObject var store: SessionStore
    @State private var hostID = "local"
    @State private var mode: Mode = .effective
    @State private var response: ConfigResponseMsg?
    @State private var error: String?
    @State private var loading = false

    private enum Mode: String, CaseIterable { case effective = "Effective", diff = "Diff vs defaults" }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                HostPicker(hostID: $hostID)
                Picker("", selection: $mode) {
                    ForEach(Mode.allCases, id: \.self) { Text($0.rawValue).tag($0) }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                Spacer()
                Button { Task { await load() } } label: {
                    Label("Reload", systemImage: "arrow.clockwise")
                }
                .disabled(loading)
            }

            if let response, response.configExists == false {
                Label("No config file — the daemon is running on built-in defaults.",
                      systemImage: "info.circle")
                    .font(.caption)
                    .foregroundStyle(Theme.overlay0)
            } else if let path = response?.configPath, !path.isEmpty {
                Text(path)
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
                    .textSelection(.enabled)
                    .lineLimit(1)
            }

            content
        }
        .padding(16)
        .task(id: hostID) { await load() }
    }

    @ViewBuilder
    private var content: some View {
        if let error {
            ContentMessage(systemImage: "exclamationmark.triangle.fill",
                           text: error, tint: Theme.red)
        } else if loading && response == nil {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let response {
            let text = mode == .effective ? response.effectiveTOML : response.diffFromDefaults
            if mode == .diff && text.isEmpty {
                ContentMessage(systemImage: "checkmark.seal",
                               text: "Configuration matches the built-in defaults.",
                               tint: Theme.green)
            } else {
                ScrollView([.vertical, .horizontal]) {
                    Text(text.isEmpty ? "(empty)" : text)
                        .font(.system(.caption, design: .monospaced))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(8)
                }
                .background(Theme.mantle)
                .clipShape(RoundedRectangle(cornerRadius: 6))
            }
        } else {
            Spacer()
        }
    }

    private func load() async {
        loading = true
        defer { loading = false }
        do {
            response = try await store.config(hostID: hostID)
            error = nil
        } catch {
            self.error = FleetModel.describeError(error)
        }
    }
}

// MARK: - Diagnostics panel (#904)

/// The `gr doctor` equivalent: the daemon's health snapshot rendered as findings
/// grouped by section, most severe first. Findings are derived by the shared
/// `HealthReport`, so macOS and iOS surface the same checks.
struct DiagnosticsSettings: View {
    @EnvironmentObject var store: SessionStore
    @State private var hostID = "local"
    @State private var diag: DiagnosticsMsg?
    @State private var error: String?
    @State private var loading = false

    private var findings: [HealthFinding] {
        guard let diag else { return [] }
        return HealthReport.findings(from: diag).sorted { ($0.level, $0.section) < ($1.level, $1.section) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                HostPicker(hostID: $hostID)
                if let diag {
                    SummaryBadge(healthy: !HealthReport.hasFailures(findings),
                                 fleet: diag.fleet)
                }
                Spacer()
                Button { Task { await load() } } label: {
                    Label("Reload", systemImage: "arrow.clockwise")
                }
                .disabled(loading)
            }

            if let error {
                ContentMessage(systemImage: "exclamationmark.triangle.fill",
                               text: error, tint: Theme.red)
            } else if loading && diag == nil {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                List {
                    ForEach(sections, id: \.self) { section in
                        Section(section) {
                            ForEach(findings.filter { $0.section == section }) { finding in
                                FindingRow(finding: finding)
                            }
                        }
                    }
                }
                .listStyle(.inset)

                Text("Covers daemon, session and storage checks. Run `gr doctor` for host-level checks (sandbox, config keys, disk).")
                    .font(.caption2)
                    .foregroundStyle(Theme.overlay0)
            }
        }
        .padding(16)
        .task(id: hostID) { await load() }
    }

    private var sections: [String] {
        var seen = Set<String>()
        return findings.compactMap { seen.insert($0.section).inserted ? $0.section : nil }
    }

    private func load() async {
        loading = true
        defer { loading = false }
        do {
            diag = try await store.diagnostics(hostID: hostID)
            error = nil
        } catch {
            self.error = FleetModel.describeError(error)
        }
    }
}

private struct SummaryBadge: View {
    let healthy: Bool
    let fleet: FleetSummary

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: healthy ? "checkmark.circle.fill" : "xmark.octagon.fill")
                .foregroundStyle(healthy ? Theme.green : Theme.red)
            Text(healthy ? "No daemon/session issues" : "Issues found")
                .font(.caption)
            Text("· \(fleet.total) session(s)")
                .font(.caption)
                .foregroundStyle(Theme.overlay0)
        }
    }
}

private struct FindingRow: View {
    let finding: HealthFinding

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: icon)
                .foregroundStyle(tint)
            VStack(alignment: .leading, spacing: 2) {
                Text(finding.message)
                    .font(.system(.caption, design: .monospaced))
                    .textSelection(.enabled)
                if let hint = finding.hint {
                    Text("→ \(hint)")
                        .font(.caption2)
                        .foregroundStyle(Theme.overlay0)
                }
            }
        }
        .padding(.vertical, 1)
    }

    private var icon: String {
        switch finding.level {
        case .fail: "xmark.circle.fill"
        case .warn: "exclamationmark.triangle.fill"
        case .ok: "checkmark.circle"
        }
    }

    private var tint: Color {
        switch finding.level {
        case .fail: Theme.red
        case .warn: Theme.yellow
        case .ok: Theme.green
        }
    }
}

/// A centered icon + message used for empty/error/clean states.
private struct ContentMessage: View {
    let systemImage: String
    let text: String
    let tint: Color

    var body: some View {
        VStack(spacing: 8) {
            Image(systemName: systemImage)
                .font(.title)
                .foregroundStyle(tint)
            Text(text)
                .font(.caption)
                .foregroundStyle(Theme.subtext0)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
