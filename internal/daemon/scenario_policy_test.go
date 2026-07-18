package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/scenariofile"
)

func newScenarioPolicyTestManager(t *testing.T, now time.Time, members ...ScenarioSession) *SessionManager {
	t.Helper()

	sm := newTestSessionManager(t)
	sm.todos = newTestTodoStore(t)
	sm.scenarioPolicyNow = func() time.Time { return now }

	ids := make([]string, len(members))
	for i := range members {
		ids[i] = members[i].Name + "-id"
		sm.state.Sessions[ids[i]] = &SessionState{
			ID: ids[i], Name: members[i].Name, Status: StatusRunning,
			LaunchGeneration: 1, ScenarioID: "sc-strath",
		}
	}

	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID: "sc-strath", Name: "strath", SessionIDs: ids, Sessions: members, CreatedAt: now.Add(-time.Hour),
		Policy: &ScenarioPolicyState{Completion: scenariofile.CompletionAll, OnExhausted: scenariofile.OnExhaustedWait, Active: true},
	}

	return sm
}

func policyMember(name string, required bool, timeout time.Duration, retries int, now time.Time) ScenarioSession {
	deadline := now.Add(timeout)

	return ScenarioSession{
		Name: name,
		Policy: &ScenarioMemberPolicyState{
			Required: required, TimeoutNanos: int64(timeout), Retries: retries, Attempt: 1,
			AttemptStartedAt: timePtr(now), Deadline: &deadline,
		},
	}
}

func addScenarioResult(t *testing.T, sm *SessionManager, sessionID string, done bool) {
	t.Helper()

	item, err := sm.todos.Add(TodoAdd{
		Scope: "scenario:sc-strath", Title: "braw result", Assignee: sessionID, CreatedBy: "scenario:sc-strath",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !done {
		return
	}

	if _, ok, err := sm.todos.Claim(item.ID, sessionID, false); err != nil || !ok {
		t.Fatalf("claim result: ok=%v err=%v", ok, err)
	}

	if _, err := sm.todos.Transition(item.ID, TodoStatusDone, sessionID, false); err != nil {
		t.Fatal(err)
	}
}

func TestActivateScenarioPolicyFreezesInitialDeadlines(t *testing.T) {
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	sc := &ScenarioState{
		Policy: &ScenarioPolicyState{Completion: scenariofile.CompletionAll, OnExhausted: scenariofile.OnExhaustedWait},
		Sessions: []ScenarioSession{
			{Name: "braw", Policy: &ScenarioMemberPolicyState{Required: true, TimeoutNanos: int64(30 * time.Second), Retries: 1}},
			{Name: "canny", Policy: &ScenarioMemberPolicyState{Required: false}},
		},
	}

	activateScenarioPolicy(sc, now)

	if !sc.Policy.Active || sc.Sessions[0].Policy.Attempt != 1 || sc.Sessions[1].Policy.Attempt != 1 {
		t.Fatalf("activation = %+v members=%+v", sc.Policy, sc.Sessions)
	}

	want := now.Add(30 * time.Second)
	if got := sc.Sessions[0].Policy.Deadline; got == nil || !got.Equal(want) {
		t.Fatalf("deadline = %v, want %v", got, want)
	}

	activateScenarioPolicy(sc, now.Add(time.Hour))

	if got := sc.Sessions[0].Policy.Deadline; got == nil || !got.Equal(want) {
		t.Fatalf("second activation moved immutable deadline to %v", got)
	}
}

func TestScenarioPolicyRequiresTodosAndRequiredResults(t *testing.T) {
	now := time.Date(2026, 7, 17, 9, 30, 0, 0, time.UTC)
	member := policyMember("braw", true, time.Hour, 0, now)
	member.Results = []ScenarioResultState{
		{Name: "review", Required: true, Status: ScenarioResultPending},
		{Name: "notes", Required: false, Status: ScenarioResultPending},
	}
	sm := newScenarioPolicyTestManager(t, now, member)
	addScenarioResult(t, sm, "braw-id", true)

	sm.reconcileScenarioPolicies(context.Background(), now)
	policy := sm.state.Scenarios["sc-strath"].Sessions[0].Policy

	if policy.SucceededAt != nil {
		t.Fatal("completed todo bypassed the required result contract")
	}

	sm.state.Scenarios["sc-strath"].Sessions[0].Results[0].Status = ScenarioResultAvailable
	sm.reconcileScenarioPolicies(context.Background(), now.Add(time.Second))

	if policy.SucceededAt == nil || sm.state.Scenarios["sc-strath"].Policy.Outcome != scenarioOutcomeComplete {
		t.Fatalf("required result did not complete member: policy=%+v", policy)
	}
}

func TestScenarioPolicyRequiredResultOnlyAndOptionalResult(t *testing.T) {
	now := time.Date(2026, 7, 17, 9, 45, 0, 0, time.UTC)
	member := policyMember("canny", true, time.Hour, 0, now)
	member.Results = []ScenarioResultState{
		{Name: "facts", Required: true, Status: ScenarioResultAvailable},
		{Name: "appendix", Required: false, Status: ScenarioResultPending},
	}
	sm := newScenarioPolicyTestManager(t, now, member)

	sm.reconcileScenarioPolicies(context.Background(), now)

	if got := sm.state.Scenarios["sc-strath"].Sessions[0].Policy.SucceededAt; got == nil {
		t.Fatal("required-result-only member did not succeed")
	}
}

func TestScenarioPolicyTimeoutRetriesExactlyOnce(t *testing.T) {
	started := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))
	addScenarioResult(t, sm, "braw-id", false)

	restarts := 0
	sm.scenarioRestart = func(id string, _, _ uint16) error {
		restarts++

		sm.mu.Lock()
		sm.state.Sessions[id].LaunchGeneration++
		sm.mu.Unlock()

		return nil
	}

	sm.reconcileScenarioPolicies(context.Background(), now)

	member := sm.state.Scenarios["sc-strath"].Sessions[0].Policy
	if restarts != 1 || member.Attempt != 2 || member.RetryPending {
		t.Fatalf("restarts=%d policy=%+v", restarts, member)
	}

	wantDeadline := now.Add(time.Minute)
	if member.Deadline == nil || !member.Deadline.Equal(wantDeadline) {
		t.Fatalf("deadline = %v, want immutable %v", member.Deadline, wantDeadline)
	}

	sm.reconcileScenarioPolicies(context.Background(), now.Add(30*time.Second))

	if restarts != 1 {
		t.Fatalf("duplicate retry before deadline: restarts=%d", restarts)
	}

	if !member.Deadline.Equal(wantDeadline) {
		t.Fatalf("activity/reconcile extended deadline to %v", member.Deadline)
	}

	sm.reconcileScenarioPolicies(context.Background(), wantDeadline)

	if restarts != 1 || member.ExhaustedAt == nil {
		t.Fatalf("bounded retry budget not exhausted: restarts=%d policy=%+v", restarts, member)
	}
}

