package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// newTestSMAt builds a SessionManager rooted at a caller-supplied data dir so a
// test can simulate a daemon restart by constructing a second manager over the
// same on-disk state (the per-session cursor ownership markers live under
// DataDir/hooks/<id>/, so they survive the "restart").
func newTestSMAt(t *testing.T, dir string) *SessionManager {
	t.Helper()

	cfg := config.Default()
	enabled := true
	cfg.Approvals.Enabled = &enabled

	return NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(dir, "state.json"),
		DataDir:   dir,
	}, slog.Default())
}

// addSessionState registers a live session in state so the shared-ownership
// refcount predicate (liveCursorHookCoOwnerExists) counts it as an owner.
func addSessionState(sm *SessionManager, id, worktree string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.state.Sessions[id] = &SessionState{ID: id, WorktreePath: worktree}
}

// dropSessionState removes a session from state, mirroring session_delete.go
// which deletes the session from state BEFORE calling cleanupHooks.
func dropSessionState(sm *SessionManager, id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	delete(sm.state.Sessions, id)
}

func statExists(t *testing.T, path string) bool {
	t.Helper()

	_, err := os.Stat(path)
	if err == nil {
		return true
	}

	if os.IsNotExist(err) {
		return false
	}

	t.Fatalf("stat %s: %v", path, err)

	return false
}

// TestCursorHooksSharedByConcurrentSessions is the core #1328 regression: two
// --allow-concurrent sessions sharing one worktree, each generating the identical
// graith hooks.json, must both launch (the second ADOPTS the first's artifact),
// and the artifact must be removed only after the LAST owner exits — in either
// deletion order.
func TestCursorHooksSharedByConcurrentSessions(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		deleteFirst, deleteSecond string
	}{
		{"delete s1 then s2", "canny-one", "canny-two"},
		{"delete s2 then s1", "canny-two", "canny-one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSessionManagerWithDataDir(t)
			worktree := t.TempDir()
			s1, s2 := "canny-one", "canny-two"

			addSessionState(sm, s1, worktree)
			addSessionState(sm, s2, worktree)

			if _, _, err := sm.injectCursorHooks(s1, worktree, false, cursorProjectAgent(), false); err != nil {
				t.Fatalf("s1 injectCursorHooks: %v", err)
			}

			// s2 generates the identical artifact; it must adopt (co-own), not refuse.
			if _, _, err := sm.injectCursorHooks(s2, worktree, false, cursorProjectAgent(), false); err != nil {
				t.Fatalf("s2 injectCursorHooks (adopt): %v", err)
			}

			hooksPath := cursorHooksPath(worktree)
			if !statExists(t, hooksPath) {
				t.Fatal("shared hooks.json missing after both injections")
			}

			// Both sessions must have recorded their own ownership marker.
			if !statExists(t, sm.cursorHooksOwnershipPath(s1)) || !statExists(t, sm.cursorHooksOwnershipPath(s2)) {
				t.Fatal("both co-owners must record an ownership marker")
			}

			// First owner exits: the artifact must survive for the remaining owner.
			dropSessionState(sm, tc.deleteFirst)
			sm.cleanupHooks(tc.deleteFirst, "cursor", worktree)

			if !statExists(t, hooksPath) {
				t.Errorf("hooks.json removed while a live co-owner (%s) remained", tc.deleteSecond)
			}

			// Last owner exits: the artifact must now be removed.
			dropSessionState(sm, tc.deleteSecond)
			sm.cleanupHooks(tc.deleteSecond, "cursor", worktree)

			if statExists(t, hooksPath) {
				t.Error("hooks.json not removed after the last owner exited")
			}
		})
	}
}

