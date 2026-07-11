import Foundation

// Codable mirrors of the graith framed-protocol control messages
// (`internal/protocol/messages.go`), limited to the subset the mobile app
// needs. Field names and `CodingKeys` match the Go `json:"..."` tags exactly so
// these decode/encode against the real daemon without translation.
//
// This is part of the boundary contract (`GraithClientAPI`). The concrete
// transport (`GraithProtocolClient`, macOS track) frames and ships these; the
// UI layers above the boundary only ever see these value types.

// MARK: - Sessions

/// One session as reported by `list` / `status` (`protocol.SessionInfo`).
public struct SessionInfo: Codable, Identifiable, Hashable, Sendable {
    public let id: String
    public let parentID: String?
    public let name: String
    public let repoPath: String
    public let repoName: String
    public let worktreePath: String
    public let branch: String
    public let baseBranch: String
    public let agent: String
    public let agentSessionID: String?
    public let status: String
    public let agentStatus: String?
    public let exitCode: Int?
    public let exitSignal: String?
    public let createdAt: String
    public let lastAttachedAt: String?
    public let statusChangedAt: String?
    public let dirty: Bool?
    public let unpushedCount: Int?
    public let sandboxed: Bool?
    public let mirror: Bool?
    public let inPlace: Bool?
    public let yolo: Bool?
    public let model: String?
    public let toolName: String?
    public let includes: [IncludedRepoInfo]?
    public let configStale: Bool?
    public let starred: Bool?
    public let systemKind: String?
    public let scenarioID: String?
    public let scenarioName: String?
    public let summaryText: String?
    public let summaryFaded: Bool?
    public let lastOutputAt: String?
    public let migratedFrom: String?
    public let pullRequest: PRInfo?
    public let ci: CIInfo?

    public enum CodingKeys: String, CodingKey {
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
        case exitSignal = "exit_signal"
        case createdAt = "created_at"
        case lastAttachedAt = "last_attached_at"
        case statusChangedAt = "status_changed_at"
        case dirty
        case unpushedCount = "unpushed_count"
        case sandboxed
        case mirror = "mirror"
        case inPlace = "in_place"
        case yolo
        case model
        case toolName = "tool_name"
        case includes
        case configStale = "config_stale"
        case starred
        case systemKind = "system_kind"
        case scenarioID = "scenario_id"
        case scenarioName = "scenario_name"
        case summaryText = "summary_text"
        case summaryFaded = "summary_faded"
        case lastOutputAt = "last_output_at"
        case migratedFrom = "migrated_from"
        case pullRequest = "pull_request"
        case ci
    }

    public var isRunning: Bool { status == "running" }
    public var isStopped: Bool { status == "stopped" }
    public var isErrored: Bool { status == "errored" }
    public var needsApproval: Bool { agentStatus == "approval" }
    public var isYolo: Bool { yolo == true }
    public var isScenarioMember: Bool { !(scenarioID ?? "").isEmpty }

    /// The trailing segment of a `user/graith/<name>-<id>` branch.
    public var shortBranch: String {
        let parts = branch.split(separator: "/", maxSplits: 2)
        if parts.count == 3 { return String(parts[2]) }
        return branch
    }

    public init(
        id: String, parentID: String? = nil, name: String, repoPath: String = "",
        repoName: String = "", worktreePath: String = "", branch: String = "",
        baseBranch: String = "", agent: String = "claude", agentSessionID: String? = nil,
        status: String, agentStatus: String? = nil, exitCode: Int? = nil,
        exitSignal: String? = nil, createdAt: String = "", lastAttachedAt: String? = nil,
        statusChangedAt: String? = nil, dirty: Bool? = nil, unpushedCount: Int? = nil,
        sandboxed: Bool? = nil, mirror: Bool? = nil, inPlace: Bool? = nil,
        yolo: Bool? = nil, model: String? = nil, toolName: String? = nil,
        includes: [IncludedRepoInfo]? = nil, configStale: Bool? = nil, starred: Bool? = nil,
        systemKind: String? = nil, scenarioID: String? = nil, scenarioName: String? = nil,
        summaryText: String? = nil, summaryFaded: Bool? = nil, lastOutputAt: String? = nil,
        migratedFrom: String? = nil, pullRequest: PRInfo? = nil, ci: CIInfo? = nil
    ) {
        self.id = id
        self.parentID = parentID
        self.name = name
        self.repoPath = repoPath
        self.repoName = repoName
        self.worktreePath = worktreePath
        self.branch = branch
        self.baseBranch = baseBranch
        self.agent = agent
        self.agentSessionID = agentSessionID
        self.status = status
        self.agentStatus = agentStatus
        self.exitCode = exitCode
        self.exitSignal = exitSignal
        self.createdAt = createdAt
        self.lastAttachedAt = lastAttachedAt
        self.statusChangedAt = statusChangedAt
        self.dirty = dirty
        self.unpushedCount = unpushedCount
        self.sandboxed = sandboxed
        self.mirror = mirror
        self.inPlace = inPlace
        self.yolo = yolo
        self.model = model
        self.toolName = toolName
        self.includes = includes
        self.configStale = configStale
        self.starred = starred
        self.systemKind = systemKind
        self.scenarioID = scenarioID
        self.scenarioName = scenarioName
        self.summaryText = summaryText
        self.summaryFaded = summaryFaded
        self.lastOutputAt = lastOutputAt
        self.migratedFrom = migratedFrom
        self.pullRequest = pullRequest
        self.ci = ci
    }
}

