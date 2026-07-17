package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/scenariofile"
)

const (
	scenarioOutcomeComplete = "complete"
	scenarioOutcomeFailed   = "failed"
)

type scenarioRetryAction struct {
	scenarioID     string
	sessionID      string
	memberIndex    int
	attempt        int
	fromGeneration uint64
}

// lifecycleGate is a context-aware binary semaphore used for scenario and
// session lifecycle serialization. Operator paths wait with a background
// context; automatic policy work uses its loop context so daemon shutdown can
// abandon queued work before it launches another process.
type lifecycleGate struct {
	token chan struct{}
}

func newLifecycleGate() *lifecycleGate {
	return &lifecycleGate{token: make(chan struct{}, 1)}
}

func (g *lifecycleGate) lock(ctx context.Context) (func(), error) {
	select {
	case g.token <- struct{}{}:
		return func() { <-g.token }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func protocolPolicyConfig(in *protocol.ScenarioPolicyInput) *scenariofile.PolicyConfig {
	if in == nil {
		return nil
	}

	return &scenariofile.PolicyConfig{
		Completion: in.Completion, Quorum: in.Quorum, OnExhausted: in.OnExhausted,
	}
}

func protocolMemberPolicyConfig(in *protocol.ScenarioMemberPolicyInput) *scenariofile.MemberPolicyConfig {
	if in == nil {
		return nil
	}

	return &scenariofile.MemberPolicyConfig{
		Required: in.Required, Timeout: in.Timeout, Retries: in.Retries,
	}
}

func normalizeProtocolScenarioPolicy(msg protocol.ScenarioStartMsg, maxTitle int) (*scenariofile.NormalizedPolicy, error) {
	members := make([]scenariofile.PolicyMember, len(msg.Sessions))
	for i, member := range msg.Sessions {
		members[i] = scenariofile.PolicyMember{
			Name: member.Name, Task: member.Task, Shared: member.Shared,
			HasRequiredResult: protocolResultsHaveRequired(member.Results), Policy: protocolMemberPolicyConfig(member.Policy),
		}
	}

	policy, err := scenariofile.NormalizePolicy(protocolPolicyConfig(msg.Policy), members)
	if err != nil {
		return nil, err
	}

	if err := scenariofile.ValidatePolicyContracts(policy, members, maxTitle); err != nil {
		return nil, err
	}

	return policy, nil
}

func newScenarioPolicyState(policy *scenariofile.NormalizedPolicy) *ScenarioPolicyState {
	if policy == nil {
		return nil
	}

	return &ScenarioPolicyState{
		Completion: policy.Completion, Quorum: policy.Quorum, OnExhausted: policy.OnExhausted,
	}
}

func newScenarioMemberPolicyState(policy scenariofile.NormalizedMemberPolicy) *ScenarioMemberPolicyState {
	return &ScenarioMemberPolicyState{
		Required: policy.Required, TimeoutNanos: int64(policy.Timeout), Retries: policy.Retries,
	}
}

func activateScenarioPolicy(sc *ScenarioState, now time.Time) {
	if sc.Policy == nil || sc.Policy.Active {
		return
	}

	now = now.UTC()

	sc.Policy.Active = true

	for i := range sc.Sessions {
		startScenarioMemberAttempt(sc.Sessions[i].Policy, now, false, 0)
	}
}

func startScenarioMemberAttempt(policy *ScenarioMemberPolicyState, now time.Time, retry bool, fromGeneration uint64) {
	if policy == nil {
		return
	}

	now = now.UTC()
	policy.Attempt++
	policy.AttemptStartedAt = timePtr(now)
	policy.ExhaustedAt = nil
	policy.ExhaustionReason = ""
	policy.RetryPending = retry
	policy.RetryDispatched = false

	policy.RetryFromGeneration = fromGeneration

	if policy.TimeoutNanos > 0 {
		deadline := now.Add(time.Duration(policy.TimeoutNanos))
		policy.Deadline = &deadline
	} else {
		policy.Deadline = nil
	}
}

func timePtr(t time.Time) *time.Time {
	t = t.UTC()

	return &t
}

// recoverInterruptedScenarioStarts terminally records a policy scenario whose
// durable reserve record survived a daemon exit but whose activation did not.
// Such a record can contain only a partial member set, so retrying it would
// violate atomic startup and risk duplicate processes. Keeping it as a visible
// failed scenario preserves identities/worktrees for explicit inspection and
// cleanup while ensuring the policy loop never launches it.
func recoverInterruptedScenarioStarts(state *State, now time.Time) bool {
	changed := false
	now = now.UTC()

	for _, scenario := range state.Scenarios {
		if scenario.Policy == nil || scenario.Policy.Active || scenario.Policy.Outcome != "" {
			continue
		}

		reason := "daemon restarted before atomic scenario startup was activated"
		scenario.Policy.Outcome = scenarioOutcomeFailed
		scenario.Policy.OutcomeReason = reason
		scenario.Policy.OutcomeAt = timePtr(now)

		for memberIndex := range scenario.Sessions {
			member := scenario.Sessions[memberIndex].Policy
			if member == nil || member.SucceededAt != nil || member.ExhaustedAt != nil {
				continue
			}

			member.RetryPending = false
			member.RetryDispatched = false
			member.RetryFromGeneration = 0
			member.ExhaustedAt = timePtr(now)
			member.ExhaustionReason = "startup was interrupted before the first attempt committed"
		}

		changed = true
	}

	return changed
}

func (sm *SessionManager) scenarioPolicyTime() time.Time {
	if sm.scenarioPolicyNow != nil {
		return sm.scenarioPolicyNow().UTC()
	}

	return time.Now().UTC()
}

// RunScenarioPolicyLoop reconciles durable scenario timeout, retry, and quorum
// state. It runs once immediately so daemon downtime consumes wall-clock
// deadlines, then at the documented one-second resolution.
func (sm *SessionManager) RunScenarioPolicyLoop(ctx context.Context) {
	sm.reconcileScenarioPolicies(ctx, sm.scenarioPolicyTime())

	ticker := sm.loopTicker(scenariofile.PolicyResolution)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case tick := <-ticker.C():
			if tick.IsZero() {
				tick = sm.scenarioPolicyTime()
			}

			sm.reconcileScenarioPolicies(ctx, tick.UTC())
		}
	}
}

