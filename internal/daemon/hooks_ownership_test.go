package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
			approvalsDisabled := false
			sm.cfg.Approvals.Enabled = &approvalsDisabled
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

			_, _, err := sm.injectCursorHooks("unowned-bothy", worktree, false)
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

			_, _, err := sm.injectCursorHooks("first-racer", worktree, false)
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
			approvalsDisabled := false
			sm.cfg.Approvals.Enabled = &approvalsDisabled
			worktree := t.TempDir()
			sessionID := "republish-racer"
			hooksPath := testCursorHooksPath(worktree)

			if tc.inPlace {
				if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("strath content\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if _, _, err := sm.injectCursorHooks(sessionID, worktree, false); err != nil {
				t.Fatalf("first injectCursorHooks(): %v", err)
			}

			userContent := []byte(`{"version":1,"hooks":{"stop":[{"command":"thrawn republish replacement"}]}}`)
			fired := installCursorHooksRace(t, tc.point, func() {
				replaceCursorHooks(t, hooksPath, userContent)
			})

			// Yolo adds preToolUse, forcing a real replacement rather than the
			// byte-identical no-op reinjection path.
			_, _, err := sm.injectCursorHooks(sessionID, worktree, true)
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
			approvalsDisabled := false
			sm.cfg.Approvals.Enabled = &approvalsDisabled
			worktree := t.TempDir()
			sessionID := "successful-republish"

			if tc.hardLinkFallback {
				original := linkCursorHooksFile
				linkCursorHooksFile = func(_, _ string) error { return errors.ErrUnsupported }

				t.Cleanup(func() { linkCursorHooksFile = original })
			}

			if _, _, err := sm.injectCursorHooks(sessionID, worktree, false); err != nil {
				t.Fatalf("first injectCursorHooks(): %v", err)
			}

			before := readCursorHooks(t, testCursorHooksPath(worktree))
			if strings.Contains(string(before), "preToolUse") {
				t.Fatal("first publication unexpectedly contains preToolUse")
			}

			if _, _, err := sm.injectCursorHooks(sessionID, worktree, true); err != nil {
				t.Fatalf("re-injectCursorHooks(): %v", err)
			}

			after := readCursorHooks(t, testCursorHooksPath(worktree))
			if !strings.Contains(string(after), "preToolUse") {
				t.Fatal("republication did not install preToolUse")
			}

			marker := readCursorHooks(t, sm.cursorHooksOwnershipPath(sessionID))
			if string(marker) != sha256Hex(after) {
				t.Fatalf("ownership marker = %q, want hash of republished file", marker)
			}
		})
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

			if _, _, err := sm.injectCursorHooks(sessionID, worktree, false); err != nil {
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

	if _, _, err := sm.injectCursorHooks(sessionID, worktree, false); err != nil {
		t.Fatalf("injectCursorHooks(): %v", err)
	}

	userContent := []byte(`{"version":1,"hooks":{"stop":[{"command":"blether user edit"}]}}`)
	replaceCursorHooks(t, hooksPath, userContent)

	sm.cleanupHooks(sessionID, "cursor", worktree)

	if got := readCursorHooks(t, hooksPath); string(got) != string(userContent) {
		t.Fatalf("cleanup changed user modification: got %q", got)
	}
}