/// Linked GitHub pull request for a session's branch (`protocol.PRInfo`).
public struct PRInfo: Codable, Hashable, Sendable {
    public let number: Int
    public let state: String // open | draft | merged | closed
    public let url: String?
    public let reviewDecision: String? // approved | changes_requested | review_required
    public let conflicting: Bool?

    public enum CodingKeys: String, CodingKey {
        case number, state, url
        case reviewDecision = "review_decision"
        case conflicting
    }

    public init(number: Int, state: String, url: String? = nil,
                reviewDecision: String? = nil, conflicting: Bool? = nil) {
        self.number = number
        self.state = state
        self.url = url
        self.reviewDecision = reviewDecision
        self.conflicting = conflicting
    }
}

/// Aggregate CI status for a session's PR (`protocol.CIInfo`).
public struct CIInfo: Codable, Hashable, Sendable {
    public let state: String // passing | failing | pending
    public let failingChecks: [String]?

    public enum CodingKeys: String, CodingKey {
        case state
        case failingChecks = "failing_checks"
    }

    public init(state: String, failingChecks: [String]? = nil) {
        self.state = state
        self.failingChecks = failingChecks
    }
}

/// A repo included in a session's worktree (`protocol.IncludedRepoInfo`).
public struct IncludedRepoInfo: Codable, Hashable, Sendable {
    public let repoName: String
    public let worktreePath: String
    public let branch: String
    public let baseBranch: String
    public let dirty: Bool?
    public let unpushed: Int?

    public enum CodingKeys: String, CodingKey {
        case repoName = "repo_name"
        case worktreePath = "worktree_path"
        case branch
        case baseBranch = "base_branch"
        case dirty
        case unpushed
    }

    public init(repoName: String, worktreePath: String, branch: String,
                baseBranch: String, dirty: Bool? = nil, unpushed: Int? = nil) {
        self.repoName = repoName
        self.worktreePath = worktreePath
        self.branch = branch
        self.baseBranch = baseBranch
        self.dirty = dirty
        self.unpushed = unpushed
    }
}

/// Aggregate fleet counts (`protocol.FleetSummary`).
public struct FleetSummary: Codable, Hashable, Sendable {
    public let total: Int
    public let active: Int
    public let approval: Int
    public let ready: Int
    public let errored: Int
    public let stopped: Int

    public init(total: Int = 0, active: Int = 0, approval: Int = 0,
                ready: Int = 0, errored: Int = 0, stopped: Int = 0) {
        self.total = total
        self.active = active
        self.approval = approval
        self.ready = ready
        self.errored = errored
        self.stopped = stopped
    }
}

public struct StatusResponse: Codable, Sendable {
    public let session: SessionInfo
    public let unreadCount: Int
    public let fleet: FleetSummary

    public enum CodingKeys: String, CodingKey {
        case session
        case unreadCount = "unread_count"
        case fleet
    }

    public init(session: SessionInfo, unreadCount: Int, fleet: FleetSummary) {
        self.session = session
        self.unreadCount = unreadCount
        self.fleet = fleet
    }
}

// MARK: - Create (remote, repo-picker driven)

/// Parameters for `create` (`protocol.CreateMsg`). Remote create has no local
/// cwd, so `repoPath` comes from the `repo_list` picker (design §C.4).
public struct CreateRequest: Codable, Sendable {
    public var name: String
    public var agent: String
    public var repoPath: String
    public var base: String?
    public var prompt: String?
    public var model: String?
    public var parentID: String?
    public var agentHooks: Bool

    public enum CodingKeys: String, CodingKey {
        case name
        case agent
        case repoPath = "repo_path"
        case base
        case prompt
        case model
        case parentID = "parent_id"
        case agentHooks = "agent_hooks"
    }

    public init(name: String, agent: String, repoPath: String, base: String? = nil,
                prompt: String? = nil, model: String? = nil, parentID: String? = nil,
                agentHooks: Bool = true) {
        self.name = name
        self.agent = agent
        self.repoPath = repoPath
        self.base = base
        self.prompt = prompt
        self.model = model
        self.parentID = parentID
        self.agentHooks = agentHooks
    }
}

