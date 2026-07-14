import Foundation
import Combine
import GraithProtocol
import GraithRemoteKit

/// The multi-host session aggregator both apps bind to (#1131). Owns the
/// ``HostRegistry``, device identity, an optional tailnet reachability probe,
/// the client factory, and one ``HostConnection`` per host. Provides the
/// aggregated, cross-host view the sidebars render, plus the session-action and
/// single-attach surface the macOS app used to keep in its own `SessionStore`.
///
/// This unifies what was previously two separate stores: the iOS `AppModel`
/// (per-host `HostConnection` aggregation) and the macOS `SessionStore`
/// (multi-host merge, grouping, polling, per-host errors, single-attach). A
/// capability added here appears on both platforms.
@MainActor
open class FleetModel: ObservableObject {
    public let registry: HostRegistry
    public let identity: DeviceIdentity?
    public let reachability: TailnetReachability?
    public let pairing: PairingCoordinator

    private let factory: HostClientFactory
    /// Whether per-host connections own the approval subscription + aggregation.
    /// macOS drives approvals through its own `ApprovalMonitor` presenter, so it
    /// opts out (false) to avoid a redundant second subscription per host.
    private let subscribeApprovals: Bool

    /// One connection per host, keyed by host id, in registry order.
    @Published public private(set) var connections: [HostConnection] = []
    /// The currently selected session, namespaced by host (host id + session id).
    @Published public var selection: SessionRef?
    /// Session ids whose subtree is collapsed in the sidebar tree.
    @Published public var collapsedSessions: Set<String> = []

    /// Forward each connection's changes up so views bound to derived,
    /// cross-connection state (`sessions`, `allSessions`, `error`, …) refresh.
    private var connectionObservers: [AnyCancellable] = []
    private var registryObserver: AnyCancellable?
    private var pollTimer: Timer?

    /// - Parameters:
    ///   - identity: the device ed25519 identity, or nil if it could not be
    ///     created (remote connections + pairing are then unavailable).
    ///   - reachability: the tailnet probe (iOS); nil on platforms that only
    ///     talk to a local daemon.
    ///   - poll: when true, refresh every connection on a 2s timer (the macOS
    ///     desktop behaviour). iOS refreshes on connect/foreground instead.
    public init(
        registry: HostRegistry,
        identity: DeviceIdentity?,
        reachability: TailnetReachability?,
        factory: HostClientFactory,
        pairing: PairingCoordinator,
        poll: Bool = false,
        subscribeApprovals: Bool = true
    ) {
        self.registry = registry
        self.identity = identity
        self.reachability = reachability
        self.factory = factory
        self.pairing = pairing
        self.subscribeApprovals = subscribeApprovals
        rebuildConnections()
        // Rebuild + reconnect when the *membership* changes (a pairing completes
        // or a host is removed). Gated on the set of host ids — a display-only
        // mutation like `markSeen` (same ids, new `lastSeen`) must NOT tear down
        // and re-dial every connection.
        var knownHostIDs = Set(registry.hosts.map(\.id))
        registryObserver = registry.$hosts
            .dropFirst()
            .sink { [weak self] hosts in
                let ids = Set(hosts.map(\.id))
                guard ids != knownHostIDs else { return }
                knownHostIDs = ids
                Task { @MainActor in
                    guard let self else { return }
                    self.rebuildConnections()
                    await self.connectAll()
                }
            }
        if poll {
            // Desktop (always-on) mode: connect immediately and keep polling.
            // iOS instead drives connect/reconnect from the view lifecycle
            // (`RootView.task` / scene-phase changes).
            startPolling()
            Task { await connectAll() }
        }
    }

    // MARK: - Connections

    /// (Re)create `HostConnection`s from the registry. Preserves an existing
    /// connection whenever its *connection-relevant* host fields are unchanged
    /// (ignoring display-only fields like `label`/`lastSeen`), and disconnects
    /// any connection that is dropped or replaced so it can't leak an open
    /// client socket + approval task.
    public func rebuildConnections() {
        let existing = Dictionary(uniqueKeysWithValues: connections.map { ($0.id, $0) })
        let next: [HostConnection] = registry.hosts.compactMap { entry -> HostConnection? in
            if let conn = existing[entry.id], Self.connectionUnchanged(conn.entry, entry) {
                return conn
            }
            switch entry.kind {
            case .local:
                let client = factory.makeLocalClient(transport: entry.transport, profile: entry.daemonProfile)
                return HostConnection(entry: entry, client: client, subscribeApprovals: subscribeApprovals)
            case .remote:
                guard entry.isPaired, let creds = registry.credentials(for: entry), let identity else { return nil }
                let client = factory.makeClient(transport: entry.transport, credentials: creds, signer: identity)
                return HostConnection(entry: entry, client: client, subscribeApprovals: subscribeApprovals)
            }
        }
        // Tear down connections that went away or were replaced by a fresh client.
        for conn in connections where !next.contains(where: { $0 === conn }) {
            Task { await conn.disconnect() }
        }
        connections = next
        connectionObservers = connections.map { conn in
            conn.objectWillChange.sink { [weak self] _ in self?.objectWillChange.send() }
        }
    }

