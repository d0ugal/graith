package daemonservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/testprocess"
)

type ServiceReport struct {
	Profile              string        `json:"profile,omitempty"`
	Slot                 string        `json:"slot"`
	Label                string        `json:"label"`
	LeaseState           string        `json:"lease_state"`
	RegisteredGeneration string        `json:"registered_generation,omitempty"`
	RunningGeneration    string        `json:"running_generation,omitempty"`
	Status               ServiceStatus `json:"status"`
	JobPresent           bool          `json:"job_present"`
	JobRunning           bool          `json:"job_running"`
	PID                  int           `json:"pid,omitempty"`
	RecordedPID          int           `json:"recorded_pid,omitempty"`
	QuarantineReason     string        `json:"quarantine_reason,omitempty"`
	Paths                config.Paths  `json:"-"`
	Job                  JobState      `json:"-"`
}

func (manager *Manager) Reports(ctx context.Context, profile string, all bool) ([]ServiceReport, error) {
	receipt, err := manager.Store.Load()
	if err != nil {
		return nil, err
	}

	type selected struct {
		profile    string
		definition Definition
		registered string
		running    string
		runningPID int
		paths      config.Paths
		leaseState string
		quarantine string
	}

	var selections []selected

	if all || profile == "" {
		definition := Definitions()[0]

		item := selected{definition: definition, leaseState: "unregistered", quarantine: receipt.Quarantined[DefaultSlot]}
		if item.quarantine != "" {
			item.leaseState = "quarantined"
		}

		if receipt.Default != nil {
			item.registered = receipt.Default.RegisteredGeneration
			item.running = receipt.Default.RunningGeneration
			item.runningPID = receipt.Default.RunningPID

			item.paths = receipt.Default.Paths
			if item.quarantine == "" {
				item.leaseState = "registered"
			}
		}

		selections = append(selections, item)
	}

	for mappedProfile, lease := range receipt.Leases {
		if !all && mappedProfile != profile {
			continue
		}

		definition, definitionErr := DefinitionForSlot(lease.Slot)
		if definitionErr != nil {
			return nil, definitionErr
		}

		leaseState := "leased"
		if receipt.Quarantined[lease.Slot] != "" {
			leaseState = "quarantined"
		}

		selections = append(selections, selected{
			profile: mappedProfile, definition: definition,
			registered: lease.RegisteredGeneration, running: lease.RunningGeneration,
			runningPID: lease.RunningPID, paths: lease.Paths,
			leaseState: leaseState, quarantine: receipt.Quarantined[lease.Slot],
		})
	}

	if all {
		for slot, reason := range receipt.Quarantined {
			found := false

			for _, selection := range selections {
				if selection.definition.Slot == slot {
					found = true
				}
			}

			if !found {
				definition, definitionErr := DefinitionForSlot(slot)
				if definitionErr != nil {
					return nil, definitionErr
				}

				selections = append(selections, selected{definition: definition, leaseState: "quarantined", quarantine: reason})
			}
		}
	}

	if !all && profile != "" && len(selections) == 0 {
		return nil, fmt.Errorf("profile %q has no registered daemon service lease", profile)
	}

	reports := make([]ServiceReport, 0, len(selections))
	for _, selection := range selections {
		controllerPath := controllerExecutable(manager.Bundle.AppPath)
		if generation, ok := receipt.Generations[selection.registered]; ok {
			controllerPath = controllerExecutable(generation.AppPath)
		}

		status, statusErr := manager.Controller.Status(ctx, controllerPath, selection.definition)
		if statusErr != nil {
			return nil, statusErr
		}

		job, jobErr := manager.Controller.JobState(ctx, manager.UID, selection.definition)
		if jobErr != nil {
			return nil, jobErr
		}

		reports = append(reports, ServiceReport{
			Profile: selection.profile, Slot: selection.definition.Slot, Label: selection.definition.Label,
			LeaseState: selection.leaseState, RegisteredGeneration: selection.registered,
			RunningGeneration: selection.running, Status: status,
			JobPresent: job.Present, JobRunning: job.Running, PID: job.PID, RecordedPID: selection.runningPID,
			QuarantineReason: selection.quarantine,
			Paths:            selection.paths,
			Job:              job,
		})
	}

	sort.Slice(reports, func(i, j int) bool {
		if reports[i].Slot == DefaultSlot {
			return true
		}

		if reports[j].Slot == DefaultSlot {
			return false
		}

		return reports[i].Slot < reports[j].Slot
	})

	return reports, nil
}

