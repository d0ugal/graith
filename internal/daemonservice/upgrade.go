package daemonservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ResolveUpgradeCandidateContext returns the immutable cached service payload
// that a managed daemon may exec. Fallback installations keep using the
// caller's current executable.
func ResolveUpgradeCandidateContext(ctx context.Context, currentPath, version, commit string, uid int) (string, bool, error) {
	resolution, err := ResolveContext(ctx, currentPath, version, commit, uid)
	if err != nil {
		return "", false, err
	}

	if resolution.Manager == nil {
		return currentPath, false, nil
	}

	return filepath.Join(resolution.Manager.Bundle.AppPath, "Contents", "MacOS", DaemonExecutable), true, nil
}

type ManagedProcess struct {
	Definition Definition
	Profile    string
	Generation Generation
	PID        int
	Store      ReceiptStore
}

type managedProcessEnvironment struct {
	uid         int
	receiptRoot func(int) (string, error)
	executable  func() (string, error)
	pid         int
	validate    func(Generation) (ValidatedBundle, error)
}

func RunningManagedProcess(profile string) (ManagedProcess, bool, error) {
	return runningManagedProcess(profile, managedProcessEnvironment{
		uid:         os.Geteuid(),
		receiptRoot: ReceiptRoot,
		executable:  os.Executable,
		pid:         os.Getpid(),
		validate:    validateRecordedGeneration,
	})
}

func runningManagedProcess(profile string, environment managedProcessEnvironment) (ManagedProcess, bool, error) {
	uid := environment.uid

	root, err := environment.receiptRoot(uid)
	if err != nil {
		return ManagedProcess{}, false, err
	}

	store := ReceiptStore{Root: root, UID: uid}

	receipt, err := store.Load()
	if errors.Is(err, ErrReceiptMissing) {
		return ManagedProcess{}, false, nil
	}

	if err != nil {
		return ManagedProcess{}, false, err
	}

	var (
		definition   Definition
		generationID string
		pid          int
	)

	if profile == "" {
		if receipt.Default == nil || receipt.Default.RunningGeneration == "" {
			return ManagedProcess{}, false, nil
		}

		definition = Definitions()[0]
		generationID = receipt.Default.RunningGeneration
		pid = receipt.Default.RunningPID
	} else {
		lease, ok := receipt.Leases[profile]
		if !ok || lease.RunningGeneration == "" {
			return ManagedProcess{}, false, nil
		}

		definition, err = DefinitionForSlot(lease.Slot)
		if err != nil {
			return ManagedProcess{}, false, err
		}

		generationID = lease.RunningGeneration
		pid = lease.RunningPID
	}

	generation, ok := receipt.Generations[generationID]
	if !ok {
		return ManagedProcess{}, false, fmt.Errorf("running daemon service generation %s is absent from receipt", generationID)
	}

	validated, err := environment.validate(generation)
	if err != nil {
		return ManagedProcess{}, false, err
	}

	current, err := environment.executable()
	if err != nil {
		return ManagedProcess{}, false, err
	}

	if !sameExecutable(current, filepath.Join(validated.AppPath, "Contents", "MacOS", DaemonExecutable)) {
		return ManagedProcess{}, false, nil
	}

	if pid != environment.pid {
		return ManagedProcess{}, false, errors.New("daemon service receipt running PID does not match current process")
	}

	return ManagedProcess{Definition: definition, Profile: profile, Generation: validated.Generation, PID: pid, Store: store}, true, nil
}

func validateRecordedGeneration(generation Generation) (ValidatedBundle, error) {
	teamID := generation.TeamID
	requirement := generation.Requirement

	expectedTeamID, expectedRequirement, err := stableSigningExpectation()
	if err != nil {
		return ValidatedBundle{}, err
	}

	if expectedTeamID != "" {
		if teamID != expectedTeamID || requirement != expectedRequirement {
			return ValidatedBundle{}, errors.New("recorded daemon service signing identity does not match this build")
		}

		teamID = expectedTeamID
		requirement = expectedRequirement
	}

	validated, err := ValidateBundle(generation.AppPath, BundleExpectations{
		Version: generation.Version, Commit: generation.Commit,
		TeamID: teamID, Requirement: requirement,
		AllowDevelopmentSig: DevelopmentBuild == "true", VerifySignature: VerifyDarwinSignature,
	})
	if err != nil {
		return ValidatedBundle{}, err
	}

	if !generationMatchesReceipt(generation, validated.Generation) {
		return ValidatedBundle{}, errors.New("validated daemon service generation does not match its receipt")
	}

	return validated, nil
}

func generationMatchesReceipt(recorded, validated Generation) bool {
	return recorded.ID == validated.ID && recorded.AppPath == validated.AppPath &&
		recorded.Version == validated.Version && recorded.BundleBuild == validated.BundleBuild &&
		recorded.Commit == validated.Commit && recorded.PayloadHash == validated.PayloadHash &&
		recorded.TeamID == validated.TeamID && recorded.Requirement == validated.Requirement
}

