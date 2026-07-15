import Testing
import Foundation
import GraithProtocol
@testable import GraithSessionKit

// Config viewer + diagnostics panel (#904): the shared HealthReport derivation
// and the FleetModel fetch paths both apps bind to.

@Suite("HealthReport findings")
struct HealthReportTests {
    /// A helper to build a diagnostic for one session with overridable health flags.
    private func diag(_ session: SessionDiagnostic,
                      scrollback: ScrollbackDiagnostic = .init(totalFiles: 1, totalBytes: 2048, saturatedCount: 0)) -> DiagnosticsMsg {
        DiagnosticsMsg(daemonPID: 7, daemonVersion: "1.2.3", daemonUptime: "5m",
                       fleet: FleetSummary(total: 1), sessions: [session],
                       scrollback: scrollback,
                       messages: MessagesDiagnostic(totalStreams: 1, totalMessages: 4))
    }

    private func session(status: String = "running", pid: Int? = 100, pidAlive: Bool = true,
                         hasPTY: Bool? = true, worktreePath: String? = "/glen/bothy",
                         worktreeExists: Bool = true, configStale: Bool = false,
                         saturated: Bool = false, hasToken: Bool = true) -> SessionDiagnostic {
        SessionDiagnostic(id: "braw01", name: "braw", status: status, agentStatus: "active",
                          pid: pid, pidAlive: pidAlive, hasPTY: hasPTY,
                          worktreePath: worktreePath, worktreeExists: worktreeExists,
                          configStale: configStale, hookStale: false,
                          scrollbackBytes: 10, scrollbackMax: 5_000_000, saturated: saturated, hasToken: hasToken)
    }

    @Test func healthySessionYieldsNoFailures() {
        let findings = HealthReport.findings(from: diag(session()))
        #expect(!HealthReport.hasFailures(findings))
        // Daemon line is always present.
        #expect(findings.contains { $0.section == "Daemon" && $0.message.contains("PID 7") })
        // A clean fleet reports "No issues found".
        #expect(findings.contains { $0.section == "Sessions" && $0.message.contains("No issues found") })
    }

    @Test func deadPIDIsAFailure() {
        let findings = HealthReport.findings(from: diag(session(pidAlive: false)))
        #expect(HealthReport.hasFailures(findings))
        #expect(findings.contains { $0.level == .fail && $0.message.contains("not alive") })
    }

    @Test func orphanedProcessIsAFailure() {
        let findings = HealthReport.findings(from: diag(session(hasPTY: false)))
        #expect(findings.contains { $0.level == .fail && $0.message.contains("orphaned") })
    }

    @Test func missingWorktreeIsAFailure() {
        let findings = HealthReport.findings(from: diag(session(worktreeExists: false)))
        #expect(findings.contains { $0.level == .fail && $0.message.contains("worktree") })
    }

    @Test func staleConfigAndMissingTokenAreWarnings() {
        let findings = HealthReport.findings(from: diag(session(configStale: true, hasToken: false)))
        #expect(!HealthReport.hasFailures(findings))
        #expect(findings.contains { $0.level == .warn && $0.message.contains("drifted") })
        #expect(findings.contains { $0.level == .warn && $0.message.contains("auth token") })
    }

    @Test func saturatedScrollbackSurfacesInStorage() {
        let d = diag(session(),
                     scrollback: .init(totalFiles: 3, totalBytes: 9_000_000, saturatedCount: 2))
        let findings = HealthReport.findings(from: d)
        #expect(findings.contains { $0.section == "Storage" && $0.message.contains("saturated") })
    }

    @Test func findingsSortFailuresFirst() {
        let findings = HealthReport.findings(from: diag(session(pidAlive: false)))
            .sorted { ($0.level, $0.section) < ($1.level, $1.section) }
        #expect(findings.first?.level == .fail)
    }

    @Test func formatBytesMatchesUnits() {
        #expect(HealthReport.formatBytes(512) == "512 B")
        #expect(HealthReport.formatBytes(2048) == "2.0 KB")
        #expect(HealthReport.formatBytes(5 * 1024 * 1024) == "5.0 MB")
    }
}

@Suite("FleetModel host introspection")
struct FleetIntrospectionTests {
    @MainActor @Test func configFetchesFromOwningHost() async throws {
        let (fleet, _) = makeFleetWithRemote()
        await fleet.connectAll()
        let resp = try await fleet.config(hostID: "ben")
        #expect(!resp.effectiveTOML.isEmpty)
        #expect(resp.configExists)
    }

    @MainActor @Test func diagnosticsFetchesFromOwningHost() async throws {
        let (fleet, _) = makeFleetWithRemote(sessions: [makeSession(id: "s1", name: "braw")])
        await fleet.connectAll()
        let diag = try await fleet.diagnostics(hostID: "ben")
        #expect(diag.sessions.count == 1)
        #expect(diag.daemonPID > 0)
    }

    @MainActor @Test func unknownHostThrows() async {
        let fleet = makeEmptyFleet()
        await #expect(throws: (any Error).self) {
            _ = try await fleet.config(hostID: "nae-sic-host")
        }
    }

    @MainActor @Test func configFetchErrorPropagates() async throws {
        let (fleet, mock) = makeFleetWithRemote()
        await fleet.connectAll()
        await mock.setFailConfig(.daemon("thrawn daemon"))
        await #expect(throws: (any Error).self) {
            _ = try await fleet.config(hostID: "ben")
        }
    }
}
