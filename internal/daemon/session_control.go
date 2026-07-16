package daemon

import (
	"context"
	"fmt"
	"time"
)

// Stop sends SIGTERM to a session's process without removing the session or worktree.
func (sm *SessionManager) Stop(id string) error {
	return sm.stopWithReason(id, StopReasonUser, "user-stop")
}

func (sm *SessionManager) stopWithReason(id, reason, initiator string) error {
	sm.mu.Lock()
	sessState, ok := sm.state.Sessions[id]

	var status SessionStatus
	if ok {
		status = sessState.Status
	}

	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("session %q not found", id)
	}

	if status != StatusRunning {
		sm.mu.Unlock()
		return fmt.Errorf("session %q is not running", id)
	}

	name := sessState.Name
	sessState.StopReason = reason
	_ = sm.saveState()
	sm.mu.Unlock()

	ptySess, ok := sm.GetPTY(id)
	if ok {
		sm.logStopping(id, name, reason, initiator, ptySess)

		if err := ptySess.Kill(); err != nil {
			return fmt.Errorf("send SIGTERM: %w", err)
		}

		return nil
	}

	sm.mu.Lock()
	pid := sessState.PID
	startTime := sessState.PIDStartTime
	sm.mu.Unlock()

	sm.logStoppingPID(id, name, reason, initiator+"-orphan", pid, pid)

	killed, err := sm.killVerifiedProcess(pid, startTime)

	sm.mu.Lock()
	if s, ok := sm.state.Sessions[id]; ok {
		switch {
		case killed:
			s.Status = StatusStopped
			s.StatusChangedAt = time.Now()
			s.PID = 0
			s.PIDStartTime = 0
			applyLifecycleSummaryLocked(s, "Orphaned process killed")
		case err != nil:
			s.Status = StatusErrored
			s.StatusChangedAt = time.Now()
			applyLifecycleSummaryLocked(s, fmt.Sprintf("Could not kill orphaned process (PID %d): %v", pid, err))
		default:
			s.Status = StatusStopped
			s.StatusChangedAt = time.Now()
			s.PID = 0
			s.PIDStartTime = 0
			applyLifecycleSummaryLocked(s, "Process already exited")
		}

		_ = sm.saveState()
	}
	sm.mu.Unlock()

	return err
}

func filterExcludeRoot(ids []string, rootID string) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != rootID {
			result = append(result, id)
		}
	}

	return result
}

