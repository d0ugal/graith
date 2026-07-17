package daemon

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/store"
)

func resultSpec(name, format, destination string, required bool) protocol.ScenarioResultSpec {
	return protocol.ScenarioResultSpec{Name: name, Format: format, Store: destination, Required: required}
}

func seedScenarioResults(t *testing.T, sm *SessionManager, specsByMember map[string][]protocol.ScenarioResultSpec) {
	t.Helper()

	memberNames := make([]string, 0, len(specsByMember))
	for name := range specsByMember {
		memberNames = append(memberNames, name)
	}

	sort.Strings(memberNames)

	seen := make(map[string]string)
	scenario := &ScenarioState{ID: "sc-braw", Name: "braw-fanout", CreatedAt: time.Now().UTC()}

	for _, name := range memberNames {
		id := name + "-id"

		results, err := compileScenarioResults("sc-braw", "braw-fanout", id, name, specsByMember[name], seen)
		if err != nil {
			t.Fatalf("compile results for %s: %v", name, err)
		}

		scenario.SessionIDs = append(scenario.SessionIDs, id)
		scenario.Sessions = append(scenario.Sessions, ScenarioSession{Name: name, Results: results})
		sm.state.Sessions[id] = &SessionState{ID: id, Name: name, Status: StatusRunning}
	}

	sm.state.Scenarios[scenario.ID] = scenario

	if err := sm.saveState(); err != nil {
		t.Fatalf("save seeded scenario: %v", err)
	}
}

func resultForMember(t *testing.T, sm *SessionManager, member, name string) ScenarioResultState {
	t.Helper()

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	scenario := sm.state.Scenarios["sc-braw"]
	for _, session := range scenario.Sessions {
		if session.Name != member {
			continue
		}

		for _, result := range session.Results {
			if result.Name == name {
				return result
			}
		}
	}

	t.Fatalf("result %s/%s not found", member, name)

	return ScenarioResultState{}
}

func TestValidateScenarioResultDeclarations(t *testing.T) {
	valid := []protocol.ScenarioSessionInput{
		{Name: "canny", Results: []protocol.ScenarioResultSpec{
			resultSpec("review", "markdown", "{session_name}/review.md", true),
			resultSpec("facts", "json", "{session_id}/{result_name}.json", false),
		}},
		{Name: "dreich", Results: []protocol.ScenarioResultSpec{
			resultSpec("notes", "text", "notes.txt", true),
		}},
	}
	if err := validateScenarioResultDeclarations("braw-fanout", valid); err != nil {
		t.Fatalf("valid declarations: %v", err)
	}

	tests := []struct {
		name    string
		specs   []protocol.ScenarioResultSpec
		wantErr string
	}{
		{"empty name", []protocol.ScenarioResultSpec{resultSpec("", "text", "a.txt", true)}, "name is invalid"},
		{"uppercase name", []protocol.ScenarioResultSpec{resultSpec("Review", "text", "a.txt", true)}, "name is invalid"},
		{"underscore name", []protocol.ScenarioResultSpec{resultSpec("braw_review", "text", "a.txt", true)}, "name is invalid"},
		{"long name", []protocol.ScenarioResultSpec{resultSpec("a"+strings.Repeat("b", 64), "text", "a.txt", true)}, "name is invalid"},
		{"duplicate name", []protocol.ScenarioResultSpec{resultSpec("review", "text", "a.txt", true), resultSpec("review", "text", "b.txt", true)}, "duplicate result name"},
		{"unsupported format", []protocol.ScenarioResultSpec{resultSpec("review", "yaml", "a.yaml", true)}, "unsupported format"},
		{"empty store", []protocol.ScenarioResultSpec{resultSpec("review", "text", "", true)}, "store template is required"},
		{"unknown placeholder", []protocol.ScenarioResultSpec{resultSpec("review", "text", "{member}/a.txt", true)}, "unknown or malformed"},
		{"absolute store", []protocol.ScenarioResultSpec{resultSpec("review", "text", "/a.txt", true)}, "must not start"},
		{"traversal", []protocol.ScenarioResultSpec{resultSpec("review", "text", "../a.txt", true)}, "'..'"},
		{"directory destination", []protocol.ScenarioResultSpec{resultSpec("review", "text", "braw/", true)}, "must name a document"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateScenarioResultDeclarations("braw-fanout", []protocol.ScenarioSessionInput{{Name: "canny", Results: test.specs}})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}

	collision := []protocol.ScenarioSessionInput{
		{Name: "canny", Results: []protocol.ScenarioResultSpec{resultSpec("review", "text", "shared.txt", true)}},
		{Name: "dreich", Results: []protocol.ScenarioResultSpec{resultSpec("review", "markdown", "shared.txt", true)}},
	}
	if err := validateScenarioResultDeclarations("braw-fanout", collision); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("collision error = %v", err)
	}
}

