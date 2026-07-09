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
