import CoreGraphics
import Foundation

/// User-tunable presentation preferences shared by the graith GUIs (issue #1254).
///
/// Presentation policy — how often the fleet re-polls, how long a tailnet probe
/// waits, the terminal font size, and the desktop sidebar width — was hard-coded
/// as literals scattered across `FleetModel`, `TailnetReachability`, the macOS
/// `SessionStore`/`ContentView`, and the iOS renderer, so it could only change by
/// rebuilding. This value type gathers the **tunable** knobs into one place with a
/// single set of defaults and lets them be overridden at runtime via
/// `UserDefaults` (see `init(userDefaults:)`), so a user — or a future settings
/// screen — can retune them without a rebuild. It mirrors the design of
/// `TerminalGestureConfig` (issue #1255).
///
/// ## Tunable vs. invariant
///
/// Only values that are genuinely a matter of preference live here. Values that
/// are **layout invariants** — correct for the platform rather than a matter of
/// taste — stay as documented constants next to the code they belong to, because
/// changing them would break rendering rather than merely retune it:
///
/// - **Display-link / redraw rate `60` fps** (`BaseTerminalNSView`,
///   `BaseTerminalUIView`) — hardware frame cadence, not a preference.
/// - **Catppuccin colour tokens** (`GraithDesign`, `Theme.terminal*`) — the
///   design system's palette; a shared identity, not a per-user knob.
/// - **Sidebar/font clamp bounds** (`minFontSize`…`maxSidebarWidth` below) —
///   the range a preference may take, not the preference itself.
public struct PresentationPreferences: Equatable, Sendable {

    /// Seconds between automatic fleet refreshes (the macOS desktop poll cadence;
    /// iOS refreshes on connect/foreground instead).
    public var fleetPollInterval: TimeInterval
    /// Seconds a tailnet reachability TCP probe waits before it is treated as
    /// unreachable. A short timeout keeps the probe cheap to call.
    public var reachabilityProbeTimeout: TimeInterval
    /// Terminal font point size.
    public var terminalFontSize: CGFloat
    /// Desktop (macOS) sidebar width in points.
    public var sidebarWidth: CGFloat

    // MARK: - Clamp bounds (invariants, not preferences)

    /// The smallest / largest legible terminal font size. Mirrors the historical
    /// `Theme.minFontSize` / `Theme.maxFontSize`.
    public static let minFontSize: CGFloat = 8
    public static let maxFontSize: CGFloat = 32
    /// The narrowest / widest a resized sidebar may become.
    public static let minSidebarWidth: CGFloat = 180
    public static let maxSidebarWidth: CGFloat = 600

    /// The shipped default for each field, kept as literal constants (not derived
    /// from `.default`) so the memberwise init and clamp helpers can fall back to
    /// them without re-entering `.default`'s own initialization.
    private enum Defaults {
        static let fleetPollInterval: TimeInterval = 2.0
        static let reachabilityProbeTimeout: TimeInterval = 3
        static let terminalFontSize: CGFloat = 13
        static let sidebarWidth: CGFloat = 260
        /// Floor for the cadence/timeout knobs so a zero can't busy-loop or
        /// instantly fail a probe.
        static let minInterval: TimeInterval = 0.1
    }

    /// The built-in defaults — the values the GUIs previously hard-coded. These
    /// mirror `FleetModel`'s 2s poll, `TailnetReachability`'s 3s probe,
    /// `Theme.defaultFontSize` (13), and `GraithDesign.sidebarWidth` (260).
    public static let `default` = PresentationPreferences(
        fleetPollInterval: Defaults.fleetPollInterval,
        reachabilityProbeTimeout: Defaults.reachabilityProbeTimeout,
        terminalFontSize: Defaults.terminalFontSize,
        sidebarWidth: Defaults.sidebarWidth
    )

