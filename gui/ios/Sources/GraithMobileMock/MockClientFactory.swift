import Foundation
import GraithClientAPI

/// A `HostClientFactory` that hands out `MockHostClient`s. Optionally keyed by
/// the transport's host so previews can show different sessions per daemon.
public struct MockClientFactory: HostClientFactory {
    private let clientForHost: @Sendable (String) -> MockHostClient

    /// - Parameter clientForHost: builds a client given the MagicDNS host name.
    ///   Defaults to a fresh `MockHostClient` with the standard fixtures.
    public init(clientForHost: @escaping @Sendable (String) -> MockHostClient = { _ in MockHostClient() }) {
        self.clientForHost = clientForHost
    }

    public func makeClient(
        transport: GraithTransport,
        credentials: HostCredentials,
        signer: DeviceKeySigner
    ) -> any GraithHostClient {
        let host: String
        switch transport {
        case .remote(let h, _, _): host = h
        case .unix(let p): host = p
        }
        return clientForHost(host)
    }
}
