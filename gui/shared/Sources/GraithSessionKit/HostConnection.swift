import Foundation
import Combine
import GraithProtocol
import GraithRemoteKit

/// A `MainActor` view-model wrapping one host's `GraithHostClient`: owns the
/// connection lifecycle, the session list, and the approval subscription for
/// that daemon. The SwiftUI shell observes an array of these (one per host).
@MainActor
public final class HostConnection: ObservableObject, Identifiable {
    public enum ConnectionState: Equatable, Sendable {
        case idle
        case connecting
        case connected
        case failed(String)
    }

    public let entry: Host
    public nonisolated var id: String { entry.id }

    @Published public private(set) var state: ConnectionState = .idle
    @Published public private(set) var sessions: [SessionInfo] = []
    @Published public private(set) var approvals: [ApprovalInfo] = []
    @Published public private(set) var scenarios: [ScenarioRecord] = []
    /// The daemon's configured agent catalog + default_agent (#1234), including
    /// explicit loading/unavailable states. No client-side catalog is invented.
    @Published public private(set) var agentCatalogState: AgentCatalogState = .loading
    @Published public private(set) var lastError: String?

    private let client: any GraithHostClient
    private var approvalTask: Task<Void, Never>?
    /// Guards `refresh()` against overlapping list() calls piling up.
    private var isRefreshing = false
    /// Set when a refresh is requested while one is already in flight, so the
    /// coalescing is *lossless*: the in-flight refresh loops once more instead of
    /// dropping the request. Without this, a mutation's post-action refresh that
    /// lands during a poll's in-flight `list` is silently discarded, leaving
    /// published state stale — indefinitely on iOS, which doesn't poll.
    private var refreshQueued = false

    /// The underlying host client — used to build a terminal attach view-model.
    public var hostClient: any GraithHostClient { client }

    /// The raw `GraithProtocolClient` when this connection is backed by the real
    /// transport (nil for mock clients). The macOS terminal view attaches
    /// through this directly for its richer AppKit terminal chrome.
    public var protocolClient: GraithProtocolClient? { (client as? RealHostClient)?.protocolClient }

    /// Whether this connection owns the approval subscription. iOS aggregates
    /// approvals through `FleetModel` (default true); macOS runs its own
    /// `ApprovalMonitor` presenter subscribing via the raw clients, so it opts
    /// out here (false) to avoid a redundant second event subscription per host.
    private let ownsApprovals: Bool

    public init(entry: Host, client: any GraithHostClient, subscribeApprovals: Bool = true) {
        self.entry = entry
        self.client = client
        self.ownsApprovals = subscribeApprovals
    }

    // MARK: - Lifecycle

    /// Connect the control + event connections and start reading approvals.
    public func connect() async {
        guard state != .connecting else { return }
        state = .connecting
        agentCatalogState = .loading
        lastError = nil
        do {
            try await client.connect()
            state = .connected
            await refresh()
            await refreshAgentCatalog()
            if ownsApprovals { startApprovalSubscription() }
        } catch {
            state = .failed(Self.describe(error))
            agentCatalogState = .unavailable(Self.describe(error))
            lastError = Self.describe(error)
        }
    }

    public func disconnect() async {
        approvalTask?.cancel()
        approvalTask = nil
        await client.disconnect()
        state = .idle
        agentCatalogState = .unavailable("Host is disconnected.")
    }

    /// Reload the session list (and scenarios). Overlapping calls coalesce
    /// *losslessly*: while a `list` is in flight, a further `refresh()` sets a
    /// pending flag instead of running concurrently, and the in-flight refresh
    /// loops once more when it finishes. This keeps a slow/hung host from piling
    /// up concurrent RPCs on its single control connection while guaranteeing a
    /// post-mutation refresh is never dropped. A successful list clears a prior
    /// error.
    public func refresh() async {
        guard state == .connected else { return }
        if isRefreshing {
            refreshQueued = true
            return
        }
        isRefreshing = true
        defer { isRefreshing = false }
        repeat {
            refreshQueued = false
            do {
                sessions = try await client.listSessions()
                lastError = nil
            } catch {
                lastError = Self.describe(error)
            }
            await refreshScenarios()
        } while refreshQueued && state == .connected
    }

    /// Reload this host's running scenarios. Best-effort: on failure the
    /// last-known scenarios are **retained** (not cleared) and the error is not
    /// surfaced on `lastError` — the session list is the primary health signal,
    /// and a daemon that lists sessions but hiccups on scenarios shouldn't blank
    /// the fleet view or paint the host red. Only runs while connected.
    private func refreshScenarios() async {
        guard state == .connected else { return }
        if let fetched = try? await client.listScenarios() {
            scenarios = fetched
        }
    }

