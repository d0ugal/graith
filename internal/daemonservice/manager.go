package daemonservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/d0ugal/graith/internal/agent"
	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/testprocess"
)

type Mode string

const (
	ModeManaged           Mode = "managed"
	ModeLinuxFallback     Mode = "linux-direct-spawn"
	ModeOlderMacFallback  Mode = "older-macos-direct-spawn"
	ModeUnbundledFallback Mode = "unbundled-direct-spawn"
)

const pendingReconcileGrace = 15 * time.Second

var ErrPendingInProgress = errors.New("daemon service transaction is still in progress")

type Resolution struct {
	Mode    Mode
	Reason  string
	Manager *Manager
}

type Manager struct {
	UID         int
	Bundle      ValidatedBundle
	Store       ReceiptStore
	ControlRoot string
	Controller  ServiceController
	Now         func() time.Time
	TeamID      string
	Requirement string
	Development bool
	Verifier    func(string) (SignatureInfo, error)
	SkipCacheGC bool // test-only seam for synthetic managers without real bundles

	// lifecycleMutationGuard is nil in production-created managers, which uses
	// the fail-closed process guard. Package tests may set it on one synthetic
	// manager so fake controllers and temporary receipts exercise allowed paths
	// without weakening unrelated tests or production callers.
	lifecycleMutationGuard func(string) error
}

func (manager *Manager) guardLifecycleMutation(operation string) error {
	if manager.lifecycleMutationGuard != nil {
		return manager.lifecycleMutationGuard(operation)
	}

	return testprocess.RefuseDaemonLifecycleMutation(operation)
}

type ResolveOptions struct {
	GOOS             string
	MacOSMajor       int
	Managed          bool
	Executable       string
	Version          string
	Commit           string
	UID              int
	Controller       ServiceController
	Expectations     BundleExpectations
	ControlRoot      string
	ReceiptRoot      string
	SkipReceiptCheck bool

	lifecycleMutationGuard func(string) error
}

func Resolve(executable, version, commit string, uid int) (Resolution, error) {
	return ResolveContext(context.Background(), executable, version, commit, uid)
}

func ResolveContext(ctx context.Context, executable, version, commit string, uid int) (Resolution, error) {
	return resolveDefault(ctx, executable, version, commit, uid, false)
}

func ResolveForRepair(executable, version, commit string, uid int) (Resolution, error) {
	return resolveDefault(context.Background(), executable, version, commit, uid, true)
}

func resolveDefault(ctx context.Context, executable, version, commit string, uid int, skipReceiptCheck bool) (Resolution, error) {
	major, err := currentMacOSMajorContext(ctx)
	if err != nil && runtime.GOOS == "darwin" {
		return Resolution{}, err
	}

	teamID, requirement, err := stableSigningExpectation()
	if err != nil {
		return Resolution{}, err
	}

	var verifyDistribution func(string) error
	if teamID != "" {
		verifyDistribution = func(path string) error {
			return VerifyDarwinDistributionContext(ctx, path)
		}
	}

	return resolveWith(ctx, ResolveOptions{
		GOOS: runtime.GOOS, MacOSMajor: major, Managed: IsManagedBuild(),
		Executable: executable, Version: version, Commit: commit, UID: uid,
		Controller: DarwinController{},
		Expectations: BundleExpectations{
			Version: version, Commit: commit, StandalonePath: executable,
			TeamID: teamID, Requirement: requirement,
			AllowDevelopmentSig: DevelopmentBuild == "true",
			VerifySignature: func(path string) (SignatureInfo, error) {
				return VerifyDarwinSignatureContext(ctx, path)
			},
			VerifyDistribution: verifyDistribution,
		},
		SkipReceiptCheck: skipReceiptCheck,
	})
}

func ResolveWith(options ResolveOptions) (Resolution, error) {
	return resolveWith(context.Background(), options)
}