// reconcileScenarioPolicies serializes durable planning, then executes each
// action under a per-scenario lifecycle gate. A second planner can rediscover a
// persisted pending claim while its action is running, but the gate and the
// last-moment pending check make that continuation a no-op. Slow process work
// in one scenario therefore never blocks commands for another scenario.
func (sm *SessionManager) reconcileScenarioPolicies(ctx context.Context, now time.Time) {
	sm.reconcileScenarioPoliciesFor(ctx, now, "")
}

// reconcileScenarioPoliciesFor performs a full reconcile when scenarioID is
// empty and otherwise limits both planning and execution to one scenario.
func (sm *SessionManager) reconcileScenarioPoliciesFor(ctx context.Context, now time.Time, scenarioID string) {
	sm.scenarioPolicyPlanMu.Lock()
	actions := sm.planScenarioPolicyActionsFor(now.UTC(), scenarioID)
	sm.scenarioPolicyPlanMu.Unlock()

	for _, action := range actions {
		if ctx.Err() != nil {
			return
		}

		unlock, err := sm.lockScenarioPolicyContext(ctx, action.scenarioID)
		if err != nil {
			return
		}

		sm.executeScenarioRetry(ctx, action, now.UTC())
		unlock()
	}
}

// lockScenarioPolicy serializes roster and process lifecycle changes only
// within one scenario. Its returned unlock function must always be called.
func (sm *SessionManager) lockScenarioPolicy(scenarioID string) func() {
	unlock, _ := sm.lockScenarioPolicyContext(context.Background(), scenarioID)

	return unlock
}

func (sm *SessionManager) lockScenarioPolicyContext(ctx context.Context, scenarioID string) (func(), error) {
	sm.scenarioPolicyLocksMu.Lock()
	if sm.scenarioPolicyLocks == nil {
		sm.scenarioPolicyLocks = make(map[string]*lifecycleGate)
	}

	gate := sm.scenarioPolicyLocks[scenarioID]
	if gate == nil {
		gate = newLifecycleGate()
		sm.scenarioPolicyLocks[scenarioID] = gate
	}
	sm.scenarioPolicyLocksMu.Unlock()

	return gate.lock(ctx)
}

func (sm *SessionManager) lockSessionLaunch(sessionID string) func() {
	unlock, _ := sm.lockSessionLaunchContext(context.Background(), sessionID)

	return unlock
}

