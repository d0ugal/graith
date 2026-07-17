import Foundation
import Testing
@testable import GraithProtocol

struct ControlAndMessageTests {
    @Test func envelopeRoundTripWithToken() throws {
        let data = try encodeControl("attach", AttachMsg(sessionID: "braw"), token: "hmac-tok")
        let env = try decodeControl(data)
        #expect(env.type == "attach")
        #expect(env.token == "hmac-tok")
        let payload = try decodePayload(env, as: AttachMsg.self)
        #expect(payload.sessionID == "braw")
    }

    @Test func envelopeOmitsEmptyTokenAndPayload() throws {
        let data = try encodeControl("list")
        let json = String(decoding: data, as: UTF8.self)
        #expect(!json.contains("token"))
        #expect(!json.contains("payload"))
        let env = try decodeControl(data)
        #expect(env.type == "list")
        #expect(env.token == nil)
        #expect(env.payload == nil)
    }

    @Test func payloadKeptAsRawJSON() throws {
        let go = #"{"type":"attached","payload":{"id":"canny","name":"canny","repo_path":"/croft","repo_name":"croft","worktree_path":"/bothy","branch":"b","base_branch":"main","agent":"claude","status":"running","created_at":"2026-07-08T00:00:00Z"}}"#
        let env = try decodeControl(Data(go.utf8))
        #expect(env.type == "attached")
        let info = try decodePayload(env, as: SessionInfo.self)
        #expect(info.name == "canny")
        #expect(info.repoName == "croft")
        #expect(info.status == "running")
    }

    @Test func versionCompatibility() {
        #expect(versionCompatible("1.0"))
        #expect(versionCompatible("1.9"))
        #expect(!versionCompatible("2.0"))
        #expect(!versionCompatible("garbage"))
    }

    @Test func handshakeUsesSnakeCaseKeys() throws {
        let hs = HandshakeMsg(clientID: "42", terminalSize: [120, 40], cwd: "/glen", profile: "kirk")
        let json = String(decoding: try JSONEncoder().encode(hs), as: UTF8.self)
        #expect(json.contains("\"client_id\":\"42\""))
        #expect(json.contains("\"terminal_size\":[120,40]"))
        #expect(json.contains("\"profile\":\"kirk\""))
    }

    @Test func createOmitsEmptyAgentForDaemonDefaultCompatibility() throws {
        let msg = CreateMsg(name: "braw", agent: "", repoPath: "/croft")
        let data = try JSONEncoder().encode(msg)
        let object = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(object["agent"] == nil)
        #expect(object["name"] as? String == "braw")
        #expect(object["repo_path"] as? String == "/croft")
    }

    // Cross-version decode guards (issue #1299): the receipt fields must tolerate
    // absence so a legacy daemon / required-fields-only probe still decodes.
    @Test func pairRequestDecodesWithoutReceiptAck() throws {
        let json = #"{"device_label":"ben","device_pub_key":"cHVi"}"#
        let msg = try JSONDecoder().decode(PairRequestMsg.self, from: Data(json.utf8))
        #expect(msg.receiptAck == false)
        #expect(msg.deviceLabel == "ben")
    }

    @Test func pairResponseDecodesWithoutRequestID() throws {
        // A legacy (pre-receipt) daemon omits request_id.
        let json = #"{"device_id":"d","client_token":"t","daemon_profile":"","tls_pin_spki":"cGlu"}"#
        let msg = try JSONDecoder().decode(PairResponseMsg.self, from: Data(json.utf8))
        #expect(msg.requestID == "")
        #expect(msg.deviceID == "d")
        #expect(msg.clientToken == "t")
    }

    @Test func sessionInfoIgnoresRetiredFieldsKeepsPRCI() throws {
        // cost_usd/context_percent are NOT on the wire model; a payload
        // carrying them must still decode (extra keys ignored). PR/CI decode.
        let json = #"{"id":"skelf","name":"skelf","repo_path":"/croft","repo_name":"croft","worktree_path":"/w","branch":"b","base_branch":"main","agent":"codex","status":"stopped","created_at":"t","cost_usd":1.5,"context_percent":80,"pull_request":{"number":7,"state":"open"},"ci":{"state":"failing","failing_checks":["build"]}}"#
        let info = try JSONDecoder().decode(SessionInfo.self, from: Data(json.utf8))
        #expect(info.pullRequest?.number == 7)
        #expect(info.ci?.failingChecks == ["build"])
    }

    @Test func pairRequestSnakeCase() throws {
        let msg = PairRequestMsg(deviceLabel: "bairn", devicePubKey: "AAAA")
        let json = String(decoding: try JSONEncoder().encode(msg), as: UTF8.self)
        #expect(json.contains("\"device_label\":\"bairn\""))
        #expect(json.contains("\"device_pub_key\":\"AAAA\""))
    }

    @Test func pairResponseDecodesSPKIKey() throws {
        let json = #"{"device_id":"d1","client_token":"tok","daemon_profile":"","tls_pin_spki":"pin=="}"#
        let resp = try JSONDecoder().decode(PairResponseMsg.self, from: Data(json.utf8))
        #expect(resp.deviceID == "d1")
        #expect(resp.tlsPinSPKI == "pin==")
    }

    @Test func resizeMessageKeys() throws {
        let json = String(decoding: try JSONEncoder().encode(ResizeMsg(cols: 100, rows: 30)), as: UTF8.self)
        #expect(json.contains("\"cols\":100"))
        #expect(json.contains("\"rows\":30"))
    }
}
