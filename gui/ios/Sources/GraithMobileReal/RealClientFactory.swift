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
        let inner = GraithProtocolClient(
            transport: GraithProtocol.GraithTransport(transport),
            profile: credentials.daemonProfile,
            clientID: clientID,
            token: credentials.clientToken,
            signer: signer
        )
        return RealHostClient(inner: inner)
    }
}
