import Foundation

// Wire message types for the graith control protocol.
//
// These mirror `internal/protocol/messages.go` field-for-field, with explicit
// `CodingKeys` so the JSON keys match the Go `json:"…"` tags exactly (no
// reliance on Swift's snake-case conversion heuristics, which mangle
// acronym-heavy names like `tls_pin_spki`). Only fields the apps actually use
// are modelled; optional wire fields are `Optional` so partial payloads decode.

// MARK: - Handshake

public struct HandshakeMsg: Codable, Sendable {
    public var version: String
    public var clientID: String
    /// `[cols, rows]` — the daemon reads index 0 as cols, index 1 as rows.
    public var terminalSize: [UInt16]
    public var cwd: String
    public var profile: String?

    public init(version: String = protocolVersion, clientID: String,
                terminalSize: [UInt16], cwd: String, profile: String? = nil) {
        self.version = version
        self.clientID = clientID
        self.terminalSize = terminalSize
        self.cwd = cwd
        self.profile = profile
    }

    enum CodingKeys: String, CodingKey {
        case version
        case clientID = "client_id"
        case terminalSize = "terminal_size"
        case cwd
        case profile
    }
}

public struct HandshakeOkMsg: Codable, Sendable {
    public var version: String
    public var daemonVersion: String

    enum CodingKeys: String, CodingKey {
        case version
        case daemonVersion = "daemon_version"
    }
}

public struct HandshakeErrMsg: Codable, Sendable {
    public var reason: String
}

// MARK: - Lifecycle / control (client -> daemon)

public struct CreateMsg: Codable, Sendable {
    public var name: String
    public var parentID: String?
    public var agent: String
    public var repoPath: String
    public var base: String?
    public var prompt: String?
    public var model: String?
    public var noRepo: Bool?
    public var mirror: String?
    public var agentHooks: Bool?
    public var inPlace: Bool?
    public var allowConcurrent: Bool?
    public var skipModelValidation: Bool?
    public var yolo: Bool?

    public init(name: String, agent: String, repoPath: String, base: String? = nil,
                prompt: String? = nil, model: String? = nil, parentID: String? = nil,
                noRepo: Bool? = nil, agentHooks: Bool? = nil, yolo: Bool? = nil) {
        self.name = name
        self.agent = agent
        self.repoPath = repoPath
        self.base = base
        self.prompt = prompt
        self.model = model
        self.parentID = parentID
        self.noRepo = noRepo
        self.agentHooks = agentHooks
        self.yolo = yolo
    }

    enum CodingKeys: String, CodingKey {
        case name
        case parentID = "parent_id"
        case agent
        case repoPath = "repo_path"
        case base
        case prompt
        case model
        case noRepo = "no_repo"
        case mirror = "mirror"
        case agentHooks = "agent_hooks"
        case inPlace = "in_place"
        case allowConcurrent = "allow_concurrent"
        case skipModelValidation = "skip_model_validation"
        case yolo
    }
}

public struct ForkMsg: Codable, Sendable {
    public var name: String
    public var sourceSessionID: String
    public init(name: String, sourceSessionID: String) {
        self.name = name
        self.sourceSessionID = sourceSessionID
    }
    enum CodingKeys: String, CodingKey {
        case name
        case sourceSessionID = "source_session_id"
    }
}

public struct MigrateMsg: Codable, Sendable {
    public var sessionID: String
    public var agent: String
    public var model: String?
    public init(sessionID: String, agent: String, model: String? = nil) {
        self.sessionID = sessionID
        self.agent = agent
        self.model = model
    }
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case agent
        case model
    }
}

public struct AttachMsg: Codable, Sendable {
    public var sessionID: String
    public init(sessionID: String) { self.sessionID = sessionID }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id" }
}

/// Shared shape for `stop` / `delete` / `restart` (all optionally recurse).
public struct SessionScopeMsg: Codable, Sendable {
    public var sessionID: String
    public var children: Bool?
    public var excludeRoot: Bool?
    public init(sessionID: String, children: Bool? = nil, excludeRoot: Bool? = nil) {
        self.sessionID = sessionID
        self.children = children
        self.excludeRoot = excludeRoot
    }
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case children
        case excludeRoot = "exclude_root"
    }
}

public struct RenameMsg: Codable, Sendable {
    public var sessionID: String
    public var newName: String
    public init(sessionID: String, newName: String) {
        self.sessionID = sessionID
        self.newName = newName
    }
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case newName = "new_name"
    }
}

public struct SessionIDMsg: Codable, Sendable {
    public var sessionID: String
    public init(sessionID: String) { self.sessionID = sessionID }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id" }
}

public struct SetStatusMsg: Codable, Sendable {
    public var sessionID: String
    public var text: String
    public var ttlSeconds: Int?
    public var clear: Bool?
    public init(sessionID: String, text: String, ttlSeconds: Int? = nil, clear: Bool? = nil) {
        self.sessionID = sessionID
        self.text = text
        self.ttlSeconds = ttlSeconds
        self.clear = clear
    }
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case text
        case ttlSeconds = "ttl_seconds"
        case clear
    }
}

