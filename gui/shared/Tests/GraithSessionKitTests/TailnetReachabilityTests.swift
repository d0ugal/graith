import Testing
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
}
