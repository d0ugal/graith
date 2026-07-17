package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// jailN quarantines n comments in store s so a jail listing has enough rows for
// its cap to be the bound under test.
func jailN(t *testing.T, s *MsgStore, n int) {
	t.Helper()

	for i := 0; i < n; i++ {
		if _, _, err := s.Jail(JailedComment{
			CommentID: int64(i), Surface: "conversation", PRNumber: 1, Author: "scunner",
			Association: "NONE", Body: "untrusted", TargetSession: "wynd", TargetName: "wynd",
		}); err != nil {
			t.Fatalf("Jail %d: %v", i, err)
		}
	}
}

// TestStoreLimitsReloadPath is the regression for issue #1291: the jail listing
// cap and the todo title/note length limits are documented as hot-reloadable but
// were frozen at store open. It drives a real config reload (write file →
// ReloadConfig) and asserts each of the three limits both tightens and relaxes
// on the live store without reopening the databases. Before the fix the stores
// keep their open-time defaults, so every post-reload assertion fails.
func TestStoreLimitsReloadPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	writeCfg := func(jail, title, note int) {
		t.Helper()

		content := fmt.Sprintf("[messages]\njail_list_limit = %d\n\n[todo]\nmax_title = %d\nmax_note = %d\n",
			jail, title, note)
		if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	sm := newTestSessionManager(t)
	sm.configFile = cfgPath
	sm.messages = testStore(t)
	sm.todos = newTestTodoStore(t)

	// Six jailed comments so a cap of 2 (tighten) and 5 (relax) each bind below
	// the total, giving distinct, non-default listing sizes.
	jailN(t, sm.messages, 6)

	// --- Tighten all three through the real reload path. ---
	writeCfg(2, 10, 20)

	if err := sm.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig (tighten): %v", err)
	}

	if list, err := sm.messages.ListJailed(false); err != nil {
		t.Fatalf("ListJailed after tighten: %v", err)
	} else if len(list) != 2 {
		t.Errorf("after tighten: ListJailed returned %d, want 2 (reloaded jail_list_limit)", len(list))
	}

	if _, err := sm.todos.Add(TodoAdd{Scope: "session:braw", Title: strings.Repeat("x", 11), CreatedBy: "canny"}); err == nil || !strings.Contains(err.Error(), "max 10") {
		t.Errorf("after tighten: Add 11-char title err = %v, want one reporting 'max 10'", err)
	}

	if _, err := sm.todos.Add(TodoAdd{Scope: "session:braw", Title: "ok", Note: strings.Repeat("x", 21), CreatedBy: "canny"}); err == nil || !strings.Contains(err.Error(), "max 20") {
		t.Errorf("after tighten: Add 21-char note err = %v, want one reporting 'max 20'", err)
	}

	// --- Relax all three through the real reload path. ---
	writeCfg(5, 200, 400)

	if err := sm.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig (relax): %v", err)
	}

	if list, err := sm.messages.ListJailed(false); err != nil {
		t.Fatalf("ListJailed after relax: %v", err)
	} else if len(list) != 5 {
		t.Errorf("after relax: ListJailed returned %d, want 5 (relaxed jail_list_limit)", len(list))
	}

	if _, err := sm.todos.Add(TodoAdd{Scope: "session:strath", Title: strings.Repeat("x", 11), CreatedBy: "canny"}); err != nil {
		t.Errorf("after relax: Add 11-char title should now pass, got %v", err)
	}

	if _, err := sm.todos.Add(TodoAdd{Scope: "session:strath", Title: "ok", Note: strings.Repeat("x", 21), CreatedBy: "canny"}); err != nil {
		t.Errorf("after relax: Add 21-char note should now pass, got %v", err)
	}
}

// TestMsgStoreSetJailListLimitLive confirms the live jail-cap setter tightens,
// relaxes, and resolves a non-positive value back to the default — the same
// semantics as store open (issue #1291).
func TestMsgStoreSetJailListLimitLive(t *testing.T) {
	s := testStore(t)
	jailN(t, s, 5)

	s.SetJailListLimit(2)

	if list, err := s.ListJailed(false); err != nil {
		t.Fatalf("ListJailed: %v", err)
	} else if len(list) != 2 {
		t.Errorf("tightened: ListJailed returned %d, want 2", len(list))
	}

	s.SetJailListLimit(4)

	if list, err := s.ListJailed(false); err != nil {
		t.Fatalf("ListJailed: %v", err)
	} else if len(list) != 4 {
		t.Errorf("relaxed: ListJailed returned %d, want 4", len(list))
	}

	// A non-positive value resolves to the default (2000), well above the five
	// jailed rows, so the listing returns them all.
	s.SetJailListLimit(0)

	if list, err := s.ListJailed(false); err != nil {
		t.Fatalf("ListJailed: %v", err)
	} else if len(list) != 5 {
		t.Errorf("default: ListJailed returned %d, want 5 (all rows under the default cap)", len(list))
	}
}

