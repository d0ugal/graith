package daemonservice

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

type Bootstrap struct {
	Definition Definition
	Request    StartupRequest
	Generation Generation
}

type bootstrapEnvironment struct {
	uid             int
	receiptRoot     func(int) (string, error)
	controlRoot     func(int) (string, error)
	executable      func() (string, error)
	verifySignature func(string) (SignatureInfo, error)
}

// BootstrapFreshService is the only fresh managed-service entry point. It
// derives both roots from the OS account, validates the immutable cached app
// and receipt/lease agreement, consumes the request once, installs its minimal
// environment, and clears the durable nonce.
func BootstrapFreshService(label, slot string, now time.Time) (_ Bootstrap, returnErr error) {
	return bootstrapFreshService(label, slot, now, bootstrapEnvironment{
		uid:             os.Geteuid(),
		receiptRoot:     ReceiptRoot,
		controlRoot:     ServiceControlRoot,
		executable:      os.Executable,
		verifySignature: VerifyDarwinSignature,
	})
}

func bootstrapFreshService(label, slot string, now time.Time, environment bootstrapEnvironment) (_ Bootstrap, returnErr error) {
	definition, err := ValidateMarker(label, slot)
	if err != nil {
		return Bootstrap{}, err
	}

	uid := environment.uid

	receiptRoot, err := environment.receiptRoot(uid)
	if err != nil {
		return Bootstrap{}, err
	}

	store := ReceiptStore{Root: receiptRoot, UID: uid}

	receipt, err := store.Load()
	if err != nil {
		return Bootstrap{}, err
	}

	intent, ok := receipt.Starts[definition.Label]
	if !ok {
		return Bootstrap{}, fmt.Errorf("no pending daemon service startup intent for %s", definition.Label)
	}

	bootstrapComplete := false
	defer func() {
		if bootstrapComplete {
			return
		}

		var cleanupErrs []error

		if controlRoot, rootErr := environment.controlRoot(uid); rootErr == nil {
			if removeErr := RemoveStartupRequest(controlRoot, uid, definition); removeErr != nil {
				cleanupErrs = append(cleanupErrs, removeErr)
			}
		} else {
			cleanupErrs = append(cleanupErrs, rootErr)
		}

		_, updateErr := store.Update(false, func(receipt *Receipt) error {
			if pending, exists := receipt.Starts[definition.Label]; exists && pending.Nonce == intent.Nonce {
				delete(receipt.Starts, definition.Label)
			}

			return nil
		})
		if updateErr != nil {
			cleanupErrs = append(cleanupErrs, updateErr)
		}

		if cleanupErr := errors.Join(cleanupErrs...); cleanupErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("clean failed daemon service bootstrap: %w", cleanupErr))
		}
	}()

	if intent.Label != definition.Label || intent.Slot != definition.Slot {
		return Bootstrap{}, errors.New("daemon service startup intent marker mismatch")
	}

	generation, ok := receipt.Generations[intent.Generation]
	if !ok {
		return Bootstrap{}, fmt.Errorf("daemon service startup generation %s is absent from receipt", intent.Generation)
	}

	teamID := generation.TeamID
	requirement := generation.Requirement

	expectedTeamID, expectedRequirement, err := stableSigningExpectation()
	if err != nil {
		return Bootstrap{}, err
	}

	if expectedTeamID != "" {
		if generation.TeamID != expectedTeamID || generation.Requirement != expectedRequirement {
			return Bootstrap{}, errors.New("daemon service receipt signing identity does not match this build")
		}

		teamID = expectedTeamID
		requirement = expectedRequirement
	}

	validated, err := ValidateBundle(generation.AppPath, BundleExpectations{
		Version: generation.Version, Commit: generation.Commit,
		TeamID: teamID, Requirement: requirement,
		AllowDevelopmentSig: DevelopmentBuild == "true", VerifySignature: environment.verifySignature,
	})
	if err != nil {
		return Bootstrap{}, fmt.Errorf("validate daemon service startup generation: %w", err)
	}

	if !generationMatchesReceipt(generation, validated.Generation) {
		return Bootstrap{}, errors.New("validated daemon service startup generation does not match its receipt")
	}

	currentExecutable, err := environment.executable()
	if err != nil {
		return Bootstrap{}, err
	}

	currentExecutable, err = filepath.EvalSymlinks(currentExecutable)
	if err != nil {
		return Bootstrap{}, err
	}

	wantExecutable, err := filepath.EvalSymlinks(filepath.Join(validated.AppPath, "Contents", "MacOS", DaemonExecutable))
	if err != nil {
		return Bootstrap{}, err
	}

	if currentExecutable != wantExecutable {
		return Bootstrap{}, errors.New("managed daemon service marker was invoked outside its validated cached app")
	}

	if definition.Slot == DefaultSlot {
		if receipt.Default == nil || receipt.Default.RegisteredGeneration != generation.ID || intent.Profile != "" {
			return Bootstrap{}, errors.New("default daemon service receipt/start agreement failed")
		}
	} else {
		lease, ok := receipt.Leases[intent.Profile]
		if !ok || lease.Slot != definition.Slot || lease.UID != uid || lease.RegisteredGeneration != generation.ID {
			return Bootstrap{}, errors.New("named daemon service lease/start agreement failed")
		}
	}

	controlRoot, err := environment.controlRoot(uid)
	if err != nil {
		return Bootstrap{}, err
	}

	request, err := ConsumeStartupRequest(controlRoot, uid, definition, ExpectedStartup{
		Profile: intent.Profile, Generation: intent.Generation, Nonce: intent.Nonce,
	}, now)
	if err != nil {
		_, _ = store.Update(false, func(receipt *Receipt) error { return CancelStart(receipt, definition.Label, intent.Nonce) })
		return Bootstrap{}, err
	}

	if err := InstallRequestEnvironment(request); err != nil {
		_, _ = store.Update(false, func(receipt *Receipt) error { return CancelStart(receipt, definition.Label, intent.Nonce) })
		return Bootstrap{}, err
	}

	if _, err := store.Update(false, func(receipt *Receipt) error {
		if err := CompleteStart(receipt, definition.Label, intent.Nonce); err != nil {
			return err
		}

		if definition.Slot == DefaultSlot {
			receipt.Default.RunningGeneration = generation.ID
			receipt.Default.RunningPID = os.Getpid()
			receipt.Default.Paths = request.Paths
		} else {
			lease := receipt.Leases[intent.Profile]
			lease.RunningGeneration = generation.ID
			lease.RunningPID = os.Getpid()
			lease.Paths = request.Paths
			receipt.Leases[intent.Profile] = lease
		}

		return nil
	}); err != nil {
		return Bootstrap{}, err
	}

	bootstrapComplete = true

	return Bootstrap{Definition: definition, Request: request, Generation: generation}, nil
}

func (bootstrap Bootstrap) ValidateResolvedConfig(configFile string, paths config.Paths) error {
	if configFile != bootstrap.Request.ConfigFile {
		return fmt.Errorf("daemon service config path %q does not match startup request %q", configFile, bootstrap.Request.ConfigFile)
	}

	if paths != bootstrap.Request.Paths {
		return errors.New("daemon service resolved config/data/runtime paths do not match startup request")
	}

	return nil
}

// Abort clears only the running marker installed by this fresh bootstrap. The
// request was already consumed, so later config/path/bootstrap failure must not
// leave the failed PID recorded as a live generation.
func (bootstrap Bootstrap) Abort() error {
	return clearRunningProcess(bootstrap.Request.Profile, os.Getpid(), bootstrap.Generation.ID)
}
