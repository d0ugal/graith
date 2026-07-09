import Foundation
import GraithProtocol
@testable import GraithRemoteKit

/// macOS ships `Foundation.Host`, which would make bare `Host` ambiguous in the
/// test module once GraithRemoteKit is imported. Pin it to our type module-wide.
typealias Host = GraithRemoteKit.Host

/// `PairResponseMsg`'s memberwise init is internal to GraithProtocol, so tests
/// build one the way the wire does: decode it from the daemon's JSON shape.
func makePairResponse(
    deviceID: String,
    clientToken: String,
    daemonProfile: String = "",
    tlsPinSPKI: String
) -> PairResponseMsg {
    let json = """
    {
      "device_id": "\(deviceID)",
      "client_token": "\(clientToken)",
      "daemon_profile": "\(daemonProfile)",
      "tls_pin_spki": "\(tlsPinSPKI)"
    }
    """
    // Force-try: the literal above is always valid JSON for this type.
    // swiftlint:disable:next force_try
    return try! JSONDecoder().decode(PairResponseMsg.self, from: Data(json.utf8))
}

/// A canned pairing backend for driving `PairingCoordinator` without a daemon.
struct StubPairing: GraithPairing {
    enum Outcome: Sendable {
        case succeed(PairResponseMsg)
        case fail(ControlError)
    }
    let outcome: Outcome

    func requestPairing(
        transport: GraithTransport,
        deviceLabel: String,
        profile: String,
        signer: DeviceKeySigner
    ) async throws -> PairResponseMsg {
        switch outcome {
        case let .succeed(resp): return resp
        case let .fail(err): throw err
        }
    }
}
