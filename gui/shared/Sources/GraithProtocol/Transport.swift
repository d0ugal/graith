import Foundation
import Network

/// Where a ``GraithConnection`` should connect.
///
/// The protocol is identical over both; only the underlying socket differs —
/// this is the transport abstraction that lets the same client serve the
/// local macOS daemon and remote tailnet daemons.
public enum GraithTransport: Sendable, Equatable, Hashable {
    /// The local daemon's Unix domain socket (macOS only). No TLS — the `0700`
    /// socket is the trust boundary, exactly as the `gr` CLI relies on.
    case unix(path: String)
    /// A remote daemon reached over the tailnet: `host:port` with TLS. When
    /// `tlsPinSPKI` is set, the server certificate's SPKI SHA-256 (base64) must
    /// match it (TOFU pinning); this also allows self-signed `interface`-mode
    /// certs. When nil, default system trust evaluation is used.
    case remote(host: String, port: UInt16, tlsPinSPKI: String?)

    /// Whether this transport is a remote (network) connection. Remote
    /// connections perform the PoP `auth_challenge`/`auth_proof` exchange.
    public var isRemote: Bool {
        if case .remote = self { return true }
        return false
    }
}

/// A bidirectional byte stream. ``GraithConnection`` layers framing on top of
/// this, so the concrete socket type (Network.framework, or an in-memory pipe
/// for tests) is hidden behind one small async surface.
public protocol ByteStream: Sendable {
    /// Open the connection, resolving once it is ready to carry bytes.
    func open() async throws
    /// Write bytes to the peer.
    func send(_ data: Data) async throws
    /// Read up to `maxLength` bytes. Returns empty `Data` on a clean EOF.
    func receive(maxLength: Int) async throws -> Data
    /// Close the connection.
    func close()
}

public enum TransportError: Error {
    case notReady(String)
    case failed(String)
}

/// A ``ByteStream`` that can report the SPKI pin of the leaf certificate the
/// peer presented during its TLS handshake.
///
/// The pairing lane opens a first-contact (TOFU) TLS connection with no pin
/// yet, captures the presented leaf's SPKI here, and — before trusting the
/// daemon-reported pin from `pair_response` — confirms the two match. This
/// binds the pin to the endpoint actually spoken to, closing the MITM window
/// of blindly storing whatever pin the reply carried.
public protocol PinCapturingByteStream: ByteStream {
    /// The base64 SHA-256 SPKI pin captured during the TLS handshake, or nil
    /// for a non-TLS transport or before the handshake has produced one.
    func capturedSPKIPin() async -> String?
}

// MARK: - In-memory transport (tests + potential same-process use)

/// A byte buffer shared by one direction of an in-memory duplex pipe.
private actor DirectionBuffer {
    private var data = Data()
    private var closed = false
    private var waiter: (max: Int, cont: CheckedContinuation<Data, Error>)?

    func write(_ d: Data) {
        data.append(d)
        drain()
    }

    func read(maxLength: Int) async throws -> Data {
        if !data.isEmpty {
            return take(maxLength)
        }
        if closed { return Data() }
        return try await withCheckedThrowingContinuation { cont in
            waiter = (maxLength, cont)
        }
    }

    func close() {
        closed = true
        drain()
    }

    private func take(_ maxLength: Int) -> Data {
        let n = Swift.min(maxLength, data.count)
        let out = Data(data.prefix(n))
        data.removeFirst(n)
        return out
    }

    private func drain() {
        guard let w = waiter else { return }
        if !data.isEmpty {
            waiter = nil
            w.cont.resume(returning: take(w.max))
        } else if closed {
            waiter = nil
            w.cont.resume(returning: Data())
        }
    }
}

/// One end of an in-memory duplex connection. Create a connected pair with
/// ``InMemoryByteStream/makePair()``.
public final class InMemoryByteStream: ByteStream, PinCapturingByteStream, @unchecked Sendable {
    private let inbound: DirectionBuffer
    private let outbound: DirectionBuffer
    /// A simulated captured SPKI pin, so the pairing lane's TOFU pin-binding can
    /// be exercised without a real TLS handshake (there is none in-memory).
    private let simulatedPin: String?

