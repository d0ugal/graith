package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// scenarioWatchTrigger is a minimal role-selecting scenario trigger for tests.
func scenarioWatchTrigger(name, role string) config.TriggerConfig {
	return config.TriggerConfig{
		Name:   name,
		Watch:  &config.WatchConfig{Role: role},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "blether"}},
	}
}

func TestScenarioTriggerName_RoundTrip(t *testing.T) {
	full := scenarioTriggerName("sc-abc123", "review-go")
	if full != "scenario:sc-abc123:review-go" {
		t.Fatalf("full = %q", full)
	}

	id, bare, ok := parseScenarioTriggerName(full)
	if !ok || id != "sc-abc123" || bare != "review-go" {
		t.Fatalf("parse = %q %q %v", id, bare, ok)
	}

	// A bare name that itself contains ':' still round-trips (scenario IDs never do).
	id2, bare2, ok2 := parseScenarioTriggerName(scenarioTriggerName("sc-x", "a:b"))
	if !ok2 || id2 != "sc-x" || bare2 != "a:b" {
		t.Fatalf("colon bare = %q %q %v", id2, bare2, ok2)
	}

	// A plain config-origin name is not a scenario trigger.
	if _, _, ok3 := parseScenarioTriggerName("plain-name"); ok3 {
		t.Error("plain name should not parse as a scenario trigger")
	}
}

// addActiveScenario registers a scenario with one running, non-shared member and
// the given embedded triggers.
func addActiveScenario(t *testing.T, sm *SessionManager, scenarioID, sessionID, role string, triggers ...config.TriggerConfig) {
	t.Helper()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.state.Sessions[sessionID] = &SessionState{
		ID:           sessionID,
		Name:         "ben",
		Status:       StatusRunning,
		ScenarioID:   scenarioID,
		ScenarioRole: role,
		WorktreePath: "/wt/" + sessionID,
	}
	sm.state.Scenarios[scenarioID] = &ScenarioState{
		ID:         scenarioID,
		Name:       "strath",
		SessionIDs: []string{sessionID},
		Sessions:   []ScenarioSession{{Name: "ben", Role: role}},
		Triggers:   triggers,
	}
}

func TestAllTriggers_ScenarioActiveAndInactive(t *testing.T) {
	sm := newTriggerTestSM(t)
	addActiveScenario(t, sm, "sc-1", "s1", "implementer", scenarioWatchTrigger("review-go", "implementer"))

	want := scenarioTriggerName("sc-1", "review-go")

	has := func() bool {
		for _, tt := range sm.allTriggers() {
			if tt.Name == want {
				return true
			}
		}

		return false
	}

	if !has() {
		t.Fatal("scenario trigger not enumerated while a member is running")
	}

	// Stop the only member: the scenario is inactive, so its triggers drop out.
	sm.mu.Lock()
	sm.state.Sessions["s1"].Status = StatusStopped
	sm.mu.Unlock()

	if has() {
		t.Fatal("scenario trigger still enumerated after member stopped")
	}
}

func TestAllTriggers_CompletionRemainsAddressableWhenStopped(t *testing.T) {
	sm := newTriggerTestSM(t)
	completion := config.TriggerConfig{
		Name:       "archive",
		Completion: &config.CompletionConfig{Session: "ben"},
		Action: config.ActionConfig{
			Type: config.ActionMessage,
			Body: "archive the strath",
			Deliver: config.DeliverConfig{
				Topic: "reports",
			},
		},
	}
	addActiveScenario(t, sm, "sc-1", "s1", "implementer", completion)

	sm.mu.Lock()
	sm.state.Sessions["s1"].Status = StatusStopped
	sm.mu.Unlock()

	full := scenarioTriggerName("sc-1", "archive")
	if got := sm.triggerByName(full); got == nil || !got.IsCompletion() {
		t.Fatalf("stopped scenario completion trigger = %+v", got)
	} else if record := sm.triggerRecord(got); record.Source != "completion" {
		t.Fatalf("completion trigger source = %q", record.Source)
	}

	found := false

	for _, trigger := range sm.allTriggers() {
		if trigger.Name == full {
			found = true
			break
		}
	}

	if !found {
		t.Fatal("completion trigger disappeared after scenario member stopped")
	}
}

