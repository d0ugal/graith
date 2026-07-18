import Foundation
import GraithProtocol

/// The production `GraithHostClient`, wrapping the shared `GraithProtocolClient`
/// actor. Since the boundary now speaks the canonical `GraithProtocol` wire
/// models directly (#1131), the old `GraithProtocol.* → GraithClientAPI.*`
/// translation is gone — this adapter now only reshapes the API surface the
/// boundary needs (including `status` synthesized from `list`) and normalises
/// errors.
public actor RealHostClient: GraithHostClient {
    private let inner: GraithProtocolClient
    private var connected = false

    public init(inner: GraithProtocolClient) {
        self.inner = inner
    }

    public var isConnected: Bool { connected }

    /// The wrapped `GraithProtocolClient`. Exposed for platform-specific
    /// terminal-attach chrome (the macOS AppKit terminal view attaches through
    /// the raw client's richer `AttachSession`, which carries the control-event
    /// stream the boundary's `TerminalAttachSession` intentionally omits). `let`
    /// is immutable so this is safe to read off the actor.
    public nonisolated var protocolClient: GraithProtocolClient { inner }

    // MARK: - Lifecycle

    public func connect() async throws {
        do {
            try await inner.connect()
            connected = true
        } catch {
            connected = false
            throw RealClientError.map(error)
        }
    }

    public func disconnect() async {
        await inner.close()
        connected = false
    }

    // MARK: - Reads

    public func listSessions() async throws -> [SessionInfo] {
        do {
            return try await inner.list()
        } catch {
            throw RealClientError.map(error)
        }
    }

    public func listDeletedSessions() async throws -> [SessionInfo] {
        do {
            return try await inner.list(deleted: true)
        } catch {
            throw RealClientError.map(error)
        }
    }

    /// `GraithProtocolClient` has no dedicated `status` RPC, and the mobile UI
    /// does not call this directly — synthesize it from `list` to satisfy the
    /// boundary (session + a fleet summary derived from the full list).
    public func status(sessionID: String) async throws -> StatusResponse {
        let sessions: [SessionInfo]
        do {
            sessions = try await inner.list()
        } catch {
            throw RealClientError.map(error)
        }
        guard let target = sessions.first(where: { $0.id == sessionID }) else {
            throw GraithClientError.daemon("session not found: \(sessionID)")
        }
        return StatusResponse(session: target, unreadCount: 0, fleet: Self.fleet(from: sessions))
    }

    public func repoList() async throws -> [RepoEntry] {
        do {
            return try await inner.repoList()
        } catch {
            throw RealClientError.map(error)
        }
    }

    public func logs(sessionID: String, lines: Int) async throws -> String {
        do {
            return try await inner.logs(sessionID: sessionID, lines: lines)
        } catch {
            throw RealClientError.map(error)
        }
    }

    public func screenSnapshot(sessionID: String) async throws -> ScreenSnapshot {
        do {
            return try await inner.screenSnapshot(sessionID: sessionID)
        } catch {
            throw RealClientError.map(error)
        }
    }

    public func storeList(repo: String?, shared: Bool, prefix: String?) async throws -> [StoreEntryInfo] {
        do {
            return try await inner.storeList(repo: repo, shared: shared, prefix: prefix)
        } catch {
            throw RealClientError.map(error)
        }
    }

    public func config() async throws -> ConfigResponseMsg {
        do {
            return try await inner.config()
        } catch {
            throw RealClientError.map(error)
        }
    }

    public func agentCatalog() async throws -> AgentCatalogResponseMsg {
        do {
            return try await inner.agentCatalog()
        } catch {
            throw RealClientError.map(error)
        }
    }

    public func storeGet(repo: String?, shared: Bool, key: String) async throws -> StoreGetResponseMsg {
        do {
            return try await inner.storeGet(repo: repo, shared: shared, key: key)
        } catch {
            throw RealClientError.map(error)
        }
    }

    public func diagnostics() async throws -> DiagnosticsMsg {
        do {
            return try await inner.diagnostics()
        } catch {
            throw RealClientError.map(error)
        }
    }

    // MARK: - Mutations

    public func create(_ request: CreateRequest) async throws {
        do {
            _ = try await inner.create(request)
        } catch {
            throw RealClientError.map(error)
        }
    }

    public func stop(sessionID: String) async throws { try await run { try await self.inner.stop(sessionID: sessionID) } }
    public func resume(sessionID: String) async throws { try await run { try await self.inner.resume(sessionID: sessionID) } }
    public func restart(sessionID: String) async throws { try await run { try await self.inner.restart(sessionID: sessionID) } }
    public func interrupt(sessionID: String) async throws { try await run { try await self.inner.interrupt(sessionID: sessionID) } }
    public func delete(sessionID: String) async throws { try await run { try await self.inner.delete(sessionID: sessionID) } }
    public func restore(sessionID: String) async throws { try await run { try await self.inner.restore(sessionID: sessionID) } }
    public func purge(sessionID: String) async throws { try await run { try await self.inner.purge(sessionID: sessionID) } }
    public func setStatus(sessionID: String, text: String, ttlSeconds: Int?, clear: Bool) async throws {
        try await run { try await self.inner.setStatus(sessionID: sessionID, text: text, ttlSeconds: ttlSeconds, clear: clear) }
    }
    public func rename(sessionID: String, newName: String) async throws {
        try await run { _ = try await self.inner.update(sessionID: sessionID, name: newName) }
    }
    public func update(sessionID: String, name: String?, parentID: String?, starred: Bool?) async throws -> UpdateResultMsg {
        do {
            return try await inner.update(sessionID: sessionID, name: name, parentID: parentID, starred: starred)
        } catch {
            throw RealClientError.map(error)
        }
    }
    public func fork(name: String, sourceSessionID: String) async throws {
        try await run { _ = try await self.inner.fork(name: name, sourceSessionID: sourceSessionID) }
    }
    public func migrate(sessionID: String, agent: String, model: String?) async throws {
        try await run { _ = try await self.inner.migrate(sessionID: sessionID, agent: agent, model: model) }
    }

    // MARK: - Messaging (gr msg)

    public func sendMessage(toSessionID sessionID: String, body: String) async throws -> ConversationMessage {
        do {
            // Label local-human sends "human" so the recipient sees a sensible
            // sender; a remote human's identity is forced daemon-side regardless.
            return try await inner.sendMessage(toSessionID: sessionID, body: body, senderName: "human")
        } catch {
            throw RealClientError.map(error)
        }
    }

    public func conversation(sessionID: String, limit: Int) async throws -> [ConversationMessage] {
        do {
            return try await inner.conversation(sessionID: sessionID, limit: limit)
        } catch {
            throw RealClientError.map(error)
        }
    }

    public func ackInbox(sessionID: String) async throws {
        try await run { try await self.inner.ackInbox(sessionID: sessionID) }
    }

    // MARK: - Attach

    public func attach(sessionID: String) async throws -> any TerminalAttachSession {
        do {
            // Best-guess 80x24 initial size; the terminal view sends the real
            // geometry via `resize()` after attach.
            let session = try await inner.attach(sessionID: sessionID, cols: 80, rows: 24)
            return RealAttachSession(inner: session)
        } catch {
            throw RealClientError.map(error)
        }
    }

    // MARK: - Helpers

    private func run(_ op: @Sendable () async throws -> Void) async throws {
        do { try await op() }
        catch { throw RealClientError.map(error) }
    }

    private static func fleet(from sessions: [SessionInfo]) -> FleetSummary {
        FleetSummary(
            total: sessions.count,
            active: sessions.filter { $0.agentStatus == "active" }.count,
            approval: sessions.filter { $0.agentStatus == "approval" }.count,
            ready: sessions.filter { $0.agentStatus == "ready" }.count,
            errored: sessions.filter { $0.status == "errored" }.count,
            stopped: sessions.filter { $0.status == "stopped" }.count
        )
    }
}
