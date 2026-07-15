package daemon

import (
	"strings"

	"github.com/d0ugal/graith/internal/config"
)

// scenarioTriggerPrefix namespaces a scenario-embedded trigger so two running
// instances of the same scenario file can't collide on trigger name. The full
// runtime name is "scenario:<scenarioID>:<bareName>". Config-origin trigger
// names are barred from this prefix (config.validateTriggers) so a user name
// can't be misrouted here.
const scenarioTriggerPrefix = config.ReservedTriggerNamePrefix

// scenarioTriggerName builds the namespaced runtime name for a scenario-embedded
// trigger (issue #1027).
func scenarioTriggerName(scenarioID, bare string) string {
	return scenarioTriggerPrefix + scenarioID + ":" + bare
}

// parseScenarioTriggerName splits a namespaced scenario trigger name back into
// its scenario ID and bare name. ok is false for a plain (config-origin)
// trigger name. The scenario ID never contains ':' (it is "sc-<hex>"), so a
// bare name that itself contains ':' still round-trips.
func parseScenarioTriggerName(name string) (scenarioID, bare string, ok bool) {
	rest, found := strings.CutPrefix(name, scenarioTriggerPrefix)
	if !found {
		return "", "", false
	}

	id, bareName, split := strings.Cut(rest, ":")
	if !split {
		return "", "", false
	}

	return id, bareName, true
}

// allTriggers returns every active trigger definition: the config-origin
// [[trigger]] blocks (with their bare names) followed by the scenario-embedded
// triggers of each currently-active scenario (with namespaced names). A
// scenario's triggers are only included while it has at least one running,
// non-shared member — so a rolled-back or fully-stopped scenario contributes
// none, and a resumed one contributes them again (the reconcile loops rebind
// automatically). The returned slice is a fresh copy; namespaced names are
// applied to copies so the stored ScenarioState.Triggers are never mutated.
func (sm *SessionManager) allTriggers() []config.TriggerConfig {
	cfg := sm.Config()

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	out := make([]config.TriggerConfig, 0, len(cfg.Triggers))
	out = append(out, cfg.Triggers...)

	for _, sc := range sm.state.Scenarios {
		if len(sc.Triggers) == 0 || !sm.scenarioActiveLocked(sc) {
			continue
		}

		for _, t := range sc.Triggers {
			t.Name = scenarioTriggerName(sc.ID, t.Name)
			out = append(out, t)
		}
	}

	return out
}

// scenarioActiveLocked reports whether a scenario has at least one running,
// non-shared member session. Shared sessions (e.g. the orchestrator itself) are
// excluded so a scenario whose own sessions have all stopped counts as inactive
// even while a shared member keeps running. Caller holds sm.mu (read or write).
func (sm *SessionManager) scenarioActiveLocked(sc *ScenarioState) bool {
	for i, id := range sc.SessionIDs {
		if i < len(sc.Sessions) && sc.Sessions[i].Shared {
			continue
		}

		if s, ok := sm.state.Sessions[id]; ok && s.Status == StatusRunning && !s.IsSoftDeleted() {
			return true
		}
	}

	return false
}

// scenarioTriggerByName resolves a namespaced scenario trigger to its (copied)
// definition with the namespaced name applied, or nil if the scenario is gone,
// inactive, or has no such trigger. Caller must NOT hold sm.mu.
func (sm *SessionManager) scenarioTriggerByName(scenarioID, bare, full string) *config.TriggerConfig {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sc := sm.state.Scenarios[scenarioID]
	if sc == nil || !sm.scenarioActiveLocked(sc) {
		return nil
	}

	for i := range sc.Triggers {
		if sc.Triggers[i].Name == bare {
			t := sc.Triggers[i]
			t.Name = full

			return &t
		}
	}

	return nil
}

// scenarioTriggerOrphanedLocked reports whether name is a scenario-embedded
// trigger whose owning scenario record no longer exists — i.e. a write to its
// runtime state would resurrect an entry pruneScenarioTriggerState removed.
// Config-origin names always return false. Caller holds sm.mu.
func (sm *SessionManager) scenarioTriggerOrphanedLocked(name string) bool {
	scenarioID, _, ok := parseScenarioTriggerName(name)
	if !ok {
		return false
	}

	_, exists := sm.state.Scenarios[scenarioID]

	return !exists
}

// teardownScenarioTriggerBindings tears down every watch binding belonging to a
// scenario's triggers. Used on scenario stop/delete so watchers stop promptly
// rather than waiting for the next reconcile tick.
func (sm *SessionManager) teardownScenarioTriggerBindings(scenarioID string) {
	prefix := scenarioTriggerName(scenarioID, "")

	sm.triggers.mu.Lock()

	var keys []string

	for key, b := range sm.triggers.bindings {
		if strings.HasPrefix(b.triggerName, prefix) {
			keys = append(keys, key)
		}
	}

	sm.triggers.mu.Unlock()

	for _, key := range keys {
		sm.teardownBinding(key)
	}
}

// pruneScenarioTriggerState removes all in-memory and persisted runtime state
// for a deleted scenario's triggers, so scenario churn can't leak trigger
// runtime entries. Used on scenario delete only (stop keeps the definitions for
// resume).
func (sm *SessionManager) pruneScenarioTriggerState(scenarioID string) {
	sm.teardownScenarioTriggerBindings(scenarioID)

	prefix := scenarioTriggerName(scenarioID, "")

	sm.triggers.mu.Lock()
	for name := range sm.triggers.cron {
		if strings.HasPrefix(name, prefix) {
			delete(sm.triggers.cron, name)
		}
	}

	for name := range sm.triggers.nextFire {
		if strings.HasPrefix(name, prefix) {
			delete(sm.triggers.nextFire, name)
			delete(sm.triggers.inFlight, name)
		}
	}

	// rateLog is keyed by plain name (schedule) OR bindingKey(name, sessionID)
	// (watch) — the latter never has a nextFire entry, so scan it independently
	// by prefix or the watch rate-log leaks on scenario churn. bindingKey keeps
	// the trigger name as its prefix, so HasPrefix still matches.
	for name := range sm.triggers.rateLog {
		if strings.HasPrefix(name, prefix) {
			delete(sm.triggers.rateLog, name)
		}
	}
	sm.triggers.mu.Unlock()

	sm.mu.Lock()

	changed := false

	for name := range sm.state.TriggerRuntime {
		if strings.HasPrefix(name, prefix) {
			delete(sm.state.TriggerRuntime, name)

			changed = true
		}
	}

	if changed {
		if err := sm.saveState(); err != nil {
			sm.log.Warn("scenario: prune trigger runtime save failed", "scenario", scenarioID, "err", err)
		}
	}

	sm.mu.Unlock()
}
