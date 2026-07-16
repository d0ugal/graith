import Foundation

/// A live attach to one session over its own dedicated connection.
///
/// Terminal output arrives on ``output`` (raw channel-0x01 bytes, including the
/// initial scrollback tail the daemon replays on attach). Keystrokes go back
/// via ``send(_:)`` and window size changes via ``resize(cols:rows:)``.
public struct AttachSession: Sendable {
    /// The `attached` session info the daemon returned.
    public let session: SessionInfo
    /// Raw PTY output bytes (channel 0x01), in order.
    public let output: AsyncStream<Data>
    /// Unsolicited control pushes on the attach connection (notably
    /// `detached`, when the attach is replaced or the session stops).
    public let events: AsyncStream<ControlEnvelope>

    private let connection: GraithConnection

    init(session: SessionInfo, connection: GraithConnection) {
        self.session = session
        self.connection = connection
        self.output = connection.dataStream
        self.events = connection.events
    }

    /// Send keystroke bytes to the session's PTY.
    public func send(_ data: Data) async throws {
        try await connection.sendData(data)
    }

    /// Tell the daemon to resize the session's PTY (design C.3 — no local
    /// `TIOCSWINSZ`; the app computes cols/rows and sends a control message).
    public func resize(cols: UInt16, rows: UInt16) async throws {
        try await connection.send(control: "resize", payload: ResizeMsg(cols: cols, rows: rows))
    }

    /// Detach cleanly (the session keeps running).
    public func detach() async {
        try? await connection.send(control: "detach")
        await connection.close()
    }

    /// Tear down the attach connection.
    public func close() async {
        await connection.close()
    }
}

