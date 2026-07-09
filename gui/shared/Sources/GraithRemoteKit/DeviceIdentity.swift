import Foundation
import CryptoKit
import GraithProtocol

/// The device's long-lived ed25519 identity, used for pairing proof-of-possession.
/// The private key is generated once and persisted in the ``SecretStore``
/// (Keychain in production); the public key is sent in `pair_request`, and the
/// daemon challenges every remote connection with a nonce this device signs.
///
/// Conforms to `GraithProtocol.DeviceKeySigner` so the shared transport can sign
/// challenges without ever seeing the private key material directly. This is the
/// shared counterpart of `GraithMobileKit.DeviceIdentity`.
public final class DeviceIdentity: DeviceKeySigner, @unchecked Sendable {
    private let keychain: SecretStore
    private let privateKeyAccount: String
    private let deviceIDAccount: String

    private let lock = NSLock()
    private var cachedKey: Curve25519.Signing.PrivateKey?
    private var cachedDeviceID: String

    /// - Parameters:
    ///   - keychain: backing store for the private key + device ID.
    ///   - privateKeyAccount: store account for the raw private key.
    ///   - deviceIDAccount: store account for the daemon-assigned device ID.
    public init(
        keychain: SecretStore,
        privateKeyAccount: String = "device.ed25519.privateKey",
        deviceIDAccount: String = "device.id"
    ) throws {
        self.keychain = keychain
        self.privateKeyAccount = privateKeyAccount
        self.deviceIDAccount = deviceIDAccount
        self.cachedDeviceID = (try? keychain.string(for: deviceIDAccount)) ?? ""
        _ = try loadOrCreateKey()
    }

    // MARK: - DeviceKeySigner

    public var deviceID: String {
        lock.lock(); defer { lock.unlock() }
        return cachedDeviceID
    }

    /// The raw 32-byte ed25519 public key. Base64-encoded into `PairRequestMsg`.
    public func publicKeyRaw() throws -> Data {
        let key = try loadOrCreateKey()
        return key.publicKey.rawRepresentation
    }

    /// A raw 64-byte ed25519 signature over `nonce`. The daemon signs
    /// `[]byte(nonce)` (verbatim UTF-8), so the caller passes the nonce's UTF-8
    /// bytes — see `DeviceKeySigner.proof(forNonce:)`.
    public func sign(_ nonce: Data) throws -> Data {
        let key = try loadOrCreateKey()
        return try key.signature(for: nonce)
    }

    // MARK: - Pairing lifecycle

    /// Record the device ID assigned by the daemon on a successful pairing.
    /// Connections present a *per-host* device ID via ``HostScopedSigner``; this
    /// global value is a convenience fallback (last device ID assigned).
    public func setDeviceID(_ id: String) throws {
        lock.lock(); defer { lock.unlock() }
        try keychain.set(id, for: deviceIDAccount)
        cachedDeviceID = id
    }

    /// Whether pairing has completed anywhere (a device ID has been assigned).
    public var isPaired: Bool { !deviceID.isEmpty }

    /// Destroy the key + device ID (e.g. after a revoke or "forget this device").
    /// A fresh key is generated lazily on next use.
    public func reset() throws {
        lock.lock(); defer { lock.unlock() }
        try keychain.remove(privateKeyAccount)
        try keychain.remove(deviceIDAccount)
        cachedKey = nil
        cachedDeviceID = ""
    }

    // MARK: - Key storage

    private func loadOrCreateKey() throws -> Curve25519.Signing.PrivateKey {
        lock.lock(); defer { lock.unlock() }
        if let cachedKey { return cachedKey }

        if let raw = try keychain.data(for: privateKeyAccount) {
            let key = try Curve25519.Signing.PrivateKey(rawRepresentation: raw)
            cachedKey = key
            return key
        }

        let key = Curve25519.Signing.PrivateKey()
        try keychain.set(key.rawRepresentation, for: privateKeyAccount)
        cachedKey = key
        return key
    }
}

/// Wraps the device signer to present a **per-host** device ID during
/// proof-of-possession while reusing the single global ed25519 signing key.
///
/// One device has one key, but each daemon assigns this device its own device
/// ID at pairing. Signing for host A must carry A's device ID — not whatever a
/// later pairing with host B set — so each host's client gets its own scoped
/// signer rather than mutating a global identity.
public struct HostScopedSigner: DeviceKeySigner {
    public let base: DeviceKeySigner
    public let deviceID: String

    public init(base: DeviceKeySigner, deviceID: String) {
        self.base = base
        self.deviceID = deviceID
    }

    public func publicKeyRaw() throws -> Data { try base.publicKeyRaw() }
    public func sign(_ nonce: Data) throws -> Data { try base.sign(nonce) }
}