public struct TypeMsg: Codable, Sendable {
    public var sessionID: String
    public var input: String
    public var noNewline: Bool?
    public init(sessionID: String, input: String, noNewline: Bool? = nil) {
        self.sessionID = sessionID
        self.input = input
        self.noNewline = noNewline
    }
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case input
        case noNewline = "no_newline"
    }
}

public struct ResizeMsg: Codable, Sendable {
    public var cols: UInt16
    public var rows: UInt16
    public init(cols: UInt16, rows: UInt16) {
        self.cols = cols
        self.rows = rows
    }
}

public struct LogsMsg: Codable, Sendable {
    public var sessionID: String
    public var lines: Int
    public var follow: Bool
    public init(sessionID: String, lines: Int, follow: Bool) {
        self.sessionID = sessionID
        self.lines = lines
        self.follow = follow
    }
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case lines
        case follow
    }
}

// MARK: - Session model (daemon -> client)

/// Canonical session model, matching `protocol.SessionInfo`.
///
/// This is the shared wire model used by both apps (reconciled with the
/// current daemon: the gui-poc's `cost_usd`/`context_percent` are NOT on the
/// wire and have been dropped; the PR/CI/scenario/summary/system-kind fields
/// the POC lacked are added).
public struct SessionInfo: Codable, Sendable, Identifiable, Hashable {
    public var id: String
    public var parentID: String?
    public var name: String
    public var repoPath: String
    public var repoName: String
    public var worktreePath: String
    public var branch: String
    public var baseBranch: String
    public var agent: String
    public var agentSessionID: String?
    public var status: String
    public var agentStatus: String?
    public var exitCode: Int?
    public var exitSignal: String?
    public var createdAt: String
    public var lastAttachedAt: String?
    public var statusChangedAt: String?
    public var dirty: Bool?
    public var unpushedCount: Int?
    public var sandboxed: Bool?
    public var mirror: Bool?
    public var inPlace: Bool?
    public var yolo: Bool?
    public var model: String?
    public var toolName: String?
    public var includes: [IncludedRepoInfo]?
    public var configStale: Bool?
    public var starred: Bool?
    public var systemKind: String?
    public var scenarioID: String?
    public var scenarioName: String?
    public var summaryText: String?
    public var summaryFaded: Bool?
    public var lastOutputAt: String?
    public var migratedFrom: String?
    public var pullRequest: PRInfo?
    public var ci: CIInfo?

    public static func == (lhs: SessionInfo, rhs: SessionInfo) -> Bool { lhs.id == rhs.id }
    public func hash(into hasher: inout Hasher) { hasher.combine(id) }

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
}

public struct PRInfo: Codable, Sendable, Hashable {
    public var number: Int
    public var state: String
    public var url: String?
    public var reviewDecision: String?
    public var conflicting: Bool?
    enum CodingKeys: String, CodingKey {
        case number, state, url
        case reviewDecision = "review_decision"
        case conflicting
    }
}

public struct CIInfo: Codable, Sendable, Hashable {
    public var state: String
    public var failingChecks: [String]?
    enum CodingKeys: String, CodingKey {
        case state
        case failingChecks = "failing_checks"
    }
}

public struct IncludedRepoInfo: Codable, Sendable, Hashable {
    public var repoName: String
    public var worktreePath: String
    public var branch: String
    public var baseBranch: String
    public var dirty: Bool?
    public var unpushed: Int?
    enum CodingKeys: String, CodingKey {
        case repoName = "repo_name"
        case worktreePath = "worktree_path"
        case branch
        case baseBranch = "base_branch"
        case dirty
        case unpushed
    }
}

public struct SessionListMsg: Codable, Sendable {
    public var sessions: [SessionInfo]
}

public struct DetachedMsg: Codable, Sendable {
    public var reason: String
}

public struct ErrorMsg: Codable, Sendable {
    public var message: String
}

// MARK: - Screen peek

public struct ScreenPreviewResponseMsg: Codable, Sendable {
    public var sessionID: String
    public var preview: String
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case preview
    }
}

public struct ScreenSnapshotResponseMsg: Codable, Sendable {
    public var sessionID: String
    public var frame: String
    public var cursorX: Int
    public var cursorY: Int
    public var cursorVisible: Bool
    public var cols: Int
    public var rows: Int
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case frame
        case cursorX = "cursor_x"
        case cursorY = "cursor_y"
        case cursorVisible = "cursor_visible"
        case cols
        case rows
    }
}

// MARK: - Approvals

public struct ApprovalInfo: Codable, Sendable, Identifiable {
    public var requestID: String
    public var sessionID: String
    public var sessionName: String
    public var toolName: String
    public var toolInput: String?
    public var agent: String
    public var repoName: String
    public var requestedAt: String