// StopWithChildren stops all descendants of rootID. If excludeRoot is true,
// the root session itself is not stopped. Already-stopped sessions are skipped.
// Returns the list of session IDs that were actually stopped.
func (sm *SessionManager) StopWithChildren(rootID string, excludeRoot bool) ([]string, error) {
	sm.mu.Lock()

	if _, ok := sm.state.Sessions[rootID]; !ok {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q not found", rootID)
	}

	toStop := sm.collectDescendants(rootID)
	if excludeRoot {
		toStop = filterExcludeRoot(toStop, rootID)
	}

	sm.mu.Unlock()

	var stopped []string

	for _, id := range toStop {
		sm.mu.Lock()

		sess, ok := sm.state.Sessions[id]
		if !ok {
			sm.mu.Unlock()
			continue
		}

		if sess.Starred {
			sm.mu.Unlock()
			sm.log.Info("skipping starred session in bulk stop", "session_id", id, "name", sess.Name)

			continue
		}

		if sess.Status != StatusRunning {
			sm.mu.Unlock()
			continue
		}

		sess.StopReason = StopReasonUser
		name := sess.Name
		pid := sess.PID
		startTime := sess.PIDStartTime
		_ = sm.saveState()
		sm.mu.Unlock()

		ptySess, ok := sm.GetPTY(id)
		if ok {
			sm.logStopping(id, name, StopReasonUser, "stop-children", ptySess)

			if err := ptySess.Kill(); err != nil {
				sm.log.Warn("stop child failed", "session_id", id, "error", err)
				continue
			}

			stopped = append(stopped, id)

			continue
		}

		sm.logStoppingPID(id, name, StopReasonUser, "stop-children-orphan", pid, pid)

		killed, killErr := sm.killVerifiedProcess(pid, startTime)
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			switch {
			case killed:
				s.Status = StatusStopped
				s.StatusChangedAt = time.Now()
				s.PID = 0
				s.PIDStartTime = 0
				applyLifecycleSummaryLocked(s, "Orphaned process killed")

				stopped = append(stopped, id)
			case killErr != nil:
				s.Status = StatusErrored
				s.StatusChangedAt = time.Now()
				applyLifecycleSummaryLocked(s, fmt.Sprintf("Could not kill orphaned process (PID %d): %v", pid, killErr))
			default:
				s.Status = StatusStopped
				s.StatusChangedAt = time.Now()
				s.PID = 0
				s.PIDStartTime = 0
				applyLifecycleSummaryLocked(s, "Process already exited")

				stopped = append(stopped, id)
			}

			_ = sm.saveState()
		}
		sm.mu.Unlock()
	}

	// Sweep for sessions created between collectDescendants and the stop loop.
	stoppedSet := make(map[string]bool, len(toStop))
	for _, id := range toStop {
		stoppedSet[id] = true
	}

	const maxSweepRounds = 10
	for sweep := 0; sweep < maxSweepRounds; sweep++ {
		sm.mu.Lock()

		var late []string

		progress := false

		for sid, sess := range sm.state.Sessions {
			if stoppedSet[sid] || sess.ParentID == "" || !stoppedSet[sess.ParentID] {
				continue
			}

			stoppedSet[sid] = true

			if sess.Starred {
				continue
			}

			if sess.Status == StatusCreating {
				// Remove placeholder so Phase 3 of Create detects the
				// cancellation and cleans up the PTY/worktree.
				delete(sm.state.Sessions, sid)
				delete(sm.hookReports, sid)

				progress = true

				continue
			}

			if sess.Status != StatusRunning {
				continue
			}

			progress = true

			late = append(late, sid)
			sess.StopReason = StopReasonUser
		}

		if !progress {
			sm.mu.Unlock()
			break
		}

		if len(late) > 0 {
			sm.log.Info("sweep found late-arriving descendants to stop", "count", len(late), "round", sweep+1)
		}

		_ = sm.saveState()
		sm.mu.Unlock()

		for _, lid := range late {
			ptySess, ok := sm.GetPTY(lid)
			if !ok {
				continue
			}

			sm.logStopping(lid, sm.sessionName(lid), StopReasonUser, "stop-children-sweep", ptySess)

			if err := ptySess.Kill(); err != nil {
				sm.log.Warn("stop late child failed", "session_id", lid, "error", err)
				continue
			}

			stopped = append(stopped, lid)
		}

		if sweep == maxSweepRounds-1 {
			sm.log.Warn("stop sweep reached round cap, some descendants may remain", "cap", maxSweepRounds)
		}
	}

	return stopped, nil
}

// Restart stops a running session (or no-ops if already stopped) and resumes it,
// picking up the current agent and sandbox configuration. A plain user restart
// attributes the teardown to StopReasonUser; internal callers use
// restartWithReason to preserve the true subsystem (e.g. the startup watchdog).
func (sm *SessionManager) Restart(id string, rows, cols uint16) (SessionState, error) {
	return sm.restartWithReason(id, rows, cols, StopReasonUser, "restart")
}

