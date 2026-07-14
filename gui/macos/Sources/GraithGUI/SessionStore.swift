import Foundation
import Combine
import GraithProtocol
import GraithRemoteKit

/// Disambiguate our `Host` model from `Foundation.Host` (macOS ships both once
/// GraithRemoteKit is imported). A module-scope typealias shadows the Foundation
/// type across the whole GraithGUI module, mirroring the in-module `Host` struct
/// this replaced.
typealias Host = GraithRemoteKit.Host

/// Resolves the local daemon's Unix socket path, mirroring the Go client's
/// `config.ResolvePaths` on macOS.
///
/// On macOS `adrg/xdg` maps the (unset) `XDG_RUNTIME_DIR` to
/// `~/Library/Application Support`, and the daemon puts its socket at
/// `<runtimeDir>/<appName>/graith.sock` where `<appName>` is `graith` (default
/// profile) or `graith-<profile>`.
enum GraithLocalSocket {
    static func defaultPath() -> String {
        let env = ProcessInfo.processInfo.environment
        let profile = env["GRAITH_PROFILE"] ?? ""
        let appName = profile.isEmpty ? "graith" : "graith-\(profile)"

        // Mirror the daemon's runtimeDirForApp() (internal/config/paths.go):
        // when XDG_RUNTIME_DIR is set the socket is <runtimeDir>/<app>/graith.sock;
        // otherwise it is <dataHome>/<app>/run/graith.sock — note the "run"
        // component, which the daemon appends only in the fallback case.
        if let xdg = env["XDG_RUNTIME_DIR"], !xdg.isEmpty {
            return "\(xdg)/\(appName)/graith.sock"
        }

        return "\(NSHomeDirectory())/Library/Application Support/\(appName)/run/graith.sock"
    }

    static var profile: String { ProcessInfo.processInfo.environment["GRAITH_PROFILE"] ?? "" }

    /// The local daemon host entry for this machine.
    static func localHost() -> Host {
        Host.local(socketPath: defaultPath(), profile: profile)
    }
}

@MainActor
class SessionStore: ObservableObject {
    @Published var sessions: [Session] = []
    /// The primary (local daemon) error, kept for the sidebar footer.
    @Published var error: String?
    /// Per-host connection error, keyed by host id (shown in host headers).
    @Published var hostErrors: [String: String] = [:]
    @Published var collapsedSessions: Set<String> = []

    enum RendererType: String, CaseIterable {
        case ghosttyCoreText = "Ghostty (Core Text)"
        case ghosttyMetal = "Ghostty (Metal)"
    }

    @Published var renderer: RendererType = .ghosttyCoreText
    @Published var fontSize: CGFloat = Theme.defaultFontSize

    /// App-global single-attach coordination across windows.
    ///
    /// The daemon enforces single-attach-per-session: a second `attach` to a
    /// session kicks the first (design §"Single-attach-per-session"). With
    /// multiple windows sharing one connection, two windows showing the same
    /// session would otherwise ping-pong kicks forever. We record which window
    /// currently owns a session's live attach; a second window sees it's owned
    /// elsewhere and shows a placeholder instead of stealing it. Windows owning
    /// *different* sessions never appear here for the same key, so they don't
    /// fight.
    ///
    /// The owner is held *weakly*: if a window closes without releasing (its
    /// `onDisappear` didn't fire), the reference goes nil and the session
    /// becomes available again on the next render — no stale entry can wedge a
    /// session as "open elsewhere" forever.
    private final class AttachOwnerRef {
        weak var window: WindowState?
        init(_ window: WindowState) { self.window = window }
    }
    @Published private var attachOwners: [String: AttachOwnerRef] = [:]

    /// Claim a session's attach for `owner` if it is currently unowned. No-op if
    /// another (live) window already owns it, or `owner` already holds it.
    func claimAttach(_ sessionID: String, owner: WindowState) {
        if attachOwners[sessionID]?.window == nil {
            attachOwners[sessionID] = AttachOwnerRef(owner)
        }
    }

    /// Take over a session's attach for `owner`, regardless of the prior owner.
    /// Used when the human explicitly chooses "Open Here" in a second window —
    /// an intentional takeover, not a silent kick.
    func forceClaimAttach(_ sessionID: String, owner: WindowState) {
        attachOwners[sessionID] = AttachOwnerRef(owner)
    }

    /// Release a session's attach if `owner` holds it (window closed, switched
    /// away, or session deleted). Ownership held by another window is untouched.
    func releaseAttach(_ sessionID: String, owner: WindowState) {
        if attachOwners[sessionID]?.window === owner {
            attachOwners.removeValue(forKey: sessionID)
        }
    }

