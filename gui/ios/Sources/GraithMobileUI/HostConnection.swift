import Foundation
import Combine
import GraithClientAPI
import GraithMobileKit

/// A `MainActor` view-model wrapping one host's `GraithHostClient`: owns the
/// connection lifecycle, the session list, and the approval subscription for
/// that daemon. The SwiftUI shell observes an array of these (one per host).
@MainActor
public final class HostConnection: ObservableObject, Identifiable {
    public enum ConnectionState: Equatable, Sendable {
        case idle
        case connecting
        case connected
        case failed(String)
    }

    public let entry: HostEntry
    public nonisolated var id: String { entry.id }

    @Published public private(set) var state: ConnectionState = .idle
    @Published public private(set) var sessions: [SessionInfo] = []
    @Published public private(set) var approvals: [ApprovalInfo] = []
    @Published public private(set) var lastError: String?

    private let client: any GraithHostClient
    private var approvalTask: Task<Void, Never>?

    /// The underlying host client — used to build a terminal attach view-model.
    public var hostClient: any GraithHostClient { client }

    public init(entry: HostEntry, client: any GraithHostClient) {
        self.entry = entry
        self.client = client
    }

    // MARK: - Lifecycle

    /// Connect the control + event connections and start reading approvals.
    public func connect() async {
        guard state != .connecting else { return }
        state = .connecting
        lastError = nil
        do {
            try await client.connect()
            state = .connected
            await refresh()
            startApprovalSubscription()
        } catch {
            state = .failed(Self.describe(error))
            lastError = Self.describe(error)
        }
    }

    public func disconnect() async {
        approvalTask?.cancel()
        approvalTask = nil
        await client.disconnect()
        state = .idle
    }

    /// Reload the session list.
    public func refresh() async {
        guard state == .connected else { return }
        do {
            sessions = try await client.listSessions()
        } catch {
            lastError = Self.describe(error)
        }
    }

    // MARK: - Approvals (design §C.6 — subscribe, don't attach)

    private func startApprovalSubscription() {
        approvalTask?.cancel()
        approvalTask = Task { [weak self] in
            guard let self else { return }
            // Mirrors the macOS ApprovalMonitor retry loop: a finished stream
            // means the subscription ended (setup failed or the event
            // connection dropped), never "no approvals". While we're still
            // meant to be connected, re-subscribe with bounded exponential
            // backoff so a flaky link recovers without dying silently or
            // tight-looping. An intentional disconnect() cancels this task, so
            // we never re-subscribe after it.
            let baseBackoff: UInt64 = 500_000_000 // 0.5s
            let maxBackoff: UInt64 = 8_000_000_000 // 8s
            var backoff = baseBackoff
            while !Task.isCancelled {
                // `approvalStream()` is actor-isolated on the client, so resolve
                // it inside the task. This Task inherits @MainActor, so the
                // assignments below are main-actor safe.
                let stream = await self.client.approvalStream()
                var delivered = false
                for await pending in stream {
                    delivered = true
                    backoff = baseBackoff // healthy delivery — reset backoff
                    self.approvals = pending
                }
                // Stream ended. Stop if we're cancelled or no longer connected
                // (an intentional disconnect). Otherwise surface it and retry.
                if Task.isCancelled || self.state != .connected { break }
                self.lastError = delivered
                    ? "Approval stream dropped; reconnecting…"
                    : "Approval subscription failed to start; retrying…"
                try? await Task.sleep(nanoseconds: backoff)
                backoff = Swift.min(backoff * 2, maxBackoff)
            }
        }
    }

    public func respond(_ approval: ApprovalInfo, decision: ApprovalDecision, reason: String? = nil) async {
        do {
            try await client.respondApproval(requestID: approval.requestID, decision: decision, reason: reason)
        } catch {
            lastError = Self.describe(error)
        }
    }

    // MARK: - Mutations / reads used by the UI

    public func repoList() async -> [RepoEntry] {
        (try? await client.repoList()) ?? []
    }

    public func create(_ request: CreateRequest) async -> Bool {
        do {
            try await client.create(request)
            await refresh()
            return true
        } catch {
            lastError = Self.describe(error)
            return false
        }
    }

    public func stop(_ session: SessionInfo) async { await run { try await self.client.stop(sessionID: session.id) } }
    public func resume(_ session: SessionInfo) async { await run { try await self.client.resume(sessionID: session.id) } }
    public func restart(_ session: SessionInfo) async { await run { try await self.client.restart(sessionID: session.id) } }
    public func interrupt(_ session: SessionInfo) async { await run { try await self.client.interrupt(sessionID: session.id) } }

    public func logs(_ session: SessionInfo, lines: Int = 300) async -> String {
        (try? await client.logs(sessionID: session.id, lines: lines)) ?? ""
    }

    public func screenSnapshot(_ session: SessionInfo) async -> ScreenSnapshot? {
        try? await client.screenSnapshot(sessionID: session.id)
    }

    /// Expose the underlying client for the attach path (Task 20).
    public var underlyingClient: any GraithHostClient { client }

    private func run(_ op: @escaping () async throws -> Void) async {
        do { try await op(); await refresh() }
        catch { lastError = Self.describe(error) }
    }

    static func describe(_ error: Error) -> String {
        if let e = error as? GraithClientError { return PairingCoordinator.describe(e) }
        return error.localizedDescription
    }
}
