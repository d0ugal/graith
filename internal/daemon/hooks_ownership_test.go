package daemon

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"golang.org/x/sys/unix"
)

func testCursorHooksPath(worktree string) string {
	return filepath.Join(worktree, ".cursor", "hooks.json")
}

func readCursorHooks(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return data
}

// replaceCursorHooks atomically swaps a complete file into hooksPath. Tests use
// it from the production seam so the interleaving is deterministic and changes
// the pathname to a different file object (rather than truncating in place).
func replaceCursorHooks(t *testing.T, hooksPath string, data []byte) {
	t.Helper()

	f, err := os.CreateTemp(filepath.Dir(hooksPath), "canny-user-hooks-*")
	if err != nil {
		t.Fatalf("create user replacement: %v", err)
	}

	tmpPath := f.Name()

	t.Cleanup(func() { _ = os.Remove(tmpPath) })

	if _, err := f.Write(data); err != nil {
		_ = f.Close()

		t.Fatalf("write user replacement: %v", err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("close user replacement: %v", err)
	}

	if err := os.Rename(tmpPath, hooksPath); err != nil {
		t.Fatalf("replace cursor hooks: %v", err)
	}
}

func installCursorHooksRace(t *testing.T, point cursorHooksRacePoint, fn func()) *bool {
	t.Helper()

	original := cursorHooksRaceHook
	fired := false
	cursorHooksRaceHook = func(got cursorHooksRacePoint) {
		if got != point || fired {
			return
		}

		fired = true

		fn()
	}

	t.Cleanup(func() { cursorHooksRaceHook = original })

	return &fired
}

func TestInjectCursorHooksRefusesPreExistingUserFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inPlace bool
	}{
		{name: "worktree"},
		{name: "in-place worktree", inPlace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			sm := newTestSessionManagerWithDataDir(t)
			worktree := t.TempDir()
			hooksPath := testCursorHooksPath(worktree)

			if err := os.MkdirAll(filepath.Dir(hooksPath), 0o700); err != nil {
				t.Fatal(err)
			}

			if tc.inPlace {
				if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("braw user repo\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			userContent := []byte(`{"version":1,"hooks":{"stop":[{"command":"canny user hook"}]}}`)
			if err := os.WriteFile(hooksPath, userContent, 0o600); err != nil {
				t.Fatal(err)
			}

			_, _, err := sm.injectCursorHooks("unowned-bothy", worktree)
			if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
				t.Fatalf("injectCursorHooks() error = %v, want refusal", err)
			}

			// Session-create rollback calls cleanup even after injection fails. A
			// markerless pre-existing file must survive that path too.
			sm.cleanupHooks("unowned-bothy", "cursor", worktree)

			if got := readCursorHooks(t, hooksPath); string(got) != string(userContent) {
				t.Fatalf("pre-existing user hooks changed: got %q", got)
			}
		})
	}
}

func TestCursorHooksFirstPublicationRacePreservesReplacement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inPlace bool
	}{
		{name: "worktree"},
		{name: "in-place worktree", inPlace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			sm := newTestSessionManagerWithDataDir(t)
			worktree := t.TempDir()
			hooksPath := testCursorHooksPath(worktree)
			userContent := []byte(`{"version":1,"hooks":{"stop":[{"command":"dreich first-publish replacement"}]}}`)

			if tc.inPlace {
				if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("croft content\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			fired := installCursorHooksRace(t, cursorHooksBeforeCreate, func() {
				replaceCursorHooks(t, hooksPath, userContent)
			})

			_, _, err := sm.injectCursorHooks("first-racer", worktree)
			if !errors.Is(err, errCursorHooksRaced) {
				t.Fatalf("injectCursorHooks() error = %v, want %v", err, errCursorHooksRaced)
			}

			if !*fired {
				t.Fatal("first-publication race seam did not fire")
			}

			if got := readCursorHooks(t, hooksPath); string(got) != string(userContent) {
				t.Fatalf("concurrent user file changed: got %q", got)
			}

			if _, err := os.Stat(sm.cursorHooksOwnershipPath("first-racer")); !os.IsNotExist(err) {
				t.Fatalf("failed publication left an ownership marker: %v", err)
			}
		})
	}
}

