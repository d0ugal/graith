package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/scenariofile"
	"github.com/d0ugal/graith/internal/store"
)

func TestValidateScenarioName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "strath-braw", false},
		{"valid with numbers", "test123", false},
		{"valid single char", "a", false},
		{"empty", "", true},
		{"uppercase", "MyScenario", true},
		{"starts with hyphen", "-bad", true},
		{"has underscore", "my_scenario", true},
		{"has dots", "my.scenario", true},
		{"has spaces", "my scenario", true},
		{"too long", string(make([]byte, 129)), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateScenarioName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateScenarioName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestScenarioStateInState(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Scenarios["sc-kirk"] = &ScenarioState{
		ID:         "sc-kirk",
		Name:       "strath-braw",
		Goal:       "kirk goal",
		SessionIDs: []string{"braw1", "bonnie1"},
		Sessions: []ScenarioSession{
			{Name: "braw-forge", Role: "braw-forge dev", Repo: "croft-forge"},
			{Name: "bonnie-loom", Role: "bonnie-loom dev", Repo: "croft-loom"},
		},
	}
	_ = sm.saveState()
	sm.mu.Unlock()

	// Reload state and verify scenario persisted.
	state, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	if len(state.Scenarios) != 1 {
		t.Fatalf("expected 1 scenario, got %d", len(state.Scenarios))
	}

	sc := state.Scenarios["sc-kirk"]
	if sc.Name != "strath-braw" {
		t.Errorf("scenario name = %q, want %q", sc.Name, "strath-braw")
	}

	if sc.Goal != "kirk goal" {
		t.Errorf("scenario goal = %q, want %q", sc.Goal, "kirk goal")
	}

	if len(sc.Sessions) != 2 {
		t.Errorf("scenario sessions = %d, want 2", len(sc.Sessions))
	}
}

func TestScenarioFieldsOnSessionState(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["braw1"] = &SessionState{
		ID:           "braw1",
		Name:         "braw-forge",
		Status:       StatusRunning,
		ScenarioID:   "sc-glen",
		ScenarioRole: "Braw forge engineer",
		ScenarioGoal: "Build the braw thing",
	}
	_ = sm.saveState()
	sm.mu.Unlock()

	state, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	sess := state.Sessions["braw1"]
	if sess.ScenarioID != "sc-glen" {
		t.Errorf("ScenarioID = %q, want %q", sess.ScenarioID, "sc-glen")
	}

	if sess.ScenarioRole != "Braw forge engineer" {
		t.Errorf("ScenarioRole = %q, want %q", sess.ScenarioRole, "Braw forge engineer")
	}

	if sess.ScenarioGoal != "Build the braw thing" {
		t.Errorf("ScenarioGoal = %q, want %q", sess.ScenarioGoal, "Build the braw thing")
	}
}

func TestBuildScenarioRecord(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["braw-s1"] = &SessionState{ID: "braw-s1", Name: "braw-forge", Status: StatusRunning}
	sm.state.Sessions["bonnie-s2"] = &SessionState{ID: "bonnie-s2", Name: "bonnie-loom", Status: StatusStopped}
	sm.state.Scenarios["sc-braw"] = &ScenarioState{
		ID:         "sc-braw",
		Name:       "strath-kirk",
		Goal:       "kirk-goal",
		SessionIDs: []string{"braw-s1", "bonnie-s2"},
		Sessions: []ScenarioSession{
			{Name: "braw-forge", Mirror: "bonnie-loom", Role: "braw-forge dev", Repo: "croft-forge", Agent: "claude"},
			{Name: "bonnie-loom", Role: "bonnie-loom dev", Repo: "croft-loom", Agent: "claude"},
		},
	}

	record := sm.buildScenarioRecord(sm.state.Scenarios["sc-braw"])
	sm.mu.Unlock()

	if record.Status != "partial" {
		t.Errorf("status = %q, want %q", record.Status, "partial")
	}

	if len(record.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(record.Sessions))
	}

	if record.Sessions[0].Status != "running" {
		t.Errorf("session[0].Status = %q, want %q", record.Sessions[0].Status, "running")
	}

	if record.Sessions[0].Mirror != "bonnie-loom" {
		t.Errorf("session[0].Mirror = %q, want bonnie-loom", record.Sessions[0].Mirror)
	}

	if record.Sessions[1].Status != "stopped" {
		t.Errorf("session[1].Status = %q, want %q", record.Sessions[1].Status, "stopped")
	}
}

func TestBuildScenarioRecordAggregateStatus(t *testing.T) {
	tests := []struct {
		name          string
		sessionStatus SessionStatus
		wantStatus    string
	}{
		{"all running", StatusRunning, "running"},
		{"all stopped", StatusStopped, "stopped"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := newTestSessionManager(t)

			sm.mu.Lock()
			sm.state.Sessions["braw-s1"] = &SessionState{ID: "braw-s1", Name: "braw-a", Status: tt.sessionStatus}
			sm.state.Sessions["bonnie-s2"] = &SessionState{ID: "bonnie-s2", Name: "bonnie-b", Status: tt.sessionStatus}
			sm.state.Scenarios["sc-braw"] = &ScenarioState{
				ID:         "sc-braw",
				Name:       "strath-neep",
				SessionIDs: []string{"braw-s1", "bonnie-s2"},
				Sessions: []ScenarioSession{
					{Name: "braw-a"},
					{Name: "bonnie-b"},
				},
			}
			record := sm.buildScenarioRecord(sm.state.Scenarios["sc-braw"])
			sm.mu.Unlock()

			if record.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", record.Status, tt.wantStatus)
			}
		})
	}
}

func TestListScenarios(t *testing.T) {
	sm := newTestSessionManager(t)

	records := sm.ListScenarios()
	if len(records) != 0 {
		t.Errorf("expected 0 scenarios, got %d", len(records))
	}

	sm.mu.Lock()
	sm.state.Scenarios["sc-braw"] = &ScenarioState{
		ID:   "sc-braw",
		Name: "strath-neep",
	}
	sm.mu.Unlock()

	records = sm.ListScenarios()
	if len(records) != 1 {
		t.Fatalf("expected 1 scenario, got %d", len(records))
	}

	if records[0].Name != "strath-neep" {
		t.Errorf("name = %q, want %q", records[0].Name, "strath-neep")
	}
}

func TestStartScenarioRequiresOrchestrator(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["neep-reg"] = &SessionState{
		ID:     "neep-reg",
		Name:   "neep-session",
		Status: StatusRunning,
	}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "neep-reg",
		Name:            "strath-kirk",
		Goal:            "kirk-goal",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-forge", Repo: "/glen/croft"},
		},
	}, 24, 80)
	if err == nil {
		t.Fatal("expected error for non-orchestrator caller")
	}

	if got := err.Error(); got != "only the orchestrator session can start scenarios" {
		t.Errorf("error = %q, want orchestrator-only message", got)
	}
}

func TestStartScenarioRejectsImpossiblePolicyBeforeCallerLookup(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		Name:     "strath-policy",
		Policy:   &protocol.ScenarioPolicyInput{Completion: "quorum", Quorum: 2},
		Sessions: []protocol.ScenarioSessionInput{{Name: "braw", Repo: "/tmp/croft"}},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "exceeds member count") {
		t.Fatalf("error = %v, want authoritative policy preflight rejection", err)
	}
}

func TestStartScenarioPolicyRejectsPromptWithoutContractBeforeCallerLookup(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		Name:   "strath-policy",
		Policy: &protocol.ScenarioPolicyInput{},
		Sessions: []protocol.ScenarioSessionInput{{
			Name: "braw", Repo: "/tmp/croft", Prompt: "Inspect the croft",
		}},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "non-empty task or required result contract") {
		t.Fatalf("error = %v, want prompt-only contract rejection", err)
	}
}

func TestStartScenarioCallerNotFound(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "haar-caller",
		Name:            "strath-neep",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-a", Repo: "/glen/croft"},
		},
	}, 24, 80)
	if err == nil {
		t.Fatal("expected error for nonexistent caller")
	}
}

func TestStartScenarioValidation(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["ben-orch"] = &SessionState{
		ID:         "ben-orch",
		Name:       "ben-session",
		Status:     StatusRunning,
		SystemKind: SystemKindOrchestrator,
	}
	sm.mu.Unlock()

	tests := []struct {
		name    string
		msg     protocol.ScenarioStartMsg
		wantErr string
	}{
		{
			name: "empty scenario name",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch",
				Name:            "",
				Sessions:        []protocol.ScenarioSessionInput{{Name: "braw-a", Repo: "/glen"}},
			},
			wantErr: "scenario name must not be empty",
		},
		{
			name: "invalid scenario name",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch",
				Name:            "Bad Name",
				Sessions:        []protocol.ScenarioSessionInput{{Name: "braw-a", Repo: "/glen"}},
			},
			wantErr: "invalid",
		},
		{
			name: "no sessions",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch",
				Name:            "strath-neep",
				Sessions:        nil,
			},
			wantErr: "at least one session",
		},
		{
			name: "session missing name",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch",
				Name:            "strath-neep",
				Sessions:        []protocol.ScenarioSessionInput{{Repo: "/glen"}},
			},
			wantErr: "name is required",
		},
		{
			name: "session missing repo",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch",
				Name:            "strath-neep",
				Sessions:        []protocol.ScenarioSessionInput{{Name: "braw-a"}},
			},
			wantErr: "repo is required",
		},
		{
			name: "duplicate session name",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch",
				Name:            "strath-neep",
				Sessions: []protocol.ScenarioSessionInput{
					{Name: "braw-a", Repo: "/glen"},
					{Name: "braw-a", Repo: "/glen"},
				},
			},
			wantErr: "duplicate session name",
		},
		{
			name: "oversized startup prompt",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch",
				Name:            "strath-neep",
				Sessions: []protocol.ScenarioSessionInput{{
					Name: "braw-a", Repo: "/glen", Prompt: strings.Repeat("p", protocol.MaxScenarioPromptBytes+1),
				}},
			},
			wantErr: "prompt is too large",
		},
		{
			name: "shared startup prompt",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch",
				Name:            "strath-neep",
				Sessions: []protocol.ScenarioSessionInput{{
					Name: "braw-a", Repo: "/glen", Shared: true, Prompt: "instructions",
				}},
			},
			wantErr: "prompt is not valid for a shared session",
		},
		{
			name: "invalid result before repo preflight",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch",
				Name:            "strath-neep",
				Sessions: []protocol.ScenarioSessionInput{{
					Name: "braw-a", Repo: "/glen",
					Results: []protocol.ScenarioResultSpec{{Name: "review", Format: "yaml", Store: "review.yaml"}},
				}},
			},
			wantErr: "unsupported format",
		},
		{
			name: "missing mirror target",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch", Name: "strath-neep",
				Sessions: []protocol.ScenarioSessionInput{{Name: "reader", Mirror: "outsider"}},
			},
			wantErr: "not a member of this scenario",
		},
		{
			name: "cyclic mirror targets",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch", Name: "strath-neep",
				Sessions: []protocol.ScenarioSessionInput{
					{Name: "reader-a", Mirror: "reader-b"},
					{Name: "reader-b", Mirror: "reader-a"},
				},
			},
			wantErr: "cyclic mirror references",
		},
		{
			name: "mirror repo conflict",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch", Name: "strath-neep",
				Sessions: []protocol.ScenarioSessionInput{
					{Name: "subject", Repo: "/glen"},
					{Name: "reader", Repo: "/glen", Mirror: "subject"},
				},
			},
			wantErr: "mirror and repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sm.StartScenario(tt.msg, 24, 80)
			if err == nil {
				t.Fatal("expected error")
			}

			if got := err.Error(); !strings.Contains(got, tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", got, tt.wantErr)
			}
		})
	}
}

