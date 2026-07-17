import Foundation
import Testing
@testable import GraithProtocol

struct ClientIntegrationTests {
    /// End-to-end over the in-memory transport: handshake -> PoP
    /// challenge/proof -> list. Mirrors the design's mock-server acceptance.
    @Test func remoteHandshakeProofAndList() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)
        let signer = TestSigner(deviceID: "bairn")
        let nonce = "whin-nonce-123"
        // The PoP proof binds the nonce to the pinned server SPKI (issue #886);
        // it must match the transport's tlsPinSPKI below.
        let pin = "bide-pin"

        let server = Task {
            let hs = try await daemon.readControl()
            #expect(hs.type == "handshake")
            let hsMsg = try decodePayload(hs, as: HandshakeMsg.self)
            #expect(hsMsg.terminalSize == [80, 24])
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))

            try await daemon.writeControl("auth_challenge", AuthChallengeMsg(nonce: nonce))
            let proofEnv = try await daemon.readControl()
            #expect(proofEnv.type == "auth_proof")
            let proof = try decodePayload(proofEnv, as: AuthProofMsg.self)
            #expect(proof.deviceID == "bairn")
            #expect(signer.verify(base64Signature: proof.signature, nonce: nonce, channelBinding: pin))
            // Bound to a MITM's different cert (SPKI), the same proof must NOT
            // verify — this is what defeats a relayed handshake (issue #886).
            #expect(!signer.verify(base64Signature: proof.signature, nonce: nonce, channelBinding: "thrawn-mitm-pin"))

            // A valid proof ⇒ the daemon replies auth_ok (handler.go). The client
            // blocks in completeProofOfPossession awaiting it before sending the
            // first RPC, so the mock must send it or the exchange deadlocks.
            try await daemon.writeControl("auth_ok", EmptyMsg())

            let listReq = try await daemon.readControl()
            #expect(listReq.type == "list")
            #expect(listReq.token == "client-tok")
            try await daemon.writeControl("session_list", SessionListMsg(sessions: [makeSession(id: "braw", name: "braw")]))
        }

        let conn = GraithConnection(
            // An authenticated remote connection (token present) must carry a
            // TLS pin — the transport guard refuses accept-any-cert otherwise.
            // The in-memory stream ignores the pin (there is no real TLS).
            transport: .remote(host: "ben", port: 4823, tlsPinSPKI: "bide-pin"),
            stream: clientStream,
            token: "client-tok"
        )
        let ok = try await conn.connect(
            handshake: HandshakeMsg(clientID: "1", terminalSize: [80, 24], cwd: "", profile: ""),
            signer: signer
        )
        #expect(ok.version == "1.0")

        let reply = try await conn.request("list")
        let list = try decodePayload(reply, as: SessionListMsg.self)
        #expect(list.sessions.map(\.name) == ["braw"])

        _ = await server.result
        await conn.close()
    }

    /// The high-level client, driven through its injectable stream factory.
    /// Local transport sends NO auth_challenge.
    @Test func protocolClientListLocal() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            let hs = try await daemon.readControl()
            #expect(hs.type == "handshake")
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            let listReq = try await daemon.readControl()
            #expect(listReq.type == "list")
            try await daemon.writeControl("session_list", SessionListMsg(sessions: [
                makeSession(id: "canny", name: "canny"),
                makeSession(id: "dreich", name: "dreich"),
            ]))
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        let sessions = try await client.list()
        #expect(sessions.map(\.name) == ["canny", "dreich"])

        _ = await server.result
        await client.close()
    }

    /// A daemon `error` reply surfaces as `ControlError.daemon`.
    @Test func errorReplyThrows() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl()
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            _ = try await daemon.readControl()
            try await daemon.writeControl("error", ErrorMsg(message: "session not found"))
        }

        let conn = GraithConnection(transport: .unix(path: "/tmp/x.sock"), stream: clientStream, token: nil)
        try await conn.connect(handshake: HandshakeMsg(clientID: "1", terminalSize: [80, 24], cwd: "", profile: ""))
        await #expect(throws: ControlError.self) {
            _ = try await conn.request("attach", payload: AttachMsg(sessionID: "thrawn"))
        }
        _ = await server.result
        await conn.close()
    }

    /// Attach: `attached` reply then scrollback bytes stream on channel 0x01;
    /// keystrokes flow back on channel 0x01.
    @Test func attachStreamsData() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task { () -> Data in
            _ = try await daemon.readControl()
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            let attach = try await daemon.readControl()
            #expect(attach.type == "attach")
            try await daemon.writeControl("attached", makeSession(id: "braw", name: "braw"))
            try await daemon.writeData(Data("hello bothy".utf8))
            return try await daemon.readData()
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/x.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        let attach = try await client.attach(sessionID: "braw", cols: 80, rows: 24)
        #expect(attach.session.name == "braw")

        var it = attach.output.makeAsyncIterator()
        let first = await it.next()
        #expect(first == Data("hello bothy".utf8))

        try await attach.send(Data("x".utf8))
        let key = try await server.value
        #expect(key == Data("x".utf8))
        await attach.close()
    }

    /// `star`/`unstar` send `{session_id}` and complete on the daemon's
    /// `starred`/`unstarred` acks (issue #899).
    @Test func starAndUnstarRoundTrip() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl() // handshake
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))

            let starReq = try await daemon.readControl()
            #expect(starReq.type == "star")
            #expect(try decodePayload(starReq, as: SessionIDMsg.self).sessionID == "braw")
            try await daemon.writeControl("starred", SessionIDMsg(sessionID: "braw"))

            let unstarReq = try await daemon.readControl()
            #expect(unstarReq.type == "unstar")
            #expect(try decodePayload(unstarReq, as: SessionIDMsg.self).sessionID == "braw")
            try await daemon.writeControl("unstarred", SessionIDMsg(sessionID: "braw"))
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        try await client.star(sessionID: "braw")
        try await client.unstar(sessionID: "braw")

        _ = await server.result
        await client.close()
    }

    /// `fork` sends `{name, source_session_id}` and returns the `created`
    /// session the daemon replies with (issue #899).
    @Test func forkReturnsCreatedSession() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl() // handshake
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            let forkReq = try await daemon.readControl()
            #expect(forkReq.type == "fork")
            let f = try decodePayload(forkReq, as: ForkMsg.self)
            #expect(f.name == "bairn")
            #expect(f.sourceSessionID == "braw")
            try await daemon.writeControl("created", makeSession(id: "bairn01", name: "bairn"))
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        let forked = try await client.fork(name: "bairn", sourceSessionID: "braw")
        #expect(forked.id == "bairn01")
        #expect(forked.name == "bairn")

        _ = await server.result
        await client.close()
    }

    /// `migrate` sends `{session_id, agent, model?}` and returns the `migrated`
    /// session (issue #899).
    @Test func migrateReturnsMigratedSession() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl() // handshake
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            let migReq = try await daemon.readControl()
            #expect(migReq.type == "migrate")
            let m = try decodePayload(migReq, as: MigrateMsg.self)
            #expect(m.sessionID == "canny")
            #expect(m.agent == "codex")
            #expect(m.model == "o3")
            try await daemon.writeControl("migrated", makeSession(id: "canny", name: "canny"))
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        let migrated = try await client.migrate(sessionID: "canny", agent: "codex", model: "o3")
        #expect(migrated.id == "canny")

        _ = await server.result
        await client.close()
    }

    /// A daemon `error` reply to `fork` surfaces as a thrown `ControlError`
    /// rather than a bogus session (issue #899).
    @Test func forkErrorReplyThrows() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl() // handshake
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            _ = try await daemon.readControl() // fork
            try await daemon.writeControl("error", ErrorMsg(message: "source not found"))
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        await #expect(throws: ControlError.self) {
            _ = try await client.fork(name: "dreich", sourceSessionID: "missing")
        }

        _ = await server.result
        await client.close()
    }

    // MARK: - Restore / purge / set-status / deleted-list wire shapes (#1148)

    /// Small decoder for the deleted-`list` request body (`{deleted:true}`),
    /// which has no `session_id` so it can't reuse SessionScopeMsg.
    private struct ListProbe: Decodable { let deleted: Bool? }

    /// `list(deleted:)` sends type `list` with `{"deleted": true}`; the default
    /// live `list` stays payload-free (asserted by `protocolClientListLocal`).
    @Test func listDeletedSendsDeletedFlag() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)
        let server = Task {
            _ = try await daemon.readControl()
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            let req = try await daemon.readControl()
            #expect(req.type == "list")
            #expect(try decodePayload(req, as: ListProbe.self).deleted == true)
            try await daemon.writeControl("session_list", SessionListMsg(sessions: [makeSession(id: "bide", name: "bide")]))
        }
        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        let sessions = try await client.list(deleted: true)
        #expect(sessions.map(\.name) == ["bide"])
        _ = await server.result
        await client.close()
    }

    /// `restore` sends type `restore` with the session_id (+ children) subset.
    @Test func restoreSendsRestoreType() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)
        let server = Task {
            _ = try await daemon.readControl()
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            let req = try await daemon.readControl()
            #expect(req.type == "restore")
            let scope = try decodePayload(req, as: SessionScopeMsg.self)
            #expect(scope.sessionID == "braw")
            #expect(scope.purge == nil)  // restore never sets the delete-only flag
            try await daemon.writeControl("restored", EmptyMsg())
        }
        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        try await client.restore(sessionID: "braw")
        _ = await server.result
        await client.close()
    }

    /// `purge` is sent as a `delete` with `purge: true` (the `gr purge` verb).
    @Test func purgeSendsDeleteWithPurgeFlag() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)
        let server = Task {
            _ = try await daemon.readControl()
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            let req = try await daemon.readControl()
            #expect(req.type == "delete")
            let scope = try decodePayload(req, as: SessionScopeMsg.self)
            #expect(scope.sessionID == "canny")
            #expect(scope.purge == true)
            try await daemon.writeControl("deleted", EmptyMsg())
        }
        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        try await client.purge(sessionID: "canny")
        _ = await server.result
        await client.close()
    }

    /// `setStatus` sends type `set_status` carrying text, ttl_seconds, and clear.
    @Test func setStatusSendsSetStatusPayload() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)
        let server = Task {
            _ = try await daemon.readControl()
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            let req = try await daemon.readControl()
            #expect(req.type == "set_status")
            let msg = try decodePayload(req, as: SetStatusMsg.self)
            #expect(msg.sessionID == "braw")
            #expect(msg.text == "building the bonnie feature")
            #expect(msg.ttlSeconds == 600)
            #expect(msg.clear == false)
            try await daemon.writeControl("status_set", EmptyMsg())
        }
        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        try await client.setStatus(sessionID: "braw", text: "building the bonnie feature", ttlSeconds: 600, clear: false)
        _ = await server.result
        await client.close()
    }
}

    /// CRITICAL regression: the token-less pairing lane must consume the
    /// unsolicited `auth_challenge` the daemon sends after `handshake_ok` on
    /// every remote connection, then await the (delayed) `pair_response` —
    /// rather than mistaking the challenge for the pairing reply. Mirrors the Go
    /// client (internal/client/remote.go).
    @Test func pairingLaneSkipsAuthChallenge() async throws {
        let pin = "bide-spki-pin=="
        let (clientStream, serverStream) = InMemoryByteStream.makePair(clientSimulatedPin: pin)
        let daemon = MockDaemon(stream: serverStream)
        let signer = TestSigner(deviceID: "")

        let server = Task {
            let hs = try await daemon.readControl()
            #expect(hs.type == "handshake")
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            // Unsolicited challenge issued to EVERY remote connection, before the
            // client has even sent pair_request.
            try await daemon.writeControl("auth_challenge", AuthChallengeMsg(nonce: "haar-nonce"))
            let pair = try await daemon.readControl()
            #expect(pair.type == "pair_request")
            let req = try decodePayload(pair, as: PairRequestMsg.self)
            #expect(req.deviceLabel == "bonnie-phone")
            // Awaiting the local human's approval takes time — delay the reply.
            try await Task.sleep(nanoseconds: 40_000_000)
            try await daemon.writeControl("pair_response", PairResponseMsg(
                deviceID: "dev-braw-1", clientToken: "tok-braw",
                daemonProfile: "", tlsPinSPKI: pin))
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .remote(host: "ben", port: 4823, tlsPinSPKI: nil),
            profile: "", clientID: "app", token: nil, signer: signer,
            streamFactory: { _ in stream }
        )
        let resp = try await client.pairRequest(deviceLabel: "bonnie-phone")
        #expect(resp.clientToken == "tok-braw")
        #expect(resp.deviceID == "dev-braw-1")
        _ = await server.result
        await client.close()
    }

    /// Pairing binds TOFU: if the pin the daemon reports differs from the SPKI of
    /// the cert actually presented on the handshake, pairing is refused (MITM).
    @Test func pairingRejectsPinMismatch() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair(clientSimulatedPin: "presented-thrawn==")
        let daemon = MockDaemon(stream: serverStream)
        let signer = TestSigner(deviceID: "")

        let server = Task {
            _ = try await daemon.readControl()
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            try await daemon.writeControl("auth_challenge", AuthChallengeMsg(nonce: "haar"))
            _ = try await daemon.readControl()
            // Daemon reports a pin that does NOT match the presented cert.
            try await daemon.writeControl("pair_response", PairResponseMsg(
                deviceID: "d", clientToken: "t", daemonProfile: "", tlsPinSPKI: "reported-scunner=="))
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .remote(host: "ben", port: 4823, tlsPinSPKI: nil),
            profile: "", clientID: "app", token: nil, signer: signer,
            streamFactory: { _ in stream }
        )
        await #expect(throws: ControlError.self) {
            _ = try await client.pairRequest(deviceLabel: "dreich-phone")
        }
        _ = await server.result
        await client.close()
    }

    /// A daemon that reports no TLS pin cannot have its endpoint confirmed, so
    /// pairing is refused.
    @Test func pairingRejectsEmptyReportedPin() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair(clientSimulatedPin: "presented==")
        let daemon = MockDaemon(stream: serverStream)
        let signer = TestSigner(deviceID: "")

        let server = Task {
            _ = try await daemon.readControl()
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            try await daemon.writeControl("auth_challenge", AuthChallengeMsg(nonce: "haar"))
            _ = try await daemon.readControl()
            try await daemon.writeControl("pair_response", PairResponseMsg(
                deviceID: "d", clientToken: "t", daemonProfile: "", tlsPinSPKI: ""))
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .remote(host: "ben", port: 4823, tlsPinSPKI: nil),
            profile: "", clientID: "app", token: nil, signer: signer,
            streamFactory: { _ in stream }
        )
        await #expect(throws: ControlError.self) {
            _ = try await client.pairRequest(deviceLabel: "fash-phone")
        }
        _ = await server.result
        await client.close()
    }

    /// MAJOR regression: two overlapping RPCs on one connection must not
    /// mis-route replies. The daemon echoes each request's type back; with the
    /// per-connection RPC mutex, each caller receives the reply to *its own*
    /// request regardless of scheduling.
    @Test func overlappingRequestsAreNotMisrouted() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl() // handshake
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            for _ in 0..<2 {
                let env = try await daemon.readControl()
                // Reply type echoes the request type so mis-routing is detectable.
                try await daemon.writeControl(env.type, EmptyMsg())
            }
        }

        let conn = GraithConnection(transport: .unix(path: "/tmp/brig.sock"), stream: clientStream, token: nil)
        try await conn.connect(handshake: HandshakeMsg(clientID: "1", terminalSize: [80, 24], cwd: "", profile: ""))

        async let alpha = conn.request("alpha", payload: EmptyMsg())
        async let bravo = conn.request("bravo", payload: EmptyMsg())
        let (a, b) = try await (alpha, bravo)
        #expect(a.type == "alpha")
        #expect(b.type == "bravo")

        _ = await server.result
        await conn.close()
    }

    /// A no-argument log peek must send the daemon-default sentinel (`lines: 0`)
    /// so the daemon resolves the configured `[limits] log_lines` instead of the
    /// GUI imposing a compile-time count that bypasses it (issue #1289).
    @Test func logsDefaultSendsDaemonSentinel() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl() // handshake
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))

            let logsReq = try await daemon.readControl()
            #expect(logsReq.type == "logs")
            let msg = try decodePayload(logsReq, as: LogsMsg.self)
            #expect(msg.sessionID == "bothy")
            #expect(msg.lines == 0)
            #expect(msg.follow == false)

            try await daemon.writeData(Data("dreich output\n".utf8))
            try await daemon.writeControl("logs_done", EmptyMsg())
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        let text = try await client.logs(sessionID: "bothy")
        #expect(text == "dreich output\n")

        _ = await server.result
        await client.close()
    }

    /// An explicit positive `lines` count is preserved verbatim — the sentinel
    /// default must not clobber a caller's real override (issue #1289).
    @Test func logsExplicitLineCountPreserved() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl() // handshake
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))

            let logsReq = try await daemon.readControl()
            #expect(logsReq.type == "logs")
            #expect(try decodePayload(logsReq, as: LogsMsg.self).lines == 42)

            try await daemon.writeData(Data("canny\n".utf8))
            try await daemon.writeControl("logs_done", EmptyMsg())
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        let text = try await client.logs(sessionID: "bothy", lines: 42)
        #expect(text == "canny\n")

        _ = await server.result
        await client.close()
    }

/// Build a minimal valid ``SessionInfo`` for fixtures.
func makeSession(id: String, name: String) -> SessionInfo {
    let json = """
    {"id":"\(id)","name":"\(name)","repo_path":"/croft","repo_name":"croft",\
    "worktree_path":"/bothy","branch":"b","base_branch":"main","agent":"claude",\
    "status":"running","created_at":"2026-07-08T00:00:00Z"}
    """
    return try! JSONDecoder().decode(SessionInfo.self, from: Data(json.utf8))
}
