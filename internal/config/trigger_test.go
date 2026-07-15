package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// TestActionRepoPathExpandsTilde locks issue #1051: a leading ~/ in a trigger
// action's repo must expand to the home directory, the same as every other
// configured path. A raw ~/... reached the daemon and failed the git check
// ("not inside a git repository: ~/...").
func TestActionRepoPathExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}

	want := filepath.Join(home, "Code", "croft")

	a := ActionConfig{Type: ActionSession, Repo: "~/Code/croft"}
	if got := a.RepoPath(); got != want {
		t.Errorf("RepoPath() = %q, want %q", got, want)
	}

	// An unset repo stays empty (ExpandPath would resolve "" to the cwd).
	empty := ActionConfig{Type: ActionCommand}
	if got := empty.RepoPath(); got != "" {
		t.Errorf("RepoPath() on empty repo = %q, want \"\"", got)
	}

	// An already-absolute path is preserved (cleaned).
	abs := ActionConfig{Type: ActionCommand, Repo: "/glen/bothy"}
	if got := abs.RepoPath(); got != "/glen/bothy" {
		t.Errorf("RepoPath() = %q, want /glen/bothy", got)
	}
}

func schedTrigger(name string, sched ScheduleConfig, action ActionConfig) TriggerConfig {
	return TriggerConfig{Name: name, Schedule: &sched, Action: action}
}

func watchTrigger(name string, watch WatchConfig, action ActionConfig) TriggerConfig {
	return TriggerConfig{Name: name, Watch: &watch, Action: action}
}

func validateOne(t TriggerConfig, orchestrator bool) []error {
	c := &Config{Orchestrator: OrchestratorConfig{Enabled: orchestrator}, Triggers: []TriggerConfig{t}}
	return c.validateTriggers()
}

func TestValidateTriggers_Valid(t *testing.T) {
	cases := []struct {
		name         string
		trig         TriggerConfig
		orchestrator bool
	}{
		{
			name: "schedule cron message",
			trig: schedTrigger("braw-report", ScheduleConfig{Cron: "0 9 * * *"},
				ActionConfig{Type: ActionMessage, Body: "morning", Deliver: DeliverConfig{Inbox: "orchestrator"}}),
		},
		{
			name: "schedule interval command",
			trig: schedTrigger("dreich-sweep", ScheduleConfig{Every: "15m"},
				ActionConfig{Type: ActionCommand, Command: "go test ./...", Repo: "/tmp/croft"}),
		},
		{
			name: "watch command no repo",
			trig: watchTrigger("canny-lint", WatchConfig{Repo: "/tmp/croft", Paths: []string{"**/*.go"}},
				ActionConfig{Type: ActionCommand, Command: "golangci-lint run"}),
		},
		{
			name:         "watch ensure session by role",
			trig:         watchTrigger("bonnie-review", WatchConfig{Role: "implementer"}, ActionConfig{Type: ActionSession, Ensure: true, Prompt: "review"}),
			orchestrator: true,
		},
		{
			name:         "schedule scenario",
			trig:         schedTrigger("strath-fleet", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionScenario, Scenario: "clachan"}),
			orchestrator: true,
		},
		{
			name:         "session auto_cleanup true",
			trig:         schedTrigger("braw-briefing", ScheduleConfig{Cron: "0 8 * * *"}, ActionConfig{Type: ActionSession, Prompt: "brief", AutoCleanup: true}),
			orchestrator: true,
		},
		{
			name:         "session auto_cleanup on_success",
			trig:         schedTrigger("canny-briefing", ScheduleConfig{Cron: "0 8 * * *"}, ActionConfig{Type: ActionSession, Prompt: "brief", AutoCleanup: "on_success"}),
			orchestrator: true,
		},
		{
			name:         "session auto_cleanup false",
			trig:         schedTrigger("bide-briefing", ScheduleConfig{Cron: "0 8 * * *"}, ActionConfig{Type: ActionSession, Prompt: "brief", AutoCleanup: false}),
			orchestrator: true,
		},
		{
			name:         "session idle_timeout",
			trig:         schedTrigger("skelf-briefing", ScheduleConfig{Cron: "0 8 * * *"}, ActionConfig{Type: ActionSession, Prompt: "brief", AutoCleanup: true, IdleTimeout: "2m"}),
			orchestrator: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if errs := validateOne(tc.trig, tc.orchestrator); len(errs) != 0 {
				t.Fatalf("expected valid, got %v", errs)
			}
		})
	}
}

