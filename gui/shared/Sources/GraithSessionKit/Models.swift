import Foundation
import GraithProtocol
import GraithRemoteKit

/// macOS ships `Foundation.Host` (NSHost), which makes a bare `Host` ambiguous
/// once GraithRemoteKit is imported. Pin it to our type module-wide, mirroring
/// the macOS `SessionStore` shadow this layer replaced.
public typealias Host = GraithRemoteKit.Host

// Session/feature-layer model vocabulary (#1131). The wire models are the
// canonical `GraithProtocol.*` types — the iOS track used to mirror them in a
// `WireMessages.swift` + translate with a `ModelMapping.swift`; both are folded
// away here. This file supplies only the small app-level types that are *not*
// wire messages, a few aliases that keep call sites readable, and the UI
// convenience accessors both apps share.

// MARK: - Aliases onto the canonical wire types

/// Parameters for `create`. The canonical `CreateMsg` is a superset of the
/// old boundary `CreateRequest` (adds base/parentID/yolo/…), so the alias keeps
/// existing `CreateRequest(...)` call sites compiling.
public typealias CreateRequest = GraithProtocol.CreateMsg

/// A rendered screen snapshot (non-attaching peek).
public typealias ScreenSnapshot = GraithProtocol.ScreenSnapshotResponseMsg

/// The one-time pairing response returned on approval.
public typealias PairResponse = GraithProtocol.PairResponseMsg

// MARK: - App-level value types (no wire counterpart)

/// An approval decision the human makes on a pending tool request.
public enum ApprovalDecision: String, Codable, Sendable {
    case allow
    case deny
}

// `FleetSummary` moved to `GraithProtocol` (Messages.swift) — it is a wire type
// (embedded in `DiagnosticsMsg` and the Go `StatusResponseMsg`), and the
// diagnostics panel (#904) needs it in the module the conformance test imports.
// GraithSessionKit re-exports GraithProtocol, so every consumer here still sees
// it unqualified.

/// The reply to a per-session `status` request: the session plus its unread
/// count and a fleet summary. Synthesized from `list` by `RealHostClient`.
public struct StatusResponse: Sendable {
    public let session: SessionInfo
    public let unreadCount: Int
    public let fleet: FleetSummary

    public init(session: SessionInfo, unreadCount: Int, fleet: FleetSummary) {
        self.session = session
        self.unreadCount = unreadCount
        self.fleet = fleet
    }
}

// MARK: - UI conveniences on the canonical models

public extension SessionInfo {
    var isRunning: Bool { status == "running" }
    var isStopped: Bool { status == "stopped" }
    var isErrored: Bool { status == "errored" }
    var needsApproval: Bool { agentStatus == "approval" }
    var isYolo: Bool { yolo == true }
    var isSandboxed: Bool { sandboxed == true }
    var isConfigStale: Bool { configStale == true }
    var isScenarioMember: Bool { !(scenarioID ?? "").isEmpty }

    /// The trailing segment of a `user/graith/<name>-<id>` branch.
    var shortBranch: String {
        let parts = branch.split(separator: "/", maxSplits: 2)
        if parts.count == 3 { return String(parts[2]) }
        return branch
    }
}

public extension RepoEntry {
    /// The daemon marks `recent` omitempty, so it is absent (nil) when false.
    var isRecent: Bool { recent ?? false }
}

// Surface the friendly message through `localizedDescription` too, so a generic
// `catch { phase = .failed(error.localizedDescription) }` (e.g. in the shared
// PairingCoordinator) still shows the case-specific text rather than an opaque
// "error N".
extension GraithClientError: LocalizedError {
    public var errorDescription: String? { userMessage }
}

public extension GraithClientError {
    /// A friendly, case-specific message for surfacing in the UI (used by
    /// `HostConnection` and the approvals/detail views).
    var userMessage: String {
        switch self {
        case .notPaired:
            return "This device isn't paired with that host yet."
        case let .authenticationFailed(m):
            return "Authentication failed: \(m)"
        case .tlsPinMismatch:
            return "The host's TLS key changed — re-pair to re-establish trust."
        case .tailnetUnreachable:
            return "The tailnet isn't reachable (host offline or Tailscale down)."
        case let .daemon(m):
            return m
        case let .disconnected(m):
            return "Connection dropped: \(m)"
        case let .decoding(m):
            return "Couldn't decode a reply: \(m)"
        }
    }
}
