import Foundation
import GraithProtocol
@testable import GraithRemoteKit

/// macOS ships `Foundation.Host`, which would make bare `Host` ambiguous in the
/// test module once GraithRemoteKit is imported. Pin it to our type module-wide.
typealias Host = GraithRemoteKit.Host

/// `PairResponseMsg`'s memberwise init is internal to GraithProtocol, so tests
/// build one the way the wire does: decode it from the daemon's JSON shape.
func makePairResponse(
    requestID: String = "req-canny",
    deviceID: String,
    clientToken: String,
    daemonProfile: String = "",
    tlsPinSPKI: String
) -> PairResponseMsg {
    let json = """
    {
      "request_id": "\(requestID)",
      "device_id": "\(deviceID)",
      "client_token": "\(clientToken)",
      "daemon_profile": "\(daemonProfile)",
      "tls_pin_spki": "\(tlsPinSPKI)"
    }
    """
    // Force-try: the literal above is always valid JSON for this type.
    // swiftlint:disable:next force_try
    return try! JSONDecoder().decode(PairResponseMsg.self, from: Data(json.utf8))
}

/// Builds a legacy (pre-receipt) daemon's pair_response: it has NO request_id
/// key, exercising the backward-compatible decoder (issue #1299).
func makeLegacyPairResponse(
    deviceID: String,
    clientToken: String,
    daemonProfile: String = "",
    tlsPinSPKI: String
) -> PairResponseMsg {
    let json = """
    {
      "device_id": "\(deviceID)",
      "client_token": "\(clientToken)",
      "daemon_profile": "\(daemonProfile)",
      "tls_pin_spki": "\(tlsPinSPKI)"
    }
    """
    // swiftlint:disable:next force_try
    return try! JSONDecoder().decode(PairResponseMsg.self, from: Data(json.utf8))
}

/// A canned ``PairingSession`` for driving `PairingCoordinator` without a daemon.
/// It records whether the receipt ack ran and whether it was abandoned, and lets
/// a test inject the commit outcome. `onAck` fires when `ackAndAwaitCommit`
/// begins — used to prove the credential was persisted BEFORE the ack.
final class StubPairingSession: PairingSession, @unchecked Sendable {
    let response: PairResponseMsg
    private let commitOutcome: Result<Void, Error>
    private let onAck: (@Sendable () -> Void)?
    /// When set, `ackAndAwaitCommit` awaits this before returning, so a test can
    /// hold the confirm mid-flight and drive a concurrent reset/pair.
    private let ackGate: Gate?

    private let lock = NSLock()
    private var _ackCount = 0
    private(set) var abandoned = false

    var ackCount: Int {
        lock.lock(); defer { lock.unlock() }
        return _ackCount
    }

    var ackCalled: Bool { ackCount > 0 }

    init(response: PairResponseMsg,
         commitOutcome: Result<Void, Error> = .success(()),
         onAck: (@Sendable () -> Void)? = nil,
         ackGate: Gate? = nil) {
        self.response = response
        self.commitOutcome = commitOutcome
        self.onAck = onAck
        self.ackGate = ackGate
    }

    func ackAndAwaitCommit() async throws {
        lock.lock(); _ackCount += 1; lock.unlock()
        onAck?()
        if let ackGate { await ackGate.wait() }
        switch commitOutcome {
        case .success: return
        case .failure(let error): throw error
        }
    }

    func abandon() async { abandoned = true }
}

/// A one-shot gate a test can open to release a blocked `ackAndAwaitCommit`.
final class Gate: @unchecked Sendable {
    private let sem = DispatchSemaphore(value: 0)
    func wait() async {
        await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
            DispatchQueue.global().async {
                self.sem.wait()
                cont.resume()
            }
        }
    }
    func open() { sem.signal() }
}

