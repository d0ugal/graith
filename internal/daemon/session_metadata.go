package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

// Star marks a session as starred.
func (sm *SessionManager) Star(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}

	if s.Status == StatusDeleting {
		return fmt.Errorf("session %q is being deleted", id)
	}

	if s.IsSoftDeleted() {
		return errSoftDeleted(s.Name)
	}

	s.Starred = true

	return sm.saveState()
}

func (sm *SessionManager) Unstar(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}

	if s.Status == StatusDeleting {
		return fmt.Errorf("session %q is being deleted", id)
	}

	if s.IsSoftDeleted() {
		return errSoftDeleted(s.Name)
	}

	s.Starred = false

	return sm.saveState()
}

func sanitizeSummaryText(text string) string {
	var b strings.Builder

	for _, r := range text {
		if r >= 32 && r != 127 {
			b.WriteRune(r)
		}
	}

	return strings.TrimSpace(b.String())
}

func (sm *SessionManager) SetSummary(sessionID, text string, ttlSeconds int) error {
	text = sanitizeSummaryText(text)
	if len(text) > 100 {
		return errors.New("summary text exceeds 100 bytes")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}

	// A soft-deleted session's summary is the "recoverable until …" trash marker;
	// a lingering background `gr status` must not overwrite it and mask the
	// session's deleted state in the overlay/logs.
	if s.IsSoftDeleted() {
		return errSoftDeleted(s.Name)
	}

	now := time.Now()
	s.SummaryText = text
	s.SummarySetAt = &now
	s.SummaryTTL = ttlSeconds

	if text == "" {
		s.SummaryText = ""
		s.SummarySetAt = nil
		s.SummaryTTL = 0
	}

	return sm.saveState()
}

func (sm *SessionManager) ClearSummary(sessionID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}

	if s.IsSoftDeleted() {
		return errSoftDeleted(s.Name)
	}

	s.SummaryText = ""
	s.SummarySetAt = nil
	s.SummaryTTL = 0

	return sm.saveState()
}

func (sm *SessionManager) Rename(id, newName string) error {
	if err := ValidateSessionName(newName); err != nil {
		return err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}

	if IsSystemSession(s) {
		return fmt.Errorf("cannot rename system session %q", s.Name)
	}

	if s.IsSoftDeleted() {
		return errSoftDeleted(s.Name)
	}

	s.Name = newName

	return sm.saveState()
}

func (sm *SessionManager) Update(id string, name *string, parentID *string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}

	if IsSystemSession(s) {
		return fmt.Errorf("cannot update system session %q", s.Name)
	}

	if s.IsSoftDeleted() {
		return errSoftDeleted(s.Name)
	}

	if name != nil {
		if err := ValidateSessionName(*name); err != nil {
			return err
		}
	}

	newParentValue := s.ParentID

	if parentID != nil {
		newParent := *parentID
		if newParent == "" {
			newParentValue = ""
		} else {
			if newParent == id {
				return errors.New("cannot set session as its own parent")
			}

			if _, ok := sm.state.Sessions[newParent]; !ok {
				return fmt.Errorf("parent session %q not found", newParent)
			}

			descendants := sm.collectDescendants(id)
			for _, d := range descendants {
				if d == newParent {
					return fmt.Errorf("cannot set descendant %q as parent (would create cycle)", newParent)
				}
			}

			newParentValue = newParent
		}
	}

	if name != nil {
		s.Name = *name
	}

	s.ParentID = newParentValue

	return sm.saveState()
}

// List returns copies of all known session states.
func (sm *SessionManager) List() []SessionState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	list := make([]SessionState, 0, len(sm.state.Sessions))
	for _, s := range sm.state.Sessions {
		list = append(list, cloneSessionState(s))
	}

	return list
}

func (sm *SessionManager) fleetSummary() protocol.FleetSummary {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var f protocol.FleetSummary

	for _, s := range sm.state.Sessions {
		if s.IsSoftDeleted() {
			continue
		}

		f.Total++

		switch s.Status {
		case StatusRunning:
			switch s.AgentStatus {
			case "approval":
				f.Approval++
			case "ready":
				f.Ready++
			default:
				f.Active++
			}
		case StatusCreating:
			f.Active++
		case StatusStopped:
			f.Stopped++
		case StatusErrored:
			f.Errored++
		}
	}

	return f
}

