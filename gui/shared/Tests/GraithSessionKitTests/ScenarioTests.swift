import Testing
import Foundation
import GraithProtocol
import GraithRemoteKit
@testable import GraithSessionKit

// Shared-layer coverage for the scenario surface (#903): the wire model decodes,
// HostConnection fetches scenarios on refresh, FleetModel aggregates + resolves
// members + routes the lifecycle actions to the owning host.

private func makeScenario(
    name: String = "strath",
    status: String = "running",
    sessions: [ScenarioSessionInfo] = [
        ScenarioSessionInfo(name: "braw", sessionID: "braw0001", role: "Backend",
                            task: "ingest", taskDone: false, repo: "croft", agent: "claude", status: "running"),
        ScenarioSessionInfo(name: "bide", sessionID: "bide0003", role: "Reviewer",
                            task: "review", taskDone: true, repo: "glen", agent: "claude", status: "stopped"),
    ]
) -> ScenarioRecord {
    ScenarioRecord(
        id: "sc-\(name)", name: name, orchestratorID: "orch0001",
        goal: "Wire end-to-end tracing", status: status,
        sessionIDs: sessions.map(\.sessionID), sessions: sessions,
        createdAt: "2026-07-14T09:00:00Z")
}

@Suite("ScenarioRecord decoding")
struct ScenarioDecodingTests {
    @Test func decodesDaemonShape() throws {
        // The exact JSON the daemon's ScenarioListResponse emits.
        let json = """
        {"scenarios":[{"id":"sc-abc","name":"strath","orchestrator_id":"orch0001",
        "goal":"g","status":"running","session_ids":["s1"],
        "sessions":[{"name":"braw","session_id":"s1","role":"Backend","task":"t",
        "task_done":true,"repo":"croft","agent":"claude","model":"opus","status":"running","shared":true}],
        "created_at":"2026-07-14T09:00:00Z"}]}
        """
        let resp = try JSONDecoder().decode(ScenarioListResponse.self, from: Data(json.utf8))
        #expect(resp.scenarios.count == 1)
        let sc = resp.scenarios[0]
        #expect(sc.name == "strath")
        #expect(sc.orchestratorID == "orch0001")
        #expect(sc.sessionIDs == ["s1"])
        let member = sc.sessions[0]
        #expect(member.sessionID == "s1")
        #expect(member.role == "Backend")
        #expect(member.taskDone == true)
        #expect(member.shared == true)
    }

    @Test func decodesWithOptionalMemberFieldsOmitted() throws {
        // Only the two required member fields present — the omitempty ones must
        // decode to nil, not fail.
        let json = """
        {"scenarios":[{"id":"sc-a","name":"n","orchestrator_id":"o","goal":"",
        "status":"running","session_ids":[],"sessions":[{"name":"braw","session_id":"s1"}],
        "created_at":""}]}
        """
        let resp = try JSONDecoder().decode(ScenarioListResponse.self, from: Data(json.utf8))
        let member = resp.scenarios[0].sessions[0]
        #expect(member.role == nil)
        #expect(member.taskDone == nil)
        #expect(member.shared == nil)
    }
}

@Suite("FleetModel — scenarios")
@MainActor
struct ScenarioFleetTests {
    private func sampleSessions() -> [SessionInfo] {
        [
            makeSession(id: "braw0001", name: "braw", status: "running", repoName: "croft"),
            makeSession(id: "bide0003", name: "bide", status: "stopped", repoName: "glen"),
        ]
    }

    @Test func refreshFetchesScenarios() async {
        let (fleet, _) = makeFleetWithRemote(
            sessions: sampleSessions(), scenarios: [makeScenario()], subscribeApprovals: false)
        await fleet.connectAll()
        #expect(fleet.connections.first?.scenarios.count == 1)
        #expect(fleet.hostedScenarios.count == 1)
        #expect(fleet.hostedScenarios.first?.host.id == "ben")
        #expect(fleet.scenarios.first?.name == "strath")
    }

    @Test func scenariosSortedByName() async {
        let (fleet, _) = makeFleetWithRemote(
            sessions: sampleSessions(),
            scenarios: [makeScenario(name: "wynd"), makeScenario(name: "brae")],
            subscribeApprovals: false)
        await fleet.connectAll()
        #expect(fleet.hostedScenarios.map(\.scenario.name) == ["brae", "wynd"])
    }

    @Test func resolvesLiveMembers() async {
        let (fleet, _) = makeFleetWithRemote(
            sessions: sampleSessions(), scenarios: [makeScenario()], subscribeApprovals: false)
        await fleet.connectAll()
        let scenario = fleet.hostedScenarios[0]
        // Both members exist in the live session list, in declared order.
        #expect(fleet.sessions(in: scenario).map(\.id) == ["braw0001", "bide0003"])
    }

    @Test func resolvesOnlyLiveMembers() async {
        // A scenario referencing a session that isn't in the live list (e.g.
        // soft-deleted) resolves to just the live subset.
        let scenario = makeScenario(sessions: [
            ScenarioSessionInfo(name: "braw", sessionID: "braw0001"),
            ScenarioSessionInfo(name: "ghost", sessionID: "missing99"),
        ])
        let (fleet, _) = makeFleetWithRemote(
            sessions: sampleSessions(), scenarios: [scenario], subscribeApprovals: false)
        await fleet.connectAll()
        #expect(fleet.sessions(in: fleet.hostedScenarios[0]).map(\.id) == ["braw0001"])
    }