func TestCursorHooksRepublicationRacePreservesReplacement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		point   cursorHooksRacePoint
		inPlace bool
	}{
		{name: "worktree replaced before claim", point: cursorHooksBeforeClaim},
		{name: "worktree recreated after claim", point: cursorHooksAfterClaim},
		{name: "in-place worktree replaced before claim", point: cursorHooksBeforeClaim, inPlace: true},
		{name: "in-place worktree recreated after claim", point: cursorHooksAfterClaim, inPlace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			sm := newTestSessionManagerWithDataDir(t)
			worktree := t.TempDir()
			sessionID := "republish-racer"
			hooksPath := testCursorHooksPath(worktree)
			sm.state.Sessions[sessionID] = &SessionState{
				ID: sessionID, Agent: "cursor", WorktreePath: worktree, Status: StatusRunning,
			}

			if tc.inPlace {
				if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("strath content\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if _, _, err := sm.injectCursorHooksWithGrBin(sessionID, worktree, "/opt/braw/gr"); err != nil {
				t.Fatalf("first injectCursorHooks(): %v", err)
			}

			userContent := []byte(`{"version":1,"hooks":{"stop":[{"command":"thrawn republish replacement"}]}}`)
			fired := installCursorHooksRace(t, tc.point, func() {
				replaceCursorHooks(t, hooksPath, userContent)
			})

			// A changed Graith binary path forces a real replacement rather than the
			// byte-identical no-op reinjection path.
			_, _, err := sm.injectCursorHooksWithGrBin(sessionID, worktree, "/opt/canny/gr")
			if !errors.Is(err, errCursorHooksRaced) {
				t.Fatalf("re-inject error = %v, want %v", err, errCursorHooksRaced)
			}

			if !*fired {
				t.Fatal("republish race seam did not fire")
			}

			if got := readCursorHooks(t, hooksPath); string(got) != string(userContent) {
				t.Fatalf("concurrent user replacement changed: got %q", got)
			}

			// Failed-launch cleanup must also preserve the replacement.
			sm.cleanupHooks(sessionID, "cursor", worktree)

			if got := readCursorHooks(t, hooksPath); string(got) != string(userContent) {
				t.Fatalf("cleanup changed concurrent user replacement: got %q", got)
			}
		})
	}
}

func TestCursorHooksSuccessfulRepublication(t *testing.T) {
	for _, tc := range []struct {
		name             string
		hardLinkFallback bool
	}{
		{name: "hard link"},
		{name: "exclusive-create fallback", hardLinkFallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			sm := newTestSessionManagerWithDataDir(t)
			worktree := t.TempDir()
			sessionID := "successful-republish"
			sm.state.Sessions[sessionID] = &SessionState{
				ID: sessionID, Agent: "cursor", WorktreePath: worktree, Status: StatusRunning,
			}

			if tc.hardLinkFallback {
				original := linkCursorHooksFile
				linkCursorHooksFile = func(_, _ string) error { return errors.ErrUnsupported }

				t.Cleanup(func() { linkCursorHooksFile = original })
			}

			if _, _, err := sm.injectCursorHooksWithGrBin(sessionID, worktree, "/opt/braw/gr"); err != nil {
				t.Fatalf("first injectCursorHooks(): %v", err)
			}

			before := readCursorHooks(t, testCursorHooksPath(worktree))
			if !strings.Contains(string(before), "/opt/braw/gr") {
				t.Fatal("first publication does not contain original Graith binary")
			}

			if _, _, err := sm.injectCursorHooksWithGrBin(sessionID, worktree, "/opt/canny/gr"); err != nil {
				t.Fatalf("re-injectCursorHooks(): %v", err)
			}

			after := readCursorHooks(t, testCursorHooksPath(worktree))
			if !strings.Contains(string(after), "/opt/canny/gr") {
				t.Fatal("republication did not install updated Graith binary")
			}

			marker := readCursorHooks(t, sm.cursorHooksOwnershipPath(sessionID))

			ownership, err := decodeCursorHooksOwnership(marker, "")
			if err != nil {
				t.Fatalf("decode ownership marker: %v", err)
			}

			if ownership.SHA256 != sha256Hex(after) {
				t.Fatalf("ownership hash = %q, want hash of republished file", ownership.SHA256)
			}

			if ownership.WorktreePath != canonicalCursorHooksWorktree(worktree) {
				t.Fatalf("ownership worktree = %q, want %q", ownership.WorktreePath, worktree)
			}
		})
	}
}