func TestBuildManifest(t *testing.T) {
	sm := newTestSessionManager(t)

	msg := protocol.ScenarioStartMsg{
		CallerSessionID: "ben-1",
		Name:            "strath-kirk",
		Goal:            "Build braw things",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-forge", Repo: "/hame/glen/croft-forge", Role: "Braw forge dev", Prompt: "Use the detailed API plan", Task: "Build braw API",
				Results: []protocol.ScenarioResultSpec{{Name: "review", Format: "markdown", Store: "{session_name}/review.md", Required: true}}},
			{Name: "bonnie-loom", Mirror: "braw-forge", Role: "Bonnie loom dev", Task: "Build bonnie UI",
				Results: []protocol.ScenarioResultSpec{{Name: "facts", Format: "json", Store: "{session_name}/facts.json", Required: true}}},
		},
	}
	sessionIDs := []string{"braw-s1", "bonnie-s2"}
	repos := []string{"croft-forge", "croft-loom"}

	m := sm.buildManifest("sc-glen", msg, repos, sessionIDs, 0)

	if m.Version != 1 {
		t.Errorf("version = %d, want 1", m.Version)
	}

	if m.ScenarioID != "sc-glen" {
		t.Errorf("scenario_id = %q", m.ScenarioID)
	}

	if m.You.Name != "braw-forge" {
		t.Errorf("you.name = %q", m.You.Name)
	}

	if m.You.SessionID != "braw-s1" {
		t.Errorf("you.session_id = %q", m.You.SessionID)
	}

	if m.You.Prompt != "Use the detailed API plan" || m.You.Task != "Build braw API" {
		t.Errorf("self prompt/task = %q/%q", m.You.Prompt, m.You.Task)
	}

	if len(m.Siblings) != 1 {
		t.Fatalf("siblings = %d, want 1", len(m.Siblings))
	}

	if m.Siblings[0].Name != "bonnie-loom" {
		t.Errorf("sibling.name = %q", m.Siblings[0].Name)
	}

	if m.Siblings[0].Repo != "croft-loom" {
		t.Errorf("sibling.repo = %q, want %q", m.Siblings[0].Repo, "croft-loom")
	}

	if len(m.You.Results) != 1 || m.You.Results[0].Destination != "scenarios/sc-glen/results/braw-forge/review.md" {
		t.Fatalf("self results = %+v", m.You.Results)
	}

	if len(m.Siblings[0].Results) != 1 || m.Siblings[0].Results[0].Destination != "scenarios/sc-glen/results/bonnie-loom/facts.json" {
		t.Fatalf("sibling results = %+v", m.Siblings[0].Results)
	}

	if m.Siblings[0].Mirror != "braw-forge" {
		t.Errorf("sibling.mirror = %q, want braw-forge", m.Siblings[0].Mirror)
	}

	if m.Orchestrator.SessionID != "ben-1" {
		t.Errorf("orchestrator.session_id = %q", m.Orchestrator.SessionID)
	}

	// Test JSON round-trip
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	var parsed scenarioManifest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}

	if parsed.ScenarioName != "strath-kirk" {
		t.Errorf("after roundtrip: scenario_name = %q", parsed.ScenarioName)
	}
}

