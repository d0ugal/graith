package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
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
			{Name: "braw-forge", Role: "braw-forge dev", Repo: "croft-forge", Agent: "claude"},
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
	if record.Sessions[1].Status != "stopped" {
		t.Errorf("session[1].Status = %q, want %q", record.Sessions[1].Status, "stopped")
	}
}

func TestBuildScenarioRecordAllRunning(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["braw-s1"] = &SessionState{ID: "braw-s1", Name: "braw-a", Status: StatusRunning}
	sm.state.Sessions["bonnie-s2"] = &SessionState{ID: "bonnie-s2", Name: "bonnie-b", Status: StatusRunning}
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

	if record.Status != "running" {
		t.Errorf("status = %q, want %q", record.Status, "running")
	}
}

func TestBuildScenarioRecordAllStopped(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["braw-s1"] = &SessionState{ID: "braw-s1", Name: "braw-a", Status: StatusStopped}
	sm.state.Sessions["bonnie-s2"] = &SessionState{ID: "bonnie-s2", Name: "bonnie-b", Status: StatusStopped}
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

	if record.Status != "stopped" {
		t.Errorf("status = %q, want %q", record.Status, "stopped")
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
			{Name: "braw-forge", Repo: "/hame/glen/croft-forge", Role: "Braw forge dev", Task: "Build braw API"},
			{Name: "bonnie-loom", Repo: "/hame/glen/croft-loom", Role: "Bonnie loom dev", Task: "Build bonnie UI"},
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
	if len(m.Siblings) != 1 {
		t.Fatalf("siblings = %d, want 1", len(m.Siblings))
	}
	if m.Siblings[0].Name != "bonnie-loom" {
		t.Errorf("sibling.name = %q", m.Siblings[0].Name)
	}
	if m.Siblings[0].Repo != "croft-loom" {
		t.Errorf("sibling.repo = %q, want %q", m.Siblings[0].Repo, "croft-loom")
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

func TestScenarioTaskDone(t *testing.T) {
	sm := newTestSessionManager(t)

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

	if err := sm.ScenarioTaskDone("strath-kirk", "braw-s1"); err != nil {
		t.Fatal(err)
	}

	sm.mu.RLock()
	sc := sm.state.Scenarios["sc-braw"]
	if !sc.Sessions[0].TaskDone {
		t.Error("session 0 should be marked as task done")
	}
	if sc.Sessions[1].TaskDone {
		t.Error("session 1 should not be marked as task done")
	}
	sm.mu.RUnlock()

	if err := sm.ScenarioTaskDone("strath-kirk", "haar-glen"); err == nil {
		t.Error("expected error for nonexistent session")
	}

	if err := sm.ScenarioTaskDone("haar-strath", "braw-s1"); err == nil {
		t.Error("expected error for nonexistent scenario")
	}
}

func TestScenarioTaskDoneInRecord(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Scenarios["sc-braw"] = &ScenarioState{
		ID:         "sc-braw",
		Name:       "strath-kirk",
		SessionIDs: []string{"braw-s1"},
		Sessions: []ScenarioSession{
			{Name: "braw-forge", Task: "forge braw API", TaskDone: true},
		},
	}
	sm.state.Sessions["braw-s1"] = &SessionState{ID: "braw-s1", Name: "braw-forge", Status: StatusRunning}
	record := sm.buildScenarioRecord(sm.state.Scenarios["sc-braw"])
	sm.mu.Unlock()

	if !record.Sessions[0].TaskDone {
		t.Error("record should reflect task_done")
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
