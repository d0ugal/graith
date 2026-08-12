package config

import (
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func FuzzExpand(f *testing.F) {
	f.Add("{username}/graith")
	f.Add("--session-id {agent_session_id}")
	f.Add("{session_name}-{session_id}")
	f.Add("--model {model}")
	f.Add("{unknown}")
	f.Add("literal {user-name} braces")
	f.Add("")

	vars := TemplateVars{
		Username:                 "braw-lad",
		AgentSessionID:           "abc-123",
		SessionName:              "canny-fix",
		SessionID:                "a3f2b1c9",
		WorktreePath:             "/tmp/bothy",
		ForkSourceAgentSessionID: "def-456",
		Model:                    "codex",
		Dir:                      "/tmp/croft",
		Profile:                  "braw",
		ReasoningEffort:          "medium",
		ServiceTier:              "auto",
		WebSearch:                true,
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 8192 {
			t.Skip()
		}

		got, err := Expand(input, vars)
		gotAgain, errAgain := Expand(input, vars)

		if (err == nil) != (errAgain == nil) || got != gotAgain {
			t.Fatalf("Expand(%q) was not deterministic: (%q, %v) then (%q, %v)", input, got, err, gotAgain, errAgain)
		}

		wantErr := hasUnknownTemplateToken(input, IsTemplateVar)
		if (err != nil) != wantErr {
			t.Fatalf("Expand(%q) error = %v, want error: %v", input, err, wantErr)
		}

		if err != nil {
			return
		}

		gotSlice, err := ExpandSlice([]string{input}, vars)
		if err != nil {
			t.Fatalf("ExpandSlice(%q) failed after Expand succeeded: %v", input, err)
		}

		if len(gotSlice) != 1 || gotSlice[0] != got {
			t.Fatalf("ExpandSlice(%q) = %#v, want [%q]", input, gotSlice, got)
		}
	})
}

func FuzzExpandTrigger(f *testing.F) {
	f.Add("report {name} on {date}")
	f.Add("{change_count} files in {session_name}")
	f.Add("at {worktree_path}: {changed_files}")
	f.Add("alert {gcx_event_id}")
	f.Add("body: {issue_body}")
	f.Add("{unknown_trigger}")
	f.Add("literal {user-name} braces")
	f.Add("")

	const issueBody = "Use the canny path with {worktree_path} and {gcx_event_url}."

	vars := TriggerVars{
		Name:             "canny-lint",
		Date:             "2026-07-11",
		Datetime:         "2026-07-11T09:00:00Z",
		FireTime:         "2026-07-11T09:00:00Z",
		SessionName:      "braw",
		WorktreePath:     "/tmp/bothy",
		ChangedFiles:     "glen/a.go, glen/b.go",
		ChangeCount:      "2",
		ScenarioID:       "sc-braw",
		ScenarioName:     "strath",
		CompletionEpoch:  "7",
		ResultIndex:      "results/index.json",
		IssueNumber:      "643",
		IssueTitle:       "Inspect the brig",
		IssueBody:        issueBody,
		IssueURL:         "https://example.invalid/issues/643",
		IssueLabels:      "braw,canny",
		GCXEventID:       "AG-BRAW",
		GCXEventKind:     "oncall_alert_group",
		GCXEventState:    "firing",
		GCXEventURL:      "https://example.invalid/alerts/ag-braw",
		GCXTeamID:        "team-braw",
		GCXIntegrationID: "int-canny",
		GCXStartedAt:     "2026-07-11T08:59:00Z",
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 8192 {
			t.Skip()
		}

		got, err := ExpandTrigger(input, vars)
		gotAgain, errAgain := ExpandTrigger(input, vars)

		if (err == nil) != (errAgain == nil) || got != gotAgain {
			t.Fatalf("ExpandTrigger(%q) was not deterministic: (%q, %v) then (%q, %v)", input, got, err, gotAgain, errAgain)
		}

		wantErr := hasUnknownTemplateToken(input, IsTriggerTemplateVar)
		if (err != nil) != wantErr {
			t.Fatalf("ExpandTrigger(%q) error = %v, want error: %v", input, err, wantErr)
		}

		if err != nil {
			if got != "" {
				t.Fatalf("ExpandTrigger(%q) returned %q with error %v, want empty output", input, got, err)
			}

			return
		}

		if strings.Contains(input, "{issue_body}") && !strings.Contains(got, issueBody) {
			t.Fatalf("ExpandTrigger(%q) = %q, want issue body left unexpanded as %q", input, got, issueBody)
		}
	})
}