// TestScenarioProgressDerivedFromTodos verifies scenario member progress is
// derived from assigned todo items (the task-done replacement, issue #591): a
// completed assigned item shows as done/total, and a member with no assigned
// items reports "no tracked work" (0/0).
func TestScenarioProgressDerivedFromTodos(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.todos = newTestTodoStore(t)

	sm.mu.Lock()
	sm.state.Scenarios["sc-braw"] = &ScenarioState{
		ID:         "sc-braw",
		Name:       "strath-kirk",
		SessionIDs: []string{"braw-s1", "bonnie-s2"},
		Sessions: []ScenarioSession{
			{Name: "braw-forge", Task: "forge braw API"},
			{Name: "bonnie-loom", Task: "loom bonnie UI"},
		},
	}
	sm.state.Sessions["braw-s1"] = &SessionState{ID: "braw-s1", Name: "braw-forge", Status: StatusRunning, ScenarioID: "sc-braw"}
	sm.state.Sessions["bonnie-s2"] = &SessionState{ID: "bonnie-s2", Name: "bonnie-loom", Status: StatusRunning, ScenarioID: "sc-braw"}
	sm.mu.Unlock()

	// braw-s1 gets two assigned items and completes one; bonnie-s2 gets none.
	item, err := sm.todos.Add(TodoAdd{Scope: "scenario:sc-braw", Title: "forge braw API", Assignee: "braw-s1", CreatedBy: "orch"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := sm.todos.Add(TodoAdd{Scope: "scenario:sc-braw", Title: "second", Assignee: "braw-s1", CreatedBy: "orch"}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := sm.todos.Claim(item.ID, "braw-s1", false); err != nil {
		t.Fatal(err)
	}

	if _, err := sm.todos.Transition(item.ID, TodoStatusDone, "braw-s1", false); err != nil {
		t.Fatal(err)
	}

	sm.mu.RLock()
	record := sm.buildScenarioRecord(sm.state.Scenarios["sc-braw"])
	sm.mu.RUnlock()

	if record.Sessions[0].TodoDone != 1 || record.Sessions[0].TodoTotal != 2 {
		t.Errorf("member 0 progress: got %d/%d, want 1/2", record.Sessions[0].TodoDone, record.Sessions[0].TodoTotal)
	}

	if record.Sessions[1].TodoTotal != 0 {
		t.Errorf("member 1 should have no tracked work, got total %d", record.Sessions[1].TodoTotal)
	}
}

func TestResumeScenarioNotFound(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.ResumeScenario("haar-glen", 24, 80)
	if err == nil {
		t.Fatal("expected error for nonexistent scenario")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want to contain 'not found'", err.Error())
	}
}

func TestAddToScenarioNotFound(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.AddToScenario("haar-glen", protocol.ScenarioSessionInput{
		Name: "braw-new",
		Repo: "/glen/croft",
	}, 24, 80)
	if err == nil {
		t.Fatal("expected error for nonexistent scenario")
	}
}

func TestAddToScenarioValidation(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Scenarios["sc-braw"] = &ScenarioState{
		ID:   "sc-braw",
		Name: "strath-neep",
	}
	sm.mu.Unlock()

	_, err := sm.AddToScenario("strath-neep", protocol.ScenarioSessionInput{
		Name: "",
		Repo: "/glen",
	}, 24, 80)
	if err == nil {
		t.Fatal("expected error for empty name")
	}

	_, err = sm.AddToScenario("strath-neep", protocol.ScenarioSessionInput{
		Name: "bonnie-valid",
		Repo: "",
	}, 24, 80)
	if err == nil {
		t.Fatal("expected error for empty repo")
	}

	_, err = sm.AddToScenario("strath-neep", protocol.ScenarioSessionInput{
		Name: "canny-reader", Mirror: "braw-source",
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "does not support mirror") {
		t.Fatalf("error = %v, want scenario-add mirror rejection", err)
	}
}

func TestAddToScenarioRejectsSharedMember(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.AddToScenario("strath-neep", protocol.ScenarioSessionInput{
		Name: "braw", Repo: "/tmp/croft", Shared: true,
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "cannot add shared") {
		t.Fatalf("error = %v, want shared-add rejection", err)
	}
}

func TestAddToScenarioRejectsPausedPolicy(t *testing.T) {
	sm := newTestSessionManager(t)
	repo := initTempGitRepo(t)
	sm.state.Scenarios["sc-paused"] = &ScenarioState{
		ID: "sc-paused", Name: "strath-paused",
		Policy: &ScenarioPolicyState{Completion: scenariofile.CompletionAll, OnExhausted: scenariofile.OnExhaustedWait, Active: true, Paused: true},
	}

	_, err := sm.AddToScenario("strath-paused", protocol.ScenarioSessionInput{
		Name: "braw", Repo: repo, Task: "review the croft",
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("error = %v, want paused scenario rejection", err)
	}
}

func TestMigrateV10ToV11(t *testing.T) {
	state := &State{
		Version:  10,
		Sessions: map[string]*SessionState{},
	}

	if err := migrateState(state); err != nil {
		t.Fatal(err)
	}

	if state.Version != CurrentStateVersion {
		t.Errorf("version = %d, want %d", state.Version, CurrentStateVersion)
	}

	if state.Scenarios == nil {
		t.Error("Scenarios map should be initialized after migration")
	}
}

// --- StopScenario ---

func TestStopScenarioCovNotFound(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.StopScenario("haar-strath")
	if err == nil {
		t.Fatal("expected error for nonexistent scenario")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want to contain 'not found'", err.Error())
	}
}

func TestStopScenarioCovStopsRunningSkipsRest(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	// running with no PTY and PID 0 -> stopWithReason treats as "already exited"
	// and marks it stopped, exercising the real stop path.
	sm.state.Sessions["braw-run"] = &SessionState{ID: "braw-run", Name: "braw-run", Status: StatusRunning}
	// already stopped -> skipped by the not-running guard.
	sm.state.Sessions["canny-stop"] = &SessionState{ID: "canny-stop", Name: "canny-stop", Status: StatusStopped}
	// shared session -> skipped by the shared guard even though running.
	sm.state.Sessions["ben-shared"] = &SessionState{ID: "ben-shared", Name: "ben-shared", Status: StatusRunning}
	// referenced but absent from state -> skipped by the nil guard.
	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID:         "sc-strath",
		Name:       "strath-clachan",
		SessionIDs: []string{"braw-run", "canny-stop", "ben-shared", "haar-ghost"},
		Sessions: []ScenarioSession{
			{Name: "braw-run"},
			{Name: "canny-stop"},
			{Name: "ben-shared", Shared: true},
			{Name: "haar-ghost"},
		},
	}
	sm.mu.Unlock()

	stopped, err := sm.StopScenario("strath-clachan")
	if err != nil {
		t.Fatalf("StopScenario() error = %v", err)
	}

	if len(stopped) != 1 || stopped[0] != "braw-run" {
		t.Fatalf("stopped = %v, want [braw-run]", stopped)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.state.Sessions["braw-run"].Status != StatusStopped {
		t.Errorf("braw-run status = %q, want stopped", sm.state.Sessions["braw-run"].Status)
	}

	if sm.state.Sessions["ben-shared"].Status != StatusRunning {
		t.Error("shared session should not have been stopped")
	}
}

// --- DeleteScenario ---

func TestDeleteScenarioCovNotFound(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.DeleteScenario("haar-strath")
	if err == nil {
		t.Fatal("expected error for nonexistent scenario")
	}
}

func TestDeleteScenarioCovDeletesAndRemovesRecord(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	// Stopped, no repo/worktree -> Delete succeeds with no git teardown.
	sm.state.Sessions["braw-del"] = &SessionState{ID: "braw-del", Name: "braw-del", Status: StatusStopped}
	// Running session gets stopped first, then deleted.
	sm.state.Sessions["canny-del"] = &SessionState{ID: "canny-del", Name: "canny-del", Status: StatusRunning}
	// Shared session is skipped entirely.
	sm.state.Sessions["ben-shared"] = &SessionState{ID: "ben-shared", Name: "ben-shared", Status: StatusRunning}
	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID:         "sc-strath",
		Name:       "strath-clachan",
		SessionIDs: []string{"braw-del", "canny-del", "ben-shared", "haar-ghost"},
		Sessions: []ScenarioSession{
			{Name: "braw-del"},
			{Name: "canny-del"},
			{Name: "ben-shared", Shared: true},
			{Name: "haar-ghost"}, // absent from state -> counted as deleted
		},
	}
	sm.mu.Unlock()

	deleted, err := sm.DeleteScenario("strath-clachan")
	if err != nil {
		t.Fatalf("DeleteScenario() error = %v", err)
	}

	// braw-del, canny-del, and the ghost (absent) are all reported deleted,
	// in scenario-session order; the shared session is not.
	want := []string{"braw-del", "canny-del", "haar-ghost"}
	if len(deleted) != len(want) {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}

	for i, id := range want {
		if deleted[i] != id {
			t.Errorf("deleted[%d] = %q, want %q", i, deleted[i], id)
		}
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if _, ok := sm.state.Scenarios["sc-strath"]; ok {
		t.Error("scenario record should be removed when all sessions cleaned up")
	}

	if _, ok := sm.state.Sessions["braw-del"]; ok {
		t.Error("braw-del should be deleted")
	}

	if _, ok := sm.state.Sessions["canny-del"]; ok {
		t.Error("canny-del (running) should be stopped then deleted")
	}

	if _, ok := sm.state.Sessions["ben-shared"]; !ok {
		t.Error("shared session should survive scenario delete")
	}
}

func TestDeleteScenarioCovKeepsRecordOnTeardownFailure(t *testing.T) {
	sm := newTestSessionManager(t)

	// repoPath is a real dir but not a git repo, and the worktree dir exists,
	// so git teardown fails and Delete keeps the session for retry.
	notGitRepo := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "bothy")

	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}

	sm.mu.Lock()
	sm.state.Sessions["thrawn-del"] = &SessionState{
		ID: "thrawn-del", Name: "thrawn-del", Status: StatusStopped,
		RepoPath: notGitRepo, WorktreePath: worktree, Branch: "some-branch",
	}
	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID:         "sc-strath",
		Name:       "strath-thrawn",
		SessionIDs: []string{"thrawn-del"},
		Sessions:   []ScenarioSession{{Name: "thrawn-del", Policy: &ScenarioMemberPolicyState{Required: true, Attempt: 1}}},
		Policy:     &ScenarioPolicyState{Completion: scenariofile.CompletionAll, OnExhausted: scenariofile.OnExhaustedFail, Active: true},
	}
	sm.mu.Unlock()

	_, err := sm.DeleteScenario("strath-thrawn")
	if err == nil {
		t.Fatal("expected error when session teardown fails")
	}

	if !strings.Contains(err.Error(), "kept for retry") {
		t.Errorf("error = %q, want mention of retry", err.Error())
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if _, ok := sm.state.Scenarios["sc-strath"]; !ok {
		t.Error("scenario record should be kept when a session failed to delete")
	}

	if !sm.state.Scenarios["sc-strath"].Policy.Paused {
		t.Error("partially deleted scenario policy should remain durably paused")
	}

	// The failed session must survive so cleanup can be retried, with its
	// status restored (Delete downgrades a running session to stopped on
	// teardown failure; this one was already stopped).
	sess, ok := sm.state.Sessions["thrawn-del"]
	if !ok {
		t.Fatal("failed session should be kept in state for retry")
	}

	if sess.Status != StatusStopped {
		t.Errorf("failed session status = %q, want stopped", sess.Status)
	}
}

// --- ScenarioStatus ---

func TestScenarioStatusCovNotFound(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.ScenarioStatus("haar-strath")
	if err == nil {
		t.Fatal("expected error for nonexistent scenario")
	}
}

func TestScenarioStatusCovErrored(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["braw-s1"] = &SessionState{ID: "braw-s1", Name: "braw-a", Status: StatusRunning}
	sm.state.Sessions["dreich-s2"] = &SessionState{ID: "dreich-s2", Name: "dreich-b", Status: StatusErrored}
	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID:         "sc-strath",
		Name:       "strath-dreich",
		Goal:       "mixed goal",
		SessionIDs: []string{"braw-s1", "dreich-s2"},
		Sessions:   []ScenarioSession{{Name: "braw-a"}, {Name: "dreich-b"}},
	}
	sm.mu.Unlock()

	rec, err := sm.ScenarioStatus("strath-dreich")
	if err != nil {
		t.Fatalf("ScenarioStatus() error = %v", err)
	}

	if rec.Status != "errored" {
		t.Errorf("status = %q, want errored", rec.Status)
	}

	if rec.Goal != "mixed goal" {
		t.Errorf("goal = %q, want %q", rec.Goal, "mixed goal")
	}
}

// --- ResumeScenario ---

func TestResumeScenarioCovSkipsAndLogsErrors(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	// Running -> skipped (not stopped/errored).
	sm.state.Sessions["braw-run"] = &SessionState{ID: "braw-run", Name: "braw-run", Status: StatusRunning}
	// Stopped but unknown agent -> Resume errors, logged, not counted as resumed.
	sm.state.Sessions["dreich-stop"] = &SessionState{ID: "dreich-stop", Name: "dreich-stop", Status: StatusStopped, Agent: "nae-such-agent"}
	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID:         "sc-strath",
		Name:       "strath-bide",
		SessionIDs: []string{"braw-run", "dreich-stop", "haar-ghost"},
		Sessions: []ScenarioSession{
			{Name: "braw-run"},
			{Name: "dreich-stop"},
			{Name: "haar-ghost"}, // absent -> nil guard
		},
	}
	sm.mu.Unlock()

	resumed, err := sm.ResumeScenario("strath-bide", 24, 80)
	if err != nil {
		t.Fatalf("ResumeScenario() error = %v", err)
	}

	if len(resumed) != 0 {
		t.Errorf("resumed = %v, want none (all skipped or errored)", resumed)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// The running session must be left untouched by the skip guard.
	if sm.state.Sessions["braw-run"].Status != StatusRunning {
		t.Errorf("braw-run status = %q, want running (should be skipped)", sm.state.Sessions["braw-run"].Status)
	}

	// The stopped session was attempted but Resume failed on the unknown
	// agent, so it stays stopped rather than being marked resumed/running.
	if sm.state.Sessions["dreich-stop"].Status != StatusStopped {
		t.Errorf("dreich-stop status = %q, want stopped (resume should have failed)", sm.state.Sessions["dreich-stop"].Status)
	}
}

// --- AddToScenario ---

func TestAddToScenarioCovUnknownAgent(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.AddToScenario("strath-neep", protocol.ScenarioSessionInput{
		Name:  "bonnie-new",
		Repo:  "/glen/croft",
		Agent: "nae-such-agent",
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("error = %v, want unknown agent", err)
	}
}

func TestAddToScenarioCovRepoResolveError(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.AddToScenario("strath-neep", protocol.ScenarioSessionInput{
		Name: "bonnie-new",
		Repo: filepath.Join(t.TempDir(), "no-such-croft"),
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "resolve repo root") {
		t.Fatalf("error = %v, want resolve repo root", err)
	}
}

func TestAddToScenarioCovSessionNameCollision(t *testing.T) {
	sm := newTestSessionManager(t)
	repo := initTempGitRepo(t)

	sm.mu.Lock()
	sm.state.Sessions["existing"] = &SessionState{ID: "existing", Name: "bonnie-dup", Status: StatusRunning}
	sm.state.Scenarios["sc-strath"] = &ScenarioState{ID: "sc-strath", Name: "strath-neep"}
	sm.mu.Unlock()

	_, err := sm.AddToScenario("strath-neep", protocol.ScenarioSessionInput{
		Name: "bonnie-dup",
		Repo: repo,
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want name collision", err)
	}
}

func TestAddToScenarioRejectsResultDestinationCollision(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox.Enabled = false
	cfg.Agents["claude"] = shAgent()
	sm := newSMWithConfig(t, cfg)
	repo := initTempGitRepo(t)

	results, err := compileScenarioResults(
		"sc-strath", "strath-neep", "braw-existing-id", "braw-existing",
		[]protocol.ScenarioResultSpec{{
			Name: "review", Format: "markdown", Store: "shared/review.md", Required: true,
		}},
		map[string]string{},
	)
	if err != nil {
		t.Fatal(err)
	}

	sm.mu.Lock()
	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID: "sc-strath", Name: "strath-neep",
		SessionIDs: []string{"braw-existing-id"},
		Sessions:   []ScenarioSession{{Name: "braw-existing", Results: results}},
	}
	sm.state.Sessions["braw-existing-id"] = &SessionState{
		ID: "braw-existing-id", Name: "braw-existing", Status: StatusRunning,
	}
	sm.mu.Unlock()

	_, err = sm.AddToScenario("strath-neep", protocol.ScenarioSessionInput{
		Name: "canny-new", Repo: repo, Agent: "claude",
		Results: []protocol.ScenarioResultSpec{{
			Name: "facts", Format: "json", Store: "shared/review.md", Required: true,
		}},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("error = %v, want result destination collision", err)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if got := len(sm.state.Scenarios["sc-strath"].Sessions); got != 1 {
		t.Fatalf("scenario members = %d, want original member only", got)
	}

	for _, session := range sm.state.Sessions {
		if session.Name == "canny-new" {
			t.Fatalf("colliding member was not rolled back: %+v", session)
		}
	}
}

// --- StartScenario collision / preflight paths (return before Create) ---

func startScenarioOrchestrator(t *testing.T) *SessionManager {
	t.Helper()
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["ben-orch"] = &SessionState{
		ID: "ben-orch", Name: "ben-orch", Status: StatusRunning,
		SystemKind: SystemKindOrchestrator,
	}
	sm.mu.Unlock()

	return sm
}

func TestStartScenarioCovNotGitRepo(t *testing.T) {
	sm := startScenarioOrchestrator(t)

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch",
		Name:            "strath-neep",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-a", Repo: filepath.Join(t.TempDir(), "no-croft")},
		},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("error = %v, want not-a-git-repository", err)
	}
}

func TestStartScenarioCovUnknownAgent(t *testing.T) {
	sm := startScenarioOrchestrator(t)
	repo := initTempGitRepo(t)

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch",
		Name:            "strath-neep",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-a", Repo: repo, Agent: "nae-such-agent"},
		},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("error = %v, want unknown agent", err)
	}
}

func TestStartScenarioCovDuplicateScenarioName(t *testing.T) {
	sm := startScenarioOrchestrator(t)
	repo := initTempGitRepo(t)

	sm.mu.Lock()
	sm.state.Scenarios["sc-existing"] = &ScenarioState{ID: "sc-existing", Name: "strath-neep"}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch",
		Name:            "strath-neep",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-a", Repo: repo},
		},
	}, 24, 80)
	// Use the full scenario-specific message so this can't be satisfied by the
	// session-name collision path (which also contains "already exists").
	if err == nil || !strings.Contains(err.Error(), `scenario "strath-neep" already exists`) {
		t.Fatalf("error = %v, want scenario-already-exists", err)
	}
}

