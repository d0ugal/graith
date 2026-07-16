import Testing
import CoreGraphics
import Foundation
@testable import GraithSessionKit

// Covers the user-tunable iOS terminal gesture physics config (#1255): the
// shipped defaults, the UserDefaults override + fallback path, and the clamping
// that keeps an out-of-range override from wedging the physics.

@Suite("TerminalGestureConfig — tunable gesture physics (#1255)")
struct TerminalGestureConfigTests {

    /// A throwaway UserDefaults suite so a test's writes never touch the shared
    /// standard domain or leak between tests.
    private func scratchDefaults(_ name: String) -> UserDefaults {
        let defaults = UserDefaults(suiteName: name)!
        defaults.removePersistentDomain(forName: name)
        return defaults
    }

    @Test func defaultMatchesShippedValues() {
        let c = TerminalGestureConfig.default
        #expect(c.scrollFriction == 4.5)
        #expect(c.scrollMomentumCutoff == 24)
        #expect(c.scrollSpringStiffness == 220)
        #expect(c.scrollSpringDamping == 26)
        #expect(c.spaceActivationThreshold == 22)
        #expect(c.spaceInitialRepeatDelay == 0.5)
        #expect(c.spaceRepeatInterval == 0.1)
        #expect(c.spaceDirectionHysteresis == 1.5)
        #expect(c.selectionLongPressDuration == 0.3)
    }

    @Test func emptyUserDefaultsFallsBackToDefault() {
        let defaults = scratchDefaults("gesture.empty.braw")
        #expect(TerminalGestureConfig(userDefaults: defaults) == .default)
    }

    @Test func presentKeysOverrideDefault() {
        let defaults = scratchDefaults("gesture.override.canny")
        defaults.set(9.0, forKey: TerminalGestureConfig.Key.scrollFriction)
        defaults.set(40.0, forKey: TerminalGestureConfig.Key.spaceActivationThreshold)
        defaults.set(0.25, forKey: TerminalGestureConfig.Key.spaceRepeatInterval)
        defaults.set(0.6, forKey: TerminalGestureConfig.Key.selectionLongPressDuration)

        let c = TerminalGestureConfig(userDefaults: defaults)
        #expect(c.scrollFriction == 9.0)
        #expect(c.spaceActivationThreshold == 40)
        #expect(c.spaceRepeatInterval == 0.25)
        #expect(c.selectionLongPressDuration == 0.6)
        // Untouched keys still inherit the shipped defaults.
        #expect(c.scrollSpringStiffness == TerminalGestureConfig.default.scrollSpringStiffness)
        #expect(c.spaceDirectionHysteresis == TerminalGestureConfig.default.spaceDirectionHysteresis)
    }

    @Test func aPresentZeroIsNormalizedNotTreatedAsAbsent() {
        // object(forKey:) distinguishes "set to 0" from "missing". Settle-
        // critical zeroes use their positive stability floors, not the default.
        let defaults = scratchDefaults("gesture.zero.dreich")
        defaults.set(0.0, forKey: TerminalGestureConfig.Key.scrollFriction)
        defaults.set(0.0, forKey: TerminalGestureConfig.Key.scrollMomentumCutoff)
        defaults.set(0.0, forKey: TerminalGestureConfig.Key.scrollSpringStiffness)
        defaults.set(0.0, forKey: TerminalGestureConfig.Key.scrollSpringDamping)
        let c = TerminalGestureConfig(userDefaults: defaults)
        #expect(c.scrollFriction == 1)
        #expect(c.scrollMomentumCutoff == 1)
        #expect(c.scrollSpringStiffness == 30)
        #expect(c.scrollSpringDamping == 4)
    }

    @Test func outOfRangeOverridesAreClamped() {
        let defaults = scratchDefaults("gesture.clamp.thrawn")
        defaults.set(-5.0, forKey: TerminalGestureConfig.Key.scrollFriction)
        defaults.set(-2.0, forKey: TerminalGestureConfig.Key.scrollMomentumCutoff)
        defaults.set(-100.0, forKey: TerminalGestureConfig.Key.scrollSpringStiffness)
        defaults.set(-10.0, forKey: TerminalGestureConfig.Key.scrollSpringDamping)
        defaults.set(0.0, forKey: TerminalGestureConfig.Key.spaceActivationThreshold)
        defaults.set(0.0, forKey: TerminalGestureConfig.Key.spaceRepeatInterval)
        defaults.set(0.5, forKey: TerminalGestureConfig.Key.spaceDirectionHysteresis)
        defaults.set(-1.0, forKey: TerminalGestureConfig.Key.selectionLongPressDuration)

        let c = TerminalGestureConfig(userDefaults: defaults)
        #expect(c.scrollFriction == 1)
        #expect(c.scrollMomentumCutoff == 1)
        #expect(c.scrollSpringStiffness == 30)
        #expect(c.scrollSpringDamping == 4)
        #expect(c.spaceActivationThreshold == 1)       // max(1, 0)
        #expect(c.spaceRepeatInterval == 0.001)        // max(0.001, 0)
        #expect(c.spaceDirectionHysteresis == 1)       // max(1, 0.5)
        #expect(c.selectionLongPressDuration == 0)     // max(0, -1)
    }

    @Test func memberwiseInitAlsoClamps() {
        let c = TerminalGestureConfig(spaceActivationThreshold: -3, spaceDirectionHysteresis: 0)
        #expect(c.spaceActivationThreshold == 1)
        #expect(c.spaceDirectionHysteresis == 1)
    }

    @Test func nonFiniteOverridesFallBackToShippedValues() {
        let defaults = scratchDefaults("gesture.nonfinite.bothy")
        defaults.set(Double.nan, forKey: TerminalGestureConfig.Key.scrollFriction)
        defaults.set(Double.infinity, forKey: TerminalGestureConfig.Key.scrollMomentumCutoff)
        defaults.set(-Double.infinity, forKey: TerminalGestureConfig.Key.scrollSpringStiffness)
        defaults.set(Double.nan, forKey: TerminalGestureConfig.Key.scrollSpringDamping)

        let c = TerminalGestureConfig(userDefaults: defaults)
        #expect(c.scrollFriction == TerminalGestureConfig.default.scrollFriction)
        #expect(c.scrollMomentumCutoff == TerminalGestureConfig.default.scrollMomentumCutoff)
        #expect(c.scrollSpringStiffness == TerminalGestureConfig.default.scrollSpringStiffness)
        #expect(c.scrollSpringDamping == TerminalGestureConfig.default.scrollSpringDamping)
    }

    @Test func extremeFinitePhysicsValuesUseStableCeilings() {
        let c = TerminalGestureConfig(
            scrollFriction: .greatestFiniteMagnitude,
            scrollMomentumCutoff: .greatestFiniteMagnitude,
            scrollSpringStiffness: .greatestFiniteMagnitude,
            scrollSpringDamping: .greatestFiniteMagnitude)
        #expect(c.scrollFriction == 60)
        #expect(c.scrollMomentumCutoff == 10_000)
        #expect(c.scrollSpringStiffness == 400)
        #expect(c.scrollSpringDamping == 29)
    }
}
