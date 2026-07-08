import Foundation
import GraithClientAPI
import GraithProtocol

/// The production `GraithPairing`: opens a pre-auth (token-less) connection to a
/// daemon and sends `pair_request`, then blocks until the local human runs
/// `gr pair approve` (design §B.2). Drop-in replacement for
/// `GraithMobileMock.MockPairing`.
public struct RealPairing: GraithPairing {
    private let clientID: String

    public init(clientID: String = "graith-ios") {
        self.clientID = clientID
    }

    public func requestPairing(
        transport: GraithClientAPI.GraithTransport,
        deviceLabel: String,
        profile: String,
        signer: DeviceKeySigner
    ) async throws -> PairResponse {
        // token == nil ⇒ the connection skips proof-of-possession and may only
        // send `pair_request` (a brand-new device has no daemon record yet).
        // `profile` is threaded into the handshake so a named-profile daemon
        // does not reject pairing with a profile mismatch.
        let client = GraithProtocolClient(
            transport: GraithProtocol.GraithTransport(transport),
            profile: profile,
            clientID: clientID,
            token: nil,
            signer: signer
        )
        do {
            let resp = try await client.pairRequest(deviceLabel: deviceLabel)
            await client.close()
            return PairResponse(resp)
        } catch {
            await client.close()
            throw RealClientError.map(error)
        }
    }
}
