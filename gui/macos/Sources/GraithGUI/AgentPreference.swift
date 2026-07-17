import Foundation
import GraithProtocol

/// macOS-only preference layered over the selected host's daemon catalog.
/// Absence is meaningful: a fresh profile follows the daemon's default_agent.
enum AgentPreference {
    static let key = "defaultAgent"

    static func explicitAgent(defaults: UserDefaults = .standard) -> String? {
        guard let raw = defaults.object(forKey: key) as? String else { return nil }
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }

    static func store(_ agent: String?, defaults: UserDefaults = .standard) {
        let trimmed = agent?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if trimmed.isEmpty {
            defaults.removeObject(forKey: key)
        } else {
            defaults.set(trimmed, forKey: key)
        }
    }

    /// Resolve an explicit local override only when this host offers it. With no
    /// override (or a removed one), use the daemon's configured default. With no
    /// catalog, return empty so Create omits `agent` and the daemon resolves it.
    static func resolve(explicit: String?, catalog: AgentCatalogResponseMsg?) -> String {
        guard let catalog, !catalog.names.isEmpty else { return "" }
        if let explicit, catalog.names.contains(explicit) { return explicit }
        return catalog.resolvedDefault
    }

    /// The Settings picker selection to show for a host whose catalog is
    /// `catalog`, given the stored global `explicit` preference. Returns the
    /// explicit value only when *this* host offers it; otherwise `""` ("follow
    /// this host's daemon default").
    ///
    /// The preference is stored globally, so this is deliberately **read-only**:
    /// a host that does not offer the agent shows the daemon-default row but must
    /// never erase the stored value, or inspecting one host in Settings would
    /// destroy a choice still valid on another (#1234). Only an explicit user
    /// pick — via `store(_:)` — changes the persisted preference.
    static func selection(explicit: String?, catalog: AgentCatalogResponseMsg?) -> String {
        guard let explicit, let catalog, catalog.names.contains(explicit) else { return "" }
        return explicit
    }
}
