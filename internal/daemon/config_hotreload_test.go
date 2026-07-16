package daemon

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
)

// TestApplyConfigReloadsJailListLimit proves messages.jail_list_limit is
// hot-reloaded into the live MsgStore by applyConfig — both tightening (a lower
// cap drops rows) and relaxing (a higher cap restores them) take effect on the
// next ListJailed without reopening the database (issue #1291).
func TestApplyConfigReloadsJailListLimit(t *testing.T) {
	sm := newSMWithConfig(t, config.Default())

	ms, err := NewMsgStore(filepath.Join(t.TempDir(), "msg.db"))
	if err != nil {
		t.Fatalf("NewMsgStore: %v", err)
	}

	defer func() { _ = ms.Close() }()

	sm.messages = ms

	const jailed = 5
	for i := 0; i < jailed; i++ {
		if _, _, err := ms.Jail(JailedComment{
			CommentID: int64(i + 1), Surface: "conversation", PRNumber: 1,
			Author: "scunner", TargetSession: "wynd",
		}); err != nil {
			t.Fatalf("Jail %d: %v", i, err)
		}
	}

	// Tighten: cap below the jailed count.
	tighten := config.Default()
	tighten.Messages.JailListLimit = 2
	sm.applyConfig(tighten)

	if list, err := ms.ListJailed(true); err != nil {
		t.Fatalf("ListJailed: %v", err)
	} else if len(list) != 2 {
		t.Fatalf("after tightening jail_list_limit=2, ListJailed returned %d, want 2", len(list))
	}

	// Relax: cap above the jailed count restores the full listing.
	relax := config.Default()
	relax.Messages.JailListLimit = 50
	sm.applyConfig(relax)

	if list, err := ms.ListJailed(true); err != nil {
		t.Fatalf("ListJailed: %v", err)
	} else if len(list) != jailed {
		t.Fatalf("after relaxing jail_list_limit=50, ListJailed returned %d, want %d", len(list), jailed)
	}
}

// TestApplyConfigReloadsTodoLimits proves todo.max_title and todo.max_note are
// hot-reloaded into the live TodoStore by applyConfig, and that the database
// hard ceilings are preserved (a value above the ceiling falls back to the
// default rather than being accepted) (issue #1291).
func TestApplyConfigReloadsTodoLimits(t *testing.T) {
	sm := newSMWithConfig(t, config.Default())

	ts, err := NewTodoStore(filepath.Join(t.TempDir(), "todos.sqlite"))
	if err != nil {
		t.Fatalf("NewTodoStore: %v", err)
	}

	defer func() { _ = ts.Close() }()

	sm.todos = ts

	const (
		title = "a-twelve-len" // 12 bytes
		note  = "a-twelve-len"
	)

	// Tighten below the title/note length.
	tighten := config.Default()
	tighten.Todo.MaxTitle = 5
	tighten.Todo.MaxNote = 5
	sm.applyConfig(tighten)

	if _, err := ts.Add(TodoAdd{Scope: "session:ben", Title: title, CreatedBy: "braw"}); err == nil {
		t.Fatal("expected Add to reject an over-length title after tightening max_title")
	} else if !strings.Contains(err.Error(), "too long") {
		t.Fatalf("title error = %v, want 'too long'", err)
	}

	if _, err := ts.Add(TodoAdd{Scope: "session:ben", Title: "ok", Note: note, CreatedBy: "braw"}); err == nil {
		t.Fatal("expected Add to reject an over-length note after tightening max_note")
	} else if !strings.Contains(err.Error(), "too long") {
		t.Fatalf("note error = %v, want 'too long'", err)
	}

	// Relax above the length: the same title/note is now accepted.
	relax := config.Default()
	relax.Todo.MaxTitle = 100
	relax.Todo.MaxNote = 100
	sm.applyConfig(relax)

	if _, err := ts.Add(TodoAdd{Scope: "session:ben", Title: title, Note: note, CreatedBy: "braw"}); err != nil {
		t.Fatalf("after relaxing limits, Add should succeed: %v", err)
	}

	// The database CHECK ceilings must survive an oversized config: an above-
	// ceiling max_title falls back to the default, never past the schema limit.
	overCeiling := config.Default()
	overCeiling.Todo.MaxTitle = todoTitleHardCeiling + 1000
	sm.applyConfig(overCeiling)

	if got := ts.titleLimit(); got != config.TodoMaxTitleDefault {
		t.Fatalf("above-ceiling max_title resolved to %d, want default %d", got, config.TodoMaxTitleDefault)
	}

	if got := ts.titleLimit(); got > todoTitleHardCeiling {
		t.Fatalf("titleLimit %d exceeds the database hard ceiling %d", got, todoTitleHardCeiling)
	}
}

// TestApplyConfigUpdatesLiveInputDelay proves a reloaded [lifecycle] input_delay
// takes effect on an already-running PTY: the next WriteInputAndSubmit uses the
// new pause without the session being restarted or resumed (issue #1294).
func TestApplyConfigUpdatesLiveInputDelay(t *testing.T) {
	cfg := config.Default()
	cfg.Lifecycle.InputDelay = "1ms"
	sm := newSMWithConfig(t, cfg)

	sess, err := grpty.NewSession(grpty.SessionOpts{
		ID: "croft", Command: "sh", Args: []string{"-c", "sleep 30"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: filepath.Join(t.TempDir(), "pty.log"), MaxLogSize: 1024 * 1024,
		InputDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	t.Cleanup(func() {
		if !sess.Exited() {
			_ = sess.Kill()
		}

		sess.Close()
	})

	sm.mu.Lock()
	sm.sessions["croft"] = sess
	sm.mu.Unlock()

	// Reload with a much larger delay.
	newCfg := config.Default()
	newCfg.Lifecycle.InputDelay = "300ms"
	sm.applyConfig(newCfg)

	start := time.Now()

	if err := sess.WriteInputAndSubmit([]byte("bonnie")); err != nil {
		t.Fatalf("WriteInputAndSubmit: %v", err)
	}

	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("submit took %v after reloading input_delay=300ms; live PTY did not observe the reload", elapsed)
	}
}
