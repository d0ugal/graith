package daemonservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type managedUpgradeFixture struct {
	profile     string
	definition  Definition
	auld        ValidatedBundle
	newer       ValidatedBundle
	store       ReceiptStore
	environment managedProcessEnvironment
	validate    func(Generation) (ValidatedBundle, error)
}

func newManagedUpgradeFixture(t *testing.T, profile, slot string) managedUpgradeFixture {
	t.Helper()

	temp := t.TempDir()
	cacheRoot := filepath.Join(temp, "services")
	auld, _ := cachedBundleFixture(t, cacheRoot, "1.0.0", "auld", "auld payload")
	newer, _ := cachedBundleFixture(t, cacheRoot, "2.0.0", "canny", "canny payload")

	var definition Definition
	if profile == "" {
		definition = Definitions()[0]
	} else {
		var err error

		definition, err = DefinitionForSlot(slot)
		if err != nil {
			t.Fatal(err)
		}
	}

	store := ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Geteuid()}
	if _, err := store.Update(true, func(receipt *Receipt) error {
		receipt.Generations[auld.Generation.ID] = auld.Generation

		receipt.Generations[newer.Generation.ID] = newer.Generation
		if profile == "" {
			receipt.Default = &Registration{
				Slot: definition.Slot, Label: definition.Label,
				RegisteredGeneration: auld.Generation.ID,
				RunningGeneration:    auld.Generation.ID, RunningPID: 4242,
			}
		} else {
			receipt.Leases[profile] = Lease{
				Profile: profile, Slot: definition.Slot, UID: os.Geteuid(),
				RegisteredGeneration: auld.Generation.ID,
				RunningGeneration:    auld.Generation.ID, RunningPID: 4242,
			}
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	bundles := map[string]ValidatedBundle{
		auld.Generation.ID:  auld,
		newer.Generation.ID: newer,
	}
	validate := func(generation Generation) (ValidatedBundle, error) {
		bundle, ok := bundles[generation.ID]
		if !ok {
			return ValidatedBundle{}, errors.New("dreich generation")
		}

		return bundle, nil
	}
	auldPayload := filepath.Join(auld.AppPath, "Contents", "MacOS", DaemonExecutable)
	environment := managedProcessEnvironment{
		uid: os.Geteuid(),
		receiptRoot: func(uid int) (string, error) {
			if uid != os.Geteuid() {
				t.Fatalf("receipt root UID = %d", uid)
			}

			return store.Root, nil
		},
		executable: func() (string, error) { return auldPayload, nil },
		pid:        4242, validate: validate,
	}

	return managedUpgradeFixture{
		profile: profile, definition: definition, auld: auld, newer: newer,
		store: store, environment: environment, validate: validate,
	}
}

func TestManagedUpgradeChangesAndRollsBackOnlyItsProfile(t *testing.T) {
	for _, test := range []struct {
		name    string
		profile string
		slot    string
	}{
		{name: "default"},
		{name: "named", profile: "canny", slot: "07"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newManagedUpgradeFixture(t, test.profile, test.slot)

			process, managed, err := runningManagedProcess(test.profile, fixture.environment)
			if err != nil || !managed {
				t.Fatalf("runningManagedProcess() = (%#v, %v, %v)", process, managed, err)
			}

			newPayload := filepath.Join(fixture.newer.AppPath, "Contents", "MacOS", DaemonExecutable)

			definition, rollback, managed, err := prepareManagedUpgrade(process, test.profile, newPayload, fixture.validate, 4242)
			if err != nil || !managed || rollback == nil || definition != fixture.definition {
				t.Fatalf("prepareManagedUpgrade() = (definition %#v, rollback set %t, managed %v, error %v)", definition, rollback != nil, managed, err)
			}

			assertRunningGeneration := func(want string) {
				t.Helper()

				receipt, loadErr := fixture.store.Load()
				if loadErr != nil {
					t.Fatal(loadErr)
				}

				if test.profile == "" {
					if receipt.Default.RunningGeneration != want || receipt.Default.RunningPID != 4242 {
						t.Fatalf("default running state = %#v", receipt.Default)
					}
				} else {
					lease := receipt.Leases[test.profile]
					if lease.RunningGeneration != want || lease.RunningPID != 4242 {
						t.Fatalf("named running state = %#v", lease)
					}
				}
			}

			assertRunningGeneration(fixture.newer.Generation.ID)

			if err := rollback(); err != nil {
				t.Fatal(err)
			}

			assertRunningGeneration(fixture.auld.Generation.ID)
		})
	}
}

func TestRunningManagedProcessRejectsStaleOrForeignIdentity(t *testing.T) {
	fixture := newManagedUpgradeFixture(t, "", DefaultSlot)

	missing := fixture.environment

	missing.receiptRoot = func(int) (string, error) { return filepath.Join(t.TempDir(), "missing"), nil }
	if _, managed, err := runningManagedProcess("", missing); err != nil || managed {
		t.Fatalf("missing receipt = (%v, %v), want unmanaged", managed, err)
	}

	foreignExecutable := filepath.Join(t.TempDir(), "gr")
	if err := os.WriteFile(foreignExecutable, []byte("thrawn"), 0o755); err != nil { // #nosec G306 -- executable identity fixture.
		t.Fatal(err)
	}

	foreign := fixture.environment

	foreign.executable = func() (string, error) { return foreignExecutable, nil }
	if _, managed, err := runningManagedProcess("", foreign); err != nil || managed {
		t.Fatalf("foreign executable = (%v, %v), want unmanaged", managed, err)
	}

	retainedExecutable := filepath.Join(t.TempDir(), "gr")
	auldPayload := filepath.Join(fixture.auld.AppPath, "Contents", "MacOS", DaemonExecutable)
	auldContents, err := os.ReadFile(auldPayload)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(retainedExecutable, auldContents, 0o755); err != nil { // #nosec G306 -- retained executable identity fixture.
		t.Fatal(err)
	}

	retained := fixture.environment
	retained.executable = func() (string, error) { return retainedExecutable, nil }

	if _, managed, err := runningManagedProcess("", retained); err != nil || managed {
		t.Fatalf("retained executable = (%v, %v), want ordinary validation to remain path-strict", managed, err)
	}

	stalePID := fixture.environment

	stalePID.pid = 4343
	if _, _, err := runningManagedProcess("", stalePID); err == nil || !strings.Contains(err.Error(), "running PID") {
		t.Fatalf("stale PID error = %v", err)
	}
}

func TestRetainedAdoptedServiceAcceptsValidatedCandidateWithoutCurrentPathMatch(t *testing.T) {
	for _, test := range []struct {
		name    string
		profile string
		slot    string
	}{
		{name: "default", slot: DefaultSlot},
		{name: "named", profile: "canny", slot: "07"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newManagedUpgradeFixture(t, test.profile, test.slot)
			candidate := filepath.Join(fixture.auld.AppPath, "Contents", "MacOS", DaemonExecutable)
			environment := retainedAdoptionEnvironment{
				uid: fixture.environment.uid, receiptRoot: fixture.environment.receiptRoot,
				pid: fixture.environment.pid, validate: fixture.validate,
			}

			got, err := validateRetainedAdoptedService(
				fixture.definition.Label, fixture.definition.Slot, test.profile, candidate, environment,
			)
			if err != nil || got != fixture.definition {
				t.Fatalf("validateRetainedAdoptedService() = (%#v, %v)", got, err)
			}
		})
	}
}

