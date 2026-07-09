import SwiftUI
import AppKit
import GraithRemoteKit

@main
struct GraithApp: App {
    /// Stable identifier for the main window group, so `openWindow(id:)` from
    /// the "New Window" command spawns another one.
    static let mainWindowID = "graith-main"

    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    /// App-global state shared by every window: one host registry (local +
    /// paired remotes), one aggregated session list, one approvals stream, one
    /// pairing coordinator. Per-window state (selection, split) lives in
    /// `WindowState`, owned by each `ContentView`.
    @StateObject private var store: SessionStore
    @StateObject private var registry: HostRegistry
    @StateObject private var approvals: ApprovalMonitor
    @StateObject private var pairing: PairingCoordinator

    init() {
        // One secret store shared by the device identity and the host token
        // registry. Prefer the Keychain; fall back to an in-memory store when it
        // is unavailable (an ad-hoc-signed `swift run` dev build), so the app
        // still runs with an ephemeral identity instead of trapping.
        let secrets = Self.makeSecretStore()
        // Identity is effectively always creatable (an in-memory store never
        // fails key generation); the fallback keeps the type non-optional so the
        // pairing coordinator always has a signer.
        let identity = (try? DeviceIdentity(keychain: secrets))
            ?? (try! DeviceIdentity(keychain: InMemorySecretStore()))
        let registry = HostRegistry(keychain: secrets, localHost: GraithLocalSocket.localHost())
        let store = SessionStore(registry: registry, identity: identity)

        _store = StateObject(wrappedValue: store)
        _registry = StateObject(wrappedValue: registry)
        _approvals = StateObject(wrappedValue: ApprovalMonitor(store: store))
        _pairing = StateObject(wrappedValue: PairingCoordinator(
            pairing: RealPairing(clientID: "graith-macos"),
            identity: identity,
            registry: registry
        ))
    }

    var body: some Scene {
        WindowGroup(id: Self.mainWindowID) {
            ContentView()
                .environmentObject(store)
                .environmentObject(registry)
                .environmentObject(approvals)
                .environmentObject(pairing)
                .frame(minWidth: 800, minHeight: 500)
        }
        .defaultSize(width: 1200, height: 800)
        .windowStyle(.hiddenTitleBar)
        .commands {
            GraithCommands(store: store)
        }

        Settings {
            SettingsView()
                .environmentObject(store)
                .environmentObject(registry)
                .environmentObject(pairing)
        }
    }

    /// Probe the Keychain with a throwaway write/read/delete; on any failure
    /// (unsigned build, no entitlement) fall back to an in-memory store.
    private static func makeSecretStore() -> SecretStore {
        let keychain = KeychainStore(service: "com.graith.macos")
        let probe = "keychain.probe"
        do {
            try keychain.set(Data([0x01]), for: probe)
            _ = try keychain.data(for: probe)
            try keychain.remove(probe)
            return keychain
        } catch {
            NSLog("GraithApp: Keychain unavailable (\(error)); using in-memory secret store")
            return InMemorySecretStore()
        }
    }
}
