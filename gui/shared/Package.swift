// swift-tools-version: 5.9
import PackageDescription

// Shared core for the graith universal app (#628): the transport-abstract
// framed-protocol client, the terminal core (libghostty-vt wrapper + renderer),
// and the CGhosttyVT shim. Both the macOS app (../macos) and the iOS app
// (../ios) depend on this package.
let package = Package(
    name: "GraithShared",
    platforms: [.macOS(.v14), .iOS(.v16)],
    products: [
        .library(name: "GraithProtocol", targets: ["GraithProtocol"]),
        // Cross-platform remote/pairing substrate over GraithProtocol: device
        // identity (Keychain + ed25519), the host registry, and the pairing
        // coordinator. The macOS app consumes it to gain the multi-host remote
        // experience iOS already has (#885); iOS keeps its GraithMobileKit copies
        // for now (a future task can unify them onto this).
        .library(name: "GraithRemoteKit", targets: ["GraithRemoteKit"]),
        .library(name: "GraithTerminalCore", targets: ["GraithTerminalCore"]),
        .library(name: "CGhosttyVT", targets: ["CGhosttyVT"]),
        // Shared design language (Catppuccin palette, monospace type, GRAITH
        // wordmark, empty states). Pure SwiftUI — no protocol/libghostty deps —
        // so any UI target (macOS + iOS) can link it without pulling the VT lib.
        .library(name: "GraithDesign", targets: ["GraithDesign"]),
    ],
    dependencies: [],
    targets: [
        .target(name: "GraithProtocol"),
        .target(name: "GraithDesign"),
        .testTarget(
            name: "GraithProtocolTests",
            dependencies: ["GraithProtocol"]
        ),
        .target(
            name: "GraithRemoteKit",
            dependencies: ["GraithProtocol"]
        ),
        .testTarget(
            name: "GraithRemoteKitTests",
            dependencies: ["GraithRemoteKit", "GraithProtocol"]
        ),
        .target(
            name: "GraithTerminalCore",
            dependencies: ["CGhosttyVT"]
        ),
        .testTarget(
            name: "GraithTerminalCoreTests",
            dependencies: ["GraithTerminalCore"]
        ),
        // The C shim exposes the `CGhosttyVT` module (headers + a trivial .c so
        // SPM accepts the target). The actual libghostty-vt implementation comes
        // from the checksummed remote .xcframework below — SPM auto-selects the
        // right slice (macos / ios / ios-simulator), replacing the old macOS-only
        // Libraries/libghostty-vt.a + unsafe -L hack (Task 13, now done).
        .target(
            name: "CGhosttyVT",
            dependencies: ["GhosttyVt"],
            path: "Sources/CGhosttyVT",
            publicHeadersPath: "include",
            cSettings: [
                .define("GHOSTTY_STATIC"),
            ]
        ),
        // Not a committed artifact: a checksummed remote binaryTarget pinned to
        // Ghostty @ 91f66da. SPM downloads it once, caches it, and verifies the
        // checksum on every resolve, so the ~10 MB xcframework stays out of git
        // while the build remains reproducible. Rebuild + re-upload via
        // build-libghostty.sh and bump the url/checksum to move the pin.
        .binaryTarget(
            name: "GhosttyVt",
            url: "https://github.com/d0ugal/graith/releases/download/libghostty-vt-91f66da/libghostty-vt.xcframework.zip",
            checksum: "25c1620e63311cc687637c8e3bdfae1fe3e070892966c07d0d91065ccda541c0"
        ),
    ]
)
