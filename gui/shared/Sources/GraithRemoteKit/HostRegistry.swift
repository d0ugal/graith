import Foundation
import Combine
import GraithProtocol

/// The registry of daemons the app knows about. Persists non-secret host
/// metadata to a JSON file and per-host client tokens to the ``SecretStore``
/// (Keychain). Observable so the SwiftUI shell reacts to add/remove/pair.
///
/// The local host is always present and never removable. Only remote hosts are
/// persisted to disk (the local host is re-seeded from the app at launch, so a
/// changed socket path is never stale).
public enum HostRegistryError: Error {
    /// `completePairing` / `beginCandidate` was called for a host that is not in
    /// the registry (its placeholder was removed before pairing was confirmed).
    case unknownHost(String)
    /// A durable pairing write failed AND the rollback to the prior state was not
    /// fully clean. Carries the original write error plus any rollback errors, so
    /// the caller does not silently believe a clean rollback occurred (issue #1299).
    case pairingRollbackIncomplete(write: Error, rollback: [Error])
    /// `commitCandidate` found no (or an empty) candidate token, so promotion is
    /// refused — a paired row must never exist without its live token (issue #1299).
    case missingCandidateToken(String)
    /// `beginCandidate` refused because another pairing attempt is already in
    /// flight for a host (a pending journal exists). Fail-closed: the single
    /// attempt-scoped journal/candidate must never be overwritten, or a concurrent
    /// attempt could strand the other's committed credential (issue #1299).
    case pairingInFlight(String)
}

/// An attempt-scoped record of a pairing in flight, persisted next to the host
/// store so a crash between the pre-ack durable write and the daemon's commit can
/// be reconciled on the next launch (issue #1299). It never marks a host paired
/// on its own: the candidate token lives under a *separate* Keychain account and
/// the paired row only appears once the candidate is committed, so a crash can
/// neither expose a ghost paired host nor destroy a prior working credential.
struct PendingPairing: Codable {
    enum Stage: String, Codable {
        /// The candidate credential was durably stored but no `pair_ack` was sent
        /// yet — the daemon cannot have committed, so relaunch discards it.
        case candidate
        /// `pair_ack` was (about to be) sent — the daemon may be durable OR may have
        /// timed out, so relaunch does NOT promote it blind (that could be a ghost);
        /// it probes with the candidate credential as the commit oracle.
        case acked
    }

    /// The host row exactly as it should appear once paired (metadata only; the
    /// token lives in the Keychain candidate account). `isPaired` is stored false
    /// and set true only at commit/reconcile.
    var candidateHost: Host
    var stage: Stage
    /// Whether this attempt created a fresh placeholder row. Only such rows are
    /// dropped on a discard — a re-pair of an existing host keeps its prior row.
    var createdPlaceholder: Bool
}

@MainActor
public final class HostRegistry: ObservableObject {
    @Published public private(set) var hosts: [Host] = []

    private let keychain: SecretStore
    /// The local daemon entry, or nil on platforms with no local daemon (iOS,
    /// which only ever talks to remote hosts over the tailnet). When present it
    /// is always kept first and never removable.
    private let localHost: Host?
    private let storeURL: URL
    private let fileOps: DurableFileOps
    private let encoder = JSONEncoder()
    private let decoder = JSONDecoder()

    /// - Parameters:
    ///   - keychain: backing store for per-host client tokens.
    ///   - localHost: the local daemon entry (its socket path is platform-specific,
    ///     so the app resolves it). Always kept present and never removable.
    ///   - storeURL: JSON file for remote-host metadata. Defaults to
    ///     `<ApplicationSupport>/graith-app/hosts.json`.
    ///   - fileOps: durable-write primitives (injected in tests to assert fsync
    ///     ordering / force a failure). Defaults to real POSIX fsync+rename.
    public init(keychain: SecretStore, localHost: Host, storeURL: URL? = nil, fileOps: DurableFileOps = POSIXFileOps()) {
        self.keychain = keychain
        self.localHost = localHost
        self.storeURL = storeURL ?? HostRegistry.defaultStoreURL()
        self.fileOps = fileOps
        load()
    }

