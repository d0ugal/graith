package daemon

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func TestCreatePersistsNormalizedLabelsAcrossReload(t *testing.T) {
	cfg := config.Default()
	cfg.Agents["sleeper"] = config.Agent{
		NonInteractiveArgs: []string{},
		Command:            "sleep",
		Args:               []string{"60"},
	}
	sm := newSMWithConfig(t, cfg)

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

func TestParentedCreateDoesNotInheritLabels(t *testing.T) {
	cfg := config.Default()
	cfg.Agents["sleeper"] = config.Agent{
		NonInteractiveArgs: []string{},
		Command:            "sleep",
		Args:               []string{"60"},
	}
	sm := newSMWithConfig(t, cfg)

	parent, err := sm.Create(CreateOpts{
		Name: "ben-labels", Labels: []string{"urgent"}, AgentName: "sleeper", NoRepo: true,
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

	if child.Labels == nil || len(child.Labels) != 0 {
		t.Fatalf("parented create labels = %#v, want explicit empty set", child.Labels)
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
