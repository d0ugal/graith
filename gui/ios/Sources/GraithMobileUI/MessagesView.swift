import SwiftUI
import GraithSessionKit
import GraithProtocol

/// The inbox / compose surface for a session (`gr msg`): shows its direct-message
/// conversation (both directions) with a compose field to send to the session's
/// inbox and a mark-as-read (ack) action. Routed through the shared
/// `HostConnection`/`FleetModel` messaging API, so this and the macOS
/// `MessagesSheet` share one implementation (issue #898).
struct MessagesView: View {
    @ObservedObject var connection: HostConnection
    let session: SessionInfo
    @Environment(\.dismiss) private var dismiss

    @State private var messages: [ConversationMessage] = []
    @State private var draft = ""
    @State private var loading = true
    @State private var sending = false
    @State private var error: String?
    /// A transient one-line notice (e.g. the ack outcome) shown above the compose
    /// bar so an action isn't silently swallowed.
    @State private var notice: String?
    /// Bumped per load so a slow earlier fetch can't overwrite a newer one.
    @State private var generation = 0

    private var trimmedDraft: String {
        draft.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                content
                if let notice {
                    Text(notice)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 12)
                        .padding(.top, 6)
                }
                Divider()
                composeBar
            }
            .navigationTitle("Messages")
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
                ToolbarItem(placement: .cancellationAction) {
                    Button("Mark Read") { Task { await markRead() } }
                        .disabled(loading || messages.isEmpty)
                }
            }
            .task(id: session.id) { await load() }
        }
    }

    @ViewBuilder
    private var content: some View {
        if loading {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let error {
            ContentUnavailableCompat(
                title: "Couldn't load messages",
                systemImage: "exclamationmark.triangle",
                description: error
            )
        } else if messages.isEmpty {
            ContentUnavailableCompat(
                title: "No messages yet",
                systemImage: "bubble.left.and.bubble.right",
                description: "Send a message to \(session.name) below."
            )
        } else {
            ScrollViewReader { proxy in
                List {
                    ForEach(messages) { MessageRow(message: $0) }
                }
                .listStyle(.plain)
                .onAppear {
                    // Start pinned to the newest message: the list only renders
                    // after loading finishes with messages present, so the
                    // count-change observer below never sees the initial 0→N
                    // transition (it fires while the ProgressView is shown).
                    if let last = messages.last { proxy.scrollTo(last.id, anchor: .bottom) }
                }
                .onChange(of: messages.count) { _ in
                    if let last = messages.last { withAnimation { proxy.scrollTo(last.id, anchor: .bottom) } }
                }
            }
        }
    }

    private var composeBar: some View {
        HStack(spacing: 8) {
            TextField("Message \(session.name)…", text: $draft, axis: .vertical)
                .textFieldStyle(.roundedBorder)
                .lineLimit(1...4)
                .disabled(sending)
            Button {
                Task { await send() }
            } label: {
                if sending {
                    ProgressView()
                } else {
                    Image(systemName: "paperplane.fill")
                }
            }
            .buttonStyle(.borderedProminent)
            .disabled(sending || trimmedDraft.isEmpty)
        }
        .padding(12)
    }

    private func load() async {
        generation += 1
        let gen = generation
        loading = true
        error = nil
        let loaded = await connection.conversation(for: session)
        guard gen == generation else { return } // superseded by a newer load
        if loaded.isEmpty, let e = connection.lastError {
            error = e
        } else {
            messages = loaded
        }
        loading = false
    }

    private func send() async {
        let body = trimmedDraft
        guard !body.isEmpty, !sending else { return }
        sending = true
        notice = nil
        let ok = await connection.sendMessage(to: session, body: body)
        sending = false
        if ok {
            draft = ""
            await load()
        } else {
            error = connection.lastError ?? "Failed to send message."
        }
    }

    private func markRead() async {
        let ok = await connection.ackInbox(for: session)
        notice = ok ? "Inbox marked read." : (connection.lastError ?? "Couldn't mark inbox read.")
    }
}

/// One message: sender + timestamp, then the body. System (daemon-authored)
/// notices read distinctly so they don't imply a repliable sender.
private struct MessageRow: View {
    let message: ConversationMessage

    private var sender: String {
        let name = (message.senderName?.isEmpty == false) ? message.senderName! : message.senderID
        return name.isEmpty ? "unknown" : name
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Text(sender)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(message.system == true ? Color.orange : Color.accentColor)
                if message.system == true {
                    Text("automated")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Text(MessageRow.shortTime(message.createdAt))
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            Text(message.body)
                .font(.system(.footnote, design: .monospaced))
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.vertical, 4)
    }

    /// Best-effort friendly time from an RFC3339 timestamp; falls back to the raw
    /// string if it can't be parsed.
    static func shortTime(_ iso: String) -> String {
        let parser = ISO8601DateFormatter()
        parser.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let date = parser.date(from: iso) ?? ISO8601DateFormatter().date(from: iso)
        guard let date else { return iso }
        let out = DateFormatter()
        out.dateFormat = "MMM d, HH:mm"
        return out.string(from: date)
    }
}