    // The HostConnection-level actions are awaited, so these prove the RPC fires
    // with the right name deterministically (no timing guesses).
    @Test func connectionStopSendsNamedRPC() async {
        let (fleet, mock) = makeFleetWithRemote(
            sessions: sampleSessions(), scenarios: [makeScenario()], subscribeApprovals: false)
        await fleet.connectAll()
        await fleet.connections[0].stopScenario("strath")
        let op = await mock.lastScenarioOp
        #expect(op?.op == "stop")
        #expect(op?.name == "strath")
    }

    @Test func connectionResumeSendsNamedRPC() async {
        let (fleet, mock) = makeFleetWithRemote(
            sessions: sampleSessions(), scenarios: [makeScenario()], subscribeApprovals: false)
        await fleet.connectAll()
        await fleet.connections[0].resumeScenario("strath")
        let op = await mock.lastScenarioOp
        #expect(op?.op == "resume")
    }

    @Test func deleteRemovesScenarioAfterRefresh() async {
        let (fleet, _) = makeFleetWithRemote(
            sessions: sampleSessions(), scenarios: [makeScenario()], subscribeApprovals: false)
        await fleet.connectAll()
        #expect(fleet.hostedScenarios.count == 1)
        await fleet.connections[0].deleteScenario("strath")
        #expect(fleet.hostedScenarios.isEmpty)
    }

    // FleetModel routing: with two hosts each owning a same-named scenario, the
    // action must reach ONLY the owning host. A single-host test can't prove
    // this (routing to the sole connection would pass even if host were ignored).
    @Test func fleetActionRoutesToOwningHostOnly() async {
        let (fleet, ben, canny) = makeTwoHostFleet(
            benScenarios: [makeScenario(name: "strath")],
            cannyScenarios: [makeScenario(name: "strath")])
        await fleet.connectAll()
        // Two hosts, each with a scenario named "strath".
        let cannyScenario = fleet.hostedScenarios.first { $0.host.id == "canny" }!
        fleet.stopScenario(cannyScenario)
        #expect(await waitUntil { await canny.lastScenarioOp != nil })
        let benOp = await ben.lastScenarioOp
        let cannyOp = await canny.lastScenarioOp
        #expect(cannyOp?.op == "stop")
        #expect(benOp == nil, "the action must not reach the non-owning host")
    }

    @Test func fleetActionOnUnknownHostIsNoOp() async {
        let (fleet, ben, canny) = makeTwoHostFleet(
            benScenarios: [makeScenario()], cannyScenarios: [])
        await fleet.connectAll()
        fleet.stopScenario(name: "strath", hostID: "no-such-host")
        // Yield generously; nothing should ever be recorded on either host.
        for _ in 0..<50 { await Task.yield() }
        #expect(await ben.lastScenarioOp == nil)
        #expect(await canny.lastScenarioOp == nil)
    }
}

/// Poll a condition across actor-hops without a fixed sleep: yields up to
/// `maxYields` times, returning true as soon as it holds. Deterministic under a
/// loaded runner (unlike `Task.sleep`), since it only advances when the executor
/// makes progress.
@MainActor
private func waitUntil(maxYields: Int = 500, _ cond: () async -> Bool) async -> Bool {
    for _ in 0..<maxYields {
        if await cond() { return true }
        await Task.yield()
    }
    return await cond()
}

/// A two-remote-host fleet ("ben" + "canny"), each backed by its own mock, so a
/// test can prove cross-host action routing.
@MainActor
private func makeTwoHostFleet(
    benScenarios: [ScenarioRecord] = [],
    cannyScenarios: [ScenarioRecord] = []
) -> (fleet: FleetModel, ben: MockHostClient, canny: MockHostClient) {
    let secrets = InMemorySecretStore()
    // swiftlint:disable:next force_try
    let identity = try! DeviceIdentity(keychain: secrets)
    let registry = HostRegistry(
        keychain: secrets,
        storeURL: FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-two-host-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json"))
    registry.upsert(Host(id: "ben", label: "Ben Nevis", kind: .remote, magicDNSName: "ben.tail", isPaired: false))
    registry.upsert(Host(id: "canny", label: "Canny", kind: .remote, magicDNSName: "canny.tail", isPaired: false))
    // swiftlint:disable:next force_try
    try! registry.completePairing(hostID: "ben", response: PairResponseMsg(
        deviceID: "dev-ben", clientToken: "tok-ben", daemonProfile: "", tlsPinSPKI: "cGlu"))
    // swiftlint:disable:next force_try
    try! registry.completePairing(hostID: "canny", response: PairResponseMsg(
        deviceID: "dev-canny", clientToken: "tok-canny", daemonProfile: "", tlsPinSPKI: "cGlu"))
    let ben = MockHostClient(scenarios: benScenarios)
    let canny = MockHostClient(scenarios: cannyScenarios)
    let factory = MockFactory(clients: ["tok-ben": ben, "tok-canny": canny])
    let pairing = PairingCoordinator(pairing: StubPairing(), identity: identity, registry: registry)
    let fleet = FleetModel(
        registry: registry, identity: identity, reachability: nil,
        factory: factory, pairing: pairing, subscribeApprovals: false)
    return (fleet, ben, canny)
}
