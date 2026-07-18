package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/scenariofile"
	"github.com/d0ugal/graith/internal/store"
)

var validScenarioName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func ValidateScenarioName(name string) error {
	if name == "" {
		return errors.New("scenario name must not be empty")
	}

	if len(name) > 128 {
		return fmt.Errorf("scenario name must be 128 characters or fewer (got %d)", len(name))
	}

	if !validScenarioName.MatchString(name) {
		return fmt.Errorf("scenario name %q is invalid: must be lowercase alphanumeric with hyphens only", name)
	}

	return nil
}

// unstarAndDelete clears a session's Starred flag, then hard-deletes it.
// Scenario teardown and rollback/cleanup paths must use this rather than calling
// Delete directly: a scenario session may be created with star = true (issue
// #1046), and Delete refuses starred sessions. Without the unstar, a rollback
// after a sibling fails — or a scenario deleted mid-create — would strand the
// starred member as an orphan and leave a partial scenario record.
func (sm *SessionManager) unstarAndDelete(id string) error {
	sm.mu.Lock()
	if s, ok := sm.state.Sessions[id]; ok {
		s.Starred = false
	}
	sm.mu.Unlock()

	return sm.Delete(id)
}

type scenarioSharedSource struct {
	id           string
	repoPath     string
	worktreePath string
}

// resolveScenarioSharedSourceLocked resolves the one reusable session behind a
// shared scenario member. Stopped sessions remain reusable because Stop keeps
// their state and worktree intact; transient, errored, and soft-deleted rows do
// not. Caller must hold sm.mu for reading.
func (sm *SessionManager) resolveScenarioSharedSourceLocked(name string) (scenarioSharedSource, error) {
	var (
		candidates  []*SessionState
		deleted     int
		unavailable []SessionStatus
	)

	for _, existing := range sm.state.Sessions {
		if existing.Name != name {
			continue
		}

		if existing.IsSoftDeleted() {
			deleted++
			continue
		}

		switch existing.Status {
		case StatusRunning, StatusStopped:
			candidates = append(candidates, existing)
		default:
			unavailable = append(unavailable, existing.Status)
		}
	}

	switch len(candidates) {
	case 0:
		switch {
		case deleted == 1 && len(unavailable) == 0:
			return scenarioSharedSource{}, fmt.Errorf("shared session %q is deleted", name)
		case deleted > 1 && len(unavailable) == 0:
			return scenarioSharedSource{}, fmt.Errorf("shared session %q is unavailable: %d matching sessions are deleted", name, deleted)
		case len(unavailable) == 1 && deleted == 0:
			return scenarioSharedSource{}, fmt.Errorf("shared session %q is %s; only running or stopped sessions can be shared", name, unavailable[0])
		default:
			return scenarioSharedSource{}, fmt.Errorf("shared session %q: no running or stopped session with that name exists", name)
		}
	case 1:
		// Continue below.
	default:
		return scenarioSharedSource{}, fmt.Errorf("shared session %q is ambiguous: %d running or stopped sessions have that name", name, len(candidates))
	}

	source := candidates[0]

	return scenarioSharedSource{
		id:           source.ID,
		repoPath:     source.RepoPath,
		worktreePath: source.WorktreePath,
	}, nil
}

// snapshotScenarioSharedSources performs the authoritative state-only lookup
// under the manager lock. Filesystem validation is deliberately left to the
// caller so scenario startup never performs slow I/O while holding sm.mu.
func (sm *SessionManager) snapshotScenarioSharedSources(sessions []protocol.ScenarioSessionInput) ([]scenarioSharedSource, error) {
	sources := make([]scenarioSharedSource, len(sessions))

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for i, session := range sessions {
		if !session.Shared {
			continue
		}

		source, err := sm.resolveScenarioSharedSourceLocked(session.Name)
		if err != nil {
			return nil, err
		}

		sources[i] = source
	}

	return sources, nil
}

func validateScenarioMirrorWorktree(memberName, targetName, worktreePath string) error {
	if worktreePath == "" {
		return fmt.Errorf("session %q: mirror target %q has no worktree to mirror", memberName, targetName)
	}

	info, err := os.Stat(worktreePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("session %q: mirror target %q worktree %q no longer exists", memberName, targetName, worktreePath)
		}

		return fmt.Errorf("session %q: mirror target %q worktree %q is unavailable: %w", memberName, targetName, worktreePath, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("session %q: mirror target %q worktree %q is not a directory", memberName, targetName, worktreePath)
	}

	return nil
}

