package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteRemoveTombstoneRoundTrip(t *testing.T) {
	sm := newTestSessionManager(t)

	tomb := tombstone{
		teardownSpec: teardownSpec{ID: "braw1", WorktreePath: "/some/bothy", Branch: "b"},
		Name:         "braw",
		CreatedAt:    time.Now(),
	}

	if err := sm.writeTombstone(tomb); err != nil {
		t.Fatalf("writeTombstone: %v", err)
	}

	path := sm.tombstonePath("braw1")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tombstone: %v", err)
	}

	var got tombstone
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal tombstone: %v", err)
	}

	if got.ID != "braw1" || got.WorktreePath != "/some/bothy" || got.Name != "braw" {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	sm.removeTombstone("braw1")

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("tombstone still present after remove (err=%v)", err)
	}
}

// TestResumeTombstonesFinishesInterruptedDelete is the regression test for the
// crash-mid-delete case: a session marked deleting with a leftover tombstone and
// an on-disk worktree must be fully cleaned on next startup.
func TestResumeTombstonesFinishesInterruptedDelete(t *testing.T) {
	sm := newTestSessionManager(t)

	// A bare worktree dir (no git repo) exercises the os.RemoveAll teardown path.
	worktree := filepath.Join(sm.paths.DataDir, "worktrees", "croft", "hash", "thrawn1")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	// Simulate the state a crash leaves: session stuck in deleting.
	sm.state.Sessions["thrawn1"] = &SessionState{
		ID:           "thrawn1",
		Name:         "thrawn",
		WorktreePath: worktree,
		Status:       StatusDeleting,
	}

	if err := sm.writeTombstone(tombstone{
		teardownSpec: teardownSpec{ID: "thrawn1", WorktreePath: worktree},
		Name:         "thrawn",
		CreatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("writeTombstone: %v", err)
	}

	sm.resumeTombstones()

	if _, ok := sm.state.Sessions["thrawn1"]; ok {
		t.Error("session still in state after resume")
	}

	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("worktree still on disk after resume (err=%v)", err)
	}

	if _, err := os.Stat(sm.tombstonePath("thrawn1")); !os.IsNotExist(err) {
		t.Error("tombstone not removed after resume")
	}
}

// TestResumeTombstonesRemovesTombstoneEvenWhenWorktreeGone covers the crash
// window between a successful teardown and tombstone removal: on resume the
// stale tombstone must be cleared without error even though the worktree is
// already gone.
func TestResumeTombstonesRemovesTombstoneEvenWhenWorktreeGone(t *testing.T) {
	sm := newTestSessionManager(t)

	missing := filepath.Join(sm.paths.DataDir, "worktrees", "croft", "hash", "gone")

	if err := sm.writeTombstone(tombstone{
		teardownSpec: teardownSpec{ID: "gone", WorktreePath: missing},
		Name:         "haar",
		CreatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("writeTombstone: %v", err)
	}

	sm.resumeTombstones()

	if _, err := os.Stat(sm.tombstonePath("gone")); !os.IsNotExist(err) {
		t.Error("stale tombstone not removed when worktree already gone")
	}
}

func TestResumeTombstonesIgnoresCorruptFile(t *testing.T) {
	sm := newTestSessionManager(t)

	if err := os.MkdirAll(sm.tombstoneDir(), 0o700); err != nil {
		t.Fatalf("mkdir tombstones: %v", err)
	}

	corrupt := filepath.Join(sm.tombstoneDir(), "fash.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	// Must not panic, and should clear the unparseable file.
	sm.resumeTombstones()

	if _, err := os.Stat(corrupt); !os.IsNotExist(err) {
		t.Error("corrupt tombstone not removed")
	}
}

func TestTeardownArtifactsInPlaceIsNoop(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir()
	// In-place teardown must leave the repo completely untouched.
	if err := sm.teardownArtifacts(teardownSpec{ID: "x", InPlace: true, RepoPath: repo, WorktreePath: repo}); err != nil {
		t.Fatalf("teardownArtifacts: %v", err)
	}

	if _, err := os.Stat(repo); err != nil {
		t.Errorf("in-place teardown removed the repo: %v", err)
	}
}
