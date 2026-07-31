package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
)

func newReadOnlyBranchTestManager(t *testing.T) (*SessionManager, *recordingWorktreePort) {
	t.Helper()

	backend := filepath.Join(t.TempDir(), "safehouse-stub")
	if err := os.WriteFile(backend, []byte("#!/bin/sh\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = \"--\" ]; then\n    shift\n    exec \"$@\"\n  fi\n  shift\ndone\nexit 64\n"), 0o755); err != nil { //nolint:gosec // G306: test backend stub must be executable.
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.FetchOnCreate = true
	cfg.DefaultAgent = "sleeper"
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"60"}, ResumeArgs: []string{"60"}}
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: "safehouse", Command: backend}

	sm := newSMWithConfig(t, cfg)
	sm.sandboxResolver = func(string) (bool, error) { return true, nil }

	port := &recordingWorktreePort{readOnlyRev: "braw-revision", refreshRev: "canny-revision"}
	sm.worktreePort = port

	return sm, port
}

func TestCreateReadOnlyBranchSessionUsesDetachedMirrorWorktree(t *testing.T) {
	sm, port := newReadOnlyBranchTestManager(t)

	repo := filepath.Join(t.TempDir(), "croft")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}

	created, err := sm.Create(CreateOpts{
		Name: "canny-reader", AgentName: "sleeper", RepoPath: repo,
		ReadOnly: true, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

	if !created.Mirror || !created.ReadOnlyBranch {
		t.Fatalf("created mirror/read-only = %v/%v, want true/true", created.Mirror, created.ReadOnlyBranch)
	}

	if created.MirrorSourceID != "" {
		t.Fatalf("MirrorSourceID = %q, want empty for branch-backed read-only session", created.MirrorSourceID)
	}

	if created.Branch != "main" || created.BaseBranch != "main" {
		t.Fatalf("branch/base = %q/%q, want main/main", created.Branch, created.BaseBranch)
	}

	if created.ReadOnlyRevision != "braw-revision" {
		t.Fatalf("ReadOnlyRevision = %q, want braw-revision", created.ReadOnlyRevision)
	}

	if created.CWD == "" || created.CWD == created.WorktreePath {
		t.Fatalf("CWD/worktree = %q/%q, want writable scratch distinct from read-only worktree", created.CWD, created.WorktreePath)
	}

	if wantScratch := filepath.Join(sm.paths.DataDir, "scratch", created.ID); created.CWD != wantScratch {
		t.Fatalf("CWD = %q, want scratch %q", created.CWD, wantScratch)
	}

	if _, err := os.Stat(created.CWD); err != nil {
		t.Fatalf("scratch dir missing: %v", err)
	}

	if port.readOnlySetupCall == nil {
		t.Fatal("SetupReadOnly was not called")
	}

	if port.readOnlySetupCall.repoPath != repo || port.readOnlySetupCall.branch != "main" || !port.readOnlySetupCall.fetch {
		t.Fatalf("SetupReadOnly call = %+v, want repo %q branch main fetch=true", port.readOnlySetupCall, repo)
	}

	if !slices.Contains(port.calls, "branch") {
		t.Fatalf("default branch discovery not called: %v", port.calls)
	}

	persisted, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	stored := persisted.Sessions[created.ID]
	if stored == nil || !stored.Mirror || !stored.ReadOnlyBranch || stored.ReadOnlyRevision != "braw-revision" {
		t.Fatalf("persisted session = %+v", stored)
	}

	info := toSessionInfo(created, sm.Config(), nil)
	if !info.Mirror || !info.ReadOnlyBranch || info.ReadOnlyRevision != "braw-revision" {
		t.Fatalf("protocol session info = %+v", info)
	}
}

func TestCreateReadOnlyBranchRequiresSandbox(t *testing.T) {
	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"60"}}

	sm := newSMWithConfig(t, cfg)
	port := &recordingWorktreePort{}
	sm.worktreePort = port

	repo := filepath.Join(t.TempDir(), "croft")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := sm.Create(CreateOpts{
		Name: "canny-reader", AgentName: "sleeper", RepoPath: repo,
		BaseBranch: "main", ReadOnly: true, Rows: 24, Cols: 80,
	})
	if err == nil || !strings.Contains(err.Error(), "--read-only requires sandbox") {
		t.Fatalf("Create() error = %v, want read-only sandbox requirement", err)
	}

	if port.readOnlySetupCall != nil {
		t.Fatalf("SetupReadOnly should not run before sandbox validation: %+v", port.readOnlySetupCall)
	}
}