func TestRenderScenarioResultDestination(t *testing.T) {
	got, err := renderScenarioResultDestination(
		"sc-canny", "braw-fanout", "sess-dreich", "dreich", "review",
		"{scenario_name}/{session_name}/{session_id}/{result_name}.md",
	)
	if err != nil {
		t.Fatal(err)
	}

	want := "scenarios/sc-canny/results/braw-fanout/dreich/sess-dreich/review.md"
	if got != want {
		t.Fatalf("destination = %q, want %q", got, want)
	}
}

func TestPublishScenarioResultJSONPersistsAndStores(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.mu.Lock()
	seedScenarioResults(t, sm, map[string][]protocol.ScenarioResultSpec{
		"canny": {resultSpec("facts", "json", "{session_name}/facts.json", true)},
	})
	sm.mu.Unlock()

	response, err := sm.PublishScenarioResult(
		authContext{authenticated: true, role: roleSession, sessionID: "canny-id"},
		protocol.ScenarioResultPublishMsg{Scenario: "braw-fanout", Name: "facts", Body: `{"braw":true}`},
	)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	if response.Result.Status != "available" || response.Result.SizeBytes != len(`{"braw":true}`) {
		t.Fatalf("response = %+v", response)
	}

	body, err := store.Get(store.SharedStorePath(sm.paths.DataDir), response.Result.Destination)
	if err != nil {
		t.Fatalf("get stored result: %v", err)
	}

	if body != `{"braw":true}` {
		t.Fatalf("stored body = %q", body)
	}

	loaded, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}

	result := loaded.Scenarios["sc-braw"].Sessions[0].Results[0]
	if result.Status != ScenarioResultAvailable || result.PublishedAt.IsZero() || result.SizeBytes != len(body) {
		t.Fatalf("reloaded result = %+v", result)
	}

	restarted := NewSessionManager(sm.Config(), sm.paths, sm.log)
	if err := restarted.LoadState(); err != nil {
		t.Fatalf("restart manager load: %v", err)
	}

	record, err := restarted.ScenarioStatus("braw-fanout")
	if err != nil {
		t.Fatalf("restart scenario status: %v", err)
	}

	if got := record.Sessions[0].Results[0].Status; got != "available" {
		t.Fatalf("restart status = %q, want available", got)
	}
}

func TestPublishRequiredMarkdownAndJSONFromMultipleMembers(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.mu.Lock()
	seedScenarioResults(t, sm, map[string][]protocol.ScenarioResultSpec{
		"canny":  {resultSpec("review", "markdown", "{session_name}/{result_name}.md", true)},
		"dreich": {resultSpec("facts", "json", "{session_name}/{result_name}.json", true)},
	})
	sm.mu.Unlock()

	publications := []struct {
		sessionID string
		name      string
		body      string
		wantKey   string
	}{
		{"canny-id", "review", "# Canny review", "scenarios/sc-braw/results/canny/review.md"},
		{"dreich-id", "facts", `{"verdict":"braw"}`, "scenarios/sc-braw/results/dreich/facts.json"},
	}

	for _, publication := range publications {
		response, err := sm.PublishScenarioResult(
			authContext{authenticated: true, role: roleSession, sessionID: publication.sessionID},
			protocol.ScenarioResultPublishMsg{Scenario: "braw-fanout", Name: publication.name, Body: publication.body},
		)
		if err != nil {
			t.Fatalf("publish %s: %v", publication.name, err)
		}

		if response.Result.Destination != publication.wantKey || response.Result.Status != "available" {
			t.Fatalf("publish %s response = %+v", publication.name, response)
		}

		body, err := store.Get(store.SharedStorePath(sm.paths.DataDir), publication.wantKey)
		if err != nil {
			t.Fatalf("consume %s: %v", publication.name, err)
		}

		if body != publication.body {
			t.Fatalf("consume %s body = %q, want %q", publication.name, body, publication.body)
		}
	}

	record, err := sm.ScenarioStatus("braw-fanout")
	if err != nil {
		t.Fatal(err)
	}

	if record.Status != "complete" {
		t.Fatalf("scenario status = %q, want complete", record.Status)
	}
}

