package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/d0ugal/graith/internal/git"
)

// putSession is a small helper that inserts a session directly into the
// manager's state without spawning a PTY, so lifecycle/query logic can be
// exercised without a real process or socket.
func putSession(sm *SessionManager, s *SessionState) {
	sm.mu.Lock()
	sm.state.Sessions[s.ID] = s
	sm.mu.Unlock()
}

// spawnContainedSleeper starts a long-lived child in its own process group and
// returns its PID. It is used as a *live* PID fixture for the kill/orphan guard
// tests: the guards under test are supposed to refuse to signal this PID, but if
// one ever regresses, the resulting kill is contained to this child's process
// group (Setpgid) instead of taking down the test runner or unrelated
// processes. Cleanup SIGKILLs the group and reaps the child.
func spawnContainedSleeper(t *testing.T) int {
	t.Helper()

	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}

	pid := cmd.Process.Pid

	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	return pid
}

func TestCovStarUnstarLifecycle(t *testing.T) {
	sm := newTestSessionManager(t)
	putSession(sm, &SessionState{ID: "braw1", Name: "braw", Status: StatusStopped})

	if err := sm.Star("braw1"); err != nil {
		t.Fatalf("Star: %v", err)
	}

	if s, _ := sm.Get("braw1"); !s.Starred {
		t.Fatal("expected session to be starred")
	}

	if err := sm.Unstar("braw1"); err != nil {
		t.Fatalf("Unstar: %v", err)
	}

	if s, _ := sm.Get("braw1"); s.Starred {
		t.Fatal("expected session to be unstarred")
	}

	if err := sm.Star("haar-missing"); err == nil {
		t.Fatal("expected error starring unknown session")
	}

	if err := sm.Unstar("haar-missing"); err == nil {
		t.Fatal("expected error unstarring unknown session")
	}

	// A session being deleted rejects both operations.
	putSession(sm, &SessionState{ID: "thrawn1", Name: "thrawn", Status: StatusDeleting})

	if err := sm.Star("thrawn1"); err == nil {
		t.Fatal("expected error starring a deleting session")
	}

	if err := sm.Unstar("thrawn1"); err == nil {
		t.Fatal("expected error unstarring a deleting session")
	}
}