    private init(inbound: DirectionBuffer, outbound: DirectionBuffer, simulatedPin: String? = nil) {
        self.inbound = inbound
        self.outbound = outbound
        self.simulatedPin = simulatedPin
    }

    /// Returns two connected endpoints — bytes written to one are read by the
    /// other. Use one as the client stream and one to drive a mock server.
    ///
    /// `clientSimulatedPin` fakes the SPKI the client's TLS handshake would
    /// have captured, so pairing's pin-binding check can be tested end-to-end.
    public static func makePair(clientSimulatedPin: String? = nil) -> (client: InMemoryByteStream, server: InMemoryByteStream) {
        let a = DirectionBuffer()
        let b = DirectionBuffer()
        return (InMemoryByteStream(inbound: a, outbound: b, simulatedPin: clientSimulatedPin),
                InMemoryByteStream(inbound: b, outbound: a))
    }

    public func capturedSPKIPin() async -> String? { simulatedPin }

    public func open() async throws {}

    public func send(_ data: Data) async throws {
        await outbound.write(data)
    }

    public func receive(maxLength: Int) async throws -> Data {
        try await inbound.read(maxLength: maxLength)
    }

    public func close() {
        Task { await outbound.close() }
    }
}

// MARK: - Network.framework transport

/// A ``ByteStream`` backed by an `NWConnection`, serving both the Unix-socket
/// (local) and TCP+TLS (remote) transports.
///
/// > Important: The Network.framework Unix-socket path and the TLS SPKI-pinning
/// > verify block cannot be exercised in this build environment. Both are
/// > flagged in `gui/NEEDS-MAC-VALIDATION.md` for on-device validation, and the
/// > SPKI formula must be reconciled with the daemon's TLS task once it lands.
public final class NWByteStream: ByteStream, PinCapturingByteStream, @unchecked Sendable {
    private let connection: NWConnection
    private let queue = DispatchQueue(label: "com.graith.nwbytestream")
    /// Holds the leaf SPKI captured by the first-contact (pin-less) TLS verify
    /// block, for the pairing lane's TOFU binding. Nil for unix / already-pinned
    /// transports.
    private let pinBox: PinBox?

    public init(transport: GraithTransport) {
        switch transport {
        case let .unix(path):
            let endpoint = NWEndpoint.unix(path: path)
            // A Unix domain stream socket: TCP options select SOCK_STREAM
            // framing; there is no IP layer for a unix endpoint.
            connection = NWConnection(to: endpoint, using: .tcp)
            pinBox = nil
        case let .remote(host, port, pin):
            // NWEndpoint.Port(rawValue:) is nil only for port 0, which is never a
            // valid daemon port; fall back to the default rather than crash.
            let endpoint = NWEndpoint.hostPort(
                host: NWEndpoint.Host(host),
                port: NWEndpoint.Port(rawValue: port) ?? NWEndpoint.Port(rawValue: 4823)!
            )
            // With no pin yet (first contact / pairing) capture the presented
            // leaf SPKI for TOFU binding; once pinned, enforce the pin instead.
            let box = pin == nil ? PinBox() : nil
            let params = NWByteStream.tlsParameters(pinSPKI: pin, captureBox: box)
            connection = NWConnection(to: endpoint, using: params)
            pinBox = box
        }
    }

    public func capturedSPKIPin() async -> String? { pinBox?.get() }