// TestCursorHooksIncompatibleConcurrentRejected is the #1328 incompatibility
// case: a second concurrent session sharing the worktree with a DIFFERENT hook
// definition (approval hooks toggled) must fail with a clear pre-launch error
// and must not disturb the first session's published artifact.
func TestCursorHooksIncompatibleConcurrentRejected(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	s1, s2 := "thrawn-a", "thrawn-b"

	addSessionState(sm, s1, worktree)
	addSessionState(sm, s2, worktree)

	// s1: approval hooks OFF.
	if _, _, err := sm.injectCursorHooks(s1, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("s1 injectCursorHooks: %v", err)
	}

	before := readFile(t, cursorHooksPath(worktree))

	// s2: approval hooks ON → a different artifact → incompatible.
	_, _, err := sm.injectCursorHooks(s2, worktree, false, cursorProjectAgent(), true)
	if err == nil {
		t.Fatal("expected an incompatible-definition error for the second concurrent session")
	}

	if !strings.Contains(err.Error(), "incompatible") {
		t.Errorf("error should name the incompatibility clearly; got %v", err)
	}

	// s1's artifact must be untouched, and s2 must not have taken ownership.
	if after := readFile(t, cursorHooksPath(worktree)); after != before {
		t.Error("incompatible second session mutated the first session's hooks.json")
	}

	if statExists(t, sm.cursorHooksOwnershipPath(s2)) {
		t.Error("rejected session must not record an ownership marker")
	}
}

// TestCursorHooksSharedSurvivesDaemonRestart proves shared ownership is recovered
// across a daemon restart: the per-session markers persist on disk, so a fresh
// SessionManager still refuses to remove the artifact while a live co-owner
// remains, and removes it once the last owner exits (issue #1328).
func TestCursorHooksSharedSurvivesDaemonRestart(t *testing.T) {
	dir := t.TempDir()
	worktree := t.TempDir()
	s1, s2 := "bide-one", "bide-two"

	sm1 := newTestSMAt(t, dir)
	addSessionState(sm1, s1, worktree)
	addSessionState(sm1, s2, worktree)

	if _, _, err := sm1.injectCursorHooks(s1, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("s1 inject: %v", err)
	}

	if _, _, err := sm1.injectCursorHooks(s2, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("s2 inject: %v", err)
	}

	// "Restart": a new manager over the same data dir, with both sessions resumed.
	sm2 := newTestSMAt(t, dir)
	addSessionState(sm2, s1, worktree)
	addSessionState(sm2, s2, worktree)

	hooksPath := cursorHooksPath(worktree)

	dropSessionState(sm2, s1)
	sm2.cleanupHooks(s1, "cursor", worktree)

	if !statExists(t, hooksPath) {
		t.Error("post-restart cleanup removed hooks.json while a live co-owner remained")
	}

	dropSessionState(sm2, s2)
	sm2.cleanupHooks(s2, "cursor", worktree)

	if statExists(t, hooksPath) {
		t.Error("post-restart cleanup did not remove hooks.json after the last owner exited")
	}
}

// TestCursorHooksFailedLaunchReleasesOwnership proves a session whose launch
// fails after publishing/adopting the shared artifact releases its ownership on
// cleanup without stranding or deleting a surviving co-owner's artifact
// (issue #1328).
func TestCursorHooksFailedLaunchReleasesOwnership(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	s1, s2 := "haar-live", "haar-doomed"

	addSessionState(sm, s1, worktree)
	addSessionState(sm, s2, worktree)

	if _, _, err := sm.injectCursorHooks(s1, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("s1 inject: %v", err)
	}

	if _, _, err := sm.injectCursorHooks(s2, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("s2 inject (adopt): %v", err)
	}

	// s2's launch fails: the create rollback removes it from state, then runs
	// cleanupHooks. The surviving owner s1 must keep the artifact.
	dropSessionState(sm, s2)
	sm.cleanupHooks(s2, "cursor", worktree)

	hooksPath := cursorHooksPath(worktree)
	if !statExists(t, hooksPath) {
		t.Error("failed-launch cleanup deleted the artifact still owned by a live session")
	}

	// s1 later exits normally: now the artifact is removed.
	dropSessionState(sm, s1)
	sm.cleanupHooks(s1, "cursor", worktree)

	if statExists(t, hooksPath) {
		t.Error("artifact not removed after the last owner exited")
	}
}