/// A repo offered by `repo_list` (`protocol.RepoEntry`).
public struct RepoEntry: Codable, Identifiable, Hashable, Sendable {
    public let path: String
    public let name: String
    public let recent: Bool

    public var id: String { path }

    public enum CodingKeys: String, CodingKey {
        case path, name, recent
    }

    public init(path: String, name: String, recent: Bool = false) {
        self.path = path
        self.name = name
        self.recent = recent
    }

    // The daemon marks `recent` omitempty, so it is absent when false; decode it
    // with a default rather than requiring the key (which would throw).
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        name = try c.decode(String.self, forKey: .name)
        recent = try c.decodeIfPresent(Bool.self, forKey: .recent) ?? false
    }
}

// MARK: - Approvals

/// A pending approval (`protocol.ApprovalInfo`).
public struct ApprovalInfo: Codable, Identifiable, Hashable, Sendable {
    public let requestID: String
    public let sessionID: String
    public let sessionName: String
    public let toolName: String
    public let toolInput: String?
    public let agent: String
    public let repoName: String
    public let requestedAt: String

    public var id: String { requestID }

    public enum CodingKeys: String, CodingKey {
        case requestID = "request_id"
        case sessionID = "session_id"
        case sessionName = "session_name"
        case toolName = "tool_name"
        case toolInput = "tool_input"
        case agent
        case repoName = "repo_name"
        case requestedAt = "requested_at"
    }

    public init(requestID: String, sessionID: String, sessionName: String,
                toolName: String, toolInput: String? = nil, agent: String = "",
                repoName: String = "", requestedAt: String = "") {
        self.requestID = requestID
        self.sessionID = sessionID
        self.sessionName = sessionName
        self.toolName = toolName
        self.toolInput = toolInput
        self.agent = agent
        self.repoName = repoName
        self.requestedAt = requestedAt
    }
}

public enum ApprovalDecision: String, Codable, Sendable {
    case allow
    case deny
}

// MARK: - Screen peek (non-attaching)

/// A rendered screen snapshot (`protocol.ScreenSnapshotResponseMsg`).
public struct ScreenSnapshot: Codable, Sendable {
    public let sessionID: String
    public let frame: String
    public let cursorX: Int
    public let cursorY: Int
    public let cursorVisible: Bool
    public let cols: Int
    public let rows: Int

    public enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case frame
        case cursorX = "cursor_x"
        case cursorY = "cursor_y"
        case cursorVisible = "cursor_visible"
        case cols
        case rows
    }

    public init(sessionID: String, frame: String, cursorX: Int = 0, cursorY: Int = 0,
                cursorVisible: Bool = false, cols: Int = 0, rows: Int = 0) {
        self.sessionID = sessionID
        self.frame = frame
        self.cursorX = cursorX
        self.cursorY = cursorY
        self.cursorVisible = cursorVisible
        self.cols = cols
        self.rows = rows
    }
}

// MARK: - Pairing + proof-of-possession (design §B.2, §B.5)

/// `protocol.PairRequestMsg`. `devicePubKey` is base64 raw ed25519 public key.
public struct PairRequest: Codable, Sendable {
    public let deviceLabel: String
    public let devicePubKey: String

    public enum CodingKeys: String, CodingKey {
        case deviceLabel = "device_label"
        case devicePubKey = "device_pub_key"
    }

    public init(deviceLabel: String, devicePubKey: String) {
        self.deviceLabel = deviceLabel
        self.devicePubKey = devicePubKey
    }
}

/// `protocol.PairResponseMsg` — returned once on approval.
public struct PairResponse: Codable, Sendable {
    public let deviceID: String
    public let clientToken: String
    public let daemonProfile: String
    public let tlsPinSPKI: String

    public enum CodingKeys: String, CodingKey {
        case deviceID = "device_id"
        case clientToken = "client_token"
        case daemonProfile = "daemon_profile"
        case tlsPinSPKI = "tls_pin_spki"
    }

    public init(deviceID: String, clientToken: String, daemonProfile: String, tlsPinSPKI: String) {
        self.deviceID = deviceID
        self.clientToken = clientToken
        self.daemonProfile = daemonProfile
        self.tlsPinSPKI = tlsPinSPKI
    }
}

/// `protocol.AuthChallengeMsg` — daemon → client, nonce is base64.
public struct AuthChallenge: Codable, Sendable {
    public let nonce: String
    public init(nonce: String) { self.nonce = nonce }
}

/// `protocol.AuthProofMsg` — client → daemon, `signature` is base64 ed25519 sig.
public struct AuthProof: Codable, Sendable {
    public let deviceID: String
    public let signature: String

    public enum CodingKeys: String, CodingKey {
        case deviceID = "device_id"
        case signature
    }

    public init(deviceID: String, signature: String) {
        self.deviceID = deviceID
        self.signature = signature
    }
}