    public func open() async throws {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            let resumed = ManagedAtomicFlag()
            connection.stateUpdateHandler = { state in
                switch state {
                case .ready:
                    if resumed.tryset() { cont.resume() }
                case let .failed(err):
                    if resumed.tryset() { cont.resume(throwing: TransportError.failed("\(err)")) }
                case let .waiting(err):
                    // Surface persistent waiting (e.g. tailnet down) as failure
                    // rather than hanging forever.
                    if resumed.tryset() { cont.resume(throwing: TransportError.notReady("\(err)")) }
                case .cancelled:
                    if resumed.tryset() { cont.resume(throwing: TransportError.failed("cancelled")) }
                default:
                    break
                }
            }
            connection.start(queue: queue)
        }
    }

    public func send(_ data: Data) async throws {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            connection.send(content: data, completion: .contentProcessed { err in
                if let err {
                    cont.resume(throwing: TransportError.failed("\(err)"))
                } else {
                    cont.resume()
                }
            })
        }
    }

    public func receive(maxLength: Int) async throws -> Data {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Data, Error>) in
            issueReceive(maxLength: maxLength, cont: cont)
        }
    }

    /// Issue one `NWConnection.receive` and resume `cont` exactly once. EOF is
    /// surfaced (empty `Data`) *only* when `isComplete` is true. A callback with
    /// no data, no error, and `isComplete == false` is not EOF — it just means
    /// this receive produced nothing yet, so we re-issue rather than reporting a
    /// spurious disconnect upstream. This can't tight-loop: Network.framework
    /// only calls back when there is data or a state change.
    private func issueReceive(maxLength: Int, cont: CheckedContinuation<Data, Error>) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: maxLength) { [weak self] data, _, isComplete, err in
            if let err {
                cont.resume(throwing: TransportError.failed("\(err)"))
                return
            }
            if let data, !data.isEmpty {
                cont.resume(returning: data)
            } else if isComplete {
                cont.resume(returning: Data()) // genuine EOF
            } else if let self {
                // No data, no error, not complete: re-issue; don't fake EOF.
                self.issueReceive(maxLength: maxLength, cont: cont)
            } else {
                cont.resume(returning: Data())
            }
        }
    }

    public func close() {
        connection.cancel()
    }

    /// Build TLS parameters. With `pinSPKI` set, the verify block accepts the
    /// leaf iff its SPKI SHA-256 (base64) matches — this both pins the key and
    /// permits self-signed `interface`-mode certs. With no pin but a
    /// `captureBox` (the pairing first-contact lane), the verify block accepts
    /// the leaf TOFU-style but records its SPKI so the pairing code can bind it
    /// to the daemon-reported pin; it fails closed if the SPKI can't be
    /// extracted.
    private static func tlsParameters(pinSPKI: String?, captureBox: PinBox?) -> NWParameters {
        let tls = NWProtocolTLS.Options()
        // Disable TLS session resumption. Resumed sessions skip the peer-cert
        // verify block, which is where SPKI pinning happens — so a resumed
        // session would bypass the pin. The Go client disables it for the same
        // reason (SessionTicketsDisabled; gosec G123). Available since
        // macOS 10.15 / iOS 13, well below our deployment targets (macOS 14 /
        // iOS 16).
        sec_protocol_options_set_tls_resumption_enabled(tls.securityProtocolOptions, false)
        if let pinSPKI {
            sec_protocol_options_set_verify_block(
                tls.securityProtocolOptions,
                { _, trustRef, complete in
                    let trust = sec_trust_copy_ref(trustRef).takeRetainedValue()
                    let ok = TLSPinning.leafMatchesSPKI(trust, expectedBase64: pinSPKI)
                    complete(ok)
                },
                DispatchQueue(label: "com.graith.tls-verify")
            )
        } else if let captureBox {
            sec_protocol_options_set_verify_block(
                tls.securityProtocolOptions,
                { _, trustRef, complete in
                    let trust = sec_trust_copy_ref(trustRef).takeRetainedValue()
                    if let pin = TLSPinning.leafSPKIBase64(trust) {
                        captureBox.set(pin)
                        complete(true) // TOFU accept; binding confirmed in pairRequest
                    } else {
                        complete(false) // can't extract SPKI → fail closed
                    }
                },
                DispatchQueue(label: "com.graith.tls-verify")
            )
        }
        return NWParameters(tls: tls)
    }
}

/// A tiny thread-safe holder for the SPKI pin captured off the TLS handshake
/// callback (which runs on a private queue) and read back from the actor.
private final class PinBox: @unchecked Sendable {
    private var value: String?
    private let lock = NSLock()
    func set(_ v: String?) { lock.lock(); value = v; lock.unlock() }
    func get() -> String? { lock.lock(); defer { lock.unlock() }; return value }
}

/// A tiny one-shot atomic flag so an `NWConnection` state handler resumes its
/// continuation exactly once even though the handler fires repeatedly.
private final class ManagedAtomicFlag: @unchecked Sendable {
    private var value = false
    private let lock = NSLock()
    func tryset() -> Bool {
        lock.lock(); defer { lock.unlock() }
        if value { return false }
        value = true
        return true
    }
}
