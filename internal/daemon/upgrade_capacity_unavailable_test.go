//go:build libghostty && (!cgo || (!darwin && !linux))

package daemon

import "testing"

func TestUnavailableLibghosttyRejectsUpgradeAdoption(t *testing.T) {
	probe := CurrentUpgradeCapacityProbe()
	if probe.Backend != "unavailable" || probe.MaxSessions != 0 || probe.HelperHandoffVersion != 2 {
		t.Fatalf("unavailable probe = %+v", probe)
	}
	if err := validateAdoptionCapacity(&UpgradeManifest{}); err == nil {
		t.Fatal("unavailable tagged backend accepted adoption")
	}
}
