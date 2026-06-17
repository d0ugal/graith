import Foundation
import Combine

@MainActor
class SessionStore: ObservableObject {
    @Published var sessions: [Session] = []
    @Published var selectedSessionID: String?
    @Published var splitSessionID: String?
    @Published var isSplit: Bool = false
    /// Which pane is focused: .primary (left) or .secondary (right)
    @Published var focusedPane: SplitPane = .primary
    @Published var error: String?

    enum SplitPane {
        case primary, secondary
    }
    enum RendererType: String, CaseIterable {
        case ghosttyCoreText = "Ghostty (Core Text)"
        case ghosttyMetal = "Ghostty (Metal)"
    }

    @Published var renderer: RendererType = .ghosttyCoreText
    @Published var fontSize: CGFloat = Theme.defaultFontSize

    let grPath: String
    private var timer: Timer?
    private var refreshGeneration: UInt64 = 0

    init() {
        self.grPath = SessionStore.findGR()
        refresh()
        startPolling()
    }

    var selectedSession: Session? {
        guard let id = selectedSessionID else { return nil }
        return sessions.first { $0.id == id }
    }

    var splitSession: Session? {
        guard let id = splitSessionID else { return nil }
        return sessions.first { $0.id == id }
    }

    // Sessions grouped by repo name for sidebar display
    var sessionsByRepo: [(repo: String, sessions: [Session])] {
        let grouped = Dictionary(grouping: sessions) { $0.repoName }
        return grouped.sorted { $0.key < $1.key }
            .map { (repo: $0.key, sessions: $0.value.sorted { $0.name < $1.name }) }
    }

    func roots(in sessions: [Session]) -> [Session] {
        let ids = Set(sessions.map(\.id))
        return sessions.filter {
            $0.parentID == nil || $0.parentID!.isEmpty || !ids.contains($0.parentID!)
        }
    }

    func children(of parentID: String, in sessions: [Session]) -> [Session] {
        sessions.filter { $0.parentID == parentID }
    }

    func selectSession(_ session: Session) {
        if isSplit && focusedPane == .secondary {
            if session.id != selectedSessionID {
                splitSessionID = session.id
            }
        } else {
            if session.id != splitSessionID || !isSplit {
                selectedSessionID = session.id
            }
        }
    }

    // MARK: - Split Pane

    /// Open the split view. The right pane starts with the next available
    /// session (different from the primary) to avoid single-attach conflicts.
    func splitRight() {
        guard !isSplit else { return }
        isSplit = true
        let sorted = sessions.sorted { $0.name < $1.name }
        splitSessionID = sorted.first { $0.id != selectedSessionID }?.id
        focusedPane = .secondary
    }

    /// Close the split view. The primary selection is preserved.
    func closeSplit() {
        isSplit = false
        splitSessionID = nil
        focusedPane = .primary
    }

    // MARK: - Session Actions

    func stopSession(_ session: Session) {
        runGRAction(["stop", session.name])
    }

    func resumeSession(_ session: Session) {
        runGRAction(["resume", session.name])
    }

    func deleteSession(_ session: Session) {
        if selectedSessionID == session.id {
            selectedSessionID = nil
        }
        if splitSessionID == session.id {
            splitSessionID = nil
        }
        runGRAction(["delete", session.name])
    }

    func restartSession(_ session: Session) {
        runGRAction(["restart", session.name])
    }

    func createSession(args: [String], completion: @escaping (Result<Session?, Error>) -> Void) {
        Task {
            do {
                let data = try await runGR(args)
                let session = try? JSONDecoder().decode(Session.self, from: data)
                refresh()
                completion(.success(session))
            } catch {
                completion(.failure(error))
            }
        }
    }

    func selectSessionByIndex(_ index: Int) {
        let flat = sessions.sorted { $0.name < $1.name }
        guard index >= 0 && index < flat.count else { return }
        selectSession(flat[index])
    }

    func selectAdjacentSession(offset: Int) {
        let flat = sessions.sorted { $0.name < $1.name }
        guard !flat.isEmpty else { return }
        let currentID = isSplit && focusedPane == .secondary ? splitSessionID : selectedSessionID
        guard let currentID, let idx = flat.firstIndex(where: { $0.id == currentID }) else {
            selectSession(flat.first!)
            return
        }
        var newIdx = (idx + offset + flat.count) % flat.count
        let otherID = isSplit ? (focusedPane == .secondary ? selectedSessionID : splitSessionID) : nil
        if let otherID, flat[newIdx].id == otherID {
            newIdx = (newIdx + offset + flat.count) % flat.count
        }
        selectSession(flat[newIdx])
    }

