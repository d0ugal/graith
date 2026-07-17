package scenariofile

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
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
			want: "require a [watch] source",
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
			want: "exactly one of [schedule], [watch], or [gcx]",
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
