import Foundation
import CryptoKit
import GraithClientAPI
import GraithMobileKit
import GraithMobileMock
import GraithMobileUI
import GraithTerminalUIKit
import GraithMobileReal
import GraithProtocol

// A runnable smoke check for the SDK-neutral logic (see Package.swift). Mirrors
// the XCTest suites so we can actually execute the logic on the CLT toolchain,
// which lacks XCTest. Exits non-zero on the first failure.

var failures = 0
func check(_ cond: Bool, _ label: String) {
    if cond {
        print("  ok  — \(label)")
    } else {
        failures += 1
        print("  FAIL — \(label)")
    }
}

func section(_ name: String) { print("\n▸ \(name)") }

// MARK: - Device identity (ed25519 PoP)

func testDeviceIdentity() throws {
    section("DeviceIdentity — ed25519")
    let secrets = InMemorySecretStore()
    let id = try DeviceIdentity(keychain: secrets)
    let pub = try id.publicKeyRaw()
    check(pub.count == 32, "public key is 32 raw bytes")

    let nonce = Data("haar-nonce".utf8)
    let sig = try id.sign(nonce)
    check(sig.count == 64, "signature is 64 raw bytes")

    let key = try Curve25519.Signing.PublicKey(rawRepresentation: pub)
    check(key.isValidSignature(sig, for: nonce), "signature verifies over the nonce")
    check(!key.isValidSignature(sig, for: Data("thrawn".utf8)), "signature rejects a different message")

    // Proof-of-possession binds the nonce to the TLS channel's SPKI (issue #886).
    let proof = try id.proof(forNonce: "haar-nonce", channelBinding: "bide-spki-pin")
    let proofSig = Data(base64Encoded: proof.signature)!
    let bound = Data("graith-pop-v1:haar-nonce:bide-spki-pin".utf8)
    check(key.isValidSignature(proofSig, for: bound), "proof verifies over the channel-bound input")
    check(!key.isValidSignature(proofSig, for: Data("haar-nonce".utf8)),
          "proof does NOT verify as a bare nonce (defeats MITM relay)")
    check(!key.isValidSignature(proofSig, for: Data("graith-pop-v1:haar-nonce:thrawn-mitm-pin".utf8)),
          "proof does NOT verify bound to a different SPKI")

    // Key is stable across instances backed by the same store.
    let id2 = try DeviceIdentity(keychain: secrets)
    check(try id2.publicKeyRaw() == pub, "key is stable across instances")

    try id.setDeviceID("dev-skelf-1")
    check(try DeviceIdentity(keychain: secrets).deviceID == "dev-skelf-1", "device id persists")
    try id.reset()
    check(try DeviceIdentity(keychain: secrets).deviceID == "", "reset clears device id")
}

// MARK: - Host registry

@MainActor
func testHostRegistry() throws {
    section("HostRegistry — persistence + credentials")
    let secrets = InMemorySecretStore()
    let url = FileManager.default.temporaryDirectory
        .appendingPathComponent("graith-smoke-\(UUID().uuidString)")
        .appendingPathComponent("hosts.json")
    let registry = HostRegistry(keychain: secrets, storeURL: url)

    registry.upsert(HostEntry(id: "ben", label: "ben", magicDNSName: "graith-ben.ts.net"))
    check(registry.hosts.count == 1, "upsert adds a host")
    check(registry.credentials(for: registry.host(id: "ben")!) == nil, "no creds before pairing")

    try registry.completePairing(hostID: "ben", response:
        PairResponse(deviceID: "dev-bairn", clientToken: "tok-canny",
                     daemonProfile: "default", tlsPinSPKI: "cGlu"))
    let creds = registry.credentials(for: registry.host(id: "ben")!)
    check(creds?.clientToken == "tok-canny", "token retrieved from secret store")
    check(creds?.deviceID == "dev-bairn", "per-host device id from the entry")
    check(registry.host(id: "ben")?.isPaired == true, "host marked paired")

    // Reload from disk.
    let reloaded = HostRegistry(keychain: secrets, storeURL: url)
    check(reloaded.host(id: "ben")?.magicDNSName == "graith-ben.ts.net", "entry reloads from disk")

    registry.remove(hostID: "ben")
    check(registry.hosts.isEmpty, "remove drops the host")
    check((try? secrets.string(for: "host.ben.clientToken")) == nil, "remove wipes the token")
}

// MARK: - Pairing coordinator

