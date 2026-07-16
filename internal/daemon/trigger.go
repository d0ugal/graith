package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/store"
	"github.com/robfig/cron/v3"
)

const triggerTick = 1 * time.Second // coarse; cron granularity is 1 minute

// cronParser accepts 5-field expressions plus @hourly/@daily/@weekly/@monthly.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// fireCause labels a run in the history.
const (
	causeSchedule = "schedule"
	causeCatchUp  = "catch_up"
	causeManual   = "manual"
	causeFile     = "file"
)

const triggerHistoryMax = 20

// RunTriggerLoop is the daemon-owned schedule (#592) trigger loop. Modeled on
// RunPRWatchLoop: config-gated, off the request path, independent mutex.
func (sm *SessionManager) RunTriggerLoop(ctx context.Context) {
	ticker := time.NewTicker(triggerTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			// Always reconcile, even with no live triggers: reconcileSchedules is
			// what prunes an in-memory next-fire cursor when its trigger goes away
			// (e.g. a scenario stops and its schedule trigger leaves allTriggers).
			// Skipping it would strand a stale cursor and fire an overdue run on
			// resume even under catch_up=false.
			sm.reconcileSchedules(sm.allTriggers(), now)

			for _, name := range sm.dueSchedules(now) {
				go sm.fireSchedule(ctx, name, causeSchedule)
			}
		}
	}
}

// reconcileSchedules (re)parses schedule triggers, handles fingerprint resets
// and catch_up, and prunes runtime state for removed triggers. Cheap enough to
// run every tick.
func (sm *SessionManager) reconcileSchedules(triggers []config.TriggerConfig, now time.Time) {
	live := make(map[string]bool)

	for i := range triggers {
		t := &triggers[i]
		if !t.IsSchedule() || !t.TriggerEnabled() {
			continue
		}

		live[t.Name] = true
		fp := triggerFingerprint(t)

		rt := sm.getTriggerRuntime(t.Name)

		changed := rt == nil || rt.Fingerprint != fp
		if changed {
			// New or redefined: reset the persisted cursor and re-anchor.
			nowCopy := now
			newRT := &TriggerRuntimeState{Name: t.Name, Fingerprint: fp, ActivatedAt: &nowCopy}
			sm.putTriggerRuntime(t.Name, newRT)
			rt = newRT
		}

		sm.triggers.mu.Lock()
		_, haveCron := sm.triggers.cron[t.Name]
		_, haveNext := sm.triggers.nextFire[t.Name]
		sm.triggers.mu.Unlock()

		if changed || !haveCron && !haveNext {
			sm.armSchedule(t, rt, now)
		}
	}

	// Prune schedule triggers no longer present.
	sm.triggers.mu.Lock()
	for name := range sm.triggers.cron {
		if !live[name] {
			delete(sm.triggers.cron, name)
		}
	}

	for name := range sm.triggers.nextFire {
		if !live[name] {
			delete(sm.triggers.nextFire, name)
			delete(sm.triggers.inFlight, name)
			delete(sm.triggers.rateLog, name)
		}
	}
	sm.triggers.mu.Unlock()
}