    // MARK: - Font Size

    func increaseFontSize() {
        let newSize = min(fontSize + 1, Theme.maxFontSize)
        if newSize != fontSize {
            fontSize = newSize
            NotificationCenter.default.post(name: .terminalFontSizeChanged, object: fontSize)
        }
    }

    func decreaseFontSize() {
        let newSize = max(fontSize - 1, Theme.minFontSize)
        if newSize != fontSize {
            fontSize = newSize
            NotificationCenter.default.post(name: .terminalFontSizeChanged, object: fontSize)
        }
    }

    func resetFontSize() {
        if fontSize != Theme.defaultFontSize {
            fontSize = Theme.defaultFontSize
            NotificationCenter.default.post(name: .terminalFontSizeChanged, object: fontSize)
        }
    }

    // MARK: - Polling

    func startPolling() {
        timer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: true) { [weak self] _ in
            Task { @MainActor in
                self?.refresh()
            }
        }
    }

    func refresh() {
        refreshGeneration &+= 1
        let gen = refreshGeneration
        Task {
            do {
                let data = try await runGR(["list", "--json"])
                guard gen == refreshGeneration else { return }
                let list = try JSONDecoder().decode(SessionList.self, from: data)
                self.sessions = list.sessions
                self.error = nil

                if let id = selectedSessionID,
                   !list.sessions.contains(where: { $0.id == id }) {
                    selectedSessionID = nil
                }
                if let id = splitSessionID,
                   !list.sessions.contains(where: { $0.id == id }) {
                    splitSessionID = nil
                }
            } catch {
                guard gen == refreshGeneration else { return }
                self.error = error.localizedDescription
            }
        }
    }

    // MARK: - Process Helpers

    private func runGR(_ args: [String]) async throws -> Data {
        let grPath = self.grPath
        let env = Self.buildEnv()
        return try await Task.detached {
            let process = Process()
            process.executableURL = URL(fileURLWithPath: grPath)
            process.arguments = args
            process.environment = env
            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = FileHandle.nullDevice
            try process.run()
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            process.waitUntilExit()
            guard process.terminationStatus == 0 else {
                throw NSError(domain: "GraithGUI", code: Int(process.terminationStatus),
                              userInfo: [NSLocalizedDescriptionKey: "gr exited with status \(process.terminationStatus)"])
            }
            return data
        }.value
    }

    private func runGRAction(_ args: [String]) {
        let grPath = self.grPath
        let env = Self.buildEnv()
        Task {
            await Task.detached {
                let process = Process()
                process.executableURL = URL(fileURLWithPath: grPath)
                process.arguments = args
                process.environment = env
                process.standardOutput = FileHandle.nullDevice
                process.standardError = FileHandle.nullDevice
                try? process.run()
                process.waitUntilExit()
            }.value
            refresh()
        }
    }

    static func buildEnv() -> [String: String] {
        var env = ProcessInfo.processInfo.environment
        // Ensure homebrew is in PATH (macOS GUI apps don't inherit shell PATH)
        let extra = "/opt/homebrew/bin:/usr/local/bin"
        env["PATH"] = extra + ":" + (env["PATH"] ?? "/usr/bin:/bin")
        env["GR_AGENT_MODE"] = "1"
        return env
    }

    static func findGR() -> String {
        let candidates = [
            "/opt/homebrew/bin/gr",
            "/usr/local/bin/gr",
            NSHomeDirectory() + "/.local/bin/gr",
            NSHomeDirectory() + "/go/bin/gr",
        ]
        for path in candidates {
            if FileManager.default.isExecutableFile(atPath: path) {
                return path
            }
        }

        // Try `which`
        let which = Process()
        which.executableURL = URL(fileURLWithPath: "/usr/bin/which")
        which.arguments = ["gr"]
        let pipe = Pipe()
        which.standardOutput = pipe
        which.standardError = FileHandle.nullDevice
        try? which.run()
        which.waitUntilExit()
        let output = String(data: pipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8)?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !output.isEmpty && FileManager.default.isExecutableFile(atPath: output) {
            return output
        }

        return "/opt/homebrew/bin/gr"
    }
}
