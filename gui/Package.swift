// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "GraithGUI",
    platforms: [.macOS(.v14)],
    dependencies: [],
    targets: [
        .executableTarget(
            name: "GraithGUI",
            dependencies: ["CGhosttyVT"]
        ),
        .target(
            name: "CGhosttyVT",
            path: "Sources/CGhosttyVT",
            publicHeadersPath: "include",
            cSettings: [
                .define("GHOSTTY_STATIC"),
            ],
            linkerSettings: [
                .unsafeFlags(["-LLibraries"]),
                .linkedLibrary("ghostty-vt"),
            ]
        ),
    ]
)
