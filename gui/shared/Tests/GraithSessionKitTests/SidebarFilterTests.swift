import Testing
import Foundation
import GraithProtocol
import GraithRemoteKit
@testable import GraithSessionKit

// Exercises the shared sidebar filter/search logic (#906): the pure
// `SidebarFilter` predicates and the `FleetModel` filter state that drives both
// GUIs' grouping helpers.

@Suite("SidebarFilter — pure predicates (#906)")
struct SidebarFilterPureTests {
    // MARK: - Search

    @Test func searchMatchesName() {
        let s = makeSession(id: "bonnie01", name: "bonnie", status: "running", repoName: "croft")
        #expect(SidebarFilter.matchesSearch(s, query: "bonn"))
        #expect(SidebarFilter.matchesSearch(s, query: "BONN")) // case-insensitive
    }

    @Test func searchMatchesRepo() {
        let s = makeSession(id: "bonnie02", name: "bonnie", status: "running", repoName: "glen")
        #expect(SidebarFilter.matchesSearch(s, query: "gle"))
    }

    @Test func searchEmptyMatchesEverything() {
        let s = makeSession(id: "neep0001", name: "neep", status: "running")
        #expect(SidebarFilter.matchesSearch(s, query: ""))
        #expect(SidebarFilter.matchesSearch(s, query: "   "))
    }

    @Test func searchNoMatch() {
        let s = makeSession(id: "whin0001", name: "whin", status: "running", repoName: "croft")
        #expect(!SidebarFilter.matchesSearch(s, query: "zzz"))
    }

    // MARK: - apply (composed criteria)

    private func mixed() -> [SessionInfo] {
        [
            makeSession(id: "braw0001", name: "braw", status: "running", agentStatus: "active", repoName: "croft", starred: true),
            makeSession(id: "canny001", name: "canny", status: "running", agentStatus: "ready", repoName: "croft"),
            makeSession(id: "dreich01", name: "dreich", status: "errored", repoName: "glen"),
            makeSession(id: "bide0001", name: "bide", status: "stopped", repoName: "glen", starred: true),
            makeSession(id: "scunner1", name: "scunner", status: "stopped", repoName: "bothy", dirty: true),
        ]
    }

    @Test func applyDefaultReturnsEverything() {
        let out = SidebarFilter.apply(mixed(), .init())
        #expect(out.count == 5)
    }

    @Test func applyStarredOnly() {
        let out = SidebarFilter.apply(mixed(), .init(starredOnly: true))
        #expect(Set(out.map(\.id)) == ["braw0001", "bide0001"])
    }

    @Test func applyRepoFilter() {
        let out = SidebarFilter.apply(mixed(), .init(repo: "glen"))
        #expect(Set(out.map(\.id)) == ["dreich01", "bide0001"])
    }

    @Test func applyComposedFilters() {
        let criteria = SidebarFilter.Criteria(searchQuery: "braw", starredOnly: true, repo: "croft")
        let out = SidebarFilter.apply(mixed(), criteria)
        #expect(out.map(\.id) == ["braw0001"])
    }

    @Test func applyPreservesInputOrder() {
        let out = SidebarFilter.apply(mixed(), .init(repo: "croft"))
        #expect(out.map(\.id) == ["braw0001", "canny001"])
    }

    @Test func criteriaIsActive() {
        #expect(!SidebarFilter.Criteria().isActive)
        #expect(SidebarFilter.Criteria(searchQuery: "x").isActive)
        #expect(!SidebarFilter.Criteria(searchQuery: "  ").isActive)
        #expect(SidebarFilter.Criteria(starredOnly: true).isActive)
        #expect(SidebarFilter.Criteria(repo: "glen").isActive)
    }
}

@Suite("FleetModel — sidebar filter state (#906)")
@MainActor
struct FleetModelFilterTests {
    private func sample() -> [SessionInfo] {
        [
            makeSession(id: "braw0001", name: "braw", status: "running", agentStatus: "active", repoName: "croft", starred: true),
            makeSession(id: "canny001", name: "canny", status: "errored", repoName: "croft"),
            makeSession(id: "bide0001", name: "bide", status: "stopped", repoName: "glen"),
        ]
    }