func TestScenarioStatusExposesEveryResultState(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.mu.Lock()
	seedScenarioResults(t, sm, map[string][]protocol.ScenarioResultSpec{
		"canny": {
			resultSpec("pending", "text", "pending.txt", false),
			resultSpec("available", "markdown", "available.md", false),
			resultSpec("invalid", "json", "invalid.json", false),
			resultSpec("failed", "text", "failed.txt", false),
		},
	})
	scenario := sm.state.Scenarios["sc-braw"]
	scenario.Sessions[0].Results[1].Status = ScenarioResultAvailable
	scenario.Sessions[0].Results[2].Status = ScenarioResultInvalid
	scenario.Sessions[0].Results[2].Error = "not valid JSON"
	scenario.Sessions[0].Results[3].Status = ScenarioResultFailed
	scenario.Sessions[0].Results[3].Error = "store unavailable"
	record := sm.buildScenarioRecord(scenario)
	sm.mu.Unlock()

	want := []string{"pending", "available", "invalid", "failed"}
	for i, status := range want {
		if got := record.Sessions[0].Results[i].Status; got != status {
			t.Fatalf("result %d status = %q, want %q", i, got, status)
		}
	}

	if record.Sessions[0].Results[2].Error != "not valid JSON" || record.Sessions[0].Results[3].Error != "store unavailable" {
		t.Fatalf("result errors not exposed: %+v", record.Sessions[0].Results)
	}
}

func TestPublishScenarioResultMalformedThenRetry(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.mu.Lock()
	seedScenarioResults(t, sm, map[string][]protocol.ScenarioResultSpec{
		"canny": {resultSpec("facts", "json", "facts.json", true)},
	})
	sm.mu.Unlock()

	auth := authContext{authenticated: true, role: roleSession, sessionID: "canny-id"}

	if _, err := sm.PublishScenarioResult(auth, protocol.ScenarioResultPublishMsg{
		Scenario: "braw-fanout", Name: "facts", Body: `{"truncated":`,
	}); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("malformed error = %v", err)
	}

	invalid := resultForMember(t, sm, "canny", "facts")
	if invalid.Status != ScenarioResultInvalid || !strings.Contains(invalid.Error, "not valid JSON") {
		t.Fatalf("invalid metadata = %+v", invalid)
	}

	if _, err := sm.PublishScenarioResult(auth, protocol.ScenarioResultPublishMsg{
		Scenario: "braw-fanout", Name: "facts", Body: `{"complete":true}`,
	}); err != nil {
		t.Fatalf("retry: %v", err)
	}

	available := resultForMember(t, sm, "canny", "facts")
	if available.Status != ScenarioResultAvailable || available.Error != "" {
		t.Fatalf("available metadata = %+v", available)
	}
}