func (manager *Manager) MarkStopped(profile string, pid int) error {
	if err := manager.guardLifecycleMutation("record stopped managed daemon service"); err != nil {
		return err
	}

	_, err := manager.Store.Update(false, func(receipt *Receipt) error {
		if profile == "" {
			if receipt.Default != nil && receipt.Default.RunningPID == pid {
				receipt.Default.RunningGeneration = ""
				receipt.Default.RunningPID = 0
			}

			return nil
		}

		lease, ok := receipt.Leases[profile]
		if !ok {
			return nil
		}

		if lease.RunningPID == pid {
			lease.RunningGeneration = ""
			lease.RunningPID = 0
			receipt.Leases[profile] = lease
		}

		return nil
	})

	return err
}

func clearRunningProcess(profile string, pid int, generation string) error {
	return clearRunningProcessWith(
		profile,
		pid,
		generation,
		os.Geteuid(),
		ReceiptRoot,
		testprocess.RefuseDaemonLifecycleMutation,
	)
}

func clearRunningProcessWith(
	profile string,
	pid int,
	generation string,
	uid int,
	receiptRoot func(int) (string, error),
	guard func(string) error,
) error {
	if err := guard("clear managed daemon service running generation"); err != nil {
		return err
	}

	root, err := receiptRoot(uid)
	if err != nil {
		return err
	}

	store := ReceiptStore{Root: root, UID: uid}

	_, err = store.Update(false, func(receipt *Receipt) error {
		if profile == "" {
			if receipt.Default != nil && receipt.Default.RunningPID == pid && (generation == "" || receipt.Default.RunningGeneration == generation) {
				receipt.Default.RunningPID = 0
				receipt.Default.RunningGeneration = ""
			}

			return nil
		}

		lease, ok := receipt.Leases[profile]
		if ok && lease.RunningPID == pid && (generation == "" || lease.RunningGeneration == generation) {
			lease.RunningPID = 0
			lease.RunningGeneration = ""
			receipt.Leases[profile] = lease
		}

		return nil
	})
	if errors.Is(err, ErrReceiptMissing) {
		return nil
	}

	return err
}

// MarkCurrentProcessStopped is a no-op outside a managed packaged build. A
// managed foreground daemon invokes it on every ordinary exit; same-PID exec
// never runs defers, so preserved upgrades keep their running marker intact.
func MarkCurrentProcessStopped(profile string) error {
	if !IsManagedBuild() {
		return nil
	}

	return clearRunningProcess(profile, os.Getpid(), "")
}

func generationReferences(receipt Receipt, references map[string]bool) {
	if receipt.Default != nil {
		references[receipt.Default.RegisteredGeneration] = true
		references[receipt.Default.RunningGeneration] = true
	}

	for _, lease := range receipt.Leases {
		references[lease.RegisteredGeneration] = true
		references[lease.RunningGeneration] = true
	}

	if receipt.Pending != nil {
		references[receipt.Pending.Generation] = true
	}

	for _, start := range receipt.Starts {
		references[start.Generation] = true
	}

	delete(references, "")
}