    /// Whether `sessionID`'s attach is owned by a *live* window other than
    /// `owner`. A nil (closed-window) owner counts as available.
    func isAttachedElsewhere(_ sessionID: String, owner: WindowState) -> Bool {
        guard let current = attachOwners[sessionID]?.window else { return false }
        return current !== owner
    }

    // MARK: - Multi-host connections

    /// The registry of daemons (local + paired remotes). Owns pairing metadata;
    /// per-host client tokens live in the Keychain.
    let registry: HostRegistry
    /// The device's ed25519 identity, used to sign proof-of-possession for
    /// remote hosts. Nil only if the Keychain/identity could not be created.
    let identity: DeviceIdentity?

    /// One protocol client per reachable host, keyed by host id. Local uses the
    /// Unix socket (tokenless); remotes use TLS + a Keychain token + PoP signer.
    private var clients: [String: GraithProtocolClient] = [:]
    /// The host each client was built from, so we only rebuild on change.
    private var builtFrom: [String: Host] = [:]
    /// Which host owns each session id (session ids are per-daemon).
    private var hostBySession: [String: String] = [:]

    private var timer: Timer?
    private var refreshGeneration: UInt64 = 0
    /// Guards against overlapping refresh passes piling up when a host is slow:
    /// the 2s timer skips a tick while one refresh is still in flight.
    private var isRefreshing = false
    /// Per-host list() deadline — a host that connects but never replies fails
    /// closed after this rather than wedging the whole refresh forever.
    private let hostRefreshTimeout: Double = 12.0
    private var cancellables = Set<AnyCancellable>()

    /// Production initializer: builds clients from the registry.
    init(registry: HostRegistry, identity: DeviceIdentity?) {
        self.registry = registry
        self.identity = identity
        rebuildClients()
        // Rebuild + refresh whenever the set of paired hosts changes (e.g. a
        // pairing completes or a host is removed).
        registry.$hosts
            .dropFirst()
            .sink { [weak self] _ in
                Task { @MainActor in
                    self?.rebuildClients()
                    self?.refresh()
                }
            }
            .store(in: &cancellables)
        refresh()
        startPolling()
    }

    /// Convenience initializer for tests / previews: local host only, backed by
    /// an in-memory secret store so no Keychain access is attempted.
    convenience init() {
        let secrets = InMemorySecretStore()
        let identity = try? DeviceIdentity(keychain: secrets)
        let registry = HostRegistry(
            keychain: secrets,
            localHost: GraithLocalSocket.localHost(),
            storeURL: FileManager.default.temporaryDirectory
                .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
                .appendingPathComponent("hosts.json")
        )
        self.init(registry: registry, identity: identity)
    }

    /// (Re)build the per-host clients from the registry, preserving existing
    /// clients for hosts whose transport/credentials are unchanged.
    private func rebuildClients() {
        var next: [String: GraithProtocolClient] = [:]
        var builds: [String: Host] = [:]
        for host in registry.hosts {
            if let existing = clients[host.id], let prev = builtFrom[host.id],
               Self.connectionUnchanged(prev, host) {
                next[host.id] = existing
                builds[host.id] = host
                continue
            }
            guard let client = makeClient(for: host) else { continue }
            next[host.id] = client
            builds[host.id] = host
        }
        // Close clients for hosts that went away.
        for (id, client) in clients where next[id] == nil {
            Task { await client.close() }
        }
        clients = next
        builtFrom = builds
    }

    /// Whether two host records describe the *same connection*, so a cached
    /// client can be reused. Deliberately ignores display-only fields (label,
    /// `lastSeen`) — otherwise a future `markSeen()` tick would tear down and
    /// re-dial every remote client each refresh.
    private static func connectionUnchanged(_ a: Host, _ b: Host) -> Bool {
        a.id == b.id && a.kind == b.kind && a.transport == b.transport
            && a.daemonProfile == b.daemonProfile && a.deviceID == b.deviceID
            && a.isPaired == b.isPaired
    }

    private func makeClient(for host: Host) -> GraithProtocolClient? {
        switch host.kind {
        case .local:
            // The desktop app is the *local human*: it owns the 0700 Unix socket
            // trust boundary and connects tokenless (no PoP). We deliberately do
            // NOT forward GRAITH_TOKEN — that per-session token would make the
            // daemon treat every window as the launching agent.
            return GraithProtocolClient(
                transport: host.transport,
                profile: host.daemonProfile,
                clientID: "graith-macos",
                token: nil,
                signer: nil
            )
        case .remote:
            guard let creds = registry.credentials(for: host), let identity else { return nil }
            // Sign PoP with the per-host device ID (a later pairing of host B
            // must not clobber how we identify to host A).
            let signer = HostScopedSigner(base: identity, deviceID: creds.deviceID)
            return GraithProtocolClient(
                transport: host.transport,
                profile: creds.daemonProfile,
                clientID: "graith-macos",
                token: creds.clientToken,
                signer: signer
            )
        }
    }