func resolveWith(ctx context.Context, options ResolveOptions) (Resolution, error) {
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}

	if options.GOOS != "darwin" {
		return Resolution{Mode: ModeLinuxFallback, Reason: "Service Management is macOS-only"}, nil
	}

	if options.MacOSMajor < 13 {
		return Resolution{Mode: ModeOlderMacFallback, Reason: "SMAppService requires macOS 13 or newer"}, nil
	}

	if !options.Managed {
		return Resolution{Mode: ModeUnbundledFallback, Reason: "this source/unbundled build has no signed matching Graith.app"}, nil
	}

	guard := options.lifecycleMutationGuard
	if guard == nil {
		guard = testprocess.RefuseDaemonLifecycleMutation
	}

	if err := guard("resolve managed daemon service state"); err != nil {
		return Resolution{}, err
	}

	bundle, present, err := DiscoverBundle(options.Executable, options.Expectations)
	if err != nil {
		return Resolution{}, err
	}

	if !present {
		return Resolution{}, errors.New("managed Graith build is missing its required Graith.app")
	}

	cached, err := CacheBundleContext(ctx, bundle, options.Expectations, options.UID)
	if err != nil {
		return Resolution{}, err
	}

	controlRoot := options.ControlRoot
	if controlRoot == "" {
		controlRoot, err = ServiceControlRootContext(ctx, options.UID)
		if err != nil {
			return Resolution{}, err
		}
	}

	receiptRoot := options.ReceiptRoot
	if receiptRoot == "" {
		receiptRoot, err = ReceiptRoot(options.UID)
		if err != nil {
			return Resolution{}, err
		}
	}

	controller := options.Controller
	if controller == nil {
		controller = DarwinController{}
	}

	manager := &Manager{
		UID: options.UID, Bundle: cached,
		Store:       ReceiptStore{Root: receiptRoot, UID: options.UID},
		ControlRoot: controlRoot, Controller: controller, Now: time.Now,
		TeamID: options.Expectations.TeamID, Requirement: options.Expectations.Requirement,
		Development: options.Expectations.AllowDevelopmentSig, Verifier: options.Expectations.VerifySignature,

		// Preserve a package-test opt-in only on the one synthetic manager that
		// the injected resolution produced. Production resolves the real guard.
		lifecycleMutationGuard: guard,
	}
	if !options.SkipReceiptCheck {
		if err := manager.ensureReceipt(ctx); err != nil {
			return Resolution{}, err
		}
	}

	return Resolution{Mode: ModeManaged, Manager: manager}, nil
}

func DetectMode() (Mode, string, error) {
	if runtime.GOOS != "darwin" {
		return ModeLinuxFallback, "Service Management is macOS-only", nil
	}

	major, err := currentMacOSMajor()
	if err != nil {
		return "", "", err
	}

	if major < 13 {
		return ModeOlderMacFallback, "SMAppService requires macOS 13 or newer", nil
	}

	if !IsManagedBuild() {
		return ModeUnbundledFallback, "this source/unbundled build has no signed matching Graith.app", nil
	}

	return ModeManaged, "signed packaged build", nil
}

// ValidateManagedInstallationContext verifies the exact package-associated app
// without caching it or creating service state. Diagnostic commands use this
// read-only gate before reporting a managed installation as healthy.
func ValidateManagedInstallationContext(ctx context.Context, executable, version, commit string) error {
	teamID, requirement, err := stableSigningExpectation()
	if err != nil {
		return err
	}

	var verifyDistribution func(string) error
	if teamID != "" {
		verifyDistribution = func(path string) error { return VerifyDarwinDistributionContext(ctx, path) }
	}

	return validateManagedInstallation(executable, BundleExpectations{
		Version: version, Commit: commit, StandalonePath: executable,
		TeamID: teamID, Requirement: requirement,
		AllowDevelopmentSig: DevelopmentBuild == "true",
		VerifySignature: func(path string) (SignatureInfo, error) {
			return VerifyDarwinSignatureContext(ctx, path)
		},
		VerifyDistribution: verifyDistribution,
	})
}

func validateManagedInstallation(executable string, expectations BundleExpectations) error {
	bundle, present, err := DiscoverBundle(executable, expectations)
	if err != nil {
		return err
	}

	if !present {
		return errors.New("managed Graith build is missing its required Graith.app")
	}

	if expectations.VerifyDistribution != nil {
		if err := expectations.VerifyDistribution(bundle.AppPath); err != nil {
			return fmt.Errorf("validate daemon service distribution: %w", err)
		}
	}

	return nil
}

func ReadReceipt(uid int) (Receipt, error) {
	root, err := ReceiptRoot(uid)
	if err != nil {
		return Receipt{}, err
	}

	return (ReceiptStore{Root: root, UID: uid}).Load()
}

func (manager *Manager) ensureReceipt(ctx context.Context) error {
	if _, err := manager.Store.Load(); err == nil {
		_, err = manager.Store.Update(false, func(receipt *Receipt) error {
			receipt.Generations[manager.Bundle.Generation.ID] = manager.Bundle.Generation
			return nil
		})
		if err != nil {
			return err
		}

		if err := manager.reconcilePending(ctx); errors.Is(err, ErrPendingInProgress) {
			return nil
		} else {
			return err
		}
	} else if !errors.Is(err, ErrReceiptMissing) {
		return err
	}

	controllerPath := controllerExecutable(manager.Bundle.AppPath)
	for _, definition := range Definitions() {
		status, err := manager.Controller.Status(ctx, controllerPath, definition)
		if err != nil {
			return fmt.Errorf("inspect service %s before receipt initialization: %w", definition.Label, err)
		}

		state, err := manager.Controller.JobState(ctx, manager.UID, definition)
		if err != nil {
			return err
		}

		if status != StatusNotRegistered && status != StatusNotFound || state.Present {
			return fmt.Errorf("daemon service receipt is missing while %s has status %s or a launchd job; run gr daemon service repair", definition.Label, status)
		}
	}

	_, err := manager.Store.Update(true, func(receipt *Receipt) error {
		receipt.Generations[manager.Bundle.Generation.ID] = manager.Bundle.Generation
		return nil
	})

	return err
}