// GarbageCollectCache removes only validated immutable generations that are
// unreferenced by the current receipt, its backup, or this installed CLI. The
// receipt lock stays held across the decision and removal so another profile
// cannot acquire a generation between the reference check and deletion.
func (manager *Manager) GarbageCollectCache() ([]string, error) {
	if err := manager.guardLifecycleMutation("remove unused managed daemon service cache"); err != nil {
		return nil, err
	}

	if manager.SkipCacheGC {
		return nil, nil
	}

	var removed []string

	err := manager.Store.withLock(false, func() error {
		primary, err := manager.Store.loadLocked()
		if err != nil {
			return err
		}

		references := map[string]bool{manager.Bundle.Generation.ID: true}
		generationReferences(primary, references)

		if data, readErr := os.ReadFile(manager.Store.backupPath()); readErr == nil {
			backup, decodeErr := decodeReceipt(data)
			if decodeErr != nil {
				return fmt.Errorf("refuse cache collection with invalid backup receipt: %w", decodeErr)
			}

			generationReferences(backup, references)
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}

		cacheRoot := filepath.Dir(filepath.Dir(manager.Bundle.AppPath))

		bundles, err := validatedCachedBundles(cacheRoot, manager.UID, BundleExpectations{
			TeamID: manager.TeamID, Requirement: manager.Requirement,
			AllowDevelopmentSig: manager.Development, VerifySignature: manager.signatureVerifier(),
		})
		if err != nil {
			return err
		}

		var generationDirs []string

		for _, bundle := range bundles {
			if references[bundle.Generation.ID] {
				continue
			}

			generationDir := filepath.Dir(bundle.AppPath)
			if filepath.Dir(generationDir) != cacheRoot || filepath.Base(generationDir) != bundle.Generation.ID {
				return fmt.Errorf("refuse unsafe daemon service cache removal %s", generationDir)
			}

			delete(primary.Generations, bundle.Generation.ID)
			removed = append(removed, bundle.Generation.ID)
			generationDirs = append(generationDirs, generationDir)
		}

		if len(removed) > 0 {
			// Persist the unreference twice before deleting any bundle. The first
			// transaction makes the primary safe; the second rotates that safe
			// primary into the rollback receipt. A crash or save failure can then
			// retain an orphaned directory, but can never leave either valid receipt
			// pointing at a directory that was already removed.
			primary.Transaction++
			if err := manager.Store.saveLocked(primary); err != nil {
				return err
			}

			primary.Transaction++
			if err := manager.Store.saveLocked(primary); err != nil {
				return err
			}

			for _, generationDir := range generationDirs {
				if err := os.RemoveAll(generationDir); err != nil {
					return err
				}
			}
		}

		return nil
	})

	return removed, err
}

type StopDaemonFunc func(ServiceReport) error

