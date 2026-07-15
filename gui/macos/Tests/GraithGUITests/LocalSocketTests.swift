import XCTest
@testable import GraithGUI

final class LocalSocketTests: XCTestCase {
    func testConfiguredDataDirFindsLegacySocketAtDataRoot() throws {
        let hame = try makeHame()
        let dataDir = hame.appendingPathComponent(".graith", isDirectory: true)
        try FileManager.default.createDirectory(at: dataDir, withIntermediateDirectories: true)
        let socket = dataDir.appendingPathComponent("graith.sock")
        XCTAssertTrue(FileManager.default.createFile(atPath: socket.path, contents: Data()))
        try writeConfig("data_dir = \"~/.graith\"\n", beneath: hame)

        let resolution = GraithLocalSocket.resolve(environment: ["HOME": hame.path])

        XCTAssertEqual(resolution.socketPath, socket.path)
        XCTAssertEqual(resolution.source, .config)
    }

    func testConfiguredDataDirUsesRunDirectoryForCurrentLayout() throws {
        let hame = try makeHame()
        let dataDir = hame.appendingPathComponent("bothy", isDirectory: true)
        try writeConfig("data_dir = \"\(dataDir.path)\" # the local daemon\n", beneath: hame)

        let resolution = GraithLocalSocket.resolve(environment: ["HOME": hame.path])

        XCTAssertEqual(resolution.socketPath, dataDir.appendingPathComponent("run/graith.sock").path)
        XCTAssertEqual(resolution.source, .config)
    }

    func testConfigPathOverrideSelectsAnotherConfig() throws {
        let hame = try makeHame()
        let config = hame.appendingPathComponent("canny.toml")
        let dataDir = hame.appendingPathComponent("croft", isDirectory: true)
        try "data_dir = '\(dataDir.path)'\n".write(to: config, atomically: true, encoding: .utf8)

        let resolution = GraithLocalSocket.resolve(
            environment: ["HOME": hame.path],
            configPathOverride: config.path
        )

        XCTAssertEqual(resolution.configPath, config.path)
        XCTAssertEqual(resolution.socketPath, dataDir.appendingPathComponent("run/graith.sock").path)
        XCTAssertEqual(resolution.source, .config)
    }

    func testSocketPathOverrideWinsOverConfig() throws {
        let hame = try makeHame()
        try writeConfig("data_dir = \"~/.graith\"\n", beneath: hame)
        let override = hame.appendingPathComponent("wynd/braw.sock").path

        let resolution = GraithLocalSocket.resolve(
            environment: ["HOME": hame.path],
            socketPathOverride: override
        )

        XCTAssertEqual(resolution.socketPath, override)
        XCTAssertEqual(resolution.source, .override)
    }

    func testProfileChoosesProfileConfigAndRuntimeDirectory() throws {
        let hame = try makeHame()
        let runtime = hame.appendingPathComponent("run", isDirectory: true)

        let resolution = GraithLocalSocket.resolve(environment: [
            "HOME": hame.path,
            "GRAITH_PROFILE": "braw",
            "XDG_RUNTIME_DIR": runtime.path,
        ])

        XCTAssertEqual(
            resolution.configPath,
            hame.appendingPathComponent(".config/graith-braw/config.toml").path
        )
        XCTAssertEqual(
            resolution.socketPath,
            runtime.appendingPathComponent("graith-braw/graith.sock").path
        )
        XCTAssertEqual(resolution.source, .environment)
    }

    func testProfileOverrideWinsOverEnvironmentAndSelectsProfileConfig() throws {
        let hame = try makeHame()

        let resolution = GraithLocalSocket.resolve(
            environment: ["HOME": hame.path, "GRAITH_PROFILE": "dreich"],
            profileOverride: "canny"
        )

        XCTAssertEqual(resolution.profile, "canny")
        XCTAssertEqual(
            resolution.configPath,
            hame.appendingPathComponent(".config/graith-canny/config.toml").path
        )
        XCTAssertEqual(
            resolution.socketPath,
            hame.appendingPathComponent("Library/Application Support/graith-canny/run/graith.sock").path
        )
    }

    private func makeHame() throws -> URL {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("hame-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: url) }
        return url
    }

    private func writeConfig(_ contents: String, beneath hame: URL) throws {
        let directory = hame.appendingPathComponent(".config/graith", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try contents.write(
            to: directory.appendingPathComponent("config.toml"),
            atomically: true,
            encoding: .utf8
        )
    }
}
