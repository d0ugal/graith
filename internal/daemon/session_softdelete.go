package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// shouldPurge reports whether a soft-deleted session's recovery window has
// elapsed and it is due for a hard delete. It is the single predicate shared by
// the purge loop and Restore's after-expiry check, so the two can never
// disagree about whether a session is still recoverable. `now` is injected so
// callers (and tests) control the clock.
//
// The deadline is normally the session's frozen ExpiresAt (set at delete time),
// NOT a recomputation from current retention — a config change must not
// retroactively shift the "Recoverable until <time>" the user was promised.
// When ExpiresAt is nil on a soft-deleted session (corrupt/hand-edited state, or
// an interrupted pre-ExpiresAt delete), fallbackExpiry is used so such a session
// is neither hidden-forever nor purged without a deadline (trash leak). Callers
// compute fallbackExpiry = DeletedAt + current retention (or now) and log the
// fallback.
func shouldPurge(s *SessionState, now, fallbackExpiry time.Time) bool {
	if s.DeletedAt == nil {
		return false
	}

	expiry := fallbackExpiry
	if s.ExpiresAt != nil {
		expiry = *s.ExpiresAt
	}

	return !now.Before(expiry)
}

// fallbackExpiryLocked computes the fallback purge deadline for a soft-deleted
// session whose ExpiresAt is missing, and reports whether the fallback applies
// (so callers can log it). Must be called with sm.mu held.
func (sm *SessionManager) fallbackExpiryLocked(s *SessionState, now time.Time) (time.Time, bool) {
	if s.ExpiresAt != nil {
		return *s.ExpiresAt, false
	}

	if s.DeletedAt != nil {
		return s.DeletedAt.Add(sm.cfg.Delete.RetentionDuration()), true
	}

	return now, true
}

// SoftDelete marks a session as deleted without removing its worktree or state.
// The agent process is stopped and the session moves to the stopped state, but
// everything is preserved so `gr restore` can recover it within the configured
// retention window. The daemon's purge loop hard-deletes it once the window
// elapses. System and starred sessions are protected, matching Delete. Returns
// a snapshot of the soft-deleted session so the caller can report the expiry.
func (sm *SessionManager) SoftDelete(id string) (SessionState, error) {
	return sm.softDelete(id, true)
}