func TestScenarioPolicyDoesNotLaunchWithoutDurableClaim(t *testing.T) {
	started := time.Date(2026, 7, 17, 10, 30, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))

	restarts := 0
	sm.scenarioRestart = func(string, uint16, uint16) error { restarts++; return nil }
	sm.saveStateFault = func() error { return errors.New("dreich disk") }
	sm.reconcileScenarioPolicies(context.Background(), now)

	member := sm.state.Scenarios["sc-strath"].Sessions[0].Policy
	if restarts != 0 || member.Attempt != 1 || member.RetryPending {
		t.Fatalf("non-durable retry launched or mutated state: restarts=%d policy=%+v", restarts, member)
	}

	sm.saveStateFault = nil
	sm.scenarioRestart = func(id string, _, _ uint16) error {
		restarts++

		sm.mu.Lock()
		sm.state.Sessions[id].LaunchGeneration++
		sm.mu.Unlock()

		return nil
	}
	later := now.Add(time.Second)
	sm.reconcileScenarioPolicies(context.Background(), later)

	member = sm.state.Scenarios["sc-strath"].Sessions[0].Policy

	if restarts != 1 || member.Attempt != 2 || member.AttemptStartedAt == nil || !member.AttemptStartedAt.Equal(later) {
		t.Fatalf("durable retry = restarts %d policy %+v", restarts, member)
	}
}

func TestScenarioPolicyRetriesResultPersistenceWithoutReplayingAction(t *testing.T) {
	started := time.Date(2026, 7, 17, 10, 40, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))

	restarts := 0
	sm.scenarioRestart = func(string, uint16, uint16) error {
		restarts++

		return errors.New("dreich launch")
	}

	saves := 0
	sm.saveStateFault = func() error {
		saves++
		if saves == 3 {
			return errors.New("dreich disk")
		}

		return nil
	}

	sm.reconcileScenarioPolicies(context.Background(), now)

	member := sm.state.Scenarios["sc-strath"].Sessions[0].Policy

	if restarts != 1 || member.ExhaustedAt == nil || !sm.scenarioPolicyDirty["sc-strath"] {
		t.Fatalf("first reconcile = restarts %d dirty %v policy %+v", restarts, sm.scenarioPolicyDirty, member)
	}

	sm.saveStateFault = nil
	sm.reconcileScenarioPolicies(context.Background(), now.Add(time.Second))

	if restarts != 1 || sm.scenarioPolicyDirty["sc-strath"] {
		t.Fatalf("dirty flush replayed action: restarts=%d dirty=%v", restarts, sm.scenarioPolicyDirty)
	}

	loaded, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	if got := loaded.Scenarios["sc-strath"].Sessions[0].Policy; got.ExhaustedAt == nil || got.ExhaustionReason == "" {
		t.Fatalf("retry result was not persisted: %+v", got)
	}
}

