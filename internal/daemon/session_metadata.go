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
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/sessionlabel"
)

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

// SessionUpdate is an atomic delta over mutable session metadata. Nil scalar
// fields are left unchanged; labels are individual add/remove operations so a
// stale client never replaces another client's complete set.
type SessionUpdate struct {
	Name         *string
	ParentID     *string
	Starred      *bool
	AddLabels    []string
	RemoveLabels []string
}

// Update preserves the pre-label call shape for internal lifecycle callers.
func (sm *SessionManager) Update(id string, name *string, parentID *string, starred *bool) (SessionState, error) {
	return sm.UpdateMetadata(id, SessionUpdate{Name: name, ParentID: parentID, Starred: starred})
}

// UpdateMetadata atomically validates, applies, and persists the requested
// session metadata fields. A failed save restores the complete in-memory
// snapshot, so callers never observe a mutation that exists only in RAM.
func (sm *SessionManager) UpdateMetadata(id string, update SessionUpdate) (SessionState, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[id]
	if !ok {
		return SessionState{}, fmt.Errorf("session %q not found", id)
	}

	if IsSystemSession(s) {
		return SessionState{}, fmt.Errorf("cannot update system session %q", s.Name)
	}

	if s.Status == StatusDeleting {
		return SessionState{}, fmt.Errorf("session %q is being deleted", id)
	}

	if s.IsSoftDeleted() {
		return SessionState{}, errSoftDeleted(s.Name)
	}

	if sm.subtreeDeleteActiveLocked(id) {
		return SessionState{}, fmt.Errorf("session %q is undergoing subtree deletion", id)
	}

	if update.Name != nil {
		if err := ValidateSessionName(*update.Name); err != nil {
			return SessionState{}, err
		}
	}

	labels := s.Labels

	if len(update.AddLabels) > 0 || len(update.RemoveLabels) > 0 {
		var err error

		labels, err = sessionlabel.Apply(s.Labels, update.AddLabels, update.RemoveLabels)
		if err != nil {
			return SessionState{}, err
		}
	}

	newParentValue := s.ParentID

	if update.ParentID != nil {
		newParent := *update.ParentID
		if newParent == "" {
			newParentValue = ""
		} else {
			if sm.subtreeDeleteActiveLocked(newParent) {
				return SessionState{}, errors.New("parent session is undergoing subtree deletion")
			}

			if newParent == id {
				return SessionState{}, errors.New("cannot set session as its own parent")
			}

			parent, ok := sm.state.Sessions[newParent]
			if !ok {
				return SessionState{}, fmt.Errorf("parent session %q not found", newParent)
			}

			if parent.Status == StatusCreating {
				return SessionState{}, fmt.Errorf("parent session %q is being created", parent.Name)
			}

			if parent.Status == StatusDeleting || parent.IsSoftDeleted() {
				return SessionState{}, fmt.Errorf("parent session %q is being deleted", parent.Name)
			}

			descendants := sm.collectDescendants(id)
			for _, d := range descendants {
				if d == newParent {
					return SessionState{}, fmt.Errorf("cannot set descendant %q as parent (would create cycle)", newParent)
				}
			}

			newParentValue = newParent
		}
	}

	before := cloneSessionState(s)

	if update.Name != nil {
		s.Name = *update.Name
	}

	s.ParentID = newParentValue
	if update.Starred != nil {
		s.Starred = *update.Starred
	}

	if len(update.AddLabels) > 0 || len(update.RemoveLabels) > 0 {
		s.Labels = labels
	}

	if err := sm.saveState(); err != nil {
		*s = before
		return SessionState{}, err
	}

	return cloneSessionState(s), nil
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
			case "error":
				f.Errored++
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
//
//nolint:wsl_v5 // diagnostics assembly keeps related snapshots together.
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
			case "error":
				fleet.Errored++
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

	msg := protocol.DiagnosticsMsg{
		DaemonPID:         os.Getpid(),
		DaemonUptime:      now.Sub(sm.startedAt).Truncate(time.Second).String(),
		TerminalBackend:   grpty.TerminalBackend(),
		Fleet:             fleet,
		Sessions:          sessions,
		DeletedSessionIDs: deletedSessionIDs,
		Scrollback:        sbDiag,
		Messages:          msgDiag,
		Triggers:          sm.degradedTriggerDiagnostics(),
		Purge:             sm.purgeDiagnostic(),
	}
	if sm.prPush != nil {
		stats := sm.prPush.stats.snapshot()

		push := &protocol.PRPushDiagnostic{State: stats.State, LastError: stats.LastError, Accepted: stats.Accepted, Rejected: stats.Rejected, Duplicate: stats.Duplicate, Coalesced: stats.Coalesced, Dropped: stats.Dropped, Kicks: stats.Kicks}
		if !stats.LastDelivery.IsZero() {
			push.LastDelivery = stats.LastDelivery.UTC().Format(time.RFC3339)
		}

		msg.PRPush = push
	}
	return msg
}

// purgeDiagnostic builds the soft-delete purge schedule consumed by gr doctor:
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
func (sm *SessionManager) GetPTY(id string) (sessionDriver, bool) {
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

			sm.setStopReasonLocked(id, s, StopReasonShutdown)
			text := formatStopSummary(StopReasonShutdown, nil, "", prevSummary, prevSetAt, prevTTL)
			applyLifecycleSummaryLocked(s, text)
		}
	}

	_ = sm.saveState()

	type snapshot struct {
		id   string
		name string
		sess sessionDriver
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
		go func(id string, sess sessionDriver) {
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
