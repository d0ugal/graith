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
		{"valid simple", "my-scenario", false},
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
	sm.state.Scenarios["sc-test"] = &ScenarioState{
		ID:         "sc-test",
		Name:       "my-scenario",
		Goal:       "test goal",
		SessionIDs: []string{"sess1", "sess2"},
		Sessions: []ScenarioSession{
			{Name: "backend", Role: "backend dev", Repo: "my-backend"},
			{Name: "frontend", Role: "frontend dev", Repo: "my-frontend"},
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
	sc := state.Scenarios["sc-test"]
	if sc.Name != "my-scenario" {
		t.Errorf("scenario name = %q, want %q", sc.Name, "my-scenario")
	}
	if sc.Goal != "test goal" {
		t.Errorf("scenario goal = %q, want %q", sc.Goal, "test goal")
	}
	if len(sc.Sessions) != 2 {
		t.Errorf("scenario sessions = %d, want 2", len(sc.Sessions))
	}
}

func TestScenarioFieldsOnSessionState(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["sess1"] = &SessionState{
		ID:           "sess1",
		Name:         "backend",
		Status:       StatusRunning,
		ScenarioID:   "sc-123",
		ScenarioRole: "Backend engineer",
		ScenarioGoal: "Build the thing",
	}
	_ = sm.saveState()
	sm.mu.Unlock()

	state, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	sess := state.Sessions["sess1"]
	if sess.ScenarioID != "sc-123" {
		t.Errorf("ScenarioID = %q, want %q", sess.ScenarioID, "sc-123")
	}
	if sess.ScenarioRole != "Backend engineer" {
		t.Errorf("ScenarioRole = %q, want %q", sess.ScenarioRole, "Backend engineer")
	}
	if sess.ScenarioGoal != "Build the thing" {
		t.Errorf("ScenarioGoal = %q, want %q", sess.ScenarioGoal, "Build the thing")
	}
}

func TestBuildScenarioRecord(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["s1"] = &SessionState{ID: "s1", Name: "backend", Status: StatusRunning}
	sm.state.Sessions["s2"] = &SessionState{ID: "s2", Name: "frontend", Status: StatusStopped}
	sm.state.Scenarios["sc-1"] = &ScenarioState{
		ID:         "sc-1",
		Name:       "test-scenario",
		Goal:       "test",
		SessionIDs: []string{"s1", "s2"},
		Sessions: []ScenarioSession{
			{Name: "backend", Role: "backend dev", Repo: "my-backend", Agent: "claude"},
			{Name: "frontend", Role: "frontend dev", Repo: "my-frontend", Agent: "claude"},
		},
	}

	record := sm.buildScenarioRecord(sm.state.Scenarios["sc-1"])
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
	sm.state.Sessions["s1"] = &SessionState{ID: "s1", Name: "a", Status: StatusRunning}
	sm.state.Sessions["s2"] = &SessionState{ID: "s2", Name: "b", Status: StatusRunning}
	sm.state.Scenarios["sc-1"] = &ScenarioState{
		ID:         "sc-1",
		Name:       "test",
		SessionIDs: []string{"s1", "s2"},
		Sessions: []ScenarioSession{
			{Name: "a"},
			{Name: "b"},
		},
	}
	record := sm.buildScenarioRecord(sm.state.Scenarios["sc-1"])
	sm.mu.Unlock()

	if record.Status != "running" {
		t.Errorf("status = %q, want %q", record.Status, "running")
	}
}

func TestBuildScenarioRecordAllStopped(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["s1"] = &SessionState{ID: "s1", Name: "a", Status: StatusStopped}
	sm.state.Sessions["s2"] = &SessionState{ID: "s2", Name: "b", Status: StatusStopped}
	sm.state.Scenarios["sc-1"] = &ScenarioState{
		ID:         "sc-1",
		Name:       "test",
		SessionIDs: []string{"s1", "s2"},
		Sessions: []ScenarioSession{
			{Name: "a"},
			{Name: "b"},
		},
	}
	record := sm.buildScenarioRecord(sm.state.Scenarios["sc-1"])
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
	sm.state.Scenarios["sc-1"] = &ScenarioState{
		ID:   "sc-1",
		Name: "test",
	}
	sm.mu.Unlock()

	records = sm.ListScenarios()
	if len(records) != 1 {
		t.Fatalf("expected 1 scenario, got %d", len(records))
	}
	if records[0].Name != "test" {
		t.Errorf("name = %q, want %q", records[0].Name, "test")
	}
}

func TestStartScenarioRequiresOrchestrator(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["regular"] = &SessionState{
		ID:     "regular",
		Name:   "regular-session",
		Status: StatusRunning,
	}
	sm.mu.Unlock()

	_, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: "regular",
		Name:            "test-scenario",
		Goal:            "test",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "backend", Repo: "/tmp/repo"},
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
		CallerSessionID: "nonexistent",
		Name:            "test",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "a", Repo: "/tmp/repo"},
		},
	}, 24, 80)
	if err == nil {
		t.Fatal("expected error for nonexistent caller")
	}
}

