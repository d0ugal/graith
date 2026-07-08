import SwiftUI
import GraithProtocol

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
    @EnvironmentObject var hosts: HostRegistry

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
                            Text(host.kind == .local
                                 ? "Local daemon · Unix socket"
                                 : "Remote · \(host.magicDNSName ?? "?"):\(host.port ?? 0)")
                                .font(.system(.caption2, design: .monospaced))
                                .foregroundStyle(Theme.overlay0)
                        }
                        Spacer()
                        if host.kind != .local {
                            Button(role: .destructive) {
                                hosts.remove(host)
                            } label: {
                                Image(systemName: "minus.circle")
                            }
                            .buttonStyle(.borderless)
                        }
                    }
                    .padding(.vertical, 2)
                }
            }
            Text("Remote hosts are paired with `gr remote pair` over Tailscale. "
                 + "Keychain-backed pairing from the app is not wired up yet.")
                .font(.caption)
                .foregroundStyle(Theme.overlay0)
                .padding(12)
        }
    }
}