func TestScenarioPolicyFailedDispatchCrashDoesNotReplayAttempt(t *testing.T) {
	started := time.Date(2026, 7, 17, 10, 50, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))

	restarts := 0
	sm.scenarioRestart = func(string, uint16, uint16) error {
		restarts++

		return errors.New("dreich launch")
	}

	saves := 0
	sm.saveStateFault = func() error {
		saves++
		if saves == 3 {
			return errors.New("dreich disk")
		}

		return nil
	}
	sm.reconcileScenarioPolicies(context.Background(), now)

	if restarts != 1 {
		t.Fatalf("initial retry dispatches = %d, want 1", restarts)
	}

	loaded, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	durable := loaded.Scenarios["sc-strath"].Sessions[0].Policy
	if !durable.RetryPending || !durable.RetryDispatched {
		t.Fatalf("durable dispatch boundary = %+v", durable)
	}

	restarted := newTestSessionManager(t)
	restarted.todos = newTestTodoStore(t)
	restarted.state = loaded
	replayed := 0
	restarted.scenarioRestart = func(string, uint16, uint16) error { replayed++; return nil }
	restarted.reconcileScenarioPolicies(context.Background(), now.Add(time.Second))

	member := restarted.state.Scenarios["sc-strath"].Sessions[0].Policy
	if replayed != 0 || member.ExhaustedAt == nil || !strings.Contains(member.ExhaustionReason, "interrupted") {
		t.Fatalf("crash recovery replayed=%d policy=%+v", replayed, member)
	}
}

func TestScenarioPolicyTodoReadFailureDoesNotTimeout(t *testing.T) {
	started := time.Date(2026, 7, 17, 10, 45, 0, 0, time.UTC)
	now := started.Add(time.Minute)

	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))

	if err := sm.todos.Close(); err != nil {
		t.Fatal(err)
	}

	restarts := 0
	sm.scenarioRestart = func(string, uint16, uint16) error { restarts++; return nil }
	sm.reconcileScenarioPolicies(context.Background(), now)

	member := sm.state.Scenarios["sc-strath"].Sessions[0].Policy
	if restarts != 0 || member.Attempt != 1 {
		t.Fatalf("todo read failure caused timeout action: restarts=%d policy=%+v", restarts, member)
	}
}

func TestScenarioPolicyRestartRecoveryUsesLaunchGeneration(t *testing.T) {
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
	sm := newScenarioPolicyTestManager(t, now, policyMember("canny", true, time.Minute, 1, now.Add(-time.Minute)))
	policy := sm.state.Scenarios["sc-strath"].Sessions[0].Policy
	policy.Attempt = 2
	policy.RetryPending = true
	policy.RetryFromGeneration = 4
	sm.state.Sessions["canny-id"].LaunchGeneration = 5

	if err := SaveState(sm.paths.StateFile, sm.state); err != nil {
		t.Fatal(err)
	}

	reloaded := newTestSessionManager(t)
	reloaded.paths.StateFile = sm.paths.StateFile

	state, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	reloaded.state = state

	restarts := 0
	reloaded.scenarioRestart = func(string, uint16, uint16) error { restarts++; return nil }
	reloaded.reconcileScenarioPolicies(context.Background(), now)

	recoveredPolicy := reloaded.state.Scenarios["sc-strath"].Sessions[0].Policy
	if restarts != 0 || recoveredPolicy.RetryPending {
		t.Fatalf("completed pre-restart launch retried: restarts=%d policy=%+v", restarts, recoveredPolicy)
	}
}

