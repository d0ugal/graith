import Foundation
import GraithProtocol

/// Sidebar view modes, mirroring the CLI attach overlay's modes
/// (`internal/client/overlay.go`): All / Needs Attention / Active. Shared by
/// both GUIs (#906) so filtering behaves identically on macOS and iOS.
public enum SidebarViewMode: String, CaseIterable, Sendable, Identifiable {
    case all
    case needsAttention
    case active

    public var id: String { rawValue }

    /// Human-readable label for the segmented control / menu.
    public var displayName: String {
        switch self {
        case .all: return "All"
        case .needsAttention: return "Needs Attention"
        case .active: return "Active"
        }
    }

    /// SF Symbol used in compact UI (e.g. the iOS menu).
    public var symbolName: String {
        switch self {
        case .all: return "square.grid.2x2"
        case .needsAttention: return "exclamationmark.triangle"
        case .active: return "bolt.horizontal"
        }
    }
}

/// Pure, stateless session-filtering logic shared by both sidebars (#906).
///
/// Kept free of any UI or `FleetModel` state so it is trivially unit-testable
/// and identical across platforms. `FleetModel` holds the *selected* filter
/// state and calls `apply` to narrow its session lists before grouping.
public enum SidebarFilter {
    /// The full filter criteria a sidebar can apply.
    public struct Criteria: Equatable, Sendable {
        public var viewMode: SidebarViewMode
        /// Free-text query matched (case-insensitively) against name + repo.
        public var searchQuery: String
        /// Restrict to starred sessions only.
        public var starredOnly: Bool
        /// Restrict to a single repo (by `repoName`); nil means all repos.
        public var repo: String?

        public init(
            viewMode: SidebarViewMode = .all,
            searchQuery: String = "",
            starredOnly: Bool = false,
            repo: String? = nil
        ) {
            self.viewMode = viewMode
            self.searchQuery = searchQuery
            self.starredOnly = starredOnly
            self.repo = repo
        }

        /// Whether any criterion actually narrows the list (used to decide
        /// whether to show a "clear filters" affordance / empty state).
        public var isActive: Bool {
            viewMode != .all
                || starredOnly
                || repo != nil
                || !searchQuery.trimmingCharacters(in: .whitespaces).isEmpty
        }
    }

    /// A single session's "needs attention" test, matching the overlay's
    /// `filterNeedsAttention` (`internal/client/overlay.go`): pending approval,
    /// errored, running-and-ready, or a stopped non-mirror session with
    /// uncommitted/unpushed work.
    public static func needsAttention(_ session: SessionInfo) -> Bool {
        if session.needsApproval { return true }
        if session.isErrored { return true }
        if session.isRunning && session.agentStatus == "ready" { return true }
        if session.isStopped && !(session.mirror ?? false)
            && ((session.dirty ?? false) || (session.unpushedCount ?? 0) > 0) {
            return true
        }
        return false
    }

    /// Whether a session matches a free-text query (case-insensitive substring
    /// over name + repo name), mirroring the overlay's `FilterValue`.
    public static func matchesSearch(_ session: SessionInfo, query: String) -> Bool {
        let trimmed = query.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty else { return true }
        let haystack = "\(session.name) \(session.repoName)"
        return haystack.range(of: trimmed, options: .caseInsensitive) != nil
    }

    /// Apply all criteria to a session list, preserving input order.
    public static func apply(_ sessions: [SessionInfo], _ criteria: Criteria) -> [SessionInfo] {
        sessions.filter { session in
            switch criteria.viewMode {
            case .all:
                break
            case .needsAttention:
                if !needsAttention(session) { return false }
            case .active:
                if !session.isRunning { return false }
            }
            if criteria.starredOnly && !(session.starred ?? false) { return false }
            if let repo = criteria.repo, session.repoName != repo { return false }
            if !matchesSearch(session, query: criteria.searchQuery) { return false }
            return true
        }
    }
}