func FuzzParseDurationWithDays(f *testing.F) {
	for _, seed := range []string{
		"",
		"0",
		"0s",
		"1ns",
		"500ms",
		"1s",
		"15m",
		"1h30m",
		"7d",
		"7d12h",
		" 12h ",
		"\t2d3h\n",
		"-1s",
		"-1d",
		"1d-2h",
		"1.5d",
		"soon",
		"999999999999999999999d",
		"9223372036854775807h",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 256 {
			t.Skip()
		}

		got, err := ParseDurationWithDays(input)
		gotAgain, errAgain := ParseDurationWithDays(input)

		if got != gotAgain || fuzzErrorString(err) != fuzzErrorString(errAgain) {
			t.Fatalf("ParseDurationWithDays(%q) was not deterministic: (%v, %v) then (%v, %v)", input, got, err, gotAgain, errAgain)
		}

		trimmed := strings.TrimSpace(input)
		gotTrimmed, errTrimmed := ParseDurationWithDays(trimmed)

		if got != gotTrimmed || fuzzErrorString(err) != fuzzErrorString(errTrimmed) {
			t.Fatalf("ParseDurationWithDays(%q) disagrees with trimmed input %q: (%v, %v) vs (%v, %v)",
				input, trimmed, got, err, gotTrimmed, errTrimmed)
		}

		if err == nil && got < 0 {
			t.Fatalf("ParseDurationWithDays(%q) accepted negative duration %v", input, got)
		}
	})
}

const (
	triggerSourceSchedule = 1 << iota
	triggerSourceWatch
	triggerSourceGCX
	triggerSourceCompletion
)

const (
	triggerFlagEnsure = 1 << iota
	triggerFlagHeadless
	triggerFlagMutating
	triggerFlagSandboxSet
	triggerFlagSandboxValue
	triggerFlagTrackerBlock
)