    /// Whether two host records describe the *same connection*, so a cached
    /// connection can be reused. Ignores display-only fields (`label`,
    /// `lastSeen`) — otherwise a `markSeen` tick would tear down and re-dial
    /// every client.
    private static func connectionUnchanged(_ a: Host, _ b: Host) -> Bool {
        a.id == b.id && a.kind == b.kind && a.socketPath == b.socketPath
            && a.magicDNSName == b.magicDNSName && a.port == b.port
            && a.daemonProfile == b.daemonProfile && a.tlsPinSPKI == b.tlsPinSPKI
            && a.deviceID == b.deviceID && a.isPaired == b.isPaired
    }

    /// Connect all hosts (called on appear / on returning to foreground).
    public func connectAll() async {
        await withTaskGroup(of: Void.self) { group in
            for conn in connections {
                group.addTask { await conn.connect() }
            }
        }
        refreshReachability()
    }

    public func disconnectAll() async {
        for conn in connections { await conn.disconnect() }
    }

    /// Force every host to re-establish its connection — used on returning to
    /// the foreground, where a socket that was open when we backgrounded may now
    /// be dead. Tear each connection down first so the reconnect dials fresh.
    public func reconnectAll() async {
        for conn in connections { await conn.disconnect() }
        await connectAll()
    }

    /// Drive the tailnet banner from ground truth: if any host's control
    /// connection is live we are demonstrably on the tailnet. Only report
    /// "not on tailnet" when hosts exist but none connected.
    private func refreshReachability() {
        guard let reachability, !connections.isEmpty else { return }
        reachability.observed(reachable: connections.contains { $0.state == .connected })
    }

    public func connection(for ref: SessionRef) -> HostConnection? {
        connections.first { $0.id == ref.hostID }
    }

    /// The connection that owns `sessionID` (session ids are per-daemon).
    public func connection(ownerOf sessionID: String) -> HostConnection? {
        connections.first { conn in conn.sessions.contains { $0.id == sessionID } }
    }

    /// The host a session belongs to.
    public func host(for sessionID: String) -> Host? {
        connection(ownerOf: sessionID)?.entry
    }

    // MARK: - Host management

    /// Remove a host and its connection.
    public func removeHost(_ entry: Host) async {
        guard entry.kind != .local else { return }
        if let conn = connections.first(where: { $0.id == entry.id }) {
            await conn.disconnect()
        }
        registry.remove(hostID: entry.id)
        rebuildConnections()
    }

    /// After a successful pairing, rebuild connections and connect the new host.
    public func didPair() async {
        rebuildConnections()
        await connectAll()
    }

    // MARK: - Aggregation

    /// All sessions across all hosts, tagged with their host, for a flat feed.
    public var allSessions: [HostedSession] {
        connections.flatMap { conn in
            conn.sessions.map { HostedSession(host: conn.entry, session: $0) }
        }
    }

    /// The merged, host-agnostic session list in registry order, de-duplicated
    /// by id. The single-host (local-only) rendering path binds to this.
    public var sessions: [SessionInfo] {
        var seen = Set<String>()
        var merged: [SessionInfo] = []
        for conn in connections {
            for session in conn.sessions where !seen.contains(session.id) {
                seen.insert(session.id)
                merged.append(session)
            }
        }
        return merged
    }

    /// Total pending approvals across all hosts (for a badge).
    public var totalPendingApprovals: Int {
        connections.reduce(0) { $0 + $1.approvals.count }
    }

    /// Every pending approval across hosts, tagged with its host.
    public var allApprovals: [HostedApproval] {
        connections.flatMap { conn in
            conn.approvals.map { HostedApproval(host: conn.entry, approval: $0) }
        }
    }

