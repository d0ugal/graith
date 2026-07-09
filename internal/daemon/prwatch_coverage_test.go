package daemon

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func newPRWatchCovSM() *SessionManager {
	return &SessionManager{
		state:   NewState(),
		prWatch: newPRWatchState(),
		cfg:     &config.Config{},
		log:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

func TestPollIntervalFor_Cov(t *testing.T) {
	cfg := &config.PRWatchConfig{
		PollTerminal: "10m",
		PollPending:  "1m",
		PollMerged:   "1h",
	}

	if got := pollIntervalFor(cfg, "merged", ""); got != time.Hour {
		t.Errorf("merged should use PollMerged, got %v", got)
	}

	if got := pollIntervalFor(cfg, "closed", "passing"); got != time.Hour {
		t.Errorf("closed should use PollMerged, got %v", got)
	}

	if got := pollIntervalFor(cfg, "open", "pending"); got != time.Minute {
		t.Errorf("open+pending should use PollPending, got %v", got)
	}

	if got := pollIntervalFor(cfg, "open", "passing"); got != 10*time.Minute {
		t.Errorf("open+passing should use PollTerminal, got %v", got)
	}
}

func TestSchedulePoll_Cov(t *testing.T) {
	sm := newPRWatchCovSM()

	before := time.Now()
	sm.schedulePoll("bide", time.Hour)
	after := time.Now()

	sm.prWatch.mu.Lock()
	next, ok := sm.prWatch.nextPoll["bide"]
	sm.prWatch.mu.Unlock()

	if !ok {
		t.Fatal("schedulePoll should record a nextPoll time")
	}

	// nextPoll must be now+1h, bracketed by the before/after snapshots — a bug
	// that used a different multiple of the interval would fall outside this window.
	if next.Before(before.Add(time.Hour)) || next.After(after.Add(time.Hour)) {
		t.Errorf("nextPoll should be ~1h out, got %v (window %v..%v)", next, before.Add(time.Hour), after.Add(time.Hour))
	}
}

func TestWriteAndClearPRState_Cov(t *testing.T) {
	sm := newPRWatchCovSM()
	sm.state.Sessions["braw"] = &SessionState{ID: "braw"}

	sm.writePRState("braw", prData{
		Number: 7, State: "open", URL: "https://example/pr/7",
		ReviewDecision: "approved", HeadRefOid: "sha1", Mergeable: "MERGEABLE",
		CIState: "failing", FailingChecks: []string{"build"},
	})

	s := sm.state.Sessions["braw"]
	if s.PullRequest.Number != 7 || s.PullRequest.State != "open" || s.PullRequest.Mergeable != "MERGEABLE" {
		t.Errorf("PR state not written: %+v", s.PullRequest)
	}

	if s.CI.State != "failing" || len(s.CI.FailingChecks) != 1 {
		t.Errorf("CI state not written: %+v", s.CI)
	}

	// An empty CIState must NOT clobber the last-known CI badge.
	sm.writePRState("braw", prData{Number: 7, State: "open", CIState: ""})
	if sm.state.Sessions["braw"].CI.State != "failing" {
		t.Errorf("empty CIState should preserve last-known CI, got %q", sm.state.Sessions["braw"].CI.State)
	}

	// Unknown session is a no-op (no panic).
	sm.writePRState("ghost", prData{Number: 1})

	// clearPRState resets both.
	sm.clearPRState("braw")
	if sm.state.Sessions["braw"].PullRequest.Number != 0 || sm.state.Sessions["braw"].CI.State != "" {
		t.Errorf("clearPRState should reset PR and CI, got %+v / %+v",
			sm.state.Sessions["braw"].PullRequest, sm.state.Sessions["braw"].CI)
	}

	sm.clearPRState("ghost") // no-op, no panic
}

func TestReviewDecisionBody_Cov(t *testing.T) {
	tgt := prWatchTarget{branch: "canny"}

	approved := reviewDecisionBody(tgt, prData{Number: 1, ReviewDecision: "approved"})
	if !strings.Contains(approved, "approved") || !strings.Contains(approved, "No action needed") {
		t.Errorf("approved body wrong: %s", approved)
	}

	changes := reviewDecisionBody(tgt, prData{Number: 2, ReviewDecision: "changes_requested"})
	if !strings.Contains(changes, "requested changes") || !strings.Contains(changes, "You decide") {
		t.Errorf("changes_requested body wrong: %s", changes)
	}

	other := reviewDecisionBody(tgt, prData{Number: 3, ReviewDecision: "review_required"})
	if !strings.Contains(other, "review status changed") || !strings.Contains(other, "review_required") {
		t.Errorf("default review body wrong: %s", other)
	}
}

func TestTruncate_Cov(t *testing.T) {
	if got := truncate("  bide  ", 10); got != "bide" {
		t.Errorf("truncate should trim whitespace, got %q", got)
	}

	if got := truncate("abcdefgh", 4); got != "abcd…" {
		t.Errorf("truncate should cut to n and add ellipsis, got %q", got)
	}

	if got := truncate("short", 100); got != "short" {
		t.Errorf("truncate under limit should be unchanged, got %q", got)
	}
}

func TestCommentsAfter_Cov(t *testing.T) {
	comments := []ghComment{
		{ID: 3}, {ID: 1}, {ID: 5}, {ID: 2},
	}

	got := commentsAfter(comments, 2)
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 5 {
		t.Errorf("commentsAfter(>2) should return sorted [3,5], got %+v", got)
	}

	// Callers only care about the count; nil vs empty is not part of the contract.
	if got := commentsAfter(comments, 100); len(got) != 0 {
		t.Errorf("commentsAfter with a cursor past all IDs should be empty, got %+v", got)
	}
}

func TestMaxInt64_Cov(t *testing.T) {
	if got := maxInt64(3, 7); got != 7 {
		t.Errorf("maxInt64(3,7) = %d, want 7", got)
	}

	if got := maxInt64(9, 2); got != 9 {
		t.Errorf("maxInt64(9,2) = %d, want 9", got)
	}
}

func TestPRInfoAndCIInfo_Cov(t *testing.T) {
	if prInfo(PRStatus{Number: 0}) != nil {
		t.Error("prInfo with number 0 should be nil")
	}

	info := prInfo(PRStatus{Number: 5, State: "open", Mergeable: "CONFLICTING", ReviewDecision: "approved"})
	if info == nil || info.Number != 5 || !info.Conflicting || info.ReviewDecision != "approved" {
		t.Errorf("prInfo wrong: %+v", info)
	}

	nonConflict := prInfo(PRStatus{Number: 6, Mergeable: "MERGEABLE"})
	if nonConflict == nil || nonConflict.Conflicting {
		t.Errorf("MERGEABLE should not be marked conflicting: %+v", nonConflict)
	}

	if ciInfo(CIStatus{State: ""}) != nil {
		t.Error("ciInfo with empty state should be nil")
	}

	ci := ciInfo(CIStatus{State: "passing", FailingChecks: nil})
	if ci == nil || ci.State != "passing" {
		t.Errorf("ciInfo wrong: %+v", ci)
	}
}

// TestPollSession_NonGitHubBacksOff verifies pollSession negative-caches a
// worktree whose origin is not a GitHub remote.
func TestPollSession_NonGitHubBacksOff_Cov(t *testing.T) {
	bareDir, cloneDir := setupTestRepo(t)
	_ = bareDir

	// The clone's origin points at a local bare repo — not github.com — so
	// repoSlug fails and pollSession applies the no-PR negative cache.
	sm := newPRWatchCovSM()
	cfg := &config.PRWatchConfig{Enabled: true}
	tgt := prWatchTarget{id: "haar", branch: "main", worktreePath: cloneDir}

	sm.pollSession(context.Background(), cfg, tgt)

	sm.prWatch.mu.Lock()
	next, ok := sm.prWatch.nextPoll["haar"]
	sm.prWatch.mu.Unlock()

	if !ok {
		t.Fatal("non-GitHub remote should schedule a back-off poll")
	}

	if next.Before(time.Now().Add(prNoPRNegCache - time.Minute)) {
		t.Errorf("non-GitHub remote should get the long negative-cache back-off, got %v", next)
	}
}

// TestPollSession_FoundPRWritesState drives pollSession end-to-end with a mocked
// gh, against a real GitHub-style remote, and asserts it writes display state and
// schedules the next poll.
func TestPollSession_FoundPRWritesState_Cov(t *testing.T) {
	tmp := t.TempDir()
	cloneDir := tmp + "/clone"

	gitRun(t, "", "init", "--initial-branch=main", cloneDir)
	gitRun(t, cloneDir, "remote", "add", "origin", "git@github.com:croft/loch.git")

	orig := ghRunner
	defer func() { ghRunner = orig }()

	calls := 0
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		calls++
		if calls == 1 { // gh pr list
			return `[{"number":9,"state":"OPEN","isDraft":false,"url":"https://github.com/croft/loch/pull/9","headRefOid":"sha9","mergeable":"MERGEABLE"}]`, nil
		}

		return `[]`, nil // checks + comments empty
	}

	sm := newPRWatchCovSM()
	sm.state.Sessions["bonnie"] = &SessionState{ID: "bonnie"}
	cfg := &config.PRWatchConfig{Enabled: true, PollTerminal: "10m", PollPending: "1m", PollMerged: "1h"}
	tgt := prWatchTarget{id: "bonnie", branch: "bide", worktreePath: cloneDir}

	sm.pollSession(context.Background(), cfg, tgt)

	if sm.state.Sessions["bonnie"].PullRequest.Number != 9 {
		t.Errorf("pollSession should write PR #9, got %+v", sm.state.Sessions["bonnie"].PullRequest)
	}

	sm.prWatch.mu.Lock()
	_, ok := sm.prWatch.nextPoll["bonnie"]
	sm.prWatch.mu.Unlock()

	if !ok {
		t.Error("pollSession should schedule the next poll")
	}
}

// TestPollSession_NoPRClearsState covers the found=false branch: state is
// cleared and the branch is negative-cached.
func TestPollSession_NoPRClearsState_Cov(t *testing.T) {
	tmp := t.TempDir()
	cloneDir := tmp + "/clone"

	gitRun(t, "", "init", "--initial-branch=main", cloneDir)
	gitRun(t, cloneDir, "remote", "add", "origin", "https://github.com/croft/loch.git")

	orig := ghRunner
	defer func() { ghRunner = orig }()
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		return `[]`, nil // no PR
	}

	sm := newPRWatchCovSM()
	sm.state.Sessions["ken"] = &SessionState{
		ID:          "ken",
		PullRequest: PRStatus{Number: 3},
		CI:          CIStatus{State: "passing"},
	}
	cfg := &config.PRWatchConfig{Enabled: true}
	tgt := prWatchTarget{id: "ken", branch: "bide", worktreePath: cloneDir}

	before := time.Now()
	sm.pollSession(context.Background(), cfg, tgt)

	s := sm.state.Sessions["ken"]
	if s.PullRequest.Number != 0 {
		t.Errorf("no-PR poll should clear PR state, got %+v", s.PullRequest)
	}

	if s.CI.State != "" {
		t.Errorf("no-PR poll should clear CI state, got %+v", s.CI)
	}

	// The branch should be negative-cached (long back-off), not re-polled soon.
	sm.prWatch.mu.Lock()
	next, ok := sm.prWatch.nextPoll["ken"]
	sm.prWatch.mu.Unlock()

	if !ok {
		t.Fatal("no-PR poll should schedule a back-off poll")
	}

	if next.Before(before.Add(prNoPRNegCache - time.Minute)) {
		t.Errorf("no-PR poll should apply the long negative-cache back-off, got %v", next)
	}
}