func TestInjectCursorHooksSharesIdenticalDefinition(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()

	for _, id := range []string{"braw-owner", "canny-owner"} {
		sm.state.Sessions[id] = &SessionState{
			ID: id, Agent: "cursor", WorktreePath: worktree, Status: StatusCreating,
		}
	}

	if _, _, err := sm.injectCursorHooks("braw-owner", worktree); err != nil {
		t.Fatalf("inject first owner: %v", err)
	}

	want := readCursorHooks(t, testCursorHooksPath(worktree))
	if _, _, err := sm.injectCursorHooks("canny-owner", worktree); err != nil {
		t.Fatalf("inject second owner: %v", err)
	}

	if got := readCursorHooks(t, testCursorHooksPath(worktree)); string(got) != string(want) {
		t.Fatalf("shared hooks changed: got %q, want %q", got, want)
	}

	for _, id := range []string{"braw-owner", "canny-owner"} {
		marker := readCursorHooks(t, sm.cursorHooksOwnershipPath(id))

		ownership, err := decodeCursorHooksOwnership(marker, "")
		if err != nil {
			t.Fatalf("decode %s ownership: %v", id, err)
		}

		if ownership.SHA256 != sha256Hex(want) {
			t.Errorf("%s ownership hash = %q, want %q", id, ownership.SHA256, sha256Hex(want))
		}
	}
}

func TestInjectCursorHooksRejectsIncompatibleSharedDefinition(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()

	for _, id := range []string{"braw-owner", "dreich-owner"} {
		sm.state.Sessions[id] = &SessionState{
			ID: id, Agent: "cursor", WorktreePath: worktree, Status: StatusCreating,
		}
	}

	if _, _, err := sm.injectCursorHooks("braw-owner", worktree); err != nil {
		t.Fatalf("inject first owner: %v", err)
	}

	want := readCursorHooks(t, testCursorHooksPath(worktree))

	_, _, err := sm.injectCursorHooksWithGrBin("dreich-owner", worktree, "/opt/dreich/gr")
	if err == nil || !strings.Contains(err.Error(), "shared by session(s) braw-owner") || !strings.Contains(err.Error(), "different hook definition") {
		t.Fatalf("incompatible injection error = %v, want shared-definition refusal", err)
	}

	if got := readCursorHooks(t, testCursorHooksPath(worktree)); string(got) != string(want) {
		t.Fatalf("incompatible injection changed hooks: got %q, want %q", got, want)
	}

	if _, err := os.Stat(sm.cursorHooksOwnershipPath("dreich-owner")); !os.IsNotExist(err) {
		t.Fatalf("incompatible injection left ownership marker: %v", err)
	}
}

