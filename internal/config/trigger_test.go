package config

import (
	"os"
	"path/filepath"
	"slices"
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
		{
			name:         "schedule tracker minimal",
			trig:         schedTrigger("braw-tracker", ScheduleConfig{Every: "5m"}, ActionConfig{Type: ActionTracker, Prompt: "work #{issue_number}", Tracker: &TrackerConfig{Repo: "/tmp/croft"}}),
			orchestrator: true,
		},
		{
			name: "schedule tracker full knobs",
			trig: schedTrigger("canny-tracker", ScheduleConfig{Cron: "@hourly"}, ActionConfig{Type: ActionTracker, Prompt: "{issue_title}", Tracker: &TrackerConfig{
				Provider: "github", Repo: "/tmp/croft", ActiveState: "open", ActiveLabels: []string{"thrawn"},
				Assignee: "@me", Grace: "10m", MaxConcurrent: 3, Reap: "delete", Limit: 25,
			}}),
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
		{"tightened descriptor rejected", schedTrigger("fash", ScheduleConfig{Cron: "@yearly"}, ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}), false, "unsupported descriptor"},
		{"seconds field rejected", schedTrigger("fash", ScheduleConfig{Cron: "0 0 9 * * *"}, ActionConfig{Type: ActionMessage, Body: "x", Deliver: DeliverConfig{Topic: "t"}}), false, "must have exactly 5 fields"},
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
		{"tracker needs orchestrator", schedTrigger("scunner", ScheduleConfig{Every: "5m"}, ActionConfig{Type: ActionTracker, Tracker: &TrackerConfig{Repo: "/tmp/x"}}), false, "requires [orchestrator] enabled"},
		{"tracker no block", schedTrigger("scunner", ScheduleConfig{Every: "5m"}, ActionConfig{Type: ActionTracker}), true, "requires an [action.tracker] block"},
		{"tracker no repo", schedTrigger("scunner", ScheduleConfig{Every: "5m"}, ActionConfig{Type: ActionTracker, Tracker: &TrackerConfig{}}), true, "requires action.tracker.repo"},
		{"tracker on watch source", watchTrigger("scunner", WatchConfig{Repo: "/r"}, ActionConfig{Type: ActionTracker, Tracker: &TrackerConfig{Repo: "/tmp/x"}}), true, "requires a [schedule] source"},
		{"tracker bad provider", schedTrigger("scunner", ScheduleConfig{Every: "5m"}, ActionConfig{Type: ActionTracker, Tracker: &TrackerConfig{Repo: "/tmp/x", Provider: "jira"}}), true, "provider \"jira\" is unsupported"},
		{"tracker bad active_state", schedTrigger("scunner", ScheduleConfig{Every: "5m"}, ActionConfig{Type: ActionTracker, Tracker: &TrackerConfig{Repo: "/tmp/x", ActiveState: "wibbly"}}), true, "active_state \"wibbly\" is invalid"},
		{"tracker bad reap", schedTrigger("scunner", ScheduleConfig{Every: "5m"}, ActionConfig{Type: ActionTracker, Tracker: &TrackerConfig{Repo: "/tmp/x", Reap: "incinerate"}}), true, "reap \"incinerate\" is invalid"},
		{"tracker bad grace", schedTrigger("scunner", ScheduleConfig{Every: "5m"}, ActionConfig{Type: ActionTracker, Tracker: &TrackerConfig{Repo: "/tmp/x", Grace: "soon"}}), true, "grace"},
		{"tracker negative max_concurrent", schedTrigger("scunner", ScheduleConfig{Every: "5m"}, ActionConfig{Type: ActionTracker, Tracker: &TrackerConfig{Repo: "/tmp/x", MaxConcurrent: -1}}), true, "max_concurrent must not be negative"},
		{"tracker negative limit", schedTrigger("scunner", ScheduleConfig{Every: "5m"}, ActionConfig{Type: ActionTracker, Tracker: &TrackerConfig{Repo: "/tmp/x", Limit: -5}}), true, "limit must not be negative"},
		{"tracker overlap allow", TriggerConfig{Name: "scunner", Schedule: &ScheduleConfig{Every: "5m"}, Action: ActionConfig{Type: ActionTracker, Tracker: &TrackerConfig{Repo: "/tmp/x"}}, Policy: TriggerPolicy{Overlap: "allow"}}, true, "requires policy.overlap = skip"},
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

func TestValidateTriggers_TrackerReapDeleteNeedsRetention(t *testing.T) {
	trig := schedTrigger("dreich-tracker", ScheduleConfig{Every: "5m"},
		ActionConfig{Type: ActionTracker, Tracker: &TrackerConfig{Repo: "/tmp/croft", Reap: "delete"}})

	// retention disabled → reap = delete would become a hard purge → rejected.
	off := &Config{Orchestrator: OrchestratorConfig{Enabled: true}, Delete: Delete{Retention: "0"}, Triggers: []TriggerConfig{trig}}
	if joined := errorsString(off.validateTriggers()); !strings.Contains(joined, "requires [delete] retention > 0") {
		t.Fatalf("expected retention error, got: %s", joined)
	}

	// retention enabled → allowed.
	on := &Config{Orchestrator: OrchestratorConfig{Enabled: true}, Delete: Delete{Retention: "24h"}, Triggers: []TriggerConfig{trig}}
	if errs := on.validateTriggers(); len(errs) != 0 {
		t.Fatalf("expected valid with retention, got %v", errs)
	}
}

func TestTrackerConfigDefaults(t *testing.T) {
	var empty TrackerConfig

	if got := empty.ProviderOr(); got != TrackerProviderGitHub {
		t.Errorf("ProviderOr() = %q, want %q", got, TrackerProviderGitHub)
	}

	if got := empty.ActiveStateOr(); got != TrackerStateOpen {
		t.Errorf("ActiveStateOr() = %q, want %q", got, TrackerStateOpen)
	}

	if got := empty.ReapMode(); got != TrackerReapStop {
		t.Errorf("ReapMode() = %q, want %q", got, TrackerReapStop)
	}

	if got := empty.GraceDuration(); got != defaultTrackerGrace {
		t.Errorf("GraceDuration() = %v, want %v", got, defaultTrackerGrace)
	}

	if got := empty.LimitOr(); got != defaultTrackerLimit {
		t.Errorf("LimitOr() = %d, want %d", got, defaultTrackerLimit)
	}

	set := TrackerConfig{Provider: "github", ActiveState: "all", Reap: "delete", Grace: "10m", Limit: 7}
	if got := set.GraceDuration(); got != 10*time.Minute {
		t.Errorf("GraceDuration() = %v, want 10m", got)
	}

	if got := set.LimitOr(); got != 7 {
		t.Errorf("LimitOr() = %d, want 7", got)
	}

	if got := set.ActiveStateOr(); got != TrackerStateAll {
		t.Errorf("ActiveStateOr() = %q, want all", got)
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

// TestTriggersAdvancedDefaults covers the [triggers.advanced] accessor fallbacks:
// an empty runtime resolves to the historical daemon defaults (issue #1248).
func TestTriggersAdvancedDefaults(t *testing.T) {
	r := TriggersRuntime{}

	if got := r.SchedulerTickDuration(); got != time.Second {
		t.Errorf("scheduler_tick = %v, want 1s", got)
	}

	if got := r.RunHistoryMax(); got != 20 {
		t.Errorf("run_history_max = %d, want 20", got)
	}

	if got := r.WatchReconcileIntervalDuration(); got != 2*time.Second {
		t.Errorf("watch_reconcile_interval = %v, want 2s", got)
	}

	if got := r.WatchRetryBaseBackoffDuration(); got != 5*time.Second {
		t.Errorf("watch_retry_base_backoff = %v, want 5s", got)
	}

	if got := r.WatchRetryMaxBackoffDuration(); got != 5*time.Minute {
		t.Errorf("watch_retry_max_backoff = %v, want 5m", got)
	}

	if got := r.CommandOutputCap(); got != 4096 {
		t.Errorf("command_output_cap = %d, want 4096", got)
	}

	if got := r.WatchBuiltinIgnores(); !slices.Equal(got, DefaultWatchBuiltinIgnores) {
		t.Errorf("watch_builtin_ignores = %v, want %v", got, DefaultWatchBuiltinIgnores)
	}
}

// TestTriggersAdvancedOverrides covers explicit values overriding the defaults,
// and rejects invalid/non-positive values back to the fallback.
func TestTriggersAdvancedOverrides(t *testing.T) {
	r := TriggersRuntime{Advanced: TriggersAdvancedConfig{
		SchedulerTick:          "500ms",
		RunHistoryMax:          5,
		WatchReconcileInterval: "10s",
		WatchRetryBaseBackoff:  "1s",
		WatchRetryMaxBackoff:   "1m",
		WatchBuiltinIgnores:    []string{"node_modules/"},
		CommandOutputCap:       128,
	}}

	if got := r.SchedulerTickDuration(); got != 500*time.Millisecond {
		t.Errorf("scheduler_tick = %v, want 500ms", got)
	}

	if got := r.RunHistoryMax(); got != 5 {
		t.Errorf("run_history_max = %d, want 5", got)
	}

	if got := r.WatchReconcileIntervalDuration(); got != 10*time.Second {
		t.Errorf("watch_reconcile_interval = %v, want 10s", got)
	}

	if got := r.WatchRetryBaseBackoffDuration(); got != time.Second {
		t.Errorf("watch_retry_base_backoff = %v, want 1s", got)
	}

	if got := r.WatchRetryMaxBackoffDuration(); got != time.Minute {
		t.Errorf("watch_retry_max_backoff = %v, want 1m", got)
	}

	if got := r.CommandOutputCap(); got != 128 {
		t.Errorf("command_output_cap = %d, want 128", got)
	}

	if got := r.WatchBuiltinIgnores(); !slices.Equal(got, []string{"node_modules/"}) {
		t.Errorf("watch_builtin_ignores = %v, want [node_modules/]", got)
	}

	// Non-positive ints and bad durations fall back to the defaults.
	bad := TriggersRuntime{Advanced: TriggersAdvancedConfig{
		SchedulerTick: "notaduration", RunHistoryMax: -1, CommandOutputCap: 0,
	}}
	if got := bad.SchedulerTickDuration(); got != time.Second {
		t.Errorf("bad scheduler_tick = %v, want 1s fallback", got)
	}

	if got := bad.RunHistoryMax(); got != 20 {
		t.Errorf("negative run_history_max = %d, want 20 fallback", got)
	}

	if got := bad.CommandOutputCap(); got != 4096 {
		t.Errorf("zero command_output_cap = %d, want 4096 fallback", got)
	}
}

// TestTriggersAdvancedWatchRetryBounds verifies degraded-watch retry delays
// remain positive and coherent even when callers construct a config directly.
func TestTriggersAdvancedWatchRetryBounds(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		max      string
		wantBase time.Duration
		wantMax  time.Duration
	}{
		{name: "zero base", base: "0s", wantBase: 5 * time.Second, wantMax: 5 * time.Minute},
		{name: "negative base", base: "-1s", wantBase: 5 * time.Second, wantMax: 5 * time.Minute},
		{name: "zero max", max: "0s", wantBase: 5 * time.Second, wantMax: 5 * time.Minute},
		{name: "negative max", max: "-1s", wantBase: 5 * time.Second, wantMax: 5 * time.Minute},
		{name: "base above max", base: "10s", max: "1s", wantBase: time.Second, wantMax: time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := TriggersRuntime{Advanced: TriggersAdvancedConfig{
				WatchRetryBaseBackoff: tc.base,
				WatchRetryMaxBackoff:  tc.max,
			}}

			if got := r.WatchRetryBaseBackoffDuration(); got != tc.wantBase {
				t.Errorf("WatchRetryBaseBackoffDuration() = %v, want %v", got, tc.wantBase)
			}

			if got := r.WatchRetryMaxBackoffDuration(); got != tc.wantMax {
				t.Errorf("WatchRetryMaxBackoffDuration() = %v, want %v", got, tc.wantMax)
			}
		})
	}
}

// TestTriggersTickerCadenceNonPositiveSafety proves the two [triggers.advanced]
// cadences that feed time.NewTicker (scheduler_tick, watch_reconcile_interval)
// fall back to their documented defaults for "0", "0s", and negative values, so a
// validly-parsed config can never construct a non-positive ticker (issue #1285).
func TestTriggersTickerCadenceNonPositiveSafety(t *testing.T) {
	for _, bad := range []string{"0", "0s", "-1s", "-250ms"} {
		r := TriggersRuntime{Advanced: TriggersAdvancedConfig{
			SchedulerTick:          bad,
			WatchReconcileInterval: bad,
		}}

		if got := r.SchedulerTickDuration(); got != defaultSchedulerTick {
			t.Errorf("SchedulerTickDuration(%q) = %v, want default %v", bad, got, defaultSchedulerTick)
		}

		if got := r.WatchReconcileIntervalDuration(); got != defaultWatchReconcile {
			t.Errorf("WatchReconcileIntervalDuration(%q) = %v, want default %v", bad, got, defaultWatchReconcile)
		}

		// The values fed to time.NewTicker must be strictly positive.
		if got := r.SchedulerTickDuration(); got <= 0 {
			t.Errorf("SchedulerTickDuration(%q) = %v, must be > 0 for time.NewTicker", bad, got)
		}

		if got := r.WatchReconcileIntervalDuration(); got <= 0 {
			t.Errorf("WatchReconcileIntervalDuration(%q) = %v, must be > 0 for time.NewTicker", bad, got)
		}
	}
}

// TestWatchBuiltinIgnoresCopy verifies the accessor returns a fresh slice so a
// caller can't mutate the shared default.
func TestWatchBuiltinIgnoresCopy(t *testing.T) {
	got := (TriggersRuntime{}).WatchBuiltinIgnores()
	got[0] = "mutated"

	if DefaultWatchBuiltinIgnores[0] == "mutated" {
		t.Fatal("WatchBuiltinIgnores must not alias the shared default slice")
	}
}

// TestWatchBuiltinIgnoresExplicitEmpty is the #1309 regression for the
// nil-versus-present-empty conflation: an explicit `watch_builtin_ignores = []`
// must resolve to "only the mandatory ignores" (an empty list), while an omitted
// key still resolves to the full default list. Before the fix the accessor tested
// len == 0, so `[]` was indistinguishable from unset and restored the defaults.
func TestWatchBuiltinIgnoresExplicitEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[triggers.advanced]\nwatch_builtin_ignores = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	// An explicit [] must decode to a present (non-nil) empty slice so the accessor
	// can tell it apart from an omitted key.
	if cfg.TriggersRuntime.Advanced.WatchBuiltinIgnores == nil {
		t.Fatal("explicit empty watch_builtin_ignores decoded as unset (nil)")
	}

	if got := cfg.TriggersRuntime.WatchBuiltinIgnores(); len(got) != 0 {
		t.Fatalf("explicit empty watch_builtin_ignores resolved to %v, want empty", got)
	}

	// An omitted key still yields the defaults (a zero runtime has a nil field).
	if got := (TriggersRuntime{}).WatchBuiltinIgnores(); !slices.Equal(got, DefaultWatchBuiltinIgnores) {
		t.Fatalf("unset watch_builtin_ignores = %v, want defaults %v", got, DefaultWatchBuiltinIgnores)
	}
}

