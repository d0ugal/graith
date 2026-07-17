package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func newScenarioCompletionTestSM(t *testing.T, trigger config.TriggerConfig, lifecycle config.ScenarioLifecycleConfig) (*SessionManager, TodoItem) {
	t.Helper()

	sm := newTestSessionManager(t)
	sm.todos = newTestTodoStore(t)
	worktree := t.TempDir()

	sm.mu.Lock()
	sm.state.Sessions["ben-id"] = &SessionState{
		ID: "ben-id", Name: "ben", Status: StatusRunning, ScenarioID: "sc-braw",
		ScenarioName: "braw", WorktreePath: worktree,
	}
	sm.state.Scenarios["sc-braw"] = &ScenarioState{
		ID: "sc-braw", Name: "braw", OrchestratorID: "orch", CreatedAt: time.Now(),
		SessionIDs: []string{"ben-id"},
		Sessions:   []ScenarioSession{{Name: "ben", Role: "maker"}},
		Triggers:   []config.TriggerConfig{trigger}, Lifecycle: lifecycle,
	}
	sm.mu.Unlock()

	item, err := sm.todos.Add(TodoAdd{
		Scope: "scenario:sc-braw", Title: "finish the bothy", Assignee: "ben-id", CreatedBy: "orch",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok, err := sm.todos.Claim(item.ID, "ben-id"); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	item, err = sm.todos.Transition(item.ID, TodoStatusDone, "orch", true)
	if err != nil {
		t.Fatal(err)
	}

	return sm, item
}

func completionCommandTrigger(name string) config.TriggerConfig {
	noSandbox := false

	return config.TriggerConfig{
		Name:       name,
		Completion: &config.CompletionConfig{Event: "complete", Session: "ben"},
		Action:     config.ActionConfig{Type: config.ActionCommand, Command: "true", Sandbox: &noSandbox},
	}
}

func TestScenarioCompletionEpochIdempotentAndRecompletion(t *testing.T) {
	sm, item := newScenarioCompletionTestSM(t, completionCommandTrigger("archive"), config.ScenarioLifecycleConfig{
		Cleanup: config.ScenarioCleanupOnSuccess, Delay: "1h",
	})

	if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
		t.Fatal(err)
	}

	sm.mu.RLock()

	sc := sm.state.Scenarios["sc-braw"]
	if !sc.Completion.Complete || sc.Completion.Epoch != 1 || len(sc.Completion.Actions) != 1 ||
		sc.Completion.Actions[0].State != CompletionActionPending {
		t.Fatalf("first epoch = %+v", sc.Completion)
	}

	sm.mu.RUnlock()

	// Duplicate hints/reconciles do not create another epoch or action.
	if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
		t.Fatal(err)
	}

	sm.mu.RLock()

	if got := sm.state.Scenarios["sc-braw"].Completion.Epoch; got != 1 {
		t.Fatalf("duplicate reconcile epoch = %d, want 1", got)
	}

	sm.mu.RUnlock()

	if _, err := sm.todos.Transition(item.ID, TodoStatusTodo, "orch", true); err != nil {
		t.Fatal(err)
	}

	if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
		t.Fatal(err)
	}

	sm.mu.RLock()

	if sm.state.Scenarios["sc-braw"].Completion.Complete {
		t.Fatal("reopened scenario remained complete")
	}

	sm.mu.RUnlock()

	if _, ok, err := sm.todos.Claim(item.ID, "ben-id"); err != nil || !ok {
		t.Fatalf("reclaim: ok=%v err=%v", ok, err)
	}

	if _, err := sm.todos.Transition(item.ID, TodoStatusDone, "orch", true); err != nil {
		t.Fatal(err)
	}

	if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
		t.Fatal(err)
	}

	sm.mu.RLock()

	if got := sm.state.Scenarios["sc-braw"].Completion.Epoch; got != 2 {
		t.Fatalf("recompletion epoch = %d, want 2", got)
	}

	sm.mu.RUnlock()

	loaded, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	if got := loaded.Scenarios["sc-braw"].Completion.Epoch; got != 2 {
		t.Fatalf("persisted epoch = %d, want 2", got)
	}
}