func (sm *SessionManager) StartScenario(msg protocol.ScenarioStartMsg, rows, cols uint16) (*ScenarioState, error) {
	if err := ValidateScenarioName(msg.Name); err != nil {
		return nil, err
	}

	if len(msg.Sessions) == 0 {
		return nil, errors.New("scenario must define at least one session")
	}

	if err := scenariofile.ValidateSessionContracts(msg.Sessions, sm.scenarioTodoTitleLimit()); err != nil {
		return nil, err
	}

	normalizedPolicy, err := normalizeProtocolScenarioPolicy(msg, sm.scenarioTodoTitleLimit())
	if err != nil {
		return nil, err
	}

	if normalizedPolicy != nil && sm.todos == nil {
		for _, member := range msg.Sessions {
			if strings.TrimSpace(member.Task) != "" {
				return nil, errors.New("todo store is required for scenario runtime-policy task contracts")
			}
		}
	}

	// Validate caller is orchestrator (snapshot under lock).
	sm.mu.RLock()
	callerSess := sm.state.Sessions[msg.CallerSessionID]

	var callerSystemKind string
	if callerSess != nil {
		callerSystemKind = callerSess.SystemKind
	}

	sm.mu.RUnlock()

	if callerSess == nil {
		return nil, fmt.Errorf("caller session %q not found", msg.CallerSessionID)
	}

	if callerSystemKind != SystemKindOrchestrator {
		return nil, errors.New("only the orchestrator session can start scenarios")
	}

	// Validate session definitions and build the scenario-local mirror graph.
	// Mirror values are member names only; they never reach Create as an
	// unscoped name or path.
	var (
		seenNames     = make(map[string]bool, len(msg.Sessions))
		mirrorMembers = make([]scenariofile.MirrorMember, len(msg.Sessions))
	)

	for i, s := range msg.Sessions {
		if s.Name == "" {
			return nil, fmt.Errorf("session %d: name is required", i)
		}

		if err := ValidateSessionName(s.Name); err != nil {
			return nil, fmt.Errorf("session %q: %w", s.Name, err)
		}

		if seenNames[s.Name] {
			return nil, fmt.Errorf("duplicate session name %q", s.Name)
		}

		seenNames[s.Name] = true

		if s.Repo == "" && !s.Shared && s.Mirror == "" {
			return nil, fmt.Errorf("session %q: repo is required", s.Name)
		}

		mirrorMembers[i] = scenariofile.MirrorMember{
			Name: s.Name, Mirror: s.Mirror, Repo: s.Repo, Base: s.Base,
			Shared: s.Shared, Includes: len(s.Includes),
		}
	}

	mirrorDepths, err := scenariofile.ValidateMirrorMembers(mirrorMembers)
	if err != nil {
		return nil, err
	}

	var (
		memberIndexes  = make(map[string]int, len(msg.Sessions))
		maxMirrorDepth int
	)

	for i, s := range msg.Sessions {
		memberIndexes[s.Name] = i
		if mirrorDepths[i] > maxMirrorDepth {
			maxMirrorDepth = mirrorDepths[i]
		}
	}

	if err := validateScenarioResultDeclarations(msg.Name, msg.Sessions); err != nil {
		return nil, err
	}

	// Validate scenario-embedded triggers against the roles this scenario defines
	// (issue #1027). Done authoritatively here — before any filesystem work — so a
	// client can't bypass the CLI's check. They are only associated with the
	// scenario once the two-phase start below succeeds.
	definedRoles := make(map[string]bool, len(msg.Sessions))
	definedMembers := make(map[string]bool, len(msg.Sessions))
	definedOwnedMembers := make(map[string]bool, len(msg.Sessions))

	for _, s := range msg.Sessions {
		// Shared members keep their original scenario identity, so a watch trigger
		// can never bind to them — exclude their role from the selectable set (a
		// shared member is still a valid delivery target by name).
		if s.Role != "" && !s.Shared {
			definedRoles[s.Role] = true
		}

		if s.Name != "" {
			definedMembers[s.Name] = true
			if !s.Shared {
				definedOwnedMembers[s.Name] = true
			}
		}
	}

	if err := scenariofile.ValidateScenarioTriggers(msg.Triggers, definedRoles, definedMembers, definedOwnedMembers); err != nil {
		return nil, err
	}

	if err := config.ValidateScenarioLifecycle(msg.Lifecycle); err != nil {
		return nil, err
	}

	if err := scenariofile.ValidateSessionDependencies(msg.Sessions); err != nil {
		return nil, err
	}

	cfg := sm.Config()
	repoRoots := make([]string, len(msg.Sessions))

	// Validate agents, sandbox enforcement, and explicitly supplied repos before
	// reserving anything. Mirrored members have no repo path to validate: their
	// effective repo/worktree is derived from the target during reservation.
	for i, s := range msg.Sessions {
		agentName := s.Agent
		if agentName == "" {
			agentName = cfg.DefaultAgent
		}

		if _, ok := cfg.Agents[agentName]; !ok {
			return nil, fmt.Errorf("session %q: unknown agent %q", s.Name, agentName)
		}

		if s.Mirror != "" {
			sandboxed, sandboxErr := sm.resolveSandboxFromConfig(cfg, agentName)
			if sandboxErr != nil {
				return nil, fmt.Errorf("session %q: mirror sandbox unavailable: %w", s.Name, sandboxErr)
			}

			if !sandboxed {
				return nil, fmt.Errorf("session %q: mirror requires sandbox to be enabled so the source worktree is read-only", s.Name)
			}
		}

		if s.Repo == "" {
			continue
		}

		if !git.IsInsideGitRepo(s.Repo) {
			return nil, fmt.Errorf("session %q: repo %q is not a git repository", s.Name, s.Repo)
		}

		repoRoot, repoErr := git.RepoRootPath(s.Repo)
		if repoErr != nil {
			return nil, fmt.Errorf("session %q: resolve repo root: %w", s.Name, repoErr)
		}

		if !cfg.RepoPathAllowed(repoRoot) {
			return nil, fmt.Errorf("session %q: repo %q is not under any allowed_repo_paths", s.Name, s.Repo)
		}

		repoRoots[i] = repoRoot
	}

	// Resolve shared members before reserving scenario state. Running and
	// stopped sessions are both eligible; soft-deleted or transient rows remain
	// unavailable. Mirror worktrees are checked outside sm.mu so an already-
	// cleaned stopped session fails before any scenario-owned member starts.
	sharedSources, err := sm.snapshotScenarioSharedSources(msg.Sessions)
	if err != nil {
		return nil, err
	}

	for i, session := range msg.Sessions {
		if !session.Shared {
			continue
		}

		source := sharedSources[i]
		// RepoRootPath returns the canonical Git top-level path for an explicit
		// repo, and SessionState.RepoPath stores that same value.
		if repoRoots[i] != "" && filepath.Clean(repoRoots[i]) != filepath.Clean(source.repoPath) {
			return nil, fmt.Errorf("shared session %q: configured repo %q does not match selected session repo %q", session.Name, repoRoots[i], source.repoPath)
		}

		repoRoots[i] = source.repoPath
	}

	validatedSharedRoots := make(map[int]bool)

	for depth := 1; depth <= maxMirrorDepth; depth++ {
		for i, session := range msg.Sessions {
			if mirrorDepths[i] != depth {
				continue
			}

			target := memberIndexes[session.Mirror]
			repoRoots[i] = repoRoots[target]

			root := target
			for msg.Sessions[root].Mirror != "" {
				root = memberIndexes[msg.Sessions[root].Mirror]
			}

			if !msg.Sessions[root].Shared || validatedSharedRoots[root] {
				continue
			}

			if err := validateScenarioMirrorWorktree(session.Name, msg.Sessions[root].Name, sharedSources[root].worktreePath); err != nil {
				return nil, err
			}

			validatedSharedRoots[root] = true
		}
	}

	// Serialize every lifecycle operation that can discover this scenario after
	// its reserve record becomes visible. Generating the stable ID before the
	// reserve means stop/delete/add will wait for start to commit or roll back.
	scenarioID := "sc-" + generateID()
	unlockScenario := sm.lockScenarioPolicy(scenarioID)

	defer unlockScenario()

	// --- Reserve phase: lock, validate no collisions, create scenario + placeholders ---
	sm.mu.Lock()

	// Check scenario name uniqueness.
	for _, sc := range sm.state.Scenarios {
		if sc.Name == msg.Name {
			sm.mu.Unlock()
			return nil, fmt.Errorf("scenario %q already exists (id: %s)", msg.Name, sc.ID)
		}
	}

	// Check session name uniqueness against existing sessions (shared sessions may reuse).
	for _, s := range msg.Sessions {
		if s.Shared {
			continue
		}

		for _, existing := range sm.state.Sessions {
			// Soft-deleted sessions are hidden and scheduled for purge, so a
			// scenario may reuse their name — trash must not block a new scenario.
			if existing.IsSoftDeleted() {
				continue
			}

			if existing.Name == s.Name {
				sm.mu.Unlock()
				return nil, fmt.Errorf("session name %q already exists", s.Name)
			}
		}
	}

	now := time.Now().UTC()

	scenarioSessions := make([]ScenarioSession, len(msg.Sessions))
	sessionIDs := make([]string, len(msg.Sessions))
	sharedReused := make([]bool, len(msg.Sessions))
	seenResultDestinations := make(map[string]string)

	// Resolve shared members authoritatively while holding the state lock. A
	// mirror reference never searches global daemon state itself; it resolves to
	// one of these scenario member indexes, and that member owns the only allowed
	// global binding. Re-check the preflight snapshot so a concurrent lifecycle
	// change cannot silently substitute a different source or worktree.
	for i, s := range msg.Sessions {
		if !s.Shared {
			continue
		}

		source, resolveErr := sm.resolveScenarioSharedSourceLocked(s.Name)
		if resolveErr != nil {
			sm.mu.Unlock()
			return nil, resolveErr
		}

		if source != sharedSources[i] {
			sm.mu.Unlock()
			return nil, fmt.Errorf("shared session %q changed during scenario start; try again", s.Name)
		}

		sessionIDs[i] = source.id
		sharedReused[i] = true
	}

	for i, s := range msg.Sessions {
		agentName := s.Agent
		if agentName == "" {
			agentName = cfg.DefaultAgent
		}

		repoName := ""
		if repoRoots[i] != "" {
			repoName = filepath.Base(repoRoots[i])
		}

		scenarioSessions[i] = ScenarioSession{
			Name: s.Name, Mirror: s.Mirror, Role: s.Role, Prompt: s.Prompt, Task: s.Task,
			Repo: repoName, Agent: agentName, Model: s.Model, Shared: s.Shared,
		}
		if normalizedPolicy != nil {
			scenarioSessions[i].Policy = newScenarioMemberPolicyState(normalizedPolicy.Members[i])
		}

		if s.Shared {
			results, compileErr := compileScenarioResults(
				scenarioID, msg.Name, sessionIDs[i], s.Name, s.Results, seenResultDestinations,
			)
			if compileErr != nil {
				for previousIndex, previousID := range sessionIDs[:i] {
					if !sharedReused[previousIndex] {
						delete(sm.state.Sessions, previousID)
					}
				}
				sm.mu.Unlock()

				return nil, fmt.Errorf("session %q: %w", s.Name, compileErr)
			}

			scenarioSessions[i].Results = results

			continue
		}

		id := sm.uniqueSessionIDLocked()
		sessionIDs[i] = id

		results, compileErr := compileScenarioResults(
			scenarioID, msg.Name, id, s.Name, s.Results, seenResultDestinations,
		)
		if compileErr != nil {
			for previousIndex, previousID := range sessionIDs[:i] {
				if !sharedReused[previousIndex] {
					delete(sm.state.Sessions, previousID)
				}
			}
			sm.mu.Unlock()

			return nil, fmt.Errorf("session %q: %w", s.Name, compileErr)
		}

		scenarioSessions[i].Results = results

		sm.state.Sessions[id] = &SessionState{
			ID:              id,
			ParentID:        msg.CallerSessionID,
			Name:            s.Name,
			RepoPath:        repoRoots[i],
			RepoName:        repoName,
			Agent:           agentName,
			Model:           s.Model,
			AgentHooks:      s.AgentHooks,
			Status:          StatusCreating,
			CreatedAt:       now,
			StatusChangedAt: now,
			ScenarioID:      scenarioID,
			ScenarioName:    msg.Name,
			ScenarioRole:    s.Role,
			ScenarioGoal:    msg.Goal,
		}
	}

	scenario := &ScenarioState{
		ID:             scenarioID,
		Name:           msg.Name,
		OrchestratorID: msg.CallerSessionID,
		Goal:           msg.Goal,
		SessionIDs:     sessionIDs,
		Sessions:       scenarioSessions,
		CreatedAt:      now,
		Lifecycle:      msg.Lifecycle,
		Policy:         newScenarioPolicyState(normalizedPolicy),
	}
	sm.state.Scenarios[scenarioID] = scenario

	if err := sm.saveState(); err != nil {
		// Rollback: remove all placeholders and scenario.
		for i, id := range sessionIDs {
			if !sharedReused[i] {
				delete(sm.state.Sessions, id)
			}
		}

		delete(sm.state.Scenarios, scenarioID)
		sm.mu.Unlock()

		return nil, fmt.Errorf("persist scenario state: %w", err)
	}

	sm.mu.Unlock()

	// --- Start phase: create dependency waves concurrently ---
	// Remove all placeholders first, then hand each reserved ID back to Create
	// (via CreateOpts.ID) so the created session keeps the ID we reserved and
	// ScenarioState.SessionIDs stays valid without a rewrite. The delete is
	// still needed because Create re-reserves the ID itself and would reject an
	// existing entry as a collision. This is not a held reservation across the
	// gap: between the delete here and Create's re-reserve the ID is briefly
	// free, so a concurrent Create using the same ID would win and this
	// scenario's Create would fail and roll back — the same window that existed
	// before IDs were made stable. Shared-reused sessions already have real IDs
	// and don't need creation. Mirror dependency depths then ensure every source
	// wave is running before its readers are handed to the normal Create path.

	sm.mu.Lock()
	for i, id := range sessionIDs {
		if sharedReused[i] {
			continue
		}

		delete(sm.state.Sessions, id)
	}

	_ = sm.saveState()
	sm.mu.Unlock()

	type createResult struct {
		index int
		sess  SessionState
		err   error
	}

	var (
		startedIDs  []string
		startErrors []string
	)

	for depth := 0; depth <= maxMirrorDepth; depth++ {
		var (
			results = make([]createResult, len(msg.Sessions))
			wg      sync.WaitGroup
		)

		for i, s := range msg.Sessions {
			if sharedReused[i] || mirrorDepths[i] != depth {
				continue
			}

			wg.Add(1)
			go func(idx int, s protocol.ScenarioSessionInput) {
				defer wg.Done()

				agentName := s.Agent
				if agentName == "" {
					agentName = cfg.DefaultAgent
				}

				scenarioEnv := map[string]string{
					"GRAITH_SCENARIO":      scenarioID,
					"GRAITH_SCENARIO_NAME": msg.Name,
				}
				if s.Role != "" {
					scenarioEnv["GRAITH_SCENARIO_ROLE"] = s.Role
				}

				if msg.Goal != "" {
					scenarioEnv["GRAITH_SCENARIO_GOAL"] = msg.Goal
				}

				mirrorSourceID := ""
				if s.Mirror != "" {
					mirrorSourceID = sessionIDs[memberIndexes[s.Mirror]]
				}

				sess, createErr := sm.Create(CreateOpts{
					ID: sessionIDs[idx], Name: s.Name, AgentName: agentName,
					RepoPath: repoRoots[idx], Mirror: mirrorSourceID,
					BaseBranch: s.Base, Prompt: s.StartupPrompt(), Model: s.Model,
					ParentID: msg.CallerSessionID, AgentHooks: s.AgentHooks,
					Includes: s.Includes, Starred: s.Star,
					ForcePTY: normalizedPolicy != nil && normalizedPolicy.Members[idx].Retries > 0,
					Rows:     rows, Cols: cols,
					EnvExtra: []map[string]string{scenarioEnv},
				})
				results[idx] = createResult{index: idx, sess: sess, err: createErr}
			}(i, s)
		}

		wg.Wait()

		var waveStarted []createResult

		for i, result := range results {
			if sharedReused[i] || mirrorDepths[i] != depth {
				continue
			}

			if result.err != nil {
				startErrors = append(startErrors, fmt.Sprintf("session %q: %v", msg.Sessions[i].Name, result.err))
				continue
			}

			startedIDs = append(startedIDs, result.sess.ID)
			sessionIDs[i] = result.sess.ID
			waveStarted = append(waveStarted, result)
		}

		if len(waveStarted) > 0 {
			sm.mu.Lock()
			for _, result := range waveStarted {
				if created, ok := sm.state.Sessions[result.sess.ID]; ok {
					created.ScenarioID = scenarioID
					created.ScenarioName = msg.Name
					created.ScenarioRole = msg.Sessions[result.index].Role
					created.ScenarioGoal = msg.Goal
				}
			}

			_ = sm.saveState()
			sm.mu.Unlock()
		}

		if len(startErrors) > 0 {
			break
		}
	}

	if len(startErrors) > 0 {
		// Update scenario with real session IDs so retry/cleanup can find them.
		sm.mu.Lock()
		if sc := sm.state.Scenarios[scenarioID]; sc != nil {
			sc.SessionIDs = sessionIDs
		}

		_ = sm.saveState()
		sm.mu.Unlock()

		// Rollback: stop every started member before deleting any of them. A
		// source may have readers in a later dependency wave; stopping the whole
		// set first ensures no live reader observes its source worktree being
		// removed. Delete in reverse creation order so mirrors go before roots.
		var rollbackErrors []string

		for _, id := range startedIDs {
			if err := sm.Stop(id); err != nil {
				sm.log.Warn("scenario rollback: stop failed", "session", id, "err", err)
			}
		}

		for i := len(startedIDs) - 1; i >= 0; i-- {
			id := startedIDs[i]
			if err := sm.unstarAndDelete(id); err != nil {
				sm.log.Warn("scenario rollback: delete failed", "session", id, "err", err)
				rollbackErrors = append(rollbackErrors, id)
			}
		}

		sm.mu.Lock()
		if len(rollbackErrors) == 0 {
			delete(sm.state.Scenarios, scenarioID)
		}

		_ = sm.saveState()
		sm.mu.Unlock()

		return nil, fmt.Errorf("failed to create %d session(s): %s", len(startErrors), strings.Join(startErrors, "; "))
	}

	// Update the scenario with final session IDs.
	sm.mu.Lock()

	scenario = sm.state.Scenarios[scenarioID]
	if scenario == nil {
		sm.mu.Unlock()

		for _, id := range startedIDs {
			if err := sm.stopWithReason(id, StopReasonUser, "scenario-rollback"); err != nil {
				sm.log.Warn("scenario deleted during creation: stop failed", "session", id, "err", err)
			}

			_ = sm.unstarAndDelete(id)
		}

		return nil, fmt.Errorf("scenario %q was deleted during session creation", msg.Name)
	}

	scenario.SessionIDs = sessionIDs
	_ = sm.saveState()
	sm.mu.Unlock()

	// Seed all assigned task items and their member-name dependency graph in one
	// store transaction. A seed failure creates no rows and rolls back the
	// newly-created scenario members, preserving scenario start's all-or-none
	// contract.
	seededTodoIDs, err := sm.seedScenarioTodos(scenarioID, sessionIDs, msg.Sessions)
	if err != nil {
		rollbackErrors := sm.rollbackScenarioAfterSeedFailure(scenarioID, sessionIDs, sharedReused)
		if len(rollbackErrors) > 0 {
			return nil, fmt.Errorf("seed scenario todos: %w (rollback failed for: %s)", err, strings.Join(rollbackErrors, ", "))
		}

		return nil, fmt.Errorf("seed scenario todos: %w", err)
	}

	// Activate scenario-embedded triggers only after the todo graph commits. A
	// scenario rolled back for seed failure therefore never exposes watchers.
	sm.mu.Lock()

	scenario = sm.state.Scenarios[scenarioID]

	if scenario == nil {
		sm.mu.Unlock()
		sm.removeScenarioTodos(seededTodoIDs)

		return nil, fmt.Errorf("scenario %q was deleted during todo seeding", msg.Name)
	}

	scenario.Triggers = msg.Triggers
	activateScenarioPolicy(scenario, sm.scenarioPolicyTime())

	if err := sm.saveState(); err != nil {
		delete(sm.state.Scenarios, scenarioID)
		_ = sm.saveState()
		sm.mu.Unlock()
		sm.removeScenarioTodos(seededTodoIDs)

		for _, id := range startedIDs {
			if stopErr := sm.stopWithReason(id, StopReasonUser, "scenario-rollback"); stopErr != nil {
				sm.log.Warn("scenario activation rollback: stop failed", "session", id, "err", stopErr)
			}

			if deleteErr := sm.unstarAndDelete(id); deleteErr != nil {
				sm.log.Warn("scenario activation rollback: delete failed", "session", id, "err", deleteErr)
			}
		}

		return nil, fmt.Errorf("persist scenario activation: %w", err)
	}
	sm.mu.Unlock()

	// --- Manifest phase: build and publish manifest to each session's inbox ---
	repos := make([]string, len(scenarioSessions))
	for i, ss := range scenarioSessions {
		repos[i] = ss.Repo
	}

	for i, id := range sessionIDs {
		manifest := sm.buildManifest(scenarioID, msg, repos, sessionIDs, i)

		manifestJSON, err := json.Marshal(manifest)
		if err != nil {
			sm.log.Error("failed to marshal scenario manifest", "session", id, "err", err)
			continue
		}

		stream := "inbox:" + id
		if sm.messages != nil {
			_, err = sm.messages.Publish(PublishOpts{Stream: stream, SenderID: msg.CallerSessionID, SenderName: "orchestrator", Body: string(manifestJSON)})
			if err != nil {
				sm.log.Error("failed to publish scenario manifest", "session", id, "err", err)
			}
		}
	}

	// Persist manifest to shared store.
	sm.persistManifest(scenarioID, msg, repos, sessionIDs)

	return scenario, nil
}

