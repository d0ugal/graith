package daemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

const scenarioCompletionTick = 500 * time.Millisecond

// scenarioCompletionRuntime contains only restart-rebuildable coordination.
// Epoch, action, and cleanup facts live on ScenarioState and are always saved
// before external work starts.
type scenarioCompletionRuntime struct {
	mu       sync.Mutex
	wake     chan string
	cancels  map[string]context.CancelFunc
	cleanups map[string]bool
}

func newScenarioCompletionRuntime() *scenarioCompletionRuntime {
	return &scenarioCompletionRuntime{
		wake:     make(chan string, 64),
		cancels:  make(map[string]context.CancelFunc),
		cleanups: make(map[string]bool),
	}
}

func completionCleanupKey(scenarioID string, epoch int) string {
	return fmt.Sprintf("%s\x00%d", scenarioID, epoch)
}

func (r *scenarioCompletionRuntime) setCleanup(key string, running bool) {
	r.mu.Lock()
	if running {
		r.cleanups[key] = true
	} else {
		delete(r.cleanups, key)
	}
	r.mu.Unlock()
}

func (r *scenarioCompletionRuntime) hasCleanup(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.cleanups[key]
}

func completionActionKey(scenarioID string, epoch int, action string) string {
	return fmt.Sprintf("%s\x00%d\x00%s", scenarioID, epoch, action)
}

func (r *scenarioCompletionRuntime) setCancel(key string, cancel context.CancelFunc) {
	r.mu.Lock()
	r.cancels[key] = cancel
	r.mu.Unlock()
}

func (r *scenarioCompletionRuntime) clearCancel(key string) {
	r.mu.Lock()
	delete(r.cancels, key)
	r.mu.Unlock()
}

func (r *scenarioCompletionRuntime) hasCancel(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, ok := r.cancels[key]

	return ok
}

func (r *scenarioCompletionRuntime) cancelScenario(scenarioID string) {
	prefix := scenarioID + "\x00"

	r.mu.Lock()

	var cancels []context.CancelFunc
	for key, cancel := range r.cancels {
		if strings.HasPrefix(key, prefix) {
			cancels = append(cancels, cancel)
			delete(r.cancels, key)
		}
	}
	r.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}

// hintScenarioCompletion is deliberately lossy. Todo events are hints only;
// the loop always rereads the authoritative todo store and also polls so a lost
// hint or daemon crash cannot lose an edge.
func (sm *SessionManager) hintScenarioCompletion(scope string) {
	if sm.completion == nil || !strings.HasPrefix(scope, "scenario:") {
		return
	}

	id := strings.TrimPrefix(scope, "scenario:")
	select {
	case sm.completion.wake <- id:
	default:
	}
}

// RunScenarioCompletionLoop reconciles authoritative todo-derived state,
// dispatches durable actions, adopts session actions after restart, and runs
// due cleanup. The first pass happens immediately for startup recovery.
func (sm *SessionManager) RunScenarioCompletionLoop(ctx context.Context) {
	if sm.completion == nil {
		return
	}

	ticker := time.NewTicker(scenarioCompletionTick)
	defer ticker.Stop()

	sm.processScenarioCompletions(ctx, "")

	for {
		select {
		case <-ctx.Done():
			return
		case id := <-sm.completion.wake:
			sm.processScenarioCompletions(ctx, id)
		case <-ticker.C:
			sm.processScenarioCompletions(ctx, "")
		}
	}
}

