import Foundation

/// The low-level file operations a durable write is composed of, split into
/// individually-injectable steps so the receipt-protocol store (issue #1299) can
/// be tested for both ordering and failure:
///
///   1. write bytes to a fresh temp file in the *same directory* as the target,
///   2. fsync that file so its bytes reach stable storage,
///   3. atomically rename it over the destination, and
///   4. fsync the parent directory so the rename's new directory entry is durable.
///
/// A plain `Data.write(to:options:.atomic)` performs an atomic *replace* but does
/// not fsync the file or the directory, so a power loss after it returns can lose
/// the credential the daemon is about to commit against. Splitting the steps lets
/// tests assert all four complete *before* `pair_ack` is released and force a
/// failure at any step (which must stay pre-ack and restore the prior state).
public protocol DurableFileOps: Sendable {
    /// Write `data` to a fresh temp file in the same directory as `destination`
    /// (so the later rename is atomic on a single filesystem), returning its URL.
    func writeTemp(_ data: Data, forDestination destination: URL) throws -> URL
    /// fsync the file at `url`, forcing its bytes to stable storage.
    func syncFile(at url: URL) throws
    /// Atomically replace `destination` with the file at `source` (`rename(2)`).
    func replaceItem(at destination: URL, with source: URL) throws
    /// fsync the directory at `url`, forcing a rename's new directory entry to
    /// stable storage.
    func syncDirectory(at url: URL) throws
    /// Best-effort removal of a leftover temp file after a failed write.
    func discardTemp(at url: URL)
    /// Durably remove the file at `url`: unlink then fsync the parent directory so
    /// the removal survives power loss. No-op if the file is absent. Used for the
    /// pending-pairing journal, whose lingering presence would permanently block
    /// new pairings (issue #1299).
    func removeItem(at url: URL) throws
}

public extension DurableFileOps {
    /// Durably write `data` to `url`: temp file → file fsync → atomic rename →
    /// parent-directory fsync. The destination is only ever replaced by a fully
    /// written-and-synced temp, so a failure before the rename leaves it byte-for-
    /// byte untouched; on any failure the temp is discarded and the error
    /// propagates (issue #1299).
    func writeDurably(_ data: Data, to url: URL) throws {
        let tmp = try writeTemp(data, forDestination: url)
        do {
            try syncFile(at: tmp)
            try replaceItem(at: url, with: tmp)
            try syncDirectory(at: url.deletingLastPathComponent())
        } catch {
            discardTemp(at: tmp)
            throw error
        }
    }
}

/// The production ``DurableFileOps``: real POSIX `write`/`fsync`/`rename`. Used by
/// ``HostRegistry`` so a successful pairing store is genuinely durable before the
/// receipt ack (issue #1299).
public struct POSIXFileOps: DurableFileOps {
    public init() {}

    public func writeTemp(_ data: Data, forDestination destination: URL) throws -> URL {
        let dir = destination.deletingLastPathComponent()
        let dirExisted = FileManager.default.fileExists(atPath: dir.path)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        if !dirExisted {
            // The parent directory was just created — fsync ITS parent so the new
            // directory entry is itself durable before we commit anything into it
            // (issue #1299). Otherwise a power loss could lose the whole directory.
            let grandparent = dir.deletingLastPathComponent()
            if FileManager.default.fileExists(atPath: grandparent.path) {
                try Self.fsyncPath(grandparent.path, what: "grandparent directory")
            }
        }
        let tmp = dir.appendingPathComponent(".\(destination.lastPathComponent).tmp-\(UUID().uuidString)")
        try data.write(to: tmp)
        return tmp
    }

    public func syncFile(at url: URL) throws {
        try Self.fsyncPath(url.path, what: "file")
    }

    public func replaceItem(at destination: URL, with source: URL) throws {
        if rename(source.path, destination.path) != 0 {
            throw Self.errnoError("rename", destination)
        }
    }

    public func syncDirectory(at url: URL) throws {
        try Self.fsyncPath(url.path, what: "directory")
    }

    public func discardTemp(at url: URL) {
        try? FileManager.default.removeItem(at: url)
    }

    public func removeItem(at url: URL) throws {
        guard FileManager.default.fileExists(atPath: url.path) else { return }
        try FileManager.default.removeItem(at: url)
        // fsync the parent so the unlink is itself durable — a lingering journal
        // would permanently block new pairings (issue #1299).
        try Self.fsyncPath(url.deletingLastPathComponent().path, what: "directory after remove")
    }

    /// Open `path` read-only and fsync it. A directory opened read-only can be
    /// fsync'd on both Darwin and Linux, which is how the rename's new dirent is
    /// forced durable.
    private static func fsyncPath(_ path: String, what: String) throws {
        let fd = open(path, O_RDONLY)
        if fd < 0 { throw errnoError("open \(what) for fsync", path) }
        defer { close(fd) }
        if fsync(fd) != 0 { throw errnoError("fsync \(what)", path) }
    }

    private static func errnoError(_ op: String, _ url: URL) -> Error {
        errnoError(op, url.path)
    }

    private static func errnoError(_ op: String, _ path: String) -> Error {
        let code = errno
        return NSError(domain: NSPOSIXErrorDomain, code: Int(code), userInfo: [
            NSLocalizedDescriptionKey: "\(op) \(path): \(String(cString: strerror(code)))",
        ])
    }
}