func TestStartScenarioCovSessionNameCollision(t *testing.T) {
	sm := startScenarioOrchestrator(t)
	repo := initTempGitRepo(t)

	sm.mu.Lock()
	sm.state.Sessions["dup"] = &SessionState{ID: "dup", Name: "braw-a", Status: StatusRunning}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch",
		Name:            "strath-neep",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-a", Repo: repo},
		},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "session name") {
		t.Fatalf("error = %v, want session-name-already-exists", err)
	}
}

func TestStartScenarioCovSharedSessionNotRunning(t *testing.T) {
	sm := startScenarioOrchestrator(t)
	repo := initTempGitRepo(t)

	// A session with the shared name exists but is stopped, so the shared
	// block's StatusRunning requirement (scenario.go) is not satisfied and
	// reuse must be rejected — covers "present but not running", not merely
	// "absent".
	sm.mu.Lock()
	sm.state.Sessions["stale-shared"] = &SessionState{
		ID: "stale-shared", Name: "clachan-shared", Status: StatusStopped,
	}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch",
		Name:            "strath-neep",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "clachan-shared", Repo: repo, Shared: true},
		},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "no running session") {
		t.Fatalf("error = %v, want shared-session-not-running", err)
	}
}

func TestStartScenarioMirrorRequiresSandboxBeforeReservation(t *testing.T) {
	sm := startScenarioOrchestrator(t)
	repo := initTempGitRepo(t)

	sm.mu.Lock()
	sm.state.Sessions["braw-source"] = &SessionState{
		ID: "braw-source", Name: "subject", Status: StatusRunning,
		RepoPath: repo, WorktreePath: repo,
	}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch", Name: "strath-readers",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "subject", Repo: repo, Shared: true},
			{Name: "reader", Mirror: "subject"},
		},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "mirror requires sandbox") {
		t.Fatalf("error = %v, want preflight sandbox requirement", err)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.state.Scenarios) != 0 {
		t.Errorf("sandbox preflight reserved scenario state: %+v", sm.state.Scenarios)
	}
}

func TestStartScenarioRejectsAmbiguousSharedSource(t *testing.T) {
	sm := startScenarioOrchestrator(t)
	repo := initTempGitRepo(t)

	sm.mu.Lock()
	sm.state.Sessions["source-a"] = &SessionState{ID: "source-a", Name: "subject", Status: StatusRunning, RepoPath: repo, WorktreePath: repo}
	sm.state.Sessions["source-b"] = &SessionState{ID: "source-b", Name: "subject", Status: StatusRunning, RepoPath: repo, WorktreePath: repo}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch", Name: "strath-ambiguous",
		Sessions: []protocol.ScenarioSessionInput{{Name: "subject", Repo: repo, Shared: true}},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error = %v, want ambiguous shared source", err)
	}
}

func TestStartScenarioRejectsSharedMirrorSourceWithoutWorktree(t *testing.T) {
	sm := startScenarioOrchestrator(t)
	sm.sandboxResolver = func(string) (bool, error) { return true, nil }

	sm.mu.Lock()
	sm.state.Sessions["braw-source"] = &SessionState{
		ID: "braw-source", Name: "subject", Status: StatusRunning,
	}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch", Name: "strath-no-worktree",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "subject", Shared: true},
			{Name: "reader", Mirror: "subject"},
		},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "has no worktree") {
		t.Fatalf("error = %v, want incompatible source-worktree rejection", err)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.state.Scenarios) != 0 {
		t.Errorf("worktree preflight reserved scenario state: %+v", sm.state.Scenarios)
	}
}

// --- persistManifest / republishManifests (store side effects) ---

func TestPersistManifestCovWritesStore(t *testing.T) {
	sm := newTestSessionManager(t)

	msg := protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch",
		Name:            "strath-loch",
		Goal:            "build the loch",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-forge", Role: "forger", Task: "forge"},
			{Name: "bonnie-loom", Role: "loomer", Task: "loom"},
		},
	}
	repos := []string{"croft-forge", "croft-loom"}
	sessionIDs := []string{"braw-s1", "bonnie-s2"}

	sm.persistManifest("sc-loch", msg, repos, sessionIDs)

	storeDir := store.SharedStorePath(sm.paths.DataDir)

	body, err := store.Get(storeDir, "scenarios/sc-loch/manifest-braw-forge.json")
	if err != nil {
		t.Fatalf("manifest not persisted: %v", err)
	}

	var m scenarioManifest
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("manifest not valid JSON: %v", err)
	}

	if m.You.Name != "braw-forge" {
		t.Errorf("you.name = %q, want braw-forge", m.You.Name)
	}

	if m.ScenarioName != "strath-loch" {
		t.Errorf("scenario_name = %q", m.ScenarioName)
	}

	if _, err := store.Get(storeDir, "scenarios/sc-loch/manifest-bonnie-loom.json"); err != nil {
		t.Errorf("second manifest not persisted: %v", err)
	}
}

func TestRepublishManifestsCovNoScenario(t *testing.T) {
	sm := newTestSessionManager(t)

	// Unknown scenario id: republish returns before touching the store, so no
	// manifest directory should be created (and it must not panic).
	sm.republishManifests("sc-nope")

	storeDir := store.SharedStorePath(sm.paths.DataDir)
	if _, err := os.Stat(filepath.Join(storeDir, "scenarios", "sc-nope")); !os.IsNotExist(err) {
		t.Errorf("expected no manifest dir for unknown scenario, stat err = %v", err)
	}
}

func TestRepublishManifestsCovWritesStore(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Scenarios["sc-loch"] = &ScenarioState{
		ID:             "sc-loch",
		Name:           "strath-loch",
		OrchestratorID: "ben-orch",
		Goal:           "build the loch",
		SessionIDs:     []string{"braw-s1", "bonnie-s2"},
		Sessions: []ScenarioSession{
			{Name: "braw-forge", Role: "forger", Task: "forge", Repo: "croft-forge", Agent: "claude"},
			{Name: "bonnie-loom", Role: "loomer", Task: "loom", Repo: "croft-loom", Agent: "claude"},
		},
	}
	sm.mu.Unlock()

	sm.republishManifests("sc-loch")

	storeDir := store.SharedStorePath(sm.paths.DataDir)

	body, err := store.Get(storeDir, "scenarios/sc-loch/manifest-bonnie-loom.json")
	if err != nil {
		t.Fatalf("republished manifest not persisted: %v", err)
	}

	var m scenarioManifest
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("manifest not valid JSON: %v", err)
	}

	// From bonnie-loom's perspective, braw-forge is a sibling.
	if len(m.Siblings) != 1 || m.Siblings[0].Name != "braw-forge" {
		t.Errorf("siblings = %+v, want [braw-forge]", m.Siblings)
	}

	if m.Orchestrator.SessionID != "ben-orch" {
		t.Errorf("orchestrator.session_id = %q, want ben-orch", m.Orchestrator.SessionID)
	}
}

// --- buildScenarioRecord edge cases ---

func TestBuildScenarioRecordCovEmpty(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	rec := sm.buildScenarioRecord(&ScenarioState{ID: "sc-e", Name: "strath-empty"})
	sm.mu.Unlock()

	// StartScenario rejects empty scenarios, so this only arises from corrupted
	// or hand-built in-memory state. Documents that buildScenarioRecord reports
	// "running" for total == 0 because the running == total branch wins first.
	if rec.Status != "running" {
		t.Errorf("status = %q, want running for empty scenario", rec.Status)
	}
}

// initScenarioGitRepo creates a real git repo the scenario preflight
// (git.IsInsideGitRepo / RepoRootPath) will accept, with global config
// neutralized so commit signing can't interfere.
func initScenarioGitRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	gitRun(t, "", "init", "--initial-branch=main", dir)
	gitRun(t, dir, "commit", "--allow-empty", "-m", "initial")

	root, err := git.RepoRootPath(dir)
	if err != nil {
		t.Fatalf("resolve scenario repo root: %v", err)
	}

	return root
}