    /// The primary error for the sidebar footer: the local daemon's if it has
    /// one, else the first host reporting any error (a `.failed` connection *or*
    /// a connected host whose `list` is failing).
    public var error: String? {
        if let local = connections.first(where: { $0.entry.kind == .local }), let e = local.lastError {
            return e
        }
        return connections.compactMap { $0.lastError }.first
    }

    /// Per-host connection error, keyed by host id (shown in host headers).
    /// Any non-nil `lastError` counts — a host that handshook and then had its
    /// `list` fail stays `.connected` but must still surface as degraded.
    public var hostErrors: [String: String] {
        var errors: [String: String] = [:]
        for conn in connections {
            if let e = conn.lastError { errors[conn.id] = e }
        }
        return errors
    }

    /// Whether any remote hosts are configured (drives the sidebar's per-host
    /// grouping vs. the familiar single-host layout).
    public var hasRemoteHosts: Bool {
        registry.hosts.contains { $0.kind == .remote }
    }

    // MARK: - Sidebar grouping

    private func groups(for sessions: [SessionInfo]) -> [(repo: String, sessions: [SessionInfo])] {
        Dictionary(grouping: sessions) { $0.repoName }
            .sorted { $0.key < $1.key }
            .map { (repo: $0.key, sessions: $0.value.sorted { $0.name < $1.name }) }
    }

    /// Sessions grouped by repo (flat, host-agnostic).
    public var sessionsByRepo: [(repo: String, sessions: [SessionInfo])] {
        groups(for: sessions)
    }

    /// Sessions grouped by host, then repo — for the multi-host sidebar. Every
    /// configured host appears even with no sessions, so its connection state is
    /// visible.
    public var sessionsByHost: [(host: Host, groups: [(repo: String, sessions: [SessionInfo])])] {
        connections.map { conn in (host: conn.entry, groups: groups(for: conn.sessions)) }
    }

    public func roots(in sessions: [SessionInfo]) -> [SessionInfo] {
        let ids = Set(sessions.map(\.id))
        return sessions.filter {
            $0.parentID == nil || $0.parentID!.isEmpty || !ids.contains($0.parentID!)
        }
    }

    public func children(of parentID: String, in sessions: [SessionInfo]) -> [SessionInfo] {
        sessions.filter { $0.parentID == parentID }
    }

    public func descendantCount(of sessionID: String, in sessions: [SessionInfo]) -> Int {
        let kids = children(of: sessionID, in: sessions)
        return kids.reduce(kids.count) { $0 + descendantCount(of: $1.id, in: sessions) }
    }

    public func toggleCollapsed(_ sessionID: String) {
        if collapsedSessions.contains(sessionID) {
            collapsedSessions.remove(sessionID)
        } else {
            collapsedSessions.insert(sessionID)
        }
    }

    // MARK: - Session actions

    public func stopSession(_ session: SessionInfo) { act(session) { await $0.stop(session) } }
    public func resumeSession(_ session: SessionInfo) { act(session) { await $0.resume(session) } }
    public func restartSession(_ session: SessionInfo) { act(session) { await $0.restart(session) } }
    public func interruptSession(_ session: SessionInfo) { act(session) { await $0.interrupt(session) } }
    public func deleteSession(_ session: SessionInfo) { act(session) { await $0.delete(session) } }
    public func renameSession(_ session: SessionInfo, to newName: String) { act(session) { await $0.rename(session, to: newName) } }
    public func toggleStar(_ session: SessionInfo) { act(session) { await $0.toggleStar(session) } }
    public func forkSession(_ session: SessionInfo, name: String) { act(session) { await $0.fork(session, name: name) } }
    public func migrateSession(_ session: SessionInfo, agent: String, model: String? = nil) {
        let trimmed = model?.trimmingCharacters(in: .whitespacesAndNewlines)
        let normalized = (trimmed?.isEmpty ?? true) ? nil : trimmed
        act(session) { await $0.migrate(session, agent: agent, model: normalized) }
    }

    /// Run an action against the session's owning connection, then refresh.
    private func act(_ session: SessionInfo, _ op: @escaping (HostConnection) async -> Void) {
        guard let conn = connection(ownerOf: session.id) else { return }
        Task { await op(conn) }
    }

