package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// cfgVariants returns two distinct config pointers cloned from base, differing
// only in their [git] timeouts. Because every lock-free sm.cfg read first loads
// the sm.cfg pointer field, swapping between two distinct pointers is enough to
// race any unsynchronized reader — the specific field that differs is
// irrelevant. Shallow copies share base's maps/slices (agents, repos), which
// applyConfig only reads, so Create/Fork/Migrate still resolve their agent and
// repo under either generation.
func cfgVariants(base *config.Config) (a, b *config.Config) {
	x := *base
	x.Git.FetchTimeout = "5s"
	x.Git.MergeTimeout = "5s"
	x.Git.UsernameTimeout = "1s"

	y := *base
	y.Git.FetchTimeout = "2m"
	y.Git.MergeTimeout = "2m"
	y.Git.UsernameTimeout = "15s"

	return &x, &y
}

// gitTimeoutConfigs returns two full default configs that differ only in their
// [git] fetch/merge/username timeouts. Keeping every other section identical
// means applyConfig's change-detection publishes just the new pointer (plus the
// launch-throttle resize, a no-op when sm.launch is nil) and spawns none of its
// detached workers (tools resolver, transcript caps, PR-watch trust release),
// so these races isolate the sm.cfg pointer read/write under test.
func gitTimeoutConfigs() (fast, slow *config.Config) {
	fast = config.Default()
	fast.GitPull.Enabled = true
	fast.Git.FetchTimeout = "5s"
	fast.Git.MergeTimeout = "5s"
	fast.Git.UsernameTimeout = "1s"

	slow = config.Default()
	slow.GitPull.Enabled = true
	slow.Git.FetchTimeout = "2m"
	slow.Git.MergeTimeout = "2m"
	slow.Git.UsernameTimeout = "15s"

	return fast, slow
}

// hammerApplyConfig swaps sm.cfg between two configs in a tight loop until the
// returned stop func is called. It models a config hot reload racing whatever
// production reader the test drives concurrently.
func hammerApplyConfig(sm *SessionManager, a, b *config.Config) (stop func()) {
	var (
		done atomic.Bool
		wg   sync.WaitGroup
	)

	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; !done.Load(); i++ {
			if i%2 == 0 {
				sm.applyConfig(a)
			} else {
				sm.applyConfig(b)
			}
		}
	}()

	return func() {
		done.Store(true)
		wg.Wait()
	}
}

// TestGitPullConfigReloadRace exercises the background git-pull reader
// concurrently with config hot reload. Before #1287 (regression introduced by
// 2acbe3c, which replaced the gitFetchTimeout/gitMergeTimeout package constants
// with sm.cfg reads), pullIfClean read the fetch and merge timeouts from sm.cfg
// without holding sm.mu, racing applyConfig's pointer swap. The reader now
// takes a coherent [git] snapshot for the whole operation, so `go test -race`
// is clean. This test fails under -race on the pre-fix code.
func TestGitPullConfigReloadRace(t *testing.T) {
	_, cloneDir := setupTestRepo(t)

	sm := newTestSM(t)
	fast, slow := gitTimeoutConfigs()
	sm.cfg = slow

	stop := hammerApplyConfig(sm, fast, slow)
	defer stop()

	// Each pullIfClean reaches both the fetch and merge timeout reads (the clone
	// is up to date, so the fetch/merge are no-ops but the timeouts are still
	// read). Looping widens the window the reader overlaps the writer.
	for i := 0; i < 80; i++ {
		if _, err := sm.pullIfClean(context.Background(), cloneDir); err != nil {
			t.Fatalf("pullIfClean iteration %d: %v", i, err)
		}
	}
}

