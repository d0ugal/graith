import Foundation
import GraithProtocol

/// One health finding derived from a `DiagnosticsMsg`, mirroring a line `gr
/// doctor` would print. The GUI diagnostics panel (#904) renders these grouped
/// by section. Kept as pure data so it is trivially unit-testable and identical
/// across macOS and iOS.
public struct HealthFinding: Identifiable, Hashable, Sendable {
    public enum Level: String, Sendable, Comparable {
        case fail, warn, ok
        // Sort failures first, then warnings, then healthy lines.
        public static func < (a: Level, b: Level) -> Bool { a.rank < b.rank }
        private var rank: Int { switch self { case .fail: 0; case .warn: 1; case .ok: 2 } }
    }

    public let level: Level
    public let section: String
    public let message: String
    public let hint: String?

    public var id: String { "\(section)|\(level.rawValue)|\(message)" }

    public init(level: Level, section: String, message: String, hint: String? = nil) {
        self.level = level
        self.section = section
        self.message = message
        self.hint = hint
    }
}

/// Derives the doctor-style findings the GUI shows from a daemon diagnostics
/// snapshot. This is the subset of `gr doctor` computable purely from the
/// `DiagnosticsMsg` wire payload — the daemon-, session-, and storage-level
/// checks. Host-filesystem checks that only the CLI can run locally (sandbox
/// grants, human-token mode, config-key typos) are out of scope here.
public enum HealthReport {
    /// All findings, most severe first, in a stable section order.
    public static func findings(from diag: DiagnosticsMsg) -> [HealthFinding] {
        daemon(diag) + sessions(diag) + storage(diag)
    }

    /// True if any finding is a hard failure — drives the panel's summary badge.
    public static func hasFailures(_ findings: [HealthFinding]) -> Bool {
        findings.contains { $0.level == .fail }
    }

    // MARK: - Sections

    private static func daemon(_ diag: DiagnosticsMsg) -> [HealthFinding] {
        var out: [HealthFinding] = []
        let version = diag.daemonVersion.map { " · \($0)" } ?? ""
        out.append(.init(level: .ok, section: "Daemon",
                         message: "Running (PID \(diag.daemonPID), uptime \(diag.daemonUptime))\(version)"))
        return out
    }

    private static func sessions(_ diag: DiagnosticsMsg) -> [HealthFinding] {
        var out: [HealthFinding] = []
        for s in diag.sessions {
            let who = "\(s.name) (\(s.id))"

            if s.status == "running", let pid = s.pid, pid > 0, !s.pidAlive {
                out.append(.init(level: .fail, section: "Sessions",
                                 message: "\(who): PID \(pid) not alive but status is running",
                                 hint: "Restart the daemon"))
            }

            if s.status == "running", let pid = s.pid, pid > 0, s.pidAlive, s.hasPTY == false {
                out.append(.init(level: .fail, section: "Sessions",
                                 message: "\(who): PID \(pid) alive but not managed by the daemon (orphaned after a crash)",
                                 hint: "Stop the session to reap the orphaned process group"))
            }

            if s.status == "errored", let pid = s.pid, pid > 0 {
                out.append(.init(level: .warn, section: "Sessions",
                                 message: "\(who): errored with PID \(pid) still recorded — may need manual cleanup"))
            }

            if let wt = s.worktreePath, !wt.isEmpty, !s.worktreeExists {
                out.append(.init(level: .fail, section: "Sessions",
                                 message: "\(who): worktree path does not exist",
                                 hint: "Delete the session"))
            }

            if s.configStale {
                out.append(.init(level: .warn, section: "Sessions",
                                 message: "\(who): config has drifted since creation",
                                 hint: "Restart the session to pick up new config"))
            }

            if s.saturated {
                out.append(.init(level: .warn, section: "Sessions",
                                 message: "\(who): scrollback saturated (\(formatBytes(s.scrollbackMax)))"))
            }

            if !s.hasToken {
                out.append(.init(level: .warn, section: "Sessions",
                                 message: "\(who): missing auth token — may need a restart to receive one",
                                 hint: "Restart the session"))
            }
        }

        if out.isEmpty {
            out.append(.init(level: .ok, section: "Sessions",
                             message: "No issues found across \(diag.sessions.count) session(s)"))
        }
        return out
    }

    private static func storage(_ diag: DiagnosticsMsg) -> [HealthFinding] {
        var out: [HealthFinding] = []
        let sb = diag.scrollback
        if sb.saturatedCount > 0 {
            out.append(.init(level: .warn, section: "Storage",
                             message: "Scrollback: \(sb.totalFiles) files, \(formatBytes(sb.totalBytes)) total (\(sb.saturatedCount) saturated)"))
        } else {
            out.append(.init(level: .ok, section: "Storage",
                             message: "Scrollback: \(sb.totalFiles) files, \(formatBytes(sb.totalBytes)) total"))
        }
        out.append(.init(level: .ok, section: "Storage",
                         message: "Messages: \(diag.messages.totalStreams) streams, \(diag.messages.totalMessages) messages"))
        return out
    }

    // MARK: - Helpers

    /// Human-readable byte size, matching the CLI's `formatBytes`.
    static func formatBytes(_ b: Int) -> String {
        let unit = 1024
        if b >= unit * unit * unit { return String(format: "%.1f GB", Double(b) / Double(unit * unit * unit)) }
        if b >= unit * unit { return String(format: "%.1f MB", Double(b) / Double(unit * unit)) }
        if b >= unit { return String(format: "%.1f KB", Double(b) / Double(unit)) }
        return "\(b) B"
    }
}
