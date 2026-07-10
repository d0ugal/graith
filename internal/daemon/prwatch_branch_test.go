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
// recorded branch, return the live branch to poll, and reset the PR-watch cursor
// + nextPoll so PR matching re-runs against the new branch.
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
	// Seed stale PR/CI display to prove it is cleared on the change.
	sm.state.Sessions["braw1"].PullRequest = PRStatus{Number: 7, State: "open"}
	sm.state.Sessions["braw1"].CI = CIStatus{State: "passing"}

	// Agent adopts an existing PR: a new local branch is checked out.
	gitRun(t, repo, "checkout", "-b", "adopt-pr-42")

	got := sm.reconcileBranch("braw1", "main", repo)
	if got != "adopt-pr-42" {
		t.Fatalf("reconcileBranch = %q, want adopt-pr-42", got)
	}

	if _, ok := sm.prWatch.cursors["braw1"]; ok {
		t.Error("PR-watch cursor should be cleared after a branch change")
	}

	if _, ok := sm.prWatch.nextPoll["braw1"]; ok {
		t.Error("nextPoll should be cleared so the new branch is polled immediately")
	}

	if pr := sm.state.Sessions["braw1"].PullRequest; pr.Number != 0 {
		t.Errorf("stale PR display should be cleared on branch change, got %+v", pr)
	}

	if ci := sm.state.Sessions["braw1"].CI; ci.State != "" {
		t.Errorf("stale CI display should be cleared on branch change, got %+v", ci)
	}
}

// TestReconcileBranch_DoesNotMutateOwnedBranch is the regression test for the
// teardown-key hazard: reconcileBranch must NOT rewrite SessionState.Branch,
// because that field is the branch teardown/purge force-deletes (git branch -D)
// and the git-pull blocks check keys off. If it were rewritten to the adopted
// branch, `gr purge` would delete the adopted PR branch and leak the graith
// created one.
func TestReconcileBranch_DoesNotMutateOwnedBranch(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", RepoPath: repo, WorktreePath: repo,
		Branch: "d0ugal/graith/braw", Status: StatusRunning,
	}
	// Rename the initial branch to match the recorded (graith-owned) branch.
	gitRun(t, repo, "branch", "-m", "d0ugal/graith/braw")
	// Agent adopts a foreign PR branch.
	gitRun(t, repo, "checkout", "-b", "adopt-pr-42")

	if got := sm.reconcileBranch("braw1", "d0ugal/graith/braw", repo); got != "adopt-pr-42" {
		t.Fatalf("reconcileBranch should poll the live branch, got %q", got)
	}

	if b := sm.state.Sessions["braw1"].Branch; b != "d0ugal/graith/braw" {
		t.Errorf("owned Branch (teardown key) must NOT change, got %q", b)
	}
}

// TestReconcileBranch_SwitchBack covers the issue's "switches back to the
// original branch" case: after adopting a PR branch, checking the original back
// out is detected the same way and resets the cursor again.
func TestReconcileBranch_SwitchBack(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)
	gitRun(t, repo, "branch", "adopt-pr-42")

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", RepoPath: repo, WorktreePath: repo,
		Branch: "main", Status: StatusRunning,
	}

	// First: adopt the PR branch (a change relative to recorded "main").
	if got := sm.reconcileBranch("braw1", "main", repo); got != "main" {
		t.Fatalf("worktree is on main, want main, got %q", got)
	}

	// Move to the adopted branch — a change.
	gitRun(t, repo, "checkout", "adopt-pr-42")

	sm.prWatch.cursors["braw1"] = &prWatchCursor{number: 42, primed: true}
	if got := sm.reconcileBranch("braw1", "main", repo); got != "adopt-pr-42" {
		t.Fatalf("want adopt-pr-42, got %q", got)
	}

	if _, ok := sm.prWatch.cursors["braw1"]; ok {
		t.Error("cursor should reset when moving to the adopted branch")
	}

	// Switch back to main — detected as another change against the last poll.
	gitRun(t, repo, "checkout", "main")

	sm.prWatch.cursors["braw1"] = &prWatchCursor{number: 42, primed: true}
	if got := sm.reconcileBranch("braw1", "main", repo); got != "main" {
		t.Fatalf("want main after switch-back, got %q", got)
	}

	if _, ok := sm.prWatch.cursors["braw1"]; ok {
		t.Error("cursor should reset again on switch-back")
	}
}