func (sm *SessionManager) lockSessionLaunchContext(ctx context.Context, sessionID string) (func(), error) {
	sm.sessionLaunchLocksMu.Lock()
	if sm.sessionLaunchLocks == nil {
		sm.sessionLaunchLocks = make(map[string]*lifecycleGate)
	}

	gate := sm.sessionLaunchLocks[sessionID]
	if gate == nil {
		gate = newLifecycleGate()
		sm.sessionLaunchLocks[sessionID] = gate
	}
	sm.sessionLaunchLocksMu.Unlock()

	return gate.lock(ctx)
}

func (sm *SessionManager) scenarioIDByName(name string) (string, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for id, sc := range sm.state.Scenarios {
		if sc.Name == name {
			return id, true
		}
	}

	return "", false
}

type scenarioPolicySnapshot struct {
	id string
}

func (sm *SessionManager) policySnapshots(onlyScenarioID string) []scenarioPolicySnapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var out []scenarioPolicySnapshot

	for id, sc := range sm.state.Scenarios {
		if onlyScenarioID != "" && id != onlyScenarioID {
			continue
		}

		if sc.Policy == nil {
			continue
		}

		if !sm.scenarioPolicyDirty[id] && (!sc.Policy.Active || sc.Policy.Paused || sc.Policy.Outcome != "") {
			continue
		}

		out = append(out, scenarioPolicySnapshot{id: sc.ID})
	}

	return out
}

func (sm *SessionManager) planScenarioPolicyActions(now time.Time) []scenarioRetryAction {
	return sm.planScenarioPolicyActionsFor(now, "")
}

func (sm *SessionManager) planScenarioPolicyActionsFor(now time.Time, scenarioID string) []scenarioRetryAction {
	snapshots := sm.policySnapshots(scenarioID)

	var actions []scenarioRetryAction

	for _, snapshot := range snapshots {
		if sm.flushScenarioPolicyDirty(snapshot.id) {
			continue
		}

		var progress map[string][2]int

		if sm.todos != nil {
			var err error

			progress, err = sm.todos.AssigneeProgress("scenario:" + snapshot.id)
			if err != nil {
				sm.log.Error("read scenario result contracts", "scenario", snapshot.id, "err", err)
				continue
			}
		}

		sm.mu.Lock()

		sc := sm.state.Scenarios[snapshot.id]
		if sc == nil || sc.Policy == nil {
			sm.mu.Unlock()
			continue
		}

		if !sc.Policy.Active || sc.Policy.Paused || sc.Policy.Outcome != "" {
			sm.mu.Unlock()
			continue
		}

		beforePolicy, beforeMembers := cloneScenarioPolicyRuntime(sc)
		changed := observeScenarioPolicySuccesses(sc, progress, now)
		terminal := updateScenarioPolicyOutcome(sc, now)
		changed = changed || terminal

		for i := 0; i < len(sc.Sessions) && !terminal; i++ {
			member := &sc.Sessions[i]

			policy := member.Policy

			if policy == nil || policy.SucceededAt != nil {
				continue
			}

			var sessionID string
			if i < len(sc.SessionIDs) {
				sessionID = sc.SessionIDs[i]
			}

			sess := sm.state.Sessions[sessionID]
			if sessionID == "" || sess == nil {
				policy.RetryPending = false
				policy.RetryDispatched = false
				policy.RetryFromGeneration = 0
				policy.ExhaustedAt = timePtr(now)
				policy.ExhaustionReason = fmt.Sprintf("attempt %d cannot continue because the member session is missing", policy.Attempt)
				changed = true

				continue
			}

			generation := sess.LaunchGeneration

			if policy.RetryPending {
				if generation > policy.RetryFromGeneration {
					policy.RetryPending = false
					policy.RetryDispatched = false
					policy.RetryFromGeneration = 0
					changed = true
				} else if policy.RetryDispatched {
					action := scenarioRetryAction{
						scenarioID: sc.ID, sessionID: sessionID, memberIndex: i,
						attempt: policy.Attempt, fromGeneration: policy.RetryFromGeneration,
					}
					if !sm.scenarioRetryInFlight(action) {
						policy.RetryPending = false
						policy.RetryDispatched = false
						policy.RetryFromGeneration = 0
						policy.ExhaustedAt = timePtr(now)
						policy.ExhaustionReason = fmt.Sprintf("attempt %d retry dispatch was interrupted before its outcome was durably recorded", policy.Attempt)
						changed = true
					}
				} else {
					actions = append(actions, scenarioRetryAction{
						scenarioID: sc.ID, sessionID: sessionID, memberIndex: i,
						attempt: policy.Attempt, fromGeneration: policy.RetryFromGeneration,
					})
					// Re-persist a pending claim before every continuation. A fresh
					// claim whose atomic state write failed must never launch.
					changed = true
				}

				continue
			}

			if policy.ExhaustedAt != nil || policy.Deadline == nil || now.Before(*policy.Deadline) {
				continue
			}

			if policy.Attempt <= policy.Retries {
				startScenarioMemberAttempt(policy, now, true, generation)
				actions = append(actions, scenarioRetryAction{
					scenarioID: sc.ID, sessionID: sessionID, memberIndex: i,
					attempt: policy.Attempt, fromGeneration: generation,
				})
			} else {
				policy.ExhaustedAt = timePtr(now)
				policy.ExhaustionReason = fmt.Sprintf("attempt %d timed out at %s", policy.Attempt, policy.Deadline.UTC().Format(time.RFC3339Nano))
			}

			changed = true
		}

		if !terminal {
			terminal = updateScenarioPolicyOutcome(sc, now)
			changed = changed || terminal
		}

		saveFailed := false

		if changed {
			if err := sm.saveState(); err != nil {
				sm.log.Error("persist scenario policy reconciliation", "scenario", sc.ID, "err", err)
				restoreScenarioPolicyRuntime(sc, beforePolicy, beforeMembers)

				saveFailed = true
			}
		}
		sm.mu.Unlock()

		if terminal || saveFailed {
			actions = filterScenarioRetryActions(actions, sc.ID)
		}
	}

	return actions
}

