import Foundation
import GraithProtocol
import GraithRemoteKit

/// The production `HostClientFactory`: builds a `GraithProtocolClient` per host
/// from the paired `HostCredentials` + injected `DeviceKeySigner`, wrapped in a
/// `RealHostClient`. Drop-in replacement for `MockClientFactory`.
public struct RealHostClientFactory: HostClientFactory {
    private let clientID: String

    /// - Parameter clientID: identifier carried in the handshake (logging only).
    ///   Defaults to a platform-neutral value; each app may pass its own.
    public init(clientID: String = "graith-app") {
        self.clientID = clientID
    }

    public func makeClient(
        transport: GraithTransport,
        credentials: HostCredentials,
        signer: DeviceKeySigner
    ) -> any GraithHostClient {
        // The signer holds the global ed25519 key, but proof-of-possession must
        // present the device ID this specific host assigned us (carried on the
        // credentials, sourced from the Host). Scope the signer to that ID so
        // connecting to host A always signs as A even after pairing host B.
        let scopedSigner = HostScopedSigner(base: signer, deviceID: credentials.deviceID)
        let inner = GraithProtocolClient(
            transport: transport,
            profile: credentials.daemonProfile,
            clientID: clientID,
            token: credentials.clientToken,
            signer: scopedSigner
        )
        return RealHostClient(inner: inner)
    }

    public func makeLocalClient(transport: GraithTransport, profile: String) -> any GraithHostClient {
        // Tokenless, no PoP signer: the desktop app connects to its own daemon
        // over the 0700 Unix socket as the local human. We deliberately do NOT
        // forward GRAITH_TOKEN — that per-session token would make the daemon
        // treat the app as the launching agent.
        let inner = GraithProtocolClient(
            transport: transport,
            profile: profile,
            clientID: clientID,
            token: nil,
            signer: nil
        )
        return RealHostClient(inner: inner)
    }
}
