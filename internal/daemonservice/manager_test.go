package daemonservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
)

type fakeServiceController struct {
	mu               sync.Mutex
	statuses         map[string]ServiceStatus
	jobs             map[string]JobState
	kickstarts       []string
	registrations    []string
	unregistrations  []string
	registerErr      error
	registerErrs     []error
	registrationJobs []JobState
	registerOnce     sync.Once
	registerStarted  chan struct{}
	registerRelease  chan struct{}
}

func newFakeServiceController() *fakeServiceController {
	return &fakeServiceController{statuses: make(map[string]ServiceStatus), jobs: make(map[string]JobState)}
}

func (controller *fakeServiceController) Status(_ context.Context, _ string, definition Definition) (ServiceStatus, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()

	if status, ok := controller.statuses[definition.Label]; ok {
		return status, nil
	}

	return StatusNotFound, nil
}

func (controller *fakeServiceController) Register(_ context.Context, path string, definition Definition) (ServiceStatus, error) {
	return controller.register(path, definition)
}

func (controller *fakeServiceController) RegisterFresh(_ context.Context, path string, definition Definition) (ServiceStatus, error) {
	return controller.register(path, definition)
}

func (controller *fakeServiceController) register(path string, definition Definition) (ServiceStatus, error) {
	controller.registerOnce.Do(func() {
		if controller.registerStarted != nil {
			close(controller.registerStarted)
		}

		if controller.registerRelease != nil {
			<-controller.registerRelease
		}
	})

	controller.mu.Lock()
	defer controller.mu.Unlock()

	if controller.registerErr != nil {
		return "", controller.registerErr
	}

	if len(controller.registerErrs) > 0 {
		err := controller.registerErrs[0]
		controller.registerErrs = controller.registerErrs[1:]

		if err != nil {
			return "", err
		}
	}

	controller.statuses[definition.Label] = StatusEnabled
	controller.registrations = append(controller.registrations, definition.Label)

	job := fakeRegisteredJob(path)
	if len(controller.registrationJobs) > 0 {
		job = controller.registrationJobs[0]
		controller.registrationJobs = controller.registrationJobs[1:]
	}

	controller.jobs[definition.Label] = job

	return StatusEnabled, nil
}

func fakeRegisteredJob(controllerPath string) JobState {
	appPath := strings.TrimSuffix(controllerPath, "/Contents/MacOS/"+ControllerExecutable)

	bundleBuild := ""
	if plist, err := readPlistStrings(filepath.Join(appPath, "Contents", "Info.plist")); err == nil {
		bundleBuild = plist["CFBundleVersion"]
	}

	return JobState{
		Present:                true,
		ProgramIdentifier:      "Contents/MacOS/" + DaemonExecutable,
		ParentBundleIdentifier: ServiceManifest().BundleIdentifier,
		ParentBundleVersion:    bundleBuild,
	}
}

func (controller *fakeServiceController) Unregister(_ context.Context, _ string, definition Definition) (ServiceStatus, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()

	controller.statuses[definition.Label] = StatusNotRegistered
	delete(controller.jobs, definition.Label)
	controller.unregistrations = append(controller.unregistrations, definition.Label)

	return StatusNotRegistered, nil
}

func (controller *fakeServiceController) Kickstart(_ context.Context, _ int, definition Definition) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()

	state := controller.jobs[definition.Label]
	state.Present = true
	state.Running = true
	state.PID = 4242
	controller.jobs[definition.Label] = state
	controller.kickstarts = append(controller.kickstarts, definition.Label)

	return nil
}

func (controller *fakeServiceController) JobState(_ context.Context, _ int, definition Definition) (JobState, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()

	return controller.jobs[definition.Label], nil
}

func testManager(t *testing.T, controller *fakeServiceController) *Manager {
	t.Helper()

	temp := t.TempDir()

	services := filepath.Join(temp, "services")
	if err := os.Mkdir(services, 0o700); err != nil { // #nosec G301 -- owner-only test control root.
		t.Fatal(err)
	}

	manager := &Manager{
		UID:         os.Getuid(),
		Bundle:      ValidatedBundle{AppPath: "/bothy/Graith.app", Generation: Generation{ID: "1-braw", AppPath: "/bothy/Graith.app", Version: "1", Commit: "braw"}},
		Store:       ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()},
		ControlRoot: filepath.Join(services, "control", "bootstrap"),
		Controller:  controller,
		Now:         time.Now,
		SkipCacheGC: true,
	}
	if err := manager.ensureReceipt(context.Background()); err != nil {
		t.Fatal(err)
	}

	return manager
}

func seedPendingRotation(t *testing.T, manager *Manager, auld, newer ValidatedBundle, profile string, definition Definition) {
	t.Helper()

	if _, err := manager.Store.Update(true, func(receipt *Receipt) error {
		receipt.Generations[auld.Generation.ID] = auld.Generation
		receipt.Generations[newer.Generation.ID] = newer.Generation
		receipt.Leases[profile] = Lease{Profile: profile, Slot: definition.Slot, UID: os.Getuid(), RegisteredGeneration: auld.Generation.ID}
		receipt.Pending = &PendingOperation{Kind: "rotate", Profile: profile, Slot: definition.Slot, UID: os.Getuid(), Generation: newer.Generation.ID}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveFallbackModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options ResolveOptions
		want    Mode
	}{
		{name: "linux", options: ResolveOptions{GOOS: "linux"}, want: ModeLinuxFallback},
		{name: "macOS 12", options: ResolveOptions{GOOS: "darwin", MacOSMajor: 12, Managed: true}, want: ModeOlderMacFallback},
		{name: "source macOS", options: ResolveOptions{GOOS: "darwin", MacOSMajor: 13, Managed: false}, want: ModeUnbundledFallback},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolution, err := ResolveWith(tt.options)
			if err != nil {
				t.Fatal(err)
			}

			if resolution.Mode != tt.want || resolution.Manager != nil {
				t.Fatalf("ResolveWith() = %#v, want mode %s and no manager", resolution, tt.want)
			}
		})
	}
}