func TestScenarioCompletionActionSuccessSchedulesDelayedCleanup(t *testing.T) {
	sm, _ := newScenarioCompletionTestSM(t, completionCommandTrigger("report"), config.ScenarioLifecycleConfig{
		Cleanup: config.ScenarioCleanupOnSuccess, Delay: "1h",
	})
	if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
		t.Fatal(err)
	}

	sm.dispatchPendingCompletionActions(t.Context(), "sc-braw")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sm.mu.RLock()
		a := sm.state.Scenarios["sc-braw"].Completion.Actions[0]
		cleanup := sm.state.Scenarios["sc-braw"].Completion.Cleanup
		sm.mu.RUnlock()

		if a.State == CompletionActionSucceeded {
			if cleanup == nil || cleanup.State != ScenarioCleanupScheduled || cleanup.ScheduledAt == nil || !cleanup.ScheduledAt.After(time.Now()) {
				t.Fatalf("cleanup after success = %+v", cleanup)
			}

			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("completion action did not reach succeeded")
}

func TestScenarioCompletionRequiredDeliveryFailureAndRetry(t *testing.T) {
	trigger := config.TriggerConfig{
		Name: "report", Completion: &config.CompletionConfig{Session: "ben"},
		Action: config.ActionConfig{
			Type: config.ActionMessage, Body: "done {completion_epoch}",
			Deliver: config.DeliverConfig{Inbox: "ben", Required: true},
		},
	}

	sm, _ := newScenarioCompletionTestSM(t, trigger, config.ScenarioLifecycleConfig{Cleanup: config.ScenarioCleanupOnSuccess})
	if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
		t.Fatal(err)
	}

	sm.dispatchPendingCompletionActions(t.Context(), "sc-braw")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sm.mu.RLock()
		a := sm.state.Scenarios["sc-braw"].Completion.Actions[0]
		cleanup := sm.state.Scenarios["sc-braw"].Completion.Cleanup
		sm.mu.RUnlock()

		if a.State == CompletionActionFailed {
			if !strings.Contains(a.Error, "message store unavailable") {
				t.Fatalf("action error = %q", a.Error)
			}

			if cleanup == nil || cleanup.State != ScenarioCleanupFailed {
				t.Fatalf("on_success cleanup was not blocked: %+v", cleanup)
			}

			if err := sm.TriggerRunNow(t.Context(), "scenario:sc-braw:report"); err != nil {
				t.Fatalf("retry: %v", err)
			}

			sm.mu.RLock()
			defer sm.mu.RUnlock()

			if got := sm.state.Scenarios["sc-braw"].Completion.Actions[0].State; got != CompletionActionPending {
				t.Fatalf("retried state = %q", got)
			}

			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("required delivery did not fail the action")
}

func TestScenarioCompletionRestartTransitions(t *testing.T) {
	t.Run("running command becomes diagnosable failure", func(t *testing.T) {
		sm, _ := newScenarioCompletionTestSM(t, completionCommandTrigger("archive"), config.ScenarioLifecycleConfig{Cleanup: config.ScenarioCleanupOnSuccess})
		if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
			t.Fatal(err)
		}

		sm.mu.Lock()
		sm.state.Scenarios["sc-braw"].Completion.Actions[0].State = CompletionActionRunning
		_ = sm.saveState()
		sm.mu.Unlock()

		sm.recoverOrFinishCompletionSessions("sc-braw")

		sm.mu.RLock()
		defer sm.mu.RUnlock()

		a := sm.state.Scenarios["sc-braw"].Completion.Actions[0]
		if a.State != CompletionActionFailed || !strings.Contains(a.Error, "daemon restart") {
			t.Fatalf("recovered action = %+v", a)
		}
	})

	t.Run("running cleanup is requeued", func(t *testing.T) {
		sm, _ := newScenarioCompletionTestSM(t, completionCommandTrigger("archive"), config.ScenarioLifecycleConfig{Cleanup: config.ScenarioCleanupAlways})
		if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
			t.Fatal(err)
		}

		sm.mu.Lock()
		sm.state.Scenarios["sc-braw"].Completion.Cleanup.State = ScenarioCleanupRunning
		_ = sm.saveState()
		sm.mu.Unlock()

		sm.recoverScenarioCleanup("sc-braw")

		sm.mu.RLock()
		defer sm.mu.RUnlock()

		cleanup := sm.state.Scenarios["sc-braw"].Completion.Cleanup
		if cleanup.State != ScenarioCleanupScheduled || cleanup.ScheduledAt == nil {
			t.Fatalf("recovered cleanup = %+v", cleanup)
		}
	})

	t.Run("terminal session action is adopted", func(t *testing.T) {
		trigger := config.TriggerConfig{
			Name: "synthesise", Completion: &config.CompletionConfig{Session: "ben"},
			Action: config.ActionConfig{Type: config.ActionSession, Prompt: "synthesise then exit"},
		}

		sm, _ := newScenarioCompletionTestSM(t, trigger, config.ScenarioLifecycleConfig{Cleanup: config.ScenarioCleanupOnSuccess})
		if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
			t.Fatal(err)
		}

		exit0 := 0

		sm.mu.Lock()
		sm.state.Scenarios["sc-braw"].Completion.Actions[0].State = CompletionActionRunning
		sm.state.Scenarios["sc-braw"].Completion.Actions[0].SessionID = "synth-id"
		sm.state.Sessions["synth-id"] = &SessionState{
			ID: "synth-id", Status: StatusStopped, StopReason: StopReasonCrash, ExitCode: &exit0,
			CompletionScenarioID: "sc-braw", CompletionEpoch: 1, CompletionAction: "synthesise",
		}
		_ = sm.saveState()
		sm.mu.Unlock()

		sm.recoverOrFinishCompletionSessions("sc-braw")

		sm.mu.RLock()
		defer sm.mu.RUnlock()

		if got := sm.state.Scenarios["sc-braw"].Completion.Actions[0].State; got != CompletionActionSucceeded {
			t.Fatalf("adopted session action = %q", got)
		}
	})
}