// softDelete performs a single soft delete. Subtree operations already hold
// the ownership invariant at their preflight boundary and may disable the
// per-node child check while walking leaves-first.
func (sm *SessionManager) softDelete(id string, rejectChildren bool) (SessionState, error) {
	if err := sm.beginLifecycleOperation(); err != nil {
		return SessionState{}, err
	}
	defer sm.endLifecycleOperation()

	sm.mu.Lock()

	sessState, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q not found", id)
	}

	if rejectChildren && sm.subtreeDeleteOverlapsLocked(id) {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is undergoing subtree deletion", id)
	}

	if rejectChildren {
		if err := sm.rejectDeleteWithChildrenLocked(id); err != nil {
			sm.mu.Unlock()
			return SessionState{}, err
		}
	}

	if err := sm.rejectPendingUpgradeCleanupLocked(id); err != nil {
		sm.mu.Unlock()

		return SessionState{}, err
	}

	if IsSystemSession(sessState) && sm.systemSessionEnabledInConfig(sessState) {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is a system session managed by config.toml — disable it there and reload before deleting", sessState.Name)
	}

	if sessState.Starred {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is starred; unstar it first to delete", id)
	}

	if sessState.IsSoftDeleted() {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is already deleted; use `gr restore` to recover it or `gr purge` to remove it now", sessState.Name)
	}

	// Unlike Delete — which special-cases a mid-creation session by removing the
	// placeholder so the in-flight create's Phase 3 cleans up — soft-deleting a
	// half-created session is not meaningful, so we reject it outright.
	if sessState.Status == StatusCreating {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is still being created; wait for it to finish before deleting", sessState.Name)
	}

	if sessState.Status == StatusDeleting {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q is already being deleted", id)
	}

	orphanPID := sessState.PID
	orphanStartTime := sessState.PIDStartTime

	// Persist the marker BEFORE the blocking kill (crash-safety): if the daemon
	// died mid-kill with DeletedAt unwritten, Reconcile would find a dead PID and
	// mark the session a live stopped session, silently undoing the delete.
	// ExpiresAt is frozen here (DeletedAt + retention) so a later config change
	// never shifts the promised deadline. The PID is intentionally left recorded
	// through this save: if we crash before the kill below completes, the startup
	// sweep uses the recorded PID to re-kill the orphaned agent (Reconcile skips
	// stopped sessions). It is zeroed only after the kill succeeds.
	//
	// The save is done BEFORE removing the PTY/client from the runtime maps and
	// before killing, and its error is load-bearing: if it fails, the marker is
	// not durable, so we roll back the in-memory fields and abort rather than
	// kill the agent and report a delete that a crash could silently undo.
	prevStatus := sessState.Status
	now := time.Now()
	retention := sm.cfg.Delete.RetentionDuration()
	expiresAt := now.Add(retention)
	sessState.DeletedAt = &now
	sessState.ExpiresAt = &expiresAt
	sessState.Status = StatusStopped
	sessState.StatusChangedAt = now
	applyLifecycleSummaryLocked(sessState, softDeleteSummary(expiresAt))

	if err := sm.saveState(); err != nil {
		// Roll back: the session stays live and fully consistent (nothing has
		// been removed from the runtime maps or killed yet).
		sessState.DeletedAt = nil
		sessState.ExpiresAt = nil
		sessState.Status = prevStatus
		sm.mu.Unlock()

		return SessionState{}, fmt.Errorf("soft delete aborted: could not persist marker: %w", err)
	}

	// Marker is durable — now detach the client and remove the PTY from
	// sm.sessions BEFORE killing it: watchSession treats a session as stale when
	// it is no longer in the map, so the exit watcher won't race in and clobber
	// DeletedAt/Status when the agent exits. Mirrors Delete.
	ac, hasClient := sm.attachedClients[id]
	if hasClient {
		delete(sm.attachedClients, id)
	}

	ptySess, hasPTY := sm.sessions[id]
	if hasPTY {
		delete(sm.sessions, id)
	}

	sm.mu.Unlock()

	// Stop the agent outside the lock using Delete's kill path (detach → kill →
	// grace → force-kill → Close), NOT Stop's single SIGTERM. The marker is
	// already durable, so a best-effort kill is fine.
	killedOK := true

	if hasPTY {
		ptySess.Detach()

		if !ptySess.Exited() {
			sm.logStopping(id, sm.sessionName(id), StopReasonDelete, "soft-delete", ptySess)

			_ = ptySess.Kill()
			select {
			case <-ptySess.Done():
			case <-time.After(5 * time.Second):
				_ = ptySess.ForceKill()
			}
		}

		ptySess.Close()
	} else if orphanPID > 0 {
		sm.logStoppingPID(id, sm.sessionName(id), StopReasonDelete, "soft-delete-orphan", orphanPID, orphanPID)

		if _, err := sm.killVerifiedProcess(orphanPID, orphanStartTime); err != nil {
			sm.log.Warn("failed to kill process during soft delete", "id", id, "pid", orphanPID, "err", err)
			// Keep the PID recorded so the startup orphan sweep can retry the
			// kill; clearing it would strand a live, hidden agent unmanaged.
			killedOK = false
		}
	}

	// Snapshot the result. Clear the recorded PID only if the kill succeeded —
	// otherwise leave it for reconcileSoftDeletedOrphans to re-kill on restart.
	sm.mu.Lock()

	snapshot := SessionState{ID: id}
	if s, ok := sm.state.Sessions[id]; ok {
		if killedOK {
			s.PID = 0
			s.PIDStartTime = 0
		}

		_ = sm.saveState()
		snapshot = cloneSessionState(s)
	}

	sm.mu.Unlock()

	if hasClient {
		ac.kick()
	}

	return snapshot, nil
}

