import Foundation
import GraithClientAPI
import GraithProtocol

/// The production `HostClientFactory`: builds a `GraithProtocolClient` per host
/// from the paired `HostCredentials` + injected `DeviceKeySigner`, wrapped in a
/// `RealHostClient`. Drop-in replacement for `GraithMobileMock.MockClientFactory`.
public struct RealHostClientFactory: HostClientFactory {
    private let clientID: String

    /// - Parameter clientID: identifier carried in the handshake (logging only).
    public init(clientID: String = "graith-ios") {
        self.clientID = clientID
    }

    public func makeClient(
        transport: GraithClientAPI.GraithTransport,
        credentials: HostCredentials,
        signer: DeviceKeySigner
    ) -> any GraithHostClient {
        // The signer holds the global ed25519 key, but proof-of-possession must
        // present the device ID this specific host assigned us (carried on the
        // credentials, sourced from the HostEntry). Scope the signer to that ID
        // so connecting to host A always signs as A even after pairing host B.
        let scopedSigner = HostScopedSigner(base: signer, deviceID: credentials.deviceID)
        let inner = GraithProtocolClient(
            transport: GraithProtocol.GraithTransport(transport),
            profile: credentials.daemonProfile,
            clientID: clientID,
            token: credentials.clientToken,
            signer: scopedSigner
        )
        return RealHostClient(inner: inner)
    }
}

/// Wraps the shared device signer to present a **per-host** device ID during
/// proof-of-possession while reusing the single global ed25519 signing key.
///
/// One device has one key, but each daemon assigns this device its own device
/// ID at pairing. Signing for host A must carry A's device ID — not whatever a
/// later pairing with host B set — so each host's client gets its own scoped
/// signer rather than mutating a global identity (issue: F4).
struct HostScopedSigner: DeviceKeySigner {
    let base: DeviceKeySigner
    let deviceID: String

    func publicKeyRaw() throws -> Data { try base.publicKeyRaw() }
    func sign(_ nonce: Data) throws -> Data { try base.sign(nonce) }
}