// armSchedule parses the schedule and computes the initial next-fire cursor,
// honoring catch_up and any persisted NextScheduledFireAt.
func (sm *SessionManager) armSchedule(t *config.TriggerConfig, rt *TriggerRuntimeState, now time.Time) {
	sched := t.Schedule

	var next time.Time

	switch {
	case sched.Cron != "":
		loc := time.Local

		if sched.Timezone != "" {
			if l, err := time.LoadLocation(sched.Timezone); err == nil {
				loc = l
			} else {
				sm.log.Warn("trigger: bad timezone, using local", "trigger", t.Name, "tz", sched.Timezone, "err", err)
			}
		}

		cs, err := cronParser.Parse(sched.Cron)
		if err != nil {
			sm.recordTriggerError(t.Name, fmt.Sprintf("bad cron %q: %v", sched.Cron, err))
			return
		}

		sm.triggers.mu.Lock()
		sm.triggers.cron[t.Name] = cs
		sm.triggers.mu.Unlock()

		next = cs.Next(now.In(loc))
	case sched.Every != "":
		every, err := config.ParseDurationWithDays(sched.Every)
		if err != nil || every <= 0 {
			sm.recordTriggerError(t.Name, fmt.Sprintf("bad interval %q", sched.Every))
			return
		}

		anchor := now
		if rt.LastScheduledFireAt != nil {
			anchor = *rt.LastScheduledFireAt
		} else if rt.ActivatedAt != nil {
			anchor = *rt.ActivatedAt
		}

		next = anchor.Add(every)
		for !next.After(now) {
			next = next.Add(every)
		}
	}

	// catch_up: if a scheduled instant elapsed while we were down, fire once now.
	if t.Policy.CatchUp && rt.NextScheduledFireAt != nil && rt.NextScheduledFireAt.Before(now) {
		next = now
	}

	sm.triggers.mu.Lock()
	sm.triggers.nextFire[t.Name] = next
	sm.triggers.mu.Unlock()

	nextCopy := next

	_ = sm.updateTriggerRuntime(t.Name, func(r *TriggerRuntimeState) { r.NextScheduledFireAt = &nextCopy })
}

// dueSchedules returns the names of schedule triggers due to fire now. It
// atomically advances each cursor and persists the fire commit BEFORE the
// caller dispatches (at-most-once), and applies the overlap guard.
func (sm *SessionManager) dueSchedules(now time.Time) []string {
	var due []string

	triggers := sm.allTriggers()

	byName := make(map[string]*config.TriggerConfig, len(triggers))
	for i := range triggers {
		byName[triggers[i].Name] = &triggers[i]
	}

	sm.triggers.mu.Lock()

	names := make([]string, 0, len(sm.triggers.nextFire))
	for name := range sm.triggers.nextFire {
		names = append(names, name)
	}
	sm.triggers.mu.Unlock()

	for _, name := range names {
		t := byName[name]
		if t == nil || !t.TriggerEnabled() {
			continue
		}

		rt := sm.getTriggerRuntime(name)
		if rt != nil && rt.Paused {
			continue
		}

		sm.triggers.mu.Lock()

		next, ok := sm.triggers.nextFire[name]
		if !ok || now.Before(next) {
			sm.triggers.mu.Unlock()
			continue
		}
		// overlap: skip (default) suppresses while a prior run is still in flight.
		// The reservation covers ALL action types (not just command).
		if !sm.reserveFireLocked(name, t.Policy.OverlapMode()) {
			newNext := sm.advanceSchedule(name, t, now)
			sm.triggers.mu.Unlock()

			nn := newNext

			_ = sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) { r.NextScheduledFireAt = &nn })
			sm.log.Info("trigger: skipped (overlap)", "trigger", name)

			continue
		}

		newNext := sm.advanceSchedule(name, t, now)
		sm.triggers.mu.Unlock()

		// Commit the fire durably BEFORE dispatch (at-most-once), together with
		// the advanced cursor. If the save fails, do NOT dispatch — release the
		// reservation and leave the cursor advanced (a missed fire, never a
		// double-fire).
		fireAt, nn := next, newNext

		if err := sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) {
			r.LastScheduledFireAt = &fireAt
			r.NextScheduledFireAt = &nn
		}); err != nil {
			sm.releaseFire(name)
			sm.log.Warn("trigger: not dispatched, fire not durably recorded", "trigger", name, "err", err)

			continue
		}

		due = append(due, name)
	}

	return due
}