// softDeleteSummary builds the lifecycle summary shown in the overlay/logs for a
// soft-deleted session, including the frozen recovery deadline.
func softDeleteSummary(expiresAt time.Time) string {
	return "Soft-deleted, recoverable until " + expiresAt.Format("2006-01-02 15:04")
}

// softDeletableLocked reports whether a session is a candidate for soft delete
// in a bulk/sweep context. Must be called with sm.mu held (read or write).
func softDeletableLocked(sess *SessionState) bool {
	return sess != nil && !sess.IsSoftDeleted() && !sess.Starred && !IsSystemSession(sess) &&
		sess.Status != StatusCreating && sess.Status != StatusDeleting
}

// SoftDeleteWithChildren soft-deletes a session and all of its transitive
// descendants. If excludeRoot is true, the root session itself is left alone.
// Sessions that are already soft-deleted, starred, system, or mid-creation are
// skipped. A lightweight sweep re-marks descendants that appear mid-operation
// (a child agent spawning a new session) so the subtree stays coherent — it
// only re-marks, never tears down, since deferring teardown is the whole point.
// Returns the list of session IDs that were soft-deleted.
func (sm *SessionManager) SoftDeleteWithChildren(rootID string, excludeRoot bool) ([]string, error) {
	return sm.softDeleteWithChildren(rootID, excludeRoot, nil)
}

// softDeleteWithChildrenOwned applies an additional ownership predicate while
// reserving the subtree, so callers such as scenario cleanup cannot validate
// ownership and then race with a concurrent reparent before deletion starts.
func (sm *SessionManager) softDeleteWithChildrenOwned(rootID string, excludeRoot bool, owned func(string, *SessionState) bool) ([]string, error) {
	return sm.softDeleteWithChildren(rootID, excludeRoot, owned)
}

