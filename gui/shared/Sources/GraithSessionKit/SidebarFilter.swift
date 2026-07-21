import Foundation
import GraithProtocol

/// Pure, stateless session-filtering logic shared by both sidebars (#906).
///
/// Kept free of any UI or `FleetModel` state so it is trivially unit-testable
/// and identical across platforms. `FleetModel` holds the *selected* filter
/// state and calls `apply` to narrow its session lists before grouping.
public enum SidebarFilter {
    /// Canonical representatives for the multi-member case-fold orbits in
    /// Go's `unicode.SimpleFold` table. Swift's lower/uppercase APIs expose
    /// directed mappings, so walking them alone cannot discover reverse-only
    /// members such as micro sign (µ) from Greek mu (μ). Two-member orbits are
    /// handled by the casing walk below; these exceptional cycles complete the
    /// daemon's locale-independent identity semantics.
    private static let multiMemberSimpleFoldCanonical: [UInt32: UInt32] = {
        let orbits: [[UInt32]] = [
            [0x004B, 0x006B, 0x212A],
            [0x0053, 0x0073, 0x017F],
            [0x00B5, 0x039C, 0x03BC],
            [0x00C5, 0x00E5, 0x212B],
            [0x01C4, 0x01C5, 0x01C6],
            [0x01C7, 0x01C8, 0x01C9],
            [0x01CA, 0x01CB, 0x01CC],
            [0x01F1, 0x01F2, 0x01F3],
            [0x0345, 0x0399, 0x03B9, 0x1FBE],
            [0x0392, 0x03B2, 0x03D0],
            [0x0395, 0x03B5, 0x03F5],
            [0x0398, 0x03B8, 0x03D1, 0x03F4],
            [0x039A, 0x03BA, 0x03F0],
            [0x03A0, 0x03C0, 0x03D6],
            [0x03A1, 0x03C1, 0x03F1],
            [0x03A3, 0x03C2, 0x03C3],
            [0x03A6, 0x03C6, 0x03D5],
            [0x03A9, 0x03C9, 0x2126],
            [0x0412, 0x0432, 0x1C80],
            [0x0414, 0x0434, 0x1C81],
            [0x041E, 0x043E, 0x1C82],
            [0x0421, 0x0441, 0x1C83],
            [0x0422, 0x0442, 0x1C84, 0x1C85],
            [0x042A, 0x044A, 0x1C86],
            [0x0462, 0x0463, 0x1C87],
            [0x1C88, 0xA64A, 0xA64B],
            [0x1E60, 0x1E61, 0x1E9B],
        ]
        var canonical: [UInt32: UInt32] = [:]
        for orbit in orbits {
            guard let representative = orbit.min() else { continue }
            for member in orbit { canonical[member] = representative }
        }
        return canonical
    }()

    /// Locale-independent Unicode simple-fold identity, matching Go's
    /// `strings.EqualFold` without applying canonical normalization. Expanding
    /// mappings such as ß → SS are intentionally ignored because they are not
    /// part of Unicode simple folding.
    static func labelIdentity(_ value: String) -> [UInt32] {
        value.unicodeScalars.map { scalar in
            if let canonical = multiMemberSimpleFoldCanonical[scalar.value] {
                return canonical
            }

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