func TestScenarioPolicyRetryUsesLaunchThrottleAndAdvancesGeneration(t *testing.T) {
	repo := initTempGitRepo(t)
	dir := t.TempDir()

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Launch.MaxConcurrent = 1
	cfg.Launch.SettleTimeout = "0"
	cfg.Agents["echo"] = config.Agent{
		NonInteractiveArgs: []string{},
		Command:            "sh",
		Args:               []string{"-c", "exec cat"},
		ResumeArgs:         []string{"-c", "exec cat"},
	}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		DataDir:    dir,
		LogDir:     dir,
		RuntimeDir: dir,
		TmpDir:     filepath.Join(dir, "tmp"),
	}, quietLogger())
	sm.sandboxResolver = func(string) (bool, error) { return false, nil }
	sm.todos = newTestTodoStore(t)

	created, err := sm.Create(CreateOpts{
		Name: "braw", AgentName: "echo", RepoPath: repo, BaseBranch: "main", Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

	now := time.Date(2026, 7, 17, 11, 15, 0, 0, time.UTC)
	started := now.Add(-time.Minute)

	sm.mu.Lock()
	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID: "sc-strath", Name: "strath", SessionIDs: []string{created.ID},
		Sessions: []ScenarioSession{policyMember("braw", true, time.Minute, 1, started)},
		Policy: &ScenarioPolicyState{
			Completion: scenariofile.CompletionAll, OnExhausted: scenariofile.OnExhaustedWait, Active: true,
		},
	}

	if err := sm.saveState(); err != nil {
		sm.mu.Unlock()
		t.Fatal(err)
	}
	sm.mu.Unlock()

	// Occupy the only launch slot. The retry may claim the next attempt and
	// stop the old process, but it must not spawn a replacement or advance the
	// generation until the existing launch-concurrency control admits it.
	held, err := sm.launch.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	released := false

	t.Cleanup(func() {
		if !released {
			held.release()
		}
	})

	done := make(chan struct{})

	go func() {
		sm.reconcileScenarioPolicies(context.Background(), now)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)

	for {
		sm.launch.mu.Lock()
		waiting := sm.launch.waiting
		sm.launch.mu.Unlock()

		if waiting == 1 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("policy retry did not queue behind the occupied launch slot")
		}

		time.Sleep(10 * time.Millisecond)
	}

	sm.mu.RLock()
	queuedGeneration := sm.state.Sessions[created.ID].LaunchGeneration
	queuedPolicy := *sm.state.Scenarios["sc-strath"].Sessions[0].Policy
	sm.mu.RUnlock()

	if queuedGeneration != 1 || queuedPolicy.Attempt != 2 || !queuedPolicy.RetryPending || !queuedPolicy.RetryDispatched {
		t.Fatalf("queued retry generation/policy = %d/%+v", queuedGeneration, queuedPolicy)
	}

	held.release()

	released = true

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("policy retry did not finish after releasing launch slot")
	}

	sm.mu.RLock()
	gotGeneration := sm.state.Sessions[created.ID].LaunchGeneration
	gotPolicy := *sm.state.Scenarios["sc-strath"].Sessions[0].Policy
	sm.mu.RUnlock()

	if gotGeneration != 2 || gotPolicy.Attempt != 2 || gotPolicy.RetryPending || gotPolicy.RetryDispatched || gotPolicy.ExhaustedAt != nil {
		t.Fatalf("completed retry generation/policy = %d/%+v", gotGeneration, gotPolicy)
	}

	wantDeadline := now.Add(time.Minute)

	if gotPolicy.Deadline == nil || !gotPolicy.Deadline.Equal(wantDeadline) {
		t.Fatalf("retry deadline = %v, want immutable %v", gotPolicy.Deadline, wantDeadline)
	}
}

func TestScenarioPolicyCompletionAfterClaimCountsForNewAttempt(t *testing.T) {
	started := time.Date(2026, 7, 17, 11, 30, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))
	addScenarioResult(t, sm, "braw-id", false)

	actions := sm.planScenarioPolicyActions(now)
	if len(actions) != 1 {
		t.Fatalf("actions = %d, want one claimed retry", len(actions))
	}

	items, err := sm.todos.List("scenario:sc-strath", TodoFilter{})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok, err := sm.todos.Claim(items[0].ID, "braw-id", false); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	if _, err := sm.todos.Transition(items[0].ID, TodoStatusDone, "braw-id", false); err != nil {
		t.Fatal(err)
	}

	restarts := 0
	sm.scenarioRestart = func(id string, _, _ uint16) error {
		restarts++

		sm.mu.Lock()
		sm.state.Sessions[id].LaunchGeneration++
		sm.mu.Unlock()

		return nil
	}
	sm.executeScenarioRetry(context.Background(), actions[0], now)
	sm.reconcileScenarioPolicies(context.Background(), now)

	sc := sm.state.Scenarios["sc-strath"]
	if restarts != 1 || sc.Policy.Outcome != scenarioOutcomeComplete || sc.Sessions[0].Policy.Attempt != 2 {
		t.Fatalf("restarts=%d scenario=%+v member=%+v", restarts, sc.Policy, sc.Sessions[0].Policy)
	}
}

func TestScenarioPolicyProcessExitIsNotSuccess(t *testing.T) {
	now := time.Date(2026, 7, 17, 11, 45, 0, 0, time.UTC)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Hour, 0, now))
	exitZero := 0
	sm.state.Sessions["braw-id"].Status = StatusStopped
	sm.state.Sessions["braw-id"].ExitCode = &exitZero

	sm.reconcileScenarioPolicies(context.Background(), now)

	if sc := sm.state.Scenarios["sc-strath"]; sc.Policy.Outcome != "" || sc.Sessions[0].Policy.SucceededAt != nil {
		t.Fatalf("clean process exit satisfied result contract: policy=%+v member=%+v", sc.Policy, sc.Sessions[0].Policy)
	}
}

func TestScenarioPolicyCompletionWinsObservedTimeoutRace(t *testing.T) {
	started := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))
	addScenarioResult(t, sm, "braw-id", true)

	restarts := 0
	sm.scenarioRestart = func(string, uint16, uint16) error { restarts++; return nil }
	sm.reconcileScenarioPolicies(context.Background(), now)

	sc := sm.state.Scenarios["sc-strath"]
	if restarts != 0 || sc.Policy.Outcome != scenarioOutcomeComplete || sc.Sessions[0].Policy.SucceededAt == nil {
		t.Fatalf("restarts=%d scenario=%+v member=%+v", restarts, sc.Policy, sc.Sessions[0].Policy)
	}
}