    /// Remote-only registry for platforms with no local daemon (iOS). Identical
    /// to the designated init but seeds no local host, so `hosts` is exactly the
    /// persisted set of paired remotes.
    public init(keychain: SecretStore, storeURL: URL? = nil, fileOps: DurableFileOps = POSIXFileOps()) {
        self.keychain = keychain
        self.localHost = nil
        self.storeURL = storeURL ?? HostRegistry.defaultStoreURL()
        self.fileOps = fileOps
        load()
    }

    // MARK: - Query

    public func host(id: String) -> Host? {
        hosts.first { $0.id == id }
    }

    /// Build the credentials to present when connecting to `host`, reading the
    /// client token from the store. Returns nil for a local host (it connects
    /// tokenless over the Unix socket) and for a remote host that isn't fully
    /// paired.
    ///
    /// Security invariant: an authenticated connection must never run in
    /// accept-any-cert (TOFU) mode. A host with a token but an empty
    /// `tlsPinSPKI` would connect with a nil pin, which the transport treats as
    /// first-contact TOFU — so we refuse to vend credentials for it and treat
    /// it as unpaired. (The tokenless pairing lane doesn't go through here, so
    /// it keeps working.)
    public func credentials(for host: Host) -> HostCredentials? {
        guard host.kind == .remote else { return nil }
        guard let token = try? keychain.string(for: Self.tokenAccount(host.id)), !token.isEmpty else {
            return nil
        }
        guard !host.tlsPinSPKI.isEmpty else {
            NSLog("HostRegistry: refusing credentials for host \(host.id) — token present but no TLS pin (would connect in accept-any mode); re-pair required")
            return nil
        }
        return HostCredentials(
            clientToken: token,
            deviceID: host.deviceID,
            daemonProfile: host.daemonProfile,
            tlsPinSPKI: host.tlsPinSPKI
        )
    }

    // MARK: - Mutations

    /// Add or replace a host entry (metadata only; token set separately at
    /// pairing). The local host cannot be replaced through here.
    public func upsert(_ host: Host) {
        guard host.id != localHost?.id else { return }
        setHostInMemory(host)
        persist()
    }

    /// Record the result of a successful pairing in one synchronous transaction:
    /// durably store the candidate, mark it acked, and commit it (token → live
    /// account + paired row). Used by direct callers (tests, smoke, and any flow
    /// with no separate async ack step). The async receipt flow instead drives
    /// ``beginCandidate(host:response:createdPlaceholder:)`` /
    /// ``markCandidateAcked(hostID:)`` / ``commitCandidate(hostID:)`` around the
    /// live `pair_ack`.
    ///
    /// All-or-nothing: any failure rolls back to the exact prior token + row, so a
    /// failed re-pair of an existing host never loses its working credential.
    public func completePairing(hostID: String, response: PairResponseMsg) throws {
        guard let host = host(id: hostID) else {
            throw HostRegistryError.unknownHost(hostID)
        }

        // Snapshot the exact prior live state so a failure anywhere in the
        // transaction restores it (prior-secret rollback, issue #1299). The read
        // is `try` so a snapshot failure aborts before we mutate anything.
        let priorHost = host
        let priorToken = try keychain.string(for: Self.tokenAccount(hostID))

        // beginCandidate is the ownership boundary: if it throws (including a
        // pairingInFlight refusal against a PRE-EXISTING receipt), this transaction
        // created nothing, so it must NOT roll back / discard — that would delete
        // another attempt's in-flight candidate. beginCandidate already cleans up
        // its own partial write. Only after it succeeds do we own the candidate.
        try beginCandidate(host: host, response: response, createdPlaceholder: false)

        do {
            try markCandidateAcked(hostID: hostID)
            try commitCandidate(hostID: hostID)
        } catch {
            // Restore the exact prior row + token, AND aggregate EVERY rollback
            // failure — candidate token, journal, secret restore, and re-persist —
            // so the caller never silently believes a clean rollback occurred
            // (fail-closed contract, issue #1299).
            var rollbackErrors: [Error] = []
            rollbackErrors.append(contentsOf: discardCandidate(hostID: hostID))
            setHostInMemory(priorHost)
            do {
                if let priorToken {
                    try keychain.set(priorToken, for: Self.tokenAccount(hostID))
                } else {
                    try keychain.remove(Self.tokenAccount(hostID))
                }
            } catch { rollbackErrors.append(error) }
            do {
                // Re-persist the restored row so the on-disk JSON matches memory.
                // Pre-commit failures never touch hosts.json (only commitCandidate
                // writes it), but a commit-stage failure may have — so restore it.
                try persistThrowing()
            } catch { rollbackErrors.append(error) }

            if rollbackErrors.isEmpty { throw error }
            throw HostRegistryError.pairingRollbackIncomplete(write: error, rollback: rollbackErrors)
        }
    }

