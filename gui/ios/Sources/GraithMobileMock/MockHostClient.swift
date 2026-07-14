import Foundation
import GraithSessionKit

/// An in-memory `GraithHostClient` for SwiftUI previews and unit tests. Serves
/// canned sessions, repos, approvals, and screen snapshots without any network.
/// Fixture strings use old Scots words per the project convention.
public actor MockHostClient: GraithHostClient {
    private var sessions: [SessionInfo]
    private var repos: [RepoEntry]
    private var pending: [ApprovalInfo]
    private var snapshotFrame: String
    private var connected = false

    private var approvalContinuations: [UUID: AsyncStream<[ApprovalInfo]>.Continuation] = [:]

    /// Optional artificial failure for a given control type (tests error paths).
    public var failOn: Set<String> = []

    public init(
        sessions: [SessionInfo] = MockHostClient.defaultSessions,
        repos: [RepoEntry] = MockHostClient.defaultRepos,
        pending: [ApprovalInfo] = MockHostClient.defaultApprovals,
        snapshotFrame: String = "braw session — screen peek\n$ █"
    ) {
        self.sessions = sessions
        self.repos = repos
        self.pending = pending
        self.snapshotFrame = snapshotFrame
    }

    public var isConnected: Bool { connected }

    public func connect() async throws {
        if failOn.contains(ControlType.handshake) {
            throw GraithClientError.authenticationFailed("mock refused handshake")
        }
        connected = true
    }

    public func disconnect() async {
        connected = false
        for cont in approvalContinuations.values { cont.finish() }
        approvalContinuations.removeAll()
    }

    // MARK: - Reads

    public func listSessions() async throws -> [SessionInfo] {
        try check(ControlType.list)
        return sessions
    }

    public func status(sessionID: String) async throws -> StatusResponse {
        try check(ControlType.status)
        let s = sessions.first { $0.id == sessionID } ?? sessions[0]
        return StatusResponse(session: s, unreadCount: 0, fleet: fleet())
    }

    public func repoList() async throws -> [RepoEntry] {
        try check(ControlType.repoList)
        return repos
    }

    public func logs(sessionID: String, lines: Int) async throws -> String {
        try check(ControlType.logs)
        let all = (1...max(lines, 1)).map { "loch log line \($0) — session \(sessionID)" }
        return all.suffix(lines).joined(separator: "\n")
    }

    public func screenSnapshot(sessionID: String) async throws -> ScreenSnapshot {
        try check(ControlType.screenSnapshot)
        return ScreenSnapshot(sessionID: sessionID, frame: snapshotFrame,
                              cursorX: 2, cursorY: 1, cursorVisible: true, cols: 80, rows: 24)
    }

    // MARK: - Mutations

    public func create(_ request: CreateRequest) async throws {
        try check(ControlType.create)
        let new = SessionInfo(
            id: String(UUID().uuidString.prefix(8)),
            name: request.name,
            repoPath: request.repoPath,
            repoName: (request.repoPath as NSString).lastPathComponent,
            agent: request.agent,
            status: "running",
            agentStatus: "active"
        )
        sessions.append(new)
    }

    public func stop(sessionID: String) async throws {
        try check(ControlType.stop)
        mutate(sessionID) { $0 = $0.with(status: "stopped", agentStatus: "ready") }
    }

    public func resume(sessionID: String) async throws {
        try check(ControlType.resume)
        mutate(sessionID) { $0 = $0.with(status: "running", agentStatus: "active") }
    }

    public func restart(sessionID: String) async throws {
        try check(ControlType.restart)
        mutate(sessionID) { $0 = $0.with(status: "running", agentStatus: "active") }
    }

    public func interrupt(sessionID: String) async throws {
        try check(ControlType.interrupt)
    }

    public func delete(sessionID: String) async throws {
        try check(ControlType.delete)
        sessions.removeAll { $0.id == sessionID }
    }

    public func rename(sessionID: String, newName: String) async throws {
        try check(ControlType.rename)
        mutate(sessionID) { $0 = $0.with(name: newName) }
    }

    public func star(sessionID: String) async throws {
        try check(ControlType.star)
        mutate(sessionID) { $0 = $0.with(starred: true) }
    }

    public func unstar(sessionID: String) async throws {
        try check(ControlType.unstar)
        mutate(sessionID) { $0 = $0.with(starred: false) }
    }

    public func fork(name: String, sourceSessionID: String) async throws {
        try check(ControlType.fork)
        guard let source = sessions.first(where: { $0.id == sourceSessionID }) else {
            throw GraithClientError.daemon("session not found: \(sourceSessionID)")
        }
        let new = SessionInfo(
            id: String(UUID().uuidString.prefix(8)),
            name: name,
            repoPath: source.repoPath,
            repoName: source.repoName,
            agent: source.agent,
            status: "running",
            agentStatus: "active"
        )
        sessions.append(new)
    }

    public func migrate(sessionID: String, agent: String, model: String?) async throws {
        try check(ControlType.migrate)
        mutate(sessionID) { $0 = $0.with(agent: agent) }
    }

    // MARK: - Approvals

    public func approvalStream() -> AsyncStream<[ApprovalInfo]> {
        AsyncStream { continuation in
            let id = UUID()
            approvalContinuations[id] = continuation
            continuation.yield(pending)
            continuation.onTermination = { [weak self] _ in
                Task { await self?.removeApprovalContinuation(id) }
            }
        }
    }

    private func removeApprovalContinuation(_ id: UUID) {
        approvalContinuations[id] = nil
    }

    public func respondApproval(requestID: String, decision: ApprovalDecision, reason: String?) async throws {
        try check(ControlType.approvalRespond)
        pending.removeAll { $0.requestID == requestID }
        broadcastApprovals()
    }

    /// Test helper: inject a new pending approval and notify subscribers.
    public func pushApproval(_ approval: ApprovalInfo) {
        pending.append(approval)
        broadcastApprovals()
    }

    private func broadcastApprovals() {
        for cont in approvalContinuations.values { cont.yield(pending) }
    }

    // MARK: - Attach

    public func attach(sessionID: String) async throws -> any TerminalAttachSession {
        try check(ControlType.attach)
        return MockAttachSession(sessionID: sessionID)
    }

    // MARK: - Helpers

    private func check(_ type: String) throws {
        if failOn.contains(type) { throw GraithClientError.daemon("mock failure: \(type)") }
        if !connected { throw GraithClientError.disconnected("mock not connected") }
    }

    private func mutate(_ id: String, _ body: (inout SessionInfo) -> Void) {
        guard let idx = sessions.firstIndex(where: { $0.id == id }) else { return }
        body(&sessions[idx])
    }

    private func fleet() -> FleetSummary {
        FleetSummary(
            total: sessions.count,
            active: sessions.filter { $0.agentStatus == "active" }.count,
            approval: pending.count,
            ready: sessions.filter { $0.agentStatus == "ready" }.count,
            errored: sessions.filter { $0.isErrored }.count,
            stopped: sessions.filter { $0.isStopped }.count
        )
    }
}

