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
			cfg := sm.Config()
			if len(cfg.Triggers) == 0 {
				continue
			}
			sm.reconcileSchedules(cfg, now)
			for _, name := range sm.dueSchedules(now) {
				//nolint:contextcheck // fired actions may spawn/auto-resume sessions that must outlive this tick's ctx.
				go sm.fireSchedule(ctx, name, causeSchedule)
			}
		}
	}
}

// reconcileSchedules (re)parses schedule triggers, handles fingerprint resets
// and catch_up, and prunes runtime state for removed triggers. Cheap enough to
// run every tick.
func (sm *SessionManager) reconcileSchedules(cfg *config.Config, now time.Time) {
	live := make(map[string]bool)

	for i := range cfg.Triggers {
		t := &cfg.Triggers[i]
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
	sm.updateTriggerRuntime(t.Name, func(r *TriggerRuntimeState) { r.NextScheduledFireAt = &nextCopy })
}

// dueSchedules returns the names of schedule triggers due to fire now. It
// atomically advances each cursor and persists the fire commit BEFORE the
// caller dispatches (at-most-once), and applies the overlap guard.
func (sm *SessionManager) dueSchedules(now time.Time) []string {
	var due []string

	cfg := sm.Config()
	byName := make(map[string]*config.TriggerConfig, len(cfg.Triggers))
	for i := range cfg.Triggers {
		byName[cfg.Triggers[i].Name] = &cfg.Triggers[i]
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
		// overlap: skip (default) suppresses while a command run is in flight.
		if t.Action.Type == config.ActionCommand && t.Policy.OverlapMode() == config.OverlapSkip && sm.triggers.inFlight[name] {
			newNext := sm.advanceSchedule(name, t, now)
			sm.triggers.mu.Unlock()
			nn := newNext
			sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) { r.NextScheduledFireAt = &nn })
			sm.log.Info("trigger: skipped (overlap)", "trigger", name)
			continue
		}
		newNext := sm.advanceSchedule(name, t, now)
		if t.Action.Type == config.ActionCommand {
			sm.triggers.inFlight[name] = true
		}
		sm.triggers.mu.Unlock()

		// Commit the fire durably BEFORE dispatch (at-most-once), together with
		// the advanced cursor.
		fireAt, nn := next, newNext
		sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) {
			r.LastScheduledFireAt = &fireAt
			r.NextScheduledFireAt = &nn
		})
		due = append(due, name)
	}

	return due
}

// advanceSchedule moves the in-memory cursor forward and returns the new
// next-fire time. Caller holds ts.mu; persistence is the caller's job (off-lock).
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
		next = now.Add(every)
	}
	sm.triggers.nextFire[name] = next
	return next
}

// fireSchedule runs a schedule trigger's action and records the run.
func (sm *SessionManager) fireSchedule(ctx context.Context, name, cause string) {
	t := sm.triggerByName(name)
	if t == nil {
		return
	}
	defer func() {
		if t.Action.Type == config.ActionCommand {
			sm.triggers.mu.Lock()
			delete(sm.triggers.inFlight, name)
			sm.triggers.mu.Unlock()
		}
	}()

	result, err := sm.fireAction(ctx, t, fireContext{cause: cause, now: time.Now()})
	sm.recordTriggerRun(name, TriggerRun{ScheduledAt: time.Now(), Cause: cause, Result: result})
	if err != nil {
		sm.recordTriggerError(name, err.Error())
		sm.log.Warn("trigger: action failed", "trigger", name, "err", err)
	}
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
		return sm.actionMessage(t, fc)
	case config.ActionCommand:
		return sm.actionCommand(ctx, t, fc)
	case config.ActionSession:
		return sm.actionSession(t, fc)
	case config.ActionScenario:
		return sm.actionScenario(t)
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
	sm.state.TriggerRuntime[name] = rt
	if err := sm.saveState(); err != nil {
		sm.log.Warn("trigger: save state failed", "trigger", name, "err", err)
	}
}

func (sm *SessionManager) updateTriggerRuntime(name string, fn func(*TriggerRuntimeState)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	rt, ok := sm.state.TriggerRuntime[name]
	if !ok {
		rt = &TriggerRuntimeState{Name: name}
		sm.state.TriggerRuntime[name] = rt
	}
	fn(rt)
	if err := sm.saveState(); err != nil {
		sm.log.Warn("trigger: save state failed", "trigger", name, "err", err)
	}
}

func (sm *SessionManager) recordTriggerError(name, msg string) {
	sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) { r.LastError = msg })
}

func (sm *SessionManager) recordTriggerRun(name string, run TriggerRun) {
	sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) {
		r.RunCount++
		r.History = append(r.History, run)
		if len(r.History) > triggerHistoryMax {
			r.History = r.History[len(r.History)-triggerHistoryMax:]
		}
	})
}

// --- helpers ---

func (sm *SessionManager) triggerByName(name string) *config.TriggerConfig {
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
func (sm *SessionManager) deliver(d config.DeliverConfig, body, repo string, vars config.TriggerVars) {
	if d.Inbox != "" {
		target, err := config.ExpandTrigger(d.Inbox, vars)
		if err == nil {
			sm.deliverInbox(target, body, d.Wake)
		}
	}
	if d.Topic != "" {
		topic, err := config.ExpandTrigger(d.Topic, vars)
		if err == nil && sm.messages != nil {
			if _, perr := sm.messages.Publish(topic, systemSenderID, systemSenderName, body, "", ""); perr != nil {
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
func (sm *SessionManager) deliverInbox(target, body string, wake bool) {
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
		sm.notifyFromDaemon(id, body)
		return
	}
	if _, err := sm.messages.Publish("inbox:"+id, systemSenderID, systemSenderName, body, "", ""); err != nil {
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
				}
			}
		}
		sm.triggers.mu.Unlock()
	}
	if rt != nil {
		rec.Paused = rt.Paused
		rec.RunCount = rt.RunCount
		rec.LastError = rt.LastError
		if rt.LastScheduledFireAt != nil {
			rec.LastRun = rt.LastScheduledFireAt.Format(time.RFC3339)
		}
		if len(rt.History) > 0 {
			rec.LastResult = rt.History[len(rt.History)-1].Result
		}
	}
	return rec
}

// TriggerList returns records for all configured triggers.
func (sm *SessionManager) TriggerList() []protocol.TriggerRecord {
	cfg := sm.Config()
	out := make([]protocol.TriggerRecord, 0, len(cfg.Triggers))
	for i := range cfg.Triggers {
		out = append(out, sm.triggerRecord(&cfg.Triggers[i]))
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
	//nolint:contextcheck // manual fire may spawn sessions that outlive the request ctx.
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
	sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) { r.Paused = pause })
	return nil
}