func TestManagerLaunchDefaultIsDemandStartedAndRaceSafe(t *testing.T) {
	t.Parallel()

	controller := newFakeServiceController()
	manager := testManager(t, controller)
	cfg := config.Default()
	paths := config.Paths{SocketPath: "/tmp/braw.sock", PIDFile: "/tmp/braw.pid"}

	environ := []string{"PATH=/usr/bin:/bin", "SHELL=/bin/zsh", "HOME=/untrusted"}
	if err := manager.Launch(context.Background(), cfg, paths, "/bothy/config.toml", 5*time.Second, environ); err != nil {
		t.Fatal(err)
	}

	if err := manager.Launch(context.Background(), cfg, paths, "/other/config.toml", 5*time.Second, environ); err != nil {
		t.Fatal(err)
	}

	if len(controller.kickstarts) != 1 {
		t.Fatalf("kickstarts = %v, want exactly one", controller.kickstarts)
	}

	if len(controller.registrations) != 1 || controller.registrations[0] != ServiceManifest().DefaultLabel {
		t.Fatalf("registrations = %v", controller.registrations)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if receipt.Default == nil || receipt.Default.RegisteredGeneration != manager.Bundle.Generation.ID {
		t.Fatalf("default registration = %#v", receipt.Default)
	}

	if len(receipt.Starts) != 1 {
		t.Fatalf("start intents = %#v, want winner only", receipt.Starts)
	}
}

func TestConcurrentFirstRegistrationKeepsTransactionGenerationIsolated(t *testing.T) {
	for _, profile := range []string{"", "canny"} {
		name := "default"
		if profile != "" {
			name = "named"
		}

		t.Run(name, func(t *testing.T) {
			temp := t.TempDir()
			cacheRoot := filepath.Join(temp, "services")
			first, expectations := cachedBundleFixture(t, cacheRoot, "1.0.0", "braw", "braw payload")
			second, _ := cachedBundleFixture(t, cacheRoot, "2.0.0", "canny", "canny payload")
			store := ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Geteuid()}

			if _, err := store.Update(true, func(receipt *Receipt) error {
				receipt.Generations[first.Generation.ID] = first.Generation
				receipt.Generations[second.Generation.ID] = second.Generation

				return nil
			}); err != nil {
				t.Fatal(err)
			}

			controller := newFakeServiceController()
			controller.registerStarted = make(chan struct{})
			controller.registerRelease = make(chan struct{})

			newManager := func(bundle ValidatedBundle) *Manager {
				return &Manager{
					UID: os.Geteuid(), Bundle: bundle, Store: store, Controller: controller,
					TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature,
					SkipCacheGC: true,
				}
			}

			firstManager := newManager(first)
			secondManager := newManager(second)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

			defer cancel()

			errs := make(chan error, 1)

			go func() {
				_, err := firstManager.definitionForProfile(ctx, profile)
				errs <- err
			}()

			select {
			case <-controller.registerStarted:
			case <-ctx.Done():
				t.Fatal("first registration did not reach controller")
			}

			var secondErr error
			if profile == "" {
				secondErr = secondManager.ensureDefaultRegistration(ctx, Definitions()[0])
			} else {
				_, secondErr = secondManager.definitionForNamedProfile(ctx, profile)
			}

			if !errors.Is(secondErr, ErrPendingInProgress) {
				t.Fatalf("second registration attempt = %v, want pending-owner isolation", secondErr)
			}

			receipt, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}

			if receipt.Pending == nil || receipt.Pending.Generation != first.Generation.ID || receipt.Pending.Token == "" {
				t.Fatalf("concurrent caller changed first transaction: %#v", receipt.Pending)
			}

			controller.mu.Lock()
			registrationsWhileBlocked := len(controller.registrations)
			controller.mu.Unlock()

			if registrationsWhileBlocked != 0 {
				t.Fatalf("second caller registered while first transaction was blocked: %d", registrationsWhileBlocked)
			}

			close(controller.registerRelease)

			if err := <-errs; err != nil {
				t.Fatal(err)
			}

			if _, err := secondManager.definitionForProfile(ctx, profile); err != nil {
				t.Fatal(err)
			}

			receipt, err = store.Load()
			if err != nil {
				t.Fatal(err)
			}

			var registered string

			if profile == "" {
				if receipt.Default == nil {
					t.Fatal("default registration was not committed")
				}

				registered = receipt.Default.RegisteredGeneration
			} else {
				registered = receipt.Leases[profile].RegisteredGeneration
			}

			if registered != second.Generation.ID {
				t.Fatalf("final registered generation = %q, want second caller %q", registered, second.Generation.ID)
			}

			definition := Definitions()[0]
			if profile != "" {
				definition, err = DefinitionForSlot(receipt.Leases[profile].Slot)
				if err != nil {
					t.Fatal(err)
				}
			}

			job, err := controller.JobState(ctx, os.Geteuid(), definition)
			if err != nil {
				t.Fatal(err)
			}

			if !jobMatchesGeneration(job, second) {
				t.Fatalf("final launchd job does not match committed generation: %#v", job)
			}
		})
	}
}