func FuzzValidateTriggerStructure(f *testing.F) {
	type seed struct {
		sourceMask           int
		cron                 string
		timezone             string
		watchRepo            string
		watchRole            string
		gcxContext           string
		gcxEvent             string
		gcxOnCallUserID      string
		gcxScheduleIDs       string
		gcxTeamIDs           string
		gcxIntegrationIDs    string
		gcxStates            string
		completionEvent      string
		actionType           string
		command              string
		repo                 string
		prompt               string
		autoCleanup          string
		scenario             string
		body                 string
		deliverInbox         string
		deliverTopic         string
		trackerRepo          string
		trackerProvider      string
		trackerActiveState   string
		trackerReap          string
		overlap              string
		rateLimit            string
		durationA            string
		durationB            string
		durationC            string
		cleanupKind          int
		flags                int
		gcxLimit             int
		trackerMaxConcurrent int
		trackerLimit         int
	}

	seeds := []seed{
		{
			sourceMask:   triggerSourceSchedule,
			cron:         "@daily",
			timezone:     "UTC",
			actionType:   ActionMessage,
			body:         "braw report",
			deliverTopic: "reports",
		},
		{
			sourceMask: triggerSourceSchedule,
			actionType: ActionCommand,
			command:    "go test ./internal/config",
			repo:       "/tmp/croft",
			durationA:  "15m",
			durationB:  "5m",
		},
		{
			sourceMask: triggerSourceWatch,
			watchRepo:  "/tmp/croft",
			actionType: ActionCommand,
			command:    "go test ./...",
			durationB:  "30s",
		},
		{
			sourceMask:      triggerSourceGCX,
			gcxContext:      "croft",
			gcxEvent:        GCXEventOnCallAlertGroup,
			gcxOnCallUserID: GCXOnCallAnyUser,
			gcxScheduleIDs:  "S-BRAW,S-CANNY",
			gcxStates:       "firing,acknowledged",
			actionType:      ActionSession,
			prompt:          "investigate {gcx_event_id}",
			durationA:       "1m",
			durationB:       "30s",
			durationC:       "24h",
			flags:           triggerFlagEnsure,
			gcxLimit:        25,
		},
		{
			sourceMask:      triggerSourceCompletion,
			completionEvent: "complete",
			actionType:      ActionMessage,
			body:            "done",
			deliverInbox:    "orchestrator",
		},
		{
			sourceMask: triggerSourceSchedule,
			cron:       "@hourly",
			actionType: ActionScenario,
			scenario:   "strath",
		},
		{
			sourceMask:         triggerSourceSchedule,
			actionType:         ActionTracker,
			trackerRepo:        "/tmp/croft",
			trackerProvider:    TrackerProviderGitHub,
			trackerActiveState: TrackerStateOpen,
			trackerReap:        TrackerReapStop,
			durationA:          "5m",
			durationC:          "10m",
			flags:              triggerFlagTrackerBlock,
			trackerLimit:       50,
		},
		{
			sourceMask: triggerSourceSchedule | triggerSourceWatch,
			cron:       "@daily",
			watchRole:  "impl",
			actionType: ActionMessage,
			body:       "two sources",
		},
		{
			sourceMask:  0,
			actionType:  ActionSession,
			prompt:      "missing source",
			autoCleanup: "always",
			cleanupKind: 3,
		},
		{
			sourceMask:  triggerSourceSchedule,
			cron:        "@daily",
			actionType:  ActionSession,
			prompt:      "brief",
			autoCleanup: "on_success",
			cleanupKind: 3,
			durationC:   "1s",
		},
		{
			sourceMask:  triggerSourceSchedule,
			cron:        "@daily",
			actionType:  ActionSession,
			prompt:      "brief",
			autoCleanup: "sometimes",
			cleanupKind: 3,
		},
		{
			sourceMask:  triggerSourceSchedule,
			cron:        "@daily",
			actionType:  ActionSession,
			prompt:      "brief",
			cleanupKind: 6,
		},
		{
			sourceMask: triggerSourceSchedule,
			actionType: ActionMessage,
			body:       "bad duration",
			durationA:  "0s",
			durationB:  "500ms",
			durationC:  "-1s",
		},
		{
			sourceMask:           triggerSourceSchedule,
			actionType:           ActionTracker,
			trackerRepo:          "/tmp/croft",
			trackerProvider:      "jira",
			trackerActiveState:   "wibbly",
			trackerReap:          "incinerate",
			durationA:            "5m",
			durationC:            "soon",
			flags:                triggerFlagTrackerBlock,
			trackerMaxConcurrent: -1,
			trackerLimit:         -1,
		},
	}

	for _, seed := range seeds {
		f.Add(seed.sourceMask, seed.cron, seed.timezone, seed.watchRepo, seed.watchRole,
			seed.gcxContext, seed.gcxEvent, seed.gcxOnCallUserID, seed.gcxScheduleIDs,
			seed.gcxTeamIDs, seed.gcxIntegrationIDs, seed.gcxStates, seed.completionEvent,
			seed.actionType, seed.command, seed.repo, seed.prompt, seed.autoCleanup,
			seed.scenario, seed.body, seed.deliverInbox, seed.deliverTopic, seed.trackerRepo,
			seed.trackerProvider, seed.trackerActiveState, seed.trackerReap, seed.overlap,
			seed.rateLimit, seed.durationA, seed.durationB, seed.durationC, seed.cleanupKind,
			seed.flags, seed.gcxLimit, seed.trackerMaxConcurrent, seed.trackerLimit)
	}

	f.Fuzz(func(t *testing.T,
		sourceMask int,
		cron, timezone, watchRepo, watchRole string,
		gcxContext, gcxEvent, gcxOnCallUserID, gcxScheduleIDs, gcxTeamIDs, gcxIntegrationIDs, gcxStates string,
		completionEvent string,
		actionType, command, repo, prompt, autoCleanup, scenario, body, deliverInbox, deliverTopic string,
		trackerRepo, trackerProvider, trackerActiveState, trackerReap string,
		overlap, rateLimit, durationA, durationB, durationC string,
		cleanupKind, flags, gcxLimit, trackerMaxConcurrent, trackerLimit int,
	) {
		skipLargeFuzzStrings(t, 256, 3072,
			cron, timezone, watchRepo, watchRole,
			gcxContext, gcxEvent, gcxOnCallUserID, gcxScheduleIDs, gcxTeamIDs, gcxIntegrationIDs, gcxStates,
			completionEvent, actionType, command, repo, prompt, autoCleanup, scenario, body, deliverInbox,
			deliverTopic, trackerRepo, trackerProvider, trackerActiveState, trackerReap, overlap, rateLimit,
			durationA, durationB, durationC)

		trig := TriggerConfig{
			Action: ActionConfig{
				Type:        actionType,
				Command:     command,
				Repo:        repo,
				Timeout:     durationB,
				Mutating:    flags&triggerFlagMutating != 0,
				Sandbox:     fuzzBoolPtr(flags),
				Prompt:      prompt,
				Headless:    flags&triggerFlagHeadless != 0,
				Ensure:      flags&triggerFlagEnsure != 0,
				AutoCleanup: fuzzAutoCleanup(cleanupKind, autoCleanup),
				IdleTimeout: durationC,
				Scenario:    scenario,
				Tracker:     fuzzTrackerConfig(flags, trackerRepo, trackerProvider, trackerActiveState, durationC, trackerMaxConcurrent, trackerReap, trackerLimit),
				Body:        body,
				Deliver:     DeliverConfig{Inbox: deliverInbox, Topic: deliverTopic},
			},
			Policy: TriggerPolicy{Overlap: overlap, RateLimit: rateLimit},
		}

		if sourceMask&triggerSourceSchedule != 0 {
			schedule := ScheduleConfig{Cron: cron, Every: durationA, Timezone: timezone}
			trig.Schedule = &schedule
		}

		if sourceMask&triggerSourceWatch != 0 {
			watch := WatchConfig{Repo: watchRepo, Role: watchRole, Debounce: durationB}
			trig.Watch = &watch
		}

		if sourceMask&triggerSourceGCX != 0 {
			gcx := GCXConfig{
				Event:          gcxEvent,
				Context:        gcxContext,
				Every:          durationA,
				Timeout:        durationB,
				OnCallUserID:   gcxOnCallUserID,
				ScheduleIDs:    splitFuzzList(gcxScheduleIDs),
				TeamIDs:        splitFuzzList(gcxTeamIDs),
				IntegrationIDs: splitFuzzList(gcxIntegrationIDs),
				States:         splitFuzzList(gcxStates),
				MaxAge:         durationC,
				Limit:          gcxLimit,
			}
			trig.GCX = &gcx
		}

		if sourceMask&triggerSourceCompletion != 0 {
			completion := CompletionConfig{Event: completionEvent}
			trig.Completion = &completion
		}

		errs := ValidateTriggerStructure("trigger fuzz", &trig)
		errsAgain := ValidateTriggerStructure("trigger fuzz", &trig)
		gotText := errorsString(errs)
		gotTextAgain := errorsString(errsAgain)

		if gotText != gotTextAgain {
			t.Fatalf("ValidateTriggerStructure was not deterministic:\nfirst:\n%s\nsecond:\n%s", gotText, gotTextAgain)
		}

		sourceCount := fuzzTriggerSourceCount(&trig)
		if sourceCount != 1 && !strings.Contains(gotText, "exactly one of [schedule]") {
			t.Fatalf("ValidateTriggerStructure with %d sources did not report the exactly-one-source rule; errors:\n%s", sourceCount, gotText)
		}

		if len(errs) == 0 {
			assertAcceptedTriggerAccessors(t, &trig)
		}
	})
}

