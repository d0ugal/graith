package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// TestConcurrentConfigReloadVsHookAndStoreReaders exercises the config-driven
// readers fixed for issue #1287 (hook generation) concurrently with applyConfig,
// alongside the store-limit reads reloaded for #1291. Run with -race to detect a
// data race between a reader and the config-pointer swap.
func TestConcurrentConfigReloadVsHookAndStoreReaders(t *testing.T) {
	// preTrustCursorWorkspace writes under $HOME/.cursor; isolate it.
	t.Setenv("HOME", t.TempDir())

	sm := newTestSessionManagerWithDataDir(t)

	ms, err := NewMsgStore(filepath.Join(t.TempDir(), "msg.db"))
	if err != nil {
		t.Fatalf("NewMsgStore: %v", err)
	}
	defer func() { _ = ms.Close() }()

	sm.messages = ms

	ts, err := NewTodoStore(filepath.Join(t.TempDir(), "todos.sqlite"))
	if err != nil {
		t.Fatalf("NewTodoStore: %v", err)
	}
	defer func() { _ = ts.Close() }()

	sm.todos = ts

	worktree := t.TempDir()

	const iters = 200

	var wg sync.WaitGroup

	// Writer: swap the config repeatedly, varying every field the readers touch.
	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; i < iters; i++ {
			enabled := i%2 == 0

			delay := "10ms"
			if !enabled {
				delay = "20ms"
			}

			cfg := config.Default()
			cfg.Approvals.Enabled = &enabled
			cfg.Messages.JailListLimit = 1 + i%64
			cfg.Todo.MaxTitle = 1 + i%200
			cfg.Todo.MaxNote = 1 + i%500
			cfg.Lifecycle.InputDelay = delay
			cfg.Git.FetchTimeout = "1m"

			sm.applyConfig(cfg)
		}
	}()

	// Reader 1: hook generation (reads Approvals + agent policy via sm.Config()).
	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; i < iters; i++ {
			if _, err := sm.generateClaudeSettings("sess-hook", false); err != nil {
				t.Errorf("generateClaudeSettings: %v", err)
				return
			}

			if _, _, err := sm.injectCodexHooks("sess-hook", false); err != nil {
				t.Errorf("injectCodexHooks: %v", err)
				return
			}

			if _, _, err := sm.injectCursorHooks("sess-hook", worktree, false); err != nil {
				t.Errorf("injectCursorHooks: %v", err)
				return
			}
		}
	}()

	// Reader 2: store limit reads updated by applyConfig.
	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; i < iters; i++ {
			if _, err := ms.ListJailed(true); err != nil {
				t.Errorf("ListJailed: %v", err)
				return
			}

			_ = ts.titleLimit()
			_ = ts.noteLimit()
		}
	}()

	// Reader 3: plain Config() snapshots.
	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; i < iters; i++ {
			cfg := sm.Config()
			_ = cfg.Git.FetchTimeoutDuration()
			_ = cfg.Lifecycle.InputDelayDuration()
			_ = cfg.Approvals.HookEnabled()
		}
	}()

	wg.Wait()
}

// TestConcurrentConfigReloadVsGitPull exercises the git-pull reader fixed for
// issue #1287: pullIfClean reads its fetch/merge timeouts from a single
// sm.Config() snapshot, so a concurrent applyConfig must not race it. Run with
// -race.
func TestConcurrentConfigReloadVsGitPull(t *testing.T) {
	_, cloneDir := setupTestRepo(t)

	sm := newTestSM(t)

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; i < 100; i++ {
			timeout := "30s"
			if i%2 != 0 {
				timeout = "45s"
			}

			cfg := config.Default()
			cfg.GitPull.Enabled = true
			cfg.Git.FetchTimeout = timeout
			cfg.Git.MergeTimeout = timeout

			sm.applyConfig(cfg)
		}
	}()

	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; i < 15; i++ {
			if _, err := sm.pullIfClean(context.Background(), cloneDir); err != nil {
				t.Errorf("pullIfClean: %v", err)
				return
			}
		}
	}()

	wg.Wait()
}