func TestPRWatchTargets_Cov(t *testing.T) {
	tmp := t.TempDir()
	cloneDir := tmp + "/clone"

	gitRun(t, "", "init", "--initial-branch=main", cloneDir)
	gitRun(t, cloneDir, "remote", "add", "origin", "git@github.com:croft/loch.git")

	sm := newPRWatchCovSM()

	// Eligible: running, has repo, recorded branch.
	sm.state.Sessions["braw"] = &SessionState{
		ID: "braw", Name: "braw", Status: StatusRunning,
		RepoPath: cloneDir, WorktreePath: cloneDir, Branch: "canny-feature",
	}
	// Ineligible: no repo path.
	sm.state.Sessions["norepo"] = &SessionState{ID: "norepo", Status: StatusRunning}
	// Ineligible: in-place.
	sm.state.Sessions["inplace"] = &SessionState{ID: "inplace", Status: StatusRunning, RepoPath: cloneDir, InPlace: true}
	// Ineligible: shared worktree.
	sm.state.Sessions["shared"] = &SessionState{ID: "shared", Status: StatusRunning, RepoPath: cloneDir, SharedWorktree: true}
	// Ineligible: errored status.
	sm.state.Sessions["errored"] = &SessionState{ID: "errored", Status: StatusErrored, RepoPath: cloneDir}

	targets := sm.prWatchTargets()

	if len(targets) != 1 || targets[0].id != "braw" || targets[0].branch != "canny-feature" {
		t.Fatalf("expected only 'braw' eligible with recorded branch, got %+v", targets)
	}
}

