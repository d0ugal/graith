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

    // MARK: - Default magnitudes

    /// The individual default magnitudes. Kept as standalone constants (rather
    /// than only inside `.default`) so the non-finite fallback and the clamp
    /// helpers can reach them without re-entering `.default`'s own lazy
    /// initialization while it is still being constructed.
    static let defaultFleetPollInterval: TimeInterval = 2.0
    static let defaultReachabilityProbeTimeout: TimeInterval = 3
    static let defaultFontSize: CGFloat = 13
    static let defaultSidebarWidth: CGFloat = 260

    /// The built-in defaults — the values the GUIs previously hard-coded. These
    /// mirror `FleetModel`'s 2s poll, `TailnetReachability`'s 3s probe,
    /// `Theme.defaultFontSize` (13), and `GraithDesign.sidebarWidth` (260).
    public static let `default` = PresentationPreferences(
        fleetPollInterval: defaultFleetPollInterval,
        reachabilityProbeTimeout: defaultReachabilityProbeTimeout,
        terminalFontSize: defaultFontSize,
        sidebarWidth: defaultSidebarWidth
    )

    /// Memberwise init with clamping so an out-of-range value (from user defaults
    /// or a caller) can't wedge the UI. Cadences/timeouts are floored at a small
    /// positive value so a zero can't busy-loop or instantly fail a probe.
    ///
    /// Every value is first normalized against non-finite input (`NaN`, `±∞`);
    /// see `finite(_:default:)` for why nested `min`/`max` clamps alone are not
    /// enough (issue #1323).
    public init(fleetPollInterval: TimeInterval = PresentationPreferences.default.fleetPollInterval,
                reachabilityProbeTimeout: TimeInterval = PresentationPreferences.default.reachabilityProbeTimeout,
                terminalFontSize: CGFloat = PresentationPreferences.default.terminalFontSize,
                sidebarWidth: CGFloat = PresentationPreferences.default.sidebarWidth) {
        self.fleetPollInterval = max(0.1, Self.finite(fleetPollInterval, default: Self.defaultFleetPollInterval))
        self.reachabilityProbeTimeout = max(0.1, Self.finite(reachabilityProbeTimeout, default: Self.defaultReachabilityProbeTimeout))
        self.terminalFontSize = Self.clampFontSize(terminalFontSize)
        self.sidebarWidth = Self.clampSidebarWidth(sidebarWidth)
    }

    // MARK: - Non-finite normalization

    /// Recover a non-finite value (`NaN`, `+∞`, `-∞`) to a safe fallback before
    /// range clamping. This is load-bearing: `UserDefaults.double(forKey:)` and
    /// arbitrary callers can yield non-finite doubles, and IEEE comparison
    /// semantics let those slip straight through the nested `min`/`max` clamps —
    /// `min(max(.nan, lo), hi)` is `.nan`, and `max(0.1, .infinity)` is
    /// `.infinity` — so a corrupt stored preference would poison UI geometry and
    /// persist across launches (issue #1323).
    ///
    /// We recover to the **product default** rather than a nearest bound: a
    /// non-finite value carries no meaningful magnitude to clamp toward, so the
    /// least surprising recovery is the value the GUI ships with. Finite values
    /// pass through untouched and reach the ordinary min/max clamp unchanged.
    static func finite<F: FloatingPoint>(_ value: F, default fallback: F) -> F {
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
    /// bounds instead of re-deriving them. A non-finite input recovers to the
    /// default font size before clamping (issue #1323).
    public static func clampFontSize(_ size: CGFloat) -> CGFloat {
        min(max(finite(size, default: defaultFontSize), minFontSize), maxFontSize)
    }

    /// Clamp an arbitrary sidebar width to the supported range. A non-finite
    /// input recovers to the default sidebar width before clamping (issue #1323).
    public static func clampSidebarWidth(_ width: CGFloat) -> CGFloat {
        min(max(finite(width, default: defaultSidebarWidth), minSidebarWidth), maxSidebarWidth)
    }
}
