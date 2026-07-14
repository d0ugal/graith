import SwiftUI
import AppKit
import GraithProtocol

/// A non-attaching peek at a session's recent scrollback (`logs`). Fetches the
/// tail once on appear; a Reload button re-fetches. Read-only — this never
/// attaches, so it can't kick a desktop session (#1130).
struct LogsSheet: View {
    @EnvironmentObject var store: SessionStore
    @Environment(\.dismiss) private var dismiss
    let session: Session

    @State private var text = ""
    @State private var loading = true
    @State private var error: String?
    /// Bumped per load so a slow response from an earlier fetch can't overwrite
    /// a newer one (the shared logs RPC isn't cancellation-aware).
    @State private var generation = 0

    var body: some View {
        VStack(spacing: 0) {
            PeekHeader(title: "Logs", subtitle: session.name,
                       reloadDisabled: loading,
                       reload: { Task { await load() } }) { dismiss() }

            Divider().background(Theme.surface0)

            if loading {
                loadingView
            } else if let error {
                PeekError(message: error)
            } else if text.isEmpty {
                PeekEmpty(systemImage: "doc.plaintext", message: "No output yet.")
            } else {
                ScrollView([.vertical, .horizontal]) {
                    Text(text)
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundStyle(Theme.subtext1)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(12)
                }
                .background(Theme.crust)
            }
        }
        .frame(width: 720, height: 560)
        .background(Theme.mantle)
        .task(id: session.id) { await load() }
    }

    private var loadingView: some View {
        VStack { ProgressView().controlSize(.large) }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func load() async {
        generation += 1
        let gen = generation
        loading = true
        error = nil
        do {
            let loaded = try await store.fetchLogs(session)
            guard gen == generation else { return } // superseded by a newer load
            text = loaded
        } catch {
            guard gen == generation else { return }
            self.error = error.localizedDescription
        }
        loading = false
    }
}

/// A one-shot render of a session's current screen (`screen_snapshot`). Shows the
/// rendered frame as monospaced text plus its geometry, without attaching.
struct SnapshotSheet: View {
    @EnvironmentObject var store: SessionStore
    @Environment(\.dismiss) private var dismiss
    let session: Session

    @State private var snapshot: ScreenSnapshotResponseMsg?
    @State private var loading = true
    @State private var error: String?
    /// Bumped per load so a stale response can't overwrite a newer one.
    @State private var generation = 0

    var body: some View {
        VStack(spacing: 0) {
            PeekHeader(title: "Screen Snapshot", subtitle: session.name,
                       reloadDisabled: loading,
                       reload: { Task { await load() } }) { dismiss() }

            Divider().background(Theme.surface0)

            if loading {
                VStack { ProgressView().controlSize(.large) }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let error {
                PeekError(message: error)
            } else if let snapshot {
                VStack(spacing: 0) {
                    ScrollView([.vertical, .horizontal]) {
                        Text(snapshot.frame.isEmpty ? " " : snapshot.frame)
                            .font(.system(size: 11, design: .monospaced))
                            .foregroundStyle(Theme.text)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(12)
                    }
                    .background(Theme.crust)

                    HStack(spacing: 12) {
                        Label("\(snapshot.cols)\u{d7}\(snapshot.rows)", systemImage: "rectangle.split.3x3")
                        if snapshot.cursorVisible {
                            Label("cursor \(snapshot.cursorX),\(snapshot.cursorY)", systemImage: "cursorarrow")
                        }
                        Spacer()
                    }
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 6)
                    .background(Theme.mantle)
                }
            } else {
                PeekEmpty(systemImage: "rectangle.dashed", message: "No screen to show.")
            }
        }
        .frame(width: 720, height: 560)
        .background(Theme.mantle)
        .task(id: session.id) { await load() }
    }

    private func load() async {
        generation += 1
        let gen = generation
        loading = true
        error = nil
        do {
            let loaded = try await store.fetchSnapshot(session)
            guard gen == generation else { return } // superseded by a newer load
            snapshot = loaded
        } catch {
            guard gen == generation else { return }
            self.error = error.localizedDescription
        }
        loading = false
    }
}

// MARK: - Shared peek chrome

/// Header shared by the logs / snapshot peek sheets: title, session subtitle, a
/// reload button, and a close button.
struct PeekHeader: View {
    let title: String
    let subtitle: String
    var reloadDisabled: Bool = false
    let reload: () -> Void
    let close: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.system(.title3, design: .monospaced))
                    .fontWeight(.semibold)
                    .foregroundStyle(Theme.text)
                Text(subtitle)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
            }
            Spacer()
            Button(action: reload) {
                Image(systemName: "arrow.clockwise")
                    .foregroundStyle(reloadDisabled ? Theme.overlay0 : Theme.subtext0)
                    .font(.system(size: 14))
            }
            .buttonStyle(.plain)
            .disabled(reloadDisabled)
            .help("Reload")
            Button(action: close) {
                Image(systemName: "xmark.circle.fill")
                    .foregroundStyle(Theme.overlay0)
                    .font(.system(size: 18))
            }
            .buttonStyle(.plain)
        }
        .padding(20)
    }
}

struct PeekError: View {
    let message: String
    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.system(size: 32))
                .foregroundStyle(Theme.red)
            Text(message)
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(Theme.red)
                .multilineTextAlignment(.center)
        }
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

struct PeekEmpty: View {
    let systemImage: String
    let message: String
    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: systemImage)
                .font(.system(size: 32))
                .foregroundStyle(Theme.overlay0)
            Text(message)
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(Theme.overlay0)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
