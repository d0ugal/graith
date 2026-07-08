import Foundation

/// A single framed connection to a graith daemon.
///
/// Wraps a ``ByteStream`` with the 5-byte framing, performs the handshake and
/// (on remote transports) the proof-of-possession exchange, and then exposes:
/// - ``request(_:payload:)`` — send a control message and await its reply,
/// - ``send(control:payload:)`` — fire-and-forget a control message,
/// - ``sendData(_:)`` / ``dataStream`` — the raw PTY byte channel (0x01),
/// - ``events`` — unsolicited control notifications (`detached`,
///   `approval_notification`).
///
/// Because the daemon handler is *not* fully multiplexed (some ops take over
/// the connection in blocking read loops), a client opens several of these —
/// one for control RPCs, one per attached terminal, one for the event/approval
/// subscription — rather than multiplexing everything onto one. See
/// ``GraithProtocolClient``.
public actor GraithConnection {
    public let transport: GraithTransport

    private let stream: ByteStream
    private let token: String?
    private var decoder = FrameDecoder()

    /// Control envelopes treated as unsolicited pushes rather than RPC replies.
    private static let unsolicitedTypes: Set<String> = ["detached", "approval_notification"]

    // Reply plumbing: replies are FIFO. If a reply arrives before anyone is
    // waiting (a benign race on a fast local socket), it is buffered so the
    // next `nextReply()` consumes it in order.
    private var replyWaiters: [CheckedContinuation<ControlEnvelope, Error>] = []
    private var bufferedReplies: [ControlEnvelope] = []

    // RPC serialization: replies are matched to callers purely by FIFO order,
    // so a request's send + reply-await must be atomic with respect to other
    // requests on the same connection. Without this gate, actor reentrancy at
    // the `await send(...)` suspension point lets a second `request()` interleave
    // its send between the first's send and its waiter enqueue, mis-routing
    // reply N to caller M. `rpcBusy`/`rpcQueue` form a tiny async mutex so only
    // one RPC round-trip is in flight at a time (mirrors the Go client, which
    // owns its connection from one goroutine).
    private var rpcBusy = false
    private var rpcQueue: [CheckedContinuation<Void, Never>] = []

    private var dataContinuation: AsyncStream<Data>.Continuation?
    private var eventContinuation: AsyncStream<ControlEnvelope>.Continuation?
    /// Raw PTY output frames (channel 0x01) from the daemon.
    public nonisolated let dataStream: AsyncStream<Data>
    /// Unsolicited control pushes (`detached`, `approval_notification`).
    public nonisolated let events: AsyncStream<ControlEnvelope>

    private var closed = false

    public init(transport: GraithTransport, stream: ByteStream, token: String?) {
        self.transport = transport
        self.stream = stream
        self.token = token

        var dataCont: AsyncStream<Data>.Continuation!
        dataStream = AsyncStream { dataCont = $0 }
        var evCont: AsyncStream<ControlEnvelope>.Continuation!
        events = AsyncStream { evCont = $0 }
        dataContinuation = dataCont
        eventContinuation = evCont
    }

    /// Convenience initializer that builds the appropriate ``ByteStream`` for
    /// the transport (Network.framework).
    public init(transport: GraithTransport, token: String?) {
        self.init(transport: transport, stream: NWByteStream(transport: transport), token: token)
    }

    // MARK: - Connect

    /// Open the stream, handshake, and (for remote transports) complete PoP.
    ///
    /// - Parameters:
    ///   - handshake: the handshake message (version/profile/terminal size).
    ///   - signer: device key signer, required for remote transports to answer
    ///     the `auth_challenge`. Ignored for local (Unix) transports.
    /// - Returns: the daemon's `handshake_ok` payload.
    @discardableResult
    public func connect(handshake: HandshakeMsg, signer: DeviceKeySigner? = nil) async throws -> HandshakeOkMsg {
        // Security invariant: an authenticated remote connection must present a
        // TLS pin. A token with no pin would run the accept-any-cert (TOFU)
        // verify block, exposing the bearer token to a MITM. The tokenless
        // pairing lane (token == nil/empty) is the only legitimate TOFU path.
        if case let .remote(_, _, pin) = transport, token?.isEmpty == false,
           (pin ?? "").isEmpty {
            throw ControlError.malformed("authenticated remote connection requires a TLS pin (refusing accept-any-cert)")
        }
        try await stream.open()
        startReceiveLoop()

        try await send(control: "handshake", payload: handshake)
        let resp = try await nextReply()
        switch resp.type {
        case "handshake_ok":
            break
        case "handshake_err":
            let e = try decodePayload(resp, as: HandshakeErrMsg.self)
            throw ControlError.handshakeRejected(e.reason)
        default:
            throw ControlError.unexpectedReply(resp.type)
        }
        let ok = try decodePayload(resp, as: HandshakeOkMsg.self)

        // Proof-of-possession runs only on an authenticated remote connection —
        // one that already carries a client token. The pairing lane (token == nil)
        // must NOT run PoP: a brand-new device has no device record yet, so the
        // daemon would reject auth_proof, and the pairing exchange never needs it.
        if transport.isRemote, token?.isEmpty == false {
            try await completeProofOfPossession(signer: signer)
        }
        return ok
    }

    /// Answer the daemon's `auth_challenge` with an `auth_proof` (design B.2.4).
    private func completeProofOfPossession(signer: DeviceKeySigner?) async throws {
        guard let signer else {
            throw ControlError.malformed("remote connection requires a DeviceKeySigner for proof-of-possession")
        }
        let challengeEnv = try await nextReply()
        guard challengeEnv.type == "auth_challenge" else {
            throw ControlError.unexpectedReply("expected auth_challenge, got \(challengeEnv.type)")
        }
        let challenge = try decodePayload(challengeEnv, as: AuthChallengeMsg.self)
        let proof = try signer.proof(forNonce: challenge.nonce)
        try await send(control: "auth_proof", payload: proof)

        // The daemon replies `auth_ok` on a valid proof (handler.go) — consume it
        // here, otherwise it would be mistaken for the reply to the first real
        // RPC (list/attach), throwing every subsequent read off by one. A bad
        // proof yields an `error`.
        let ack = try await nextReply()
        switch ack.type {
        case "auth_ok":
            return
        case "error":
            let e = try decodePayload(ack, as: ErrorMsg.self)
            throw ControlError.daemon(e.message)
        default:
            throw ControlError.unexpectedReply("expected auth_ok, got \(ack.type)")
        }
    }

    // MARK: - RPC

    /// Send a control message and await its reply envelope. Throws
    /// ``ControlError/daemon(_:)`` if the daemon replied with `error`.
    @discardableResult
    public func request<P: Encodable>(_ type: String, payload: P) async throws -> ControlEnvelope {
        await acquireRPC()
        defer { releaseRPC() }
        try await send(control: type, payload: payload)
        let reply = try await nextReply()
        if reply.type == "error" {
            let e = try decodePayload(reply, as: ErrorMsg.self)
            throw ControlError.daemon(e.message)
        }
        return reply
    }

    /// Send a control message with no payload and await its reply.
    @discardableResult
    public func request(_ type: String) async throws -> ControlEnvelope {
        try await request(type, payload: EmptyMsg())
    }

    /// Fire-and-forget a control message with a payload.
    public func send<P: Encodable>(control type: String, payload: P) async throws {
        let data = try encodeControl(type, payload, token: token)
        try await rawSend(channel: Channel.control, payload: data)
    }

    /// Fire-and-forget a control message with no payload.
    public func send(control type: String) async throws {
        let data = try encodeControl(type, token: token)
        try await rawSend(channel: Channel.control, payload: data)
    }

    /// Send raw bytes on the data channel (0x01) — keystrokes during an attach.
    public func sendData(_ data: Data) async throws {
        try await rawSend(channel: Channel.data, payload: data)
    }

    /// Await the next control reply envelope, skipping the unsolicited pushes
    /// that are routed to ``events``.
    ///
    /// Exposed for the token-less pairing lane: a brand-new device skips
    /// proof-of-possession, so the daemon's unsolicited `auth_challenge` (which
    /// it sends after `handshake_ok` on *every* remote connection) is left
    /// buffered and must be consumed before the `pair_response`. Mirrors the Go
    /// client's pairing loop (`internal/client/remote.go`). Callers that use
    /// this must not also use ``request(_:payload:)`` concurrently on the same
    /// connection — the pairing connection is single-purpose and short-lived.
    public func nextControlReply() async throws -> ControlEnvelope {
        try await nextReply()
    }

    /// The SPKI pin (base64 SHA-256) of the leaf certificate the peer presented
    /// during this connection's TLS handshake, or nil for a non-TLS transport
    /// or a stream that does not capture it. Used by the pairing lane to bind
    /// TOFU: the captured pin must equal the pin the daemon reports in
    /// `pair_response`.
    public func capturedSPKIPin() async -> String? {
        guard let capturing = stream as? PinCapturingByteStream else { return nil }
        return await capturing.capturedSPKIPin()
    }

    public func close() {
        guard !closed else { return }
        closed = true
        stream.close()
        dataContinuation?.finish()
        eventContinuation?.finish()
        let waiters = replyWaiters
        replyWaiters.removeAll()
        for w in waiters { w.resume(throwing: FrameError.closed) }
        // Release anyone blocked on the RPC mutex so they unwind (their send /
        // reply-await then throws on the now-closed stream).
        let queued = rpcQueue
        rpcQueue.removeAll()
        rpcBusy = false
        for c in queued { c.resume() }
    }

    // MARK: - Internals

    private func rawSend(channel: UInt8, payload: Data) async throws {
        let frame = try encodeFrame(channel: channel, payload: payload)
        try await stream.send(frame)
    }

    /// Acquire the per-connection RPC mutex, suspending until it is free.
    private func acquireRPC() async {
        if !rpcBusy {
            rpcBusy = true
            return
        }
        await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
            rpcQueue.append(cont)
        }
    }

    /// Release the RPC mutex, handing it to the next queued waiter if any.
    private func releaseRPC() {
        if rpcQueue.isEmpty {
            rpcBusy = false
        } else {
            rpcQueue.removeFirst().resume()
        }
    }

    private func nextReply() async throws -> ControlEnvelope {
        if !bufferedReplies.isEmpty {
            return bufferedReplies.removeFirst()
        }
        if closed { throw FrameError.closed }
        return try await withCheckedThrowingContinuation { cont in
            replyWaiters.append(cont)
        }
    }

    private func startReceiveLoop() {
        Task { await self.receiveLoop() }
    }

    private func receiveLoop() async {
        while !closed {
            let chunk: Data
            do {
                chunk = try await stream.receive(maxLength: 64 * 1024)
            } catch {
                break
            }
            if chunk.isEmpty { break } // EOF
            decoder.append(chunk)
            do {
                while let frame = try decoder.next() {
                    route(frame)
                }
            } catch {
                break // oversized frame / protocol error
            }
        }
        close()
    }

    private func route(_ frame: Frame) {
        switch frame.channel {
        case Channel.data:
            dataContinuation?.yield(frame.payload)
        case Channel.control:
            guard let env = try? decodeControl(frame.payload) else { return }
            if Self.unsolicitedTypes.contains(env.type) {
                eventContinuation?.yield(env)
            } else if !replyWaiters.isEmpty {
                replyWaiters.removeFirst().resume(returning: env)
            } else {
                bufferedReplies.append(env)
            }
        default:
            break // never open channel 0x02 (MCP); ignore anything else
        }
    }
}
