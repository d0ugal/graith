//go:build !libghostty

package daemon

import "testing"

func TestDefaultUpgradeAdoptionCapacityIsUnlimited(t *testing.T) {
	probe := CurrentUpgradeCapacityProbe()
	if probe.Backend != "unlimited" || probe.MaxSessions != 0 || probe.HelperHandoffVersion != 2 {
		t.Fatalf("probe = %+v", probe)
	}
	manifest := &UpgradeManifest{Sessions: make([]UpgradeSession, 65)}
	if err := validateAdoptionCapacity(manifest); err != nil {
		t.Fatalf("pure-Go adoption rejected 65 sessions: %v", err)
	}
}
