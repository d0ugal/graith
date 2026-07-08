import Foundation
import Combine
import GraithClientAPI
import GraithMobileKit

/// Top-level app state: owns the `HostRegistry`, device identity, reachability,
/// the client factory, and one `HostConnection` per paired host. Provides the
/// aggregated, multi-host view the sidebar renders (design §C.4).
@MainActor
public final class AppModel: ObservableObject {
    public let registry: HostRegistry
    public let identity: DeviceIdentity
    public let reachability: TailnetReachability
    public let pairing: PairingCoordinator

    private let factory: HostClientFactory

    /// One connection per host, keyed by host id, in registry order.
    @Published public private(set) var connections: [HostConnection] = []
    /// The currently selected session, namespaced by host (host id + session id).
    @Published public var selection: SessionRef?

    public init(
        registry: HostRegistry,
        identity: DeviceIdentity,
        reachability: TailnetReachability,
        factory: HostClientFactory,
        pairingBackend: GraithPairing
    ) {
        self.registry = registry
        self.identity = identity
        self.reachability = reachability
        self.factory = factory
        self.pairing = PairingCoordinator(pairing: pairingBackend, identity: identity, registry: registry)
        rebuildConnections()
    }

    // MARK: - Connections

    /// (Re)create `HostConnection`s from the registry's paired hosts. Preserves
    /// existing connections for hosts that are unchanged.
    public func rebuildConnections() {
        let existing = Dictionary(uniqueKeysWithValues: connections.map { ($0.id, $0) })
        connections = registry.hosts.compactMap { entry in
            guard entry.isPaired else { return nil }
            if let conn = existing[entry.id], conn.entry == entry { return conn }
            guard let creds = registry.credentials(for: entry, deviceID: identity.deviceID) else { return nil }
            let client = factory.makeClient(transport: entry.transport, credentials: creds, signer: identity)
            return HostConnection(entry: entry, client: client)
        }
    }

    /// Connect all hosts (called on appear / on returning to foreground).
    public func connectAll() async {
        await withTaskGroup(of: Void.self) { group in
            for conn in connections {
                group.addTask { await conn.connect() }
            }
        }
    }

    public func disconnectAll() async {
        for conn in connections { await conn.disconnect() }
    }

    public func connection(for ref: SessionRef) -> HostConnection? {
        connections.first { $0.id == ref.hostID }
    }

    // MARK: - Aggregation

    /// All sessions across all hosts, tagged with their host, for a flat feed.
    public var allSessions: [HostedSession] {
        connections.flatMap { conn in
            conn.sessions.map { HostedSession(host: conn.entry, session: $0) }
        }
    }

    /// Total pending approvals across all hosts (for a badge).
    public var totalPendingApprovals: Int {
        connections.reduce(0) { $0 + $1.approvals.count }
    }

    /// Every pending approval across hosts, tagged with its host.
    public var allApprovals: [HostedApproval] {
        connections.flatMap { conn in
            conn.approvals.map { HostedApproval(host: conn.entry, approval: $0) }
        }
    }

    // MARK: - Host management

    /// Remove a host and its connection.
    public func removeHost(_ entry: HostEntry) async {
        if let conn = connections.first(where: { $0.id == entry.id }) {
            await conn.disconnect()
        }
        registry.remove(hostID: entry.id)
        rebuildConnections()
    }

    /// After a successful pairing, rebuild connections and connect the new host.
    public func didPair() async {
        rebuildConnections()
        await connectAll()
    }
}

/// A session identified across hosts (session IDs are per-daemon, not global).
public struct SessionRef: Hashable, Sendable, Identifiable {
    public let hostID: String
    public let sessionID: String
    public var id: String { "\(hostID)/\(sessionID)" }
    public init(hostID: String, sessionID: String) {
        self.hostID = hostID
        self.sessionID = sessionID
    }
}

/// A session paired with the host it belongs to (for the aggregated sidebar).
public struct HostedSession: Identifiable, Hashable, Sendable {
    public let host: HostEntry
    public let session: SessionInfo
    public var id: String { "\(host.id)/\(session.id)" }
    public var ref: SessionRef { SessionRef(hostID: host.id, sessionID: session.id) }
}

/// An approval paired with the host it belongs to.
public struct HostedApproval: Identifiable, Hashable, Sendable {
    public let host: HostEntry
    public let approval: ApprovalInfo
    public var id: String { "\(host.id)/\(approval.requestID)" }
}