func TestScenarioPolicyQuorumCompletionCancelsSameTickTimeouts(t *testing.T) {
	started := time.Date(2026, 7, 17, 12, 30, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now,
		policyMember("braw", true, time.Hour, 0, started),
		policyMember("canny", false, time.Minute, 1, started),
	)
	sc := sm.state.Scenarios["sc-strath"]
	sc.Policy.Completion = scenariofile.CompletionQuorum
	sc.Policy.Quorum = 1

	addScenarioResult(t, sm, "braw-id", true)

	restarts := 0
	sm.scenarioRestart = func(string, uint16, uint16) error { restarts++; return nil }
	sm.reconcileScenarioPolicies(context.Background(), now)

	optional := sc.Sessions[1].Policy
	if sc.Policy.Outcome != scenarioOutcomeComplete || restarts != 0 || optional.Attempt != 1 || optional.RetryPending {
		t.Fatalf("same-tick completion = outcome %q restarts %d optional %+v", sc.Policy.Outcome, restarts, optional)
	}
}

func TestScenarioPolicyQuorumRequiresRequiredResults(t *testing.T) {
	now := time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC)
	members := []ScenarioSession{
		policyMember("braw", true, time.Hour, 0, now),
		policyMember("canny", false, time.Hour, 0, now),
		policyMember("dreich", false, time.Hour, 0, now),
	}
	sm := newScenarioPolicyTestManager(t, now, members...)
	sc := sm.state.Scenarios["sc-strath"]
	sc.Policy.Completion = scenariofile.CompletionQuorum
	sc.Policy.Quorum = 2

	addScenarioResult(t, sm, "canny-id", true)
	addScenarioResult(t, sm, "dreich-id", true)
	addScenarioResult(t, sm, "braw-id", false)
	sm.reconcileScenarioPolicies(context.Background(), now)

	if sc.Policy.Outcome != "" {
		t.Fatalf("optional quorum bypassed required member: %+v", sc.Policy)
	}

	items, err := sm.todos.List("scenario:sc-strath", TodoFilter{})
	if err != nil {
		t.Fatal(err)
	}

	for _, item := range items {
		if item.Assignee != "braw-id" {
			continue
		}

		if _, ok, err := sm.todos.Claim(item.ID, "braw-id", false); err != nil || !ok {
			t.Fatalf("claim required result: ok=%v err=%v", ok, err)
		}

		if _, err := sm.todos.Transition(item.ID, TodoStatusDone, "braw-id", false); err != nil {
			t.Fatal(err)
		}
	}

	sm.reconcileScenarioPolicies(context.Background(), now.Add(time.Second))

	if sc.Policy.Outcome != scenarioOutcomeComplete {
		t.Fatalf("outcome = %q, want complete", sc.Policy.Outcome)
	}
}

func TestScenarioPolicyRequiredAndOptionalExhaustion(t *testing.T) {
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)

	t.Run("required fails", func(t *testing.T) {
		sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 0, now.Add(-time.Minute)))
		sm.state.Scenarios["sc-strath"].Policy.OnExhausted = scenariofile.OnExhaustedFail
		sm.reconcileScenarioPolicies(context.Background(), now)

		sc := sm.state.Scenarios["sc-strath"]
		if sc.Policy.Outcome != scenarioOutcomeFailed || !strings.Contains(sc.Policy.OutcomeReason, "required member") {
			t.Fatalf("policy = %+v", sc.Policy)
		}
	})

	t.Run("optional does not fail", func(t *testing.T) {
		sm := newScenarioPolicyTestManager(t, now,
			policyMember("braw", true, time.Hour, 0, now),
			policyMember("canny", false, time.Minute, 0, now.Add(-time.Minute)),
		)
		sm.state.Scenarios["sc-strath"].Policy.OnExhausted = scenariofile.OnExhaustedFail
		sm.reconcileScenarioPolicies(context.Background(), now)

		sc := sm.state.Scenarios["sc-strath"]
		if sc.Policy.Outcome != "" || sc.Sessions[1].Policy.ExhaustedAt == nil {
			t.Fatalf("policy=%+v optional=%+v", sc.Policy, sc.Sessions[1].Policy)
		}
	})
}

func TestScenarioPolicyFailsWhenQuorumBecomesUnreachable(t *testing.T) {
	now := time.Date(2026, 7, 17, 14, 30, 0, 0, time.UTC)
	sm := newScenarioPolicyTestManager(t, now,
		policyMember("braw", true, time.Hour, 0, now),
		policyMember("canny", false, time.Minute, 0, now.Add(-time.Minute)),
		policyMember("dreich", false, time.Minute, 0, now.Add(-time.Minute)),
	)
	sc := sm.state.Scenarios["sc-strath"]
	sc.Policy.Completion = scenariofile.CompletionQuorum
	sc.Policy.Quorum = 2
	sc.Policy.OnExhausted = scenariofile.OnExhaustedFail

	addScenarioResult(t, sm, "braw-id", true)

	sm.reconcileScenarioPolicies(context.Background(), now)

	if sc.Policy.Outcome != scenarioOutcomeFailed || !strings.Contains(sc.Policy.OutcomeReason, "unreachable") {
		t.Fatalf("policy = %+v", sc.Policy)
	}
}