/// The transport-abstract client for one daemon host.
///
/// Owns multiple ``GraithConnection``s per host: a lazily-opened **control**
/// connection for short-lived RPCs, one **attach** connection per live
/// terminal, and (on demand) an **event** connection carrying the
/// approval subscription — matching the daemon's non-multiplexed handler
/// (design C.2).
///
/// The same client serves the local macOS daemon (`.unix`) and remote tailnet
/// daemons (`.remote` + TLS); only the ``GraithTransport`` differs.
public actor GraithProtocolClient {
    public let transport: GraithTransport

    private let profile: String
    private let clientID: String
    private let signer: DeviceKeySigner?
    /// Bearer token placed in every envelope: the session token locally, or the
    /// paired client token remotely. Nil before pairing.
    private var token: String?

    private var control: GraithConnection?
    private var eventConn: GraithConnection?

    /// How to build the underlying byte stream for a connection. Defaults to
    /// Network.framework (``NWByteStream``); tests inject an in-memory stream.
    private let streamFactory: @Sendable (GraithTransport) -> ByteStream

    /// - Parameters:
    ///   - transport: how to reach the daemon.
    ///   - profile: the daemon profile (empty string for the default profile).
    ///     The handshake carries this and the daemon rejects a mismatch.
    ///   - clientID: an identifier for logging (e.g. the app's instance id).
    ///   - token: session/client bearer token, or nil (e.g. local human).
    ///   - signer: device key signer for remote PoP; nil for local transports.
    public init(
        transport: GraithTransport,
        profile: String = "",
        clientID: String = "graith-app",
        token: String? = nil,
        signer: DeviceKeySigner? = nil
    ) {
        self.transport = transport
        self.profile = profile
        self.clientID = clientID
        self.token = token
        self.signer = signer
        self.streamFactory = { NWByteStream(transport: $0) }
    }

    /// Test-only initializer that injects a custom byte-stream factory so the
    /// client can be driven by an in-memory mock daemon.
    init(
        transport: GraithTransport,
        profile: String,
        clientID: String,
        token: String?,
        signer: DeviceKeySigner?,
        streamFactory: @escaping @Sendable (GraithTransport) -> ByteStream
    ) {
        self.transport = transport
        self.profile = profile
        self.clientID = clientID
        self.token = token
        self.signer = signer
        self.streamFactory = streamFactory
    }

    /// Update the bearer token (e.g. after pairing completes).
    public func setToken(_ token: String?) {
        self.token = token
    }

    /// Force the control connection open (handshake + proof-of-possession), so a
    /// caller can verify reachability/auth up front rather than lazily on the
    /// first RPC. Idempotent — reuses the cached control connection.
    ///
    /// Added for the iOS host-client adapter (#628); no behaviour change for
    /// existing callers, which still connect lazily on first use.
    public func connect() async throws {
        _ = try await controlConnection()
    }

    // MARK: - Connections

    private func newConnection(cols: UInt16 = 80, rows: UInt16 = 24) async throws -> GraithConnection {
        let conn = GraithConnection(transport: transport, stream: streamFactory(transport), token: token)
        let hs = HandshakeMsg(
            clientID: clientID,
            terminalSize: [cols, rows],
            cwd: "",
            profile: profile
        )
        try await conn.connect(handshake: hs, signer: signer)
        return conn
    }

    private func controlConnection() async throws -> GraithConnection {
        if let control { return control }
        let conn = try await newConnection()
        control = conn
        return conn
    }

    // MARK: - Read RPCs

    /// `list` — sessions on this daemon. When `deleted` is true, returns the
    /// soft-deleted sessions (for the GUI's Deleted/restore surface) instead of
    /// the live ones. The daemon's `ListMsg` carries an optional `deleted` flag;
    /// the empty request (default) returns only non-deleted sessions.
    public func list(deleted: Bool = false) async throws -> [SessionInfo] {
        let conn = try await controlConnection()
        let reply = deleted
            ? try await conn.request("list", payload: ListRequest(deleted: true))
            : try await conn.request("list")
        return try decodePayload(reply, as: SessionListMsg.self).sessions
    }

    /// The wire payload for a deleted-session `list`. Kept private: the shared
    /// `ListMsg` maps to `EmptyMsg` in the conformance manifest (Messages.swift),
    /// so a live `list` still sends no body — only the deleted variant needs this.
    private struct ListRequest: Encodable {
        let deleted: Bool
    }

    /// `repo_list` — repositories the daemon offers for session creation
    /// (design §C.4: the app can't pass a local cwd).
    public func repoList() async throws -> [RepoEntry] {
        let conn = try await controlConnection()
        let reply = try await conn.request("repo_list", payload: RepoListMsg())
        return try decodePayload(reply, as: RepoListResponseMsg.self).repos
    }

    /// `config` — the daemon's effective (merged) configuration as TOML plus a
    /// unified diff against the built-in defaults, for the read-only config
    /// viewer in Settings (#904).
    public func config() async throws -> ConfigResponseMsg {
        let conn = try await controlConnection()
        let reply = try await conn.request("config", payload: EmptyMsg())
        return try decodePayload(reply, as: ConfigResponseMsg.self)
    }

    /// `agent_catalog` — the daemon's configured agent catalog + default_agent
    /// for the GUI agent pickers (#1234).
    public func agentCatalog() async throws -> AgentCatalogResponseMsg {
        let conn = try await controlConnection()
        let reply = try await conn.request("agent_catalog", payload: EmptyMsg())
        return try decodePayload(reply, as: AgentCatalogResponseMsg.self)
    }

    /// `diagnostics` — the daemon's health snapshot for the diagnostics panel
    /// (the `gr doctor` equivalent, #904).
    public func diagnostics() async throws -> DiagnosticsMsg {
        let conn = try await controlConnection()
        let reply = try await conn.request("diagnostics", payload: EmptyMsg())
        return try decodePayload(reply, as: DiagnosticsMsg.self)
    }

    /// `screen_snapshot` — a non-attaching peek at a session's current screen.
    public func screenSnapshot(sessionID: String) async throws -> ScreenSnapshotResponseMsg {
        let conn = try await controlConnection()
        let reply = try await conn.request("screen_snapshot", payload: SessionIDMsg(sessionID: sessionID))
        return try decodePayload(reply, as: ScreenSnapshotResponseMsg.self)
    }

    /// `store_list` — list document keys in the git-backed store (#902).
    /// See ``StoreListMsg`` for target resolution.
    public func storeList(repo: String?, shared: Bool, prefix: String?) async throws -> [StoreEntryInfo] {
        let conn = try await controlConnection()
        let reply = try await conn.request("store_list",
                                           payload: StoreListMsg(repo: repo, shared: shared ? true : nil, prefix: prefix))
        return try decodePayload(reply, as: StoreListResponseMsg.self).entries
    }

    /// `store_get` — fetch a single document body from the store (#902).
    public func storeGet(repo: String?, shared: Bool, key: String) async throws -> StoreGetResponseMsg {
        let conn = try await controlConnection()
        let reply = try await conn.request("store_get",
                                           payload: StoreGetMsg(repo: repo, shared: shared ? true : nil, key: key))
        return try decodePayload(reply, as: StoreGetResponseMsg.self)
    }

    /// `logs` — fetch the last `lines` of a session's scrollback as text.
    ///
    /// The daemon streams the tail on the data channel (0x01) and terminates
    /// with a `logs_done` control message (handler.go), so this opens a dedicated
    /// connection (like `attach`), collects the bytes until `logs_done`, then
    /// closes. `follow` is intentionally not supported here — this is the
    /// one-shot peek the mobile session view uses. Added for the iOS adapter
    /// (#628).
    public func logs(sessionID: String, lines: Int = 300,
                     timeoutSeconds: Double = 30, maxBytes: Int = 4 * 1024 * 1024) async throws -> String {
        let conn = try await newConnection()
        let dataStream = conn.dataStream
        let cap = maxBytes
        let collector = Task { () -> Data in
            var acc = Data()
            // Bounded drain: stop accumulating once we hit the cap so a
            // misbehaving daemon can't exhaust memory.
            for await chunk in dataStream {
                acc.append(chunk)
                if acc.count >= cap { break }
            }
            return acc
        }
        // Watchdog: if `logs_done` never arrives, close the connection after the
        // timeout. Closing resumes the pending `request` (so it stops hanging)
        // and finishes the data stream (so the collector completes). `conn.request`
        // awaits a continuation that isn't itself cancellation-aware, so closing
        // the connection — not task cancellation — is what unblocks it.
        let timeoutNanos = UInt64(max(0, timeoutSeconds) * 1_000_000_000)
        let watchdog = Task {
            try? await Task.sleep(nanoseconds: timeoutNanos)
            if !Task.isCancelled { await conn.close() }
        }
        do {
            // Resolves on `logs_done`; throws `ControlError.daemon` on `error`,
            // or a close-induced error if the watchdog fired.
            _ = try await conn.request("logs", payload: LogsMsg(sessionID: sessionID, lines: lines, follow: false))
            watchdog.cancel()
        } catch {
            watchdog.cancel()
            collector.cancel()
            await conn.close()
            _ = await collector.value
            throw error
        }
        await conn.close()
        let data = await collector.value
        return String(decoding: data, as: UTF8.self)
    }

    // MARK: - Mutating RPCs

    /// `create` — start a new session; returns the created session.
    public func create(_ msg: CreateMsg) async throws -> SessionInfo {
        let conn = try await controlConnection()
        let reply = try await conn.request("create", payload: msg)
        return try decodePayload(reply, as: SessionInfo.self)
    }

    public func stop(sessionID: String, children: Bool = false) async throws {
        try await lifecycle("stop", sessionID: sessionID, children: children)
    }

    public func delete(sessionID: String, children: Bool = false) async throws {
        try await lifecycle("delete", sessionID: sessionID, children: children)
    }

    public func restart(sessionID: String, children: Bool = false) async throws {
        try await lifecycle("restart", sessionID: sessionID, children: children)
    }

    /// `restore` — un-delete a soft-deleted session (inverse of a soft `delete`),
    /// returning it to `stopped`. The daemon replies `restored` with a
    /// `RestoreResultMsg`; the GUI only needs the side effect and re-lists, so the
    /// reply body is ignored here. `RestoreMsg` is wire-identical to
    /// `SessionScopeMsg` (`{session_id, children}`), so the latter is reused.
    public func restore(sessionID: String, children: Bool = false) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request("restore", payload: SessionScopeMsg(sessionID: sessionID, children: children))
    }

    /// `purge` — an immediate **hard** delete (worktree + branch + state removed),
    /// bypassing the soft-delete retention window. Sent as a `delete` with the
    /// `purge` flag set, matching the `gr purge` verb.
    public func purge(sessionID: String, children: Bool = false) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request("delete", payload: SessionScopeMsg(sessionID: sessionID, children: children, purge: true))
    }

    public func resume(sessionID: String) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request("resume", payload: SessionIDMsg(sessionID: sessionID))
    }

    /// `interrupt` — deliver an interrupt (Ctrl-C) to the session's agent. The
    /// daemon replies `interrupted` (handler.go). `InterruptMsg` is wire-identical
    /// to `SessionIDMsg` (`{session_id}`), so the latter is reused. Added for the
    /// iOS host-client adapter (#628).
    public func interrupt(sessionID: String) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request("interrupt", payload: SessionIDMsg(sessionID: sessionID))
    }

    public func rename(sessionID: String, newName: String) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request("rename", payload: RenameMsg(sessionID: sessionID, newName: newName))
    }

    /// `star` — mark a session as starred. The daemon replies `starred`.
    /// `StarMsg` is wire-identical to `SessionIDMsg` (`{session_id}`), so the
    /// latter is reused.
    public func star(sessionID: String) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request("star", payload: SessionIDMsg(sessionID: sessionID))
    }

    /// `unstar` — clear a session's star. The daemon replies `unstarred`.
    public func unstar(sessionID: String) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request("unstar", payload: SessionIDMsg(sessionID: sessionID))
    }

    /// `fork` — create a new session cloning `sourceSessionID`'s worktree and
    /// agent state under `name`. The daemon replies `created` with the new
    /// session (handler.go), so this returns the created `SessionInfo`.
    public func fork(name: String, sourceSessionID: String) async throws -> SessionInfo {
        let conn = try await controlConnection()
        let reply = try await conn.request("fork", payload: ForkMsg(name: name, sourceSessionID: sourceSessionID))
        return try decodePayload(reply, as: SessionInfo.self)
    }

    /// `migrate` — swap a session's agent (and optionally model) in place. The
    /// daemon replies `migrated` with the updated session (handler.go), so this
    /// returns the migrated `SessionInfo`.
    public func migrate(sessionID: String, agent: String, model: String? = nil) async throws -> SessionInfo {
        let conn = try await controlConnection()
        let reply = try await conn.request("migrate", payload: MigrateMsg(sessionID: sessionID, agent: agent, model: model))
        return try decodePayload(reply, as: SessionInfo.self)
    }

    public func setStatus(sessionID: String, text: String, ttlSeconds: Int? = nil, clear: Bool = false) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request("set_status", payload: SetStatusMsg(sessionID: sessionID, text: text, ttlSeconds: ttlSeconds, clear: clear))
    }

    private func lifecycle(_ type: String, sessionID: String, children: Bool) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request(type, payload: SessionScopeMsg(sessionID: sessionID, children: children))
    }

    // MARK: - Scenarios (#903)

    /// `scenario_list` — every running scenario on this daemon, each with its
    /// member sessions. Read-only; the daemon does not gate it.
    public func listScenarios() async throws -> [ScenarioRecord] {
        let conn = try await controlConnection()
        let reply = try await conn.request("scenario_list", payload: EmptyMsg())
        return try decodePayload(reply, as: ScenarioListResponse.self).scenarios
    }

    /// `scenario_stop` — stop every session in the named scenario. The daemon
    /// replies `scenario_stopped`; the GUI re-lists, so the body is ignored.
    public func stopScenario(name: String) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request("scenario_stop", payload: ScenarioNameMsg(name: name))
    }

    /// `scenario_resume` — resume every stopped/errored session in the scenario.
    public func resumeScenario(name: String) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request("scenario_resume", payload: ScenarioNameMsg(name: name))
    }

    /// `scenario_delete` — delete the scenario and all its sessions/worktrees.
    public func deleteScenario(name: String) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request("scenario_delete", payload: ScenarioNameMsg(name: name))
    }

    // MARK: - Inter-agent messaging (gr msg)

    /// Send a direct message to `sessionID`'s inbox (`gr msg send`).
    ///
    /// Publishes to the `inbox:<session-id>` stream and returns the published
    /// message the daemon echoes back (`msg_published`). The daemon forces the
    /// sender identity by role: a local human's `senderName` is honoured, a
    /// remote human's is replaced with its device identity.
    @discardableResult
    public func sendMessage(toSessionID sessionID: String, body: String,
                            senderName: String? = nil) async throws -> ConversationMessage {
        let conn = try await controlConnection()
        let reply = try await conn.request("msg_pub", payload: MsgPubMsg(
            stream: "inbox:" + sessionID, body: body, senderName: senderName))
        return try decodePayload(reply, as: ConversationMessage.self)
    }

    /// The full direct-message conversation (both directions) for `sessionID`
    /// (`gr msg` inbox view). Ordered oldest-to-newest; when `limit > 0` only
    /// the most recent `limit` messages are returned.
    public func conversation(sessionID: String, limit: Int = 0) async throws -> [ConversationMessage] {
        let conn = try await controlConnection()
        let reply = try await conn.request("msg_conversation",
                                           payload: MsgConversationMsg(sessionID: sessionID,
                                                                       limit: limit > 0 ? limit : nil))
        return try decodePayload(reply, as: MsgConversationListMsg.self).messages
    }

    /// Mark `sessionID`'s inbox read (`gr msg inbox --ack` / `gr msg ack`),
    /// acking on the session's behalf so its unread count clears.
    public func ackInbox(sessionID: String) async throws {
        let conn = try await controlConnection()
        _ = try await conn.request("msg_ack",
                                   payload: MsgAckMsg(stream: "inbox:" + sessionID, subscriber: sessionID))
    }

    // MARK: - Attach

    /// Open a dedicated attach connection for `sessionID`.
    ///
    /// The returned ``AttachSession`` streams PTY output (starting with the
    /// scrollback tail the daemon replays) and accepts keystrokes/resize.
    public func attach(sessionID: String, cols: UInt16, rows: UInt16) async throws -> AttachSession {
        let conn = try await newConnection(cols: cols, rows: rows)
        let reply = try await conn.request("attach", payload: AttachMsg(sessionID: sessionID))
        guard reply.type == "attached" else {
            await conn.close()
            throw ControlError.unexpectedReply(reply.type)
        }
        let info = try decodePayload(reply, as: SessionInfo.self)
        return AttachSession(session: info, connection: conn)
    }

    // MARK: - Approvals (event connection)

    /// Subscribe to approval notifications on a dedicated event connection
    /// (design §C.6 — no PTY attach, no desktop kick).
    ///
    /// Yields the full pending-approval list each time it changes.
    ///
    /// Contract: **a finished stream means the subscription has ended** (the
    /// event connection dropped, or the consumer stopped iterating), never "no
    /// approvals". If establishing the subscription fails, this call throws
    /// before returning a stream. When the consumer stops iterating, the pump
    /// task is cancelled and the underlying connection is closed, so nothing
    /// buffers forever.
    public func subscribeApprovals() async throws -> AsyncStream<[ApprovalInfo]> {
        let conn = try await newConnection()
        eventConn = conn
        try await conn.send(control: "approval_subscribe", payload: ApprovalSubscribeMsg())
        return AsyncStream { continuation in
            let events = conn.events
            let pump = Task {
                for await env in events where env.type == "approval_notification" {
                    if let note = try? decodePayload(env, as: ApprovalNotificationMsg.self) {
                        continuation.yield(note.pending)
                    }
                }
                // The event connection ended (dropped/closed) — finish promptly
                // so the consumer's `for await` completes rather than hanging.
                continuation.finish()
            }
            continuation.onTermination = { _ in
                pump.cancel()
                Task { await conn.close() }
            }
        }
    }

    /// Respond to a pending approval (approve/deny).
    public func respondApproval(requestID: String, decision: String, reason: String? = nil) async throws {
        // Use the event connection if present (it is the human's approval
        // channel); otherwise the control connection.
        let conn: GraithConnection
        if let eventConn {
            conn = eventConn
        } else {
            conn = try await controlConnection()
        }
        _ = try await conn.request("approval_respond", payload: ApprovalRespondMsg(requestID: requestID, decision: decision, reason: reason))
    }

    // MARK: - Pairing (design §B.2)

    /// Send a `pair_request` and await the `pair_response`.
    ///
    /// This opens a fresh (token-less) remote connection. The daemon surfaces a
    /// pending pairing to the local human; this call resolves once that human
    /// runs `gr pair approve` (or the daemon errors). On success the returned
    /// token is adopted for subsequent connections.
    ///
    /// > Note: The daemon's `pair_request` handler is landing under Phase 1
    /// > Task 6; the blocking-until-approved semantics assumed here must be
    /// > reconciled once it lands (tracked in `gui/NEEDS-MAC-VALIDATION.md`).
    public func pairRequest(deviceLabel: String) async throws -> PairResponseMsg {
        guard let signer else {
            throw ControlError.malformed("pairing requires a DeviceKeySigner")
        }
        let conn = try await newConnection()
        defer { Task { await conn.close() } }
        let pubKey = try signer.publicKeyBase64()

        // The daemon issues an `auth_challenge` after `handshake_ok` on *every*
        // remote connection. The token-less pairing lane skips proof-of-possession
        // (a brand-new device has no daemon record yet), so that challenge is left
        // buffered on the connection. Send `pair_request`, then consume replies
        // until `pair_response` or `error`, skipping the `auth_challenge` — exactly
        // as the Go client does (internal/client/remote.go). Awaiting local human
        // approval can take minutes; that is bounded by the transport's own
        // deadline, not here.
        try await conn.send(control: "pair_request",
                            payload: PairRequestMsg(deviceLabel: deviceLabel, devicePubKey: pubKey))
        while true {
            let reply = try await conn.nextControlReply()
            switch reply.type {
            case "auth_challenge":
                continue // pairing lane ignores PoP
            case "pair_response":
                let resp = try decodePayload(reply, as: PairResponseMsg.self)
                try await bindTOFUPin(reported: resp.tlsPinSPKI, on: conn)
                token = resp.clientToken
                return resp
            case "error":
                let e = try decodePayload(reply, as: ErrorMsg.self)
                throw ControlError.daemon(e.message)
            default:
                throw ControlError.unexpectedReply(reply.type)
            }
        }
    }

    /// Confirm the daemon-reported SPKI pin against the certificate actually
    /// presented on this pairing handshake before it is stored (TOFU binding).
    ///
    /// Without this, pairing would trust whatever pin the reply carried, leaving
    /// a MITM window: an attacker terminating TLS could hand us its own cert
    /// while echoing the real daemon's pin. Mirrors the Go client
    /// (internal/client/remote.go): a remote daemon must report a pin, and it
    /// must equal the pin captured off the wire.
    private func bindTOFUPin(reported: String, on conn: GraithConnection) async throws {
        guard transport.isRemote else { return } // unix socket: no TLS, no pin
        guard !reported.isEmpty else {
            throw ControlError.tlsPinMismatch("daemon reported no TLS pin; refusing to pair (cannot confirm the endpoint)")
        }
        // A remote transport's stream captures the presented leaf SPKI; if it is
        // absent the endpoint can't be confirmed, so fail closed.
        guard let captured = await conn.capturedSPKIPin() else {
            throw ControlError.tlsPinMismatch("could not capture the daemon's TLS certificate; refusing to pair")
        }
        guard captured == reported else {
            throw ControlError.tlsPinMismatch(
                "TLS pin mismatch: daemon reported \(reported) but presented \(captured) (possible MITM)")
        }
    }

    // MARK: - Lifecycle

    public func close() async {
        if let control { await control.close() }
        if let eventConn { await eventConn.close() }
        control = nil
        eventConn = nil
    }
}
