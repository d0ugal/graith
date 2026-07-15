import Foundation
import GraithSessionKit

/// An in-memory `GraithHostClient` for SwiftUI previews and unit tests. Serves
/// canned sessions, repos, approvals, and screen snapshots without any network.
/// Fixture strings use old Scots words per the project convention.
public actor MockHostClient: GraithHostClient {
    private var sessions: [SessionInfo]
    /// Soft-deleted sessions: `delete` moves a session here, `restore` moves it
    /// back, `purge` removes it permanently — models the retention window.
    private var deleted: [SessionInfo] = []
    private var repos: [RepoEntry]
    private var pending: [ApprovalInfo]
    private var scenarios: [ScenarioRecord]
    private var snapshotFrame: String
    /// Canned store documents keyed by "<repo>/<key>" → body (#902 browser).
    private var storeDocs: [String: String]
    private var connected = false

    private var approvalContinuations: [UUID: AsyncStream<[ApprovalInfo]>.Continuation] = [:]

    /// Optional artificial failure for a given control type (tests error paths).
    public var failOn: Set<String> = []

    public init(
        sessions: [SessionInfo] = MockHostClient.defaultSessions,
        repos: [RepoEntry] = MockHostClient.defaultRepos,
        pending: [ApprovalInfo] = MockHostClient.defaultApprovals,
        scenarios: [ScenarioRecord] = MockHostClient.defaultScenarios,
        snapshotFrame: String = "braw session — screen peek\n$ █",
        storeDocs: [String: String] = MockHostClient.defaultStoreDocs
    ) {
        self.sessions = sessions
        self.repos = repos
        self.pending = pending
        self.scenarios = scenarios
        self.snapshotFrame = snapshotFrame
        self.storeDocs = storeDocs
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

    public func listDeletedSessions() async throws -> [SessionInfo] {
        try check(ControlType.list)
        return deleted
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

    public func storeList(repo: String?, shared: Bool, prefix: String?) async throws -> [StoreEntryInfo] {
        try check(ControlType.storeList)
        // The mock keys docs as "<repo>/<key>"; the target repo is "shared" when
        // `shared`, else the given `repo` (nil → list all stores).
        let target = shared ? "shared" : repo
        return storeDocs.keys.compactMap { composite -> StoreEntryInfo? in
            guard let slash = composite.firstIndex(of: "/") else { return nil }
            let docRepo = String(composite[..<slash])
            let key = String(composite[composite.index(after: slash)...])
            if let target, docRepo != target { return nil }
            if let prefix, !prefix.isEmpty, !key.hasPrefix(prefix) { return nil }
            return StoreEntryInfo(key: key, repo: docRepo, updatedAt: "2026-07-15T09:00:00Z")
        }.sorted { $0.id < $1.id }
    }

    public func storeGet(repo: String?, shared: Bool, key: String) async throws -> StoreGetResponseMsg {
        try check(ControlType.storeGet)
        let target = shared ? "shared" : (repo ?? "")
        guard let body = storeDocs["\(target)/\(key)"] else {
            throw GraithClientError.daemon("document not found: \(key)")
        }
        return StoreGetResponseMsg(key: key, repo: target, body: body)
    }

    public func config() async throws -> ConfigResponseMsg {
        try check(ControlType.config)
        return ConfigResponseMsg(
            effectiveTOML: MockHostClient.mockEffectiveTOML,
            diffFromDefaults: MockHostClient.mockConfigDiff,
            configPath: "/Users/x/.config/graith/config.toml",
            configExists: true
        )
    }

    public func diagnostics() async throws -> DiagnosticsMsg {
        try check(ControlType.diagnostics)
        let diags = sessions.map { s in
            SessionDiagnostic(
                id: s.id, name: s.name, status: s.status, agentStatus: s.agentStatus,
                pid: 4242, pidAlive: s.status == "running", hasPTY: s.status == "running",
                worktreePath: s.worktreePath, worktreeExists: true,
                configStale: s.configStale ?? false, hookStale: false,
                scrollbackBytes: 12_000, scrollbackMax: 5_000_000, saturated: false, hasToken: true
            )
        }
        return DiagnosticsMsg(
            daemonPID: 4242, daemonVersion: "dev", daemonUptime: "1h23m",
            fleet: fleet(), sessions: diags, deletedSessionIDs: deleted.map(\.id),
            scrollback: ScrollbackDiagnostic(totalFiles: sessions.count, totalBytes: 36_000, saturatedCount: 0),
            messages: MessagesDiagnostic(totalStreams: 3, totalMessages: 17)
        )
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
        // Soft delete: move into the deleted list.
        if let idx = sessions.firstIndex(where: { $0.id == sessionID }) {
            deleted.append(sessions.remove(at: idx))
        }
    }

    public func restore(sessionID: String) async throws {
        try check(ControlType.restore)
        if let idx = deleted.firstIndex(where: { $0.id == sessionID }) {
            sessions.append(deleted.remove(at: idx))
        }
    }

    public func purge(sessionID: String) async throws {
        try check(ControlType.delete)
        // Hard delete: gone from both lists.
        sessions.removeAll { $0.id == sessionID }
        deleted.removeAll { $0.id == sessionID }
    }

    public func setStatus(sessionID: String, text: String, ttlSeconds: Int?, clear: Bool) async throws {
        try check(ControlType.setStatus)
        // `.some(nil)` clears the summary; `.some(text)` sets it.
        mutate(sessionID) { $0 = $0.with(summaryText: clear ? .some(nil) : .some(text)) }
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

    // MARK: - Scenarios

    public func listScenarios() async throws -> [ScenarioRecord] {
        try check(ControlType.scenarioList)
        return scenarios
    }

    public func stopScenario(name: String) async throws {
        try check(ControlType.scenarioStop)
    }

    public func resumeScenario(name: String) async throws {
        try check(ControlType.scenarioResume)
    }

    public func deleteScenario(name: String) async throws {
        try check(ControlType.scenarioDelete)
        scenarios.removeAll { $0.name == name }
    }

    // MARK: - Messaging (gr msg)

    /// Per-session inbox, keyed by session id. Seeded with a canned conversation
    /// so previews show a populated inbox; a send appends to it.
    private var inbox: [String: [ConversationMessage]] = MockHostClient.defaultInbox

    public func sendMessage(toSessionID sessionID: String, body: String) async throws -> ConversationMessage {
        try check(ControlType.msgPub)
        let existing = inbox[sessionID] ?? []
        let msg = ConversationMessage(
            id: "msg-\(sessionID)-\(existing.count)", seq: Int64(existing.count + 1),
            stream: "inbox:\(sessionID)", senderID: "human", senderName: "human",
            body: body, createdAt: "2026-07-08T09:00:00Z")
        inbox[sessionID] = existing + [msg]
        return msg
    }

    public func conversation(sessionID: String, limit: Int) async throws -> [ConversationMessage] {
        try check(ControlType.msgConversation)
        let all = inbox[sessionID] ?? []
        return limit > 0 ? Array(all.suffix(limit)) : all
    }

    public func ackInbox(sessionID: String) async throws {
        try check(ControlType.msgAck)
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

    /// Canned store documents keyed by "<repo>/<key>" (#902 browser).
    public static let defaultStoreDocs: [String: String] = [
        "croft-abcdef012345/design/api.md": "# API Design\n\nEndpoints for the bonnie service.",
        "croft-abcdef012345/notes/braw.md": "A wee note in the croft store.",
        "shared/prompts/review.md": "Review this code with a canny eye.",
    ]

    /// A canned inbox for the `braw` session so the messaging preview isn't
    /// empty — a sibling message plus an automated PR notice.
    public static let defaultInbox: [String: [ConversationMessage]] = [
        "braw0001": [
            ConversationMessage(id: "msg-braw-0", seq: 1, stream: "inbox:braw0001",
                                senderID: "canny002", senderName: "canny",
                                body: "Landed the ingest endpoint — ready for you to wire the UI.",
                                createdAt: "2026-07-08T08:30:00Z"),
            ConversationMessage(id: "msg-braw-1", seq: 2, stream: "inbox:braw0001",
                                senderID: "graith:system", senderName: "pr-watch",
                                body: "PR #42 checks passed.",
                                createdAt: "2026-07-08T08:45:00Z", system: true),
        ],
    ]

    public static let defaultApprovals: [ApprovalInfo] = [
        ApprovalInfo(requestID: "req-canny-1", sessionID: "canny002", sessionName: "canny",
                     toolName: "Bash", toolInput: "rm -rf build/", agent: "codex",
                     repoName: "croft", requestedAt: "2026-07-08T07:00:00Z"),
    ]

    public static let defaultScenarios: [ScenarioRecord] = [
        ScenarioRecord(
            id: "sc-strath1",
            name: "strath",
            orchestratorID: "orch0001",
            goal: "Wire distributed tracing end to end",
            status: "running",
            sessionIDs: ["braw0001", "canny002", "bide0003"],
            sessions: [
                ScenarioSessionInfo(name: "braw", sessionID: "braw0001", role: "Backend engineer",
                                    task: "Add tracing ingest endpoint", taskDone: false,
                                    repo: "croft", agent: "claude", status: "running"),
                ScenarioSessionInfo(name: "canny", sessionID: "canny002", role: "Frontend developer",
                                    task: "Add trace export UI", taskDone: false,
                                    repo: "croft", agent: "codex", status: "running"),
                ScenarioSessionInfo(name: "bide", sessionID: "bide0003", role: "Reviewer",
                                    task: "Review the pipeline", taskDone: true,
                                    repo: "glen", agent: "claude", status: "stopped"),
            ],
            createdAt: "2026-07-14T09:00:00Z"),
    ]

    /// Canned effective-config TOML for the config viewer preview (#904).
    public static let mockEffectiveTOML = """
    [sandbox]
    enabled = true
    backend = "nono"

    [pr_watch]
    enabled = true

    [delete]
    retention = "24h"
    """

    /// Canned diff-vs-defaults for the config viewer preview (#904).
    public static let mockConfigDiff = """
    --- defaults
    +++ effective
    @@ -1,2 +1,2 @@
     [sandbox]
    -enabled = false
    +enabled = true
    """
}

extension SessionInfo {
    /// Return a copy overriding selected fields (mock convenience). Unspecified
    /// parameters keep the receiver's current value.
    func with(
        status: String? = nil,
        agentStatus: String? = nil,
        name: String? = nil,
        agent: String? = nil,
        starred: Bool? = nil,
        summaryText: String?? = nil
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
            scenarioID: scenarioID, scenarioName: scenarioName,
            summaryText: summaryText ?? self.summaryText,
            summaryFaded: summaryFaded, lastOutputAt: lastOutputAt, migratedFrom: migratedFrom,
            pullRequest: pullRequest, ci: ci
        )
    }
}