func observeScenarioPolicySuccesses(sc *ScenarioState, progress map[string][2]int, now time.Time) bool {
	changed := false

	for i := range sc.Sessions {
		policy := sc.Sessions[i].Policy
		if policy == nil || policy.SucceededAt != nil || i >= len(sc.SessionIDs) {
			continue
		}

		tracked, complete := scenarioMemberContractProgress(sc.Sessions[i], progress[sc.SessionIDs[i]])
		if !tracked || !complete {
			continue
		}

		policy.SucceededAt = timePtr(now)
		policy.RetryPending = false
		policy.RetryDispatched = false
		policy.RetryFromGeneration = 0
		policy.ExhaustedAt = nil
		policy.ExhaustionReason = ""
		changed = true
	}

	return changed
}

func (sm *SessionManager) flushScenarioPolicyDirty(id string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.scenarioPolicyDirty[id] {
		return false
	}

	if sm.state.Scenarios[id] == nil {
		delete(sm.scenarioPolicyDirty, id)

		return true
	}

	if err := sm.saveState(); err != nil {
		sm.log.Error("persist scenario policy retry result", "scenario", id, "err", err)
	} else {
		delete(sm.scenarioPolicyDirty, id)
	}

	return true
}

func cloneScenarioPolicyRuntime(sc *ScenarioState) (*ScenarioPolicyState, []*ScenarioMemberPolicyState) {
	var policy *ScenarioPolicyState

	if sc.Policy != nil {
		copyPolicy := *sc.Policy
		if sc.Policy.OutcomeAt != nil {
			copyPolicy.OutcomeAt = timePtr(*sc.Policy.OutcomeAt)
		}

		policy = &copyPolicy
	}

	members := make([]*ScenarioMemberPolicyState, len(sc.Sessions))
	for i := range sc.Sessions {
		member := sc.Sessions[i].Policy
		if member == nil {
			continue
		}

		copyMember := *member
		copyMember.AttemptStartedAt = cloneScenarioTime(member.AttemptStartedAt)
		copyMember.Deadline = cloneScenarioTime(member.Deadline)
		copyMember.SucceededAt = cloneScenarioTime(member.SucceededAt)
		copyMember.ExhaustedAt = cloneScenarioTime(member.ExhaustedAt)
		members[i] = &copyMember
	}

	return policy, members
}

func cloneScenarioTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}

	return timePtr(*value)
}

func restoreScenarioPolicyRuntime(sc *ScenarioState, policy *ScenarioPolicyState, members []*ScenarioMemberPolicyState) {
	sc.Policy = policy
	for i := range sc.Sessions {
		if i < len(members) {
			sc.Sessions[i].Policy = members[i]
		}
	}
}