func sameExecutable(left, right string) bool {
	resolvedLeft, err := filepath.EvalSymlinks(left)
	if err != nil {
		return false
	}

	resolvedRight, err := filepath.EvalSymlinks(right)

	return err == nil && resolvedLeft == resolvedRight
}

// PrepareManagedUpgrade validates candidatePath as an exact embedded payload
// already recorded by a trusted new CLI. It updates only running-generation
// state; launchd registration remains on the old app until the daemon is down.
func PrepareManagedUpgrade(profile, candidatePath string) (Definition, func() error, bool, error) {
	// Fallback installations preserve the established direct exec path and must
	// not derive or create macOS service receipt state. Check the cheap build and
	// platform gates before RunningManagedProcess touches the protected root.
	if runtime.GOOS != "darwin" || !IsManagedBuild() {
		return Definition{}, nil, false, nil
	}

	major, err := currentMacOSMajor()
	if err != nil {
		return Definition{}, nil, false, err
	}

	if major < 13 {
		return Definition{}, nil, false, nil
	}

	process, managed, err := RunningManagedProcess(profile)
	if err != nil || !managed {
		return Definition{}, nil, managed, err
	}

	return prepareManagedUpgrade(process, profile, candidatePath, validateRecordedGeneration, os.Getpid())
}

func prepareManagedUpgrade(process ManagedProcess, profile, candidatePath string, validate func(Generation) (ValidatedBundle, error), pid int) (Definition, func() error, bool, error) {
	receipt, err := process.Store.Load()
	if err != nil {
		return Definition{}, nil, true, err
	}

	candidate, err := recordedUpgradeCandidate(receipt, candidatePath, validate)
	if err != nil {
		return Definition{}, nil, true, err
	}

	oldGeneration := process.Generation.ID

	_, err = process.Store.Update(false, func(receipt *Receipt) error {
		return setRunningGeneration(receipt, profile, process.Definition, candidate.ID, pid)
	})
	if err != nil {
		return Definition{}, nil, true, err
	}

	rollback := func() error {
		_, err := process.Store.Update(false, func(receipt *Receipt) error {
			return setRunningGeneration(receipt, profile, process.Definition, oldGeneration, pid)
		})

		return err
	}

	return process.Definition, rollback, true, nil
}

func recordedUpgradeCandidate(receipt Receipt, candidatePath string, validate func(Generation) (ValidatedBundle, error)) (Generation, error) {
	var candidate Generation

	found := false

	for _, generation := range receipt.Generations {
		if sameExecutable(candidatePath, filepath.Join(generation.AppPath, "Contents", "MacOS", DaemonExecutable)) {
			candidate = generation
			found = true

			break
		}
	}

	if !found {
		return Generation{}, errors.New("managed daemon upgrade candidate is not a recorded cached Graith.app payload")
	}

	validated, err := validate(candidate)
	if err != nil {
		return Generation{}, fmt.Errorf("validate managed daemon upgrade candidate: %w", err)
	}

	if validated.Generation.ID != candidate.ID || !sameExecutable(candidatePath, filepath.Join(validated.AppPath, "Contents", "MacOS", DaemonExecutable)) {
		return Generation{}, errors.New("managed daemon upgrade candidate path or generation changed during validation")
	}

	return validated.Generation, nil
}

func setRunningGeneration(receipt *Receipt, profile string, definition Definition, generation string, pid int) error {
	if profile == "" {
		if receipt.Default == nil || definition.Slot != DefaultSlot {
			return errors.New("default daemon service registration is missing")
		}

		receipt.Default.RunningGeneration = generation
		receipt.Default.RunningPID = pid

		return nil
	}

	lease, ok := receipt.Leases[profile]
	if !ok || lease.Slot != definition.Slot {
		return errors.New("named daemon service lease changed during upgrade")
	}

	lease.RunningGeneration = generation
	lease.RunningPID = pid
	receipt.Leases[profile] = lease

	return nil
}

// ValidateRetainedAdoptedService validates a managed daemon after an exec
// upgrade whose running image may be an immutable retained copy. Unlike
// RunningManagedProcess, this does not compare os.Executable with the cached
// app payload: the daemon layer has already bound and verified the running
// image against its upgrade manifest before calling this function. Instead it
// anchors the manifest's original candidate path to the exact validated
// generation recorded as running for this service and PID.
//
// This is deliberately adoption-only and fail-closed. A missing receipt or
// service registration is an error, never an unmanaged result.
func ValidateRetainedAdoptedService(label, slot, profile, candidatePath string) (Definition, error) {
	return validateRetainedAdoptedService(label, slot, profile, candidatePath, retainedAdoptionEnvironment{
		uid:         os.Geteuid(),
		receiptRoot: ReceiptRoot,
		pid:         os.Getpid(),
		validate:    validateRecordedGeneration,
	})
}

