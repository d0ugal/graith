package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/tools"
)

const gcxJSONFields = "metadata.name,spec.integration.id,spec.permalinks.web,spec.team.id,status.state,status.timestamps.started"

type gcxRunner func(context.Context, string, ...string) ([]byte, error)

// gcxEvent is the deliberately narrow external-event shape exposed to trigger
// templates. Raw alert text is omitted because it is untrusted prompt input.
type gcxEvent struct {
	ID            string
	Kind          string
	State         string
	URL           string
	TeamID        string
	IntegrationID string
	StartedAt     string
}

type gcxScheduleResource struct {
	ID        string `json:"metadata.name"`
	OnCallNow []struct {
		PK string `json:"pk"`
	} `json:"spec.on_call_now"`
}

type gcxAlertGroupList struct {
	Items []struct {
		ID            string `json:"metadata.name"`
		IntegrationID string `json:"spec.integration.id"`
		URL           string `json:"spec.permalinks.web"`
		TeamID        string `json:"spec.team.id"`
		State         string `json:"status.state"`
		StartedAt     string `json:"status.timestamps.started"`
	} `json:"items"`
}

// RunGCXTriggerLoop reconciles gcx source definitions and polls due bindings.
// Poll I/O always happens outside both the manager and trigger-state locks.
func (sm *SessionManager) RunGCXTriggerLoop(ctx context.Context) {
	ticker := time.NewTicker(sm.Config().TriggersRuntime.SchedulerTickDuration())
	defer ticker.Stop()

	// Reconcile immediately: a default one-minute source should not wait for an
	// unrelated scheduler tick before beginning its initial (non-firing) prime.
	now := time.Now()
	sm.reconcileGCXBindings(sm.allTriggers(), now)

	for _, name := range sm.dueGCXPolls(now) {
		go sm.pollGCXTrigger(ctx, name, runGCXCommand)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			sm.reconcileGCXBindings(sm.allTriggers(), now)

			for _, name := range sm.dueGCXPolls(now) {
				go sm.pollGCXTrigger(ctx, name, runGCXCommand)
			}
		}
	}
}

func (sm *SessionManager) reconcileGCXBindings(triggers []config.TriggerConfig, now time.Time) {
	live := make(map[string]bool)

	for i := range triggers {
		t := &triggers[i]
		if !t.IsGCX() || !t.TriggerEnabled() {
			continue
		}

		live[t.Name] = true
		fp := triggerFingerprint(t)
		rt := sm.getTriggerRuntime(t.Name)
		changed := rt == nil || rt.Fingerprint != fp

		if changed {
			nowCopy := now
			rt = &TriggerRuntimeState{
				Name:        t.Name,
				Fingerprint: fp,
				ActivatedAt: &nowCopy,
				GCXSeen:     make(map[string]time.Time),
			}
			sm.putTriggerRuntime(t.Name, rt)
		}

		sm.triggers.mu.Lock()

		binding := sm.triggers.gcxBindings[t.Name]
		if binding == nil || binding.fingerprint != fp {
			// A brand-new/changed definition always primes. On daemon restart, a
			// same-definition binding also primes unless catch_up was explicitly
			// enabled and there is a durable successful alert snapshot to resume.
			prime := changed || !t.Policy.CatchUp || rt.LastGCXPollAt == nil
			sm.triggers.gcxBindings[t.Name] = &gcxBinding{
				fingerprint: fp,
				nextPoll:    now,
				prime:       prime,
			}
		}
		sm.triggers.mu.Unlock()
	}

	var removed []string

	sm.triggers.gcxCommit.Lock()
	sm.triggers.mu.Lock()
	for name := range sm.triggers.gcxBindings {
		if !live[name] {
			delete(sm.triggers.gcxBindings, name)
			delete(sm.triggers.rateLog, name)
			removed = append(removed, name)
		}
	}
	sm.triggers.mu.Unlock()

	// Re-enabling or re-adding a source is a fresh activation, even when the
	// definition bytes are identical. Forget only its external cursor (history
	// and pause state remain) so catch_up=true cannot replay an arbitrarily old
	// gap after an intentional disable/remove.
	for _, name := range removed {
		_ = sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) {
			r.GCXSeen = nil
			r.LastGCXPollAt = nil
		})
	}
	sm.triggers.gcxCommit.Unlock()
}

