package scenariofile

import (
	"strings"
	"testing"
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