func TestCovStarBlocksDelete(t *testing.T) {
	sm := newTestSessionManager(t)

	// A no-repo scratch session whose worktree is a real temp dir so Delete
	// can exercise the os.RemoveAll teardown branch.
	dir := filepath.Join(t.TempDir(), "bothy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	putSession(sm, &SessionState{ID: "canny1", Name: "canny", Status: StatusStopped, WorktreePath: dir})

	if err := sm.Star("canny1"); err != nil {
		t.Fatalf("Star: %v", err)
	}

	if err := sm.Delete("canny1"); err == nil {
		t.Fatal("expected starred session to reject Delete")
	}

	if err := sm.Unstar("canny1"); err != nil {
		t.Fatalf("Unstar: %v", err)
	}

	if err := sm.Delete("canny1"); err != nil {
		t.Fatalf("Delete after unstar: %v", err)
	}

	if _, ok := sm.Get("canny1"); ok {
		t.Fatal("expected session removed after Delete")
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir removed, stat err = %v", err)
	}
}

func TestCovFleetSummaryCounts(t *testing.T) {
	sm := newTestSessionManager(t)

	putSession(sm, &SessionState{ID: "s1", Status: StatusRunning, AgentStatus: "active"})
	putSession(sm, &SessionState{ID: "s2", Status: StatusRunning, AgentStatus: "approval"})
	putSession(sm, &SessionState{ID: "s3", Status: StatusRunning, AgentStatus: "ready"})
	putSession(sm, &SessionState{ID: "s4", Status: StatusRunning, AgentStatus: ""}) // default -> active
	putSession(sm, &SessionState{ID: "s5", Status: StatusCreating})                 // counts as active
	putSession(sm, &SessionState{ID: "s6", Status: StatusStopped})
	putSession(sm, &SessionState{ID: "s7", Status: StatusErrored})

	f := sm.fleetSummary()

	if f.Total != 7 {
		t.Errorf("Total = %d, want 7", f.Total)
	}

	if f.Active != 3 {
		t.Errorf("Active = %d, want 3 (active + default-running + creating)", f.Active)
	}

	if f.Approval != 1 {
		t.Errorf("Approval = %d, want 1", f.Approval)
	}

	if f.Ready != 1 {
		t.Errorf("Ready = %d, want 1", f.Ready)
	}

	if f.Stopped != 1 {
		t.Errorf("Stopped = %d, want 1", f.Stopped)
	}

	if f.Errored != 1 {
		t.Errorf("Errored = %d, want 1", f.Errored)
	}
}

func TestCovDiagnosticsReportsSessions(t *testing.T) {
	sm := newTestSessionManager(t)
	wt := t.TempDir()
	putSession(sm, &SessionState{ID: "ken1", Name: "ken", Status: StatusRunning, WorktreePath: wt, Token: "tok"})
	putSession(sm, &SessionState{ID: "ken2", Name: "ken-two", Status: StatusStopped})

	d := sm.Diagnostics()

	if d.Fleet.Total != 2 {
		t.Errorf("Fleet.Total = %d, want 2", d.Fleet.Total)
	}

	if len(d.Sessions) != 2 {
		t.Fatalf("len(Sessions) = %d, want 2", len(d.Sessions))
	}

	if d.DaemonPID != os.Getpid() {
		t.Errorf("DaemonPID = %d, want %d", d.DaemonPID, os.Getpid())
	}

	var found bool

	for _, s := range d.Sessions {
		if s.ID == "ken1" {
			found = true

			if !s.WorktreeExists {
				t.Error("expected WorktreeExists true for existing worktree")
			}

			if !s.HasToken {
				t.Error("expected HasToken true")
			}
		}
	}

	if !found {
		t.Fatal("session ken1 not present in diagnostics")
	}
}

func TestCovKillVerifiedProcess(t *testing.T) {
	sm := newTestSessionManager(t)

	t.Run("non-positive pid", func(t *testing.T) {
		killed, err := sm.killVerifiedProcess(0, 0)
		if killed || err != nil {
			t.Fatalf("got (killed=%v, err=%v), want (false, nil)", killed, err)
		}
	})

	t.Run("dead pid", func(t *testing.T) {
		// A pid that is almost certainly not alive.
		killed, err := sm.killVerifiedProcess(1<<30, 12345)
		if killed || err != nil {
			t.Fatalf("got (killed=%v, err=%v), want (false, nil)", killed, err)
		}
	})

	t.Run("live pid without recorded start time errors", func(t *testing.T) {
		// A live child in its own process group; startTime 0 means identity was
		// never recorded, so the kill is refused rather than risk killing the
		// wrong process. Using a contained child (not os.Getpid) keeps a guard
		// regression from ever terminating the test runner.
		pid := spawnContainedSleeper(t)

		_, err := sm.killVerifiedProcess(pid, 0)
		if err == nil {
			t.Fatal("expected error for live pid with no recorded start time")
		}

		if !isProcessAlive(pid) {
			t.Fatal("sleeper was killed; kill guard should have refused")
		}
	})

	t.Run("live pid with mismatched start time errors", func(t *testing.T) {
		// startTime=1 will not match the real start time, so the identity
		// check fails and no signal is sent.
		pid := spawnContainedSleeper(t)

		_, err := sm.killVerifiedProcess(pid, 1)
		if err == nil {
			t.Fatal("expected identity-mismatch error")
		}

		if !isProcessAlive(pid) {
			t.Fatal("sleeper was killed; identity check should have refused")
		}
	})
}

func TestCovStopWithChildren(t *testing.T) {
	sm := newTestSessionManager(t)

	if _, err := sm.StopWithChildren("haar-missing", false); err == nil {
		t.Fatal("expected error for unknown root")
	}

	// ben (root) -> bairn (running orphan, pid 0) and canny (starred, skipped).
	putSession(sm, &SessionState{ID: "ben1", Name: "ben", Status: StatusRunning})
	putSession(sm, &SessionState{ID: "bairn1", Name: "bairn", ParentID: "ben1", Status: StatusRunning})
	putSession(sm, &SessionState{ID: "canny1", Name: "canny", ParentID: "ben1", Status: StatusRunning, Starred: true})

	stopped, err := sm.StopWithChildren("ben1", false)
	if err != nil {
		t.Fatalf("StopWithChildren: %v", err)
	}

	got := map[string]bool{}
	for _, id := range stopped {
		got[id] = true
	}

	if !got["ben1"] || !got["bairn1"] {
		t.Errorf("expected ben1 and bairn1 stopped, got %v", stopped)
	}

	if got["canny1"] {
		t.Error("starred session canny1 should have been skipped")
	}

	if s, _ := sm.Get("canny1"); s.Status != StatusRunning {
		t.Errorf("starred session status = %q, want running (untouched)", s.Status)
	}

	if s, _ := sm.Get("bairn1"); s.Status != StatusStopped {
		t.Errorf("bairn1 status = %q, want stopped", s.Status)
	}
}

func TestCovStopWithReasonOrphanAndErrors(t *testing.T) {
	sm := newTestSessionManager(t)

	if err := sm.Stop("haar-missing"); err == nil {
		t.Fatal("expected error stopping unknown session")
	}

	putSession(sm, &SessionState{ID: "dreich1", Name: "dreich", Status: StatusStopped})
	if err := sm.Stop("dreich1"); err == nil {
		t.Fatal("expected error stopping a non-running session")
	}

	// Running with no PTY and pid 0: the orphan path treats it as already exited.
	putSession(sm, &SessionState{ID: "bide1", Name: "bide", Status: StatusRunning})
	if err := sm.Stop("bide1"); err != nil {
		t.Fatalf("Stop orphan: %v", err)
	}

	if s, _ := sm.Get("bide1"); s.Status != StatusStopped {
		t.Errorf("status = %q, want stopped", s.Status)
	}
}

func TestCovCleanupOrphanedProcesses(t *testing.T) {
	sm := newTestSessionManager(t)

	// A live child (in its own process group) with no recorded identity: cannot
	// be verified, so it is marked errored rather than killed. A contained child
	// rather than os.Getpid means a regression that killed here can't take down
	// the test runner.
	sleeper := spawnContainedSleeper(t)
	putSession(sm, &SessionState{ID: "scunner1", Name: "scunner", Status: StatusRunning, PID: sleeper, PIDStartTime: 0})
	// Dead pid: not a candidate, left untouched.
	putSession(sm, &SessionState{ID: "whin1", Name: "whin", Status: StatusRunning, PID: 1 << 30})
	// Not running: ignored.
	putSession(sm, &SessionState{ID: "neep1", Name: "neep", Status: StatusStopped, PID: sleeper})

	sm.cleanupOrphanedProcesses()

	if !isProcessAlive(sleeper) {
		t.Fatal("sleeper was killed; unverifiable orphan must not be signalled")
	}

	if s, _ := sm.Get("scunner1"); s.Status != StatusErrored {
		t.Errorf("scunner1 status = %q, want errored (unverifiable orphan)", s.Status)
	}

	if s, _ := sm.Get("whin1"); s.Status != StatusRunning {
		t.Errorf("whin1 status = %q, want running (dead pid, not a candidate)", s.Status)
	}

	if s, _ := sm.Get("neep1"); s.Status != StatusStopped {
		t.Errorf("neep1 status = %q, want stopped (not running)", s.Status)
	}
}

func TestCovExpandPaths(t *testing.T) {
	sm := newTestSessionManager(t)

	if got := expandPaths(nil, sm.log, "read"); got != nil {
		t.Errorf("expandPaths(nil) = %v, want nil", got)
	}

	existing := t.TempDir()
	missing := filepath.Join(existing, "no-such-glen")

	got := expandPaths([]string{existing, missing}, sm.log, "read")
	if len(got) != 1 || got[0] != existing {
		t.Errorf("expandPaths = %v, want [%q]", got, existing)
	}
}

func TestCovAgentBinaryDir(t *testing.T) {
	if got := agentBinaryDir(""); got != "" {
		t.Errorf("agentBinaryDir(\"\") = %q, want empty", got)
	}

	// A path with a separator returns its directory verbatim.
	if got := agentBinaryDir("/opt/bin/claude"); got != "/opt/bin" {
		t.Errorf("agentBinaryDir(/opt/bin/claude) = %q, want /opt/bin", got)
	}

	// A bare command name is resolved via PATH.
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}

	if got := agentBinaryDir("sh"); got != filepath.Dir(shPath) {
		t.Errorf("agentBinaryDir(sh) = %q, want %q", got, filepath.Dir(shPath))
	}

	// An unresolvable bare command yields empty.
	if got := agentBinaryDir("definitely-not-a-real-binary-xyzzy"); got != "" {
		t.Errorf("agentBinaryDir(unknown) = %q, want empty", got)
	}
}