@MainActor
func testPairing() async throws {
    section("PairingCoordinator")
    let secrets = InMemorySecretStore()
    let identity = try DeviceIdentity(keychain: secrets)
    let registry = HostRegistry(keychain: secrets, storeURL:
        FileManager.default.temporaryDirectory
            .appendingPathComponent("smoke-pair-\(UUID().uuidString)/hosts.json"))

    let ok = PairingCoordinator(pairing: MockPairing(), identity: identity, registry: registry)
    await ok.pair(hostID: "brae", label: "brae", magicDNSName: "graith-brae.ts.net", deviceLabel: "bairn phone")
    // Daemon replied but nothing is trusted until the fingerprint is confirmed.
    if case .awaitingConfirmation = ok.phase { check(true, "pairing pauses for fingerprint confirmation") }
    else { check(false, "pairing pauses for fingerprint confirmation") }
    check(ok.spkiFingerprint != nil, "SPKI fingerprint surfaced for TOFU")
    check(registry.host(id: "brae")?.isPaired != true, "not marked paired before confirmation")
    // Confirming persists trust.
    ok.confirmPairing()
    if case .paired = ok.phase { check(true, "confirming reaches .paired") }
    else { check(false, "confirming reaches .paired") }
    // Device ID is recorded per host on the entry, not on the shared identity (F4).
    check(registry.host(id: "brae")?.deviceID == "dev-bairn-001", "host records the assigned device id")

    let bad = PairingCoordinator(
        pairing: MockPairing(failure: .authenticationFailed("thrawn identity")),
        identity: try DeviceIdentity(keychain: InMemorySecretStore()), registry: registry)
    await bad.pair(hostID: "dreich", label: "dreich", magicDNSName: "graith-dreich.ts.net", deviceLabel: "scunner")
    if case .failed(let m) = bad.phase { check(m.contains("thrawn identity"), "failed pairing surfaces the error") }
    else { check(false, "failed pairing surfaces the error") }
}

// MARK: - Mock host client

func testMockClient() async throws {
    section("MockHostClient — read / create / approvals")
    let client = MockHostClient()
    try await client.connect()
    let sessions = try await client.listSessions()
    check(sessions.count == 3, "lists seeded sessions")
    check(sessions.contains { $0.needsApproval }, "a session needs approval")

    let repos = try await client.repoList()
    check(repos.contains { $0.recent }, "repo_list marks a recent repo")

    try await client.create(CreateRequest(name: "bonnie", agent: "claude", repoPath: "/Users/x/Code/croft"))
    check(try await client.listSessions().count == 4, "create adds a session")

    // Approval stream yields the pending set and clears on respond.
    let stream = await client.approvalStream()
    var iterator = stream.makeAsyncIterator()
    let first = await iterator.next()
    check(first?.count == 1, "approval stream yields one pending")
    try await client.respondApproval(requestID: "req-canny-1", decision: .deny, reason: "no")
    let second = await iterator.next()
    check(second?.isEmpty == true, "respond clears the pending approval")
}

// MARK: - AppModel multi-host aggregation (Task 19)

