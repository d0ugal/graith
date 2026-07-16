import XCTest
import GraithProtocol
@testable import GraithGUI

final class AgentPreferenceTests: XCTestCase {
    private func defaults(_ name: String) -> UserDefaults {
        let suite = "graith.agent-preference.\(name).\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        return defaults
    }

    private var catalog: AgentCatalogResponseMsg {
        AgentCatalogResponseMsg(
            agents: [AgentCatalogEntry(name: "claude"), AgentCatalogEntry(name: "codex")],
            defaultAgent: "codex")
    }

    func testFreshUserDefaultsUsesDaemonDefault() {
        let defaults = defaults("fresh-braw")
        let explicit = AgentPreference.explicitAgent(defaults: defaults)
        XCTAssertNil(explicit)
        XCTAssertEqual(AgentPreference.resolve(explicit: explicit, catalog: catalog), "codex")
    }

    func testExplicitPreferenceOverridesDaemonDefaultWhenOffered() {
        let defaults = defaults("explicit-canny")
        AgentPreference.store("claude", defaults: defaults)
        XCTAssertEqual(
            AgentPreference.resolve(
                explicit: AgentPreference.explicitAgent(defaults: defaults),
                catalog: catalog),
            "claude")
    }

    func testRemovedPreferenceFallsBackToDaemonDefault() {
        XCTAssertEqual(AgentPreference.resolve(explicit: "dreich", catalog: catalog), "codex")
    }

    func testUnavailableCatalogOmitsAgent() {
        XCTAssertEqual(AgentPreference.resolve(explicit: "claude", catalog: nil), "")
    }

    func testClearingPreferenceRemovesDefaultsKey() {
        let defaults = defaults("clear-bothy")
        AgentPreference.store("claude", defaults: defaults)
        AgentPreference.store(nil, defaults: defaults)
        XCTAssertNil(defaults.object(forKey: AgentPreference.key))
    }
}