func (sm *SessionManager) dueGCXPolls(now time.Time) []string {
	triggers := sm.allTriggers()
	due := make([]string, 0)

	for i := range triggers {
		t := &triggers[i]
		if !t.IsGCX() || !t.TriggerEnabled() {
			continue
		}

		rt := sm.getTriggerRuntime(t.Name)
		if rt != nil && rt.Paused {
			continue
		}

		sm.triggers.mu.Lock()

		binding := sm.triggers.gcxBindings[t.Name]
		if binding != nil && !binding.inFlight && !now.Before(binding.nextPoll) {
			binding.inFlight = true
			binding.nextPoll = now.Add(t.GCX.EveryDuration())
			due = append(due, t.Name)
		}
		sm.triggers.mu.Unlock()
	}

	sort.Strings(due)

	return due
}

func (sm *SessionManager) finishGCXPoll(name, fingerprint string) {
	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	if binding := sm.triggers.gcxBindings[name]; binding != nil && binding.fingerprint == fingerprint {
		binding.inFlight = false
	}
}

func (sm *SessionManager) pollGCXTrigger(ctx context.Context, name string, runner gcxRunner) {
	t := sm.triggerByName(name)
	if t == nil || !t.IsGCX() || !t.TriggerEnabled() {
		return
	}

	fingerprint := triggerFingerprint(t)
	defer sm.finishGCXPoll(name, fingerprint)

	onCall, err := pollGCXOnCall(ctx, t.GCX, runner)
	if err != nil {
		// We cannot prove whether a handoff happened while the gate was unreadable.
		// Fail closed now and baseline the next successful on-call snapshot.
		sm.primeGCXBinding(name, fingerprint)
		sm.recordTriggerError(name, err.Error())
		sm.log.Warn("trigger: gcx on-call poll failed", "trigger", name, "err", err)

		return
	}

	prime, current := sm.updateGCXGate(name, fingerprint, onCall)
	if !current {
		return
	}

	if !onCall {
		return
	}

	events, err := pollGCXAlertGroups(ctx, t.GCX, runner)
	if err != nil {
		sm.recordTriggerError(name, err.Error())
		sm.log.Warn("trigger: gcx alert-group poll failed", "trigger", name, "err", err)

		return
	}

	if len(events) >= t.GCX.LimitOr() {
		err := fmt.Errorf("gcx alert-group result reached limit %d; cursor not advanced (narrow filters or raise gcx.limit)", t.GCX.LimitOr())
		sm.recordTriggerError(name, err.Error())
		sm.log.Warn("trigger: incomplete gcx alert-group poll", "trigger", name, "err", err)

		return
	}

	now := time.Now()

	newEvents, seen, ok := sm.planGCXSnapshot(name, fingerprint, events, now, t.GCX.MaxAgeDuration(), prime)
	if !ok {
		return
	}

	// Reserve only the events that can actually dispatch in this poll. IDs
	// deferred by the rolling rate limit or daemon-wide concurrency cap remain
	// absent from the durable cursor so a later complete poll can retry them.
	reservedFires := 0
	fired := 0
	haveSlot := false

	if len(newEvents) > 0 {
		n, window := t.Policy.RateLimitParsed()
		reservedFires = sm.reserveRateSlots(t.Name, n, window, now, len(newEvents))

		for _, event := range newEvents[reservedFires:] {
			delete(seen, event.ID)
		}

		if reservedFires < len(newEvents) {
			sm.log.Info("trigger: gcx events deferred by rate limit", "trigger", name, "deferred", len(newEvents)-reservedFires)
		}

		newEvents = newEvents[:reservedFires]
	}

	defer func() {
		sm.releaseRateSlots(t.Name, reservedFires-fired)
	}()

	// A slow alert query can straddle a shift handoff. Re-check only when an
	// action would actually fire, keeping empty polls cheap while making the gate
	// decision as close to dispatch as practical.
	if len(newEvents) > 0 && t.GCX.OnCallUserID != "" {
		stillOnCall, err := pollGCXOnCall(ctx, t.GCX, runner)
		if err != nil {
			sm.primeGCXBinding(name, fingerprint)
			sm.recordTriggerError(name, err.Error())

			return
		}

		if !stillOnCall {
			sm.updateGCXGate(name, fingerprint, false)
			return
		}
	}

	if len(newEvents) > 0 {
		haveSlot = sm.acquireSlot()
		if !haveSlot {
			for _, event := range newEvents {
				delete(seen, event.ID)
			}

			sm.releaseRateSlots(t.Name, reservedFires)
			reservedFires = 0
			newEvents = nil

			sm.log.Info("trigger: gcx events deferred by max_concurrent", "trigger", name)
		}
	}

	if haveSlot {
		defer sm.releaseSlot()
	}

	committed, err := sm.commitGCXSnapshot(name, fingerprint, seen, now)
	if err != nil {
		sm.log.Warn("trigger: gcx cursor save failed; suppressing dispatch", "trigger", name, "err", err)
		return
	}

	if !committed {
		return
	}

	sm.triggers.mu.Lock()
	if binding := sm.triggers.gcxBindings[name]; binding != nil && binding.fingerprint == fingerprint {
		binding.prime = false
	}
	sm.triggers.mu.Unlock()

	for _, event := range newEvents {
		currentTrigger := sm.triggerByName(name)
		if currentTrigger == nil || !currentTrigger.IsGCX() || !currentTrigger.TriggerEnabled() || triggerFingerprint(currentTrigger) != fingerprint {
			return
		}

		if rt := sm.getTriggerRuntime(name); rt != nil && rt.Paused {
			return
		}

		sm.fireReservedGCXEvent(ctx, currentTrigger, event)

		fired++
	}
}

