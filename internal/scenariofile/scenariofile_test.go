package scenariofile

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestParse_Valid(t *testing.T) {
	data := []byte(`
version = 1
[scenario]
name = "strath"
goal = "build the brig"
[[sessions]]
name = "ben"
repo = "~/Code/croft"
role = "implementer"
[[sessions]]
name = "bairn"
shared = true
`)

	sf, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if sf.Scenario.Name != "strath" {
		t.Errorf("name = %q", sf.Scenario.Name)
	}

	if len(sf.Sessions) != 2 {
		t.Fatalf("sessions = %d", len(sf.Sessions))
	}

	inputs, err := SessionInputs(sf)
	if err != nil {
		t.Fatalf("SessionInputs: %v", err)
	}

	if len(inputs) != 2 {
		t.Fatalf("inputs = %d", len(inputs))
	}

	if !inputs[0].AgentHooks {
		t.Error("agent_hooks should default true")
	}

	if inputs[0].Role != "implementer" {
		t.Errorf("role = %q", inputs[0].Role)
	}

	if !inputs[1].Shared {
		t.Error("bairn should be shared")
	}
}

func TestParse_IncludesAndStar(t *testing.T) {
	data := []byte(`
version = 1
[scenario]
name = "strath"
[[sessions]]
name = "ben"
repo = "~/Code/croft"
includes = ["~/Code/bothy", "~/Code/glen"]
star = true
[[sessions]]
name = "canny"
repo = "~/Code/whin"
`)

	sf, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	inputs, err := SessionInputs(sf)
	if err != nil {
		t.Fatalf("SessionInputs: %v", err)
	}

	if got := inputs[0].Includes; len(got) != 2 || got[0] != "~/Code/bothy" || got[1] != "~/Code/glen" {
		t.Errorf("includes = %v", got)
	}

	if !inputs[0].Star {
		t.Error("ben should be starred")
	}

	// Defaults: no includes, not starred.
	if len(inputs[1].Includes) != 0 {
		t.Errorf("canny includes = %v, want none", inputs[1].Includes)
	}

	if inputs[1].Star {
		t.Error("canny should not be starred by default")
	}
}

func TestParseScenarioResults(t *testing.T) {
	data := []byte(`
version = 1
[scenario]
name = "strath"
[[sessions]]
name = "canny"
repo = "~/Code/croft"
[[sessions.results]]
name = "review"
format = "markdown"
store = "{session_name}/review.md"
required = true
[[sessions.results]]
name = "facts"
format = "json"
store = "{session_name}/facts.json"
`)

	sf, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(sf.Sessions[0].Results) != 2 || !sf.Sessions[0].Results[0].Required {
		t.Fatalf("parsed results = %+v", sf.Sessions[0].Results)
	}

	inputs, err := SessionInputs(sf)
	if err != nil {
		t.Fatalf("SessionInputs: %v", err)
	}

	if len(inputs[0].Results) != 2 || inputs[0].Results[1].Format != "json" {
		t.Fatalf("wire results = %+v", inputs[0].Results)
	}
}

func TestParseScenarioStartupPromptContracts(t *testing.T) {
	longPrompt := "Inspect the croft and publish the declared report.\n\n" + strings.Repeat("braw detail ", 60)
	data := []byte(fmt.Sprintf(`
version = 1
[scenario]
name = "strath-prompts"
[scenario.policy]
completion = "all"
[[sessions]]
name = "reporter"
repo = "~/Code/croft"
prompt = %q
[[sessions.results]]
name = "report"
format = "markdown"
store = "{session_name}/report.md"
required = true
[[sessions]]
name = "builder"
repo = "~/Code/bothy"
prompt = "Follow the detailed build instructions."
task = "build the brig"
[[sessions]]
name = "legacy"
repo = "~/Code/glen"
task = "inspect the brig"
`, longPrompt))

	sf, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	inputs, err := SessionInputs(sf)
	if err != nil {
		t.Fatalf("SessionInputs: %v", err)
	}

	if inputs[0].Prompt != longPrompt || inputs[0].Task != "" || inputs[0].StartupPrompt() != longPrompt {
		t.Fatalf("prompt-only input = %+v", inputs[0])
	}

	if inputs[1].StartupPrompt() != "Follow the detailed build instructions." || inputs[1].Task != "build the brig" {
		t.Fatalf("prompt+task input = %+v", inputs[1])
	}

	if inputs[2].Prompt != "" || inputs[2].StartupPrompt() != inputs[2].Task {
		t.Fatalf("task-only compatibility input = %+v", inputs[2])
	}
}