func TestInjectCursorHooksReconcilesMissingSharedArtifact(t *testing.T) {
	for _, tc := range []struct {
		name       string
		different  bool
		wantErr    bool
		wantAbsent bool
	}{
		{name: "republish compatible definition"},
		{name: "reject incompatible definition", different: true, wantErr: true, wantAbsent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			sm := newTestSessionManagerWithDataDir(t)
			worktree := t.TempDir()
			hooksPath := testCursorHooksPath(worktree)

			for _, id := range []string{"braw-owner", "dreich-owner"} {
				sm.state.Sessions[id] = &SessionState{
					ID: id, Agent: "cursor", WorktreePath: worktree, Status: StatusCreating,
				}
			}

			if _, _, err := sm.injectCursorHooksWithGrBin("braw-owner", worktree, "/opt/braw/gr"); err != nil {
				t.Fatalf("inject first owner: %v", err)
			}

			if err := os.Remove(hooksPath); err != nil {
				t.Fatalf("remove generated hooks: %v", err)
			}

			grBin := "/opt/braw/gr"
			if tc.different {
				grBin = "/opt/dreich/gr"
			}

			_, _, err := sm.injectCursorHooksWithGrBin("dreich-owner", worktree, grBin)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "braw-owner") || !strings.Contains(err.Error(), "different hook definition") {
					t.Fatalf("incompatible injection error = %v, want durable-owner refusal", err)
				}
			} else if err != nil {
				t.Fatalf("republish compatible definition: %v", err)
			}

			_, statErr := os.Stat(hooksPath)
			if tc.wantAbsent && !os.IsNotExist(statErr) {
				t.Fatalf("incompatible injection published hooks: %v", statErr)
			}

			if !tc.wantAbsent && statErr != nil {
				t.Fatalf("compatible injection did not republish hooks: %v", statErr)
			}

			_, markerErr := os.Stat(sm.cursorHooksOwnershipPath("dreich-owner"))
			if tc.wantErr && !os.IsNotExist(markerErr) {
				t.Fatalf("incompatible injection left ownership marker: %v", markerErr)
			}

			if !tc.wantErr && markerErr != nil {
				t.Fatalf("compatible injection did not record ownership: %v", markerErr)
			}
		})
	}
}

func TestInjectCursorHooksRefusesStaleStructuredOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	hooksPath := testCursorHooksPath(worktree)

	if _, _, err := sm.injectCursorHooks("stale-owner", worktree); err != nil {
		t.Fatalf("inject stale owner: %v", err)
	}

	generated := readCursorHooks(t, hooksPath)
	if err := os.Remove(hooksPath); err != nil {
		t.Fatalf("remove original generated hooks: %v", err)
	}

	// Model a crash after cleanup made the public pathname's absence durable but
	// before it removed the marker, followed by a user recreating byte-identical
	// content. The stale hash must not regain authority over this new file object.
	if err := os.WriteFile(hooksPath, generated, 0o600); err != nil {
		t.Fatalf("recreate user hooks: %v", err)
	}

	for _, tc := range []struct {
		name      string
		sessionID string
	}{
		{name: "identical definition", sessionID: "bairn-owner"},
		{name: "another session", sessionID: "thrawn-owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := sm.injectCursorHooks(tc.sessionID, worktree)
			if err == nil || !strings.Contains(err.Error(), "not owned by graith") {
				t.Fatalf("inject with stale owner error = %v, want ownership refusal", err)
			}

			if _, err := os.Stat(sm.cursorHooksOwnershipPath(tc.sessionID)); !os.IsNotExist(err) {
				t.Fatalf("refused injection left ownership marker: %v", err)
			}

			if got := readCursorHooks(t, hooksPath); string(got) != string(generated) {
				t.Fatalf("refused injection changed user hooks: got %q", got)
			}
		})
	}

	if _, err := os.Stat(sm.cursorHooksOwnershipPath("stale-owner")); !os.IsNotExist(err) {
		t.Fatalf("refused injection did not retire stale marker: %v", err)
	}

	sm.cleanupHooks("bairn-owner", "cursor", worktree)

	if got := readCursorHooks(t, hooksPath); string(got) != string(generated) {
		t.Fatalf("cleanup after refusal changed user hooks: got %q", got)
	}
}