/// A pairing backend that records whether `beginPairing` was ever invoked, so a
/// test can prove the coordinator's early fail-closed guard refuses BEFORE opening
/// any pairing flow (issue #1299).
final class RecordingPairing: GraithPairing, @unchecked Sendable {
    private let lock = NSLock()
    private var _beginCalled = false

    var beginCalled: Bool { lock.withLock { _beginCalled } }

    func beginPairing(
        transport: GraithTransport,
        deviceLabel: String,
        profile: String,
        signer: DeviceKeySigner
    ) async throws -> PairingSession {
        lock.withLock { _beginCalled = true }
        throw ControlError.daemon("beginPairing must not be called after a fail-closed refusal")
    }
}

/// A canned pairing backend for driving `PairingCoordinator` without a daemon.
struct StubPairing: GraithPairing {
    enum Outcome: Sendable {
        case succeed(StubPairingSession)
        case fail(ControlError)
    }
    let outcome: Outcome

    func beginPairing(
        transport: GraithTransport,
        deviceLabel: String,
        profile: String,
        signer: DeviceKeySigner
    ) async throws -> PairingSession {
        switch outcome {
        case let .succeed(session): return session
        case let .fail(err): throw err
        }
    }
}

/// A ``SecretStore`` whose next `set` writes the value (side effect) and THEN
/// throws once, modelling a store that partially applies before failing — the
/// transactional rollback must still restore the exact prior secret.
final class ThrowOnceOnSetStore: SecretStore, @unchecked Sendable {
    struct SetFailure: Error {}

    private let backing = InMemorySecretStore()
    private let lock = NSLock()
    private var failNextSet = false

    func armSetFailure() {
        lock.lock(); failNextSet = true; lock.unlock()
    }

    func data(for account: String) throws -> Data? { try backing.data(for: account) }

    func set(_ data: Data, for account: String) throws {
        lock.lock()
        let fail = failNextSet
        failNextSet = false
        lock.unlock()

        try backing.set(data, for: account) // apply the side effect first
        if fail { throw SetFailure() }
    }

    func remove(_ account: String) throws { try backing.remove(account) }
}

/// Thread-safe ordered event recorder shared between ``RecordingFileOps`` and a
/// ``StubPairingSession``'s `onAck`, so a test can assert every durable file step
/// completes before pair_ack is sent (issue #1299).
final class Timeline: @unchecked Sendable {
    private let lock = NSLock()
    private var events: [String] = []
    func record(_ e: String) { lock.withLock { events.append(e) } }
    var all: [String] { lock.withLock { events } }
}

/// A ``DurableFileOps`` that records each step's order (into an optional
/// ``Timeline``) and can inject a one-shot failure at any step, while delegating
/// to real POSIX ops so the store genuinely persists. Lets tests prove (a) write →
/// file sync → rename → dir sync all precede pair_ack, and (b) a failed durable
/// step stays pre-ack and rolls back to the exact prior state (issue #1299).
final class RecordingFileOps: DurableFileOps, @unchecked Sendable {
    enum Step: String { case writeTemp, syncFile, replace, syncDir, removeItem }

    private let real = POSIXFileOps()
    private let lock = NSLock()
    private let timeline: Timeline?
    private var failAt: Step?
    private var failPathSubstring: String?

    init(timeline: Timeline? = nil, failAt: Step? = nil) {
        self.timeline = timeline
        self.failAt = failAt
    }

    /// Arm a one-shot failure at `step` for the next durable write.
    func armFailure(at step: Step) { lock.withLock { failAt = step; failPathSubstring = nil } }

    /// Arm a one-shot failure at `step`, but only for a write whose path contains
    /// `substring` — so a test can target the hosts.json write while letting the
    /// pending-pairing.json journal writes succeed (issue #1299).
    func armFailure(at step: Step, pathContains substring: String) {
        lock.withLock { failAt = step; failPathSubstring = substring }
    }

    struct InjectedFailure: Error { let step: Step }

