import Foundation
import Combine
import GraithProtocol

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
}

@MainActor
class SessionStore: ObservableObject {
    @Published var sessions: [Session] = []
    @Published var error: String?
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

    /// The shared, transport-abstract protocol client, connected to the local
    /// daemon over its Unix socket. Replaces the old `gr` subprocess model —
    /// no child process is spawned for list/status/lifecycle or attach.
    let client: GraithProtocolClient
    private var timer: Timer?
    private var refreshGeneration: UInt64 = 0

    init() {
        self.client = GraithProtocolClient(
            transport: .unix(path: GraithLocalSocket.defaultPath()),
            profile: GraithLocalSocket.profile,
            clientID: "graith-macos",
            // The desktop app is the *local human* — it owns the 0700 Unix
            // socket trust boundary and must resolve as `roleLocalHuman`, not
            // `roleSession`. We deliberately do NOT forward `GRAITH_TOKEN`: when
            // the app is launched from inside an agent session that env var is a
            // per-session bearer token, and carrying it would make the daemon
            // treat every window as that agent (denying local-human operations).
            // A tokenless connection over the local socket is the human.
            token: nil,
            signer: nil // local Unix socket: no proof-of-possession
        )
        refresh()
        startPolling()
    }

    // Sessions grouped by repo name for sidebar display
    var sessionsByRepo: [(repo: String, sessions: [Session])] {
        let grouped = Dictionary(grouping: sessions) { $0.repoName }
        return grouped.sorted { $0.key < $1.key }
            .map { (repo: $0.key, sessions: $0.value.sorted { $0.name < $1.name }) }
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
        runAction { try await self.client.stop(sessionID: session.id) }
    }

    func resumeSession(_ session: Session) {
        runAction { try await self.client.resume(sessionID: session.id) }
    }

    func deleteSession(_ session: Session) {
        runAction { try await self.client.delete(sessionID: session.id) }
    }

    func restartSession(_ session: Session) {
        runAction { try await self.client.restart(sessionID: session.id) }
    }

    func createSession(
        name: String,
        agent: String,
        repoPath: String,
        model: String,
        prompt: String,
        completion: @escaping (Result<Session?, Error>) -> Void
    ) {
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
                refresh()
                completion(.success(session))
            } catch {
                completion(.failure(error))
            }
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

    /// Refresh the session list from the daemon over the protocol client.
    /// A generation counter drops stale responses if several are in flight.
    func refresh() {
        refreshGeneration &+= 1
        let gen = refreshGeneration
        Task {
            do {
                let list = try await client.list()
                guard gen == refreshGeneration else { return }
                self.sessions = list
                // Drop attach ownership for sessions that no longer exist (or
                // whose owning window has gone) so nothing wedges a session as
                // "open elsewhere".
                let live = Set(list.map(\.id))
                attachOwners = attachOwners.filter { live.contains($0.key) && $0.value.window != nil }
                self.error = nil
            } catch {
                guard gen == refreshGeneration else { return }
                self.error = error.localizedDescription
            }
        }
    }

    /// Run a mutating client action, then refresh. Errors surface on `error`.
    private func runAction(_ action: @escaping () async throws -> Void) {
        Task {
            do {
                try await action()
            } catch {
                self.error = error.localizedDescription
            }
            refresh()
        }
    }
}
