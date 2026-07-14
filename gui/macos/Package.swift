// swift-tools-version: 5.9
import PackageDescription

// The graith macOS app (#628): GraithGUI, an AppKit/SwiftUI + Metal terminal
// front end. All non-macOS-specific logic lives in the shared core package
// (../shared): GraithProtocol, GraithTerminalCore, CGhosttyVT.
let package = Package(
    name: "GraithMacOS",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "GraithGUI", targets: ["GraithGUI"]),
    ],
    dependencies: [
        .package(path: "../shared"),
    ],
    targets: [
        .executableTarget(
            name: "GraithGUI",
            dependencies: [
                .product(name: "GraithProtocol", package: "shared"),
                .product(name: "GraithRemoteKit", package: "shared"),
                .product(name: "GraithTerminalCore", package: "shared"),
                .product(name: "CGhosttyVT", package: "shared"),
                .product(name: "GraithDesign", package: "shared"),
            ]
            // libghostty-vt links transitively via the shared package's
            // GhosttyVt binaryTarget (a checksummed remote .xcframework SPM
            // resolves + links automatically) — no -L hack needed.
        ),
        .testTarget(
            name: "GraithGUITests",
            dependencies: [
                "GraithGUI",
                .product(name: "GraithProtocol", package: "shared"),
            ]
        ),
    ]
)
