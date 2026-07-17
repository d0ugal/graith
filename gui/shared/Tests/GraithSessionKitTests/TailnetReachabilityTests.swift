import Testing
import Foundation
@testable import GraithSessionKit

// Moved from the iOS GraithMobileKit tests when TailnetReachability folded into
// the shared session layer (#1131). Converted from XCTest to swift-testing to
// match the rest of the shared suite (XCTest isn't available under the CLT-only
// `test-clt` path).

@Suite("TailnetReachability")
@MainActor
struct TailnetReachabilityTests {
    // Regression: the banner used to be stuck on "not connected" because the
    // only thing that set `.onTailnet` was a `probe(host:port:)` that was never
    // called. A real host connection must drive the state to `.onTailnet`.
    @Test func observedReachableGoesOnTailnet() {
        let reach = TailnetReachability()
        #expect(reach.state == .unknown)

        reach.observed(reachable: true)
        #expect(reach.state == .onTailnet)  // a live host connection means we're on the tailnet
    }

    // The only caller aggregates across all hosts, so `observed(false)` means
    // the whole fleet is unreachable — it must downgrade a prior `.onTailnet`
    // to `.notOnTailnet` so the banner reappears when the tailnet drops.
    @Test func observedNotReachableDowngradesOffTailnet() {
        let reach = TailnetReachability()
        reach.observed(reachable: true)
        #expect(reach.state == .onTailnet)
        reach.observed(reachable: false)
        #expect(reach.state == .notOnTailnet)  // aggregated all-hosts-failed downgrades off the tailnet
    }

    // MARK: - probe(host:port:timeout:) seam (#1254)
    //
    // The configurable `reachabilityProbeTimeout` used to have no production
    // caller; it is now wired through `probe`. These drive the probe with an
    // injected TCP prober (the real `NWConnection` needs a live socket) to
    // prove the timeout is threaded through and the state transitions are right.

    /// A reachable host (TCP connect succeeds) while a network path exists
    /// proves we're on the tailnet — and the configured timeout reaches the
    /// prober verbatim.
    @Test func probeReachableGoesOnTailnetWithConfiguredTimeout() async {
        let captured = TimeoutBox()
        let reach = TailnetReachability(tcpProber: { _, _, timeout in
            captured.value = timeout
            return true
        })
        reach.applyNetworkPath(usable: true)   // network path present (path monitor seam)

        await reach.probe(host: "graith-ben.ts.net", port: 4823, timeout: 7.5)
        #expect(reach.state == .onTailnet)
        #expect(captured.value == 7.5)          // tunable timeout threaded through, not the 3s default
    }

    /// A network path but no reachable host is the "network up, tailnet down"
    /// case the banner is for.
    @Test func probeUnreachableGoesNotOnTailnet() async {
        let reach = TailnetReachability(tcpProber: { _, _, _ in false })
        reach.applyNetworkPath(usable: true)

        await reach.probe(host: "graith-ben.ts.net", port: 4823, timeout: 1)
        #expect(reach.state == .notOnTailnet)
    }

    /// With no network path at all the probe short-circuits to `.offline`
    /// without dialing — the path monitor owns network-loss detection.
    @Test func probeWithoutNetworkPathIsOffline() async {
        let dialed = ProbeFlagBox()
        let reach = TailnetReachability(tcpProber: { _, _, _ in dialed.value = true; return true })
        // No applyNetworkPath(usable: true): hasNetworkPath stays false.

        await reach.probe(host: "graith-ben.ts.net", port: 4823, timeout: 1)
        #expect(reach.state == .offline)
        #expect(dialed.value == false)          // never dialed without a path
    }
}

/// Mutable capture boxes for the async `@Sendable` prober closure (a captured
/// `var` can't be mutated from an `@escaping @Sendable` closure directly).
private final class TimeoutBox: @unchecked Sendable { var value: TimeInterval? }
private final class ProbeFlagBox: @unchecked Sendable { var value = false }
