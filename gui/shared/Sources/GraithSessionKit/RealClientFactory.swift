import Foundation
import GraithProtocol
import GraithRemoteKit

/// The production `HostClientFactory`: builds a `GraithProtocolClient` per host
/// from the paired `HostCredentials` + injected `DeviceKeySigner`, wrapped in a
/// `RealHostClient`. Drop-in replacement for `MockClientFactory`.
public struct RealHostClientFactory: HostClientFactory {
    private let clientID: String
    private let localHumanToken: @Sendable () -> String?

    /// - Parameters:
    ///   - clientID: identifier carried in the handshake (logging only).
    ///   - localHumanToken: late-bound lookup for the daemon-written credential
    ///     used by local Unix-socket connections. Remote clients continue to
    ///     use paired credentials.
    public init(
        clientID: String = "graith-app",
        localHumanToken: @escaping @Sendable () -> String? = { nil }
    ) {
        self.clientID = clientID
        self.localHumanToken = localHumanToken
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
        // Local human token, no PoP signer: this is the same transparent local
        // authentication the CLI uses outside a session. The composition root
        // reads human.token for each new connection rather than forwarding
        // GRAITH_TOKEN, whose per-session value would make the desktop app act
        // as its launching agent instead of as the human operator.
        let inner = GraithProtocolClient(
            transport: transport,
            profile: profile,
            clientID: clientID,
            token: nil,
            signer: nil,
            tokenProvider: localHumanToken
        )
        return RealHostClient(inner: inner)
    }
}
