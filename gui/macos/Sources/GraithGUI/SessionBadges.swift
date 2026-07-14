import SwiftUI
import GraithProtocol

// Small, compact metadata badges shown on each sidebar row (issue #901). The
// daemon already sends these fields on the wire (sandboxed, yolo, config_stale,
// scenario, pull_request, ci) — before this they were decoded and thrown away.
//
// The presentation *logic* (which state maps to which colour/icon) is factored
// into pure functions/enums so it can be unit-tested without driving SwiftUI —
// `Color` values are opaque in tests, the style buckets are not.

extension SessionInfo {
    /// YOLO mode — the agent runs with approvals bypassed.
    var isYolo: Bool { yolo == true }
    /// Agent process is confined by the OS sandbox.
    var isSandboxed: Bool { sandboxed == true }
    /// Session belongs to a scenario (multi-session orchestration).
    var isScenarioMember: Bool { !(scenarioID ?? "").isEmpty }
    /// Config changed since the session launched — restart to pick it up.
    var isConfigStale: Bool { configStale == true }
}

/// Visual style buckets for the PR badge, derived from the PR state (plus the
/// merge-conflict flag). Kept separate from the SwiftUI `Color` so it is testable.
enum PRBadgeStyle: Equatable {
    case merged, closed, draft, conflicting, open
}

func prBadgeStyle(for pr: PRInfo) -> PRBadgeStyle {
    switch pr.state {
    case "merged": return .merged
    case "closed": return .closed
    case "draft": return .draft
    default: return pr.conflicting == true ? .conflicting : .open
    }
}

/// Visual style buckets for the CI badge.
enum CIBadgeStyle: Equatable {
    case passing, failing, pending
}

func ciBadgeStyle(for ci: CIInfo) -> CIBadgeStyle {
    switch ci.state {
    case "passing": return .passing
    case "failing": return .failing
    default: return .pending
    }
}

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
        case .conflicting: return Theme.peach
        case .open: return Theme.blue
        }
    }

    private var helpText: String {
        switch prBadgeStyle(for: pr) {
        case .merged: return "PR #\(pr.number) merged"
        case .closed: return "PR #\(pr.number) closed"
        case .draft: return "PR #\(pr.number) (draft)"
        case .conflicting: return "PR #\(pr.number) has merge conflicts"
        case .open: return "PR #\(pr.number) open"
        }
    }
}

/// Compact CI status badge: a single coloured glyph (pass / fail / pending).
struct CIBadge: View {
    let ci: CIInfo

    var body: some View {
        Image(systemName: icon)
            .font(.system(size: 9))
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
            return failing.isEmpty ? "CI failing" : "CI failing: \(failing.joined(separator: ", "))"
        case .pending: return "CI running"
        }
    }
}