type scenarioManifest struct {
	Version      int                       `json:"version"`
	ScenarioID   string                    `json:"scenario_id"`
	ScenarioName string                    `json:"scenario_name"`
	Goal         string                    `json:"goal"`
	You          scenarioManifestSelf      `json:"you"`
	Siblings     []scenarioManifestSibling `json:"siblings"`
	Orchestrator scenarioManifestOrch      `json:"orchestrator"`
}

type scenarioManifestSelf struct {
	Name      string                   `json:"name"`
	SessionID string                   `json:"session_id"`
	Mirror    string                   `json:"mirror,omitempty"`
	Role      string                   `json:"role"`
	Prompt    string                   `json:"prompt,omitempty"`
	Task      string                   `json:"task"`
	Results   []scenarioManifestResult `json:"results,omitempty"`
}

type scenarioManifestSibling struct {
	Name      string                   `json:"name"`
	SessionID string                   `json:"session_id"`
	Mirror    string                   `json:"mirror,omitempty"`
	Role      string                   `json:"role"`
	Repo      string                   `json:"repo"`
	Results   []scenarioManifestResult `json:"results,omitempty"`
}

type scenarioManifestResult struct {
	Name        string `json:"name"`
	Format      string `json:"format"`
	Destination string `json:"destination"`
	Required    bool   `json:"required,omitempty"`
}

