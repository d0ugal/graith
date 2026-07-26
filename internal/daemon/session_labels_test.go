package daemon

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func newLabelTestSessionManager(t *testing.T) *SessionManager {
	t.Helper()

	cfg := config.Default()
	cfg.Agents["sleeper"] = config.Agent{
		NonInteractiveArgs: []string{},
		Command:            "sleep",
		Args:               []string{"60"},
	}

	return newSMWithConfig(t, cfg)
}

func TestCreatePersistsNormalizedLabelsAcrossReload(t *testing.T) {
	sm := newLabelTestSessionManager(t)
	cfg := sm.Config()

	created, err := sm.Create(CreateOpts{
		Name: "braw-labels", Labels: []string{"  Urgent ", "urgent", "release"},
		AgentName: "sleeper", NoRepo: true, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

	if want := []string{"Urgent", "release"}; !reflect.DeepEqual(created.Labels, want) {
		t.Fatalf("created labels = %#v, want %#v", created.Labels, want)
	}

	restarted := NewSessionManager(cfg, sm.paths, sm.log)
	if err := restarted.LoadState(); err != nil {
		t.Fatalf("LoadState() after restart = %v", err)
	}

	got, ok := restarted.Get(created.ID)
	if !ok || !reflect.DeepEqual(got.Labels, created.Labels) {
		t.Fatalf("restarted session = %+v, ok=%t; want labels %#v", got, ok, created.Labels)
	}
}

func TestParentedCreateInheritsLabelsByDefault(t *testing.T) {
	sm := newLabelTestSessionManager(t)

	parent, err := sm.Create(CreateOpts{
		Name: "ben-labels", Labels: []string{"strath", "braw"}, AgentName: "sleeper", NoRepo: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, parent.ID) })

	child, err := sm.Create(CreateOpts{
		Name: "bairn-labels", ParentID: parent.ID, AgentName: "sleeper", NoRepo: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, child.ID) })

	want := []string{"strath", "braw"}
	if !reflect.DeepEqual(child.Labels, want) {
		t.Fatalf("parented create labels = %#v, want %#v", child.Labels, want)
	}

	sm.mu.RLock()
	parentLabels := sm.state.Sessions[parent.ID].Labels
	childLabels := sm.state.Sessions[child.ID].Labels
	labelsAlias := len(parentLabels) > 0 && len(childLabels) > 0 && &parentLabels[0] == &childLabels[0]

	sm.mu.RUnlock()

	if len(parentLabels) == 0 || len(childLabels) == 0 {
		t.Fatalf("stored labels unexpectedly empty: parent=%#v child=%#v", parentLabels, childLabels)
	}

	if labelsAlias {
		t.Fatal("parented create labels alias parent labels")
	}

	if _, err := sm.UpdateMetadata(parent.ID, SessionUpdate{AddLabels: []string{"ben-only"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := sm.UpdateMetadata(child.ID, SessionUpdate{AddLabels: []string{"bairn-only"}}); err != nil {
		t.Fatal(err)
	}

	parentAfter, _ := sm.Get(parent.ID)
	childAfter, _ := sm.Get(child.ID)

	if !reflect.DeepEqual(parentAfter.Labels, []string{"strath", "braw", "ben-only"}) {
		t.Fatalf("parent labels after independent update = %#v", parentAfter.Labels)
	}

	if !reflect.DeepEqual(childAfter.Labels, []string{"strath", "braw", "bairn-only"}) {
		t.Fatalf("child labels after independent update = %#v", childAfter.Labels)
	}
}

func TestParentedCreateFromUnlabelledParentStaysUnlabelled(t *testing.T) {
	sm := newLabelTestSessionManager(t)

	parent, err := sm.Create(CreateOpts{
		Name: "ben-empty-labels", AgentName: "sleeper", NoRepo: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, parent.ID) })

	child, err := sm.Create(CreateOpts{
		Name: "bairn-empty-labels", ParentID: parent.ID, AgentName: "sleeper", NoRepo: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, child.ID) })

	if child.Labels == nil || len(child.Labels) != 0 {
		t.Fatalf("parented create labels = %#v, want explicit empty set", child.Labels)
	}
}

func TestParentedCreateExplicitLabelsOverrideInheritance(t *testing.T) {
	sm := newLabelTestSessionManager(t)

	parent, err := sm.Create(CreateOpts{
		Name: "ben-override-labels", Labels: []string{"strath"}, AgentName: "sleeper", NoRepo: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, parent.ID) })

	child, err := sm.Create(CreateOpts{
		Name: "bairn-override-labels", ParentID: parent.ID,
		Labels: []string{"bothy"}, AgentName: "sleeper", NoRepo: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, child.ID) })

	if !reflect.DeepEqual(child.Labels, []string{"bothy"}) {
		t.Fatalf("explicit child labels = %#v, want override set", child.Labels)
	}
}