    /// Pre-ack step: durably record the attempt-scoped candidate credential (token
    /// in a *separate* Keychain account) plus a `.candidate` journal. Touches
    /// neither the live token account nor the paired row, so a failure here — or a
    /// crash before the ack — leaves any prior working credential intact and
    /// exposes no ghost paired host (relaunch discards a `.candidate` journal).
    public func beginCandidate(host: Host, response: PairResponseMsg, createdPlaceholder: Bool) throws {
        guard hosts.contains(where: { $0.id == host.id }) else {
            throw HostRegistryError.unknownHost(host.id)
        }

        // Fail closed: refuse to overwrite an existing attempt's journal/candidate.
        // Gate on file EXISTENCE (not a decoded read) — a corrupt/unreadable journal
        // must still block, or a `try? readJournal()` nil would silently clobber a
        // real in-flight receipt. The single attempt-scoped journal holds one
        // in-flight pairing; a second (concurrent / cross-generation) attempt must
        // not clobber it (that could strand the first's committed credential, or make
        // a stale confirm promote the wrong token) (issue #1299).
        if hasPendingJournal() {
            throw HostRegistryError.pairingInFlight(host.id)
        }

        var candidate = host
        candidate.isPaired = false
        candidate.daemonProfile = response.daemonProfile
        candidate.tlsPinSPKI = response.tlsPinSPKI
        candidate.deviceID = response.deviceID

        let journal = PendingPairing(candidateHost: candidate, stage: .candidate, createdPlaceholder: createdPlaceholder)
        do {
            try keychain.set(response.clientToken, for: Self.candidateTokenAccount(host.id))
            try writeJournal(journal)
        } catch {
            // Nothing live was touched, so the clean rollback is just dropping the
            // candidate token; the prior credential + row are inherently intact. If
            // that cleanup ALSO fails, surface it (fail-closed, issue #1299).
            let cleanup = discardCandidate(hostID: host.id)
            if cleanup.isEmpty { throw error }
            throw HostRegistryError.pairingRollbackIncomplete(write: error, rollback: cleanup)
        }
    }

    /// Pre-ack step: durably transition the journal `.candidate` → `.acked` BEFORE
    /// the `pair_ack` is sent, so a crash after the ack is reconciled as
    /// commit-unknown/retain rather than discarded (issue #1299).
    public func markCandidateAcked(hostID: String) throws {
        guard var journal = try readJournal(), journal.candidateHost.id == hostID else {
            throw HostRegistryError.unknownHost(hostID)
        }
        journal.stage = .acked
        try writeJournal(journal)
    }

