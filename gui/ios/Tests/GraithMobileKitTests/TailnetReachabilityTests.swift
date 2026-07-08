import XCTest
@testable import GraithMobileKit

@MainActor
final class TailnetReachabilityTests: XCTestCase {
    // Regression: the banner used to be stuck on "not connected" because the
    // only thing that set `.onTailnet` was a `probe(host:port:)` that was never
    // called. A real host connection must drive the state to `.onTailnet`.
    func testObservedReachableGoesOnTailnet() {
        let reach = TailnetReachability()
        XCTAssertEqual(reach.state, .unknown)

        reach.observed(reachable: true)
        XCTAssertEqual(reach.state, .onTailnet, "a live host connection means we're on the tailnet")
    }

    // The only caller aggregates across all hosts, so `observed(false)` means
    // the whole fleet is unreachable — it must downgrade a prior `.onTailnet`
    // to `.notOnTailnet` so the banner reappears when the tailnet drops.
    func testObservedNotReachableDowngradesOffTailnet() {
        let reach = TailnetReachability()
        reach.observed(reachable: true)
        XCTAssertEqual(reach.state, .onTailnet)
        reach.observed(reachable: false)
        XCTAssertEqual(reach.state, .notOnTailnet, "an aggregated all-hosts-failed observation downgrades off the tailnet")
    }
}