func (sm *SessionManager) processScenarioCompletions(ctx context.Context, onlyID string) {
	ids := []string{onlyID}
	if onlyID == "" {
		sm.mu.RLock()

		ids = make([]string, 0, len(sm.state.Scenarios))
		for id := range sm.state.Scenarios {
			ids = append(ids, id)
		}

		sm.mu.RUnlock()
	}

	for _, id := range ids {
		if id == "" {
			continue
		}

		if err := sm.reconcileScenarioCompletion(id); err != nil {
			sm.log.Warn("scenario completion reconcile failed", "scenario", id, "err", err)
			continue
		}

		//nolint:contextcheck // Recovery may resume a durable PTY lifecycle that outlives this pass.
		sm.recoverOrFinishCompletionSessions(id)
		sm.recoverScenarioCleanup(id)
		sm.dispatchPendingCompletionActions(ctx, id)
		sm.dispatchDueScenarioCleanup(ctx, id)
	}
}

// authoritativeScenarioComplete rereads todo state outside sm.mu. Todo event
// payloads are never trusted to establish an edge.
func (sm *SessionManager) authoritativeScenarioComplete(id string) (bool, error) {
	if sm.todos == nil {
		return false, nil
	}

	progress, err := sm.todos.AssigneeProgress("scenario:" + id)
	if err != nil {
		return false, err
	}

	sm.mu.RLock()

	sc := sm.state.Scenarios[id]
	if sc == nil {
		sm.mu.RUnlock()
		return false, nil
	}

	ids := append([]string(nil), sc.SessionIDs...)
	errored := false

	for _, sid := range ids {
		if s := sm.state.Sessions[sid]; s != nil && s.Status == StatusErrored {
			errored = true
			break
		}
	}

	sm.mu.RUnlock()

	tracked := 0
	complete := 0

	for _, sid := range ids {
		if p, ok := progress[sid]; ok && p[1] > 0 {
			tracked++

			if p[0] == p[1] {
				complete++
			}
		}
	}

	return !errored && tracked > 0 && complete == tracked, nil
}

func (sm *SessionManager) reconcileScenarioCompletion(id string) error {
	complete, err := sm.authoritativeScenarioComplete(id)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	reopened := false

	var cancelSessions []string

	sm.mu.Lock()

	sc := sm.state.Scenarios[id]
	if sc == nil || sc.Completion.Complete == complete {
		sm.mu.Unlock()
		return nil
	}

	if !complete {
		sc.Completion.Complete = false

		sc.Completion.TransitionedAt = &now
		for i := range sc.Completion.Actions {
			a := &sc.Completion.Actions[i]
			if a.State == CompletionActionPending || a.State == CompletionActionRunning {
				if a.SessionID != "" {
					cancelSessions = append(cancelSessions, a.SessionID)
				}

				a.State = CompletionActionFailed
				a.Error = "scenario reopened before completion action finished"
				a.FinishedAt = &now
			}
		}

		if sc.Completion.Cleanup != nil && sc.Completion.Cleanup.State != ScenarioCleanupSucceeded {
			sc.Completion.Cleanup.State = ScenarioCleanupCancelled
			sc.Completion.Cleanup.Error = "scenario reopened"
			sc.Completion.Cleanup.FinishedAt = &now
		}

		reopened = true
	} else {
		sc.Completion.Complete = true
		sc.Completion.Epoch++
		sc.Completion.TransitionedAt = &now
		sc.Completion.Actions = nil

		for _, trigger := range sc.Triggers {
			if trigger.IsCompletion() && trigger.TriggerEnabled() {
				sc.Completion.Actions = append(sc.Completion.Actions, ScenarioCompletionActionState{
					Name:  trigger.Name,
					State: CompletionActionPending,
				})
			}
		}

		if policy := sc.Lifecycle.CleanupMode(); policy != config.ScenarioCleanupOff {
			sc.Completion.Cleanup = &ScenarioCleanupState{Policy: policy, State: ScenarioCleanupPending}
		} else {
			sc.Completion.Cleanup = nil
		}

		sm.evaluateScenarioCleanupLocked(sc, now)
	}

	if err := sm.saveState(); err != nil {
		sm.mu.Unlock()
		return err
	}
	sm.mu.Unlock()

	if reopened {
		sm.completion.cancelScenario(id)

		for _, sessionID := range cancelSessions {
			_ = sm.stopWithReason(sessionID, StopReasonUser, "scenario-reopened")
		}
	}

	return nil
}