type scenarioManifestOrch struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
}

func (sm *SessionManager) buildManifest(scenarioID string, msg protocol.ScenarioStartMsg, repos []string, sessionIDs []string, selfIndex int) scenarioManifest {
	self := msg.Sessions[selfIndex]

	var siblings []scenarioManifestSibling

	for j, s := range msg.Sessions {
		if j == selfIndex {
			continue
		}

		repo := ""
		if j < len(repos) {
			repo = repos[j]
		}

		siblings = append(siblings, scenarioManifestSibling{
			Name:      s.Name,
			SessionID: sessionIDs[j],
			Mirror:    s.Mirror,
			Role:      s.Role,
			Repo:      repo,
			Results:   sm.manifestScenarioResults(scenarioID, msg.Name, sessionIDs[j], s),
		})
	}

	return scenarioManifest{
		Version:      1,
		ScenarioID:   scenarioID,
		ScenarioName: msg.Name,
		Goal:         msg.Goal,
		You: scenarioManifestSelf{
			Name:      self.Name,
			SessionID: sessionIDs[selfIndex],
			Mirror:    self.Mirror,
			Role:      self.Role,
			Prompt:    self.StartupPrompt(),
			Task:      self.Task,
			Results:   sm.manifestScenarioResults(scenarioID, msg.Name, sessionIDs[selfIndex], self),
		},
		Siblings: siblings,
		Orchestrator: scenarioManifestOrch{
			SessionID: msg.CallerSessionID,
			Name:      "orchestrator",
		},
	}
}

func (sm *SessionManager) manifestScenarioResults(
	scenarioID, scenarioName, sessionID string,
	session protocol.ScenarioSessionInput,
) []scenarioManifestResult {
	if len(session.Results) == 0 {
		return nil
	}

	results := make([]scenarioManifestResult, 0, len(session.Results))
	for _, spec := range session.Results {
		destination, err := renderScenarioResultDestination(
			scenarioID, scenarioName, sessionID, session.Name, spec.Name, spec.Store,
		)
		if err != nil {
			sm.log.Error("failed to render validated scenario result destination",
				"scenario", scenarioName,
				"session", session.Name,
				"result", spec.Name,
				"err", err,
			)

			continue
		}

		results = append(results, scenarioManifestResult{
			Name: spec.Name, Format: spec.Format, Destination: destination, Required: spec.Required,
		})
	}

	return results
}

func (sm *SessionManager) persistManifest(scenarioID string, msg protocol.ScenarioStartMsg, repos []string, sessionIDs []string) {
	storeDir := store.SharedStorePath(sm.paths.DataDir)
	if err := store.Init(storeDir); err != nil {
		sm.log.Error("failed to init shared store for manifest", "err", err)
		return
	}

	for i := range msg.Sessions {
		manifest := sm.buildManifest(scenarioID, msg, repos, sessionIDs, i)

		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			sm.log.Error("failed to marshal manifest for store", "err", err)
			continue
		}

		key := fmt.Sprintf("scenarios/%s/manifest-%s.json", scenarioID, msg.Sessions[i].Name)
		if err := store.Put(storeDir, key, string(data)); err != nil {
			sm.log.Error("failed to persist manifest", "key", key, "err", err)
		}
	}
}

func (sm *SessionManager) StopScenario(name string) ([]string, error) {
	return sm.StopScenarioContext(context.Background(), name)
}

func (sm *SessionManager) StopScenarioContext(ctx context.Context, name string) ([]string, error) {
	resolvedID, ok := sm.scenarioIDByName(name)
	if !ok {
		return nil, fmt.Errorf("scenario %q not found", name)
	}

	unlock, err := sm.lockScenarioPolicyContext(ctx, resolvedID)
	if err != nil {
		return nil, fmt.Errorf("wait for scenario lifecycle: %w", err)
	}
	defer unlock()

	sm.mu.Lock()

	var (
		sessionIDs []string
		sharedSet  map[int]bool
		scenarioID string
	)

	for id, sc := range sm.state.Scenarios {
		if id == resolvedID && sc.Name == name {
			sessionIDs = make([]string, len(sc.SessionIDs))
			copy(sessionIDs, sc.SessionIDs)

			scenarioID = id

			if sc.Policy != nil {
				wasPaused := sc.Policy.Paused

				sc.Policy.Paused = true
				if err := sm.saveState(); err != nil {
					sc.Policy.Paused = wasPaused
					sm.mu.Unlock()

					return nil, fmt.Errorf("persist scenario policy suspension: %w", err)
				}
			}

			sharedSet = make(map[int]bool, len(sc.Sessions))
			for i, ss := range sc.Sessions {
				if ss.Shared {
					sharedSet[i] = true
				}
			}

			break
		}
	}

	sm.mu.Unlock()

	if sessionIDs == nil {
		return nil, fmt.Errorf("scenario %q not found", name)
	}

	sm.cancelScenarioCompletion(scenarioID, "cancelled by manual scenario stop")

	// Tear down any scenario-embedded trigger watchers up front — the sessions
	// they bind to are about to stop. The definitions stay on ScenarioState so a
	// later resume rebinds them.
	sm.teardownScenarioTriggerBindings(scenarioID)

	var stopped []string

	for i, id := range sessionIDs {
		if sharedSet[i] {
			continue
		}

		sm.mu.RLock()
		sess := sm.state.Sessions[id]

		var status SessionStatus
		if sess != nil {
			status = sess.Status
		}

		sm.mu.RUnlock()

		if sess == nil || status != StatusRunning {
			continue
		}

		if err := sm.stopWithReason(id, StopReasonUser, "scenario-stop"); err != nil {
			sm.log.Warn("failed to stop scenario session", "session", id, "err", err)
			continue
		}

		stopped = append(stopped, id)
	}

	return stopped, nil
}

func (sm *SessionManager) DeleteScenario(name string) ([]string, error) {
	return sm.DeleteScenarioContext(context.Background(), name)
}

