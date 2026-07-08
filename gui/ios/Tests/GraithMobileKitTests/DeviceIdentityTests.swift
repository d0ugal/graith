import XCTest
import CryptoKit
@testable import GraithMobileKit
import GraithMobileMock

final class DeviceIdentityTests: XCTestCase {
    func testKeyIsStableAcrossInstances() throws {
        let secrets = InMemorySecretStore()
        let first = try DeviceIdentity(keychain: secrets)
        let pub1 = try first.publicKeyRaw()

        // A second identity over the same store must load the same key.
        let second = try DeviceIdentity(keychain: secrets)
        let pub2 = try second.publicKeyRaw()
        XCTAssertEqual(pub1, pub2)
        XCTAssertEqual(pub1.count, 32, "raw ed25519 public key is 32 bytes")
    }

    func testSignatureVerifiesWithPublicKey() throws {
        let identity = try DeviceIdentity(keychain: InMemorySecretStore())
        let nonce = Data("haar-nonce".utf8)
        let sig = try identity.sign(nonce)
        XCTAssertEqual(sig.count, 64, "raw ed25519 signature is 64 bytes")

        let pub = try Curve25519.Signing.PublicKey(rawRepresentation: try identity.publicKeyRaw())
        XCTAssertTrue(pub.isValidSignature(sig, for: nonce))
        XCTAssertFalse(pub.isValidSignature(sig, for: Data("thrawn".utf8)))
    }

    func testDeviceIDPersistsAndResetClears() throws {
        let secrets = InMemorySecretStore()
        let identity = try DeviceIdentity(keychain: secrets)
        XCTAssertFalse(identity.isPaired)

        try identity.setDeviceID("dev-skelf-1")
        XCTAssertTrue(identity.isPaired)
        XCTAssertEqual(try DeviceIdentity(keychain: secrets).deviceID, "dev-skelf-1")

        try identity.reset()
        XCTAssertFalse(identity.isPaired)
        // A new key is generated after reset (different from before).
        let fresh = try DeviceIdentity(keychain: secrets)
        XCTAssertEqual(fresh.deviceID, "")
    }
}

final class PairingCoordinatorTests: XCTestCase {
    @MainActor
    func testSuccessfulPairingCompletes() async throws {
        let secrets = InMemorySecretStore()
        let identity = try DeviceIdentity(keychain: secrets)
        let registry = HostRegistry(keychain: secrets, storeURL:
            FileManager.default.temporaryDirectory
                .appendingPathComponent("pair-\(UUID().uuidString)/hosts.json"))
        let coordinator = PairingCoordinator(pairing: MockPairing(), identity: identity, registry: registry)

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "graith-ben.ts.net", deviceLabel: "bairn phone")

        // The daemon replied, but nothing is trusted until the user confirms the
        // fingerprint: phase is awaitingConfirmation and the registry host is
        // NOT yet marked paired (no token persisted).
        guard case .awaitingConfirmation(let pending) = coordinator.phase else {
            return XCTFail("expected awaitingConfirmation, got \(coordinator.phase)")
        }
        XCTAssertNotNil(coordinator.spkiFingerprint)
        XCTAssertEqual(pending.deviceID, "dev-bairn-001")
        XCTAssertNotEqual(registry.host(id: "ben")?.isPaired, true,
                          "must not be marked paired before confirmation")

        // Confirming the fingerprint is the point trust is persisted.
        coordinator.confirmPairing()

        if case .paired(let entry) = coordinator.phase {
            XCTAssertTrue(entry.isPaired)
            XCTAssertEqual(registry.host(id: "ben")?.isPaired, true)
            // The device ID is recorded PER HOST on the entry, not on the shared
            // identity (F4) — pairing must not mutate a global device ID.
            XCTAssertEqual(entry.deviceID, "dev-bairn-001")
            XCTAssertEqual(registry.host(id: "ben")?.deviceID, "dev-bairn-001")
            XCTAssertNotNil(coordinator.spkiFingerprint)
        } else {
            XCTFail("expected paired phase, got \(coordinator.phase)")
        }
    }

    @MainActor
    func testRejectingFingerprintDiscardsTrust() async throws {
        let secrets = InMemorySecretStore()
        let identity = try DeviceIdentity(keychain: secrets)
        let registry = HostRegistry(keychain: secrets, storeURL:
            FileManager.default.temporaryDirectory
                .appendingPathComponent("pair-\(UUID().uuidString)/hosts.json"))
        let coordinator = PairingCoordinator(pairing: MockPairing(), identity: identity, registry: registry)

        await coordinator.pair(hostID: "thrawn", label: "thrawn",
                               magicDNSName: "graith-thrawn.ts.net", deviceLabel: "scunner phone")
        guard case .awaitingConfirmation = coordinator.phase else {
            return XCTFail("expected awaitingConfirmation, got \(coordinator.phase)")
        }

        coordinator.rejectPairing()

        // Rejecting must persist nothing and drop the placeholder host entry.
        XCTAssertEqual(coordinator.phase, .idle)
        XCTAssertNil(registry.host(id: "thrawn"), "rejected host must not remain in the registry")
        XCTAssertNil(registry.credentials(for: HostEntry(id: "thrawn", label: "thrawn",
                                                         magicDNSName: "graith-thrawn.ts.net")),
                     "no token should have been written to the Keychain")
    }

    @MainActor
    func testFailedPairingSurfacesError() async throws {
        let secrets = InMemorySecretStore()
        let identity = try DeviceIdentity(keychain: secrets)
        let registry = HostRegistry(keychain: secrets, storeURL:
            FileManager.default.temporaryDirectory
                .appendingPathComponent("pair-\(UUID().uuidString)/hosts.json"))
        let failing = MockPairing(failure: .authenticationFailed("thrawn identity"))
        let coordinator = PairingCoordinator(pairing: failing, identity: identity, registry: registry)

        await coordinator.pair(hostID: "dreich", label: "dreich",
                               magicDNSName: "graith-dreich.ts.net", deviceLabel: "scunner")

        if case .failed(let msg) = coordinator.phase {
            XCTAssertTrue(msg.contains("thrawn identity"))
        } else {
            XCTFail("expected failed phase")
        }
        XCTAssertFalse(identity.isPaired)
    }
}