func (sm *SessionManager) completionTriggerLocked(sc *ScenarioState, bare string) *config.TriggerConfig {
	for _, trigger := range sc.Triggers {
		if trigger.Name == bare && trigger.IsCompletion() {
			t := trigger
			t.Name = scenarioTriggerName(sc.ID, bare)

			return &t
		}
	}

	return nil
}

func (sm *SessionManager) completionFireContextLocked(sc *ScenarioState, trigger *config.TriggerConfig) (fireContext, error) {
	fc := fireContext{
		cause:            causeScenarioComplete,
		now:              time.Now().UTC(),
		scenarioID:       sc.ID,
		scenarioName:     sc.Name,
		completionEpoch:  sc.Completion.Epoch,
		completionAction: strings.TrimPrefix(trigger.Name, scenarioTriggerName(sc.ID, "")),
	}

	member := trigger.Completion.Session
	if member == "" {
		return fc, nil
	}

	for i, ss := range sc.Sessions {
		if ss.Name != member || i >= len(sc.SessionIDs) {
			continue
		}

		sid := sc.SessionIDs[i]

		s := sm.state.Sessions[sid]
		if s == nil || s.IsSoftDeleted() || s.ScenarioID != sc.ID {
			return fireContext{}, fmt.Errorf("completion context session %q is unavailable", member)
		}

		fc.sessionID = sid
		fc.sessionName = ss.Name
		fc.worktree = s.WorktreePath

		return fc, nil
	}

	return fireContext{}, fmt.Errorf("completion context session %q not found", member)
}

func (sm *SessionManager) dispatchPendingCompletionActions(ctx context.Context, scenarioID string) {
	sm.mu.RLock()

	sc := sm.state.Scenarios[scenarioID]
	if sc == nil || !sc.Completion.Complete {
		sm.mu.RUnlock()
		return
	}

	epoch := sc.Completion.Epoch

	var names []string

	for _, action := range sc.Completion.Actions {
		if action.State == CompletionActionPending {
			names = append(names, action.Name)
		}
	}

	sm.mu.RUnlock()

	for _, name := range names {
		if rt := sm.getTriggerRuntime(scenarioTriggerName(scenarioID, name)); rt != nil && rt.Paused {
			continue
		}

		if !sm.acquireSlot() {
			return
		}

		if !sm.claimCompletionAction(scenarioID, epoch, name) {
			sm.releaseSlot()
			continue
		}

		actionCtx, cancel := context.WithCancel(ctx)
		key := completionActionKey(scenarioID, epoch, name)

		sm.completion.setCancel(key, cancel)
		go sm.runCompletionAction(actionCtx, cancel, scenarioID, epoch, name)
	}
}

func (sm *SessionManager) claimCompletionAction(scenarioID string, epoch int, name string) bool {
	now := time.Now().UTC()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sc := sm.state.Scenarios[scenarioID]
	if sc == nil || !sc.Completion.Complete || sc.Completion.Epoch != epoch {
		return false
	}

	for i := range sc.Completion.Actions {
		a := &sc.Completion.Actions[i]
		if a.Name != name || a.State != CompletionActionPending {
			continue
		}

		a.State = CompletionActionRunning
		a.Attempt++
		a.StartedAt = &now
		a.FinishedAt = nil
		a.Result = ""
		a.Error = ""

		a.SessionID = ""
		if err := sm.saveState(); err != nil {
			a.State = CompletionActionPending
			return false
		}

		return true
	}

	return false
}

