package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
)

// Delete stops a session, removes its worktree/branch, and deletes state.
// Git teardown is attempted before removing the session from state; if teardown
// fails the session is kept for retry and the error is returned.
func (sm *SessionManager) Delete(id string) error {
	return sm.deleteWithContext(context.Background(), id)
}

func (sm *SessionManager) deleteWithContext(ctx context.Context, id string) error {
	sm.mu.Lock()

	sessState, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("session %q not found", id)
	}

	// The orchestrator is declarative: deleting it is a request to discard the
	// current instance, and the presence reconciler will create a fresh one when
	// it remains enabled in config. Keep unknown/future system kinds fail-closed
	// until their ownership and recreation semantics are defined explicitly.
	if IsSystemSession(sessState) && sessState.SystemKind != SystemKindOrchestrator && sm.systemSessionEnabledInConfig(sessState) {
		sm.mu.Unlock()
		return fmt.Errorf("session %q is a system session managed by config.toml — disable it there and reload before deleting", sessState.Name)
	}

	if sessState.Starred {
		sm.mu.Unlock()
		return fmt.Errorf("session %q is starred; unstar it first to delete", id)
	}

	if sessState.Status == StatusDeleting {
		sm.mu.Unlock()
		return fmt.Errorf("session %q is already being deleted", id)
	}

	// Remove any staged migration context (full prior conversation) on delete.
	defer sm.removeMigrationContext(sessState)

	ac, hasClient := sm.attachedClients[id]
	if hasClient {
		delete(sm.attachedClients, id)
	}

	// Snapshot PTY session and remove from map under the lock so no concurrent
	// access is possible, then release the lock before blocking waits.
	ptySess, hasPTY := sm.sessions[id]
	if hasPTY {
		delete(sm.sessions, id)
	}

	name := sessState.Name
	repoPath := sessState.RepoPath
	worktreePath := sessState.WorktreePath
	branch := sessState.Branch
	shared := sessState.Mirror
	inPlace := sessState.InPlace
	agentName := sessState.Agent
	sessSystemKind := sessState.SystemKind
	prevStatus := sessState.Status
	sessToken := sessState.Token
	parentID := sessState.ParentID
	sessionIncludes := make([]IncludedRepoState, len(sessState.Includes))
	copy(sessionIncludes, sessState.Includes)

	if sessState.Status == StatusCreating {
		// Session is mid-creation (Phase 2). Remove from state so Phase 3 detects
		// the deletion and handles cleanup (worktree, PTY).
		sm.reparentChildrenLocked(id, parentID)
		delete(sm.state.Sessions, id)
		delete(sm.hookReports, id)

		for _, s := range sm.state.Sessions {
			if s.ParentID == id {
				s.ParentID = ""
			}
		}

		_ = sm.saveState()
		sm.mu.Unlock()

		if hasClient {
			ac.kick()
		}

		return nil
	}

	orphanPID := sessState.PID
	orphanStartTime := sessState.PIDStartTime
	sessState.Status = StatusDeleting
	sessState.StatusChangedAt = time.Now()
	// The PID stays in state until the tombstone (which carries it) is durably
	// written, so a crash before the tombstone lands still leaves a reap-able
	// PID rather than a silently-orphaned process.
	_ = sm.saveState()
	sm.mu.Unlock()

	// Write a tombstone before any teardown so a crash mid-delete is resumed on
	// next startup. This must be durable BEFORE the destructive teardown runs:
	// if it can't be written, fail closed — revert the session and abort rather
	// than tear down a worktree with no recovery marker.
	spec := teardownSpec{
		ID:           id,
		RepoPath:     repoPath,
		WorktreePath: worktreePath,
		Branch:       branch,
		Shared:       shared,
		InPlace:      inPlace,
		SystemKind:   sessSystemKind,
		Includes:     sessionIncludes,
	}
	if err := sm.writeTombstone(tombstone{
		teardownSpec: spec,
		Name:         name,
		PID:          orphanPID,
		PIDStartTime: orphanStartTime,
		CreatedAt:    time.Now(),
	}); err != nil {
		sm.log.Error("failed to write delete tombstone; aborting delete", "id", id, "err", err)
		sm.mu.Lock()

		var saveErr error

		if s, ok := sm.state.Sessions[id]; ok {
			s.Status = prevStatus
			s.StatusChangedAt = time.Now()

			// Restore manager ownership removed above: nothing has been killed or
			// torn down on this path, so the session must return fully intact
			// (still tracked, process still reachable) rather than a live agent
			// orphaned from sm.sessions and shown as stopped. The PID was never
			// cleared (that happens only after a successful tombstone), so the live
			// process remains recorded regardless of the save result below.
			if hasPTY {
				sm.sessions[id] = ptySess
			}

			if hasClient {
				sm.attachedClients[id] = ac
			}

			saveErr = sm.saveState()
		}
		sm.mu.Unlock()

		// writeTombstone can fail after the temp file was renamed into place (a
		// dir-fsync error), leaving a marker on disk. Durably remove it so a later
		// startup doesn't resume a delete we just aborted against a still-live
		// process. This is safe even if the revert save failed: the PID was never
		// cleared, so on-disk state still records the process for reap/retry.
		rmErr := sm.removeTombstone(id)

		retErr := fmt.Errorf("delete aborted: could not write recovery tombstone: %w", err)
		if saveErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("persist reverted state: %w", saveErr))
		}

		if rmErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove stray tombstone: %w", rmErr))
		}

		return retErr
	}

	// Tombstone is durable; the PID it carries lets resume reap the process, so
	// drop the PID from live state now.
	sm.mu.Lock()
	if s, ok := sm.state.Sessions[id]; ok {
		s.PID = 0
		s.PIDStartTime = 0
		_ = sm.saveState()
	}
	sm.mu.Unlock()

	// Blocking operations outside the lock.
	if hasPTY {
		ptySess.Detach()

		if !ptySess.Exited() {
			sm.logStopping(id, sm.sessionName(id), StopReasonDelete, "delete", ptySess)
		}

		if err := sm.teardownLiveDriver(ctx, ptySess); err != nil {
			// The process may still be alive: teardownLiveDriver leaves the launch
			// watcher owning the driver rather than Close-ing it when even SIGKILL
			// does not confirm exit. Committing the delete now would orphan a live
			// agent and tear down its worktree with no way to reach the process, so
			// abort and keep the session recoverable: restore the driver into
			// sm.sessions and its PID into state so a retry (or orphan reap) can
			// finish the job, drop the recovery tombstone (the delete did not
			// commit, and must not auto-resume against a live process), and return
			// the error (issue #1326).
			sm.log.Error("live driver did not terminate during delete; session kept for retry",
				"id", id, "err", err)
			sm.mu.Lock()

			var saveErr error

			if s, ok := sm.state.Sessions[id]; ok {
				s.Status = StatusErrored
				s.StatusChangedAt = time.Now()
				s.PID = orphanPID
				s.PIDStartTime = orphanStartTime
				applyLifecycleSummaryLocked(s, fmt.Sprintf("Delete aborted: session process did not terminate during teardown: %v", err))

				sm.sessions[id] = ptySess
				saveErr = sm.saveState()
			}
			sm.mu.Unlock()

			// Drop the recovery marker only once the restored PID is durable, and
			// only if the durable removal itself succeeds. If either fails, keep the
			// marker — it is the sole record of the still-live PID for the crash-reap
			// path — and surface the error (issue #1326).
			var rmErr error
			if saveErr == nil {
				rmErr = sm.removeTombstone(id)
			}

			if hasClient {
				ac.kick()
			}

			retErr := fmt.Errorf("delete aborted: session process did not terminate during teardown (kept for retry): %w", err)
			if saveErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("persist retry state: %w", saveErr))
			}

			if rmErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("remove tombstone: %w", rmErr))
			}

			return retErr
		}
	} else if orphanPID > 0 {
		sm.logStoppingPID(id, sm.sessionName(id), StopReasonDelete, "delete-orphan", orphanPID, orphanPID)

		if _, err := sm.killVerifiedProcess(orphanPID, orphanStartTime); err != nil {
			sm.log.Warn("failed to kill orphaned process during delete", "id", id, "pid", orphanPID, "err", err)
			sm.mu.Lock()

			var saveErr error

			if s, ok := sm.state.Sessions[id]; ok {
				s.Status = StatusErrored
				s.StatusChangedAt = time.Now()
				s.PID = orphanPID
				s.PIDStartTime = orphanStartTime
				applyLifecycleSummaryLocked(s, fmt.Sprintf("Delete aborted: orphaned process (PID %d) could not be killed: %v", orphanPID, err))

				saveErr = sm.saveState()
			}
			sm.mu.Unlock()

			// Keep the marker if the restored PID could not be persisted or the
			// durable removal itself failed: the process is alive and the marker is
			// the only durable record of it (issue #1326).
			var rmErr error
			if saveErr == nil {
				rmErr = sm.removeTombstone(id)
			}

			if hasClient {
				ac.kick()
			}

			retErr := fmt.Errorf("delete aborted: orphaned process (PID %d) could not be killed: %w", orphanPID, err)
			if saveErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("persist retry state: %w", saveErr))
			}

			if rmErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("remove tombstone: %w", rmErr))
			}

			return retErr
		}
	}

	// Attempt git teardown before removing the session from state.
	if teardownErr := sm.teardownArtifacts(spec); teardownErr != nil {
		sm.log.Error("git teardown failed, session kept for retry",
			"session_id", id, "err", teardownErr)
		sm.mu.Lock()

		var saveErr error

		if s, ok := sm.state.Sessions[id]; ok {
			if prevStatus == StatusRunning {
				s.Status = StatusStopped
			} else {
				s.Status = prevStatus
			}

			saveErr = sm.saveState()
		}
		sm.mu.Unlock()

		// Drop the tombstone only once the reverted retry-state is durable and the
		// durable removal succeeds; the process is already dead here, but keeping the
		// marker on a save or removal failure lets startup resume the interrupted
		// delete rather than strand it (issue #1326).
		var rmErr error
		if saveErr == nil {
			rmErr = sm.removeTombstone(id)
		}

		if hasClient {
			ac.kick()
		}

		retErr := fmt.Errorf("git teardown failed (session kept for retry): %w", teardownErr)
		if saveErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("persist retry state: %w", saveErr))
		}

		if rmErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove tombstone: %w", rmErr))
		}

		return retErr
	}

	sm.mu.Lock()
	// Re-read parentID under the lock: a concurrent Delete of our parent may
	// have updated sessState.ParentID while we were doing teardown without the
	// lock held. Using the stale captured value would reparent children to a
	// session that no longer exists.
	if s, ok := sm.state.Sessions[id]; ok {
		parentID = s.ParentID
	}

	sm.reparentChildrenLocked(id, parentID)
	delete(sm.state.Sessions, id)
	delete(sm.hookReports, id)
	delete(sm.silentWarned, id)
	delete(sm.headlessEscalated, id)

	if sessToken != "" {
		delete(sm.tokenIndex, sessToken)
	}

	for _, s := range sm.state.Sessions {
		if s.ParentID == id {
			s.ParentID = ""
		}
	}

	err := sm.saveState()
	sm.mu.Unlock()

	// The removal-from-state save is the durable commit point. Only drop the
	// tombstone once it succeeds; if it failed, keep the tombstone so startup
	// finishes the delete (state.json may still list this now-torn-down session).
	// Best-effort: the session is already gone from state, so a lingering marker
	// only re-runs a harmless resume of an already-completed delete.
	if err == nil {
		_ = sm.removeTombstone(id)
	}

	_ = os.Remove(filepath.Join(sm.paths.LogDir, id+".log"))
	_ = os.Remove(sm.nonoProfilePath(id))
	_ = os.Remove(sm.safehouseFragmentPath(id))
	sm.cleanupHooks(id, agentName, worktreePath)

	if hasClient {
		ac.kick()
	}

	// Reconcile only after the state removal has committed and its tombstone is
	// gone. A failed delete must never race a replacement against startup's
	// pending-delete recovery, which removes the same shared orchestrator tree.
	if err == nil && sessSystemKind == SystemKindOrchestrator {
		sm.notifyOrchestratorReconcile()
	}

	return err
}

