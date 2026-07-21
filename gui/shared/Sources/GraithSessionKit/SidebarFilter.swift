import Foundation
import GraithProtocol

/// Pure, stateless session-filtering logic shared by both sidebars (#906).
///
/// Kept free of any UI or `FleetModel` state so it is trivially unit-testable
/// and identical across platforms. `FleetModel` holds the *selected* filter
/// state and calls `apply` to narrow its session lists before grouping.
public enum SidebarFilter {
    /// Locale-independent Unicode simple-fold identity, matching Go's
    /// `strings.EqualFold` without applying canonical normalization. Expanding
    /// mappings such as ß → SS are intentionally ignored because they are not
    /// part of Unicode simple folding.
    static func labelIdentity(_ value: String) -> [UInt32] {
        value.unicodeScalars.map { scalar in
            var seen: Set<UInt32> = []
            var pending: [UInt32] = [scalar.value]
            var minimum = scalar.value

            while let current = pending.popLast() {
                guard seen.insert(current).inserted, let unicode = UnicodeScalar(current) else { continue }
                minimum = min(minimum, current)
                let text = String(unicode)
                for mappedText in [text.lowercased(), text.uppercased()] {
                    let mapped = Array(mappedText.unicodeScalars)
                    if mapped.count == 1, !seen.contains(mapped[0].value) {
                        pending.append(mapped[0].value)
                    }
                }
            }

            return minimum
        }
    }

    public static func labelsEqual(_ lhs: String, _ rhs: String) -> Bool {
        labelIdentity(lhs) == labelIdentity(rhs)
    }

    /// The full filter criteria a sidebar can apply.
    public struct Criteria: Equatable, Sendable {
        /// Free-text query matched (case-insensitively) against name + repo + labels.
        public var searchQuery: String
        /// Restrict to starred sessions only.
        public var starredOnly: Bool
        /// Restrict to a single repo (by `repoName`); nil means all repos.
        public var repo: String?
        /// Restrict to a single session label; nil means all labels.
        public var label: String?

        public init(
            searchQuery: String = "",
            starredOnly: Bool = false,
            repo: String? = nil,
            label: String? = nil
        ) {
            self.searchQuery = searchQuery
            self.starredOnly = starredOnly
            self.repo = repo
            self.label = label
        }

        /// Whether any criterion actually narrows the list (used to decide
        /// whether to show a "clear filters" affordance / empty state).
        public var isActive: Bool {
            starredOnly
                || repo != nil
                || label != nil
                || !searchQuery.trimmingCharacters(in: .whitespaces).isEmpty
        }
    }

    /// Whether a session matches a free-text query: a case-insensitive
    /// contiguous substring over name + repo name + labels. This is a deliberate subset
    /// of the CLI overlay's search (which also matches status/agent/branch/
    /// summary tokens and ANDs whitespace-separated terms); the GUI v1 searches
    /// only session identity and organizational metadata.
    public static func matchesSearch(_ session: SessionInfo, query: String) -> Bool {
        let trimmed = query.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty else { return true }
        let haystack = ([session.name, session.repoName] + (session.labels ?? [])).joined(separator: " ")
        return haystack.range(of: trimmed, options: .caseInsensitive) != nil
    }

    /// Apply all criteria to a session list, preserving input order.
    public static func apply(_ sessions: [SessionInfo], _ criteria: Criteria) -> [SessionInfo] {
        sessions.filter { session in
            if criteria.starredOnly && !(session.starred ?? false) { return false }
            if let repo = criteria.repo, session.repoName != repo { return false }
            if let label = criteria.label,
               !(session.labels ?? []).contains(where: { labelsEqual($0, label) }) {
                return false
            }
            if !matchesSearch(session, query: criteria.searchQuery) { return false }
            return true
        }
    }
}