func (sm *SessionManager) runCompletionAction(ctx context.Context, cancel context.CancelFunc, scenarioID string, epoch int, name string) {
	defer sm.releaseSlot()

	key := completionActionKey(scenarioID, epoch, name)

	defer func() {
		cancel()
		sm.completion.clearCancel(key)
	}()

	sm.mu.RLock()

	sc := sm.state.Scenarios[scenarioID]
	if sc == nil || sc.Completion.Epoch != epoch {
		sm.mu.RUnlock()
		return
	}

	trigger := sm.completionTriggerLocked(sc, name)
	if trigger == nil {
		sm.mu.RUnlock()
		sm.finishCompletionAction(scenarioID, epoch, name, "", errors.New("completion trigger definition is missing"))

		return
	}

	fc, err := sm.completionFireContextLocked(sc, trigger)
	sm.mu.RUnlock()

	if err != nil {
		sm.finishCompletionAction(scenarioID, epoch, name, "", err)
		return
	}

	result, actionErr := sm.fireAction(ctx, trigger, fc)
	if ctx.Err() != nil {
		// Daemon shutdown is not a user cancellation. Leave running state for
		// restart recovery rather than inventing a terminal result.
		return
	}

	if trigger.Action.Type == config.ActionSession && actionErr == nil {
		sessionID := sm.findCompletionActionSession(scenarioID, epoch, name)
		if sessionID == "" {
			actionErr = errors.New("spawned completion session could not be found")
		} else {
			sm.setCompletionActionSession(scenarioID, epoch, name, sessionID)
			// Session actions remain running. The periodic reconciler observes the
			// terminal session state, including after a daemon restart.
			return
		}
	}

	sm.finishCompletionAction(scenarioID, epoch, name, result, actionErr)
}

func (sm *SessionManager) findCompletionActionSession(scenarioID string, epoch int, action string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for id, s := range sm.state.Sessions {
		if s.CompletionScenarioID == scenarioID && s.CompletionEpoch == epoch && s.CompletionAction == action {
			return id
		}
	}

	return ""
}

func (sm *SessionManager) setCompletionActionSession(scenarioID string, epoch int, action, sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sc := sm.state.Scenarios[scenarioID]; sc != nil && sc.Completion.Epoch == epoch {
		for i := range sc.Completion.Actions {
			if sc.Completion.Actions[i].Name == action && sc.Completion.Actions[i].State == CompletionActionRunning {
				sc.Completion.Actions[i].SessionID = sessionID
				_ = sm.saveState()

				return
			}
		}
	}
}

// recoverOrFinishCompletionSessions adopts session actions and marks unknown
// non-session running states interrupted after restart. A locally-running
// command has a cancel entry and is left alone.
func (sm *SessionManager) recoverOrFinishCompletionSessions(scenarioID string) {
	type runningAction struct {
		epoch     int
		name      string
		sessionID string
		typeName  string
	}

	sm.mu.RLock()

	sc := sm.state.Scenarios[scenarioID]
	if sc == nil {
		sm.mu.RUnlock()
		return
	}

	var running []runningAction

	for _, a := range sc.Completion.Actions {
		if a.State != CompletionActionRunning {
			continue
		}

		typeName := ""
		if t := sm.completionTriggerLocked(sc, a.Name); t != nil {
			typeName = t.Action.Type
		}

		running = append(running, runningAction{sc.Completion.Epoch, a.Name, a.SessionID, typeName})
	}

	sm.mu.RUnlock()

	for _, a := range running {
		key := completionActionKey(scenarioID, a.epoch, a.name)
		if a.typeName != config.ActionSession {
			if !sm.completion.hasCancel(key) {
				sm.finishCompletionAction(scenarioID, a.epoch, a.name, "", errors.New("action interrupted by daemon restart; retry explicitly"))
			}

			continue
		}

		id := a.sessionID
		if id == "" {
			id = sm.findCompletionActionSession(scenarioID, a.epoch, a.name)
			if id != "" {
				sm.setCompletionActionSession(scenarioID, a.epoch, a.name, id)
			}
		}

		if id == "" {
			sm.finishCompletionAction(scenarioID, a.epoch, a.name, "", errors.New("action interrupted before its session was durably created; retry explicitly"))
			continue
		}

		sm.mu.RLock()

		s := sm.state.Sessions[id]
		if s == nil || s.Status == StatusRunning || s.Status == StatusCreating {
			sm.mu.RUnlock()
			continue
		}

		stopReason := s.StopReason

		exitCode := -1
		if s.ExitCode != nil {
			exitCode = *s.ExitCode
		}

		status := s.Status

		sm.mu.RUnlock()

		if status == StatusStopped && shouldAutoCleanup(config.CleanupOnSuccess, stopReason, exitCode) {
			sm.finishCompletionAction(scenarioID, a.epoch, a.name, "session "+id+" exited 0", nil)
		} else if status == StatusStopped && stopReason == StopReasonShutdown {
			lc := sm.Config().Lifecycle

			if _, err := sm.Resume(id, lc.DefaultRowsOrDefault(), lc.DefaultColsOrDefault()); err != nil {
				sm.log.Warn("scenario completion session resume after restart failed", "session", id, "err", err)
			}
		} else {
			sm.finishCompletionAction(scenarioID, a.epoch, a.name, "", fmt.Errorf("session %s stopped without successful completion (status=%s reason=%s exit=%d)", id, status, stopReason, exitCode))
		}
	}
}

