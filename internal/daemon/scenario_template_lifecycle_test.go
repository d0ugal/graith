package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/store"
)

func TestStartScenarioTemplatesRenderSharedInitiatorReferencesResultsAndRestart(t *testing.T) {
	sm, orchestratorID := newMirroredScenarioOrchestrator(t)
	repo := initScenarioGitRepo(t)
	fixedNow := time.Date(2026, 7, 18, 9, 10, 11, 123, time.UTC)
	sm.scenarioPolicyNow = func() time.Time { return fixedNow }

	const initiatorID = "braw-source"

	sm.mu.Lock()
	sm.state.Sessions[initiatorID] = &SessionState{
		ID: initiatorID, Name: "braw-source", Agent: "sleeper",
		RepoPath: repo, RepoName: filepath.Base(repo), WorktreePath: repo,
		Status: StatusRunning,
	}
	sm.mu.Unlock()

	msg := protocol.ScenarioStartMsg{
		CallerSessionID:    orchestratorID,
		ParentSessionID:    orchestratorID,
		InitiatorSessionID: initiatorID,
		Name:               "parallel-{initiator}-{date}-{short_id}",
		Goal:               "review the croft",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "{initiator}", Shared: true},
			{
				Name: "{scenario}-reviewer", Mirror: "{initiator}", Role: "reviewer",
				Results: []protocol.ScenarioResultSpec{{
					Name: "review", Format: "markdown", Store: "{session_name}/review.md", Required: true,
				}},
			},
		},
		Triggers: []config.TriggerConfig{{
			Name:       "publish-review",
			Completion: &config.CompletionConfig{Event: "complete", Session: "{scenario}-reviewer"},
			Action: config.ActionConfig{
				Type: "message", Body: "review complete",
				Deliver: config.DeliverConfig{Inbox: "{scenario}-reviewer"},
			},
		}},
	}

	scenario, err := sm.StartScenario(msg, 24, 80)
	if err != nil {
		t.Fatalf("StartScenario: %v", err)
	}

	wantScenario := "parallel-braw-source-20260718-" + strings.TrimPrefix(scenario.ID, "sc-")

	wantReviewer := wantScenario + "-reviewer"
	if scenario.Name != wantScenario {
		t.Fatalf("scenario name = %q, want %q", scenario.Name, wantScenario)
	}

	if len(scenario.Sessions) != 2 || scenario.Sessions[0].Name != "braw-source" || scenario.Sessions[1].Name != wantReviewer || scenario.Sessions[1].Mirror != "braw-source" {
		t.Fatalf("rendered sessions = %+v", scenario.Sessions)
	}

	if len(scenario.Triggers) != 1 || scenario.Triggers[0].Completion.Session != wantReviewer || scenario.Triggers[0].Action.Deliver.Inbox != wantReviewer {
		t.Fatalf("rendered trigger = %+v", scenario.Triggers)
	}

	if scenario.Render == nil || scenario.Render.AuthoredName != msg.Name || scenario.Render.Initiator.SessionID != initiatorID || scenario.Render.Parent.SessionID != orchestratorID {
		t.Fatalf("render state = %+v", scenario.Render)
	}

	if got := scenario.Sessions[1].Results[0].Destination; !strings.Contains(got, "/"+wantReviewer+"/review.md") {
		t.Fatalf("result destination = %q, want rendered session name", got)
	}

	record, err := sm.ScenarioStatus(wantScenario)
	if err != nil {
		t.Fatal(err)
	}

	if record.Render == nil || record.Render.Members[1].RenderedName != wantReviewer || record.Name != wantScenario {
		t.Fatalf("status record = %+v", record)
	}

	manifestKey := "scenarios/" + scenario.ID + "/manifest-" + wantReviewer + ".json"

	manifestBody, err := store.Get(store.SharedStorePath(sm.paths.DataDir), manifestKey)
	if err != nil {
		t.Fatalf("read rendered manifest: %v", err)
	}

	var manifest scenarioManifest
	if err := json.Unmarshal([]byte(manifestBody), &manifest); err != nil {
		t.Fatal(err)
	}

	if manifest.Version != 2 || manifest.ScenarioName != wantScenario || manifest.You.Name != wantReviewer || manifest.Render == nil || manifest.Render.AuthoredName != msg.Name {
		t.Fatalf("manifest = %+v", manifest)
	}

	stopped, err := sm.StopScenario(wantScenario)
	if err != nil || len(stopped) != 1 {
		t.Fatalf("StopScenario = %v, %v", stopped, err)
	}

	if source, ok := sm.Get(initiatorID); !ok || source.Status != StatusRunning {
		t.Fatalf("shared initiator changed by stop: %+v, ok=%t", source, ok)
	}

	reviewerID := scenario.SessionIDs[1]
	waitForScenarioSessionStopped(t, sm, reviewerID)

	restarted := NewSessionManager(sm.Config(), sm.paths, sm.log)
	if err := restarted.LoadState(); err != nil {
		t.Fatalf("restart load: %v", err)
	}

	restartedRecord, err := restarted.ScenarioStatus(wantScenario)
	if err != nil {
		t.Fatalf("restart status: %v", err)
	}

	if restartedRecord.Render == nil || restartedRecord.Render.RenderedAt != "2026-07-18T09:10:11.000000123Z" || restartedRecord.Sessions[1].Mirror != "braw-source" {
		t.Fatalf("restart record = %+v", restartedRecord)
	}

	if source, ok := restarted.Get(initiatorID); !ok || source.Status != StatusRunning {
		t.Fatalf("shared initiator changed across restart: %+v, ok=%t", source, ok)
	}

	resumed, err := restarted.ResumeScenario(wantScenario, 24, 80)
	if err != nil || len(resumed) != 1 || resumed[0] != reviewerID {
		t.Fatalf("restart ResumeScenario = %v, %v", resumed, err)
	}

	afterResume, err := restarted.ScenarioStatus(wantScenario)
	if err != nil || !reflect.DeepEqual(afterResume.Render, restartedRecord.Render) {
		t.Fatalf("render metadata changed on restart resume: before=%+v after=%+v err=%v", restartedRecord.Render, afterResume.Render, err)
	}

	stopped, err = restarted.StopScenario(wantScenario)
	if err != nil || len(stopped) != 1 || stopped[0] != reviewerID {
		t.Fatalf("restart StopScenario = %v, %v", stopped, err)
	}

	waitForScenarioSessionStopped(t, restarted, reviewerID)

	if source, ok := restarted.Get(initiatorID); !ok || source.Status != StatusRunning {
		t.Fatalf("shared initiator changed by restarted lifecycle: %+v, ok=%t", source, ok)
	}

	deleted, err := restarted.DeleteScenario(wantScenario)
	if err != nil || len(deleted) != 1 {
		t.Fatalf("restart DeleteScenario = %v, %v", deleted, err)
	}

	if _, err := restarted.ScenarioStatus(wantScenario); err == nil {
		t.Fatal("rendered scenario remained after delete")
	}
}