func TestStartScenarioValidation(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["orch"] = &SessionState{
		ID:         "orch",
		Name:       "orchestrator",
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
				CallerSessionID: "orch",
				Name:            "",
				Sessions:        []protocol.ScenarioSessionInput{{Name: "a", Repo: "/tmp"}},
			},
			wantErr: "scenario name must not be empty",
		},
		{
			name: "invalid scenario name",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "orch",
				Name:            "Bad Name",
				Sessions:        []protocol.ScenarioSessionInput{{Name: "a", Repo: "/tmp"}},
			},
			wantErr: "invalid",
		},
		{
			name: "no sessions",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "orch",
				Name:            "test",
				Sessions:        nil,
			},
			wantErr: "at least one session",
		},
		{
			name: "session missing name",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "orch",
				Name:            "test",
				Sessions:        []protocol.ScenarioSessionInput{{Repo: "/tmp"}},
			},
			wantErr: "name is required",
		},
		{
			name: "session missing repo",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "orch",
				Name:            "test",
				Sessions:        []protocol.ScenarioSessionInput{{Name: "a"}},
			},
			wantErr: "repo is required",
		},
		{
			name: "duplicate session name",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "orch",
				Name:            "test",
				Sessions: []protocol.ScenarioSessionInput{
					{Name: "a", Repo: "/tmp"},
					{Name: "a", Repo: "/tmp"},
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
		CallerSessionID: "orch-1",
		Name:            "test-scenario",
		Goal:            "Build things",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "backend", Repo: "/home/user/Code/my-backend", Role: "Backend dev", Task: "Build API"},
			{Name: "frontend", Repo: "/home/user/Code/my-frontend", Role: "Frontend dev", Task: "Build UI"},
		},
	}
	sessionIDs := []string{"s1", "s2"}
	scenario := &ScenarioState{
		ID:   "sc-123",
		Name: "test-scenario",
		Sessions: []ScenarioSession{
			{Name: "backend", Repo: "my-backend"},
			{Name: "frontend", Repo: "my-frontend"},
		},
	}

	m := sm.buildManifest("sc-123", msg, scenario, sessionIDs, 0)

	if m.Version != 1 {
		t.Errorf("version = %d, want 1", m.Version)
	}
	if m.ScenarioID != "sc-123" {
		t.Errorf("scenario_id = %q", m.ScenarioID)
	}
	if m.You.Name != "backend" {
		t.Errorf("you.name = %q", m.You.Name)
	}
	if m.You.SessionID != "s1" {
		t.Errorf("you.session_id = %q", m.You.SessionID)
	}
	if len(m.Siblings) != 1 {
		t.Fatalf("siblings = %d, want 1", len(m.Siblings))
	}
	if m.Siblings[0].Name != "frontend" {
		t.Errorf("sibling.name = %q", m.Siblings[0].Name)
	}
	if m.Siblings[0].Repo != "my-frontend" {
		t.Errorf("sibling.repo = %q, want %q", m.Siblings[0].Repo, "my-frontend")
	}
	if m.Orchestrator.SessionID != "orch-1" {
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
	if parsed.ScenarioName != "test-scenario" {
		t.Errorf("after roundtrip: scenario_name = %q", parsed.ScenarioName)
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
