import Foundation
import GraithClientAPI
import GraithProtocol

// Translate the shared transport/control errors into the boundary's
// `GraithClientError` so the UI (PairingCoordinator / HostConnection) renders
// the friendly, case-specific messages it already knows how to describe.
enum RealClientError {
    static func map(_ error: Error) -> GraithClientError {
        if let e = error as? GraithClientError { return e }
        if let e = error as? ControlError {
            switch e {
            case let .daemon(m):
                // The daemon has no error codes on the wire, only text. Recognise
                // the genuine "this device is not paired / not allowed on a remote
                // link" signals explicitly (an unpaired or revoked device's RPCs
                // are rejected with these), and preserve every other message.
                //
                // Previously any message merely *containing* "pair" collapsed to
                // .notPaired — which mis-mapped pairing-flow failures (rate limits,
                // capacity limits, timeouts, "unknown or expired") and dropped
                // their detail, while missing the real "invalid token" signal that
                // does not contain "pair".
                let lower = m.lowercased()
                if lower.contains("invalid token") || lower.contains("not authorized over remote") {
                    return .notPaired
                }
                return .daemon(m)
            case let .handshakeRejected(m):
                return .authenticationFailed(m)
            case let .malformed(m):
                return .decoding(m)
            case let .unexpectedReply(m):
                return .decoding("unexpected reply: \(m)")
            case .tlsPinMismatch:
                // Endpoint could not be confirmed during pairing (reported pin
                // absent, uncapturable, or ≠ the presented cert). Surface as the
                // typed pin-mismatch case so the UI prompts to re-establish trust.
                return .tlsPinMismatch
            }
        }
        if let e = error as? TransportError {
            switch e {
            case .notReady:
                // Persistent `waiting` — the tailnet/host isn't reachable.
                return .tailnetUnreachable
            case let .failed(m):
                return .disconnected(m)
            }
        }
        return .disconnected(error.localizedDescription)
    }
}
