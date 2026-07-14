// GraithSessionKit is the umbrella the SwiftUI apps import for the whole
// session/feature layer. Re-export the layers it is built on so a consumer that
// `import GraithSessionKit` also sees the canonical wire models
// (`SessionInfo`, `ApprovalInfo`, `RepoEntry`, `CreateMsg`, …) and the host /
// pairing / identity substrate (`Host`, `HostRegistry`, `DeviceIdentity`,
// `PairingCoordinator`, `RealPairing`, …) without a second import. This is what
// lets a feature be wired once here and bound identically on both platforms.
@_exported import GraithProtocol
@_exported import GraithRemoteKit