    /// The client for a host id, if connected.
    func client(forHost hostID: String) -> GraithProtocolClient? { clients[hostID] }

    /// The client owning `sessionID` (the daemon it lives on).
    func client(for sessionID: String) -> GraithProtocolClient? {
        guard let hostID = hostBySession[sessionID] else { return clients["local"] }
        return clients[hostID] ?? clients["local"]
    }

    /// The host a session belongs to.
    func host(for sessionID: String) -> Host? {
        guard let hostID = hostBySession[sessionID] else { return nil }
        return registry.host(id: hostID)
    }

    /// All (host, client) pairs, in registry order — used by the approvals
    /// monitor to subscribe across every daemon.
    var hostClients: [(host: Host, client: GraithProtocolClient)] {
        registry.hosts.compactMap { host in
            guard let client = clients[host.id] else { return nil }
            return (host, client)
        }
    }

    /// Remove a remote host: close its client, drop it from the registry (which
    /// wipes the Keychain token and triggers a rebuild).
    func removeHost(_ host: Host) {
        guard host.kind != .local else { return }
        if let client = clients[host.id] {
            Task { await client.close() }
        }
        registry.remove(hostID: host.id)
    }

    // MARK: - Grouping for the sidebar

    /// Sessions on a single host grouped by repo name.
    private func groups(for sessions: [Session]) -> [(repo: String, sessions: [Session])] {
        let grouped = Dictionary(grouping: sessions) { $0.repoName }
        return grouped.sorted { $0.key < $1.key }
            .map { (repo: $0.key, sessions: $0.value.sorted { $0.name < $1.name }) }
    }

    /// Sessions grouped by repo (flat, host-agnostic). Retained for the
    /// single-host rendering path.
    var sessionsByRepo: [(repo: String, sessions: [Session])] {
        groups(for: sessions)
    }

    /// Whether any remote hosts are configured (drives the sidebar's per-host
    /// grouping vs. the familiar single-host layout).
    var hasRemoteHosts: Bool {
        registry.hosts.contains { $0.kind == .remote }
    }

    /// Sessions grouped by host, then repo — for the multi-host sidebar. Every
    /// configured host appears even with no sessions, so its connection state is
    /// visible.
    var sessionsByHost: [(host: Host, groups: [(repo: String, sessions: [Session])])] {
        registry.hosts.map { host in
            let owned = sessions.filter { hostBySession[$0.id] == host.id }
            return (host: host, groups: groups(for: owned))
        }
    }

    func roots(in sessions: [Session]) -> [Session] {
        let ids = Set(sessions.map(\.id))
        return sessions.filter {
            $0.parentID == nil || $0.parentID!.isEmpty || !ids.contains($0.parentID!)
        }
    }

    func children(of parentID: String, in sessions: [Session]) -> [Session] {
        sessions.filter { $0.parentID == parentID }
    }

    func descendantCount(of sessionID: String, in sessions: [Session]) -> Int {
        let kids = children(of: sessionID, in: sessions)
        return kids.reduce(kids.count) { $0 + descendantCount(of: $1.id, in: sessions) }
    }

    func toggleCollapsed(_ sessionID: String) {
        if collapsedSessions.contains(sessionID) {
            collapsedSessions.remove(sessionID)
        } else {
            collapsedSessions.insert(sessionID)
        }
    }

    // MARK: - Session Actions

    func stopSession(_ session: Session) {
        runAction(session) { try await $0.stop(sessionID: session.id) }
    }

    func resumeSession(_ session: Session) {
        runAction(session) { try await $0.resume(sessionID: session.id) }
    }

    func deleteSession(_ session: Session) {
        runAction(session) { try await $0.delete(sessionID: session.id) }
    }

    func restartSession(_ session: Session) {
        runAction(session) { try await $0.restart(sessionID: session.id) }
    }

    func interruptSession(_ session: Session) {
        runAction(session) { try await $0.interrupt(sessionID: session.id) }
    }

    func renameSession(_ session: Session, to newName: String) {
        let trimmed = newName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, trimmed != session.name else { return }
        runAction(session) { try await $0.rename(sessionID: session.id, newName: trimmed) }
    }

