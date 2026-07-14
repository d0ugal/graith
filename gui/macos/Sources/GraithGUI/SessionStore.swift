import Foundation
import GraithProtocol
import GraithRemoteKit
import GraithSessionKit

/// Disambiguate our `Host` model from `Foundation.Host` (macOS ships both once
/// GraithRemoteKit is imported). A module-scope typealias shadows the Foundation
/// type across the whole GraithGUI module.
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

/// The macOS session store. Since #1131 this is a thin subclass of the shared
/// `FleetModel` — the multi-host client management, session list, refresh,
/// grouping, actions, and single-attach coordination all live in the shared
/// session/feature layer both apps bind to. Only macOS-specific pieces remain
/// here: terminal presentation state (font size, renderer), the raw-client
/// accessors the AppKit terminal view / read-only peeks attach through, and the
/// `hostClients` view the macOS `ApprovalMonitor` presenter subscribes over.
@MainActor
final class SessionStore: FleetModel {
    enum RendererType: String, CaseIterable {
        case ghosttyCoreText = "Ghostty (Core Text)"
        case ghosttyMetal = "Ghostty (Metal)"
    }

    @Published var renderer: RendererType = .ghosttyCoreText
    @Published var fontSize: CGFloat = Theme.defaultFontSize

    enum SessionStoreError: LocalizedError {
        case hostUnavailable
        var errorDescription: String? {
            switch self {
            case .hostUnavailable: return "That host isn't connected."
            }
        }
    }

    /// Production initializer: local + paired remote hosts, 2s polling (desktop).
    /// `subscribeApprovals: false` — the macOS `ApprovalMonitor` owns the approval
    /// subscription (over `hostClients`), so the shared per-host connections skip
    /// it to avoid a redundant second event subscription per host.
    init(registry: HostRegistry, identity: DeviceIdentity?, pairing: PairingCoordinator) {
        super.init(
            registry: registry,
            identity: identity,
            reachability: nil,
            factory: RealHostClientFactory(clientID: "graith-macos"),
            pairing: pairing,
            poll: true,
            subscribeApprovals: false
        )
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
        let pairing = PairingCoordinator(
            pairing: RealPairing(clientID: "graith-macos"),
            identity: identity ?? (try! DeviceIdentity(keychain: InMemorySecretStore())),
            registry: registry
        )
        self.init(registry: registry, identity: identity, pairing: pairing)
    }

    // MARK: - Raw client accessors (macOS AppKit chrome)

    /// The raw protocol client owning `sessionID`, used by the AppKit terminal
    /// view to open a rich attach connection. Falls back to the local connection
    /// when the session isn't yet in a list (a lifecycle action still needs a
    /// client to error against).
    func client(for sessionID: String) -> GraithProtocolClient? {
        (connection(ownerOf: sessionID) ?? connections.first)?.protocolClient
    }

    /// The raw protocol client for a host id.
    func client(forHost hostID: String) -> GraithProtocolClient? {
        connections.first { $0.id == hostID }?.protocolClient
    }

    /// The client that *strictly* owns `sessionID` — no local-daemon fallback.
    /// `client(for:)` falls back to local so a lifecycle action still has a
    /// client to error against; the read-only peeks must not, or a removed
    /// remote session id would be sent to the local daemon.
    private func ownerClient(for sessionID: String) -> GraithProtocolClient? {
        connection(ownerOf: sessionID)?.protocolClient
    }

    /// All (host, raw client) pairs, in registry order — the macOS
    /// `ApprovalMonitor` presenter subscribes to approvals across each.
    var hostClients: [(host: Host, client: GraithProtocolClient)] {
        connections.compactMap { conn in conn.protocolClient.map { (host: conn.entry, client: $0) } }
    }

    // MARK: - Read-only peeks (logs, screen snapshot, repo list)

    /// Fetch the tail of a session's scrollback as plain text (a non-attaching
    /// peek). Routes strictly to the session's owning host client.
    func fetchLogs(_ session: Session, lines: Int = 500) async throws -> String {
        guard let client = ownerClient(for: session.id) else { throw SessionStoreError.hostUnavailable }
        return try await client.logs(sessionID: session.id, lines: lines)
    }

    /// Fetch a one-shot render of a session's current screen (no attach, no
    /// desktop kick). Routes strictly to the session's owning host client.
    func fetchSnapshot(_ session: Session) async throws -> ScreenSnapshotResponseMsg {
        guard let client = ownerClient(for: session.id) else { throw SessionStoreError.hostUnavailable }
        return try await client.screenSnapshot(sessionID: session.id)
    }

    /// The repositories a host offers for session creation, recent-first
    /// (design §C.4 — the app can't pass a local cwd). Failures surface as an
    /// empty list so the create form falls back to the free-text path field.
    func fetchRepos(hostID: String = "local") async -> [RepoEntry] {
        guard let client = connections.first(where: { $0.id == hostID })?.protocolClient else { return [] }
        let repos = (try? await client.repoList()) ?? []
        return Self.orderedRepos(repos)
    }

    /// Order repos recent-first, then alphabetically by name, then by path — a
    /// fully-deterministic order regardless of how the daemon returned them.
    /// Path is the final tiebreak because Swift's sort isn't stable and two
    /// repos can share a (case-insensitive) name.
    ///
    /// `nonisolated` — pure (no access to `SessionStore` state), so it can be
    /// called off the main actor (e.g. from a synchronous test context).
    nonisolated static func orderedRepos(_ repos: [RepoEntry]) -> [RepoEntry] {
        repos.sorted { lhs, rhs in
            let lRecent = lhs.recent ?? false
            let rRecent = rhs.recent ?? false
            if lRecent != rRecent { return lRecent }
            switch lhs.name.localizedCaseInsensitiveCompare(rhs.name) {
            case .orderedAscending: return true
            case .orderedDescending: return false
            case .orderedSame: return lhs.path < rhs.path
            }
        }
    }

    // MARK: - Font size (macOS terminal presentation)

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
}