// reconcilePending completes or safely abandons the one global registration
// transaction after an interrupted CLI. launchd's exact resolved program path
// distinguishes the old and candidate immutable app generations. Any live or
// indeterminate job is quarantined rather than stopped or reassigned.
func (manager *Manager) reconcilePending(ctx context.Context) error {
	receipt, err := manager.Store.Load()
	if err != nil || receipt.Pending == nil {
		return err
	}

	pending := *receipt.Pending
	if pendingOwnerMatches(pending) || pending.OwnerPIDStartTime == 0 && pendingIsFresh(pending, manager.now()) {
		return ErrPendingInProgress
	}

	definition, err := DefinitionForSlot(pending.Slot)
	if err != nil {
		return err
	}

	if pending.UID != manager.UID {
		return errors.New("daemon service pending transaction belongs to another user")
	}

	candidate, ok := receipt.Generations[pending.Generation]
	if !ok {
		return fmt.Errorf("pending daemon service generation %s is absent from receipt", pending.Generation)
	}

	validatedCandidate, err := manager.validateGeneration(candidate)
	if err != nil {
		return fmt.Errorf("validate pending daemon service generation: %w", err)
	}

	controllerPath := controllerExecutable(validatedCandidate.AppPath)

	status, err := manager.Controller.Status(ctx, controllerPath, definition)
	if err != nil {
		return err
	}

	if err := validateControllerStatus(status); err != nil {
		return err
	}

	job, err := manager.Controller.JobState(ctx, manager.UID, definition)
	if err != nil {
		return err
	}

	if pending.Kind == "remove" {
		return manager.reconcilePendingRemoval(receipt, pending, job, status)
	}

	if job.Running || job.PID > 0 {
		return manager.quarantinePending(pending, "interrupted registration has a live or indeterminate daemon")
	}

	if job.Present {
		matches, matchErr := manager.matchingReceiptBundles(receipt, job)
		if matchErr != nil {
			return matchErr
		}

		if len(matches) != 1 {
			return manager.quarantinePending(pending, "interrupted registration cannot identify one verified app generation")
		}

		if matches[0].Generation.ID == validatedCandidate.Generation.ID {
			return manager.commitPendingGeneration(pending, definition)
		}

		if pending.Kind == "rotate" {
			oldGeneration, oldErr := registeredGenerationID(receipt, pending)
			if oldErr != nil {
				return oldErr
			}

			if matches[0].Generation.ID == oldGeneration {
				return manager.clearPending(pending)
			}
		}

		return manager.quarantinePending(pending, "interrupted registration points at an unknown service program")
	}

	switch status {
	case StatusNotRegistered, StatusNotFound:
		// No job was installed. A registration retry can start from the durable
		// pre-operation mapping (rotation) or allocate this slot again.
		return manager.clearPending(pending)
	case StatusEnabled, StatusRequiresApproval:
		return manager.quarantinePending(pending, "Service Management status has no matching launchd job")
	default:
		return fmt.Errorf("unsupported Service Management status %q", status)
	}
}

func pendingOwnerMatches(pending PendingOperation) bool {
	if pending.OwnerPID <= 1 || pending.OwnerPIDStartTime == 0 {
		return false
	}

	startTime, err := grpty.ProcessStartTime(pending.OwnerPID)

	return err == nil && startTime == pending.OwnerPIDStartTime
}

func pendingIsFresh(pending PendingOperation, now time.Time) bool {
	return !pending.CreatedAt.IsZero() && now.Before(pending.CreatedAt.Add(pendingReconcileGrace))
}

func (manager *Manager) claimPending(pending *PendingOperation) error {
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return fmt.Errorf("create daemon service transaction token: %w", err)
	}

	pending.Token = hex.EncodeToString(token)
	pending.CreatedAt = manager.now()
	pending.OwnerPID = os.Getpid()

	startTime, err := grpty.ProcessStartTime(pending.OwnerPID)
	if err != nil {
		return fmt.Errorf("identify daemon service transaction owner: %w", err)
	}

	pending.OwnerPIDStartTime = startTime

	return nil
}