// TestStartScenarioSharedSuccess_Cov2 drives StartScenario all the way through
// its success path using only a shared session, which reuses an existing running
// session instead of spawning a new agent. This exercises the reserve phase, the
// shared-reuse branch, the final session-ID update, and the manifest publish +
// persist steps — the bulk of StartScenario that round 1's error-path tests
// never reached — without depending on a real agent binary.
func TestStartScenarioSharedSuccess_Cov2(t *testing.T) {
	sm := newTestSessionManager(t)

	msgStore, err := NewMsgStore(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = msgStore.Close() })

	sm.SetMsgStore(msgStore)

	repo := initScenarioGitRepo(t)

	sm.mu.Lock()
	// The orchestrator caller.
	sm.state.Sessions["ben-orch"] = &SessionState{
		ID: "ben-orch", Name: OrchestratorSessionName,
		SystemKind: SystemKindOrchestrator, Status: StatusRunning,
	}
	// The already-running session the scenario will reuse as shared.
	sm.state.Sessions["braw-live"] = &SessionState{
		ID: "braw-live", Name: "braw-mason", Status: StatusRunning,
		RepoPath: repo,
	}
	sm.mu.Unlock()

	scenario, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch",
		Name:            "strath-clachan",
		Goal:            "raise the brig",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-mason", Repo: repo, Role: "stonemason", Task: "cut the keystone", Shared: true},
		},
	}, 24, 80)
	if err != nil {
		t.Fatalf("StartScenario() error = %v", err)
	}

	if scenario == nil || scenario.Name != "strath-clachan" {
		t.Fatalf("scenario = %+v, want name strath-clachan", scenario)
	}

	// The shared session's real ID must be recorded, not a discarded placeholder.
	if len(scenario.SessionIDs) != 1 || scenario.SessionIDs[0] != "braw-live" {
		t.Fatalf("SessionIDs = %v, want [braw-live]", scenario.SessionIDs)
	}

	// The manifest must have been published to the shared session's inbox.
	msgs, err := msgStore.Read("inbox:braw-live", "braw-live", false, "")
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("inbox message count = %d, want 1", len(msgs))
	}

	var manifest scenarioManifest
	if err := json.Unmarshal([]byte(msgs[0].Body), &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	if manifest.You.Name != "braw-mason" || manifest.Goal != "raise the brig" {
		t.Fatalf("manifest self = %+v, want braw-mason/raise the brig", manifest)
	}

	// And persisted to the shared store.
	storeDir := store.SharedStorePath(sm.paths.DataDir)
	key := "scenarios/" + scenario.ID + "/manifest-braw-mason.json"

	body, err := store.Get(storeDir, key)
	if err != nil {
		t.Fatalf("store.Get(%q): %v", key, err)
	}

	if !strings.Contains(body, "raise the brig") {
		t.Fatalf("persisted manifest missing goal: %q", body)
	}
}

// TestStartScenarioSharedRepoNotGit_Cov2 asserts the preflight git check runs
// for shared sessions too: a shared session pointing at a non-git repo is
// rejected before any state is reserved.
func TestStartScenarioSharedRepoNotGit_Cov2(t *testing.T) {
	sm := newTestSessionManager(t)

	notGit := t.TempDir()

	sm.mu.Lock()
	sm.state.Sessions["ben-orch"] = &SessionState{
		ID: "ben-orch", SystemKind: SystemKindOrchestrator, Status: StatusRunning,
	}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch",
		Name:            "strath-haar",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "dreich-one", Repo: notGit, Shared: true},
		},
	}, 24, 80)
	if err == nil {
		t.Fatal("expected error for non-git repo")
	}

	if !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("error = %q, want git-repo complaint", err.Error())
	}
}

// TestStartScenarioRepoNotAllowed_Cov2 covers the allowed_repo_paths gate:
// a repo outside the configured allow-list is rejected.
func TestStartScenarioRepoNotAllowed_Cov2(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := initScenarioGitRepo(t)

	sm.mu.Lock()
	// Restrict allowed repos to somewhere the test repo is not under.
	sm.cfg.AllowedRepoPaths = []string{filepath.Join(t.TempDir(), "elsewhere")}
	sm.state.Sessions["ben-orch"] = &SessionState{
		ID: "ben-orch", SystemKind: SystemKindOrchestrator, Status: StatusRunning,
	}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch",
		Name:            "strath-fash",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "dreich-two", Repo: repo, Shared: true},
		},
	}, 24, 80)
	if err == nil {
		t.Fatal("expected error for disallowed repo path")
	}

	if !strings.Contains(err.Error(), "allowed_repo_paths") {
		t.Fatalf("error = %q, want allowed_repo_paths complaint", err.Error())
	}
}

// TestRepublishManifestsFullPath_Cov2 exercises republishManifests end to end:
// it must publish a manifest to every member's inbox and persist each to the
// shared store.
func TestRepublishManifestsFullPath_Cov2(t *testing.T) {
	sm := newTestSessionManager(t)

	msgStore, err := NewMsgStore(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = msgStore.Close() })

	sm.SetMsgStore(msgStore)

	sm.mu.Lock()
	sm.state.Scenarios["sc-blether"] = &ScenarioState{
		ID:             "sc-blether",
		Name:           "strath-blether",
		OrchestratorID: "ben-orch",
		Goal:           "share the news",
		SessionIDs:     []string{"braw-a", "canny-b"},
		Sessions: []ScenarioSession{
			{Name: "braw-a", Repo: "croft-a", Role: "scribe"},
			{Name: "canny-b", Repo: "croft-b", Role: "herald"},
		},
	}
	sm.mu.Unlock()

	sm.republishManifests("sc-blether")

	for _, id := range []string{"braw-a", "canny-b"} {
		msgs, err := msgStore.Read("inbox:"+id, id, false, "")
		if err != nil {
			t.Fatalf("read inbox %q: %v", id, err)
		}

		if len(msgs) != 1 {
			t.Fatalf("inbox %q message count = %d, want 1", id, len(msgs))
		}
	}

	storeDir := store.SharedStorePath(sm.paths.DataDir)
	if _, err := store.Get(storeDir, "scenarios/sc-blether/manifest-braw-a.json"); err != nil {
		t.Fatalf("persisted manifest for braw-a missing: %v", err)
	}

	if _, err := os.Stat(storeDir); err != nil {
		t.Fatalf("shared store dir missing: %v", err)
	}
}

// newScenarioOrchestrator builds a SessionManager whose default agent is a
// sleeper (a real process that keeps the PTY alive so Create succeeds), plus a
// running orchestrator session and a message store — ready to drive
// StartScenario through real (non-shared) session creation. Spawned PTYs are
// torn down on cleanup.
func newScenarioOrchestrator(t *testing.T) (*SessionManager, string) {
	t.Helper()

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.DefaultAgent = "sleeper"
	cfg.Agents["sleeper"] = config.Agent{Command: "sh", Args: []string{"-c", "exec sleep 60"}}

	sm := newSMWithConfig(t, cfg)

	msgStore, err := NewMsgStore(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = msgStore.Close() })
	sm.SetMsgStore(msgStore)

	const orchID = "ben-orch"

	sm.mu.Lock()
	sm.state.Sessions[orchID] = &SessionState{
		ID: orchID, Name: OrchestratorSessionName,
		SystemKind: SystemKindOrchestrator, Status: StatusRunning,
	}
	sm.mu.Unlock()

	t.Cleanup(func() {
		sm.mu.RLock()

		ids := make([]string, 0, len(sm.sessions))
		for id := range sm.sessions {
			ids = append(ids, id)
		}

		sm.mu.RUnlock()

		for _, id := range ids {
			stopAndClosePTY(sm, id)
		}
	})

	return sm, orchID
}

func configureScenarioPromptRecorder(sm *SessionManager, recordPath string) {
	sm.mu.Lock()
	sm.cfg.Agents["recorder"] = config.Agent{
		Command: "sh",
		Args:    []string{"-c", `printf %s "$0" > "$GRAITH_PROMPT_RECORD"; exec sleep 60`},
		Env:     map[string]string{"GRAITH_PROMPT_RECORD": recordPath},
	}
	sm.mu.Unlock()
}

func readScenarioPromptRecord(t *testing.T, path string) string {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil {
			return string(body)
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("startup prompt was not recorded at %s", path)

	return ""
}

func TestStartScenarioPromptOnlyRequiredResultCompletesWithoutTodo(t *testing.T) {
	sm, orchID := newScenarioOrchestrator(t)
	sm.todos = newTestTodoStore(t)
	repo := initScenarioGitRepo(t)
	recordPath := filepath.Join(t.TempDir(), "prompt.txt")
	configureScenarioPromptRecorder(sm, recordPath)

	prompt := "Inspect the project and publish the declared report result.\n\n" + strings.Repeat("canny detail ", 60)

	scenario, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: orchID,
		Name:            "strath-reporter",
		Sessions: []protocol.ScenarioSessionInput{{
			Name: "canny-reporter", Repo: repo, Agent: "recorder", Prompt: prompt,
			Results: []protocol.ScenarioResultSpec{{
				Name: "report", Format: "markdown", Store: "{session_name}/report.md", Required: true,
			}},
		}},
	}, 24, 80)
	if err != nil {
		t.Fatalf("StartScenario: %v", err)
	}

	if got := readScenarioPromptRecord(t, recordPath); got != prompt {
		t.Fatalf("launched prompt length/content = %d/%q, want %d bytes", len(got), got, len(prompt))
	}

	items, err := sm.todos.List("scenario:"+scenario.ID, TodoFilter{})
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 0 {
		t.Fatalf("prompt-only member seeded todos: %+v", items)
	}

	record, err := sm.ScenarioStatus("strath-reporter")
	if err != nil {
		t.Fatal(err)
	}

	if record.Status == "complete" || record.Sessions[0].Prompt != prompt || record.Sessions[0].Task != "" || record.Sessions[0].TodoTotal != 0 {
		t.Fatalf("pending prompt-only status = %+v", record)
	}

	persisted, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	if got := persisted.Scenarios[scenario.ID].Sessions[0]; got.Prompt != prompt || got.Task != "" {
		t.Fatalf("persisted prompt/task = %q/%q", got.Prompt, got.Task)
	}

	manifestMessages, err := sm.messages.Read("inbox:"+scenario.SessionIDs[0], scenario.SessionIDs[0], false, "")
	if err != nil || len(manifestMessages) != 1 {
		t.Fatalf("manifest messages = %d, err=%v", len(manifestMessages), err)
	}

	var manifest scenarioManifest
	if err := json.Unmarshal([]byte(manifestMessages[0].Body), &manifest); err != nil {
		t.Fatal(err)
	}

	if manifest.You.Prompt != prompt || manifest.You.Task != "" {
		t.Fatalf("manifest prompt/task = %q/%q", manifest.You.Prompt, manifest.You.Task)
	}

	if _, err := sm.PublishScenarioResult(
		authContext{authenticated: true, role: roleSession, sessionID: scenario.SessionIDs[0]},
		protocol.ScenarioResultPublishMsg{Scenario: scenario.Name, Name: "report", Body: "# Canny report\n"},
	); err != nil {
		t.Fatal(err)
	}

	if actions := sm.planScenarioPolicyActionsFor(time.Now().UTC(), scenario.ID); len(actions) != 0 {
		t.Fatalf("unexpected policy actions after result publication: %+v", actions)
	}

	record, err = sm.ScenarioStatus("strath-reporter")
	if err != nil {
		t.Fatal(err)
	}

	if record.Status != "complete" || record.Policy != nil {
		t.Fatalf("completed prompt-only result status = %+v", record)
	}
}