// tombstoneBeforeBulkTeardown writes the recovery tombstone (carrying the PID)
// and only then clears the live PID from state, mirroring the single-delete
// ordering so a crash during bulk teardown always leaves either a reap-able PID
// in state (before the tombstone lands) or a tombstone that carries it (after) —
// never a forgotten live process, and never a torn-down worktree with no way to
// resume the delete (issue #1326). On write failure it leaves the process
// identity in state and returns an error so the caller keeps the session for
// retry without killing it. It must be called before teardownBulkDeleteDriver.
func (sm *SessionManager) tombstoneBeforeBulkTeardown(spec teardownSpec, name string, pid int, pidStartTime int64) error {
	if err := sm.writeTombstone(tombstone{
		teardownSpec: spec,
		Name:         name,
		PID:          pid,
		PIDStartTime: pidStartTime,
		CreatedAt:    time.Now(),
	}); err != nil {
		// writeTombstone can fail after the temp file was renamed into place (a
		// dir-fsync error), leaving a marker on disk. Durably remove it so a later
		// startup doesn't resume a delete this session is being kept out of (mirrors
		// the single-delete abort, issue #1326). Surface a removal failure too — the
		// caller must not report a cleanly restored session while a marker lingers.
		writeErr := fmt.Errorf("session %s: write tombstone: %w", spec.ID, err)
		if rmErr := sm.removeTombstone(spec.ID); rmErr != nil {
			writeErr = errors.Join(writeErr, fmt.Errorf("session %s: remove stray tombstone: %w", spec.ID, rmErr))
		}

		return writeErr
	}

	// The tombstone durably carries the PID now, so drop it from live state.
	sm.mu.Lock()
	if sess, ok := sm.state.Sessions[spec.ID]; ok {
		sess.PID = 0
		sess.PIDStartTime = 0
	}

	_ = sm.saveState()
	sm.mu.Unlock()

	return nil
}