func (sm *SessionManager) softDeleteWithChildren(rootID string, excludeRoot bool, owned func(string, *SessionState) bool) ([]string, error) {
	if err := sm.beginLifecycleOperation(); err != nil {
		return nil, err
	}
	defer sm.endLifecycleOperation()

	sm.mu.RLock()

	if _, ok := sm.state.Sessions[rootID]; !ok {
		sm.mu.RUnlock()
		return nil, fmt.Errorf("session %q not found", rootID)
	}

	if sm.subtreeDeleteOverlapsLocked(rootID) {
		sm.mu.RUnlock()
		return nil, fmt.Errorf("session %q is undergoing subtree deletion", rootID)
	}

	if err := sm.rejectUnsafeDeleteDescendantsLocked(rootID, excludeRoot, false); err != nil {
		sm.mu.RUnlock()
		return nil, err
	}

	initial := sm.collectDescendants(rootID)
	if excludeRoot {
		initial = filterExcludeRoot(initial, rootID)
	}

	if err := sm.rejectPendingUpgradeCleanupForIDsLocked(initial); err != nil {
		sm.mu.RUnlock()

		return nil, err
	}

	sm.mu.RUnlock()

	// Reserve the root under the exclusive lock. The preflight above is kept
	// outside this short critical section because the actual soft-delete work
	// may tear down drivers; recheck overlap after acquiring the lock so two
	// concurrent subtree operations cannot both reserve overlapping roots.
	sm.mu.Lock()
	if sm.subtreeDeleteOverlapsLocked(rootID) {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q is undergoing subtree deletion", rootID)
	}

	if owned != nil {
		if root := sm.state.Sessions[rootID]; !owned(rootID, root) {
			sm.mu.Unlock()
			return nil, fmt.Errorf("session %q is no longer owned by the cleanup scope", rootID)
		}

		for _, descendantID := range sm.collectDescendants(rootID) {
			if !owned(descendantID, sm.state.Sessions[descendantID]) {
				sm.mu.Unlock()
				return nil, fmt.Errorf("session %q has a descendant outside the cleanup scope", rootID)
			}
		}
	}

	if sm.subtreeDeleteRoots == nil {
		sm.subtreeDeleteRoots = make(map[string]struct{})
	}

	sm.subtreeDeleteRoots[rootID] = struct{}{}
	sm.mu.Unlock()

	defer func() {
		sm.mu.Lock()
		delete(sm.subtreeDeleteRoots, rootID)
		sm.mu.Unlock()
	}()

	deletedSet := make(map[string]bool)

	var deleted []string

	var deleteErrors []error

	softDeleteOne := func(id string) {
		if deletedSet[id] {
			return
		}

		sm.mu.RLock()
		ok := softDeletableLocked(sm.state.Sessions[id])
		sm.mu.RUnlock()

		if !ok {
			// Mark it seen so descendants of a skipped (e.g. starred) session are
			// still reachable in the sweep below.
			deletedSet[id] = true
			return
		}

		if _, err := sm.softDelete(id, false); err != nil {
			sm.log.Warn("soft delete of descendant failed", "id", id, "err", err)
			deleteErrors = append(deleteErrors, fmt.Errorf("session %s: %w", id, err))

			return
		}

		deletedSet[id] = true
		deleted = append(deleted, id)
	}

	for _, id := range initial {
		softDeleteOne(id)
	}

	// Sweep for descendants created between collectDescendants and now, up to a
	// bounded number of rounds. Cheap: each round only re-marks. The bound is a
	// deliberate safety invariant, not a tunable: it caps a convergence loop over
	// a live session tree so a pathological or adversarial spawn rate can never
	// spin it unbounded. It is intentionally NOT exposed as config (see the
	// #1230 epic's "small defensive bounds" exclusion).
	const maxSweepRounds = 10
	for sweep := 0; sweep < maxSweepRounds; sweep++ {
		sm.mu.RLock()

		var late []string

		for sid, sess := range sm.state.Sessions {
			if deletedSet[sid] || sess.ParentID == "" || !deletedSet[sess.ParentID] {
				continue
			}

			late = append(late, sid)
		}

		sm.mu.RUnlock()

		if len(late) == 0 {
			break
		}

		for _, id := range late {
			softDeleteOne(id)
		}
	}

	if len(deleteErrors) > 0 {
		return deleted, fmt.Errorf("soft delete subtree incomplete: %w", errors.Join(deleteErrors...))
	}

	return deleted, nil
}

// Restore un-deletes a soft-deleted session, clearing its deletion marker and
// leaving it in the stopped state so it can be resumed. Returns an error if the
// session does not exist, is not soft-deleted, or its recovery window has
// already elapsed (in which case it is scheduled for purge and must not be
// resurrected past its advertised deadline).
func (sm *SessionManager) Restore(id string) (SessionState, error) {
	if err := sm.beginLifecycleOperation(); err != nil {
		return SessionState{}, err
	}
	defer sm.endLifecycleOperation()

	sm.mu.Lock()

	original, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q not found", id)
	}

	if err := sm.rejectPendingUpgradeCleanupLocked(id); err != nil {
		sm.mu.Unlock()

		return SessionState{}, err
	}

	before := cloneSessionState(original)
	restored, err := sm.restoreLocked(id)
	sm.mu.Unlock()

	if err != nil {
		return SessionState{}, err
	}

	if err := sm.persistLatestUpgradeState(); err != nil {
		sm.rollbackRestoredStates(map[string]SessionState{id: before})
		return SessionState{}, err
	}

	return restored, nil
}

