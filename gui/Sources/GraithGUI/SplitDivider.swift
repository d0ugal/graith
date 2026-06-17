import SwiftUI

/// Ghostty-inspired draggable split divider between sidebar and terminal.
struct ResizableSplitView<Leading: View, Trailing: View>: View {
    let leading: Leading
    let trailing: Trailing

    @Binding var sidebarWidth: CGFloat
    let minWidth: CGFloat = 180
    let maxWidth: CGFloat = 450

    init(
        sidebarWidth: Binding<CGFloat>,
        @ViewBuilder leading: () -> Leading,
        @ViewBuilder trailing: () -> Trailing
    ) {
        self._sidebarWidth = sidebarWidth
        self.leading = leading()
        self.trailing = trailing()
    }

    var body: some View {
        HStack(spacing: 0) {
            leading
                .frame(width: sidebarWidth)

            DividerHandle(sidebarWidth: $sidebarWidth, minWidth: minWidth, maxWidth: maxWidth)

            trailing
        }
    }
}

struct DividerHandle: View {
    @Binding var sidebarWidth: CGFloat
    let minWidth: CGFloat
    let maxWidth: CGFloat
    @State private var isDragging = false
    @State private var dragStartWidth: CGFloat?

    var body: some View {
        Rectangle()
            .fill(isDragging ? Theme.surface1 : Theme.surface0)
            .frame(width: isDragging ? 2 : 1)
            .contentShape(Rectangle().inset(by: -3))
            .onHover { hovering in
                if hovering {
                    NSCursor.resizeLeftRight.push()
                } else {
                    NSCursor.pop()
                }
            }
            .gesture(
                DragGesture(minimumDistance: 1)
                    .onChanged { value in
                        isDragging = true
                        if dragStartWidth == nil { dragStartWidth = sidebarWidth }
                        let newWidth = (dragStartWidth ?? sidebarWidth) + value.translation.width
                        sidebarWidth = min(max(newWidth, minWidth), maxWidth)
                    }
                    .onEnded { _ in
                        isDragging = false
                        dragStartWidth = nil
                    }
            )
            .onTapGesture(count: 2) {
                withAnimation(.easeInOut(duration: 0.2)) {
                    sidebarWidth = Theme.sidebarWidth
                }
            }
    }
}