// advanceSchedule moves the in-memory cursor forward and returns the new
// next-fire time. Caller holds ts.mu; persistence is the caller's job (off-lock).
// Intervals advance from the previous scheduled instant (not from now), so a
// slow tick doesn't accumulate drift.
func (sm *SessionManager) advanceSchedule(name string, t *config.TriggerConfig, now time.Time) time.Time {
	var next time.Time

	if cs, ok := sm.triggers.cron[name]; ok {
		loc := time.Local

		if t.Schedule.Timezone != "" {
			if l, err := time.LoadLocation(t.Schedule.Timezone); err == nil {
				loc = l
			}
		}

		next = cs.Next(now.In(loc))
	} else {
		every, err := config.ParseDurationWithDays(t.Schedule.Every)
		if err != nil || every <= 0 {
			every = time.Minute
		}

		// Anchor on the previous scheduled instant so ticks don't drift; catch up
		// past now if the daemon was slow/behind.
		next = sm.triggers.nextFire[name].Add(every)
		for !next.After(now) {
			next = next.Add(every)
		}
	}

	sm.triggers.nextFire[name] = next

	return next
}

// reserveFireLocked records a fire in-flight for overlap accounting. Under the
// skip policy it returns false when a prior run is still in flight. Caller holds
// ts.mu.
func (sm *SessionManager) reserveFireLocked(name, overlap string) bool {
	if overlap == config.OverlapSkip && sm.triggers.inFlight[name] > 0 {
		return false
	}

	sm.triggers.inFlight[name]++

	return true
}

// releaseFire ends a reserved fire.
func (sm *SessionManager) releaseFire(name string) {
	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	if sm.triggers.inFlight[name] > 0 {
		sm.triggers.inFlight[name]--
	}

	if sm.triggers.inFlight[name] <= 0 {
		delete(sm.triggers.inFlight, name)
	}
}

// acquireSlot reserves one of the daemon-wide max_concurrent action slots.
func (sm *SessionManager) acquireSlot() bool {
	maxc := sm.Config().TriggersRuntime.MaxConcurrentOr()

	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	if sm.triggers.running >= maxc {
		return false
	}

	sm.triggers.running++

	return true
}

func (sm *SessionManager) releaseSlot() {
	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	if sm.triggers.running > 0 {
		sm.triggers.running--
	}
}

// fireSchedule runs a schedule trigger's action and records the run. It always
// releases the overlap reservation made by the caller (dueSchedules/TriggerRunNow)
// and gates dispatch on the concurrency cap and rate limit.
func (sm *SessionManager) fireSchedule(ctx context.Context, name, cause string) {
	defer sm.releaseFire(name)

	t := sm.triggerByName(name)
	if t == nil {
		return
	}

	n, win := t.Policy.RateLimitParsed()
	if sm.rateLimited(name, n, win, time.Now()) {
		sm.log.Info("trigger: schedule fire rate-limited", "trigger", name)
		return
	}

	if !sm.acquireSlot() {
		sm.log.Info("trigger: max_concurrent reached, skipping", "trigger", name)
		return
	}
	defer sm.releaseSlot()

	fc := fireContext{cause: cause, now: time.Now()}
	result, err := sm.fireAction(ctx, t, fc)
	sm.recordTriggerRun(name, TriggerRun{ScheduledAt: time.Now(), Cause: cause, Result: result})

	if err != nil {
		sm.recordTriggerError(name, err.Error())
		sm.log.Warn("trigger: action failed", "trigger", name, "err", err)
	}

	sm.notifyOnComplete(t, fc, err)
}

// fireContext carries per-fire data (source session, changed files) to the
// action executor and template expander.
type fireContext struct {
	cause        string
	now          time.Time
	sessionID    string   // watch: the bound source session
	sessionName  string   // watch
	worktree     string   // watch
	changedFiles []string // watch
}

// fireAction dispatches to the per-type executor and returns a result summary.
func (sm *SessionManager) fireAction(ctx context.Context, t *config.TriggerConfig, fc fireContext) (string, error) {
	switch t.Action.Type {
	case config.ActionMessage:
		return sm.actionMessage(ctx, t, fc)
	case config.ActionCommand:
		return sm.actionCommand(ctx, t, fc)
	case config.ActionSession:
		return sm.actionSession(ctx, t, fc)
	case config.ActionScenario:
		return sm.actionScenario(ctx, t)
	case config.ActionTracker:
		return sm.actionTracker(ctx, t, fc)
	default:
		return "", fmt.Errorf("unknown action type %q", t.Action.Type)
	}
}