// revertBulkDeleteForRetry restores a session whose recovery tombstone could not
// be written. Its process was never signalled, so it is returned to its EXACT
// prior state — status, driver ownership, and attached client, all removed at
// snapshot time — rather than being downgraded (a Running session must stay
// Running, still reachable). This mirrors the single-delete pre-teardown abort
// (issue #1326). Because the process is untouched, the caller must not kick the
// restored client.
func (sm *SessionManager) revertBulkDeleteForRetry(s bulkDeleteSnapshot) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sess, ok := sm.state.Sessions[s.id]
	if !ok {
		return nil
	}

	sess.Status = s.prevStatus
	sess.StatusChangedAt = time.Now()

	if s.ptySess != nil {
		sm.sessions[s.id] = s.ptySess
	}

	if s.client != nil {
		sm.attachedClients[s.id] = s.client
	}

	return sm.saveState()
}

// teardownBulkDeleteDriver stops one bulk-delete session's process — its live
// driver via the bounded escalation policy, or, for an orphan with only a PID,
// its verified process group. It returns which kind of failure (if any) kept the
// session for retry so the caller can record it; on live-driver failure it also
// restores the driver for retry. initiator labels the "stopping session" audit
// line so the initial pass and the late sweep stay distinguishable (issue #1326).
// teardownBulkDeleteDriver returns keepTombstone=true when the session is kept
// for retry but its restored retry-state could not be durably saved: the on-disk
// state may still show the cleared PID, so the recovery marker MUST be retained
// for the crash-reap path (issue #1326). err then carries both the teardown and
// the persistence failure.
func (sm *SessionManager) teardownBulkDeleteDriver(ctx context.Context, id, name string, driver SessionDriver, pid int, pidStartTime int64, initiator string) (liveFailed, killFailed, keepTombstone bool, err error) {
	if driver != nil {
		driver.Detach()

		if !driver.Exited() {
			sm.logStopping(id, name, StopReasonDelete, initiator, driver)
		}

		if e := sm.teardownLiveDriver(ctx, driver); e != nil {
			sm.log.Error("live driver did not terminate during bulk delete; session kept for retry",
				"id", id, "err", e)
			retryErr := fmt.Errorf("session %s: live driver did not terminate: %w", id, e)

			if saveErr := sm.restoreLiveDriverForRetry(id, driver, pid, pidStartTime, e); saveErr != nil {
				return true, false, true, errors.Join(retryErr, fmt.Errorf("session %s: persist retry state: %w", id, saveErr))
			}

			return true, false, false, retryErr
		}

		return false, false, false, nil
	}

	if pid > 0 {
		if retryErr, saveErr := sm.killBulkDeleteOrphan(id, name, pid, pidStartTime); retryErr != nil {
			if saveErr != nil {
				return false, true, true, errors.Join(retryErr, fmt.Errorf("session %s: persist retry state: %w", id, saveErr))
			}

			return false, true, false, retryErr
		}
	}

	return false, false, false, nil
}