func TestManagerNamedProfilesUseDistinctStableSlots(t *testing.T) {
	t.Parallel()

	controller := newFakeServiceController()
	manager := testManager(t, controller)
	cfg := config.Default()

	for _, profile := range []string{"canny", "dreich"} {
		paths := config.Paths{Profile: profile, SocketPath: "/tmp/" + profile + ".sock"}
		if err := manager.Launch(context.Background(), cfg, paths, "", 5*time.Second, []string{"PATH=/usr/bin:/bin"}); err != nil {
			t.Fatal(err)
		}
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if receipt.Leases["canny"].Slot != "00" || receipt.Leases["dreich"].Slot != "01" {
		t.Fatalf("profile leases = %#v", receipt.Leases)
	}

	if len(controller.kickstarts) != 2 || controller.kickstarts[0] == controller.kickstarts[1] {
		t.Fatalf("named kickstarts = %v", controller.kickstarts)
	}
}

func TestManagerDisabledServiceNeverKickstarts(t *testing.T) {
	t.Parallel()

	controller := newFakeServiceController()
	manager := testManager(t, controller)
	controller.statuses[ServiceManifest().DefaultLabel] = StatusRequiresApproval

	err := manager.Launch(context.Background(), config.Default(), config.Paths{}, "", 5*time.Second, []string{"PATH=/usr/bin:/bin"})
	if err == nil {
		t.Fatal("disabled service launch succeeded")
	}

	if len(controller.kickstarts) != 0 {
		t.Fatalf("disabled service kickstarted: %v", controller.kickstarts)
	}
}

func TestFreshRegistrationRejectsPreexistingEnabledStatus(t *testing.T) {
	controller := newFakeServiceController()
	definition := Definitions()[0]
	controller.statuses[definition.Label] = StatusEnabled

	err := registerFreshService(context.Background(), controller, "/bothy/Graith.app/Contents/MacOS/"+ControllerExecutable, definition)
	if err == nil || !strings.Contains(err.Error(), "requires an absent job") {
		t.Fatalf("fresh registration error = %v", err)
	}

	if len(controller.registrations) != 0 {
		t.Fatalf("fresh registration called through for an existing job: %v", controller.registrations)
	}
}

func TestManagerRejectsAgentAsFirstStarter(t *testing.T) {
	t.Parallel()

	for _, marker := range []string{
		"GRAITH_SESSION_ID=bairn",
		"GRAITH_AGENT_TYPE=codex",
		"CLAUDE_CODE=1",
		"CURSOR_AGENT=1",
		"GRAITH_SESSION_ID=bairn\x00GR_AGENT_MODE=0",
		"CLAUDE_CODE=1\x00GR_AGENT_MODE=false",
	} {
		controller := newFakeServiceController()
		manager := testManager(t, controller)

		environ := append([]string{"PATH=/usr/bin:/bin"}, strings.Split(marker, "\x00")...)

		err := manager.Launch(context.Background(), config.Default(), config.Paths{}, "", 5*time.Second, environ)
		if err == nil {
			t.Errorf("agent caller %q became first managed starter", marker)
		}

		if len(controller.registrations) != 0 || len(controller.kickstarts) != 0 {
			t.Errorf("agent caller %q changed service state: register=%v kickstart=%v", marker, controller.registrations, controller.kickstarts)
		}
	}
}

func TestManagerRejectsProtectedTMPDIRBeforeServiceMutation(t *testing.T) {
	controller := newFakeServiceController()
	manager := testManager(t, controller)

	servicesRoot, err := CacheRoot(os.Getuid())
	if err != nil {
		t.Fatal(err)
	}

	err = manager.Launch(
		context.Background(), config.Default(), config.Paths{Profile: "canny"}, "", 5*time.Second,
		[]string{"PATH=/usr/bin:/bin", "TMPDIR=" + servicesRoot},
	)
	if err == nil || !strings.Contains(err.Error(), "must not expose") {
		t.Fatalf("protected TMPDIR launch error = %v", err)
	}

	if len(controller.registrations) != 0 || len(controller.kickstarts) != 0 {
		t.Fatalf("invalid TMPDIR changed service state: register=%v kickstart=%v", controller.registrations, controller.kickstarts)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if len(receipt.Leases) != 0 || receipt.Default != nil || receipt.Pending != nil || len(receipt.Starts) != 0 {
		t.Fatalf("invalid TMPDIR changed receipt state: %#v", receipt)
	}
}

func TestExpiredStartIntentIsReconciledAfterLauncherCrash(t *testing.T) {
	controller := newFakeServiceController()
	manager := testManager(t, controller)
	now := time.Now().UTC().Round(0)
	manager.Now = func() time.Time { return now }

	paths := config.Paths{SocketPath: "/bothy/braw.sock"}
	if err := manager.Launch(context.Background(), config.Default(), paths, "", time.Minute, []string{"PATH=/usr/bin:/bin"}); err != nil {
		t.Fatal(err)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	first := receipt.Starts[ServiceManifest().DefaultLabel]
	if first.Nonce == "" {
		t.Fatal("first launch did not persist its start intent")
	}

	delete(controller.jobs, ServiceManifest().DefaultLabel)

	now = first.ExpiresAt.Add(time.Second)

	if err := manager.Launch(context.Background(), config.Default(), paths, "", time.Minute, []string{"PATH=/usr/bin:/bin"}); err != nil {
		t.Fatal(err)
	}

	receipt, err = manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	replacement := receipt.Starts[ServiceManifest().DefaultLabel]
	if replacement.Nonce == "" || replacement.Nonce == first.Nonce {
		t.Fatalf("expired start intent was not replaced: first=%#v replacement=%#v", first, replacement)
	}

	if len(controller.kickstarts) != 2 {
		t.Fatalf("kickstarts = %v, want retry after expired intent", controller.kickstarts)
	}
}

func TestManagerRegistrationFailureRetainsPendingLease(t *testing.T) {
	t.Parallel()

	controller := newFakeServiceController()
	manager := testManager(t, controller)
	controller.registerErr = errors.New("dreich")

	err := manager.Launch(context.Background(), config.Default(), config.Paths{Profile: "canny"}, "", 5*time.Second, []string{"PATH=/usr/bin:/bin"})
	if err == nil {
		t.Fatal("registration failure ignored")
	}

	receipt, loadErr := manager.Receipt()
	if loadErr != nil {
		t.Fatal(loadErr)
	}

	if receipt.Pending == nil || receipt.Pending.Profile != "canny" || receipt.Pending.Slot != "00" {
		t.Fatalf("pending registration not retained: %#v", receipt.Pending)
	}
}

func TestMissingReceiptWithExistingJobFailsClosed(t *testing.T) {
	t.Parallel()

	controller := newFakeServiceController()
	controller.statuses[ServiceManifest().DefaultLabel] = StatusEnabled
	controller.jobs[ServiceManifest().DefaultLabel] = JobState{Present: true, Running: true, PID: 4242}
	temp := t.TempDir()

	manager := &Manager{
		UID: os.Getuid(), Bundle: ValidatedBundle{AppPath: "/bothy/Graith.app", Generation: Generation{ID: "1-braw"}},
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
	}
	if err := manager.ensureReceipt(context.Background()); err == nil {
		t.Fatal("missing receipt with existing service initialized empty")
	}
}

func TestStopStatePersistsLeaseAndExplicitRemoveReleasesIt(t *testing.T) {
	t.Parallel()

	controller := newFakeServiceController()
	manager := testManager(t, controller)

	paths := config.Paths{Profile: "canny", SocketPath: "/tmp/canny.sock"}
	if err := manager.Launch(context.Background(), config.Default(), paths, "", 5*time.Second, []string{"PATH=/usr/bin:/bin"}); err != nil {
		t.Fatal(err)
	}

	definition, found, err := manager.CurrentDefinition("canny")
	if err != nil || !found {
		t.Fatalf("CurrentDefinition() = (%#v, %v, %v)", definition, found, err)
	}

	if _, err := manager.Store.Update(false, func(receipt *Receipt) error {
		lease := receipt.Leases["canny"]
		lease.RunningGeneration = manager.Bundle.Generation.ID
		lease.RunningPID = 4343
		receipt.Leases["canny"] = lease

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := manager.MarkStopped("canny", 4242); err != nil {
		t.Fatal(err)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if lease := receipt.Leases["canny"]; lease.RunningPID != 4343 {
		t.Fatalf("stale stop cleared replacement daemon marker: %#v", lease)
	}

	if err := manager.MarkStopped("canny", 4343); err != nil {
		t.Fatal(err)
	}

	receipt, err = manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if lease, ok := receipt.Leases["canny"]; !ok || lease.Slot != definition.Slot || lease.RunningGeneration != "" {
		t.Fatalf("stop did not retain dormant lease: %#v", receipt.Leases)
	}

	stopCalled := false

	err = manager.Remove(context.Background(), "canny", false, func(report ServiceReport) error {
		stopCalled = true

		if report.PID != 4242 || report.Profile != "canny" {
			t.Fatalf("stop report = %#v, want canny PID 4242", report)
		}

		controller.mu.Lock()
		controller.jobs[definition.Label] = JobState{Present: true, Running: false}
		controller.mu.Unlock()

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if !stopCalled {
		t.Fatal("remove did not stop running exact job")
	}

	receipt, err = manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := receipt.Leases["canny"]; ok {
		t.Fatalf("explicit remove retained lease: %#v", receipt.Leases)
	}

	if len(controller.unregistrations) != 1 || controller.unregistrations[0] != definition.Label {
		t.Fatalf("unregistrations = %v", controller.unregistrations)
	}
}

func TestReportsKeepProfilesIsolated(t *testing.T) {
	t.Parallel()

	controller := newFakeServiceController()

	manager := testManager(t, controller)
	for _, profile := range []string{"canny", "dreich"} {
		if err := manager.Launch(context.Background(), config.Default(), config.Paths{Profile: profile}, "", 5*time.Second, []string{"PATH=/usr/bin:/bin"}); err != nil {
			t.Fatal(err)
		}
	}

	reports, err := manager.Reports(context.Background(), "canny", false)
	if err != nil {
		t.Fatal(err)
	}

	if len(reports) != 1 || reports[0].Profile != "canny" || reports[0].Slot != "00" {
		t.Fatalf("profile report = %#v", reports)
	}

	all, err := manager.Reports(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}

	if len(all) != 3 || all[0].Slot != DefaultSlot || all[1].Profile != "canny" || all[2].Profile != "dreich" {
		t.Fatalf("all reports = %#v", all)
	}
}

func TestRepairRemovesOnlyProvenDownExactOrphan(t *testing.T) {
	t.Parallel()

	controller := newFakeServiceController()
	definition, _ := DefinitionForSlot("05")
	controller.statuses[definition.Label] = StatusEnabled
	controller.jobs[definition.Label] = JobState{Present: true, Running: false}
	temp := t.TempDir()
	cacheRoot := filepath.Join(temp, "services")

	generationDir := filepath.Join(cacheRoot, testVersion+"-"+testCommit)
	if err := os.MkdirAll(generationDir, 0o700); err != nil {
		t.Fatal(err)
	}

	app, standalone := writeBundleFixture(t, generationDir)
	expectations := bundleExpectations(standalone)

	bundle, err := ValidateBundle(app, expectations)
	if err != nil {
		t.Fatal(err)
	}

	controller.jobs[definition.Label] = jobStateForBundle(bundle)
	manager := &Manager{
		UID: os.Getuid(), Bundle: bundle,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature,
	}

	actions, err := manager.Repair(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(actions) != 1 || len(controller.unregistrations) != 1 || controller.unregistrations[0] != definition.Label {
		t.Fatalf("repair actions=%v unregister=%v", actions, controller.unregistrations)
	}

	if _, err := manager.Receipt(); err != nil {
		t.Fatalf("repair did not create receipt: %v", err)
	}
}

func TestRepairQuarantinesDormantUnknownProgram(t *testing.T) {
	controller := newFakeServiceController()
	definition, _ := DefinitionForSlot("12")
	controller.statuses[definition.Label] = StatusEnabled
	controller.jobs[definition.Label] = JobState{
		Present: true, ProgramIdentifier: "Contents/MacOS/gr",
		ParentBundleIdentifier: ServiceManifest().BundleIdentifier, ParentBundleVersion: "unknown",
	}
	temp := t.TempDir()
	bundle, expectations := cachedBundleFixture(t, filepath.Join(temp, "services"), "2.0.0", "canny", "canny payload")
	manager := &Manager{
		UID: os.Getuid(), Bundle: bundle,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature,
	}

	if _, err := manager.Repair(context.Background()); err != nil {
		t.Fatal(err)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if receipt.Quarantined[definition.Slot] == "" || len(controller.unregistrations) != 0 {
		t.Fatalf("unknown dormant program was not quarantined: receipt=%#v unregister=%v", receipt, controller.unregistrations)
	}
}

func TestRemoveAllClearsProvenDownQuarantinedSlots(t *testing.T) {
	controller := newFakeServiceController()

	manager := testManager(t, controller)
	if _, err := manager.Store.Update(false, func(receipt *Receipt) error {
		receipt.Quarantined["13"] = "dreich registration"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := manager.Remove(context.Background(), "", true, nil); err != nil {
		t.Fatal(err)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if len(receipt.Quarantined) != 0 {
		t.Fatalf("remove all retained quarantined slots: %#v", receipt.Quarantined)
	}
}

func cachedBundleFixture(t *testing.T, cacheRoot, version, commit, payload string) (ValidatedBundle, BundleExpectations) {
	t.Helper()

	id, err := GenerationID(version, commit)
	if err != nil {
		t.Fatal(err)
	}

	generationDir := filepath.Join(cacheRoot, id)
	if err := os.MkdirAll(generationDir, 0o700); err != nil {
		t.Fatal(err)
	}

	app, standalone := writeBundleFixtureFor(t, generationDir, version, commit, []byte(payload))
	expectations := BundleExpectations{
		Version: version, Commit: commit, StandalonePath: standalone,
		TeamID: testTeam, Requirement: testRequirement,
		VerifySignature: func(string) (SignatureInfo, error) {
			return SignatureInfo{Identifier: ServiceManifest().BundleIdentifier, TeamID: testTeam, Requirement: testRequirement}, nil
		},
	}

	bundle, err := ValidateBundle(app, expectations)
	if err != nil {
		t.Fatal(err)
	}

	return bundle, expectations
}

func jobStateForBundle(bundle ValidatedBundle) JobState {
	return JobState{
		Present:                true,
		ProgramIdentifier:      "Contents/MacOS/" + DaemonExecutable,
		ParentBundleIdentifier: ServiceManifest().BundleIdentifier,
		ParentBundleVersion:    bundle.Generation.BundleBuild,
	}
}

func TestRepairQuarantinesUnknownLiveSlotAfterReceiptLoss(t *testing.T) {
	controller := newFakeServiceController()
	definition, _ := DefinitionForSlot("06")
	controller.statuses[definition.Label] = StatusEnabled
	controller.jobs[definition.Label] = JobState{Present: true, Running: true, PID: 4242}
	temp := t.TempDir()
	bundle, expectations := cachedBundleFixture(t, filepath.Join(temp, "services"), "2.0.0", "canny", "canny payload")
	manager := &Manager{
		UID: os.Getuid(), Bundle: bundle,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature,
	}

	actions, err := manager.Repair(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(actions) != 1 || len(controller.unregistrations) != 0 {
		t.Fatalf("repair actions=%v unregister=%v", actions, controller.unregistrations)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if receipt.Quarantined["06"] == "" {
		t.Fatalf("unknown live slot was not quarantined: %#v", receipt.Quarantined)
	}

	if _, _, err := ReserveProfile(&receipt, "bothy", os.Getuid(), bundle.Generation.ID); err != nil {
		t.Fatal(err)
	}

	if receipt.Pending == nil || receipt.Pending.Slot == "06" {
		t.Fatalf("allocator reused quarantined slot: pending=%#v", receipt.Pending)
	}
}

func TestRepairPreservesCorruptReceiptsBeforeQuarantineInitialization(t *testing.T) {
	controller := newFakeServiceController()
	temp := t.TempDir()
	bundle, expectations := cachedBundleFixture(t, filepath.Join(temp, "services"), "2.0.0", "dreich", "dreich payload")

	store := ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}
	if _, err := store.Update(true, func(*Receipt) error { return nil }); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Update(false, func(*Receipt) error { return nil }); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(store.primaryPath(), []byte("dreich"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(store.backupPath(), []byte("thrawn"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager := &Manager{
		UID: os.Getuid(), Bundle: bundle, Store: store, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature,
	}
	if _, err := manager.Repair(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{store.primaryPath() + ".corrupt", store.backupPath() + ".corrupt"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("corrupt receipt was not retained at %s: %v", path, err)
		}
	}

	if _, err := manager.Receipt(); err != nil {
		t.Fatalf("repaired receipt is invalid: %v", err)
	}
}

func TestRotationRollsBackAndRetainsLeaseOnRegistrationFailure(t *testing.T) {
	controller := newFakeServiceController()
	definition := Definitions()[0]
	controller.statuses[definition.Label] = StatusEnabled
	temp := t.TempDir()
	cacheRoot := filepath.Join(temp, "services")
	oldBundle, expectations := cachedBundleFixture(t, cacheRoot, "1.0.0", "auld", "auld payload")
	newBundle, _ := cachedBundleFixture(t, cacheRoot, "2.0.0", "new", "new payload")

	manager := &Manager{
		UID: os.Getuid(), Bundle: newBundle,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature, SkipCacheGC: true,
	}
	if _, err := manager.Store.Update(true, func(receipt *Receipt) error {
		receipt.Default = &Registration{Slot: DefaultSlot, Label: definition.Label, RegisteredGeneration: oldBundle.Generation.ID}
		receipt.Generations[oldBundle.Generation.ID] = oldBundle.Generation
		receipt.Generations[newBundle.Generation.ID] = newBundle.Generation

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	controller.registerErrs = []error{errors.New("new registration failed"), nil}

	if err := manager.rotateIfNeeded(context.Background(), definition, "", oldBundle.Generation.ID); err == nil {
		t.Fatal("rotation failure was ignored")
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if receipt.Pending != nil || receipt.Default.RegisteredGeneration != oldBundle.Generation.ID {
		t.Fatalf("rollback did not preserve old registration: %#v", receipt)
	}

	if len(controller.unregistrations) != 2 || len(controller.registrations) != 1 {
		t.Fatalf("rotation calls unregister=%v register=%v", controller.unregistrations, controller.registrations)
	}
}

func TestRotationVerifiesCandidateBundleBeforeCommittingReceipt(t *testing.T) {
	controller := newFakeServiceController()
	definition := Definitions()[0]
	temp := t.TempDir()
	cacheRoot := filepath.Join(temp, "services")
	auld, expectations := cachedBundleFixture(t, cacheRoot, "1.0.0", "auld", "auld payload")
	newer, _ := cachedBundleFixture(t, cacheRoot, "2.0.0", "canny", "canny payload")

	manager := &Manager{
		UID: os.Getuid(), Bundle: newer,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature, SkipCacheGC: true,
	}
	if _, err := manager.Store.Update(true, func(receipt *Receipt) error {
		receipt.Default = &Registration{Slot: DefaultSlot, Label: definition.Label, RegisteredGeneration: auld.Generation.ID}
		receipt.Generations[auld.Generation.ID] = auld.Generation
		receipt.Generations[newer.Generation.ID] = newer.Generation

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	controller.statuses[definition.Label] = StatusEnabled
	controller.registrationJobs = []JobState{
		jobStateForBundle(auld), // Candidate registration still resolves to the old app.
		jobStateForBundle(auld), // Rollback proves the old app was restored.
	}

	err := manager.rotateIfNeeded(context.Background(), definition, "", auld.Generation.ID)
	if err == nil || !strings.Contains(err.Error(), "does not belong to candidate bundle") {
		t.Fatalf("rotation identity error = %v", err)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if receipt.Pending != nil || receipt.Default.RegisteredGeneration != auld.Generation.ID || receipt.Quarantined[DefaultSlot] != "" {
		t.Fatalf("candidate identity failure was not rolled back: %#v", receipt)
	}
}

func TestCacheGCKeepsBackupReferenceThenRemovesUnreferencedGeneration(t *testing.T) {
	controller := newFakeServiceController()
	temp := t.TempDir()
	cacheRoot := filepath.Join(temp, "services")
	oldBundle, expectations := cachedBundleFixture(t, cacheRoot, "1.0.0", "auld", "auld payload")
	newBundle, _ := cachedBundleFixture(t, cacheRoot, "2.0.0", "new", "new payload")

	manager := &Manager{
		UID: os.Getuid(), Bundle: newBundle,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature,
	}
	if _, err := manager.Store.Update(true, func(receipt *Receipt) error {
		receipt.Default = &Registration{Slot: DefaultSlot, Label: Definitions()[0].Label, RegisteredGeneration: oldBundle.Generation.ID}
		receipt.Generations[oldBundle.Generation.ID] = oldBundle.Generation
		receipt.Generations[newBundle.Generation.ID] = newBundle.Generation

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Store.Update(false, func(receipt *Receipt) error {
		receipt.Default.RegisteredGeneration = newBundle.Generation.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if removed, err := manager.GarbageCollectCache(); err != nil || len(removed) != 0 {
		t.Fatalf("GC with backup reference = (%v, %v), want no removal", removed, err)
	}

	if _, err := manager.Store.Update(false, func(*Receipt) error { return nil }); err != nil {
		t.Fatal(err)
	}

	removed, err := manager.GarbageCollectCache()
	if err != nil {
		t.Fatal(err)
	}

	if len(removed) != 1 || removed[0] != oldBundle.Generation.ID {
		t.Fatalf("GC removed %v, want old generation", removed)
	}

	if _, err := os.Stat(filepath.Dir(oldBundle.AppPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old cache still exists or failed unexpectedly: %v", err)
	}

	if _, err := os.Stat(filepath.Dir(newBundle.AppPath)); err != nil {
		t.Fatalf("current cache was removed: %v", err)
	}

	backupData, err := os.ReadFile(manager.Store.backupPath())
	if err != nil {
		t.Fatal(err)
	}

	backup, err := decodeReceipt(backupData)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := backup.Generations[oldBundle.Generation.ID]; ok {
		t.Fatal("cache was deleted while the rollback receipt still referenced it")
	}
}

func TestLifecycleTreatsPIDAsLiveWhenLaunchctlStateLags(t *testing.T) {
	controller := newFakeServiceController()
	manager := testManager(t, controller)

	paths := config.Paths{Profile: "canny", SocketPath: "/tmp/canny.sock"}
	if err := manager.Launch(context.Background(), config.Default(), paths, "", 5*time.Second, []string{"PATH=/usr/bin:/bin"}); err != nil {
		t.Fatal(err)
	}

	definition, _, err := manager.CurrentDefinition("canny")
	if err != nil {
		t.Fatal(err)
	}

	controller.jobs[definition.Label] = JobState{Present: true, Running: false, PID: 4242}
	stopped := false

	err = manager.Remove(context.Background(), "canny", false, func(ServiceReport) error {
		stopped = true
		controller.jobs[definition.Label] = JobState{Present: true}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if !stopped {
		t.Fatal("remove treated a positive launchctl PID as dormant")
	}

	controller = newFakeServiceController()
	manager = testManager(t, controller)
	definition = Definitions()[0]

	controller.jobs[definition.Label] = JobState{Present: true, Running: false, PID: 4343}
	if err := manager.rotateIfNeeded(context.Background(), definition, "", "auld-generation"); err == nil {
		t.Fatal("rotation treated a positive launchctl PID as dormant")
	}

	if len(controller.unregistrations) != 0 {
		t.Fatal("rotation unregistered a job with a positive PID")
	}
}

func TestReserveForCleanRestartRejectsUnusableExistingService(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     ServiceStatus
		quarantine string
		missing    bool
	}{
		{name: "approval required", status: StatusRequiresApproval},
		{name: "quarantined", status: StatusEnabled, quarantine: "dreich metadata"},
		{name: "missing generation", status: StatusEnabled, missing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := newFakeServiceController()
			temp := t.TempDir()
			bundle, expectations := cachedBundleFixture(t, filepath.Join(temp, "services"), "2.0.0", "canny", "canny payload")
			manager := &Manager{
				UID: os.Getuid(), Bundle: bundle,
				Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
				TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature, SkipCacheGC: true,
			}

			generation := bundle.Generation.ID
			if test.missing {
				generation = "1-missing"
			}

			if _, err := manager.Store.Update(true, func(receipt *Receipt) error {
				receipt.Default = &Registration{Slot: DefaultSlot, Label: Definitions()[0].Label, RegisteredGeneration: generation}
				if !test.missing {
					receipt.Generations[generation] = bundle.Generation
				}

				if test.quarantine != "" {
					receipt.Quarantined[DefaultSlot] = test.quarantine
				}

				return nil
			}); err != nil {
				t.Fatal(err)
			}

			controller.statuses[Definitions()[0].Label] = test.status

			if err := manager.ReserveForCleanRestart(context.Background(), ""); err == nil {
				t.Fatal("unusable existing service was accepted before destructive stop")
			}
		})
	}
}

func TestPendingRegistrationReconcilesExactDormantProgram(t *testing.T) {
	controller := newFakeServiceController()
	temp := t.TempDir()
	bundle, expectations := cachedBundleFixture(t, filepath.Join(temp, "services"), "2.0.0", "canny", "canny payload")
	definition, _ := DefinitionForSlot("08")

	manager := &Manager{
		UID: os.Getuid(), Bundle: bundle,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature, SkipCacheGC: true,
	}
	if _, err := manager.Store.Update(true, func(receipt *Receipt) error {
		receipt.Generations[bundle.Generation.ID] = bundle.Generation
		receipt.Pending = &PendingOperation{Kind: "register", Profile: "bothy", Slot: definition.Slot, UID: os.Getuid(), Generation: bundle.Generation.ID}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	controller.statuses[definition.Label] = StatusEnabled
	controller.jobs[definition.Label] = jobStateForBundle(bundle)

	if err := manager.reconcilePending(context.Background()); err != nil {
		t.Fatal(err)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	lease, ok := receipt.Leases["bothy"]
	if !ok || lease.Slot != definition.Slot || lease.RegisteredGeneration != bundle.Generation.ID || receipt.Pending != nil {
		t.Fatalf("reconciled receipt = %#v", receipt)
	}
}

func TestPendingLiveRegistrationIsQuarantinedWithoutStopping(t *testing.T) {
	controller := newFakeServiceController()
	temp := t.TempDir()
	bundle, expectations := cachedBundleFixture(t, filepath.Join(temp, "services"), "2.0.0", "dreich", "dreich payload")
	definition, _ := DefinitionForSlot("09")

	manager := &Manager{
		UID: os.Getuid(), Bundle: bundle,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature, SkipCacheGC: true,
	}
	if _, err := manager.Store.Update(true, func(receipt *Receipt) error {
		receipt.Generations[bundle.Generation.ID] = bundle.Generation
		receipt.Pending = &PendingOperation{Kind: "register", Profile: "strath", Slot: definition.Slot, UID: os.Getuid(), Generation: bundle.Generation.ID}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	controller.statuses[definition.Label] = StatusEnabled
	controller.jobs[definition.Label] = JobState{Present: true, Running: true, PID: 4242}

	if err := manager.reconcilePending(context.Background()); err != nil {
		t.Fatal(err)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if receipt.Pending != nil || receipt.Quarantined[definition.Slot] == "" {
		t.Fatalf("live interrupted registration was not quarantined: %#v", receipt)
	}

	if len(controller.unregistrations) != 0 {
		t.Fatalf("live interrupted registration was stopped: %v", controller.unregistrations)
	}
}

func TestPendingRotationReconcilesCandidateWithoutChangingProfileSlot(t *testing.T) {
	controller := newFakeServiceController()
	temp := t.TempDir()
	cacheRoot := filepath.Join(temp, "services")
	auld, expectations := cachedBundleFixture(t, cacheRoot, "1.0.0", "auld", "auld payload")
	newer, _ := cachedBundleFixture(t, cacheRoot, "2.0.0", "new", "new payload")
	definition, _ := DefinitionForSlot("10")
	manager := &Manager{
		UID: os.Getuid(), Bundle: newer,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature, SkipCacheGC: true,
	}
	seedPendingRotation(t, manager, auld, newer, "croft", definition)
	controller.statuses[definition.Label] = StatusEnabled
	controller.jobs[definition.Label] = jobStateForBundle(newer)

	if err := manager.reconcilePending(context.Background()); err != nil {
		t.Fatal(err)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	lease := receipt.Leases["croft"]
	if lease.Slot != definition.Slot || lease.RegisteredGeneration != newer.Generation.ID || receipt.Pending != nil {
		t.Fatalf("rotation reconciliation changed isolation: %#v", receipt)
	}
}

func TestPendingRotationQuarantinesAmbiguousBundleBuildMetadata(t *testing.T) {
	controller := newFakeServiceController()
	temp := t.TempDir()
	cacheRoot := filepath.Join(temp, "services")
	auld, expectations := cachedBundleFixture(t, cacheRoot, "2.0.0", "auld", "auld payload")
	newer, newerExpectations := cachedBundleFixture(t, cacheRoot, "2.0.0", "new", "new payload")
	infoPath := filepath.Join(newer.AppPath, "Contents", "Info.plist")

	data, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatal(err)
	}

	oldBuild := "<key>CFBundleVersion</key><string>" + newer.Generation.BundleBuild + "</string>"
	newBuild := "<key>CFBundleVersion</key><string>" + auld.Generation.BundleBuild + "</string>"

	updated := strings.Replace(string(data), oldBuild, newBuild, 1)
	if updated == string(data) {
		t.Fatal("fixture bundle build metadata was not updated")
	}

	if err := os.WriteFile(infoPath, []byte(updated), 0o644); err != nil { // #nosec G306 G703 -- controlled public plist ambiguity fixture.
		t.Fatal(err)
	}

	newer, err = ValidateBundle(newer.AppPath, newerExpectations)
	if err != nil {
		t.Fatal(err)
	}

	definition, _ := DefinitionForSlot("14")
	manager := &Manager{
		UID: os.Getuid(), Bundle: newer,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature, SkipCacheGC: true,
	}
	seedPendingRotation(t, manager, auld, newer, "haar", definition)
	controller.statuses[definition.Label] = StatusEnabled
	controller.jobs[definition.Label] = jobStateForBundle(newer)

	if err := manager.reconcilePending(context.Background()); err != nil {
		t.Fatal(err)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if receipt.Pending != nil || receipt.Quarantined[definition.Slot] == "" || receipt.Leases["haar"].RegisteredGeneration != auld.Generation.ID {
		t.Fatalf("ambiguous rotation was not quarantined with its old lease intact: %#v", receipt)
	}
}

func TestFreshPendingRegistrationWaitsWithinStartupDeadline(t *testing.T) {
	controller := newFakeServiceController()
	temp := t.TempDir()
	bundle, expectations := cachedBundleFixture(t, filepath.Join(temp, "services"), "2.0.0", "braw", "braw payload")
	definition, _ := DefinitionForSlot("15")
	now := time.Now().UTC().Round(0)

	manager := &Manager{
		UID: os.Getuid(), Bundle: bundle,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature,
		Now: func() time.Time { return now }, SkipCacheGC: true,
	}
	if _, err := manager.Store.Update(true, func(receipt *Receipt) error {
		receipt.Generations[bundle.Generation.ID] = bundle.Generation
		receipt.Pending = &PendingOperation{
			Kind: "register", Profile: "bairn", Slot: definition.Slot, UID: os.Getuid(),
			Generation: bundle.Generation.ID, CreatedAt: now,
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	err := manager.Launch(ctx, config.Default(), config.Paths{Profile: "other"}, "", time.Second, []string{"PATH=/usr/bin:/bin"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Launch() = %v, want bounded wait for active global transaction", err)
	}

	receipt, err := manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if receipt.Pending == nil || len(controller.registrations) != 0 {
		t.Fatalf("concurrent starter disturbed active transaction: receipt=%#v registrations=%v", receipt, controller.registrations)
	}

	now = now.Add(pendingReconcileGrace + time.Second)

	if err := manager.reconcilePending(context.Background()); err != nil {
		t.Fatal(err)
	}

	receipt, err = manager.Receipt()
	if err != nil || receipt.Pending != nil {
		t.Fatalf("stale absent registration was not reconciled: receipt=%#v err=%v", receipt, err)
	}
}

func TestReconcilePendingDoesNotMistakeReusedPIDForOwner(t *testing.T) {
	controller := newFakeServiceController()
	temp := t.TempDir()
	bundle, expectations := cachedBundleFixture(t, filepath.Join(temp, "services"), "2.0.0", "canny", "canny payload")
	definition, _ := DefinitionForSlot("16")
	now := time.Now().UTC().Round(0)

	ownerStart, err := grpty.ProcessStartTime(os.Getpid())
	if err != nil {
		t.Skipf("ProcessStartTime unsupported on this platform: %v", err)
	}

	manager := &Manager{
		UID: os.Getuid(), Bundle: bundle,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature,
		Now: func() time.Time { return now }, SkipCacheGC: true,
	}
	if _, err := manager.Store.Update(true, func(receipt *Receipt) error {
		receipt.Generations[bundle.Generation.ID] = bundle.Generation
		receipt.Pending = &PendingOperation{
			Kind: "register", Profile: "bairn", Slot: definition.Slot, UID: os.Getuid(),
			Generation: bundle.Generation.ID, CreatedAt: now,
			OwnerPID: os.Getpid(), OwnerPIDStartTime: ownerStart + 1,
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := manager.reconcilePending(context.Background()); err != nil {
		t.Fatal(err)
	}

	receipt, err := manager.Receipt()
	if err != nil || receipt.Pending != nil {
		t.Fatalf("reused owner PID kept transaction pending: receipt=%#v err=%v", receipt, err)
	}
}

func TestInterruptedRemovalCompletesOnlyAfterExactJobIsAbsent(t *testing.T) {
	controller := newFakeServiceController()
	temp := t.TempDir()
	bundle, expectations := cachedBundleFixture(t, filepath.Join(temp, "services"), "2.0.0", "thrawn", "thrawn payload")
	definition, _ := DefinitionForSlot("16")
	manager := &Manager{
		UID: os.Getuid(), Bundle: bundle,
		Store: ReceiptStore{Root: filepath.Join(temp, "receipt"), UID: os.Getuid()}, Controller: controller,
		TeamID: testTeam, Requirement: testRequirement, Verifier: expectations.VerifySignature, SkipCacheGC: true,
	}
	seed := func() {
		if _, err := manager.Store.Update(false, func(receipt *Receipt) error {
			receipt.Leases["thrawn"] = Lease{Profile: "thrawn", Slot: definition.Slot, UID: os.Getuid(), RegisteredGeneration: bundle.Generation.ID}
			receipt.Pending = &PendingOperation{Kind: "remove", Profile: "thrawn", Slot: definition.Slot, UID: os.Getuid(), Generation: bundle.Generation.ID}

			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := manager.Store.Update(true, func(receipt *Receipt) error {
		receipt.Generations[bundle.Generation.ID] = bundle.Generation
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	seed()

	controller.statuses[definition.Label] = StatusEnabled
	controller.jobs[definition.Label] = jobStateForBundle(bundle)

	if err := manager.reconcilePending(context.Background()); err != nil {
		t.Fatal(err)
	}

	receipt, err := manager.Receipt()
	if err != nil || receipt.Pending != nil {
		t.Fatalf("present removal was not safely abandoned: receipt=%#v err=%v", receipt, err)
	}

	if _, ok := receipt.Leases["thrawn"]; !ok {
		t.Fatal("present service lease was released")
	}

	seed()

	controller.statuses[definition.Label] = StatusNotRegistered
	delete(controller.jobs, definition.Label)

	if err := manager.reconcilePending(context.Background()); err != nil {
		t.Fatal(err)
	}

	receipt, err = manager.Receipt()
	if err != nil {
		t.Fatal(err)
	}

	if receipt.Pending != nil {
		t.Fatalf("absent removal remains pending: %#v", receipt.Pending)
	}

	if _, ok := receipt.Leases["thrawn"]; ok {
		t.Fatal("confirmed absent removal retained its lease")
	}
}