func TestScenarioCompletionSessionCreateWindowStaysRunning(t *testing.T) {
	trigger := config.TriggerConfig{
		Name: "synthesise", Completion: &config.CompletionConfig{Session: "ben"},
		Action: config.ActionConfig{Type: config.ActionSession, Prompt: "summarise the croft"},
	}

	sm, _ := newScenarioCompletionTestSM(t, trigger, config.ScenarioLifecycleConfig{Cleanup: config.ScenarioCleanupOnSuccess})
	if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
		t.Fatal(err)
	}

	sm.mu.Lock()
	a := &sm.state.Scenarios["sc-braw"].Completion.Actions[0]
	a.State = CompletionActionRunning
	a.Attempt = 1

	if err := sm.saveState(); err != nil {
		t.Fatal(err)
	}
	sm.mu.Unlock()

	key := completionActionKey("sc-braw", 1, "synthesise", 1)
	_, cancel := context.WithCancel(t.Context())
	sm.completion.setCancel(key, cancel)
	t.Cleanup(cancel)

	// A fast second reconcile can observe running before Create has durably
	// inserted the tagged session. The live dispatch marker distinguishes that
	// window from a daemon restart and must prevent a false failure.
	sm.recoverOrFinishCompletionSessions("sc-braw")

	sm.mu.RLock()
	got := sm.state.Scenarios["sc-braw"].Completion.Actions[0].State
	sm.mu.RUnlock()

	if got != CompletionActionRunning {
		t.Fatalf("live create-window action = %q, want running", got)
	}

	// With no live dispatch marker, the same durable state is a real restart
	// interruption and remains diagnosable rather than silently replayed.
	sm.completion.clearCancel(key)
	sm.recoverOrFinishCompletionSessions("sc-braw")

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	a = &sm.state.Scenarios["sc-braw"].Completion.Actions[0]
	if a.State != CompletionActionFailed || !strings.Contains(a.Error, "durably created") {
		t.Fatalf("restart create-window action = %+v", a)
	}
}

