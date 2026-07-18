//go:build libghostty && cgo && (darwin || linux)

package daemon

import "testing"

func TestLibghosttyUpgradeAdoptionCapacityBoundary(t *testing.T) {
	probe := CurrentUpgradeCapacityProbe()
	if probe.Backend != "limited" || probe.MaxSessions != 64 || probe.HelperHandoffVersion != 2 {
		t.Fatalf("active native probe = %+v", probe)
	}
	if err := validateAdoptionCapacity(&UpgradeManifest{Sessions: make([]UpgradeSession, 64)}); err != nil {
		t.Fatalf("64-session manifest rejected: %v", err)
	}
	if err := validateAdoptionCapacity(&UpgradeManifest{Sessions: make([]UpgradeSession, 65)}); err == nil {
		t.Fatal("65-session manifest exceeded the helper capacity")
	}
}