func (sm *SessionManager) finishCompletionAction(scenarioID string, epoch int, name, result string, actionErr error) {
	now := time.Now().UTC()
	state := CompletionActionSucceeded
	errText := ""

	if actionErr != nil {
		state = CompletionActionFailed
		errText = actionErr.Error()
	}

	updated := false

	sm.mu.Lock()
	if sc := sm.state.Scenarios[scenarioID]; sc != nil && sc.Completion.Epoch == epoch {
		for i := range sc.Completion.Actions {
			a := &sc.Completion.Actions[i]
			if a.Name == name && a.State == CompletionActionRunning {
				a.State = state
				a.Result = result
				a.Error = errText
				a.FinishedAt = &now
				sm.evaluateScenarioCleanupLocked(sc, now)
				_ = sm.saveState()
				updated = true

				break
			}
		}
	}
	sm.mu.Unlock()

	if !updated {
		return
	}

	fullName := scenarioTriggerName(scenarioID, name)
	sm.recordTriggerRun(fullName, TriggerRun{ScheduledAt: now, Cause: causeScenarioComplete, Result: result})

	if actionErr != nil {
		sm.recordTriggerError(fullName, actionErr.Error())
	}
}

func (sm *SessionManager) evaluateScenarioCleanupLocked(sc *ScenarioState, now time.Time) {
	c := sc.Completion.Cleanup
	if c == nil || !sc.Completion.Complete || c.State == ScenarioCleanupRunning || c.State == ScenarioCleanupSucceeded || c.State == ScenarioCleanupCancelled {
		return
	}

	allTerminal := true
	allSucceeded := true

	for _, a := range sc.Completion.Actions {
		switch a.State {
		case CompletionActionSucceeded:
		case CompletionActionFailed:
			allSucceeded = false
		default:
			allTerminal = false
			allSucceeded = false
		}
	}

	if !allTerminal {
		c.State = ScenarioCleanupPending
		return
	}

	if c.Policy == config.ScenarioCleanupOnSuccess && !allSucceeded {
		c.State = ScenarioCleanupFailed
		c.Error = "blocked by failed completion action; retry the failed action"

		return
	}

	when := now.Add(sc.Lifecycle.DelayDuration())
	c.State = ScenarioCleanupScheduled
	c.ScheduledAt = &when
	c.Error = ""
}

