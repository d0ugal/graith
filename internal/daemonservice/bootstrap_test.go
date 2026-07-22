package daemonservice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

type bootstrapFixture struct {
	definition  Definition
	bundle      ValidatedBundle
	request     StartupRequest
	store       ReceiptStore
	controlRoot string
	environment bootstrapEnvironment
}

func newBootstrapFixture(t *testing.T, profile, slot string) bootstrapFixture {
	t.Helper()

	now := time.Now().UTC().Truncate(time.Millisecond)

	services := filepath.Join(t.TempDir(), "services")
	if err := os.Mkdir(services, 0o700); err != nil {
		t.Fatal(err)
	}

	bundle, expectations := cachedBundleFixture(t, services, "2.0.0", "canny", "canny payload")

	var definition Definition
	if slot == DefaultSlot {
		definition = Definitions()[0]
	} else {
		var err error

		definition, err = DefinitionForSlot(slot)
		if err != nil {
			t.Fatal(err)
		}
	}

	paths := config.Paths{
		Profile: profile, SocketPath: filepath.Join(t.TempDir(), "canny.sock"),
		PIDFile: filepath.Join(t.TempDir(), "canny.pid"),
	}

	request, err := NewStartupRequest(
		definition, profile, "/bothy/config.toml", paths, bundle.Generation.ID,
		map[string]string{"PATH": "/usr/bin:/bin"}, os.Geteuid(), now, time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}

	receiptRoot := filepath.Join(services, "control")
	controlRoot := filepath.Join(receiptRoot, "bootstrap")
	store := ReceiptStore{Root: receiptRoot, UID: os.Geteuid()}

	if _, err := store.Update(true, func(receipt *Receipt) error {
		receipt.Generations[bundle.Generation.ID] = bundle.Generation
		if profile == "" {
			receipt.Default = &Registration{
				Slot: definition.Slot, Label: definition.Label,
				RegisteredGeneration: bundle.Generation.ID,
			}
		} else {
			receipt.Leases[profile] = Lease{
				Profile: profile, Slot: definition.Slot, UID: os.Geteuid(),
				RegisteredGeneration: bundle.Generation.ID,
			}
		}

		return BeginStart(receipt, StartIntent{
			Label: definition.Label, Slot: definition.Slot, Profile: profile,
			Generation: bundle.Generation.ID, Nonce: request.Nonce,
			CreatedAt: request.CreatedAt, ExpiresAt: request.ExpiresAt,
		})
	}); err != nil {
		t.Fatal(err)
	}

	if err := WriteStartupRequest(controlRoot, request); err != nil {
		t.Fatal(err)
	}

	payload := filepath.Join(bundle.AppPath, "Contents", "MacOS", DaemonExecutable)
	environment := bootstrapEnvironment{
		uid: os.Geteuid(),
		receiptRoot: func(uid int) (string, error) {
			if uid != os.Geteuid() {
				t.Fatalf("receipt root UID = %d", uid)
			}

			return receiptRoot, nil
		},
		controlRoot: func(uid int) (string, error) {
			if uid != os.Geteuid() {
				t.Fatalf("control root UID = %d", uid)
			}

			return controlRoot, nil
		},
		executable:      func() (string, error) { return payload, nil },
		verifySignature: expectations.VerifySignature,

		lifecycleMutationGuard: allowDaemonLifecycleMutation,
	}

	return bootstrapFixture{
		definition: definition, bundle: bundle, request: request,
		store: store, controlRoot: controlRoot, environment: environment,
	}
}

func preserveProcessEnvironment(t *testing.T) {
	t.Helper()

	original := os.Environ()

	t.Cleanup(func() {
		os.Clearenv()

		for _, entry := range original {
			name, value, found := strings.Cut(entry, "=")
			if found {
				_ = os.Setenv(name, value)
			}
		}
	})
}

