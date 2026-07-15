import Foundation
import GraithProtocol
import GraithRemoteKit

// The capability boundary for the shared session/feature layer (#1131): the one
// definition of "what a session app can do." Both the macOS and iOS SwiftUI
// apps bind to the protocols below, so a new capability is wired once here and
// appears on both platforms. Originally the iOS-only `GraithClientAPI` boundary
// (topic `apple-track-628`), now retyped onto the canonical `GraithProtocol`
// wire models + `GraithRemoteKit` host types and lifted into `gui/shared`:
//
//   - `GraithProtocolClient` is an `actor` with an async/await public API and
//     owns multiple connections per host (control / attach / event).
//   - Attach exposes `AsyncStream<Data>` for channel 0x01 output plus
//     `send(_:)` / `resize(cols:rows:)`.
//   - Handshake + proof-of-possession happen inside `connect()`; the client
//     signs the challenge nonce through an injected `DeviceKeySigner` that the
//     iOS app owns (Keychain / CryptoKit).
//
// The UI layers depend only on the protocols below, so they can run against
// the in-memory `GraithMobileMock` today and the real actor later with no
// change above the boundary.

// MARK: - Transport + host identity
//
// `GraithTransport` and `HostCredentials` are the canonical shared types
// (`GraithProtocol.GraithTransport`, `GraithRemoteKit.HostCredentials`) — the
// boundary used to redeclare local copies; they are folded away here (#1131).

// MARK: - Device key signer (owned by the app, injected into the client)

/// The device's ed25519 signer for pairing + proof-of-possession.
///
/// Re-homed onto the shared `GraithProtocol.DeviceKeySigner` (design §B.2.4):
/// the boundary protocol was byte-for-byte identical to the shared one, so this
/// typealias unifies them into a single contract. `GraithMobileKit.DeviceIdentity`
/// (Keychain / CryptoKit) conforms to it and is injected straight into
/// `GraithProtocolClient` with no bridge type.
public typealias DeviceKeySigner = GraithProtocol.DeviceKeySigner

// MARK: - Control-message type strings (wire `Envelope.type`)

/// The control envelope `type` strings this app sends/receives. Kept as an enum
/// of raw strings so both tracks reference identical literals.
public enum ControlType {
    public static let handshake = "handshake"
    public static let list = "list"
    public static let status = "status"
    public static let create = "create"
    public static let stop = "stop"
    public static let resume = "resume"
    public static let restart = "restart"
    public static let delete = "delete"
    public static let restore = "restore"
    public static let setStatus = "set_status"
    public static let interrupt = "interrupt"
    public static let rename = "rename"
    public static let star = "star"
    public static let unstar = "unstar"
    public static let fork = "fork"
    public static let migrate = "migrate"
    public static let logs = "logs"
    public static let resize = "resize"
    public static let attach = "attach"
    public static let detach = "detach"
    public static let screenSnapshot = "screen_snapshot"
    public static let screenPreview = "screen_preview"
    public static let repoList = "repo_list"
    public static let scenarioList = "scenario_list"
    public static let scenarioStop = "scenario_stop"
    public static let scenarioResume = "scenario_resume"
    public static let scenarioDelete = "scenario_delete"
    public static let storeList = "store_list"
    public static let storeGet = "store_get"
    public static let approvalList = "approval_list"
    public static let approvalSubscribe = "approval_subscribe"
    public static let approvalRespond = "approval_respond"
    public static let approvalNotification = "approval_notification"
    public static let msgPub = "msg_pub"
    public static let msgConversation = "msg_conversation"
    public static let msgAck = "msg_ack"
    public static let pairRequest = "pair_request"
    public static let pairResponse = "pair_response"
    public static let authChallenge = "auth_challenge"
    public static let authProof = "auth_proof"
    public static let error = "error"
}

// MARK: - Errors

public enum GraithClientError: Error, Sendable, Equatable {
    /// The device is not paired with this host yet — only `pair` is allowed.
    case notPaired
    /// Gate 1/2 or PoP rejected this connection.
    case authenticationFailed(String)
    /// TLS pin mismatch — the daemon presented a different key than pinned.
    case tlsPinMismatch
    /// The tailnet is not reachable (Tailscale tunnel down / host offline).
    case tailnetUnreachable
    /// The daemon replied with an `error` control message.
    case daemon(String)
    /// The connection dropped.
    case disconnected(String)
    /// A reply could not be decoded.
    case decoding(String)
}

// MARK: - The per-host client

/// A transport-abstract client for a single daemon. Satisfied by the macOS
/// agent's `actor GraithProtocolClient`; mocked by `GraithMobileMock`.
///
/// All methods are `async` and safe to call from the main actor. The
/// implementation opens and manages the control / attach / event connections
/// internally (multiple connections per host — the daemon handler is not fully
/// multiplexed).
public protocol GraithHostClient: Actor {
    /// Establish the control connection: handshake → PoP → present token.
    /// Throws `GraithClientError.notPaired` if no credentials are set.
    func connect() async throws
    func disconnect() async

    var isConnected: Bool { get }

