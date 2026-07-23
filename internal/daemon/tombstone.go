package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/d0ugal/graith/internal/atomicfile"
	"github.com/d0ugal/graith/internal/git"
)

// teardownSpec captures everything needed to tear down a session's on-disk
// artifacts (worktree, branch, scratch/orchestrator dirs). It is shared by the
// live delete paths and by tombstone resume so the teardown rules live in one
// place.
type teardownSpec struct {
	ID           string              `json:"id"`
	RepoPath     string              `json:"repo_path,omitempty"`
	WorktreePath string              `json:"worktree_path,omitempty"`
	Branch       string              `json:"branch,omitempty"`
	Shared       bool                `json:"shared,omitempty"`
	InPlace      bool                `json:"in_place,omitempty"`
	SystemKind   string              `json:"system_kind,omitempty"`
	Includes     []IncludedRepoState `json:"includes,omitempty"`
}

// tombstone is a durable marker written before a session's teardown begins and
// removed once teardown + state removal succeed. A leftover tombstone on daemon
// startup unambiguously means a delete was interrupted mid-flight (crash, kill,
// power loss), so it is safe to finish the deletion — the worktree is orphaned
// and the session was already committed to removal.
type tombstone struct {
	teardownSpec

	Name string `json:"name,omitempty"`
	// PID/PIDStartTime record the agent process that was being killed when the
	// delete started, so resume can reap a leftover orphan verified by identity.
	PID          int       `json:"pid,omitempty"`
	PIDStartTime int64     `json:"pid_start_time,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// teardownArtifacts removes a session's on-disk artifacts according to its
// kind. It is idempotent: git.TeardownSession and os.RemoveAll both tolerate
// already-removed targets, so a resumed teardown after a partial delete is
// safe.
func (sm *SessionManager) teardownArtifacts(t teardownSpec) error {
	switch {
	case t.SystemKind == SystemKindOrchestrator:
		// The orchestrator has no worktree/branch; its scratch + tmp live under
		// DataDir/orchestrator, which the per-session scratch cleanup doesn't
		// cover. Remove the whole tree.
		return os.RemoveAll(filepath.Join(sm.paths.DataDir, "orchestrator"))
	case t.Shared:
		return os.RemoveAll(filepath.Join(sm.paths.DataDir, "scratch", t.ID))
	case t.InPlace:
		// In-place sessions leave the repo untouched.
		return nil
	case t.RepoPath != "" && len(t.Includes) > 0:
		return sm.teardownIncludes(t.RepoPath, t.WorktreePath, t.Branch, t.Includes)
	case t.RepoPath != "":
		return git.TeardownSession(t.RepoPath, t.WorktreePath, t.Branch)
	case t.WorktreePath != "":
		return os.RemoveAll(t.WorktreePath)
	}

	return nil
}

// tombstoneDir returns the directory holding pending-delete tombstones.
func (sm *SessionManager) tombstoneDir() string {
	return filepath.Join(sm.paths.DataDir, "tombstones")
}

func (sm *SessionManager) tombstonePath(id string) string {
	return filepath.Join(sm.tombstoneDir(), id+".json")
}

// writeTombstone durably records a pending deletion before teardown begins.
// Callers treat a failure as fatal to the delete and fail closed (abort and
// keep the session) rather than tear down artifacts with no recovery marker, so
// the returned error must be checked.
func (sm *SessionManager) writeTombstone(t tombstone) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tombstone: %w", err)
	}

	if err := atomicfile.Write(sm.tombstonePath(t.ID), data, 0o600); err != nil {
		return err
	}

	// Test seam: simulate a post-rename dir-fsync failure — the marker is already
	// on disk but the write is reported as failed, so callers must fail closed and
	// clean up the landed marker (issue #1326).
	if sm.writeTombstoneFault != nil {
		return sm.writeTombstoneFault(t.ID)
	}

	return nil
}

// removeTombstone durably clears a tombstone once its session's teardown resolves
// (either completed or reverted for retry). The unlink is followed by a parent-
// directory fsync: without it a crash could resurrect a "removed" marker and, on
// an abort/retry path, resume a delete against a session whose state and driver
// were restored (issue #1326). A missing tombstone is success (idempotent). The
// error is returned so callers that rely on removal to establish a kept-for-retry
// or aborted state can propagate it rather than silently reporting a clean state.
func (sm *SessionManager) removeTombstone(id string) error {
	if err := os.Remove(sm.tombstonePath(id)); err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		sm.log.Warn("failed to remove tombstone", "id", id, "err", err)

		return err
	}

	if err := sm.fsyncTombstoneDir(); err != nil {
		sm.log.Warn("failed to fsync tombstone dir after removal", "id", id, "err", err)

		return err
	}

	return nil
}

// fsyncTombstoneDir makes a preceding unlink durable, honouring the test fault
// seam so the dir-fsync-failure branch can be exercised deterministically.
func (sm *SessionManager) fsyncTombstoneDir() error {
	if sm.tombstoneDirSyncFault != nil {
		return sm.tombstoneDirSyncFault()
	}

	return syncTombstoneDir(sm.tombstoneDir())
}

// syncTombstoneDir fsyncs the tombstone directory so an unlink is durable. A
// non-existent directory is treated as success (nothing to make durable).
func syncTombstoneDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return err
	}

	err = d.Sync()
	_ = d.Close()

	return err
}

// resumeTombstones finishes any deletions that were interrupted mid-flight. It
// runs once on daemon startup after LoadState/Reconcile: for each leftover
// tombstone it reaps a verified orphan process, tears down the worktree, drops
// the session from state, and removes the tombstone. Teardown errors are
// logged but not fatal — a leftover tombstone means the session was already
// committed to deletion, so a stubborn worktree is reported for manual cleanup
// (or the next `gr gc`) rather than resurrecting the session.
func (sm *SessionManager) resumeTombstones() {
	entries, err := os.ReadDir(sm.tombstoneDir())
	if err != nil {
		if !os.IsNotExist(err) {
			sm.log.Warn("failed to read tombstone dir", "err", err)
		}

		return
	}

	for pass := 0; pass < len(entries); pass++ {
		madeProgress := false

		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}

			path := filepath.Join(sm.tombstoneDir(), e.Name())

			data, err := os.ReadFile(path)
			if err != nil {
				sm.log.Warn("failed to read tombstone", "path", path, "err", err)
				continue
			}

			var t tombstone
			if err := json.Unmarshal(data, &t); err != nil {
				sm.log.Warn("corrupt tombstone, removing", "path", path, "err", err)
				_ = os.Remove(path)

				continue
			}

			sm.mu.RLock()
			stateSession := sm.state.Sessions[t.ID]
			removedHookCleanupPending := stateSession != nil && stateSession.RemovedHookCleanupPending
			processStillRecorded := stateSession == nil ||
				(stateSession.PID == t.PID && stateSession.PIDStartTime == t.PIDStartTime)

			sm.mu.RUnlock()

			// Removed-hook ownership is reconciled before tombstones during normal
			// cold startup. Keep this defense at the destructive boundary so a direct
			// or reordered call cannot signal or erase a still-pending generation.
			if removedHookCleanupPending {
				sm.log.Warn("deferring interrupted delete while removed-hook cleanup is pending",
					"id", t.ID, "pid", t.PID)

				continue
			}

			sm.log.Info("resuming interrupted delete", "id", t.ID, "name", t.Name)

			sm.mu.RLock()

			blockedByChild := false

			type missingChildTombstone struct {
				name         string
				spec         teardownSpec
				pid          int
				pidStartTime int64
			}

			var missing []missingChildTombstone

			for _, child := range sm.state.Sessions {
				if child.ParentID == t.ID && !hasTombstoneFile(sm.tombstoneDir(), child.ID) {
					blockedByChild = true

					if child.Status == StatusDeleting {
						missing = append(missing, missingChildTombstone{
							name: child.Name,
							spec: teardownSpec{
								ID:           child.ID,
								RepoPath:     child.RepoPath,
								WorktreePath: child.WorktreePath,
								Branch:       child.Branch,
								Shared:       child.Mirror,
								InPlace:      child.InPlace,
								SystemKind:   child.SystemKind,
								Includes:     child.Includes,
							},
							pid:          child.PID,
							pidStartTime: child.PIDStartTime,
						})
					}
				}
			}

			sm.mu.RUnlock()

			for _, child := range missing {
				if err := sm.tombstoneBeforeBulkTeardown(child.spec, child.name, child.pid, child.pidStartTime); err != nil {
					sm.log.Warn("failed to synthesize child delete tombstone during recovery", "name", child.name, "err", err)
				} else {
					sm.log.Info("synthesized missing child delete tombstone during recovery", "name", child.name)

					madeProgress = true
				}
			}

			if blockedByChild {
				sm.log.Warn("deferring delete tombstone with surviving child", "id", t.ID)
				continue
			}

			// Reap a leftover orphan process (verified by start time) before removing
			// the worktree it may still be running in.
			if t.PID > 0 && processStillRecorded {
				if _, err := sm.killVerifiedProcess(t.PID, t.PIDStartTime); err != nil {
					sm.log.Warn("could not reap orphan during delete-resume",
						"id", t.ID, "pid", t.PID, "err", err)
				}
			}

			if err := sm.teardownArtifacts(t.teardownSpec); err != nil {
				sm.log.Error("teardown failed during delete-resume (leaving for gr gc)",
					"id", t.ID, "err", err)
			}

			// Persist the state removal BEFORE unlinking the tombstone: the state
			// save is the durable commit point. If it fails, keep the tombstone so a
			// later startup retries — unlinking first could leave state.json still
			// listing a torn-down session with no marker to finish it.
			sm.mu.Lock()
			if sess, ok := sm.state.Sessions[t.ID]; ok {
				if sess.Token != "" {
					delete(sm.tokenIndex, sess.Token)
				}

				delete(sm.state.Sessions, t.ID)
				delete(sm.hookReports, t.ID)
			}

			saveErr := sm.saveState()
			sm.mu.Unlock()

			if saveErr != nil {
				sm.log.Error("failed to persist state during delete-resume; keeping tombstone",
					"id", t.ID, "err", saveErr)

				continue
			}

			_ = os.Remove(filepath.Join(sm.paths.LogDir, t.ID+".log"))
			_ = os.Remove(sm.nonoProfilePath(t.ID))
			_ = os.Remove(sm.safehouseFragmentPath(t.ID))
			_ = os.Remove(path)
			madeProgress = true
		}

		if !madeProgress {
			break
		}

		if refreshed, err := os.ReadDir(sm.tombstoneDir()); err == nil {
			entries = refreshed
		}
	}
}

func hasTombstoneFile(dir, id string) bool {
	_, err := os.Stat(filepath.Join(dir, id+".json"))
	return err == nil
}