// restoreLocked performs the restore under an already-held write lock.
func (sm *SessionManager) restoreLocked(id string) (SessionState, error) {
	sessState, ok := sm.state.Sessions[id]
	if !ok {
		return SessionState{}, fmt.Errorf("session %q not found", id)
	}

	if err := sm.rejectPendingUpgradeCleanupLocked(id); err != nil {
		return SessionState{}, err
	}

	if !sessState.IsSoftDeleted() {
		return SessionState{}, fmt.Errorf("session %q is not deleted", sessState.Name)
	}

	if sm.subtreeDeleteOverlapsLocked(id) {
		return SessionState{}, fmt.Errorf("cannot restore session %q while its ownership subtree is being deleted; wait for deletion to finish", sessState.Name)
	}

	// Use the same predicate purge uses: never resurrect a session past its
	// advertised deadline, even if the coarse purge cadence hasn't reaped it yet.
	now := time.Now()

	fallback, fellBack := sm.fallbackExpiryLocked(sessState, now)
	if fellBack {
		sm.log.Warn("soft-deleted session missing ExpiresAt; using fallback deadline for restore check", "id", id)
	}

	if shouldPurge(sessState, now, fallback) {
		return SessionState{}, fmt.Errorf("session %q has expired its recovery window and is scheduled for purge", sessState.Name)
	}

	if sessState.ParentID != "" {
		if parent, ok := sm.state.Sessions[sessState.ParentID]; ok &&
			(parent.IsSoftDeleted() || parent.Status == StatusDeleting) {
			return SessionState{}, fmt.Errorf("cannot restore session %q while parent %q is hidden or being deleted; restore the parent first or use `gr restore %s --children`", sessState.Name, parent.Name, parent.ID)
		}
	}

	sessState.DeletedAt = nil
	sessState.ExpiresAt = nil
	sessState.Status = StatusStopped
	sessState.StatusChangedAt = time.Now()
	applyLifecycleSummaryLocked(sessState, "Restored — resume to continue")

	return cloneSessionState(sessState), nil
}

// RestoreWithChildren restores a soft-deleted session and every soft-deleted
// descendant, bringing a subtree hidden by a `--children` delete back at once.
// Non-deleted or expired descendants are skipped. Returns the restored IDs.
func (sm *SessionManager) RestoreWithChildren(rootID string) ([]SessionState, error) {
	if err := sm.beginLifecycleOperation(); err != nil {
		return nil, err
	}
	defer sm.endLifecycleOperation()

	sm.mu.Lock()

	if _, ok := sm.state.Sessions[rootID]; !ok {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q not found", rootID)
	}

	ids := sm.collectDescendants(rootID)
	if err := sm.rejectPendingUpgradeCleanupForIDsLocked(ids); err != nil {
		sm.mu.Unlock()

		return nil, err
	}

	var restored []SessionState

	originals := make(map[string]SessionState)

	for i := len(ids) - 1; i >= 0; i-- {
		id := ids[i]

		sess, ok := sm.state.Sessions[id]
		if !ok || !sess.IsSoftDeleted() {
			continue
		}

		originals[id] = cloneSessionState(sess)

		s, err := sm.restoreLocked(id)
		if err != nil {
			delete(originals, id)
			sm.log.Warn("restore of descendant failed", "id", id, "err", err)

			continue
		}

		restored = append(restored, s)
	}

	if len(restored) == 0 {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q is not deleted", rootID)
	}
	sm.mu.Unlock()

	if err := sm.persistLatestUpgradeState(); err != nil {
		sm.rollbackRestoredStates(originals)
		return nil, err
	}

	return restored, nil
}

func (sm *SessionManager) rollbackRestoredStates(originals map[string]SessionState) {
	sm.mu.Lock()
	for id, original := range originals {
		current := sm.state.Sessions[id]
		if current == nil || current.DeletedAt != nil || current.ExpiresAt != nil || current.Status != StatusStopped {
			continue
		}

		*current = original
	}
	sm.mu.Unlock()

	if err := sm.persistLatestUpgradeState(); err != nil {
		sm.log.Error("failed to persist restore rollback", "err", err)
	}
}