    /// Toggle a session's star, calling `unstar` when currently starred and
    /// `star` otherwise.
    func toggleStar(_ session: Session) {
        let starred = session.starred ?? false
        runAction(session) {
            if starred {
                try await $0.unstar(sessionID: session.id)
            } else {
                try await $0.star(sessionID: session.id)
            }
        }
    }

    /// Fork `session` into a new session named `name` on the same host, then
    /// refresh so the fork appears in the sidebar.
    func forkSession(_ session: Session, name: String) {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        guard let client = client(for: session.id) else {
            self.error = SessionStoreError.hostUnavailable.localizedDescription
            return
        }
        let hostID = hostBySession[session.id] ?? "local"
        Task {
            do {
                let forked = try await client.fork(name: trimmed, sourceSessionID: session.id)
                hostBySession[forked.id] = hostID
            } catch {
                self.error = error.localizedDescription
            }
            refresh()
        }
    }

    /// Migrate `session` to a different agent (and optionally model) in place.
    func migrateSession(_ session: Session, agent: String, model: String? = nil) {
        let trimmedModel = model?.trimmingCharacters(in: .whitespacesAndNewlines)
        runAction(session) {
            _ = try await $0.migrate(
                sessionID: session.id,
                agent: agent,
                model: (trimmedModel?.isEmpty ?? true) ? nil : trimmedModel
            )
        }
    }

    func createSession(
        name: String,
        agent: String,
        repoPath: String,
        model: String,
        prompt: String,
        hostID: String = "local",
        completion: @escaping (Result<Session?, Error>) -> Void
    ) {
        guard let client = clients[hostID] else {
            completion(.failure(SessionStoreError.hostUnavailable))
            return
        }
        let msg = CreateMsg(
            name: name,
            agent: agent,
            repoPath: repoPath,
            prompt: prompt.isEmpty ? nil : prompt,
            model: model.isEmpty ? nil : model,
            agentHooks: true
        )
        Task {
            do {
                let session = try await client.create(msg)
                hostBySession[session.id] = hostID
                refresh()
                completion(.success(session))
            } catch {
                completion(.failure(error))
            }
        }
    }

    enum SessionStoreError: LocalizedError {
        case hostUnavailable
        var errorDescription: String? {
            switch self {
            case .hostUnavailable: return "That host isn't connected."
            }
        }
    }

    // MARK: - Read-only peeks (logs, screen snapshot, repo list)

    /// Fetch the tail of a session's scrollback as plain text (a non-attaching
    /// peek). Routes to the session's owning host client.
    func fetchLogs(_ session: Session, lines: Int = 500) async throws -> String {
        guard let client = client(for: session.id) else { throw SessionStoreError.hostUnavailable }
        return try await client.logs(sessionID: session.id, lines: lines)
    }

    /// Fetch a one-shot render of a session's current screen (no attach, no
    /// desktop kick).
    func fetchSnapshot(_ session: Session) async throws -> ScreenSnapshotResponseMsg {
        guard let client = client(for: session.id) else { throw SessionStoreError.hostUnavailable }
        return try await client.screenSnapshot(sessionID: session.id)
    }

    /// The repositories a host offers for session creation, recent-first
    /// (design §C.4 — the app can't pass a local cwd). Failures surface as an
    /// empty list so the create form falls back to the free-text path field.
    func fetchRepos(hostID: String = "local") async -> [RepoEntry] {
        guard let client = clients[hostID] else { return [] }
        let repos = (try? await client.repoList()) ?? []
        return Self.orderedRepos(repos)
    }

    /// Order repos recent-first, then alphabetically by name — a stable order
    /// for the picker regardless of how the daemon returned them.
    static func orderedRepos(_ repos: [RepoEntry]) -> [RepoEntry] {
        repos.sorted { lhs, rhs in
            let lRecent = lhs.recent ?? false
            let rRecent = rhs.recent ?? false
            if lRecent != rRecent { return lRecent }
            return lhs.name.localizedCaseInsensitiveCompare(rhs.name) == .orderedAscending
        }
    }

    // MARK: - Font Size

    func increaseFontSize() {
        let newSize = min(fontSize + 1, Theme.maxFontSize)
        if newSize != fontSize {
            fontSize = newSize
            NotificationCenter.default.post(name: .terminalFontSizeChanged, object: fontSize)
        }
    }

    func decreaseFontSize() {
        let newSize = max(fontSize - 1, Theme.minFontSize)
        if newSize != fontSize {
            fontSize = newSize
            NotificationCenter.default.post(name: .terminalFontSizeChanged, object: fontSize)
        }
    }