// killBulkDeleteOrphan kills a bulk-delete snapshot's orphaned process group —
// a session recorded with a live PID but no live driver (e.g. spawned into the
// tree after a daemon crash) — and, on failure, keeps the session for retry:
// errored, with its PID/identity restored. It returns a non-nil retryErr when the
// kill failed so the caller keeps the session out of the teardown/removal set and
// surfaces the failure, mirroring the single-delete orphan branch, plus the
// saveState error so the caller retains the recovery marker if the retry state
// could not be persisted (issue #1326).
func (sm *SessionManager) killBulkDeleteOrphan(id, name string, pid int, pidStartTime int64) (retryErr, saveErr error) {
	sm.logStoppingPID(id, name, StopReasonDelete, "delete-children-orphan", pid, pid)

	_, err := sm.killVerifiedProcess(pid, pidStartTime)
	if err == nil {
		return nil, nil
	}

	sm.log.Warn("failed to kill orphaned process during delete", "id", id, "pid", pid, "err", err)
	sm.mu.Lock()

	if sess, ok := sm.state.Sessions[id]; ok {
		sess.Status = StatusErrored
		sess.StatusChangedAt = time.Now()
		sess.PID = pid
		sess.PIDStartTime = pidStartTime
		applyLifecycleSummaryLocked(sess, fmt.Sprintf("Delete aborted: orphaned process (PID %d) could not be killed: %v", pid, err))
	}

	saveErr = sm.saveState()
	sm.mu.Unlock()

	return fmt.Errorf("session %s: orphaned process (PID %d) could not be killed: %w", id, pid, err), saveErr
}

// restoreLiveDriverForRetry re-arms a session whose live driver did not confirm
// termination during a bulk delete so a later retry (or orphan reap) can finish
// the job. The driver is put back into sm.sessions and its PID restored into
// state, and the session is marked errored with an explanatory summary. It
// returns the saveState error: a non-nil result means the restored PID is not
// durable, so the caller MUST keep the recovery marker (issue #1326).
func (sm *SessionManager) restoreLiveDriverForRetry(id string, driver SessionDriver, pid int, pidStartTime int64, cause error) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sess, ok := sm.state.Sessions[id]
	if !ok {
		return nil
	}

	sess.Status = StatusErrored
	sess.StatusChangedAt = time.Now()
	sess.PID = pid
	sess.PIDStartTime = pidStartTime
	applyLifecycleSummaryLocked(sess, fmt.Sprintf("Delete aborted: session process did not terminate during teardown: %v", cause))

	sm.sessions[id] = driver

	return sm.saveState()
}

// reparentChildrenLocked reassigns all direct children of the deleted session
// to its parent. Must be called with sm.mu held.
func (sm *SessionManager) reparentChildrenLocked(deletedID, newParentID string) {
	for _, s := range sm.state.Sessions {
		if s.ParentID == deletedID {
			s.ParentID = newParentID
		}
	}
}

// DeleteWithChildren deletes a session and all its transitive descendants.
// Git teardown is attempted before removing each session from state; sessions
// whose teardown fails are kept for retry. Returns the list of deleted session
// IDs and an error if any teardowns failed.
func (sm *SessionManager) DeleteWithChildren(id string, excludeRoot bool) ([]string, error) {
	return sm.deleteWithChildrenContext(context.Background(), id, excludeRoot)
}

// bulkDeleteSnapshot captures the per-session data DeleteWithChildren needs to
// tear a session down outside the manager lock.
type bulkDeleteSnapshot struct {
	id           string
	name         string
	agent        string
	repoPath     string
	worktreePath string
	branch       string
	shared       bool
	inPlace      bool
	prevStatus   SessionStatus
	includes     []IncludedRepoState
	ptySess      SessionDriver
	client       *attachedClient
	pid          int
	pidStartTime int64
}

func (s bulkDeleteSnapshot) teardownSpec() teardownSpec {
	return teardownSpec{
		ID:           s.id,
		RepoPath:     s.repoPath,
		WorktreePath: s.worktreePath,
		Branch:       s.branch,
		Shared:       s.shared,
		InPlace:      s.inPlace,
		Includes:     s.includes,
	}
}

