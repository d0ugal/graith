import Foundation
import GraithProtocol

// Shared CI-badge presentation logic (#1173). Both GUIs render a compact CI
// status badge on each sidebar row; before this the style/visibility mapping was
// duplicated (macOS had pure functions, iOS inlined the switch) and neither
// showed the passed/total check count the terminal overlay/`gr ls` grew in
// #1172. Factoring the *logic* here — as pure functions the apps' SwiftUI views
// bind to — keeps the two platforms in lockstep (parity-by-construction, #1147)
// and lets it be unit-tested without driving SwiftUI. The `Color`/`Image`
// choices stay in each app's view; the buckets, visibility, and count text live
// here.

/// Visual style buckets for the CI badge, derived from the wire state.
public enum CIBadgeStyle: Equatable, Sendable {
    case passing, failing, pending
}

/// Map the wire `state` to a style bucket. Anything unrecognised falls back to
/// the neutral "running" bucket so a new daemon state can't blank the badge.
public func ciBadgeStyle(for ci: CIInfo) -> CIBadgeStyle {
    switch ci.state {
    case "passing": return .passing
    case "failing": return .failing
    default: return .pending
    }
}

/// Whether the CI badge should be shown alongside a PR. The daemon keeps the
/// last-known CI value even after a PR is merged/closed (it stops polling
/// checks), so — matching the terminal overlay, which drops CI for terminal PR
/// states — suppress the CI badge for merged/closed PRs to avoid showing stale
/// results (#773). CI is still shown for open/draft PRs and for sessions with
/// no PR.
public func shouldShowCI(pr: PRInfo?, ci: CIInfo?) -> Bool {
    guard ci != nil else { return false }
    if let pr, pr.state == "merged" || pr.state == "closed" { return false }
    return true
}

public extension CIInfo {
    /// The "<passed>/<total>" progress fragment, or nil when no count is
    /// available (`total` nil or <= 0) so callers fall back to a plain glyph.
    /// Mirrors the terminal overlay's `ciCounts`.
    var progressText: String? {
        guard let total, total > 0 else { return nil }
        return "\(passed ?? 0)/\(total)"
    }

    /// The count label to render beside the CI glyph, or nil when a count adds
    /// no information. Passing keeps the bare "✓" — a "22/22" is redundant once
    /// the run is done; pending and failing show "16/22" when a count is
    /// available (the glyph already conveys the pass/fail state), matching the
    /// terminal overlay's compact row (#1173).
    var badgeCountText: String? {
        switch ciBadgeStyle(for: self) {
        case .passing: return nil
        case .pending, .failing: return progressText
        }
    }
}