func TestBootstrapFreshServiceConsumesIntentAndRecordsRunningProcess(t *testing.T) {
	for _, test := range []struct {
		name    string
		profile string
		slot    string
	}{
		{name: "default", slot: DefaultSlot},
		{name: "named profile", profile: "canny", slot: "07"},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveProcessEnvironment(t)
			fixture := newBootstrapFixture(t, test.profile, test.slot)

			bootstrap, err := bootstrapFreshService(
				fixture.definition.Label, fixture.definition.Slot,
				fixture.request.CreatedAt.Add(time.Second), fixture.environment,
			)
			if err != nil {
				t.Fatal(err)
			}

			if bootstrap.Definition != fixture.definition || bootstrap.Generation.ID != fixture.bundle.Generation.ID || bootstrap.Request.Nonce != fixture.request.Nonce {
				t.Fatalf("bootstrap result = %#v", bootstrap)
			}

			receipt, err := fixture.store.Load()
			if err != nil {
				t.Fatal(err)
			}

			if _, exists := receipt.Starts[fixture.definition.Label]; exists {
				t.Fatal("successful bootstrap retained its one-shot start intent")
			}

			if test.profile == "" {
				if receipt.Default.RunningGeneration != fixture.bundle.Generation.ID || receipt.Default.RunningPID != os.Getpid() || receipt.Default.Paths != fixture.request.Paths {
					t.Fatalf("default running registration = %#v", receipt.Default)
				}
			} else {
				lease := receipt.Leases[test.profile]
				if lease.RunningGeneration != fixture.bundle.Generation.ID || lease.RunningPID != os.Getpid() || lease.Paths != fixture.request.Paths {
					t.Fatalf("named running lease = %#v", lease)
				}
			}

			if _, err := os.Stat(filepath.Join(fixture.controlRoot, requestFilename(fixture.definition))); !os.IsNotExist(err) {
				t.Fatalf("one-shot request still exists: %v", err)
			}
		})
	}
}

func TestBootstrapFreshServiceCleansFailedReceiptAgreement(t *testing.T) {
	fixture := newBootstrapFixture(t, "", DefaultSlot)
	if _, err := fixture.store.Update(false, func(receipt *Receipt) error {
		receipt.Default = nil

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := bootstrapFreshService(
		fixture.definition.Label, fixture.definition.Slot,
		fixture.request.CreatedAt.Add(time.Second), fixture.environment,
	); err == nil || !strings.Contains(err.Error(), "agreement failed") {
		t.Fatalf("bootstrap agreement error = %v", err)
	}

	receipt, err := fixture.store.Load()
	if err != nil {
		t.Fatal(err)
	}

	if _, exists := receipt.Starts[fixture.definition.Label]; exists {
		t.Fatal("failed bootstrap retained its start intent")
	}

	if _, err := os.Stat(filepath.Join(fixture.controlRoot, requestFilename(fixture.definition))); !os.IsNotExist(err) {
		t.Fatalf("failed bootstrap retained request: %v", err)
	}
}

func TestBootstrapFreshServiceRejectsGoTestBeforeHostRootResolution(t *testing.T) {
	definition := Definitions()[0]
	resolvedRoot := false
	environment := bootstrapEnvironment{
		uid: os.Geteuid(),
		receiptRoot: func(int) (string, error) {
			resolvedRoot = true
			return t.TempDir(), nil
		},
	}

	_, err := bootstrapFreshService(definition.Label, definition.Slot, time.Now(), environment)
	if err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("bootstrapFreshService() error = %v, want Go-test refusal", err)
	}

	if resolvedRoot {
		t.Fatal("Go-test refusal resolved the managed-service receipt root")
	}
}

func TestBootstrapAbortRejectsGoTestBeforeHostRootResolution(t *testing.T) {
	resolvedRoot := false
	bootstrap := Bootstrap{
		receiptRoot: func(int) (string, error) {
			resolvedRoot = true
			return t.TempDir(), nil
		},
	}

	err := bootstrap.Abort()
	if err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("Bootstrap.Abort() error = %v, want Go-test refusal", err)
	}

	if resolvedRoot {
		t.Fatal("Go-test refusal resolved the managed-service receipt root")
	}
}