func TestRetainedAdoptedServiceFailsClosedOnIdentityMismatch(t *testing.T) {
	fixture := newManagedUpgradeFixture(t, "canny", "07")
	candidate := filepath.Join(fixture.auld.AppPath, "Contents", "MacOS", DaemonExecutable)
	environment := retainedAdoptionEnvironment{
		uid: fixture.environment.uid, receiptRoot: fixture.environment.receiptRoot,
		pid: fixture.environment.pid, validate: fixture.validate,
	}

	otherDefinition, err := DefinitionForSlot("08")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		label     string
		slot      string
		profile   string
		candidate string
		environ   retainedAdoptionEnvironment
		want      string
	}{
		{
			name: "wrong marker label", label: otherDefinition.Label, slot: fixture.definition.Slot,
			profile: fixture.profile, candidate: candidate, environ: environment, want: "does not match slot",
		},
		{
			name: "wrong marker slot", label: otherDefinition.Label, slot: otherDefinition.Slot,
			profile: fixture.profile, candidate: candidate, environ: environment, want: "lease does not match",
		},
		{
			name: "wrong profile", label: fixture.definition.Label, slot: fixture.definition.Slot,
			profile: "dreich", candidate: candidate, environ: environment, want: "lease does not match",
		},
		{
			name: "wrong pid", label: fixture.definition.Label, slot: fixture.definition.Slot,
			profile: fixture.profile, candidate: candidate,
			environ: func() retainedAdoptionEnvironment {
				changed := environment
				changed.pid++

				return changed
			}(),
			want: "running PID",
		},
		{
			name: "wrong candidate path", label: fixture.definition.Label, slot: fixture.definition.Slot,
			profile: fixture.profile, candidate: filepath.Join(fixture.newer.AppPath, "Contents", "MacOS", DaemonExecutable),
			environ: environment, want: "not the validated",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateRetainedAdoptedService(test.label, test.slot, test.profile, test.candidate, test.environ)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateRetainedAdoptedService() error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("wrong running generation", func(t *testing.T) {
		if _, err := fixture.store.Update(false, func(receipt *Receipt) error {
			lease := receipt.Leases[fixture.profile]
			lease.RunningGeneration = fixture.newer.Generation.ID
			receipt.Leases[fixture.profile] = lease

			return nil
		}); err != nil {
			t.Fatal(err)
		}

		_, err := validateRetainedAdoptedService(
			fixture.definition.Label, fixture.definition.Slot, fixture.profile, candidate, environment,
		)
		if err == nil || !strings.Contains(err.Error(), "not the validated") {
			t.Fatalf("wrong running generation error = %v", err)
		}
	})
}

func TestRetainedAdoptedServiceDoesNotFallBackToUnmanaged(t *testing.T) {
	definition := Definitions()[0]
	environment := retainedAdoptionEnvironment{
		uid: os.Geteuid(),
		receiptRoot: func(int) (string, error) {
			return filepath.Join(t.TempDir(), "missing"), nil
		},
		pid: 4242,
		validate: func(Generation) (ValidatedBundle, error) {
			t.Fatal("missing receipt reached generation validation")

			return ValidatedBundle{}, nil
		},
	}

	_, err := validateRetainedAdoptedService(
		definition.Label, definition.Slot, "", "/bothy/Graith.app/Contents/MacOS/gr", environment,
	)
	if err == nil || !errors.Is(err, ErrReceiptMissing) {
		t.Fatalf("missing receipt error = %v, want ErrReceiptMissing", err)
	}
}

func TestRetainedAdoptedServiceRejectsChangedValidatedGeneration(t *testing.T) {
	fixture := newManagedUpgradeFixture(t, "", DefaultSlot)
	candidate := filepath.Join(fixture.auld.AppPath, "Contents", "MacOS", DaemonExecutable)
	environment := retainedAdoptionEnvironment{
		uid: fixture.environment.uid, receiptRoot: fixture.environment.receiptRoot,
		pid: fixture.environment.pid,
		validate: func(Generation) (ValidatedBundle, error) {
			changed := fixture.auld
			changed.Generation.PayloadHash = "dreich"

			return changed, nil
		},
	}

	_, err := validateRetainedAdoptedService(
		fixture.definition.Label, fixture.definition.Slot, fixture.profile, candidate, environment,
	)
	if err == nil || !strings.Contains(err.Error(), "does not match its receipt") {
		t.Fatalf("changed validated generation error = %v", err)
	}
}

func TestRecordedUpgradeCandidateRequiresExactValidatedCachedPayload(t *testing.T) {
	root := t.TempDir()
	bundle, _ := cachedBundleFixture(t, root, "2.0.0", "canny", "canny payload")
	receipt := NewReceipt()
	receipt.Generations[bundle.Generation.ID] = bundle.Generation
	payload := filepath.Join(bundle.AppPath, "Contents", "MacOS", DaemonExecutable)

	got, err := recordedUpgradeCandidate(receipt, payload, func(generation Generation) (ValidatedBundle, error) {
		if generation.ID != bundle.Generation.ID {
			t.Fatalf("validated generation = %q", generation.ID)
		}

		return bundle, nil
	})
	if err != nil || got.ID != bundle.Generation.ID {
		t.Fatalf("recordedUpgradeCandidate() = (%#v, %v)", got, err)
	}

	arbitrary := filepath.Join(t.TempDir(), "gr")
	if err := os.WriteFile(arbitrary, []byte("thrawn payload"), 0o755); err != nil { // #nosec G306 -- executable rejection fixture.
		t.Fatal(err)
	}

	if _, err := recordedUpgradeCandidate(receipt, arbitrary, func(Generation) (ValidatedBundle, error) {
		return bundle, nil
	}); err == nil || !strings.Contains(err.Error(), "not a recorded cached") {
		t.Fatalf("arbitrary candidate error = %v", err)
	}

	changed := bundle
	changed.Generation.ID = "2.0.0-dreich"

	if _, err := recordedUpgradeCandidate(receipt, payload, func(Generation) (ValidatedBundle, error) {
		return changed, nil
	}); err == nil || !strings.Contains(err.Error(), "changed during validation") {
		t.Fatalf("changed candidate error = %v", err)
	}
}

func TestRecordedUpgradeCandidateSeparatesSameVersionGenerations(t *testing.T) {
	root := t.TempDir()
	auld, _ := cachedBundleFixture(t, root, "2.0.0", "auld", "auld payload")
	newer, _ := cachedBundleFixture(t, root, "2.0.0", "new", "new payload")
	receipt := NewReceipt()
	receipt.Generations[auld.Generation.ID] = auld.Generation
	receipt.Generations[newer.Generation.ID] = newer.Generation
	payload := filepath.Join(newer.AppPath, "Contents", "MacOS", DaemonExecutable)

	got, err := recordedUpgradeCandidate(receipt, payload, func(generation Generation) (ValidatedBundle, error) {
		if generation.ID != newer.Generation.ID {
			t.Fatalf("selected same-version generation %q, want %q", generation.ID, newer.Generation.ID)
		}

		return newer, nil
	})
	if err != nil || got.ID != newer.Generation.ID {
		t.Fatalf("same-version candidate = (%#v, %v)", got, err)
	}
}

func TestRunningGenerationRemainsProfileIsolated(t *testing.T) {
	receipt := NewReceipt()
	defaultDefinition := Definitions()[0]

	namedDefinition, err := DefinitionForSlot("07")
	if err != nil {
		t.Fatal(err)
	}

	receipt.Default = &Registration{Slot: DefaultSlot, Label: defaultDefinition.Label}
	receipt.Leases["canny"] = Lease{Profile: "canny", Slot: namedDefinition.Slot}

	if err := setRunningGeneration(&receipt, "canny", namedDefinition, "2-canny", 4242); err != nil {
		t.Fatal(err)
	}

	if receipt.Default.RunningGeneration != "" || receipt.Leases["canny"].RunningGeneration != "2-canny" {
		t.Fatalf("running generations crossed profiles: %#v", receipt)
	}

}

func TestSourceUpgradeCandidateKeepsDirectSpawnPath(t *testing.T) {
	originalManaged := ManagedBuild
	originalTeam := ExpectedTeamID
	originalRequirement := ExpectedRequirementBase64

	t.Cleanup(func() {
		ManagedBuild = originalManaged
		ExpectedTeamID = originalTeam
		ExpectedRequirementBase64 = originalRequirement
	})

	ManagedBuild = "false"
	ExpectedTeamID = ""
	ExpectedRequirementBase64 = ""

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	got, managed, err := ResolveUpgradeCandidateContext(context.Background(), executable, "dev", "unknown", os.Geteuid())
	if err != nil || managed || got != executable {
		t.Fatalf("ResolveUpgradeCandidateContext() = (%q, %v, %v), want direct path", got, managed, err)
	}
}

func TestSourcePrepareManagedUpgradeDoesNotReadServiceReceipt(t *testing.T) {
	originalManaged := ManagedBuild

	t.Cleanup(func() { ManagedBuild = originalManaged })

	ManagedBuild = "false"

	definition, commit, managed, err := PrepareManagedUpgrade("canny", "/bothy/unbundled-gr")
	if err != nil || managed || commit != nil || definition != (Definition{}) {
		t.Fatalf("PrepareManagedUpgrade() = (%#v, commit=%t, %v, %v), want untouched direct-spawn fallback", definition, commit != nil, managed, err)
	}
}

func TestGenerationReceiptEqualityIncludesEveryIdentityField(t *testing.T) {
	generation := Generation{ID: "1-canny", AppPath: "/bothy/Graith.app", Version: "1", BundleBuild: "1473", Commit: "canny", PayloadHash: "braw", TeamID: "TEAM", Requirement: "req"}
	if !generationMatchesReceipt(generation, generation) {
		t.Fatal("identical generation did not match its receipt")
	}

	changed := generation

	changed.PayloadHash = "thrawn"
	if generationMatchesReceipt(generation, changed) {
		t.Fatal("changed generation identity matched its receipt")
	}
}