func TestScenarioCompletionAttemptIsolation(t *testing.T) {
	trigger := config.TriggerConfig{
		Name: "synthesise", Completion: &config.CompletionConfig{Session: "ben"},
		Action: config.ActionConfig{Type: config.ActionSession, Prompt: "summarise the croft"},
	}

	sm, _ := newScenarioCompletionTestSM(t, trigger, config.ScenarioLifecycleConfig{Cleanup: config.ScenarioCleanupOnSuccess})
	if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
		t.Fatal(err)
	}

	exit0 := 0

	sm.mu.Lock()
	a := &sm.state.Scenarios["sc-braw"].Completion.Actions[0]
	a.State = CompletionActionRunning
	a.Attempt = 2
	sm.state.Sessions["stale-attempt"] = &SessionState{
		ID: "stale-attempt", Status: StatusStopped, StopReason: StopReasonCrash, ExitCode: &exit0,
		CompletionScenarioID: "sc-braw", CompletionEpoch: 1, CompletionAction: "synthesise", CompletionAttempt: 1,
	}
	sm.state.Sessions["current-attempt"] = &SessionState{
		ID: "current-attempt", Status: StatusStopped, StopReason: StopReasonCrash, ExitCode: &exit0,
		CompletionScenarioID: "sc-braw", CompletionEpoch: 1, CompletionAction: "synthesise", CompletionAttempt: 2,
	}
	sm.mu.Unlock()

	// A late completion from attempt one must not mutate attempt two.
	sm.finishCompletionAction("sc-braw", 1, "synthesise", 1, "stale", nil)
	sm.recoverOrFinishCompletionSessions("sc-braw")

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	a = &sm.state.Scenarios["sc-braw"].Completion.Actions[0]
	if a.State != CompletionActionSucceeded || a.SessionID != "current-attempt" || a.Result == "stale" {
		t.Fatalf("isolated retry attempt = %+v", a)
	}
}

func TestScenarioCompletionCancelledClaimDoesNotExecute(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	trigger := completionCommandTrigger("archive")
	trigger.Action.Command = fmt.Sprintf("touch %q", marker)

	sm, _ := newScenarioCompletionTestSM(t, trigger, config.ScenarioLifecycleConfig{Cleanup: config.ScenarioCleanupAlways})
	if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
		t.Fatal(err)
	}

	attempt, claimed := sm.claimCompletionAction("sc-braw", 1, "archive")
	if !claimed {
		t.Fatal("completion action was not claimed")
	}

	sm.cancelScenarioCompletion("sc-braw", "cancelled while dispatching")
	sm.runCompletionAction(context.Background(), func() {}, "sc-braw", 1, "archive", attempt)

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("cancelled action executed: stat err=%v", err)
	}
}

func TestScenarioCompletionRetryCleanupGuards(t *testing.T) {
	for _, cleanupState := range []string{ScenarioCleanupRunning, ScenarioCleanupSucceeded} {
		t.Run(cleanupState, func(t *testing.T) {
			sm, _ := newScenarioCompletionTestSM(t, completionCommandTrigger("archive"), config.ScenarioLifecycleConfig{Cleanup: config.ScenarioCleanupAlways})
			if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
				t.Fatal(err)
			}

			sm.mu.Lock()
			sc := sm.state.Scenarios["sc-braw"]
			sc.Completion.Actions[0].State = CompletionActionFailed
			sc.Completion.Cleanup.State = cleanupState
			sm.mu.Unlock()

			if err := sm.retryScenarioCompletionAction("scenario:sc-braw:archive"); err == nil {
				t.Fatalf("retry succeeded with cleanup %s", cleanupState)
			}

			sm.mu.RLock()
			defer sm.mu.RUnlock()

			if sc.Completion.Actions[0].State != CompletionActionFailed || sc.Completion.Cleanup.State != cleanupState {
				t.Fatalf("rejected retry mutated state: action=%s cleanup=%s", sc.Completion.Actions[0].State, sc.Completion.Cleanup.State)
			}
		})
	}

	t.Run("scheduled cleanup returns to pending", func(t *testing.T) {
		sm, _ := newScenarioCompletionTestSM(t, completionCommandTrigger("archive"), config.ScenarioLifecycleConfig{Cleanup: config.ScenarioCleanupAlways})
		if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
			t.Fatal(err)
		}

		sm.mu.Lock()
		sc := sm.state.Scenarios["sc-braw"]
		sc.Completion.Actions[0].State = CompletionActionFailed
		sc.Completion.Cleanup.State = ScenarioCleanupScheduled
		sm.mu.Unlock()

		if err := sm.retryScenarioCompletionAction("scenario:sc-braw:archive"); err != nil {
			t.Fatal(err)
		}

		sm.mu.RLock()
		defer sm.mu.RUnlock()

		if sc.Completion.Actions[0].State != CompletionActionPending || sc.Completion.Cleanup.State != ScenarioCleanupPending {
			t.Fatalf("scheduled retry state: action=%s cleanup=%s", sc.Completion.Actions[0].State, sc.Completion.Cleanup.State)
		}
	})
}

