//go:build !libghostty

package daemon

import "testing"

func TestDefaultUpgradeAdoptionCapacityFailsClosed(t *testing.T) {
	probe := CurrentUpgradeCapacityProbe()
	if probe.Backend != "unavailable" || probe.MaxSessions != 0 || probe.HelperHandoffVersion != 2 {
		t.Fatalf("probe = %+v", probe)
	}

	manifest := &UpgradeManifest{Sessions: make([]UpgradeSession, 65)}
	if err := validateAdoptionCapacity(manifest); err == nil {
		t.Fatal("unsupported default build accepted upgrade adoption")
	}
}