func TestStartScenarioTemplateConcurrentSameSecondStartsCreateDistinctOwnedMembers(t *testing.T) {
	sm, orchestratorID := newScenarioOrchestrator(t)
	repo := initScenarioGitRepo(t)
	fixedNow := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	sm.scenarioPolicyNow = func() time.Time { return fixedNow }

	msg := protocol.ScenarioStartMsg{
		CallerSessionID: orchestratorID,
		Name:            "parallel-{datetime}-{short_id}",
		Sessions: []protocol.ScenarioSessionInput{{
			Name: "{scenario}-worker", Repo: repo, Agent: "sleeper",
		}},
	}

	var (
		wg        sync.WaitGroup
		scenarios [2]*ScenarioState
		errs      [2]error
	)

	for i := range scenarios {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			scenarios[index], errs[index] = sm.StartScenario(msg, 24, 80)
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}

	if scenarios[0].ID == scenarios[1].ID || scenarios[0].Name == scenarios[1].Name {
		t.Fatalf("concurrent instances collided: %+v %+v", scenarios[0], scenarios[1])
	}

	for _, scenario := range scenarios {
		if scenario.CreatedAt != fixedNow || len(scenario.Sessions) != 1 || scenario.Sessions[0].Name != scenario.Name+"-worker" {
			t.Fatalf("rendered concurrent scenario = %+v", scenario)
		}
	}

	if scenarios[0].Sessions[0].Name == scenarios[1].Sessions[0].Name {
		t.Fatalf("owned member names collided: %q", scenarios[0].Sessions[0].Name)
	}

	for _, scenario := range scenarios {
		if _, err := sm.DeleteScenario(scenario.Name); err != nil {
			t.Fatalf("delete %q: %v", scenario.Name, err)
		}
	}
}

