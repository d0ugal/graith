import Foundation
import GraithProtocol

/// Loading/result state for the daemon-authoritative agent catalog (#1234).
///
/// There is intentionally no Swift fallback catalog. Before the RPC completes,
/// the UI shows loading; if it fails (including against an older daemon), the UI
/// shows unavailable and leaves the create request's agent empty so the daemon
/// resolves its own default. This prevents a client-side list from drifting from
/// the selected host's effective `[agents.*]` configuration.
public enum AgentCatalogState: Sendable {
    case loading
    case available(AgentCatalogResponseMsg)
    case unavailable(String)

    public var catalog: AgentCatalogResponseMsg? {
        guard case let .available(catalog) = self else { return nil }
        return catalog
    }

    public var unavailableReason: String? {
        guard case let .unavailable(reason) = self else { return nil }
        return reason
    }
}

public extension AgentCatalogResponseMsg {
    /// The agent names in display order — the value bound to each picker chip.
    var names: [String] { agents.map(\.name) }

    /// The default agent to preselect, guaranteed to be one of `names` when the
    /// catalog is non-empty (falls back to the first entry if `defaultAgent`
    /// somehow isn't present, e.g. a misconfigured daemon).
    var resolvedDefault: String {
        if names.contains(defaultAgent) { return defaultAgent }
        return names.first ?? defaultAgent
    }
}