func TestInjectCursorHooksRejectsMalformedSharedOwnership(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	hooksPath := testCursorHooksPath(worktree)

	for _, id := range []string{"braw-owner", "dreich-owner", "canny-owner"} {
		sm.state.Sessions[id] = &SessionState{
			ID: id, Agent: "cursor", WorktreePath: worktree, Status: StatusRunning,
		}
	}

	if _, _, err := sm.injectCursorHooks("braw-owner", worktree); err != nil {
		t.Fatalf("inject valid owner: %v", err)
	}

	want := readCursorHooks(t, hooksPath)

	malformedPath := sm.cursorHooksOwnershipPath("dreich-owner")
	if err := os.MkdirAll(filepath.Dir(malformedPath), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(malformedPath, []byte(`{"version":1,"worktree_path":"blether"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := sm.injectCursorHooks("canny-owner", worktree)
	if err == nil || !strings.Contains(err.Error(), "ownership metadata is unreadable") {
		t.Fatalf("identical join with malformed peer error = %v, want fail-closed refusal", err)
	}

	if _, err := os.Stat(sm.cursorHooksOwnershipPath("canny-owner")); !os.IsNotExist(err) {
		t.Fatalf("refused join left ownership marker: %v", err)
	}

	delete(sm.state.Sessions, "braw-owner")
	sm.cleanupHooks("braw-owner", "cursor", worktree)

	if got := readCursorHooks(t, hooksPath); string(got) != string(want) {
		t.Fatalf("cleanup with malformed peer changed hooks: got %q, want %q", got, want)
	}
}

func TestInjectCursorHooksRejectsBlockingOwnershipMarker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		symlink bool
	}{
		{name: "fifo"},
		{name: "symlink to fifo", symlink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			sm := newTestSessionManagerWithDataDir(t)
			worktree := t.TempDir()

			for _, id := range []string{"braw-owner", "dreich-owner", "canny-owner"} {
				sm.state.Sessions[id] = &SessionState{
					ID: id, Agent: "cursor", WorktreePath: worktree, Status: StatusRunning,
				}
			}

			if _, _, err := sm.injectCursorHooks("braw-owner", worktree); err != nil {
				t.Fatalf("inject valid owner: %v", err)
			}

			markerPath := sm.cursorHooksOwnershipPath("dreich-owner")
			if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
				t.Fatal(err)
			}

			fifoPath := markerPath
			if tc.symlink {
				fifoPath = filepath.Join(t.TempDir(), "ownership-fifo")
			}

			if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
				t.Fatalf("create ownership FIFO: %v", err)
			}

			if tc.symlink {
				if err := os.Symlink(fifoPath, markerPath); err != nil {
					t.Fatalf("symlink ownership FIFO: %v", err)
				}
			}

			done := make(chan error, 1)

			go func() {
				_, _, err := sm.injectCursorHooks("canny-owner", worktree)
				done <- err
			}()

			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "ownership metadata is unreadable") {
					t.Fatalf("join with blocking marker error = %v, want fail-closed refusal", err)
				}
			case <-time.After(time.Second):
				t.Fatal("ownership scan blocked on non-regular marker")
			}
		})
	}
}

func TestSharedCursorHooksCleanupPreservesUserModification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()

	for _, id := range []string{"braw-owner", "canny-owner"} {
		sm.state.Sessions[id] = &SessionState{
			ID: id, Agent: "cursor", WorktreePath: worktree, Status: StatusRunning,
		}

		if _, _, err := sm.injectCursorHooks(id, worktree); err != nil {
			t.Fatalf("inject %s: %v", id, err)
		}
	}

	userContent := []byte(`{"version":1,"hooks":{"stop":[{"command":"blether shared user edit"}]}}`)
	replaceCursorHooks(t, testCursorHooksPath(worktree), userContent)

	delete(sm.state.Sessions, "braw-owner")
	sm.cleanupHooks("braw-owner", "cursor", worktree)
	delete(sm.state.Sessions, "canny-owner")
	sm.cleanupHooks("canny-owner", "cursor", worktree)

	if got := readCursorHooks(t, testCursorHooksPath(worktree)); string(got) != string(userContent) {
		t.Fatalf("shared cleanup changed user modification: got %q", got)
	}
}

func TestInjectCursorHooksReadsLegacyOwnershipMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	sessionID := "legacy-owner"
	sm.state.Sessions[sessionID] = &SessionState{
		ID: sessionID, Agent: "cursor", WorktreePath: worktree, Status: StatusRunning,
	}

	if _, _, err := sm.injectCursorHooks(sessionID, worktree); err != nil {
		t.Fatalf("inject owner: %v", err)
	}

	hooks := readCursorHooks(t, testCursorHooksPath(worktree))
	if err := os.WriteFile(sm.cursorHooksOwnershipPath(sessionID), []byte(sha256Hex(hooks)), 0o600); err != nil {
		t.Fatalf("write legacy marker: %v", err)
	}

	if _, _, err := sm.injectCursorHooks(sessionID, worktree); err != nil {
		t.Fatalf("reinject with legacy marker: %v", err)
	}

	marker := readCursorHooks(t, sm.cursorHooksOwnershipPath(sessionID))
	if !strings.HasPrefix(string(marker), "{\"") {
		t.Fatalf("legacy marker was not upgraded: %q", marker)
	}
}

func TestInjectCursorHooksRefusesByteIdenticalUnownedFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	generatedWorktree := t.TempDir()

	if _, _, err := sm.injectCursorHooks("source-owner", generatedWorktree); err != nil {
		t.Fatalf("generate hooks fixture: %v", err)
	}

	generated := readCursorHooks(t, testCursorHooksPath(generatedWorktree))
	userWorktree := t.TempDir()

	userHooksPath := testCursorHooksPath(userWorktree)
	if err := os.MkdirAll(filepath.Dir(userHooksPath), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(userHooksPath, generated, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := sm.injectCursorHooks("unowned-bothy", userWorktree); err == nil || !strings.Contains(err.Error(), "not owned by graith") {
		t.Fatalf("byte-identical unowned injection error = %v, want refusal", err)
	}

	sm.cleanupHooks("unowned-bothy", "cursor", userWorktree)

	if got := readCursorHooks(t, userHooksPath); string(got) != string(generated) {
		t.Fatalf("byte-identical user hooks changed: got %q", got)
	}
}

func cursorHooksLifecyclePaths(dataDir string) config.Paths {
	return config.Paths{
		StateFile:  filepath.Join(dataDir, "state.json"),
		DataDir:    dataDir,
		LogDir:     dataDir,
		RuntimeDir: dataDir,
		TmpDir:     filepath.Join(dataDir, "tmp"),
	}
}

func newCursorHooksLifecycleManager(t *testing.T, repoDir, dataDir, command string) *SessionManager {
	t.Helper()

	disabled := false
	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.AgentPrompt = ""
	cfg.Agents["cursor"] = config.Agent{
		Command: command, Args: []string{"-c", "exec cat"}, ResumeArgs: []string{"-c", "exec cat"},
		NonInteractiveArgs: []string{}, InjectPrompt: &disabled, PreTrustWorkspace: &disabled,
	}
	cfg.Repos = []config.RepoConfig{{Path: repoDir, AllowConcurrent: true}}

	sm := NewSessionManager(cfg, cursorHooksLifecyclePaths(dataDir), slog.Default())
	sm.sandboxResolver = func(string) (bool, error) { return false, nil }

	return sm
}

func createConcurrentCursorSession(t *testing.T, sm *SessionManager, id, name, repoDir string) SessionState {
	t.Helper()

	created, err := sm.Create(CreateOpts{
		ID: id, Name: name, AgentName: "cursor", RepoPath: repoDir,
		InPlace: true, AllowConcurrent: true, AgentHooks: true,
		SkipModelValidation: true, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create(%s): %v", name, err)
	}

	return created
}

func TestConcurrentCursorHooksCreateDeleteBothOrders(t *testing.T) {
	for _, tc := range []struct {
		name        string
		firstDelete int
	}{
		{name: "first owner then second", firstDelete: 0},
		{name: "second owner then first", firstDelete: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repoDir := initTempGitRepo(t)
			sm := newCursorHooksLifecycleManager(t, repoDir, t.TempDir(), "sh")
			sessions := []SessionState{
				createConcurrentCursorSession(t, sm, "c0ffee01", "braw", repoDir),
				createConcurrentCursorSession(t, sm, "c0ffee02", "canny", repoDir),
			}

			hooksPath := testCursorHooksPath(repoDir)
			if _, err := os.Stat(hooksPath); err != nil {
				t.Fatalf("shared hooks missing after create: %v", err)
			}

			first := sessions[tc.firstDelete]
			last := sessions[1-tc.firstDelete]

			if err := sm.Delete(first.ID); err != nil {
				t.Fatalf("Delete(%s): %v", first.Name, err)
			}

			if _, err := os.Stat(hooksPath); err != nil {
				t.Fatalf("first delete removed shared hooks: %v", err)
			}

			if err := sm.Delete(last.ID); err != nil {
				t.Fatalf("Delete(%s): %v", last.Name, err)
			}

			if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
				t.Fatalf("last delete left shared hooks: %v", err)
			}
		})
	}
}

func TestConcurrentCursorHooksSurviveDaemonRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repoDir := initTempGitRepo(t)
	dataDir := t.TempDir()
	sm := newCursorHooksLifecycleManager(t, repoDir, dataDir, "sh")
	first := createConcurrentCursorSession(t, sm, "c0ffee03", "dreich", repoDir)

	if err := sm.Stop(first.ID); err != nil {
		t.Fatalf("Stop(first): %v", err)
	}

	waitForStatus(t, sm, first.ID, StatusStopped)

	restarted := newCursorHooksLifecycleManager(t, repoDir, dataDir, "sh")
	if err := restarted.LoadState(); err != nil {
		t.Fatalf("LoadState after restart: %v", err)
	}

	second := createConcurrentCursorSession(t, restarted, "c0ffee04", "thrawn", repoDir)
	if err := restarted.Stop(second.ID); err != nil {
		t.Fatalf("Stop(second): %v", err)
	}

	waitForStatus(t, restarted, second.ID, StatusStopped)

	// Restart again with both owner records persisted. Deletion after this point
	// exercises refcount reconstruction without any in-memory ownership state.
	restarted = newCursorHooksLifecycleManager(t, repoDir, dataDir, "sh")
	if err := restarted.LoadState(); err != nil {
		t.Fatalf("LoadState with two owners: %v", err)
	}

	hooksPath := testCursorHooksPath(repoDir)

	if err := restarted.Delete(first.ID); err != nil {
		t.Fatalf("Delete(first) after restart: %v", err)
	}

	if _, err := os.Stat(hooksPath); err != nil {
		t.Fatalf("restart owner delete removed shared hooks: %v", err)
	}

	if err := restarted.Delete(second.ID); err != nil {
		t.Fatalf("Delete(second) after restart: %v", err)
	}

	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("last owner after restart left hooks: %v", err)
	}
}

func TestConcurrentCursorHooksFailedLaunchReleasesOnlyJoiningOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repoDir := initTempGitRepo(t)
	sm := newCursorHooksLifecycleManager(t, repoDir, t.TempDir(), "sh")
	first := createConcurrentCursorSession(t, sm, "c0ffee05", "croft", repoDir)

	sm.mu.Lock()
	broken := sm.cfg.Agents["cursor"]
	broken.Command = filepath.Join(t.TempDir(), "missing-cursor")
	sm.cfg.Agents["cursor"] = broken
	sm.mu.Unlock()

	_, err := sm.Create(CreateOpts{
		ID: "c0ffee06", Name: "bothy", AgentName: "cursor", RepoPath: repoDir,
		InPlace: true, AllowConcurrent: true, AgentHooks: true,
		SkipModelValidation: true, Rows: 24, Cols: 80,
	})
	if err == nil || !strings.Contains(err.Error(), "start pty session") {
		t.Fatalf("failed joining launch error = %v, want PTY start failure", err)
	}

	if _, err := os.Stat(testCursorHooksPath(repoDir)); err != nil {
		t.Fatalf("failed joining launch removed first owner's hooks: %v", err)
	}

	if _, err := os.Stat(sm.cursorHooksOwnershipPath("c0ffee06")); !os.IsNotExist(err) {
		t.Fatalf("failed joining launch left marker: %v", err)
	}

	if _, ok := sm.Get("c0ffee06"); ok {
		t.Fatal("failed joining launch remained in session state")
	}

	if err := sm.Delete(first.ID); err != nil {
		t.Fatalf("Delete(first): %v", err)
	}

	if _, err := os.Stat(testCursorHooksPath(repoDir)); !os.IsNotExist(err) {
		t.Fatalf("last owner after failed launch left hooks: %v", err)
	}
}

func TestCursorHooksCleanupRacePreservesReplacement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		point   cursorHooksRacePoint
		inPlace bool
	}{
		{name: "worktree replaced before claim", point: cursorHooksBeforeClaim},
		{name: "worktree recreated after claim", point: cursorHooksAfterClaim},
		{name: "in-place worktree replaced before claim", point: cursorHooksBeforeClaim, inPlace: true},
		{name: "in-place worktree recreated after claim", point: cursorHooksAfterClaim, inPlace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			sm := newTestSessionManagerWithDataDir(t)
			worktree := t.TempDir()
			sessionID := "cleanup-racer"
			hooksPath := testCursorHooksPath(worktree)

			if tc.inPlace {
				if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("bothy content\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if _, _, err := sm.injectCursorHooks(sessionID, worktree); err != nil {
				t.Fatalf("injectCursorHooks(): %v", err)
			}

			userContent := []byte(`{"version":1,"hooks":{"stop":[{"command":"haar cleanup replacement"}]}}`)
			fired := installCursorHooksRace(t, tc.point, func() {
				replaceCursorHooks(t, hooksPath, userContent)
			})

			sm.cleanupHooks(sessionID, "cursor", worktree)

			if !*fired {
				t.Fatal("cleanup race seam did not fire")
			}

			if got := readCursorHooks(t, hooksPath); string(got) != string(userContent) {
				t.Fatalf("cleanup changed concurrent user replacement: got %q", got)
			}

			if tc.inPlace {
				if _, err := os.Stat(filepath.Join(worktree, "README.md")); err != nil {
					t.Fatalf("cleanup disturbed in-place repo content: %v", err)
				}
			}
		})
	}
}

func TestCursorHooksCleanupPreservesUserModification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	sessionID := "modified-bairn"
	hooksPath := testCursorHooksPath(worktree)

	if _, _, err := sm.injectCursorHooks(sessionID, worktree); err != nil {
		t.Fatalf("injectCursorHooks(): %v", err)
	}

	userContent := []byte(`{"version":1,"hooks":{"stop":[{"command":"blether user edit"}]}}`)
	replaceCursorHooks(t, hooksPath, userContent)

	sm.cleanupHooks(sessionID, "cursor", worktree)

	if got := readCursorHooks(t, hooksPath); string(got) != string(userContent) {
		t.Fatalf("cleanup changed user modification: got %q", got)
	}
}
