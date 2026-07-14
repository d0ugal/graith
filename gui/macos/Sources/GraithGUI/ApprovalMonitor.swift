import Foundation
import Combine
import AppKit
import UserNotifications
import GraithProtocol
import GraithRemoteKit

/// Subscribes to the daemon's pending-approval stream and surfaces it through
/// native macOS affordances:
///
/// - a **Dock tile badge** with the pending count (always safe), and
/// - **Notification Center** banners for newly-arrived approvals (only when the
///   binary is a real .app bundle — `UNUserNotificationCenter.current()` traps
///   when there's no bundle identifier, which is the case under `swift run`).
///
/// The published `pending` list also backs any in-app approvals UI.
@MainActor
final class ApprovalMonitor: ObservableObject {
    @Published private(set) var pending: [ApprovalInfo] = []
    /// The last respond failure, surfaced in the approvals panel. Cleared when
    /// the human dismisses it or a subsequent respond succeeds.
    @Published var lastError: String?

    private let store: SessionStore
    /// One subscription task per host, keyed by host id.
    private var tasks: [String: Task<Void, Never>] = [:]
    private var monitorTask: Task<Void, Never>?
    /// The latest pending list from each host, merged into `pending` for the
    /// Dock badge, banners, and the in-app approvals panel.
    private var queue = ApprovalQueue()
    /// Request IDs we've already notified about, so re-emitted lists don't
    /// re-fire banners.
    private var notified = Set<String>()
    /// Composite `host:request` keys for approvals we've optimistically removed
    /// and are awaiting the daemon's acknowledgement for. A stream snapshot that
    /// still lists one is suppressed (see ``ApprovalQueue/applySnapshot``) so it
    /// can't reappear + re-fire a banner + be answered twice mid-flight.
    private var inFlight = Set<String>()
    private var notificationsReady = false

    /// Notifications need a bundle identifier; the SPM `swift run` binary has
    /// none, so we degrade to the Dock badge only rather than trapping.
    private var canUseNotifications: Bool { Bundle.main.bundleIdentifier != nil }

    init(store: SessionStore) {
        self.store = store
        requestNotificationAuthorization()
        start()
    }

    deinit {
        monitorTask?.cancel()
        for task in tasks.values { task.cancel() }
    }

