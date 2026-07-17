package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// waitForKick blocks until a session ID lands on the PR-watch kick channel or the
// timeout elapses. Returns the kicked ID and whether one arrived.
func waitForKick(t *testing.T, sm *SessionManager, timeout time.Duration) (string, bool) {
	t.Helper()

	select {
	case id := <-sm.prWatch.kick:
		return id, true
	case <-time.After(timeout):
		return "", false
	}
}

// TestGitRefWatchDirs_Plain resolves the ref directories for an ordinary repo:
// the gitdir top, its logs, the refs subtree, and the reflog subtree — never the
// object store.
func TestGitRefWatchDirs_Plain(t *testing.T) {
	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	dirs := gitRefWatchDirs(repo)
	if len(dirs) == 0 {
		t.Fatal("expected at least one ref dir for a real repo")
	}

	var haveRefs, haveObjects bool

	for _, d := range dirs {
		if strings.Contains(d, string(filepath.Separator)+"refs") {
			haveRefs = true
		}

		if strings.Contains(d, string(filepath.Separator)+"objects") {
			haveObjects = true
		}
	}

	if !haveRefs {
		t.Errorf("expected a refs/ dir in the watch set, got %v", dirs)
	}

	if haveObjects {
		t.Errorf("object store must never be watched, got %v", dirs)
	}
}

// TestGitRefWatchDirs_LinkedWorktree covers the linked-worktree case: HEAD lives
// in the per-worktree gitdir while refs live in the shared common dir, so the
// watch set must span both.
func TestGitRefWatchDirs_LinkedWorktree(t *testing.T) {
	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	wt := t.TempDir() + "/bothy"
	gitRun(t, repo, "worktree", "add", wt, "-b", "bide")

	dirs := gitRefWatchDirs(wt)
	if len(dirs) == 0 {
		t.Fatal("expected ref dirs for a linked worktree")
	}

	// The common dir's refs subtree (shared with the main repo) must be present.
	// gitRefWatchDirs derives paths from `git rev-parse`, which resolves symlinks
	// (on macOS /var/folders → /private/var/folders), so compare on the
	// symlink-resolved repo path rather than the raw t.TempDir() path.
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		resolvedRepo = repo
	}

	commonRefs := filepath.Join(resolvedRepo, ".git", "refs")

	var haveCommonRefs bool

	for _, d := range dirs {
		if d == commonRefs {
			haveCommonRefs = true
		}
	}

	if !haveCommonRefs {
		t.Errorf("expected common refs dir %q in watch set, got %v", commonRefs, dirs)
	}
}

// TestGitRefWatchDirs_FailOpen: a non-git path resolves to no dirs (no panic),
// so createPRRefWatcher fails open to the poll fallback.
func TestGitRefWatchDirs_FailOpen(t *testing.T) {
	if dirs := gitRefWatchDirs(""); dirs != nil {
		t.Errorf("empty worktree should give nil dirs, got %v", dirs)
	}

	if dirs := gitRefWatchDirs(t.TempDir()); dirs != nil {
		t.Errorf("non-git dir should give nil dirs, got %v", dirs)
	}
}

// TestPRRefWatch_CommitKicks is the core behaviour test: a commit in a watched
// worktree delivers a kick for that session within the debounce window.
func TestPRRefWatch_CommitKicks(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm.createPRRefWatcher(ctx, "braw1", repo)

	sm.prRefWatch.mu.Lock()
	_, watching := sm.prRefWatch.watchers["braw1"]
	sm.prRefWatch.mu.Unlock()

	if !watching {
		t.Fatal("expected a ref watcher for the session")
	}

	// A commit writes refs/heads/main + the reflog — both watched.
	gitRun(t, repo, "commit", "--allow-empty", "-m", "second")

	id, ok := waitForKick(t, sm, 5*time.Second)
	if !ok {
		t.Fatal("expected a kick after a commit, got none")
	}

	if id != "braw1" {
		t.Errorf("kick session = %q, want braw1", id)
	}
}

