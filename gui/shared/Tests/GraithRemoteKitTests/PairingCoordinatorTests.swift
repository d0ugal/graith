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
            // The candidate credential (separate account) is durable BEFORE the ack;
            // the live token is only promoted on commit (issue #1299).
            persistedAtAck.value = (try? store.string(for: "host.ben.candidateToken")) == "tok-braw"
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
        #expect((try? store.string(for: "host.ben.candidateToken")) == nil,
                "no candidate must be stored before the user confirms the fingerprint")
        #expect((try? store.string(for: "host.ben.clientToken")) == nil,
                "token must not be stored before the user confirms the fingerprint")
        #expect(registry.host(id: "ben")?.isPaired == false)
        #expect(!session.ackCalled, "no ack before confirmation")

        // Confirm — stages the candidate durably BEFORE the ack, then commits and
        // reaches .paired.
        await coordinator.confirmPairing()
        guard case .paired = coordinator.phase else {
            Issue.record("expected paired, got \(coordinator.phase)")
            return
        }
        #expect(session.ackCalled, "ack must be sent on confirm")
        #expect(registry.host(id: "ben")?.isPaired == true)
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-braw")
        #expect((try? store.string(for: "host.ben.candidateToken")) == nil,
                "the candidate is cleared once committed")
        #expect(persistedAtAck.value, "the candidate credential must be durably stored BEFORE pair_ack")
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
        // failure that already landed the device on disk), so it is commit-unknown:
        // the durable candidate MUST be retained for the probe-based commit oracle,
        // NOT promoted to a paired host (that would be a ghost) and NOT discarded
        // (that would strand a committed device) (issue #1299).
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
        // Not promoted (no ghost), but retained as a pending candidate for the probe.
        #expect(registry.host(id: "ben")?.isPaired != true, "commit-unknown must NOT promote a ghost paired host")
        #expect((try? store.string(for: "host.ben.clientToken")) == nil, "live token not written until the probe commits")
        #expect((try? store.string(for: "host.ben.candidateToken")) == "tok-braw", "candidate retained for the probe")
        #expect(registry.pendingReceipt()?.credentials.clientToken == "tok-braw", "a pending receipt awaits the commit oracle")
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
        // Retained as a pending candidate (the daemon may already be durable), not
        // promoted to a paired host until the probe confirms it.
        #expect(registry.host(id: "ben")?.isPaired != true, "commit-unknown must NOT promote a ghost paired host")
        #expect((try? store.string(for: "host.ben.candidateToken")) == "tok-braw")
        #expect(registry.pendingReceipt() != nil, "a pending receipt awaits the commit oracle")
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

        // Reject while confirm is blocked post-ack. The durable candidate survives.
        await coordinator.rejectPairing()
        #expect((try? store.string(for: "host.ben.candidateToken")) == "tok-braw",
                "reject must not wipe a durably-acked candidate mid-commit")

        gate.open()
        await confirmTask.value
        // The gated ack succeeded, so the (now stale) confirm commits the candidate —
        // the daemon may have it, so it is retained, not discarded.
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

    @Test func gatedSecondAttemptIsRefusedAndFirstCommitsItsOwnToken() async throws {
        // Token-identity safety (issue #1299): while a gen1 confirm is gated post-ack
        // (ack sent for tok-1), a second attempt must be REFUSED without bumping the
        // generation or abandoning gen1 — the single journal/candidate is never
        // overwritten, so gen1 commits ITS OWN tok-1 (never a second attempt's
        // token), and a gen2 that would later fail cannot strand tok-1.
        let r1 = makePairResponse(requestID: "req-1", deviceID: "dev-1", clientToken: "tok-1", tlsPinSPKI: "AQID")
        let gate1 = Gate()
        let s1 = StubPairingSession(response: r1, ackGate: gate1)

        let store = InMemorySecretStore()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: store,
                                    localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"),
                                    storeURL: url)
        let identity = try DeviceIdentity(keychain: store)
        // Only s1 is ever vended — the refused second pair() never reaches beginPairing.
        let coordinator = PairingCoordinator(pairing: QueuePairing([s1]),
                                             identity: identity, registry: registry)

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        let confirm1 = Task { await coordinator.confirmPairing() }
        while !s1.ackCalled { await Task.yield() }

        // gen1 is gated post-ack. A second attempt must fail closed (settle/retry).
        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        guard case .failed = coordinator.phase else {
            Issue.record("expected a settle/retry .failed, got \(coordinator.phase)")
            return
        }
        #expect(!s1.abandoned, "the in-flight gen1 session must not be abandoned by a refused attempt")

        gate1.open()
        await confirm1.value

        // gen1 committed ITS OWN token — the generation was never bumped.
        #expect(registry.host(id: "ben")?.isPaired == true)
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-1")
        #expect(registry.pendingReceipt() == nil, "gen1's receipt is committed + cleared")
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

        // The durably-acked candidate must survive the reset.
        #expect((try? store.string(for: "host.ben.candidateToken")) == "tok-braw",
                "reset must not wipe a durably-acked candidate mid-commit")

        gate.open()
        await confirmTask.value
        // The gated ack succeeded, so the (now stale) confirm commits the candidate.
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-braw")
        // The stale confirm must not overwrite the newer .idle phase (gate 7).
        #expect(coordinator.phase == .idle, "stale confirm must not resurrect a phase after reset")
    }

    @Test func durableStepsAllPrecedePairAck() async throws {
        // The pre-ack candidate write must be genuinely fsync-backed: temp write →
        // file fsync → atomic rename → parent-directory fsync, ALL before pair_ack
        // (issue #1299). A recording fileOps + an ack timeline marker prove the order.
        let timeline = Timeline()
        let fileOps = RecordingFileOps(timeline: timeline)
        let store = InMemorySecretStore()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: store,
                                    localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"),
                                    storeURL: url, fileOps: fileOps)
        let identity = try DeviceIdentity(keychain: store)
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        let session = StubPairingSession(response: resp, onAck: { timeline.record("ack") })
        let coordinator = PairingCoordinator(pairing: StubPairing(outcome: .succeed(session)),
                                             identity: identity, registry: registry)

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        await coordinator.confirmPairing()

        let events = timeline.all
        let ackIdx = try #require(events.firstIndex(of: "ack"), "ack must have been sent")
        let beforeAck = Array(events.prefix(ackIdx))
        let w = try #require(beforeAck.firstIndex(of: "writeTemp"))
        let s = try #require(beforeAck.firstIndex(of: "syncFile"))
        let r = try #require(beforeAck.firstIndex(of: "replace"))
        let d = try #require(beforeAck.firstIndex(of: "syncDir"))
        #expect(w < s && s < r && r < d,
                "write → file sync → rename → dir sync must all precede pair_ack")
    }

    @Test func pairRefusesOnCorruptJournalAfterRelaunchWithoutStartingFlow() async throws {
        // A corrupt/unreadable journal on relaunch makes pendingReceipt() nil while
        // persisted=false — but the early guard gates on hasPendingJournal (file
        // existence), so pair() must fail closed WITHOUT bumping the generation,
        // creating a placeholder, or opening a pairing flow (issue #1299).
        let store = InMemorySecretStore()
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-corrupt-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent("hosts.json")
        // A corrupt journal file exists on disk before this coordinator starts.
        try Data("{ not valid json".utf8).write(to: dir.appendingPathComponent("pending-pairing.json"))

        let registry = HostRegistry(keychain: store,
                                    localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"),
                                    storeURL: url)
        let identity = try DeviceIdentity(keychain: store)
        let recording = RecordingPairing()
        let coordinator = PairingCoordinator(pairing: recording, identity: identity, registry: registry)

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")

        guard case .failed = coordinator.phase else {
            Issue.record("expected a fail-closed .failed, got \(coordinator.phase)")
            return
        }
        #expect(!recording.beginCalled, "no pairing flow may be opened while a journal exists")
        #expect(registry.host(id: "ben") == nil, "no placeholder created on a fail-closed refusal")
    }

    @Test func preAckSyncFailureSendsNoAckAndKeepsPriorCredential() async throws {
        // A durable-write failure during the pre-ack candidate stage must stay
        // pre-ack (no pair_ack sent) and leave a prior working credential exactly
        // intact — a re-pair that fails must not destroy the old token (issue #1299).
        let store = InMemorySecretStore()
        let fileOps = RecordingFileOps()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: store,
                                    localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"),
                                    storeURL: url, fileOps: fileOps)
        let identity = try DeviceIdentity(keychain: store)
        let r1 = makePairResponse(requestID: "req-1", deviceID: "dev-1", clientToken: "tok-1",
                                  daemonProfile: "prof-1", tlsPinSPKI: "AQID")
        let r2 = makePairResponse(requestID: "req-2", deviceID: "dev-2", clientToken: "tok-2",
                                  daemonProfile: "prof-2", tlsPinSPKI: "BAUG")
        let s1 = StubPairingSession(response: r1)
        let s2 = StubPairingSession(response: r2)
        let coordinator = PairingCoordinator(pairing: QueuePairing([s1, s2]),
                                             identity: identity, registry: registry)

        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        await coordinator.confirmPairing()
        #expect(registry.host(id: "ben")?.isPaired == true)

        // Arm a syncFile failure for the re-pair's pre-ack candidate write.
        fileOps.armFailure(at: .syncFile)
        await coordinator.pair(hostID: "ben", label: "ben",
                               magicDNSName: "ben.tail.ts.net", deviceLabel: "canny-mac")
        await coordinator.confirmPairing()

        guard case .failed = coordinator.phase else {
            Issue.record("expected failed on sync failure, got \(coordinator.phase)")
            return
        }
        #expect(!s2.ackCalled, "a pre-ack sync failure must send no pair_ack")
        // The prior credential is exactly intact.
        #expect(registry.host(id: "ben")?.isPaired == true)
        #expect(registry.host(id: "ben")?.tlsPinSPKI == "AQID")
        #expect(registry.host(id: "ben")?.deviceID == "dev-1")
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-1")
        #expect((try? store.string(for: "host.ben.candidateToken")) == nil, "the failed candidate is rolled back")
        #expect(registry.pendingReceipt() == nil)
    }
}
