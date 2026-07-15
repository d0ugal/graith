import SwiftUI
import AppKit
import GraithProtocol

/// The inbox / compose surface for a session (`gr msg`). Shows the session's
/// direct-message conversation (both directions) and a compose field to send a
/// message to its inbox, plus a mark-as-read (ack) action. Reachable from the
/// session context menu. Routed through the shared `FleetModel` messaging API so
/// this and the iOS equivalent share one implementation (issue #898).
struct MessagesSheet: View {
    @EnvironmentObject var store: SessionStore
    @Environment(\.dismiss) private var dismiss
    let session: Session

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
        VStack(spacing: 0) {
            PeekHeader(title: "Messages", subtitle: session.name,
                       reloadDisabled: loading,
                       reload: { Task { await load() } }) { dismiss() }

            Divider().background(Theme.surface0)

            if loading {
                VStack { ProgressView().controlSize(.large) }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let error {
                PeekError(message: error)
            } else if messages.isEmpty {
                PeekEmpty(systemImage: "bubble.left.and.bubble.right",
                          message: "No messages yet.\nSend one below.")
            } else {
                conversationList
            }

            if let notice {
                Text(notice)
                    .font(.system(size: 10, design: .monospaced))
                    .foregroundStyle(Theme.subtext0)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 12)
                    .padding(.top, 6)
            }

            Divider().background(Theme.surface0)
            composeBar
        }
        .frame(width: 640, height: 560)
        .background(Theme.mantle)
        .task(id: session.id) { await load() }
    }

    private var conversationList: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 10) {
                    ForEach(messages) { MessageRow(message: $0) }
                }
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .background(Theme.crust)
            .onAppear {
                // Start pinned to the newest message. The list only renders once
                // loading finishes with messages present, so the count-change
                // observer below never sees the initial 0→N transition (it fires
                // while the loading view is shown) — scroll here for first paint.
                if let last = messages.last { proxy.scrollTo(last.id, anchor: .bottom) }
            }
            .onChange(of: messages.count) { _, _ in
                // Keep the newest message in view after a subsequent load / send.
                if let last = messages.last { withAnimation { proxy.scrollTo(last.id, anchor: .bottom) } }
            }
        }
    }

    private var composeBar: some View {
        HStack(spacing: 8) {
            Button(action: { Task { await markRead() } }) {
                Image(systemName: "checkmark.circle")
                    .foregroundStyle(Theme.subtext0)
            }
            .buttonStyle(.plain)
            .disabled(loading || messages.isEmpty)
            .help("Mark inbox read")

            TextField("Message \(session.name)…", text: $draft, axis: .vertical)
                .textFieldStyle(.plain)
                .lineLimit(1...4)
                .font(.system(size: 12, design: .monospaced))
                .foregroundStyle(Theme.text)
                .padding(8)
                .background(Theme.surface0)
                .clipShape(RoundedRectangle(cornerRadius: 6))
                .onSubmit { Task { await send() } }

            Button(action: { Task { await send() } }) {
                if sending {
                    ProgressView().controlSize(.small)
                } else {
                    Image(systemName: "paperplane.fill")
                        .foregroundStyle(trimmedDraft.isEmpty ? Theme.overlay0 : Theme.blue)
                }
            }
            .buttonStyle(.plain)
            .keyboardShortcut(.return, modifiers: .command)
            .disabled(sending || trimmedDraft.isEmpty)
            .help("Send (⌘↵)")
        }
        .padding(12)
        .background(Theme.mantle)
    }

    private func load() async {
        generation += 1
        let gen = generation
        loading = true
        error = nil
        let loaded = await store.conversation(for: session)
        guard gen == generation else { return } // superseded by a newer load
        if let e = store.connection(ownerOf: session.id)?.lastError, loaded.isEmpty {
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
        let ok = await store.sendMessage(to: session, body: body)
        sending = false
        if ok {
            draft = ""
            await load()
        } else {
            error = store.connection(ownerOf: session.id)?.lastError ?? "Failed to send message."
        }
    }

    private func markRead() async {
        let ok = await store.ackInbox(for: session)
        notice = ok
            ? "Inbox marked read."
            : (store.connection(ownerOf: session.id)?.lastError ?? "Couldn't mark inbox read.")
    }
}

/// One message bubble: sender + timestamp header, then the body. System
/// (daemon-authored) notices read distinctly so they don't imply a repliable
/// sender.
private struct MessageRow: View {
    let message: ConversationMessage

    private var sender: String {
        let name = (message.senderName?.isEmpty == false) ? message.senderName! : message.senderID
        return name.isEmpty ? "unknown" : name
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                Text(sender)
                    .font(.system(size: 11, weight: .semibold, design: .monospaced))
                    .foregroundStyle(message.system == true ? Theme.peach : Theme.blue)
                if message.system == true {
                    Text("automated")
                        .font(.system(size: 9, design: .monospaced))
                        .foregroundStyle(Theme.overlay0)
                }
                Spacer()
                Text(Self.shortTime(message.createdAt))
                    .font(.system(size: 9, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
            }
            Text(message.body)
                .font(.system(size: 12, design: .monospaced))
                .foregroundStyle(Theme.subtext1)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(8)
        .background(Theme.surface0.opacity(message.system == true ? 0.4 : 0.7))
        .clipShape(RoundedRectangle(cornerRadius: 6))
    }

    /// Best-effort friendly time from an RFC3339 timestamp; falls back to the
    /// raw string if it can't be parsed.
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