func TestAllTriggers_SharedOnlyMemberIsInactive(t *testing.T) {
	sm := newTriggerTestSM(t)

	sm.mu.Lock()
	sm.state.Sessions["orch"] = &SessionState{ID: "orch", Name: "orch", Status: StatusRunning}
	sm.state.Scenarios["sc-1"] = &ScenarioState{
		ID:         "sc-1",
		Name:       "strath",
		SessionIDs: []string{"orch"},
		Sessions:   []ScenarioSession{{Name: "orch", Shared: true}},
		Triggers:   []config.TriggerConfig{scenarioWatchTrigger("review-go", "implementer")},
	}
	sm.mu.Unlock()

	for _, tt := range sm.allTriggers() {
		if strings.HasPrefix(tt.Name, "scenario:") {
			t.Fatal("a scenario with only a shared running member should be inactive")
		}
	}
}

func TestTriggerByName_ScenarioResolution(t *testing.T) {
	sm := newTriggerTestSM(t)
	addActiveScenario(t, sm, "sc-1", "s1", "implementer", scenarioWatchTrigger("review-go", "implementer"))

	full := scenarioTriggerName("sc-1", "review-go")

	got := sm.triggerByName(full)
	if got == nil {
		t.Fatal("expected active scenario trigger to resolve")
	}

	if got.Name != full {
		t.Errorf("name = %q, want namespaced %q", got.Name, full)
	}

	if got.Watch == nil || got.Watch.Role != "implementer" {
		t.Errorf("watch = %+v", got.Watch)
	}

	// Inactive scenario → not resolvable.
	sm.mu.Lock()
	sm.state.Sessions["s1"].Status = StatusStopped
	sm.mu.Unlock()

	if sm.triggerByName(full) != nil {
		t.Error("stopped scenario trigger should not resolve")
	}

	// Unknown bare name → nil even while active.
	sm.mu.Lock()
	sm.state.Sessions["s1"].Status = StatusRunning
	sm.mu.Unlock()

	if sm.triggerByName(scenarioTriggerName("sc-1", "nope")) != nil {
		t.Error("unknown scenario trigger name should not resolve")
	}
}

func TestMatchingWatchSessions_ScenarioScoped(t *testing.T) {
	sm := newTriggerTestSM(t)

	sm.mu.Lock()
	sm.state.Sessions["a"] = &SessionState{ID: "a", Status: StatusRunning, ScenarioID: "sc-1", ScenarioRole: "impl", WorktreePath: "/wt/a"}
	sm.state.Sessions["b"] = &SessionState{ID: "b", Status: StatusRunning, ScenarioID: "sc-2", ScenarioRole: "impl", WorktreePath: "/wt/b"}
	sm.mu.Unlock()

	scoped := sm.matchingWatchSessions(&config.WatchConfig{Role: "impl"}, "sc-1")
	if len(scoped) != 1 || scoped[0].id != "a" {
		t.Fatalf("scoped match = %+v, want only sc-1's session", scoped)
	}

	// A config-origin role trigger (scenarioID "") still matches globally.
	global := sm.matchingWatchSessions(&config.WatchConfig{Role: "impl"}, "")
	if len(global) != 2 {
		t.Fatalf("global match = %+v, want both", global)
	}
}

func TestPruneScenarioTriggerState(t *testing.T) {
	sm := newTriggerTestSM(t)
	name := scenarioTriggerName("sc-1", "review-go")

	_ = sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) { r.RunCount = 1 })

	// A scenario record must exist for pruning to be meaningful (delete calls
	// prune after removing the record; here we exercise prune directly, so the
	// record's presence doesn't matter — the keys are what we assert on).
	sm.triggers.mu.Lock()
	sm.triggers.bindings[bindingKey(name, "s1")] = &watchBinding{triggerName: name}
	sm.triggers.nextFire[name] = time.Now()
	sm.triggers.inFlight[name] = 1
	// A schedule rate-log entry is keyed by the plain name...
	sm.triggers.rateLog[name] = []time.Time{time.Now()}
	// ...but a *watch* rate-log entry is keyed by bindingKey(name, sessionID) and
	// has no matching nextFire entry, so it must be pruned by prefix scan.
	watchRateKey := bindingKey(name, "s1")
	sm.triggers.rateLog[watchRateKey] = []time.Time{time.Now()}
	// An unrelated config trigger must survive the prune.
	sm.triggers.nextFire["keep"] = time.Now()
	sm.triggers.rateLog["keep"] = []time.Time{time.Now()}
	sm.triggers.mu.Unlock()

	sm.pruneScenarioTriggerState("sc-1")

	if sm.getTriggerRuntime(name) != nil {
		t.Error("persisted runtime not pruned")
	}

	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	if len(sm.triggers.bindings) != 0 {
		t.Errorf("bindings not torn down: %d", len(sm.triggers.bindings))
	}

	if _, ok := sm.triggers.nextFire[name]; ok {
		t.Error("nextFire not pruned")
	}

	if _, ok := sm.triggers.inFlight[name]; ok {
		t.Error("inFlight not pruned")
	}

	if _, ok := sm.triggers.rateLog[name]; ok {
		t.Error("schedule rateLog not pruned")
	}

	if _, ok := sm.triggers.rateLog[watchRateKey]; ok {
		t.Error("watch rateLog (bindingKey-shaped) not pruned")
	}

	if _, ok := sm.triggers.nextFire["keep"]; !ok {
		t.Error("unrelated config trigger nextFire wrongly pruned")
	}

	if _, ok := sm.triggers.rateLog["keep"]; !ok {
		t.Error("unrelated config trigger rateLog wrongly pruned")
	}
}