func TestScenarioPolicyGenerationAdvanceBeforeDispatchSkipsRetry(t *testing.T) {
	started := time.Date(2026, 7, 17, 14, 45, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))

	actions := sm.planScenarioPolicyActions(now)
	if len(actions) != 1 {
		t.Fatalf("actions = %v, want one", actions)
	}

	sm.state.Sessions["braw-id"].LaunchGeneration++
	restarts := 0
	sm.scenarioRestart = func(string, uint16, uint16) error { restarts++; return nil }
	sm.executeScenarioRetry(context.Background(), actions[0], now)
	sm.reconcileScenarioPolicies(context.Background(), now.Add(time.Second))

	member := sm.state.Scenarios["sc-strath"].Sessions[0].Policy
	if restarts != 0 || member.RetryPending || member.ExhaustedAt != nil {
		t.Fatalf("generation race restarts=%d policy=%+v", restarts, member)
	}
}

func TestScenarioPolicyStopResumeConsumesDeadline(t *testing.T) {
	started := time.Date(2026, 7, 17, 15, 0, 0, 0, time.UTC)
	now := started.Add(2 * time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 0, started))
	sm.state.Sessions["braw-id"].Status = StatusStopped

	if _, err := sm.StopScenario("strath"); err != nil {
		t.Fatal(err)
	}

	if !sm.state.Scenarios["sc-strath"].Policy.Paused {
		t.Fatal("scenario policy was not paused by stop")
	}

	if _, err := sm.ResumeScenario("strath", 24, 80); err != nil {
		t.Fatal(err)
	}

	policy := sm.state.Scenarios["sc-strath"].Sessions[0].Policy
	if sm.state.Scenarios["sc-strath"].Policy.Paused || policy.ExhaustedAt == nil {
		t.Fatalf("elapsed deadline was reset on resume: %+v", policy)
	}
}

func TestScenarioPolicyResumeSaveFailureStopsResumedMembers(t *testing.T) {
	now := time.Date(2026, 7, 17, 15, 15, 0, 0, time.UTC)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Hour, 0, now))
	sm.state.Sessions["braw-id"].Status = StatusStopped
	sm.state.Scenarios["sc-strath"].Policy.Paused = true
	sm.scenarioResume = func(id string, _, _ uint16) error {
		sm.mu.Lock()
		sm.state.Sessions[id].Status = StatusRunning
		sm.mu.Unlock()

		return nil
	}
	sm.saveStateFault = func() error {
		if !sm.state.Scenarios["sc-strath"].Policy.Paused {
			return errors.New("dreich disk")
		}

		return nil
	}

	resumed, err := sm.ResumeScenario("strath", 24, 80)
	if err == nil || !strings.Contains(err.Error(), "persist scenario policy resume") {
		t.Fatalf("error = %v, want durable resume failure", err)
	}

	if len(resumed) != 0 {
		t.Fatalf("reported resumed members after rollback: %v", resumed)
	}

	if status := sm.state.Sessions["braw-id"].Status; status != StatusStopped {
		t.Fatalf("resumed process state = %s, want stopped rollback", status)
	}

	if !sm.state.Scenarios["sc-strath"].Policy.Paused {
		t.Fatal("failed unpause changed durable policy state")
	}
}

func TestScenarioPolicyDeleteDuringRetryDoesNotRecreate(t *testing.T) {
	started := time.Date(2026, 7, 17, 16, 0, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))

	sm.scenarioRestart = func(id string, _, _ uint16) error {
		sm.mu.Lock()
		delete(sm.state.Scenarios, "sc-strath")
		delete(sm.state.Sessions, id)
		sm.mu.Unlock()

		return errors.New("deleted")
	}

	sm.reconcileScenarioPolicies(context.Background(), now)

	if sm.state.Scenarios["sc-strath"] != nil || sm.state.Sessions["braw-id"] != nil {
		t.Fatal("retry recreated state after scenario deletion")
	}
}

func TestScenarioPolicySlowRetryDoesNotBlockOtherScenario(t *testing.T) {
	started := time.Date(2026, 7, 17, 16, 15, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))
	sm.state.Sessions["bothy-id"] = &SessionState{ID: "bothy-id", Name: "bothy", Status: StatusStopped, LaunchGeneration: 1}
	sm.state.Scenarios["sc-bothy"] = &ScenarioState{
		ID: "sc-bothy", Name: "bothy", SessionIDs: []string{"bothy-id"},
		Sessions: []ScenarioSession{{Name: "bothy", Policy: &ScenarioMemberPolicyState{Required: true, Attempt: 1}}},
		Policy:   &ScenarioPolicyState{Completion: scenariofile.CompletionAll, OnExhausted: scenariofile.OnExhaustedWait, Active: true},
	}

	startedRetry := make(chan struct{})
	releaseRetry := make(chan struct{})
	sm.scenarioRestart = func(id string, _, _ uint16) error {
		close(startedRetry)
		<-releaseRetry

		sm.mu.Lock()
		sm.state.Sessions[id].LaunchGeneration++
		sm.mu.Unlock()

		return nil
	}

	reconciled := make(chan struct{})

	go func() {
		sm.reconcileScenarioPolicies(context.Background(), now)
		close(reconciled)
	}()

	<-startedRetry

	stopped := make(chan error, 1)

	go func() {
		_, err := sm.StopScenario("bothy")
		stopped <- err
	}()

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry in one scenario blocked stop in another")
	}

	close(releaseRetry)
	<-reconciled
}

