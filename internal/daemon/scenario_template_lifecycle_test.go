package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
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

	stopped, err := sm.StopScenario(wantScenario)
	if err != nil || len(stopped) != 1 {
		t.Fatalf("StopScenario = %v, %v", stopped, err)
	}

	if source, ok := sm.Get(initiatorID); !ok || source.Status != StatusRunning {
		t.Fatalf("shared initiator changed by stop: %+v, ok=%t", source, ok)
	}

	reviewerID := scenario.SessionIDs[1]
	deadline := time.Now().Add(2 * time.Second)

	for {
		reviewer, ok := sm.Get(reviewerID)
		if ok && (reviewer.Status == StatusStopped || reviewer.Status == StatusErrored) {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("reviewer did not settle after stop: %+v, ok=%t", reviewer, ok)
		}

		time.Sleep(10 * time.Millisecond)
	}

	resumed, err := sm.ResumeScenario(wantScenario, 24, 80)
	if err != nil || len(resumed) != 1 {
		t.Fatalf("ResumeScenario = %v, %v", resumed, err)
	}

	deleted, err := sm.DeleteScenario(wantScenario)
	if err != nil || len(deleted) != 1 {
		t.Fatalf("DeleteScenario = %v, %v", deleted, err)
	}

	if _, err := sm.ScenarioStatus(wantScenario); err == nil {
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