func FuzzConfigValidateAccessors(f *testing.F) {
	for _, seed := range []struct {
		reloadDebounce        string
		updatesInterval       string
		updatesTimeout        string
		statusTTL             string
		connectionDial        string
		connectionStartPoll   string
		launchWatchdog        string
		launchSlotPoll        string
		lifecycleProcessKill  string
		messagesMaxAge        string
		messagesBusyTimeout   string
		todoClaimLease        string
		gitPullInterval       string
		triggerSchedulerTick  string
		triggerWatchRetryBase string
		triggerWatchRetryMax  string
		watchBuiltinIgnore    string
		flags                 int
	}{
		{"200ms", "1h", "5s", "30s", "500ms", "50ms", "15s", "100ms", "3s", "30d", "5s", "30m", "1h", "1s", "5s", "5m", "node_modules/", 0},
		{" 200ms ", " 2h ", " 10s ", " 45s ", " 750ms ", " 100ms ", " 30s ", " 250ms ", " 5s ", " 12h ", " 1s ", " 0 ", " 2h ", " 500ms ", " 1s ", " 1m ", "", 1},
		{"0s", "0s", "0s", "bad", "-1s", "soon", "0s", "-1s", "0s", "-5m", "500us", "bad", "30s", "0s", "10s", "1s", "", 1},
		{"999999999999999999999d", "1.5d", "soon", "", "1ns", "1ns", "1ns", "1ns", "1ns", "0", "5m", "7d", "1m", "bad", "-1s", "0s", "*.swp", 0},
	} {
		f.Add(seed.reloadDebounce, seed.updatesInterval, seed.updatesTimeout, seed.statusTTL,
			seed.connectionDial, seed.connectionStartPoll, seed.launchWatchdog, seed.launchSlotPoll,
			seed.lifecycleProcessKill, seed.messagesMaxAge, seed.messagesBusyTimeout, seed.todoClaimLease,
			seed.gitPullInterval, seed.triggerSchedulerTick, seed.triggerWatchRetryBase, seed.triggerWatchRetryMax,
			seed.watchBuiltinIgnore, seed.flags)
	}

	f.Fuzz(func(t *testing.T,
		reloadDebounce, updatesInterval, updatesTimeout, statusTTL string,
		connectionDial, connectionStartPoll, launchWatchdog, launchSlotPoll string,
		lifecycleProcessKill, messagesMaxAge, messagesBusyTimeout, todoClaimLease, gitPullInterval string,
		triggerSchedulerTick, triggerWatchRetryBase, triggerWatchRetryMax, watchBuiltinIgnore string,
		flags int,
	) {
		skipLargeFuzzStrings(t, 256, 3072,
			reloadDebounce, updatesInterval, updatesTimeout, statusTTL, connectionDial, connectionStartPoll,
			launchWatchdog, launchSlotPoll, lifecycleProcessKill, messagesMaxAge, messagesBusyTimeout,
			todoClaimLease, gitPullInterval, triggerSchedulerTick, triggerWatchRetryBase, triggerWatchRetryMax,
			watchBuiltinIgnore)

		cfg := Default()
		cfg.ConfigReload.ReloadDebounce = reloadDebounce
		cfg.Updates.Interval = updatesInterval
		cfg.Updates.Timeout = updatesTimeout
		cfg.Status.TTL = statusTTL
		cfg.Connection.DialTimeout = connectionDial
		cfg.Connection.StartPollInterval = connectionStartPoll
		cfg.Launch.WatchdogInterval = launchWatchdog
		cfg.Launch.SlotPollInterval = launchSlotPoll
		cfg.Lifecycle.ProcessKillGrace = lifecycleProcessKill
		cfg.Messages.MaxAge = messagesMaxAge
		cfg.Messages.BusyTimeout = messagesBusyTimeout
		cfg.Todo.ClaimLease = todoClaimLease
		cfg.GitPull.Interval = gitPullInterval
		cfg.TriggersRuntime.Advanced.SchedulerTick = triggerSchedulerTick
		cfg.TriggersRuntime.Advanced.WatchRetryBaseBackoff = triggerWatchRetryBase
		cfg.TriggersRuntime.Advanced.WatchRetryMaxBackoff = triggerWatchRetryMax

		if flags&1 != 0 {
			cfg.TriggersRuntime.Advanced.WatchBuiltinIgnores = []string{}
		} else if watchBuiltinIgnore != "" {
			cfg.TriggersRuntime.Advanced.WatchBuiltinIgnores = []string{watchBuiltinIgnore}
		} else {
			cfg.TriggersRuntime.Advanced.WatchBuiltinIgnores = nil
		}

		err := cfg.Validate()
		errAgain := cfg.Validate()

		if fuzzErrorString(err) != fuzzErrorString(errAgain) {
			t.Fatalf("Config.Validate was not deterministic:\nfirst: %v\nsecond: %v", err, errAgain)
		}

		if flags&1 != 0 {
			got := cfg.TriggersRuntime.WatchBuiltinIgnores()
			if got == nil || len(got) != 0 {
				t.Fatalf("explicit empty watch_builtin_ignores resolved to %#v, want non-nil empty slice", got)
			}
		}

		if cfg.TriggersRuntime.SchedulerTickDuration() <= 0 {
			t.Fatalf("SchedulerTickDuration() = %v, want a positive fallback", cfg.TriggersRuntime.SchedulerTickDuration())
		}

		if cfg.TriggersRuntime.WatchRetryBaseBackoffDuration() <= 0 || cfg.TriggersRuntime.WatchRetryMaxBackoffDuration() <= 0 {
			t.Fatalf("watch retry accessors returned non-positive values: base=%v max=%v",
				cfg.TriggersRuntime.WatchRetryBaseBackoffDuration(), cfg.TriggersRuntime.WatchRetryMaxBackoffDuration())
		}

		if cfg.TriggersRuntime.WatchRetryBaseBackoffDuration() > cfg.TriggersRuntime.WatchRetryMaxBackoffDuration() {
			t.Fatalf("watch retry base %v exceeded max %v",
				cfg.TriggersRuntime.WatchRetryBaseBackoffDuration(), cfg.TriggersRuntime.WatchRetryMaxBackoffDuration())
		}

		if err == nil {
			assertAcceptedConfigAccessors(t, cfg)
		}
	})
}