func TestValidateTriggers_Invalid(t *testing.T) {
	cases := []struct {
		name         string
		trig         TriggerConfig
		orchestrator bool
		wantContains string
	}{
		{"no name", schedTrigger("", ScheduleConfig{Cron: "0 9 * * *"}, ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "blether"}}), false, "name is required"},
		{"no source", TriggerConfig{Name: "haar", Action: ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}}, false, "exactly one of"},
		{"both sources", TriggerConfig{Name: "haar", Schedule: &ScheduleConfig{Cron: "@daily"}, Watch: &WatchConfig{Repo: "/r"}, Action: ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}}, false, "both set"},
		{"cron and every", schedTrigger("fash", ScheduleConfig{Cron: "@daily", Every: "5m"}, ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}), false, "exactly one of cron or every"},
		{"neither cron nor every", schedTrigger("fash", ScheduleConfig{}, ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}), false, "neither set"},
		{"timezone with every", schedTrigger("fash", ScheduleConfig{Every: "5m", Timezone: "Europe/London"}, ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}), false, "timezone is only valid with cron"},
		{"zero interval", schedTrigger("fash", ScheduleConfig{Every: "0s"}, ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}), false, "every must be > 0"},
		{"watch both selectors", watchTrigger("thrawn", WatchConfig{Repo: "/r", Role: "impl"}, ActionConfig{Type: ActionCommand, Command: "x"}), false, "exactly one of repo or role"},
		{"watch no selector", watchTrigger("thrawn", WatchConfig{}, ActionConfig{Type: ActionCommand, Command: "x"}), false, "neither set"},
		{"unknown action", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: "explode"}), false, "unknown action.type"},
		{"empty action", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{}), false, "action.type is required"},
		{"command no command", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionCommand, Repo: "/tmp/x"}), false, "requires action.command"},
		{"mutating rejected", watchTrigger("scunner", WatchConfig{Repo: "/r"}, ActionConfig{Type: ActionCommand, Command: "gofmt -w .", Mutating: true}), false, "not supported in v1"},
		{"schedule command no repo", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionCommand, Command: "x"}), false, "requires action.repo"},
		{"watch command with repo", watchTrigger("scunner", WatchConfig{Repo: "/r"}, ActionConfig{Type: ActionCommand, Command: "x", Repo: "/r"}), false, "must not set action.repo"},
		{"session needs orchestrator", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionSession, Prompt: "hi"}), false, "requires [orchestrator] enabled"},
		{"ensure on schedule", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionSession, Ensure: true}), true, "ensure=true is only valid for a [watch]"},
		{"scenario needs name", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionScenario}), true, "requires action.scenario"},
		{"message needs body", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionMessage, Deliver: DeliverConfig{Topic: "t"}}), false, "requires action.body"},
		{"message needs destination", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionMessage, Body: "x"}), false, "requires action.deliver.inbox or action.deliver.topic"},
		{"scenario rejects deliver", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionScenario, Scenario: "c", Deliver: DeliverConfig{Topic: "t"}}), true, "does not support [action.deliver]"},
		{"queue overlap rejected", TriggerConfig{Name: "q", Schedule: &ScheduleConfig{Cron: "@daily"}, Action: ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}, Policy: TriggerPolicy{Overlap: "queue"}}, false, "not supported in v1"},
		{"bad overlap", TriggerConfig{Name: "q", Schedule: &ScheduleConfig{Cron: "@daily"}, Action: ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}, Policy: TriggerPolicy{Overlap: "sometimes"}}, false, "is invalid"},
		{"bad cron", schedTrigger("fash", ScheduleConfig{Cron: "not a cron"}, ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}), false, "cron"},
		{"bad timezone", schedTrigger("fash", ScheduleConfig{Cron: "@daily", Timezone: "Europe/Londn"}, ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}), false, "timezone"},
		{"zero debounce", watchTrigger("haar", WatchConfig{Repo: "/r", Debounce: "0s"}, ActionConfig{Type: ActionCommand, Command: "x"}), false, "debounce must be > 0"},
		{"zero timeout", schedTrigger("haar", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionCommand, Command: "x", Repo: "/tmp/x", Timeout: "0s"}), false, "timeout must be > 0"},
		{"bad rate_limit", TriggerConfig{Name: "rl", Schedule: &ScheduleConfig{Cron: "@daily"}, Action: ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}, Policy: TriggerPolicy{RateLimit: "0/1h"}}, false, "rate_limit"},
		{"auto_cleanup bad string", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionSession, Prompt: "hi", AutoCleanup: "sometimes"}), true, "auto_cleanup \"sometimes\" is invalid"},
		{"auto_cleanup bad type", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionSession, Prompt: "hi", AutoCleanup: 7}), true, "auto_cleanup must be a boolean"},
		{"auto_cleanup on command", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionCommand, Command: "x", Repo: "/tmp/x", AutoCleanup: true}), false, "only valid for a session action"},
		{"auto_cleanup with ensure", watchTrigger("scunner", WatchConfig{Role: "impl"}, ActionConfig{Type: ActionSession, Ensure: true, Prompt: "hi", AutoCleanup: "always"}), true, "incompatible with ensure=true"},
		{"bad idle_timeout", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionSession, Prompt: "hi", IdleTimeout: "soon"}), true, "action.idle_timeout"},
		{"zero idle_timeout", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionSession, Prompt: "hi", IdleTimeout: "0s"}), true, "idle_timeout must be at least 1s"},
		{"sub-second idle_timeout", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionSession, Prompt: "hi", IdleTimeout: "500ms"}), true, "idle_timeout must be at least 1s"},
		{"idle_timeout on command", schedTrigger("scunner", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionCommand, Command: "x", Repo: "/tmp/x", IdleTimeout: "1m"}), false, "idle_timeout is only valid for a session action"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateOne(tc.trig, tc.orchestrator)
			if len(errs) == 0 {
				t.Fatalf("expected error containing %q, got none", tc.wantContains)
			}

			joined := errorsString(errs)
			if !strings.Contains(joined, tc.wantContains) {
				t.Fatalf("expected error containing %q, got: %s", tc.wantContains, joined)
			}
		})
	}
}