// TestCreateConfigReloadRace drives a real repo-backed Create concurrently with
// config hot reload. Before #1287 (regression from 2acbe3c/e534406), Create
// read the git username timeout (pre-lock) and the phase-2 fetch timeout /
// headless limits from sm.cfg without the lock, racing applyConfig's swap.
// Create now reads those from the pre-lock and phase-2 snapshots
// (preLockCfg / cfgSnapshot). Fails under -race on the pre-fix code.
func TestCreateConfigReloadRace(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}

	repoDir := initTempGitRepo(t)
	sm, _ := newRecorderManager(t, repoDir, nil)

	a, b := cfgVariants(sm.Config())

	stop := hammerApplyConfig(sm, a, b)
	defer stop()

	for i := 0; i < 6; i++ {
		created, err := sm.Create(CreateOpts{
			Name:       fmt.Sprintf("canny-%d", i),
			AgentName:  "cursor",
			RepoPath:   repoDir,
			BaseBranch: "main",
			Rows:       24,
			Cols:       80,
		})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}

		stopAndClosePTY(sm, created.ID)
	}
}

// TestForkConfigReloadRace drives a real Fork concurrently with config hot
// reload. Before #1287 (regression from 2acbe3c/e534406), Fork read the git
// username/fetch timeouts and the transcript render limits from sm.cfg without
// the lock. Fork now uses the cfgSnapshot captured under sm.mu. Fails under
// -race on the pre-fix code.
func TestForkConfigReloadRace(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}

	repoDir := initTempGitRepo(t)
	sm, _ := newRecorderManager(t, repoDir, nil)

	sm.mu.Lock()
	sm.state.Sessions["src1"] = &SessionState{
		ID:             "src1",
		Name:           "braw-source",
		RepoPath:       repoDir,
		RepoName:       "testrepo",
		WorktreePath:   repoDir,
		Branch:         "main",
		BaseBranch:     "main",
		Agent:          "cursor",
		AgentSessionID: "braw-agent-id",
		Status:         StatusRunning,
	}
	sm.mu.Unlock()

	a, b := cfgVariants(sm.Config())

	stop := hammerApplyConfig(sm, a, b)
	defer stop()

	for i := 0; i < 6; i++ {
		forked, err := sm.Fork(fmt.Sprintf("braw-fork-%d", i), "src1", 24, 80)
		if err != nil {
			t.Fatalf("Fork %d: %v", i, err)
		}

		stopAndClosePTY(sm, forked.ID)
	}
}

// TestMigrateConfigReloadRace drives a real Migrate (which also exercises the
// resume path) concurrently with config hot reload. Before #1287 (regression
// from e534406/2acbe3c), Migrate read the transcript render limits and the
// migration health-window from sm.cfg — and the resume path read the git
// username timeout — without the lock. Migrate now threads its cfg :=
// sm.Config() snapshot, and resume captures the username timeout under sm.mu.
// Fails under -race on the pre-fix code.
func TestMigrateConfigReloadRace(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}

	repoDir := initTempGitRepo(t)

	// Bound codex id-capture and stage a Claude transcript so Migrate's
	// transcript read succeeds and reaches the racy render-limit read.
	t.Setenv("CODEX_HOME", t.TempDir())
	claudeRoot := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeRoot)

	sid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	projDir := filepath.Join(claudeRoot, "projects", "-some-proj")

	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatal(err)
	}

	lines := `{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"fix the bothy"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"done, braw"}]}}
`
	if err := os.WriteFile(filepath.Join(projDir, sid+".jsonl"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	sm := newMigrateTestManager(t)
	sm.mu.Lock()
	sm.state.Sessions["m1"] = &SessionState{
		ID: "m1", Name: "braw-bothy", Agent: "claude",
		AgentSessionID: sid, Status: StatusStopped,
		WorktreePath: repoDir, RepoPath: repoDir, CreatedAt: time.Now(),
	}
	sm.mu.Unlock()

	a, b := cfgVariants(sm.Config())

	stop := hammerApplyConfig(sm, a, b)
	defer stop()

	if _, err := sm.Migrate("m1", "codex", "", 24, 80); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	stop()

	if p, ok := sm.GetPTY("m1"); ok {
		_ = p.Kill()
		<-p.Done()
	}

	// Let watchSession persist the stop before temp dirs are torn down.
	time.Sleep(250 * time.Millisecond)
}