// softDeletedDescendantCount returns how many transitive descendants of id are
// currently soft-deleted (excluding id itself). Used to warn on a bare restore
// that leaves hidden children behind.
func (sm *SessionManager) softDeletedDescendantCount(id string) int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	n := 0

	for _, did := range sm.collectDescendants(id) {
		if did == id {
			continue
		}

		if sess, ok := sm.state.Sessions[did]; ok && sess.IsSoftDeleted() {
			n++
		}
	}

	return n
}

// sessionName returns a session's name, or "" if it no longer exists. Used to
// capture a name before a hard delete removes the session from state.
func (sm *SessionManager) sessionName(id string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if s, ok := sm.state.Sessions[id]; ok {
		return s.Name
	}

	return ""
}

// sessionSnapshot returns a clone of a session's state, or a zero value with the
// ID set if it no longer exists.
func (sm *SessionManager) sessionSnapshot(id string) SessionState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if s, ok := sm.state.Sessions[id]; ok {
		return cloneSessionState(s)
	}

	return SessionState{ID: id}
}

// purgeExpired hard-deletes every soft-deleted session whose frozen ExpiresAt
// has passed. It snapshots the expired sessions under a read lock, then
// hard-deletes each via a compare-and-delete: it re-checks under a read lock
// that the session is still soft-deleted with the *same* ExpiresAt and still
// expired before calling Delete.
//
// Why this is race-free against a concurrent gr restore, even though the
// re-check and Delete are not one atomic critical section: purge only ever
// targets *expired* sessions, and Restore refuses to un-delete an expired
// session using the *same* shouldPurge predicate (see restoreLocked). So a
// session that qualifies for purge cannot be flipped back to live in the window
// between the re-check and Delete — the only operation that could clear the
// marker (Restore) is itself gated on the session NOT being expired. The
// compare-and-delete then additionally guards the delete/restore/re-delete case
// (a new ExpiresAt won't Equal the snapshot). This invariant is load-bearing:
// any future change that lets Restore succeed on an expired session must also
// make this delete atomic.
type expiredPurgeCandidate struct {
	id        string
	expiresAt time.Time
	depth     int
	cycleRoot string
}

func sortExpiredPurgeCandidates(candidates []expiredPurgeCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].depth != candidates[j].depth {
			return candidates[i].depth > candidates[j].depth
		}

		if candidates[i].cycleRoot != candidates[j].cycleRoot {
			return candidates[i].cycleRoot < candidates[j].cycleRoot
		}

		if !candidates[i].expiresAt.Equal(candidates[j].expiresAt) {
			return candidates[i].expiresAt.Before(candidates[j].expiresAt)
		}

		return candidates[i].id < candidates[j].id
	})
}