// Diagnostics collects runtime health data for gr doctor.
func (sm *SessionManager) Diagnostics() protocol.DiagnosticsMsg {
	sm.mu.RLock()
	cfg := sm.cfg
	now := time.Now()

	var (
		sessions          []protocol.SessionDiagnostic
		deletedSessionIDs []string
		sbDiag            protocol.ScrollbackDiagnostic
		fleet             protocol.FleetSummary
	)

	for id, s := range sm.state.Sessions {
		// Soft-deleted sessions are hidden trash awaiting purge; exclude them from
		// diagnostics and the fleet tally so `gr doctor` reflects live work only.
		// Keep their IDs as a separate ownership signal so doctor's orphan cleanup
		// does not destroy resources that remain recoverable until purge.
		if s.IsSoftDeleted() {
			deletedSessionIDs = append(deletedSessionIDs, id)
			continue
		}

		sd := protocol.SessionDiagnostic{
			ID:          id,
			Name:        s.Name,
			Status:      string(s.Status),
			AgentStatus: s.AgentStatus,
			PID:         s.PID,
		}

		// Tally fleet summary from the same snapshot as the session list.
		fleet.Total++

		switch s.Status {
		case StatusRunning:
			switch s.AgentStatus {
			case "approval":
				fleet.Approval++
			case "ready":
				fleet.Ready++
			default:
				fleet.Active++
			}
		case StatusCreating:
			fleet.Active++
		case StatusStopped:
			fleet.Stopped++
		case StatusErrored:
			fleet.Errored++
		}

		if s.Status == StatusRunning && s.PID > 0 {
			sd.PIDAlive = isProcessAlive(s.PID)
		}

		_, hasPTY := sm.sessions[id]
		hasPTYVal := hasPTY
		sd.HasPTY = &hasPTYVal

		if s.WorktreePath != "" {
			sd.WorktreePath = s.WorktreePath
			if _, err := os.Stat(s.WorktreePath); err == nil {
				sd.WorktreeExists = true
			}
		}

		sd.ConfigStale = isConfigStale(*s, cfg)
		sd.HasToken = s.Token != ""

		if hr, ok := sm.hookReports[id]; ok && s.Status == StatusRunning {
			sd.HookStale = now.After(hr.AuthoritativeUntil)
		}

		if ptySess, ok := sm.sessions[id]; ok {
			written, maxSize, saturated := ptySess.ScrollbackFile().Stats()
			sd.ScrollbackBytes = written
			sd.ScrollbackMax = maxSize
			sd.Saturated = saturated

			sbDiag.TotalFiles++

			sbDiag.TotalBytes += written
			if saturated {
				sbDiag.SaturatedCount++
			}
		}

		sessions = append(sessions, sd)
	}

	sm.mu.RUnlock()

	var msgDiag protocol.MessagesDiagnostic

	if sm.messages != nil {
		if streams, err := sm.messages.ListStreams("", true); err == nil {
			msgDiag.TotalStreams = len(streams)
			for _, s := range streams {
				msgDiag.TotalMessages += s.Total
			}
		}
	}

	return protocol.DiagnosticsMsg{
		DaemonPID:         os.Getpid(),
		DaemonUptime:      now.Sub(sm.startedAt).Truncate(time.Second).String(),
		Fleet:             fleet,
		Sessions:          sessions,
		DeletedSessionIDs: deletedSessionIDs,
		Scrollback:        sbDiag,
		Messages:          msgDiag,
		Triggers:          sm.degradedTriggerDiagnostics(),
		Purge:             sm.purgeDiagnostic(),
	}
}

// purgeDiagnostic reports the soft-delete purge sweep schedule for gr doctor:
// the configured cadence plus the last/next sweep times once a sweep has run.
func (sm *SessionManager) purgeDiagnostic() *protocol.PurgeDiagnostic {
	sm.mu.RLock()
	del := sm.cfg.Delete
	sm.mu.RUnlock()

	last, next := sm.purgeSweepStats()

	diag := &protocol.PurgeDiagnostic{
		StartupDelay: del.PurgeStartupDelayDuration().String(),
		Interval:     del.PurgeIntervalDuration().String(),
	}

	if !last.IsZero() {
		diag.LastSweep = last.Format(time.RFC3339)
	}

	if !next.IsZero() {
		diag.NextSweep = next.Format(time.RFC3339)
	}

	return diag
}

