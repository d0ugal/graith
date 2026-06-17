import Foundation

struct SessionList: Codable {
    let sessions: [Session]
}

struct Session: Codable, Identifiable, Hashable {
    let id: String
    let parentID: String?
    let name: String
    let repoPath: String
    let repoName: String
    let worktreePath: String
    let branch: String
    let baseBranch: String
    let agent: String
    let agentSessionID: String?
    let status: String
    let agentStatus: String?
    let exitCode: Int?
    let createdAt: String
    let lastAttachedAt: String?
    let statusChangedAt: String?
    let dirty: Bool?
    let unpushedCount: Int?
    let sandboxed: Bool?
    let sharedWorktree: Bool?
    let inPlace: Bool?
    let model: String?
    let toolName: String?
    let costUSD: Double?
    let contextPercent: Double?
    let starred: Bool?
    let summaryText: String?
    let summaryFaded: Bool?
    let lastOutputAt: String?

    enum CodingKeys: String, CodingKey {
        case id
        case parentID = "parent_id"
        case name
        case repoPath = "repo_path"
        case repoName = "repo_name"
        case worktreePath = "worktree_path"
        case branch
        case baseBranch = "base_branch"
        case agent
        case agentSessionID = "agent_session_id"
        case status
        case agentStatus = "agent_status"
        case exitCode = "exit_code"
        case createdAt = "created_at"
        case lastAttachedAt = "last_attached_at"
        case statusChangedAt = "status_changed_at"
        case dirty
        case unpushedCount = "unpushed_count"
        case sandboxed
        case sharedWorktree = "shared_worktree"
        case inPlace = "in_place"
        case model
        case toolName = "tool_name"
        case costUSD = "cost_usd"
        case contextPercent = "context_percent"
        case starred
        case summaryText = "summary_text"
        case summaryFaded = "summary_faded"
        case lastOutputAt = "last_output_at"
    }

    var isRunning: Bool { status == "running" }
    var isStopped: Bool { status == "stopped" }
    var isErrored: Bool { status == "errored" }
    var needsApproval: Bool { agentStatus == "approval" }

    var shortBranch: String {
        let parts = branch.split(separator: "/", maxSplits: 2)
        if parts.count == 3 { return String(parts[2]) }
        return branch
    }

    static func == (lhs: Session, rhs: Session) -> Bool { lhs.id == rhs.id }
    func hash(into hasher: inout Hasher) { hasher.combine(id) }
}