func TestStartScenarioPromptAndTaskRemainIndependent(t *testing.T) {
	sm, orchID := newScenarioOrchestrator(t)
	sm.todos = newTestTodoStore(t)
	repo := initScenarioGitRepo(t)
	recordPath := filepath.Join(t.TempDir(), "prompt.txt")
	configureScenarioPromptRecorder(sm, recordPath)

	prompt := "Follow these launch instructions:\n" + strings.Repeat("braw context ", 50)
	task := "publish the tracked report"

	scenario, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: orchID,
		Name:            "strath-both",
		Sessions: []protocol.ScenarioSessionInput{{
			Name: "braw-reporter", Repo: repo, Agent: "recorder", Prompt: prompt, Task: task,
		}},
	}, 24, 80)
	if err != nil {
		t.Fatalf("StartScenario: %v", err)
	}

	if got := readScenarioPromptRecord(t, recordPath); got != prompt {
		t.Fatalf("launched prompt = %q, want %q", got, prompt)
	}

	items, err := sm.todos.List("scenario:"+scenario.ID, TodoFilter{})
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 1 || items[0].Title != task || items[0].Title == prompt {
		t.Fatalf("seeded todo = %+v, want tracked task only", items)
	}

	record, err := sm.ScenarioStatus("strath-both")
	if err != nil {
		t.Fatal(err)
	}

	if record.Sessions[0].Prompt != prompt || record.Sessions[0].Task != task {
		t.Fatalf("status prompt/task = %q/%q", record.Sessions[0].Prompt, record.Sessions[0].Task)
	}
}

// newMirroredScenarioOrchestrator uses the real safehouse command-line adapter
// with a tiny exec-through backend stub. That keeps this lifecycle test
// portable while still driving scenario members through Create's mirror
// branch, sandbox option construction, scratch setup, PTY start, resume, and
// deletion. Backend-specific read-only enforcement remains covered in the
// sandbox package's safehouse and nono enforcement tests.
func newMirroredScenarioOrchestrator(t *testing.T) (*SessionManager, string) {
	t.Helper()

	backend := filepath.Join(t.TempDir(), "safehouse-stub")
	if err := os.WriteFile(backend, []byte("#!/bin/sh\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = \"--\" ]; then\n    shift\n    exec \"$@\"\n  fi\n  shift\ndone\nexit 64\n"), 0o755); err != nil { //nolint:gosec // G306: test backend stub must be executable
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.DefaultAgent = "sleeper"
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"60"}}
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: "safehouse", Command: backend}

	sm := newSMWithConfig(t, cfg)
	sm.sandboxResolver = func(string) (bool, error) { return true, nil }

	msgStore, err := NewMsgStore(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = msgStore.Close() })
	sm.SetMsgStore(msgStore)

	const orchID = "ben-orch"

	sm.mu.Lock()
	sm.state.Sessions[orchID] = &SessionState{
		ID: orchID, Name: OrchestratorSessionName,
		SystemKind: SystemKindOrchestrator, Status: StatusRunning,
	}
	sm.mu.Unlock()

	t.Cleanup(func() {
		sm.mu.RLock()

		ids := make([]string, 0, len(sm.sessions))
		for id := range sm.sessions {
			ids = append(ids, id)
		}

		sm.mu.RUnlock()

		for _, id := range ids {
			stopAndClosePTY(sm, id)
		}
	})

	return sm, orchID
}

func TestStartScenarioMirrorsSharedSourceLifecycle(t *testing.T) {
	sm, orchID := newMirroredScenarioOrchestrator(t)

	repo := initScenarioGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dreich-uncommitted.txt"), []byte("visible before commit\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	worktreesBefore := gitOut(t, repo, "worktree", "list", "--porcelain")

	const sourceID = "braw-source"

	sm.mu.Lock()
	sm.state.Sessions[sourceID] = &SessionState{
		ID: sourceID, Name: "subject", Status: StatusRunning,
		RepoPath: repo, RepoName: filepath.Base(repo), WorktreePath: repo, BaseBranch: "main",
	}
	sm.mu.Unlock()

	scenario, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: orchID, Name: "strath-readers", Goal: "inspect the subject",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "subject", Repo: repo, Shared: true},
			{Name: "reader-a", Mirror: "subject", Role: "auditor"},
			{Name: "reader-b", Mirror: "subject", Role: "investigator"},
			{Name: "reader-c", Mirror: "reader-a", Role: "second opinion"},
		},
	}, 24, 80)
	if err != nil {
		t.Fatalf("StartScenario: %v", err)
	}

	if len(scenario.SessionIDs) != 4 || scenario.SessionIDs[0] != sourceID {
		t.Fatalf("session IDs = %v, want shared source plus three readers", scenario.SessionIDs)
	}

	for i, wantSource := range []string{sourceID, sourceID, scenario.SessionIDs[1]} {
		id := scenario.SessionIDs[i+1]

		sm.mu.RLock()
		reader := sm.state.Sessions[id]
		sm.mu.RUnlock()

		if reader == nil || !reader.Mirror || reader.MirrorSourceID != wantSource {
			t.Fatalf("reader %d state = %+v, want mirror source %q", i, reader, wantSource)
		}

		if reader.WorktreePath != repo || reader.Branch != "" {
			t.Errorf("reader %d worktree/branch = %q/%q, want source path and no branch", i, reader.WorktreePath, reader.Branch)
		}

		body, readErr := os.ReadFile(filepath.Join(reader.WorktreePath, "dreich-uncommitted.txt"))
		if readErr != nil || string(body) != "visible before commit\n" {
			t.Errorf("reader %d cannot see source's uncommitted file: body=%q err=%v", i, body, readErr)
		}
	}

	if got := gitOut(t, repo, "worktree", "list", "--porcelain"); got != worktreesBefore {
		t.Errorf("mirrored members created Git worktrees:\nbefore:\n%s\nafter:\n%s", worktreesBefore, got)
	}

	record, err := sm.ScenarioStatus("strath-readers")
	if err != nil {
		t.Fatal(err)
	}

	if record.Sessions[1].Mirror != "subject" || record.Sessions[3].Mirror != "reader-a" {
		t.Errorf("status mirror relationships = %+v", record.Sessions)
	}

	msgs, err := sm.messages.Read("inbox:"+scenario.SessionIDs[1], scenario.SessionIDs[1], false, "")
	if err != nil || len(msgs) != 1 {
		t.Fatalf("reader manifest messages = %d, err=%v", len(msgs), err)
	}

	var manifest scenarioManifest
	if err := json.Unmarshal([]byte(msgs[0].Body), &manifest); err != nil {
		t.Fatal(err)
	}

	if manifest.You.Mirror != "subject" || manifest.Siblings[2].Mirror != "reader-a" {
		t.Errorf("manifest mirror relationships = %+v", manifest)
	}

	persisted, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	if persisted.Scenarios[scenario.ID].Sessions[1].Mirror != "subject" ||
		!persisted.Sessions[scenario.SessionIDs[1]].Mirror {
		t.Error("mirror relationships did not survive state reload")
	}

	stopped, err := sm.StopScenario("strath-readers")
	if err != nil || len(stopped) != 3 {
		t.Fatalf("StopScenario = %v, err=%v, want three readers", stopped, err)
	}

	sm.mu.RLock()
	sourceStatus := sm.state.Sessions[sourceID].Status
	sm.mu.RUnlock()

	if sourceStatus != StatusRunning {
		t.Error("shared source must survive scenario stop")
	}

	resumed, err := sm.ResumeScenario("strath-readers", 24, 80)
	if err != nil || len(resumed) != 3 {
		t.Fatalf("ResumeScenario = %v, err=%v, want three readers", resumed, err)
	}

	deleted, err := sm.DeleteScenario("strath-readers")
	if err != nil || len(deleted) != 3 {
		t.Fatalf("DeleteScenario = %v, err=%v, want three readers", deleted, err)
	}

	sm.mu.RLock()
	_, sourceExists := sm.state.Sessions[sourceID]
	sm.mu.RUnlock()

	if !sourceExists {
		t.Error("shared source must survive scenario delete")
	}
}

func TestStartScenarioMirrorsScenarioOwnedSource(t *testing.T) {
	sm, orchID := newMirroredScenarioOrchestrator(t)
	repo := initScenarioGitRepo(t)

	scenario, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: orchID, Name: "strath-owned-source",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "subject", Repo: repo},
			{Name: "reader", Mirror: "subject"},
		},
	}, 24, 80)
	if err != nil {
		t.Fatalf("StartScenario: %v", err)
	}

	if len(scenario.SessionIDs) != 2 {
		t.Fatalf("session IDs = %v, want owned source and reader", scenario.SessionIDs)
	}

	var (
		source, sourceOK = sm.Get(scenario.SessionIDs[0])
		reader, readerOK = sm.Get(scenario.SessionIDs[1])
	)

	if !sourceOK || !readerOK {
		t.Fatalf("source/reader missing after start: source=%t reader=%t", sourceOK, readerOK)
	}

	if !reader.Mirror || reader.MirrorSourceID != source.ID || reader.WorktreePath != source.WorktreePath {
		t.Fatalf("reader = %+v, want mirror of scenario-owned source %+v", reader, source)
	}

	deleted, err := sm.DeleteScenario("strath-owned-source")
	if err != nil || len(deleted) != 2 {
		t.Fatalf("DeleteScenario = %v, err=%v, want source and reader", deleted, err)
	}
}