    private func record(_ step: Step, path: String) throws {
        timeline?.record(step.rawValue)
        let matches = lock.withLock { () -> Bool in
            let m = failAt == step && (failPathSubstring == nil || path.contains(failPathSubstring!))
            if m { failAt = nil; failPathSubstring = nil }
            return m
        }
        if matches { throw InjectedFailure(step: step) }
    }

    func writeTemp(_ data: Data, forDestination destination: URL) throws -> URL {
        try record(.writeTemp, path: destination.path)
        return try real.writeTemp(data, forDestination: destination)
    }

    func syncFile(at url: URL) throws {
        try record(.syncFile, path: url.path)
        try real.syncFile(at: url)
    }

    func replaceItem(at destination: URL, with source: URL) throws {
        try record(.replace, path: destination.path)
        try real.replaceItem(at: destination, with: source)
    }

    func syncDirectory(at url: URL) throws {
        try record(.syncDir, path: url.path)
        try real.syncDirectory(at: url)
    }

    func discardTemp(at url: URL) { real.discardTemp(at: url) }

    func removeItem(at url: URL) throws {
        try record(.removeItem, path: url.path)
        try real.removeItem(at: url)
    }
}

/// A ``DurableFileOps`` that fails `removeItem` on demand while delegating every
/// other op to a real POSIX writer, so a test can force a journal-removal failure
/// and assert the journal-first ordering never leaves a stuck receipt (#1299).
final class FailingRemoveFileOps: DurableFileOps, @unchecked Sendable {
    private let real = POSIXFileOps()
    private let lock = NSLock()
    private var failRemove = false

    func setFailRemove(_ on: Bool) { lock.withLock { failRemove = on } }

    func writeTemp(_ data: Data, forDestination destination: URL) throws -> URL {
        try real.writeTemp(data, forDestination: destination)
    }
    func syncFile(at url: URL) throws { try real.syncFile(at: url) }
    func replaceItem(at destination: URL, with source: URL) throws { try real.replaceItem(at: destination, with: source) }
    func syncDirectory(at url: URL) throws { try real.syncDirectory(at: url) }
    func discardTemp(at url: URL) { real.discardTemp(at: url) }
    func removeItem(at url: URL) throws {
        if lock.withLock({ failRemove }) { throw NSError(domain: "FailingRemoveFileOps", code: 1) }
        try real.removeItem(at: url)
    }
}

/// A ``SecretStore`` whose `remove` fails for armed accounts (delegating reads /
/// writes to an in-memory backing), so a test can force the candidate-token
/// cleanup to fail during a rollback and assert it surfaces via
/// ``HostRegistryError/pairingRollbackIncomplete`` (issue #1299).
final class ThrowOnRemoveStore: SecretStore, @unchecked Sendable {
    struct RemoveFailure: Error {}

    private let backing = InMemorySecretStore()
    private let lock = NSLock()
    private var failAccounts: Set<String> = []

    func armRemoveFailure(for account: String) {
        lock.withLock { _ = failAccounts.insert(account) }
    }

    func data(for account: String) throws -> Data? { try backing.data(for: account) }
    func set(_ data: Data, for account: String) throws { try backing.set(data, for: account) }

    func remove(_ account: String) throws {
        if lock.withLock({ failAccounts.contains(account) }) { throw RemoveFailure() }
        try backing.remove(account)
    }
}

/// A pairing backend that vends a queued session per `beginPairing` call, for
/// tests that drive more than one pairing attempt through one coordinator.
final class QueuePairing: GraithPairing, @unchecked Sendable {
    private let lock = NSLock()
    private var sessions: [StubPairingSession]

    init(_ sessions: [StubPairingSession]) { self.sessions = sessions }

    func beginPairing(
        transport: GraithTransport,
        deviceLabel: String,
        profile: String,
        signer: DeviceKeySigner
    ) async throws -> PairingSession {
        lock.lock(); defer { lock.unlock() }
        return sessions.removeFirst()
    }
}
