package scenariofile

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestTriggerProtocolConversionRoundTripsDomain(t *testing.T) {
	want := []config.TriggerConfig{fullTriggerConfig(t)}

	wire, err := TriggersToProtocol(want)
	if err != nil {
		t.Fatalf("TriggersToProtocol: %v", err)
	}

	got, err := TriggersFromProtocol(wire)
	if err != nil {
		t.Fatalf("TriggersFromProtocol: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("domain round trip changed trigger\n got: %#v\nwant: %#v", got, want)
	}

	// Conversion must not leave mutable pointers or slices aliased across the
	// config/wire boundary.
	*wire[0].Enabled = true
	wire[0].Watch.Paths[0] = "changed/**"

	wire[0].Action.SandboxConfig.Network.AllowDomains[0] = "changed.example"
	if *want[0].Enabled || want[0].Watch.Paths[0] != "internal/**" ||
		want[0].Action.SandboxConfig.Network.AllowDomains[0] != "grafana.com" {
		t.Fatal("config input was aliased by protocol conversion")
	}
}

func TestTriggerProtocolConversionRoundTripsWire(t *testing.T) {
	domain := []config.TriggerConfig{fullTriggerConfig(t)}

	want, err := TriggersToProtocol(domain)
	if err != nil {
		t.Fatal(err)
	}

	converted, err := TriggersFromProtocol(want)
	if err != nil {
		t.Fatalf("TriggersFromProtocol: %v", err)
	}

	got, err := TriggersToProtocol(converted)
	if err != nil {
		t.Fatalf("TriggersToProtocol: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire round trip changed trigger\n got: %#v\nwant: %#v", got, want)
	}
}

func TestTriggerProtocolConversionPreservesMutuallyExclusiveSources(t *testing.T) {
	tests := []struct {
		name    string
		trigger config.TriggerConfig
		check   func(config.TriggerConfig) bool
	}{
		{
			name: "schedule",
			trigger: config.TriggerConfig{
				Name: "braw", Schedule: &config.ScheduleConfig{Every: "15m"},
				Action: config.ActionConfig{Type: config.ActionMessage, Body: "done"},
			},
			check: func(trigger config.TriggerConfig) bool { return trigger.IsSchedule() },
		},
		{
			name: "watch",
			trigger: config.TriggerConfig{
				Name: "canny", Watch: &config.WatchConfig{Role: "reviewer"},
				Action: config.ActionConfig{Type: config.ActionSession, Prompt: "review", AutoCleanup: true},
			},
			check: func(trigger config.TriggerConfig) bool { return trigger.IsWatch() },
		},
		{
			name: "gcx",
			trigger: config.TriggerConfig{
				Name: "croft", GCX: &config.GCXConfig{Context: "braw"},
				Action: config.ActionConfig{Type: config.ActionMessage, Body: "page"},
			},
			check: func(trigger config.TriggerConfig) bool { return trigger.IsGCX() },
		},
		{
			name: "completion",
			trigger: config.TriggerConfig{
				Name: "bothy", Completion: &config.CompletionConfig{Session: "writer"},
				Action: config.ActionConfig{Type: config.ActionCommand, Command: "make braw"},
			},
			check: func(trigger config.TriggerConfig) bool { return trigger.IsCompletion() },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire, err := TriggersToProtocol([]config.TriggerConfig{test.trigger})
			if err != nil {
				t.Fatal(err)
			}

			got, err := TriggersFromProtocol(wire)
			if err != nil {
				t.Fatal(err)
			}

			if len(got) != 1 || !test.check(got[0]) || !reflect.DeepEqual(got[0], test.trigger) {
				t.Fatalf("source/action shape changed\n got: %#v\nwant: %#v", got, test.trigger)
			}
		})
	}
}

func TestTriggerProtocolConversionPreservesNilEmptyAndAutoCleanupUnion(t *testing.T) {
	for _, test := range []struct {
		name     string
		triggers []config.TriggerConfig
	}{
		{name: "nil", triggers: nil},
		{name: "empty", triggers: []config.TriggerConfig{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire, err := TriggersToProtocol(test.triggers)
			if err != nil {
				t.Fatal(err)
			}

			if (wire == nil) != (test.triggers == nil) {
				t.Fatalf("wire nil = %v, want %v", wire == nil, test.triggers == nil)
			}

			domain, err := TriggersFromProtocol(wire)
			if err != nil {
				t.Fatal(err)
			}

			if (domain == nil) != (test.triggers == nil) {
				t.Fatalf("domain nil = %v, want %v", domain == nil, test.triggers == nil)
			}
		})
	}

	for _, value := range []any{nil, false, true, "always", "on_success"} {
		t.Run(autoCleanupName(value), func(t *testing.T) {
			want := config.TriggerConfig{Action: config.ActionConfig{AutoCleanup: value}}

			wire, err := TriggersToProtocol([]config.TriggerConfig{want})
			if err != nil {
				t.Fatal(err)
			}

			got, err := TriggersFromProtocol(wire)
			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(got[0].Action.AutoCleanup, value) {
				t.Fatalf("auto_cleanup = %#v, want %#v", got[0].Action.AutoCleanup, value)
			}
		})
	}

	emptySlices := config.TriggerConfig{
		Watch: &config.WatchConfig{Paths: []string{}, Ignore: []string{}},
		GCX: &config.GCXConfig{
			ScheduleIDs: []string{}, TeamIDs: []string{}, IntegrationIDs: []string{}, States: []string{},
		},
		Action: config.ActionConfig{
			Tracker: &config.TrackerConfig{ActiveLabels: []string{}},
			SandboxConfig: &config.SandboxConfig{
				Features: []string{}, ReadDirs: []string{}, WriteDirs: []string{},
				ReadFiles: []string{}, WriteFiles: []string{},
				Network: &config.SandboxNetworkConfig{AllowDomains: []string{}},
			},
		},
	}

	wire, err := TriggersToProtocol([]config.TriggerConfig{emptySlices})
	if err != nil {
		t.Fatal(err)
	}

	got, err := TriggersFromProtocol(wire)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got[0], emptySlices) {
		t.Fatalf("explicit empty nested slices changed\n got: %#v\nwant: %#v", got[0], emptySlices)
	}
}