func (sm *SessionManager) deleteWithChildrenContext(ctx context.Context, id string, excludeRoot bool) ([]string, error) {
	sm.mu.Lock()

	sess, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q not found", id)
	}

	if !excludeRoot && sess.Starred {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q is starred; unstar it first to delete", id)
	}

	toDelete := sm.collectDescendants(id)
	if excludeRoot {
		toDelete = filterExcludeRoot(toDelete, id)
	}

	snaps := make([]bulkDeleteSnapshot, 0, len(toDelete))

	var creatingIDs []string

	for _, did := range toDelete {
		sess := sm.state.Sessions[did]
		if IsSystemSession(sess) {
			sm.log.Info("skipping system session in bulk delete", "session_id", did, "name", sess.Name)
			continue
		}

		if sess.Starred {
			sm.log.Info("skipping starred session in bulk delete", "session_id", did, "name", sess.Name)
			continue
		}

		if sess.Status == StatusDeleting {
			continue
		}

		if sess.Status == StatusCreating {
			// Mid-creation: remove from state so Phase 3 detects the deletion.
			delete(sm.state.Sessions, did)
			delete(sm.hookReports, did)

			if ac, ok := sm.attachedClients[did]; ok {
				delete(sm.attachedClients, did)
				creatingIDs = append(creatingIDs, did)
				_ = ac // kick after unlock
			} else {
				creatingIDs = append(creatingIDs, did)
			}

			continue
		}

		s := bulkDeleteSnapshot{
			id:           did,
			name:         sess.Name,
			agent:        sess.Agent,
			repoPath:     sess.RepoPath,
			worktreePath: sess.WorktreePath,
			branch:       sess.Branch,
			shared:       sess.Mirror,
			inPlace:      sess.InPlace,
			prevStatus:   sess.Status,
			includes:     make([]IncludedRepoState, len(sess.Includes)),
		}
		copy(s.includes, sess.Includes)

		if pty, ok := sm.sessions[did]; ok {
			s.ptySess = pty

			delete(sm.sessions, did)
		}

		if ac, ok := sm.attachedClients[did]; ok {
			s.client = ac

			delete(sm.attachedClients, did)
		}

		s.pid = sess.PID
		s.pidStartTime = sess.PIDStartTime
		snaps = append(snaps, s)
		sess.Status = StatusDeleting
		sess.StatusChangedAt = time.Now()
		// The PID stays in state until this session's recovery tombstone (which
		// carries it) is durably written below, so a crash before the tombstone
		// lands leaves a reap-able PID rather than a silently-orphaned process
		// (issue #1326). Mirrors the single-delete ordering.
	}

	_ = sm.saveState()
	sm.mu.Unlock()

	// killFailed / liveFailed track sessions whose process could not be confirmed
	// dead; like a failed teardown they must not have their artifacts/worktrees
	// removed — the process may still be alive — so they are kept for retry
	// (issue #1326). tombstoned tracks sessions with a durable recovery marker,
	// which are the only ones eligible for the destructive kill/teardown below.
	// retryErrs / teardownErrs collect every kept-for-retry cause so the bulk
	// operation reports them instead of silently dropping a session.
	killFailed := make(map[string]bool)
	liveFailed := make(map[string]bool)
	tombstoned := make(map[string]bool, len(snaps))

	var (
		retryErrs    []error
		teardownErrs []error
	)

	// Recovery-marker phase: write each session's tombstone (carrying its PID)
	// and clear the live PID only once it is durable, before any process is
	// signalled. A session whose marker can't be written is kept for retry with
	// its process untouched.
	for _, s := range snaps {
		if err := sm.tombstoneBeforeBulkTeardown(s.teardownSpec(), s.name, s.pid, s.pidStartTime); err != nil {
			sm.log.Error("failed to write delete tombstone; keeping session for retry", "id", s.id, "err", err)
			teardownErrs = append(teardownErrs, err)

			if saveErr := sm.revertBulkDeleteForRetry(s); saveErr != nil {
				teardownErrs = append(teardownErrs, fmt.Errorf("session %s: persist reverted state: %w", s.id, saveErr))
			}

			continue
		}

		tombstoned[s.id] = true
	}

	for _, s := range snaps {
		if !tombstoned[s.id] {
			continue
		}

		live, killed, keepTombstone, err := sm.teardownBulkDeleteDriver(ctx, s.id, s.name, s.ptySess, s.pid, s.pidStartTime, "delete-children")
		if live {
			liveFailed[s.id] = true
		}

		if killed {
			killFailed[s.id] = true
		}

		if err != nil {
			retryErrs = append(retryErrs, err)
			// Kept for retry: the process may still be alive and the restore path
			// re-armed its PID, so drop the recovery marker — but only if that retry
			// state was durably saved. If the save failed (keepTombstone), the marker
			// is the sole record of the still-live PID and must be retained. A durable
			// removal failure is itself surfaced, since a lingering marker would
			// resurrect the delete against the kept session (#1326).
			if !keepTombstone {
				if rmErr := sm.removeTombstone(s.id); rmErr != nil {
					retryErrs = append(retryErrs, fmt.Errorf("session %s: remove tombstone: %w", s.id, rmErr))
				}
			}
		}
	}

	// Sweep for sessions created between collectDescendants and PTY kills.
	// Child agents may have spawned new sessions during that window.
	deletedSet := make(map[string]bool, len(snaps)+len(creatingIDs))
	for _, s := range snaps {
		deletedSet[s.id] = true
	}

	for _, cid := range creatingIDs {
		deletedSet[cid] = true
	}

	const maxSweepRounds = 10
	for sweep := 0; sweep < maxSweepRounds; sweep++ {
		sm.mu.Lock()

		var lateSnaps []bulkDeleteSnapshot

		progress := false

		for sid, sess := range sm.state.Sessions {
			if deletedSet[sid] || sess.ParentID == "" || !deletedSet[sess.ParentID] {
				continue
			}
			// Add to traversal set before skip checks so descendants of
			// starred/system sessions are still reachable in later rounds.
			deletedSet[sid] = true

			if IsSystemSession(sess) || sess.Starred || sess.Status == StatusDeleting {
				continue
			}

			progress = true

			if sess.Status == StatusCreating {
				delete(sm.state.Sessions, sid)
				delete(sm.hookReports, sid)

				if ac, ok := sm.attachedClients[sid]; ok {
					delete(sm.attachedClients, sid)

					_ = ac
				}

				creatingIDs = append(creatingIDs, sid)

				continue
			}

			ls := bulkDeleteSnapshot{
				id:           sid,
				name:         sess.Name,
				agent:        sess.Agent,
				repoPath:     sess.RepoPath,
				worktreePath: sess.WorktreePath,
				branch:       sess.Branch,
				shared:       sess.Mirror,
				inPlace:      sess.InPlace,
				prevStatus:   sess.Status,
				includes:     make([]IncludedRepoState, len(sess.Includes)),
			}
			copy(ls.includes, sess.Includes)

			if pty, ok := sm.sessions[sid]; ok {
				ls.ptySess = pty

				delete(sm.sessions, sid)
			}

			if ac, ok := sm.attachedClients[sid]; ok {
				ls.client = ac

				delete(sm.attachedClients, sid)
			}

			ls.pid = sess.PID
			ls.pidStartTime = sess.PIDStartTime
			lateSnaps = append(lateSnaps, ls)
			sess.Status = StatusDeleting
			sess.StatusChangedAt = time.Now()
			// Keep the PID in state until this late session's tombstone is durable
			// (below), matching the initial pass's crash-safety (issue #1326).
		}

		if !progress {
			sm.mu.Unlock()
			break
		}

		sm.log.Info("sweep found late-arriving descendants", "count", len(lateSnaps), "round", sweep+1)
		_ = sm.saveState()
		sm.mu.Unlock()

		for _, s := range lateSnaps {
			// Tombstone-before-teardown for late descendants too, or a crash between
			// discovery and teardown would forget the process (issue #1326).
			if err := sm.tombstoneBeforeBulkTeardown(s.teardownSpec(), s.name, s.pid, s.pidStartTime); err != nil {
				sm.log.Error("failed to write delete tombstone for late descendant; keeping session for retry", "id", s.id, "err", err)
				teardownErrs = append(teardownErrs, err)

				if saveErr := sm.revertBulkDeleteForRetry(s); saveErr != nil {
					teardownErrs = append(teardownErrs, fmt.Errorf("session %s: persist reverted state: %w", s.id, saveErr))
				}

				continue
			}

			tombstoned[s.id] = true

			// A late descendant with a live PID but no live driver is killed by the
			// same orphan branch — otherwise its worktree would be torn down while
			// its process may still be alive (issue #1326).
			live, killed, keepTombstone, err := sm.teardownBulkDeleteDriver(ctx, s.id, s.name, s.ptySess, s.pid, s.pidStartTime, "delete-children-sweep")
			if live {
				liveFailed[s.id] = true
			}

			if killed {
				killFailed[s.id] = true
			}

			if err != nil {
				retryErrs = append(retryErrs, err)

				if !keepTombstone {
					if rmErr := sm.removeTombstone(s.id); rmErr != nil {
						retryErrs = append(retryErrs, fmt.Errorf("session %s: remove tombstone: %w", s.id, rmErr))
					}
				}
			}
		}

		snaps = append(snaps, lateSnaps...)

		if sweep == maxSweepRounds-1 {
			sm.log.Warn("sweep reached round cap, some descendants may remain", "cap", maxSweepRounds)
		}
	}

	// Artifact-teardown phase. Each surviving session already has a durable
	// tombstone (written before its process was signalled), so teardown only needs
	// to remove the worktree here. Sessions kept for retry — a failed tombstone
	// write, a live driver that would not die, or an un-killable orphan — are
	// skipped so their artifacts are never destroyed while the process may be alive
	// (issue #1326).
	succeeded := make(map[string]bool, len(snaps))
	teardownFailed := make(map[string]bool)

	for _, s := range snaps {
		if !tombstoned[s.id] || killFailed[s.id] || liveFailed[s.id] {
			continue
		}

		if err := sm.teardownArtifacts(s.teardownSpec()); err != nil {
			sm.log.Error("git teardown failed, session kept for retry",
				"session_id", s.id, "err", err)
			teardownErrs = append(teardownErrs, fmt.Errorf("session %s: %w", s.id, err))
			// Delete did not commit, but the session's retry state (reverted status)
			// is only saved below — defer dropping the tombstone until that save is
			// durable, so a crash in between still finds the marker (issue #1326).
			teardownFailed[s.id] = true
		} else {
			succeeded[s.id] = true
		}
	}

	// Remove successfully torn-down sessions; revert failed ones to their prior status.
	sm.mu.Lock()

	deletedIDs := append([]string{}, creatingIDs...)

	removedSet := make(map[string]bool, len(creatingIDs))
	for _, cid := range creatingIDs {
		removedSet[cid] = true
	}

	for _, s := range snaps {
		if succeeded[s.id] {
			if sess, ok := sm.state.Sessions[s.id]; ok && sess.Token != "" {
				delete(sm.tokenIndex, sess.Token)
			}

			delete(sm.state.Sessions, s.id)
			delete(sm.hookReports, s.id)
			deletedIDs = append(deletedIDs, s.id)
			removedSet[s.id] = true
		} else if !tombstoned[s.id] || killFailed[s.id] || liveFailed[s.id] {
			// Sessions kept for retry already hold their intended state: a failed
			// tombstone write was restored to its exact prior state (process
			// untouched), and the orphan-kill / live-driver failure paths set an
			// errored status with the PID re-armed. Leave all of them as-is rather
			// than reverting (issue #1326).
			continue
		} else if sess, ok := sm.state.Sessions[s.id]; ok {
			// Tombstoned and killed cleanly, but artifact teardown failed: the
			// process is dead, so revert to stopped/prior for a git-teardown retry.
			if s.prevStatus == StatusRunning {
				sess.Status = StatusStopped
			} else {
				sess.Status = s.prevStatus
			}
		}
	}

	for _, s := range sm.state.Sessions {
		if s.ParentID != "" && removedSet[s.ParentID] {
			s.ParentID = ""
		}
	}

	stateErr := sm.saveState()
	sm.mu.Unlock()

	for _, s := range snaps {
		if succeeded[s.id] {
			// Only drop the tombstone once the removal-from-state save is durable;
			// if it failed, keep the tombstone so startup finishes the delete.
			// Best-effort: the session is already gone from state, so a lingering
			// marker only re-runs a harmless resume of an already-completed delete.
			if stateErr == nil {
				_ = sm.removeTombstone(s.id)
			}

			_ = os.Remove(filepath.Join(sm.paths.LogDir, s.id+".log"))
			_ = os.Remove(sm.nonoProfilePath(s.id))
			_ = os.Remove(sm.safehouseFragmentPath(s.id))
			sm.cleanupHooks(s.id, s.agent, s.worktreePath)
		} else if teardownFailed[s.id] && stateErr == nil {
			// The reverted retry-state is now durable, so the (already carried-out)
			// git-teardown failure can safely drop its marker; a save or durable-
			// removal failure keeps it so startup resumes the delete (issue #1326).
			if rmErr := sm.removeTombstone(s.id); rmErr != nil {
				teardownErrs = append(teardownErrs, fmt.Errorf("session %s: remove tombstone: %w", s.id, rmErr))
			}
		}

		// Kick the client except for a tombstone-write failure, where the process
		// was never touched and revertBulkDeleteForRetry restored the client to
		// attachedClients — kicking would needlessly disconnect a live session.
		if s.client != nil && tombstoned[s.id] {
			s.client.kick()
		}
	}

	if stateErr != nil {
		return deletedIDs, stateErr
	}

	// Report git-teardown, live-driver-teardown, and orphan-kill failures: each
	// keeps its session for retry, and none may be swallowed (issue #1326).
	// Combining them means a bulk delete never returns a nil error while a session
	// it left behind still has a possibly-live process.
	keptErrs := append(teardownErrs, retryErrs...)
	if len(keptErrs) > 0 {
		return deletedIDs, fmt.Errorf("teardown failed for %d session(s) (kept for retry): %w",
			len(keptErrs), errors.Join(keptErrs...))
	}

	return deletedIDs, nil
}