// TestRunPRWatchLoop_CancelledCtxReturns ensures the loop exits promptly when
// its context is cancelled.
func TestRunPRWatchLoop_CancelledCtxReturns_Cov(t *testing.T) {
	sm := newPRWatchCovSM()
	sm.cfg = &config.Config{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		sm.RunPRWatchLoop(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RunPRWatchLoop did not return on cancelled context")
	}
}

// TestRunPRWatchTick_DisabledAndBatch drives runPRWatchTick against several due
// targets and asserts the per-tick batch cap bounds how many are polled.
func TestRunPRWatchTick_BatchCap_Cov(t *testing.T) {
	tmp := t.TempDir()
	cloneDir := tmp + "/clone"

	gitRun(t, "", "init", "--initial-branch=main", cloneDir)
	// Non-GitHub remote → each polled session just negative-caches (no network).
	gitRun(t, cloneDir, "remote", "add", "origin", "git@example.com:croft/loch.git")

	sm := newPRWatchCovSM()
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		sm.state.Sessions[id] = &SessionState{
			ID: id, Name: id, Status: StatusRunning,
			RepoPath: cloneDir, WorktreePath: cloneDir, Branch: "canny-feature",
		}
	}

	cfg := &config.PRWatchConfig{Enabled: true}
	sm.runPRWatchTick(context.Background(), cfg)

	// At most prWatchBatchCap sessions should have been polled (scheduled).
	sm.prWatch.mu.Lock()
	polled := len(sm.prWatch.nextPoll)
	sm.prWatch.mu.Unlock()

	if polled != prWatchBatchCap {
		t.Errorf("runPRWatchTick should poll at most %d per tick, polled %d", prWatchBatchCap, polled)
	}
}
