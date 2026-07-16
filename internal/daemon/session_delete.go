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
		if s, ok := sm.state.Sessions[id]; ok {
			s.Status = prevStatus
			s.StatusChangedAt = time.Now()

			// Restore manager ownership removed above: nothing has been killed or
			// torn down on this path, so the session must return fully intact
			// (still tracked, process still reachable) rather than a live agent
			// orphaned from sm.sessions and shown as stopped.
			if hasPTY {
				sm.sessions[id] = ptySess
			}

			if hasClient {
				sm.attachedClients[id] = ac
			}

			_ = sm.saveState()
		}
		sm.mu.Unlock()

		// writeTombstone can fail after the temp file was renamed into place (a
		// dir-fsync error), leaving a marker on disk. Remove it so a later startup
		// doesn't resume a delete we just reported as aborted.
		sm.removeTombstone(id)

		return fmt.Errorf("delete aborted: could not write recovery tombstone: %w", err)
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
			sm.log.Warn("live driver did not finish during delete", "id", id, "err", err)
		}
	} else if orphanPID > 0 {
		sm.logStoppingPID(id, sm.sessionName(id), StopReasonDelete, "delete-orphan", orphanPID, orphanPID)

		if _, err := sm.killVerifiedProcess(orphanPID, orphanStartTime); err != nil {
			sm.log.Warn("failed to kill orphaned process during delete", "id", id, "pid", orphanPID, "err", err)
			sm.mu.Lock()
			if s, ok := sm.state.Sessions[id]; ok {
				s.Status = StatusErrored
				s.StatusChangedAt = time.Now()
				s.PID = orphanPID
				s.PIDStartTime = orphanStartTime
				applyLifecycleSummaryLocked(s, fmt.Sprintf("Delete aborted: orphaned process (PID %d) could not be killed: %v", orphanPID, err))

				_ = sm.saveState()
			}
			sm.mu.Unlock()

			// Delete is aborted and the session is kept, so drop the tombstone —
			// there is no interrupted delete to resume.
			sm.removeTombstone(id)

			if hasClient {
				ac.kick()
			}

			return fmt.Errorf("delete aborted: orphaned process (PID %d) could not be killed: %w", orphanPID, err)
		}
	}

	// Attempt git teardown before removing the session from state.
	if teardownErr := sm.teardownArtifacts(spec); teardownErr != nil {
		sm.log.Error("git teardown failed, session kept for retry",
			"session_id", id, "err", teardownErr)
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			if prevStatus == StatusRunning {
				s.Status = StatusStopped
			} else {
				s.Status = prevStatus
			}

			_ = sm.saveState()
		}
		sm.mu.Unlock()

		// Teardown failed and the session is kept for retry, so drop the
		// tombstone — the delete did not commit and must not auto-resume.
		sm.removeTombstone(id)

		if hasClient {
			ac.kick()
		}

		return fmt.Errorf("git teardown failed (session kept for retry): %w", teardownErr)
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
	if err == nil {
		sm.removeTombstone(id)
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

	type snapshot struct {
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

	snaps := make([]snapshot, 0, len(toDelete))

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

		s := snapshot{
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
		sess.PID = 0
		sess.PIDStartTime = 0
	}

	_ = sm.saveState()
	sm.mu.Unlock()

	// Kill all PTY processes outside the lock.
	killFailed := make(map[string]bool)

	for _, s := range snaps {
		if s.ptySess != nil {
			s.ptySess.Detach()

			if !s.ptySess.Exited() {
				sm.logStopping(s.id, s.name, StopReasonDelete, "delete-children", s.ptySess)
			}

			if err := sm.teardownLiveDriver(ctx, s.ptySess); err != nil {
				sm.log.Warn("live driver did not finish during bulk delete", "id", s.id, "err", err)
			}
		} else if s.pid > 0 {
			sm.logStoppingPID(s.id, s.name, StopReasonDelete, "delete-children-orphan", s.pid, s.pid)

			if _, err := sm.killVerifiedProcess(s.pid, s.pidStartTime); err != nil {
				sm.log.Warn("failed to kill orphaned process during delete", "id", s.id, "pid", s.pid, "err", err)
				sm.mu.Lock()
				if sess, ok := sm.state.Sessions[s.id]; ok {
					sess.Status = StatusErrored
					sess.StatusChangedAt = time.Now()
					sess.PID = s.pid
					sess.PIDStartTime = s.pidStartTime
					applyLifecycleSummaryLocked(sess, fmt.Sprintf("Delete aborted: orphaned process (PID %d) could not be killed: %v", s.pid, err))
				}

				_ = sm.saveState()
				sm.mu.Unlock()

				killFailed[s.id] = true
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

		var lateSnaps []snapshot

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

			ls := snapshot{
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

			lateSnaps = append(lateSnaps, ls)
			sess.Status = StatusDeleting
			sess.StatusChangedAt = time.Now()
			sess.PID = 0
		}

		if !progress {
			sm.mu.Unlock()
			break
		}

		sm.log.Info("sweep found late-arriving descendants", "count", len(lateSnaps), "round", sweep+1)
		_ = sm.saveState()
		sm.mu.Unlock()

		for _, s := range lateSnaps {
			if s.ptySess != nil {
				s.ptySess.Detach()

				if !s.ptySess.Exited() {
					sm.logStopping(s.id, s.name, StopReasonDelete, "delete-children-sweep", s.ptySess)
				}

				if err := sm.teardownLiveDriver(ctx, s.ptySess); err != nil {
					sm.log.Warn("late live driver did not finish during bulk delete", "id", s.id, "err", err)
				}
			}
		}

		snaps = append(snaps, lateSnaps...)

		if sweep == maxSweepRounds-1 {
			sm.log.Warn("sweep reached round cap, some descendants may remain", "cap", maxSweepRounds)
		}
	}

	// Attempt teardowns, tracking which succeed.
	var teardownErrs []error

	succeeded := make(map[string]bool, len(snaps))
	for _, s := range snaps {
		if killFailed[s.id] {
			continue
		}

		spec := teardownSpec{
			ID:           s.id,
			RepoPath:     s.repoPath,
			WorktreePath: s.worktreePath,
			Branch:       s.branch,
			Shared:       s.shared,
			InPlace:      s.inPlace,
			Includes:     s.includes,
		}

		// Tombstone before teardown so a crash mid-delete is resumed on startup.
		// Fail closed: if the recovery marker can't be written, skip this
		// session's teardown and keep it for retry rather than tear down a
		// worktree with no way to resume the delete.
		if err := sm.writeTombstone(tombstone{
			teardownSpec: spec,
			Name:         s.name,
			PID:          s.pid,
			PIDStartTime: s.pidStartTime,
			CreatedAt:    time.Now(),
		}); err != nil {
			sm.log.Error("failed to write delete tombstone; keeping session for retry",
				"id", s.id, "err", err)
			teardownErrs = append(teardownErrs, fmt.Errorf("session %s: write tombstone: %w", s.id, err))

			continue
		}

		if err := sm.teardownArtifacts(spec); err != nil {
			sm.log.Error("git teardown failed, session kept for retry",
				"session_id", s.id, "err", err)
			teardownErrs = append(teardownErrs, fmt.Errorf("session %s: %w", s.id, err))
			// Delete did not commit; drop the tombstone so it does not auto-resume.
			sm.removeTombstone(s.id)
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
		} else if sess, ok := sm.state.Sessions[s.id]; ok {
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
			if stateErr == nil {
				sm.removeTombstone(s.id)
			}

			_ = os.Remove(filepath.Join(sm.paths.LogDir, s.id+".log"))
			_ = os.Remove(sm.nonoProfilePath(s.id))
			_ = os.Remove(sm.safehouseFragmentPath(s.id))
			sm.cleanupHooks(s.id, s.agent, s.worktreePath)
		}

		if s.client != nil {
			s.client.kick()
		}
	}

	if stateErr != nil {
		return deletedIDs, stateErr
	}

	if len(teardownErrs) > 0 {
		return deletedIDs, fmt.Errorf("git teardown failed for %d session(s) (kept for retry): %w",
			len(teardownErrs), errors.Join(teardownErrs...))
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
		if sess.Status != StatusRunning || sess.PID <= 0 {
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