// --- template expansion ---

func (sm *SessionManager) triggerVars(t *config.TriggerConfig, fc fireContext) config.TriggerVars {
	changed := strings.Join(fc.changedFiles, ", ")

	return config.TriggerVars{
		Name:         t.Name,
		Date:         fc.now.Format("2006-01-02"),
		Datetime:     fc.now.Format(time.RFC3339),
		FireTime:     fc.now.Format(time.RFC3339),
		SessionName:  fc.sessionName,
		WorktreePath: fc.worktree,
		ChangedFiles: changed,
		ChangeCount:  fmt.Sprintf("%d", len(fc.changedFiles)),
	}
}

// --- run-state persistence (under sm.mu) ---

func (sm *SessionManager) getTriggerRuntime(name string) *TriggerRuntimeState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	rt, ok := sm.state.TriggerRuntime[name]
	if !ok {
		return nil
	}

	cp := *rt

	return &cp
}

func (sm *SessionManager) putTriggerRuntime(name string, rt *TriggerRuntimeState) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Don't resurrect runtime for a scenario trigger whose scenario is gone: a
	// stale reconcile snapshot or an in-flight action could otherwise re-persist
	// a scenario:<deleted-id>:... entry that pruneScenarioTriggerState just removed.
	if sm.scenarioTriggerOrphanedLocked(name) {
		return
	}

	sm.state.TriggerRuntime[name] = rt
	if err := sm.saveState(); err != nil {
		sm.log.Warn("trigger: save state failed", "trigger", name, "err", err)
	}
}

// updateTriggerRuntime applies fn and persists, returning any save error so a
// caller that depends on durability (the at-most-once fire commit) can refuse to
// dispatch when the fire was not durably recorded.
func (sm *SessionManager) updateTriggerRuntime(name string, fn func(*TriggerRuntimeState)) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// A scenario trigger whose scenario has been deleted must not resurrect its
	// runtime entry (see putTriggerRuntime).
	if sm.scenarioTriggerOrphanedLocked(name) {
		return nil
	}

	rt, ok := sm.state.TriggerRuntime[name]
	if !ok {
		rt = &TriggerRuntimeState{Name: name}
		sm.state.TriggerRuntime[name] = rt
	}

	fn(rt)

	if err := sm.saveState(); err != nil {
		sm.log.Warn("trigger: save state failed", "trigger", name, "err", err)
		return err
	}

	return nil
}

func (sm *SessionManager) recordTriggerError(name, msg string) {
	_ = sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) { r.LastError = msg })
}

func (sm *SessionManager) recordTriggerRun(name string, run TriggerRun) {
	_ = sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) {
		r.RunCount++

		r.History = append(r.History, run)
		if len(r.History) > triggerHistoryMax {
			r.History = r.History[len(r.History)-triggerHistoryMax:]
		}
	})
}

// --- helpers ---

func (sm *SessionManager) triggerByName(name string) *config.TriggerConfig {
	// Scenario-embedded triggers carry a namespaced name and live on the owning
	// ScenarioState, not in config.
	if scenarioID, bare, ok := parseScenarioTriggerName(name); ok {
		return sm.scenarioTriggerByName(scenarioID, bare, name)
	}

	cfg := sm.Config()
	for i := range cfg.Triggers {
		if cfg.Triggers[i].Name == name {
			t := cfg.Triggers[i]
			return &t
		}
	}

	return nil
}

// triggerFingerprint is a canonical hash of the fire-affecting definition.
func triggerFingerprint(t *config.TriggerConfig) string {
	payload := struct {
		Schedule *config.ScheduleConfig
		Watch    *config.WatchConfig
		Action   config.ActionConfig
		Policy   config.TriggerPolicy
	}{t.Schedule, t.Watch, t.Action, t.Policy}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:8])
}

