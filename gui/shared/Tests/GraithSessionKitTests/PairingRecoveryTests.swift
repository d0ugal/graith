import Foundation
import Testing
import GraithProtocol
import GraithRemoteKit
@testable import GraithSessionKit

/// FleetModel-level recovery of a pairing that was in flight at launch, plus the
/// connection-fingerprint observer that surfaces a commit-unknown host (issue
/// #1299). The commit oracle is an authenticated probe: auth success promotes,
/// auth rejection discards (no ghost), transport failure retains for retry.
@MainActor
struct PairingRecoveryTests {
    /// Build a registry with a durable `.acked` pending receipt for host "ben"
    /// (candidate token "cand-tok"), i.e. a crash after markCandidateAcked but
    /// before the commit.
    private func makeRegistryWithAckedReceipt(store: InMemorySecretStore) throws -> HostRegistry {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-recovery-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: store, storeURL: url)
        registry.upsert(Host(id: "ben", label: "Ben", kind: .remote, magicDNSName: "ben.tail", isPaired: false))
        let resp = PairResponseMsg(deviceID: "dev-ben", clientToken: "cand-tok", daemonProfile: "", tlsPinSPKI: "cGlu")
        try registry.beginCandidate(host: try #require(registry.host(id: "ben")), response: resp, createdPlaceholder: true)
        try registry.markCandidateAcked(hostID: "ben")
        return registry
    }

    @Test func launchProbeAuthRejectionDiscardsCandidateNoGhost() async throws {
        // Crash after markCandidateAcked but before the ack bytes were sent → the
        // daemon timed the request out and committed nothing. On relaunch the probe
        // is rejected (auth), so the candidate is discarded and NO paired ghost
        // remains (issue #1299).
        let secrets = InMemorySecretStore()
        let registry = try makeRegistryWithAckedReceipt(store: secrets)
        let identity = try DeviceIdentity(keychain: secrets)
        let probe = MockHostClient(failConnect: .notPaired) // daemon rejects: never committed
        let factory = MockFactory(clients: ["cand-tok": probe])
        let pairing = PairingCoordinator(pairing: StubPairing(), identity: identity, registry: registry)
        let fleet = FleetModel(registry: registry, identity: identity, reachability: nil,
                               factory: factory, pairing: pairing)

        await fleet.reconcilePendingReceipt()

        #expect(registry.host(id: "ben") == nil, "auth rejection must leave no paired ghost")
        #expect(registry.pendingReceipt() == nil, "the candidate is discarded")
        #expect((try? secrets.string(for: "host.ben.candidateToken")) == nil)
        #expect(!fleet.connections.contains { $0.id == "ben" })
    }

    @Test func launchProbeAuthSuccessPromotesAndConnects() async throws {
        // Crash after the daemon durably committed → on relaunch the probe
        // authenticates, so the candidate is promoted and its live connection is
        // established — the recovered host enters the fleet (issue #1299).
        let secrets = InMemorySecretStore()
        let registry = try makeRegistryWithAckedReceipt(store: secrets)
        let identity = try DeviceIdentity(keychain: secrets)
        let probe = MockHostClient(sessions: [makeSession(id: "s1", name: "canny")])
        let factory = MockFactory(clients: ["cand-tok": probe])
        let pairing = PairingCoordinator(pairing: StubPairing(), identity: identity, registry: registry)
        let fleet = FleetModel(registry: registry, identity: identity, reachability: nil,
                               factory: factory, pairing: pairing)

        await fleet.reconcilePendingReceipt()

        let ben = try #require(registry.host(id: "ben"))
        #expect(ben.isPaired == true, "auth success promotes the candidate")
        #expect(registry.pendingReceipt() == nil, "the journal is cleared after commit")
        #expect(registry.credentials(for: ben)?.clientToken == "cand-tok")
        // reconcile rebuilds + connects deterministically (awaited), so the recovered
        // host is in the fleet the moment reconcile returns — no polling.
        #expect(fleet.connections.contains { $0.id == "ben" }, "the recovered host enters the fleet")
    }

    @Test func launchProbeNonAuthFailuresRetainCandidate() async throws {
        // Only a canonical invalid-token rejection (.notPaired) proves no-commit. A
        // generic proof-of-possession failure (bad signature / identity mismatch —
        // possible for a COMMITTED candidate), a handshake profile/version rejection,
        // a TLS pin failure, a transport failure, and a timeout are all INDETERMINATE
        // and MUST retain the candidate for a later retry (issue #1299).
        let indeterminate: [GraithClientError] = [
            .daemon("proof of possession failed"),
            .authenticationFailed("profile mismatch"),
            .tlsPinMismatch,
            .tailnetUnreachable,
            .disconnected("timed out"),
        ]
        for failure in indeterminate {
            let secrets = InMemorySecretStore()
            let registry = try makeRegistryWithAckedReceipt(store: secrets)
            let identity = try DeviceIdentity(keychain: secrets)
            let probe = MockHostClient(failConnect: failure)
            let factory = MockFactory(clients: ["cand-tok": probe])
            let pairing = PairingCoordinator(pairing: StubPairing(), identity: identity, registry: registry)
            let fleet = FleetModel(registry: registry, identity: identity, reachability: nil,
                                   factory: factory, pairing: pairing)

            await fleet.reconcilePendingReceipt()

            #expect(registry.pendingReceipt() != nil, "\(failure) is indeterminate — must retain the candidate")
            #expect((try? secrets.string(for: "host.ben.candidateToken")) == "cand-tok", "\(failure) must keep the candidate token")
            #expect(registry.host(id: "ben")?.isPaired != true, "\(failure) must not promote a paired host")
        }
    }