func TestStartScenarioMirrorFailureRollsBackReaders(t *testing.T) {
	sm, orchID := newMirroredScenarioOrchestrator(t)
	repo := initScenarioGitRepo(t)

	badAgent := config.Agent{Command: "sleep", Args: []string{"60"}}
	badAgent.Sandbox.Command = filepath.Join(t.TempDir(), "missing-safehouse")

	sm.mu.Lock()
	sm.cfg.Agents["broken"] = badAgent
	sm.state.Sessions["braw-source"] = &SessionState{
		ID: "braw-source", Name: "subject", Status: StatusRunning,
		RepoPath: repo, RepoName: filepath.Base(repo), WorktreePath: repo,
	}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: orchID, Name: "strath-rollback",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "subject", Repo: repo, Shared: true},
			{Name: "reader-good", Mirror: "subject"},
			{Name: "reader-bad", Mirror: "subject", Agent: "broken"},
		},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "failed to create") {
		t.Fatalf("error = %v, want mirrored-member creation failure", err)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if _, ok := sm.state.Sessions["braw-source"]; !ok {
		t.Error("rollback removed shared source")
	}

	for _, session := range sm.state.Sessions {
		if strings.HasPrefix(session.Name, "reader-") {
			t.Errorf("rollback left mirrored member %+v", session)
		}
	}

	for _, sc := range sm.state.Scenarios {
		if sc.Name == "strath-rollback" {
			t.Errorf("rollback left scenario %+v", sc)
		}
	}
}

// TestStartScenarioStarredAndIncludes drives StartScenario through real session
// creation and asserts the per-session star / includes fields (#1046) reach the
// created SessionState: the session is starred and carries the extra worktree.
func TestStartScenarioStarredAndIncludes(t *testing.T) {
	sm, orchID := newScenarioOrchestrator(t)

	repo := initScenarioGitRepo(t)
	inc := initScenarioGitRepo(t)

	scenario, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: orchID,
		Name:            "strath-brig",
		Goal:            "raise the brig",
		Sessions: []protocol.ScenarioSessionInput{
			// No task/prompt: the sleeper agent stays alive so we can inspect the
			// running session's state without racing an async crash-triggered save.
			{Name: "braw-mason", Repo: repo, Role: "mason",
				Includes: []string{inc}, Star: true},
		},
	}, 24, 80)
	if err != nil {
		t.Fatalf("StartScenario() error = %v", err)
	}

	if len(scenario.SessionIDs) != 1 {
		t.Fatalf("SessionIDs = %v, want one", scenario.SessionIDs)
	}

	id := scenario.SessionIDs[0]

	sm.mu.RLock()

	created := sm.state.Sessions[id]
	starred := created.Starred
	includes := created.Includes

	sm.mu.RUnlock()

	if !starred {
		t.Error("scenario session should be starred (star = true)")
	}

	if len(includes) != 1 || includes[0].RepoName != filepath.Base(inc) {
		t.Errorf("includes = %+v, want the extra worktree %q", includes, filepath.Base(inc))
	}
}

// TestStartScenarioRollbackDeletesStarredMember is the regression test for the
// rollback bug (#1046): a starred member that succeeds while a sibling fails to
// start must still be torn down. Delete refuses starred sessions, so without the
// unstar-before-delete in the rollback path the starred member is stranded and
// the scenario record is retained. The failing sibling uses a non-existent base
// branch (passes preflight, fails in Create).
func TestStartScenarioRollbackDeletesStarredMember(t *testing.T) {
	sm, orchID := newScenarioOrchestrator(t)

	goodRepo := initScenarioGitRepo(t)
	badRepo := initScenarioGitRepo(t)

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: orchID,
		Name:            "strath-thrawn",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-mason", Repo: goodRepo, Star: true},
			{Name: "dreich-hand", Repo: badRepo, Base: "no-such-branch"},
		},
	}, 24, 80)
	if err == nil {
		t.Fatal("expected StartScenario to fail when a sibling cannot start")
	}

	// The scenario record must be fully rolled back — a stranded starred member
	// would have left rollbackErrors non-empty and kept the record.
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, sc := range sm.state.Scenarios {
		if sc.Name == "strath-thrawn" {
			t.Errorf("scenario record %q should have been rolled back, still present with sessions %v", sc.Name, sc.SessionIDs)
		}
	}

	for id, s := range sm.state.Sessions {
		if s.Name == "braw-mason" || s.Name == "dreich-hand" {
			t.Errorf("scenario session %q (%s, starred=%v) should have been deleted in rollback", s.Name, id, s.Starred)
		}
	}
}

func TestStartScenarioRejectsOversizedTaskBeforeCreatingMembers(t *testing.T) {
	sm, orchID := newScenarioOrchestrator(t)
	sm.todos = newTestTodoStore(t)
	sm.todos.SetMaxTitle(12)

	repo := initScenarioGitRepo(t)

	sm.mu.Lock()
	sm.state.Sessions["braw-shared"] = &SessionState{
		ID: "braw-shared", Name: "braw-shared", RepoPath: repo, Status: StatusRunning,
	}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: orchID,
		Name:            "strath-seed-fail",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-shared", Repo: repo, Task: "cut stone", Shared: true},
			{Name: "dreich-mason", Repo: repo, Task: "raise a very long stone wall", Star: true},
		},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "task exceeds todo title limit") {
		t.Fatalf("task preflight failure = %v", err)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.state.Sessions["braw-shared"] == nil {
		t.Fatal("shared member was removed during seed rollback")
	}

	for _, scenario := range sm.state.Scenarios {
		if scenario.Name == "strath-seed-fail" {
			t.Fatalf("failed scenario survived rollback: %+v", scenario)
		}
	}

	for _, session := range sm.state.Sessions {
		if session.Name == "dreich-mason" {
			t.Fatalf("created member survived rollback: %+v", session)
		}
	}

	items, listErr := sm.todos.ListAll(TodoFilter{})
	if listErr != nil {
		t.Fatal(listErr)
	}

	if len(items) != 0 {
		t.Fatalf("seed failure left todo rows: %+v", items)
	}
}

func TestAddToScenarioRejectsOversizedTaskBeforeCreatingMember(t *testing.T) {
	sm, orchID := newScenarioOrchestrator(t)
	sm.todos = newTestTodoStore(t)
	sm.todos.SetMaxTitle(12)

	repo := initScenarioGitRepo(t)

	sm.mu.Lock()
	sm.state.Scenarios["sc-add-fail"] = &ScenarioState{
		ID: "sc-add-fail", Name: "strath-add-fail", OrchestratorID: orchID,
	}
	sm.mu.Unlock()

	_, err := sm.AddToScenario("strath-add-fail", protocol.ScenarioSessionInput{
		Name: "dreich-mason", Repo: repo, Task: "raise a very long stone wall", Star: true,
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "task exceeds todo title limit") {
		t.Fatalf("task preflight failure = %v", err)
	}

	sm.mu.RLock()

	scenario := sm.state.Scenarios["sc-add-fail"]
	if len(scenario.SessionIDs) != 0 || len(scenario.Sessions) != 0 {
		t.Fatalf("scenario membership survived rollback: %+v", scenario)
	}

	for _, session := range sm.state.Sessions {
		if session.Name == "dreich-mason" {
			t.Fatalf("created member survived rollback: %+v", session)
		}
	}

	sm.mu.RUnlock()

	items, listErr := sm.todos.List("scenario:sc-add-fail", TodoFilter{})
	if listErr != nil {
		t.Fatal(listErr)
	}

	if len(items) != 0 {
		t.Fatalf("seed failure left todo rows: %+v", items)
	}
}

func TestAddToScenarioRejectsPolicyOptInWithoutLegacyContract(t *testing.T) {
	sm := startScenarioOrchestrator(t)
	sm.todos = newTestTodoStore(t)
	repo := initScenarioGitRepo(t)

	sm.mu.Lock()
	sm.state.Sessions["braw-old"] = &SessionState{
		ID: "braw-old", Name: "braw-old", Status: StatusStopped, RepoPath: repo,
	}
	sm.state.Scenarios["sc-croft-old"] = &ScenarioState{
		ID: "sc-croft-old", Name: "croft-old", OrchestratorID: "ben-orch",
		SessionIDs: []string{"braw-old"},
		Sessions:   []ScenarioSession{{Name: "braw-old", Task: "review old croft"}},
	}
	sm.mu.Unlock()

	_, err := sm.AddToScenario("croft-old", protocol.ScenarioSessionInput{
		Name: "canny-new", Repo: repo, Task: "review new croft",
		Policy: &protocol.ScenarioMemberPolicyInput{Required: boolPtr(true)},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "no durable seeded todo contract") {
		t.Fatalf("error = %v, want legacy contract verification failure", err)
	}

	if sm.state.Scenarios["sc-croft-old"].Policy != nil || sm.state.Sessions["canny-new"] != nil {
		t.Fatal("failed policy opt-in mutated legacy scenario")
	}
}

func TestAddToScenarioDependencyUsesOriginalSeedIdentity(t *testing.T) {
	sm, orchID := newScenarioOrchestrator(t)
	sm.todos = newTestTodoStore(t)
	repo := initScenarioGitRepo(t)
	scope := "scenario:sc-stable-seed"

	sm.mu.Lock()
	sm.state.Scenarios["sc-stable-seed"] = &ScenarioState{
		ID: "sc-stable-seed", Name: "strath-stable-seed", OrchestratorID: orchID,
		SessionIDs: []string{"braw-id"},
		Sessions:   []ScenarioSession{{Name: "braw", Task: "build", Repo: filepath.Base(repo)}},
	}
	sm.state.Sessions["braw-id"] = &SessionState{ID: "braw-id", Name: "braw", Status: StatusRunning}
	sm.mu.Unlock()

	upstream, err := sm.todos.Add(TodoAdd{
		Scope: scope, Title: "build", Assignee: "braw-id", CreatedBy: scope,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := sm.todos.Assign(upstream.ID, "canny-owner"); err != nil {
		t.Fatal(err)
	}

	created, err := sm.AddToScenario("strath-stable-seed", protocol.ScenarioSessionInput{
		Name: "canny", Repo: repo, Task: "inspect", DependsOn: []string{"braw"},
	}, 24, 80)
	if err != nil {
		t.Fatalf("add dependent after seed reassignment: %v", err)
	}

	seeds, err := sm.todos.ScenarioSeedItems(scope)
	if err != nil {
		t.Fatal(err)
	}

	dependent := seeds[created.ID]
	if len(dependent.DependsOn) != 1 || dependent.DependsOn[0] != upstream.ID {
		t.Fatalf("added dependent seed = %+v", dependent)
	}
}

func TestStartScenarioPolicyActivationSaveFailureRollsBackContracts(t *testing.T) {
	sm := startScenarioOrchestrator(t)
	sm.todos = newTestTodoStore(t)
	repo := initScenarioGitRepo(t)

	sm.mu.Lock()
	sm.state.Sessions["braw-live"] = &SessionState{
		ID: "braw-live", Name: "braw-live", Status: StatusRunning, RepoPath: repo,
	}
	sm.mu.Unlock()

	sm.saveStateFault = func() error {
		for _, scenario := range sm.state.Scenarios {
			if scenario.Name == "strath-rollback" && scenario.Policy != nil && scenario.Policy.Active {
				return errors.New("dreich disk")
			}
		}

		return nil
	}

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch",
		Name:            "strath-rollback",
		Policy:          &protocol.ScenarioPolicyInput{},
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-live", Repo: repo, Task: "review the croft", Shared: true},
		},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "persist scenario activation") {
		t.Fatalf("error = %v, want activation persistence failure", err)
	}

	for _, sc := range sm.state.Scenarios {
		if sc.Name == "strath-rollback" {
			t.Fatalf("scenario survived activation rollback: %+v", sc)
		}
	}

	items, listErr := sm.todos.ListAll(TodoFilter{})
	if listErr != nil {
		t.Fatal(listErr)
	}

	if len(items) != 0 {
		t.Fatalf("seeded result contracts survived rollback: %+v", items)
	}
}

func TestStartScenarioPolicyTodoWriteFailureRollsBack(t *testing.T) {
	sm := startScenarioOrchestrator(t)
	sm.todos = newTestTodoStore(t)
	repo := initScenarioGitRepo(t)

	sm.mu.Lock()
	sm.state.Sessions["canny-live"] = &SessionState{
		ID: "canny-live", Name: "canny-live", Status: StatusRunning, RepoPath: repo,
	}
	sm.mu.Unlock()

	if err := sm.todos.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "ben-orch",
		Name:            "strath-contract",
		Policy:          &protocol.ScenarioPolicyInput{},
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "canny-live", Repo: repo, Task: "inspect the bothy", Shared: true},
		},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "seed scenario todos") {
		t.Fatalf("error = %v, want result-contract write failure", err)
	}

	for _, sc := range sm.state.Scenarios {
		if sc.Name == "strath-contract" {
			t.Fatalf("scenario survived contract rollback: %+v", sc)
		}
	}
}