// restartWithReason is Restart with an explicit stop attribution so a watchdog
// recovery isn't logged as an authenticated user restart (issue #1104).
func (sm *SessionManager) restartWithReason(id string, rows, cols uint16, stopReason, initiator string) (SessionState, error) {
	sm.mu.RLock()

	softDeleted := false
	if s, ok := sm.state.Sessions[id]; ok {
		softDeleted = s.IsSoftDeleted()
	}

	sm.mu.RUnlock()

	if softDeleted {
		return SessionState{}, errSoftDeleted(sm.sessionName(id))
	}

	ptySess, hasPTY := sm.GetPTY(id)

	sm.log.Info("restart requested", "session_id", id, "has_live_pty", hasPTY,
		"pty_exited", hasPTY && ptySess.Exited(),
		"scrollback_path", sm.scrollbackLogPath(id))

	if hasPTY && !ptySess.Exited() {
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.StopReason = stopReason
		}
		sm.mu.Unlock()

		sm.logStopping(id, sm.sessionName(id), stopReason, initiator, ptySess)

		if err := sm.teardownLiveDriver(context.Background(), ptySess); err != nil {
			return SessionState{}, fmt.Errorf("stop session: %w", err)
		}

		// teardownLiveDriver closes the old PTY once its bounded escalation has
		// completed. The stale watcher may safely double-close it.
		sm.log.Info("restart: old pty stopped, closing", "session_id", id,
			"old_output_bytes", ptySess.BytesRead())

		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok && s.Status == StatusRunning {
			exitCode := ptySess.ExitCode()
			s.Status = StatusStopped
			s.StatusChangedAt = time.Now()
			s.ExitCode = &exitCode
			s.PID = 0
			s.PIDStartTime = 0

			_ = sm.saveState()
		}
		sm.mu.Unlock()
	} else if !hasPTY {
		sm.mu.Lock()

		sess, ok := sm.state.Sessions[id]
		if ok && sess.Status == StatusRunning && sess.PID > 0 {
			pid := sess.PID
			startTime := sess.PIDStartTime
			name := sess.Name
			sm.mu.Unlock()

			sm.logStoppingPID(id, name, stopReason, initiator+"-orphan", pid, pid)

			killed, killErr := sm.killVerifiedProcess(pid, startTime)

			sm.mu.Lock()
			if s, ok := sm.state.Sessions[id]; ok && s.Status == StatusRunning {
				switch {
				case killed:
					s.Status = StatusStopped
					s.StatusChangedAt = time.Now()
					s.PID = 0
					s.PIDStartTime = 0
					s.StopReason = StopReasonUser
					applyLifecycleSummaryLocked(s, "Orphaned process killed for restart")

					_ = sm.saveState()
				case killErr == nil:
					s.Status = StatusStopped
					s.StatusChangedAt = time.Now()
					s.PID = 0
					s.PIDStartTime = 0
					s.StopReason = StopReasonUser
					applyLifecycleSummaryLocked(s, "Process already exited")

					_ = sm.saveState()
				default:
					s.Status = StatusErrored
					s.StatusChangedAt = time.Now()
					applyLifecycleSummaryLocked(s,
						fmt.Sprintf("Cannot restart: orphaned process (PID %d) — %v", pid, killErr))

					_ = sm.saveState()
					sm.mu.Unlock()

					return SessionState{}, fmt.Errorf("cannot restart: orphaned process (PID %d) could not be killed: %w", pid, killErr)
				}
			}
			sm.mu.Unlock()
		} else {
			sm.mu.Unlock()
		}
	}

	return sm.resumeWithSummary(id, rows, cols, "Restarted")
}

func (sm *SessionManager) RestartWithChildren(rootID string, excludeRoot bool, rows, cols uint16) ([]string, error) {
	sm.mu.Lock()
	if _, ok := sm.state.Sessions[rootID]; !ok {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q not found", rootID)
	}

	toRestart := sm.collectDescendants(rootID)
	if excludeRoot {
		toRestart = filterExcludeRoot(toRestart, rootID)
	}
	sm.mu.Unlock()

	var restarted []string

	for _, id := range toRestart {
		sm.mu.RLock()

		sess, ok := sm.state.Sessions[id]
		if !ok {
			sm.mu.RUnlock()
			continue
		}

		if sess.Starred {
			sm.mu.RUnlock()
			sm.log.Info("skipping starred session in bulk restart", "session_id", id, "name", sess.Name)

			continue
		}

		if sess.Status == StatusDeleting || sess.Status == StatusCreating {
			sm.mu.RUnlock()
			continue
		}

		if sess.IsSoftDeleted() {
			sm.mu.RUnlock()
			sm.log.Info("skipping soft-deleted session in bulk restart", "session_id", id, "name", sess.Name)

			continue
		}

		sm.mu.RUnlock()

		if _, err := sm.Restart(id, rows, cols); err != nil {
			sm.log.Warn("restart child failed", "session_id", id, "error", err)
			continue
		}

		restarted = append(restarted, id)
	}

	return restarted, nil
}
