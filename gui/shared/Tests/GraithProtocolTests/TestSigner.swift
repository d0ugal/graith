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

    func sign(_ nonce: Data) throws -> Data {
        try key.signature(for: nonce)
    }

    /// Verify a base64-std signature over a nonce string's UTF-8 bytes, exactly
    /// as the daemon's `verifyPoP` does.
    func verify(base64Signature: String, nonce: String) -> Bool {
        guard let sig = Data(base64Encoded: base64Signature) else { return false }
        return key.publicKey.isValidSignature(sig, for: Data(nonce.utf8))
    }
}
