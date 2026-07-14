// swift-tools-version: 5.9
import PackageDescription

// Isolated package for the iOS track of the universal app (issue #628,
// Tasks 18-20). Kept separate from ../Package.swift (macOS track) so the two
// tracks don't collide. Targets both platforms because the SwiftUI shell is
// universal; the UIKit terminal input (GraithTerminalUIKit) is iOS/Catalyst
// only and is guarded with #if canImport(UIKit).
let package = Package(
    name: "GraithMobile",
    platforms: [
        .iOS(.v16),
        .macOS(.v14),
    ],
    products: [
        .library(name: "GraithMobileUI", targets: ["GraithMobileUI"]),
        .library(name: "GraithTerminalUIKit", targets: ["GraithTerminalUIKit"]),
        .library(name: "GraithMobileMock", targets: ["GraithMobileMock"]),
        .library(name: "GraithMobileRealTerminal", targets: ["GraithMobileRealTerminal"]),
        .executable(name: "GraithMobileApp", targets: ["GraithMobileApp"]),
    ],
    dependencies: [
        // The shared cross-platform core (../shared). The session/feature layer
        // (`GraithSessionKit`, #1131) now provides the capability boundary,
        // per-host connection view-model, multi-host FleetModel, and the real
        // client wrapping GraithProtocolClient — all previously duplicated in
        // this tree's GraithClientAPI / GraithMobileKit / GraithMobileReal
        // targets, which are folded away. Host/pairing/identity live in
        // GraithRemoteKit; the VT wrapper in GraithTerminalCore.
        .package(path: "../shared"),
    ],
    targets: [
        .target(
            name: "GraithTerminalUIKit",
            dependencies: [.product(name: "GraithSessionKit", package: "shared")]
        ),
        .target(
            name: "GraithMobileMock",
            dependencies: [
                .product(name: "GraithSessionKit", package: "shared"),
                .product(name: "GraithRemoteKit", package: "shared"),
                .product(name: "GraithProtocol", package: "shared"),
            ]
        ),
        // The real TerminalCoreDriving adapter, backed by GraithTerminalCore's
        // GhosttyTerminalState. The SHA-pinned libghostty-vt.xcframework
        // (../shared) provides the ios-arm64-simulator slice, so
        // GraithTerminalCore links on iOS (Task 13 done).
        .target(
            name: "GraithMobileRealTerminal",
            dependencies: [
                .product(name: "GraithSessionKit", package: "shared"),
                "GraithTerminalUIKit",
                .product(name: "GraithTerminalCore", package: "shared"),
            ]
        ),
        .target(
            name: "GraithMobileUI",
            dependencies: [
                .product(name: "GraithSessionKit", package: "shared"),
                .product(name: "GraithRemoteKit", package: "shared"),
                "GraithTerminalUIKit", "GraithMobileRealTerminal",
                .product(name: "GraithDesign", package: "shared"),
                .product(name: "GraithTerminalCore", package: "shared"),
            ]
        ),
        .testTarget(
            name: "GraithTerminalUIKitTests",
            dependencies: ["GraithTerminalUIKit", "GraithMobileMock"]
        ),
        // A runnable smoke check for the SDK-neutral logic. XCTest is not
        // available in the Command Line Tools toolchain (no full Xcode), so the
        // XCTest targets above only compile/run in Xcode/CI; this executable
        // lets us actually exercise the logic here via `swift run`.
        .executableTarget(
            name: "GraithMobileSmoke",
            dependencies: ["GraithMobileMock", "GraithMobileUI", "GraithTerminalUIKit",
                           .product(name: "GraithSessionKit", package: "shared"),
                           .product(name: "GraithRemoteKit", package: "shared"),
                           .product(name: "GraithProtocol", package: "shared")]
        ),
        // The launchable iOS app. @main SwiftUI App presenting the shared
        // RootView. Built for the simulator SDK and bundled into a .app by
        // build-ios-app.sh / `make run` (see NEEDS-IOS-VALIDATION.md).
        .executableTarget(
            name: "GraithMobileApp",
            dependencies: ["GraithMobileMock", "GraithMobileUI", "GraithMobileRealTerminal",
                           .product(name: "GraithSessionKit", package: "shared"),
                           .product(name: "GraithRemoteKit", package: "shared")]
        ),
    ]
)
