import SwiftUI
import Foundation
import GraithClientAPI
import GraithMobileKit
import GraithMobileUI
// The client factory + pairing are now the REAL adapters (GraithMobileReal) onto
// ../shared's GraithProtocolClient, so the app connects to a live daemon over the
// tailnet and drives real sessions. Device identity, the host registry, tailnet
// reachability and the Keychain store are the real GraithMobileKit
// implementations. The mocks (GraithMobileMock) remain available for
// tests/previews. The live libghostty terminal (GhosttyTerminalDriver /
// GhosttyMetalRenderer) is wired into SessionDetailView's Terminal tab and links
// the pinned libghostty-vt.xcframework — Task 13 is done and renders on the sim.
import GraithMobileReal
// InMemorySecretStore fallback for the unsigned dev bundle (no Keychain entitlement).
import GraithMobileMock

/// The iOS/universal app entry point (#628). Builds the real `AppModel` and
/// presents the shared `RootView`.
@main
struct GraithMobileApp: App {
    @StateObject private var model = GraithMobileApp.makeModel()
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            RootView(model: model)
                // Re-dial when returning to the foreground: a connection that
                // was live when we backgrounded may have been torn down by the
                // OS, and the client would otherwise keep reusing the dead one.
                .onChange(of: scenePhase) { phase in
                    guard phase == .active else { return }
                    Task { await model.reconnectAll() }
                }
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
        do {
            let identity = try DeviceIdentity(keychain: keychain)
            return (keychain, identity)
        } catch {
            #if targetEnvironment(simulator) || DEBUG
            // Dev/simulator ergonomics only: the unsigned simulator bundle can't
            // reach the Keychain (errSecMissingEntitlement -34018), so fall back
            // to an in-memory store. Identity is ephemeral in that build.
            NSLog("graith: Keychain unavailable (\(error)); using ephemeral in-memory identity — DEBUG/simulator only")
            let memory = InMemorySecretStore()
            guard let identity = try? DeviceIdentity(keychain: memory) else {
                fatalError("graith: could not initialise device identity: \(error)")
            }
            return (memory, identity)
            #else
            // Release/device build: a Keychain failure must NOT silently degrade
            // to an ephemeral in-memory key — that would mint a fresh device
            // identity and lose the paired tokens on every launch. Surface it
            // loudly instead of connecting under a throwaway identity.
            fatalError("graith: Keychain unavailable in a signed build; device identity cannot be persisted: \(error)")
            #endif
        }
    }
}
