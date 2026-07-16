package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
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
		Sessions:   []ScenarioSession{{Name: "thrawn-del"}},
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

	return dir
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
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"60"}}

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
