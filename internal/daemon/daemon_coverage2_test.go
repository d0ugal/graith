package daemon

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
)

// sleeperCfg returns a SessionManager backed by a temp dir whose only agent is a
// harmless `sleep` process, so lifecycle paths that spawn a real PTY can be
// exercised without a real agent binary.
func sleeperSM(t *testing.T) *SessionManager {
	t.Helper()
	tmp := t.TempDir()

	cfg := config.Default()
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"300"}}

	return NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(tmp, "state.json"),
		DataDir:   tmp,
		LogDir:    tmp,
	}, slog.Default())
}

// spawnReapableSleeper starts a sleeper in its own process group and reaps it in
// the background, so a SIGTERM/SIGKILL delivered by the code under test clears
// the zombie promptly (letting killProcessGroup's poll loop observe ESRCH and
// return quickly instead of waiting out its 5s SIGKILL deadline).
func spawnReapableSleeper(t *testing.T) int {
	t.Helper()

	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}

	pid := cmd.Process.Pid
	done := make(chan struct{})

	go func() {
		_ = cmd.Wait()

		close(done)
	}()

	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)

		<-done
	})

	return pid
}

// TestLoadStateRoundTripCov2 covers LoadState: reading persisted state back,
// reconciling it, and rebuilding the token index — plus the error path when the
// state file is corrupt.
func TestLoadStateRoundTripCov2(t *testing.T) {
	sm := sleeperSM(t)

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", Name: "braw", Agent: "sleeper",
		Status: StatusStopped, Token: "tok-braw",
	}
	if err := sm.saveState(); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	loaded := NewSessionManager(sm.cfg, sm.paths, slog.Default())
	if err := loaded.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if _, ok := loaded.state.Sessions["braw1"]; !ok {
		t.Fatal("session braw1 not loaded from state file")
	}

	loaded.mu.RLock()
	owner := loaded.SessionForToken("tok-braw")
	loaded.mu.RUnlock()

	if owner != "braw1" {
		t.Errorf("token index owner = %q, want %q", owner, "braw1")
	}

	// A corrupt state file is recovered by starting fresh (no error), so a bad
	// file on disk can never wedge daemon startup.
	if err := os.WriteFile(sm.paths.StateFile, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	recovered := NewSessionManager(sm.cfg, sm.paths, slog.Default())
	if err := recovered.LoadState(); err != nil {
		t.Fatalf("LoadState on corrupt file should recover, got error: %v", err)
	}

	if len(recovered.state.Sessions) != 0 {
		t.Errorf("recovered session count = %d, want 0 (fresh start)", len(recovered.state.Sessions))
	}
}

// TestKillProcessGroupCov2 covers killProcessGroup across its three branches: a
// refused low PID, an already-dead group (ESRCH → nil), and a live group that
// terminates on SIGTERM.
func TestKillProcessGroupCov2(t *testing.T) {
	t.Run("refuses pid <= 1", func(t *testing.T) {
		for _, pid := range []int{-3, 0, 1} {
			if err := killProcessGroup(pid); err == nil {
				t.Errorf("killProcessGroup(%d) = nil, want refusal error", pid)
			}
		}
	})

	t.Run("dead group returns nil", func(t *testing.T) {
		if err := killProcessGroup(1 << 30); err != nil {
			t.Errorf("killProcessGroup(dead) = %v, want nil", err)
		}
	})

	t.Run("terminates live group", func(t *testing.T) {
		pid := spawnReapableSleeper(t)

		if err := killProcessGroup(pid); err != nil {
			t.Fatalf("killProcessGroup(live) = %v, want nil", err)
		}

		// killProcessGroup only returns once the group is gone.
		if isProcessAlive(pid) {
			t.Error("process still alive after killProcessGroup returned")
		}
	})
}

// TestKillVerifiedProcessSuccessCov2 covers the success branch of
// killVerifiedProcess (recorded identity matches → process group killed),
// complementing the round-1 tests that only exercised the refusal branches.
func TestKillVerifiedProcessSuccessCov2(t *testing.T) {
	sm := sleeperSM(t)
	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported on this platform: %v", err)
	}

	killed, err := sm.killVerifiedProcess(pid, start)
	if err != nil || !killed {
		t.Fatalf("killVerifiedProcess = (killed=%v, err=%v), want (true, nil)", killed, err)
	}

	if isProcessAlive(pid) {
		t.Error("process still alive after a verified kill")
	}
}