// TestCursorHooksStoppedCoOwnerCounts proves that a stopped-but-resumable
// co-owner (still in state, just not running) keeps the shared artifact alive:
// cleanup must count it as a live owner, not filter it out by status (#1328).
func TestCursorHooksStoppedCoOwnerCounts(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	running, stopped := "kelpie-run", "kelpie-stop"

	addSessionState(sm, running, worktree)
	addSessionState(sm, stopped, worktree)

	// Mark one co-owner stopped (resumable), the other running.
	sm.mu.Lock()
	sm.state.Sessions[running].Status = StatusRunning
	sm.state.Sessions[stopped].Status = StatusStopped
	sm.mu.Unlock()

	if _, _, err := sm.injectCursorHooks(running, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("running inject: %v", err)
	}

	if _, _, err := sm.injectCursorHooks(stopped, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("stopped inject (adopt): %v", err)
	}

	// The running session exits; the stopped co-owner must keep the artifact.
	dropSessionState(sm, running)
	sm.cleanupHooks(running, "cursor", worktree)

	if !statExists(t, cursorHooksPath(worktree)) {
		t.Error("cleanup removed hooks.json while a stopped resumable co-owner remained")
	}
}

// TestCursorHooksSidecarsNeverClobberPreexisting proves the quarantine/preserve
// machinery never overwrites a pre-existing file that happens to sit at a
// graith-style sidecar path: publication and cleanup use uniquely-reserved
// (O_EXCL) names, so stale ".graith-*" sidecars from a crashed run — or a user
// file with such a name — survive untouched (issue #1325, sidecar-clobber
// review). It exercises both the republish (own-file replace) and cleanup paths.
func TestCursorHooksSidecarsNeverClobberPreexisting(t *testing.T) {
	seedStaleSidecars := func(t *testing.T, worktree string) map[string][]byte {
		t.Helper()

		cursorDir := filepath.Join(worktree, ".cursor")
		if err := os.MkdirAll(cursorDir, 0o700); err != nil {
			t.Fatal(err)
		}

		hooksPath := cursorHooksPath(worktree)
		seeds := map[string][]byte{
			hooksPath + ".graith-claim.tmp": []byte("stale claim sidecar"),
			hooksPath + ".graith-rm.tmp":    []byte("stale rm sidecar"),
			hooksPath + ".graith-preserved": []byte("stale preserved sidecar"),
			hooksPath + ".graith-stage.tmp": []byte("stale stage sidecar"),
			hooksPath + ".graith-x.tmp":     []byte("unrelated user file"),
		}

		for path, content := range seeds {
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
		}

		return seeds
	}

	assertSeedsIntact := func(t *testing.T, seeds map[string][]byte) {
		t.Helper()

		for path, want := range seeds {
			got, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("stale sidecar %s was removed: %v", filepath.Base(path), err)

				continue
			}

			if string(got) != string(want) {
				t.Errorf("stale sidecar %s was overwritten: got %q", filepath.Base(path), got)
			}
		}
	}

	t.Run("republish", func(t *testing.T) {
		sm := newTestSessionManagerWithDataDir(t)
		worktree := t.TempDir()
		sessionID := "sidecar-republish"

		if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
			t.Fatalf("first inject: %v", err)
		}

		seeds := seedStaleSidecars(t, worktree)

		// Re-inject with different content so the own-file replace (claim + link)
		// path runs, exercising the claim and preserved reservations.
		if _, _, err := sm.injectCursorHooks(sessionID, worktree, true, cursorProjectAgent(), true); err != nil {
			t.Fatalf("re-inject: %v", err)
		}

		assertSeedsIntact(t, seeds)
	})

	t.Run("cleanup", func(t *testing.T) {
		sm := newTestSessionManagerWithDataDir(t)
		worktree := t.TempDir()
		sessionID := "sidecar-cleanup"

		if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
			t.Fatalf("inject: %v", err)
		}

		seeds := seedStaleSidecars(t, worktree)

		sm.cleanupHooks(sessionID, "cursor", worktree)

		if statExists(t, cursorHooksPath(worktree)) {
			t.Error("last-owner cleanup did not remove hooks.json")
		}

		assertSeedsIntact(t, seeds)
	})
}

// userRace returns a race hook that, when fired at the check/use boundary,
// replaces .cursor/hooks.json with distinct user content exactly once. It proves
// a concurrent external replacement is never overwritten or deleted (#1325).
func userRace(t *testing.T, hooksPath string, userContent []byte) func() {
	t.Helper()

	fired := false

	return func() {
		if fired {
			return
		}

		fired = true

		if err := os.WriteFile(hooksPath, userContent, 0o600); err != nil {
			t.Fatalf("race hook write: %v", err)
		}
	}
}