func TestCovDeriveSandboxIncludesWriteDirsNonGit(t *testing.T) {
	sm := newTestSessionManager(t)

	// A non-git worktree path: the git-dir lookup fails, so only the worktree
	// path itself is returned (with a warning).
	nonGit := t.TempDir()
	dirs := sm.deriveSandboxIncludesWriteDirs([]IncludedRepoState{
		{RepoName: "croft", WorktreePath: nonGit},
	})

	if len(dirs) != 1 || dirs[0] != nonGit {
		t.Errorf("deriveSandboxIncludesWriteDirs = %v, want [%q]", dirs, nonGit)
	}
}

func TestCovDeriveSandboxIncludesWriteDirsGit(t *testing.T) {
	sm := newTestSessionManager(t)
	_, clone := setupTestRepo(t)

	worktree := filepath.Join(t.TempDir(), "bothy")
	if err := git.SetupSession(context.Background(), clone, worktree, "graith/skelf-1", "main", false); err != nil {
		t.Fatalf("SetupSession: %v", err)
	}

	gitDir, commonDir, err := git.WorktreeGitDirs(worktree)
	if err != nil {
		t.Fatalf("WorktreeGitDirs: %v", err)
	}

	if gitDir == "" || commonDir == "" {
		t.Fatalf("expected non-empty git dirs, got gitDir=%q commonDir=%q", gitDir, commonDir)
	}

	dirs := sm.deriveSandboxIncludesWriteDirs([]IncludedRepoState{
		{RepoName: "croft", WorktreePath: worktree},
	})

	// Exact, ordered slice: worktree path plus its resolved git dir and common
	// dir — so a regression that appended empty or duplicate strings is caught.
	want := []string{worktree, gitDir, commonDir}
	if len(dirs) != len(want) {
		t.Fatalf("deriveSandboxIncludesWriteDirs = %v, want %v", dirs, want)
	}

	for i := range want {
		if dirs[i] != want[i] {
			t.Errorf("dirs[%d] = %q, want %q", i, dirs[i], want[i])
		}
	}
}