@MainActor
func testAppModel() async throws {
    section("AppModel — multi-host aggregation")
    let secrets = InMemorySecretStore()
    let identity = try DeviceIdentity(keychain: secrets)
    let registry = HostRegistry(keychain: secrets, storeURL:
        FileManager.default.temporaryDirectory
            .appendingPathComponent("smoke-app-\(UUID().uuidString)/hosts.json"))

    // Two paired hosts: ben + brae. Each records its own daemon-assigned device
    // ID on its entry (F4) — pairing brae must not clobber ben's.
    for id in ["ben", "brae"] {
        registry.upsert(HostEntry(id: id, label: id, magicDNSName: "graith-\(id).ts.net"))
        try registry.completePairing(hostID: id, response:
            PairResponse(deviceID: "dev-multi", clientToken: "tok-\(id)",
                         daemonProfile: "default", tlsPinSPKI: "cGlu"))
    }

    let model = AppModel(
        registry: registry, identity: identity, reachability: TailnetReachability(),
        factory: MockClientFactory(), pairingBackend: MockPairing())
    check(model.connections.count == 2, "one connection per paired host")

    await model.connectAll()
    check(model.allSessions.count == 6, "aggregates 3 sessions x 2 hosts")
    // Approvals arrive asynchronously over the subscription stream; give the
    // subscription tasks a moment to deliver their first yield.
    for _ in 0..<50 where model.totalPendingApprovals < 2 {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
    check(model.totalPendingApprovals == 2, "aggregates 1 approval x 2 hosts")
    check(model.allApprovals.allSatisfy { !$0.host.label.isEmpty }, "approvals tagged with host")

    // Selection resolves to the right host connection.
    let ref = SessionRef(hostID: "ben", sessionID: "braw0001")
    check(model.connection(for: ref)?.id == "ben", "selection resolves host connection")

    await model.removeHost(registry.host(id: "brae")!)
    check(model.connections.count == 1, "removeHost drops the connection")
}

// MARK: - Attach view-model + single-attach guard (Task 20)

@MainActor
func testAttach() async throws {
    section("TerminalAttachViewModel + AttachRegistry (Task 20)")
    let client = MockHostClient()
    try await client.connect()
    let registry = AttachRegistry()
    let core = MockTerminalCore()

    let vm = TerminalAttachViewModel(hostID: "ben", sessionID: "braw0001",
                                     core: core, client: client, registry: registry)
    await vm.attach()
    check(vm.phase == .attached, "attach reaches .attached")
    check(registry.isAttachedElsewhere(host: "ben", session: "braw0001"), "registry claims the slot")

    // Single-attach guard: a second VM for the same session is refused.
    let vm2 = TerminalAttachViewModel(hostID: "ben", sessionID: "braw0001",
                                      core: core, client: client, registry: registry)
    await vm2.attach()
    check(vm2.phase == .attachedElsewhere, "second attach to same session refused")

    // Input round-trips: MockAttachSession echoes sent bytes to output, which
    // the VM feeds into the core.
    vm.send(text: "hi")
    for _ in 0..<50 where !core.fedOutput.contains(Data("hi".utf8)) {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
    check(core.fedOutput.contains(Data("hi".utf8)), "typed text round-trips into the core")

    // Resize updates the core geometry.
    vm.resize(cols: 100, rows: 40, cellWidth: 8, cellHeight: 16)
    check(core.lastResize?.cols == 100 && core.lastResize?.rows == 40, "resize propagates to core")

    // Detach releases the slot so a fresh attach can claim it.
    await vm.detach()
    check(!registry.isAttachedElsewhere(host: "ben", session: "braw0001"), "detach releases the slot")
    await vm2.detach()
    let vm3 = TerminalAttachViewModel(hostID: "ben", sessionID: "braw0001",
                                      core: MockTerminalCore(), client: client, registry: registry)
    await vm3.attach()
    check(vm3.phase == .attached, "reattach after release succeeds")
    await vm3.detach()
}

// MARK: - Real adapters (GraithMobileReal): wire-model mapping + factory

func testRealAdapters() async throws {
    section("GraithMobileReal — shared→boundary mapping + factory")
    let decoder = JSONDecoder()

    // Decode a shared wire SessionInfo and map it to the boundary type. This
    // exercises the real decode path + the 1:1 field mapping in one shot.
    let sessionJSON = """
    {"id":"braw0001","name":"braw","repo_path":"/Users/x/Code/croft","repo_name":"croft",
     "worktree_path":"/wt","branch":"user/graith/braw-braw0001","base_branch":"main",
     "agent":"claude","status":"running","agent_status":"active","created_at":"2026-07-08T07:00:00Z",
     "pull_request":{"number":7,"state":"open"},"ci":{"state":"passing"}}
    """
    let shared = try decoder.decode(GraithProtocol.SessionInfo.self, from: Data(sessionJSON.utf8))
    let mapped = GraithClientAPI.SessionInfo(shared)
    check(mapped.id == "braw0001", "SessionInfo id maps")
    check(mapped.isRunning, "SessionInfo status maps (running)")
    check(!mapped.needsApproval, "SessionInfo agentStatus maps (active ⇒ no approval)")
    check(mapped.shortBranch == "braw-braw0001", "SessionInfo shortBranch derives")
    check(mapped.pullRequest?.number == 7, "nested PRInfo maps")
    check(mapped.ci?.state == "passing", "nested CIInfo maps")

    // Map a pair response.
    let pairJSON = #"{"device_id":"dev-bairn","client_token":"tok-canny","daemon_profile":"default","tls_pin_spki":"cGlu"}"#
    let pairMsg = try decoder.decode(GraithProtocol.PairResponseMsg.self, from: Data(pairJSON.utf8))
    let pairResp = GraithClientAPI.PairResponse(pairMsg)
    check(pairResp.clientToken == "tok-canny", "PairResponse maps client token")

    // The real factory builds a (disconnected) client without touching the network.
    let factory = RealHostClientFactory()
    let client = factory.makeClient(
        transport: .remote(host: "graith-ben.ts.net", port: 4823, tlsPinSPKI: nil),
        credentials: HostCredentials(clientToken: "tok-canny", deviceID: "dev-bairn",
                                     daemonProfile: "default", tlsPinSPKI: "cGlu"),
        signer: MockDeviceSigner(deviceID: "dev-bairn"))
    let connected = await client.isConnected
    check(!connected, "real host client starts disconnected")
}

// MARK: - Space-drag → arrow keys (issue #979)

func testSpaceDrag() {
    section("SpaceDragTracker — space drag → arrow keys (#979)")

    // A plain tap (no movement) emits nothing and doesn't commit ⇒ space is sent.
    var tap = SpaceDragTracker(threshold: 22)
    tap.begin()
    check(tap.update(translation: .zero).isEmpty, "no translation emits no arrow")
    check(!tap.didEmit, "a stationary tap does not commit (space is typed)")

    // A short drag past the threshold emits exactly one arrow in that direction
    // and commits (space suppressed) — the "single-tap drag" case.
    var right = SpaceDragTracker(threshold: 22)
    right.begin()
    check(right.update(translation: CGPoint(x: 25, y: 0)) == [.arrowRight], "right drag ⇒ one arrowRight")
    check(right.didEmit, "an emitted arrow commits the drag (suppresses space)")

    // Each axis maps to the expected arrow. Y grows downward on screen.
    var up = SpaceDragTracker(threshold: 22); up.begin()
    check(up.update(translation: CGPoint(x: 0, y: -30)) == [.arrowUp], "up drag ⇒ arrowUp")
    var down = SpaceDragTracker(threshold: 22); down.begin()
    check(down.update(translation: CGPoint(x: 0, y: 30)) == [.arrowDown], "down drag ⇒ arrowDown")
    var left = SpaceDragTracker(threshold: 22); left.begin()
    check(left.update(translation: CGPoint(x: -30, y: 0)) == [.arrowLeft], "left drag ⇒ arrowLeft")

    // A long continuous drag emits repeated arrows — one per threshold of travel.
    var run = SpaceDragTracker(threshold: 20); run.begin()
    check(run.update(translation: CGPoint(x: 65, y: 0)) == [.arrowRight, .arrowRight, .arrowRight],
          "65pt / 20pt threshold ⇒ three arrowRight")

    // Incremental updates accumulate: the second call only emits newly-crossed
    // thresholds, not the whole distance again.
    var incr = SpaceDragTracker(threshold: 20); incr.begin()
    check(incr.update(translation: CGPoint(x: 25, y: 0)) == [.arrowRight], "first 25pt ⇒ one arrowRight")
    check(incr.update(translation: CGPoint(x: 45, y: 0)) == [.arrowRight], "next 20pt ⇒ one more arrowRight")
    check(incr.update(translation: CGPoint(x: 50, y: 0)).isEmpty, "sub-threshold advance emits nothing")

    // Dominant axis wins per step: a mostly-vertical drag reads as up/down.
    var diag = SpaceDragTracker(threshold: 20); diag.begin()
    check(diag.update(translation: CGPoint(x: 5, y: 30)) == [.arrowDown], "vertical-dominant drag ⇒ arrowDown")

    // begin() resets committed state so the tracker is reusable across drags.
    var reused = SpaceDragTracker(threshold: 20); reused.begin()
    _ = reused.update(translation: CGPoint(x: 40, y: 0))
    check(reused.didEmit, "committed after a drag")
    reused.begin()
    check(!reused.didEmit, "begin() clears the committed flag")
}

// MARK: - Frame codec (channel byte + BE length) — mirrors frame.go

func testFrameCodec() {
    section("FrameCodec — mirrors internal/protocol/frame.go")
    let payload = Data("blether".utf8)
    let frame = GraithFrame(channel: 0x00, payload: payload)
    let encoded = frame.encoded()
    check(encoded.count == 5 + payload.count, "header is 5 bytes")
    check(encoded[0] == 0x00, "channel byte first")
    check(Int(encoded[1]) << 24 | Int(encoded[2]) << 16 | Int(encoded[3]) << 8 | Int(encoded[4]) == payload.count,
          "big-endian uint32 length")

    var buf = encoded
    let decoded = GraithFrame.decode(from: &buf)
    check(decoded?.channel == 0x00 && decoded?.payload == payload, "round-trips through decode")
    check(buf.isEmpty, "decode consumes exactly one frame")
}

// MARK: - Entry point

@main
struct Smoke {
    static func main() async {
        print("graith-mobile smoke check")
        do {
            try testDeviceIdentity()
            try await MainActor.run { try testHostRegistry() }
            try await testPairing()
            try await testMockClient()
            try await testAppModel()
            try await testAttach()
            try await testRealAdapters()
            testSpaceDrag()
            testFrameCodec()
        } catch {
            print("EXCEPTION: \(error)")
            exit(2)
        }
        print("\n\(failures == 0 ? "ALL PASS" : "\(failures) FAILURE(S)")")
        exit(failures == 0 ? 0 : 1)
    }
}