    /// Keep one approval subscription alive per connected host, re-syncing the
    /// set of subscriptions as hosts are paired / removed.
    private func start() {
        monitorTask = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled {
                self.syncSubscriptions()
                // Re-check the host set periodically (cheap; the actual approval
                // stream is event-driven inside each per-host task).
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    /// Start a subscription for any host that gained a client and stop those
    /// whose host went away.
    private func syncSubscriptions() {
        let current = Dictionary(uniqueKeysWithValues: store.hostClients.map { ($0.host.id, $0.client) })
        // Drop subscriptions for hosts that disappeared.
        for (hostID, task) in tasks where current[hostID] == nil {
            task.cancel()
            tasks[hostID] = nil
            queue.clear(host: hostID)
        }
        // Add subscriptions for new hosts.
        for (hostID, client) in current where tasks[hostID] == nil {
            tasks[hostID] = Task { [weak self] in
                guard let self else { return }
                while !Task.isCancelled {
                    do {
                        let stream = try await client.subscribeApprovals()
                        for await pending in stream {
                            self.handle(pending, host: hostID)
                        }
                    } catch {
                        // Connection dropped — clear this host's entry and retry.
                        self.handle([], host: hostID)
                    }
                    try? await Task.sleep(nanoseconds: 2_000_000_000)
                }
            }
        }
        recomputePending()
    }

    private func handle(_ pending: [ApprovalInfo], host hostID: String) {
        // Suppress any request we've answered but not yet had acknowledged, so a
        // snapshot mid-flight can't resurrect it.
        queue.applySnapshot(pending, host: hostID, suppressing: inFlight)
        recomputePending()
    }

    /// Merge every host's pending approvals into one list + fire banners for
    /// newly-arrived requests. Notification identity is keyed by
    /// `hostID:requestID` — request IDs are daemon-local, so two hosts can mint
    /// the same one, and a bare-requestID key would let one host's approval
    /// suppress the other's banner (and collide as a SwiftUI id).
    private func recomputePending() {
        let order = store.hostClients.map { $0.host.id }
        // The set of composite keys currently pending — derived from the same
        // helper the unit tests exercise, so the shipped keying can't drift from
        // what's tested.
        let currentKeys = Set(queue.keys(order: order))
        // The subset that's newly arrived (never notified) — needs the host +
        // approval pair to build a banner, so it stays an explicit loop.
        var fresh: [(hostID: String, approval: ApprovalInfo)] = []
        for entry in store.hostClients {
            let hostID = entry.host.id
            for approval in queue.byHost[hostID] ?? [] where !notified.contains("\(hostID):\(approval.requestID)") {
                fresh.append((hostID, approval))
            }
        }
        let merged = queue.merged(order: order)
        self.pending = merged
        updateDockBadge(count: merged.count)
        notified = currentKeys
        for item in fresh {
            postNotification(for: item.approval, hostID: item.hostID)
        }
    }

    // MARK: - Responding (design §C.6)

    /// Pending approvals grouped by host, in registry order, for the approvals
    /// panel. Hosts with nothing pending are dropped so the panel shows only the
    /// daemons that actually need a decision.
    var grouped: [(host: Host, approvals: [ApprovalInfo])] {
        store.hostClients.compactMap { entry in
            let items = queue.byHost[entry.host.id] ?? []
            return items.isEmpty ? nil : (entry.host, items)
        }
    }

    /// Answer a pending approval on its owning host. The row is removed
    /// optimistically so the UI updates the instant the human decides, and the
    /// request is marked in-flight so a mid-flight stream snapshot can't
    /// resurrect it. The daemon only re-broadcasts on *success* (a rejected
    /// `approval_respond` just returns an error), so on failure we roll the row
    /// back ourselves and surface the error rather than leaving it hidden.
    func respond(_ approval: ApprovalInfo, host hostID: String, decision: ApprovalDecision, reason: String? = nil) {
        guard let client = store.client(forHost: hostID) else {
            lastError = SessionStore.SessionStoreError.hostUnavailable.localizedDescription
            return
        }
        let key = "\(hostID):\(approval.requestID)"
        lastError = nil
        inFlight.insert(key)
        queue.remove(requestID: approval.requestID, host: hostID)
        recomputePending()
        Task {
            do {
                try await client.respondApproval(
                    requestID: approval.requestID,
                    decision: decision.rawValue,
                    reason: reason
                )
                // Accepted — the daemon's follow-up broadcast is authoritative;
                // stop suppressing so it reconciles.
                inFlight.remove(key)
            } catch {
                // Rejected — restore the row so the human can retry, and say why.
                inFlight.remove(key)
                queue.add(approval, host: hostID)
                recomputePending()
                lastError = "\(approval.toolName): \(error.localizedDescription)"
            }
        }
    }

    // MARK: - Dock badge

    private func updateDockBadge(count: Int) {
        NSApp.dockTile.badgeLabel = count > 0 ? "\(count)" : nil
    }

    // MARK: - Notification Center

    private func requestNotificationAuthorization() {
        guard canUseNotifications else { return }
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { [weak self] granted, _ in
            Task { @MainActor in self?.notificationsReady = granted }
        }
    }

    private func postNotification(for approval: ApprovalInfo, hostID: String) {
        guard canUseNotifications, notificationsReady else { return }
        let content = UNMutableNotificationContent()
        content.title = "Approval needed — \(approval.sessionName)"
        content.body = "\(approval.agent) wants to run \(approval.toolName)"
        content.sound = .default
        content.userInfo = [
            "hostID": hostID,
            "sessionID": approval.sessionID,
            "requestID": approval.requestID,
        ]
        // Composite identifier so two hosts with the same daemon-local request
        // id don't overwrite each other's banner.
        let request = UNNotificationRequest(
            identifier: "\(hostID):\(approval.requestID)",
            content: content,
            trigger: nil
        )
        UNUserNotificationCenter.current().add(request)
    }
}
