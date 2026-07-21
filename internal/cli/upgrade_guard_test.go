package cli

import (
	"context"
	"testing"

	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/daemonservice"
)

func TestExecuteAdoptPassesScopedServiceIdentityWithoutLeakage(t *testing.T) {
	registerCommands()

	originalRun := runAdoptBootstrapForCLI
	originalAdoptFrom := adoptFrom
	originalLabel := internalServiceLabel
	originalSlot := internalServiceSlot
	originalRootContext := rootCmd.Context()
	originalAdoptChanged := daemonStartCmd.Flags().Lookup("adopt-from").Changed
	originalLabelChanged := daemonStartCmd.Flags().Lookup("internal-service-label").Changed
	originalSlotChanged := daemonStartCmd.Flags().Lookup("internal-service-slot").Changed

	t.Cleanup(func() {
		runAdoptBootstrapForCLI = originalRun
		adoptFrom = originalAdoptFrom
		internalServiceLabel = originalLabel
		internalServiceSlot = originalSlot

		rootCmd.SetContext(originalRootContext)

		daemonStartCmd.Flags().Lookup("adopt-from").Changed = originalAdoptChanged
		daemonStartCmd.Flags().Lookup("internal-service-label").Changed = originalLabelChanged
		daemonStartCmd.Flags().Lookup("internal-service-slot").Changed = originalSlotChanged
	})

	type invocation struct {
		manifest string
		identity daemon.AdoptedServiceIdentity
	}

	var invocations []invocation

	runAdoptBootstrapForCLI = func(_ string, manifest string, identity daemon.AdoptedServiceIdentity) error {
		invocations = append(invocations, invocation{manifest: manifest, identity: identity})

		return nil
	}

	definition, err := daemonservice.DefinitionForSlot("07")
	if err != nil {
		t.Fatal(err)
	}

	wantManaged, err := daemon.NewManagedAdoptedServiceIdentity(definition.Label, definition.Slot)
	if err != nil {
		t.Fatal(err)
	}

	wantUnmanaged := daemon.NewUnmanagedAdoptedServiceIdentity()

	if err := executeWithArgs([]string{
		"daemon", "start", "--adopt-from", "/bothy/manifest.json",
		"--internal-service-label", definition.Label,
		"--internal-service-slot", definition.Slot,
	}); err != nil {
		t.Fatalf("managed adoption: %v", err)
	}

	if err := executeWithArgs([]string{
		"daemon", "start", "--adopt-from", "/croft/manifest.json",
	}); err != nil {
		t.Fatalf("unmanaged adoption: %v", err)
	}

	if len(invocations) != 2 {
		t.Fatalf("adoption invocations = %d, want 2", len(invocations))
	}

	if got := invocations[0]; got.manifest != "/bothy/manifest.json" || got.identity != wantManaged {
		t.Fatalf("managed adoption = %#v", got)
	}

	if got := invocations[1]; got.manifest != "/croft/manifest.json" || got.identity != wantUnmanaged {
		t.Fatalf("unmanaged adoption inherited managed identity: %#v", got)
	}
}

func TestDaemonStartRejectsAdoptionWithoutScopedIdentity(t *testing.T) {
	originalRun := runAdoptBootstrapForCLI
	originalAdoptFrom := adoptFrom

	t.Cleanup(func() {
		runAdoptBootstrapForCLI = originalRun
		adoptFrom = originalAdoptFrom
	})

	runAdoptBootstrapForCLI = func(string, string, daemon.AdoptedServiceIdentity) error {
		t.Fatal("adoption bootstrap ran without a scoped identity")

		return nil
	}
	adoptFrom = "/strath/manifest.json"

	cmd := *daemonStartCmd
	cmd.SetContext(context.Background())

	if err := cmd.RunE(&cmd, nil); err == nil {
		t.Fatal("adoption without scoped service identity was accepted")
	}
}

func TestDaemonStartRejectsZeroScopedIdentity(t *testing.T) {
	originalRun := runAdoptBootstrapForCLI
	originalAdoptFrom := adoptFrom

	t.Cleanup(func() {
		runAdoptBootstrapForCLI = originalRun
		adoptFrom = originalAdoptFrom
	})

	runAdoptBootstrapForCLI = func(string, string, daemon.AdoptedServiceIdentity) error {
		t.Fatal("adoption bootstrap ran with a zero scoped identity")

		return nil
	}
	adoptFrom = "/strath/manifest.json"

	ctx := context.WithValue(context.Background(), adoptedServiceContextKey{}, daemon.AdoptedServiceIdentity{})
	cmd := *daemonStartCmd
	cmd.SetContext(ctx)

	if err := cmd.RunE(&cmd, nil); err == nil {
		t.Fatal("adoption with a zero scoped service identity was accepted")
	}
}

func TestAdoptFromArgument(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"daemon", "start", "--adopt-from", "/bothy/manifest.json"}, want: "/bothy/manifest.json"},
		{args: []string{"daemon", "start", "--adopt-from=/croft/manifest.json"}, want: "/croft/manifest.json"},
		{args: []string{"daemon", "start"}, want: ""},
		{args: []string{"list", "--adopt-from=/croft/manifest.json"}, want: ""},
		{args: []string{"msg", "send", "bothy", "daemon", "start", "--adopt-from", "/croft/manifest.json"}, want: ""},
	} {
		if got := adoptFromArgument(tc.args); got != tc.want {
			t.Errorf("adoptFromArgument(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}