func (manager *Manager) reconcilePendingRemoval(receipt Receipt, pending PendingOperation, job JobState, status ServiceStatus) error {
	if job.Present {
		matches, err := manager.matchingReceiptBundles(receipt, job)
		if err != nil {
			return err
		}

		if len(matches) != 1 || matches[0].Generation.ID != pending.Generation {
			return manager.quarantinePending(pending, "interrupted removal points at an unknown app generation")
		}
		// The stop/unregister did not complete. Preserve the mapping and let an
		// explicit retry authenticate and stop it again.
		return manager.clearPending(pending)
	}

	if status == StatusNotRegistered || status == StatusNotFound {
		return manager.completePendingRemoval(pending)
	}

	return manager.quarantinePending(pending, "interrupted removal has indeterminate Service Management state")
}

func (manager *Manager) validateGeneration(generation Generation) (ValidatedBundle, error) {
	teamID, requirement := generation.TeamID, generation.Requirement
	if manager.TeamID != "" || manager.Requirement != "" {
		if manager.TeamID == "" || manager.Requirement == "" || generation.TeamID != manager.TeamID || generation.Requirement != manager.Requirement {
			return ValidatedBundle{}, errors.New("daemon service generation signing identity does not match this build")
		}

		teamID, requirement = manager.TeamID, manager.Requirement
	}

	validated, err := ValidateBundle(generation.AppPath, BundleExpectations{
		Version: generation.Version, Commit: generation.Commit,
		TeamID: teamID, Requirement: requirement,
		AllowDevelopmentSig: manager.Development, VerifySignature: manager.signatureVerifier(),
	})
	if err != nil {
		return ValidatedBundle{}, err
	}

	if !generationMatchesReceipt(generation, validated.Generation) {
		return ValidatedBundle{}, errors.New("validated daemon service generation does not match its receipt")
	}

	return validated, nil
}

func registeredGenerationID(receipt Receipt, pending PendingOperation) (string, error) {
	var generationID string

	if pending.Profile == "" {
		if receipt.Default == nil || pending.Slot != DefaultSlot {
			return "", errors.New("pending default rotation has no registration")
		}

		generationID = receipt.Default.RegisteredGeneration
	} else {
		lease, ok := receipt.Leases[pending.Profile]
		if !ok || lease.Slot != pending.Slot {
			return "", errors.New("pending named rotation has no matching lease")
		}

		generationID = lease.RegisteredGeneration
	}

	if _, ok := receipt.Generations[generationID]; !ok {
		return "", fmt.Errorf("registered daemon service generation %s is absent from receipt", generationID)
	}

	return generationID, nil
}

func jobMatchesGeneration(job JobState, bundle ValidatedBundle) bool {
	if job.ParentBundleIdentifier != ServiceManifest().BundleIdentifier || job.ParentBundleVersion != bundle.Generation.BundleBuild {
		return false
	}

	if job.ProgramIdentifier == "Contents/MacOS/"+DaemonExecutable {
		return true
	}

	return sameExecutable(job.Program, filepath.Join(bundle.AppPath, "Contents", "MacOS", DaemonExecutable))
}

func (manager *Manager) matchingReceiptBundles(receipt Receipt, job JobState) ([]ValidatedBundle, error) {
	var matches []ValidatedBundle

	for _, generation := range receipt.Generations {
		validated, err := manager.validateGeneration(generation)
		if err != nil {
			return nil, err
		}

		if jobMatchesGeneration(job, validated) {
			matches = append(matches, validated)
		}
	}

	return matches, nil
}

func (manager *Manager) commitPendingGeneration(pending PendingOperation, definition Definition) error {
	_, err := manager.Store.Update(false, func(receipt *Receipt) error {
		if receipt.Pending == nil {
			return nil
		}

		if *receipt.Pending != pending {
			return errors.New("daemon service pending transaction changed during reconciliation")
		}

		switch pending.Kind {
		case "register":
			if pending.Profile == "" {
				if definition.Slot != DefaultSlot {
					return errors.New("named service registration has an empty profile")
				}

				receipt.Default = &Registration{Slot: definition.Slot, Label: definition.Label, RegisteredGeneration: pending.Generation}
			} else {
				if definition.Slot == DefaultSlot {
					return errors.New("default service registration has a named profile")
				}

				receipt.Leases[pending.Profile] = Lease{Profile: pending.Profile, Slot: definition.Slot, UID: pending.UID, RegisteredGeneration: pending.Generation}
			}
		case "rotate":
			if pending.Profile == "" {
				if receipt.Default == nil || receipt.Default.Slot != definition.Slot {
					return errors.New("default registration changed during rotation reconciliation")
				}

				receipt.Default.RegisteredGeneration = pending.Generation
			} else {
				lease, ok := receipt.Leases[pending.Profile]
				if !ok || lease.Slot != definition.Slot {
					return errors.New("named lease changed during rotation reconciliation")
				}

				lease.RegisteredGeneration = pending.Generation
				receipt.Leases[pending.Profile] = lease
			}
		default:
			return fmt.Errorf("unknown pending daemon service operation %q", pending.Kind)
		}

		receipt.Pending = nil

		return nil
	})

	return err
}

