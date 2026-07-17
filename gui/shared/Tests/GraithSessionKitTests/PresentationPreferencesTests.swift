import Testing
import CoreGraphics
import Foundation
@testable import GraithSessionKit

// Covers the user-tunable GUI presentation preferences (#1254): the shipped
// defaults, the UserDefaults override + fallback path, the round-trip write, and
// the clamping that keeps an out-of-range override from wedging the UI.

@Suite("PresentationPreferences — tunable GUI presentation (#1254)")
struct PresentationPreferencesTests {

    /// A throwaway UserDefaults suite so a test's writes never touch the shared
    /// standard domain or leak between tests.
    private func scratchDefaults(_ name: String) -> UserDefaults {
        let defaults = UserDefaults(suiteName: name)!
        defaults.removePersistentDomain(forName: name)
        return defaults
    }

    @Test func defaultMatchesShippedValues() {
        let p = PresentationPreferences.default
        #expect(p.fleetPollInterval == 2.0)
        #expect(p.reachabilityProbeTimeout == 3)
        #expect(p.terminalFontSize == 13)
        #expect(p.sidebarWidth == 260)
    }

    @Test func emptyUserDefaultsFallsBackToDefault() {
        let defaults = scratchDefaults("presentation.empty.braw")
        #expect(PresentationPreferences(userDefaults: defaults) == .default)
    }

    @Test func presentKeysOverrideDefault() {
        let defaults = scratchDefaults("presentation.override.canny")
        defaults.set(5.0, forKey: PresentationPreferences.Key.fleetPollInterval)
        defaults.set(20.0, forKey: PresentationPreferences.Key.terminalFontSize)

        let p = PresentationPreferences(userDefaults: defaults)
        #expect(p.fleetPollInterval == 5.0)
        #expect(p.terminalFontSize == 20)
        // Untouched keys still inherit the shipped defaults.
        #expect(p.reachabilityProbeTimeout == PresentationPreferences.default.reachabilityProbeTimeout)
        #expect(p.sidebarWidth == PresentationPreferences.default.sidebarWidth)
    }

    @Test func outOfRangeOverridesAreClamped() {
        let defaults = scratchDefaults("presentation.clamp.thrawn")
        defaults.set(0.0, forKey: PresentationPreferences.Key.fleetPollInterval)
        defaults.set(-1.0, forKey: PresentationPreferences.Key.reachabilityProbeTimeout)
        defaults.set(2.0, forKey: PresentationPreferences.Key.terminalFontSize)      // below minFontSize
        defaults.set(9999.0, forKey: PresentationPreferences.Key.sidebarWidth)       // above maxSidebarWidth

        let p = PresentationPreferences(userDefaults: defaults)
        #expect(p.fleetPollInterval == 0.1)                                          // max(0.1, 0)
        #expect(p.reachabilityProbeTimeout == 0.1)                                   // max(0.1, -1)
        #expect(p.terminalFontSize == PresentationPreferences.minFontSize)           // clamped up
        #expect(p.sidebarWidth == PresentationPreferences.maxSidebarWidth)           // clamped down
    }

    @Test func writeThenReadRoundTrips() {
        let defaults = scratchDefaults("presentation.roundtrip.dreich")
        let p = PresentationPreferences(
            fleetPollInterval: 4,
            reachabilityProbeTimeout: 6,
            terminalFontSize: 18,
            sidebarWidth: 320
        )
        p.write(to: defaults)
        #expect(PresentationPreferences(userDefaults: defaults) == p)
    }

    @Test func clampHelpersMatchBounds() {
        #expect(PresentationPreferences.clampFontSize(2) == PresentationPreferences.minFontSize)
        #expect(PresentationPreferences.clampFontSize(999) == PresentationPreferences.maxFontSize)
        #expect(PresentationPreferences.clampFontSize(14) == 14)
        #expect(PresentationPreferences.clampSidebarWidth(10) == PresentationPreferences.minSidebarWidth)
        #expect(PresentationPreferences.clampSidebarWidth(10000) == PresentationPreferences.maxSidebarWidth)
        #expect(PresentationPreferences.clampSidebarWidth(300) == 300)
    }

    // MARK: - Non-finite normalization (#1323)

    /// The memberwise init must replace every non-finite value with the product
    /// default before clamping, so `min`/`max` (whose result depends on argument
    /// order for NaN) can't leak NaN/±inf into font/layout/cadence geometry.
    @Test(arguments: [CGFloat.nan, .infinity, -.infinity])
    func directInitNormalizesNonFiniteToDefault(_ bad: CGFloat) {
        let p = PresentationPreferences(
            fleetPollInterval: TimeInterval(bad),
            reachabilityProbeTimeout: TimeInterval(bad),
            terminalFontSize: bad,
            sidebarWidth: bad)
        #expect(p.fleetPollInterval == PresentationPreferences.default.fleetPollInterval)
        #expect(p.reachabilityProbeTimeout == PresentationPreferences.default.reachabilityProbeTimeout)
        #expect(p.terminalFontSize == PresentationPreferences.default.terminalFontSize)
        #expect(p.sidebarWidth == PresentationPreferences.default.sidebarWidth)
        // Every stored value is finite regardless of the bad input.
        #expect(p.fleetPollInterval.isFinite)
        #expect(p.reachabilityProbeTimeout.isFinite)
        #expect(p.terminalFontSize.isFinite)
        #expect(p.sidebarWidth.isFinite)
    }

    /// The static clamp helpers share the same normalization so the font-size
    /// commands (⌘+/⌘-/reset) and sidebar resize can't be fed a stored NaN.
    @Test(arguments: [CGFloat.nan, .infinity, -.infinity])
    func clampHelpersNormalizeNonFiniteToDefault(_ bad: CGFloat) {
        #expect(PresentationPreferences.clampFontSize(bad) == PresentationPreferences.default.terminalFontSize)
        #expect(PresentationPreferences.clampSidebarWidth(bad) == PresentationPreferences.default.sidebarWidth)
    }

    /// A `UserDefaults`-persisted NaN (what `double(forKey:)` returns for a
    /// malformed override) must recover to the safe default rather than remaining
    /// stored as non-finite geometry across launches.
    @Test(arguments: [Double.nan, .infinity, -.infinity])
    func userDefaultsNonFiniteRecoversToDefault(_ bad: Double) {
        let defaults = scratchDefaults("presentation.nonfinite.strath.\(bad)")
        defaults.set(bad, forKey: PresentationPreferences.Key.fleetPollInterval)
        defaults.set(bad, forKey: PresentationPreferences.Key.reachabilityProbeTimeout)
        defaults.set(bad, forKey: PresentationPreferences.Key.terminalFontSize)
        defaults.set(bad, forKey: PresentationPreferences.Key.sidebarWidth)

        let p = PresentationPreferences(userDefaults: defaults)
        #expect(p == .default)
    }
}