// degradedTriggerDiagnostics reports the currently-degraded watch-trigger
// bindings for gr doctor. Binding facts are snapshotted under triggers.mu, then
// session names are resolved after releasing it (sessionName takes sm.mu) to
// avoid holding both locks at once.
func (sm *SessionManager) degradedTriggerDiagnostics() []protocol.TriggerDiagnostic {
	sm.triggers.mu.Lock()

	out := make([]protocol.TriggerDiagnostic, 0)

	for _, b := range sm.triggers.bindings {
		if b.degraded == "" {
			continue
		}

		td := protocol.TriggerDiagnostic{
			Name:       b.triggerName,
			SessionID:  b.sessionID,
			Degraded:   b.degraded,
			RetryCount: b.retryCount,
		}
		if !b.nextRetryAt.IsZero() {
			td.NextRetryAt = b.nextRetryAt.Format(time.RFC3339)
		}

		out = append(out, td)
	}
	sm.triggers.mu.Unlock()

	for i := range out {
		out[i].SessionName = sm.sessionName(out[i].SessionID)
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// Get returns a copy of a session state by ID.
func (sm *SessionManager) Get(id string) (SessionState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	s, ok := sm.state.Sessions[id]
	if !ok {
		return SessionState{}, false
	}

	return cloneSessionState(s), ok
}

// scrollbackLogPath returns the on-disk scrollback log path for a session ID.
// The file persists after the live PTY is torn down, so it can be read for
// stopped or crashed sessions.
func (sm *SessionManager) scrollbackLogPath(id string) string {
	return filepath.Join(sm.paths.LogDir, id+".log")
}

// GetPTY returns the live session driver by ID. Named for its historic
// PTY-only past; today it may return any SessionDriver implementation.
func (sm *SessionManager) GetPTY(id string) (SessionDriver, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	s, ok := sm.sessions[id]

	return s, ok
}

func (sm *SessionManager) getHookReport(sessionID string) *hookReport {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if hr, ok := sm.hookReports[sessionID]; ok {
		return &hr
	}

	return nil
}

// StopAll gracefully terminates all running sessions concurrently.
// Each session gets up to 5 seconds to exit after SIGTERM before being
// force-killed. Sessions are waited on in parallel so the total wait
// time is bounded by the slowest session, not the sum.
func (sm *SessionManager) StopAll(ctx context.Context) {
	sm.mu.Lock()
	for id, s := range sm.state.Sessions {
		if s.Status == StatusRunning {
			prevSummary, prevSetAt := sm.prevStopSummaryLocked(s, id)

			prevTTL := sm.cfg.Status.TTLDuration()
			if s.SummaryTTL > 0 {
				prevTTL = time.Duration(s.SummaryTTL) * time.Second
			}

			s.StopReason = StopReasonShutdown
			text := formatStopSummary(StopReasonShutdown, nil, "", prevSummary, prevSetAt, prevTTL)
			applyLifecycleSummaryLocked(s, text)
		}
	}

	_ = sm.saveState()

	type snapshot struct {
		id   string
		name string
		sess SessionDriver
	}

	sessions := make([]snapshot, 0, len(sm.sessions))
	for id, sess := range sm.sessions {
		name := ""
		if s, ok := sm.state.Sessions[id]; ok {
			name = s.Name
		}

		sessions = append(sessions, snapshot{id, name, sess})
	}
	sm.mu.Unlock()

	for _, s := range sessions {
		if !s.sess.Exited() {
			sm.logStopping(s.id, s.name, StopReasonShutdown, "shutdown", s.sess)
			_ = s.sess.Kill()
		}
	}

	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(id string, sess SessionDriver) {
			defer wg.Done()

			select {
			case <-sess.Done():
			case <-ctx.Done():
				sm.log.Warn("shutdown context expired, force killing session", "id", id)

				_ = sess.ForceKill()
			case <-time.After(5 * time.Second):
				sm.log.Warn("force killing session", "id", id)

				_ = sess.ForceKill()
			}
		}(s.id, s.sess)
	}

	wg.Wait()

	// Wait for the exit watchers to finish their post-exit work (state writes
	// and status publishes). Every killed session above has now exited, so the
	// watchers can proceed and will not block. This guarantees no watcher is
	// still writing state or publishing to the message store after StopAll
	// returns, which matters when the caller then closes the message store or
	// removes the data dir.
	sm.watchers.Wait()
}

func (sm *SessionManager) RunMessageCleanupLoop(ctx context.Context) {
	if sm.messages == nil {
		return
	}

	runMessageCleanupLoop(ctx, sm.loopTicker, sm.runMessageCleanupFromConfig)
}

func runMessageCleanupLoop(ctx context.Context, newTicker func(time.Duration) loopTicker, cleanup func()) {
	cleanup()

	ticker := newTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			cleanup()
		}
	}
}

func (sm *SessionManager) runMessageCleanupFromConfig() {
	sm.mu.RLock()
	maxAge := sm.cfg.Messages.MaxAgeDuration()
	maxPerStream := sm.cfg.Messages.MaxPerStream
	sm.mu.RUnlock()

	if maxAge == 0 && maxPerStream == 0 {
		return
	}

	sm.runMessageCleanup(maxAge, maxPerStream)
}

func (sm *SessionManager) runMessageCleanup(maxAge time.Duration, maxPerStream int) {
	deleted, err := sm.messages.Cleanup(maxAge, maxPerStream)
	if err != nil {
		sm.log.Error("message cleanup failed", "err", err)
		return
	}

	if deleted > 0 {
		sm.log.Info("message cleanup", "deleted", deleted)
	}
}
