package cli

import (
	"bytes"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

func TestScenarioFileParse(t *testing.T) {
	input := `
version = 1

[scenario]
name = "tracing-pipeline"
goal = "Build the tracing pipeline"

[[sessions]]
name = "backend"
repo = "~/Code/my-backend"
agent = "claude"
model = "claude-opus-4-8"
role = "Backend engineer"
task = "Add tracing ingest"

[[sessions]]
name = "frontend"
repo = "~/Code/my-frontend"
role = "Frontend dev"
task = "Add trace export"
`

	var sf scenarioFile
	dec := toml.NewDecoder(bytes.NewReader([]byte(input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sf); err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if sf.Version != 1 {
		t.Errorf("version = %d, want 1", sf.Version)
	}
	if sf.Scenario.Name != "tracing-pipeline" {
		t.Errorf("name = %q", sf.Scenario.Name)
	}
	if sf.Scenario.Goal != "Build the tracing pipeline" {
		t.Errorf("goal = %q", sf.Scenario.Goal)
	}
	if len(sf.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sf.Sessions))
	}

	s0 := sf.Sessions[0]
	if s0.Name != "backend" {
		t.Errorf("session[0].name = %q", s0.Name)
	}
	if s0.Repo != "~/Code/my-backend" {
		t.Errorf("session[0].repo = %q", s0.Repo)
	}
	if s0.Agent != "claude" {
		t.Errorf("session[0].agent = %q", s0.Agent)
	}
	if s0.Model != "claude-opus-4-8" {
		t.Errorf("session[0].model = %q", s0.Model)
	}
	if s0.Role != "Backend engineer" {
		t.Errorf("session[0].role = %q", s0.Role)
	}
	if s0.Task != "Add tracing ingest" {
		t.Errorf("session[0].task = %q", s0.Task)
	}

	s1 := sf.Sessions[1]
	if s1.Agent != "" {
		t.Errorf("session[1].agent = %q, want empty", s1.Agent)
	}
}

func TestScenarioFileAgentHooksDefault(t *testing.T) {
	input := `
version = 1

[scenario]
name = "test"

[[sessions]]
name = "a"
repo = "/tmp/repo"

[[sessions]]
name = "b"
repo = "/tmp/repo"
agent_hooks = false
`

	var sf scenarioFile
	dec := toml.NewDecoder(bytes.NewReader([]byte(input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sf); err != nil {
		t.Fatal(err)
	}

	if sf.Sessions[0].AgentHooks != nil {
		t.Error("session[0].agent_hooks should be nil (omitted)")
	}

	if sf.Sessions[1].AgentHooks == nil || *sf.Sessions[1].AgentHooks {
		t.Error("session[1].agent_hooks should be false")
	}
}

func TestScenarioFileRejectsUnknownFields(t *testing.T) {
	input := `
version = 1

[scenario]
name = "test"
unknown_field = "oops"

[[sessions]]
name = "a"
repo = "/tmp/repo"
`

	var sf scenarioFile
	dec := toml.NewDecoder(bytes.NewReader([]byte(input)))
	dec.DisallowUnknownFields()
	err := dec.Decode(&sf)
	if err == nil {
		t.Fatal("expected error for unknown TOML field")
	}
	if !strings.Contains(err.Error(), "strict mode") {
		t.Errorf("error = %q, want strict mode error", err.Error())
	}
}
