import GraithSessionKit

// macOS ships `Foundation.Host` (NSHost); pin a bare `Host` to our type
// module-wide so it isn't ambiguous once GraithSessionKit (which re-exports
// GraithRemoteKit) is imported. Mirrors the macOS GraithGUI shadow.
typealias Host = GraithSessionKit.Host
