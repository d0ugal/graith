import Foundation
import GraithProtocol

// The macOS app uses the canonical wire model from GraithProtocol directly.
// `Session` is kept as an alias so existing UI code compiles unchanged. The
// status/branch conveniences (isRunning, needsApproval, shortBranch, isYolo, …)
// now live once in the shared `GraithSessionKit` (SessionInfo extension) so both
// apps share them (#1131) — import that module where they're used.
typealias Session = SessionInfo