func filterScenarioRetryActions(actions []scenarioRetryAction, scenarioID string) []scenarioRetryAction {
	kept := actions[:0]
	for _, action := range actions {
		if action.scenarioID != scenarioID {
			kept = append(kept, action)
		}
	}

	return kept
}

func updateScenarioPolicyOutcome(sc *ScenarioState, now time.Time) bool {
	if sc.Policy == nil || sc.Policy.Outcome != "" {
		return false
	}

	var (
		successful, remaining, required, requiredSuccessful int
		requiredExhausted                                   *ScenarioSession
	)

	for i := range sc.Sessions {
		member := &sc.Sessions[i]
		if member.Policy == nil {
			continue
		}

		if member.Policy.Required {
			required++
		}

		if member.Policy.SucceededAt != nil {
			successful++

			if member.Policy.Required {
				requiredSuccessful++
			}
		}

		if member.Policy.SucceededAt == nil && member.Policy.ExhaustedAt == nil {
			remaining++
		}

		if member.Policy.Required && member.Policy.ExhaustedAt != nil && requiredExhausted == nil {
			requiredExhausted = member
		}
	}

	complete := requiredSuccessful == required
	if sc.Policy.Completion == scenariofile.CompletionQuorum {
		complete = complete && successful >= sc.Policy.Quorum
	}

	if complete {
		sc.Policy.Outcome = scenarioOutcomeComplete
		sc.Policy.OutcomeReason = fmt.Sprintf("%d members succeeded; %d/%d required", successful, requiredSuccessful, required)
		sc.Policy.OutcomeAt = timePtr(now)
		clearScenarioPendingRetries(sc)

		return true
	}

	if sc.Policy.OnExhausted == scenariofile.OnExhaustedFail && requiredExhausted != nil {
		sc.Policy.Outcome = scenarioOutcomeFailed
		sc.Policy.OutcomeReason = fmt.Sprintf("required member %q exhausted: %s", requiredExhausted.Name, requiredExhausted.Policy.ExhaustionReason)
		sc.Policy.OutcomeAt = timePtr(now)
		clearScenarioPendingRetries(sc)

		return true
	}

	if sc.Policy.OnExhausted == scenariofile.OnExhaustedFail &&
		sc.Policy.Completion == scenariofile.CompletionQuorum &&
		successful+remaining < sc.Policy.Quorum {
		sc.Policy.Outcome = scenarioOutcomeFailed
		sc.Policy.OutcomeReason = fmt.Sprintf("quorum %d is unreachable: %d succeeded and %d members remain", sc.Policy.Quorum, successful, remaining)
		sc.Policy.OutcomeAt = timePtr(now)
		clearScenarioPendingRetries(sc)

		return true
	}

	return false
}

func clearScenarioPendingRetries(sc *ScenarioState) {
	for i := range sc.Sessions {
		if sc.Sessions[i].Policy == nil {
			continue
		}

		sc.Sessions[i].Policy.RetryPending = false
		sc.Sessions[i].Policy.RetryDispatched = false
		sc.Sessions[i].Policy.RetryFromGeneration = 0
	}
}