    /// Reload this host's configured agent catalog. Failure is explicit so the
    /// UI cannot confuse an old/offline daemon with a daemon-owned empty list.
    private func refreshAgentCatalog() async {
        guard state == .connected else { return }
        agentCatalogState = .loading
        do {
            agentCatalogState = .available(try await client.agentCatalog())
        } catch {
            agentCatalogState = .unavailable(Self.describe(error))
        }
    }

    // MARK: - Approvals (design §C.6 — subscribe, don't attach)

    private func startApprovalSubscription() {
        approvalTask?.cancel()
        approvalTask = Task { [weak self] in
            guard let self else { return }
            // Mirrors the macOS ApprovalMonitor retry loop: a finished stream
            // means the subscription ended (setup failed or the event
            // connection dropped), never "no approvals". While we're still
            // meant to be connected, re-subscribe with bounded exponential
            // backoff so a flaky link recovers without dying silently or
            // tight-looping. An intentional disconnect() cancels this task, so
            // we never re-subscribe after it.
            let baseBackoff: UInt64 = 500_000_000 // 0.5s
            let maxBackoff: UInt64 = 8_000_000_000 // 8s
            var backoff = baseBackoff
            while !Task.isCancelled {
                // `approvalStream()` is actor-isolated on the client, so resolve
                // it inside the task. This Task inherits @MainActor, so the
                // assignments below are main-actor safe.
                let stream = await self.client.approvalStream()
                var delivered = false
                for await pending in stream {
                    delivered = true
                    backoff = baseBackoff // healthy delivery — reset backoff
                    self.approvals = pending
                }
                // Stream ended. Stop if we're cancelled or no longer connected
                // (an intentional disconnect). Otherwise surface it and retry.
                if Task.isCancelled || self.state != .connected { break }
                self.lastError = delivered
                    ? "Approval stream dropped; reconnecting…"
                    : "Approval subscription failed to start; retrying…"
                try? await Task.sleep(nanoseconds: backoff)
                backoff = Swift.min(backoff * 2, maxBackoff)
            }
        }
    }

    public func respond(_ approval: ApprovalInfo, decision: ApprovalDecision, reason: String? = nil) async {
        do {
            try await client.respondApproval(requestID: approval.requestID, decision: decision, reason: reason)
        } catch {
            lastError = Self.describe(error)
        }
    }

    // MARK: - Mutations / reads used by the UI

    public func repoList() async -> [RepoEntry] {
        (try? await client.repoList()) ?? []
    }

    /// List document-store keys for the browser (#902). Errors surface on
    /// `lastError` and yield an empty list (never throws up to the view).
    public func storeList(repo: String? = nil, shared: Bool = false, prefix: String? = nil) async -> [StoreEntryInfo] {
        guard state == .connected else { return [] }
        do {
            let entries = try await client.storeList(repo: repo, shared: shared, prefix: prefix)
            lastError = nil
            return entries
        } catch {
            lastError = Self.describe(error)
            return []
        }
    }

    /// Fetch a single document body (#902). Throws so the viewer can distinguish
    /// a missing document from an empty one (mirrors macOS `fetchSnapshot`).
    public func storeGet(repo: String?, shared: Bool, key: String) async throws -> StoreGetResponseMsg {
        try await client.storeGet(repo: repo, shared: shared, key: key)
    }

    /// Fetch the daemon's effective configuration + diff-vs-defaults for the
    /// read-only config viewer (#904). Throws so the view can distinguish a
    /// fetch failure (offline host, permission denied) from an empty config.
    public func config() async throws -> ConfigResponseMsg {
        try await client.config()
    }

    /// Fetch the daemon's health snapshot for the diagnostics panel (#904).
    public func diagnostics() async throws -> DiagnosticsMsg {
        try await client.diagnostics()
    }

    /// Fetch the daemon's agent catalog on demand (#1234), updating and returning
    /// the explicit loading/available/unavailable state.
    @discardableResult
    public func fetchAgentCatalog() async -> AgentCatalogState {
        guard state == .connected else {
            let unavailable = AgentCatalogState.unavailable("Host is not connected.")
            agentCatalogState = unavailable
            return unavailable
        }
        agentCatalogState = .loading
        do {
            agentCatalogState = .available(try await client.agentCatalog())
        } catch {
            agentCatalogState = .unavailable(Self.describe(error))
        }
        return agentCatalogState
    }

    public func create(_ request: CreateRequest) async -> Bool {
        do {
            try await client.create(request)
            await refresh()
            return true
        } catch {
            lastError = Self.describe(error)
            return false
        }
    }

