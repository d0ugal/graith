package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// cursorProjectAgent is a minimal agent that selects the cursor_project hook
// mechanism, standing in for a custom alias/wrapper opting into it from config.
func cursorProjectAgent() config.Agent {
	return config.Agent{
		Command: "cursor",
		Hooks:   &config.AgentHookConfig{Mechanism: config.HookMechanismCursorProject},
	}
}

func cursorHooksPath(worktree string) string {
	return filepath.Join(worktree, ".cursor", "hooks.json")
}

// TestCursorHooksCleanupSurvivesReloadMechanismChange is the round-3 regression
// (issue #1236): a session launched with cursor_project hooks must have its
// generated hooks.json cleaned up even after a config reload changes or drops the
// mechanism for that agent. Cleanup uses the launch-time ownership marker, not
// current config. Covers a worktree and an in-place worktree.
func TestCursorHooksCleanupSurvivesReloadMechanismChange(t *testing.T) {
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
			sessionID := "bide-cursor"

			// An in-place session runs in the user's actual repo (worktree == repo
			// root), so seed a user-owned file that cleanup must never touch, plus a
			// user file directly under .cursor to prove cleanup removes only the
			// generated hooks.json and prunes .cursor only when empty.
			userRepoFile := filepath.Join(worktree, "README.md")
			userCursorFile := filepath.Join(worktree, ".cursor", "user-notes.txt")

			if tc.inPlace {
				if err := os.WriteFile(userRepoFile, []byte("user repo content"), 0o600); err != nil {
					t.Fatal(err)
				}

				if err := os.MkdirAll(filepath.Join(worktree, ".cursor"), 0o700); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(userCursorFile, []byte("user cursor notes"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
				t.Fatalf("injectCursorHooks: %v", err)
			}

			if _, err := os.Stat(cursorHooksPath(worktree)); err != nil {
				t.Fatalf("hooks.json not generated: %v", err)
			}

			// Simulate a reload that removes the cursor_project mechanism for this
			// agent: cleanup must still remove the generated artifact via the marker.
			sm.cfg.Agents["thrawn"] = config.Agent{Command: "cursor"} // no hooks mechanism

			sm.cleanupHooks(sessionID, "thrawn", worktree)

			if _, err := os.Stat(cursorHooksPath(worktree)); !os.IsNotExist(err) {
				t.Errorf("hooks.json not cleaned up after reload; stat err = %v", err)
			}

			if tc.inPlace {
				if _, err := os.Stat(userRepoFile); err != nil {
					t.Errorf("in-place cleanup deleted a user repo file: %v", err)
				}

				// A user file under .cursor must survive, and .cursor must not be
				// pruned while it holds user content.
				if _, err := os.Stat(userCursorFile); err != nil {
					t.Errorf("in-place cleanup deleted a user .cursor file: %v", err)
				}
			}
		})
	}
}

