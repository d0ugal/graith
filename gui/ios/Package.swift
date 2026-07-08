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
        .library(name: "GraithClientAPI", targets: ["GraithClientAPI"]),
        .library(name: "GraithMobileKit", targets: ["GraithMobileKit"]),
        .library(name: "GraithMobileUI", targets: ["GraithMobileUI"]),
        .library(name: "GraithTerminalUIKit", targets: ["GraithTerminalUIKit"]),
        .library(name: "GraithMobileMock", targets: ["GraithMobileMock"]),
        .library(name: "GraithMobileReal", targets: ["GraithMobileReal"]),
        .library(name: "GraithMobileRealTerminal", targets: ["GraithMobileRealTerminal"]),
        .executable(name: "GraithMobileApp", targets: ["GraithMobileApp"]),
    ],
    dependencies: [
        // The shared cross-platform core (../shared): GraithProtocol (transport +
        // wire client) and GraithTerminalCore (libghostty-vt wrapper). The real
        // adapters below bridge this tree's GraithClientAPI boundary onto it.
        .package(path: "../shared"),
    ],
    targets: [
        // The boundary re-homes DeviceKeySigner onto GraithProtocol's (design
        // §B.2.4): the two protocols are identical, so a typealias unifies them
        // and lets DeviceIdentity be injected straight into GraithProtocolClient.
        .target(
            name: "GraithClientAPI",
            dependencies: [.product(name: "GraithProtocol", package: "shared")]
        ),
        .target(
            name: "GraithMobileKit",
            dependencies: ["GraithClientAPI"]
        ),
        .target(
            name: "GraithTerminalUIKit",
            dependencies: ["GraithClientAPI"]
        ),
        .target(
            name: "GraithMobileMock",
            dependencies: ["GraithClientAPI", "GraithMobileKit"]
        ),
        // Real adapters bridging the GraithClientAPI boundary onto the shared
        // GraithProtocolClient: RealHostClientFactory / RealHostClient (wraps the
        // actor), RealAttachSession (wraps AttachSession), RealPairing, and the
        // wire-model mapping (GraithProtocol.* -> GraithClientAPI.*). Depends only
        // on GraithProtocol (pure Foundation+Network), so it — and the app that
        // links it — compile for both host and the iOS-simulator SDK without
        // libghostty. See NEEDS-IOS-VALIDATION.md.
        .target(
            name: "GraithMobileReal",
            dependencies: [
                "GraithClientAPI",
                .product(name: "GraithProtocol", package: "shared"),
            ]
        ),
        // The real TerminalCoreDriving adapter, backed by GraithTerminalCore's
        // GhosttyTerminalState. Isolated in its own target that NO executable in
        // this package links, because GraithTerminalCore -> CGhosttyVT ->
        // libghostty-vt.a is macOS-only + unpinned (Task 13). As a library it
        // type-checks against the real core (compiled, never final-linked), so
        // the adapter is verified here without breaking the app's iOS-sim link.
        // Wiring it into the live app is unblocked once the pinned .xcframework
        // lands (Task 13). See NEEDS-IOS-VALIDATION.md.
        .target(
            name: "GraithMobileRealTerminal",
            dependencies: [
                "GraithClientAPI",
                .product(name: "GraithTerminalCore", package: "shared"),
            ]
        ),
        .target(
            name: "GraithMobileUI",
            dependencies: [
                "GraithClientAPI", "GraithMobileKit", "GraithTerminalUIKit",
                .product(name: "GraithDesign", package: "shared"),
            ]
        ),
        .testTarget(
            name: "GraithMobileKitTests",
            dependencies: ["GraithMobileKit", "GraithMobileMock"]
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
            dependencies: ["GraithClientAPI", "GraithMobileKit", "GraithMobileMock",
                           "GraithMobileUI", "GraithTerminalUIKit", "GraithMobileReal",
                           .product(name: "GraithProtocol", package: "shared")]
        ),
        // The launchable iOS app. @main SwiftUI App presenting the shared
        // RootView. Built for the simulator SDK and bundled into a .app by
        // build-ios-app.sh / `make run` (see NEEDS-IOS-VALIDATION.md).
        .executableTarget(
            name: "GraithMobileApp",
            dependencies: ["GraithClientAPI", "GraithMobileKit", "GraithMobileMock",
                           "GraithMobileUI", "GraithMobileReal"]
        ),
    ]
)
