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
                .product(name: "GraithTerminalCore", package: "shared"),
                .product(name: "CGhosttyVT", package: "shared"),
                .product(name: "GraithDesign", package: "shared"),
            ],
            linkerSettings: [
                // CGhosttyVT's own -LLibraries is resolved against this
                // package's build dir; point the linker at the shared package's
                // Libraries/ so libghostty-vt.a is found. Temporary — goes away
                // with the pinned .xcframework (../shared TODO, Task 13).
                .unsafeFlags(["-L../shared/Libraries"]),
            ]
        ),
        .testTarget(
            name: "GraithGUITests",
            dependencies: ["GraithGUI"],
            linkerSettings: [
                // Same reason as the app target: the test binary links GraithGUI,
                // which pulls in libghostty-vt.a from the shared package.
                .unsafeFlags(["-L../shared/Libraries"]),
            ]
        ),
    ]
)