// TestCleanupOrphanedProcessesVerifiedKillCov2 covers the verified-kill branch of
// cleanupOrphanedProcesses: a running session whose PID is alive, whose identity
// matches the recorded start time, and which has no live PTY, is treated as an
// orphan and killed with a crash stop reason. Round 1 only covered the
// unverifiable (start time 0) branch.
func TestCleanupOrphanedProcessesVerifiedKillCov2(t *testing.T) {
	sm := sleeperSM(t)
	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported on this platform: %v", err)
	}

	putSession(sm, &SessionState{
		ID: "thrawn1", Name: "thrawn", Agent: "sleeper",
		Status: StatusRunning, PID: pid, PIDStartTime: start,
	})

	sm.cleanupOrphanedProcesses()

	s, _ := sm.Get("thrawn1")
	if s.Status != StatusStopped {
		t.Errorf("status = %q, want stopped (verified orphan killed)", s.Status)
	}

	if s.StopReason != StopReasonCrash {
		t.Errorf("stop reason = %q, want %q", s.StopReason, StopReasonCrash)
	}

	if s.PID != 0 {
		t.Errorf("PID = %d, want cleared to 0", s.PID)
	}
}

// TestAdoptSessionsCov2 covers AdoptSessions handling both a manifest entry for a
// session it doesn't know about (warn + skip) and one it knows about but cannot
// re-attach to (adoption fails → session marked stopped).
func TestAdoptSessionsCov2(t *testing.T) {
	sm := sleeperSM(t)

	sm.state.Sessions["bide1"] = &SessionState{
		ID: "bide1", Name: "bide", Agent: "sleeper", Status: StatusRunning,
	}

	manifest := &UpgradeManifest{
		Sessions: []UpgradeSession{
			// Fd -1 → os.NewFile returns nil → AdoptSession errors out.
			{ID: "bide1", Fd: -1, PID: 1 << 30},
			// Unknown session id: warned and skipped, never created.
			{ID: "ghaist1", Fd: -1, PID: 1},
		},
	}

	if err := sm.AdoptSessions(manifest); err != nil {
		t.Fatalf("AdoptSessions: %v", err)
	}

	s, ok := sm.Get("bide1")
	if !ok {
		t.Fatal("known session vanished after adopt")
	}

	if s.Status != StatusStopped {
		t.Errorf("un-adoptable session status = %q, want stopped", s.Status)
	}

	if _, ok := sm.Get("ghaist1"); ok {
		t.Error("unknown manifest session should not have been created")
	}
}

// TestRestartOrphanKillCov2 covers Restart's no-PTY orphan branch: a session
// recorded as running with a live, identity-verified PID but no live PTY (as
// after a daemon crash) has that process group killed and is transitioned to
// stopped before the resume attempt. Resume then fails on a missing agent, which
// keeps the test free of a respawned process (and the TempDir cleanup races that
// come with one) while still exercising the kill-and-transition logic.
func TestRestartOrphanKillCov2(t *testing.T) {
	sm := sleeperSM(t)
	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported on this platform: %v", err)
	}

	putSession(sm, &SessionState{
		ID: "bide-orphan", Name: "bide", Agent: "ghaist-agent",
		Status: StatusRunning, PID: pid, PIDStartTime: start,
	})

	// Resume fails (unknown agent), so Restart surfaces that error — but the
	// orphaned process must already have been killed and the session stopped.
	if _, err := sm.Restart("bide-orphan", 24, 80); err == nil {
		t.Fatal("expected resume-after-kill to fail on unknown agent")
	}

	if isProcessAlive(pid) {
		t.Error("orphaned process still alive after restart")
	}

	s, _ := sm.Get("bide-orphan")
	if s.Status != StatusStopped {
		t.Errorf("status = %q, want stopped after orphan kill", s.Status)
	}

	if s.PID != 0 {
		t.Errorf("PID = %d, want cleared to 0", s.PID)
	}
}

// TestRestartUnknownCov2 covers Restart of a session that does not exist: with no
// PTY and no state entry, it falls through to resume, which reports not-found.
func TestRestartUnknownCov2(t *testing.T) {
	sm := sleeperSM(t)

	if _, err := sm.Restart("nae-sic-session", 24, 80); err == nil {
		t.Fatal("expected error restarting unknown session, got nil")
	}
}