// TestTriggersAdvancedEmbeddedDefaults is the drift guard (epic #1230 pattern):
// the advanced tuning defaults must live in the embedded default_config.toml, not
// only as Go fallback literals. It asserts the RAW fields parsed from the embedded
// TOML (not just the accessors, which pass whether from TOML or the Go fallback).
func TestTriggersAdvancedEmbeddedDefaults(t *testing.T) {
	a := Default().TriggersRuntime.Advanced

	strChecks := map[string]struct{ got, want string }{
		"scheduler_tick":           {a.SchedulerTick, "1s"},
		"watch_reconcile_interval": {a.WatchReconcileInterval, "2s"},
		"watch_retry_base_backoff": {a.WatchRetryBaseBackoff, "5s"},
		"watch_retry_max_backoff":  {a.WatchRetryMaxBackoff, "5m"},
	}
	for name, c := range strChecks {
		if c.got != c.want {
			t.Errorf("Default().TriggersRuntime.Advanced.%s = %q, want %q (missing from default_config.toml?)", name, c.got, c.want)
		}
	}

	if a.RunHistoryMax != 20 {
		t.Errorf("Default() run_history_max = %d, want 20 (missing from default_config.toml?)", a.RunHistoryMax)
	}

	if a.CommandOutputCap != 4096 {
		t.Errorf("Default() command_output_cap = %d, want 4096 (missing from default_config.toml?)", a.CommandOutputCap)
	}

	if !slices.Equal(a.WatchBuiltinIgnores, DefaultWatchBuiltinIgnores) {
		t.Errorf("Default() watch_builtin_ignores = %v, want %v (missing from default_config.toml?)", a.WatchBuiltinIgnores, DefaultWatchBuiltinIgnores)
	}
}