func (manager *Manager) clearPending(pending PendingOperation) error {
	_, err := manager.Store.Update(false, func(receipt *Receipt) error {
		if receipt.Pending == nil {
			return nil
		}

		if *receipt.Pending != pending {
			return errors.New("daemon service pending transaction changed during reconciliation")
		}

		receipt.Pending = nil

		return nil
	})

	return err
}

func (manager *Manager) quarantinePending(pending PendingOperation, reason string) error {
	_, err := manager.Store.Update(false, func(receipt *Receipt) error {
		if receipt.Pending == nil {
			return nil
		}

		if *receipt.Pending != pending {
			return errors.New("daemon service pending transaction changed during reconciliation")
		}

		if err := QuarantineSlot(receipt, pending.Slot, reason); err != nil {
			return err
		}

		receipt.Pending = nil

		return nil
	})

	return err
}

func (manager *Manager) completePendingRemoval(pending PendingOperation) error {
	_, err := manager.Store.Update(false, func(receipt *Receipt) error {
		if receipt.Pending == nil {
			return nil
		}

		if *receipt.Pending != pending || pending.Kind != "remove" {
			return errors.New("daemon service pending removal changed during reconciliation")
		}

		delete(receipt.Starts, labelForSlot(pending.Slot))

		if pending.Slot == DefaultSlot {
			receipt.Default = nil
			delete(receipt.Quarantined, DefaultSlot)
		} else if pending.Profile != "" {
			if err := ReleaseProfile(receipt, pending.Profile, true); err != nil {
				return err
			}
		} else {
			delete(receipt.Quarantined, pending.Slot)
		}

		receipt.Pending = nil

		return nil
	})

	return err
}

func labelForSlot(slot string) string {
	definition, err := DefinitionForSlot(slot)
	if err != nil {
		return ""
	}

	return definition.Label
}

