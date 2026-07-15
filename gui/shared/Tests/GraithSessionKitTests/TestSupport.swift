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
    scenarioID: String? = nil,
    dirty: Bool? = nil,
    unpushedCount: Int? = nil,
    mirror: Bool? = nil
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
    if let dirty { fields["\"dirty\""] = dirty ? "true" : "false" }
    if let unpushedCount { fields["\"unpushed_count\""] = "\(unpushedCount)" }
    if let mirror { fields["\"mirror\""] = mirror ? "true" : "false" }
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
    /// Soft-deleted sessions (moved here by `delete`, restored by `restore`,
    /// removed permanently by `purge`) — models the daemon's retention window.
    private(set) var deleted: [SessionInfo] = []
    var pending: [ApprovalInfo]
    var repos: [RepoEntry]
    var scenarios: [ScenarioRecord]
    /// Records the last scenario lifecycle call so tests can assert routing.
    private(set) var lastScenarioOp: (op: String, name: String)?
    /// Canned store entries + document bodies for the browser (#902).
    var storeEntries: [StoreEntryInfo] = []
    var storeBodies: [String: String] = [:]
    var failStore: GraithClientError?
    var failConnect: GraithClientError?
    var failList: GraithClientError?
    var failSetStatus: GraithClientError?
    /// Records the last `migrate` call so tests can assert the model is forwarded.
    private(set) var lastMigrate: (agent: String, model: String?)?
    /// Records the last `create` request so tests can assert the advanced options
    /// (base/yolo/in-place/agent-hooks) are forwarded to the wire message.
    private(set) var lastCreate: CreateRequest?
    /// Records the last `set_status` call so tests can assert the text/clear flag.
    private(set) var lastSetStatus: (text: String, ttlSeconds: Int?, clear: Bool)?
    /// Blocks `listSessions()` until `releaseList()` — used to hold a refresh in
    /// flight while a second, coalesced refresh queues behind it, then assert the
    /// in-flight refresh loops exactly once more. Holds a single continuation:
    /// `HostConnection.refresh()`'s in-flight guard means at most one gated
    /// `listSessions()` exists per connection at a time, so this never overwrites
    /// (and leaks) a live waiter.
    private var listGate: CheckedContinuation<Void, Never>?
    private var gateList = false
    /// Number of `listSessions()` calls, so a test can assert the in-flight
    /// refresh's single coalesced follow-up actually fired (and only once).
    private(set) var listCallCount = 0

    init(sessions: [SessionInfo] = [], pending: [ApprovalInfo] = [],
         repos: [RepoEntry] = [], scenarios: [ScenarioRecord] = [],
         failConnect: GraithClientError? = nil) {
        self.sessions = sessions
        self.pending = pending
        self.repos = repos
        self.scenarios = scenarios
        self.failConnect = failConnect
    }

    var isConnected: Bool { connected }

    func appendSession(_ s: SessionInfo) { sessions.append(s) }
    func setFailList(_ e: GraithClientError?) { failList = e }
    func setFailSetStatus(_ e: GraithClientError?) { failSetStatus = e }
    func setGateList(_ on: Bool) { gateList = on }
    /// Release a gated `listSessions()`. Clears the gate *before* resuming so the
    /// coalesced follow-up call (the in-flight refresh looping once more) runs to
    /// completion instead of re-blocking on a fresh, never-resumed continuation —
    /// the deadlock this test guards against.
    func releaseList() {
        gateList = false
        listGate?.resume()
        listGate = nil
    }

    func connect() async throws {
        if let failConnect { throw failConnect }
        connected = true
    }
    func disconnect() async {
        connected = false
        // Resume any refresh still blocked on the gate so teardown can't strand
        // the continuation (and the swift-testing runner) if a test fails or
        // returns mid-gate.
        gateList = false
        listGate?.resume()
        listGate = nil
    }

    func listSessions() async throws -> [SessionInfo] {
        listCallCount += 1
        if gateList { await withCheckedContinuation { listGate = $0 } }
        if let failList { throw failList }
        return sessions
    }
    func listDeletedSessions() async throws -> [SessionInfo] {
        if let failList { throw failList }
        return deleted
    }
    func status(sessionID: String) async throws -> StatusResponse {
        guard let s = sessions.first(where: { $0.id == sessionID }) else {
            throw GraithClientError.daemon("not found")
        }
        return StatusResponse(session: s, unreadCount: 0, fleet: FleetSummary())
    }
    func repoList() async throws -> [RepoEntry] { repos }
    func storeList(repo: String?, shared: Bool, prefix: String?) async throws -> [StoreEntryInfo] {
        if let failStore { throw failStore }
        return storeEntries
    }
    func storeGet(repo: String?, shared: Bool, key: String) async throws -> StoreGetResponseMsg {
        if let failStore { throw failStore }
        guard let body = storeBodies[key] else { throw GraithClientError.daemon("not found: \(key)") }
        return StoreGetResponseMsg(key: key, repo: repo ?? (shared ? "shared" : ""), body: body)
    }
    func logs(sessionID: String, lines: Int) async throws -> String { "" }
    func screenSnapshot(sessionID: String) async throws -> ScreenSnapshot {
        // swiftlint:disable:next force_try
        try! JSONDecoder().decode(ScreenSnapshot.self, from: Data(
            "{\"session_id\":\"\(sessionID)\",\"frame\":\"\",\"cursor_x\":0,\"cursor_y\":0,\"cursor_visible\":false,\"cols\":80,\"rows\":24}".utf8))
    }

    func create(_ request: CreateRequest) async throws { lastCreate = request }
    func stop(sessionID: String) async throws {}
    func resume(sessionID: String) async throws {}
    func restart(sessionID: String) async throws {}
    func interrupt(sessionID: String) async throws {}
    func delete(sessionID: String) async throws {
        // Soft delete: move out of the live list into the deleted list.
        if let idx = sessions.firstIndex(where: { $0.id == sessionID }) {
            deleted.append(sessions.remove(at: idx))
        }
    }
    func restore(sessionID: String) async throws {
        if let idx = deleted.firstIndex(where: { $0.id == sessionID }) {
            sessions.append(deleted.remove(at: idx))
        }
    }
    func purge(sessionID: String) async throws {
        // Hard delete: gone from both lists.
        sessions.removeAll { $0.id == sessionID }
        deleted.removeAll { $0.id == sessionID }
    }
    func setStatus(sessionID: String, text: String, ttlSeconds: Int?, clear: Bool) async throws {
        if let failSetStatus { throw failSetStatus }
        lastSetStatus = (text, ttlSeconds, clear)
    }
    func rename(sessionID: String, newName: String) async throws {}
    func star(sessionID: String) async throws {}
    func unstar(sessionID: String) async throws {}
    func fork(name: String, sourceSessionID: String) async throws {}
    func migrate(sessionID: String, agent: String, model: String?) async throws {
        lastMigrate = (agent, model)
    }

    func listScenarios() async throws -> [ScenarioRecord] {
        if let failList { throw failList }
        return scenarios
    }
    func stopScenario(name: String) async throws { lastScenarioOp = ("stop", name) }
    func resumeScenario(name: String) async throws { lastScenarioOp = ("resume", name) }
    func deleteScenario(name: String) async throws {
        lastScenarioOp = ("delete", name)
        scenarios.removeAll { $0.name == name }
    }

    // MARK: - Messaging

    /// Per-session inbox contents, keyed by session id — a send appends here and
    /// a conversation fetch returns it, so tests can round-trip messaging.
    private(set) var inbox: [String: [ConversationMessage]] = [:]
    /// Session ids acked via `ackInbox`, for assertions.
    private(set) var acked: [String] = []
    var failSend: GraithClientError?
    var failConversation: GraithClientError?
    var failAck: GraithClientError?
    func setFailSend(_ e: GraithClientError?) { failSend = e }
    func setFailConversation(_ e: GraithClientError?) { failConversation = e }
    func setFailAck(_ e: GraithClientError?) { failAck = e }
    /// Seed a session's conversation (e.g. inbound messages) for fetch tests.
    func seedConversation(_ sessionID: String, _ messages: [ConversationMessage]) {
        inbox[sessionID] = messages
    }

    func sendMessage(toSessionID sessionID: String, body: String) async throws -> ConversationMessage {
        if let failSend { throw failSend }
        let existing = inbox[sessionID] ?? []
        let msg = ConversationMessage(
            id: "msg_\(existing.count)", seq: Int64(existing.count + 1),
            stream: "inbox:\(sessionID)", senderID: "human", senderName: "human",
            body: body, createdAt: "2026-07-14T00:00:00Z")
        inbox[sessionID] = existing + [msg]
        return msg
    }

    func conversation(sessionID: String, limit: Int) async throws -> [ConversationMessage] {
        if let failConversation { throw failConversation }
        let all = inbox[sessionID] ?? []
        return limit > 0 ? Array(all.suffix(limit)) : all
    }

    func ackInbox(sessionID: String) async throws {
        if let failAck { throw failAck }
        acked.append(sessionID)
    }

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

    var failAttach: GraithClientError?
    func setFailAttach(_ e: GraithClientError?) { failAttach = e }

    func attach(sessionID: String) async throws -> any TerminalAttachSession {
        if let failAttach { throw failAttach }
        return MockAttachSession(sessionID: sessionID)
    }
}