// collectDescendants returns the target ID plus all transitive children, leaves first.
func (sm *SessionManager) collectDescendants(rootID string) []string {
	children := make(map[string][]string)

	for id, sess := range sm.state.Sessions {
		if sess.ParentID != "" {
			children[sess.ParentID] = append(children[sess.ParentID], id)
		}
	}

	var result []string

	seen := make(map[string]bool)

	var walk func(string)

	walk = func(id string) {
		if seen[id] {
			return
		}

		seen[id] = true
		for _, child := range children[id] {
			walk(child)
		}

		result = append(result, id)
	}
	walk(rootID)

	return result
}

// killProcessGroup sends SIGTERM to the process group led by pid, waits up to
// grace for the group to exit, then sends SIGKILL if still alive. A non-positive
// grace falls back to the built-in default (the daemon passes the [lifecycle]
// process_kill_grace policy). The escalation ORDER (SIGTERM → SIGKILL) is fixed.
func killProcessGroup(pid int, grace time.Duration) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}

	if grace <= 0 {
		grace = config.ProcessKillGraceDefault
	}

	pgid := -pid
	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}

		return fmt.Errorf("SIGTERM to pgid %d: %w", pgid, err)
	}

	deadline := time.After(grace)

	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			_ = syscall.Kill(pgid, syscall.SIGKILL)
			return nil
		case <-tick.C:
			if err := syscall.Kill(pgid, 0); err != nil {
				return nil
			}
		}
	}
}

