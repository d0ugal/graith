package daemon

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// TestSQLiteBusyTimeoutMillisFloor covers the shared pragma-millisecond helper
// (issue #1322): any positive duration floors to at least 1ms so a
// sub-millisecond value can never render busy_timeout(0) and disable SQLite lock
// waiting. Non-positive inputs are left untouched — callers resolve those to a
// default before the DSN is built.
func TestSQLiteBusyTimeoutMillisFloor(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want int64
	}{
		{"1ns floors to 1ms", time.Nanosecond, 1},
		{"500us floors to 1ms", 500 * time.Microsecond, 1},
		{"1ms boundary is 1", time.Millisecond, 1},
		{"1500us floors to 1ms", 1500 * time.Microsecond, 1},
		{"2ms is 2", 2 * time.Millisecond, 2},
		{"5s is 5000", 5 * time.Second, 5000},
		{"zero untouched", 0, 0},
		{"negative untouched", -time.Second, -1000},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sqliteBusyTimeoutMillis(c.in); got != c.want {
				t.Errorf("sqliteBusyTimeoutMillis(%v) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestStoreDSNBusyTimeoutFloor asserts that both the message-store and todo-store
// DSN renderers never emit busy_timeout(0) for a positive sub-millisecond
// configured value, and carry the exact millisecond value for larger durations
// (issue #1322). This is the defensive store-level floor that backs the
// config-load rejection; for the todo store it also protects the claim contract,
// which relies on a contended writer waiting rather than erroring.
func TestStoreDSNBusyTimeoutFloor(t *testing.T) {
	builders := map[string]func(time.Duration) string{
		"messages": func(d time.Duration) string {
			return msgStoreDSN(filepath.Join("croft", "messages.sqlite"), MsgStoreSettings{BusyTimeout: d})
		},
		"todos": func(d time.Duration) string {
			return todoStoreDSN(filepath.Join("croft", "todos.sqlite"), TodoStoreSettings{BusyTimeout: d})
		},
	}

	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			for _, in := range []time.Duration{time.Nanosecond, 500 * time.Microsecond, time.Millisecond} {
				if dsn := build(in); !strings.Contains(dsn, "busy_timeout(1)") {
					t.Errorf("%s DSN(BusyTimeout=%v) = %q, want busy_timeout(1)", name, in, dsn)
				}
			}

			if dsn := build(5 * time.Second); !strings.Contains(dsn, "busy_timeout(5000)") {
				t.Errorf("%s DSN(BusyTimeout=5s) = %q, want busy_timeout(5000)", name, dsn)
			}
		})
	}
}

// TestRunMessageCleanupFromConfigAgeBased is the cleanup-path regression for
// issue #1321: with a valid retention window the age-based sweep deletes stale
// messages, so the load/reload validation that now rejects an invalid or
// negative max_age is what guards against a typo silently collapsing
// MaxAgeDuration to zero (which runMessageCleanupFromConfig treats as
// "retain forever" and skips).
func TestRunMessageCleanupFromConfigAgeBased(t *testing.T) {
	dir := t.TempDir()

	ms, err := NewMsgStore(filepath.Join(dir, "msg.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = ms.Close() })

	cfg := config.Default()
	cfg.Messages.MaxAge = "24h"

	sm := newSMWithConfig(t, cfg)
	sm.messages = ms

	oldTime := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	newTime := time.Now().UTC().Format(time.RFC3339Nano)

	insert := func(id string, seq int, body, created string) {
		t.Helper()

		if _, err := ms.db.Exec(
			`INSERT INTO messages (id, seq, stream, sender_id, sender_name, body, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, seq, "blether", "braw1", "neep", body, created,
		); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	insert("msg_auld", 1, "auld neep", oldTime)
	insert("msg_new", 2, "braw neep", newTime)

	sm.runMessageCleanupFromConfig()

	msgs, err := ms.Read("blether", "", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("after cleanup got %d messages, want 1 (the stale message should be swept)", len(msgs))
	}

	if msgs[0].Body != "braw neep" {
		t.Errorf("surviving message body = %q, want %q", msgs[0].Body, "braw neep")
	}

	// A garbage retention value resolves to the "retain forever" sentinel, which
	// is why load/reload validation (TestReloadConfigRejectsUnsafeTimingPolicies,
	// TestValidateMessagesLimits) must reject it: otherwise a typo silently
	// disables the sweep exercised above.
	if got := (config.Messages{MaxAge: "24x"}).MaxAgeDuration(); got != 0 {
		t.Errorf("invalid max_age accessor = %v, want 0 (retain-forever fail-safe)", got)
	}
}
