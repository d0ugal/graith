package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/testutil"
)

type recordingWorktreePort struct {
	calls          []string
	setup          error
	teardown       error
	branchStarted  chan struct{}
	branchContinue chan struct{}
}

func (p *recordingWorktreePort) IsInsideRepo(string) bool { return true }

func (p *recordingWorktreePort) RepoRoot(path string) (string, error) {
	p.calls = append(p.calls, "root")
	return path, nil
}

func (p *recordingWorktreePort) DiscoverGitHubUsername(context.Context, string) (string, error) {
	p.calls = append(p.calls, "username")
	return "braw", nil
}

func (p *recordingWorktreePort) DiscoverDefaultBranch(string) (string, error) {
	p.calls = append(p.calls, "branch")
	if p.branchStarted != nil {
		close(p.branchStarted)
		<-p.branchContinue
	}

	return "main", nil
}

func (p *recordingWorktreePort) DiscoverDefaultBranchOrHEAD(string) (string, error) {
	p.calls = append(p.calls, "branch-or-head")
	return "main", nil
}

func (p *recordingWorktreePort) Setup(context.Context, string, string, string, string, bool) error {
	p.calls = append(p.calls, "setup")
	return p.setup
}

func (p *recordingWorktreePort) Teardown(_, worktreePath, _ string) error {
	p.calls = append(p.calls, "teardown:"+worktreePath)
	return p.teardown
}

func TestWorktreePortContractPreservesRollbackOrder(t *testing.T) {
	port := &recordingWorktreePort{}

	if err := port.Setup(context.Background(), "/repo/croft", "/wt/canny", "graith/canny", "main", false); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	if err := port.Teardown("/repo/croft", "/wt/canny", "graith/canny"); err != nil {
		t.Fatalf("Teardown() error = %v", err)
	}

	if want := []string{"setup", "teardown:/wt/canny"}; !reflect.DeepEqual(port.calls, want) {
		t.Fatalf("calls = %v, want %v", port.calls, want)
	}
}

func TestTeardownWorktreePortRollsBackIncludesInReverseOrder(t *testing.T) {
	port := &recordingWorktreePort{}
	parent := t.TempDir()
	includes := []IncludedRepoState{
		{RepoPath: "/repo/one", WorktreePath: filepath.Join(parent, "one"), Branch: "graith/one"},
		{RepoPath: "/repo/two", WorktreePath: filepath.Join(parent, "two"), Branch: "graith/two"},
	}

	mainWorktree := filepath.Join(parent, "main")
	if err := os.MkdirAll(mainWorktree, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := teardownWorktreePort(port, "/repo/main", mainWorktree, "graith/main", includes); err != nil {
		t.Fatalf("teardownWorktreePort() error = %v", err)
	}

	if want := []string{"teardown:" + filepath.Join(parent, "two"), "teardown:" + filepath.Join(parent, "one"), "teardown:" + mainWorktree}; !reflect.DeepEqual(port.calls, want) {
		t.Fatalf("calls = %v, want %v", port.calls, want)
	}

	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("session parent still exists, stat error = %v", err)
	}
}

func TestTeardownWorktreePortRemovesParentWhenFirstIncludeFails(t *testing.T) {
	port := &recordingWorktreePort{}
	parent := t.TempDir()

	mainWorktree := filepath.Join(parent, "main")
	if err := os.MkdirAll(mainWorktree, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := teardownWorktreePort(port, "/repo/main", mainWorktree, "graith/main", nil); err != nil {
		t.Fatalf("teardownWorktreePort() error = %v", err)
	}

	if want := []string{"teardown:" + mainWorktree}; !reflect.DeepEqual(port.calls, want) {
		t.Fatalf("calls = %v, want %v", port.calls, want)
	}

	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("session parent still exists, stat error = %v", err)
	}
}

func TestWorktreePortContractPropagatesProviderFailure(t *testing.T) {
	wantErr := errors.New("dreich provider")
	port := &recordingWorktreePort{setup: wantErr}

	if err := port.Setup(context.Background(), "/repo/croft", "/wt/canny", "graith/canny", "main", false); !errors.Is(err, wantErr) {
		t.Fatalf("Setup() error = %v, want %v", err, wantErr)
	}

	if want := []string{"setup"}; !reflect.DeepEqual(port.calls, want) {
		t.Fatalf("calls after failed setup = %v, want %v", port.calls, want)
	}
}

func TestSessionManagerLogsWorktreeRollbackFailure(t *testing.T) {
	sm, buf := newLogCapturingManager(t)
	port := &recordingWorktreePort{teardown: errors.New("dreich teardown")}
	parent := t.TempDir()

	mainWorktree := filepath.Join(parent, "main")
	if err := os.MkdirAll(mainWorktree, 0o700); err != nil {
		t.Fatal(err)
	}

	sm.teardownWorktreePort(port, "/repo/main", mainWorktree, "graith/main", nil)

	record := findRecord(logRecords(t, buf), "failed to rollback worktree setup")
	if record == nil {
		t.Fatal("worktree rollback failure was not logged")
	}

	if !strings.Contains(record["err"].(string), "dreich teardown") {
		t.Fatalf("logged error = %v, want provider failure", record["err"])
	}
}

func TestCreateUsesWorktreePortWhenSetupFails(t *testing.T) {
	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"60"}}
	sm := newSMWithConfig(t, cfg)
	port := &recordingWorktreePort{setup: errors.New("dreich setup")}
	sm.worktreePort = port

	_, err := sm.Create(CreateOpts{
		Name:       "canny",
		AgentName:  "sleeper",
		RepoPath:   t.TempDir(),
		BaseBranch: "main",
		NoFetch:    true,
		Rows:       24,
		Cols:       80,
	})
	if err == nil || !strings.Contains(err.Error(), "dreich setup") {
		t.Fatalf("Create() error = %v, want provider failure", err)
	}

	if want := []string{"root", "username", "setup"}; !reflect.DeepEqual(port.calls, want) {
		t.Fatalf("calls = %v, want %v", port.calls, want)
	}

	if len(sm.state.Sessions) != 0 {
		t.Fatal("failed Create left a session reservation")
	}
}