func FuzzConfigTOMLDecodeAndValidate(f *testing.F) {
	for _, seed := range []string{
		"",
		`
[[trigger]]
name = "braw-message"
[trigger.schedule]
cron = "@daily"
timezone = "UTC"
[trigger.action]
type = "message"
body = "daily braw"
[trigger.action.deliver]
topic = "reports"
`,
		`
[[trigger]]
name = "canny-command"
[trigger.schedule]
every = "7d12h"
[trigger.action]
type = "command"
command = "go test ./internal/config"
repo = "/tmp/croft"
timeout = "5m"
`,
		`
[[trigger]]
name = "dreich-watch"
[trigger.watch]
repo = "/tmp/croft"
paths = ["**/*.go"]
ignore = []
debounce = "500ms"
[trigger.action]
type = "command"
command = "go test ./..."
`,
		`
[orchestrator]
enabled = true

[[trigger]]
name = "canny-gcx"
[trigger.gcx]
context = "croft"
event = "oncall_alert_group"
every = "1m"
timeout = "30s"
max_age = "24h"
oncall_user_id = "*"
schedule_ids = ["S-BRAW"]
states = ["firing", "acknowledged"]
limit = 25
[trigger.action]
type = "session"
prompt = "Investigate {gcx_event_id}"
ensure = true
`,
		`
[orchestrator]
enabled = true
[delete]
retention = "24h"

[[trigger]]
name = "strath-tracker"
[trigger.schedule]
every = "5m"
[trigger.action]
type = "tracker"
prompt = "Work #{issue_number}"
[trigger.action.tracker]
repo = "/tmp/croft"
provider = "github"
active_state = "open"
grace = "10m"
reap = "delete"
limit = 25
`,
		`
[orchestrator]
enabled = true

[[trigger]]
name = "brief"
[trigger.schedule]
cron = "@hourly"
[trigger.action]
type = "session"
prompt = "brief"
auto_cleanup = true
idle_timeout = "1s"
`,
		`
[[trigger]]
name = "bad-cleanup"
[trigger.schedule]
cron = "@daily"
[trigger.action]
type = "session"
prompt = "brief"
auto_cleanup = 7
`,
		`
[[trigger]]
name = "completion-global"
[trigger.completion]
event = "complete"
[trigger.action]
type = "message"
body = "done"
[trigger.action.deliver]
inbox = "orchestrator"
`,
		`
[triggers.advanced]
watch_builtin_ignores = []
`,
		`
[messages]
max_age = " 12h "
busy_timeout = "1ms"
[todo]
claim_lease = "0"
[config]
reload_debounce = "200ms"
`,
		`
[config]
reload_debounce = "0s"
[connection]
dial_timeout = "-1s"
[messages]
max_age = "-5m"
`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 4096 || strings.Count(input, "\n") > 160 {
			t.Skip()
		}

		cfg, err := LoadBytes("fuzz.toml", []byte(input))
		cfgAgain, errAgain := LoadBytes("fuzz.toml", []byte(input))

		if fuzzErrorString(err) != fuzzErrorString(errAgain) {
			t.Fatalf("LoadBytes was not deterministic:\nfirst: %v\nsecond: %v", err, errAgain)
		}

		if err != nil {
			return
		}

		if err := cfg.Validate(); err != nil {
			t.Fatalf("LoadBytes returned a config that no longer validates: %v", err)
		}

		if errAgain != nil {
			t.Fatalf("second LoadBytes failed after first succeeded: %v", errAgain)
		}

		if cfgAgain == nil {
			t.Fatal("second LoadBytes returned nil config without an error")
		}

		if hasExplicitEmptyWatchBuiltinIgnores(input) {
			got := cfg.TriggersRuntime.WatchBuiltinIgnores()
			if got == nil || len(got) != 0 {
				t.Fatalf("explicit empty watch_builtin_ignores decoded to %#v, want non-nil empty slice", got)
			}
		}
	})
}

