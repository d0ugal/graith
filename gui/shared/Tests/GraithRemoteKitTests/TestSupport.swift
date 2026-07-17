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
