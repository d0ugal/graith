import SwiftUI

/// A quiet, centered empty state matching the desktop's "No sessions /
/// Create session" and "Select a session" treatments: a faint SF Symbol, a
/// monospaced title, an optional subtitle, and an optional accent action.
public struct GraithEmptyState: View {
    private let systemImage: String
    private let title: String
    private let subtitle: String?
    private let actionTitle: String?
    private let action: (() -> Void)?

    public init(
        systemImage: String,
        title: String,
        subtitle: String? = nil,
        actionTitle: String? = nil,
        action: (() -> Void)? = nil
    ) {
        self.systemImage = systemImage
        self.title = title
        self.subtitle = subtitle
        self.actionTitle = actionTitle
        self.action = action
    }

    public var body: some View {
        VStack(spacing: 12) {
            Image(systemName: systemImage)
                .font(.system(size: 42))
                .foregroundStyle(GraithDesign.surface1)

            Text(title)
                .font(GraithDesign.mono(.title3))
                .foregroundStyle(GraithDesign.faint)

            if let subtitle {
                Text(subtitle)
                    .font(GraithDesign.mono(.caption))
                    .foregroundStyle(GraithDesign.surface1)
                    .multilineTextAlignment(.center)
            }

            if let actionTitle, let action {
                Button(actionTitle, action: action)
                    .buttonStyle(.plain)
                    .font(GraithDesign.mono(.callout))
                    .foregroundStyle(GraithDesign.accent)
                    .padding(.top, 2)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding()
    }
}
