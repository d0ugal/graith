package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestConfigDerivedWireCompatibility locks the exact JSON emitted before the
// config-backed fields were replaced by protocol DTOs. In particular, trigger
// keys remain PascalCase and zero/nil values keep their historical presence.
func TestConfigDerivedWireCompatibility(t *testing.T) {
	marshal := func(name string, value any, want string) {
		t.Helper()

		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}

		if string(data) != want {
			t.Errorf("%s JSON changed\n got: %s\nwant: %s", name, data, want)
		}
	}

	marshal("create-zero", CreateMsg{}, `{"name":"","agent":"","repo_path":""}`)
	marshal("create-codex-zero", CreateMsg{Codex: &CodexOptions{}}, `{"name":"","agent":"","repo_path":"","codex":{}}`)
	marshal("create-codex-full", CreateMsg{
		Name: "braw", Agent: "codex", RepoPath: "/croft",
		Codex: &CodexOptions{
			Profile: "canny", ReasoningEffort: "high", ServiceTier: "fast", WebSearch: true,
		},
	}, `{"name":"braw","agent":"codex","repo_path":"/croft","codex":{"profile":"canny","reasoning_effort":"high","service_tier":"fast","web_search":true}}`)

	marshal("scenario-zero", ScenarioStartMsg{}, `{"caller_session_id":"","name":"","goal":"","sessions":null,"lifecycle":{}}`)
	marshal("scenario-minimal-trigger", ScenarioStartMsg{
		Triggers: []TriggerConfig{{Name: "braw"}},
	}, `{"caller_session_id":"","name":"","goal":"","sessions":null,"triggers":[{"Name":"braw","Enabled":null,"Schedule":null,"Watch":null,"GCX":null,"Completion":null,"Action":{"Type":"","Command":"","Repo":"","Timeout":"","Mutating":false,"Sandbox":null,"SandboxConfig":null,"Prompt":"","Agent":"","Model":"","Ensure":false,"AutoCleanup":null,"IdleTimeout":"","Scenario":"","Tracker":null,"Body":"","NotifyOnComplete":false,"NotifyMessage":"","NotifyPriority":"","Deliver":{"Inbox":"","Topic":"","Store":"","Wake":false,"Required":false}},"Policy":{"CatchUp":false,"Overlap":"","RateLimit":""}}],"lifecycle":{}}`)

	disabled := false
	enabled := false
	scenarioFull := ScenarioStartMsg{
		CallerSessionID: "caller-braw",
		Name:            "braw-scenario",
		Goal:            "map the wire",
		Sessions:        []ScenarioSessionInput{},
		Triggers: []TriggerConfig{{
			Name:    "canny-trigger",
			Enabled: &enabled,
			Schedule: &ScheduleConfig{
				Cron: "@daily", Every: "15m", Timezone: "Europe/London",
			},
			Watch: &WatchConfig{
				Repo: "/croft", Role: "reviewer", Paths: []string{"internal/**"},
				Ignore: []string{"vendor/**"}, Debounce: "3s",
			},
			GCX: &GCXConfig{
				Event: "oncall_alert_group", Context: "braw", Every: "1m", Timeout: "30s",
				OnCallUserID: "user-canny", ScheduleIDs: []string{"schedule-braw"},
				TeamIDs: []string{"team-croft"}, IntegrationIDs: []string{"integration-bothy"},
				States: []string{"firing"}, MaxAge: "24h", Limit: 100,
			},
			Completion: &CompletionConfig{Event: "complete", Session: "reviewer"},
			Action: ActionConfig{
				Type: "tracker", Command: "make braw", Repo: "/croft", Timeout: "5m",
				Mutating: true, Sandbox: &disabled,
				SandboxConfig: &SandboxConfig{
					Enabled: true, Disabled: &disabled, Backend: "nono", Command: "braw",
					Profile: "always-further/codex", Features: []string{"process-control"},
					ReadDirs: []string{"/croft/read"}, WriteDirs: []string{"/croft/write"},
					ReadFiles: []string{"/croft/read.txt"}, WriteFiles: []string{"/croft/write.txt"},
					SignalMode: "isolated",
					Network: &SandboxNetworkConfig{
						Block: true, AllowDomains: []string{"grafana.com"},
					},
				},
				Prompt: "inspect {title}", Agent: "codex", Model: "gpt-5", Ensure: true,
				AutoCleanup: json.RawMessage(`"on_success"`), IdleTimeout: "1m", Scenario: "bothy",
				Tracker: &TrackerConfig{
					Provider: "github", Repo: "d0ugal/graith", ActiveState: "open",
					ActiveLabels: []string{"braw"}, Assignee: "@me", Grace: "5m",
					MaxConcurrent: 3, Reap: "delete", Limit: 50,
				},
				Body: "done", NotifyOnComplete: true, NotifyMessage: "finished",
				NotifyPriority: "high",
				Deliver: DeliverConfig{
					Inbox: "reviewer", Topic: "braw", Store: "reports/canny.md", Wake: true, Required: true,
				},
			},
			Policy: TriggerPolicy{CatchUp: true, Overlap: "skip", RateLimit: "5/30m"},
		}},
		Lifecycle: ScenarioLifecycleConfig{Cleanup: "on_success", Delay: "30m"},
	}
	marshal("scenario-full", scenarioFull, `{"caller_session_id":"caller-braw","name":"braw-scenario","goal":"map the wire","sessions":[],"triggers":[{"Name":"canny-trigger","Enabled":false,"Schedule":{"Cron":"@daily","Every":"15m","Timezone":"Europe/London"},"Watch":{"Repo":"/croft","Role":"reviewer","Paths":["internal/**"],"Ignore":["vendor/**"],"Debounce":"3s"},"GCX":{"Event":"oncall_alert_group","Context":"braw","Every":"1m","Timeout":"30s","OnCallUserID":"user-canny","ScheduleIDs":["schedule-braw"],"TeamIDs":["team-croft"],"IntegrationIDs":["integration-bothy"],"States":["firing"],"MaxAge":"24h","Limit":100},"Completion":{"Event":"complete","Session":"reviewer"},"Action":{"Type":"tracker","Command":"make braw","Repo":"/croft","Timeout":"5m","Mutating":true,"Sandbox":false,"SandboxConfig":{"enabled":true,"disabled":false,"backend":"nono","command":"braw","profile":"always-further/codex","features":["process-control"],"read_dirs":["/croft/read"],"write_dirs":["/croft/write"],"read_files":["/croft/read.txt"],"write_files":["/croft/write.txt"],"signal_mode":"isolated","network":{"block":true,"allow_domains":["grafana.com"]}},"Prompt":"inspect {title}","Agent":"codex","Model":"gpt-5","Ensure":true,"AutoCleanup":"on_success","IdleTimeout":"1m","Scenario":"bothy","Tracker":{"Provider":"github","Repo":"d0ugal/graith","ActiveState":"open","ActiveLabels":["braw"],"Assignee":"@me","Grace":"5m","MaxConcurrent":3,"Reap":"delete","Limit":50},"Body":"done","NotifyOnComplete":true,"NotifyMessage":"finished","NotifyPriority":"high","Deliver":{"Inbox":"reviewer","Topic":"braw","Store":"reports/canny.md","Wake":true,"Required":true}},"Policy":{"CatchUp":true,"Overlap":"skip","RateLimit":"5/30m"}}],"lifecycle":{"cleanup":"on_success","delay":"30m"}}`)
}

func TestConfigDerivedWireDTOFieldsHaveExplicitJSONNames(t *testing.T) {
	types := []any{
		CodexOptions{}, TriggerConfig{}, ScheduleConfig{}, WatchConfig{}, GCXConfig{},
		CompletionConfig{}, ActionConfig{}, DeliverConfig{}, TrackerConfig{}, TriggerPolicy{},
		SandboxConfig{}, SandboxNetworkConfig{}, ScenarioLifecycleConfig{},
	}

	for _, value := range types {
		typeOf := reflect.TypeOf(value)
		for i := 0; i < typeOf.NumField(); i++ {
			field := typeOf.Field(i)

			tag, ok := field.Tag.Lookup("json")
			if !ok || strings.Split(tag, ",")[0] == "" {
				t.Errorf("%s.%s has no explicit JSON field name", typeOf.Name(), field.Name)
			}
		}
	}
}
