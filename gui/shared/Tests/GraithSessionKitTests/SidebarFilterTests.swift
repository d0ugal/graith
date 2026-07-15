import Testing
import Foundation
import GraithProtocol
@testable import GraithSessionKit

// Exercises the shared sidebar filter/search/view-mode logic (#906): the pure
// `SidebarFilter` predicates and the `FleetModel` filter state that drives both
// GUIs' grouping helpers.

@Suite("SidebarFilter — pure predicates (#906)")
struct SidebarFilterPureTests {
    // MARK: - Needs-attention (mirrors overlay.filterNeedsAttention)

    @Test func needsAttentionMatchesApproval() {
        let s = makeSession(id: "fash0001", name: "fash", status: "running", agentStatus: "approval")
        #expect(SidebarFilter.needsAttention(s))
    }

    @Test func needsAttentionMatchesErrored() {
        let s = makeSession(id: "dreich01", name: "dreich", status: "errored")
        #expect(SidebarFilter.needsAttention(s))
    }

    @Test func needsAttentionMatchesRunningReady() {
        let s = makeSession(id: "thrawn01", name: "thrawn", status: "running", agentStatus: "ready")
        #expect(SidebarFilter.needsAttention(s))
    }

    @Test func needsAttentionMatchesStoppedDirty() {
        let s = makeSession(id: "scunner1", name: "scunner", status: "stopped", dirty: true)
        #expect(SidebarFilter.needsAttention(s))
    }

    @Test func needsAttentionMatchesStoppedUnpushed() {
        let s = makeSession(id: "scunner2", name: "scunner", status: "stopped", unpushedCount: 3)
        #expect(SidebarFilter.needsAttention(s))
    }

    @Test func needsAttentionExcludesMirrorEvenWhenDirty() {
        // A mirror session's dirty/unpushed state isn't the human's to action.
        let s = makeSession(id: "haar0001", name: "haar", status: "stopped", dirty: true, mirror: true)
        #expect(!SidebarFilter.needsAttention(s))
    }

    @Test func needsAttentionExcludesQuietRunning() {
        let s = makeSession(id: "braw0001", name: "braw", status: "running", agentStatus: "active")
        #expect(!SidebarFilter.needsAttention(s))
    }

    @Test func needsAttentionExcludesCleanStopped() {
        let s = makeSession(id: "bide0001", name: "bide", status: "stopped")
        #expect(!SidebarFilter.needsAttention(s))
    }

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

    @Test func applyAllReturnsEverything() {
        let out = SidebarFilter.apply(mixed(), .init(viewMode: .all))
        #expect(out.count == 5)
    }

    @Test func applyActiveReturnsRunningOnly() {
        let out = SidebarFilter.apply(mixed(), .init(viewMode: .active))
        #expect(out.map(\.id).sorted() == ["braw0001", "canny001"])
    }

    @Test func applyNeedsAttention() {
        let out = SidebarFilter.apply(mixed(), .init(viewMode: .needsAttention))
        // canny (running+ready), dreich (errored), scunner (stopped+dirty).
        #expect(Set(out.map(\.id)) == ["canny001", "dreich01", "scunner1"])
    }

    @Test func applyStarredOnly() {
        let out = SidebarFilter.apply(mixed(), .init(starredOnly: true))
        #expect(Set(out.map(\.id)) == ["braw0001", "bide0001"])
    }

    @Test func applyRepoFilter() {
        let out = SidebarFilter.apply(mixed(), .init(repo: "glen"))
        #expect(Set(out.map(\.id)) == ["dreich01", "bide0001"])
    }

    @Test func applyComposedModeAndSearch() {
        // Active + search "can" → only canny.
        let out = SidebarFilter.apply(mixed(), .init(viewMode: .active, searchQuery: "can"))
        #expect(out.map(\.id) == ["canny001"])
    }

    @Test func applyPreservesInputOrder() {
        let out = SidebarFilter.apply(mixed(), .init(repo: "croft"))
        #expect(out.map(\.id) == ["braw0001", "canny001"])
    }

    @Test func criteriaIsActive() {
        #expect(!SidebarFilter.Criteria().isActive)
        #expect(SidebarFilter.Criteria(viewMode: .active).isActive)
        #expect(SidebarFilter.Criteria(searchQuery: "x").isActive)
        #expect(!SidebarFilter.Criteria(searchQuery: "  ").isActive)
        #expect(SidebarFilter.Criteria(starredOnly: true).isActive)
        #expect(SidebarFilter.Criteria(repo: "glen").isActive)
    }

    @Test func viewModeDisplayNamesMatchOverlay() {
        #expect(SidebarViewMode.all.displayName == "All")
        #expect(SidebarViewMode.needsAttention.displayName == "Needs Attention")
        #expect(SidebarViewMode.active.displayName == "Active")
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
        let (fleet, _) = makeFleetWithRemote(sessions: sample(), subscribeApprovals: false)
        await fleet.connectAll()
        #expect(fleet.viewMode == .all)
        #expect(!fleet.isFilterActive)
        #expect(fleet.filtered(fleet.sessions).count == 3)
    }

    @Test func viewModeNarrowsGrouping() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample(), subscribeApprovals: false)
        await fleet.connectAll()
        fleet.viewMode = .needsAttention
        #expect(fleet.isFilterActive)
        let ids = fleet.sessionsByRepo.flatMap { $0.sessions }.map(\.id)
        #expect(ids == ["canny001"]) // only the errored session
    }

    @Test func searchNarrowsGrouping() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample(), subscribeApprovals: false)
        await fleet.connectAll()
        fleet.searchQuery = "bide"
        let ids = fleet.sessionsByRepo.flatMap { $0.sessions }.map(\.id)
        #expect(ids == ["bide0001"])
    }

    @Test func starredOnlyNarrowsGrouping() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample(), subscribeApprovals: false)
        await fleet.connectAll()
        fleet.starredOnly = true
        let ids = fleet.sessionsByRepo.flatMap { $0.sessions }.map(\.id)
        #expect(ids == ["braw0001"])
    }

    @Test func repoFilterNarrowsGrouping() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample(), subscribeApprovals: false)
        await fleet.connectAll()
        fleet.repoFilter = "glen"
        let ids = fleet.sessionsByRepo.flatMap { $0.sessions }.map(\.id)
        #expect(ids == ["bide0001"])
    }

    @Test func availableReposIsSortedDistinct() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample(), subscribeApprovals: false)
        await fleet.connectAll()
        #expect(fleet.availableRepos == ["croft", "glen"])
    }

    @Test func clearFiltersResetsEverything() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample(), subscribeApprovals: false)
        await fleet.connectAll()
        fleet.viewMode = .active
        fleet.searchQuery = "braw"
        fleet.starredOnly = true
        fleet.repoFilter = "croft"
        fleet.clearFilters()
        #expect(fleet.viewMode == .all)
        #expect(fleet.searchQuery.isEmpty)
        #expect(!fleet.starredOnly)
        #expect(fleet.repoFilter == nil)
        #expect(!fleet.isFilterActive)
    }

    @Test func allSessionsByRepoIgnoresFilter() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sample(), subscribeApprovals: false)
        await fleet.connectAll()
        fleet.viewMode = .active
        // Filtered view has one repo group entry (croft/braw); the raw grouping
        // keeps all sessions regardless of the active filter.
        #expect(fleet.allSessionsByRepo.flatMap { $0.sessions }.count == 3)
    }
}
