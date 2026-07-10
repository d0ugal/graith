package daemon

import (
	"testing"
	"time"
)

// initGitRepoOnBranch creates a git repo with an initial commit on `main`.
func initGitRepoOnBranch(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, "", "init", "--initial-branch=main", dir)
	gitRun(t, dir, "commit", "--allow-empty", "-m", "initial")
}

// TestReconcileBranch_DetectsCheckout is the core regression test for #1008:
// when an agent checks out a different branch in its worktree (e.g. via
// `gh pr checkout`), reconcileBranch must notice the live HEAD differs from the
// recorded branch, update SessionState.Branch, and reset the PR-watch cursor so
// PR matching re-runs against the new branch.
func TestReconcileBranch_DetectsCheckout(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", Name: "braw", RepoPath: repo, WorktreePath: repo,
		Branch: "main", Status: StatusRunning,
	}

	// Seed a stale cursor + a far-future nextPoll to prove reconcile clears both.
	sm.prWatch.cursors["braw1"] = &prWatchCursor{number: 7, primed: true}
	sm.prWatch.nextPoll["braw1"] = time.Now().Add(time.Hour)

	// Agent adopts an existing PR: a new local branch is checked out.
	gitRun(t, repo, "checkout", "-b", "adopt-pr-42")

	got := sm.reconcileBranch("braw1", "main", repo)
	if got != "adopt-pr-42" {
		t.Fatalf("reconcileBranch = %q, want adopt-pr-42", got)
	}

	if b := sm.state.Sessions["braw1"].Branch; b != "adopt-pr-42" {
		t.Errorf("session branch not updated: got %q, want adopt-pr-42", b)
	}

	if _, ok := sm.prWatch.cursors["braw1"]; ok {
		t.Error("PR-watch cursor should be cleared after a branch change")
	}

	if _, ok := sm.prWatch.nextPoll["braw1"]; ok {
		t.Error("nextPoll should be cleared so the new branch is polled immediately")
	}
}

// TestReconcileBranch_SwitchBack covers the issue's "switches back to the
// original branch" case: after adopting a PR branch, checking the original back
// out is detected the same way and re-records it.
func TestReconcileBranch_SwitchBack(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)
	gitRun(t, repo, "branch", "adopt-pr-42")

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", RepoPath: repo, WorktreePath: repo,
		Branch: "adopt-pr-42", Status: StatusRunning,
	}

	// Worktree is actually on main (agent switched back).
	if got := sm.reconcileBranch("braw1", "adopt-pr-42", repo); got != "main" {
		t.Fatalf("reconcileBranch = %q, want main", got)
	}

	if b := sm.state.Sessions["braw1"].Branch; b != "main" {
		t.Errorf("session branch not updated back: got %q, want main", b)
	}
}

// TestReconcileBranch_NoChange: when the worktree HEAD matches the recorded
// branch, reconcile is a no-op — no state write, cursor left intact.
func TestReconcileBranch_NoChange(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", RepoPath: repo, WorktreePath: repo,
		Branch: "main", Status: StatusRunning,
	}
	cur := &prWatchCursor{number: 7, primed: true}
	sm.prWatch.cursors["braw1"] = cur

	if got := sm.reconcileBranch("braw1", "main", repo); got != "main" {
		t.Fatalf("reconcileBranch = %q, want main", got)
	}

	if sm.prWatch.cursors["braw1"] != cur {
		t.Error("cursor must be preserved when the branch is unchanged")
	}
}

// TestReconcileBranch_DetachedKeepsRecorded: a detached HEAD (e.g. mid-rebase)
// resolves the live branch to "", which must NOT clobber the recorded branch —
// PR awareness should keep polling the recorded branch.
func TestReconcileBranch_DetachedKeepsRecorded(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)
	gitRun(t, repo, "checkout", "--detach", "HEAD")

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", RepoPath: repo, WorktreePath: repo,
		Branch: "main", Status: StatusRunning,
	}

	if got := sm.reconcileBranch("braw1", "main", repo); got != "main" {
		t.Fatalf("detached HEAD should keep recorded branch, got %q", got)
	}

	if b := sm.state.Sessions["braw1"].Branch; b != "main" {
		t.Errorf("recorded branch must be untouched on detach, got %q", b)
	}
}

// TestReconcileBranch_EmptyRecordedUsesLive: with no recorded branch the live
// HEAD is used verbatim (preserving the prior effectiveBranch fallback), and no
// spurious "change" is recorded.
func TestReconcileBranch_EmptyRecordedUsesLive(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", RepoPath: repo, WorktreePath: repo, Status: StatusRunning,
	}

	if got := sm.reconcileBranch("braw1", "", repo); got != "main" {
		t.Fatalf("empty recorded should resolve live HEAD, got %q", got)
	}
}

// TestUpdateSessionBranch_Guards covers the persistence guard rails: unknown
// session, soft-deleted session, and an already-current branch all return false
// (no change), while a genuine change returns true and persists.
func TestUpdateSessionBranch_Guards(t *testing.T) {
	sm := newTestSessionManager(t)

	if sm.updateSessionBranch("ghost", "dreich") {
		t.Error("unknown session should return false")
	}

	deleted := time.Now()
	sm.state.Sessions["thrawn"] = &SessionState{ID: "thrawn", Branch: "main", DeletedAt: &deleted}
	if sm.updateSessionBranch("thrawn", "dreich") {
		t.Error("soft-deleted session should return false")
	}
	if sm.state.Sessions["thrawn"].Branch != "main" {
		t.Error("soft-deleted session branch must not change")
	}

	sm.state.Sessions["braw"] = &SessionState{ID: "braw", Branch: "main"}
	if sm.updateSessionBranch("braw", "main") {
		t.Error("same branch should return false (no-op)")
	}
	if !sm.updateSessionBranch("braw", "bonnie") {
		t.Error("genuine change should return true")
	}
	if sm.state.Sessions["braw"].Branch != "bonnie" {
		t.Errorf("branch not updated, got %q", sm.state.Sessions["braw"].Branch)
	}
}

// TestPRWatchTargets_ReflectsBranchChange verifies the end-to-end wiring: a
// worktree whose HEAD has moved shows up in prWatchTargets under the new branch,
// and the session state is updated as a side effect.
func TestPRWatchTargets_ReflectsBranchChange(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", Name: "braw", RepoPath: repo, WorktreePath: repo,
		Branch: "main", Status: StatusRunning,
	}

	gitRun(t, repo, "checkout", "-b", "adopt-pr-42")

	targets := sm.prWatchTargets()
	if len(targets) != 1 {
		t.Fatalf("want 1 target, got %d", len(targets))
	}
	if targets[0].branch != "adopt-pr-42" {
		t.Errorf("target branch = %q, want adopt-pr-42", targets[0].branch)
	}
	if sm.state.Sessions["braw1"].Branch != "adopt-pr-42" {
		t.Errorf("session branch = %q, want adopt-pr-42", sm.state.Sessions["braw1"].Branch)
	}
}
