import Foundation
import GraithProtocol

/// The decision the human sends back for a pending approval. Mirrors the iOS
/// boundary's `ApprovalDecision`; the shared client takes the raw string.
enum ApprovalDecision: String {
    case allow
    case deny
}

/// Pure, view-model-free logic for the aggregated approvals queue: it holds each
/// host's pending list keyed by host id and resolves a host-ordered, flat merge
/// plus the composite `host:request` keys used for notification de-dup.
///
/// Extracted from ``ApprovalMonitor`` so the merge/removal/keying behaviour can
/// be unit-tested without a live daemon or SwiftUI. Request ids are daemon-local
/// (two hosts can mint the same one), so everything here is keyed or scoped by
/// host to keep them distinct.
struct ApprovalQueue {
    /// Per-host pending lists, keyed by host id.
    private(set) var byHost: [String: [ApprovalInfo]]

    init(byHost: [String: [ApprovalInfo]] = [:]) {
        self.byHost = byHost
    }

    /// Replace a host's pending list (a fresh push from its approval stream).
    mutating func set(_ pending: [ApprovalInfo], host: String) {
        byHost[host] = pending
    }

    /// Apply a fresh stream snapshot for `host`, dropping any request whose
    /// composite key is in `suppressing`. Used to keep an in-flight (answered
    /// but not-yet-acknowledged) approval from being resurrected by a snapshot
    /// that still lists it — which would re-fire a banner and let the human
    /// answer it twice.
    mutating func applySnapshot(_ pending: [ApprovalInfo], host: String, suppressing inFlight: Set<String>) {
        byHost[host] = pending.filter { !inFlight.contains("\(host):\($0.requestID)") }
    }

    /// Re-insert a single approval for `host` (a rollback after a failed
    /// respond). No-op if it's already present.
    mutating func add(_ approval: ApprovalInfo, host: String) {
        var list = byHost[host] ?? []
        guard !list.contains(where: { $0.requestID == approval.requestID }) else { return }
        list.append(approval)
        byHost[host] = list
    }

    /// Forget a host entirely (its subscription ended / it was removed).
    mutating func clear(host: String) {
        byHost[host] = nil
    }

    /// Optimistically drop one request from a host's list — used the instant the
    /// human taps allow/deny so the row disappears before the stream catches up.
    mutating func remove(requestID: String, host: String) {
        byHost[host]?.removeAll { $0.requestID == requestID }
    }

    /// The flat pending list in `order` (host ids). Hosts not in `order` are
    /// omitted, so a stale entry for a removed host never leaks into the count.
    func merged(order: [String]) -> [ApprovalInfo] {
        order.flatMap { byHost[$0] ?? [] }
    }

    /// Composite `host:request` keys in `order`, for notification identity — a
    /// bare request id would let one host's approval suppress another's banner.
    func keys(order: [String]) -> [String] {
        order.flatMap { host in
            (byHost[host] ?? []).map { "\(host):\($0.requestID)" }
        }
    }
}
