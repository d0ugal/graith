import Foundation
import Combine
import AppKit
import UserNotifications
import GraithProtocol

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

    private let store: SessionStore
    /// One subscription task per host, keyed by host id.
    private var tasks: [String: Task<Void, Never>] = [:]
    private var monitorTask: Task<Void, Never>?
    /// The latest pending list from each host, keyed by host id, merged into
    /// `pending` for the Dock badge + banners.
    private var pendingByHost: [String: [ApprovalInfo]] = [:]
    /// Request IDs we've already notified about, so re-emitted lists don't
    /// re-fire banners.
    private var notified = Set<String>()
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
            pendingByHost[hostID] = nil
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
        pendingByHost[hostID] = pending
        recomputePending()
    }

    /// Merge every host's pending approvals into one list + fire banners for
    /// newly-arrived requests.
    private func recomputePending() {
        let merged = store.hostClients.flatMap { pendingByHost[$0.host.id] ?? [] }
        self.pending = merged
        updateDockBadge(count: merged.count)

        let currentIDs = Set(merged.map(\.requestID))
        let fresh = merged.filter { !notified.contains($0.requestID) }
        notified = currentIDs
        for approval in fresh {
            postNotification(for: approval)
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

    private func postNotification(for approval: ApprovalInfo) {
        guard canUseNotifications, notificationsReady else { return }
        let content = UNMutableNotificationContent()
        content.title = "Approval needed — \(approval.sessionName)"
        content.body = "\(approval.agent) wants to run \(approval.toolName)"
        content.sound = .default
        content.userInfo = [
            "sessionID": approval.sessionID,
            "requestID": approval.requestID,
        ]
        let request = UNNotificationRequest(
            identifier: approval.requestID,
            content: content,
            trigger: nil
        )
        UNUserNotificationCenter.current().add(request)
    }
}
