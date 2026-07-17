import Foundation
import GraithSessionKit

/// A mock `GraithPairing` that immediately "approves" and returns a canned
/// `PairResponse`, exercising the pairing coordinator without a daemon.
public struct MockPairing: GraithPairing {
    public var approvalDelay: Duration
    public var failure: GraithClientError?

    public init(approvalDelay: Duration = .milliseconds(50), failure: GraithClientError? = nil) {
        self.approvalDelay = approvalDelay
        self.failure = failure
    }

    public func beginPairing(
        transport: GraithTransport,
        deviceLabel: String,
        profile: String,
        signer: DeviceKeySigner
    ) async throws -> PairingSession {
        // Prove we can exercise the signer's key material.
        _ = try signer.publicKeyRaw()
        try await Task.sleep(for: approvalDelay)
        if let failure { throw failure }
        let response = PairResponse(
            requestID: "req-\(UUID().uuidString)",
            deviceID: "dev-bairn-001",
            clientToken: "tok-\(UUID().uuidString)",
            daemonProfile: "default",
            tlsPinSPKI: Data("bide-spki-pin-bytes".utf8).base64EncodedString()
        )
        return MockPairingSession(response: response)
    }
}

/// A mock ``PairingSession`` whose receipt handshake trivially succeeds — the
/// coordinator can exercise persist → ack → committed without a daemon.
public struct MockPairingSession: PairingSession {
    public let response: PairResponse
    public init(response: PairResponse) { self.response = response }
    public func ackAndAwaitCommit() async throws {}
    public func abandon() async {}
}

/// A pure in-memory `DeviceKeySigner` for tests that don't need the Keychain.
public final class MockDeviceSigner: DeviceKeySigner, @unchecked Sendable {
    public private(set) var deviceID: String
    private let pub: Data

    public init(deviceID: String = "", publicKey: Data = Data(repeating: 0xAB, count: 32)) {
        self.deviceID = deviceID
        self.pub = publicKey
    }

    public func publicKeyRaw() throws -> Data { pub }
    public func sign(_ nonce: Data) throws -> Data {
        // Deterministic "signature": 64 bytes derived from the nonce.
        var out = Data(count: 64)
        for i in 0..<64 { out[i] = nonce.isEmpty ? UInt8(i) : nonce[i % nonce.count] &+ UInt8(i) }
        return out
    }

    public func setDeviceID(_ id: String) { deviceID = id }
}
