import Foundation
import Combine
import Network

/// Whether the device appears to be on the tailnet. v1 relies on the official
/// Tailscale iOS app for the tunnel (design §C.5); this type only *observes*
/// reachability so the UI can show a clear "not connected to tailnet" state —
/// it does not bring the tunnel up.
///
/// Detection is heuristic: `NWPathMonitor` tells us we have a usable network
/// path, and a lightweight TCP probe to a known host confirms the tailnet is
/// actually routable. The app must not *assume* the tunnel is up.
@MainActor
public final class TailnetReachability: ObservableObject {
    public enum State: Equatable, Sendable {
        case unknown
        /// No usable network path at all.
        case offline
        /// Network is up but no graith host responded on the tailnet.
        case notOnTailnet
        /// At least one configured host was reachable.
        case onTailnet
    }

    /// A TCP-reachability probe: returns whether `host:port` accepted a
    /// connection within `timeout`. Injectable so tests can drive `probe`
    /// deterministically without touching the real network.
    public typealias TCPProber = @Sendable (_ host: String, _ port: UInt16, _ timeout: TimeInterval) async -> Bool

    @Published public private(set) var state: State = .unknown

    private let monitor = NWPathMonitor()
    private let queue = DispatchQueue(label: "com.graith.mobile.reachability")
    private var hasNetworkPath = false
    private let tcpProber: TCPProber

    /// - Parameter tcpProber: the TCP reachability probe used by
    ///   `probe(host:port:timeout:)`. Defaults to the live `NWConnection`
    ///   probe; tests inject a stub to exercise the state transitions and the
    ///   tunable timeout wiring without a real socket.
    public init(tcpProber: @escaping TCPProber = TailnetReachability.liveTCPProbe) {
        self.tcpProber = tcpProber
    }

    /// Begin observing the network path. `FleetModel.refreshReachability` calls
    /// `probe(host:port:timeout:)` after each connect attempt to refine
    /// `notOnTailnet` vs `onTailnet` for a fleet that failed to connect.
    public func start() {
        monitor.pathUpdateHandler = { [weak self] path in
            let usable = path.status == .satisfied
            Task { @MainActor in
                self?.applyNetworkPath(usable: usable)
            }
        }
        monitor.start(queue: queue)
    }

    public func stop() {
        monitor.cancel()
    }

    /// Fold a network-path change into `state`. Extracted from the path-update
    /// handler so tests can drive the path→state transition directly (the real
    /// `NWPathMonitor` needs a live network and can't be simulated in a unit
    /// test).
    func applyNetworkPath(usable: Bool) {
        hasNetworkPath = usable
        if !usable {
            state = .offline
        } else if state == .offline || state == .unknown {
            // We have a path but haven't confirmed the tailnet yet.
            state = .notOnTailnet
        }
    }

    /// Probe a single host:port to confirm the tailnet is routable. Updates
    /// `state` to `.onTailnet` on success, `.notOnTailnet` on failure (when a
    /// network path exists) — a host that accepts a TCP connection proves the
    /// tailnet is routable even when the higher-level control handshake failed
    /// (daemon down / restarting), which is *not* the "open Tailscale" case the
    /// banner is for. Without a network path we report `.offline`. `timeout`
    /// comes from the tunable `reachabilityProbeTimeout` preference (#1254);
    /// a short timeout keeps this cheap to call.
    public func probe(host: String, port: UInt16, timeout: TimeInterval = PresentationPreferences.default.reachabilityProbeTimeout) async {
        guard hasNetworkPath else {
            state = .offline
            return
        }
        let reachable = await tcpProber(host, port, timeout)
        state = reachable ? .onTailnet : .notOnTailnet
    }

    /// Fold in ground-truth connectivity observed from a real host connection.
    /// A live control connection to any paired host proves the tailnet is
    /// routable, so it is authoritative over the heuristic probe. This is the
    /// fast path: when any host connects, the banner clears without a probe.
    /// `FleetModel.refreshReachability` only falls back to `probe` when *no*
    /// host connected, to tell "daemon down but on the tailnet" apart from
    /// "genuinely off the tailnet".
    ///
    /// Pass `reachable: true` when at least one host connected. Pass `false` only
    /// when *every* configured host failed while a network path exists — that is
    /// the "network is up but the tailnet isn't" case the banner is for. The
    /// sole caller (`AppModel.refreshReachability`) aggregates across all hosts,
    /// so `false` means the whole fleet is unreachable — safe to downgrade a
    /// previously-`.onTailnet` state to `.notOnTailnet`. We still do NOT clobber
    /// `.offline`, which the path monitor owns (it alone detects network loss).
    public func observed(reachable: Bool) {
        if reachable {
            state = .onTailnet
        } else if state != .offline {
            // Every configured host failed while a network path exists: network
            // is up yet the tailnet isn't reachable. Downgrade even a prior
            // `.onTailnet` (the aggregated `false` means the fleet is down, not
            // one flaky host), but never override a `.offline` verdict from the
            // path monitor, which owns network-loss detection.
            state = .notOnTailnet
        }
    }

    /// The live TCP probe: attempt a connection and return whether it became
    /// `.ready` within `timeout`. This is the default `TCPProber`. `nonisolated`
    /// so it can be used as the `@MainActor` initializer's default argument
    /// (the probe touches only local sockets, no actor state).
    public nonisolated static let liveTCPProbe: TCPProber = { host, port, timeout in
        await tcpProbe(host: host, port: port, timeout: timeout)
    }

    /// Attempt a TCP connection; return whether it became `.ready` in time.
    private nonisolated static func tcpProbe(host: String, port: UInt16, timeout: TimeInterval) async -> Bool {
        guard let nwPort = NWEndpoint.Port(rawValue: port) else { return false }
        let endpoint = NWEndpoint.hostPort(host: NWEndpoint.Host(host), port: nwPort)
        let conn = NWConnection(to: endpoint, using: .tcp)

        return await withCheckedContinuation { (continuation: CheckedContinuation<Bool, Never>) in
            let resumed = ResumeGuard()
            let probeQueue = DispatchQueue(label: "com.graith.mobile.tcpprobe")

            conn.stateUpdateHandler = { st in
                switch st {
                case .ready:
                    if resumed.tryResume() { conn.cancel(); continuation.resume(returning: true) }
                case .failed, .cancelled:
                    if resumed.tryResume() { conn.cancel(); continuation.resume(returning: false) }
                default:
                    break
                }
            }
            conn.start(queue: probeQueue)

            probeQueue.asyncAfter(deadline: .now() + timeout) {
                if resumed.tryResume() { conn.cancel(); continuation.resume(returning: false) }
            }
        }
    }
}

/// Guards a `CheckedContinuation` against double-resume across the connection
/// callback and the timeout.
private final class ResumeGuard: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func tryResume() -> Bool {
        lock.lock(); defer { lock.unlock() }
        if done { return false }
        done = true
        return true
    }
}