func (sm *SessionManager) DeleteScenarioContext(ctx context.Context, name string) ([]string, error) {
	resolvedID, ok := sm.scenarioIDByName(name)
	if !ok {
		return nil, fmt.Errorf("scenario %q not found", name)
	}

	unlock, err := sm.lockScenarioPolicyContext(ctx, resolvedID)
	if err != nil {
		return nil, fmt.Errorf("wait for scenario lifecycle: %w", err)
	}
	defer unlock()

	sm.mu.Lock()

	var (
		sessionIDs []string
		scenarioID string
		sharedSet  map[int]bool
	)

	for id, sc := range sm.state.Scenarios {
		if id == resolvedID && sc.Name == name {
			sessionIDs = make([]string, len(sc.SessionIDs))
			copy(sessionIDs, sc.SessionIDs)

			scenarioID = id

			if sc.Policy != nil && !sc.Policy.Paused {
				sc.Policy.Paused = true
				if err := sm.saveState(); err != nil {
					sc.Policy.Paused = false
					sm.mu.Unlock()

					return nil, fmt.Errorf("persist scenario policy suspension before delete: %w", err)
				}
			}

			sharedSet = make(map[int]bool, len(sc.Sessions))
			for i, ss := range sc.Sessions {
				if ss.Shared {
					sharedSet[i] = true
				}
			}

			break
		}
	}

	sm.mu.Unlock()

	if sessionIDs == nil {
		return nil, fmt.Errorf("scenario %q not found", name)
	}

	sm.cancelScenarioCompletion(scenarioID, "cancelled by manual scenario delete")

	// Tear down the scenario's trigger watchers up front (as StopScenario does),
	// so a live binding on a still-running member can't fire mid-teardown while
	// sessions are stopped one at a time. pruneScenarioTriggerState at the end is
	// idempotent over an already-empty binding set.
	sm.teardownScenarioTriggerBindings(scenarioID)

	// Stop running sessions first (skip shared sessions).
	for i, id := range sessionIDs {
		if sharedSet[i] {
			continue
		}

		sm.mu.RLock()
		sess := sm.state.Sessions[id]

		var status SessionStatus
		if sess != nil {
			status = sess.Status
		}

		sm.mu.RUnlock()

		if sess != nil && status == StatusRunning {
			_ = sm.stopWithReason(id, StopReasonUser, "scenario-delete")
		}
	}

	// Delete each session (skip shared sessions).
	var (
		deleted      []string
		deleteErrors []string
	)

	for i, id := range sessionIDs {
		if sharedSet[i] {
			continue
		}

		sm.mu.RLock()
		_, ok := sm.state.Sessions[id]
		sm.mu.RUnlock()

		if !ok {
			deleted = append(deleted, id)
			continue
		}

		if err := sm.unstarAndDelete(id); err != nil {
			sm.log.Warn("failed to delete scenario session", "session", id, "err", err)
			deleteErrors = append(deleteErrors, id)

			continue
		}

		deleted = append(deleted, id)
	}

	// Only remove the scenario record if all sessions were cleaned up.
	recordRemoved := len(deleteErrors) == 0

	sm.mu.Lock()
	removedScenario := sm.state.Scenarios[scenarioID]

	wasDirty := sm.scenarioPolicyDirty[scenarioID]
	if recordRemoved {
		delete(sm.state.Scenarios, scenarioID)
		delete(sm.scenarioPolicyDirty, scenarioID)
	}

	persistErr := sm.saveState()
	if persistErr != nil && recordRemoved {
		sm.state.Scenarios[scenarioID] = removedScenario
		if wasDirty {
			sm.scenarioPolicyDirty[scenarioID] = true
		}
	}
	sm.mu.Unlock()

	if persistErr != nil {
		return deleted, fmt.Errorf("persist scenario deletion: %w", persistErr)
	}

	// Drop the scenario's trigger bindings and persisted runtime once its record
	// is gone, so scenario churn can't leak trigger state.
	if recordRemoved {
		sm.pruneScenarioTriggerState(scenarioID)
	}

	if len(deleteErrors) > 0 {
		return deleted, fmt.Errorf("failed to delete %d session(s): %v — scenario record kept for retry", len(deleteErrors), deleteErrors)
	}

	return deleted, nil
}

func (sm *SessionManager) ScenarioStatus(name string) (*protocol.ScenarioRecord, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var scenario *ScenarioState

	for _, sc := range sm.state.Scenarios {
		if sc.Name == name {
			scenario = sc
			break
		}
	}

	if scenario == nil {
		return nil, fmt.Errorf("scenario %q not found", name)
	}

	return sm.buildScenarioRecord(scenario), nil
}

func (sm *SessionManager) ListScenarios() []protocol.ScenarioRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	records := make([]protocol.ScenarioRecord, 0, len(sm.state.Scenarios))
	for _, sc := range sm.state.Scenarios {
		records = append(records, *sm.buildScenarioRecord(sc))
	}

	return records
}

func (sm *SessionManager) ResumeScenario(name string, rows, cols uint16) ([]string, error) {
	resolvedID, ok := sm.scenarioIDByName(name)
	if !ok {
		return nil, fmt.Errorf("scenario %q not found", name)
	}

	unlock := sm.lockScenarioPolicy(resolvedID)
	defer func() { unlock() }()

	sm.mu.RLock()

	var scenario *ScenarioState

	for id, sc := range sm.state.Scenarios {
		if id == resolvedID && sc.Name == name {
			scenario = sc
			break
		}
	}

	if scenario == nil {
		sm.mu.RUnlock()
		return nil, fmt.Errorf("scenario %q not found", name)
	}

	sessionIDs := make([]string, len(scenario.SessionIDs))
	copy(sessionIDs, scenario.SessionIDs)
	scenarioID := scenario.ID

	shared := make([]bool, len(sessionIDs))
	for i := range sessionIDs {
		if i < len(scenario.Sessions) {
			shared[i] = scenario.Sessions[i].Shared
		}
	}

	sm.mu.RUnlock()

	var resumed []string

	for i, id := range sessionIDs {
		// Shared members are referenced context, not scenario-owned processes.
		// This is especially important for a stopped shared mirror source: bulk
		// resume must leave it stopped just as stop/delete leave it untouched.
		if shared[i] {
			continue
		}

		sm.mu.RLock()
		sess := sm.state.Sessions[id]

		var (
			status      SessionStatus
			softDeleted bool
		)

		if sess != nil {
			status = sess.Status
			softDeleted = sess.IsSoftDeleted()
		}

		sm.mu.RUnlock()

		if sess == nil {
			continue
		}

		// Skip soft-deleted members: they are hidden and scheduled for purge, so
		// a bulk scenario resume must not relaunch them.
		if softDeleted {
			continue
		}

		if status != StatusStopped && status != StatusErrored {
			continue
		}

		var resumeErr error
		if sm.scenarioResume != nil {
			resumeErr = sm.scenarioResume(id, rows, cols)
		} else {
			_, resumeErr = sm.Resume(id, rows, cols)
		}

		if resumeErr != nil {
			sm.log.Warn("failed to resume scenario session", "session", id, "err", resumeErr)
			continue
		}

		resumed = append(resumed, id)
	}

	sm.mu.Lock()
	if current := sm.state.Scenarios[scenarioID]; current != nil && current.Policy != nil {
		wasPaused := current.Policy.Paused

		current.Policy.Paused = false
		if err := sm.saveState(); err != nil {
			current.Policy.Paused = wasPaused
			sm.mu.Unlock()

			var rollbackErrors []string

			for _, id := range resumed {
				if stopErr := sm.stopWithReason(id, StopReasonUser, "scenario-resume-rollback"); stopErr != nil {
					rollbackErrors = append(rollbackErrors, fmt.Sprintf("%s: %v", id, stopErr))
				}
			}

			if len(rollbackErrors) > 0 {
				return nil, fmt.Errorf("persist scenario policy resume: %w (rollback failed: %s)", err, strings.Join(rollbackErrors, "; "))
			}

			return nil, fmt.Errorf("persist scenario policy resume: %w", err)
		}
	}
	sm.mu.Unlock()

	if len(resumed) > 0 {
		sm.republishManifests(scenarioID)
	}

	// Deadlines are immutable wall-clock instants: manual stop and daemon
	// downtime do not extend them. Reconcile immediately after unsuspending so
	// an elapsed deadline is not delayed until the next scheduler tick.
	unlock()
	unlock = func() {}

	sm.reconcileScenarioPoliciesFor(context.Background(), sm.scenarioPolicyTime(), scenarioID)

	return resumed, nil
}

