import Foundation
import GraithProtocol

/// The agent catalog the GUI presents in its New Session / Settings pickers.
///
/// The source of truth is the daemon: `HostConnection.agentCatalog` is populated
/// from the `agent_catalog` RPC on connect (#1234). This type only supplies a
/// built-in fallback for the window before that fetch lands — or when talking to
/// an older daemon that predates the RPC — so a picker is never empty.
public enum AgentCatalog {
    /// Built-in fallback matching the daemon's default `[agents.*]` set, sorted
    /// by name like the daemon returns them. Used only until the real catalog is
    /// fetched; once the daemon replies, its list (including any custom agents)
    /// replaces this.
    public static let fallback = AgentCatalogResponseMsg(
        agents: [
            AgentCatalogEntry(name: "agy"),
            AgentCatalogEntry(name: "claude"),
            AgentCatalogEntry(name: "codex"),
            AgentCatalogEntry(name: "cursor"),
            AgentCatalogEntry(name: "opencode"),
        ],
        defaultAgent: "claude"
    )
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
