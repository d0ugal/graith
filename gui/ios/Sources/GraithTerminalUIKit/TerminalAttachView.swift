import SwiftUI
import GraithClientAPI
#if canImport(UIKit)
import UIKit
#endif

/// The embeddable attach surface (Task 20). A cross-platform SwiftUI view that
/// wraps the iOS `BaseTerminalUIView` and overlays connection phase (connecting,
/// detached+reattach, attached-elsewhere, backgrounded). The app builds the
/// `TerminalAttachViewModel` (with the real client + `GhosttyTerminalState`
/// adapter) and supplies a `TerminalRenderer` factory at integration.
///
/// On macOS this shows an "unavailable" note — the macOS app uses gui-poc's
/// native `NSView` terminal, not this UIKit view (design §C.0).
public struct TerminalAttachView: View {
    @ObservedObject private var viewModel: TerminalAttachViewModel
    #if canImport(UIKit)
    private let makeRenderer: () -> TerminalRenderer
    #endif

    #if canImport(UIKit)
    public init(viewModel: TerminalAttachViewModel, makeRenderer: @escaping () -> TerminalRenderer) {
        self.viewModel = viewModel
        self.makeRenderer = makeRenderer
    }
    #else
    public init(viewModel: TerminalAttachViewModel) {
        self.viewModel = viewModel
    }
    #endif

    public var body: some View {
        ZStack {
            surface
            overlay
        }
    }

    @ViewBuilder
    private var surface: some View {
        #if canImport(UIKit)
        TerminalScreen(viewModel: viewModel, makeRenderer: makeRenderer)
            .ignoresSafeArea(.container, edges: .bottom)
        #else
        Color.black.overlay(
            Text("Interactive attach is available on iOS/iPadOS.")
                .foregroundStyle(.secondary)
        )
        #endif
    }

    @ViewBuilder
    private var overlay: some View {
        switch viewModel.phase {
        case .connecting:
            phaseCard { ProgressView(); Text("Attaching…") }
        case .attachedElsewhere:
            phaseCard {
                Image(systemName: "rectangle.on.rectangle.slash")
                Text("Already attached in another window.")
                Text("graith allows one attach per session.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        case .detached(let reason):
            phaseCard {
                Image(systemName: "bolt.horizontal.circle")
                Text(reason)
                Button("Reattach") { Task { await viewModel.reattach() } }
                    .buttonStyle(.borderedProminent)
            }
        case .failed(let msg):
            phaseCard {
                Image(systemName: "exclamationmark.triangle")
                Text(msg)
                Button("Retry") { Task { await viewModel.reattach() } }
            }
        case .idle, .attached:
            if viewModel.backgrounded {
                phaseCard { Text("Paused while in background") }
            }
        }
    }

    @ViewBuilder
    private func phaseCard<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        VStack(spacing: 10) { content() }
            .padding(20)
            .background(.ultraThinMaterial)
            .cornerRadius(12)
            .padding()
    }
}

#if canImport(UIKit)
/// Bridges `BaseTerminalUIView` into SwiftUI and manages attach/detach on
/// appear/disappear plus background/foreground transitions.
struct TerminalScreen: UIViewRepresentable {
    let viewModel: TerminalAttachViewModel
    let makeRenderer: () -> TerminalRenderer

    func makeUIView(context: Context) -> BaseTerminalUIView {
        let view = BaseTerminalUIView(viewModel: viewModel, renderer: makeRenderer())
        context.coordinator.observe(view: view, viewModel: viewModel)
        Task { await view.start() }
        return view
    }

    func updateUIView(_ uiView: BaseTerminalUIView, context: Context) {}

    static func dismantleUIView(_ uiView: BaseTerminalUIView, coordinator: Coordinator) {
        coordinator.stopObserving()
        Task { await uiView.stop() }
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    /// Observes app lifecycle notifications to pause/reattach the socket, since
    /// iOS suspends sockets when the app is backgrounded (design §C.3).
    final class Coordinator {
        private var observers: [NSObjectProtocol] = []

        func observe(view: BaseTerminalUIView, viewModel: TerminalAttachViewModel) {
            let nc = NotificationCenter.default
            observers.append(nc.addObserver(forName: UIApplication.didEnterBackgroundNotification,
                                            object: nil, queue: .main) { _ in
                MainActor.assumeIsolated { viewModel.applicationDidEnterBackground() }
            })
            observers.append(nc.addObserver(forName: UIApplication.willEnterForegroundNotification,
                                            object: nil, queue: .main) { _ in
                Task { await viewModel.applicationWillEnterForeground() }
            })
        }

        func stopObserving() {
            observers.forEach { NotificationCenter.default.removeObserver($0) }
            observers.removeAll()
        }
    }
}
#endif
