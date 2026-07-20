package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/daemonservice"
)

func TestServiceMarkerArgument(t *testing.T) {
	t.Parallel()

	named, err := daemonservice.DefinitionForSlot("07")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		args        []string
		wantLabel   string
		wantSlot    string
		wantPresent bool
		wantErr     bool
	}{
		{name: "ordinary command", args: []string{"list"}},
		{name: "manual daemon", args: []string{"daemon", "start"}},
		{name: "default equals", args: []string{"daemon", "start", "--internal-service-label=" + daemonservice.ServiceManifest().DefaultLabel, "--internal-service-slot=default"}, wantLabel: daemonservice.ServiceManifest().DefaultLabel, wantSlot: "default", wantPresent: true},
		{name: "named separated", args: []string{"daemon", "start", "--internal-service-label", named.Label, "--internal-service-slot", named.Slot}, wantLabel: named.Label, wantSlot: named.Slot, wantPresent: true},
		{name: "partial", args: []string{"daemon", "start", "--internal-service-slot=00"}, wantPresent: true, wantErr: true},
		{name: "mismatch", args: []string{"daemon", "start", "--internal-service-label=" + daemonservice.ServiceManifest().DefaultLabel, "--internal-service-slot=00"}, wantPresent: true, wantErr: true},
		{name: "duplicate", args: []string{"daemon", "start", "--internal-service-label=" + named.Label, "--internal-service-label=" + named.Label, "--internal-service-slot=07"}, wantPresent: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			label, slot, present, err := serviceMarkerArgument(tt.args)
			if (err != nil) != tt.wantErr || label != tt.wantLabel || slot != tt.wantSlot || present != tt.wantPresent {
				t.Fatalf("serviceMarkerArgument(%v) = (%q, %q, %v, %v), want (%q, %q, %v, err=%v)", tt.args, label, slot, present, err, tt.wantLabel, tt.wantSlot, tt.wantPresent, tt.wantErr)
			}
		})
	}
}