func TestScenarioPolicyRetrySerializesDirectResume(t *testing.T) {
	started := time.Date(2026, 7, 17, 16, 20, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))

	startedRetry := make(chan struct{})
	releaseRetry := make(chan struct{})
	sm.scenarioRestart = func(id string, _, _ uint16) error {
		close(startedRetry)
		<-releaseRetry

		sm.mu.Lock()
		sm.state.Sessions[id].LaunchGeneration++
		sm.mu.Unlock()

		return nil
	}

	reconciled := make(chan struct{})

	go func() {
		sm.reconcileScenarioPolicies(context.Background(), now)
		close(reconciled)
	}()

	<-startedRetry

	resumed := make(chan error, 1)

	go func() {
		_, err := sm.Resume("braw-id", 24, 80)
		resumed <- err
	}()

	select {
	case err := <-resumed:
		t.Fatalf("direct resume crossed in-flight retry unexpectedly: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseRetry)
	<-reconciled
	<-resumed
}

func TestScenarioPolicyBoundedRestartDoesNotWedgeOnDriver(t *testing.T) {
	sm := newTeardownTestManager(t, 10*time.Millisecond)
	driver := newWedgeDriver(false)
	sm.state.Sessions["braw"] = &SessionState{
		ID: "braw", Name: "braw", Status: StatusRunning, PID: 4242, PIDStartTime: 7,
	}
	sm.sessions["braw"] = driver

	started := time.Now()

	_, err := sm.restartWithReasonMode("braw", 24, 80, StopReasonScenarioTimeout, "scenario-policy", true)
	if err == nil || !strings.Contains(err.Error(), "bounded restart") {
		t.Fatalf("error = %v, want bounded restart failure", err)
	}

	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded retry restart took %s", elapsed)
	}

	driver.mu.Lock()
	kills, forces := driver.kills, driver.forces
	driver.mu.Unlock()

	if kills != 1 || forces != 1 {
		t.Fatalf("bounded escalation kills=%d forces=%d", kills, forces)
	}
}

func TestScenarioPolicyDeleteSerializesWithInFlightRetry(t *testing.T) {
	started := time.Date(2026, 7, 17, 16, 30, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))

	startedRetry := make(chan struct{})
	releaseRetry := make(chan struct{})
	sm.scenarioRestart = func(id string, _, _ uint16) error {
		close(startedRetry)
		<-releaseRetry

		sm.mu.Lock()
		sm.state.Sessions[id].LaunchGeneration++
		sm.mu.Unlock()

		return nil
	}

	reconciled := make(chan struct{})

	go func() {
		sm.reconcileScenarioPolicies(context.Background(), now)
		close(reconciled)
	}()

	<-startedRetry

	deleted := make(chan error, 1)

	go func() {
		_, err := sm.DeleteScenario("strath")
		deleted <- err
	}()

	select {
	case err := <-deleted:
		t.Fatalf("delete crossed in-flight retry unexpectedly: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseRetry)
	<-reconciled

	if err := <-deleted; err != nil {
		t.Fatal(err)
	}

	if sm.state.Scenarios["sc-strath"] != nil || sm.state.Sessions["braw-id"] != nil {
		t.Fatal("delete during retry left or recreated scenario state")
	}
}

func TestScenarioPolicyDeleteFinalSaveFailureRestoresPausedRecord(t *testing.T) {
	now := time.Date(2026, 7, 17, 16, 45, 0, 0, time.UTC)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Hour, 0, now))
	delete(sm.state.Sessions, "braw-id")

	sm.saveStateFault = func() error {
		if sm.state.Scenarios["sc-strath"] == nil {
			return errors.New("dreich disk")
		}

		return nil
	}

	_, err := sm.DeleteScenario("strath")
	if err == nil || !strings.Contains(err.Error(), "persist scenario deletion") {
		t.Fatalf("error = %v, want final persistence failure", err)
	}

	sc := sm.state.Scenarios["sc-strath"]
	if sc == nil || !sc.Policy.Paused {
		t.Fatalf("restored scenario = %+v", sc)
	}

	loaded, loadErr := LoadState(sm.paths.StateFile)
	if loadErr != nil {
		t.Fatal(loadErr)
	}

	if durable := loaded.Scenarios["sc-strath"]; durable == nil || !durable.Policy.Paused {
		t.Fatalf("durable paused scenario = %+v", durable)
	}
}