    // Read RPCs (roleRemoteHuman / roleRemoteGuest).
    func listSessions() async throws -> [SessionInfo]
    /// The soft-deleted sessions kept within the daemon's retention window, for
    /// the Deleted/restore surface (`gr list --deleted`).
    func listDeletedSessions() async throws -> [SessionInfo]
    func status(sessionID: String) async throws -> StatusResponse
    func repoList() async throws -> [RepoEntry]
    func logs(sessionID: String, lines: Int) async throws -> String
    func screenSnapshot(sessionID: String) async throws -> ScreenSnapshot
    /// Multi-session scenarios running on this daemon (`gr scenario list`).
    func listScenarios() async throws -> [ScenarioRecord]
    /// List document-store keys for the browser (#902). `repo` is a store ID or
    /// path (nil with `shared` for the shared store; both nil lists every store).
    func storeList(repo: String?, shared: Bool, prefix: String?) async throws -> [StoreEntryInfo]
    /// Fetch a single document body from the store (#902).
    func storeGet(repo: String?, shared: Bool, key: String) async throws -> StoreGetResponseMsg

    // Mutations (roleRemoteHuman only).
    func create(_ request: CreateRequest) async throws
    func stop(sessionID: String) async throws
    func resume(sessionID: String) async throws
    func restart(sessionID: String) async throws
    func interrupt(sessionID: String) async throws
    func delete(sessionID: String) async throws
    /// Restore a soft-deleted session (inverse of a soft `delete`).
    func restore(sessionID: String) async throws
    /// Hard-delete a session immediately (`gr purge`), bypassing the soft-delete
    /// retention window — removes the worktree, branch, and state.
    func purge(sessionID: String) async throws
    /// Set (or clear) a session's status summary (`gr status`).
    func setStatus(sessionID: String, text: String, ttlSeconds: Int?, clear: Bool) async throws
    func rename(sessionID: String, newName: String) async throws
    func star(sessionID: String) async throws
    func unstar(sessionID: String) async throws
    /// Fork `sourceSessionID` into a new session named `name`.
    func fork(name: String, sourceSessionID: String) async throws
    /// Migrate `sessionID` to a different `agent` (and optionally `model`).
    func migrate(sessionID: String, agent: String, model: String?) async throws

    // Scenario lifecycle (#903). Human-authorized on the daemon; start/task-done
    // are orchestrator-session-scoped and intentionally absent from the boundary.
    /// Stop every session in the named scenario.
    func stopScenario(name: String) async throws
    /// Resume every stopped/errored session in the named scenario.
    func resumeScenario(name: String) async throws
    /// Delete the scenario and all its sessions/worktrees.
    func deleteScenario(name: String) async throws

    // Messaging (gr msg).
    /// Send a direct message to `sessionID`'s inbox; returns the published message.
    func sendMessage(toSessionID sessionID: String, body: String) async throws -> ConversationMessage
    /// The full direct-message conversation (both directions) for `sessionID`.
    /// `limit > 0` returns only the most recent `limit` messages.
    func conversation(sessionID: String, limit: Int) async throws -> [ConversationMessage]
    /// Mark `sessionID`'s inbox read (acks up to the latest message).
    func ackInbox(sessionID: String) async throws

    // Approvals — event connection.
    /// Subscribe to approval notifications without attaching to any session.
    /// The stream yields the full pending set on every change (design §C.6).
    func approvalStream() -> AsyncStream<[ApprovalInfo]>
    func respondApproval(requestID: String, decision: ApprovalDecision, reason: String?) async throws

    // Full interactive attach — one attach connection (Task 20).
    func attach(sessionID: String) async throws -> any TerminalAttachSession
}

// MARK: - A live attach session

/// One attached terminal: channel 0x01 byte streams both ways plus resize.
/// Backed by a dedicated attach connection; closing it detaches.
public protocol TerminalAttachSession: Actor {
    /// Channel 0x01 output (daemon → client). Finishes on detach / EOF.
    var output: AsyncStream<Data> { get }
    /// Send channel 0x01 bytes (keystrokes / pasted text) to the daemon.
    func send(_ data: Data) async
    /// Resize the remote PTY via a `resize` control message (no local TIOCSWINSZ).
    func resize(cols: UInt16, rows: UInt16) async
    /// Detach and close the attach connection.
    func detach() async
    /// The session ID this attach is bound to.
    var sessionID: String { get }
}

// MARK: - Pairing
//
// The one-time pairing handshake lives canonically in `GraithRemoteKit`
// (`GraithPairing`, `RealPairing`, `PairingCoordinator`); GraithSessionKit does
// not redeclare it. `RealHostClientFactory` above and the app composition roots
// wire `GraithRemoteKit.RealPairing` into `PairingCoordinator`.

// MARK: - Client factory

/// Produces a `GraithHostClient` for a host. The app obtains all its clients
/// through a factory so the concrete transport (`GraithProtocolClient`) stays
/// behind the boundary and the mock can be swapped in for previews/tests.
public protocol HostClientFactory: Sendable {
    /// A client for a **remote** daemon, authenticated with the paired
    /// credentials + a proof-of-possession signer.
    func makeClient(
        transport: GraithTransport,
        credentials: HostCredentials,
        signer: DeviceKeySigner
    ) -> any GraithHostClient

    /// A client for the **local** daemon over its Unix socket. The desktop app
    /// is the local human: it owns the 0700 socket trust boundary and connects
    /// tokenless (no PoP), so no credentials/signer are presented. Never used on
    /// iOS (no local daemon).
    func makeLocalClient(
        transport: GraithTransport,
        profile: String
    ) -> any GraithHostClient
}
