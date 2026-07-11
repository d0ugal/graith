import Foundation
import GraithClientAPI
import GraithProtocol

// Bridge the shared wire models (`GraithProtocol.*`) to the `GraithClientAPI`
// boundary value types the UI consumes. Both sets mirror
// `internal/protocol/messages.go` field-for-field, so the mapping is 1:1; it
// lives at the adapter seam so the UI keeps its boundary types (with their
// convenience accessors + memberwise inits the mocks/previews rely on) while the
// transport speaks the shared wire contract. Types are fully qualified because
// this target imports both modules and the names collide by design.

extension GraithClientAPI.SessionInfo {
    public init(_ s: GraithProtocol.SessionInfo) {
        self.init(
            id: s.id, parentID: s.parentID, name: s.name, repoPath: s.repoPath,
            repoName: s.repoName, worktreePath: s.worktreePath, branch: s.branch,
            baseBranch: s.baseBranch, agent: s.agent, agentSessionID: s.agentSessionID,
            status: s.status, agentStatus: s.agentStatus, exitCode: s.exitCode,
            exitSignal: s.exitSignal, createdAt: s.createdAt, lastAttachedAt: s.lastAttachedAt,
            statusChangedAt: s.statusChangedAt, dirty: s.dirty, unpushedCount: s.unpushedCount,
            sandboxed: s.sandboxed, mirror: s.mirror, inPlace: s.inPlace,
            yolo: s.yolo, model: s.model, toolName: s.toolName,
            includes: s.includes.map { $0.map { GraithClientAPI.IncludedRepoInfo($0) } },
            configStale: s.configStale, starred: s.starred, systemKind: s.systemKind,
            scenarioID: s.scenarioID, scenarioName: s.scenarioName, summaryText: s.summaryText,
            summaryFaded: s.summaryFaded, lastOutputAt: s.lastOutputAt, migratedFrom: s.migratedFrom,
            pullRequest: s.pullRequest.map { GraithClientAPI.PRInfo($0) },
            ci: s.ci.map { GraithClientAPI.CIInfo($0) }
        )
    }
}

extension GraithClientAPI.PRInfo {
    public init(_ p: GraithProtocol.PRInfo) {
        self.init(number: p.number, state: p.state, url: p.url,
                  reviewDecision: p.reviewDecision, conflicting: p.conflicting)
    }
}

extension GraithClientAPI.CIInfo {
    public init(_ c: GraithProtocol.CIInfo) {
        self.init(state: c.state, failingChecks: c.failingChecks)
    }
}

extension GraithClientAPI.IncludedRepoInfo {
    public init(_ i: GraithProtocol.IncludedRepoInfo) {
        self.init(repoName: i.repoName, worktreePath: i.worktreePath, branch: i.branch,
                  baseBranch: i.baseBranch, dirty: i.dirty, unpushed: i.unpushed)
    }
}

extension GraithClientAPI.RepoEntry {
    public init(_ r: GraithProtocol.RepoEntry) {
        // Wire `recent` is omitempty, so nil ⇒ false.
        self.init(path: r.path, name: r.name, recent: r.recent ?? false)
    }
}

extension GraithClientAPI.ApprovalInfo {
    public init(_ a: GraithProtocol.ApprovalInfo) {
        self.init(requestID: a.requestID, sessionID: a.sessionID, sessionName: a.sessionName,
                  toolName: a.toolName, toolInput: a.toolInput, agent: a.agent,
                  repoName: a.repoName, requestedAt: a.requestedAt)
    }
}

extension GraithClientAPI.ScreenSnapshot {
    public init(_ s: GraithProtocol.ScreenSnapshotResponseMsg) {
        self.init(sessionID: s.sessionID, frame: s.frame, cursorX: s.cursorX,
                  cursorY: s.cursorY, cursorVisible: s.cursorVisible, cols: s.cols, rows: s.rows)
    }
}

extension GraithClientAPI.PairResponse {
    public init(_ p: GraithProtocol.PairResponseMsg) {
        self.init(deviceID: p.deviceID, clientToken: p.clientToken,
                  daemonProfile: p.daemonProfile, tlsPinSPKI: p.tlsPinSPKI)
    }
}

extension GraithProtocol.CreateMsg {
    public init(_ r: GraithClientAPI.CreateRequest) {
        self.init(name: r.name, agent: r.agent, repoPath: r.repoPath, base: r.base,
                  prompt: r.prompt, model: r.model, parentID: r.parentID, agentHooks: r.agentHooks)
    }
}

extension GraithProtocol.GraithTransport {
    /// Map the boundary transport (Hashable) to the shared transport (Equatable).
    public init(_ t: GraithClientAPI.GraithTransport) {
        switch t {
        case let .unix(path):
            self = .unix(path: path)
        case let .remote(host, port, pin):
            self = .remote(host: host, port: port, tlsPinSPKI: pin)
        }
    }
}