func (sm *SessionManager) killVerifiedProcess(pid int, startTime int64) (killed bool, err error) {
	if pid <= 0 || !isProcessAlive(pid) {
		return false, nil
	}

	if startTime == 0 {
		return false, fmt.Errorf("no process identity recorded for PID %d", pid)
	}

	current, err := grpty.ProcessStartTime(pid)
	if err != nil {
		if !isProcessAlive(pid) {
			return false, nil
		}

		return false, fmt.Errorf("could not read process start time for PID %d: %w", pid, err)
	}

	if current != startTime {
		return false, fmt.Errorf("PID %d identity mismatch (recorded=%d, current=%d)", pid, startTime, current)
	}

	err = killProcessGroup(pid, sm.Config().Lifecycle.ProcessKillGraceDuration())

	return err == nil, err
}

type orphanCandidate struct {
	id           string
	pid          int
	pidStartTime int64
}

func (sm *SessionManager) cleanupOrphanedProcesses() {
	sm.mu.Lock()

	var candidates []orphanCandidate

	for id, sess := range sm.state.Sessions {
		// Running and Errored sessions with a recorded PID but no live driver are
		// unmanaged orphans to reap. Errored is included so a delete that failed to
		// kill its process (a kept-for-retry live-driver/orphan-kill failure that was
		// durably saved) still has that process reaped after a daemon crash, rather
		// than leaking a live agent with no driver (issue #1326). Stopped is
		// deliberately NOT a candidate: a normal stop clears the PID, and the
		// interrupted-delete case is routed through Running by State.Reconcile.
		if sess.Status != StatusRunning && sess.Status != StatusErrored {
			continue
		}

		if sess.PID <= 0 {
			continue
		}

		if !isProcessAlive(sess.PID) {
			continue
		}

		if _, hasPTY := sm.sessions[id]; hasPTY {
			continue
		}

		candidates = append(candidates, orphanCandidate{
			id: id, pid: sess.PID, pidStartTime: sess.PIDStartTime,
		})
	}
	sm.mu.Unlock()

	grace := sm.Config().Lifecycle.ProcessKillGraceDuration()

	for _, c := range candidates {
		verified := c.pidStartTime != 0
		if verified {
			current, err := grpty.ProcessStartTime(c.pid)
			if err != nil || current != c.pidStartTime {
				verified = false
			}
		}

		if verified {
			sm.log.Warn("killing orphaned process group",
				"id", c.id, "pid", c.pid)
			err := killProcessGroup(c.pid, grace)

			sm.mu.Lock()
			if sess := sm.state.Sessions[c.id]; sess != nil {
				if err != nil {
					sess.Status = StatusErrored
					sess.StatusChangedAt = time.Now()
					sess.StopReason = StopReasonCrash
					applyLifecycleSummaryLocked(sess,
						fmt.Sprintf("Orphaned process (PID %d) — kill failed: %v", c.pid, err))
				} else {
					sess.Status = StatusStopped
					sess.StatusChangedAt = time.Now()
					sess.PID = 0
					sess.PIDStartTime = 0
					sess.StopReason = StopReasonCrash
					applyLifecycleSummaryLocked(sess,
						"Orphaned by daemon crash — killed")
				}
			}
			sm.mu.Unlock()
		} else {
			sm.mu.Lock()
			if sess := sm.state.Sessions[c.id]; sess != nil {
				sm.log.Warn("cannot verify orphaned process identity",
					"id", c.id, "pid", c.pid,
					"recorded_start_time", c.pidStartTime)

				sess.Status = StatusErrored
				sess.StatusChangedAt = time.Now()
				sess.StopReason = StopReasonCrash
				applyLifecycleSummaryLocked(sess, fmt.Sprintf(
					"Orphaned process (PID %d) — identity unverified, manual cleanup needed",
					c.pid))
			}
			sm.mu.Unlock()
		}
	}

	if len(candidates) > 0 {
		sm.mu.Lock()
		_ = sm.saveState()
		sm.mu.Unlock()
	}
}