// TestReconcilePRRefWatchers_CreateAndTeardown drives the reconcile: a running
// eligible session gets a watcher; once it stops it is torn down.
func TestReconcilePRRefWatchers_CreateAndTeardown(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", Name: "braw", RepoPath: repo, WorktreePath: repo,
		Branch: "main", Status: StatusRunning,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm.reconcilePRRefWatchers(ctx)

	sm.prRefWatch.mu.Lock()
	_, created := sm.prRefWatch.watchers["braw1"]
	sm.prRefWatch.mu.Unlock()

	if !created {
		t.Fatal("reconcile should create a watcher for a running eligible session")
	}

	// Stop the session: it is no longer eligible and must be torn down.
	sm.state.Sessions["braw1"].Status = StatusStopped
	sm.reconcilePRRefWatchers(ctx)

	sm.prRefWatch.mu.Lock()
	_, stillThere := sm.prRefWatch.watchers["braw1"]
	sm.prRefWatch.mu.Unlock()

	if stillThere {
		t.Error("reconcile should tear down the watcher once the session stops")
	}
}

// TestPRRefEligibleSessions_Excludes covers the eligibility filter: only running,
// non-soft-deleted, worktree-backed, non-mirror, non-in-place sessions qualify.
func TestPRRefEligibleSessions_Excludes(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions = map[string]*SessionState{
		"braw":   {ID: "braw", RepoPath: "/r", WorktreePath: "/w", Status: StatusRunning},
		"dreich": {ID: "dreich", RepoPath: "/r", WorktreePath: "/w", Status: StatusStopped},
		"fash":   {ID: "fash", RepoPath: "/r", WorktreePath: "/w", Status: StatusRunning, Mirror: true},
		"haar":   {ID: "haar", RepoPath: "/r", WorktreePath: "/w", Status: StatusRunning, InPlace: true},
		"scunner": {
			ID: "scunner", RepoPath: "/r", WorktreePath: "", Status: StatusRunning,
		},
	}

	got := sm.prRefEligibleSessions()
	if len(got) != 1 {
		t.Fatalf("expected only the running plain session, got %v", got)
	}

	if _, ok := got["braw"]; !ok {
		t.Errorf("expected 'braw' eligible, got %v", got)
	}
}

// TestAllowKick_Cooldown: a second kick within the kick cooldown is suppressed.
func TestAllowKick_Cooldown(t *testing.T) {
	sm := newTestSessionManager(t)

	if !sm.allowKick(&config.PRWatchConfig{}, "braw1") {
		t.Fatal("first kick should be allowed")
	}

	if sm.allowKick(&config.PRWatchConfig{}, "braw1") {
		t.Error("second kick within cooldown should be suppressed")
	}

	// A different session is independent.
	if !sm.allowKick(&config.PRWatchConfig{}, "canny2") {
		t.Error("a different session's first kick should be allowed")
	}
}

// TestPRWatchTarget resolves a single eligible session and rejects ineligible or
// missing ones, mirroring prWatchTargets.
func TestPRWatchTarget(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", Name: "braw", RepoPath: repo, WorktreePath: repo,
		Branch: "main", Status: StatusRunning,
	}

	tgt, ok := sm.prWatchTarget("braw1")
	if !ok {
		t.Fatal("expected an eligible target")
	}

	if tgt.branch != "main" || tgt.name != "braw" {
		t.Errorf("unexpected target %+v", tgt)
	}

	if _, ok := sm.prWatchTarget("missing"); ok {
		t.Error("missing session should not resolve")
	}

	now := time.Now()
	sm.state.Sessions["braw1"].DeletedAt = &now

	if _, ok := sm.prWatchTarget("braw1"); ok {
		t.Error("soft-deleted session should not resolve")
	}
}

// TestPollKicked_DrivesPollAndCooldown proves a kick runs a real poll through the
// unchanged pollSession path (writing PR display state), and that the cooldown
// suppresses an immediate second kick.
func TestPollKicked_DrivesPollAndCooldown(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	calls := 0
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		calls++

		if args[0] == "pr" && args[1] == "list" {
			return `[{"number":42,"state":"OPEN","isDraft":false,` +
				`"url":"https://github.com/croft/loch/pull/42","headRefOid":"sha1","mergeable":"MERGEABLE"}]`, nil
		}

		return `[]`, nil // checks + comments
	}

	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)
	gitRun(t, repo, "remote", "add", "origin", "git@github.com:croft/loch.git")

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", Name: "braw", RepoPath: repo, WorktreePath: repo,
		Branch: "main", Status: StatusRunning,
	}

	cfg := config.Default()

	sm.pollKicked(context.Background(), &cfg.PRWatch, "braw1")

	if pr := sm.state.Sessions["braw1"].PullRequest; pr.Number != 42 {
		t.Fatalf("expected PR #42 written by the kicked poll, got %+v", pr)
	}

	// An immediate second kick is suppressed by the cooldown — no extra gh calls.
	before := calls

	sm.pollKicked(context.Background(), &cfg.PRWatch, "braw1")

	if calls != before {
		t.Errorf("second kick within cooldown should not poll gh (calls %d -> %d)", before, calls)
	}
}