func TestStartScenarioTemplateRenderedCollisionRemainsPreflightError(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.scenarioPolicyNow = func() time.Time {
		return time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	}

	sm.mu.Lock()
	sm.state.Sessions["ben-orch"] = &SessionState{
		ID: "ben-orch", Name: OrchestratorSessionName, Status: StatusRunning, SystemKind: SystemKindOrchestrator,
	}
	sm.state.Sessions["braw-live"] = &SessionState{ID: "braw-live", Name: "braw", Status: StatusRunning}
	sm.mu.Unlock()

	msg := protocol.ScenarioStartMsg{
		CallerSessionID:    "ben-orch",
		InitiatorSessionID: "braw-live",
		Name:               "parallel-{date}",
		Sessions:           []protocol.ScenarioSessionInput{{Name: "{initiator}", Shared: true}},
	}

	first, err := sm.StartScenario(msg, 24, 80)
	if err != nil {
		t.Fatal(err)
	}

	_, err = sm.StartScenario(msg, 24, 80)
	if err == nil || !strings.Contains(err.Error(), `scenario "parallel-20260718" already exists`) {
		t.Fatalf("collision error = %v", err)
	}

	if len(sm.ListScenarios()) != 1 || first.Name != "parallel-20260718" {
		t.Fatalf("collision mutated state: %+v", sm.ListScenarios())
	}
}

func TestStartScenarioTemplateRejectsInvalidGraphAtomically(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.scenarioPolicyNow = func() time.Time {
		return time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	}

	sm.mu.Lock()
	sm.state.Sessions["ben-orch"] = &SessionState{
		ID: "ben-orch", Name: OrchestratorSessionName, Status: StatusRunning, SystemKind: SystemKindOrchestrator,
	}
	sm.state.Sessions["braw-live"] = &SessionState{ID: "braw-live", Name: "Braw_Source", Status: StatusRunning}
	sm.mu.Unlock()

	tests := []struct {
		name string
		msg  protocol.ScenarioStartMsg
		want string
	}{
		{
			name: "unknown token",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch", Name: "parallel-{dreich}",
				Sessions: []protocol.ScenarioSessionInput{{Name: "orchestrator", Shared: true}},
			},
			want: "unknown scenario name template variable",
		},
		{
			name: "invalid rendered scenario identity",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch", InitiatorSessionID: "braw-live", Name: "parallel-{initiator}",
				Sessions: []protocol.ScenarioSessionInput{{Name: "orchestrator", Shared: true}},
			},
			want: "scenario name",
		},
		{
			name: "scenario overflow",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch", Name: strings.Repeat("a", 121) + "-{short_id}",
				Sessions: []protocol.ScenarioSessionInput{{Name: "orchestrator", Shared: true}},
			},
			want: "128 characters or fewer",
		},
		{
			name: "member overflow",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch", Name: "parallel-{short_id}",
				Sessions: []protocol.ScenarioSessionInput{{Name: "{scenario}-" + strings.Repeat("a", 120), Repo: "/croft"}},
			},
			want: "session name must be 128 characters or fewer",
		},
		{
			name: "unknown reference token",
			msg: protocol.ScenarioStartMsg{
				CallerSessionID: "ben-orch", Name: "parallel-{short_id}",
				Sessions: []protocol.ScenarioSessionInput{{Name: "reader", Mirror: "{dreich}"}},
			},
			want: "unknown scenario name template variable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeSessions := len(sm.state.Sessions)
			_, statErr := os.Stat(sm.paths.StateFile)

			_, err := sm.StartScenario(test.msg, 24, 80)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}

			sm.mu.RLock()

			if len(sm.state.Scenarios) != 0 || len(sm.state.Sessions) != beforeSessions || len(sm.scenarioStartIDs) != 0 {
				t.Fatalf("failed preflight mutated state: scenarios=%d sessions=%d reservations=%d",
					len(sm.state.Scenarios), len(sm.state.Sessions), len(sm.scenarioStartIDs))
			}

			sm.mu.RUnlock()

			_, afterStatErr := os.Stat(sm.paths.StateFile)
			if os.IsNotExist(statErr) != os.IsNotExist(afterStatErr) {
				t.Fatalf("failed preflight changed state-file existence: before=%v after=%v", statErr, afterStatErr)
			}
		})
	}
}