func (manager *Manager) definitionForProfile(ctx context.Context, profile string) (Definition, error) {
	for {
		err := manager.reconcilePending(ctx)
		if err == nil {
			var definition Definition
			if profile == "" {
				definition = Definitions()[0]
				err = manager.ensureDefaultRegistration(ctx, definition)
			} else {
				definition, err = manager.definitionForNamedProfile(ctx, profile)
			}

			if err == nil {
				return definition, nil
			}
		}

		if !errors.Is(err, ErrPendingInProgress) {
			return Definition{}, err
		}

		select {
		case <-ctx.Done():
			return Definition{}, errors.Join(ctx.Err(), err)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (manager *Manager) definitionForNamedProfile(ctx context.Context, profile string) (Definition, error) {
	receipt, err := manager.Store.Load()
	if err != nil {
		return Definition{}, err
	}

	if lease, ok := receipt.Leases[profile]; ok {
		if reason := receipt.Quarantined[lease.Slot]; reason != "" {
			return Definition{}, fmt.Errorf("daemon service profile %q is quarantined: %s; run gr daemon service repair", profile, reason)
		}

		definition, err := DefinitionForSlot(lease.Slot)
		if err != nil {
			return Definition{}, err
		}

		if err := manager.rotateIfNeeded(ctx, definition, profile, lease.RegisteredGeneration); err != nil {
			return Definition{}, err
		}

		return definition, nil
	}

	var (
		lease    Lease
		pending  PendingOperation
		reserved bool
	)

	_, err = manager.Store.Update(false, func(receipt *Receipt) error {
		receipt.Generations[manager.Bundle.Generation.ID] = manager.Bundle.Generation

		var reserveErr error

		lease, reserved, reserveErr = ReserveProfile(receipt, profile, manager.UID, manager.Bundle.Generation.ID)
		if reserveErr == nil && reserved {
			if reserveErr = manager.claimPending(receipt.Pending); reserveErr == nil {
				pending = *receipt.Pending
			}
		}

		return reserveErr
	})
	if err != nil {
		return Definition{}, err
	}

	definition, err := DefinitionForSlot(lease.Slot)
	if err != nil {
		return Definition{}, err
	}

	if !reserved {
		if err := manager.rotateIfNeeded(ctx, definition, profile, lease.RegisteredGeneration); err != nil {
			return Definition{}, err
		}

		return definition, nil
	}

	if err := registerFreshService(ctx, manager.Controller, controllerExecutable(manager.Bundle.AppPath), definition); err != nil {
		return Definition{}, err
	}

	if err := manager.waitForRegisteredGeneration(ctx, definition, manager.Bundle); err != nil {
		return Definition{}, err
	}

	_, err = manager.Store.Update(false, func(receipt *Receipt) error {
		if receipt.Pending == nil || *receipt.Pending != pending {
			return errors.New("named daemon service pending registration changed")
		}

		if err := CommitProfile(receipt, lease); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return Definition{}, err
	}

	if _, err := manager.GarbageCollectCache(); err != nil {
		return Definition{}, err
	}

	return definition, nil
}

func (manager *Manager) ensureDefaultRegistration(ctx context.Context, definition Definition) error {
	receipt, err := manager.Store.Load()
	if err != nil {
		return err
	}

	if reason := receipt.Quarantined[DefaultSlot]; reason != "" {
		return fmt.Errorf("default daemon service is quarantined: %s; run gr daemon service repair", reason)
	}

	if receipt.Default != nil {
		return manager.rotateIfNeeded(ctx, definition, "", receipt.Default.RegisteredGeneration)
	}

	var (
		pending              PendingOperation
		registeredGeneration string
		acquired             bool
	)

	_, err = manager.Store.Update(false, func(receipt *Receipt) error {
		receipt.Generations[manager.Bundle.Generation.ID] = manager.Bundle.Generation

		if reason := receipt.Quarantined[DefaultSlot]; reason != "" {
			return fmt.Errorf("default daemon service is quarantined: %s; run gr daemon service repair", reason)
		}

		if receipt.Default != nil {
			registeredGeneration = receipt.Default.RegisteredGeneration
			return nil
		}

		if receipt.Pending != nil {
			return fmt.Errorf("%w for slot %s", ErrPendingInProgress, receipt.Pending.Slot)
		}

		candidate := PendingOperation{Kind: "register", Slot: DefaultSlot, UID: manager.UID, Generation: manager.Bundle.Generation.ID}
		if err := manager.claimPending(&candidate); err != nil {
			return err
		}

		pending = candidate
		receipt.Pending = &candidate
		acquired = true

		return nil
	})
	if err != nil {
		return err
	}

	if !acquired {
		return manager.rotateIfNeeded(ctx, definition, "", registeredGeneration)
	}

	if err := registerFreshService(ctx, manager.Controller, controllerExecutable(manager.Bundle.AppPath), definition); err != nil {
		return err
	}

	if err := manager.waitForRegisteredGeneration(ctx, definition, manager.Bundle); err != nil {
		return err
	}

	if err := manager.commitPendingGeneration(pending, definition); err != nil {
		return err
	}

	_, err = manager.GarbageCollectCache()

	return err
}

func (manager *Manager) rotateIfNeeded(ctx context.Context, definition Definition, profile, registeredGeneration string) error {
	if registeredGeneration == manager.Bundle.Generation.ID {
		return registerService(ctx, manager.Controller, controllerExecutable(manager.Bundle.AppPath), definition)
	}

	if registeredGeneration == "" {
		return errors.New("daemon service registration has no recorded generation")
	}

	state, err := manager.Controller.JobState(ctx, manager.UID, definition)
	if err != nil {
		return err
	}

	if state.Running || state.PID > 0 {
		return fmt.Errorf("cannot rotate running daemon service %s", definition.Label)
	}

	receipt, err := manager.Store.Load()
	if err != nil {
		return err
	}

	oldGeneration, ok := receipt.Generations[registeredGeneration]
	if !ok {
		return fmt.Errorf("registered daemon service generation %s is missing from receipt", registeredGeneration)
	}

	oldBundle, err := manager.validateGeneration(oldGeneration)
	if err != nil {
		return fmt.Errorf("validate registered daemon service generation: %w", err)
	}

	pending := PendingOperation{Kind: "rotate", Profile: profile, Slot: definition.Slot, UID: manager.UID, Generation: manager.Bundle.Generation.ID}
	if err := manager.claimPending(&pending); err != nil {
		return err
	}

	if _, err := manager.Store.Update(false, func(receipt *Receipt) error {
		if receipt.Pending != nil {
			return fmt.Errorf("%w for slot %s", ErrPendingInProgress, receipt.Pending.Slot)
		}

		if profile == "" {
			if receipt.Default == nil || receipt.Default.Slot != definition.Slot || receipt.Default.RegisteredGeneration != registeredGeneration {
				return errors.New("default daemon service registration changed before rotation")
			}
		} else {
			lease, ok := receipt.Leases[profile]
			if !ok || lease.Slot != definition.Slot || lease.RegisteredGeneration != registeredGeneration {
				return errors.New("named daemon service registration changed before rotation")
			}
		}

		receipt.Generations[manager.Bundle.Generation.ID] = manager.Bundle.Generation
		receipt.Pending = &pending

		return nil
	}); err != nil {
		return err
	}

	oldController := controllerExecutable(oldBundle.AppPath)

	status, err := manager.Controller.Unregister(ctx, oldController, definition)
	if err != nil {
		return err
	}

	if status != StatusNotRegistered && status != StatusNotFound {
		return fmt.Errorf("unregister old daemon service ended in status %q", status)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := waitForJobAbsent(waitCtx, manager.Controller, manager.UID, definition); err != nil {
		return fmt.Errorf("wait for old daemon service removal: %w", err)
	}

	candidateController := controllerExecutable(manager.Bundle.AppPath)

	registrationErr := registerFreshService(ctx, manager.Controller, candidateController, definition)
	if registrationErr == nil {
		registrationErr = manager.waitForRegisteredGeneration(ctx, definition, manager.Bundle)
	}

	if registrationErr != nil {
		cleanupStatus, cleanupErr := manager.Controller.Unregister(ctx, candidateController, definition)
		if cleanupErr == nil && cleanupStatus != StatusNotRegistered && cleanupStatus != StatusNotFound {
			cleanupErr = fmt.Errorf("unregister failed candidate ended in status %q", cleanupStatus)
		}

		if cleanupErr == nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 5*time.Second)
			cleanupErr = waitForJobAbsent(cleanupCtx, manager.Controller, manager.UID, definition)

			cleanupCancel()
		}

		restoreErr := registerService(ctx, manager.Controller, oldController, definition)
		if restoreErr == nil {
			restoreErr = manager.waitForRegisteredGeneration(ctx, definition, oldBundle)
		}

		var receiptErr error
		if restoreErr != nil {
			receiptErr = manager.quarantinePending(pending, "registration rotation and rollback failed")
		} else {
			receiptErr = manager.clearPending(pending)
		}

		return errors.Join(fmt.Errorf("register new daemon service generation: %w", registrationErr), cleanupErr, restoreErr, receiptErr)
	}

	if err := manager.commitPendingGeneration(pending, definition); err != nil {
		return err
	}

	_, err = manager.GarbageCollectCache()

	return err
}

func (manager *Manager) waitForRegisteredGeneration(ctx context.Context, definition Definition, bundle ValidatedBundle) error {
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		state, err := manager.Controller.JobState(waitCtx, manager.UID, definition)
		if err == nil && state.Present {
			if !jobMatchesGeneration(state, bundle) {
				return fmt.Errorf("registered daemon service %s does not belong to candidate bundle generation %s", definition.Label, bundle.Generation.ID)
			}

			return nil
		}

		select {
		case <-waitCtx.Done():
			if err != nil {
				return errors.Join(waitCtx.Err(), err)
			}

			return waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func (manager *Manager) signatureVerifier() func(string) (SignatureInfo, error) {
	if manager.Verifier != nil {
		return manager.Verifier
	}

	return VerifyDarwinSignature
}

func (manager *Manager) now() time.Time {
	if manager.Now != nil {
		return manager.Now().UTC().Round(0)
	}

	return time.Now().UTC().Round(0)
}

func (manager *Manager) Launch(ctx context.Context, cfg *config.Config, paths config.Paths, configFile string, lifetime time.Duration, environ []string) error {
	if err := manager.guardLifecycleMutation("launch managed daemon service"); err != nil {
		return err
	}

	if cfg == nil {
		return errors.New("daemon service launch requires loaded config")
	}

	if agent.SecurityBoundaryDetectedEnviron(environ) {
		return errors.New("an agent-mode caller cannot become the first managed daemon starter")
	}

	projected, err := ProjectEnvironment(environ, cfg.DaemonService.InheritEnv, manager.UID)
	if err != nil {
		return err
	}

	definition, err := manager.definitionForProfile(ctx, paths.Profile)
	if err != nil {
		return err
	}

	return WithStartLock(manager.ControlRoot, manager.UID, definition, func() error {
		state, err := manager.Controller.JobState(ctx, manager.UID, definition)
		if err != nil {
			return err
		}

		if state.Running || state.PID > 0 {
			return nil
		}

		if err := manager.clearExpiredStart(definition, manager.now()); err != nil {
			return err
		}

		request, err := NewStartupRequest(definition, paths.Profile, configFile, paths, manager.Bundle.Generation.ID, projected, manager.UID, manager.now(), lifetime)
		if err != nil {
			return err
		}

		if _, err := manager.Store.Update(false, func(receipt *Receipt) error {
			return BeginStart(receipt, StartIntent{
				Label: definition.Label, Slot: definition.Slot, Profile: paths.Profile,
				Generation: manager.Bundle.Generation.ID, Nonce: request.Nonce,
				CreatedAt: request.CreatedAt, ExpiresAt: request.ExpiresAt,
			})
		}); err != nil {
			return err
		}

		cleanup := func() {
			_ = RemoveStartupRequest(manager.ControlRoot, manager.UID, definition)
			_, _ = manager.Store.Update(false, func(receipt *Receipt) error { return CancelStart(receipt, definition.Label, request.Nonce) })
		}
		if err := WriteStartupRequest(manager.ControlRoot, request); err != nil {
			cleanup()
			return err
		}

		if err := manager.Controller.Kickstart(ctx, manager.UID, definition); err != nil {
			cleanup()
			return err
		}

		return nil
	})
}

func (manager *Manager) clearExpiredStart(definition Definition, now time.Time) error {
	removed := false

	_, err := manager.Store.Update(false, func(receipt *Receipt) error {
		intent, ok := receipt.Starts[definition.Label]
		if !ok || now.Before(intent.ExpiresAt) {
			return nil
		}

		delete(receipt.Starts, definition.Label)

		removed = true

		return nil
	})
	if err != nil || !removed {
		return err
	}

	if err := RemoveStartupRequest(manager.ControlRoot, manager.UID, definition); err != nil {
		return fmt.Errorf("remove expired daemon service startup request: %w", err)
	}

	return nil
}

func (manager *Manager) Receipt() (Receipt, error) { return manager.Store.Load() }

func (manager *Manager) CurrentDefinition(profile string) (Definition, bool, error) {
	if profile == "" {
		receipt, err := manager.Store.Load()
		return Definitions()[0], receipt.Default != nil, err
	}

	receipt, err := manager.Store.Load()
	if err != nil {
		return Definition{}, false, err
	}

	lease, ok := receipt.Leases[profile]
	if !ok {
		return Definition{}, false, nil
	}

	definition, err := DefinitionForSlot(lease.Slot)

	return definition, true, err
}

// ReserveForCleanRestart makes a supported profile service-addressable before
// a caller stops a working daemon. Existing leases are intentionally left on
// their registered generation while the process is live; Launch rotates the
// dormant registration after the exact old process has exited. A new profile
// is allocated and registered here so capacity or approval failure cannot turn
// an intentional restart into avoidable downtime.
func (manager *Manager) ReserveForCleanRestart(ctx context.Context, profile string) error {
	if err := manager.guardLifecycleMutation("reserve managed daemon clean restart"); err != nil {
		return err
	}

	receipt, err := manager.Store.Load()
	if err != nil {
		return err
	}

	if profile == "" {
		if receipt.Default != nil {
			return manager.validateRestartReservation(ctx, receipt, profile, Definitions()[0], receipt.Default.RegisteredGeneration)
		}
	} else if lease, ok := receipt.Leases[profile]; ok {
		definition, definitionErr := DefinitionForSlot(lease.Slot)
		if definitionErr != nil {
			return definitionErr
		}

		return manager.validateRestartReservation(ctx, receipt, profile, definition, lease.RegisteredGeneration)
	}

	_, err = manager.definitionForProfile(ctx, profile)

	return err
}

func (manager *Manager) validateRestartReservation(ctx context.Context, receipt Receipt, profile string, definition Definition, registeredGeneration string) error {
	if reason := receipt.Quarantined[definition.Slot]; reason != "" {
		return fmt.Errorf("daemon service profile %q is quarantined: %s; run gr daemon service repair", profile, reason)
	}

	if registeredGeneration == "" {
		return fmt.Errorf("daemon service %s has no recorded registered generation", definition.Label)
	}

	generation, ok := receipt.Generations[registeredGeneration]
	if !ok {
		return fmt.Errorf("daemon service generation %s is missing from receipt", registeredGeneration)
	}

	bundle, err := manager.validateGeneration(generation)
	if err != nil {
		return fmt.Errorf("validate daemon service restart generation: %w", err)
	}

	controllerPath := controllerExecutable(bundle.AppPath)

	status, err := manager.Controller.Status(ctx, controllerPath, definition)
	if err != nil {
		return err
	}

	if status != StatusEnabled {
		return fmt.Errorf("daemon service %s is not enabled (status %q); refusing to stop the working daemon", definition.Label, status)
	}

	state, err := manager.Controller.JobState(ctx, manager.UID, definition)
	if err != nil {
		return err
	}

	if (state.Running || state.PID > 0) && state.PID <= 1 {
		return fmt.Errorf("daemon service %s has an unsafe running PID", definition.Label)
	}

	return nil
}