    /// Commit step: promote the candidate to the live paired host (candidate token
    /// → live account, paired row written durably), then clear the attempt-scoped
    /// journal + candidate token. Idempotent: a missing journal for an
    /// already-paired host is a no-op success.
    ///
    /// Post-ack this RETAINS on any failure (the daemon may already be durable):
    /// it does not roll back, so the in-memory promotion lets the credential work
    /// this session and the `.acked` journal survives for a relaunch to reconcile.
    public func commitCandidate(hostID: String) throws {
        guard let journal = try readJournal(), journal.candidateHost.id == hostID else {
            // Already committed by a prior call/reconcile, or never begun.
            if host(id: hostID)?.isPaired == true { return }
            throw HostRegistryError.unknownHost(hostID)
        }

        // Require a non-empty candidate token via a THROWING read BEFORE any
        // promotion — a paired row must never exist without its live token, and a
        // read failure must abort rather than silently produce a token-less pairing
        // (issue #1299).
        guard let candidateToken = try keychain.string(for: Self.candidateTokenAccount(hostID)),
              !candidateToken.isEmpty else {
            throw HostRegistryError.missingCandidateToken(hostID)
        }
        try keychain.set(candidateToken, for: Self.tokenAccount(hostID))

        var paired = journal.candidateHost
        paired.isPaired = true
        setHostInMemory(paired)
        try persistThrowing()

        // Promotion is durable. Drop the attempt-scoped artifacts JOURNAL-FIRST,
        // removing the candidate token ONLY after the journal is durably gone. This
        // ordering (not propagation) is what guarantees no stuck state: a
        // journal-removal failure leaves BOTH artifacts (a self-healing receipt the
        // next launch's probe re-commits idempotently), and a success leaves at most
        // a harmless orphan token — neither is journal-present/token-missing. We do
        // NOT throw here: the commit already succeeded durably, and propagating would
        // wrongly revert it in `completePairing`'s all-or-nothing catch (issue #1299).
        if (try? removeJournalDurably()) != nil {
            try? keychain.remove(Self.candidateTokenAccount(hostID))
        }
    }

    /// Drop an attempt-scoped candidate (token + journal) without touching the live
    /// token or paired row. Used when a pre-ack step fails and no ack was sent.
    /// Returns any cleanup failures so a transactional caller can aggregate them
    /// into ``HostRegistryError/pairingRollbackIncomplete`` (issue #1299).
    @discardableResult
    public func discardCandidate(hostID: String) -> [Error] {
        // Journal FIRST (durable, ownership-gated), then the candidate token — so a
        // stray discard can never delete another attempt's receipt nor leave a
        // journal-present/token-missing permanent block (issue #1299).
        removeCandidateArtifacts(hostID: hostID, ownedOnly: true)
    }

    /// Update the last-seen timestamp for a host (display only).
    public func markSeen(hostID: String, at date: Date) {
        guard let idx = hosts.firstIndex(where: { $0.id == hostID }) else { return }
        hosts[idx].lastSeen = date
        persist()
    }

    /// Remove a host and wipe its client token (and any leftover candidate token)
    /// from the store. The local host is never removed.
    public func remove(hostID: String) {
        guard hostID != localHost?.id else { return }
        // If this host has an in-flight candidate journal, remove it durably FIRST.
        // If that removal fails (or the journal is unreadable), REFUSE the whole
        // removal — preserving the host, live token, and candidate — and log it,
        // rather than delete the live token while leaving the journal, which would
        // create a journal-present/token-missing permanent pairing block (issue #1299).
        do {
            if let journal = try readJournal(), journal.candidateHost.id == hostID {
                try removeJournalDurably()
            }
        } catch {
            NSLog("HostRegistry.remove(\(hostID)): refusing removal — could not durably clear the pending journal: \(error)")
            return
        }
        hosts.removeAll { $0.id == hostID }
        try? keychain.remove(Self.tokenAccount(hostID))
        try? keychain.remove(Self.candidateTokenAccount(hostID))
        persist()
    }

    // MARK: - Persistence

    private func setHostInMemory(_ host: Host) {
        if let idx = hosts.firstIndex(where: { $0.id == host.id }) {
            hosts[idx] = host
        } else {
            hosts.append(host)
        }
    }

    private func load() {
        var remotes: [Host] = []
        if let data = try? Data(contentsOf: storeURL),
           let decoded = try? decoder.decode([Host].self, from: data) {
            remotes = decoded.filter { $0.kind == .remote }
        }
        // Local host always first (when present), then persisted remotes.
        hosts = [localHost].compactMap { $0 } + remotes
        reconcilePendingPairing()
    }