func withRaceHook(t *testing.T, h func()) {
	t.Helper()

	cursorHooksRaceHook = h

	t.Cleanup(func() { cursorHooksRaceHook = nil })
}

// TestCursorHooksFirstInjectionRaceRefuses is the #1325 first-publication
// regression: if a file appears at .cursor/hooks.json between the absence check
// and the create, graith must refuse rather than overwrite it. Covers ordinary
// and in-place worktrees.
func TestCursorHooksFirstInjectionRaceRefuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inPlace bool
	}{
		{"worktree", false},
		{"in-place worktree", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSessionManagerWithDataDir(t)
			worktree := t.TempDir()

			var userRepoFile string
			if tc.inPlace {
				userRepoFile = filepath.Join(worktree, "README.md")
				if err := os.WriteFile(userRepoFile, []byte("user repo content"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			hooksPath := cursorHooksPath(worktree)
			userContent := []byte(`{"version":1,"hooks":{"stop":[{"command":"user raced this in"}]}}`)

			withRaceHook(t, userRace(t, hooksPath, userContent))

			_, _, err := sm.injectCursorHooks("racer", worktree, false, cursorProjectAgent(), false)
			if err == nil {
				t.Fatal("expected injectCursorHooks to refuse a concurrently-created file")
			}

			if got := readFile(t, hooksPath); got != string(userContent) {
				t.Errorf("concurrently-created file was overwritten; got %q", got)
			}

			// A refused first injection must leave no ownership marker behind.
			if statExists(t, sm.cursorHooksOwnershipPath("racer")) {
				t.Error("refused first injection left an ownership marker")
			}

			if tc.inPlace {
				if !statExists(t, userRepoFile) {
					t.Error("in-place race refusal disturbed an unrelated user file")
				}
			}
		})
	}
}

// TestCursorHooksReinjectRaceRefuses is the #1325 republish regression: when a
// solo session re-injects (its own file changed by config), a concurrent
// replacement landing after graith claims the file must be preserved, not
// overwritten, and the launch must fail.
func TestCursorHooksReinjectRaceRefuses(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	sessionID := "reinject-racer"

	if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("first inject: %v", err)
	}

	hooksPath := cursorHooksPath(worktree)
	userContent := []byte(`{"version":1,"hooks":{"stop":[{"command":"user replaced mid-republish"}]}}`)

	withRaceHook(t, userRace(t, hooksPath, userContent))

	// Re-inject with DIFFERENT content (approval hooks on) so publish runs.
	_, _, err := sm.injectCursorHooks(sessionID, worktree, true, cursorProjectAgent(), true)
	if err == nil {
		t.Fatal("expected re-inject to refuse when the file is replaced mid-republish")
	}

	if got := readFile(t, hooksPath); got != string(userContent) {
		t.Errorf("concurrent replacement overwritten during republish; got %q", got)
	}
}

// TestCursorHooksCleanupRaceReplacement is the #1325 cleanup regression: if the
// file is replaced between the ownership check and the pathname removal, the
// concurrent replacement must never be deleted. Covers ordinary and in-place
// worktrees.
func TestCursorHooksCleanupRaceReplacement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inPlace bool
	}{
		{"worktree", false},
		{"in-place worktree", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSessionManagerWithDataDir(t)
			worktree := t.TempDir()
			sessionID := "cleanup-racer"

			var userRepoFile string
			if tc.inPlace {
				userRepoFile = filepath.Join(worktree, "README.md")
				if err := os.WriteFile(userRepoFile, []byte("user repo content"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
				t.Fatalf("inject: %v", err)
			}

			hooksPath := cursorHooksPath(worktree)
			userContent := []byte(`{"version":1,"hooks":{"stop":[{"command":"user replaced during cleanup"}]}}`)

			withRaceHook(t, userRace(t, hooksPath, userContent))

			sm.cleanupHooks(sessionID, "cursor", worktree)

			if got := readFile(t, hooksPath); got != string(userContent) {
				t.Errorf("cleanup deleted or overwrote the concurrent replacement; got %q", got)
			}

			if tc.inPlace {
				if !statExists(t, userRepoFile) {
					t.Error("in-place cleanup race disturbed an unrelated user file")
				}
			}
		})
	}
}
