package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// eqInt is a small int-equality assertion used by the accessor tests below,
// keeping each check a single line (and keeping the near-identical messages and
// task-list default-value blocks from tripping the dupl linter).
func eqInt(t *testing.T, name string, got, want int) {
	t.Helper()

	if got != want {
		t.Errorf("%s = %d, want %d", name, got, want)
	}
}

// TestMessagesLimitAccessors covers the [messages] operational-limit accessors:
// the non-positive-uses-default fail-safe, explicit values, and the page-size
// clamp-to-max invariant.
func TestMessagesLimitAccessors(t *testing.T) {
	t.Run("defaults on unset/non-positive", func(t *testing.T) {
		m := Messages{}
		eqInt(t, "ConversationMaxLimitOrDefault", m.ConversationMaxLimitOrDefault(), MessagesConversationMaxLimitDefault)
		eqInt(t, "ConversationPageSizeOrDefault", m.ConversationPageSizeOrDefault(), MessagesConversationPageSizeDefault)
		eqInt(t, "JailListLimitOrDefault", m.JailListLimitOrDefault(), MessagesJailListLimitDefault)
		eqInt(t, "SubscriberBufferOrDefault", m.SubscriberBufferOrDefault(), MessagesSubscriberBufferDefault)

		if got := m.BusyTimeoutDuration(); got != MessagesBusyTimeoutDefault {
			t.Errorf("BusyTimeoutDuration() = %v, want %v", got, MessagesBusyTimeoutDefault)
		}
	})

	t.Run("explicit values honoured", func(t *testing.T) {
		m := Messages{
			ConversationPageSize: 100,
			ConversationMaxLimit: 5000,
			JailListLimit:        250,
			SubscriberBuffer:     8,
			BusyTimeout:          "1s",
		}
		eqInt(t, "ConversationPageSizeOrDefault", m.ConversationPageSizeOrDefault(), 100)
		eqInt(t, "ConversationMaxLimitOrDefault", m.ConversationMaxLimitOrDefault(), 5000)
		eqInt(t, "JailListLimitOrDefault", m.JailListLimitOrDefault(), 250)
		eqInt(t, "SubscriberBufferOrDefault", m.SubscriberBufferOrDefault(), 8)

		if got := m.BusyTimeoutDuration(); got != time.Second {
			t.Errorf("BusyTimeoutDuration() = %v, want 1s", got)
		}
	})

	// A page size configured larger than the effective max must be clamped down to
	// the max by the accessor, so a request can never be served a page bigger than
	// the hard cap even under a contradictory config.
	t.Run("page size clamped to max", func(t *testing.T) {
		m := Messages{ConversationPageSize: 900, ConversationMaxLimit: 400}
		eqInt(t, "ConversationPageSizeOrDefault", m.ConversationPageSizeOrDefault(), 400)
	})
}

