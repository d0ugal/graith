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

	if err := sm.removeTombstone("braw1"); err != nil {
		t.Fatalf("removeTombstone: %v", err)
	}

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

// TestDeleteAbortsWhenTombstoneUnwritable is the regression test for fail-open
// teardown: if the recovery tombstone can't be written, Delete must abort
// before removing the worktree and keep the session for retry.
func TestDeleteAbortsWhenTombstoneUnwritable(t *testing.T) {
	sm := newTestSessionManager(t)

	worktree := filepath.Join(sm.paths.DataDir, "worktrees", "croft", "hash", "fash0001")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	// Block tombstone creation by planting a regular file where the tombstones
	// directory needs to be — MkdirAll then fails, so writeTombstone errors.
	if err := os.WriteFile(sm.tombstoneDir(), []byte("thrawn"), 0o600); err != nil {
		t.Fatalf("plant tombstone-dir file: %v", err)
	}

	sm.state.Sessions["fash0001"] = &SessionState{
		ID:           "fash0001",
		Name:         "fash",
		WorktreePath: worktree,
		Status:       StatusStopped,
	}

	err := sm.Delete("fash0001")
	if err == nil {
		t.Fatal("Delete succeeded; expected abort when tombstone unwritable")
	}

	sm.mu.RLock()
	sess, ok := sm.state.Sessions["fash0001"]
	sm.mu.RUnlock()

	if !ok {
		t.Fatal("session removed from state despite aborted delete")
	}

	if sess.Status != StatusStopped {
		t.Errorf("session status = %q, want %q after abort", sess.Status, StatusStopped)
	}

	if _, err := os.Stat(worktree); err != nil {
		t.Errorf("worktree torn down despite aborted delete: %v", err)
	}
}

// TestDeleteAbortRestoresRunningSessionOwnership checks the abort path for a
// *running* session with an attached client: the session must be reverted to
// running (not stopped, since nothing was killed) and its attached-client
// ownership restored, rather than left as a live-but-unmanaged agent.
func TestDeleteAbortRestoresRunningSessionOwnership(t *testing.T) {
	sm := newTestSessionManager(t)

	worktree := filepath.Join(sm.paths.DataDir, "worktrees", "croft", "hash", "braw0001")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	// Block tombstone creation so Delete hits the fail-closed abort branch.
	if err := os.WriteFile(sm.tombstoneDir(), []byte("thrawn"), 0o600); err != nil {
		t.Fatalf("plant tombstone-dir file: %v", err)
	}

	sm.state.Sessions["braw0001"] = &SessionState{
		ID:           "braw0001",
		Name:         "braw",
		WorktreePath: worktree,
		Status:       StatusRunning,
	}

	kicked := false
	sm.attachedClients["braw0001"] = &attachedClient{kick: func() { kicked = true }}

	if err := sm.Delete("braw0001"); err == nil {
		t.Fatal("Delete succeeded; expected abort when tombstone unwritable")
	}

	sm.mu.RLock()
	sess := sm.state.Sessions["braw0001"]
	_, clientRestored := sm.attachedClients["braw0001"]
	sm.mu.RUnlock()

	if sess == nil || sess.Status != StatusRunning {
		t.Errorf("running session not reverted to running after abort: %+v", sess)
	}

	if !clientRestored {
		t.Error("attached client not restored after aborted delete")
	}

	if kicked {
		t.Error("attached client was kicked on an aborted delete (should stay attached)")
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
