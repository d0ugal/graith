package daemon

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/store"
)

var validScenarioName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func ValidateScenarioName(name string) error {
	if name == "" {
		return fmt.Errorf("scenario name must not be empty")
	}

	if len(name) > 128 {
		return fmt.Errorf("scenario name must be 128 characters or fewer (got %d)", len(name))
	}

	if !validScenarioName.MatchString(name) {
		return fmt.Errorf("scenario name %q is invalid: must be lowercase alphanumeric with hyphens only", name)
	}

	return nil
}

func (sm *SessionManager) StartScenario(msg protocol.ScenarioStartMsg, rows, cols uint16) (*ScenarioState, error) {
	if err := ValidateScenarioName(msg.Name); err != nil {
		return nil, err
	}

	if len(msg.Sessions) == 0 {
		return nil, fmt.Errorf("scenario must define at least one session")
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
		return nil, fmt.Errorf("only the orchestrator session can start scenarios")
	}

	// Validate session definitions.
	seenNames := make(map[string]bool, len(msg.Sessions))
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

		if s.Repo == "" {
			return nil, fmt.Errorf("session %q: repo is required", s.Name)
		}
	}

	// Preflight: validate repos exist and are git repos.
	for _, s := range msg.Sessions {
		if !git.IsInsideGitRepo(s.Repo) {
			return nil, fmt.Errorf("session %q: repo %q is not a git repository", s.Name, s.Repo)
		}
	}

	cfg := sm.Config()

	// Validate agents and repos against config.
	for _, s := range msg.Sessions {
		agentName := s.Agent
		if agentName == "" {
			agentName = cfg.DefaultAgent
		}

		if _, ok := cfg.Agents[agentName]; !ok {
			return nil, fmt.Errorf("session %q: unknown agent %q", s.Name, agentName)
		}

		if !cfg.RepoPathAllowed(s.Repo) {
			return nil, fmt.Errorf("session %q: repo %q is not under any allowed_repo_paths", s.Name, s.Repo)
		}
	}

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

	scenarioID := "sc-" + generateID()
	now := time.Now().UTC()

	scenarioSessions := make([]ScenarioSession, len(msg.Sessions))
	sessionIDs := make([]string, len(msg.Sessions))
	sharedReused := make([]bool, len(msg.Sessions))

	for i, s := range msg.Sessions {
		agentName := s.Agent
		if agentName == "" {
			agentName = cfg.DefaultAgent
		}

		repoRoot, err := git.RepoRootPath(s.Repo)
		if err != nil {
			sm.mu.Unlock()
			return nil, fmt.Errorf("session %q: resolve repo root: %w", s.Name, err)
		}

		scenarioSessions[i] = ScenarioSession{
			Name:   s.Name,
			Role:   s.Role,
			Task:   s.Task,
			Repo:   filepath.Base(repoRoot),
			Agent:  agentName,
			Model:  s.Model,
			Shared: s.Shared,
		}

		// Shared sessions must reuse an existing running session.
		if s.Shared {
			for existID, existing := range sm.state.Sessions {
				if existing.Name == s.Name && existing.Status == StatusRunning {
					sessionIDs[i] = existID
					sharedReused[i] = true

					break
				}
			}

			if sharedReused[i] {
				continue
			}
			sm.mu.Unlock()

			return nil, fmt.Errorf("shared session %q: no running session with that name exists", s.Name)
		}

		id := sm.uniqueSessionIDLocked()
		sessionIDs[i] = id

		sm.state.Sessions[id] = &SessionState{
			ID:              id,
			ParentID:        msg.CallerSessionID,
			Name:            s.Name,
			RepoPath:        repoRoot,
			RepoName:        filepath.Base(repoRoot),
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

	// --- Start phase: create each session concurrently ---
	// Remove all placeholders first, then hand each reserved ID back to Create
	// (via CreateOpts.ID) so the created session keeps the ID we reserved and
	// ScenarioState.SessionIDs stays valid without a rewrite. The delete is
	// still needed because Create re-reserves the ID itself and would reject an
	// existing entry as a collision. This is not a held reservation across the
	// gap: between the delete here and Create's re-reserve the ID is briefly
	// free, so a concurrent Create using the same ID would win and this
	// scenario's Create would fail and roll back — the same window that existed
	// before IDs were made stable. Shared-reused sessions already have real IDs
	// and don't need creation.
	repoRoots := make([]string, len(msg.Sessions))

	sm.mu.Lock()
	for i, id := range sessionIDs {
		if sharedReused[i] {
			if p, ok := sm.state.Sessions[id]; ok {
				repoRoots[i] = p.RepoPath
			}

			continue
		}

		if p, ok := sm.state.Sessions[id]; ok {
			repoRoots[i] = p.RepoPath
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

	results := make([]createResult, len(msg.Sessions))

	var wg sync.WaitGroup

	for i, s := range msg.Sessions {
		if sharedReused[i] {
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

			sess, err := sm.Create(CreateOpts{
				ID:         sessionIDs[idx],
				Name:       s.Name,
				AgentName:  agentName,
				RepoPath:   repoRoots[idx],
				BaseBranch: s.Base,
				Prompt:     s.Task,
				Model:      s.Model,
				ParentID:   msg.CallerSessionID,
				AgentHooks: s.AgentHooks,
				Rows:       rows,
				Cols:       cols,
				EnvExtra:   []map[string]string{scenarioEnv},
			})
			results[idx] = createResult{index: idx, sess: sess, err: err}
		}(i, s)
	}

	wg.Wait()

	var (
		startedIDs  []string
		startErrors []string
	)

	for i, r := range results {
		if sharedReused[i] {
			continue
		}

		if r.err != nil {
			startErrors = append(startErrors, fmt.Sprintf("session %q: %v", msg.Sessions[i].Name, r.err))
			continue
		}

		startedIDs = append(startedIDs, r.sess.ID)
		sessionIDs[i] = r.sess.ID

		sm.mu.Lock()
		if created, ok := sm.state.Sessions[r.sess.ID]; ok {
			created.ScenarioID = scenarioID
			created.ScenarioName = msg.Name
			created.ScenarioRole = msg.Sessions[i].Role
			created.ScenarioGoal = msg.Goal
		}

		_ = sm.saveState()
		sm.mu.Unlock()
	}

	if len(startErrors) > 0 {
		// Update scenario with real session IDs so retry/cleanup can find them.
		sm.mu.Lock()
		if sc := sm.state.Scenarios[scenarioID]; sc != nil {
			sc.SessionIDs = sessionIDs
		}

		_ = sm.saveState()
		sm.mu.Unlock()

		// Rollback: stop and delete all already-started sessions.
		var rollbackErrors []string

		for _, id := range startedIDs {
			if err := sm.Stop(id); err != nil {
				sm.log.Warn("scenario rollback: stop failed", "session", id, "err", err)
			}

			if err := sm.Delete(id); err != nil {
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

			_ = sm.Delete(id)
		}

		return nil, fmt.Errorf("scenario %q was deleted during session creation", msg.Name)
	}

	scenario.SessionIDs = sessionIDs
	_ = sm.saveState()
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
	Name      string `json:"name"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Task      string `json:"task"`
}

type scenarioManifestSibling struct {
	Name      string `json:"name"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Repo      string `json:"repo"`
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
			Role:      s.Role,
			Repo:      repo,
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
			Role:      self.Role,
			Task:      self.Task,
		},
		Siblings: siblings,
		Orchestrator: scenarioManifestOrch{
			SessionID: msg.CallerSessionID,
			Name:      "orchestrator",
		},
	}
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
	sm.mu.RLock()

	var (
		sessionIDs []string
		sharedSet  map[int]bool
	)

	for _, sc := range sm.state.Scenarios {
		if sc.Name == name {
			sessionIDs = make([]string, len(sc.SessionIDs))
			copy(sessionIDs, sc.SessionIDs)

			sharedSet = make(map[int]bool, len(sc.Sessions))
			for i, ss := range sc.Sessions {
				if ss.Shared {
					sharedSet[i] = true
				}
			}

			break
		}
	}

	sm.mu.RUnlock()

	if sessionIDs == nil {
		return nil, fmt.Errorf("scenario %q not found", name)
	}

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
	sm.mu.RLock()

	var (
		sessionIDs []string
		scenarioID string
		sharedSet  map[int]bool
	)

	for id, sc := range sm.state.Scenarios {
		if sc.Name == name {
			sessionIDs = make([]string, len(sc.SessionIDs))
			copy(sessionIDs, sc.SessionIDs)

			scenarioID = id

			sharedSet = make(map[int]bool, len(sc.Sessions))
			for i, ss := range sc.Sessions {
				if ss.Shared {
					sharedSet[i] = true
				}
			}

			break
		}
	}

	sm.mu.RUnlock()

	if sessionIDs == nil {
		return nil, fmt.Errorf("scenario %q not found", name)
	}

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
		// Unstar before deleting (Delete refuses starred sessions).
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.Starred = false
		}
		sm.mu.Unlock()

		if err := sm.Delete(id); err != nil {
			sm.log.Warn("failed to delete scenario session", "session", id, "err", err)
			deleteErrors = append(deleteErrors, id)

			continue
		}

		deleted = append(deleted, id)
	}

	// Only remove the scenario record if all sessions were cleaned up.
	sm.mu.Lock()
	if len(deleteErrors) == 0 {
		delete(sm.state.Scenarios, scenarioID)
	}

	_ = sm.saveState()
	sm.mu.Unlock()

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
	sm.mu.RLock()

	var scenario *ScenarioState

	for _, sc := range sm.state.Scenarios {
		if sc.Name == name {
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

	sm.mu.RUnlock()

	var resumed []string

	for _, id := range sessionIDs {
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

		if _, err := sm.Resume(id, rows, cols); err != nil {
			sm.log.Warn("failed to resume scenario session", "session", id, "err", err)
			continue
		}

		resumed = append(resumed, id)
	}

	if len(resumed) > 0 {
		sm.republishManifests(scenarioID)
	}

	return resumed, nil
}

func (sm *SessionManager) ScenarioTaskDone(scenarioName, sessionID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var scenario *ScenarioState

	for _, sc := range sm.state.Scenarios {
		if sc.Name == scenarioName {
			scenario = sc
			break
		}
	}

	if scenario == nil {
		return fmt.Errorf("scenario %q not found", scenarioName)
	}

	idx := -1

	for i, id := range scenario.SessionIDs {
		if id == sessionID {
			idx = i
			break
		}
	}

	if idx < 0 {
		return fmt.Errorf("session %q is not part of scenario %q", sessionID, scenarioName)
	}

	scenario.Sessions[idx].TaskDone = true

	return sm.saveState()
}

func (sm *SessionManager) AddToScenario(name string, input protocol.ScenarioSessionInput, rows, cols uint16) (*SessionState, error) {
	if err := ValidateSessionName(input.Name); err != nil {
		return nil, err
	}

	if input.Repo == "" {
		return nil, fmt.Errorf("repo is required")
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

	sm.mu.Lock()

	var scenarioID, orchestratorID, goal string

	found := false

	for id, sc := range sm.state.Scenarios {
		if sc.Name == name {
			scenarioID = id
			orchestratorID = sc.OrchestratorID
			goal = sc.Goal
			found = true

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
		Prompt:     input.Task,
		Model:      input.Model,
		ParentID:   orchestratorID,
		AgentHooks: agentHooks,
		Rows:       rows,
		Cols:       cols,
		EnvExtra:   []map[string]string{scenarioEnv},
	})
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	sm.mu.Lock()

	scenario, stillExists := sm.state.Scenarios[scenarioID]
	if !stillExists {
		sm.mu.Unlock()

		if stopErr := sm.Stop(sess.ID); stopErr != nil {
			sm.log.Warn("failed to stop orphaned session after scenario deletion", "session", sess.ID, "err", stopErr)
		}

		_ = sm.Delete(sess.ID)

		return nil, fmt.Errorf("scenario %q was deleted during session creation", name)
	}

	if created, ok := sm.state.Sessions[sess.ID]; ok {
		created.ScenarioID = scenarioID
		created.ScenarioName = name
		created.ScenarioRole = input.Role
		created.ScenarioGoal = goal
	}

	scenario.SessionIDs = append(scenario.SessionIDs, sess.ID)
	scenario.Sessions = append(scenario.Sessions, ScenarioSession{
		Name:  input.Name,
		Role:  input.Role,
		Task:  input.Task,
		Repo:  filepath.Base(repoRoot),
		Agent: agentName,
		Model: input.Model,
	})
	_ = sm.saveState()
	sm.mu.Unlock()

	sm.republishManifests(scenarioID)

	return &sess, nil
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
		sessions[i] = protocol.ScenarioSessionInput{
			Name:  ss.Name,
			Repo:  ss.Repo,
			Role:  ss.Role,
			Task:  ss.Task,
			Agent: ss.Agent,
			Model: ss.Model,
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

	var running, stopped, errored int

	for i, ss := range sc.Sessions {
		sessions[i] = protocol.ScenarioSessionInfo{
			Name:     ss.Name,
			Role:     ss.Role,
			Task:     ss.Task,
			TaskDone: ss.TaskDone,
			Repo:     ss.Repo,
			Agent:    ss.Agent,
			Model:    ss.Model,
			Shared:   ss.Shared,
		}
		if i < len(sc.SessionIDs) {
			sessions[i].SessionID = sc.SessionIDs[i]
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
	}

	total := len(sc.SessionIDs)

	var status string

	switch {
	case running == total:
		status = "running"
	case stopped == total:
		status = "stopped"
	case errored > 0:
		status = "errored"
	default:
		status = "partial"
	}

	return &protocol.ScenarioRecord{
		ID:             sc.ID,
		Name:           sc.Name,
		OrchestratorID: sc.OrchestratorID,
		Goal:           sc.Goal,
		Status:         status,
		SessionIDs:     sc.SessionIDs,
		Sessions:       sessions,
		CreatedAt:      sc.CreatedAt.Format(time.RFC3339),
	}
}