// PrepareRetainedManagedUpgrade validates a previously adopted managed daemon
// whose running image is an immutable retained copy, then stages the exact next
// cached generation. currentCandidatePath is the manifest-bound original
// payload for the running generation; the daemon layer remains responsible for
// binding the retained running image to that manifest before calling here.
//
// Unlike PrepareManagedUpgrade, this function is always fail-closed: it is used
// only after the caller has explicitly selected managed adoption, so missing or
// mismatched receipt state is an error rather than an unmanaged fallback.
func PrepareRetainedManagedUpgrade(
	label, slot, profile, currentCandidatePath, nextCandidatePath string,
) (Definition, func() error, error) {
	return prepareRetainedManagedUpgrade(
		label, slot, profile, currentCandidatePath, nextCandidatePath,
		retainedAdoptionEnvironment{
			uid:         os.Geteuid(),
			receiptRoot: ReceiptRoot,
			pid:         os.Getpid(),
			validate:    validateRecordedGeneration,
		},
	)
}

type retainedAdoptionEnvironment struct {
	uid         int
	receiptRoot func(int) (string, error)
	pid         int
	validate    func(Generation) (ValidatedBundle, error)
}

func validateRetainedAdoptedService(label, slot, profile, candidatePath string, environment retainedAdoptionEnvironment) (Definition, error) {
	process, err := retainedAdoptedManagedProcess(label, slot, profile, candidatePath, environment)
	if err != nil {
		return Definition{}, err
	}

	return process.Definition, nil
}

func prepareRetainedManagedUpgrade(
	label, slot, profile, currentCandidatePath, nextCandidatePath string,
	environment retainedAdoptionEnvironment,
) (Definition, func() error, error) {
	process, err := retainedAdoptedManagedProcess(label, slot, profile, currentCandidatePath, environment)
	if err != nil {
		return Definition{}, nil, err
	}

	definition, rollback, managed, err := prepareManagedUpgrade(
		process, profile, nextCandidatePath, environment.validate, environment.pid,
	)
	if err != nil {
		return Definition{}, nil, err
	}

	if !managed || rollback == nil {
		return Definition{}, nil, errors.New("retained managed daemon upgrade transaction is incomplete")
	}

	return definition, rollback, nil
}

func retainedAdoptedManagedProcess(
	label, slot, profile, candidatePath string,
	environment retainedAdoptionEnvironment,
) (ManagedProcess, error) {
	definition, err := ValidateMarker(label, slot)
	if err != nil {
		return ManagedProcess{}, err
	}

	if err := ProfileForDefinition(definition, profile); err != nil {
		return ManagedProcess{}, err
	}

	root, err := environment.receiptRoot(environment.uid)
	if err != nil {
		return ManagedProcess{}, err
	}

	store := ReceiptStore{Root: root, UID: environment.uid}

	receipt, err := store.Load()
	if err != nil {
		return ManagedProcess{}, fmt.Errorf("load managed daemon service receipt for adoption: %w", err)
	}

	var (
		generationID string
		runningPID   int
	)

	if profile == "" {
		registration := receipt.Default
		if registration == nil || registration.Slot != definition.Slot || registration.Label != definition.Label {
			return ManagedProcess{}, errors.New("managed daemon service default registration does not match adoption marker")
		}

		generationID = registration.RunningGeneration
		runningPID = registration.RunningPID
	} else {
		lease, ok := receipt.Leases[profile]
		if !ok || lease.Profile != profile || lease.Slot != definition.Slot {
			return ManagedProcess{}, errors.New("managed daemon service lease does not match adoption marker and profile")
		}

		generationID = lease.RunningGeneration
		runningPID = lease.RunningPID
	}

	if runningPID != environment.pid {
		return ManagedProcess{}, errors.New("managed daemon service receipt running PID does not match current process")
	}

	if generationID == "" {
		return ManagedProcess{}, errors.New("managed daemon service receipt has no running generation")
	}

	generation, ok := receipt.Generations[generationID]
	if !ok {
		return ManagedProcess{}, fmt.Errorf("running daemon service generation %s is absent from receipt", generationID)
	}

	validated, err := environment.validate(generation)
	if err != nil {
		return ManagedProcess{}, fmt.Errorf("validate retained daemon service generation: %w", err)
	}

	if !generationMatchesReceipt(generation, validated.Generation) {
		return ManagedProcess{}, errors.New("validated retained daemon service generation does not match its receipt")
	}

	payloadPath := filepath.Join(validated.AppPath, "Contents", "MacOS", DaemonExecutable)
	if !sameExecutable(candidatePath, payloadPath) {
		return ManagedProcess{}, errors.New("upgrade manifest candidate is not the validated running daemon service payload")
	}

	return ManagedProcess{
		Definition: definition,
		Profile:    profile,
		Generation: validated.Generation,
		PID:        runningPID,
		Store:      store,
	}, nil
}