func TestCreateRejectsDisallowedRepoBeforeBranchDiscovery(t *testing.T) {
	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.AllowedRepoPaths = []string{filepath.Join(t.TempDir(), "elsewhere")}
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"60"}}
	sm := newSMWithConfig(t, cfg)
	port := &recordingWorktreePort{}
	sm.worktreePort = port

	_, err := sm.Create(CreateOpts{Name: "canny", AgentName: "sleeper", RepoPath: t.TempDir(), Rows: 24, Cols: 80})
	if err == nil || !strings.Contains(err.Error(), "allowed_repo_paths") {
		t.Fatalf("Create() error = %v, want allow-list failure", err)
	}

	if want := []string{"root", "username"}; !reflect.DeepEqual(port.calls, want) {
		t.Fatalf("calls = %v, want %v", port.calls, want)
	}
}

func TestCreateRejectsSingletonBeforeBranchDiscovery(t *testing.T) {
	cfg := config.Default()
	cfg.FetchOnCreate = false
	repoPath := t.TempDir()
	cfg.Repos = []config.RepoConfig{{Path: repoPath, Singleton: true}}
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"60"}}
	sm := newSMWithConfig(t, cfg)
	sm.state.Sessions["braw"] = &SessionState{ID: "braw", RepoPath: repoPath, Status: StatusRunning, Name: "braw"}
	port := &recordingWorktreePort{}
	sm.worktreePort = port

	_, err := sm.Create(CreateOpts{Name: "canny", AgentName: "sleeper", RepoPath: repoPath, Rows: 24, Cols: 80})
	if err == nil || !strings.Contains(err.Error(), "singleton") {
		t.Fatalf("Create() error = %v, want singleton failure", err)
	}

	if want := []string{"root", "username"}; !reflect.DeepEqual(port.calls, want) {
		t.Fatalf("calls = %v, want %v", port.calls, want)
	}
}

func TestCreateRevalidatesSingletonAfterBranchDiscovery(t *testing.T) {
	cfg := config.Default()
	cfg.FetchOnCreate = false
	repoPath := t.TempDir()
	cfg.Repos = []config.RepoConfig{{Path: repoPath, Singleton: true}}
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"60"}}
	sm := newSMWithConfig(t, cfg)
	port := &recordingWorktreePort{branchStarted: make(chan struct{}), branchContinue: make(chan struct{})}
	sm.worktreePort = port

	createDone := make(chan error, 1)

	go func() {
		_, err := sm.Create(CreateOpts{Name: "canny", AgentName: "sleeper", RepoPath: repoPath, Rows: 24, Cols: 80})
		createDone <- err
	}()

	select {
	case <-port.branchStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("default branch discovery did not start")
	}

	sm.mu.Lock()
	sm.state.Sessions["braw"] = &SessionState{ID: "braw", RepoPath: repoPath, Status: StatusRunning, Name: "braw"}
	sm.mu.Unlock()
	close(port.branchContinue)

	if err := <-createDone; err == nil || !strings.Contains(err.Error(), "singleton") {
		t.Fatalf("Create() error = %v, want revalidated singleton failure", err)
	}

	if slices.Contains(port.calls, "setup") {
		t.Fatalf("provider setup called after singleton revalidation: %v", port.calls)
	}
}

func TestCreateDefaultBranchDiscoveryDoesNotHoldManagerLock(t *testing.T) {
	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"60"}}
	sm := newSMWithConfig(t, cfg)
	port := &recordingWorktreePort{
		setup:          errors.New("dreich setup"),
		branchStarted:  make(chan struct{}),
		branchContinue: make(chan struct{}),
	}
	sm.worktreePort = port

	createDone := make(chan error, 1)

	go func() {
		_, err := sm.Create(CreateOpts{Name: "canny", AgentName: "sleeper", RepoPath: t.TempDir(), Rows: 24, Cols: 80})
		createDone <- err
	}()

	select {
	case <-port.branchStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("default branch discovery did not start")
	}

	lockFree := make(chan struct{})

	go func() {
		sm.Config()
		close(lockFree)
	}()

	select {
	case <-lockFree:
	case <-time.After(5 * time.Second):
		t.Fatal("manager lock was held during default branch discovery")
	}

	close(port.branchContinue)

	if err := <-createDone; err == nil || !strings.Contains(err.Error(), "dreich setup") {
		t.Fatalf("Create() error = %v, want provider failure", err)
	}
}

func TestGitWorktreeAdapterLifecycle(t *testing.T) {
	testutil.IsolateGit(t)
	repo := t.TempDir()
	worktree := t.TempDir()
	gitOut(t, repo, "init", "-b", "main")
	gitOut(t, repo, "commit", "--allow-empty", "-m", "braw")

	port := gitWorktreeAdapter{}
	if err := port.Setup(context.Background(), repo, worktree, "graith/canny", "main", false); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	if got := gitOut(t, repo, "branch", "--list", "graith/canny"); got == "" {
		t.Fatal("Setup() did not create the branch")
	}

	if err := port.Teardown(repo, worktree, "graith/canny"); err != nil {
		t.Fatalf("Teardown() error = %v", err)
	}

	if got := gitOut(t, repo, "branch", "--list", "graith/canny"); got != "" {
		t.Fatalf("Teardown() left branch %q", got)
	}
}