    public func stop(_ session: SessionInfo) async { await run { try await self.client.stop(sessionID: session.id) } }
    public func resume(_ session: SessionInfo) async { await run { try await self.client.resume(sessionID: session.id) } }
    public func restart(_ session: SessionInfo) async { await run { try await self.client.restart(sessionID: session.id) } }
    public func interrupt(_ session: SessionInfo) async { await run { try await self.client.interrupt(sessionID: session.id) } }
    public func delete(_ session: SessionInfo) async { await run { try await self.client.delete(sessionID: session.id) } }
    public func restore(_ session: SessionInfo) async { await run { try await self.client.restore(sessionID: session.id) } }
    public func purge(_ session: SessionInfo) async { await run { try await self.client.purge(sessionID: session.id) } }

    /// Set (or clear) a session's status summary. A non-empty `text` sets it; the
    /// UI passes `clear: true` (with empty text) to remove it.
    public func setStatus(_ session: SessionInfo, text: String, ttlSeconds: Int? = nil, clear: Bool = false) async {
        await run { try await self.client.setStatus(sessionID: session.id, text: text, ttlSeconds: ttlSeconds, clear: clear) }
    }

    /// Fetch this host's soft-deleted sessions for the Deleted/restore surface.
    /// A failure surfaces on `lastError` and yields an empty list (never throws
    /// up to the view).
    public func deletedSessions() async -> [SessionInfo] {
        guard state == .connected else { return [] }
        do {
            let deleted = try await client.listDeletedSessions()
            lastError = nil
            return deleted
        } catch {
            lastError = Self.describe(error)
            return []
        }
    }

    public func rename(_ session: SessionInfo, to newName: String) async {
        let trimmed = newName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, trimmed != session.name else { return }
        await run { try await self.client.rename(sessionID: session.id, newName: trimmed) }
    }

    /// Toggle a session's star: `unstar` when currently starred, else `star`.
    public func toggleStar(_ session: SessionInfo) async {
        if session.starred == true {
            await run { try await self.client.unstar(sessionID: session.id) }
        } else {
            await run { try await self.client.star(sessionID: session.id) }
        }
    }

    public func fork(_ session: SessionInfo, name: String) async {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        await run { try await self.client.fork(name: trimmed, sourceSessionID: session.id) }
    }

    public func migrate(_ session: SessionInfo, agent: String, model: String? = nil) async {
        await run { try await self.client.migrate(sessionID: session.id, agent: agent, model: model) }
    }

    // MARK: - Scenario lifecycle (#903)

    public func stopScenario(_ name: String) async { await run { try await self.client.stopScenario(name: name) } }
    public func resumeScenario(_ name: String) async { await run { try await self.client.resumeScenario(name: name) } }
    public func deleteScenario(_ name: String) async { await run { try await self.client.deleteScenario(name: name) } }

    // MARK: - Messaging (gr msg)

    /// Send a direct message to `session`'s inbox. Returns true on success; a
    /// failure surfaces on `lastError` (the compose UI shows it). Does not
    /// refresh the session list — messaging doesn't change it.
    @discardableResult
    public func sendMessage(to session: SessionInfo, body: String) async -> Bool {
        let trimmed = body.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return false }
        do {
            _ = try await client.sendMessage(toSessionID: session.id, body: trimmed)
            lastError = nil
            return true
        } catch {
            lastError = Self.describe(error)
            return false
        }
    }

    /// Fetch the full direct-message conversation (both directions) for
    /// `session`. A failure surfaces on `lastError` and yields an empty list.
    public func conversation(for session: SessionInfo, limit: Int = 0) async -> [ConversationMessage] {
        do {
            let messages = try await client.conversation(sessionID: session.id, limit: limit)
            lastError = nil
            return messages
        } catch {
            lastError = Self.describe(error)
            return []
        }
    }

    /// Mark `session`'s inbox read (clears its unread count). Returns true on
    /// success; a failure surfaces on `lastError` and returns false so the UI can
    /// tell the user rather than silently appearing to succeed.
    @discardableResult
    public func ackInbox(for session: SessionInfo) async -> Bool {
        do {
            try await client.ackInbox(sessionID: session.id)
            lastError = nil
            return true
        } catch {
            lastError = Self.describe(error)
            return false
        }
    }

    /// Expose the underlying client for the attach path (Task 20).
    public var underlyingClient: any GraithHostClient { client }

    private func run(_ op: @escaping () async throws -> Void) async {
        do { try await op(); await refresh() }
        catch { lastError = Self.describe(error) }
    }

    static func describe(_ error: Error) -> String {
        FleetModel.describeError(error)
    }
}