func TestValidateTriggers_DuplicateNames(t *testing.T) {
	c := &Config{Triggers: []TriggerConfig{
		schedTrigger("bide", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}),
		schedTrigger("bide", ScheduleConfig{Cron: "@hourly"}, ActionConfig{Type: ActionMessage, Body: "y", Deliver: DeliverConfig{Topic: "t"}}),
	}}

	joined := errorsString(c.validateTriggers())
	if !strings.Contains(joined, "duplicate trigger name") {
		t.Fatalf("expected duplicate error, got %s", joined)
	}
}

func TestValidateTriggers_ReservedScenarioPrefix(t *testing.T) {
	c := &Config{Triggers: []TriggerConfig{
		schedTrigger("scenario:sc-x:daily", ScheduleConfig{Cron: "@daily"}, ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}),
	}}

	joined := errorsString(c.validateTriggers())
	if !strings.Contains(joined, "reserved") {
		t.Fatalf("expected reserved-prefix error, got %s", joined)
	}
}

func errorsString(errs []error) string {
	var b strings.Builder
	for _, e := range errs {
		b.WriteString(e.Error())
		b.WriteString("\n")
	}

	return b.String()
}

func TestTriggerHelpers(t *testing.T) {
	if !(TriggerConfig{}).TriggerEnabled() {
		t.Error("nil Enabled should default true")
	}

	if (TriggerConfig{Enabled: boolPtr(false)}).TriggerEnabled() {
		t.Error("explicit false should disable")
	}

	if got := (WatchConfig{}).DebounceDuration(); got != 30*time.Second {
		t.Errorf("default debounce = %v, want 30s", got)
	}

	if got := (WatchConfig{Debounce: "3s"}).DebounceDuration(); got != 3*time.Second {
		t.Errorf("debounce = %v, want 3s", got)
	}

	if got := (ActionConfig{}).TimeoutDuration(); got != 5*time.Minute {
		t.Errorf("default timeout = %v, want 5m", got)
	}

	if !(ActionConfig{}).Sandboxed() {
		t.Error("nil sandbox should default true")
	}

	if (ActionConfig{Sandbox: boolPtr(false)}).Sandboxed() {
		t.Error("explicit false should be unsandboxed")
	}

	if got := (TriggerPolicy{}).OverlapMode(); got != OverlapSkip {
		t.Errorf("default overlap = %q, want skip", got)
	}

	if got := (TriggerPolicy{Overlap: "allow"}).OverlapMode(); got != OverlapAllow {
		t.Errorf("overlap = %q, want allow", got)
	}

	if got := (TriggersRuntime{}).MaxConcurrentOr(); got != 4 {
		t.Errorf("default max_concurrent = %d, want 4", got)
	}
}