    public var id: String { requestID }

    enum CodingKeys: String, CodingKey {
        case requestID = "request_id"
        case sessionID = "session_id"
        case sessionName = "session_name"
        case toolName = "tool_name"
        case toolInput = "tool_input"
        case agent
        case repoName = "repo_name"
        case requestedAt = "requested_at"
    }
}

public struct ApprovalNotificationMsg: Codable, Sendable {
    public var pending: [ApprovalInfo]
}

public struct ApprovalRespondMsg: Codable, Sendable {
    public var requestID: String
    public var decision: String
    public var reason: String?
    public init(requestID: String, decision: String, reason: String? = nil) {
        self.requestID = requestID
        self.decision = decision
        self.reason = reason
    }
    enum CodingKeys: String, CodingKey {
        case requestID = "request_id"
        case decision
        case reason
    }
}

/// `approval_subscribe` has no payload; sent to register for approval pushes
/// without attaching. Matches `protocol.ApprovalSubscribeMsg{}`.
public struct ApprovalSubscribeMsg: Codable, Sendable {
    public init() {}
}

// MARK: - Pairing + proof-of-possession (design §B.2 / §B.2.4)

public struct PairRequestMsg: Codable, Sendable {
    public var deviceLabel: String
    /// base64-std of the raw 32-byte ed25519 public key.
    public var devicePubKey: String
    public init(deviceLabel: String, devicePubKey: String) {
        self.deviceLabel = deviceLabel
        self.devicePubKey = devicePubKey
    }
    enum CodingKeys: String, CodingKey {
        case deviceLabel = "device_label"
        case devicePubKey = "device_pub_key"
    }
}

public struct PairResponseMsg: Codable, Sendable {
    public var deviceID: String
    public var clientToken: String
    public var daemonProfile: String
    public var tlsPinSPKI: String
    enum CodingKeys: String, CodingKey {
        case deviceID = "device_id"
        case clientToken = "client_token"
        case daemonProfile = "daemon_profile"
        case tlsPinSPKI = "tls_pin_spki"
    }
}

public struct PairApproveMsg: Codable, Sendable {
    public var requestID: String
    public init(requestID: String) { self.requestID = requestID }
    enum CodingKeys: String, CodingKey { case requestID = "request_id" }
}

public struct PairRevokeMsg: Codable, Sendable {
    public var deviceID: String
    public init(deviceID: String) { self.deviceID = deviceID }
    enum CodingKeys: String, CodingKey { case deviceID = "device_id" }
}

public struct PairListResponseMsg: Codable, Sendable {
    public var pending: [PairPending]
    public var paired: [PairedDeviceInfo]
}

public struct PairPending: Codable, Sendable, Identifiable {
    public var requestID: String
    public var deviceLabel: String
    public var tailnetUser: String
    public var tailnetNode: String
    public var requestedAt: String
    public var id: String { requestID }
    enum CodingKeys: String, CodingKey {
        case requestID = "request_id"
        case deviceLabel = "device_label"
        case tailnetUser = "tailnet_user"
        case tailnetNode = "tailnet_node"
        case requestedAt = "requested_at"
    }
}

public struct PairedDeviceInfo: Codable, Sendable, Identifiable {
    public var deviceID: String
    public var label: String
    public var tailnetUser: String
    public var tailnetNode: String
    public var createdAt: String
    public var lastSeenAt: String
    public var id: String { deviceID }
    enum CodingKeys: String, CodingKey {
        case deviceID = "device_id"
        case label
        case tailnetUser = "tailnet_user"
        case tailnetNode = "tailnet_node"
        case createdAt = "created_at"
        case lastSeenAt = "last_seen_at"
    }
}

/// Daemon -> client, right after `handshake` on remote connections. The client
/// signs `nonce` (verbatim UTF-8 bytes) with its device key.
public struct AuthChallengeMsg: Codable, Sendable {
    public var nonce: String
}

/// Client -> daemon: `signature` is base64-std of the raw 64-byte ed25519
/// signature over the challenge nonce.
public struct AuthProofMsg: Codable, Sendable {
    public var deviceID: String
    public var signature: String
    public init(deviceID: String, signature: String) {
        self.deviceID = deviceID
        self.signature = signature
    }
    enum CodingKeys: String, CodingKey {
        case deviceID = "device_id"
        case signature
    }
}

// MARK: - Remote-create repo picker (design §C.4)

/// `repo_list` has no payload.
public struct RepoListMsg: Codable, Sendable {
    public init() {}
}

public struct RepoListResponseMsg: Codable, Sendable {
    public var repos: [RepoEntry]
}

public struct RepoEntry: Codable, Sendable, Identifiable {
    public var path: String
    public var name: String
    public var recent: Bool?
    public var id: String { path }
}

// MARK: - Empty-payload requests

/// `list` takes no payload. Used as an explicit "no payload" marker.
public struct EmptyMsg: Codable, Sendable {
    public init() {}
}
