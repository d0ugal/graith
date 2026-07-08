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
            name: "GraithTerminalCore",
            dependencies: ["CGhosttyVT"]
        ),
        .testTarget(
            name: "GraithTerminalCoreTests",
            dependencies: ["GraithTerminalCore"]
        ),
        .target(
            name: "CGhosttyVT",
            path: "Sources/CGhosttyVT",
            publicHeadersPath: "include",
            cSettings: [
                .define("GHOSTTY_STATIC"),
            ],
            linkerSettings: [
                // TODO(#628, Task 13): replace the unpinned macOS-only
                // Libraries/libghostty-vt.a + unsafe flags with a SHA-pinned
                // .xcframework (macos + ios + ios-sim). See build-libghostty.sh
                // and ../NEEDS-MAC-VALIDATION.md. -L is resolved against the
                // build CWD, so a dependent package that links this target from
                // its own dir (e.g. ../macos) must add its own -L to Libraries.
                .unsafeFlags(["-LLibraries"]),
                .linkedLibrary("ghostty-vt"),
            ]
        ),
    ]
)