func (sm *SessionManager) AddToScenario(name string, input protocol.ScenarioSessionInput, rows, cols uint16) (*SessionState, error) {
	if err := ValidateSessionName(input.Name); err != nil {
		return nil, err
	}

	if input.Mirror != "" {
		return nil, errors.New("scenario add does not support mirror references; declare mirrored members in the scenario file so the full topology can be preflighted atomically")
	}

	if input.Repo == "" {
		return nil, errors.New("repo is required")
	}

	if err := scenariofile.ValidateSessionContracts(
		[]protocol.ScenarioSessionInput{input}, sm.scenarioTodoTitleLimit(),
	); err != nil {
		return nil, err
	}

	if err := validateScenarioResultDeclarations(name, []protocol.ScenarioSessionInput{input}); err != nil {
		return nil, err
	}

	if input.Shared {
		return nil, errors.New("scenario add cannot add shared sessions")
	}

	cfg := sm.Config()

	agentName := input.Agent
	if agentName == "" {
		agentName = cfg.DefaultAgent
	}

	if _, ok := cfg.Agents[agentName]; !ok {
		return nil, fmt.Errorf("unknown agent %q", agentName)
	}

	repoRoot, err := git.RepoRootPath(input.Repo)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root: %w", err)
	}

	resolvedID, ok := sm.scenarioIDByName(name)
	if !ok {
		return nil, fmt.Errorf("scenario %q not found", name)
	}

	unlock := sm.lockScenarioPolicy(resolvedID)
	defer unlock()

	sm.mu.Lock()

	var (
		scenarioID, orchestratorID, goal string
		dependencyAssignees              []string
		legacyPolicyContracts            []struct{ sessionID, memberName string }
		policyEnabled                    bool
	)

	found := false

	for id, sc := range sm.state.Scenarios {
		if id == resolvedID && sc.Name == name {
			if sc.Policy != nil && sc.Policy.Outcome != "" {
				sm.mu.Unlock()
				return nil, fmt.Errorf("scenario %q is already %s", name, sc.Policy.Outcome)
			}

			if sc.Policy != nil && sc.Policy.Paused {
				sm.mu.Unlock()
				return nil, fmt.Errorf("scenario %q is paused; resume it before adding a member", name)
			}

			normalized, policyErr := normalizeScenarioAddPolicy(sc, input, sm.scenarioTodoTitleLimit())
			if policyErr != nil {
				sm.mu.Unlock()
				return nil, policyErr
			}

			policyEnabled = normalized != nil
			if sc.Policy == nil && normalized != nil {
				for memberIndex, member := range sc.Sessions {
					if strings.TrimSpace(member.Task) == "" {
						continue
					}

					if memberIndex >= len(sc.SessionIDs) || sc.SessionIDs[memberIndex] == "" {
						sm.mu.Unlock()
						return nil, fmt.Errorf("cannot enable runtime policy: existing member %q has no session identity", member.Name)
					}

					legacyPolicyContracts = append(legacyPolicyContracts, struct{ sessionID, memberName string }{
						sessionID: sc.SessionIDs[memberIndex], memberName: member.Name,
					})
				}
			}

			scenarioID = id
			orchestratorID = sc.OrchestratorID
			goal = sc.Goal
			found = true

			if len(input.DependsOn) > 0 && strings.TrimSpace(input.Task) == "" {
				sm.mu.Unlock()
				return nil, fmt.Errorf("session %q: depends_on requires a task", input.Name)
			}

			seenDependencies := make(map[string]bool, len(input.DependsOn))
			for _, dependencyName := range input.DependsOn {
				if dependencyName == input.Name {
					sm.mu.Unlock()
					return nil, fmt.Errorf("session %q: depends_on cannot reference itself", input.Name)
				}

				if seenDependencies[dependencyName] {
					sm.mu.Unlock()
					return nil, fmt.Errorf("session %q: duplicate depends_on member %q", input.Name, dependencyName)
				}

				seenDependencies[dependencyName] = true

				dependencyAssignee := ""

				for i, member := range sc.Sessions {
					if member.Name != dependencyName {
						continue
					}

					if strings.TrimSpace(member.Task) == "" {
						sm.mu.Unlock()
						return nil, fmt.Errorf("session %q: depends_on member %q has no task to track", input.Name, dependencyName)
					}

					if i < len(sc.SessionIDs) {
						dependencyAssignee = sc.SessionIDs[i]
					}

					break
				}

				if dependencyAssignee == "" {
					sm.mu.Unlock()
					return nil, fmt.Errorf("session %q: depends_on member %q is not defined", input.Name, dependencyName)
				}

				dependencyAssignees = append(dependencyAssignees, dependencyAssignee)
			}

			break
		}
	}

	if !found {
		sm.mu.Unlock()
		return nil, fmt.Errorf("scenario %q not found", name)
	}

	for _, existing := range sm.state.Sessions {
		// Soft-deleted sessions don't block name reuse (see scenario start).
		if existing.IsSoftDeleted() {
			continue
		}

		if existing.Name == input.Name {
			sm.mu.Unlock()
			return nil, fmt.Errorf("session name %q already exists", input.Name)
		}
	}
	sm.mu.Unlock()

	if policyEnabled && strings.TrimSpace(input.Task) != "" && sm.todos == nil {
		return nil, errors.New("todo store is required for scenario runtime-policy task contracts")
	}

	if len(legacyPolicyContracts) > 0 {
		if sm.todos == nil {
			return nil, errors.New("cannot enable runtime policy: todo store is required to verify existing member contracts")
		}

		seedIDs, seedErr := sm.todos.ScenarioSeedItemIDs("scenario:" + scenarioID)
		if seedErr != nil {
			return nil, fmt.Errorf("verify existing scenario contracts: %w", seedErr)
		}

		for _, contract := range legacyPolicyContracts {
			if seedIDs[contract.sessionID] == "" {
				return nil, fmt.Errorf("cannot enable runtime policy: existing member %q has no durable seeded todo contract", contract.memberName)
			}
		}
	}

	dependencyTodoIDs := make([]string, 0, len(dependencyAssignees))
	if len(dependencyAssignees) > 0 {
		if sm.todos == nil {
			return nil, errors.New("todo store is required for scenario task dependencies")
		}

		seedIDs, err := sm.todos.ScenarioSeedItemIDs("scenario:" + scenarioID)
		if err != nil {
			return nil, err
		}

		for i, assignee := range dependencyAssignees {
			id := seedIDs[assignee]
			if id == "" {
				return nil, fmt.Errorf("depends_on member %q has no seeded assigned todo item", input.DependsOn[i])
			}

			dependencyTodoIDs = append(dependencyTodoIDs, id)
		}
	}

	scenarioEnv := map[string]string{
		"GRAITH_SCENARIO":      scenarioID,
		"GRAITH_SCENARIO_NAME": name,
	}
	if input.Role != "" {
		scenarioEnv["GRAITH_SCENARIO_ROLE"] = input.Role
	}

	if goal != "" {
		scenarioEnv["GRAITH_SCENARIO_GOAL"] = goal
	}

	agentHooks := input.AgentHooks

	sess, err := sm.Create(CreateOpts{
		Name:       input.Name,
		AgentName:  agentName,
		RepoPath:   repoRoot,
		BaseBranch: input.Base,
		Prompt:     input.StartupPrompt(),
		Model:      input.Model,
		ParentID:   orchestratorID,
		AgentHooks: agentHooks,
		Includes:   input.Includes,
		Starred:    input.Star,
		ForcePTY:   policyEnabled && input.Policy != nil && input.Policy.Retries > 0,
		Rows:       rows,
		Cols:       cols,
		EnvExtra:   []map[string]string{scenarioEnv},
	})
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	seededTodoID, seedErr := sm.seedAddedScenarioTodo(scenarioID, sess.ID, input, dependencyTodoIDs)
	seededTodoIDs := []string(nil)

	if seededTodoID != "" {
		seededTodoIDs = append(seededTodoIDs, seededTodoID)
	}

	if seedErr != nil {
		sm.removeScenarioTodos(seededTodoIDs)

		if stopErr := sm.Stop(sess.ID); stopErr != nil {
			sm.log.Warn("failed to stop session after result-contract seed failed", "session", sess.ID, "err", stopErr)
		}

		if deleteErr := sm.unstarAndDelete(sess.ID); deleteErr != nil {
			sm.log.Warn("failed to delete session after result-contract seed failed", "session", sess.ID, "err", deleteErr)
		}

		return nil, fmt.Errorf("seed scenario todo: %w", seedErr)
	}

	sm.mu.Lock()

	scenario, stillExists := sm.state.Scenarios[scenarioID]
	if !stillExists {
		sm.mu.Unlock()
		sm.removeScenarioTodos(seededTodoIDs)

		if stopErr := sm.Stop(sess.ID); stopErr != nil {
			sm.log.Warn("failed to stop orphaned session after scenario deletion", "session", sess.ID, "err", stopErr)
		}

		_ = sm.unstarAndDelete(sess.ID)

		return nil, fmt.Errorf("scenario %q was deleted during session creation", name)
	}

	seenResultDestinations := make(map[string]string)

	for _, member := range scenario.Sessions {
		for _, result := range member.Results {
			seenResultDestinations[result.Destination] = fmt.Sprintf("session %q result %q", member.Name, result.Name)
		}
	}

	results, compileErr := compileScenarioResults(
		scenarioID, name, sess.ID, input.Name, input.Results, seenResultDestinations,
	)
	if compileErr != nil {
		sm.mu.Unlock()
		sm.removeScenarioTodos(seededTodoIDs)

		if stopErr := sm.Stop(sess.ID); stopErr != nil {
			sm.log.Warn("failed to stop session after result declaration collision", "session", sess.ID, "err", stopErr)
		}

		_ = sm.unstarAndDelete(sess.ID)

		return nil, fmt.Errorf("session %q: %w", input.Name, compileErr)
	}

	beforePolicy, beforeMemberPolicies := cloneScenarioPolicyRuntime(scenario)

	memberPolicy, policyErr := applyScenarioAddPolicy(scenario, input, sm.scenarioPolicyTime(), sm.scenarioTodoTitleLimit())
	if policyErr != nil {
		sm.mu.Unlock()
		sm.removeScenarioTodos(seededTodoIDs)

		if stopErr := sm.Stop(sess.ID); stopErr != nil {
			sm.log.Warn("failed to stop session after scenario policy changed during add", "session", sess.ID, "err", stopErr)
		}

		_ = sm.unstarAndDelete(sess.ID)

		return nil, policyErr
	}

	if created, ok := sm.state.Sessions[sess.ID]; ok {
		created.ScenarioID = scenarioID
		created.ScenarioName = name
		created.ScenarioRole = input.Role
		created.ScenarioGoal = goal
	}

	scenario.SessionIDs = append(scenario.SessionIDs, sess.ID)

	scenario.Sessions = append(scenario.Sessions, ScenarioSession{
		Name: input.Name, Role: input.Role, Prompt: input.Prompt, Task: input.Task,
		Repo: filepath.Base(repoRoot), Agent: agentName, Model: input.Model,
		Results: results, Policy: memberPolicy,
	})
	if err := sm.saveState(); err != nil {
		scenario.SessionIDs = scenario.SessionIDs[:len(scenario.SessionIDs)-1]
		scenario.Sessions = scenario.Sessions[:len(scenario.Sessions)-1]
		restoreScenarioPolicyRuntime(scenario, beforePolicy, beforeMemberPolicies)

		if created, ok := sm.state.Sessions[sess.ID]; ok {
			created.ScenarioID = ""
			created.ScenarioName = ""
			created.ScenarioRole = ""
			created.ScenarioGoal = ""
		}
		sm.mu.Unlock()
		sm.removeScenarioTodos(seededTodoIDs)

		if stopErr := sm.Stop(sess.ID); stopErr != nil {
			sm.log.Warn("failed to stop session after scenario add commit failed", "session", sess.ID, "err", stopErr)
		}

		if deleteErr := sm.unstarAndDelete(sess.ID); deleteErr != nil {
			sm.log.Warn("failed to delete session after scenario add commit failed", "session", sess.ID, "err", deleteErr)
		}

		return nil, fmt.Errorf("persist scenario member addition: %w", err)
	}
	sm.mu.Unlock()

	sm.republishManifests(scenarioID)

	return &sess, nil
}