func TestScenarioAddRejectsNameTemplatesAfterInstantiation(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.AddToScenario("parallel-braw", protocol.ScenarioSessionInput{
		Name: "{scenario}-late", Repo: "/croft",
	}, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "session name") {
		t.Fatalf("scenario add template error = %v", err)
	}
}

func TestScenarioAddPersistsRenderedMetadataAndRollsBackOnSaveFailure(t *testing.T) {
	t.Run("success survives manifests and restart", func(t *testing.T) {
		sm, scenario, repo, firstName := startTemplatedScenarioForAdd(t)

		const addedName = "canny-added"

		added, err := sm.AddToScenario(scenario.Name, protocol.ScenarioSessionInput{
			Name: addedName, Repo: repo, Agent: "sleeper", Task: "inspect the new bothy",
			DependsOn: []string{firstName},
		}, 24, 80)
		if err != nil {
			t.Fatalf("AddToScenario: %v", err)
		}

		record, err := sm.ScenarioStatus(scenario.Name)
		if err != nil {
			t.Fatal(err)
		}

		assertAddedScenarioRenderMetadata(t, record.Render, addedName, firstName)

		for _, member := range record.Sessions {
			key := "scenarios/" + scenario.ID + "/manifest-" + member.Name + ".json"

			body, getErr := store.Get(store.SharedStorePath(sm.paths.DataDir), key)
			if getErr != nil {
				t.Fatalf("read republished manifest %q: %v", key, getErr)
			}

			var manifest scenarioManifest
			if unmarshalErr := json.Unmarshal([]byte(body), &manifest); unmarshalErr != nil {
				t.Fatalf("decode republished manifest %q: %v", key, unmarshalErr)
			}

			if manifest.You.SessionID == added.ID && manifest.You.Name != addedName {
				t.Fatalf("added member manifest identity = %+v", manifest.You)
			}

			if !reflect.DeepEqual(manifest.Render, record.Render) {
				t.Fatalf("manifest %q render metadata = %+v, want %+v", key, manifest.Render, record.Render)
			}
		}

		restarted := NewSessionManager(sm.Config(), sm.paths, sm.log)
		if err := restarted.LoadState(); err != nil {
			t.Fatalf("restart load: %v", err)
		}

		restartedRecord, err := restarted.ScenarioStatus(scenario.Name)
		if err != nil {
			t.Fatalf("restart status: %v", err)
		}

		if !reflect.DeepEqual(restartedRecord.Render, record.Render) {
			t.Fatalf("restart render metadata = %+v, want %+v", restartedRecord.Render, record.Render)
		}
	})

	t.Run("failed save restores exact metadata", func(t *testing.T) {
		sm, scenario, repo, firstName := startTemplatedScenarioForAdd(t)

		before, err := sm.ScenarioStatus(scenario.Name)
		if err != nil {
			t.Fatal(err)
		}

		manifestKey := "scenarios/" + scenario.ID + "/manifest-" + firstName + ".json"

		beforeManifest, err := store.Get(store.SharedStorePath(sm.paths.DataDir), manifestKey)
		if err != nil {
			t.Fatalf("read pre-add manifest: %v", err)
		}

		sm.saveStateFault = func() error {
			if current := sm.state.Scenarios[scenario.ID]; current != nil && len(current.Sessions) == 2 {
				return errors.New("dreich disk")
			}

			return nil
		}

		_, err = sm.AddToScenario(scenario.Name, protocol.ScenarioSessionInput{
			Name: "dreich-failed", Repo: repo, Agent: "sleeper", Task: "inspect the failed bothy",
			DependsOn: []string{firstName},
		}, 24, 80)
		if err == nil || !strings.Contains(err.Error(), "persist scenario member addition") {
			t.Fatalf("AddToScenario error = %v, want save failure", err)
		}

		after, statusErr := sm.ScenarioStatus(scenario.Name)
		if statusErr != nil {
			t.Fatal(statusErr)
		}

		if !reflect.DeepEqual(after.Render, before.Render) || !reflect.DeepEqual(after.Sessions, before.Sessions) || !reflect.DeepEqual(after.SessionIDs, before.SessionIDs) {
			t.Fatalf("failed add changed scenario: before=%+v after=%+v", before, after)
		}

		sm.mu.RLock()

		for _, session := range sm.state.Sessions {
			if session.Name == "dreich-failed" {
				sm.mu.RUnlock()
				t.Fatalf("failed added session survived rollback: %+v", session)
			}
		}

		sm.mu.RUnlock()

		afterManifest, getErr := store.Get(store.SharedStorePath(sm.paths.DataDir), manifestKey)
		if getErr != nil {
			t.Fatalf("read post-failure manifest: %v", getErr)
		}

		if afterManifest != beforeManifest || strings.Contains(afterManifest, "dreich-failed") {
			t.Fatal("failed add changed persisted manifest metadata")
		}

		items, listErr := sm.todos.ListAll(TodoFilter{})
		if listErr != nil {
			t.Fatal(listErr)
		}

		if len(items) != 1 || items[0].Assignee != scenario.SessionIDs[0] {
			t.Fatalf("failed add changed scenario todos: %+v", items)
		}
	})
}