// TestTodoStoreSettersResolveOutOfRange confirms the live title/note setters
// tighten to a valid value and resolve a non-positive or above-ceiling value
// back to the default, matching TodoStoreSettings.resolved so a reload can never
// push a limit past the database CHECK ceiling (issue #1291).
func TestTodoStoreSettersResolveOutOfRange(t *testing.T) {
	s := newTestTodoStore(t)

	s.SetMaxTitle(12)
	s.SetMaxNote(34)

	if got := s.titleLimit(); got != 12 {
		t.Errorf("titleLimit() = %d, want 12", got)
	}

	if got := s.noteLimit(); got != 34 {
		t.Errorf("noteLimit() = %d, want 34", got)
	}

	for _, bad := range []int{0, -5, config.TodoMaxTitleCeiling + 1} {
		s.SetMaxTitle(bad)

		if got := s.titleLimit(); got != config.TodoMaxTitleDefault {
			t.Errorf("SetMaxTitle(%d): titleLimit() = %d, want default %d", bad, got, config.TodoMaxTitleDefault)
		}
	}

	for _, bad := range []int{0, -5, config.TodoMaxNoteCeiling + 1} {
		s.SetMaxNote(bad)

		if got := s.noteLimit(); got != config.TodoMaxNoteDefault {
			t.Errorf("SetMaxNote(%d): noteLimit() = %d, want default %d", bad, got, config.TodoMaxNoteDefault)
		}
	}
}

// TestStoreLimitsPublishedAtomicallyWithConfig is the deterministic ordering
// regression for the #1291 follow-up. applyConfig must apply the live store
// limits within the SAME sm.mu hold that publishes the new config pointer, so
// two reloads racing (fsnotify + SIGHUP) can never leave a published cfg paired
// with stale limits (the earlier bug: cfg from generation B, limits from A).
// The reloadLimitsPublishedHook fires while applyConfig still holds sm.mu, right
// after both the cfg swap and the limit setters; by then the live limits must
// already reflect the just-published cfg. If the setters ran only after the
// unlock (the pre-fix ordering), the atomics would still read the old defaults
// at this point and the assertion fails. This is deterministic — a single
// reload triggers it, no timing window required.
func TestStoreLimitsPublishedAtomicallyWithConfig(t *testing.T) {
	sm := newTestSM(t)
	sm.cfg = config.Default()
	sm.messages = testStore(t)
	sm.todos = newTestTodoStore(t)

	want := config.Default()
	want.Messages.JailListLimit = 2
	want.Todo.MaxTitle = 10
	want.Todo.MaxNote = 20

	var (
		invoked           bool
		jail, title, note int
	)

	reloadLimitsPublishedHook = func() {
		invoked = true
		// Lock-free reads of the live limits at the instant the cfg is published.
		jail = int(sm.messages.jailListLimit.Load())
		title = int(sm.todos.maxTitle.Load())
		note = int(sm.todos.maxNote.Load())
	}

	t.Cleanup(func() { reloadLimitsPublishedHook = nil })

	_ = sm.applyConfig(want)

	if !invoked {
		t.Fatal("reloadLimitsPublishedHook was not invoked — applyConfig did not reach the publish point")
	}

	if jail != 2 || title != 10 || note != 20 {
		t.Errorf("live limits at cfg-publish time: jail=%d title=%d note=%d; want 2/10/20 (limits must be applied within the publish lock)",
			jail, title, note)
	}
}

// TestStoreLimitsReloadRace runs config hot reload concurrently with live store
// reads/writes. The jail cap and todo title/note limits are held in atomics
// (issue #1291) precisely so ListJailed's lock-free read and Add's pre-lock
// titleLimit()/noteLimit() reads stay race-safe against applyConfig's setter
// calls. This must be clean under `go test -race`.
func TestStoreLimitsReloadRace(t *testing.T) {
	sm := newTestSM(t)
	sm.cfg = config.Default()
	sm.messages = testStore(t)
	sm.todos = newTestTodoStore(t)

	jailN(t, sm.messages, 10)

	a := config.Default()
	a.Messages.JailListLimit = 2
	a.Todo.MaxTitle = 10
	a.Todo.MaxNote = 20

	b := config.Default()
	b.Messages.JailListLimit = 50
	b.Todo.MaxTitle = 200
	b.Todo.MaxNote = 400

	stop := hammerApplyConfig(sm, a, b)
	defer stop()

	for i := 0; i < 200; i++ {
		if _, err := sm.messages.ListJailed(false); err != nil {
			t.Fatalf("ListJailed iteration %d: %v", i, err)
		}

		_ = sm.todos.titleLimit()
		_ = sm.todos.noteLimit()

		if _, err := sm.todos.Add(TodoAdd{Scope: "session:strath", Title: fmt.Sprintf("t-%d", i), CreatedBy: "canny"}); err != nil {
			t.Fatalf("Add iteration %d: %v", i, err)
		}
	}
}