    /// Create a session on `hostID` and report the created session (found by
    /// name after the connection refreshes) so the caller can select it.
    public func createSession(
        name: String,
        agent: String,
        repoPath: String,
        model: String,
        prompt: String,
        hostID: String = "local",
        completion: @escaping (Result<SessionInfo?, Error>) -> Void
    ) {
        guard let conn = connections.first(where: { $0.id == hostID }) else {
            completion(.failure(FleetError.hostUnavailable))
            return
        }
        let request = CreateRequest(
            name: name,
            agent: agent,
            repoPath: repoPath,
            prompt: prompt.isEmpty ? nil : prompt,
            model: model.isEmpty ? nil : model,
            agentHooks: true
        )
        Task {
            let ok = await conn.create(request)
            if ok {
                completion(.success(conn.sessions.first { $0.name == name }))
            } else {
                completion(.failure(FleetError.createFailed(conn.lastError ?? "create failed")))
            }
        }
    }

    public enum FleetError: LocalizedError {
        case hostUnavailable
        case createFailed(String)
        public var errorDescription: String? {
            switch self {
            case .hostUnavailable: return "That host isn't connected."
            case let .createFailed(m): return m
            }
        }
    }

    // MARK: - Refresh / polling

    /// Refresh every connected host's session list.
    public func refresh() {
        for conn in connections {
            Task { await conn.refresh() }
        }
    }

    public func startPolling() {
        pollTimer?.invalidate()
        pollTimer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.refresh() }
        }
    }

    // MARK: - Single-attach coordination (macOS windows)

    /// App-global single-attach coordination across windows. The daemon enforces
    /// single-attach-per-session; with multiple windows sharing one connection,
    /// two windows showing the same session would ping-pong kicks forever. We
    /// record which owner currently holds a session's live attach; a second
    /// owner sees it's owned elsewhere and shows a placeholder instead of
    /// stealing it. The owner is held weakly, so a window that closes without
    /// releasing frees the session on the next check.
    private final class AttachOwnerRef {
        weak var owner: AnyObject?
        init(_ owner: AnyObject) { self.owner = owner }
    }
    /// `@Published` so a takeover ("Open Here" → `forceClaimAttach`) publishes a
    /// change: SwiftUI re-renders, the new owner swaps its placeholder for a
    /// terminal, and the prior owner sees `isAttachedElsewhere` flip true and
    /// tears its attach down. A plain stored dict would leave the takeover
    /// control inert until some unrelated later render.
    @Published private var attachOwners: [String: AttachOwnerRef] = [:]

    /// Claim a session's attach for `owner` if currently unowned.
    public func claimAttach(_ sessionID: String, owner: AnyObject) {
        if attachOwners[sessionID]?.owner == nil {
            attachOwners[sessionID] = AttachOwnerRef(owner)
        }
    }

    /// Take over a session's attach for `owner`, regardless of the prior owner
    /// (the human explicitly chose "Open Here" in a second window).
    public func forceClaimAttach(_ sessionID: String, owner: AnyObject) {
        attachOwners[sessionID] = AttachOwnerRef(owner)
    }

    /// Release a session's attach if `owner` holds it. Ownership held by another
    /// owner is untouched.
    public func releaseAttach(_ sessionID: String, owner: AnyObject) {
        if attachOwners[sessionID]?.owner === owner {
            attachOwners.removeValue(forKey: sessionID)
        }
    }

    /// Whether `sessionID`'s attach is owned by a live owner other than `owner`.
    public func isAttachedElsewhere(_ sessionID: String, owner: AnyObject) -> Bool {
        guard let current = attachOwners[sessionID]?.owner else { return false }
        return current !== owner
    }
}

/// A session identified across hosts (session IDs are per-daemon, not global).
public struct SessionRef: Hashable, Sendable, Identifiable {
    public let hostID: String
    public let sessionID: String
    public var id: String { "\(hostID)/\(sessionID)" }
    public init(hostID: String, sessionID: String) {
        self.hostID = hostID
        self.sessionID = sessionID
    }
}

/// A session paired with the host it belongs to (for the aggregated sidebar).
public struct HostedSession: Identifiable, Hashable, Sendable {
    public let host: Host
    public let session: SessionInfo
    public var id: String { "\(host.id)/\(session.id)" }
    public var ref: SessionRef { SessionRef(hostID: host.id, sessionID: session.id) }
}

/// An approval paired with the host it belongs to.
public struct HostedApproval: Identifiable, Hashable, Sendable {
    public let host: Host
    public let approval: ApprovalInfo
    public var id: String { "\(host.id)/\(approval.requestID)" }
}