func hasUnknownTemplateToken(input string, known func(string) bool) bool {
	for _, match := range varPattern.FindAllStringSubmatch(input, -1) {
		if len(match) == 2 && !known(match[1]) {
			return true
		}
	}

	return false
}

func skipLargeFuzzStrings(t *testing.T, maxOne, maxTotal int, values ...string) {
	t.Helper()

	total := 0

	for _, value := range values {
		if len(value) > maxOne {
			t.Skip()
		}

		total += len(value)
		if total > maxTotal {
			t.Skip()
		}
	}
}

func fuzzErrorString(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

func fuzzAutoCleanup(kind int, raw string) any {
	switch positiveModulo(kind, 7) {
	case 0:
		return nil
	case 1:
		return true
	case 2:
		return false
	case 3:
		return raw
	case 4:
		return CleanupAlways
	case 5:
		return CleanupOnSuccess
	default:
		return []string{raw}
	}
}

func fuzzBoolPtr(flags int) *bool {
	if flags&triggerFlagSandboxSet == 0 {
		return nil
	}

	value := flags&triggerFlagSandboxValue != 0

	return &value
}

func fuzzTrackerConfig(flags int, repo, provider, activeState, grace string, maxConcurrent int, reap string, limit int) *TrackerConfig {
	if flags&triggerFlagTrackerBlock == 0 {
		return nil
	}

	return &TrackerConfig{
		Provider:      provider,
		Repo:          repo,
		ActiveState:   activeState,
		Grace:         grace,
		MaxConcurrent: maxConcurrent,
		Reap:          reap,
		Limit:         limit,
	}
}

func positiveModulo(v, mod int) int {
	if mod <= 0 {
		return 0
	}

	v %= mod
	if v < 0 {
		v += mod
	}

	return v
}

func splitFuzzList(raw string) []string {
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	if len(parts) > 4 {
		parts = parts[:4]
	}

	return append([]string(nil), parts...)
}

func fuzzTriggerSourceCount(t *TriggerConfig) int {
	count := 0

	for _, set := range []bool{t.Schedule != nil, t.Watch != nil, t.GCX != nil, t.Completion != nil} {
		if set {
			count++
		}
	}

	return count
}

func assertAcceptedTriggerAccessors(t *testing.T, trig *TriggerConfig) {
	t.Helper()

	if trig.Schedule != nil && trig.Schedule.Every != "" {
		assertParsedPositiveDuration(t, "schedule.every", trig.Schedule.Every)
	}

	if trig.Watch != nil && trig.Watch.Debounce != "" {
		assertParsedDurationEquals(t, "watch.debounce", trig.Watch.Debounce, trig.Watch.DebounceDuration())
	}

	if trig.GCX != nil {
		if trig.GCX.Every != "" {
			assertParsedDurationEquals(t, "gcx.every", trig.GCX.Every, trig.GCX.EveryDuration())
		}

		if trig.GCX.Timeout != "" {
			assertParsedDurationEquals(t, "gcx.timeout", trig.GCX.Timeout, trig.GCX.TimeoutDuration())
		}

		if trig.GCX.MaxAge != "" {
			assertParsedDurationEquals(t, "gcx.max_age", trig.GCX.MaxAge, trig.GCX.MaxAgeDuration())
		}

		if trig.GCX.Limit > 0 && trig.GCX.LimitOr() != trig.GCX.Limit {
			t.Fatalf("gcx.LimitOr() = %d, want %d", trig.GCX.LimitOr(), trig.GCX.Limit)
		}

		if len(trig.GCX.States) == 0 {
			if got := trig.GCX.StatesOr(); len(got) != 1 || got[0] != "firing" {
				t.Fatalf("gcx.StatesOr() = %#v, want default [firing]", got)
			}
		} else if got := trig.GCX.StatesOr(); !sameStringSlice(got, trig.GCX.States) {
			t.Fatalf("gcx.StatesOr() = %#v, want %#v", got, trig.GCX.States)
		}
	}

	switch trig.Action.Type {
	case ActionCommand:
		if trig.Action.Timeout != "" {
			assertParsedDurationEquals(t, "action.timeout", trig.Action.Timeout, trig.Action.TimeoutDuration())
		}
	case ActionSession:
		got, err := trig.Action.SessionIdleTimeout()
		if err != nil {
			t.Fatalf("SessionIdleTimeout() failed after validation accepted trigger: %v", err)
		}

		if trig.Action.IdleTimeout != "" {
			assertParsedDurationEquals(t, "action.idle_timeout", trig.Action.IdleTimeout, got)
		} else {
			mode, err := trig.Action.AutoCleanupMode()
			if err != nil {
				t.Fatalf("AutoCleanupMode() failed after validation accepted trigger: %v", err)
			}

			if mode == CleanupAlways && got != defaultAutoCleanupIdle {
				t.Fatalf("SessionIdleTimeout() = %v, want default auto-cleanup idle %v", got, defaultAutoCleanupIdle)
			}

			if mode != CleanupAlways && got != 0 {
				t.Fatalf("SessionIdleTimeout() = %v, want 0 for cleanup mode %q", got, mode)
			}
		}
	case ActionTracker:
		if trig.Action.Tracker != nil {
			if trig.Action.Tracker.Grace != "" {
				assertParsedDurationEquals(t, "action.tracker.grace", trig.Action.Tracker.Grace, trig.Action.Tracker.GraceDuration())
			}

			if trig.Action.Tracker.Limit > 0 && trig.Action.Tracker.LimitOr() != trig.Action.Tracker.Limit {
				t.Fatalf("tracker.LimitOr() = %d, want %d", trig.Action.Tracker.LimitOr(), trig.Action.Tracker.Limit)
			}
		}
	}

	if trig.Policy.RateLimit != "" {
		n, window := trig.Policy.RateLimitParsed()
		if n <= 0 || window <= 0 {
			t.Fatalf("RateLimitParsed(%q) = (%d, %v), want positive values", trig.Policy.RateLimit, n, window)
		}
	}
}

func assertAcceptedConfigAccessors(t *testing.T, cfg *Config) {
	t.Helper()

	assertPositiveDurationAccessor(t, "config.reload_debounce", cfg.ConfigReload.ReloadDebounce, cfg.ConfigReload.ReloadDebounceDuration())
	assertPositiveDurationAccessor(t, "updates.interval", cfg.Updates.Interval, cfg.Updates.IntervalDuration())
	assertPositiveDurationAccessor(t, "updates.timeout", cfg.Updates.Timeout, cfg.Updates.TimeoutDuration())
	assertDurationAccessor(t, "status.ttl", cfg.Status.TTL, cfg.Status.TTLDuration())
	assertPositiveDurationAccessor(t, "connection.dial_timeout", cfg.Connection.DialTimeout, cfg.Connection.DialTimeoutDuration())
	assertPositiveDurationAccessor(t, "connection.start_poll_interval", cfg.Connection.StartPollInterval, cfg.Connection.StartPollIntervalDuration())
	assertPositiveDurationAccessor(t, "launch.watchdog_interval", cfg.Launch.WatchdogInterval, cfg.Launch.WatchdogIntervalDuration())
	assertPositiveDurationAccessor(t, "launch.slot_poll_interval", cfg.Launch.SlotPollInterval, cfg.Launch.SlotPollIntervalDuration())
	assertPositiveDurationAccessor(t, "lifecycle.process_kill_grace", cfg.Lifecycle.ProcessKillGrace, cfg.Lifecycle.ProcessKillGraceDuration())
	assertDurationAccessor(t, "messages.max_age", cfg.Messages.MaxAge, cfg.Messages.MaxAgeDuration())
	assertPositiveDurationAccessor(t, "messages.busy_timeout", cfg.Messages.BusyTimeout, cfg.Messages.BusyTimeoutDuration())
	assertDurationAccessor(t, "todo.claim_lease", cfg.Todo.ClaimLease, cfg.Todo.ClaimLeaseDuration())

	if strings.TrimSpace(cfg.GitPull.Interval) != "" {
		want, err := ParseDurationWithDays(cfg.GitPull.Interval)
		if err != nil || want < time.Minute {
			t.Fatalf("git_pull.interval %q was accepted but parsed as (%v, %v)", cfg.GitPull.Interval, want, err)
		}

		if got := cfg.GitPull.IntervalDuration(); got != want {
			t.Fatalf("git_pull.interval accessor = %v, want %v", got, want)
		}
	}
}

func assertPositiveDurationAccessor(t *testing.T, field, raw string, got time.Duration) {
	t.Helper()

	if strings.TrimSpace(raw) == "" {
		return
	}

	want, err := ParseDurationWithDays(raw)
	if err != nil || want <= 0 {
		t.Fatalf("%s %q was accepted but parsed as (%v, %v)", field, raw, want, err)
	}

	if got != want {
		t.Fatalf("%s accessor = %v, want parsed duration %v from %q", field, got, want, raw)
	}
}

func assertDurationAccessor(t *testing.T, field, raw string, got time.Duration) {
	t.Helper()

	if strings.TrimSpace(raw) == "" {
		return
	}

	want, err := ParseDurationWithDays(raw)
	if err != nil {
		t.Fatalf("%s %q was accepted but did not parse: %v", field, raw, err)
	}

	if got != want {
		t.Fatalf("%s accessor = %v, want parsed duration %v from %q", field, got, want, raw)
	}
}

func assertParsedDurationEquals(t *testing.T, field, raw string, got time.Duration) {
	t.Helper()

	want, err := ParseDurationWithDays(raw)
	if err != nil {
		t.Fatalf("%s %q was accepted but did not parse: %v", field, raw, err)
	}

	if got != want {
		t.Fatalf("%s accessor = %v, want parsed duration %v from %q", field, got, want, raw)
	}
}

func assertParsedPositiveDuration(t *testing.T, field, raw string) {
	t.Helper()

	got, err := ParseDurationWithDays(raw)
	if err != nil || got <= 0 {
		t.Fatalf("%s %q was accepted but parsed as (%v, %v)", field, raw, got, err)
	}
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func hasExplicitEmptyWatchBuiltinIgnores(input string) bool {
	var raw map[string]any
	if err := toml.Unmarshal([]byte(input), &raw); err != nil {
		return false
	}

	triggers, ok := raw["triggers"].(map[string]any)
	if !ok {
		return false
	}

	advanced, ok := triggers["advanced"].(map[string]any)
	if !ok {
		return false
	}

	value, ok := advanced["watch_builtin_ignores"]
	if !ok {
		return false
	}

	switch list := value.(type) {
	case []any:
		return len(list) == 0
	case []string:
		return len(list) == 0
	default:
		return false
	}
}