func (sm *SessionManager) purgeExpired(now time.Time) {
	sm.mu.RLock()

	var expired []expiredPurgeCandidate

	for id, s := range sm.state.Sessions {
		if s.DeletedAt == nil {
			continue
		}

		expiry, fellBack := sm.fallbackExpiryLocked(s, now)
		if fellBack {
			sm.log.Warn("soft-deleted session missing ExpiresAt; using fallback deadline for purge", "id", id)
		}

		if shouldPurge(s, now, expiry) {
			depth := 0
			cycleRoot := sm.expiredOwnershipCycleRootLocked(id)

			seenParents := map[string]struct{}{id: {}}
			for parentID := s.ParentID; parentID != ""; {
				if _, seen := seenParents[parentID]; seen {
					break
				}

				seenParents[parentID] = struct{}{}

				parent, ok := sm.state.Sessions[parentID]
				if !ok {
					break
				}

				depth++
				parentID = parent.ParentID
			}

			expired = append(expired, expiredPurgeCandidate{id: id, expiresAt: expiry, depth: depth, cycleRoot: cycleRoot})
		}
	}

	sm.mu.RUnlock()
	sortExpiredPurgeCandidates(expired)

	processedCycles := make(map[string]bool)

	for _, c := range expired {
		if c.cycleRoot != "" {
			if processedCycles[c.cycleRoot] {
				continue
			}

			processedCycles[c.cycleRoot] = true

			if !sm.expiredCycleSubtreeEligible(c.cycleRoot, now) {
				sm.log.Warn("skipping expired ownership cycle with ineligible member", "root", c.cycleRoot)
				continue
			}

			sm.log.Info("purging expired ownership cycle", "root", c.cycleRoot)

			if _, err := sm.DeleteWithChildren(c.cycleRoot, false); err != nil {
				sm.log.Warn("purge of expired ownership cycle failed, will retry", "root", c.cycleRoot, "err", err)
			}

			continue
		}

		// Compare-and-delete: verify the session is still soft-deleted and its
		// deadline is unchanged before purging, so a concurrent restore (or
		// delete/restore/re-delete, which mints a new ExpiresAt) is not clobbered.
		sm.mu.RLock()
		s, ok := sm.state.Sessions[c.id]

		var stillExpired bool

		if ok && s.DeletedAt != nil {
			expiry, _ := sm.fallbackExpiryLocked(s, now)
			stillExpired = expiry.Equal(c.expiresAt) && shouldPurge(s, now, expiry)
		}

		sm.mu.RUnlock()

		if !stillExpired {
			continue
		}

		sm.log.Info("purging expired soft-deleted session", "id", c.id)

		if err := sm.Delete(c.id); err != nil {
			sm.log.Warn("purge of expired session failed, will retry", "id", c.id, "err", err)
		}
	}
}

// expiredOwnershipCycleRootLocked returns the lexicographically smallest ID in
// a parent cycle containing id, or empty when the ancestor chain is acyclic.
// The caller holds sm.mu.RLock or sm.mu.Lock.
func (sm *SessionManager) expiredOwnershipCycleRootLocked(id string) string {
	seen := make(map[string]int)
	chain := make([]string, 0)

	for current := id; current != ""; {
		if start, ok := seen[current]; ok {
			root := chain[start]
			for _, member := range chain[start:] {
				if member < root {
					root = member
				}
			}

			return root
		}

		seen[current] = len(chain)
		chain = append(chain, current)

		sess, ok := sm.state.Sessions[current]
		if !ok {
			return ""
		}

		current = sess.ParentID
	}

	return ""
}

func (sm *SessionManager) expiredCycleSubtreeEligible(rootID string, now time.Time) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, id := range sm.collectDescendants(rootID) {
		sess, ok := sm.state.Sessions[id]
		if !ok || sess.Status == StatusCreating || sess.Status == StatusDeleting || sess.Starred || IsSystemSession(sess) || !sess.IsSoftDeleted() {
			return false
		}

		expiry, _ := sm.fallbackExpiryLocked(sess, now)
		if !shouldPurge(sess, now, expiry) {
			return false
		}
	}

	return true
}

