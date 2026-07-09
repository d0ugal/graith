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
/// - ``sign(_:)`` returns the **raw 64-byte** ed25519 signature over the bytes
///   it is given; the client base64-std encodes it into `AuthProofMsg.signature`.
/// - The proof-of-possession signing input is **not** the bare nonce: it is the
///   nonce bound to the TLS channel's server-cert SPKI (issue #886), built by
///   ``proof(forNonce:channelBinding:)`` to match the daemon's
///   `protocol.PoPSigningInput`.
public protocol DeviceKeySigner: Sendable {
    /// The device ID assigned by the daemon at pairing. Empty until paired
    /// (a fresh device sends `pair_request` before it has an ID).
    var deviceID: String { get }

    /// The raw 32-byte ed25519 public key for `pair_request`.
    func publicKeyRaw() throws -> Data

    /// Sign the given bytes, returning the raw 64-byte signature.
    func sign(_ message: Data) throws -> Data
}

public extension DeviceKeySigner {
    /// base64-std of the raw public key, ready for `PairRequestMsg`.
    func publicKeyBase64() throws -> String {
        try publicKeyRaw().base64EncodedString()
    }

    /// Produce the `AuthProofMsg` for a challenge, binding the proof to the TLS
    /// channel it is presented over (issue #886): sign
    /// `"graith-pop-v1:" + nonce + ":" + spki` and base64-std encode the
    /// signature. `spki` is the base64 SHA-256 SPKI pin of the certificate this
    /// connection actually observed — never one the peer reports — so a MITM
    /// relaying the handshake (who presents a different cert, hence a different
    /// SPKI) cannot forward a captured proof. Must stay byte-for-byte identical
    /// to the daemon's `protocol.PoPSigningInput`.
    func proof(forNonce nonce: String, channelBinding spki: String) throws -> AuthProofMsg {
        let input = Data("graith-pop-v1:\(nonce):\(spki)".utf8)
        let sig = try sign(input)
        return AuthProofMsg(deviceID: deviceID, signature: sig.base64EncodedString())
    }
}
