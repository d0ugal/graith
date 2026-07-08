import Foundation
import GraithProtocol

// The macOS app now uses the canonical wire model from GraithProtocol directly,
// instead of a hand-rolled Codable that had drifted from the daemon (it carried
// cost_usd/context_percent, which are not on the wire, and lacked PR/CI/scenario
// fields). `Session` is kept as an alias so existing UI code compiles unchanged.
typealias Session = SessionInfo

extension SessionInfo {
    var isRunning: Bool { status == "running" }
    var isStopped: Bool { status == "stopped" }
    var isErrored: Bool { status == "errored" }
    var needsApproval: Bool { agentStatus == "approval" }

    var shortBranch: String {
        let parts = branch.split(separator: "/", maxSplits: 2)
        if parts.count == 3 { return String(parts[2]) }
        return branch
    }
}
