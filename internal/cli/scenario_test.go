package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

func TestScenarioFileParse(t *testing.T) {
	input := `
version = 1

[scenario]
name = "strath-pipeline"
goal = "Build the strath pipeline"

[[sessions]]
name = "bairn"
repo = "~/Code/croft-backend"
agent = "claude"
model = "claude-opus-4-8"
role = "Backend engineer"
task = "Add tracing ingest"

[[sessions]]
name = "canny"
repo = "~/Code/croft-frontend"
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

	if sf.Scenario.Name != "strath-pipeline" {
		t.Errorf("name = %q", sf.Scenario.Name)
	}

	if sf.Scenario.Goal != "Build the strath pipeline" {
		t.Errorf("goal = %q", sf.Scenario.Goal)
	}

	if len(sf.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sf.Sessions))
	}

	s0 := sf.Sessions[0]
	if s0.Name != "bairn" {
		t.Errorf("session[0].name = %q", s0.Name)
	}

	if s0.Repo != "~/Code/croft-backend" {
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
name = "kirk"

[[sessions]]
name = "braw"
repo = "/tmp/croft"

[[sessions]]
name = "bonnie"
repo = "/tmp/croft"
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
name = "kirk"
unknown_field = "oops"

[[sessions]]
name = "braw"
repo = "/tmp/croft"
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

func TestParseScenarioFile(t *testing.T) {
	data := []byte(`
version = 1

[scenario]
name = "kirk"
goal = "do things"

[[sessions]]
name = "braw"
repo = "/tmp/croft"
`)

	sf, err := parseScenarioFile(data)
	if err != nil {
		t.Fatal(err)
	}

	if sf.Scenario.Name != "kirk" {
		t.Errorf("name = %q", sf.Scenario.Name)
	}

	if sf.Scenario.Goal != "do things" {
		t.Errorf("goal = %q", sf.Scenario.Goal)
	}
}

func TestParseScenarioFileErrors(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{"bad version", `version = 2
[scenario]
name = "kirk"
[[sessions]]
name = "braw"
repo = "/tmp"`, "unsupported scenario version"},
		{"no name", `version = 1
[scenario]
goal = "kirk"
[[sessions]]
name = "braw"
repo = "/tmp"`, "scenario.name is required"},
		{"no sessions", `version = 1
[scenario]
name = "kirk"`, "at least one [[sessions]] entry"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseScenarioFile([]byte(tt.data))
			if err == nil {
				t.Fatal("expected error")
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestListAvailableScenarios(t *testing.T) {
	scenarioDir := filepath.Join(t.TempDir(), "scenarios")
	if err := os.MkdirAll(scenarioDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a valid scenario file.
	if err := os.WriteFile(filepath.Join(scenarioDir, "kirk.toml"), []byte(`
version = 1

[scenario]
name = "kirk"
goal = "Kirk goal"

[[sessions]]
name = "braw"
repo = "/tmp/croft"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write an invalid file — should be skipped.
	if err := os.WriteFile(filepath.Join(scenarioDir, "bad.toml"), []byte("invalid"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a non-TOML file — should be skipped.
	if err := os.WriteFile(filepath.Join(scenarioDir, "readme.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	available := listAvailableScenariosIn(scenarioDir)
	if len(available) != 1 {
		t.Fatalf("expected 1 available scenario, got %d", len(available))
	}

	if available[0].Name != "kirk" {
		t.Errorf("name = %q, want 'kirk'", available[0].Name)
	}

	if available[0].Goal != "Kirk goal" {
		t.Errorf("goal = %q", available[0].Goal)
	}
}

func TestResolveScenarioSource(t *testing.T) {
	scenarioDir := filepath.Join(t.TempDir(), "scenarios")
	if err := os.MkdirAll(scenarioDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := []byte("test content")
	if err := os.WriteFile(filepath.Join(scenarioDir, "strath.toml"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Resolve by name (without .toml).
	data, err := resolveScenarioSourceFrom("strath", scenarioDir)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "test content" {
		t.Errorf("data = %q", data)
	}

	// Resolve by name with .toml.
	data, err = resolveScenarioSourceFrom("strath.toml", scenarioDir)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "test content" {
		t.Errorf("data = %q", data)
	}

	// Not found.
	_, err = resolveScenarioSourceFrom("nonexistent", scenarioDir)
	if err == nil {
		t.Fatal("expected error for nonexistent scenario")
	}

	// Direct file path.
	directFile := filepath.Join(scenarioDir, "..", "direct.toml")
	if err := os.WriteFile(directFile, []byte("direct"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err = resolveScenarioSourceFrom(directFile, scenarioDir)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "direct" {
		t.Errorf("data = %q", data)
	}
}