func TestCreateReadOnlyBranchRejectsIncludedRepos(t *testing.T) {
	sm, port := newReadOnlyBranchTestManager(t)

	repo := filepath.Join(t.TempDir(), "croft")
	include := filepath.Join(t.TempDir(), "bothy")

	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}

	sm.cfg.Repos = []config.RepoConfig{{Path: repo, Includes: []string{include}}}

	_, err := sm.Create(CreateOpts{
		Name: "canny-reader", AgentName: "sleeper", RepoPath: repo,
		BaseBranch: "main", ReadOnly: true, Rows: 24, Cols: 80,
	})
	if err == nil || !strings.Contains(err.Error(), "read-only sessions do not support included repos") {
		t.Fatalf("Create() error = %v, want includes rejection", err)
	}

	if port.readOnlySetupCall != nil {
		t.Fatalf("SetupReadOnly should not run after includes rejection: %+v", port.readOnlySetupCall)
	}
}

func TestResumeReadOnlyBranchRefreshesDetachedWorktree(t *testing.T) {
	sm, port := newReadOnlyBranchTestManager(t)

	repo := filepath.Join(t.TempDir(), "croft")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}

	created, err := sm.Create(CreateOpts{
		Name: "canny-reader", AgentName: "sleeper", RepoPath: repo,
		BaseBranch: "main", ReadOnly: true, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

	if err := sm.Stop(created.ID); err != nil {
		t.Fatal(err)
	}

	waitForStatus(t, sm, created.ID, StatusStopped)

	resumed, err := sm.Resume(created.ID, 24, 80)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	if resumed.Status != StatusRunning {
		t.Fatalf("resumed status = %q, want %q", resumed.Status, StatusRunning)
	}

	if port.refreshCall == nil {
		t.Fatal("RefreshReadOnly was not called")
	}

	if port.refreshCall.repoPath != repo || port.refreshCall.branch != "main" || !port.refreshCall.fetch {
		t.Fatalf("RefreshReadOnly call = %+v, want repo %q branch main fetch=true", port.refreshCall, repo)
	}

	if resumed.ReadOnlyRevision != "canny-revision" {
		t.Fatalf("resumed revision = %q, want canny-revision", resumed.ReadOnlyRevision)
	}

	persisted, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	if got := persisted.Sessions[created.ID].ReadOnlyRevision; got != "canny-revision" {
		t.Fatalf("persisted revision = %q, want canny-revision", got)
	}
}

func TestResumeReadOnlyBranchIgnoresSingletonPeer(t *testing.T) {
	sm, _ := newReadOnlyBranchTestManager(t)

	repo := filepath.Join(t.TempDir(), "croft")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}

	sm.cfg.Repos = []config.RepoConfig{{Path: repo, Singleton: true}}
	sm.state.Sessions["braw-owner"] = &SessionState{
		ID:       "braw-owner",
		Name:     "braw-owner",
		RepoPath: repo,
		Status:   StatusRunning,
	}

	created, err := sm.Create(CreateOpts{
		Name: "canny-reader", AgentName: "sleeper", RepoPath: repo,
		BaseBranch: "main", ReadOnly: true, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

	if err := sm.Stop(created.ID); err != nil {
		t.Fatal(err)
	}

	waitForStatus(t, sm, created.ID, StatusStopped)

	if _, err := sm.Resume(created.ID, 24, 80); err != nil {
		t.Fatalf("Resume() error = %v, want singleton peer ignored for read-only branch session", err)
	}
}

func TestDeleteReadOnlyBranchRemovesDetachedWorktreeWithoutDeletingSourceBranch(t *testing.T) {
	_, repo := setupTestRepo(t)

	rev, err := git.RunOutput(repo, "rev-parse", "main")
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}

	worktree := filepath.Join(t.TempDir(), "reader")
	if err := git.CreateDetachedWorktreeContext(context.Background(), repo, worktree, rev); err != nil {
		t.Fatalf("create detached worktree: %v", err)
	}

	gitRun(t, repo, "checkout", "-b", "canny-work")

	sm := newSMWithConfig(t, config.Default())
	id := "reader1"

	scratch := filepath.Join(sm.paths.DataDir, "scratch", id)
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatal(err)
	}

	sm.state.Sessions[id] = &SessionState{
		ID:               id,
		Name:             "canny-reader",
		RepoPath:         repo,
		RepoName:         filepath.Base(repo),
		WorktreePath:     worktree,
		CWD:              scratch,
		Branch:           "main",
		BaseBranch:       "main",
		Mirror:           true,
		ReadOnlyBranch:   true,
		ReadOnlyRevision: rev,
		Status:           StatusStopped,
	}

	if err := sm.Delete(id); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, ok := sm.state.Sessions[id]; ok {
		t.Fatal("read-only branch session should be removed from state")
	}

	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("detached worktree still present: %v", err)
	}

	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch dir still present: %v", err)
	}

	if !git.RefExists(repo, "main") {
		t.Fatal("source branch main was deleted")
	}
}
