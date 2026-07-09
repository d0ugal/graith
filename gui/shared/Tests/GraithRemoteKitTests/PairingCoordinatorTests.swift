import Foundation
import Testing
import GraithProtocol
@testable import GraithRemoteKit

@MainActor
struct PairingCoordinatorTests {
    private func makeCoordinator(outcome: StubPairing.Outcome) throws -> (PairingCoordinator, HostRegistry, InMemorySecretStore) {
        let store = InMemorySecretStore()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let registry = HostRegistry(
            keychain: store,
            localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"),
            storeURL: url
        )
        let identity = try DeviceIdentity(keychain: store)
        let coordinator = PairingCoordinator(
            pairing: StubPairing(outcome: outcome),
            identity: identity,
            registry: registry
        )
        return (coordinator, registry, store)
    }

    @Test func successfulPairAwaitsFingerprintConfirmationBeforePersisting() async throws {
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        let (coordinator, registry, store) = try makeCoordinator(outcome: .succeed(resp))

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")

        // Awaiting confirmation — nothing trusted yet.
        guard case .awaitingConfirmation = coordinator.phase else {
            Issue.record("expected awaitingConfirmation, got \(coordinator.phase)")
            return
        }
        #expect(coordinator.spkiFingerprint == "01:02:03") // hex of base64 "AQID"
        #expect((try? store.string(for: "host.ben.clientToken")) == nil,
                "token must not be stored before the user confirms the fingerprint")
        #expect(registry.host(id: "ben")?.isPaired == false)

        // Confirm — now it persists.
        coordinator.confirmPairing()
        guard case .paired = coordinator.phase else {
            Issue.record("expected paired, got \(coordinator.phase)")
            return
        }
        #expect(registry.host(id: "ben")?.isPaired == true)
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-braw")
    }

    @Test func rejectDiscardsTokenAndHost() async throws {
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        let (coordinator, registry, store) = try makeCoordinator(outcome: .succeed(resp))

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        coordinator.rejectPairing()

        #expect(coordinator.phase == .idle)
        #expect(registry.host(id: "ben") == nil)
        #expect((try? store.string(for: "host.ben.clientToken")) == nil)
    }

    @Test func daemonErrorFailsAndDropsPlaceholder() async throws {
        let (coordinator, registry, _) = try makeCoordinator(
            outcome: .fail(.daemon("pairing disabled"))
        )

        await coordinator.pair(hostID: "thrawn", label: "thrawn",
                               magicDNSName: "thrawn.tail.ts.net", deviceLabel: "canny-mac")

        guard case let .failed(message) = coordinator.phase else {
            Issue.record("expected failed, got \(coordinator.phase)")
            return
        }
        #expect(message == "pairing disabled")
        #expect(registry.host(id: "thrawn") == nil, "the pending placeholder must be dropped on failure")
    }
}
