//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package daemon

import "testing"

func TestLibghosttyUpgradeAdoptionCapacityBoundary(t *testing.T) {
	probe := CurrentUpgradeCapacityProbe()
	if probe.Backend != "limited" || probe.MaxSessions != 64 || probe.HelperHandoffVersion != 2 {
		t.Fatalf("active native probe = %+v", probe)
	}
	sessions := make([]UpgradeSession, 65)
	for index := range sessions {
		sessions[index].HasPTY = true
	}
	if err := validateAdoptionCapacity(&UpgradeManifest{Sessions: sessions[:64]}); err != nil {
		t.Fatalf("64-session manifest rejected: %v", err)
	}
	if err := validateAdoptionCapacity(&UpgradeManifest{Sessions: sessions}); err == nil {
		t.Fatal("65-session manifest exceeded the helper capacity")
	}
}
