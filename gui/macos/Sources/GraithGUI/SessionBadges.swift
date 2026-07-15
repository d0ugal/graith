import SwiftUI
import GraithProtocol
import GraithSessionKit

// Small, compact metadata badges shown on each sidebar row (issue #901). The
// daemon already sends these fields on the wire (sandboxed, yolo, config_stale,
// scenario, pull_request, ci) — before this they were decoded and thrown away.
//
// The presentation *logic* (which state maps to which colour/icon) is factored
// into pure functions/enums so it can be unit-tested without driving SwiftUI —
// `Color` values are opaque in tests, the style buckets are not.

// The `isYolo`/`isSandboxed`/`isScenarioMember`/`isConfigStale` conveniences now
// live once in the shared `GraithSessionKit` (SessionInfo extension) so both
// apps share them (#1131). Import GraithSessionKit where they're consumed.

/// Visual style buckets for the PR badge, derived from the PR state (plus the
/// merge-conflict flag). Kept separate from the SwiftUI `Color` so it is testable.
enum PRBadgeStyle: Equatable {
    case merged, closed, draft, conflicting, draftConflicting, open
}

func prBadgeStyle(for pr: PRInfo) -> PRBadgeStyle {
    // Merged/closed are terminal states: the daemon stops checking, so they win
    // outright (matching the terminal overlay's `#56 merged`).
    switch pr.state {
    case "merged": return .merged
    case "closed": return .closed
    default: break
    }
    // For an open/draft PR a merge conflict is the highest-priority, actionable
    // signal — it must not be swallowed by the draft styling. This mirrors the
    // terminal overlay, which shows a draft *and* a conflict together (`#56d ⚠`).
    if pr.conflicting == true {
        return pr.state == "draft" ? .draftConflicting : .conflicting
    }
    return pr.state == "draft" ? .draft : .open
}

// The CI style buckets, visibility rule, and progress-count text now live once
// in the shared `GraithSessionKit` (`CIBadgeStyle`, `ciBadgeStyle`,
// `shouldShowCI`, `CIInfo.badgeCountText`) so both apps share them (#1173).

/// Compact pull-request badge: pull-request glyph + `#number`, coloured by state.
struct PRBadge: View {
    let pr: PRInfo

    var body: some View {
        HStack(spacing: 2) {
            Image(systemName: "arrow.triangle.pull")
            Text("#\(pr.number)")
        }
        .font(.system(size: 9, design: .monospaced))
        .foregroundStyle(color)
        .help(helpText)
    }

    private var color: Color {
        switch prBadgeStyle(for: pr) {
        case .merged: return Theme.mauve
        case .closed: return Theme.red
        case .draft: return Theme.overlay0
        // A conflict is actionable regardless of draft-ness — colour it the same.
        case .conflicting, .draftConflicting: return Theme.peach
        case .open: return Theme.blue
        }
    }

    private var helpText: String {
        switch prBadgeStyle(for: pr) {
        case .merged: return "PR #\(pr.number) merged"
        case .closed: return "PR #\(pr.number) closed"
        case .draft: return "PR #\(pr.number) (draft)"
        case .conflicting: return "PR #\(pr.number) has merge conflicts"
        case .draftConflicting: return "PR #\(pr.number) (draft) has merge conflicts"
        case .open: return "PR #\(pr.number) open"
        }
    }
}

/// Compact CI status badge: a coloured glyph, plus the passed/total check count
/// ("16/22") while CI is running/failing so progress is visible; the count falls
/// back to the bare glyph when unavailable, and passing shows only the ✓ (a
/// "22/22" adds nothing once done). Colours track the CI state (#1173).
struct CIBadge: View {
    let ci: CIInfo

    var body: some View {
        HStack(spacing: 2) {
            Image(systemName: icon)
            if let count = ci.badgeCountText {
                Text(count)
            }
        }
        .font(.system(size: 9, design: .monospaced))
        .foregroundStyle(color)
        .help(helpText)
    }

    private var icon: String {
        switch ciBadgeStyle(for: ci) {
        case .passing: return "checkmark.circle.fill"
        case .failing: return "xmark.circle.fill"
        case .pending: return "clock.fill"
        }
    }

    private var color: Color {
        switch ciBadgeStyle(for: ci) {
        case .passing: return Theme.green
        case .failing: return Theme.red
        case .pending: return Theme.yellow
        }
    }

    private var helpText: String {
        switch ciBadgeStyle(for: ci) {
        case .passing: return "CI passing"
        case .failing:
            let failing = ci.failingChecks ?? []
            let base = failing.isEmpty ? "CI failing" : "CI failing: \(failing.joined(separator: ", "))"
            if let count = ci.progressText { return "\(base) (\(count))" }
            return base
        case .pending:
            if let count = ci.progressText { return "CI running (\(count))" }
            return "CI running"
        }
    }
}