func (manager *Manager) Remove(ctx context.Context, profile string, all bool, stop StopDaemonFunc) error {
	if err := manager.guardLifecycleMutation("remove managed daemon service"); err != nil {
		return err
	}

	reports, err := manager.Reports(ctx, profile, all)
	if err != nil {
		return err
	}

	for _, report := range reports {
		definition, definitionErr := DefinitionForSlot(report.Slot)
		if definitionErr != nil {
			return definitionErr
		}

		var removal *PendingOperation

		if report.RegisteredGeneration != "" {
			pending := PendingOperation{
				Kind: "remove", Profile: report.Profile, Slot: report.Slot, UID: manager.UID,
				Generation: report.RegisteredGeneration,
			}
			if err := manager.claimPending(&pending); err != nil {
				return err
			}

			if _, err := manager.Store.Update(false, func(receipt *Receipt) error {
				if receipt.Pending != nil {
					return fmt.Errorf("daemon service transaction pending for slot %s", receipt.Pending.Slot)
				}

				receipt.Pending = &pending

				return nil
			}); err != nil {
				return err
			}

			removal = &pending
		}

		if report.JobPresent && !manager.SkipCacheGC {
			receipt, loadErr := manager.Store.Load()
			if loadErr != nil {
				return loadErr
			}

			var generationID string
			if report.Slot == DefaultSlot && receipt.Default != nil {
				generationID = receipt.Default.RegisteredGeneration
			} else if lease, ok := receipt.Leases[report.Profile]; ok && lease.Slot == report.Slot {
				generationID = lease.RegisteredGeneration
			}

			if _, ok := receipt.Generations[generationID]; !ok {
				return fmt.Errorf("cannot remove service %s with no recorded registered generation", report.Label)
			}

			matches, validateErr := manager.matchingReceiptBundles(receipt, report.Job)
			if validateErr != nil {
				return validateErr
			}

			if len(matches) != 1 || matches[0].Generation.ID != generationID {
				return fmt.Errorf("refuse to remove service %s with unknown launchd bundle/program metadata", report.Label)
			}
		}

		if report.JobRunning || report.PID > 0 {
			if stop == nil || report.PID <= 1 && report.RecordedPID <= 1 {
				return fmt.Errorf("cannot safely stop running daemon service %s", report.Label)
			}

			if err := stop(report); err != nil {
				return err
			}

			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			for {
				state, stateErr := manager.Controller.JobState(waitCtx, manager.UID, definition)
				if stateErr != nil {
					cancel()
					return stateErr
				}

				if !state.Running && state.PID <= 0 {
					break
				}

				select {
				case <-waitCtx.Done():
					cancel()
					return waitCtx.Err()
				case <-time.After(50 * time.Millisecond):
				}
			}

			cancel()
		}

		receipt, loadErr := manager.Store.Load()
		if loadErr != nil {
			return loadErr
		}

		controllerPath := controllerExecutable(manager.Bundle.AppPath)
		if generation, ok := receipt.Generations[report.RegisteredGeneration]; ok {
			controllerPath = controllerExecutable(generation.AppPath)
		}

		status, unregisterErr := manager.Controller.Unregister(ctx, controllerPath, definition)
		if unregisterErr != nil {
			return unregisterErr
		}

		if status != StatusNotRegistered && status != StatusNotFound {
			return fmt.Errorf("unregister %s ended in status %q", report.Label, status)
		}

		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = waitForJobAbsent(waitCtx, manager.Controller, manager.UID, definition)

		cancel()

		if err != nil {
			return err
		}

		if removal != nil {
			if err := manager.completePendingRemoval(*removal); err != nil {
				return err
			}
		} else {
			_, err = manager.Store.Update(false, func(receipt *Receipt) error {
				delete(receipt.Starts, report.Label)

				if report.Slot == DefaultSlot {
					receipt.Default = nil
					delete(receipt.Quarantined, DefaultSlot)

					return nil
				}

				if report.Profile == "" && report.LeaseState == "quarantined" {
					delete(receipt.Quarantined, report.Slot)
					return nil
				}

				return ReleaseProfile(receipt, report.Profile, true)
			})
			if err != nil {
				return err
			}
		}
	}

	_, err = manager.GarbageCollectCache()

	return err
}