// TestTeardownAllPRRefWatchers clears the whole set (used on shutdown / feature
// toggle-off).
func TestTeardownAllPRRefWatchers(t *testing.T) {
	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm.createPRRefWatcher(ctx, "braw1", repo)
	sm.teardownAllPRRefWatchers()

	sm.prRefWatch.mu.Lock()
	n := len(sm.prRefWatch.watchers)
	sm.prRefWatch.mu.Unlock()

	if n != 0 {
		t.Errorf("expected all watchers torn down, got %d", n)
	}
}

// TestCreatePRRefWatcher_FailOpenNonGit: a non-git worktree yields no watcher and
// no panic — the poll fallback covers it.
func TestCreatePRRefWatcher_FailOpenNonGit(t *testing.T) {
	sm := newTestSessionManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm.createPRRefWatcher(ctx, "dreich", t.TempDir())

	sm.prRefWatch.mu.Lock()
	_, ok := sm.prRefWatch.watchers["dreich"]
	sm.prRefWatch.mu.Unlock()

	if ok {
		t.Error("no watcher should be created for a non-git worktree")
	}
}

// TestPRRefWatch_PushKicks proves the push path (a `refs/remotes/origin/*` write
// under the common dir) delivers a kick — the case that actually matters for
// detecting a PR pushed to a new branch, and the one the debounce/nested-dir
// handling has to get right.
func TestPRRefWatch_PushKicks(t *testing.T) {
	sm := newTestSessionManager(t)

	remote := t.TempDir() + "/remote.git"
	gitRun(t, "", "init", "--bare", remote)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)
	gitRun(t, repo, "remote", "add", "origin", remote)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm.createPRRefWatcher(ctx, "braw1", repo)

	// The push writes refs/remotes/origin/main (+ its reflog) in the common dir.
	gitRun(t, repo, "push", "-u", "origin", "main")

	if id, ok := waitForKick(t, sm, 5*time.Second); !ok || id != "braw1" {
		t.Fatalf("expected a kick after a push, got id=%q ok=%v", id, ok)
	}
}

