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

    // MARK: - Sidebar filter state (#906)
    //
    // The selected filter is model state (shared by both GUIs) so it survives
    // view rebuilds and drives the same grouping helpers the sidebars already
    // bind to. The actual filtering is the pure `SidebarFilter`.

    /// Selected view mode (All / Needs Attention / Active).
    @Published public var viewMode: SidebarViewMode = .all
    /// Free-text search over session name + repo.
    @Published public var searchQuery: String = ""
    /// Quick filter: show starred sessions only.
    @Published public var starredOnly: Bool = false
    /// Quick filter: restrict to a single repo (`repoName`); nil = all repos.
    @Published public var repoFilter: String?

    /// Forward each connection's changes up so views bound to derived,
    /// cross-connection state (`sessions`, `allSessions`, `error`, …) refresh.
    private var connectionObservers: [AnyCancellable] = []
    private var registryObserver: AnyCancellable?
    /// Observes the pairing coordinator so a same-process commit-unknown outcome
    /// (which leaves a durable `.acked` candidate rather than a paired host)
    /// triggers the probe-based commit oracle (issue #1299).
    private var pairingObserver: AnyCancellable?
    /// Single-flight guard for the pending-receipt probe, so the init kick and the
    /// pairing-phase observer can't run overlapping probes.
    private var isReconcilingReceipt = false
    /// The last connection fingerprint the model rebuilt for. The registry observer
    /// and `reconcilePendingReceipt` both advance it, so whichever handles a change
    /// first dedups the other — keeping exactly one rebuild/connect per change
    /// (issue #1299).
    private var knownConnectionFingerprint: [String] = []
    private var pollTimer: Timer?
    /// The poll cadence used by `startPolling`, from the [terminal]-equivalent
    /// presentation preferences (issue #1254). Defaults to the historical 2s.
    private let pollInterval: TimeInterval
    /// How long a tailnet reachability TCP probe waits, from the tunable
    /// presentation preferences (issue #1254). Threaded into
    /// `reachability.probe(host:port:timeout:)` — the production caller for the
    /// otherwise-unused configurable timeout. Defaults to the shared 3s.
    private let reachabilityProbeTimeout: TimeInterval

    /// - Parameters:
    ///   - identity: the device ed25519 identity, or nil if it could not be
    ///     created (remote connections + pairing are then unavailable).
    ///   - reachability: the tailnet probe (iOS); nil on platforms that only
    ///     talk to a local daemon.
    ///   - poll: when true, refresh every connection on a timer (the macOS
    ///     desktop behaviour). iOS refreshes on connect/foreground instead.
    ///   - pollInterval: seconds between automatic refreshes when `poll` is on.
    ///     Defaults to the shared presentation default (2s).
    ///   - reachabilityProbeTimeout: seconds a tailnet reachability probe waits
    ///     before treating a host as unreachable, from the tunable presentation
    ///     preferences (#1254). Defaults to the shared 3s.
    public init(
        registry: HostRegistry,
        identity: DeviceIdentity?,
        reachability: TailnetReachability?,
        factory: HostClientFactory,
        pairing: PairingCoordinator,
        poll: Bool = false,
        subscribeApprovals: Bool = true,
        pollInterval: TimeInterval = PresentationPreferences.default.fleetPollInterval,
        reachabilityProbeTimeout: TimeInterval = PresentationPreferences.default.reachabilityProbeTimeout
    ) {
        self.registry = registry
        self.identity = identity
        self.reachability = reachability
        self.factory = factory
        self.pairing = pairing
        self.subscribeApprovals = subscribeApprovals
        self.pollInterval = max(0.1, pollInterval)
        self.reachabilityProbeTimeout = max(0.1, reachabilityProbeTimeout)
        rebuildConnections()
        // Rebuild + reconnect when any *connection-relevant* host state changes (a
        // pairing completes, a host is removed, a pin/deviceID/isPaired transition).
        // Gated on the connection fingerprint — NOT the id set alone: a
        // commit-unknown pairing flips an existing placeholder's `isPaired` without
        // adding an id, so an id-only gate would ignore the transition and never
        // dial the newly-trusted host (issue #1299). Display-only mutations
        // (`markSeen`, `label`) leave the fingerprint unchanged, so they still
        // don't tear down and re-dial every connection.
        knownConnectionFingerprint = Self.connectionFingerprint(registry.hosts)
        // @Published fires in willSet (registry.hosts still holds the OLD value when
        // the closure runs), so the rebuild MUST be deferred to a Task that reads the
        // committed state. rebuildAndConnectIfChanged re-checks the fingerprint, so a
        // change reconcile already handled is a no-op (one rebuild per change).
        registryObserver = registry.$hosts
            .dropFirst()
            .sink { [weak self] _ in
                Task { @MainActor in await self?.rebuildAndConnectIfChanged() }
            }
        // Recover a pairing that was in flight when the app last exited: probe the
        // pending `.acked` receipt and commit or discard it (issue #1299).
        Task { [weak self] in await self?.reconcilePendingReceipt() }
        // Re-probe when an in-process pairing settles to a commit-unknown outcome,
        // which leaves a durable `.acked` candidate for the same oracle.
        pairingObserver = pairing.$phase
            .dropFirst()
            .sink { [weak self] _ in
                Task { @MainActor in await self?.reconcilePendingReceipt() }
            }
        if poll {
            // Desktop (always-on) mode: connect immediately and keep polling.
            // iOS instead drives connect/reconnect from the view lifecycle
            // (`RootView.task` / scene-phase changes).
            startPolling()
            Task { await connectAll() }
        }
    }

    /// Rebuild + reconnect when the connection fingerprint has changed since the
    /// last rebuild. Exposed (internal) so tests can await the registry observer's
    /// effect deterministically instead of polling. Idempotent via the fingerprint
    /// baseline — a change already handled by `reconcilePendingReceipt` is a no-op,
    /// so there is exactly one rebuild/connect per change (issue #1299).
    func rebuildAndConnectIfChanged() async {
        let fingerprint = Self.connectionFingerprint(registry.hosts)
        guard fingerprint != knownConnectionFingerprint else { return }
        knownConnectionFingerprint = fingerprint
        rebuildConnections()
        await connectAll()
    }

    // MARK: - Pending-pairing recovery (issue #1299)

    /// Resolve a pending `.acked` pairing receipt using an authenticated
    /// connection as the commit oracle. A receipt is ambiguous — the daemon may
    /// have durably committed the device before the client crashed / lost the
    /// commit reply, or it may have timed the request out — so we never trust it
    /// as paired on disk. Instead we probe with the candidate credential:
    ///
    ///   • auth accepted  → the daemon committed → promote (`commitCandidate`).
    ///   • auth rejected  → the daemon never committed → discard the candidate and
    ///                      restore the prior state (no ghost paired host).
    ///   • transport/TLS/timeout → indeterminate → keep the journal for a later
    ///                      retry — never discards a possibly-committed credential.
    public func reconcilePendingReceipt() async {
        guard !isReconcilingReceipt, let identity, let pending = registry.pendingReceipt() else { return }
        isReconcilingReceipt = true
        defer { isReconcilingReceipt = false }

        let client = factory.makeClient(
            transport: pending.host.transport,
            credentials: pending.credentials,
            signer: identity
        )
        do {
            try await client.connect()
            await client.disconnect()
            // Authenticated → the daemon committed the device. Promote the candidate,
            // then rebuild + connect it DETERMINISTICALLY here (awaited) and advance
            // the connection-fingerprint baseline, so the registry observer's Task
            // for the same commit is a no-op — exactly one rebuild/connect path, and
            // `await`-able so callers see a settled result (issue #1299).
            try registry.commitCandidate(hostID: pending.host.id)
            knownConnectionFingerprint = Self.connectionFingerprint(registry.hosts)
            rebuildConnections()
            await connectAll()
        } catch let error as GraithClientError where Self.isAuthRejection(error) {
            // The daemon rejected the candidate: it never committed (it timed out).
            // Discard the candidate; a prior working credential (re-pair) survives.
            registry.discardPendingReceipt(hostID: pending.host.id, createdPlaceholder: pending.createdPlaceholder)
        } catch {
            // Transport/TLS/timeout — indeterminate. Keep the journal for retry.
        }
    }

    /// Whether a probe error definitively proves the daemon never committed this
    /// device — the ONLY signal that may discard a candidate (issue #1299).
    ///
    /// Only a canonical invalid-token / not-authorized rejection (`.notPaired`)
    /// qualifies. Everything else is indeterminate and must RETAIN the candidate:
    ///   • `.authenticationFailed` — a handshake rejection (profile / protocol
    ///     version mismatch), unrelated to whether the device was committed.
    ///   • `.daemon` — includes a generic "proof of possession failed", which the
    ///     daemon returns for a bad signature or a tailnet-identity mismatch that
    ///     can happen to a *committed* candidate — so it is NOT proof of no-commit.
    ///   • `.tlsPinMismatch`, `.tailnetUnreachable`, `.disconnected`, `.decoding`
    ///     — transport / TLS / timeout failures, all indeterminate.
    private static func isAuthRejection(_ error: GraithClientError) -> Bool {
        if case .notPaired = error { return true }
        return false
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

    /// A comparable snapshot of every host's *connection-relevant* fields (the
    /// same fields `connectionUnchanged` compares, in registry order). Used to gate
    /// the registry observer: a change here means some connection must be rebuilt,
    /// whereas a display-only mutation (`markSeen`, `label`) leaves it identical.
    /// Ordering matters — a reorder that changes which host a client dials must
    /// still rebuild.
    private static func connectionFingerprint(_ hosts: [Host]) -> [String] {
        hosts.map { h in
            [h.id, h.kind.rawValue, h.socketPath ?? "", h.magicDNSName ?? "",
             String(h.port), h.daemonProfile, h.tlsPinSPKI, h.deviceID, String(h.isPaired)]
                .joined(separator: "\u{1f}")
        }
    }

    /// Connect all hosts (called on appear / on returning to foreground).
    public func connectAll() async {
        await withTaskGroup(of: Void.self) { group in
            for conn in connections {
                group.addTask { await conn.connect() }
            }
        }
        await refreshReachability()
        // A normal connect is also the natural retry point for a pending pairing
        // receipt whose earlier probe hit a transport/TLS/timeout failure — so a
        // recovered link promotes it without an app restart. Awaited (not
        // fire-and-forget) so `await connectAll()` fully settles the recovery;
        // recursion-safe because reconcile's own `connectAll` re-enters while
        // `isReconcilingReceipt` is set, and reconcilePendingReceipt self-guards on it.
        await reconcilePendingReceipt()
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
    /// connection is live we are demonstrably on the tailnet. When none is
    /// live, fall back to a cheap TCP probe of the configured hosts using the
    /// tunable `reachabilityProbeTimeout` (#1254) — this is the production
    /// caller for that preference. A host that accepts a TCP connection proves
    /// the tailnet is routable even though the control handshake failed (daemon
    /// down), so the "open Tailscale" banner should *not* show; only when every
    /// probe also fails do we report `.notOnTailnet`. Hosts without a probeable
    /// endpoint fall back to the plain aggregate observation.
    private func refreshReachability() async {
        guard let reachability, !connections.isEmpty else { return }
        if connections.contains(where: { $0.state == .connected }) {
            reachability.observed(reachable: true)
            return
        }
        let targets = reachabilityProbeTargets()
        guard !targets.isEmpty else {
            reachability.observed(reachable: false)
            return
        }
        for target in targets {
            await reachability.probe(host: target.host, port: target.port,
                                     timeout: reachabilityProbeTimeout)
            // A single reachable host is enough to prove we're on the tailnet.
            if reachability.state == .onTailnet { return }
        }
    }

    /// The `host:port` endpoints a reachability probe can dial: remote hosts
    /// that advertise a MagicDNS name. Local (Unix-socket) hosts have no tailnet
    /// endpoint to probe, so they're excluded.
    private func reachabilityProbeTargets() -> [(host: String, port: UInt16)] {
        registry.hosts.compactMap { host in
            guard host.kind == .remote, let dns = host.magicDNSName, !dns.isEmpty else { return nil }
            return (dns, host.port)
        }
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

    /// Every running scenario across hosts, tagged with its host (scenarios are
    /// per-daemon), sorted by host order then scenario name.
    public var hostedScenarios: [HostedScenario] {
        connections.flatMap { conn in
            conn.scenarios
                .sorted { $0.name < $1.name }
                .map { HostedScenario(host: conn.entry, scenario: $0) }
        }
    }

    /// The host-agnostic scenario list (single-host rendering path).
    public var scenarios: [ScenarioRecord] {
        hostedScenarios.map(\.scenario)
    }

    /// The member `SessionInfo`s of a scenario, resolved from the owning host's
    /// live session list (a member may be soft-deleted/absent, so this filters to
    /// what's currently live), in the scenario's declared order.
    public func sessions(in scenario: HostedScenario) -> [SessionInfo] {
        guard let conn = connections.first(where: { $0.id == scenario.host.id }) else { return [] }
        let byID = Dictionary(uniqueKeysWithValues: conn.sessions.map { ($0.id, $0) })
        return scenario.scenario.sessions.compactMap { byID[$0.sessionID] }
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

    // MARK: - Sidebar filter (#906)

    /// The currently-selected filter criteria, assembled from the published
    /// filter state. Passed to the pure `SidebarFilter`.
    public var filterCriteria: SidebarFilter.Criteria {
        SidebarFilter.Criteria(
            viewMode: viewMode,
            searchQuery: searchQuery,
            starredOnly: starredOnly,
            repo: repoFilter
        )
    }

    /// Whether any filter is actively narrowing the session list (drives the
    /// sidebar's "clear filters" affordance and empty state).
    public var isFilterActive: Bool { filterCriteria.isActive }

    /// Apply the current filter criteria to an arbitrary session list. iOS
    /// renders per-connection, so it filters each `HostConnection.sessions`
    /// through this; macOS filters via the grouping helpers below.
    public func filtered(_ sessions: [SessionInfo]) -> [SessionInfo] {
        SidebarFilter.apply(sessions, filterCriteria)
    }

    /// The distinct repo names across all sessions, sorted — for the repo
    /// quick-filter menu. Ignores the current repo filter so the menu can
    /// always switch to any repo. Derived from `allSessions` (every connection)
    /// rather than the id-deduplicated `sessions`, so a repo isn't dropped from
    /// the menu when two hosts happen to share a per-daemon session id.
    public var availableRepos: [String] {
        Set(allSessions.map(\.session.repoName)).sorted()
    }

    /// Reset every filter to its default (used by a "clear filters" action).
    public func clearFilters() {
        viewMode = .all
        searchQuery = ""
        starredOnly = false
        repoFilter = nil
    }

    // MARK: - Sidebar grouping

    private func groups(for sessions: [SessionInfo]) -> [(repo: String, sessions: [SessionInfo])] {
        Dictionary(grouping: sessions) { $0.repoName }
            .sorted { $0.key < $1.key }
            .map { (repo: $0.key, sessions: $0.value.sorted { $0.name < $1.name }) }
    }

    /// Sessions grouped by repo (flat, host-agnostic), filtered by the current
    /// sidebar criteria (#906).
    public var sessionsByRepo: [(repo: String, sessions: [SessionInfo])] {
        groups(for: filtered(sessions))
    }

    /// Sessions grouped by host, then repo — for the multi-host sidebar, with
    /// the current sidebar filter applied per host (#906). Every configured host
    /// still appears even with no (matching) sessions, so its connection state
    /// stays visible.
    public var sessionsByHost: [(host: Host, groups: [(repo: String, sessions: [SessionInfo])])] {
        connections.map { conn in (host: conn.entry, groups: groups(for: filtered(conn.sessions))) }
    }

    /// Unfiltered repo grouping — used where the raw list is wanted regardless
    /// of the sidebar filter.
    public var allSessionsByRepo: [(repo: String, sessions: [SessionInfo])] {
        groups(for: sessions)
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
    /// Hard-delete a live session (`gr purge`). Callers surface the destructive
    /// confirmation before invoking this.
    public func purgeSession(_ session: SessionInfo) { act(session) { await $0.purge(session) } }
    /// Set (or clear) a live session's status summary (`gr status`).
    public func setStatus(_ session: SessionInfo, text: String, ttlSeconds: Int? = nil, clear: Bool = false) {
        act(session) { await $0.setStatus(session, text: text, ttlSeconds: ttlSeconds, clear: clear) }
    }
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

    // MARK: - Scenario actions (#903)
    //
    // Scenarios are per-daemon, so an action is routed to the owning host. Only
    // the human-authorized lifecycle verbs are exposed (stop/resume/delete);
    // start and task-done are orchestrator-session-scoped and stay CLI-only.

    public func stopScenario(name: String, hostID: String) { actScenario(hostID) { await $0.stopScenario(name) } }
    public func resumeScenario(name: String, hostID: String) { actScenario(hostID) { await $0.resumeScenario(name) } }
    public func deleteScenario(name: String, hostID: String) { actScenario(hostID) { await $0.deleteScenario(name) } }

    public func stopScenario(_ scenario: HostedScenario) { stopScenario(name: scenario.scenario.name, hostID: scenario.host.id) }
    public func resumeScenario(_ scenario: HostedScenario) { resumeScenario(name: scenario.scenario.name, hostID: scenario.host.id) }
    public func deleteScenario(_ scenario: HostedScenario) { deleteScenario(name: scenario.scenario.name, hostID: scenario.host.id) }

    private func actScenario(_ hostID: String, _ op: @escaping (HostConnection) async -> Void) {
        guard let conn = connections.first(where: { $0.id == hostID }) else { return }
        Task { await op(conn) }
    }

    // MARK: - Messaging (gr msg)
    //
    // Routed to the session's owning connection. These are `async` (unlike the
    // fire-and-forget lifecycle actions above) so the compose/inbox UI can await
    // the result — a send reports success/failure, a fetch returns the messages.

    /// Send a direct message to `session`'s inbox. Returns true on success.
    @discardableResult
    public func sendMessage(to session: SessionInfo, body: String) async -> Bool {
        guard let conn = connection(ownerOf: session.id) else { return false }
        return await conn.sendMessage(to: session, body: body)
    }

    /// Fetch `session`'s direct-message conversation (both directions), oldest
    /// first. `limit > 0` returns only the most recent `limit` messages.
    public func conversation(for session: SessionInfo, limit: Int = 0) async -> [ConversationMessage] {
        guard let conn = connection(ownerOf: session.id) else { return [] }
        return await conn.conversation(for: session, limit: limit)
    }

    /// Mark `session`'s inbox read (clears its unread count). Returns true on
    /// success, false if the host is unavailable or the ack failed.
    @discardableResult
    public func ackInbox(for session: SessionInfo) async -> Bool {
        guard let conn = connection(ownerOf: session.id) else { return false }
        return await conn.ackInbox(for: session)
    }

    // MARK: - Deleted sessions (restore / purge)
    //
    // A soft-deleted session is absent from every connection's live `sessions`,
    // so `act()` (which resolves the owner via `connection(ownerOf:)`) can't
    // reach it. The Deleted surface therefore carries the host explicitly.

    /// Every host's soft-deleted sessions, tagged with their host, for the
    /// Deleted/restore view. Fetched on demand (not polled).
    public func deletedSessions() async -> [HostedSession] {
        var result: [HostedSession] = []
        for conn in connections {
            let deleted = await conn.deletedSessions()
            result.append(contentsOf: deleted.map { HostedSession(host: conn.entry, session: $0) })
        }
        return result
    }

    /// Restore a soft-deleted session on `hostID` (inverse of a soft delete).
    /// `async` so the Deleted view can await it and only then re-list — a
    /// fire-and-forget Task would race the re-fetch and leave a stale row on a
    /// slow link.
    public func restore(_ session: SessionInfo, hostID: String) async {
        guard let conn = connections.first(where: { $0.id == hostID }) else { return }
        await conn.restore(session)
    }

    /// Hard-delete a session on `hostID` (`gr purge`). Works whether the session
    /// is live or already soft-deleted; callers confirm first. `async` for the
    /// same await-then-relist reason as `restore`.
    public func purge(_ session: SessionInfo, hostID: String) async {
        guard let conn = connections.first(where: { $0.id == hostID }) else { return }
        await conn.purge(session)
    }

    /// Client-side mirror of `gr new`'s mutual-exclusion checks, so a New Session
    /// form can surface the error before a daemon round-trip. Returns nil when the
    /// options are valid. Kept pure (static, no state) so it's directly testable
    /// and callable from either GUI's form.
    public static func validateCreateOptions(base: String, inPlace: Bool) -> String? {
        if inPlace && !base.trimmingCharacters(in: .whitespaces).isEmpty {
            return "In-place sessions run in the repo without a branch, so a base branch can't be set."
        }
        return nil
    }

    /// Create a session on `hostID` and report the created session (found by
    /// name after the connection refreshes) so the caller can select it.
    ///
    /// The advanced options (`base`, `yolo`, `inPlace`, `agentHooks`) mirror the
    /// matching `gr new` flags and are shared by both GUIs' New Session forms.
    public func createSession(
        name: String,
        agent: String,
        repoPath: String,
        model: String,
        prompt: String,
        base: String = "",
        yolo: Bool = false,
        inPlace: Bool = false,
        agentHooks: Bool = true,
        hostID: String = "local",
        completion: @escaping (Result<SessionInfo?, Error>) -> Void
    ) {
        // Normalise once so validation and the wire agree on what "empty" means:
        // a whitespace-only base must be treated as absent on both sides, or it
        // slips past the guard and the daemon rejects it after a round-trip.
        let trimmedBase = base.trimmingCharacters(in: .whitespacesAndNewlines)
        if let invalid = Self.validateCreateOptions(base: trimmedBase, inPlace: inPlace) {
            completion(.failure(FleetError.invalidOptions(invalid)))
            return
        }
        guard let conn = connections.first(where: { $0.id == hostID }) else {
            completion(.failure(FleetError.hostUnavailable))
            return
        }
        let request = CreateRequest(
            name: name,
            agent: agent,
            repoPath: repoPath,
            base: trimmedBase.isEmpty ? nil : trimmedBase,
            prompt: prompt.isEmpty ? nil : prompt,
            model: model.isEmpty ? nil : model,
            // Yolo forces the approval hook on daemon-side (agentHooks || yolo),
            // so send the effective value the session will actually run with.
            agentHooks: agentHooks || yolo,
            inPlace: inPlace ? true : nil,
            yolo: yolo ? true : nil
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

    /// A friendly, UI-facing string for any client error. Shared by the views
    /// (config viewer / diagnostics panel) that surface a fetch failure.
    public static func describeError(_ error: Error) -> String {
        if let e = error as? GraithClientError { return e.userMessage }
        return error.localizedDescription
    }

    public enum FleetError: LocalizedError {
        case hostUnavailable
        case createFailed(String)
        case invalidOptions(String)
        public var errorDescription: String? {
            switch self {
            case .hostUnavailable: return "That host isn't connected."
            case let .createFailed(m): return m
            case let .invalidOptions(m): return m
            }
        }
    }

    // MARK: - Host introspection (config viewer + diagnostics, #904)

    /// Fetch a host's effective configuration + diff-vs-defaults for the
    /// read-only config viewer. Defaults to the local daemon. Throws so the view
    /// can show a fetch error distinctly from an empty/default config.
    public func config(hostID: String = "local") async throws -> ConfigResponseMsg {
        guard let conn = connections.first(where: { $0.id == hostID }) else {
            throw FleetError.hostUnavailable
        }
        return try await conn.config()
    }

    /// The configured agent catalog + default_agent for a host's New Session /
    /// Settings pickers (#1234), with explicit loading/unavailable state and no
    /// client-side catalog. Defaults to the local daemon.
    public func agentCatalog(hostID: String = "local") -> AgentCatalogState {
        connections.first(where: { $0.id == hostID })?.agentCatalogState
            ?? .unavailable("That host isn't connected.")
    }

    /// Fetch a host's agent catalog on demand for the New Session / Settings
    /// pickers (#1234), refreshing the cached availability state. Non-throwing:
    /// an offline or old daemon yields `.unavailable`.
    public func fetchAgentCatalog(hostID: String = "local") async -> AgentCatalogState {
        guard let conn = connections.first(where: { $0.id == hostID }) else {
            return .unavailable("That host isn't connected.")
        }
        return await conn.fetchAgentCatalog()
    }

    /// Fetch a host's health snapshot for the diagnostics panel. Defaults to the
    /// local daemon.
    public func diagnostics(hostID: String = "local") async throws -> DiagnosticsMsg {
        guard let conn = connections.first(where: { $0.id == hostID }) else {
            throw FleetError.hostUnavailable
        }
        return try await conn.diagnostics()
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
        pollTimer = Timer.scheduledTimer(withTimeInterval: pollInterval, repeats: true) { [weak self] _ in
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

/// A scenario paired with the host it belongs to (scenarios are per-daemon).
public struct HostedScenario: Identifiable, Hashable, Sendable {
    public let host: Host
    public let scenario: ScenarioRecord
    public var id: String { "\(host.id)/\(scenario.id)" }
    public init(host: Host, scenario: ScenarioRecord) {
        self.host = host
        self.scenario = scenario
    }
}
