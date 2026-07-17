import SwiftUI
import Foundation
import GraithSessionKit
import GraithRemoteKit
import GraithMobileUI
// The session/feature layer, real client factory + pairing, host registry,
// device identity, tailnet reachability, and Keychain store all come from
// ../shared now (GraithSessionKit + GraithRemoteKit) — the iOS-local
// GraithClientAPI / GraithMobileKit / GraithMobileReal targets were folded away
// in #1131. The mocks (GraithMobileMock) remain available for tests/previews.
// The live libghostty terminal (GhosttyTerminalDriver / GhosttyMetalRenderer) is
// wired into SessionDetailView's Terminal tab and links the pinned
// libghostty-vt.xcframework. InMemorySecretStore is the fallback for the
// unsigned dev bundle (no Keychain entitlement).
import GraithMobileMock

/// The iOS/universal app entry point (#628). Builds the real `FleetModel` and
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

    private static func makeModel() -> FleetModel {
        let (store, identity) = makeIdentity()
        // Remote-only registry: iOS has no local daemon (the shared registry
        // seeds a local host only when one is supplied).
        let registry = HostRegistry(keychain: store)
        let pairing = PairingCoordinator(
            pairing: RealPairing(clientID: "graith-ios"),
            identity: identity,
            registry: registry
        )
        // Consume the user's persisted presentation preferences (#1254): the
        // production UserDefaults drives the tailnet-probe timeout here, and the
        // terminal font size in SessionTerminalPane.
        let preferences = PresentationPreferences(userDefaults: .standard)
        return FleetModel(
            registry: registry,
            identity: identity,
            reachability: TailnetReachability(),
            factory: RealHostClientFactory(clientID: "graith-ios"),
            pairing: pairing,
            reachabilityProbeTimeout: preferences.reachabilityProbeTimeout
        )
    }

    /// Device identity backed by the real Keychain when the app is signed with
    /// the keychain entitlement (Xcode / distribution builds — see
    /// Resources/GraithMobile.entitlements). The unsigned simulator bundle that
    /// build-ios-app.sh assembles can't reach the Keychain
    /// (errSecMissingEntitlement -34018), so fall back to an in-memory store —
    /// identity is ephemeral in that dev/demo build but the app runs.
    private static func makeIdentity() -> (SecretStore, DeviceIdentity) {
        // Preserve iOS's Keychain namespace (the shared default is
        // "com.graith.app"; iOS items were stored under "com.graith.mobile").
        let keychain = KeychainStore(service: "com.graith.mobile")
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