func TestPublishScenarioResultErrorsDoNotForgeSuccess(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.mu.Lock()
	seedScenarioResults(t, sm, map[string][]protocol.ScenarioResultSpec{
		"canny":  {resultSpec("review", "markdown", "canny.md", true)},
		"dreich": {resultSpec("facts", "json", "dreich.json", true)},
	})
	sm.mu.Unlock()

	tests := []struct {
		name    string
		auth    authContext
		msg     protocol.ScenarioResultPublishMsg
		wantErr string
	}{
		{"human unauthorized", authContext{role: roleLocalHuman}, protocol.ScenarioResultPublishMsg{Name: "review", Body: "braw"}, "authenticated session"},
		{"peer cannot publish other declaration", authContext{authenticated: true, role: roleSession, sessionID: "dreich-id"}, protocol.ScenarioResultPublishMsg{Scenario: "braw-fanout", Name: "review", Body: "braw"}, "not declared for this member"},
		{"misnamed", authContext{authenticated: true, role: roleSession, sessionID: "canny-id"}, protocol.ScenarioResultPublishMsg{Scenario: "braw-fanout", Name: "reivew", Body: "braw"}, "not declared"},
		{"misrouted scenario", authContext{authenticated: true, role: roleSession, sessionID: "canny-id"}, protocol.ScenarioResultPublishMsg{Scenario: "other", Name: "review", Body: "braw"}, "not found"},
		{"empty", authContext{authenticated: true, role: roleSession, sessionID: "canny-id"}, protocol.ScenarioResultPublishMsg{Scenario: "braw-fanout", Name: "review", Body: "  \n"}, "must not be empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := sm.PublishScenarioResult(test.auth, test.msg); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}

	if got := resultForMember(t, sm, "canny", "review"); got.Status == ScenarioResultAvailable {
		t.Fatalf("unauthorized/misnamed publication marked result available: %+v", got)
	}
}

func TestPublishScenarioResultRequiresSelectorForSharedMemberAmbiguity(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.mu.Lock()
	seedScenarioResults(t, sm, map[string][]protocol.ScenarioResultSpec{
		"canny": {resultSpec("review", "markdown", "review.md", true)},
	})

	secondResults, err := compileScenarioResults(
		"sc-dreich", "dreich-fanout", "canny-id", "canny",
		[]protocol.ScenarioResultSpec{resultSpec("review", "markdown", "review.md", true)},
		make(map[string]string),
	)
	if err != nil {
		t.Fatal(err)
	}

	sm.state.Scenarios["sc-dreich"] = &ScenarioState{
		ID: "sc-dreich", Name: "dreich-fanout", CreatedAt: time.Now().UTC(),
		SessionIDs: []string{"canny-id"},
		Sessions:   []ScenarioSession{{Name: "canny", Shared: true, Results: secondResults}},
	}
	if err := sm.saveState(); err != nil {
		t.Fatal(err)
	}
	sm.mu.Unlock()

	auth := authContext{authenticated: true, role: roleSession, sessionID: "canny-id"}
	if _, err := sm.PublishScenarioResult(auth, protocol.ScenarioResultPublishMsg{
		Name: "review", Body: "# Ambiguous",
	}); err == nil || !strings.Contains(err.Error(), "multiple scenarios") {
		t.Fatalf("ambiguous publication error = %v", err)
	}

	response, err := sm.PublishScenarioResult(auth, protocol.ScenarioResultPublishMsg{
		Scenario: "dreich-fanout", Name: "review", Body: "# Selected",
	})
	if err != nil {
		t.Fatalf("selected publication: %v", err)
	}

	if response.Scenario != "dreich-fanout" || response.Result.Destination != "scenarios/sc-dreich/results/review.md" {
		t.Fatalf("selected response = %+v", response)
	}
}

func TestPublishScenarioResultOversizeAndStoreFailure(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.mu.Lock()
	seedScenarioResults(t, sm, map[string][]protocol.ScenarioResultSpec{
		"canny": {resultSpec("review", "text", "review.txt", true)},
	})
	sm.mu.Unlock()

	auth := authContext{authenticated: true, role: roleSession, sessionID: "canny-id"}

	oversized := strings.Repeat("x", protocol.MaxScenarioResultBodyBytes+1)
	if _, err := sm.PublishScenarioResult(auth, protocol.ScenarioResultPublishMsg{
		Scenario: "braw-fanout", Name: "review", Body: oversized,
	}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversize error = %v", err)
	}

	if got := resultForMember(t, sm, "canny", "review"); got.Status != ScenarioResultInvalid {
		t.Fatalf("oversize status = %q, want invalid", got.Status)
	}

	blockedDataDir := filepath.Join(t.TempDir(), "dreich-file")
	if err := os.WriteFile(blockedDataDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm.paths.DataDir = blockedDataDir

	if _, err := sm.PublishScenarioResult(auth, protocol.ScenarioResultPublishMsg{
		Scenario: "braw-fanout", Name: "review", Body: "complete",
	}); err == nil || !strings.Contains(err.Error(), "initialize shared result store") {
		t.Fatalf("store failure error = %v", err)
	}

	if got := resultForMember(t, sm, "canny", "review"); got.Status != ScenarioResultFailed || got.Error != "result storage failed" {
		t.Fatalf("store failure result = %+v, want failed with sanitized error", got)
	}
}

func TestScenarioRequiredResultsGateCompletionOptionalDoesNot(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.todos = newTestTodoStore(t)
	sm.mu.Lock()
	seedScenarioResults(t, sm, map[string][]protocol.ScenarioResultSpec{
		"canny": {
			resultSpec("review", "markdown", "review.md", true),
			resultSpec("notes", "text", "notes.txt", false),
		},
	})
	sm.mu.Unlock()

	item, err := sm.todos.Add(TodoAdd{
		Scope: "scenario:sc-braw", Title: "finish the canny review", Assignee: "canny-id", CreatedBy: "orchestrator",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := sm.todos.Claim(item.ID, "canny-id"); err != nil {
		t.Fatal(err)
	}

	if _, err := sm.todos.Transition(item.ID, TodoStatusDone, "canny-id", false); err != nil {
		t.Fatal(err)
	}

	sm.mu.RLock()
	pendingStatus := sm.buildScenarioRecord(sm.state.Scenarios["sc-braw"]).Status
	sm.mu.RUnlock()

	if pendingStatus != "running" {
		t.Fatalf("done todo with pending required result status = %q, want running", pendingStatus)
	}

	sm.mu.Lock()
	scenario := sm.state.Scenarios["sc-braw"]
	scenario.Sessions[0].Results[0].Status = ScenarioResultAvailable

	scenario.Sessions[0].Results[1].Status = ScenarioResultInvalid
	completeStatus := sm.buildScenarioRecord(scenario).Status
	sm.mu.Unlock()

	if completeStatus != "complete" {
		t.Fatalf("available required + invalid optional status = %q, want complete", completeStatus)
	}
}

func TestScenarioRequiredResultWithoutTodosGatesCompletion(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.todos = newTestTodoStore(t)
	sm.mu.Lock()
	seedScenarioResults(t, sm, map[string][]protocol.ScenarioResultSpec{
		"canny": {resultSpec("review", "markdown", "review.md", true)},
	})

	scenario := sm.state.Scenarios["sc-braw"]
	if got := sm.buildScenarioRecord(scenario).Status; got != "running" {
		t.Fatalf("pending required result without todos status = %q, want running", got)
	}

	scenario.Sessions[0].Results[0].Status = ScenarioResultAvailable
	if got := sm.buildScenarioRecord(scenario).Status; got != "complete" {
		t.Fatalf("available required result without todos status = %q, want complete", got)
	}
	sm.mu.Unlock()
}

func TestDirectStoreWriteDoesNotSatisfyScenarioResult(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.mu.Lock()
	seedScenarioResults(t, sm, map[string][]protocol.ScenarioResultSpec{
		"canny": {resultSpec("review", "markdown", "review.md", true)},
	})
	sm.mu.Unlock()

	result := resultForMember(t, sm, "canny", "review")

	storeDir := store.SharedStorePath(sm.paths.DataDir)
	if err := store.Init(storeDir); err != nil {
		t.Fatal(err)
	}

	if err := store.Put(storeDir, result.Destination, "misrouted direct write"); err != nil {
		t.Fatal(err)
	}

	if got := resultForMember(t, sm, "canny", "review"); got.Status != ScenarioResultPending {
		t.Fatalf("direct store write changed status to %q", got.Status)
	}
}

func TestConcurrentScenarioResultPublicationKeepsMetadataWithContent(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.mu.Lock()
	seedScenarioResults(t, sm, map[string][]protocol.ScenarioResultSpec{
		"canny": {resultSpec("review", "text", "review.txt", true)},
	})
	sm.mu.Unlock()

	auth := authContext{authenticated: true, role: roleSession, sessionID: "canny-id"}
	bodies := []string{"short", strings.Repeat("braw", 128)}

	var wg sync.WaitGroup

	for _, body := range bodies {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if _, err := sm.PublishScenarioResult(auth, protocol.ScenarioResultPublishMsg{
				Scenario: "braw-fanout", Name: "review", Body: body,
			}); err != nil {
				t.Errorf("publish: %v", err)
			}
		}()
	}

	wg.Wait()

	result := resultForMember(t, sm, "canny", "review")

	body, err := store.Get(store.SharedStorePath(sm.paths.DataDir), result.Destination)
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != ScenarioResultAvailable || result.SizeBytes != len(body) {
		t.Fatalf("metadata %+v does not match stored body length %d", result, len(body))
	}
}

func TestScenarioResultPublishHandlerAuthenticationAndResponse(t *testing.T) {
	h := newTestHarness(t)
	h.sm.mu.Lock()
	seedScenarioResults(t, h.sm, map[string][]protocol.ScenarioResultSpec{
		"canny": {resultSpec("review", "markdown", "review.md", true)},
	})
	h.sm.state.Sessions["canny-id"].Token = "tok-canny-result"
	h.sm.tokenIndex["tok-canny-result"] = "canny-id"
	h.sm.mu.Unlock()

	// A local human is not a member identity and cannot publish on behalf of one.
	h.sendControl(t, "scenario_result_publish", protocol.ScenarioResultPublishMsg{
		Scenario: "braw-fanout", Name: "review", Body: "human forgery",
	})
	h.expectError(t, "authenticated session")

	h.sendControlWithToken(t, "scenario_result_publish", protocol.ScenarioResultPublishMsg{
		Scenario: "braw-fanout", Name: "review", Body: "# Braw review",
	}, "tok-canny-result")
	env := h.expectType(t, "scenario_result_published")

	var response protocol.ScenarioResultPublishResponse
	if err := protocol.DecodePayload(env, &response); err != nil {
		t.Fatal(err)
	}

	if response.Member != "canny" || response.Result.Status != "available" {
		t.Fatalf("response = %+v", response)
	}
}

func TestScenarioResultPublishHandlerRejectsMalformedPayload(t *testing.T) {
	h := newTestHarness(t)
	h.sendWrongShapePayload(t, "scenario_result_publish")
	h.expectError(t, "invalid scenario_result_publish")
}

func TestScenarioResultSurvivesMemberStopResume(t *testing.T) {
	sm, orchestratorID := newScenarioOrchestrator(t)
	repo := initScenarioGitRepo(t)

	scenario, err := sm.StartScenario(protocol.ScenarioStartMsg{
		CallerSessionID: orchestratorID,
		Name:            "braw-resume",
		Sessions: []protocol.ScenarioSessionInput{{
			Name: "canny-worker", Repo: repo,
			Results: []protocol.ScenarioResultSpec{resultSpec("review", "markdown", "review.md", true)},
		}},
	}, 24, 80)
	if err != nil {
		t.Fatalf("start scenario: %v", err)
	}

	memberID := scenario.SessionIDs[0]

	if _, err := sm.PublishScenarioResult(
		authContext{authenticated: true, role: roleSession, sessionID: memberID},
		protocol.ScenarioResultPublishMsg{Scenario: "braw-resume", Name: "review", Body: "# Complete"},
	); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if stopped, err := sm.StopScenario("braw-resume"); err != nil || len(stopped) != 1 {
		t.Fatalf("stop scenario: stopped=%v err=%v", stopped, err)
	}

	waitForStatus(t, sm, memberID, StatusStopped)

	sm.mu.RLock()
	statusAfterStop := sm.state.Scenarios[scenario.ID].Sessions[0].Results[0].Status
	sm.mu.RUnlock()

	if statusAfterStop != ScenarioResultAvailable {
		t.Fatalf("status after stop = %q", statusAfterStop)
	}

	if resumed, err := sm.ResumeScenario("braw-resume", 24, 80); err != nil || len(resumed) != 1 {
		t.Fatalf("resume scenario: resumed=%v err=%v", resumed, err)
	}

	sm.mu.RLock()
	got := sm.state.Scenarios[scenario.ID].Sessions[0].Results[0]
	sm.mu.RUnlock()

	if got.Status != ScenarioResultAvailable || got.PublishedAt.IsZero() {
		t.Fatalf("result after resume = %+v", got)
	}
}
