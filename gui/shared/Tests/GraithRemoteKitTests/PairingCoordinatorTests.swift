import Foundation
import Testing
import GraithProtocol
@testable import GraithRemoteKit

/// A test-shared box for recording an observation from a @Sendable callback.
final class Box<T>: @unchecked Sendable {
    var value: T
    init(_ value: T) { self.value = value }
}

@MainActor
struct PairingCoordinatorTests {
    private func makeCoordinator(
        outcome: StubPairing.Outcome,
        storeURL: URL? = nil
    ) throws -> (PairingCoordinator, HostRegistry, InMemorySecretStore) {
        let store = InMemorySecretStore()
        let url = storeURL ?? FileManager.default.temporaryDirectory
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

    @Test func successfulPairAwaitsFingerprintConfirmationThenPersistsBeforeAck() async throws {
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")

        // The store is captured by onAck so we can prove the credential was already
        // durably stored at the moment pair_ack fires (persist-before-ack).
        let store = InMemorySecretStore()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: store,
                                    localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"),
                                    storeURL: url)
        let identity = try DeviceIdentity(keychain: store)

        let persistedAtAck = Box(false)
        let session = StubPairingSession(response: resp, onAck: { [store] in
            persistedAtAck.value = (try? store.string(for: "host.ben.clientToken")) == "tok-braw"
        })
        let coordinator = PairingCoordinator(pairing: StubPairing(outcome: .succeed(session)),
                                             identity: identity, registry: registry)

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")

        // Awaiting confirmation — nothing trusted, no ack sent.
        guard case .awaitingConfirmation = coordinator.phase else {
            Issue.record("expected awaitingConfirmation, got \(coordinator.phase)")
            return
        }
        #expect(coordinator.spkiFingerprint == "01:02:03") // hex of base64 "AQID"
        #expect((try? store.string(for: "host.ben.clientToken")) == nil,
                "token must not be stored before the user confirms the fingerprint")
        #expect(registry.host(id: "ben")?.isPaired == false)
        #expect(!session.ackCalled, "no ack before confirmation")