/// A controllable `TerminalAttachSession`: tracks sent bytes / resizes and lets
/// a test drive the output stream and its EOF.
actor MockAttachSession: TerminalAttachSession {
    nonisolated let sessionID: String
    nonisolated let output: AsyncStream<Data>
    private let cont: AsyncStream<Data>.Continuation
    private(set) var sent: [Data] = []
    private(set) var lastResize: (cols: UInt16, rows: UInt16)?
    private(set) var detached = false

    init(sessionID: String) {
        self.sessionID = sessionID
        var c: AsyncStream<Data>.Continuation!
        self.output = AsyncStream { c = $0 }
        self.cont = c
    }

    func send(_ data: Data) async { sent.append(data) }
    func resize(cols: UInt16, rows: UInt16) async { lastResize = (cols, rows) }
    func detach() async { detached = true; cont.finish() }
    func emit(_ data: Data) { cont.yield(data) }
    func finish() { cont.finish() }
}

/// A minimal `TerminalCoreDriving` for driving `TerminalAttachViewModel` off the
/// GPU: records fed output + encoded strokes, answers geometry from stored cols/rows.
final class MockTerminalCore: TerminalCoreDriving, @unchecked Sendable {
    private(set) var fed: [Data] = []
    var cols: UInt16 = 80
    var rows: UInt16 = 24
    var isMouseTrackingActive = false
    var isBracketedPasteEnabled = false
    var atBottom = true

