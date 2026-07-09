package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/store"
)

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

	// braw-del, canny-del, and the ghost (absent) are all reported deleted.
	if len(deleted) != 3 {
		t.Fatalf("deleted = %v, want 3 entries", deleted)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if _, ok := sm.state.Scenarios["sc-strath"]; ok {
		t.Error("scenario record should be removed when all sessions cleaned up")
	}

	if _, ok := sm.state.Sessions["braw-del"]; ok {
		t.Error("braw-del should be deleted")
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
	if err == nil || !strings.Contains(err.Error(), "already exists") {
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

	// Should not panic or write anything for an unknown scenario id.
	sm.republishManifests("sc-nope")
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

	// total == 0: running == total, so status is "running".
	if rec.Status != "running" {
		t.Errorf("status = %q, want running for empty scenario", rec.Status)
	}
}