func TestScenarioTriggerOrphanedGuard(t *testing.T) {
	sm := newTriggerTestSM(t)
	name := scenarioTriggerName("sc-1", "review-go")

	// With no scenario record, a write to a scenario trigger's runtime is a no-op
	// — this is what stops a stale reconcile/in-flight action from resurrecting a
	// deleted scenario's runtime after pruneScenarioTriggerState.
	_ = sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) { r.RunCount = 5 })

	if sm.getTriggerRuntime(name) != nil {
		t.Fatal("orphan scenario trigger runtime should not be created")
	}

	sm.putTriggerRuntime(name, &TriggerRuntimeState{Name: name, RunCount: 3})

	if sm.getTriggerRuntime(name) != nil {
		t.Fatal("orphan scenario trigger runtime should not be put")
	}

	// Once the scenario exists, writes proceed normally.
	sm.mu.Lock()
	sm.state.Scenarios["sc-1"] = &ScenarioState{ID: "sc-1", Name: "strath"}
	sm.mu.Unlock()

	_ = sm.updateTriggerRuntime(name, func(r *TriggerRuntimeState) { r.RunCount = 5 })

	if rt := sm.getTriggerRuntime(name); rt == nil || rt.RunCount != 5 {
		t.Fatalf("live scenario trigger runtime = %+v", rt)
	}

	// A plain config trigger is never guarded.
	_ = sm.updateTriggerRuntime("plain", func(r *TriggerRuntimeState) { r.RunCount = 1 })
	if sm.getTriggerRuntime("plain") == nil {
		t.Error("config trigger runtime should be created")
	}
}

func TestTeardownScenarioTriggerBindings(t *testing.T) {
	sm := newTriggerTestSM(t)
	scName := scenarioTriggerName("sc-1", "review-go")

	sm.triggers.mu.Lock()
	sm.triggers.bindings[bindingKey(scName, "s1")] = &watchBinding{triggerName: scName}
	sm.triggers.bindings[bindingKey("config-trigger", "s2")] = &watchBinding{triggerName: "config-trigger"}
	sm.triggers.mu.Unlock()

	sm.teardownScenarioTriggerBindings("sc-1")

	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	if _, ok := sm.triggers.bindings[bindingKey(scName, "s1")]; ok {
		t.Error("scenario binding not torn down")
	}

	if _, ok := sm.triggers.bindings[bindingKey("config-trigger", "s2")]; !ok {
		t.Error("unrelated config binding wrongly torn down")
	}
}

func TestStartScenario_RejectsBadTrigger(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["ben-orch"] = &SessionState{
		ID:         "ben-orch",
		Name:       "ben-session",
		Status:     StatusRunning,
		SystemKind: SystemKindOrchestrator,
	}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch",
		Name:            "strath",
		Sessions:        []protocol.ScenarioSessionInput{{Name: "braw-a", Repo: "/glen", Role: "implementer"}},
		// Role "reviewer" is not defined by any session → rejected before any
		// filesystem work.
		Triggers: []config.TriggerConfig{scenarioWatchTrigger("t", "reviewer")},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "not defined by any scenario session") {
		t.Fatalf("want undefined-role error, got %v", err)
	}
}
