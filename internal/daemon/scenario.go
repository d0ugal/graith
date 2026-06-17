package daemon

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
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

	// Validate caller is orchestrator.
	sm.mu.RLock()
	callerSess, callerOk := sm.state.Sessions[msg.CallerSessionID]
	sm.mu.RUnlock()

	if !callerOk {
		return nil, fmt.Errorf("caller session %q not found", msg.CallerSessionID)
	}
	if callerSess.SystemKind != SystemKindOrchestrator {
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

	// Check session name uniqueness against existing sessions.
	for _, s := range msg.Sessions {
		for _, existing := range sm.state.Sessions {
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

	for i, s := range msg.Sessions {
		id := generateID()
		sessionIDs[i] = id

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
			Name:  s.Name,
			Role:  s.Role,
			Task:  s.Task,
			Repo:  filepath.Base(repoRoot),
			Agent: agentName,
			Model: s.Model,
		}

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
		for _, id := range sessionIDs {
			delete(sm.state.Sessions, id)
		}
		delete(sm.state.Scenarios, scenarioID)
		sm.mu.Unlock()
		return nil, fmt.Errorf("persist scenario state: %w", err)
	}

	sm.mu.Unlock()

	// --- Start phase: create each session using the normal Create flow ---
	var startedIDs []string
	var startErr error

	for i, s := range msg.Sessions {
		id := sessionIDs[i]
		agentName := s.Agent
		if agentName == "" {
			agentName = cfg.DefaultAgent
		}

		// Remove the placeholder — Create will make its own.
		sm.mu.Lock()
		placeholder := sm.state.Sessions[id]
		repoRoot := placeholder.RepoPath
		delete(sm.state.Sessions, id)
		_ = sm.saveState()
		sm.mu.Unlock()

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

		agentHooks := s.AgentHooks

		sess, err := sm.Create(
			s.Name,
			agentName,
			repoRoot,
			s.Base,
			s.Task,
			s.Model,
			msg.CallerSessionID,
			false,
			"",
			agentHooks,
			false,
			false,
			false,
			rows, cols,
			scenarioEnv,
		)
		if err != nil {
			startErr = fmt.Errorf("session %q: %w", s.Name, err)
			break
		}

		// Update session with scenario metadata.
		sm.mu.Lock()
		if created, ok := sm.state.Sessions[sess.ID]; ok {
			created.ScenarioID = scenarioID
			created.ScenarioRole = s.Role
			created.ScenarioGoal = msg.Goal
		}
		// Update scenario to track the real session ID (Create generates its own).
		sessionIDs[i] = sess.ID
		_ = sm.saveState()
		sm.mu.Unlock()

		startedIDs = append(startedIDs, sess.ID)
	}

	if startErr != nil {
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
		// Only remove state for sessions that were successfully cleaned up.
		for _, id := range sessionIDs {
			alreadyStarted := false
			for _, sid := range startedIDs {
				if id == sid {
					alreadyStarted = true
					break
				}
			}
			failedCleanup := false
			for _, fid := range rollbackErrors {
				if id == fid {
					failedCleanup = true
					break
				}
			}
			if !alreadyStarted || !failedCleanup {
				delete(sm.state.Sessions, id)
			}
		}
		if len(rollbackErrors) == 0 {
			delete(sm.state.Scenarios, scenarioID)
		}
		_ = sm.saveState()
		sm.mu.Unlock()
		return nil, startErr
	}

	// Update the scenario with final session IDs.
	sm.mu.Lock()
	scenario = sm.state.Scenarios[scenarioID]
	if scenario != nil {
		scenario.SessionIDs = sessionIDs
	}
	_ = sm.saveState()
	sm.mu.Unlock()

	// --- Manifest phase: build and publish manifest to each session's inbox ---
	for i, id := range sessionIDs {
		manifest := sm.buildManifest(scenarioID, msg, scenario, sessionIDs, i)
		manifestJSON, err := json.Marshal(manifest)
		if err != nil {
			sm.log.Error("failed to marshal scenario manifest", "session", id, "err", err)
			continue
		}
		stream := "inbox:" + id
		if sm.messages != nil {
			_, err = sm.messages.Publish(stream, msg.CallerSessionID, "orchestrator", string(manifestJSON), "", "")
			if err != nil {
				sm.log.Error("failed to publish scenario manifest", "session", id, "err", err)
			}
		}
	}

	// Persist manifest to shared store.
	sm.persistManifest(scenarioID, msg, scenario, sessionIDs)

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

func (sm *SessionManager) buildManifest(scenarioID string, msg protocol.ScenarioStartMsg, scenario *ScenarioState, sessionIDs []string, selfIndex int) scenarioManifest {
	self := msg.Sessions[selfIndex]

	var siblings []scenarioManifestSibling
	for j, s := range msg.Sessions {
		if j == selfIndex {
			continue
		}
		siblings = append(siblings, scenarioManifestSibling{
			Name:      s.Name,
			SessionID: sessionIDs[j],
			Role:      s.Role,
			Repo:      scenario.Sessions[j].Repo,
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

func (sm *SessionManager) persistManifest(scenarioID string, msg protocol.ScenarioStartMsg, scenario *ScenarioState, sessionIDs []string) {
	storeDir := store.SharedStorePath(sm.paths.DataDir)
	if err := store.Init(storeDir); err != nil {
		sm.log.Error("failed to init shared store for manifest", "err", err)
		return
	}

	for i := range msg.Sessions {
		manifest := sm.buildManifest(scenarioID, msg, scenario, sessionIDs, i)
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
	var sessionIDs []string
	for _, sc := range sm.state.Scenarios {
		if sc.Name == name {
			sessionIDs = make([]string, len(sc.SessionIDs))
			copy(sessionIDs, sc.SessionIDs)
			break
		}
	}
	sm.mu.RUnlock()

	if sessionIDs == nil {
		return nil, fmt.Errorf("scenario %q not found", name)
	}

	var stopped []string
	for _, id := range sessionIDs {
		sm.mu.RLock()
		sess, ok := sm.state.Sessions[id]
		sm.mu.RUnlock()
		if !ok || sess.Status != StatusRunning {
			continue
		}
		if err := sm.stopWithReason(id, StopReasonUser); err != nil {
			sm.log.Warn("failed to stop scenario session", "session", id, "err", err)
			continue
		}
		stopped = append(stopped, id)
	}
	return stopped, nil
}

func (sm *SessionManager) DeleteScenario(name string) ([]string, error) {
	sm.mu.RLock()
	var sessionIDs []string
	var scenarioID string
	for id, sc := range sm.state.Scenarios {
		if sc.Name == name {
			sessionIDs = make([]string, len(sc.SessionIDs))
			copy(sessionIDs, sc.SessionIDs)
			scenarioID = id
			break
		}
	}
	sm.mu.RUnlock()

	if sessionIDs == nil {
		return nil, fmt.Errorf("scenario %q not found", name)
	}

	// Stop running sessions first.
	for _, id := range sessionIDs {
		sm.mu.RLock()
		sess, ok := sm.state.Sessions[id]
		sm.mu.RUnlock()
		if ok && sess.Status == StatusRunning {
			_ = sm.stopWithReason(id, StopReasonUser)
		}
	}

	// Delete each session.
	var deleted []string
	var deleteErrors []string
	for _, id := range sessionIDs {
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

func (sm *SessionManager) buildScenarioRecord(sc *ScenarioState) *protocol.ScenarioRecord {
	sessions := make([]protocol.ScenarioSessionInfo, len(sc.Sessions))
	var running, stopped, errored int

	for i, ss := range sc.Sessions {
		sessions[i] = protocol.ScenarioSessionInfo{
			Name:  ss.Name,
			Role:  ss.Role,
			Task:  ss.Task,
			Repo:  ss.Repo,
			Agent: ss.Agent,
			Model: ss.Model,
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