func (sm *SessionManager) dispatchDueScenarioCleanup(ctx context.Context, scenarioID string) {
	now := time.Now().UTC()
	epoch := 0

	sm.mu.Lock()

	sc := sm.state.Scenarios[scenarioID]
	if sc != nil && sc.Completion.Complete && sc.Completion.Cleanup != nil &&
		sc.Completion.Cleanup.State == ScenarioCleanupScheduled && sc.Completion.Cleanup.ScheduledAt != nil &&
		!now.Before(*sc.Completion.Cleanup.ScheduledAt) {
		epoch = sc.Completion.Epoch
		sc.Completion.Cleanup.State = ScenarioCleanupRunning

		sc.Completion.Cleanup.Error = ""
		if err := sm.saveState(); err != nil {
			sc.Completion.Cleanup.State = ScenarioCleanupScheduled
			epoch = 0
		}
	}
	sm.mu.Unlock()

	if epoch != 0 {
		key := completionCleanupKey(scenarioID, epoch)
		cleanupCtx, cancel := context.WithCancel(ctx)
		sm.completion.setCancel(key, cancel)
		sm.completion.setCleanup(key, true)

		go func() {
			defer cancel()
			defer sm.completion.clearCancel(key)

			sm.runScenarioCleanup(cleanupCtx, scenarioID, epoch)
		}()
	}
}

func (sm *SessionManager) recoverScenarioCleanup(scenarioID string) {
	now := time.Now().UTC()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sc := sm.state.Scenarios[scenarioID]
	if sc == nil || sc.Completion.Cleanup == nil || sc.Completion.Cleanup.State != ScenarioCleanupRunning {
		return
	}

	key := completionCleanupKey(scenarioID, sc.Completion.Epoch)
	if sm.completion.hasCleanup(key) {
		return
	}

	// Cleanup is idempotent: already-soft-deleted members are skipped. Requeue a
	// crash-interrupted run immediately so a restart cannot strand it.
	sc.Completion.Cleanup.State = ScenarioCleanupScheduled
	sc.Completion.Cleanup.ScheduledAt = &now
	sc.Completion.Cleanup.Error = ""
	_ = sm.saveState()
}

func (sm *SessionManager) runScenarioCleanup(ctx context.Context, scenarioID string, epoch int) {
	defer sm.completion.setCleanup(completionCleanupKey(scenarioID, epoch), false)

	if ctx.Err() != nil {
		return
	}

	if sm.Config().Delete.RetentionDuration() <= 0 {
		sm.finishScenarioCleanup(scenarioID, epoch, "", errors.New("soft delete retention is disabled"))
		return
	}

	sm.mu.RLock()

	sc := sm.state.Scenarios[scenarioID]
	if sc == nil || !sc.Completion.Complete || sc.Completion.Epoch != epoch ||
		sc.Completion.Cleanup == nil || sc.Completion.Cleanup.State != ScenarioCleanupRunning {
		sm.mu.RUnlock()
		sm.finishScenarioCleanup(scenarioID, epoch, "", errors.New("scenario no longer has the scheduled complete epoch"))

		return
	}

	var ids []string

	for i, id := range sc.SessionIDs {
		if i < len(sc.Sessions) && !sc.Sessions[i].Shared {
			ids = append(ids, id)
		}
	}

	sm.mu.RUnlock()

	deleted := 0
	skipped := 0

	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}

		sm.mu.RLock()
		s := sm.state.Sessions[id]
		owned := s != nil && s.ScenarioID == scenarioID
		protected := s == nil || s.IsSoftDeleted() || s.Starred || IsSystemSession(s)

		sm.mu.RUnlock()

		if !owned || protected {
			skipped++
			continue
		}

		if _, err := sm.SoftDelete(id); err != nil {
			sm.finishScenarioCleanup(scenarioID, epoch, "", fmt.Errorf("soft delete %s: %w", id, err))
			return
		}

		deleted++
	}

	result := fmt.Sprintf("soft-deleted %d owned session(s); skipped %d protected or unowned", deleted, skipped)
	sm.finishScenarioCleanup(scenarioID, epoch, result, nil)
}

