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

    // MARK: - Non-finite input (#1323)
    //
    // IEEE NaN/±∞ slips through nested min/max clamps (`min(max(.nan, lo), hi)`
    // is `.nan`; `max(0.1, .infinity)` is `.infinity`), so without explicit
    // normalization a malformed persisted preference could feed non-finite
    // geometry into the UI and persist across launches. Every presentation value
    // must recover to the product default before range clamping.

    /// The non-finite doubles the clamps must survive, labelled for diagnostics.
    private static let nonFiniteCases: [(label: String, value: Double)] = [
        ("NaN", .nan),
        ("+Inf", .infinity),
        ("-Inf", -.infinity),
    ]

    @Test func directInitializerNormalizesNonFinite() {
        for c in Self.nonFiniteCases {
            let p = PresentationPreferences(
                fleetPollInterval: c.value,
                reachabilityProbeTimeout: c.value,
                terminalFontSize: CGFloat(c.value),
                sidebarWidth: CGFloat(c.value)
            )
            #expect(p.fleetPollInterval.isFinite, "fleetPollInterval finite for \(c.label)")
            #expect(p.reachabilityProbeTimeout.isFinite, "reachabilityProbeTimeout finite for \(c.label)")
            #expect(p.terminalFontSize.isFinite, "terminalFontSize finite for \(c.label)")
            #expect(p.sidebarWidth.isFinite, "sidebarWidth finite for \(c.label)")
            // Recovery target is the product default (not a bound), then clamp.
            #expect(p.fleetPollInterval == PresentationPreferences.default.fleetPollInterval, "\(c.label)")
            #expect(p.reachabilityProbeTimeout == PresentationPreferences.default.reachabilityProbeTimeout, "\(c.label)")
            #expect(p.terminalFontSize == PresentationPreferences.default.terminalFontSize, "\(c.label)")
            #expect(p.sidebarWidth == PresentationPreferences.default.sidebarWidth, "\(c.label)")
        }
    }

    @Test func clampHelpersNormalizeNonFinite() {
        for c in Self.nonFiniteCases {
            let font = PresentationPreferences.clampFontSize(CGFloat(c.value))
            let width = PresentationPreferences.clampSidebarWidth(CGFloat(c.value))
            #expect(font.isFinite, "clampFontSize finite for \(c.label)")
            #expect(width.isFinite, "clampSidebarWidth finite for \(c.label)")
            #expect(font == PresentationPreferences.default.terminalFontSize, "clampFontSize \(c.label)")
            #expect(width == PresentationPreferences.default.sidebarWidth, "clampSidebarWidth \(c.label)")
        }
    }

    @Test func nonFiniteUserDefaultsRoundTripsToDefault() {
        for c in Self.nonFiniteCases {
            let defaults = scratchDefaults("presentation.nonfinite.\(c.label).bothy")
            defaults.set(c.value, forKey: PresentationPreferences.Key.fleetPollInterval)
            defaults.set(c.value, forKey: PresentationPreferences.Key.reachabilityProbeTimeout)
            defaults.set(c.value, forKey: PresentationPreferences.Key.terminalFontSize)
            defaults.set(c.value, forKey: PresentationPreferences.Key.sidebarWidth)

            let p = PresentationPreferences(userDefaults: defaults)
            #expect(p == .default, "non-finite override recovers to default for \(c.label)")

            // A recovered value re-persists as finite, so the next launch is clean.
            p.write(to: defaults)
            #expect(PresentationPreferences(userDefaults: defaults) == .default, "\(c.label) re-read")
        }
    }

    @Test func finiteMinMaxUnchangedByNormalization() {
        // Finite extremes still clamp to the bounds exactly as before — the
        // normalization must not disturb ordinary in-range behavior.
        #expect(PresentationPreferences.clampFontSize(PresentationPreferences.minFontSize)
                == PresentationPreferences.minFontSize)
        #expect(PresentationPreferences.clampFontSize(PresentationPreferences.maxFontSize)
                == PresentationPreferences.maxFontSize)
        #expect(PresentationPreferences.clampSidebarWidth(PresentationPreferences.minSidebarWidth)
                == PresentationPreferences.minSidebarWidth)
        #expect(PresentationPreferences.clampSidebarWidth(PresentationPreferences.maxSidebarWidth)
                == PresentationPreferences.maxSidebarWidth)
        // A finite +∞-adjacent large magnitude clamps to max, not to the default.
        #expect(PresentationPreferences.clampFontSize(.greatestFiniteMagnitude)
                == PresentationPreferences.maxFontSize)
        #expect(PresentationPreferences.clampSidebarWidth(-.greatestFiniteMagnitude)
                == PresentationPreferences.minSidebarWidth)
    }
}
