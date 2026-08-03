import XCTest
import GraithProtocol
@testable import GraithGUI

final class AgentPreferenceTests: XCTestCase {
    private func defaults(_ suffix: String) -> UserDefaults {
        let name = "graith-agent-pref-\(suffix)-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: name)!
        defaults.removePersistentDomain(forName: name)
        return defaults
    }

    private func catalog(_ names: [String], default defaultAgent: String) -> AgentCatalogResponseMsg {
        AgentCatalogResponseMsg(agents: names.map { AgentCatalogEntry(name: $0) }, defaultAgent: defaultAgent)
    }

    func testFreshDefaultsFollowNonClaudeDaemonDefault() {
        let defaults = defaults("fresh-default")
        let daemonCatalog = catalog(["claude", "codex"], default: "codex")

        XCTAssertNil(AgentPreference.explicitAgent(defaults: defaults))
        XCTAssertEqual(
            AgentPreference.resolve(
                explicit: AgentPreference.explicitAgent(defaults: defaults),
                catalog: daemonCatalog
            ),
            "codex"
        )
    }

    func testExplicitPreferenceStoredTrimmedAndCleared() {
        let defaults = defaults("round-trip")

        AgentPreference.store("  strath  ", defaults: defaults)
        XCTAssertEqual(AgentPreference.explicitAgent(defaults: defaults), "strath")

        AgentPreference.store(nil, defaults: defaults)
        XCTAssertNil(defaults.object(forKey: AgentPreference.key))
    }

    func testSelectionShowsExplicitOnlyWhenHostOffersIt() {
        let hostA = catalog(["claude", "codex"], default: "claude")
        let hostB = catalog(["claude"], default: "claude")

        XCTAssertEqual(AgentPreference.selection(explicit: "codex", catalog: hostA), "codex")
        XCTAssertEqual(AgentPreference.selection(explicit: "codex", catalog: hostB), "")
        XCTAssertEqual(AgentPreference.selection(explicit: "codex", catalog: nil), "")
        XCTAssertEqual(AgentPreference.selection(explicit: nil, catalog: hostA), "")
    }

    func testInspectingHostWithoutAgentCannotEraseOtherHostChoice() {
        let defaults = defaults("crosshost")
        AgentPreference.store("codex", defaults: defaults)

        let hostA = catalog(["claude", "codex"], default: "claude")
        let hostB = catalog(["claude"], default: "claude")

        _ = AgentPreference.selection(
            explicit: AgentPreference.explicitAgent(defaults: defaults),
            catalog: hostB
        )

        XCTAssertEqual(AgentPreference.explicitAgent(defaults: defaults), "codex")
        XCTAssertEqual(
            AgentPreference.resolve(
                explicit: AgentPreference.explicitAgent(defaults: defaults),
                catalog: hostA
            ),
            "codex"
        )
    }

    func testResolveIgnoresExplicitPreferenceWhenHostDoesNotOfferIt() {
        let hostB = catalog(["claude"], default: "claude")

        XCTAssertEqual(AgentPreference.resolve(explicit: "codex", catalog: hostB), "claude")
    }

    func testResolveOmitsAgentWhenCatalogUnavailableOrEmpty() {
        XCTAssertEqual(AgentPreference.resolve(explicit: "codex", catalog: nil), "")
        XCTAssertEqual(AgentPreference.resolve(explicit: "codex", catalog: catalog([], default: "")), "")
    }
}