    /// Reconcile a pairing that was in flight when the app last exited (issue
    /// #1299).
    ///
    /// A `.candidate` journal means no `pair_ack` was ever sent, so the daemon
    /// cannot have committed — discard it here. An `.acked` journal is ambiguous
    /// (the daemon may have committed, or may have timed out before the ack
    /// arrived), and it is NOT resolvable without contacting the daemon: promoting
    /// it here would recreate the pre-ACK ghost in the crash-before-send window. So
    /// it is deliberately left un-promoted for a probe — see ``pendingReceipt()``,
    /// which the connection layer uses as the commit oracle (authenticated access
    /// proves the daemon committed).
    private func reconcilePendingPairing() {
        guard let journal = try? readJournal() else { return }
        let hostID = journal.candidateHost.id

        switch journal.stage {
        case .candidate:
            // The daemon cannot have committed. Drop the attempt JOURNAL-FIRST
            // (durable) then the candidate token, so a partial cleanup can't leave a
            // journal-present/token-missing block. The live token + prior row were
            // never touched, so a prior working credential survives.
            removeCandidateArtifacts(hostID: hostID, ownedOnly: true)
            if journal.createdPlaceholder {
                hosts.removeAll { $0.id == hostID && $0.kind == .remote && !$0.isPaired }
            }
            persist()
        case .acked:
            // Ambiguous — leave the journal + candidate for a probe-based commit
            // oracle. Do NOT mark the host paired here (no ghost).
            break
        }
    }

    /// A pending receipt awaiting the probe-based commit oracle (issue #1299): an
    /// `.acked` candidate that has not been confirmed committed by the daemon. The
    /// connection layer attempts an authenticated connection with these
    /// credentials; auth success proves the daemon committed (→ ``commitCandidate``),
    /// auth rejection proves it did not (→ ``discardPendingReceipt``).
    public struct PendingReceipt: Sendable {
        public let host: Host
        public let credentials: HostCredentials
        public let createdPlaceholder: Bool
    }

    /// Whether ANY pending-pairing journal exists, independent of whether the
    /// candidate token is currently readable. The authoritative "an attempt is in
    /// flight" signal for the coordinator's fail-closed guard — `pendingReceipt()`
    /// returns nil on a transient token-read failure even though a receipt is still
    /// outstanding, so this file-existence check must gate a new attempt (#1299).
    public func hasPendingJournal() -> Bool {
        FileManager.default.fileExists(atPath: journalURL.path)
    }

    /// The `.acked` candidate awaiting a commit probe, or nil if none / its
    /// candidate token is missing.
    public func pendingReceipt() -> PendingReceipt? {
        guard let journal = try? readJournal(), journal.stage == .acked else { return nil }
        guard let token = (try? keychain.string(for: Self.candidateTokenAccount(journal.candidateHost.id))) ?? nil,
              !token.isEmpty, !journal.candidateHost.tlsPinSPKI.isEmpty else {
            return nil
        }
        let h = journal.candidateHost
        let creds = HostCredentials(
            clientToken: token,
            deviceID: h.deviceID,
            daemonProfile: h.daemonProfile,
            tlsPinSPKI: h.tlsPinSPKI
        )
        return PendingReceipt(host: h, credentials: creds, createdPlaceholder: journal.createdPlaceholder)
    }

    /// Discard a pending receipt whose probe proved the daemon never committed
    /// (auth rejection). Drops the candidate token + journal and, if this attempt
    /// created the placeholder, its unpaired row. Never touches an existing prior
    /// paired row or its live token, so a re-pair recovery preserves the previously
    /// working credential (issue #1299).
    public func discardPendingReceipt(hostID: String, createdPlaceholder: Bool) {
        discardCandidate(hostID: hostID)
        if createdPlaceholder {
            hosts.removeAll { $0.id == hostID && $0.kind == .remote && !$0.isPaired }
            persist()
        }
    }

