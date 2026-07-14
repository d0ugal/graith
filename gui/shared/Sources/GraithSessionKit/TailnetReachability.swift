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

    @Published public private(set) var state: State = .unknown

    private let monitor = NWPathMonitor()
    private let queue = DispatchQueue(label: "com.graith.mobile.reachability")
    private var hasNetworkPath = false

    public init() {}

    /// Begin observing the network path. Call `probe(host:port:)` after a path
    /// change or before connecting to refine `notOnTailnet` vs `onTailnet`.
    public func start() {
        monitor.pathUpdateHandler = { [weak self] path in
            let usable = path.status == .satisfied
            Task { @MainActor in
                guard let self else { return }
                self.hasNetworkPath = usable
                if !usable {
                    self.state = .offline
                } else if self.state == .offline || self.state == .unknown {
                    // We have a path but haven't confirmed the tailnet yet.
                    self.state = .notOnTailnet
                }
            }
        }
        monitor.start(queue: queue)
    }

    public func stop() {
        monitor.cancel()
    }

    /// Probe a single host:port to confirm the tailnet is routable. Updates
    /// `state` to `.onTailnet` on success, `.notOnTailnet` on failure (when a
    /// network path exists). A short timeout keeps this cheap to call.
    public func probe(host: String, port: UInt16, timeout: TimeInterval = 3) async {
        guard hasNetworkPath else {
            state = .offline
            return
        }
        let reachable = await Self.tcpProbe(host: host, port: port, timeout: timeout)
        state = reachable ? .onTailnet : .notOnTailnet
    }

    /// Fold in ground-truth connectivity observed from a real host connection.
    /// A live control connection to any paired host proves the tailnet is
    /// routable, so it is authoritative over the heuristic probe — this is what
    /// actually drives the banner (the speculative `probe` was never wired up).
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

    /// Attempt a TCP connection; return whether it became `.ready` in time.
    private static func tcpProbe(host: String, port: UInt16, timeout: TimeInterval) async -> Bool {
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