func (sm *SessionManager) executeScenarioRetry(ctx context.Context, action scenarioRetryAction, now time.Time) {
	unlockLaunch, err := sm.lockSessionLaunchContext(ctx, action.sessionID)
	if err != nil {
		return
	}
	defer unlockLaunch()

	if !sm.markScenarioRetryDispatched(action) {
		return
	}
	defer sm.clearScenarioRetryInFlight(action)

	lc := sm.Config().Lifecycle

	err = ctx.Err()
	if err == nil && !sm.scenarioRetryStillCurrent(action) {
		err = errors.New("retry was superseded before process launch")
	}

	if err == nil {
		if sm.scenarioRestart != nil {
			err = sm.scenarioRestart(action.sessionID, lc.DefaultRowsOrDefault(), lc.DefaultColsOrDefault())
		} else {
			_, err = sm.restartWithReasonModeContextLocked(ctx, action.sessionID, lc.DefaultRowsOrDefault(), lc.DefaultColsOrDefault(), StopReasonScenarioTimeout, "scenario-policy", true)
		}
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sc := sm.state.Scenarios[action.scenarioID]
	if sc == nil || action.memberIndex >= len(sc.Sessions) || action.memberIndex >= len(sc.SessionIDs) || sc.SessionIDs[action.memberIndex] != action.sessionID {
		return
	}

	policy := sc.Sessions[action.memberIndex].Policy
	if policy == nil || !policy.RetryPending || !policy.RetryDispatched || policy.Attempt != action.attempt {
		return
	}

	generation := uint64(0)
	if sess := sm.state.Sessions[action.sessionID]; sess != nil {
		generation = sess.LaunchGeneration
	}

	if generation > action.fromGeneration {
		policy.RetryPending = false
		policy.RetryDispatched = false
		policy.RetryFromGeneration = 0
	} else if err != nil {
		policy.RetryPending = false
		policy.RetryDispatched = false
		policy.RetryFromGeneration = 0
		policy.ExhaustedAt = timePtr(now)
		policy.ExhaustionReason = fmt.Sprintf("attempt %d failed to start: %v", policy.Attempt, err)
	} else {
		// A successful action must advance LaunchGeneration. Fail closed if an
		// injected/alternate lifecycle path violates that idempotency contract.
		policy.RetryPending = false
		policy.RetryDispatched = false
		policy.RetryFromGeneration = 0
		policy.ExhaustedAt = timePtr(now)
		policy.ExhaustionReason = fmt.Sprintf("attempt %d restart did not advance launch generation", policy.Attempt)
	}

	updateScenarioPolicyOutcome(sc, now)

	if saveErr := sm.saveState(); saveErr != nil {
		sm.log.Error("persist scenario retry result", "scenario", action.scenarioID, "session", action.sessionID, "err", saveErr)

		if sm.scenarioPolicyDirty == nil {
			sm.scenarioPolicyDirty = make(map[string]bool)
		}

		sm.scenarioPolicyDirty[action.scenarioID] = true
	} else {
		delete(sm.scenarioPolicyDirty, action.scenarioID)
	}
}

// scenarioRetryStillCurrent repeats the launch-generation and durable-claim
// checks after both lifecycle gates have been acquired and immediately before
// process work. This closes the final compare-to-launch window without holding
// the manager lock across teardown or spawn.
func (sm *SessionManager) scenarioRetryStillCurrent(action scenarioRetryAction) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sc := sm.state.Scenarios[action.scenarioID]
	if sc == nil || sc.Policy == nil || !sc.Policy.Active || sc.Policy.Paused || sc.Policy.Outcome != "" ||
		action.memberIndex >= len(sc.Sessions) || action.memberIndex >= len(sc.SessionIDs) ||
		sc.SessionIDs[action.memberIndex] != action.sessionID {
		return false
	}

	policy := sc.Sessions[action.memberIndex].Policy
	sess := sm.state.Sessions[action.sessionID]

	return policy != nil && sess != nil && policy.RetryPending && policy.RetryDispatched &&
		policy.Attempt == action.attempt && policy.RetryFromGeneration == action.fromGeneration &&
		sess.LaunchGeneration == action.fromGeneration && sess.Status != StatusCreating
}

// markScenarioRetryDispatched adds a second durable boundary between claiming
// an attempt and touching its process. A crash after this write fails closed on
// restart instead of executing the same attempt twice when its outcome write
// was lost. The runtime in-flight mark prevents another planner in this daemon
// from mistaking a live dispatch for an interrupted one.
func (sm *SessionManager) markScenarioRetryDispatched(action scenarioRetryAction) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sc := sm.state.Scenarios[action.scenarioID]
	if sc == nil || sc.Policy == nil || !sc.Policy.Active || sc.Policy.Paused || sc.Policy.Outcome != "" ||
		action.memberIndex >= len(sc.Sessions) || action.memberIndex >= len(sc.SessionIDs) ||
		sc.SessionIDs[action.memberIndex] != action.sessionID {
		return false
	}

	policy := sc.Sessions[action.memberIndex].Policy

	sess := sm.state.Sessions[action.sessionID]
	if policy == nil || sess == nil || !policy.RetryPending || policy.RetryDispatched ||
		policy.Attempt != action.attempt || policy.RetryFromGeneration != action.fromGeneration ||
		sess.LaunchGeneration != action.fromGeneration || sess.Status == StatusCreating {
		return false
	}

	policy.RetryDispatched = true

	sm.setScenarioRetryInFlight(action, true)

	if err := sm.saveState(); err != nil {
		policy.RetryDispatched = false

		sm.setScenarioRetryInFlight(action, false)
		sm.log.Error("persist scenario retry dispatch", "scenario", action.scenarioID, "session", action.sessionID, "err", err)

		return false
	}

	return true
}