// TestClampConversationLimitBoundaries exercises the request-limit normaliser at
// its boundaries under a non-default config.
func TestClampConversationLimitBoundaries(t *testing.T) {
	m := Messages{ConversationPageSize: 50, ConversationMaxLimit: 300}

	cases := []struct {
		in, want int
	}{
		{0, 50},    // non-positive -> page size
		{-1, 50},   // negative -> page size
		{1, 1},     // lower boundary passes through
		{299, 299}, // just below max
		{300, 300}, // exactly max
		{301, 300}, // just above max is capped
		{1 << 20, 300},
	}

	for _, c := range cases {
		if got := m.ClampConversationLimit(c.in); got != c.want {
			t.Errorf("ClampConversationLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestTodoLimitAccessors covers the [todo] operational-limit accessors.
func TestTodoLimitAccessors(t *testing.T) {
	t.Run("defaults on unset/non-positive", func(t *testing.T) {
		tc := TodoConfig{}
		eqInt(t, "MaxTitleOrDefault", tc.MaxTitleOrDefault(), TodoMaxTitleDefault)
		eqInt(t, "MaxNoteOrDefault", tc.MaxNoteOrDefault(), TodoMaxNoteDefault)
		eqInt(t, "ListLimitOrDefault", tc.ListLimitOrDefault(), TodoListLimitDefault)

		if got := tc.SweepIntervalDuration(); got != TodoSweepIntervalDefault {
			t.Errorf("SweepIntervalDuration() = %v, want %v", got, TodoSweepIntervalDefault)
		}

		if got := tc.BusyTimeoutDuration(); got != TodoBusyTimeoutDefault {
			t.Errorf("BusyTimeoutDuration() = %v, want %v", got, TodoBusyTimeoutDefault)
		}
	})

	t.Run("explicit values honoured", func(t *testing.T) {
		tc := TodoConfig{MaxTitle: 120, MaxNote: 800, ListLimit: 50, SweepInterval: "10s", BusyTimeout: "2s"}
		eqInt(t, "MaxTitleOrDefault", tc.MaxTitleOrDefault(), 120)
		eqInt(t, "MaxNoteOrDefault", tc.MaxNoteOrDefault(), 800)
		eqInt(t, "ListLimitOrDefault", tc.ListLimitOrDefault(), 50)

		if got := tc.SweepIntervalDuration(); got != 10*time.Second {
			t.Errorf("SweepIntervalDuration() = %v, want 10s", got)
		}

		if got := tc.BusyTimeoutDuration(); got != 2*time.Second {
			t.Errorf("BusyTimeoutDuration() = %v, want 2s", got)
		}
	})
}

// TestValidateMessagesLimits confirms out-of-range [messages] limits fail at load
// while unset/in-range values pass.
func TestValidateMessagesLimits(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"defaults pass", func(*Config) {}, ""},
		{"conversation_max_limit above ceiling", func(c *Config) {
			c.Messages.ConversationMaxLimit = MessagesConversationMaxLimitCeiling + 1
		}, "conversation_max_limit"},
		// Struct-level Validate no longer flags a page size above the max: it
		// cannot tell an inherited default from an explicit override, and the
		// accessor clamps the page size to the max at runtime. The explicit-override
		// contradiction is enforced raw-data-aware at load (issue #1314); see
		// TestLoadConversationPageSizeOverride.
		{"page size above max is clamped, not rejected", func(c *Config) {
			c.Messages.ConversationPageSize = 3000
			c.Messages.ConversationMaxLimit = 2000
		}, ""},
		{"jail_list_limit above ceiling", func(c *Config) {
			c.Messages.JailListLimit = MessagesJailListLimitCeiling + 1
		}, "jail_list_limit"},
		{"subscriber_buffer above ceiling", func(c *Config) {
			c.Messages.SubscriberBuffer = MessagesSubscriberBufferCeiling + 1
		}, "subscriber_buffer"},
		{"busy_timeout unparseable", func(c *Config) {
			c.Messages.BusyTimeout = "dreich"
		}, "messages.busy_timeout"},
		{"busy_timeout non-positive", func(c *Config) {
			c.Messages.BusyTimeout = "0"
		}, "positive duration"},
		{"busy_timeout above ceiling", func(c *Config) {
			c.Messages.BusyTimeout = "10m"
		}, "at most"},
		// SQLite's busy_timeout pragma has millisecond resolution; a positive
		// sub-millisecond value would collapse to busy_timeout(0) (#1322).
		{"busy_timeout 500us rejected", func(c *Config) {
			c.Messages.BusyTimeout = "500us"
		}, "at least 1ms"},
		{"busy_timeout 1ns rejected", func(c *Config) {
			c.Messages.BusyTimeout = "1ns"
		}, "at least 1ms"},
		{"busy_timeout 1ms passes", func(c *Config) {
			c.Messages.BusyTimeout = "1ms"
		}, ""},
		{"max_age empty passes", func(c *Config) {
			c.Messages.MaxAge = ""
		}, ""},
		{"max_age explicit zero passes", func(c *Config) {
			c.Messages.MaxAge = "0"
		}, ""},
		{"max_age valid day syntax passes", func(c *Config) {
			c.Messages.MaxAge = "30d"
		}, ""},
		{"max_age whitespace-padded passes", func(c *Config) {
			c.Messages.MaxAge = "12h "
		}, ""},
		{"max_age unparseable rejected", func(c *Config) {
			c.Messages.MaxAge = "30x"
		}, "messages.max_age"},
		{"max_age negative rejected", func(c *Config) {
			c.Messages.MaxAge = "-5m"
		}, "messages.max_age"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Default()
			c.mutate(cfg)

			err := cfg.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", c.wantErr)
			}

			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("Validate() = %v, want error containing %q", err, c.wantErr)
			}
		})
	}
}