// TestReconcileBranch_NoChange: when the worktree HEAD matches the branch last
// polled, reconcile is a no-op — cursor left intact.
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

	// First observation baselines to recorded "main" == live "main": no change.
	if got := sm.reconcileBranch("braw1", "main", repo); got != "main" {
		t.Fatalf("reconcileBranch = %q, want main", got)
	}

	if sm.prWatch.cursors["braw1"] != cur {
		t.Error("cursor must be preserved on first observation with no drift")
	}

	// Second observation: still main, still no change.
	if got := sm.reconcileBranch("braw1", "main", repo); got != "main" {
		t.Fatalf("reconcileBranch = %q, want main", got)
	}

	if sm.prWatch.cursors["braw1"] != cur {
		t.Error("cursor must be preserved when the branch is unchanged")
	}
}

// TestReconcileBranch_DetachedKeepsRecorded: a detached HEAD (e.g. mid-rebase)
// resolves the live branch to "", which must fall back to the recorded branch —
// PR awareness should keep polling the recorded branch, not drop it.
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
}

// TestReconcileBranch_EmptyRecordedUsesLive: with no recorded branch the live
// HEAD is used, and the first observation is NOT treated as a spurious change
// (no cursor present to reset, no log noise).
func TestReconcileBranch_EmptyRecordedUsesLive(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", RepoPath: repo, WorktreePath: repo, Status: StatusRunning,
	}
	cur := &prWatchCursor{number: 3, primed: true}
	sm.prWatch.cursors["braw1"] = cur

	if got := sm.reconcileBranch("braw1", "", repo); got != "main" {
		t.Fatalf("empty recorded should resolve live HEAD, got %q", got)
	}

	if sm.prWatch.cursors["braw1"] != cur {
		t.Error("empty-recorded first observation must not be treated as a change")
	}
}

// TestNotePollBranch_Drift covers the drift-detection bookkeeping directly:
// first observation baselines to the recorded branch, a drift resets cursor +
// nextPoll, and a same-branch re-observation is a no-op.
func TestNotePollBranch_Drift(t *testing.T) {
	sm := newTestSessionManager(t)

	// First observation, live == recorded: no change.
	if sm.notePollBranch("braw", "main", "main") {
		t.Error("first observation matching recorded should not be a change")
	}
	// Same again: no change.
	if sm.notePollBranch("braw", "main", "main") {
		t.Error("re-observing the same branch should not be a change")
	}

	// Drift to a new branch: change, resets cursor + nextPoll.
	sm.prWatch.cursors["braw"] = &prWatchCursor{number: 1}

	sm.prWatch.nextPoll["braw"] = time.Now().Add(time.Hour)
	if !sm.notePollBranch("braw", "main", "bonnie") {
		t.Error("drift to a new branch should be reported as a change")
	}

	if _, ok := sm.prWatch.cursors["braw"]; ok {
		t.Error("cursor should be cleared on drift")
	}

	if _, ok := sm.prWatch.nextPoll["braw"]; ok {
		t.Error("nextPoll should be cleared on drift")
	}

	// First observation with empty recorded baselines to live: no change.
	if sm.notePollBranch("thrawn", "", "dreich") {
		t.Error("empty-recorded first observation should not be a change")
	}
}

// TestPRWatchTargets_ReflectsBranchChange verifies the end-to-end wiring: a
// worktree whose HEAD has moved shows up in prWatchTargets under the new branch,
// while the persisted SessionState.Branch (teardown key) is left untouched.
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

	if sm.state.Sessions["braw1"].Branch != "main" {
		t.Errorf("owned Branch must stay 'main', got %q", sm.state.Sessions["braw1"].Branch)
	}
}