func (sm *SessionManager) scenarioRetryInFlight(action scenarioRetryAction) bool {
	sm.scenarioPolicyInFlightMu.Lock()
	defer sm.scenarioPolicyInFlightMu.Unlock()

	return sm.scenarioPolicyInFlight[action]
}

func (sm *SessionManager) setScenarioRetryInFlight(action scenarioRetryAction, active bool) {
	sm.scenarioPolicyInFlightMu.Lock()
	defer sm.scenarioPolicyInFlightMu.Unlock()

	if active {
		if sm.scenarioPolicyInFlight == nil {
			sm.scenarioPolicyInFlight = make(map[scenarioRetryAction]bool)
		}

		sm.scenarioPolicyInFlight[action] = true
	} else {
		delete(sm.scenarioPolicyInFlight, action)
	}
}

func (sm *SessionManager) clearScenarioRetryInFlight(action scenarioRetryAction) {
	sm.setScenarioRetryInFlight(action, false)
}

func scenarioPolicyStateConfig(policy *ScenarioPolicyState) *scenariofile.PolicyConfig {
	if policy == nil {
		return nil
	}

	return &scenariofile.PolicyConfig{
		Completion: policy.Completion, Quorum: policy.Quorum, OnExhausted: policy.OnExhausted,
	}
}

func scenarioMemberStateConfig(policy *ScenarioMemberPolicyState) *scenariofile.MemberPolicyConfig {
	if policy == nil {
		return nil
	}

	required := policy.Required

	timeout := ""

	if policy.TimeoutNanos > 0 {
		timeout = time.Duration(policy.TimeoutNanos).String()
	}

	return &scenariofile.MemberPolicyConfig{Required: &required, Timeout: timeout, Retries: policy.Retries}
}

func normalizeScenarioAddPolicy(sc *ScenarioState, input protocol.ScenarioSessionInput, maxTitle int) (*scenariofile.NormalizedPolicy, error) {
	members := make([]scenariofile.PolicyMember, 0, len(sc.Sessions)+1)
	for _, member := range sc.Sessions {
		members = append(members, scenariofile.PolicyMember{
			Name: member.Name, Task: member.Task, Shared: member.Shared,
			HasRequiredResult: scenarioResultsHaveRequired(member.Results), Policy: scenarioMemberStateConfig(member.Policy),
		})
	}

	members = append(members, scenariofile.PolicyMember{
		Name: input.Name, Task: input.Task, Shared: input.Shared,
		HasRequiredResult: protocolResultsHaveRequired(input.Results), Policy: protocolMemberPolicyConfig(input.Policy),
	})

	policy, err := scenariofile.NormalizePolicy(scenarioPolicyStateConfig(sc.Policy), members)
	if err != nil {
		return nil, err
	}

	if err := scenariofile.ValidatePolicyContracts(policy, members, maxTitle); err != nil {
		return nil, err
	}

	return policy, nil
}

func protocolResultsHaveRequired(results []protocol.ScenarioResultSpec) bool {
	for _, result := range results {
		if result.Required {
			return true
		}
	}

	return false
}

func scenarioResultsHaveRequired(results []ScenarioResultState) bool {
	for _, result := range results {
		if result.Required {
			return true
		}
	}

	return false
}

// applyScenarioAddPolicy validates the current roster again at commit time and
// either keeps legacy semantics or installs/extends the normalized policy.
func applyScenarioAddPolicy(sc *ScenarioState, input protocol.ScenarioSessionInput, now time.Time, maxTitle int) (*ScenarioMemberPolicyState, error) {
	normalized, err := normalizeScenarioAddPolicy(sc, input, maxTitle)
	if err != nil || normalized == nil {
		return nil, err
	}

	if sc.Policy != nil && sc.Policy.Outcome != "" {
		return nil, fmt.Errorf("scenario %q is already %s", sc.Name, sc.Policy.Outcome)
	}

	if sc.Policy == nil {
		sc.Policy = newScenarioPolicyState(normalized)
		for i := range sc.Sessions {
			sc.Sessions[i].Policy = newScenarioMemberPolicyState(normalized.Members[i])
		}

		activateScenarioPolicy(sc, now)
	}

	policy := newScenarioMemberPolicyState(normalized.Members[len(normalized.Members)-1])
	startScenarioMemberAttempt(policy, now, false, 0)

	return policy, nil
}