// TestValidateTodoLimits confirms out-of-range [todo] limits fail at load. The
// max_title/max_note ceilings equal the database CHECK constraints, so a value
// above them must be rejected rather than silently truncated by the database.
func TestValidateTodoLimits(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"defaults pass", func(*Config) {}, ""},
		{"max_title above DB ceiling", func(c *Config) {
			c.Todo.MaxTitle = TodoMaxTitleCeiling + 1
		}, "todo.max_title"},
		{"max_note above DB ceiling", func(c *Config) {
			c.Todo.MaxNote = TodoMaxNoteCeiling + 1
		}, "todo.max_note"},
		{"list_limit above ceiling", func(c *Config) {
			c.Todo.ListLimit = TodoListLimitCeiling + 1
		}, "todo.list_limit"},
		{"sweep_interval unparseable", func(c *Config) {
			c.Todo.SweepInterval = "blether"
		}, "todo.sweep_interval"},
		{"sweep_interval non-positive", func(c *Config) {
			c.Todo.SweepInterval = "0"
		}, "positive duration"},
		{"busy_timeout above ceiling", func(c *Config) {
			c.Todo.BusyTimeout = "10m"
		}, "at most"},
		// SQLite's busy_timeout pragma has millisecond resolution; a positive
		// sub-millisecond value would collapse to busy_timeout(0), disabling the
		// wait the claim contract depends on (#1322).
		{"busy_timeout 500us rejected", func(c *Config) {
			c.Todo.BusyTimeout = "500us"
		}, "at least 1ms"},
		{"busy_timeout 1ns rejected", func(c *Config) {
			c.Todo.BusyTimeout = "1ns"
		}, "at least 1ms"},
		{"busy_timeout 1ms passes", func(c *Config) {
			c.Todo.BusyTimeout = "1ms"
		}, ""},
		{"tightened title under ceiling passes", func(c *Config) {
			c.Todo.MaxTitle = 100
			c.Todo.MaxNote = 500
		}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Default()
			c.mutate(cfg)

			err := cfg.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", c.wantErr)
			}

			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("Validate() = %v, want error containing %q", err, c.wantErr)
			}
		})
	}
}