    @Test func defaultsAreInactive() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample())
        await fleet.connectAll()
        #expect(!fleet.isFilterActive)
        #expect(fleet.filtered(fleet.sessions).count == 3)
    }

    @Test func searchNarrowsGrouping() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample())
        await fleet.connectAll()
        fleet.searchQuery = "bide"
        let ids = fleet.sessionsByRepo.flatMap { $0.sessions }.map(\.id)
        #expect(ids == ["bide0001"])
    }

    @Test func starredOnlyNarrowsGrouping() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample())
        await fleet.connectAll()
        fleet.starredOnly = true
        let ids = fleet.sessionsByRepo.flatMap { $0.sessions }.map(\.id)
        #expect(ids == ["braw0001"])
    }

    @Test func repoFilterNarrowsGrouping() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample())
        await fleet.connectAll()
        fleet.repoFilter = "glen"
        let ids = fleet.sessionsByRepo.flatMap { $0.sessions }.map(\.id)
        #expect(ids == ["bide0001"])
    }

    @Test func availableReposIsSortedDistinct() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample())
        await fleet.connectAll()
        #expect(fleet.availableRepos == ["croft", "glen"])
    }

    @Test func clearFiltersResetsEverything() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample())
        await fleet.connectAll()
        fleet.searchQuery = "braw"
        fleet.starredOnly = true
        fleet.repoFilter = "croft"
        fleet.clearFilters()
        #expect(fleet.searchQuery.isEmpty)
        #expect(!fleet.starredOnly)
        #expect(fleet.repoFilter == nil)
        #expect(!fleet.isFilterActive)
    }

    @Test func allSessionsByRepoIgnoresFilter() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample())
        await fleet.connectAll()
        fleet.starredOnly = true
        // Filtered view has one repo group entry (croft/braw); the raw grouping
        // keeps all sessions regardless of the active filter.
        #expect(fleet.allSessionsByRepo.flatMap { $0.sessions }.count == 3)
    }

    /// Regression: two hosts sharing a per-daemon session id must not drop a
    /// repo from the quick-filter menu. `availableRepos` derives from
    /// `allSessions` (every connection), not the id-deduplicated `sessions`.
    @Test func availableReposSpansHostsWithCollidingIDs() async {
        // Both hosts use session id "shared01" but in different repos.
        let benSessions = [makeSession(id: "shared01", name: "ben-braw", status: "running", repoName: "croft")]
        let braeSessions = [makeSession(id: "shared01", name: "brae-braw", status: "running", repoName: "glen")]
        let fleet = makeTwoHostFleet(hostA: benSessions, hostB: braeSessions)
        await fleet.connectAll()

        // The id-deduplicated merge drops one of the colliding sessions…
        #expect(fleet.sessions.count == 1)
        // …but the repo menu still offers both repos.
        #expect(fleet.availableRepos == ["croft", "glen"])
    }
}

/// Build a `FleetModel` over two paired remote hosts, each backed by its own
/// `MockHostClient`. Used to exercise cross-host aggregation edge cases.
@MainActor
private func makeTwoHostFleet(hostA: [SessionInfo], hostB: [SessionInfo]) -> FleetModel {
    let secrets = InMemorySecretStore()
    // swiftlint:disable:next force_try
    let identity = try! DeviceIdentity(keychain: secrets)
    let registry = HostRegistry(
        keychain: secrets,
        storeURL: FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-two-host-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
    )
    registry.upsert(Host(id: "ben", label: "Ben Nevis", kind: .remote, magicDNSName: "ben.tail", isPaired: false))
    registry.upsert(Host(id: "brae", label: "Brae", kind: .remote, magicDNSName: "brae.tail", isPaired: false))
    // swiftlint:disable force_try
    try! registry.completePairing(hostID: "ben", response: PairResponseMsg(
        deviceID: "dev-ben", clientToken: "tok-ben", daemonProfile: "", tlsPinSPKI: "cGlu"))
    try! registry.completePairing(hostID: "brae", response: PairResponseMsg(
        deviceID: "dev-brae", clientToken: "tok-brae", daemonProfile: "", tlsPinSPKI: "cGlu"))
    // swiftlint:enable force_try
    let factory = MockFactory(clients: [
        "tok-ben": MockHostClient(sessions: hostA),
        "tok-brae": MockHostClient(sessions: hostB),
    ])
    let pairing = PairingCoordinator(pairing: StubPairing(), identity: identity, registry: registry)
    return FleetModel(
        registry: registry, identity: identity, reachability: nil,
        factory: factory, pairing: pairing)
}