func TestParentedCreateExplicitEmptyLabelsOptOutOfInheritance(t *testing.T) {
	sm := newLabelTestSessionManager(t)

	parent, err := sm.Create(CreateOpts{
		Name: "ben-optout-labels", Labels: []string{"strath"}, AgentName: "sleeper", NoRepo: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, parent.ID) })

	child, err := sm.Create(CreateOpts{
		Name: "bairn-optout-labels", ParentID: parent.ID,
		Labels: []string{}, AgentName: "sleeper", NoRepo: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, child.ID) })

	if child.Labels == nil || len(child.Labels) != 0 {
		t.Fatalf("explicit empty child labels = %#v, want explicit empty set", child.Labels)
	}
}

func TestCreateWithoutParentDefaultsToEmptyLabels(t *testing.T) {
	sm := newLabelTestSessionManager(t)

	created, err := sm.Create(CreateOpts{
		Name: "braw-empty-labels", AgentName: "sleeper", NoRepo: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

	if created.Labels == nil || len(created.Labels) != 0 {
		t.Fatalf("unparented create labels = %#v, want explicit empty set", created.Labels)
	}
}

func TestTriggerCreatedSessionInheritsParentLabels(t *testing.T) {
	repo := initTempGitRepo(t)

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.DefaultAgent = "sleeper"
	cfg.Agents["sleeper"] = config.Agent{
		NonInteractiveArgs: []string{},
		Command:            "sleep",
		Args:               []string{"60"},
	}
	sm := newSMWithConfig(t, cfg)

	sm.mu.Lock()
	sm.state.Sessions["orch-id"] = &SessionState{
		ID: "orch-id", Name: OrchestratorSessionName,
		SystemKind: SystemKindOrchestrator, Status: StatusRunning,
		Labels: []string{"strath", "thrawn"},
	}
	sm.mu.Unlock()

	created, err := sm.createTriggerSession(createTriggerReq{
		name: "trigger-bairn", agent: "sleeper", repo: repo,
		parentID: "orch-id", triggerName: "thrawn",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

	if !reflect.DeepEqual(created.Labels, []string{"strath", "thrawn"}) {
		t.Fatalf("trigger-created labels = %#v, want inherited parent labels", created.Labels)
	}
}

func TestUpdateMetadataLabelsAreAtomicAndPreserveDisplaySpelling(t *testing.T) {
	sm := newTestSessionManager(t)
	putSession(sm, &SessionState{
		ID: "braw-id", Name: "braw", Status: StatusStopped,
		Labels: []string{"Urgent", "release"},
	})

	updated, err := sm.UpdateMetadata("braw-id", SessionUpdate{
		AddLabels: []string{"urgent", "customer:Brae"}, RemoveLabels: []string{"RELEASE", "missing"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"Urgent", "customer:Brae"}
	if !reflect.DeepEqual(updated.Labels, want) {
		t.Fatalf("updated labels = %#v, want %#v", updated.Labels, want)
	}

	loaded, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(loaded.Sessions["braw-id"].Labels, want) {
		t.Fatalf("persisted labels = %#v, want %#v", loaded.Sessions["braw-id"].Labels, want)
	}
}

func TestUpdateMetadataRejectsCreatingParent(t *testing.T) {
	sm := newTestSessionManager(t)
	putSession(sm, &SessionState{ID: "braw-id", Name: "braw", Status: StatusStopped})
	putSession(sm, &SessionState{ID: "canny-id", Name: "canny", Status: StatusCreating})

	parentID := "canny-id"
	if _, err := sm.UpdateMetadata("braw-id", SessionUpdate{ParentID: &parentID}); err == nil {
		t.Fatal("UpdateMetadata unexpectedly attached a session to a creating parent")
	}
}

func TestUpdateMetadataRejectsActiveSubtree(t *testing.T) {
	sm := newTestSessionManager(t)
	putSession(sm, &SessionState{ID: "braw-id", Name: "braw", Status: StatusStopped})

	sm.mu.Lock()
	sm.subtreeDeleteRoots = map[string]struct{}{"braw-id": {}}
	sm.mu.Unlock()

	name := "canny"
	if _, err := sm.UpdateMetadata("braw-id", SessionUpdate{Name: &name}); err == nil {
		t.Fatal("UpdateMetadata unexpectedly mutated an active subtree delete")
	}
}

func TestUpdateMetadataSaveFailureRollsBackEveryField(t *testing.T) {
	sm := newTestSessionManager(t)
	putSession(sm, &SessionState{
		ID: "canny-id", Name: "auld", ParentID: "ben", Status: StatusStopped,
		Labels: []string{"release"}, Starred: false,
	})
	putSession(sm, &SessionState{ID: "ben", Name: "ben", Status: StatusStopped})

	name := "bonnie"
	parent := ""
	starred := true
	sm.saveStateFault = func() error { return errors.New("dreich disk") }

	_, err := sm.UpdateMetadata("canny-id", SessionUpdate{
		Name: &name, ParentID: &parent, Starred: &starred, AddLabels: []string{"urgent"},
	})
	if err == nil {
		t.Fatal("expected save failure")
	}

	got, _ := sm.Get("canny-id")
	if got.Name != "auld" || got.ParentID != "ben" || got.Starred || !reflect.DeepEqual(got.Labels, []string{"release"}) {
		t.Fatalf("in-memory state survived failed save: %+v", got)
	}
}

func TestConcurrentLabelAndNameUpdatesRetainBoth(t *testing.T) {
	sm := newTestSessionManager(t)
	putSession(sm, &SessionState{ID: "strath-id", Name: "auld", Status: StatusStopped, Labels: []string{}})

	name := "bonnie"
	errs := make(chan error, 2)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()

		_, err := sm.Update("strath-id", &name, nil, nil)
		errs <- err
	}()

	go func() {
		defer wg.Done()

		_, err := sm.UpdateMetadata("strath-id", SessionUpdate{AddLabels: []string{"urgent"}})
		errs <- err
	}()

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent update = %v", err)
		}
	}

	got, _ := sm.Get("strath-id")
	if got.Name != "bonnie" || !reflect.DeepEqual(got.Labels, []string{"urgent"}) {
		t.Fatalf("concurrent updates lost metadata: %+v", got)
	}
}

func TestUpdateMetadataRejectsInvalidAndConflictingLabelsWithoutMutation(t *testing.T) {
	sm := newTestSessionManager(t)
	putSession(sm, &SessionState{ID: "bothy-id", Name: "bothy", Status: StatusStopped, Labels: []string{"release"}})

	for _, update := range []SessionUpdate{
		{AddLabels: []string{""}},
		{AddLabels: []string{"Urgent"}, RemoveLabels: []string{"urgent"}},
	} {
		if _, err := sm.UpdateMetadata("bothy-id", update); err == nil {
			t.Fatalf("UpdateMetadata(%+v) unexpectedly succeeded", update)
		}
	}

	got, _ := sm.Get("bothy-id")
	if !reflect.DeepEqual(got.Labels, []string{"release"}) {
		t.Fatalf("invalid update mutated labels: %#v", got.Labels)
	}
}

func TestLabelsSurviveSoftDeleteAndRestore(t *testing.T) {
	sm := newTestSessionManager(t)
	putSession(sm, &SessionState{
		ID: "bide-id", Name: "bide", Status: StatusStopped,
		Labels: []string{"incident:7", "Urgent"},
	})

	deleted, err := sm.SoftDelete("bide-id")
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(deleted.Labels, []string{"incident:7", "Urgent"}) {
		t.Fatalf("soft delete labels = %#v", deleted.Labels)
	}

	restored, err := sm.Restore("bide-id")
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(restored.Labels, deleted.Labels) {
		t.Fatalf("restored labels = %#v, want %#v", restored.Labels, deleted.Labels)
	}
}