func startTemplatedScenarioForAdd(t *testing.T) (*SessionManager, *ScenarioState, string, string) {
	t.Helper()

	sm, orchestratorID := newScenarioOrchestrator(t)
	sm.todos = newTestTodoStore(t)
	repo := initScenarioGitRepo(t)

	scenario, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: orchestratorID,
		Name:            "strath-add-{short_id}",
		Policy:          &protocol.ScenarioPolicyInput{},
		Sessions: []protocol.ScenarioSessionInput{{
			Name: "{scenario}-first", Repo: repo, Agent: "sleeper", Task: "inspect the old croft",
		}},
	}, 24, 80)
	if err != nil {
		t.Fatalf("StartScenario: %v", err)
	}

	return sm, scenario, repo, scenario.Sessions[0].Name
}

func assertAddedScenarioRenderMetadata(t *testing.T, render *protocol.ScenarioRenderInfo, addedName, dependencyName string) {
	t.Helper()

	if render == nil || len(render.Members) != 2 || render.Members[1].AuthoredName != addedName || render.Members[1].RenderedName != addedName {
		t.Fatalf("added member render metadata = %+v", render)
	}

	wantPath := "sessions[1].depends_on[0]"
	if len(render.References) != 1 || render.References[0].Path != wantPath || render.References[0].Authored != dependencyName || render.References[0].Rendered != dependencyName {
		t.Fatalf("added dependency render metadata = %+v, want path %q and target %q", render.References, wantPath, dependencyName)
	}
}

func waitForScenarioSessionStopped(t *testing.T, sm *SessionManager, sessionID string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	for {
		session, ok := sm.Get(sessionID)
		if ok && (session.Status == StatusStopped || session.Status == StatusErrored) {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("session %q did not settle after stop: %+v, ok=%t", sessionID, session, ok)
		}

		time.Sleep(10 * time.Millisecond)
	}
}
