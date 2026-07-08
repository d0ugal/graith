import Foundation

/// Provides the device's ed25519 identity for pairing and proof-of-possession.
///
/// The **app** owns the key material (generated once, stored in the Keychain /
/// Secure Enclave where available) and conforms to this protocol; the protocol
/// client only ever asks it for the raw public key (at pairing) and for a
/// signature over a challenge nonce (on every remote connection). This keeps
/// all secret handling in the app layer and out of `GraithProtocol`.
///
/// Wire contract (verified against `internal/daemon/pairing.go` `verifyPoP`):
/// - ``publicKeyRaw()`` returns the **raw 32-byte** ed25519 public key; the
///   client base64-std encodes it into `PairRequestMsg.devicePubKey`.
/// - ``sign(_:)`` returns the **raw 64-byte** ed25519 signature over the nonce
///   bytes it is given; the client base64-std encodes it into
///   `AuthProofMsg.signature`.
/// - The nonce passed to ``sign(_:)`` is the challenge string's **verbatim
///   UTF-8 bytes** (the daemon signs `[]byte(nonce)`, not a base64-decode).
public protocol DeviceKeySigner: Sendable {
    /// The device ID assigned by the daemon at pairing. Empty until paired
    /// (a fresh device sends `pair_request` before it has an ID).
    var deviceID: String { get }

    /// The raw 32-byte ed25519 public key for `pair_request`.
    func publicKeyRaw() throws -> Data

    /// Sign the challenge nonce bytes, returning the raw 64-byte signature.
    func sign(_ nonce: Data) throws -> Data
}

public extension DeviceKeySigner {
    /// base64-std of the raw public key, ready for `PairRequestMsg`.
    func publicKeyBase64() throws -> String {
        try publicKeyRaw().base64EncodedString()
    }

    /// Produce the `AuthProofMsg` for a challenge: sign the nonce's UTF-8 bytes
    /// and base64-std encode the signature.
    func proof(forNonce nonce: String) throws -> AuthProofMsg {
        let sig = try sign(Data(nonce.utf8))
        return AuthProofMsg(deviceID: deviceID, signature: sig.base64EncodedString())
    }
}