func TestParseScenarioStartupPromptLimitsAndContradictions(t *testing.T) {
	base := `
version = 1
[scenario]
name = "strath"
[[sessions]]
name = "canny"
repo = "/croft"
%s
`

	cases := []struct {
		name, fields, want string
	}{
		{
			name:   "oversized prompt",
			fields: fmt.Sprintf("prompt = %q", strings.Repeat("d", protocol.MaxScenarioPromptBytes+1)),
			want:   "prompt is too large",
		},
		{
			name:   "oversized task",
			fields: fmt.Sprintf("task = %q", strings.Repeat("t", config.TodoMaxTitleCeiling+1)),
			want:   "task exceeds todo title limit",
		},
		{
			name:   "shared prompt",
			fields: "shared = true\nprompt = \"instructions that cannot be launched\"",
			want:   "prompt is not valid for a shared session",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(fmt.Sprintf(base, tc.fields)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseScenarioPolicyRejectsPromptWithoutContract(t *testing.T) {
	_, err := Parse([]byte(`
version = 1
[scenario]
name = "strath"
[scenario.policy]
completion = "all"
[[sessions]]
name = "canny"
repo = "/croft"
prompt = "Inspect the croft"
`))
	if err == nil || !strings.Contains(err.Error(), "non-empty task or required result contract") {
		t.Fatalf("prompt-only policy contract error = %v", err)
	}
}

func TestValidateSessionContractsTreatsWhitespacePromptAsOmitted(t *testing.T) {
	sessions := []protocol.ScenarioSessionInput{{
		Name: "canny", Shared: true, Prompt: " \n\t", Task: "inspect the croft",
	}}

	if err := ValidateSessionContracts(sessions, config.TodoMaxTitleCeiling); err != nil {
		t.Fatalf("whitespace-only prompt: %v", err)
	}

	if got := sessions[0].StartupPrompt(); got != "" {
		t.Fatalf("shared startup prompt = %q, want empty", got)
	}

	NormalizeSessionContracts(sessions)

	if sessions[0].Prompt != "" || sessions[0].Task != "inspect the croft" {
		t.Fatalf("normalized prompt/task = %q/%q", sessions[0].Prompt, sessions[0].Task)
	}
}

func TestValidateSessionContractsRejectsUnsafeRawFields(t *testing.T) {
	tests := []struct {
		name    string
		session protocol.ScenarioSessionInput
		want    string
	}{
		{
			name:    "prompt NUL",
			session: protocol.ScenarioSessionInput{Name: "canny", Prompt: "inspect\x00croft"},
			want:    "prompt contains a NUL byte",
		},
		{
			name:    "task NUL",
			session: protocol.ScenarioSessionInput{Name: "dreich", Task: "inspect\x00croft"},
			want:    "task contains a NUL byte",
		},
		{
			name:    "whitespace task exceeds raw title limit",
			session: protocol.ScenarioSessionInput{Name: "thrawn", Task: strings.Repeat(" ", config.TodoMaxTitleCeiling+1)},
			want:    "task exceeds todo title limit",
		},
		{
			name:    "whitespace prompt exceeds raw body limit",
			session: protocol.ScenarioSessionInput{Name: "bairn", Prompt: strings.Repeat(" ", protocol.MaxScenarioPromptBytes+1)},
			want:    "prompt is too large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSessionContracts([]protocol.ScenarioSessionInput{tt.session}, config.TodoMaxTitleCeiling)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateSessionContractsRejectsAggregatePromptPayload(t *testing.T) {
	memberCount := protocol.MaxScenarioContractPayloadBytes/protocol.MaxScenarioPromptBytes + 1

	sessions := make([]protocol.ScenarioSessionInput, memberCount)
	for i := range sessions {
		sessions[i] = protocol.ScenarioSessionInput{
			Name:   fmt.Sprintf("canny-%d", i),
			Prompt: strings.Repeat("p", protocol.MaxScenarioPromptBytes),
		}
	}

	err := ValidateSessionContracts(sessions, config.TodoMaxTitleCeiling)
	if err == nil || !strings.Contains(err.Error(), "scenario prompt/task payload is too large") {
		t.Fatalf("error = %v, want aggregate prompt/task payload rejection", err)
	}
}

func TestParseScenarioResultRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte(`
version = 1
[scenario]
name = "strath"
[[sessions]]
name = "canny"
repo = "~/Code/croft"
[[sessions.results]]
name = "review"
format = "markdown"
store = "review.md"
destination = "dreich.md"
`))
	if err == nil || !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("unknown result field error = %v", err)
	}
}

func TestParse_MirroredMember(t *testing.T) {
	data := []byte(`
version = 1
[scenario]
name = "strath-readers"
[[sessions]]
name = "subject"
shared = true
[[sessions]]
name = "reader"
mirror = "subject"
agent = "codex"
role = "auditor"
`)

	sf, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	inputs, err := SessionInputs(sf)
	if err != nil {
		t.Fatalf("SessionInputs: %v", err)
	}

	if got := inputs[1].Mirror; got != "subject" {
		t.Errorf("mirror = %q, want subject", got)
	}

	if inputs[1].Repo != "" {
		t.Errorf("mirrored member repo = %q, want derived/empty input", inputs[1].Repo)
	}
}

func TestParseDefersTemplatedMemberGraphToDaemonRenderContext(t *testing.T) {
	data := []byte(`
version = 1
[scenario]
name = "parallel-{date}-{short_id}"
[[sessions]]
name = "{initiator}"
shared = true
[[sessions]]
name = "{scenario}-reviewer"
mirror = "{caller}"
task = "review"
depends_on = ["{initiator}"]

[[trigger]]
name = "complete-review"
[trigger.completion]
event = "complete"
session = "{scenario}-reviewer"
[trigger.action]
type = "message"
body = "done"
[trigger.action.deliver]
inbox = "{scenario}-reviewer"
`)

	sf, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse templated scenario: %v", err)
	}

	inputs, err := SessionInputs(sf)
	if err != nil {
		t.Fatalf("SessionInputs templated scenario: %v", err)
	}

	if !HasTemplatedMemberGraph(inputs) || inputs[1].Mirror != "{caller}" || inputs[1].DependsOn[0] != "{initiator}" {
		t.Fatalf("templated inputs = %+v", inputs)
	}
}

func TestValidateMirrorMembers_DepthsAndChains(t *testing.T) {
	depths, err := ValidateMirrorMembers([]MirrorMember{
		{Name: "subject", Repo: "/croft"},
		{Name: "reader-a", Mirror: "subject"},
		{Name: "reader-b", Mirror: "subject"},
		{Name: "reader-c", Mirror: "reader-a"},
	})
	if err != nil {
		t.Fatalf("ValidateMirrorMembers: %v", err)
	}

	want := []int{0, 1, 1, 2}
	for i := range want {
		if depths[i] != want[i] {
			t.Errorf("depths[%d] = %d, want %d (all depths: %v)", i, depths[i], want[i], depths)
		}
	}
}

func TestValidateMirrorMembers_RejectsInvalidTopology(t *testing.T) {
	tests := []struct {
		name    string
		members []MirrorMember
		want    string
	}{
		{"missing target", []MirrorMember{{Name: "reader", Mirror: "outsider"}}, "not a member"},
		{"self cycle", []MirrorMember{{Name: "reader", Mirror: "reader"}}, "cyclic"},
		{"multi cycle", []MirrorMember{{Name: "reader-a", Mirror: "reader-b"}, {Name: "reader-b", Mirror: "reader-a"}}, "cyclic"},
		{"ambiguous name", []MirrorMember{{Name: "subject"}, {Name: "subject"}, {Name: "reader", Mirror: "subject"}}, "ambiguous"},
		{"shared mirror", []MirrorMember{{Name: "subject"}, {Name: "reader", Mirror: "subject", Shared: true}}, "mutually exclusive"},
		{"repo conflict", []MirrorMember{{Name: "subject"}, {Name: "reader", Mirror: "subject", Repo: "/croft"}}, "mirror and repo"},
		{"base conflict", []MirrorMember{{Name: "subject"}, {Name: "reader", Mirror: "subject", Base: "main"}}, "mirror and base"},
		{"includes conflict", []MirrorMember{{Name: "subject"}, {Name: "reader", Mirror: "subject", Includes: 1}}, "mirror and includes"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateMirrorMembers(test.members)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestParse_Invalid(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"bad version", "version = 2\n[scenario]\nname=\"x\"\n[[sessions]]\nname=\"a\"\nrepo=\"/r\"\n", "unsupported scenario version"},
		{"no name", "version = 1\n[scenario]\n[[sessions]]\nname=\"a\"\nrepo=\"/r\"\n", "scenario.name is required"},
		{"no sessions", "version = 1\n[scenario]\nname=\"x\"\n", "at least one"},
		{"unknown field", "version = 1\nbogus = true\n[scenario]\nname=\"x\"\n[[sessions]]\nname=\"a\"\nrepo=\"/r\"\n", "parse scenario TOML"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.data))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestSessionInputs_MissingRepo(t *testing.T) {
	sf := &File{Sessions: []Session{{Name: "ben"}}}
	if _, err := SessionInputs(sf); err == nil || !strings.Contains(err.Error(), "repo is required") {
		t.Fatalf("expected repo-required error, got %v", err)
	}
}

func TestParse_EmbeddedTrigger(t *testing.T) {
	data := []byte(`
version = 1
[scenario]
name = "strath"
goal = "review the brig"
[[sessions]]
name = "ben"
repo = "~/Code/croft"
role = "implementer"
[[trigger]]
name = "review-go"
[trigger.watch]
role  = "implementer"
paths = ["**/*.go"]
[trigger.action]
type   = "session"
ensure = true
prompt = "Review the changes since your last look."
`)

	sf, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(sf.Triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(sf.Triggers))
	}

	trig := sf.Triggers[0]
	if trig.Name != "review-go" {
		t.Errorf("trigger name = %q", trig.Name)
	}

	if !trig.IsWatch() || trig.Watch.Role != "implementer" {
		t.Errorf("watch role = %+v", trig.Watch)
	}

	if !trig.Action.Ensure {
		t.Error("ensure should be true")
	}
}

func TestParse_CompletionTriggerAndLifecycle(t *testing.T) {
	data := []byte(`
version = 1
[scenario]
name = "strath"
goal = "finish"
[scenario.lifecycle]
cleanup = "on_success"
delay = "30m"
[[sessions]]
name = "ben"
repo = "/r"
role = "reporter"
[[trigger]]
name = "archive"
[trigger.completion]
event = "complete"
session = "ben"
[trigger.action]
type = "command"
command = "./archive"
[trigger.action.deliver]
store = "shared:reports/{completion_epoch}.md"
required = true
`)

	sf, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	if sf.Scenario.Lifecycle.CleanupMode() != config.ScenarioCleanupOnSuccess ||
		sf.Scenario.Lifecycle.DelayDuration() != 30*time.Minute {
		t.Fatalf("lifecycle = %+v", sf.Scenario.Lifecycle)
	}

	if len(sf.Triggers) != 1 || !sf.Triggers[0].IsCompletion() || sf.Triggers[0].Completion.Session != "ben" {
		t.Fatalf("completion trigger = %+v", sf.Triggers)
	}
}

func TestParse_CompletionTriggerScopeRejected(t *testing.T) {
	base := `
version = 1
[scenario]
name = "strath"
[[sessions]]
name = "ben"
repo = "/r"
[[sessions]]
name = "shared-ben"
repo = "/r"
shared = true
`

	for _, tc := range []struct {
		name, session, action, want string
	}{
		{"missing command context", "", "command", "requires completion.session"},
		{"shared context", "shared-ben", "command", "not a non-shared session"},
		{"unknown context", "canny", "session", "not a non-shared session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := "command = \"true\"\n"
			if tc.action == "session" {
				extra = "prompt = \"report\"\n"
			}

			data := base + "[[trigger]]\nname=\"finish\"\n[trigger.completion]\nsession=\"" + tc.session + "\"\n" +
				"[trigger.action]\ntype=\"" + tc.action + "\"\n" + extra

			_, err := Parse([]byte(data))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParse_EmbeddedTriggerInvalid(t *testing.T) {
	base := `
version = 1
[scenario]
name = "strath"
[[sessions]]
name = "ben"
repo = "/r"
role = "implementer"
`
	cases := []struct {
		name string
		trig string
		want string
	}{
		{
			name: "repo selector rejected",
			trig: "[[trigger]]\nname=\"t\"\n[trigger.watch]\nrepo=\"/other\"\n[trigger.action]\ntype=\"message\"\nbody=\"x\"\n[trigger.action.deliver]\ntopic=\"blether\"\n",
			want: "must select sessions by role",
		},
		{
			name: "undefined role rejected",
			trig: "[[trigger]]\nname=\"t\"\n[trigger.watch]\nrole=\"reviewer\"\n[trigger.action]\ntype=\"message\"\nbody=\"x\"\n[trigger.action.deliver]\ntopic=\"blether\"\n",
			want: "not defined by any scenario session",
		},
		{
			name: "scenario action rejected",
			trig: "[[trigger]]\nname=\"t\"\n[trigger.watch]\nrole=\"implementer\"\n[trigger.action]\ntype=\"scenario\"\nscenario=\"other\"\n",
			want: "cannot start scenarios",
		},
		{
			name: "schedule command rejected",
			trig: "[[trigger]]\nname=\"t\"\n[trigger.schedule]\nevery=\"1h\"\n[trigger.action]\ntype=\"command\"\ncommand=\"go test ./...\"\nrepo=\"/r\"\n",
			want: "require a [watch] or [completion] source",
		},
		{
			name: "session action external repo rejected",
			trig: "[[trigger]]\nname=\"t\"\n[trigger.watch]\nrole=\"implementer\"\n[trigger.action]\ntype=\"session\"\nprompt=\"go\"\nrepo=\"/other\"\n",
			want: "must not set action.repo",
		},
		{
			name: "inbox to non-member rejected",
			trig: "[[trigger]]\nname=\"t\"\n[trigger.watch]\nrole=\"implementer\"\n[trigger.action]\ntype=\"message\"\nbody=\"x\"\n[trigger.action.deliver]\ninbox=\"outsider\"\n",
			want: "not a session in this scenario",
		},
		{
			name: "duplicate trigger name",
			trig: "[[trigger]]\nname=\"t\"\n[trigger.watch]\nrole=\"implementer\"\n[trigger.action]\ntype=\"message\"\nbody=\"x\"\n[trigger.action.deliver]\ntopic=\"blether\"\n" +
				"[[trigger]]\nname=\"t\"\n[trigger.watch]\nrole=\"implementer\"\n[trigger.action]\ntype=\"message\"\nbody=\"y\"\n[trigger.action.deliver]\ntopic=\"blether\"\n",
			want: "duplicate trigger name",
		},
		{
			name: "missing name",
			trig: "[[trigger]]\n[trigger.watch]\nrole=\"implementer\"\n[trigger.action]\ntype=\"message\"\nbody=\"x\"\n[trigger.action.deliver]\ntopic=\"blether\"\n",
			want: "name is required",
		},
		{
			name: "no source rejected",
			trig: "[[trigger]]\nname=\"t\"\n[trigger.action]\ntype=\"message\"\nbody=\"x\"\n[trigger.action.deliver]\ntopic=\"blether\"\n",
			want: "exactly one of [schedule], [watch], [gcx], or [completion]",
		},
		{
			name: "gcx source rejected",
			trig: "[[trigger]]\nname=\"t\"\n[trigger.gcx]\ncontext=\"croft\"\n[trigger.action]\ntype=\"message\"\nbody=\"x\"\n[trigger.action.deliver]\ntopic=\"blether\"\n",
			want: "cannot use a [gcx] source",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(base + tc.trig))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestDefinedRoles(t *testing.T) {
	sf := &File{Sessions: []Session{
		{Name: "ben", Role: "implementer"},
		{Name: "canny", Role: "reviewer"},
		{Name: "bairn"},                               // no role
		{Name: "auld", Role: "watcher", Shared: true}, // shared → role not selectable
	}}

	roles := sf.DefinedRoles()
	if len(roles) != 2 || !roles["implementer"] || !roles["reviewer"] {
		t.Fatalf("roles = %v (shared role should be excluded)", roles)
	}

	members := sf.DefinedMembers()
	if len(members) != 4 || !members["auld"] {
		t.Fatalf("members = %v (shared session should still be a member)", members)
	}
}

func TestValidateScenarioTriggers_AllowedInboxTargets(t *testing.T) {
	roles := map[string]bool{"implementer": true}
	members := map[string]bool{"ben": true, "canny": true}

	deliver := func(inbox string) config.TriggerConfig {
		return config.TriggerConfig{
			Name:   "t",
			Watch:  &config.WatchConfig{Role: "implementer"},
			Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Inbox: inbox}},
		}
	}

	for _, inbox := range []string{"ben", "canny", "orchestrator", "{session_name}"} {
		if err := ValidateScenarioTriggers([]config.TriggerConfig{deliver(inbox)}, roles, members); err != nil {
			t.Errorf("inbox %q should be allowed, got %v", inbox, err)
		}
	}

	if err := ValidateScenarioTriggers([]config.TriggerConfig{deliver("stranger")}, roles, members); err == nil {
		t.Error("inbox to a non-member should be rejected")
	}
}

func TestParseSessionDependencies(t *testing.T) {
	data := []byte(`
version = 1
[scenario]
name = "strath"
[[sessions]]
name = "braw"
repo = "/croft"
task = "build the brig"
[[sessions]]
name = "canny"
repo = "/bothy"
task = "inspect the brig"
depends_on = ["braw"]
`)

	sf, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	inputs, err := SessionInputs(sf)
	if err != nil {
		t.Fatalf("SessionInputs: %v", err)
	}

	if len(inputs[1].DependsOn) != 1 || inputs[1].DependsOn[0] != "braw" {
		t.Fatalf("depends_on mapping = %v", inputs[1].DependsOn)
	}
}

func TestParseRejectsInvalidSessionDependencies(t *testing.T) {
	base := `
version = 1
[scenario]
name = "strath"
[[sessions]]
name = "braw"
repo = "/croft"
task = "braw work"
%s
[[sessions]]
name = "canny"
repo = "/bothy"
task = "canny work"
%s
`

	cases := []struct {
		name, first, second, want string
	}{
		{"unknown", "", `depends_on = ["haar"]`, "not defined"},
		{"self", `depends_on = ["braw"]`, "", "cannot reference itself"},
		{"duplicate", "", `depends_on = ["braw", "braw"]`, "duplicate"},
		{"cycle", `depends_on = ["canny"]`, `depends_on = ["braw"]`, "cycle"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(fmt.Sprintf(base, tc.first, tc.second)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateSessionDependenciesRequiresTrackedTasks(t *testing.T) {
	err := ValidateSessionDependencies([]protocol.ScenarioSessionInput{
		{Name: "braw", Task: ""},
		{Name: "canny", Task: "inspect", DependsOn: []string{"braw"}},
	})
	if err == nil || !strings.Contains(err.Error(), "has no task") {
		t.Fatalf("missing dependency task: %v", err)
	}

	err = ValidateSessionDependencies([]protocol.ScenarioSessionInput{
		{Name: "braw", Task: "build"},
		{Name: "canny", DependsOn: []string{"braw"}},
	})
	if err == nil || !strings.Contains(err.Error(), "requires a task") {
		t.Fatalf("missing dependent task: %v", err)
	}
}
