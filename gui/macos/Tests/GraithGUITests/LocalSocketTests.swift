import XCTest
@testable import GraithGUI

final class LocalSocketTests: XCTestCase {
    func testConfiguredDataDirRebasesSocketToDataRoot() throws {
        let hame = try makeHame()
        let dataDir = hame.appendingPathComponent(".graith", isDirectory: true)
        let socket = dataDir.appendingPathComponent("graith.sock")
        try writeConfig("data_dir = \"~/.graith\"\n", beneath: hame)

        let resolution = GraithLocalSocket.resolve(environment: ["HOME": hame.path])

        XCTAssertEqual(resolution.socketPath, socket.path)
        XCTAssertEqual(resolution.source, .config)
    }

    func testConfiguredDataDirIsDeterministicBeforeSocketExists() throws {
        let hame = try makeHame()
        let dataDir = hame.appendingPathComponent("bothy", isDirectory: true)
        try writeConfig("data_dir = \"\(dataDir.path)\" # the local daemon\n", beneath: hame)

        let resolution = GraithLocalSocket.resolve(environment: ["HOME": hame.path])

        XCTAssertEqual(resolution.socketPath, dataDir.appendingPathComponent("graith.sock").path)
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
        XCTAssertEqual(resolution.socketPath, dataDir.appendingPathComponent("graith.sock").path)
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
            hame.appendingPathComponent("Library/Application Support/graith-canny/graith.sock").path
        )
    }

    func testDefaultDarwinRuntimeIsApplicationSupportWithoutRunComponent() throws {
        let hame = try makeHame()

        let resolution = GraithLocalSocket.resolve(environment: ["HOME": hame.path])

        XCTAssertEqual(
            resolution.socketPath,
            hame.appendingPathComponent("Library/Application Support/graith/graith.sock").path
        )
    }

    func testXDGDataHomeAloneDoesNotMoveIndependentDarwinRuntime() throws {
        let hame = try makeHame()

        let resolution = GraithLocalSocket.resolve(environment: [
            "HOME": hame.path,
            "XDG_DATA_HOME": hame.appendingPathComponent("loch").path,
        ])

        XCTAssertEqual(
            resolution.socketPath,
            hame.appendingPathComponent("Library/Application Support/graith/graith.sock").path
        )
    }

    func testTildeRuntimeEnvironmentPathExpandsLikeXDG() throws {
        let hame = try makeHame()

        let resolution = GraithLocalSocket.resolve(environment: [
            "HOME": hame.path,
            "XDG_RUNTIME_DIR": "~/wynd",
        ])

        XCTAssertEqual(
            resolution.socketPath,
            hame.appendingPathComponent("wynd/graith/graith.sock").path
        )
    }

    func testMatchingXDGRootsRebaseWithConfiguredDataDir() throws {
        let hame = try makeHame()
        let xdg = hame.appendingPathComponent("glen", isDirectory: true)
        let dataDir = hame.appendingPathComponent("bothy", isDirectory: true)
        try writeConfig("data_dir = \"\(dataDir.path)\"\n", beneath: hame)

        let resolution = GraithLocalSocket.resolve(environment: [
            "HOME": hame.path,
            "XDG_DATA_HOME": xdg.path,
            "XDG_RUNTIME_DIR": xdg.path,
        ])

        XCTAssertEqual(resolution.socketPath, dataDir.appendingPathComponent("graith.sock").path)
    }

    func testDefaultProfileFallsBackToLegacyMacOSConfig() throws {
        let hame = try makeHame()
        let legacy = hame.appendingPathComponent("Library/Application Support/graith/config.toml")
        try FileManager.default.createDirectory(
            at: legacy.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        try "data_dir = \"~/.graith\"\n".write(to: legacy, atomically: true, encoding: .utf8)

        let resolution = GraithLocalSocket.resolve(environment: ["HOME": hame.path])

        XCTAssertEqual(resolution.configPath, legacy.path)
        XCTAssertEqual(resolution.socketPath, hame.appendingPathComponent(".graith/graith.sock").path)
    }

    func testInvalidProfileIsReported() throws {
        let hame = try makeHame()

        for profile in ["default", "Braw", "glen/kirk", "-braw", "braw-", String(repeating: "a", count: 33)] {
            let resolution = GraithLocalSocket.resolve(
                environment: ["HOME": hame.path],
                profileOverride: profile
            )
            XCTAssertNotNil(resolution.profileError, profile)
        }
        XCTAssertNil(GraithLocalSocket.resolve(
            environment: ["HOME": hame.path],
            profileOverride: "bonnie-profile"
        ).profileError)
    }

    func testQuotedDataDirKeyAndUnicodeEscapeMatchTOML() throws {
        let hame = try makeHame()
        try writeConfig(#""data_dir" = "/tmp/\u0062raw" # comment"# + "\n", beneath: hame)

        let resolution = GraithLocalSocket.resolve(environment: ["HOME": hame.path])

        XCTAssertEqual(resolution.socketPath, "/tmp/braw/graith.sock")
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