func TestAutoCleanupMode(t *testing.T) {
	cases := []struct {
		name    string
		val     any
		want    string
		wantErr bool
	}{
		{"absent", nil, "", false},
		{"bool true", true, CleanupAlways, false},
		{"bool false", false, "", false},
		{"empty string", "", "", false},
		{"string true", "true", CleanupAlways, false},
		{"string false", "false", "", false},
		{"always", "always", CleanupAlways, false},
		{"on_success", "on_success", CleanupOnSuccess, false},
		{"bad string", "whiles", "", true},
		{"bad type", int64(3), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (ActionConfig{AutoCleanup: tc.val}).AutoCleanupMode()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %v, got mode %q", tc.val, got)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got != tc.want {
				t.Fatalf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAutoCleanupDecodes proves the bool|string union survives TOML decoding
// both ways, since AutoCleanup is typed as any specifically to accept either.
func TestAutoCleanupDecodes(t *testing.T) {
	cases := []struct {
		name string
		toml string
		want string
	}{
		{"bool", `auto_cleanup = true`, CleanupAlways},
		{"string", `auto_cleanup = "on_success"`, CleanupOnSuccess},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a ActionConfig
			if err := toml.Unmarshal([]byte(tc.toml), &a); err != nil {
				t.Fatalf("decode: %v", err)
			}

			got, err := a.AutoCleanupMode()
			if err != nil {
				t.Fatalf("mode: %v", err)
			}

			if got != tc.want {
				t.Fatalf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSessionIdleTimeout(t *testing.T) {
	cases := []struct {
		name    string
		action  ActionConfig
		want    time.Duration
		wantErr bool
	}{
		{"explicit wins over always", ActionConfig{AutoCleanup: true, IdleTimeout: "5m"}, 5 * time.Minute, false},
		{"explicit without cleanup", ActionConfig{IdleTimeout: "30s"}, 30 * time.Second, false},
		{"always defaults to 1m", ActionConfig{AutoCleanup: "always"}, time.Minute, false},
		{"true (=always) defaults to 1m", ActionConfig{AutoCleanup: true}, time.Minute, false},
		{"on_success is not auto-idled", ActionConfig{AutoCleanup: "on_success"}, 0, false},
		{"disabled has no override", ActionConfig{}, 0, false},
		{"bad idle_timeout errors", ActionConfig{IdleTimeout: "soon"}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.action.SessionIdleTimeout()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got != tc.want {
				t.Fatalf("idle timeout = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRateLimitParsed(t *testing.T) {
	cases := []struct {
		in    string
		wantN int
		wantW time.Duration
	}{
		{"", 5, 30 * time.Minute},
		{"10/1h", 10, time.Hour},
		{"3/30m", 3, 30 * time.Minute},
		{"garbage", 5, 30 * time.Minute},
		{"0/5m", 5, 30 * time.Minute},
		{"5/notaduration", 5, 30 * time.Minute},
	}
	for _, tc := range cases {
		n, w := (TriggerPolicy{RateLimit: tc.in}).RateLimitParsed()
		if n != tc.wantN || w != tc.wantW {
			t.Errorf("RateLimitParsed(%q) = (%d,%v), want (%d,%v)", tc.in, n, w, tc.wantN, tc.wantW)
		}
	}
}