// TestNotePRRefChange_DebounceCoalesces: several ref writes in quick succession
// coalesce into exactly one kick (the debounce window).
func TestNotePRRefChange_DebounceCoalesces(t *testing.T) {
	sm := newPRWatchSM()
	w := &prRefWatcher{sessionID: "braw1"}

	// Five rapid changes — each resets the debounce, so only the last fires.
	for range 5 {
		sm.notePRRefChange(w)
	}

	if id, ok := waitForKick(t, sm, 2*time.Second); !ok || id != "braw1" {
		t.Fatalf("expected one coalesced kick, got id=%q ok=%v", id, ok)
	}

	// No second kick should follow.
	select {
	case extra := <-sm.prWatch.kick:
		t.Errorf("expected exactly one kick, got a second for %q", extra)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestPRRefWatch_ReloadDebounceAppliesToExistingWatcher is the regression for
// issue #1308: reloading ref_debounce must affect a watcher that already exists,
// without replacing it or dropping a ref change whose timer is already armed.
func TestPRRefWatch_ReloadDebounceAppliesToExistingWatcher(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[pr_watch.advanced]\nref_debounce = \"500ms\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadOrDefault(cfgPath)
	if err != nil {
		t.Fatalf("load initial config: %v", err)
	}

	sm := newSMWithConfig(t, cfg)
	sm.configFile = cfgPath

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm.createPRRefWatcher(ctx, "braw1", repo)
	defer sm.teardownPRRefWatcher("braw1")

	sm.prRefWatch.mu.Lock()
	existing := sm.prRefWatch.watchers["braw1"]
	sm.prRefWatch.mu.Unlock()
	if existing == nil {
		t.Fatal("expected an existing ref watcher")
	}

	// Arm a ref change before reload. Reload must leave this pending event intact.
	sm.notePRRefChange(existing)

	if err := os.WriteFile(cfgPath, []byte("[pr_watch.advanced]\nref_debounce = \"10ms\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sm.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}

	sm.prRefWatch.mu.Lock()
	afterReload := sm.prRefWatch.watchers["braw1"]
	sm.prRefWatch.mu.Unlock()
	if afterReload != existing {
		t.Fatal("reload should retain the existing ref watcher")
	}

	if id, ok := waitForKick(t, sm, 2*time.Second); !ok || id != "braw1" {
		t.Fatalf("pending ref change was dropped during reload: id=%q ok=%v", id, ok)
	}

	// A ref change armed after reload must use the new 10ms duration, not the
	// 500ms duration captured when the watcher was created.
	sm.notePRRefChange(existing)
	if id, ok := waitForKick(t, sm, 200*time.Millisecond); !ok || id != "braw1" {
		t.Fatalf("reloaded debounce did not affect existing watcher: id=%q ok=%v", id, ok)
	}
}

// TestNotePRRefChange_CanceledSuppressesKick: a timer that fires after the
// watcher is canceled must not deliver a kick (the fire-time canceled guard).
func TestNotePRRefChange_CanceledSuppressesKick(t *testing.T) {
	sm := newPRWatchSM()
	w := &prRefWatcher{sessionID: "dreich"}

	sm.notePRRefChange(w) // arm the debounce

	// Cancel before it fires (simulating teardown racing the timer).
	w.bmu.Lock()
	w.canceled = true
	w.bmu.Unlock()

	select {
	case id := <-sm.prWatch.kick:
		t.Errorf("canceled watcher should not kick, got %q", id)
	case <-time.After((config.PRWatchConfig{}).RefDebounceDuration() + 500*time.Millisecond):
	}
}

// TestKickPRWatch_DropClearsNextPoll: when the kick channel is saturated, the
// dropped kick clears the session's nextPoll so the next tick re-polls it rather
// than leaving it parked on a long negative cache.
func TestKickPRWatch_DropClearsNextPoll(t *testing.T) {
	sm := newPRWatchSM()

	// Saturate the kick channel.
	for range (config.PRWatchConfig{}).KickChannelSize() {
		sm.prWatch.kick <- "filler"
	}

	// Park the session far in the future (as the no-PR negative cache would).
	sm.prWatch.mu.Lock()
	sm.prWatch.nextPoll["braw1"] = time.Now().Add(time.Hour)
	sm.prWatch.mu.Unlock()

	sm.kickPRWatch("braw1") // channel full → drop path

	sm.prWatch.mu.Lock()
	_, parked := sm.prWatch.nextPoll["braw1"]
	sm.prWatch.mu.Unlock()

	if parked {
		t.Error("a dropped kick should clear nextPoll so the next tick re-polls")
	}
}

// TestPollSession_KickedNoPRShortBackoff: a kicked poll that finds no PR uses the
// short backoff (so the just-created PR is caught promptly), whereas a
// timer-driven miss parks the session on the full negative cache.
func TestPollSession_KickedNoPRShortBackoff(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		return `[]`, nil // no PR for the branch
	}

	sm := newTestSessionManager(t)

	repo := t.TempDir() + "/croft"
	initGitRepoOnBranch(t, repo)
	gitRun(t, repo, "remote", "add", "origin", "git@github.com:croft/loch.git")

	cfg := config.Default()
	tgt := prWatchTarget{id: "braw1", name: "braw", branch: "main", worktreePath: repo}

	// Kicked miss → short backoff.
	sm.pollSession(context.Background(), &cfg.PRWatch, tgt, true)

	sm.prWatch.mu.Lock()
	kickedNext := sm.prWatch.nextPoll["braw1"]
	sm.prWatch.mu.Unlock()

	if until := time.Until(kickedNext); until > time.Minute {
		t.Errorf("kicked no-PR miss should use a short backoff, got %s", until)
	}

	// Timer-driven miss → full negative cache.
	sm.pollSession(context.Background(), &cfg.PRWatch, tgt, false)

	sm.prWatch.mu.Lock()
	tickNext := sm.prWatch.nextPoll["braw1"]
	sm.prWatch.mu.Unlock()

	if until := time.Until(tickNext); until <= time.Minute {
		t.Errorf("timer-driven no-PR miss should use the full negative cache, got %s", until)
	}
}
