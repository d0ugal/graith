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
    private var task: Task<Void, Never>?
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

    deinit { task?.cancel() }

    private func start() {
        task = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled {
                do {
                    let stream = try await store.client.subscribeApprovals()
                    for await pending in stream {
                        self.handle(pending)
                    }
                } catch {
                    // Connection dropped or daemon unavailable — clear the badge
                    // and retry after a short backoff.
                    self.clear()
                }
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    private func handle(_ pending: [ApprovalInfo]) {
        self.pending = pending
        updateDockBadge(count: pending.count)

        let currentIDs = Set(pending.map(\.requestID))
        let fresh = pending.filter { !notified.contains($0.requestID) }
        notified = currentIDs
        for approval in fresh {
            postNotification(for: approval)
        }
    }

    private func clear() {
        pending = []
        notified.removeAll()
        updateDockBadge(count: 0)
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