func TestScenarioCompletionEveryDurableTransitionReloads(t *testing.T) {
	states := []struct {
		name    string
		action  string
		cleanup string
	}{
		{"edge pending", CompletionActionPending, ScenarioCleanupPending},
		{"action running", CompletionActionRunning, ScenarioCleanupPending},
		{"action succeeded cleanup scheduled", CompletionActionSucceeded, ScenarioCleanupScheduled},
		{"action failed cleanup blocked", CompletionActionFailed, ScenarioCleanupFailed},
		{"cleanup running", CompletionActionSucceeded, ScenarioCleanupRunning},
		{"cleanup succeeded", CompletionActionSucceeded, ScenarioCleanupSucceeded},
	}

	for _, tc := range states {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSessionManager(t)
			now := time.Now().UTC()

			sm.mu.Lock()

			sm.state.Scenarios["sc-braw"] = &ScenarioState{
				ID: "sc-braw", Name: "braw",
				Completion: ScenarioCompletionState{
					Complete: true, Epoch: 3, TransitionedAt: &now,
					Actions: []ScenarioCompletionActionState{{Name: "archive", State: tc.action, Attempt: 1}},
					Cleanup: &ScenarioCleanupState{Policy: config.ScenarioCleanupOnSuccess, State: tc.cleanup, ScheduledAt: &now},
				},
			}
			if err := sm.saveState(); err != nil {
				t.Fatal(err)
			}
			sm.mu.Unlock()

			loaded, err := LoadState(sm.paths.StateFile)
			if err != nil {
				t.Fatal(err)
			}

			got := loaded.Scenarios["sc-braw"].Completion
			if got.Epoch != 3 || got.Actions[0].State != tc.action || got.Cleanup.State != tc.cleanup {
				t.Fatalf("reloaded transition = %+v", got)
			}
		})
	}
}

func TestScenarioManualStopCancelsCompletion(t *testing.T) {
	sm, _ := newScenarioCompletionTestSM(t, completionCommandTrigger("archive"), config.ScenarioLifecycleConfig{Cleanup: config.ScenarioCleanupAlways})
	if err := sm.reconcileScenarioCompletion("sc-braw"); err != nil {
		t.Fatal(err)
	}

	// No PTY is needed: the member is already stopped, while its completion
	// action is pending and must become a diagnosable cancellation.
	sm.mu.Lock()
	sm.state.Sessions["ben-id"].Status = StatusStopped
	sm.mu.Unlock()

	if _, err := sm.StopScenario("braw"); err != nil {
		t.Fatal(err)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sc := sm.state.Scenarios["sc-braw"]
	if sc.Completion.Actions[0].State != CompletionActionFailed ||
		!strings.Contains(sc.Completion.Actions[0].Error, "manual scenario stop") {
		t.Fatalf("cancelled action = %+v", sc.Completion.Actions[0])
	}

	if sc.Completion.Cleanup == nil || sc.Completion.Cleanup.State != ScenarioCleanupCancelled {
		t.Fatalf("cancelled cleanup = %+v", sc.Completion.Cleanup)
	}
}

func TestScenarioCleanupOwnershipProtections(t *testing.T) {
	sm := newTestSessionManager(t)
	now := time.Now().UTC()

	sm.mu.Lock()
	sm.state.Scenarios["sc-croft"] = &ScenarioState{
		ID: "sc-croft", Name: "croft", SessionIDs: []string{"owned", "shared", "starred", "replaced"},
		Sessions: []ScenarioSession{{Name: "owned"}, {Name: "shared", Shared: true}, {Name: "starred"}, {Name: "replaced"}},
		Completion: ScenarioCompletionState{
			Complete: true, Epoch: 1,
			Cleanup: &ScenarioCleanupState{Policy: config.ScenarioCleanupAlways, State: ScenarioCleanupRunning, ScheduledAt: &now},
		},
	}
	sm.state.Sessions["owned"] = &SessionState{ID: "owned", Name: "owned", Status: StatusStopped, ScenarioID: "sc-croft"}
	sm.state.Sessions["shared"] = &SessionState{ID: "shared", Name: "shared", Status: StatusStopped}
	sm.state.Sessions["starred"] = &SessionState{ID: "starred", Name: "starred", Status: StatusStopped, ScenarioID: "sc-croft", Starred: true}
	sm.state.Sessions["replaced"] = &SessionState{ID: "replaced", Name: "replaced", Status: StatusStopped, ScenarioID: "sc-other"}
	sm.state.Sessions["trigger-child"] = &SessionState{ID: "trigger-child", Name: "trigger-child", Status: StatusStopped, TriggerID: "scenario:sc-croft:archive", ParentID: "owned"}
	_ = sm.saveState()
	sm.mu.Unlock()

	sm.runScenarioCleanup(context.Background(), "sc-croft", 1)

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if !sm.state.Sessions["owned"].IsSoftDeleted() {
		t.Fatal("owned member was not soft-deleted")
	}

	for _, id := range []string{"shared", "starred", "replaced", "trigger-child"} {
		if sm.state.Sessions[id].IsSoftDeleted() {
			t.Errorf("protected/unowned session %q was soft-deleted", id)
		}
	}

	if cleanup := sm.state.Scenarios["sc-croft"].Completion.Cleanup; cleanup.State != ScenarioCleanupSucceeded {
		t.Fatalf("cleanup state = %+v", cleanup)
	}
}

func TestScenarioCleanupCancelledBeforeWorkerStarts(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID:         "sc-strath",
		SessionIDs: []string{"owned"},
		Sessions:   []ScenarioSession{{Name: "owned"}},
		Completion: ScenarioCompletionState{
			Complete: true,
			Epoch:    1,
			Cleanup: &ScenarioCleanupState{
				Policy: config.ScenarioCleanupAlways,
				State:  ScenarioCleanupCancelled,
			},
		},
	}
	sm.state.Sessions["owned"] = &SessionState{
		ID: "owned", Name: "owned", Status: StatusStopped, ScenarioID: "sc-strath",
	}
	sm.mu.Unlock()

	// A manual cancellation or reopen can win the race after the dispatcher
	// persists running but before its goroutine starts. The worker must recheck
	// the durable state rather than proceeding from the stale dispatch decision.
	sm.runScenarioCleanup(t.Context(), "sc-strath", 1)

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.state.Sessions["owned"].IsSoftDeleted() {
		t.Fatal("cancelled cleanup soft-deleted its member")
	}
}

