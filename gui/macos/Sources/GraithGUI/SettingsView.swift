import SwiftUI
import GraithProtocol
import GraithRemoteKit

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
        }
        .frame(width: 460, height: 320)
        .preferredColorScheme(.dark)
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
    /// Default agent pre-selected in the New Session sheet.
    @AppStorage("defaultAgent") private var defaultAgent = "claude"

    private let agents = ["claude", "codex", "agy", "opencode"]

    var body: some View {
        Form {
            Section {
                Picker("Default agent", selection: $defaultAgent) {
                    ForEach(agents, id: \.self) { Text($0).tag($0) }
                }
            } footer: {
                Text("New sessions start with this agent selected.")
                    .font(.caption)
                    .foregroundStyle(Theme.overlay0)
            }
        }
        .formStyle(.grouped)
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