func (sm *SessionManager) finishScenarioCleanup(scenarioID string, epoch int, result string, cleanupErr error) {
	now := time.Now().UTC()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sc := sm.state.Scenarios[scenarioID]
	if sc == nil || sc.Completion.Epoch != epoch || sc.Completion.Cleanup == nil || sc.Completion.Cleanup.State != ScenarioCleanupRunning {
		return
	}

	c := sc.Completion.Cleanup
	c.FinishedAt = &now

	c.Result = result
	if cleanupErr != nil {
		c.State = ScenarioCleanupFailed
		c.Error = cleanupErr.Error()
	} else {
		c.State = ScenarioCleanupSucceeded
		c.Error = ""
	}

	_ = sm.saveState()
}

func (sm *SessionManager) retryScenarioCompletionAction(fullName string) error {
	scenarioID, bare, ok := parseScenarioTriggerName(fullName)
	if !ok {
		return fmt.Errorf("trigger %q is not a scenario completion trigger", fullName)
	}

	complete, err := sm.authoritativeScenarioComplete(scenarioID)
	if err != nil {
		return err
	}

	if !complete {
		return errors.New("scenario is not currently complete")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sc := sm.state.Scenarios[scenarioID]
	if sc == nil || !sc.Completion.Complete {
		return errors.New("scenario completion edge has not been reconciled yet")
	}

	if t := sm.completionTriggerLocked(sc, bare); t == nil || !t.TriggerEnabled() {
		return fmt.Errorf("completion trigger %q is unavailable", bare)
	}

	for i := range sc.Completion.Actions {
		a := &sc.Completion.Actions[i]
		if a.Name != bare {
			continue
		}

		if a.State != CompletionActionFailed {
			return fmt.Errorf("completion action %q is %s, not failed", bare, a.State)
		}

		a.State = CompletionActionPending
		a.StartedAt = nil
		a.FinishedAt = nil
		a.Result = ""
		a.Error = ""
		a.SessionID = ""

		if c := sc.Completion.Cleanup; c != nil && c.State != ScenarioCleanupSucceeded {
			c.State = ScenarioCleanupPending
			c.ScheduledAt = nil
			c.FinishedAt = nil
			c.Result = ""
			c.Error = ""
		}

		if err := sm.saveState(); err != nil {
			return err
		}

		sm.hintScenarioCompletion("scenario:" + scenarioID)

		return nil
	}

	return fmt.Errorf("completion action %q is not present in epoch %s", bare, strconv.Itoa(sc.Completion.Epoch))
}

// cancelScenarioCompletion is called for explicit human/orchestrator lifecycle
// operations. Unlike daemon shutdown it makes pending/running work terminal and
// cancels local commands before member lifecycle proceeds.
func (sm *SessionManager) cancelScenarioCompletion(scenarioID, reason string) {
	now := time.Now().UTC()

	var cancelSessions []string

	sm.mu.Lock()
	if sc := sm.state.Scenarios[scenarioID]; sc != nil {
		for i := range sc.Completion.Actions {
			a := &sc.Completion.Actions[i]
			if a.State == CompletionActionPending || a.State == CompletionActionRunning {
				if a.SessionID != "" {
					cancelSessions = append(cancelSessions, a.SessionID)
				}

				a.State = CompletionActionFailed
				a.Error = reason
				a.FinishedAt = &now
			}
		}

		if c := sc.Completion.Cleanup; c != nil && c.State != ScenarioCleanupSucceeded {
			c.State = ScenarioCleanupCancelled
			c.Error = reason
			c.FinishedAt = &now
		}

		_ = sm.saveState()
	}
	sm.mu.Unlock()

	if sm.completion != nil {
		sm.completion.cancelScenario(scenarioID)
	}

	for _, sessionID := range cancelSessions {
		_ = sm.stopWithReason(sessionID, StopReasonUser, "scenario-completion-cancel")
	}
}