func (sm *SessionManager) primeGCXBinding(name, fingerprint string) {
	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	if binding := sm.triggers.gcxBindings[name]; binding != nil && binding.fingerprint == fingerprint {
		binding.prime = true
		binding.onCallKnown = false
	}
}

// updateGCXGate returns the binding's current prime policy. Moving off call
// marks the next on-call snapshot as a prime, so handoff backlog never fires.
func (sm *SessionManager) updateGCXGate(name, fingerprint string, onCall bool) (prime, current bool) {
	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	binding := sm.triggers.gcxBindings[name]
	if binding == nil || binding.fingerprint != fingerprint {
		return false, false
	}

	if !onCall {
		binding.onCallKnown = true
		binding.onCall = false
		binding.prime = true

		return true, true
	}

	if binding.onCallKnown && !binding.onCall {
		binding.prime = true
	}

	binding.onCallKnown = true
	binding.onCall = true

	return binding.prime, true
}

func (sm *SessionManager) planGCXSnapshot(name, fingerprint string, events []gcxEvent, now time.Time, maxAge time.Duration, prime bool) ([]gcxEvent, map[string]time.Time, bool) {
	rt := sm.getTriggerRuntime(name)
	if rt == nil || rt.Fingerprint != fingerprint {
		return nil, nil, false
	}

	seen := make(map[string]time.Time, len(rt.GCXSeen)+len(events))
	cutoff := now.Add(-maxAge)

	for id, observedAt := range rt.GCXSeen {
		if observedAt.After(cutoff) {
			seen[id] = observedAt
		}
	}

	newEvents := make([]gcxEvent, 0)

	for _, event := range events {
		if event.ID == "" {
			continue
		}

		if _, exists := seen[event.ID]; !exists && !prime {
			newEvents = append(newEvents, event)
		}

		seen[event.ID] = now
	}

	sort.Slice(newEvents, func(i, j int) bool {
		if newEvents[i].StartedAt == newEvents[j].StartedAt {
			return newEvents[i].ID < newEvents[j].ID
		}

		return newEvents[i].StartedAt < newEvents[j].StartedAt
	})

	return newEvents, seen, true
}

