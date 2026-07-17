import Foundation
import Testing
@testable import GraithRemoteKit

@MainActor
struct HostRegistryTests {
    private func makeRegistry(store: SecretStore = InMemorySecretStore()) -> (HostRegistry, URL) {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let local = Host.local(socketPath: "/tmp/bothy/graith.sock")
        return (HostRegistry(keychain: store, localHost: local, storeURL: url), url)
    }

    @Test func seedsLocalHostAndKeepsItFirst() {
        let (registry, _) = makeRegistry()
        #expect(registry.hosts.count == 1)
        #expect(registry.hosts.first?.kind == .local)
        #expect(registry.hosts.first?.id == "local")
    }

    @Test func localHostIsNeverRemovable() {
        let (registry, _) = makeRegistry()
        registry.remove(hostID: "local")
        #expect(registry.hosts.contains { $0.kind == .local })
    }

    @Test func upsertAddsRemoteHost() {
        let (registry, _) = makeRegistry()
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        #expect(registry.host(id: "ben")?.label == "ben")
        #expect(registry.hosts.count == 2)
    }

    @Test func localHostVendsNoCredentials() {
        let (registry, _) = makeRegistry()
        let local = registry.host(id: "local")!
        #expect(registry.credentials(for: local) == nil)
    }

    @Test func completePairingStoresTokenAndVendsCredentials() {
        let store = InMemorySecretStore()
        let (registry, _) = makeRegistry(store: store)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))

        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw",
                                    daemonProfile: "", tlsPinSPKI: "cGlu")
        try? registry.completePairing(hostID: "ben", response: resp)

        let host = registry.host(id: "ben")!
        #expect(host.isPaired)
        #expect(host.deviceID == "dev-ben")
        #expect(host.tlsPinSPKI == "cGlu")

        let creds = registry.credentials(for: host)
        #expect(creds?.clientToken == "tok-braw")
        #expect(creds?.deviceID == "dev-ben")
    }

    @Test func refusesCredentialsWhenTokenPresentButNoPin() {
        // A token but an empty TLS pin would connect in accept-any (TOFU) mode.
        // The registry must fail closed and treat the host as unpaired.
        let store = InMemorySecretStore()
        let (registry, _) = makeRegistry(store: store)
        registry.upsert(Host(id: "thrawn", label: "thrawn", kind: .remote, magicDNSName: "thrawn.tail.ts.net"))
        try? store.set("tok-only", for: "host.thrawn.clientToken")

        let host = registry.host(id: "thrawn")!
        #expect(host.tlsPinSPKI.isEmpty)
        #expect(registry.credentials(for: host) == nil)
    }

    @Test func completePairingThrowsForUnknownHostWithoutOrphaningToken() {
        // If the placeholder was forgotten before confirmation, completePairing
        // must not write a token that no host references.
        let store = InMemorySecretStore()
        let (registry, _) = makeRegistry(store: store)
        let resp = makePairResponse(deviceID: "dev", clientToken: "tok", tlsPinSPKI: "cGlu")
        #expect(throws: HostRegistryError.self) {
            try registry.completePairing(hostID: "ghost", response: resp)
        }
        #expect((try? store.string(for: "host.ghost.clientToken")) == nil)
    }

    @Test func failedRepairOfExistingHostLeavesPriorStateExactlyIntact() throws {
        // Re-pairing an existing paired host whose durable write then fails must
        // leave the token, pin, device ID, profile, paired flag, and on-disk JSON
        // exactly as they were before the attempt (issue #1299).
        let store = InMemorySecretStore()
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-repair-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent("hosts.json")
        let local = Host.local(socketPath: "/tmp/bothy/graith.sock")
        let registry = HostRegistry(keychain: store, localHost: local, storeURL: url)

        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let first = makePairResponse(requestID: "req-1", deviceID: "dev-1", clientToken: "tok-1",
                                     daemonProfile: "profile-1", tlsPinSPKI: "cGluMQ==")
        try registry.completePairing(hostID: "ben", response: first)

        let priorJSON = try Data(contentsOf: url)

        // Make the store directory unwritable so the second durable write fails at
        // temp-file creation, before the existing hosts.json is touched.
        try FileManager.default.setAttributes([.posixPermissions: 0o500], ofItemAtPath: dir.path)
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: dir.path) }

        let repair = makePairResponse(requestID: "req-2", deviceID: "dev-2", clientToken: "tok-2",
                                      daemonProfile: "profile-2", tlsPinSPKI: "cGluMg==")
        #expect(throws: (any Error).self) {
            try registry.completePairing(hostID: "ben", response: repair)
        }

        // In-memory metadata is exactly the prior pairing.
        let host = try #require(registry.host(id: "ben"))
        #expect(host.isPaired == true)
        #expect(host.tlsPinSPKI == "cGluMQ==")
        #expect(host.deviceID == "dev-1")
        #expect(host.daemonProfile == "profile-1")
        // The secret is the prior token, not the failed repair's.
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-1")
        // On-disk JSON is byte-for-byte the prior pairing.
        #expect(try Data(contentsOf: url) == priorJSON)
    }

    @Test func failedExistingHostRepairRestoresSecretWhenSetThrows() throws {
        // A SecretStore.set that applies its side effect then throws must still be
        // rolled back to the exact prior token and metadata (issue #1299).
        let store = ThrowOnceOnSetStore()
        let (registry, _) = makeRegistry(store: store)

        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let first = makePairResponse(requestID: "req-1", deviceID: "dev-1", clientToken: "tok-1",
                                     daemonProfile: "profile-1", tlsPinSPKI: "cGluMQ==")
        try registry.completePairing(hostID: "ben", response: first)

        store.armSetFailure()
        let repair = makePairResponse(requestID: "req-2", deviceID: "dev-2", clientToken: "tok-2",
                                      daemonProfile: "profile-2", tlsPinSPKI: "cGluMg==")
        #expect(throws: (any Error).self) {
            try registry.completePairing(hostID: "ben", response: repair)
        }

        // The prior token and metadata survive the partial-then-failed set.
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-1")
        let host = try #require(registry.host(id: "ben"))
        #expect(host.tlsPinSPKI == "cGluMQ==")
        #expect(host.deviceID == "dev-1")
        #expect(host.daemonProfile == "profile-1")
        #expect(host.isPaired == true)
    }

    @Test func removeWipesToken() {
        let store = InMemorySecretStore()
        let (registry, _) = makeRegistry(store: store)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "cGlu")
        try? registry.completePairing(hostID: "ben", response: resp)

        registry.remove(hostID: "ben")
        #expect(registry.host(id: "ben") == nil)
        #expect((try? store.string(for: "host.ben.clientToken")) == nil)
    }

    // MARK: - Attempt-scoped pending-pairing journal (issue #1299)

    private func makeDurableRegistry(store: SecretStore) -> (HostRegistry, Host, URL) {
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-journal-\(UUID().uuidString)", isDirectory: true)
        let url = dir.appendingPathComponent("hosts.json")
        let local = Host.local(socketPath: "/tmp/bothy/graith.sock")
        return (HostRegistry(keychain: store, localHost: local, storeURL: url), local, url)
    }

    @Test func pendingCandidateForNewHostIsDiscardedOnRelaunchWithNoGhost() throws {
        // A crash after beginCandidate (candidate token + .candidate journal durable)
        // but before the ack: no ack was ever sent, so the daemon cannot have
        // committed. A fresh registry over the same files must expose NO paired ghost
        // and drop the candidate (issue #1299).
        let store = InMemorySecretStore()
        let (first, local, url) = makeDurableRegistry(store: store)
        first.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        try first.beginCandidate(host: try #require(first.host(id: "ben")), response: resp, createdPlaceholder: true)

        let second = HostRegistry(keychain: store, localHost: local, storeURL: url)
        #expect(second.host(id: "ben") == nil, "a .candidate journal must be discarded — no ghost host")
        #expect((try? store.string(for: "host.ben.candidateToken")) == nil, "candidate token dropped")
        #expect((try? store.string(for: "host.ben.clientToken")) == nil)
        #expect(second.pendingReceipt() == nil)
    }

    @Test func pendingCandidateDiscardKeepsPriorWorkingCredentialOnRelaunch() throws {
        // Re-pairing an already-paired host that crashes pre-ack (only a .candidate
        // journal) must, on relaunch, keep the prior working credential intact and
        // discard the candidate (issue #1299).
        let store = InMemorySecretStore()
        let (first, local, url) = makeDurableRegistry(store: store)
        first.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        try first.completePairing(hostID: "ben", response: makePairResponse(
            requestID: "r1", deviceID: "dev-1", clientToken: "tok-1", daemonProfile: "p1", tlsPinSPKI: "AQID"))
        try first.beginCandidate(host: try #require(first.host(id: "ben")), response: makePairResponse(
            requestID: "r2", deviceID: "dev-2", clientToken: "tok-2", daemonProfile: "p2", tlsPinSPKI: "BAUG"),
            createdPlaceholder: false)

        let second = HostRegistry(keychain: store, localHost: local, storeURL: url)
        let ben = try #require(second.host(id: "ben"))
        #expect(ben.isPaired == true)
        #expect(ben.tlsPinSPKI == "AQID", "prior pin retained")
        #expect(ben.deviceID == "dev-1", "prior device id retained")
        #expect(second.credentials(for: ben)?.clientToken == "tok-1", "prior working token retained")
        #expect((try? store.string(for: "host.ben.candidateToken")) == nil, "candidate token discarded")
        #expect(second.pendingReceipt() == nil)
    }

    @Test func ackedJournalIsNotPromotedOnRelaunchButAwaitsProbe() throws {
        // A crash after markCandidateAcked but before the commit: the outcome is
        // ambiguous. Relaunch must NOT promote a paired ghost, but must surface the
        // candidate as a pending receipt for the probe-based commit oracle (#1299).
        let store = InMemorySecretStore()
        let (first, local, url) = makeDurableRegistry(store: store)
        first.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", daemonProfile: "prof", tlsPinSPKI: "AQID")
        try first.beginCandidate(host: try #require(first.host(id: "ben")), response: resp, createdPlaceholder: true)
        try first.markCandidateAcked(hostID: "ben")

        let second = HostRegistry(keychain: store, localHost: local, storeURL: url)
        #expect(second.host(id: "ben")?.isPaired != true, "an .acked journal must NOT be promoted to a paired host on relaunch")
        let pending = try #require(second.pendingReceipt(), "the candidate must await the probe oracle")
        #expect(pending.host.id == "ben")
        #expect(pending.credentials.clientToken == "tok-braw")
        #expect(pending.credentials.tlsPinSPKI == "AQID")
        #expect(pending.host.deviceID == "dev-ben")
        #expect(pending.createdPlaceholder == true)
    }

    @Test func commitCandidatePromotesAndClearsJournal() throws {
        // The commit step moves the candidate token to the live account, marks the
        // row paired, and clears the attempt-scoped candidate + journal (#1299).
        let store = InMemorySecretStore()
        let (registry, _, _) = makeDurableRegistry(store: store)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", daemonProfile: "prof", tlsPinSPKI: "AQID")
        try registry.beginCandidate(host: try #require(registry.host(id: "ben")), response: resp, createdPlaceholder: true)
        try registry.markCandidateAcked(hostID: "ben")
        try registry.commitCandidate(hostID: "ben")

        let ben = try #require(registry.host(id: "ben"))
        #expect(ben.isPaired == true)
        #expect(registry.credentials(for: ben)?.clientToken == "tok-braw")
        #expect((try? store.string(for: "host.ben.candidateToken")) == nil, "candidate account cleared after commit")
        #expect(registry.pendingReceipt() == nil, "journal cleared after commit")
    }

    @Test func discardPendingReceiptPreservesPriorPairedRowOnRepair() throws {
        // The probe's discard path (auth rejection during re-pair recovery) must drop
        // only the candidate, never the prior paired row or its live token (#1299).
        let store = InMemorySecretStore()
        let (registry, _, _) = makeDurableRegistry(store: store)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        try registry.completePairing(hostID: "ben", response: makePairResponse(
            requestID: "r1", deviceID: "dev-1", clientToken: "tok-1", daemonProfile: "p1", tlsPinSPKI: "AQID"))
        try registry.beginCandidate(host: try #require(registry.host(id: "ben")), response: makePairResponse(
            requestID: "r2", deviceID: "dev-2", clientToken: "tok-2", daemonProfile: "p2", tlsPinSPKI: "BAUG"),
            createdPlaceholder: false)
        try registry.markCandidateAcked(hostID: "ben")

        registry.discardPendingReceipt(hostID: "ben", createdPlaceholder: false)

        let ben = try #require(registry.host(id: "ben"))
        #expect(ben.isPaired == true)
        #expect(ben.tlsPinSPKI == "AQID")
        #expect(registry.credentials(for: ben)?.clientToken == "tok-1", "prior live credential preserved")
        #expect((try? store.string(for: "host.ben.candidateToken")) == nil)
        #expect(registry.pendingReceipt() == nil)
    }

    @Test func commitCandidateRefusesPromotionWithoutCandidateToken() throws {
        // A missing/empty candidate token must abort promotion — never a paired row
        // with no live token (issue #1299).
        let store = InMemorySecretStore()
        let (registry, _, _) = makeDurableRegistry(store: store)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID")
        try registry.beginCandidate(host: try #require(registry.host(id: "ben")), response: resp, createdPlaceholder: true)
        try registry.markCandidateAcked(hostID: "ben")

        // Wipe the candidate token out from under the commit.
        try store.remove("host.ben.candidateToken")

        #expect(throws: HostRegistryError.self) {
            try registry.commitCandidate(hostID: "ben")
        }
        #expect(registry.host(id: "ben")?.isPaired != true, "no token ⇒ no promotion")
        #expect((try? store.string(for: "host.ben.clientToken")) == nil, "no live token written")
    }

    @Test func beginCandidateRefusesOverwritingAnInFlightJournal() throws {
        // The single attempt-scoped journal must never be overwritten by a second
        // concurrent attempt — fail closed (issue #1299).
        let store = InMemorySecretStore()
        let (registry, _, _) = makeDurableRegistry(store: store)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let r1 = makePairResponse(requestID: "r1", deviceID: "dev-1", clientToken: "tok-1", tlsPinSPKI: "AQID")
        try registry.beginCandidate(host: try #require(registry.host(id: "ben")), response: r1, createdPlaceholder: true)
        try registry.markCandidateAcked(hostID: "ben")

        // A second attempt (same host) must be refused, leaving the first intact.
        let r2 = makePairResponse(requestID: "r2", deviceID: "dev-2", clientToken: "tok-2", tlsPinSPKI: "BAUG")
        #expect(throws: HostRegistryError.self) {
            try registry.beginCandidate(host: try #require(registry.host(id: "ben")), response: r2, createdPlaceholder: false)
        }
        // The first attempt's candidate + journal survive untouched.
        #expect((try? store.string(for: "host.ben.candidateToken")) == "tok-1")
        let pending = try #require(registry.pendingReceipt())
        #expect(pending.credentials.clientToken == "tok-1")
        #expect(pending.host.deviceID == "dev-1")
    }

    @Test func completePairingRefusalPreservesPreExistingReceipt() throws {
        // If a pre-existing in-flight receipt exists for the same host, a
        // completePairing that is refused at beginCandidate must NOT delete that
        // receipt's token/journal — it owns nothing to roll back (issue #1299).
        let store = InMemorySecretStore()
        let (registry, _, _) = makeDurableRegistry(store: store)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let r1 = makePairResponse(requestID: "r1", deviceID: "dev-1", clientToken: "tok-1", tlsPinSPKI: "AQID")
        try registry.beginCandidate(host: try #require(registry.host(id: "ben")), response: r1, createdPlaceholder: true)
        try registry.markCandidateAcked(hostID: "ben")

        let r2 = makePairResponse(requestID: "r2", deviceID: "dev-2", clientToken: "tok-2", tlsPinSPKI: "BAUG")
        #expect(throws: HostRegistryError.self) {
            try registry.completePairing(hostID: "ben", response: r2)
        }
        // The pre-existing receipt is completely intact.
        #expect((try? store.string(for: "host.ben.candidateToken")) == "tok-1", "pre-existing candidate token survives")
        let pending = try #require(registry.pendingReceipt(), "pre-existing journal survives")
        #expect(pending.credentials.clientToken == "tok-1")
    }

    @Test func rollbackAggregatesCandidateCleanupFailure() throws {
        // A commit-stage failure whose rollback ALSO fails to clean up the candidate
        // token must surface via pairingRollbackIncomplete, not silently (issue #1299).
        let store = ThrowOnRemoveStore()
        let fileOps = RecordingFileOps()
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-agg-\(UUID().uuidString)", isDirectory: true)
        let url = dir.appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: store, localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"),
                                    storeURL: url, fileOps: fileOps)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        try registry.completePairing(hostID: "ben", response: makePairResponse(
            requestID: "r1", deviceID: "dev-1", clientToken: "tok-1", daemonProfile: "p1", tlsPinSPKI: "AQID"))

        // Re-pair: let the two journal writes succeed but fail the commit's hosts.json
        // write, and make the candidate-token cleanup during rollback fail too.
        fileOps.armFailure(at: .syncFile, pathContains: "hosts.json")
        store.armRemoveFailure(for: "host.ben.candidateToken")

        var thrown: Error?
        do {
            try registry.completePairing(hostID: "ben", response: makePairResponse(
                requestID: "r2", deviceID: "dev-2", clientToken: "tok-2", daemonProfile: "p2", tlsPinSPKI: "BAUG"))
        } catch { thrown = error }

        guard case .pairingRollbackIncomplete(_, let rollback)? = thrown as? HostRegistryError else {
            Issue.record("expected pairingRollbackIncomplete, got \(String(describing: thrown))")
            return
        }
        #expect(!rollback.isEmpty, "the candidate-cleanup failure must be aggregated")
    }

    @Test func commitJournalRemovalFailureLeavesSelfHealingReceiptNotStuck() throws {
        // If the durable journal removal AFTER a successful promotion fails, the
        // candidate token must NOT be removed — leaving a self-healing receipt
        // (journal + token both present) rather than a journal-present/token-missing
        // permanent block (issue #1299).
        let store = InMemorySecretStore()
        let fileOps = FailingRemoveFileOps()
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-jrm-\(UUID().uuidString)", isDirectory: true)
        let url = dir.appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: store, localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"),
                                    storeURL: url, fileOps: fileOps)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", daemonProfile: "prof", tlsPinSPKI: "AQID")
        try registry.beginCandidate(host: try #require(registry.host(id: "ben")), response: resp, createdPlaceholder: true)
        try registry.markCandidateAcked(hostID: "ben")

        fileOps.setFailRemove(true)
        try registry.commitCandidate(hostID: "ben") // promotion durable; cleanup best-effort

        // The promotion succeeded and is durable.
        let ben = try #require(registry.host(id: "ben"))
        #expect(ben.isPaired == true)
        #expect(registry.credentials(for: ben)?.clientToken == "tok-braw")
        // The journal removal failed, so the token was kept — a self-healing receipt,
        // NOT a stuck journal-present/token-missing block.
        #expect(registry.hasPendingJournal(), "journal remains when its removal failed")
        #expect((try? store.string(for: "host.ben.candidateToken")) == "tok-braw", "token kept ⇒ receipt is self-healing")
        #expect(registry.pendingReceipt() != nil, "still a committable receipt, not a permanent block")
    }

    @Test func relaunchCandidateJournalRemovalFailureRetriesNextLaunch() throws {
        // A .candidate journal whose removal fails on relaunch must be retried on the
        // NEXT launch, never left as a permanent pairing block (issue #1299).
        let store = InMemorySecretStore()
        let fileOps = FailingRemoveFileOps()
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-relaunch-\(UUID().uuidString)", isDirectory: true)
        let url = dir.appendingPathComponent("hosts.json")
        let local = Host.local(socketPath: "/tmp/bothy/graith.sock")
        let first = HostRegistry(keychain: store, localHost: local, storeURL: url, fileOps: fileOps)
        first.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        try first.beginCandidate(host: try #require(first.host(id: "ben")),
                                 response: makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "AQID"),
                                 createdPlaceholder: true)
        // crash before markAcked ⇒ a .candidate journal remains.

        // Relaunch #1: journal removal is broken, so reconcile can't drop it yet.
        fileOps.setFailRemove(true)
        let second = HostRegistry(keychain: store, localHost: local, storeURL: url, fileOps: fileOps)
        #expect(second.hasPendingJournal(), "a failed removal keeps the journal for a later retry")

        // Relaunch #2: removal works ⇒ reconcile discards it. No ghost, no block.
        fileOps.setFailRemove(false)
        let third = HostRegistry(keychain: store, localHost: local, storeURL: url, fileOps: fileOps)
        #expect(!third.hasPendingJournal(), "the next launch retries and clears the .candidate journal")
        #expect(third.host(id: "ben") == nil, "no ghost host after the retry")
    }

    @Test func removeHostRefusesWhenJournalRemovalFailsPreservingCredential() throws {
        // remove(hostID:) must not delete the live token / host while leaving an
        // owned journal behind — that would create a journal-present/token-missing
        // permanent block. On a durable journal-removal failure it refuses entirely,
        // preserving the host + live credential + candidate (issue #1299).
        let store = InMemorySecretStore()
        let fileOps = FailingRemoveFileOps()
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-rmrefuse-\(UUID().uuidString)", isDirectory: true)
        let url = dir.appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: store, localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"),
                                    storeURL: url, fileOps: fileOps)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        // A committed live credential plus an in-flight candidate (re-pair) journal.
        try registry.completePairing(hostID: "ben", response: makePairResponse(
            requestID: "r1", deviceID: "dev-1", clientToken: "tok-1", daemonProfile: "p1", tlsPinSPKI: "AQID"))
        try registry.beginCandidate(host: try #require(registry.host(id: "ben")), response: makePairResponse(
            requestID: "r2", deviceID: "dev-2", clientToken: "tok-2", tlsPinSPKI: "BAUG"), createdPlaceholder: false)

        fileOps.setFailRemove(true)
        registry.remove(hostID: "ben") // must refuse — journal removal can't complete

        // Everything is preserved (no missing-token block created).
        #expect(registry.host(id: "ben") != nil, "host preserved when journal removal fails")
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-1", "live credential preserved")
        #expect((try? store.string(for: "host.ben.candidateToken")) == "tok-2", "candidate preserved")
        #expect(registry.hasPendingJournal(), "journal still present (removal refused)")

        // With removal working, remove succeeds cleanly.
        fileOps.setFailRemove(false)
        registry.remove(hostID: "ben")
        #expect(registry.host(id: "ben") == nil)
        #expect(!registry.hasPendingJournal(), "journal cleared once removal succeeds")
        #expect((try? store.string(for: "host.ben.candidateToken")) == nil)
    }

    @Test func removeAndDiscardRefuseOnUnreadableJournalPreservingEverything() throws {
        // An existing-but-UNREADABLE journal must NOT be treated as absent: remove /
        // discard must refuse rather than delete tokens/host while the journal file
        // remains (which would recreate a journal-present/token-missing block) (#1299).
        let store = InMemorySecretStore()
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-unreadable-\(UUID().uuidString)", isDirectory: true)
        let url = dir.appendingPathComponent("hosts.json")
        let registry = HostRegistry(keychain: store, localHost: Host.local(socketPath: "/tmp/bothy/graith.sock"), storeURL: url)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        try registry.completePairing(hostID: "ben", response: makePairResponse(
            requestID: "r1", deviceID: "dev-1", clientToken: "tok-1", daemonProfile: "p1", tlsPinSPKI: "AQID"))
        try registry.beginCandidate(host: try #require(registry.host(id: "ben")), response: makePairResponse(
            requestID: "r2", deviceID: "dev-2", clientToken: "tok-2", tlsPinSPKI: "BAUG"), createdPlaceholder: false)

        // Make the journal unreadable (permission denied on read).
        let journalURL = dir.appendingPathComponent("pending-pairing.json")
        try FileManager.default.setAttributes([.posixPermissions: 0], ofItemAtPath: journalURL.path)
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: journalURL.path) }

        // discardCandidate must report the read failure and NOT remove the token.
        let discardErrors = registry.discardCandidate(hostID: "ben")
        #expect(!discardErrors.isEmpty, "an unreadable journal must be reported, not silently skipped")
        #expect((try? store.string(for: "host.ben.candidateToken")) == "tok-2", "candidate preserved on unreadable journal")

        // remove(hostID:) must refuse entirely, preserving host + both tokens.
        registry.remove(hostID: "ben")
        #expect(registry.host(id: "ben") != nil, "host preserved on unreadable journal")
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-1", "live token preserved")
        #expect((try? store.string(for: "host.ben.candidateToken")) == "tok-2", "candidate token preserved")
    }

    @Test func remoteHostsPersistAndReload() {
        let store = InMemorySecretStore()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let local = Host.local(socketPath: "/tmp/bothy/graith.sock")

        let first = HostRegistry(keychain: store, localHost: local, storeURL: url)
        first.upsert(Host(id: "ben", label: "ben", kind: .remote,
                          magicDNSName: "ben.tail.ts.net", tlsPinSPKI: "cGlu",
                          deviceID: "dev-ben", isPaired: true))

        // A fresh registry over the same file recovers the remote host + local.
        let second = HostRegistry(keychain: store, localHost: local, storeURL: url)
        #expect(second.hosts.contains { $0.id == "local" })
        #expect(second.host(id: "ben")?.magicDNSName == "ben.tail.ts.net")
    }
}