// TestLoadPreservesMessagesTodoLimitOverrides confirms a partial config overrides
// only the limits it sets and keeps the embedded defaults for the rest.
func TestLoadPreservesMessagesTodoLimitOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	toml := `
[messages]
conversation_page_size = 25
subscriber_buffer = 16

[todo]
max_title = 80
sweep_interval = "15s"
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Messages.ConversationPageSizeOrDefault(); got != 25 {
		t.Errorf("page size = %d, want 25 (override)", got)
	}

	if got := cfg.Messages.SubscriberBufferOrDefault(); got != 16 {
		t.Errorf("subscriber buffer = %d, want 16 (override)", got)
	}

	// Untouched [messages] limits keep their defaults.
	if got := cfg.Messages.ConversationMaxLimitOrDefault(); got != MessagesConversationMaxLimitDefault {
		t.Errorf("max limit = %d, want %d (default preserved)", got, MessagesConversationMaxLimitDefault)
	}

	if got := cfg.Todo.MaxTitleOrDefault(); got != 80 {
		t.Errorf("todo max title = %d, want 80 (override)", got)
	}

	if got := cfg.Todo.SweepIntervalDuration(); got != 15*time.Second {
		t.Errorf("todo sweep interval = %v, want 15s (override)", got)
	}

	// Untouched [todo] limits keep their defaults.
	if got := cfg.Todo.MaxNoteOrDefault(); got != TodoMaxNoteDefault {
		t.Errorf("todo max note = %d, want %d (default preserved)", got, TodoMaxNoteDefault)
	}
}

// TestLoadConversationMaxLimitAloneClamps is the issue #1314 acceptance case: a
// config that lowers only conversation_max_limit below the embedded default page
// size (500) must load, and the effective page size must be clamped to the new
// max rather than rejected. The inherited page size is never an explicit override.
func TestLoadConversationMaxLimitAloneClamps(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// conversation_max_limit is deliberately the ONLY key the file sets.
	toml := "[messages]\nconversation_max_limit = 100\n"
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() = %v, want nil (lowering the max alone must load)", err)
	}

	if got := cfg.Messages.ConversationMaxLimitOrDefault(); got != 100 {
		t.Errorf("max limit = %d, want 100", got)
	}

	if got := cfg.Messages.ConversationPageSizeOrDefault(); got > 100 {
		t.Errorf("effective page size = %d, want <= 100 (clamped to the max)", got)
	}
}

// TestLoadConversationPageSizeOverride confirms the raw-data-aware contradiction
// policy (issue #1314): an explicit conversation_page_size larger than the max
// still fails loudly at load — including when it equals the embedded default,
// which a value comparison could not distinguish — while an in-range explicit
// override loads.
func TestLoadConversationPageSizeOverride(t *testing.T) {
	cases := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{
			name:    "explicit page above explicit max fails",
			toml:    "[messages]\nconversation_page_size = 600\nconversation_max_limit = 100\n",
			wantErr: "must not exceed conversation_max_limit",
		},
		{
			// The explicit page size equals the embedded default (500); only the
			// raw key — not a value heuristic — proves it was set on purpose.
			name:    "explicit default-valued page above lowered max fails",
			toml:    "[messages]\nconversation_page_size = 500\nconversation_max_limit = 100\n",
			wantErr: "must not exceed conversation_max_limit",
		},
		{
			name:    "explicit page within max loads",
			toml:    "[messages]\nconversation_page_size = 80\nconversation_max_limit = 100\n",
			wantErr: "",
		},
		{
			// A non-positive explicit page means "use the default", which the
			// accessor clamps to the max — never a contradiction.
			name:    "explicit non-positive page with lowered max loads",
			toml:    "[messages]\nconversation_page_size = 0\nconversation_max_limit = 100\n",
			wantErr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.toml")

			if err := os.WriteFile(cfgPath, []byte(tc.toml), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := Load(cfgPath)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Load() = %v, want nil", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("Load() = nil, want error containing %q", tc.wantErr)
			}

			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Load() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestLoadBusyTimeoutSubMillisecond is the issue #1322 acceptance case exercised
// through the real Load path (not just Validate): SQLite's busy_timeout pragma
// has millisecond resolution, so a positive sub-1ms messages/todo busy_timeout
// is rejected at load with a field-specific error, while 1ms loads. It would
// otherwise collapse to busy_timeout(0) and disable SQLite lock waiting.
func TestLoadBusyTimeoutSubMillisecond(t *testing.T) {
	cases := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{"messages 1ns rejected", "[messages]\nbusy_timeout = \"1ns\"\n", "messages.busy_timeout"},
		{"messages 500us rejected", "[messages]\nbusy_timeout = \"500us\"\n", "messages.busy_timeout"},
		{"messages 1ms loads", "[messages]\nbusy_timeout = \"1ms\"\n", ""},
		{"todo 1ns rejected", "[todo]\nbusy_timeout = \"1ns\"\n", "todo.busy_timeout"},
		{"todo 500us rejected", "[todo]\nbusy_timeout = \"500us\"\n", "todo.busy_timeout"},
		{"todo 1ms loads", "[todo]\nbusy_timeout = \"1ms\"\n", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.toml")

			if err := os.WriteFile(cfgPath, []byte(tc.toml), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := Load(cfgPath)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Load() = %v, want nil", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("Load() = nil, want error containing %q", tc.wantErr)
			}

			if !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "at least 1ms") {
				t.Errorf("Load() = %v, want error containing %q and %q", err, tc.wantErr, "at least 1ms")
			}
		})
	}
}