// TestCursorHooksCleanupPreservesUserModification proves cleanup never deletes a
// hooks.json the user modified after launch: the recorded hash no longer matches,
// so the file is left in place (issue #1236).
func TestCursorHooksCleanupPreservesUserModification(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	sessionID := "canny-cursor"

	if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("injectCursorHooks: %v", err)
	}

	// User edits the generated file.
	if err := os.WriteFile(cursorHooksPath(worktree), []byte(`{"version":1,"hooks":{"stop":[{"command":"my own hook"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	sm.cleanupHooks(sessionID, "cursor", worktree)

	if _, err := os.Stat(cursorHooksPath(worktree)); err != nil {
		t.Errorf("user-modified hooks.json was deleted; want preserved (err=%v)", err)
	}
}

// TestCursorHooksCleanupTamperedMarkerPreserves proves a tampered ownership
// marker cannot cause deletion: cleanup derives the target from the worktree and
// requires an exact hash match, so a garbage marker leaves the real file intact
// and never deletes an attacker-chosen path (issue #1236).
func TestCursorHooksCleanupTamperedMarkerPreserves(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	sessionID := "thrawn-cursor"

	if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("injectCursorHooks: %v", err)
	}

	// Tamper: overwrite the marker with a bogus path/hash.
	if err := os.WriteFile(sm.cursorHooksOwnershipPath(sessionID), []byte("/etc/passwd"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm.cleanupHooks(sessionID, "cursor", worktree)

	if _, err := os.Stat(cursorHooksPath(worktree)); err != nil {
		t.Errorf("tampered marker caused deletion of the real hooks.json (err=%v)", err)
	}
}

// TestCursorHooksCleanupMarkerUnreadablePreserves proves an unreadable marker
// (not os.ErrNotExist) fails closed: cleanup preserves the file rather than
// falling through to a blind delete (issue #1236).
func TestCursorHooksCleanupMarkerUnreadablePreserves(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	sessionID := "dreich-cursor"

	if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("injectCursorHooks: %v", err)
	}

	// Make the marker path unreadable as a file by replacing it with a directory,
	// so os.ReadFile returns an error that is not os.ErrNotExist.
	markerPath := sm.cursorHooksOwnershipPath(sessionID)
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(markerPath, 0o700); err != nil {
		t.Fatal(err)
	}

	sm.cleanupHooks(sessionID, "cursor", worktree)

	if _, err := os.Stat(cursorHooksPath(worktree)); err != nil {
		t.Errorf("unreadable marker caused deletion; want fail-closed preserve (err=%v)", err)
	}
}

// TestCursorHooksLegacyFingerprint covers the markerless (pre-#1236) path: with
// cursor_project still selected by config, a genuinely graith-structured file is
// removed, but a user-authored v1 file that merely contains the graith command
// substrings is preserved (issue #1236).
func TestCursorHooksLegacyFingerprint(t *testing.T) {
	t.Run("graith-structured file removed", func(t *testing.T) {
		sm := newTestSessionManagerWithDataDir(t)
		worktree := t.TempDir()
		sessionID := "legacy-graith"

		// Generate a real graith file, then delete the marker to simulate a
		// pre-#1236 launch that left no ownership record.
		if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
			t.Fatalf("injectCursorHooks: %v", err)
		}

		if err := os.Remove(sm.cursorHooksOwnershipPath(sessionID)); err != nil {
			t.Fatal(err)
		}

		// Current config still selects cursor_project for this agent.
		sm.cfg.Agents["cursor"] = cursorProjectAgent()

		sm.cleanupHooks(sessionID, "cursor", worktree)

		if _, err := os.Stat(cursorHooksPath(worktree)); !os.IsNotExist(err) {
			t.Errorf("markerless graith file not removed; stat err = %v", err)
		}
	})

	t.Run("prefixed/suffixed exact-command lookalike preserved", func(t *testing.T) {
		sm := newTestSessionManagerWithDataDir(t)
		worktree := t.TempDir()
		sessionID := "legacy-prefix"

		if err := os.MkdirAll(filepath.Join(worktree, ".cursor"), 0o700); err != nil {
			t.Fatal(err)
		}

		// Build graith's exact command set, then wrap each in a larger shell command
		// (echo ...; <graith cmd>). Because matching is exact bytes (not substring),
		// this user file must survive cleanup even though it embeds every graith
		// command verbatim.
		gr := shellQuote(resolveGrBin())
		wrap := func(c string) string { return "echo tampered; " + c }

		user := `{"version":1,"hooks":{` +
			`"sessionStart":[{"command":"` + wrap(gr+" report-status --event SessionStart") + `"},{"command":"` + wrap(gr+" check-inbox") + `"}],` +
			`"postToolUse":[{"command":"` + wrap(gr+" report-status --event PostToolUse") + `"}],` +
			`"stop":[{"command":"` + wrap(gr+" report-status --event Stop") + `"}]}}`

		if err := os.WriteFile(cursorHooksPath(worktree), []byte(user), 0o600); err != nil {
			t.Fatal(err)
		}

		sm.cfg.Agents["cursor"] = cursorProjectAgent()

		sm.cleanupHooks(sessionID, "cursor", worktree)

		if _, err := os.Stat(cursorHooksPath(worktree)); err != nil {
			t.Errorf("prefixed exact-command lookalike deleted; want preserved (err=%v)", err)
		}
	})

	t.Run("user-authored lookalike preserved", func(t *testing.T) {
		sm := newTestSessionManagerWithDataDir(t)
		worktree := t.TempDir()
		sessionID := "legacy-user"

		if err := os.MkdirAll(filepath.Join(worktree, ".cursor"), 0o700); err != nil {
			t.Fatal(err)
		}

		// A v1 file that contains BOTH graith substrings but with extra events and
		// entries — not graith's exact structure, so it must survive.
		user := `{"version":1,"hooks":{` +
			`"sessionStart":[{"command":"gr report-status --event SessionStart"},{"command":"gr check-inbox"},{"command":"my extra"}],` +
			`"postToolUse":[{"command":"gr report-status --event PostToolUse"}],` +
			`"stop":[{"command":"gr report-status --event Stop"}],` +
			`"myOwnEvent":[{"command":"do a thing"}]}}`
		if err := os.WriteFile(cursorHooksPath(worktree), []byte(user), 0o600); err != nil {
			t.Fatal(err)
		}

		sm.cfg.Agents["cursor"] = cursorProjectAgent()

		sm.cleanupHooks(sessionID, "cursor", worktree)

		if _, err := os.Stat(cursorHooksPath(worktree)); err != nil {
			t.Errorf("user-authored lookalike deleted; want preserved (err=%v)", err)
		}
	})
}

// TestInjectCursorHooksRefusesPreExistingUserFile proves launch fails safely
// (never claiming/overwriting) when a hooks.json exists without this session's
// ownership marker (issue #1236).
func TestInjectCursorHooksRefusesPreExistingUserFile(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()

	if err := os.MkdirAll(filepath.Join(worktree, ".cursor"), 0o700); err != nil {
		t.Fatal(err)
	}

	userContent := []byte(`{"version":1,"hooks":{"stop":[{"command":"user hook"}]}}`)
	if err := os.WriteFile(cursorHooksPath(worktree), userContent, 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := sm.injectCursorHooks("interloper", worktree, false, cursorProjectAgent(), false)
	if err == nil {
		t.Fatal("expected injectCursorHooks to refuse a pre-existing user hooks.json")
	}

	// The user's file must be untouched.
	got, rerr := os.ReadFile(cursorHooksPath(worktree))
	if rerr != nil || string(got) != string(userContent) {
		t.Errorf("pre-existing user hooks.json was modified; content=%q err=%v", got, rerr)
	}
}

// TestInjectCursorHooksRefusesModifiedOnReinject proves that if the user modifies
// graith's hooks.json after launch, a resume/re-inject refuses rather than
// clobbering the modification (issue #1236).
func TestInjectCursorHooksRefusesModifiedOnReinject(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	sessionID := "reinject"

	if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("first injectCursorHooks: %v", err)
	}

	modified := []byte(`{"version":1,"hooks":{"stop":[{"command":"user edit"}]}}`)
	if err := os.WriteFile(cursorHooksPath(worktree), modified, 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false)
	if err == nil {
		t.Fatal("expected re-inject to refuse overwriting a user-modified hooks.json")
	}

	got, _ := os.ReadFile(cursorHooksPath(worktree))
	if string(got) != string(modified) {
		t.Errorf("user modification clobbered on re-inject; content=%q", got)
	}
}

// TestInjectCursorHooksReinjectReplacesOwnFile proves a clean re-inject (the file
// still matches the recorded hash) is allowed to rewrite graith's own file.
func TestInjectCursorHooksReinjectReplacesOwnFile(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	sessionID := "clean-reinject"

	if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("first injectCursorHooks: %v", err)
	}

	// Re-inject with approval hooks enabled → different content; the existing file
	// is unmodified graith output, so replacement is allowed.
	if _, _, err := sm.injectCursorHooks(sessionID, worktree, true, cursorProjectAgent(), true); err != nil {
		t.Fatalf("clean re-inject refused: %v", err)
	}

	if !isGraithGeneratedCursorHooks(cursorHooksPath(worktree)) {
		t.Error("re-injected file is not graith-structured")
	}
}

// withCursorWriteFailure overrides the atomic-write seam so a write to any path
// containing match fails, and restores it on cleanup.
func withCursorWriteFailure(t *testing.T, match string) {
	t.Helper()

	orig := atomicWriteCursorFile
	atomicWriteCursorFile = func(path string, data []byte, perm os.FileMode) error {
		if strings.Contains(path, match) {
			return errInjectedWrite
		}

		return orig(path, data, perm)
	}

	t.Cleanup(func() { atomicWriteCursorFile = orig })
}

var errInjectedWrite = errors.New("injected write failure")

// TestInjectCursorHooksFirstInjectionMarkerFailure proves that if recording the
// ownership marker fails on a first injection, the target is not published and no
// partial state remains (issue #1236).
func TestInjectCursorHooksFirstInjectionMarkerFailure(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()

	withCursorWriteFailure(t, "cursor_hooks_owned")

	_, _, err := sm.injectCursorHooks("rollback", worktree, false, cursorProjectAgent(), false)
	if err == nil {
		t.Fatal("expected injectCursorHooks to fail when the ownership marker can't be written")
	}

	if _, statErr := os.Stat(cursorHooksPath(worktree)); !os.IsNotExist(statErr) {
		t.Errorf("hooks.json published despite marker failure; stat err = %v", statErr)
	}
}

// assertReinjectFailurePreserves establishes an owned target+marker, injects a
// write failure at the path containing failMatch, re-injects with DIFFERENT
// content, and asserts the re-inject fails while the prior target AND marker are
// left intact (issue #1236). failMatch = "cursor_hooks_owned" exercises a
// marker-write failure; "hooks.json" a pre-rename target-write failure.
func assertReinjectFailurePreserves(t *testing.T, failMatch string) {
	t.Helper()

	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	sessionID := "reinject"

	if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("first injectCursorHooks: %v", err)
	}

	origTarget := readFile(t, cursorHooksPath(worktree))
	origMarker := readFile(t, sm.cursorHooksOwnershipPath(sessionID))

	withCursorWriteFailure(t, failMatch)

	// Different content (approval hooks on) so an unchanged target proves rollback.
	if _, _, err := sm.injectCursorHooks(sessionID, worktree, true, cursorProjectAgent(), true); err == nil {
		t.Fatalf("expected re-inject to fail on %s write", failMatch)
	}

	if got := readFile(t, cursorHooksPath(worktree)); got != origTarget {
		t.Errorf("target changed despite %s-write failure on re-inject", failMatch)
	}

	if got := readFile(t, sm.cursorHooksOwnershipPath(sessionID)); got != origMarker {
		t.Errorf("marker changed despite %s-write failure on re-inject", failMatch)
	}
}

// TestInjectCursorHooksReinjectionMarkerFailurePreserves proves a marker-write
// failure on re-injection leaves the previously published target AND marker
// intact (issue #1236).
func TestInjectCursorHooksReinjectionMarkerFailurePreserves(t *testing.T) {
	assertReinjectFailurePreserves(t, "cursor_hooks_owned")
}

// TestInjectCursorHooksTargetPublishFailurePreserves proves a target-write
// failure that happens BEFORE the rename rolls the marker back and leaves the
// previous target AND marker intact (issue #1236).
func TestInjectCursorHooksTargetPublishFailurePreserves(t *testing.T) {
	assertReinjectFailurePreserves(t, "hooks.json")
}

// TestInjectCursorHooksPostRenameErrorKeepsNewMarker covers the atomicfile.Write
// ambiguity: the writer can succeed at renaming the new bytes into place and then
// return an error (e.g. a directory fsync failure). In that case the target holds
// the NEW bytes, so the marker must stay on the NEW hash — never rolled back to
// the old one, which would mismatch the published target (issue #1236).
func TestInjectCursorHooksPostRenameErrorKeepsNewMarker(t *testing.T) {
	sm := newTestSessionManagerWithDataDir(t)
	worktree := t.TempDir()
	sessionID := "post-rename"

	// First injection establishes an owned target + marker (the "old" content).
	if _, _, err := sm.injectCursorHooks(sessionID, worktree, false, cursorProjectAgent(), false); err != nil {
		t.Fatalf("first injectCursorHooks: %v", err)
	}

	oldMarker := readFile(t, sm.cursorHooksOwnershipPath(sessionID))

	// Re-inject with DIFFERENT content, and simulate a post-rename durability
	// error: the writer actually publishes the new bytes, then returns an error.
	orig := atomicWriteCursorFile
	atomicWriteCursorFile = func(path string, data []byte, perm os.FileMode) error {
		if strings.Contains(path, "hooks.json") {
			_ = orig(path, data, perm) // the rename really happened

			return errInjectedWrite // ...but the writer reports a durability failure
		}

		return orig(path, data, perm)
	}

	t.Cleanup(func() { atomicWriteCursorFile = orig })

	_, _, err := sm.injectCursorHooks(sessionID, worktree, true, cursorProjectAgent(), true)
	if err == nil {
		t.Fatal("expected the durability error to be surfaced")
	}

	// The target now holds the new bytes; the marker must match the ACTUAL target,
	// not the old one.
	target := readFile(t, cursorHooksPath(worktree))
	if target == "" {
		t.Fatal("target unexpectedly empty")
	}

	marker := readFile(t, sm.cursorHooksOwnershipPath(sessionID))
	if marker == oldMarker {
		t.Error("marker rolled back to old value despite the new target being on disk (mismatch)")
	}

	if !isGraithGeneratedCursorHooks(cursorHooksPath(worktree)) {
		t.Error("published target is not the new graith content")
	}

	// The marker must be the SHA-256 of the actual on-disk target.
	sum := sha256.Sum256([]byte(target))
	if marker != hex.EncodeToString(sum[:]) {
		t.Errorf("marker %q does not match hash of actual target", marker)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(b)
}