    func resetFontSize() {
        if fontSize != Theme.defaultFontSize {
            fontSize = Theme.defaultFontSize
            NotificationCenter.default.post(name: .terminalFontSizeChanged, object: fontSize)
        }
    }

    /// Set an absolute font size (used by the Settings pane), clamped to the
    /// supported range. Routes through the same `.terminalFontSizeChanged`
    /// notification the ⌘=/⌘- commands use, so live terminals pick it up.
    func setFontSize(_ size: CGFloat) {
        let clamped = min(max(size, Theme.minFontSize), Theme.maxFontSize)
        if clamped != fontSize {
            fontSize = clamped
            NotificationCenter.default.post(name: .terminalFontSizeChanged, object: fontSize)
        }
    }

    // MARK: - Polling

    func startPolling() {
        timer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: true) { [weak self] _ in
            Task { @MainActor in
                self?.refresh()
            }
        }
    }

    /// Refresh the session list from every connected host, merging the results.
    /// Hosts are queried concurrently, each under a deadline, so one
    /// slow/unreachable remote can't stall the others (or the local daemon).
    /// Overlapping passes are skipped, and a generation counter drops any stale
    /// response.
    func refresh() {
        guard !isRefreshing else { return }
        isRefreshing = true
        refreshGeneration &+= 1
        let gen = refreshGeneration
        let ordered = registry.hosts.compactMap { host -> (id: String, client: GraithProtocolClient)? in
            guard let client = clients[host.id] else { return nil }
            return (host.id, client)
        }
        let timeout = hostRefreshTimeout
        Task {
            defer { isRefreshing = false }
            // Fan out one list() per host; each result is tagged with its host id
            // and bounded by a per-host deadline.
            let results = await withTaskGroup(of: (String, Result<[Session], Error>).self) { group in
                for entry in ordered {
                    group.addTask {
                        (entry.id, await Self.listWithTimeout(entry.client, seconds: timeout))
                    }
                }
                var byHost: [String: Result<[Session], Error>] = [:]
                for await (id, result) in group { byHost[id] = result }
                return byHost
            }

            guard gen == refreshGeneration else { return }

            // Assemble in registry order so the merged list is stable regardless
            // of which host answered first.
            var merged: [Session] = []
            var owners: [String: String] = [:]
            var errors: [String: String] = [:]
            for entry in ordered {
                switch results[entry.id] {
                case .success(let list):
                    for session in list where owners[session.id] == nil {
                        owners[session.id] = entry.id
                        merged.append(session)
                    }
                case .failure(let error):
                    errors[entry.id] = error.localizedDescription
                case nil:
                    break
                }
            }
            self.sessions = merged
            self.hostBySession = owners
            self.hostErrors = errors
            self.error = errors["local"]
            // Drop attach ownership for sessions that no longer exist (or whose
            // owning window has gone) so nothing wedges a session as "open
            // elsewhere". Only reassign when something was actually pruned, so
            // the steady state doesn't fire a redundant objectWillChange each
            // poll.
            let live = Set(merged.map(\.id))
            let pruned = attachOwners.filter { live.contains($0.key) && $0.value.window != nil }
            if pruned.count != attachOwners.count { attachOwners = pruned }
        }
    }

    /// Run `client.list()` under a deadline. Returns the sessions on success,
    /// or a failure (the daemon error, or a timeout) so a host that connects but
    /// never replies is isolated to its own entry instead of wedging refresh.
    private static func listWithTimeout(_ client: GraithProtocolClient, seconds: Double) async -> Result<[Session], Error> {
        await withTaskGroup(of: Result<[Session], Error>?.self) { group in
            group.addTask {
                do { return .success(try await client.list()) }
                catch { return .failure(error) }
            }
            group.addTask {
                try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
                return nil // timeout sentinel
            }
            defer { group.cancelAll() }
            // First task to finish wins: a real Result from list(), or the nil
            // timeout sentinel.
            for await outcome in group {
                if let outcome { return outcome }
                return .failure(RefreshTimeout())
            }
            return .failure(RefreshTimeout())
        }
    }

    struct RefreshTimeout: LocalizedError {
        var errorDescription: String? { "timed out" }
    }

    /// Run a mutating action against the session's owning host client, then
    /// refresh. Errors surface on `error`.
    private func runAction(_ session: Session, _ action: @escaping (GraithProtocolClient) async throws -> Void) {
        guard let client = client(for: session.id) else {
            self.error = SessionStoreError.hostUnavailable.localizedDescription
            return
        }
        Task {
            do {
                try await action(client)
            } catch {
                self.error = error.localizedDescription
            }
            refresh()
        }
    }
}