func (sm *SessionManager) seedAddedScenarioTodo(scenarioID, sessionID string, input protocol.ScenarioSessionInput, dependencyIDs []string) (string, error) {
	task := strings.TrimSpace(input.Task)
	if task == "" {
		return "", nil
	}

	if sm.todos == nil {
		return "", nil
	}

	scope := "scenario:" + scenarioID

	item, err := sm.todos.Add(TodoAdd{
		Scope: scope, Title: task, Assignee: sessionID, DependsOn: dependencyIDs, CreatedBy: scope,
	})
	if err != nil {
		return "", err
	}

	sm.emitTodoEvent(scope, "added", item)

	return item.ID, nil
}

// seedScenarioTodos atomically creates one assigned todo item per member with a
// non-empty task and resolves member-name dependencies between those items.
func (sm *SessionManager) seedScenarioTodos(scenarioID string, sessionIDs []string, sessions []protocol.ScenarioSessionInput) ([]string, error) {
	if sm.todos == nil {
		for _, session := range sessions {
			if len(session.DependsOn) > 0 {
				return nil, errors.New("todo store is required for scenario task dependencies")
			}
		}

		return nil, nil
	}

	scope := "scenario:" + scenarioID
	entries := make([]TodoBatchAdd, 0, len(sessions))

	for i, ss := range sessions {
		if i >= len(sessionIDs) {
			return nil, fmt.Errorf("scenario task %q has no session id", ss.Name)
		}

		task := strings.TrimSpace(ss.Task)
		if task == "" {
			continue
		}

		entries = append(entries, TodoBatchAdd{
			Key:           ss.Name,
			Item:          TodoAdd{Scope: scope, Title: task, Assignee: sessionIDs[i], CreatedBy: scope},
			DependsOnKeys: append([]string(nil), ss.DependsOn...),
		})
	}

	items, err := sm.todos.AddBatch(entries)
	if err != nil {
		return nil, err
	}

	seeded := make([]string, 0, len(entries))
	for _, entry := range entries {
		item := items[entry.Key]
		seeded = append(seeded, item.ID)
		sm.emitTodoEvent(scope, "added", item)
	}

	return seeded, nil
}

// rollbackScenarioAfterSeedFailure tears down only members created for this
// scenario (shared members are left running) and removes the scenario record
// once cleanup succeeds. It mirrors the earlier create rollback path.
func (sm *SessionManager) rollbackScenarioAfterSeedFailure(scenarioID string, sessionIDs []string, shared []bool) []string {
	var failed []string

	for i, id := range sessionIDs {
		if i < len(shared) && shared[i] {
			continue
		}

		if err := sm.Stop(id); err != nil {
			sm.log.Warn("scenario todo rollback: stop failed", "session", id, "err", err)
		}

		if err := sm.unstarAndDelete(id); err != nil {
			sm.log.Warn("scenario todo rollback: delete failed", "session", id, "err", err)
			failed = append(failed, id)
		}
	}

	sm.mu.Lock()
	if len(failed) == 0 {
		delete(sm.state.Scenarios, scenarioID)
	}

	_ = sm.saveState()
	sm.mu.Unlock()

	return failed
}

func (sm *SessionManager) removeScenarioTodos(ids []string) {
	if sm.todos == nil {
		return
	}

	for _, id := range ids {
		if err := sm.todos.Remove(id); err != nil && !errors.Is(err, ErrTodoNotFound) {
			sm.log.Warn("failed to remove scenario todo during rollback", "todo", id, "err", err)
		}
	}
}

func (sm *SessionManager) scenarioTodoTitleLimit() int {
	if sm.todos == nil {
		return config.TodoMaxTitleDefault
	}

	return sm.todos.titleLimit()
}

func (sm *SessionManager) republishManifests(scenarioID string) {
	sm.mu.RLock()

	scenario, ok := sm.state.Scenarios[scenarioID]
	if !ok {
		sm.mu.RUnlock()
		return
	}

	sessionIDs := make([]string, len(scenario.SessionIDs))
	copy(sessionIDs, scenario.SessionIDs)

	repos := make([]string, len(scenario.Sessions))

	sessions := make([]protocol.ScenarioSessionInput, len(scenario.Sessions))
	for i, ss := range scenario.Sessions {
		repos[i] = ss.Repo

		results := make([]protocol.ScenarioResultSpec, len(ss.Results))
		for j, result := range ss.Results {
			results[j] = protocol.ScenarioResultSpec{
				Name: result.Name, Format: result.Format,
				Store: result.StoreTemplate, Required: result.Required,
			}
		}

		sessions[i] = protocol.ScenarioSessionInput{
			Name: ss.Name, Repo: ss.Repo, Mirror: ss.Mirror, Role: ss.Role,
			Prompt: ss.Prompt, Task: ss.Task, Agent: ss.Agent, Model: ss.Model, Results: results,
		}
	}

	orchestratorID := scenario.OrchestratorID
	msg := protocol.ScenarioStartMsg{
		CallerSessionID: orchestratorID,
		Name:            scenario.Name,
		Goal:            scenario.Goal,
		Sessions:        sessions,
	}

	sm.mu.RUnlock()

	for i, id := range sessionIDs {
		manifest := sm.buildManifest(scenarioID, msg, repos, sessionIDs, i)

		manifestJSON, err := json.Marshal(manifest)
		if err != nil {
			sm.log.Error("failed to marshal manifest for republish", "session", id, "err", err)
			continue
		}

		stream := "inbox:" + id
		if sm.messages != nil {
			_, err = sm.messages.Publish(PublishOpts{Stream: stream, SenderID: orchestratorID, SenderName: "orchestrator", Body: string(manifestJSON)})
			if err != nil {
				sm.log.Error("failed to republish manifest", "session", id, "err", err)
			}
		}
	}

	storeDir := store.SharedStorePath(sm.paths.DataDir)
	if err := store.Init(storeDir); err != nil {
		sm.log.Error("failed to init shared store for manifest republish", "err", err)
		return
	}

	for i := range sessions {
		manifest := sm.buildManifest(scenarioID, msg, repos, sessionIDs, i)

		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			sm.log.Error("failed to marshal manifest for store", "err", err)
			continue
		}

		key := fmt.Sprintf("scenarios/%s/manifest-%s.json", scenarioID, sessions[i].Name)
		if err := store.Put(storeDir, key, string(data)); err != nil {
			sm.log.Error("failed to persist manifest", "key", key, "err", err)
		}
	}
}