    /// Memberwise init with clamping so an out-of-range value (from user defaults
    /// or a caller) can't wedge the UI. Cadences/timeouts are floored at a small
    /// positive value so a zero can't busy-loop or instantly fail a probe.
    ///
    /// A non-finite value (`NaN`, ±infinity) is replaced with the product default
    /// *before* clamping: `min`/`max` don't reject NaN — their result depends on
    /// argument order — so a stored NaN would otherwise slip straight through the
    /// range clamp and feed non-finite geometry into the UI (#1323).
    public init(fleetPollInterval: TimeInterval = PresentationPreferences.default.fleetPollInterval,
                reachabilityProbeTimeout: TimeInterval = PresentationPreferences.default.reachabilityProbeTimeout,
                terminalFontSize: CGFloat = PresentationPreferences.default.terminalFontSize,
                sidebarWidth: CGFloat = PresentationPreferences.default.sidebarWidth) {
        self.fleetPollInterval = max(Defaults.minInterval,
                                     Self.finite(fleetPollInterval, fallback: Defaults.fleetPollInterval))
        self.reachabilityProbeTimeout = max(Defaults.minInterval,
                                            Self.finite(reachabilityProbeTimeout, fallback: Defaults.reachabilityProbeTimeout))
        self.terminalFontSize = Self.clampFontSize(terminalFontSize)
        self.sidebarWidth = Self.clampSidebarWidth(sidebarWidth)
    }

    /// Replace a non-finite value (`NaN`, ±infinity) with a safe fallback so it
    /// can't slip through a subsequent `min`/`max` range clamp (#1323).
    private static func finite<T: BinaryFloatingPoint>(_ value: T, fallback: T) -> T {
        value.isFinite ? value : fallback
    }

    // MARK: - UserDefaults

    /// The `UserDefaults` keys each preference is read from / written to.
    /// Namespaced under `graith.presentation.` so a settings screen (or
    /// `defaults write`) can override them without colliding with other state.
    public enum Key {
        public static let fleetPollInterval = "graith.presentation.fleetPollInterval"
        public static let reachabilityProbeTimeout = "graith.presentation.reachabilityProbeTimeout"
        public static let terminalFontSize = "graith.presentation.terminalFontSize"
        public static let sidebarWidth = "graith.presentation.sidebarWidth"
    }

    /// Load from `UserDefaults`, falling back to `.default` for any key that is
    /// absent. Present-but-out-of-range values are clamped by the memberwise init.
    /// A key is treated as set only when actually present, so `.default` is used
    /// rather than reading a spurious `0` for a missing key.
    public init(userDefaults defaults: UserDefaults) {
        let base = PresentationPreferences.default
        func value(_ key: String, _ fallback: Double) -> Double {
            defaults.object(forKey: key) == nil ? fallback : defaults.double(forKey: key)
        }
        self.init(
            fleetPollInterval: value(Key.fleetPollInterval, base.fleetPollInterval),
            reachabilityProbeTimeout: value(Key.reachabilityProbeTimeout, base.reachabilityProbeTimeout),
            terminalFontSize: CGFloat(value(Key.terminalFontSize, Double(base.terminalFontSize))),
            sidebarWidth: CGFloat(value(Key.sidebarWidth, Double(base.sidebarWidth)))
        )
    }

    /// Persist the (already-clamped) preferences to `UserDefaults`. A settings
    /// screen writes individual keys directly; this is the batch form used by
    /// callers that hold a whole value.
    public func write(to defaults: UserDefaults) {
        defaults.set(fleetPollInterval, forKey: Key.fleetPollInterval)
        defaults.set(reachabilityProbeTimeout, forKey: Key.reachabilityProbeTimeout)
        defaults.set(Double(terminalFontSize), forKey: Key.terminalFontSize)
        defaults.set(Double(sidebarWidth), forKey: Key.sidebarWidth)
    }

    /// Clamp an arbitrary font size to the supported range. Exposed so the
    /// platform font-size commands (⌘+/⌘-/reset) share one definition of the
    /// bounds instead of re-deriving them. A non-finite input falls back to the
    /// product default before clamping (#1323).
    public static func clampFontSize(_ size: CGFloat) -> CGFloat {
        min(max(finite(size, fallback: Defaults.terminalFontSize), minFontSize), maxFontSize)
    }

    /// Clamp an arbitrary sidebar width to the supported range. A non-finite
    /// input falls back to the product default before clamping (#1323).
    public static func clampSidebarWidth(_ width: CGFloat) -> CGFloat {
        min(max(finite(width, fallback: Defaults.sidebarWidth), minSidebarWidth), maxSidebarWidth)
    }
}