func TestScenarioPolicyInactivePartialStartupDoesNothing(t *testing.T) {
	now := time.Date(2026, 7, 17, 17, 0, 0, 0, time.UTC)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, now.Add(-time.Minute)))
	sm.state.Scenarios["sc-strath"].Policy.Active = false

	restarts := 0
	sm.scenarioRestart = func(string, uint16, uint16) error { restarts++; return nil }
	sm.reconcileScenarioPolicies(context.Background(), now)

	if restarts != 0 || sm.state.Scenarios["sc-strath"].Sessions[0].Policy.Attempt != 1 {
		t.Fatalf("inactive partial startup reconciled: restarts=%d", restarts)
	}
}

func TestRecoverInterruptedScenarioStartFailsClosed(t *testing.T) {
	now := time.Date(2026, 7, 17, 17, 15, 0, 0, time.UTC)
	sm := newTestSessionManager(t)
	state := NewState()
	state.Scenarios["sc-croft"] = &ScenarioState{
		ID: "sc-croft", Name: "croft",
		Sessions: []ScenarioSession{{
			Name: "braw", Policy: &ScenarioMemberPolicyState{Required: true},
		}},
		Policy: &ScenarioPolicyState{Completion: scenariofile.CompletionAll, OnExhausted: scenariofile.OnExhaustedFail},
	}

	sm.state = state
	if err := sm.saveState(); err != nil {
		t.Fatal(err)
	}

	sm.state = NewState()
	if err := sm.LoadState(); err != nil {
		t.Fatal(err)
	}

	scenario := sm.state.Scenarios["sc-croft"]
	if scenario.Policy.Active || scenario.Policy.Outcome != scenarioOutcomeFailed || scenario.Policy.OutcomeAt == nil {
		t.Fatalf("recovered policy = %+v", scenario.Policy)
	}

	member := scenario.Sessions[0].Policy
	if member.ExhaustedAt == nil || !strings.Contains(member.ExhaustionReason, "startup was interrupted") {
		t.Fatalf("recovered member = %+v", member)
	}

	if recoverInterruptedScenarioStarts(sm.state, now.Add(time.Hour)) {
		t.Fatal("terminal recovery was not idempotent")
	}
}

func TestScenarioPolicyMissingSessionExhaustsPendingRetry(t *testing.T) {
	now := time.Date(2026, 7, 17, 17, 30, 0, 0, time.UTC)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, now.Add(-time.Minute)))
	member := sm.state.Scenarios["sc-strath"].Sessions[0].Policy
	member.Attempt = 2
	member.RetryPending = true
	member.RetryFromGeneration = 1

	delete(sm.state.Sessions, "braw-id")

	sm.reconcileScenarioPolicies(context.Background(), now)

	if member.ExhaustedAt == nil || member.RetryPending || !strings.Contains(member.ExhaustionReason, "session is missing") {
		t.Fatalf("missing retry member = %+v", member)
	}
}

func TestScenarioPolicyCancellationAbandonsScenarioGate(t *testing.T) {
	started := time.Date(2026, 7, 17, 17, 45, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 1, started))
	unlock := sm.lockScenarioPolicy("sc-strath")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		sm.reconcileScenarioPolicies(ctx, now)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sm.mu.RLock()
		pending := sm.state.Scenarios["sc-strath"].Sessions[0].Policy.RetryPending
		sm.mu.RUnlock()

		if pending {
			break
		}

		time.Sleep(time.Millisecond)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled policy reconcile remained blocked on scenario gate")
	}

	unlock()

	sm.mu.RLock()
	dispatched := sm.state.Scenarios["sc-strath"].Sessions[0].Policy.RetryDispatched
	sm.mu.RUnlock()

	if dispatched {
		t.Fatal("cancelled retry crossed durable dispatch boundary")
	}
}

func TestScenarioPolicyStatusIncludesAttemptsDeadlinesAndQuorum(t *testing.T) {
	now := time.Date(2026, 7, 17, 18, 0, 0, 0, time.UTC)
	sm := newScenarioPolicyTestManager(t, now, policyMember("braw", true, time.Minute, 2, now))
	sc := sm.state.Scenarios["sc-strath"]
	sc.Policy.Completion = scenariofile.CompletionQuorum
	sc.Policy.Quorum = 1
	sc.Sessions[0].Policy.ExhaustionReason = "dreich provider"

	record := sm.buildScenarioRecord(sc)
	if record.Policy == nil || record.Policy.Quorum != 1 || record.Policy.RequiredTotal != 1 {
		t.Fatalf("record policy = %+v", record.Policy)
	}

	member := record.Sessions[0].Policy
	if member == nil || member.Attempt != 1 || member.MaxAttempts != 3 || member.Deadline == "" || member.ExhaustionReason != "dreich provider" {
		t.Fatalf("member policy = %+v", member)
	}
}