    @Test func transportFailureThenLifecycleRetryPromotesWithoutRestart() async throws {
        // After a transport-failure probe retains the candidate, a normal lifecycle
        // connect (foreground / reconnect) must retry the probe and promote once the
        // link recovers — without an app restart (issue #1299).
        let secrets = InMemorySecretStore()
        let registry = try makeRegistryWithAckedReceipt(store: secrets)
        let identity = try DeviceIdentity(keychain: secrets)
        let probe = MockHostClient(sessions: [makeSession(id: "s1", name: "canny")], failConnect: .tailnetUnreachable)
        let factory = MockFactory(clients: ["cand-tok": probe])
        let pairing = PairingCoordinator(pairing: StubPairing(), identity: identity, registry: registry)
        let fleet = FleetModel(registry: registry, identity: identity, reachability: nil,
                               factory: factory, pairing: pairing)

        await fleet.reconcilePendingReceipt()
        #expect(registry.pendingReceipt() != nil, "transport failure retains the candidate")
        #expect(registry.host(id: "ben")?.isPaired != true)

        // The link recovers; a lifecycle connect retries the probe and promotes.
        // connectAll AWAITs the reconcile, so this is deterministic — no polling.
        await probe.setFailConnect(nil)
        await fleet.connectAll()

        #expect(registry.host(id: "ben")?.isPaired == true, "a later lifecycle connect must retry + promote")
        #expect(registry.pendingReceipt() == nil)
        #expect(fleet.connections.contains { $0.id == "ben" })
    }

    @Test func commitUnknownIsPairedTransitionRebuildsConnectionsForSameID() async throws {
        // gap (b): a commit reuses the existing placeholder id, flipping isPaired
        // false→true with NO change to the id set. An id-only observer gate would
        // ignore it; the connection-fingerprint gate must rebuild + connect (#1299).
        let secrets = InMemorySecretStore()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-recovery-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: secrets, storeURL: url)
        registry.upsert(Host(id: "ben", label: "Ben", kind: .remote, magicDNSName: "ben.tail", isPaired: false))
        let identity = try DeviceIdentity(keychain: secrets)
        let mock = MockHostClient(sessions: [makeSession(id: "s1", name: "canny")])
        let factory = MockFactory(clients: ["tok-ben": mock])
        let pairing = PairingCoordinator(pairing: StubPairing(), identity: identity, registry: registry)
        let fleet = FleetModel(registry: registry, identity: identity, reachability: nil,
                               factory: factory, pairing: pairing)

        #expect(!fleet.connections.contains { $0.id == "ben" }, "an unpaired placeholder has no connection")

        try registry.completePairing(hostID: "ben", response: PairResponseMsg(
            deviceID: "dev-ben", clientToken: "tok-ben", daemonProfile: "", tlsPinSPKI: "cGlu"))

        // The observer reacts to the isPaired transition (same id ⇒ id-set unchanged,
        // but the connection fingerprint changed). Await that reaction directly —
        // rebuildAndConnectIfChanged is the observer's awaitable body — rather than
        // polling a fire-and-forget Task.
        await fleet.rebuildAndConnectIfChanged()
        #expect(fleet.connections.contains { $0.id == "ben" },
                "an isPaired transition on an existing id must rebuild connections")
    }

    @Test func displayOnlyMutationDoesNotRebuildConnection() async throws {
        // A markSeen tick (same connection fingerprint) must NOT tear down + re-dial:
        // the connection instance is preserved (issue #1299 observer gate).
        let (fleet, _) = makeFleetWithRemote(sessions: [makeSession(id: "s1", name: "canny")])
        await fleet.connectAll()
        let before = try #require(fleet.connections.first { $0.id == "ben" })

        fleet.registry.markSeen(hostID: "ben", at: Date(timeIntervalSince1970: 1))
        await fleet.rebuildAndConnectIfChanged() // no-op: fingerprint unchanged

        let after = try #require(fleet.connections.first { $0.id == "ben" })
        #expect(before === after, "a display-only markSeen must not rebuild the connection")
    }
}