        // Confirm — persists durably BEFORE the ack, then reaches .paired.
        await coordinator.confirmPairing()
        guard case .paired = coordinator.phase else {
            Issue.record("expected paired, got \(coordinator.phase)")
            return
        }
        #expect(session.ackCalled, "ack must be sent on confirm")
        #expect(registry.host(id: "ben")?.isPaired == true)
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-braw")
        #expect(persistedAtAck.value, "credential must be durably stored BEFORE pair_ack")
    }

    @Test func rejectDiscardsTokenAndHostAndAbandons() async throws {
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        let session = StubPairingSession(response: resp)
        let (coordinator, registry, store) = try makeCoordinator(outcome: .succeed(session))

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        await coordinator.rejectPairing()

        #expect(coordinator.phase == .idle)
        #expect(registry.host(id: "ben") == nil)
        #expect((try? store.string(for: "host.ben.clientToken")) == nil)
        #expect(session.abandoned, "reject must abandon the open session")
        #expect(!session.ackCalled, "reject must never send pair_ack")
    }

    @Test func resetDropsUnconfirmedPlaceholderAndAbandons() async throws {
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        let session = StubPairingSession(response: resp)
        let (coordinator, registry, store) = try makeCoordinator(outcome: .succeed(session))

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        coordinator.reset()

        // Give the fire-and-forget abandon Task a chance to run.
        await Task.yield()

        #expect(coordinator.phase == .idle)
        #expect(registry.host(id: "ben") == nil)
        #expect((try? store.string(for: "host.ben.clientToken")) == nil)
        #expect(!session.ackCalled, "reset must never send pair_ack")
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

    @Test func confirmFailsWhenStoreUnwritable() async throws {
        // Point the store's parent at an existing regular file so createDirectory /
        // atomic write throws inside completePairing.
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-unwritable-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let blocker = dir.appendingPathComponent("blocker")
        try Data("x".utf8).write(to: blocker)
        let storeURL = blocker.appendingPathComponent("hosts.json") // parent is a file

        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        let session = StubPairingSession(response: resp)
        let (coordinator, registry, store) = try makeCoordinator(outcome: .succeed(session), storeURL: storeURL)

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        await coordinator.confirmPairing()

        guard case .failed = coordinator.phase else {
            Issue.record("expected failed on unwritable store, got \(coordinator.phase)")
            return
        }
        #expect(!session.ackCalled, "no ack may be sent when the durable store write fails")
        #expect((try? store.string(for: "host.ben.clientToken")) == nil,
                "the token must be rolled back when the metadata write fails")
    }

    @Test func confirmRetainsCredentialOnDaemonErrorAfterAck() async throws {
        // A daemon `error` after the ack is ambiguous (it may be a state-write
        // failure that already landed the device on disk), so it is commit-unknown
        // and the durable credential MUST be retained, not discarded (issue #1299).
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        let session = StubPairingSession(response: resp,
                                         commitOutcome: .failure(PairingError.commitUnknown(ControlError.daemon("save failed"))))
        let (coordinator, registry, store) = try makeCoordinator(outcome: .succeed(session))

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        await coordinator.confirmPairing()

        guard case .failed = coordinator.phase else {
            Issue.record("expected failed (commit unknown), got \(coordinator.phase)")
            return
        }
        #expect(session.ackCalled)
        #expect(registry.host(id: "ben")?.isPaired == true, "a post-ack daemon error must retain the durable credential")
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-braw")
    }

    @Test func confirmRetainsCredentialWhenCommitUnknown() async throws {
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        let session = StubPairingSession(response: resp, commitOutcome: .failure(PairingError.commitUnknown(ControlError.malformed("dropped"))))
        let (coordinator, registry, store) = try makeCoordinator(outcome: .succeed(session))

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        await coordinator.confirmPairing()

        guard case .failed = coordinator.phase else {
            Issue.record("expected failed (commit unknown), got \(coordinator.phase)")
            return
        }
        #expect(session.ackCalled)
        // The credential is RETAINED — the daemon may already be durable.
        #expect(registry.host(id: "ben")?.isPaired == true, "commit-unknown must retain the durable credential")
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-braw")
    }

    @Test func supersedeSameHostIDRetainsExistingPairedRow() async throws {
        // Re-pairing an already-paired host (same id) and then resetting the new
        // attempt must NOT drop the existing paired row or its token (issue #1299).
        let r1 = makePairResponse(requestID: "req-1", deviceID: "dev-1", clientToken: "tok-1", tlsPinSPKI: "AQID")
        let r2 = makePairResponse(requestID: "req-2", deviceID: "dev-2", clientToken: "tok-2", tlsPinSPKI: "AQID")
        let s1 = StubPairingSession(response: r1)
        let s2 = StubPairingSession(response: r2)

        let store = InMemorySecretStore()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: store,
                                    localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"),
                                    storeURL: url)
        let identity = try DeviceIdentity(keychain: store)
        let coordinator = PairingCoordinator(pairing: QueuePairing([s1, s2]),
                                             identity: identity, registry: registry)

        // First pairing commits durably.
        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        await coordinator.confirmPairing()
        #expect(registry.host(id: "ben")?.isPaired == true)

        // Re-pair the SAME id; the existing paired row must be retained while awaiting.
        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        #expect(registry.host(id: "ben")?.isPaired == true, "existing paired row must survive a same-id re-pair")

        // Reset the re-pair attempt: it must NOT drop the pre-existing paired host.
        coordinator.reset()
        await Task.yield()
        #expect(registry.host(id: "ben")?.isPaired == true, "reset must not drop an existing re-pair target")
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-1", "prior credential must remain intact")
    }

    @Test func legacyDaemonResponseCompletesWithoutRequestID() async throws {
        // A legacy (pre-receipt) daemon sends a pair_response with no request_id and
        // never a pair_committed. The client must decode it (requestID == "") and
        // complete on confirmation without stranding the credential (issue #1299).
        let legacy = makeLegacyPairResponse(deviceID: "dev-legacy", clientToken: "tok-legacy", tlsPinSPKI: "AQID")
        #expect(legacy.requestID == "", "a legacy response decodes with an empty request id")

        let session = StubPairingSession(response: legacy)
        let (coordinator, registry, store) = try makeCoordinator(outcome: .succeed(session))

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        guard case .awaitingConfirmation = coordinator.phase else {
            Issue.record("expected awaitingConfirmation, got \(coordinator.phase)")
            return
        }

        await coordinator.confirmPairing()
        guard case .paired = coordinator.phase else {
            Issue.record("expected paired, got \(coordinator.phase)")
            return
        }
        #expect(registry.host(id: "ben")?.isPaired == true)
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-legacy")
    }

    @Test func supersedeSameHostIDPlaceholderRemovesStaleRowAndAbandons() async throws {
        // Two unconfirmed attempts for the SAME host id: the second must abandon the
        // first session and drop the stale placeholder (not treat it as an existing
        // repair target), so resetting the second leaves nothing behind (issue #1299).
        let r1 = makePairResponse(requestID: "req-1", deviceID: "dev-1", clientToken: "tok-1", tlsPinSPKI: "AQID")
        let r2 = makePairResponse(requestID: "req-2", deviceID: "dev-2", clientToken: "tok-2", tlsPinSPKI: "AQID")
        let s1 = StubPairingSession(response: r1)
        let s2 = StubPairingSession(response: r2)

        let store = InMemorySecretStore()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: store,
                                    localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"),
                                    storeURL: url)
        let identity = try DeviceIdentity(keychain: store)
        let coordinator = PairingCoordinator(pairing: QueuePairing([s1, s2]),
                                             identity: identity, registry: registry)

        // First attempt reaches awaitingConfirmation with a NEW placeholder; not confirmed.
        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        #expect(registry.host(id: "ben") != nil)

        // Second attempt, same id, before confirming the first.
        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        // Let the fire-and-forget abandon Task run.
        for _ in 0..<10 where !s1.abandoned { await Task.yield() }
        #expect(s1.abandoned, "the first same-id session must be abandoned on supersede")

        // Resetting the second attempt must leave no placeholder behind.
        coordinator.reset()
        await Task.yield()
        #expect(registry.host(id: "ben") == nil, "the same-id placeholder must be gone after reset")
    }

    @Test func rejectDuringPostAckWaitRetainsPersistedCredential() async throws {
        // Reject can race a confirm suspended in the post-ack wait (phase stays
        // .awaitingConfirmation). It must NOT wipe a credential that is already
        // durable (issue #1299).
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        let gate = Gate()
        let session = StubPairingSession(response: resp, ackGate: gate)
        let (coordinator, registry, store) = try makeCoordinator(outcome: .succeed(session))

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")

        let confirmTask = Task { await coordinator.confirmPairing() }
        while !session.ackCalled { await Task.yield() }

        // Reject while confirm is blocked post-ack. The durable credential survives.
        await coordinator.rejectPairing()
        #expect(registry.host(id: "ben")?.isPaired == true, "reject must not wipe a persisted credential mid-commit")
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-braw")

        gate.open()
        await confirmTask.value
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-braw")
        // The stale confirm must not overwrite the newer .idle phase (gate 7).
        #expect(coordinator.phase == .idle, "stale confirm must not resurrect a phase after reject")
    }

    @Test func doubleConfirmSendsExactlyOneAck() async throws {
        // A double-tapped "Confirm & Trust" must run confirm once: no duplicate
        // persist, ack, or concurrent connection read (issue #1299).
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        let gate = Gate()
        let session = StubPairingSession(response: resp, ackGate: gate)
        let (coordinator, registry, store) = try makeCoordinator(outcome: .succeed(session))

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")

        // Fire two confirms concurrently; the second must be a no-op guard hit.
        let first = Task { await coordinator.confirmPairing() }
        while !session.ackCalled { await Task.yield() }
        let second = Task { await coordinator.confirmPairing() }
        await second.value // returns immediately (guarded)

        gate.open()
        await first.value

        #expect(session.ackCount == 1, "double confirm must send exactly one pair_ack")
        #expect(registry.host(id: "ben")?.isPaired == true)
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-braw")
    }

    @Test func staleConfirmDeferDoesNotClearNewerAttemptGuard() async throws {
        // Gate 7 isolation: a stale (gen1) confirm's defer must not clear the newer
        // (gen2) confirm's single-flight guard, or a duplicate gen2 confirm would
        // slip through and send a second ack (issue #1299).
        let r1 = makePairResponse(requestID: "req-1", deviceID: "dev-1", clientToken: "tok-1", tlsPinSPKI: "AQID")
        let r2 = makePairResponse(requestID: "req-2", deviceID: "dev-2", clientToken: "tok-2", tlsPinSPKI: "AQID")
        let gate1 = Gate()
        let gate2 = Gate()
        let s1 = StubPairingSession(response: r1, ackGate: gate1)
        let s2 = StubPairingSession(response: r2, ackGate: gate2)

        let store = InMemorySecretStore()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: store,
                                    localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"),
                                    storeURL: url)
        let identity = try DeviceIdentity(keychain: store)
        let coordinator = PairingCoordinator(pairing: QueuePairing([s1, s2]),
                                             identity: identity, registry: registry)

        // gen1 confirm blocks on gate1 (guard = gen1).
        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        let confirm1 = Task { await coordinator.confirmPairing() }
        while !s1.ackCalled { await Task.yield() }

        // gen2 supersedes and its confirm blocks on gate2 (guard now = gen2).
        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        let confirm2a = Task { await coordinator.confirmPairing() }
        while !s2.ackCalled { await Task.yield() }

        // Let the stale gen1 confirm finish; its defer must NOT clear gen2's guard.
        gate1.open()
        await confirm1.value

        // A duplicate gen2 confirm; if the guard leaked it would enter the ack and
        // gate. Do NOT await it before opening the gate, or a bug would hang here
        // instead of failing the assertion.
        let confirm2b = Task { await coordinator.confirmPairing() }
        await Task.yield()

        // Open gate2 twice so both the correct (one waiter) and buggy (two waiters)
        // cases unblock rather than deadlock; the count assertion catches the bug.
        gate2.open()
        gate2.open()
        await confirm2a.value
        await confirm2b.value

        #expect(s2.ackCount == 1, "stale gen1 cleanup must not clear the gen2 confirm guard")
    }

    @Test func resetDuringPostAckWaitRetainsPersistedCredential() async throws {
        // A reset that races the post-ack await must NOT delete a credential that
        // was already durably persisted before the ack (issue #1299).
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        let gate = Gate()
        let session = StubPairingSession(response: resp, ackGate: gate)
        let (coordinator, registry, store) = try makeCoordinator(outcome: .succeed(session))

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")

        // Start confirm; it persists, sends the ack, then blocks on the gate.
        let confirmTask = Task { await coordinator.confirmPairing() }
        // Let confirm reach the gated ack.
        while !session.ackCalled { await Task.yield() }

        // Reset races in while confirm is blocked post-ack.
        coordinator.reset()
        await Task.yield()

        // The durably-persisted credential must survive the reset.
        #expect(registry.host(id: "ben")?.isPaired == true, "reset must not wipe a persisted credential mid-commit")
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-braw")

        gate.open()
        await confirmTask.value
        // The credential remains durable regardless of the superseded confirm.
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-braw")
        // The stale confirm must not overwrite the newer .idle phase (gate 7).
        #expect(coordinator.phase == .idle, "stale confirm must not resurrect a phase after reset")
    }
}