    /// persistThrowing writes the remote-host metadata durably (fsync-backed temp
    /// file + atomic rename + parent-directory fsync via ``DurableFileOps``),
    /// surfacing any I/O error. Completion (pairing) uses this so the receipt ACK
    /// is only released once the metadata is durable (issue #1299); display-only
    /// mutations use the swallowing `persist()` below.
    private func persistThrowing() throws {
        // Only remote hosts are persisted — the local host is re-seeded at launch.
        let remotes = hosts.filter { $0.kind == .remote }
        let data = try encoder.encode(remotes)
        try fileOps.writeDurably(data, to: storeURL)
    }

    private func persist() {
        do {
            try persistThrowing()
        } catch {
            // Non-fatal for display-only mutations: the registry stays in memory.
            NSLog("HostRegistry persist failed: \(error)")
        }
    }

    // MARK: - Pending-pairing journal

    private var journalURL: URL {
        storeURL.deletingLastPathComponent().appendingPathComponent("pending-pairing.json")
    }

    /// Read the pending-pairing journal. ABSENCE is the only `nil` result: an
    /// existing-but-unreadable (permission) or corrupt journal THROWS, so cleanup
    /// paths refuse rather than treat it as absent and delete tokens/hosts while the
    /// journal file remains — which would recreate a journal-present/token-missing
    /// block (issue #1299).
    private func readJournal() throws -> PendingPairing? {
        guard FileManager.default.fileExists(atPath: journalURL.path) else { return nil }
        let data = try Data(contentsOf: journalURL)
        return try decoder.decode(PendingPairing.self, from: data)
    }

    private func writeJournal(_ journal: PendingPairing) throws {
        let data = try encoder.encode(journal)
        try fileOps.writeDurably(data, to: journalURL)
    }

    /// Durably remove the pending-pairing journal (unlink + parent-dir fsync via
    /// the injected file ops), surfacing an I/O failure. A lingering journal blocks
    /// new pairings (`hasPendingJournal`), so its removal must be durable and its
    /// failure must propagate — never swallowed (issue #1299). Missing = no-op.
    private func removeJournalDurably() throws {
        try fileOps.removeItem(at: journalURL)
    }

    /// Remove a candidate's attempt-scoped artifacts in the ONLY safe order:
    /// the journal FIRST (durably), then the candidate secret. A
    /// journal-present/token-missing state permanently blocks pairing (the journal
    /// signals "in flight" while the missing token makes it uncommittable), so the
    /// journal — the blocking signal — must go first and durably; if it can't be
    /// removed we stop before touching the token, leaving a self-healing pending
    /// receipt rather than a stuck one (issue #1299).
    ///
    /// When `ownedOnly`, the journal is removed only if it belongs to `hostID`, and
    /// a read/decode failure is REPORTED (not silently skipped) so a stray discard
    /// can never claim a clean cleanup it didn't perform.
    @discardableResult
    private func removeCandidateArtifacts(hostID: String, ownedOnly: Bool) -> [Error] {
        var errors: [Error] = []
        do {
            if ownedOnly {
                let journal = try readJournal()
                if journal?.candidateHost.id == hostID {
                    try removeJournalDurably()
                } else if journal == nil {
                    // No journal — nothing to remove; fall through to token cleanup.
                }
                // A journal owned by a different host is left untouched.
            } else if FileManager.default.fileExists(atPath: journalURL.path) {
                try removeJournalDurably()
            }
        } catch {
            // Could not read/decide ownership OR the durable removal failed. Fail
            // closed: report it and do NOT remove the token, so we never create a
            // journal-present/token-missing permanent block.
            errors.append(error)
            return errors
        }
        do { try keychain.remove(Self.candidateTokenAccount(hostID)) } catch { errors.append(error) }
        return errors
    }

    private static func tokenAccount(_ hostID: String) -> String {
        "host.\(hostID).clientToken"
    }

    private static func candidateTokenAccount(_ hostID: String) -> String {
        "host.\(hostID).candidateToken"
    }

    private static func defaultStoreURL() -> URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? FileManager.default.temporaryDirectory
        return base.appendingPathComponent("graith-app", isDirectory: true)
            .appendingPathComponent("hosts.json")
    }
}