func (manager *Manager) Repair(ctx context.Context) ([]string, error) {
	if err := manager.guardLifecycleMutation("repair managed daemon service registration"); err != nil {
		return nil, err
	}

	receipt, loadErr := manager.Store.Load()

	var actions []string

	if loadErr == nil {
		cleared, clearErr := manager.clearExpiredStarts(manager.now())
		if clearErr != nil {
			return nil, clearErr
		}

		actions = append(actions, cleared...)
		receipt, loadErr = manager.Store.Load()
	}

	if loadErr == nil && receipt.Pending != nil {
		if err := manager.reconcilePending(ctx); err != nil {
			return nil, err
		}

		receipt, loadErr = manager.Store.Load()
	}

	if loadErr == nil && len(receipt.Quarantined) == 0 {
		if len(actions) > 0 {
			return actions, nil
		}

		return []string{"receipt valid; no repair needed"}, nil
	}

	totalLoss := loadErr != nil
	if totalLoss {
		receipt = NewReceipt()
		receipt.Generations[manager.Bundle.Generation.ID] = manager.Bundle.Generation
	}

	cacheRoot := filepath.Dir(filepath.Dir(manager.Bundle.AppPath))

	bundles, err := validatedCachedBundles(cacheRoot, manager.UID, BundleExpectations{
		TeamID: manager.TeamID, Requirement: manager.Requirement,
		AllowDevelopmentSig: manager.Development, VerifySignature: manager.signatureVerifier(),
	})
	if err != nil {
		return nil, err
	}

	if len(bundles) == 0 {
		return nil, errors.New("no validated cached Graith.app controller is available for repair")
	}

	for _, definition := range Definitions() {
		if !totalLoss {
			if _, quarantined := receipt.Quarantined[definition.Slot]; !quarantined {
				continue
			}
		}

		controllerPath := controllerExecutable(bundles[0].AppPath)

		status, err := manager.Controller.Status(ctx, controllerPath, definition)
		if err != nil {
			return nil, err
		}

		job, err := manager.Controller.JobState(ctx, manager.UID, definition)
		if err != nil {
			return nil, err
		}

		jobControllerPath := controllerPath

		if job.Present {
			var matches []ValidatedBundle

			for _, bundle := range bundles {
				if jobMatchesGeneration(job, bundle) {
					matches = append(matches, bundle)
				}
			}

			if len(matches) != 1 {
				reason := "launchd job does not identify one verified cached app generation"
				receipt.Quarantined[definition.Slot] = reason
				actions = append(actions, "quarantined "+definition.Label+": "+reason)

				continue
			}

			jobControllerPath = controllerExecutable(matches[0].AppPath)
		}

		if job.Running || status == StatusRequiresApproval || job.PID > 0 || status == StatusEnabled && !job.Present {
			reason := "unknown service is live or disabled/indeterminate"
			receipt.Quarantined[definition.Slot] = reason
			actions = append(actions, "quarantined "+definition.Label+": "+reason)

			continue
		}

		if status == StatusEnabled || job.Present {
			if _, err := manager.Controller.Unregister(ctx, jobControllerPath, definition); err != nil {
				return nil, err
			}

			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = waitForJobAbsent(waitCtx, manager.Controller, manager.UID, definition)

			cancel()

			if err != nil {
				return nil, err
			}

			actions = append(actions, "removed proven-down orphan "+definition.Label)
		}

		delete(receipt.Quarantined, definition.Slot)
	}

	if totalLoss {
		if errors.Is(loadErr, ErrReceiptMissing) {
			if _, err := manager.Store.Update(true, func(stored *Receipt) error {
				*stored = receipt
				return nil
			}); err != nil {
				return nil, err
			}
		} else if err := manager.Store.InitializeAfterTotalLoss(receipt); err != nil {
			return nil, err
		}
	} else if _, err := manager.Store.Update(false, func(stored *Receipt) error {
		stored.Quarantined = receipt.Quarantined
		return nil
	}); err != nil {
		return nil, err
	}

	if len(actions) == 0 {
		if totalLoss {
			actions = append(actions, "initialized an empty verified receipt")
		} else {
			actions = append(actions, "cleared proven-down quarantined services")
		}
	}

	return actions, nil
}

func (manager *Manager) clearExpiredStarts(now time.Time) ([]string, error) {
	receipt, err := manager.Store.Load()
	if err != nil {
		return nil, err
	}

	var actions []string

	for _, intent := range receipt.Starts {
		if now.Before(intent.ExpiresAt) {
			continue
		}

		definition, err := ValidateMarker(intent.Label, intent.Slot)
		if err != nil {
			return nil, err
		}

		if err := WithStartLock(manager.ControlRoot, manager.UID, definition, func() error {
			return manager.clearExpiredStart(definition, now)
		}); err != nil {
			return nil, err
		}

		actions = append(actions, "removed expired startup intent for "+intent.Label)
	}

	return actions, nil
}