// peakRSSProcLabel names which process the peak_rss_mb reading belongs to
// (issue #1104). The value is the waited child's rusage — the sandbox wrapper
// when the session is sandboxed, otherwise the agent itself.
func peakRSSProcLabel(sandboxed bool) string {
	if sandboxed {
		return "sandbox-wrapper"
	}

	return "agent"
}

// logStopping records a daemon-initiated stop on the daemon log the instant
// before the SIGTERM is sent, so every kill is attributable from the log alone
// (issue #1104). reason is the StopReason category being applied; initiator
// names the code path that requested the stop (idle-loop, user-stop, delete,
// restart, shutdown, …). pid/pgid come from the live PTY (nil-safe) and enable
// OS-level signal forensics. Paired with the "session exited" line, this closes
// the "which subsystem killed this session, and when?" gap.
func (sm *SessionManager) logStopping(id, name, reason, initiator string, sess SessionDriver) {
	pid, pgid := 0, 0
	if sess != nil {
		pid = sess.ProcessPID()
		pgid = sess.Pgid()
	}

	sm.recordSignalRequest(id, pid, syscall.SIGTERM, initiator)

	sm.logStoppingPID(id, name, reason, initiator, pid, pgid)
}

// logStoppingPID is logStopping for the orphan-reap paths where there is no live
// PTY, only a recorded pid (killVerifiedProcess signals the process group via
// -pid, so pgid == pid). Keeping these on the same "stopping session" line means
// `grep "stopping session"` is a complete daemon-kill audit (issue #1104).
func (sm *SessionManager) logStoppingPID(id, name, reason, initiator string, pid, pgid int) {
	sm.log.Info("stopping session",
		"id", id, "name", name, "reason", reason, "initiator", initiator,
		"pid", pid, "pgid", pgid)
}
