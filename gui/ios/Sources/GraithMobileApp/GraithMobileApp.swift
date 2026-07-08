import SwiftUI
import GraithClientAPI
import GraithMobileKit
import GraithMobileUI
// The client factory + pairing are now the REAL adapters (GraithMobileReal) onto
// ../shared's GraithProtocolClient, so the app connects to a live daemon over the
// tailnet and drives real sessions. Device identity, the host registry, tailnet
// reachability and the Keychain store are the real GraithMobileKit
// implementations. The mocks (GraithMobileMock) remain available for
// tests/previews. The live terminal renderer (GhosttyTerminalDriver /
// GraithMobileRealTerminal) is compiled + type-checked here but not yet wired
// into the app — it links libghostty-vt, which is Task 13 (pinned .xcframework).
// See NEEDS-IOS-VALIDATION.md.
import GraithMobileReal
// InMemorySecretStore fallback for the unsigned dev bundle (no Keychain entitlement).
import GraithMobileMock

/// The iOS/universal app entry point (#628). Builds the real `AppModel` and
/// presents the shared `RootView`.
@main
struct GraithMobileApp: App {
    @StateObject private var model = GraithMobileApp.makeModel()

    var body: some Scene {
        WindowGroup {
            RootView(model: model)
        }
    }

    private static func makeModel() -> AppModel {
        let (store, identity) = makeIdentity()
        return AppModel(
            registry: HostRegistry(keychain: store),
            identity: identity,
            reachability: TailnetReachability(),
            factory: RealHostClientFactory(),
            pairingBackend: RealPairing()
        )
    }

    /// Device identity backed by the real Keychain when the app is signed with
    /// the keychain entitlement (Xcode / distribution builds — see
    /// Resources/GraithMobile.entitlements). The unsigned simulator bundle that
    /// build-ios-app.sh assembles can't reach the Keychain
    /// (errSecMissingEntitlement -34018), so fall back to an in-memory store —
    /// identity is ephemeral in that dev/demo build but the app runs.
    private static func makeIdentity() -> (SecretStore, DeviceIdentity) {
        let keychain = KeychainStore()
        if let identity = try? DeviceIdentity(keychain: keychain) {
            return (keychain, identity)
        }
        let memory = InMemorySecretStore()
        guard let identity = try? DeviceIdentity(keychain: memory) else {
            fatalError("graith: could not initialise device identity")
        }
        return (memory, identity)
    }
}
