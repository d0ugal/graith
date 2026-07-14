import Foundation
import GraithProtocol
import GraithRemoteKit
@testable import GraithSessionKit

// macOS ships `Foundation.Host`, which would make bare `Host` ambiguous once
// GraithRemoteKit is imported. Pin it to our type module-wide, mirroring the
// GraithRemoteKitTests helper.
typealias Host = GraithRemoteKit.Host

/// `SessionInfo`'s memberwise init is internal to GraithProtocol, so tests build
/// one the way the wire does: decode it from the daemon's JSON shape. Only the
/// fields the session/feature layer exercises are set.
func makeSession(
    id: String,
    name: String,
    status: String = "running",
    agentStatus: String? = nil,
    repoName: String = "croft",
    parentID: String? = nil,
    starred: Bool? = nil,
    yolo: Bool? = nil,
    scenarioID: String? = nil
) -> SessionInfo {
    var fields: [String: String] = [
        "\"id\"": "\"\(id)\"",
        "\"name\"": "\"\(name)\"",
        "\"repo_path\"": "\"/tmp/\(repoName)\"",
        "\"repo_name\"": "\"\(repoName)\"",
        "\"worktree_path\"": "\"/tmp/\(repoName)/wt\"",
        "\"branch\"": "\"user/graith/\(name)-\(id)\"",
        "\"base_branch\"": "\"main\"",
        "\"agent\"": "\"claude\"",
        "\"status\"": "\"\(status)\"",
        "\"created_at\"": "\"2026-07-14T00:00:00Z\"",
    ]
    if let agentStatus { fields["\"agent_status\""] = "\"\(agentStatus)\"" }
    if let parentID { fields["\"parent_id\""] = "\"\(parentID)\"" }
    if let starred { fields["\"starred\""] = starred ? "true" : "false" }
    if let yolo { fields["\"yolo\""] = yolo ? "true" : "false" }
    if let scenarioID { fields["\"scenario_id\""] = "\"\(scenarioID)\"" }
    let body = fields.map { "\($0.key): \($0.value)" }.joined(separator: ", ")
    let json = "{ \(body) }"
    // swiftlint:disable:next force_try
    return try! JSONDecoder().decode(SessionInfo.self, from: Data(json.utf8))
}

/// An in-memory `GraithHostClient` for driving `HostConnection`/`FleetModel`
/// without a daemon. Serves a canned session list and pending-approval set.
actor MockHostClient: GraithHostClient {
    private(set) var connected = false
    var sessions: [SessionInfo]
    var pending: [ApprovalInfo]
    var failConnect: GraithClientError?

    init(sessions: [SessionInfo] = [], pending: [ApprovalInfo] = [], failConnect: GraithClientError? = nil) {
        self.sessions = sessions
        self.pending = pending
        self.failConnect = failConnect
    }

    var isConnected: Bool { connected }

    func connect() async throws {
        if let failConnect { throw failConnect }
        connected = true
    }
    func disconnect() async { connected = false }

    func listSessions() async throws -> [SessionInfo] { sessions }
    func status(sessionID: String) async throws -> StatusResponse {
        guard let s = sessions.first(where: { $0.id == sessionID }) else {
            throw GraithClientError.daemon("not found")
        }
        return StatusResponse(session: s, unreadCount: 0, fleet: FleetSummary())
    }
    func repoList() async throws -> [RepoEntry] { [] }
    func logs(sessionID: String, lines: Int) async throws -> String { "" }
    func screenSnapshot(sessionID: String) async throws -> ScreenSnapshot {
        // swiftlint:disable:next force_try
        try! JSONDecoder().decode(ScreenSnapshot.self, from: Data(
            "{\"session_id\":\"\(sessionID)\",\"frame\":\"\",\"cursor_x\":0,\"cursor_y\":0,\"cursor_visible\":false,\"cols\":80,\"rows\":24}".utf8))
    }

    func create(_ request: CreateRequest) async throws {}
    func stop(sessionID: String) async throws {}
    func resume(sessionID: String) async throws {}
    func restart(sessionID: String) async throws {}
    func interrupt(sessionID: String) async throws {}
    func delete(sessionID: String) async throws { sessions.removeAll { $0.id == sessionID } }
    func rename(sessionID: String, newName: String) async throws {}
    func star(sessionID: String) async throws {}
    func unstar(sessionID: String) async throws {}
    func fork(name: String, sourceSessionID: String) async throws {}
    func migrate(sessionID: String, agent: String, model: String?) async throws {}

    func approvalStream() -> AsyncStream<[ApprovalInfo]> {
        let snapshot = pending
        return AsyncStream { continuation in
            continuation.yield(snapshot)
            continuation.finish()
        }
    }
    func respondApproval(requestID: String, decision: ApprovalDecision, reason: String?) async throws {
        pending.removeAll { $0.requestID == requestID }
    }

    func attach(sessionID: String) async throws -> any TerminalAttachSession {
        throw GraithClientError.daemon("attach not supported in mock")
    }
}

/// A factory that hands out a preconfigured `MockHostClient` per host id.
struct MockFactory: HostClientFactory {
    let clients: [String: MockHostClient]
    func makeClient(transport: GraithTransport, credentials: HostCredentials, signer: DeviceKeySigner) -> any GraithHostClient {
        clients[credentials.clientToken] ?? MockHostClient()
    }
    func makeLocalClient(transport: GraithTransport, profile: String) -> any GraithHostClient {
        clients["local"] ?? MockHostClient()
    }
}

/// A no-op pairing backend so `PairingCoordinator` can be constructed in tests.
struct StubPairing: GraithPairing {
    func requestPairing(transport: GraithTransport, deviceLabel: String, profile: String, signer: DeviceKeySigner) async throws -> PairResponseMsg {
        throw ControlError.daemon("stub")
    }
}

/// Build a `FleetModel` over a remote-only in-memory registry with no hosts, for
/// exercising the pure grouping/tree helpers and single-attach coordination.
@MainActor
func makeEmptyFleet() -> FleetModel {
    let secrets = InMemorySecretStore()
    let identity = try? DeviceIdentity(keychain: secrets)
    let registry = HostRegistry(
        keychain: secrets,
        storeURL: FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-sessionkit-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
    )
    let pairing = PairingCoordinator(pairing: StubPairing(), identity: identity!, registry: registry)
    return FleetModel(
        registry: registry,
        identity: identity,
        reachability: nil,
        factory: MockFactory(clients: [:]),
        pairing: pairing
    )
}