func TestScenarioCompletionScopedInboxChoosesScenarioMember(t *testing.T) {
	sm := newTestSessionManager(t)

	store, err := NewMsgStore(filepath.Join(t.TempDir(), "messages.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	sm.messages = store

	sm.mu.Lock()
	sm.state.Sessions["outside"] = &SessionState{ID: "outside", Name: "ben", Status: StatusStopped}
	sm.state.Sessions["inside"] = &SessionState{ID: "inside", Name: "ben", Status: StatusStopped, ScenarioID: "sc-braw"}
	sm.state.Scenarios["sc-braw"] = &ScenarioState{ID: "sc-braw", SessionIDs: []string{"inside"}, Sessions: []ScenarioSession{{Name: "ben"}}}
	sm.mu.Unlock()

	if err := sm.deliverInboxScoped(t.Context(), "ben", "canny", false, "sc-braw"); err != nil {
		t.Fatal(err)
	}

	inside, err := store.Read("inbox:inside", "inside", false, "")
	if err != nil || len(inside) != 1 {
		t.Fatalf("scenario inbox messages = %v, err=%v", inside, err)
	}

	outside, err := store.Read("inbox:outside", "outside", false, "")
	if err != nil || len(outside) != 0 {
		t.Fatalf("outside inbox received scoped delivery: %v, err=%v", outside, err)
	}

	// A stale scenario index must not route to a session that has since moved to
	// another scenario.
	sm.mu.Lock()
	sm.state.Sessions["inside"].ScenarioID = "sc-croft"
	sm.mu.Unlock()

	if err := sm.deliverInboxScoped(t.Context(), "ben", "dreich", false, "sc-braw"); err == nil {
		t.Fatal("scoped delivery accepted a member now owned by another scenario")
	}

	// Shared members intentionally have no ScenarioID but remain valid scoped
	// delivery targets while present in the scenario's explicit member index.
	sm.mu.Lock()
	sm.state.Sessions["inside"].ScenarioID = ""
	sm.state.Scenarios["sc-braw"].Sessions[0].Shared = true
	sm.mu.Unlock()

	if err := sm.deliverInboxScoped(t.Context(), "ben", "thrawn", false, "sc-braw"); err != nil {
		t.Fatal(err)
	}

	inside, err = store.Read("inbox:inside", "inside", false, "")
	if err != nil || len(inside) != 2 {
		t.Fatalf("shared scenario inbox messages = %v, err=%v", inside, err)
	}
}
