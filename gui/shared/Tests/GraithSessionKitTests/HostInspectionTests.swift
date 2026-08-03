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

// Agent catalog driven by daemon config (#1234): the New Session / Settings
// pickers bind to the daemon's catalog + default, never a hardcoded list.
@Suite("FleetModel agent catalog")
struct AgentCatalogTests {
    @MainActor @Test func catalogPopulatedOnConnectFromDaemon() async throws {
        let (fleet, mock) = makeFleetWithRemote()
        await mock.setAgentCatalog(AgentCatalogResponseMsg(
            agents: [
                AgentCatalogEntry(name: "croft", command: "croft-cli"),
                AgentCatalogEntry(name: "strath", command: "strath-cli"),
            ],
            defaultAgent: "strath"))
        await fleet.connectAll()

        guard case let .available(catalog) = fleet.agentCatalog(hostID: "ben") else {
            Issue.record("expected available agent catalog")
            return
        }
        #expect(catalog.names == ["croft", "strath"])
        #expect(catalog.defaultAgent == "strath")
        // A custom agent the old hardcoded list never had is offered.
        #expect(catalog.names.contains("croft"))
    }

    @MainActor @Test func resolvedDefaultMatchesConfiguredDefault() async throws {
        let (fleet, mock) = makeFleetWithRemote()
        await mock.setAgentCatalog(AgentCatalogResponseMsg(
            agents: [AgentCatalogEntry(name: "claude"), AgentCatalogEntry(name: "codex")],
            defaultAgent: "codex"))
        await fleet.connectAll()
        guard case let .available(catalog) = fleet.agentCatalog(hostID: "ben") else {
            Issue.record("expected available agent catalog")
            return
        }
        #expect(catalog.resolvedDefault == "codex")
    }

    @MainActor @Test func noFallbackBeforeAnyFetch() {
        let (fleet, _) = makeFleetWithRemote()
        guard case .loading = fleet.agentCatalog(hostID: "ben") else {
            Issue.record("expected a configured-but-unfetched host to be loading")
            return
        }

        guard case let .unavailable(reason) = fleet.agentCatalog(hostID: "nae-sic-host") else {
            Issue.record("expected an unknown host to be unavailable")
            return
        }
        #expect(reason.contains("host"))
    }

    @MainActor @Test func fetchFailureReturnsUnavailableWithoutFallback() async {
        let (fleet, mock) = makeFleetWithRemote()
        await fleet.connectAll()
        await mock.setFailAgentCatalog(.daemon("old daemon: no agent_catalog"))
        let state = await fleet.fetchAgentCatalog(hostID: "ben")
        guard case let .unavailable(reason) = state else {
            Issue.record("expected failed catalog refresh to be unavailable")
            return
        }
        #expect(reason.contains("agent_catalog"))
    }

    @MainActor @Test func fetchDuringConnectResolvesToAvailableCatalog() async {
        let client = MockHostClient()
        await client.setGateConnect(true)
        await client.setAgentCatalog(AgentCatalogResponseMsg(
            agents: [AgentCatalogEntry(name: "codex"), AgentCatalogEntry(name: "strath")],
            defaultAgent: "strath"))
        let conn = HostConnection(entry: Host(id: "ben", label: "Ben Nevis", kind: .remote,
                                              magicDNSName: "ben.tail", isPaired: true),
                                  client: client)

        let connectTask = Task { await conn.connect() }
        await Task.yield()
        #expect(conn.state == HostConnection.ConnectionState.connecting)

        let fetchTask = Task { await conn.fetchAgentCatalog() }
        await Task.yield()
        guard case .loading = conn.agentCatalogState else {
            Issue.record("expected catalog fetch to wait in loading while connect is in flight")
            await client.releaseConnect()
            await connectTask.value
            _ = await fetchTask.value
            return
        }

        await client.releaseConnect()
        await connectTask.value
        let state = await fetchTask.value
        guard case let .available(catalog) = state else {
            Issue.record("expected available catalog after connect completed")
            return
        }
        #expect(catalog.resolvedDefault == "strath")
    }

    @MainActor @Test func fetchFromIdleConnectsBeforeReturningCatalog() async {
        let client = MockHostClient()
        await client.setAgentCatalog(AgentCatalogResponseMsg(
            agents: [AgentCatalogEntry(name: "codex")],
            defaultAgent: "codex"))
        let conn = HostConnection(entry: Host(id: "ben", label: "Ben Nevis", kind: .remote,
                                              magicDNSName: "ben.tail", isPaired: true),
                                  client: client)

        let state = await conn.fetchAgentCatalog()

        #expect(conn.state == HostConnection.ConnectionState.connected)
        guard case let .available(catalog) = state else {
            Issue.record("expected idle fetch to connect and return the catalog")
            return
        }
        #expect(catalog.names == ["codex"])
    }

    @Test func resolvedDefaultFallsBackToFirstWhenDefaultMissing() {
        let catalog = AgentCatalogResponseMsg(
            agents: [AgentCatalogEntry(name: "bothy"), AgentCatalogEntry(name: "croft")],
            defaultAgent: "gone")
        // A misconfigured daemon whose default_agent isn't in the catalog still
        // yields a selectable value rather than an empty picker.
        #expect(catalog.resolvedDefault == "bothy")
    }

    @Test func resolvedDefaultForEmptyCatalogIsEmpty() {
        let catalog = AgentCatalogResponseMsg(agents: [], defaultAgent: "")
        #expect(catalog.resolvedDefault == "")
    }
}