// MARK: - Fixtures (Scots words)

extension MockHostClient {
    public static let defaultSessions: [SessionInfo] = [
        SessionInfo(id: "braw0001", name: "braw", repoPath: "/Users/x/Code/croft", repoName: "croft",
                    branch: "user/graith/braw-braw0001", agent: "claude", status: "running",
                    agentStatus: "active", model: "claude-opus-4-8",
                    summaryText: "implementing the bonnie feature",
                    pullRequest: PRInfo(number: 42, state: "open", reviewDecision: "review_required"),
                    ci: CIInfo(state: "passing")),
        SessionInfo(id: "canny002", name: "canny", repoPath: "/Users/x/Code/croft", repoName: "croft",
                    branch: "user/graith/canny-canny002", agent: "codex", status: "running",
                    agentStatus: "approval", summaryText: "awaiting tool approval"),
        SessionInfo(id: "bide0003", name: "bide", repoPath: "/Users/x/Code/glen", repoName: "glen",
                    branch: "user/graith/bide-bide0003", agent: "claude", status: "stopped",
                    agentStatus: "ready", exitCode: 0, summaryText: "task done"),
    ]

    public static let defaultRepos: [RepoEntry] = [
        RepoEntry(path: "/Users/x/Code/croft", name: "croft", recent: true),
        RepoEntry(path: "/Users/x/Code/glen", name: "glen"),
        RepoEntry(path: "/Users/x/Code/strath", name: "strath"),
    ]

    public static let defaultApprovals: [ApprovalInfo] = [
        ApprovalInfo(requestID: "req-canny-1", sessionID: "canny002", sessionName: "canny",
                     toolName: "Bash", toolInput: "rm -rf build/", agent: "codex",
                     repoName: "croft", requestedAt: "2026-07-08T07:00:00Z"),
    ]
}

extension SessionInfo {
    /// Return a copy overriding selected fields (mock convenience). Unspecified
    /// parameters keep the receiver's current value.
    func with(
        status: String? = nil,
        agentStatus: String? = nil,
        name: String? = nil,
        agent: String? = nil,
        starred: Bool? = nil
    ) -> SessionInfo {
        SessionInfo(
            id: id, parentID: parentID, name: name ?? self.name, repoPath: repoPath, repoName: repoName,
            worktreePath: worktreePath, branch: branch, baseBranch: baseBranch, agent: agent ?? self.agent,
            agentSessionID: agentSessionID, status: status ?? self.status,
            agentStatus: agentStatus ?? self.agentStatus,
            exitCode: exitCode, exitSignal: exitSignal, createdAt: createdAt,
            lastAttachedAt: lastAttachedAt, statusChangedAt: statusChangedAt, dirty: dirty,
            unpushedCount: unpushedCount, sandboxed: sandboxed, mirror: mirror,
            inPlace: inPlace, yolo: yolo, model: model, toolName: toolName, includes: includes,
            configStale: configStale, starred: starred ?? self.starred, systemKind: systemKind,
            scenarioID: scenarioID, scenarioName: scenarioName, summaryText: summaryText,
            summaryFaded: summaryFaded, lastOutputAt: lastOutputAt, migratedFrom: migratedFrom,
            pullRequest: pullRequest, ci: ci
        )
    }
}