// rateLimited reports whether the key has exceeded its rolling rate limit, and
// records the fire if not. Caller must NOT hold ts.mu.
func (sm *SessionManager) rateLimited(key string, n int, window time.Duration, now time.Time) bool {
	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	cutoff := now.Add(-window)

	var recent []time.Time

	for _, ts := range sm.triggers.rateLog[key] {
		if ts.After(cutoff) {
			recent = append(recent, ts)
		}
	}

	if len(recent) >= n {
		sm.triggers.rateLog[key] = recent
		return true
	}

	sm.triggers.rateLog[key] = append(recent, now)

	return false
}

// deliverStorePath resolves the store dir + key for a deliver.store value. A
// "shared:" prefix (or an action with no repo) targets the shared store.
func (sm *SessionManager) deliverStorePath(key, repo string) (dir, cleanKey string) {
	if strings.HasPrefix(key, "shared:") {
		return store.SharedStorePath(sm.paths.DataDir), strings.TrimPrefix(key, "shared:")
	}

	if repo == "" {
		return store.SharedStorePath(sm.paths.DataDir), key
	}

	return store.StorePath(sm.paths.DataDir, repo), key
}

// deliver routes body per the deliver block. repo scopes a repo-store key.
func (sm *SessionManager) deliver(ctx context.Context, d config.DeliverConfig, body, repo string, vars config.TriggerVars) {
	if d.Inbox != "" {
		target, err := config.ExpandTrigger(d.Inbox, vars)
		if err == nil {
			sm.deliverInbox(ctx, target, body, d.Wake)
		}
	}

	if d.Topic != "" {
		topic, err := config.ExpandTrigger(d.Topic, vars)
		if err == nil && sm.messages != nil {
			if _, perr := sm.messages.Publish(PublishOpts{Stream: topic, SenderID: systemSenderID, SenderName: systemSenderName, Body: body}); perr != nil {
				sm.log.Warn("trigger: topic publish failed", "topic", topic, "err", perr)
			}
		}
	}

	if d.Store != "" {
		key, err := config.ExpandTrigger(d.Store, vars)
		if err == nil {
			dir, cleanKey := sm.deliverStorePath(key, repo)
			if ierr := store.Init(dir); ierr == nil {
				if perr := store.Put(dir, cleanKey, body); perr != nil {
					sm.log.Warn("trigger: store put failed", "key", cleanKey, "err", perr)
				}
			}
		}
	}
}

// deliverInbox delivers to a session inbox, resolving "orchestrator", gating
// resume by wake, and never waking a soft-deleted session.
func (sm *SessionManager) deliverInbox(ctx context.Context, target, body string, wake bool) {
	_ = ctx // reserved for future cancellation; notifyFromDaemon detaches by design

	sm.mu.RLock()

	var id string

	orchestrator := target == "orchestrator"
	if orchestrator {
		id = sm.findOrchestratorID()
	} else {
		for sid, s := range sm.state.Sessions {
			if s.Name == target {
				id = sid
				break
			}
		}
	}

	var softDeleted bool
	if s, ok := sm.state.Sessions[id]; ok {
		softDeleted = s.IsSoftDeleted()
	}

	sm.mu.RUnlock()

	if id == "" || softDeleted || sm.messages == nil {
		return
	}

	// notifyFromDaemon publishes AND auto-resumes; a bare Publish does not.
	if orchestrator || wake {
		//nolint:contextcheck // notifyFromDaemon detaches its auto-resume; it must outlive this call.
		_ = sm.notifyFromDaemon(id, body)
		return
	}

	if _, err := sm.messages.Publish(PublishOpts{Stream: "inbox:" + id, SenderID: systemSenderID, SenderName: systemSenderName, Body: body}); err != nil {
		sm.log.Warn("trigger: inbox publish failed", "session", id, "err", err)
	}
}

// --- status / control API (used by handler & CLI) ---

