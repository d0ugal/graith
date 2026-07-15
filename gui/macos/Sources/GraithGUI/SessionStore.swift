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
/// This follows the active profile, XDG overrides, and the top-level `data_dir`
/// in that profile's config. Explicit profile/config/socket settings provide an
/// escape hatch for unusual development setups.
enum GraithLocalSocket {
    static let profileOverrideKey = "localDaemon.profileOverride"
    static let configPathOverrideKey = "localDaemon.configPathOverride"
    static let socketPathOverrideKey = "localDaemon.socketPathOverride"

    enum ResolutionSource: Equatable {
        case override
        case config
        case environment
        case `default`
    }

    struct Resolution: Equatable {
        let profile: String
        let configPath: String
        let socketPath: String
        let source: ResolutionSource
    }

    /// Resolve the local daemon exactly as the CLI does, with explicit settings
    /// overrides as an escape hatch. The daemon historically put a configured
    /// data directory's socket directly at `<data_dir>/graith.sock`; current
    /// builds use `<data_dir>/run/graith.sock`. Prefer the legacy path only when
    /// it actually exists so upgrades naturally move to the current layout.
    static func resolve(
        environment: [String: String] = ProcessInfo.processInfo.environment,
        profileOverride: String? = nil,
        configPathOverride: String? = nil,
        socketPathOverride: String? = nil,
        fileManager: FileManager = .default
    ) -> Resolution {
        let home = environment["HOME"].flatMap { $0.isEmpty ? nil : $0 }
            ?? NSHomeDirectory()
        let profile = nonEmpty(profileOverride) ?? environment["GRAITH_PROFILE"] ?? ""
        let appName = profile.isEmpty ? "graith" : "graith-\(profile)"
        let configRoot = environment["XDG_CONFIG_HOME"].flatMap { $0.isEmpty ? nil : $0 }
            ?? URL(fileURLWithPath: home).appendingPathComponent(".config").path
        let automaticConfigPath = URL(fileURLWithPath: configRoot)
            .appendingPathComponent(appName)
            .appendingPathComponent("config.toml").path
        let configPath = expandPath(nonEmpty(configPathOverride) ?? automaticConfigPath, home: home)

        if let explicit = nonEmpty(socketPathOverride) {
            return Resolution(
                profile: profile,
                configPath: configPath,
                socketPath: expandPath(explicit, home: home),
                source: .override
            )
        }

        // XDG_RUNTIME_DIR is independent of data_dir in the Go resolver and
        // therefore wins whenever it is explicitly present.
        if let runtimeRoot = environment["XDG_RUNTIME_DIR"].flatMap({ $0.isEmpty ? nil : $0 }) {
            let socket = URL(fileURLWithPath: runtimeRoot)
                .appendingPathComponent(appName)
                .appendingPathComponent("graith.sock").path
            return Resolution(profile: profile, configPath: configPath, socketPath: socket, source: .environment)
        }

        if let configured = configuredDataDir(at: configPath, home: home) {
            let legacy = URL(fileURLWithPath: configured).appendingPathComponent("graith.sock").path
            let current = URL(fileURLWithPath: configured)
                .appendingPathComponent("run")
                .appendingPathComponent("graith.sock").path
            return Resolution(
                profile: profile,
                configPath: configPath,
                socketPath: fileManager.fileExists(atPath: legacy) ? legacy : current,
                source: .config
            )
        }

        let dataRoot = environment["XDG_DATA_HOME"].flatMap { $0.isEmpty ? nil : $0 }
            ?? URL(fileURLWithPath: home).appendingPathComponent("Library/Application Support").path
        let socket = URL(fileURLWithPath: dataRoot)
            .appendingPathComponent(appName)
            .appendingPathComponent("run/graith.sock").path
        return Resolution(profile: profile, configPath: configPath, socketPath: socket, source: .default)
    }

    static func defaultPath() -> String {
        let defaults = UserDefaults.standard
        return resolve(
            profileOverride: defaults.string(forKey: profileOverrideKey),
            configPathOverride: defaults.string(forKey: configPathOverrideKey),
            socketPathOverride: defaults.string(forKey: socketPathOverrideKey)
        ).socketPath
    }

    static var profile: String {
        nonEmpty(UserDefaults.standard.string(forKey: profileOverrideKey))
            ?? ProcessInfo.processInfo.environment["GRAITH_PROFILE"] ?? ""
    }

    /// The local daemon host entry for this machine.
    static func localHost() -> Host {
        Host.local(socketPath: defaultPath(), profile: profile)
    }

    private static func configuredDataDir(at configPath: String, home: String) -> String? {
        guard let contents = try? String(contentsOfFile: configPath, encoding: .utf8) else {
            return nil
        }
        for rawLine in contents.split(whereSeparator: \Character.isNewline) {
            let line = rawLine.trimmingCharacters(in: .whitespaces)
            if line.hasPrefix("[") { break } // data_dir is a top-level TOML key.
            guard !line.hasPrefix("#"),
                  let equals = line.firstIndex(of: "=") else { continue }
            let key = line[..<equals].trimmingCharacters(in: .whitespaces)
            guard key == "data_dir" else { continue }
            let value = line[line.index(after: equals)...].trimmingCharacters(in: .whitespaces)
            guard let parsed = parseTOMLString(value), !parsed.isEmpty else { return nil }
            return expandPath(parsed, home: home)
        }
        return nil
    }

    /// Parse the quoted TOML strings accepted for config paths. Path settings
    /// are deliberately constrained by the Go config validator to quoted
    /// strings, so a complete TOML parser would add considerable weight for no
    /// additional valid input here.
    private static func parseTOMLString(_ value: String) -> String? {
        guard let quote = value.first, quote == "\"" || quote == "'" else { return nil }
        var result = ""
        var escaped = false
        for character in value.dropFirst() {
            if quote == "\"", escaped {
                switch character {
                case "\"", "\\": result.append(character)
                case "n": result.append("\n")
                case "r": result.append("\r")
                case "t": result.append("\t")
                default: return nil
                }
                escaped = false
            } else if quote == "\"", character == "\\" {
                escaped = true
            } else if character == quote {
                return result
            } else {
                result.append(character)
            }
        }
        return nil
    }

    private static func expandPath(_ path: String, home: String) -> String {
        if path == "~" { return home }
        if path.hasPrefix("~/") {
            return URL(fileURLWithPath: home)
                .appendingPathComponent(String(path.dropFirst(2))).path
        }
        return path
    }

    private static func nonEmpty(_ value: String?) -> String? {
        let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? nil : trimmed
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
    /// view to open a rich attach connection. Resolves the session's owning
    /// connection strictly — a bare local fallback is only safe when the local
    /// daemon is the *only* one configured; with remotes present, falling back
    /// to local could attach a remote daemon-local session id to the wrong
    /// daemon (ids are unique only per daemon).
    func client(for sessionID: String) -> GraithProtocolClient? {
        if let owner = connection(ownerOf: sessionID) { return owner.protocolClient }
        guard !hasRemoteHosts else { return nil }
        return connections.first?.protocolClient
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