    func feedOutput(_ data: Data) { fed.append(data) }
    func encode(_ stroke: TerminalKeyStroke) -> Data? { Data("k".utf8) }
    func resize(cols: UInt16, rows: UInt16, cellWidth: UInt32, cellHeight: UInt32) {
        self.cols = cols; self.rows = rows
    }
    func scrollViewport(byRows delta: Int) {}
    func scrollToBottom() {}
    var isViewportAtBottom: Bool { atBottom }
    func scrollMetrics() -> ScrollMetrics { ScrollMetrics(total: 0, offset: 0, len: Int(rows)) }
    func encodeScrollWheel(ticks: Int, surfaceX: Double, surfaceY: Double,
                           screenWidth: UInt32, screenHeight: UInt32,
                           cellWidth: UInt32, cellHeight: UInt32) -> [Data] { [] }
    func beginSelection(at cell: ViewportCell, surfaceX: Double, surfaceY: Double, timeNs: UInt64) {}
    func dragSelection(to cell: ViewportCell, surfaceX: Double, surfaceY: Double,
                       columns: UInt32, cellWidth: UInt32, screenHeight: UInt32) {}
    func endSelection(at cell: ViewportCell) {}
    func clearSelection() {}
    func selectedText() -> String? { nil }
}

/// Build a `FleetModel` over a remote-only registry with one **paired** host
/// backed by `mock`, so tests can exercise the connected connection paths.
@MainActor
func makeFleetWithRemote(
    sessions: [SessionInfo] = [],
    pending: [ApprovalInfo] = [],
    repos: [RepoEntry] = [],
    scenarios: [ScenarioRecord] = [],
    subscribeApprovals: Bool = true
) -> (fleet: FleetModel, mock: MockHostClient) {
    let secrets = InMemorySecretStore()
    let identity = try! DeviceIdentity(keychain: secrets)
    let registry = HostRegistry(
        keychain: secrets,
        storeURL: FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-fleet-remote-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
    )
    registry.upsert(Host(id: "ben", label: "Ben Nevis", kind: .remote, magicDNSName: "ben.tail", isPaired: false))
    // swiftlint:disable:next force_try
    try! registry.completePairing(hostID: "ben", response: PairResponseMsg(
        deviceID: "dev-ben", clientToken: "tok-ben", daemonProfile: "", tlsPinSPKI: "cGlu"))
    let mock = MockHostClient(sessions: sessions, pending: pending, repos: repos, scenarios: scenarios)
    let factory = MockFactory(clients: ["tok-ben": mock])
    let pairing = PairingCoordinator(pairing: StubPairing(), identity: identity, registry: registry)
    let fleet = FleetModel(
        registry: registry, identity: identity, reachability: nil,
        factory: factory, pairing: pairing, subscribeApprovals: subscribeApprovals)
    return (fleet, mock)
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