// reconcileSoftDeletedOrphans kills any agent process still alive on a
// soft-deleted session and clears its recorded PID. It closes the crash window
// in SoftDelete between persisting the marker (with the PID still recorded) and
// completing the kill: Reconcile only re-checks liveness for running sessions,
// so a soft-deleted (stopped) session with a live PID would otherwise leave an
// orphaned, invisible agent. Run once at startup, before the first purge sweep.
func (sm *SessionManager) reconcileSoftDeletedOrphans() {
	sm.mu.RLock()

	type orphan struct {
		id        string
		pid       int
		startTime int64
	}

	var orphans []orphan

	for id, s := range sm.state.Sessions {
		if s.IsSoftDeleted() && s.PID > 0 && !s.RemovedHookCleanupPending {
			orphans = append(orphans, orphan{id: id, pid: s.PID, startTime: s.PIDStartTime})
		}
	}

	sm.mu.RUnlock()

	for _, o := range orphans {
		sm.log.Info("re-killing orphaned process on soft-deleted session", "id", o.id, "pid", o.pid)

		if _, err := sm.killVerifiedProcess(o.pid, o.startTime); err != nil {
			// Leave the PID recorded so a later run can retry; clearing it would
			// strand a live orphan with no handle to kill it.
			sm.log.Warn("failed to re-kill orphan on soft-deleted session", "id", o.id, "pid", o.pid, "err", err)
			continue
		}

		sm.mu.Lock()
		// Generation check: only clear if the session is still soft-deleted with
		// the same PID we killed. A concurrent restore+resume could have replaced
		// the process; we must not zero the new generation's PID.
		if s, ok := sm.state.Sessions[o.id]; ok && s.IsSoftDeleted() && s.PID == o.pid {
			s.PID = 0
			s.PIDStartTime = 0
			_ = sm.saveState()
		}
		sm.mu.Unlock()
	}
}

// purgeStartupDelay and purgeInterval read the current [delete] cadence under
// the session-manager lock. They are re-read on every tick (not captured once)
// so a `gr reload` that changes the timing takes effect on the running loop's
// next Reset — matching RunGitPullLoop's hot-reload behaviour.
func (sm *SessionManager) purgeStartupDelay() time.Duration {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return sm.cfg.Delete.PurgeStartupDelayDuration()
}

func (sm *SessionManager) purgeInterval() time.Duration {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return sm.cfg.Delete.PurgeIntervalDuration()
}

// RunPurgeLoop periodically hard-deletes soft-deleted sessions whose retention
// window has elapsed. Modeled on RunGitPullLoop: one sweep shortly after startup
// (to catch windows that elapsed while the daemon was down), then a coarse
// ticker whose interval is re-read from config each tick. Stops cleanly on
// context cancel.
func (sm *SessionManager) RunPurgeLoop(ctx context.Context) {
	runPurgeLoop(ctx, sm.loopTimer, sm.reconcileSoftDeletedOrphans, sm.purgeExpired, time.Now,
		sm.purgeStartupDelay, sm.purgeInterval, sm.recordPurgeSweep)
}

func runPurgeLoop(
	ctx context.Context,
	newTimer func(time.Duration) loopTimer,
	reconcile func(),
	purge func(time.Time),
	now func() time.Time,
	startupDelay func() time.Duration,
	interval func() time.Duration,
	recordSweep func(ranAt time.Time, nextInterval time.Duration),
) {
	// Close the SoftDelete crash window first: re-kill any agent left alive on a
	// soft-deleted session before the state is otherwise trusted.
	reconcile()

	timer := newTimer(startupDelay())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C():
			ranAt := now()
			purge(ranAt)

			// Re-read the interval so a hot config reload takes effect here.
			next := interval()
			recordSweep(ranAt, next)
			timer.Reset(next)
		}
	}
}

// recordPurgeSweep records when the last purge sweep ran and when the next is
// due, for surfacing in `gr doctor` diagnostics. nextInterval is the delay
// until the next sweep from ranAt.
func (sm *SessionManager) recordPurgeSweep(ranAt time.Time, nextInterval time.Duration) {
	sm.purgeStatsMu.Lock()
	defer sm.purgeStatsMu.Unlock()

	sm.lastPurgeSweep = ranAt
	sm.nextPurgeSweep = ranAt.Add(nextInterval)
}

// purgeSweepStats returns the last and next purge-sweep times. Zero values mean
// no sweep has run yet (the daemon is still within its startup delay).
func (sm *SessionManager) purgeSweepStats() (last, next time.Time) {
	sm.purgeStatsMu.Lock()
	defer sm.purgeStatsMu.Unlock()

	return sm.lastPurgeSweep, sm.nextPurgeSweep
}
