package daemon

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// TestSQLiteBusyTimeoutMillisFloorsSubMillisecond guards the core of #1322: a
// positive sub-millisecond wait must never render as 0 (which disables SQLite
// lock waiting), so it is floored to the pragma's 1ms resolution. Zero or
// negative inputs pass straight through — callers resolve those to a default
// before they reach the DSN.
func TestSQLiteBusyTimeoutMillisFloorsSubMillisecond(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want int64
	}{
		{"1ns floors to 1", time.Nanosecond, 1},
		{"500us floors to 1", 500 * time.Microsecond, 1},
		{"999us floors to 1", 999 * time.Microsecond, 1},
		{"1ms is exact", time.Millisecond, 1},
		{"1500us truncates to 1", 1500 * time.Microsecond, 1},
		{"2ms is exact", 2 * time.Millisecond, 2},
		{"5s renders whole", 5 * time.Second, 5000},
		{"zero stays zero", 0, 0},
		{"negative stays as-is", -time.Second, -1000},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sqliteBusyTimeoutMillis(c.in); got != c.want {
				t.Errorf("sqliteBusyTimeoutMillis(%v) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestStoreDSNNeverRendersZeroBusyTimeout confirms both store DSN builders emit
// busy_timeout(1) rather than busy_timeout(0) for a positive sub-millisecond
// wait, at the 1ns / 500us / 1ms boundaries called out in #1322.
func TestStoreDSNNeverRendersZeroBusyTimeout(t *testing.T) {
	builders := []struct {
		name  string
		build func(dbPath string, d time.Duration) string
	}{
		{"messages", msgStoreDSN},
		{"todo", todoStoreDSN},
	}

	cases := []struct {
		name    string
		in      time.Duration
		wantMS  string
		wantBad string
	}{
		{"1ns", time.Nanosecond, "busy_timeout(1)", "busy_timeout(0)"},
		{"500us", 500 * time.Microsecond, "busy_timeout(1)", "busy_timeout(0)"},
		{"1ms", time.Millisecond, "busy_timeout(1)", "busy_timeout(0)"},
		{"5s", 5 * time.Second, "busy_timeout(5000)", "busy_timeout(0)"},
	}

	for _, b := range builders {
		for _, c := range cases {
			t.Run(fmt.Sprintf("%s/%s", b.name, c.name), func(t *testing.T) {
				dsn := b.build(filepath.Join(t.TempDir(), "braw.sqlite"), c.in)
				if !strings.Contains(dsn, c.wantMS) {
					t.Errorf("%s DSN for %v = %q, want it to contain %q", b.name, c.in, dsn, c.wantMS)
				}

				if strings.Contains(dsn, c.wantBad) {
					t.Errorf("%s DSN for %v = %q, must not contain %q", b.name, c.in, dsn, c.wantBad)
				}
			})
		}
	}
}

// TestStoreDSNAppliesNonZeroPragma opens a database with each store's DSN at a
// sub-millisecond busy timeout and reads back PRAGMA busy_timeout, proving the
// engine actually honours a non-zero wait rather than the lock-wait-disabling
// busy_timeout(0) that #1322 produced.
func TestStoreDSNAppliesNonZeroPragma(t *testing.T) {
	builders := []struct {
		name  string
		build func(dbPath string, d time.Duration) string
	}{
		{"messages", msgStoreDSN},
		{"todo", todoStoreDSN},
	}

	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			dsn := b.build(filepath.Join(t.TempDir(), "canny.sqlite"), 500*time.Microsecond)

			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatalf("open %s db: %v", b.name, err)
			}

			t.Cleanup(func() { _ = db.Close() })

			var busyMS int
			if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyMS); err != nil {
				t.Fatalf("read PRAGMA busy_timeout: %v", err)
			}

			if busyMS < 1 {
				t.Errorf("PRAGMA busy_timeout = %d, want >= 1 (busy_timeout(0) disables lock waiting)", busyMS)
			}
		})
	}
}

// TestStoreSettingsFloorSubMillisecondBusyTimeout confirms the defensive floor
// in each store's resolved(): a direct caller passing a positive sub-millisecond
// wait gets 1ms, an at-resolution 1ms is kept exactly, and zero resolves to the
// store's built-in default. This is the belt-and-braces path for callers that
// bypass config Validate. See #1322.
func TestStoreSettingsFloorSubMillisecondBusyTimeout(t *testing.T) {
	stores := []struct {
		name    string
		resolve func(time.Duration) time.Duration
		def     time.Duration
	}{
		{
			name:    "messages",
			resolve: func(d time.Duration) time.Duration { return MsgStoreSettings{BusyTimeout: d}.resolved().BusyTimeout },
			def:     config.MessagesBusyTimeoutDefault,
		},
		{
			name:    "todo",
			resolve: func(d time.Duration) time.Duration { return TodoStoreSettings{BusyTimeout: d}.resolved().BusyTimeout },
			def:     config.TodoBusyTimeoutDefault,
		},
	}

	for _, s := range stores {
		t.Run(s.name, func(t *testing.T) {
			cases := []struct {
				name string
				in   time.Duration
				want time.Duration
			}{
				{"1ns floors to 1ms", time.Nanosecond, config.SQLiteBusyTimeoutResolution},
				{"500us floors to 1ms", 500 * time.Microsecond, config.SQLiteBusyTimeoutResolution},
				{"1ms kept exactly", time.Millisecond, time.Millisecond},
				{"zero resolves to default", 0, s.def},
			}

			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					if got := s.resolve(c.in); got != c.want {
						t.Errorf("resolved(%v) busy_timeout = %v, want %v", c.in, got, c.want)
					}
				})
			}
		})
	}
}