func (sm *SessionManager) triggerRecord(t *config.TriggerConfig) protocol.TriggerRecord {
	rt := sm.getTriggerRuntime(t.Name)

	rec := protocol.TriggerRecord{
		Name:    t.Name,
		Action:  t.Action.Type,
		Enabled: t.TriggerEnabled(),
	}
	if t.IsSchedule() {
		rec.Source = "schedule"
		if t.Schedule.Cron != "" {
			rec.Schedule = t.Schedule.Cron
		} else {
			rec.Schedule = "every " + t.Schedule.Every
		}

		sm.triggers.mu.Lock()
		if nf, ok := sm.triggers.nextFire[t.Name]; ok {
			rec.NextFire = nf.Format(time.RFC3339)
		}
		sm.triggers.mu.Unlock()
	} else {
		rec.Source = "watch"
		if t.Watch.Repo != "" {
			rec.WatchScope = "repo:" + t.Watch.Repo
		} else {
			rec.WatchScope = "role:" + t.Watch.Role
		}

		sm.triggers.mu.Lock()
		for _, b := range sm.triggers.bindings {
			if b.triggerName == t.Name {
				rec.Bindings++
				if b.degraded != "" {
					rec.Degraded = b.degraded
					rec.DegradedRetryCount = b.retryCount

					if !b.nextRetryAt.IsZero() {
						rec.DegradedRetryAt = b.nextRetryAt.Format(time.RFC3339)
					}
				}
			}
		}
		sm.triggers.mu.Unlock()
	}

	if rt != nil {
		rec.Paused = rt.Paused
		rec.RunCount = rt.RunCount

		rec.LastError = rt.LastError

		if len(rt.History) > 0 {
			last := rt.History[len(rt.History)-1]
			rec.LastResult = last.Result
			// Use the actual last dispatch time — watch/manual runs never populate
			// LastScheduledFireAt, so fall back to the history entry's timestamp.
			rec.LastRun = last.ScheduledAt.Format(time.RFC3339)
		} else if rt.LastScheduledFireAt != nil {
			rec.LastRun = rt.LastScheduledFireAt.Format(time.RFC3339)
		}
	}

	return rec
}

// TriggerList returns records for all configured triggers.
func (sm *SessionManager) TriggerList() []protocol.TriggerRecord {
	triggers := sm.allTriggers()

	out := make([]protocol.TriggerRecord, 0, len(triggers))
	for i := range triggers {
		out = append(out, sm.triggerRecord(&triggers[i]))
	}

	return out
}

// TriggerStatus returns one trigger's record, or an error if unknown.
func (sm *SessionManager) TriggerStatus(name string) (protocol.TriggerRecord, error) {
	t := sm.triggerByName(name)
	if t == nil {
		return protocol.TriggerRecord{}, fmt.Errorf("trigger %q not found", name)
	}

	return sm.triggerRecord(t), nil
}

// TriggerRunNow fires a trigger out-of-band (cause "manual"), respecting the
// overlap guard but not shifting the schedule cursor.
func (sm *SessionManager) TriggerRunNow(ctx context.Context, name string) error {
	t := sm.triggerByName(name)
	if t == nil {
		return fmt.Errorf("trigger %q not found", name)
	}

	if t.IsWatch() {
		return fmt.Errorf("trigger %q is a watch trigger; it fires on file changes, not on demand", name)
	}

	// Respect overlap: reserve through the same guard the scheduled path uses so a
	// manual run and a scheduled run can't both run under overlap=skip. fireSchedule
	// releases the reservation.
	sm.triggers.mu.Lock()
	reserved := sm.reserveFireLocked(name, t.Policy.OverlapMode())
	sm.triggers.mu.Unlock()

	if !reserved {
		return fmt.Errorf("trigger %q is already running (overlap=skip)", name)
	}

	go sm.fireSchedule(context.WithoutCancel(ctx), name, causeManual)

	return nil
}

// TriggerPause pauses/resumes a trigger. A config-disabled trigger cannot be
// resumed.
func (sm *SessionManager) TriggerPause(name string, pause bool) error {
	t := sm.triggerByName(name)
	if t == nil {
		return fmt.Errorf("trigger %q not found", name)
	}

	if !pause && !t.TriggerEnabled() {
		return fmt.Errorf("trigger %q is disabled in config; cannot resume", name)
	}

	return sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) { r.Paused = pause })
}