func TestCovTeardownIncludes(t *testing.T) {
	sm := newTestSessionManager(t)
	_, clone := setupTestRepo(t)

	sessionDir := filepath.Join(t.TempDir(), "session")
	mainWorktree := filepath.Join(sessionDir, "croft")

	if err := git.SetupSession(context.Background(), clone, mainWorktree, "graith/skelf-main", "main", false); err != nil {
		t.Fatalf("SetupSession main: %v", err)
	}

	incWorktree := filepath.Join(sessionDir, "bairn")
	if err := git.SetupSession(context.Background(), clone, incWorktree, "graith/skelf-inc", "main", false); err != nil {
		t.Fatalf("SetupSession include: %v", err)
	}

	includes := []IncludedRepoState{
		{RepoPath: clone, RepoName: "bairn", WorktreePath: incWorktree, Branch: "graith/skelf-inc"},
	}

	// Sanity: both branches and worktrees exist before teardown, so the
	// post-conditions below prove teardown actually removed them (not that they
	// were never created).
	if !git.RefExists(clone, "graith/skelf-main") || !git.RefExists(clone, "graith/skelf-inc") {
		t.Fatal("expected both branches to exist before teardown")
	}

	if !git.IsRegisteredWorktree(clone, mainWorktree) || !git.IsRegisteredWorktree(clone, incWorktree) {
		t.Fatal("expected both worktrees registered before teardown")
	}

	if err := sm.teardownIncludes(clone, mainWorktree, "graith/skelf-main", includes); err != nil {
		t.Fatalf("teardownIncludes: %v", err)
	}

	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Errorf("expected session dir removed, stat err = %v", err)
	}

	// The branches and worktree registrations in the source repo must also be
	// gone — removing only the session dir would leave git metadata behind.
	if git.RefExists(clone, "graith/skelf-main") {
		t.Error("main branch still exists after teardown")
	}

	if git.RefExists(clone, "graith/skelf-inc") {
		t.Error("include branch still exists after teardown")
	}

	if git.IsRegisteredWorktree(clone, mainWorktree) {
		t.Error("main worktree still registered after teardown")
	}

	if git.IsRegisteredWorktree(clone, incWorktree) {
		t.Error("include worktree still registered after teardown")
	}
}

func TestCovSetStores(t *testing.T) {
	sm := newTestSessionManager(t)

	// Assign non-nil sentinels and assert the pointers round-trip, so a no-op
	// setter would fail (the fields default to nil after NewSessionManager).
	ms := &MsgStore{}
	mm := &MCPManager{}

	sm.SetMsgStore(ms)
	sm.SetMCPManager(mm)

	if sm.messages != ms {
		t.Error("SetMsgStore did not assign the given pointer")
	}

	if sm.mcpManager != mm {
		t.Error("SetMCPManager did not assign the given pointer")
	}
}