func (sm *SessionManager) buildScenarioRecord(sc *ScenarioState) *protocol.ScenarioRecord {
	sessions := make([]protocol.ScenarioSessionInfo, len(sc.Sessions))

	// Per-member progress is derived from the todo items assigned to each member
	// (issue #591), keyed by session ID — this replaces the old TaskDone bool.
	var (
		progress  map[string][2]int
		seedItems map[string]TodoItem
	)

	if sm.todos != nil {
		scope := "scenario:" + sc.ID
		progress, _ = sm.todos.AssigneeProgress(scope)
		seedItems, _ = sm.todos.ScenarioSeedItems(scope)
	}

	seedMemberNames := make(map[string]string, len(seedItems))
	for i, id := range sc.SessionIDs {
		if i < len(sc.Sessions) {
			if seed, ok := seedItems[id]; ok {
				seedMemberNames[seed.ID] = sc.Sessions[i].Name
			}
		}
	}

	var (
		running, stopped, errored, tracked, completeMembers int
		successful, requiredSuccessful, requiredTotal       int
		retryPending, requiredExhausted                     bool
	)

	for i, ss := range sc.Sessions {
		sessions[i] = protocol.ScenarioSessionInfo{
			Name: ss.Name, Mirror: ss.Mirror, Role: ss.Role, Prompt: ss.startupPrompt(), Task: ss.Task,
			Repo: ss.Repo, Agent: ss.Agent, Model: ss.Model, Shared: ss.Shared,
		}

		if len(ss.Results) > 0 {
			sessions[i].Results = make([]protocol.ScenarioResultInfo, len(ss.Results))
			for resultIndex, result := range ss.Results {
				sessions[i].Results[resultIndex] = scenarioResultInfo(result)
			}
		}

		if i < len(sc.SessionIDs) {
			sessions[i].SessionID = sc.SessionIDs[i]
			if p, ok := progress[sc.SessionIDs[i]]; ok {
				sessions[i].TodoDone = p[0]
				sessions[i].TodoTotal = p[1]
			}

			if seed, ok := seedItems[sc.SessionIDs[i]]; ok {
				for _, dependencyID := range seed.BlockedBy {
					name := seedMemberNames[dependencyID]
					if name == "" {
						name = dependencyID
					}

					sessions[i].BlockedBy = append(sessions[i].BlockedBy, name)
				}
			}

			memberTracked, memberComplete := scenarioMemberContractProgress(
				ss, [2]int{sessions[i].TodoDone, sessions[i].TodoTotal},
			)
			if memberTracked {
				tracked++

				if memberComplete {
					completeMembers++
				}
			}

			if sess, ok := sm.state.Sessions[sc.SessionIDs[i]]; ok {
				sessions[i].Status = string(sess.Status)
				switch sess.Status {
				case StatusRunning:
					running++
				case StatusStopped:
					stopped++
				case StatusErrored:
					errored++
				}
			}
		}

		if ss.Policy != nil {
			memberPolicy := ss.Policy
			sessions[i].Policy = &protocol.ScenarioMemberPolicyInfo{
				Required:         memberPolicy.Required,
				Attempt:          memberPolicy.Attempt,
				MaxAttempts:      memberPolicy.Retries + 1,
				AttemptStartedAt: formatScenarioPolicyTime(memberPolicy.AttemptStartedAt),
				Deadline:         formatScenarioPolicyTime(memberPolicy.Deadline),
				RetryPending:     memberPolicy.RetryPending,
				SucceededAt:      formatScenarioPolicyTime(memberPolicy.SucceededAt),
				ExhaustedAt:      formatScenarioPolicyTime(memberPolicy.ExhaustedAt),
				ExhaustionReason: memberPolicy.ExhaustionReason,
			}

			if memberPolicy.Required {
				requiredTotal++
			}

			if memberPolicy.SucceededAt != nil {
				successful++

				if memberPolicy.Required {
					requiredSuccessful++
				}
			}

			retryPending = retryPending || memberPolicy.RetryPending
			requiredExhausted = requiredExhausted || (memberPolicy.Required && memberPolicy.ExhaustedAt != nil)
		}
	}

	total := len(sc.SessionIDs)

	var status string

	switch {
	case sc.Policy != nil && sc.Policy.Outcome != "":
		status = sc.Policy.Outcome
	case sc.Policy != nil && retryPending:
		status = "retrying"
	case sc.Policy != nil && requiredExhausted:
		status = "exhausted"
	// Completion is derived from tracked todos plus required result contracts.
	// Optional results do not block it. Policy scenarios become complete only
	// through their durable policy outcome.
	case sc.Policy == nil && errored == 0 && tracked > 0 && completeMembers == tracked:
		status = "complete"
	case running == total:
		status = "running"
	case stopped == total:
		status = "stopped"
	case errored > 0:
		status = "errored"
	default:
		status = "partial"
	}

	record := &protocol.ScenarioRecord{
		ID:                sc.ID,
		Name:              sc.Name,
		OrchestratorID:    sc.OrchestratorID,
		Goal:              sc.Goal,
		Status:            status,
		SessionIDs:        sc.SessionIDs,
		Sessions:          sessions,
		CreatedAt:         sc.CreatedAt.Format(time.RFC3339),
		CompletionEpoch:   sc.Completion.Epoch,
		CompletionActions: scenarioCompletionActionsToWire(sc.Completion.Actions),
		Cleanup:           scenarioCleanupToWire(sc.Completion.Cleanup),
	}
	if sc.Policy != nil {
		record.Policy = &protocol.ScenarioPolicyInfo{
			Completion:         sc.Policy.Completion,
			Quorum:             sc.Policy.Quorum,
			OnExhausted:        sc.Policy.OnExhausted,
			Active:             sc.Policy.Active,
			Paused:             sc.Policy.Paused,
			Successful:         successful,
			RequiredSuccessful: requiredSuccessful,
			RequiredTotal:      requiredTotal,
			Outcome:            sc.Policy.Outcome,
			OutcomeReason:      sc.Policy.OutcomeReason,
			OutcomeAt:          formatScenarioPolicyTime(sc.Policy.OutcomeAt),
		}
	}

	return record
}

func formatScenarioPolicyTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339Nano)
}

func scenarioCompletionActionsToWire(actions []ScenarioCompletionActionState) []protocol.ScenarioCompletionActionInfo {
	out := make([]protocol.ScenarioCompletionActionInfo, 0, len(actions))
	for _, action := range actions {
		info := protocol.ScenarioCompletionActionInfo{
			Name: action.Name, State: action.State, Attempt: action.Attempt,
			Result: action.Result, Error: action.Error, SessionID: action.SessionID,
		}
		if action.StartedAt != nil {
			info.StartedAt = action.StartedAt.Format(time.RFC3339)
		}

		if action.FinishedAt != nil {
			info.FinishedAt = action.FinishedAt.Format(time.RFC3339)
		}

		out = append(out, info)
	}

	return out
}

func scenarioCleanupToWire(cleanup *ScenarioCleanupState) *protocol.ScenarioCleanupInfo {
	if cleanup == nil {
		return nil
	}

	info := &protocol.ScenarioCleanupInfo{
		Policy: cleanup.Policy, State: cleanup.State, Result: cleanup.Result, Error: cleanup.Error,
	}
	if cleanup.ScheduledAt != nil {
		info.ScheduledAt = cleanup.ScheduledAt.Format(time.RFC3339)
	}

	if cleanup.FinishedAt != nil {
		info.FinishedAt = cleanup.FinishedAt.Format(time.RFC3339)
	}

	return info
}
