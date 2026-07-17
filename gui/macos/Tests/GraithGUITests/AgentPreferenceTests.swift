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

    // MARK: - Cross-host non-destructive policy (#1234)

    private func catalog(_ names: [String], default def: String) -> AgentCatalogResponseMsg {
        AgentCatalogResponseMsg(agents: names.map { AgentCatalogEntry(name: $0) }, defaultAgent: def)
    }

    func testSelectionShowsExplicitOnlyWhenHostOffersIt() {
        let hostA = catalog(["claude", "codex"], default: "claude")
        let hostB = catalog(["claude"], default: "claude")
        // Host A offers codex ⇒ the picker shows it selected.
        XCTAssertEqual(AgentPreference.selection(explicit: "codex", catalog: hostA), "codex")
        // Host B lacks codex ⇒ the picker follows the daemon default (empty tag).
        XCTAssertEqual(AgentPreference.selection(explicit: "codex", catalog: hostB), "")
        // No catalog / no preference ⇒ daemon default (empty tag).
        XCTAssertEqual(AgentPreference.selection(explicit: "codex", catalog: nil), "")
        XCTAssertEqual(AgentPreference.selection(explicit: nil, catalog: hostA), "")
    }

    /// The regression for the round-3 finding: inspecting host B in Settings —
    /// whose catalog lacks the agent — must not erase a preference still valid on
    /// host A. `selection` is read-only, so the stored value survives and host A
    /// still resolves it.
    func testInspectingHostWithoutAgentCannotEraseOtherHostChoice() {
        let defaults = defaults("crosshost-strath")
        AgentPreference.store("codex", defaults: defaults)   // valid on host A

        let hostA = catalog(["claude", "codex"], default: "claude")
        let hostB = catalog(["claude"], default: "claude")   // no codex

        // Simulate opening Settings against host B (the destructive path before
        // the fix): compute the selection with B's catalog.
        _ = AgentPreference.selection(
            explicit: AgentPreference.explicitAgent(defaults: defaults),
            catalog: hostB)

        // The stored global preference is untouched...
        XCTAssertEqual(AgentPreference.explicitAgent(defaults: defaults), "codex")
        // ...and host A still resolves codex for a new session.
        XCTAssertEqual(
            AgentPreference.resolve(
                explicit: AgentPreference.explicitAgent(defaults: defaults),
                catalog: hostA),
            "codex")
    }
}