// commitGCXSnapshot verifies the definition and saves the complete cursor before
// any action is dispatched. It deliberately mirrors dueSchedules' at-most-once
// ordering: a failed save suppresses dispatch; a later crash may miss, not repeat.
func (sm *SessionManager) commitGCXSnapshot(name, fingerprint string, seen map[string]time.Time, now time.Time) (bool, error) {
	sm.triggers.gcxCommit.Lock()
	defer sm.triggers.gcxCommit.Unlock()

	sm.triggers.mu.Lock()
	binding := sm.triggers.gcxBindings[name]
	active := binding != nil && binding.fingerprint == fingerprint
	sm.triggers.mu.Unlock()

	if !active {
		return false, nil
	}

	current := sm.triggerByName(name)
	if current == nil || !current.IsGCX() || triggerFingerprint(current) != fingerprint {
		return false, nil
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	rt := sm.state.TriggerRuntime[name]
	if rt == nil || rt.Fingerprint != fingerprint {
		return false, nil
	}

	nowCopy := now
	rt.GCXSeen = seen
	rt.LastGCXPollAt = &nowCopy

	if err := sm.saveState(); err != nil {
		return false, err
	}

	return true, nil
}

// fireReservedGCXEvent dispatches an event after the poll has reserved both a
// rolling rate-limit entry and one daemon-wide action slot.
func (sm *SessionManager) fireReservedGCXEvent(ctx context.Context, t *config.TriggerConfig, event gcxEvent) {
	now := time.Now()
	fc := fireContext{cause: causeGCX, now: now, gcxEvent: &event, reactorSuffix: gcxReactorSuffix(event.ID)}
	result, err := sm.fireAction(ctx, t, fc)
	sm.recordTriggerRun(t.Name, TriggerRun{ScheduledAt: now, Cause: causeGCX, Result: result})

	if err != nil {
		sm.recordTriggerError(t.Name, err.Error())
		sm.log.Warn("trigger: gcx action failed", "trigger", t.Name, "event_id", event.ID, "err", err)
	}

	sm.notifyOnComplete(t, fc, err)
}

func gcxReactorSuffix(eventID string) string {
	sum := sha256.Sum256([]byte(eventID))

	return fmt.Sprintf("gcx-%x", sum[:6])
}

func pollGCXOnCall(ctx context.Context, cfg *config.GCXConfig, runner gcxRunner) (bool, error) {
	if cfg.OnCallUserID == "" {
		return true, nil
	}

	out, err := runGCXWithTimeout(ctx, cfg, runner,
		"irm", "oncall", "schedules", "list",
		"--json", "metadata.name,spec.on_call_now", "-o", "json",
	)
	if err != nil {
		return false, fmt.Errorf("list OnCall schedules: %w", err)
	}

	var schedules []gcxScheduleResource
	if err := json.Unmarshal(out, &schedules); err != nil {
		return false, fmt.Errorf("decode OnCall schedules: %w", err)
	}

	wanted := make(map[string]bool, len(cfg.ScheduleIDs))
	for _, id := range cfg.ScheduleIDs {
		wanted[id] = true
	}

	found := make(map[string]bool, len(wanted))
	onCall := false

	for _, schedule := range schedules {
		if !wanted[schedule.ID] {
			continue
		}

		found[schedule.ID] = true
		for _, user := range schedule.OnCallNow {
			if user.PK == cfg.OnCallUserID {
				onCall = true
			}
		}
	}

	if len(found) != len(wanted) {
		missing := make([]string, 0, len(wanted)-len(found))
		for id := range wanted {
			if !found[id] {
				missing = append(missing, id)
			}
		}

		sort.Strings(missing)

		return false, fmt.Errorf("configured OnCall schedules missing from gcx result: %s", strings.Join(missing, ", "))
	}

	return onCall, nil
}

func pollGCXAlertGroups(ctx context.Context, cfg *config.GCXConfig, runner gcxRunner) ([]gcxEvent, error) {
	args := []string{"irm", "oncall", "alert-groups", "list"}

	for _, state := range cfg.StatesOr() {
		args = append(args, "--state", state)
	}

	for _, teamID := range cfg.TeamIDs {
		args = append(args, "--team", teamID)
	}

	for _, integrationID := range cfg.IntegrationIDs {
		args = append(args, "--integration", integrationID)
	}

	maxAge := cfg.MaxAge
	if maxAge == "" {
		maxAge = "24h"
	}

	args = append(args,
		"--max-age", maxAge,
		"--limit", strconv.Itoa(cfg.LimitOr()),
		"--json", gcxJSONFields,
		"-o", "json",
	)

	out, err := runGCXWithTimeout(ctx, cfg, runner, args...)
	if err != nil {
		return nil, fmt.Errorf("list OnCall alert groups: %w", err)
	}

	var list gcxAlertGroupList
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("decode OnCall alert groups: %w", err)
	}

	events := make([]gcxEvent, 0, len(list.Items))
	for _, item := range list.Items {
		if item.ID == "" {
			return nil, errors.New("decode OnCall alert groups: item has empty metadata.name")
		}

		events = append(events, gcxEvent{
			ID:            item.ID,
			Kind:          config.GCXEventOnCallAlertGroup,
			State:         item.State,
			URL:           item.URL,
			TeamID:        item.TeamID,
			IntegrationID: item.IntegrationID,
			StartedAt:     item.StartedAt,
		})
	}

	return events, nil
}

func runGCXWithTimeout(ctx context.Context, cfg *config.GCXConfig, runner gcxRunner, args ...string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, cfg.TimeoutDuration())
	defer cancel()

	out, err := runner(runCtx, cfg.Context, args...)
	if err != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("timed out after %s", cfg.TimeoutDuration())
	}

	return out, err
}

func runGCXCommand(ctx context.Context, contextName string, args ...string) ([]byte, error) {
	gcxArgs := []string{"--context", contextName, "--no-color"}
	gcxArgs = append(gcxArgs, args...)

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, tools.GCX(), gcxArgs...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > 4096 {
			detail = detail[:4096] + "…"
		}

		if detail != "" {
			return nil, fmt.Errorf("gcx: %w: %s", err, detail)
		}

		return nil, fmt.Errorf("gcx: %w", err)
	}

	return stdout.Bytes(), nil
}