// TestForkErrorsCov2 covers Fork's early validation branches that don't require a
// git worktree or PTY: bad name, missing source, system session, in-place
// source, and unknown agent.
func TestForkErrorsCov2(t *testing.T) {
	sm := sleeperSM(t)

	if _, err := sm.Fork("Bad Name", "whatever", 24, 80); err == nil {
		t.Error("expected name-validation error for invalid fork name")
	}

	if _, err := sm.Fork("bonnie", "ghaist-source", 24, 80); err == nil {
		t.Error("expected not-found error for unknown source session")
	}

	putSession(sm, &SessionState{
		ID: "orch1", Name: "orchestrator", RepoPath: "/tmp/croft",
		SystemKind: SystemKindOrchestrator,
	})

	if _, err := sm.Fork("bonnie", "orch1", 24, 80); err == nil {
		t.Error("expected error forking a system session")
	}

	putSession(sm, &SessionState{
		ID: "inplace1", Name: "inplace", RepoPath: "/tmp/croft", InPlace: true,
	})

	if _, err := sm.Fork("bonnie", "inplace1", 24, 80); err == nil {
		t.Error("expected error forking an in-place session")
	}

	putSession(sm, &SessionState{
		ID: "src1", Name: "source", RepoPath: "/tmp/croft", Agent: "ghaist-agent",
	})

	if _, err := sm.Fork("bonnie", "src1", 24, 80); err == nil {
		t.Error("expected unknown-agent error forking session with missing agent")
	}
}

// TestApplyConfigChangesCov2 covers applyConfig's change-detection branches by
// swapping in a config that differs in every logged dimension.
func TestApplyConfigChangesCov2(t *testing.T) {
	sm := newTestSessionManager(t)
	old := sm.Config()

	newCfg := config.Default()
	newCfg.DefaultAgent = "codex"
	newCfg.BranchPrefix = "bonnie"
	newCfg.FetchOnCreate = !old.FetchOnCreate
	newCfg.GitHubUsername = "speir"
	newCfg.GitPull.Enabled = !old.GitPull.Enabled
	newCfg.GitPull.Interval = "42m"
	newCfg.Sandbox.Enabled = !old.Sandbox.Enabled
	newCfg.Sandbox.ReadDirs = []string{"/tmp/loch"}
	newCfg.Sandbox.WriteDirs = []string{"/tmp/glen"}
	newCfg.Sandbox.ReadFiles = []string{"/tmp/skelf.txt"}
	newCfg.Sandbox.WriteFiles = []string{"/tmp/wynd.txt"}
	newCfg.Sandbox.Features = []string{"process-control"}
	newCfg.Agents["neep"] = config.Agent{Command: "sleep"}
	delete(newCfg.Agents, "claude") // exercise the "removed" branch

	sm.applyConfig(newCfg)

	if got := sm.Config().DefaultAgent; got != "codex" {
		t.Errorf("DefaultAgent after applyConfig = %q, want codex", got)
	}

	if _, ok := sm.Config().Agents["claude"]; ok {
		t.Error("removed agent still present after applyConfig")
	}
}

// TestDetectAgentStatusesGitBranchesCov2 exercises the git-status branches of
// detectAgentStatuses that round 1 left uncovered: a non-shared session with a
// base branch (so UnpushedCommitCount runs) and an included repo (so the
// includes loop runs). A hook report drives the agent status so the test does
// not depend on PTY screen scraping.
func TestDetectAgentStatusesGitBranchesCov2(t *testing.T) {
	sm := sleeperSM(t)

	repoDir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()

		full := append([]string{"-c", "commit.gpgsign=false"}, args...)
		env := append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)

		cmd := exec.Command("git", full...)
		cmd.Dir = repoDir
		cmd.Env = env

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	runGit("init", "-b", "main")

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	runGit("add", "file.txt")
	runGit("commit", "-m", "init")

	// Leave an uncommitted modification so HasUncommittedChanges reports dirty.
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}

	logDir := t.TempDir()

	pty, err := grpty.NewSession(grpty.SessionOpts{
		ID: "glen1", Command: "sleep", Args: []string{"60"},
		Dir: repoDir, Rows: 24, Cols: 80,
		LogPath: filepath.Join(logDir, "glen.log"),
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = pty.Kill() }()

	sm.state.Sessions["glen1"] = &SessionState{
		ID: "glen1", Name: "glen", Agent: "claude",
		Status: StatusRunning, WorktreePath: repoDir, RepoPath: repoDir,
		BaseBranch: "main",
		Includes: []IncludedRepoState{
			{RepoName: "croft", RepoPath: repoDir, WorktreePath: repoDir, BaseBranch: "main"},
		},
	}
	sm.sessions["glen1"] = pty

	sm.mu.Lock()
	sm.hookReports["glen1"] = hookReport{
		Status: "active", Event: "Notification",
		ReportedAt: time.Now(), AuthoritativeUntil: time.Now().Add(5 * time.Minute),
	}
	sm.mu.Unlock()

	sm.detectAgentStatuses()

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	s := sm.state.Sessions["glen1"]
	if !s.GitDirty {
		t.Error("expected GitDirty=true for modified worktree")
	}

	if s.AgentStatus != "active" {
		t.Errorf("AgentStatus = %q, want active (from hook report)", s.AgentStatus)
	}
}