func TestTriggerProtocolConversionRejectsMalformedAutoCleanup(t *testing.T) {
	_, err := TriggersToProtocol([]config.TriggerConfig{{
		Name: "braw", Action: config.ActionConfig{AutoCleanup: make(chan int)},
	}})
	if err == nil || !strings.Contains(err.Error(), "marshal action.auto_cleanup") {
		t.Fatalf("unmarshalable domain auto_cleanup error = %v", err)
	}

	_, err = TriggersFromProtocol([]protocol.TriggerConfig{{
		Name: "braw", Action: protocol.ActionConfig{AutoCleanup: json.RawMessage(`{`)},
	}})
	if err == nil || !strings.Contains(err.Error(), "decode action.auto_cleanup") {
		t.Fatalf("malformed wire auto_cleanup error = %v", err)
	}
}

func TestLifecycleProtocolConversionRoundTrips(t *testing.T) {
	wantDomain := config.ScenarioLifecycleConfig{Cleanup: config.ScenarioCleanupOnSuccess, Delay: "30m"}

	wire := LifecycleToProtocol(wantDomain)
	if got := LifecycleFromProtocol(wire); got != wantDomain {
		t.Fatalf("domain lifecycle round trip = %+v, want %+v", got, wantDomain)
	}

	wantWire := protocol.ScenarioLifecycleConfig{Cleanup: config.ScenarioCleanupAlways, Delay: "5m"}
	if got := LifecycleToProtocol(LifecycleFromProtocol(wantWire)); got != wantWire {
		t.Fatalf("wire lifecycle round trip = %+v, want %+v", got, wantWire)
	}
}

func fullTriggerConfig(t *testing.T) config.TriggerConfig {
	t.Helper()

	enabled := false
	disabled := false

	return config.TriggerConfig{
		Name:    "canny-trigger",
		Enabled: &enabled,
		Schedule: &config.ScheduleConfig{
			Cron: "@daily", Every: "15m", Timezone: "Europe/London",
		},
		Watch: &config.WatchConfig{
			Repo: "/croft", Role: "reviewer", Paths: []string{"internal/**"},
			Ignore: []string{"vendor/**"}, Debounce: "3s",
		},
		GCX: &config.GCXConfig{
			Event: "oncall_alert_group", Context: "braw", Every: "1m", Timeout: "30s",
			OnCallUserID: "user-canny", ScheduleIDs: []string{"schedule-braw"},
			TeamIDs: []string{"team-croft"}, IntegrationIDs: []string{"integration-bothy"},
			States: []string{"firing"}, MaxAge: "24h", Limit: 100,
		},
		Completion: &config.CompletionConfig{Event: "complete", Session: "reviewer"},
		Action: config.ActionConfig{
			Type: "tracker", Command: "make braw", Repo: "/croft", Timeout: "5m",
			Mutating: true, Sandbox: &disabled,
			SandboxConfig: &config.SandboxConfig{
				Enabled: true, Disabled: &disabled, Backend: "nono", Command: "braw",
				Profile: "always-further/codex", Features: []string{"process-control"},
				ReadDirs: []string{"/croft/read"}, WriteDirs: []string{"/croft/write"},
				ReadFiles: []string{"/croft/read.txt"}, WriteFiles: []string{"/croft/write.txt"},
				SignalMode: "isolated",
				Network:    &config.SandboxNetworkConfig{Block: true, AllowDomains: []string{"grafana.com"}},
			},
			Prompt: "inspect {title}", Agent: "codex", Model: "gpt-5", Ensure: true,
			AutoCleanup: "on_success", IdleTimeout: "1m", Scenario: "bothy",
			Tracker: &config.TrackerConfig{
				Provider: "github", Repo: "d0ugal/graith", ActiveState: "open",
				ActiveLabels: []string{"braw"}, Assignee: "@me", Grace: "5m",
				MaxConcurrent: 3, Reap: "delete", Limit: 50,
			},
			Body: "done", NotifyOnComplete: true, NotifyMessage: "finished", NotifyPriority: "high",
			Deliver: config.DeliverConfig{
				Inbox: "reviewer", Topic: "braw", Store: "reports/canny.md", Wake: true, Required: true,
			},
		},
		Policy: config.TriggerPolicy{CatchUp: true, Overlap: "skip", RateLimit: "5/30m"},
	}
}

func autoCleanupName(value any) string {
	if value == nil {
		return "nil"
	}

	return fmt.Sprintf("%T-%v", value, value)
}