func TestAddToScenarioCommitSaveFailureRollsBackPolicyAndContract(t *testing.T) {
	sm, orchID := newScenarioOrchestrator(t)
	sm.todos = newTestTodoStore(t)
	repo := initScenarioGitRepo(t)

	sm.mu.Lock()
	sm.cfg.Agents["sleeper"] = config.Agent{Command: "sh", Args: []string{"-c", "sleep 60", "--"}}
	sm.state.Sessions["braw-old"] = &SessionState{ID: "braw-old", Name: "braw-old", Status: StatusStopped}
	sm.state.Scenarios["sc-add"] = &ScenarioState{
		ID: "sc-add", Name: "strath-add", OrchestratorID: orchID,
		SessionIDs: []string{"braw-old"},
		Sessions:   []ScenarioSession{{Name: "braw-old", Task: "review the old croft"}},
	}
	sm.mu.Unlock()

	if _, err := sm.todos.Add(TodoAdd{
		Scope: "scenario:sc-add", Title: "review the old croft", Assignee: "braw-old", CreatedBy: "scenario:sc-add",
	}); err != nil {
		t.Fatal(err)
	}

	sm.saveStateFault = func() error {
		if sc := sm.state.Scenarios["sc-add"]; sc != nil && len(sc.Sessions) == 2 {
			return errors.New("dreich disk")
		}

		return nil
	}

	_, err := sm.AddToScenario("strath-add", protocol.ScenarioSessionInput{
		Name: "canny-new", Repo: repo, Task: "review the new croft",
		Policy: &protocol.ScenarioMemberPolicyInput{Timeout: "1m"},
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "persist scenario member addition") {
		t.Fatalf("error = %v, want commit failure", err)
	}

	sc := sm.state.Scenarios["sc-add"]
	if sc.Policy != nil || len(sc.SessionIDs) != 1 || len(sc.Sessions) != 1 {
		t.Fatalf("scenario rollback = %+v", sc)
	}

	for _, sess := range sm.state.Sessions {
		if sess.Name == "canny-new" {
			t.Fatalf("added session survived rollback: %+v", sess)
		}
	}

	items, listErr := sm.todos.ListAll(TodoFilter{})
	if listErr != nil {
		t.Fatal(listErr)
	}

	if len(items) != 1 || items[0].Assignee != "braw-old" || items[0].Title != "review the old croft" {
		t.Fatalf("todo rollback removed the legacy contract or retained the added contract: %+v", items)
	}
}

func TestScenarioRetryPolicyForcesPTYUnderHeadlessDefault(t *testing.T) {
	sm, orchID := newScenarioOrchestrator(t)
	sm.todos = newTestTodoStore(t)
	repo := initScenarioGitRepo(t)
	capable := true

	sm.mu.Lock()
	sm.cfg.Headless.Experimental = true
	sm.cfg.Headless.Default = true
	sm.cfg.Agents["sleeper"] = config.Agent{
		Command: "sh", Args: []string{"-c", "sleep 60", "--"}, HeadlessCapable: &capable,
	}
	sm.mu.Unlock()

	sc, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: orchID,
		Name:            "strath-pty",
		Policy:          &protocol.ScenarioPolicyInput{},
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "braw-pty", Repo: repo, Task: "review the croft", Policy: &protocol.ScenarioMemberPolicyInput{Timeout: "1m", Retries: 1}},
		},
	}, 24, 80)
	if err != nil {
		t.Fatal(err)
	}

	if got := sm.state.Sessions[sc.SessionIDs[0]].DriverKind; got != DriverPTY {
		t.Fatalf("retryable scenario driver = %q, want %q", got, DriverPTY)
	}
}

// TestScenarioCompleteDerivedFromTodos verifies the scenario reports "complete"
// once every member with tracked work has finished its assigned items — the
// derived replacement for the removed task-done completion.
func TestScenarioCompleteDerivedFromTodos(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.todos = newTestTodoStore(t)

	sm.mu.Lock()
	sm.state.Scenarios["sc-braw"] = &ScenarioState{
		ID: "sc-braw", Name: "strath-kirk",
		SessionIDs: []string{"braw-s1"},
		Sessions:   []ScenarioSession{{Name: "braw-forge", Task: "forge"}},
	}
	sm.state.Sessions["braw-s1"] = &SessionState{ID: "braw-s1", Status: StatusRunning, ScenarioID: "sc-braw"}
	sm.mu.Unlock()

	item, err := sm.todos.Add(TodoAdd{Scope: "scenario:sc-braw", Title: "forge", Assignee: "braw-s1", CreatedBy: "orch"})
	if err != nil {
		t.Fatal(err)
	}

	sm.mu.RLock()
	rec := sm.buildScenarioRecord(sm.state.Scenarios["sc-braw"])
	sm.mu.RUnlock()

	if rec.Status == "complete" {
		t.Fatal("scenario should not be complete before the item is done")
	}

	if _, _, err := sm.todos.Claim(item.ID, "braw-s1", false); err != nil {
		t.Fatal(err)
	}

	if _, err := sm.todos.Transition(item.ID, TodoStatusDone, "braw-s1", false); err != nil {
		t.Fatal(err)
	}

	sm.mu.RLock()
	rec = sm.buildScenarioRecord(sm.state.Scenarios["sc-braw"])
	sm.mu.RUnlock()

	if rec.Status != "complete" {
		t.Errorf("scenario status: got %q, want complete", rec.Status)
	}
}

func TestSeedScenarioTodosResolvesMemberDependencies(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.todos = newTestTodoStore(t)

	inputs := []protocol.ScenarioSessionInput{
		{Name: "braw", Task: "build the brig"},
		{Name: "canny", Task: "inspect the brig", DependsOn: []string{"braw"}},
		{Name: "dreich"}, // compatibility: no task means no seeded item
	}
	if _, err := sm.seedScenarioTodos("sc-strath", []string{"braw-id", "canny-id", "dreich-id"}, inputs); err != nil {
		t.Fatalf("seedScenarioTodos: %v", err)
	}

	items, err := sm.todos.List("scenario:sc-strath", TodoFilter{})
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 2 {
		t.Fatalf("seeded items = %+v", items)
	}

	byTitle := make(map[string]TodoItem, len(items))
	for _, item := range items {
		byTitle[item.Title] = item
	}

	upstream := byTitle["build the brig"]

	downstream := byTitle["inspect the brig"]
	if upstream.Assignee != "braw-id" || downstream.Assignee != "canny-id" {
		t.Fatalf("assignees: upstream=%q downstream=%q", upstream.Assignee, downstream.Assignee)
	}

	if downstream.Status != TodoStatusBlocked || len(downstream.DependsOn) != 1 || downstream.DependsOn[0] != upstream.ID {
		t.Fatalf("resolved downstream = %+v", downstream)
	}

	if _, err := sm.todos.Assign(upstream.ID, "canny-id"); err != nil {
		t.Fatalf("reassign upstream seed: %v", err)
	}

	sm.mu.Lock()

	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID: "sc-strath", Name: "strath", SessionIDs: []string{"braw-id", "canny-id", "dreich-id"},
		Sessions: []ScenarioSession{{Name: "braw", Task: "build the brig"}, {Name: "canny", Task: "inspect the brig"}, {Name: "dreich"}},
	}
	for _, id := range []string{"braw-id", "canny-id", "dreich-id"} {
		sm.state.Sessions[id] = &SessionState{ID: id, Status: StatusRunning}
	}

	record := sm.buildScenarioRecord(sm.state.Scenarios["sc-strath"])
	sm.mu.Unlock()

	if len(record.Sessions[1].BlockedBy) != 1 || record.Sessions[1].BlockedBy[0] != "braw" {
		t.Fatalf("scenario status waiting reason = %v", record.Sessions[1].BlockedBy)
	}
}

func TestSeedScenarioTodosCycleIsAtomic(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.todos = newTestTodoStore(t)

	_, err := sm.seedScenarioTodos("sc-thrawn", []string{"braw-id", "canny-id"}, []protocol.ScenarioSessionInput{
		{Name: "braw", Task: "first", DependsOn: []string{"canny"}},
		{Name: "canny", Task: "second", DependsOn: []string{"braw"}},
	})
	if err == nil {
		t.Fatal("expected cyclic seed failure")
	}

	items, listErr := sm.todos.List("scenario:sc-thrawn", TodoFilter{})
	if listErr != nil {
		t.Fatal(listErr)
	}

	if len(items) != 0 {
		t.Fatalf("cyclic seed left partial items: %+v", items)
	}
}
