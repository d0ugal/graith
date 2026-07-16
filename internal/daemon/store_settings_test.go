package daemon

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// TestTodoHardCeilingsMatchConfig guards that the length limits baked into the
// database CHECK constraints stay in lockstep with the config ceilings. If one
// side changes, a configured limit could be accepted by Validate yet rejected by
// the database (or vice versa), so this assertion must hold.
func TestTodoHardCeilingsMatchConfig(t *testing.T) {
	if todoTitleHardCeiling != config.TodoMaxTitleCeiling {
		t.Errorf("todoTitleHardCeiling = %d, config.TodoMaxTitleCeiling = %d: must match the DB CHECK",
			todoTitleHardCeiling, config.TodoMaxTitleCeiling)
	}

	if todoNoteHardCeiling != config.TodoMaxNoteCeiling {
		t.Errorf("todoNoteHardCeiling = %d, config.TodoMaxNoteCeiling = %d: must match the DB CHECK",
			todoNoteHardCeiling, config.TodoMaxNoteCeiling)
	}
}

// TestTodoStoreHonoursConfiguredLimits confirms a store built with tightened
// title/note/list limits enforces them and reports the configured value in the
// error, rather than the old fixed literals.
func TestTodoStoreHonoursConfiguredLimits(t *testing.T) {
	s, err := NewTodoStore(filepath.Join(t.TempDir(), "todos.sqlite"), TodoStoreSettings{
		MaxTitle:  10,
		MaxNote:   20,
		ListLimit: 3,
	})
	if err != nil {
		t.Fatalf("NewTodoStore: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	// A title at the limit passes; one over it is rejected with the configured max.
	if _, err := s.Add(TodoAdd{Scope: "session:braw", Title: "0123456789", CreatedBy: "canny"}); err != nil {
		t.Fatalf("Add title==limit: %v", err)
	}

	_, err = s.Add(TodoAdd{Scope: "session:braw", Title: "0123456789X", CreatedBy: "canny"})
	if err == nil || !strings.Contains(err.Error(), "max 10") {
		t.Fatalf("Add over-length title: err = %v, want one reporting 'max 10'", err)
	}

	_, err = s.Add(TodoAdd{Scope: "session:braw", Title: "ok", Note: strings.Repeat("x", 21), CreatedBy: "canny"})
	if err == nil || !strings.Contains(err.Error(), "max 20") {
		t.Fatalf("Add over-length note: err = %v, want one reporting 'max 20'", err)
	}

	// The list cap bounds a scope query at the configured limit.
	for i := 0; i < 6; i++ {
		if _, err := s.Add(TodoAdd{Scope: "session:strath", Title: fmt.Sprintf("item-%d", i), CreatedBy: "canny"}); err != nil {
			t.Fatalf("Add strath item %d: %v", i, err)
		}
	}

	items, err := s.List("session:strath", TodoFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(items) != 3 {
		t.Errorf("List returned %d items, want 3 (configured list_limit)", len(items))
	}
}

// TestTodoStoreDefaultsWhenUnset confirms a zero-value settings (and the bare
// NewTodoStore call) resolves to the built-in defaults, and that a settings
// value above the DB ceiling is clamped back to the default rather than accepted.
func TestTodoStoreDefaultsWhenUnset(t *testing.T) {
	s, err := NewTodoStore(filepath.Join(t.TempDir(), "todos.sqlite"), TodoStoreSettings{
		MaxTitle: config.TodoMaxTitleCeiling + 5, // out of range -> default
	})
	if err != nil {
		t.Fatalf("NewTodoStore: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	if got := s.titleLimit(); got != config.TodoMaxTitleDefault {
		t.Errorf("titleLimit() = %d, want %d (out-of-range setting falls back to default)", got, config.TodoMaxTitleDefault)
	}

	if got := s.listCap(); got != config.TodoListLimitDefault {
		t.Errorf("listCap() = %d, want %d (default)", got, config.TodoListLimitDefault)
	}
}

// TestMsgStoreHonoursJailListLimit confirms a store built with a tightened jail
// list limit caps the listing at the configured value, newest first.
func TestMsgStoreHonoursJailListLimit(t *testing.T) {
	s, err := NewMsgStore(filepath.Join(t.TempDir(), "msg.sqlite"), MsgStoreSettings{JailListLimit: 2})
	if err != nil {
		t.Fatalf("NewMsgStore: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	for i := 0; i < 5; i++ {
		if _, _, err := s.Jail(JailedComment{
			CommentID: int64(i), Surface: "conversation", PRNumber: 1, Author: "scunner",
			Association: "NONE", Body: "untrusted", TargetSession: "wynd", TargetName: "wynd",
		}); err != nil {
			t.Fatalf("Jail %d: %v", i, err)
		}
	}

	list, err := s.ListJailed(false)
	if err != nil {
		t.Fatalf("ListJailed: %v", err)
	}

	if len(list) != 2 {
		t.Errorf("ListJailed returned %d, want 2 (configured jail_list_limit)", len(list))
	}
}

// TestMsgStoreHonoursSubscriberBuffer confirms the per-subscriber channel is
// sized from config. A publisher that outruns a non-draining subscriber past the
// buffer must not block: the store drops the overflow (the log stays
// authoritative), and the buffered messages are still delivered.
func TestMsgStoreHonoursSubscriberBuffer(t *testing.T) {
	const buffer = 4

	s, err := NewMsgStore(filepath.Join(t.TempDir(), "msg.sqlite"), MsgStoreSettings{SubscriberBuffer: buffer})
	if err != nil {
		t.Fatalf("NewMsgStore: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	ch, unsub := s.Subscribe("bothy")
	defer unsub()

	if got := cap(ch); got != buffer {
		t.Fatalf("subscriber channel cap = %d, want %d", got, buffer)
	}

	// Publish more than the buffer holds without draining; the publish path must
	// not deadlock on the full channel.
	done := make(chan struct{})

	go func() {
		defer close(done)

		for i := 0; i < buffer*3; i++ {
			if _, err := s.Publish(PublishOpts{Stream: "bothy", SenderID: "canny", Body: "blether"}); err != nil {
				t.Errorf("Publish %d: %v", i, err)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing past a full subscriber buffer deadlocked")
	}

	// The buffered messages are still readable off the channel.
	got := 0

	for drained := false; !drained; {
		select {
		case <-ch:
			got++
		default:
			drained = true
		}
	}

	if got == 0 || got > buffer {
		t.Errorf("drained %d buffered messages, want 1..%d", got, buffer)
	}
}

// TestMsgStoreSubscriberBufferDefault confirms a bare store uses the default
// subscriber buffer capacity.
func TestMsgStoreSubscriberBufferDefault(t *testing.T) {
	s := testStore(t)

	ch, unsub := s.Subscribe("bothy")
	defer unsub()

	if got := cap(ch); got != config.MessagesSubscriberBufferDefault {
		t.Errorf("default subscriber channel cap = %d, want %d", got, config.MessagesSubscriberBufferDefault)
	}
}

// TestTodoStoreConcurrentAddUnderLowBusyTimeout exercises the claim/insert path
// under concurrent writers with a short (but positive) configured busy timeout,
// confirming the configurable busy_timeout keeps the store correct under
// contention rather than surfacing SQLITE_BUSY.
func TestTodoStoreConcurrentAddUnderLowBusyTimeout(t *testing.T) {
	s, err := NewTodoStore(filepath.Join(t.TempDir(), "todos.sqlite"), TodoStoreSettings{
		BusyTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewTodoStore: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	const writers = 8

	var wg sync.WaitGroup

	errs := make(chan error, writers)

	for i := 0; i < writers; i++ {
		wg.Add(1)

		go func(n int) {
			defer wg.Done()

			if _, err := s.Add(TodoAdd{Scope: "session:strath", Title: fmt.Sprintf("t-%d", n), CreatedBy: "canny"}); err != nil {
				errs <- err
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Add failed: %v", err)
	}

	items, err := s.List("session:strath", TodoFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(items) != writers {
		t.Errorf("List returned %d items, want %d", len(items), writers)
	}
}
