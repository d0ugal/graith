import CryptoKit
import Foundation
import GraithProtocol

/// A ``DeviceKeySigner`` backed by an in-memory CryptoKit ed25519 key, for
/// tests. CryptoKit's `Curve25519.Signing` is standard Ed25519, so signatures
/// it produces verify under Go's `crypto/ed25519` (`verifyPoP`).
struct TestSigner: DeviceKeySigner {
    let deviceID: String
    // Generated at runtime in init(); this is a type declaration, not a
    // hardcoded secret (gitleaks' generic-api-key rule false-positives on
    // "key: Curve25519.Signing.PrivateKey"). gitleaks:allow
    private let key: Curve25519.Signing.PrivateKey // gitleaks:allow

    init(deviceID: String) {
        self.deviceID = deviceID
        self.key = Curve25519.Signing.PrivateKey()
    }

    func publicKeyRaw() throws -> Data {
        key.publicKey.rawRepresentation
    }

    func sign(_ message: Data) throws -> Data {
        try key.signature(for: message)
    }

    /// Verify a base64-std signature over the channel-bound proof-of-possession
    /// signing input, exactly as the daemon's `verifyPoP` does (issue #886):
    /// `"graith-pop-v1:" + nonce + ":" + spki`.
    func verify(base64Signature: String, nonce: String, channelBinding spki: String) -> Bool {
        guard let sig = Data(base64Encoded: base64Signature) else { return false }
        return key.publicKey.isValidSignature(sig, for: Data("graith-pop-v1:\(nonce):\(spki)".utf8))
    }
}
