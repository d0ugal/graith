package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	toml "github.com/pelletier/go-toml/v2"
)

func TestDecodeLifecycleResult(t *testing.T) {
	// The daemon's success payloads mix a string "name" field with the
	// []string result. Decoding must extract the result cleanly and must not
	// choke on the string field (issue #785).
	tests := []struct {
		name      string
		payload   string
		resultKey string
		want      []string
		wantNil   bool
		wantErr   bool
	}{
		{
			name:      "stopped with name field",
			payload:   `{"name":"strath","stopped":["a","b","c"]}`,
			resultKey: "stopped",
			want:      []string{"a", "b", "c"},
		},
		{
			name:      "deleted with name field",
			payload:   `{"name":"strath","deleted":["braw"]}`,
			resultKey: "deleted",
			want:      []string{"braw"},
		},
		{
			name:      "resumed empty list",
			payload:   `{"name":"strath","resumed":[]}`,
			resultKey: "resumed",
			want:      []string{},
			wantNil:   false,
		},
		{
			name:      "present null result value is a no-op nil",
			payload:   `{"name":"strath","stopped":null}`,
			resultKey: "stopped",
			want:      nil,
			wantNil:   true,
		},
		{
			name:      "missing result key errors (protocol drift)",
			payload:   `{"name":"strath"}`,
			resultKey: "stopped",
			wantErr:   true,
		},
		{
			name:      "misspelled result key errors (protocol drift)",
			payload:   `{"name":"strath","stoped":["a"]}`,
			resultKey: "stopped",
			wantErr:   true,
		},
		{
			name:      "empty payload errors",
			payload:   "",
			resultKey: "stopped",
			wantErr:   true,
		},
		{
			name:      "null payload errors",
			payload:   "null",
			resultKey: "stopped",
			wantErr:   true,
		},
		{
			name:      "result key wrong type errors",
			payload:   `{"name":"strath","stopped":"not-a-list"}`,
			resultKey: "stopped",
			wantErr:   true,
		},
		{
			name:      "malformed payload errors",
			payload:   `{"name":`,
			resultKey: "stopped",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeLifecycleResult(json.RawMessage(tt.payload), tt.resultKey)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (result=%v)", got)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil && got != nil {
				t.Fatalf("got %v, want nil slice", got)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}

			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

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

func TestParseAndBuildScenarioRepoValidation(t *testing.T) {
	tests := []struct {
		name       string
		sessions   string
		wantErr    string
		wantShared bool
		wantMirror string
	}{
		{
			name: "repo-less shared member",
			sessions: `[[sessions]]
name = "braw"
shared = true`,
			wantShared: true,
		},
		{
			name: "repo-less mirrored member",
			sessions: `[[sessions]]
name = "croft"
repo = "/tmp/croft"

[[sessions]]
name = "canny"
mirror = "croft"`,
			wantMirror: "croft",
		},
		{
			name: "repo-less ordinary member",
			sessions: `[[sessions]]
name = "dreich"`,
			wantErr: "repo is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte("version = 1\n[scenario]\nname = \"strath\"\n" + tt.sessions)

			sf, err := parseScenarioFile(data)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseScenarioFile error = %v, want substring %q", err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("parseScenarioFile: %v", err)
			}

			inputs, err := buildSessionInputs(sf)
			if err != nil {
				t.Fatalf("buildSessionInputs: %v", err)
			}

			got := inputs[len(inputs)-1]
			if got.Repo != "" {
				t.Errorf("derived repo input = %q, want empty", got.Repo)
			}

			if got.Shared != tt.wantShared {
				t.Errorf("shared = %t, want %t", got.Shared, tt.wantShared)
			}

			if got.Mirror != tt.wantMirror {
				t.Errorf("mirror = %q, want %q", got.Mirror, tt.wantMirror)
			}
		})
	}
}

func TestParseAndBuildScenarioResults(t *testing.T) {
	data := []byte(`
version = 1
[scenario]
name = "kirk"
[[sessions]]
name = "canny"
repo = "/tmp/croft"
prompt = "Inspect the croft and publish the review."
[[sessions.results]]
name = "review"
format = "markdown"
store = "{session_name}/review.md"
required = true
`)

	sf, err := parseScenarioFile(data)
	if err != nil {
		t.Fatal(err)
	}

	inputs, err := buildSessionInputs(sf)
	if err != nil {
		t.Fatal(err)
	}

	if len(inputs[0].Results) != 1 {
		t.Fatalf("results = %+v", inputs[0].Results)
	}

	if inputs[0].Prompt != "Inspect the croft and publish the review." || inputs[0].Task != "" {
		t.Fatalf("prompt/task = %q/%q", inputs[0].Prompt, inputs[0].Task)
	}

	result := inputs[0].Results[0]
	if result.Name != "review" || result.Format != "markdown" || !result.Required {
		t.Fatalf("result = %+v", result)
	}
}

func TestParseScenarioFileRuntimePolicy(t *testing.T) {
	data := []byte(`
version = 1

[scenario]
name = "strath"

[scenario.policy]
completion = "quorum"
quorum = 2
on_exhausted = "fail"

[[sessions]]
name = "braw"
repo = "/tmp/croft"
task = "review"

[sessions.policy]
timeout = "30s"
retries = 2

[[sessions]]
name = "canny"
repo = "/tmp/bothy"
task = "review"

[sessions.policy]
required = false
timeout = "1m"
`)

	sf, err := parseScenarioFile(data)
	if err != nil {
		t.Fatal(err)
	}

	if sf.Scenario.Policy == nil || sf.Scenario.Policy.Quorum != 2 || sf.Scenario.Policy.OnExhausted != "fail" {
		t.Fatalf("scenario policy = %+v", sf.Scenario.Policy)
	}

	inputs, err := buildSessionInputs(sf)
	if err != nil {
		t.Fatal(err)
	}

	if inputs[0].Policy == nil || inputs[0].Policy.Timeout != "30s" || inputs[0].Policy.Retries != 2 {
		t.Fatalf("required input policy = %+v", inputs[0].Policy)
	}

	if inputs[1].Policy == nil || inputs[1].Policy.Required == nil || *inputs[1].Policy.Required {
		t.Fatalf("optional input policy = %+v", inputs[1].Policy)
	}
}

func TestParseScenarioFileRejectsRuntimePolicy(t *testing.T) {
	data := []byte(`
version = 1
[scenario]
name = "strath"
[scenario.policy]
completion = "quorum"
quorum = 3
[[sessions]]
name = "braw"
repo = "/tmp/croft"
`)

	_, err := parseScenarioFile(data)
	if err == nil || !strings.Contains(err.Error(), "exceeds member count") {
		t.Fatalf("error = %v, want excessive quorum", err)
	}
}

func TestParseScenarioFileRejectsPolicyWithoutResultContract(t *testing.T) {
	data := []byte(`
version = 1
[scenario]
name = "strath"
[scenario.policy]
completion = "all"
[[sessions]]
name = "braw"
repo = "/tmp/croft"
`)

	_, err := parseScenarioFile(data)
	if err == nil || !strings.Contains(err.Error(), "non-empty task") {
		t.Fatalf("error = %v, want missing result contract", err)
	}
}

func TestFormatScenarioPolicyStatus(t *testing.T) {
	policy := &protocol.ScenarioPolicyInfo{
		Completion: "quorum", Quorum: 2, OnExhausted: "fail", Paused: true,
		Successful: 1, RequiredSuccessful: 1, RequiredTotal: 1,
	}
	if got := formatScenarioPolicySummary(policy); got != "quorum 2; 1 successful; required 1/1; on exhausted fail; paused" {
		t.Fatalf("summary = %q", got)
	}

	if got := formatScenarioPolicyProgress(policy); got != "1/2 quorum; required 1/1" {
		t.Fatalf("progress = %q", got)
	}

	required, attempt, deadline, result := formatScenarioMemberPolicy(&protocol.ScenarioMemberPolicyInfo{
		Required: true, Attempt: 2, MaxAttempts: 3, Deadline: "2026-07-17T18:30:00Z",
		ExhaustionReason: "attempt 2 timed out",
	})

	if required != "yes" || attempt != "2/3" || deadline != "2026-07-17T18:30:00Z" || result != "attempt 2 timed out" {
		t.Fatalf("member status = %q %q %q %q", required, attempt, deadline, result)
	}
}

func TestScenarioPolicyColumnsAppendToLegacyHumanOutput(t *testing.T) {
	if !strings.HasPrefix(scenarioStatusTableHeader, "NAME\tSESSION\tSTATUS\tAGENT\tROLE\tPROGRESS\tWAITING ON\tMIRROR\tRESULTS\tSHARED\t") {
		t.Fatalf("status header moved legacy columns: %q", scenarioStatusTableHeader)
	}

	if !strings.HasPrefix(scenarioListTableHeader, "  NAME\tID\tSTATUS\tSESSIONS\tGOAL\t") {
		t.Fatalf("list header moved legacy columns: %q", scenarioListTableHeader)
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
	if err := os.MkdirAll(scenarioDir, 0o750); err != nil {
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
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Write an invalid file — should be skipped.
	if err := os.WriteFile(filepath.Join(scenarioDir, "bad.toml"), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Write a non-TOML file — should be skipped.
	if err := os.WriteFile(filepath.Join(scenarioDir, "readme.md"), []byte("# hi"), 0o600); err != nil {
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
	if err := os.MkdirAll(scenarioDir, 0o750); err != nil {
		t.Fatal(err)
	}

	content := []byte("test content")
	if err := os.WriteFile(filepath.Join(scenarioDir, "strath.toml"), content, 0o600); err != nil {
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
	if err := os.WriteFile(directFile, []byte("direct"), 0o600); err != nil {
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

// TestBuildSessionInputsCov2 verifies the full mapping from parsed TOML sessions
// to protocol inputs: field passthrough, ~-expansion of repo paths, the
// agent_hooks default (nil → true) vs an explicit false, and the shared flag.
func TestBuildSessionInputsCov2(t *testing.T) {
	hooksOff := false

	sf := &scenarioFile{
		Version:  1,
		Scenario: scenarioFileMeta{Name: "strath", Goal: "wire the clachan"},
		Sessions: []scenarioFileSession{
			{
				Name:  "bairn",
				Repo:  "~/Code/croft",
				Agent: "claude",
				Model: "claude-opus-4-8",
				Base:  "main",
				Role:  "Backend engineer",
				Task:  "Add ingest",
				// AgentHooks omitted → defaults to true.
			},
			{
				Name:       "canny",
				Repo:       "/tmp/bothy",
				AgentHooks: &hooksOff,
				Shared:     true,
			},
		},
	}

	got, err := buildSessionInputs(sf)
	if err != nil {
		t.Fatalf("buildSessionInputs: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d inputs, want 2", len(got))
	}

	s0 := got[0]
	if s0.Name != "bairn" || s0.Agent != "claude" || s0.Model != "claude-opus-4-8" ||
		s0.Base != "main" || s0.Role != "Backend engineer" || s0.Task != "Add ingest" {
		t.Errorf("session[0] fields not mapped correctly: %+v", s0)
	}

	if wantRepo := config.ExpandPath("~/Code/croft"); s0.Repo != wantRepo {
		t.Errorf("session[0].Repo = %q, want expanded %q", s0.Repo, wantRepo)
	}

	if !strings.HasPrefix(s0.Repo, "/") {
		t.Errorf("expected ~ expanded to an absolute path, got %q", s0.Repo)
	}

	if !s0.AgentHooks {
		t.Error("session[0].AgentHooks should default to true when omitted")
	}

	if s0.Shared {
		t.Error("session[0].Shared should be false")
	}

	s1 := got[1]
	if s1.AgentHooks {
		t.Error("session[1].AgentHooks should be false (explicitly set)")
	}

	if !s1.Shared {
		t.Error("session[1].Shared should be true")
	}

	if s1.Repo != "/tmp/bothy" {
		t.Errorf("session[1].Repo = %q, want /tmp/bothy unchanged", s1.Repo)
	}
}

// TestBuildSessionInputsIncludesAndStar verifies the includes/star fields are
// mapped through, and that include paths are ~-expanded like repo (issue #1046).
func TestBuildSessionInputsIncludesAndStar(t *testing.T) {
	sf := &scenarioFile{
		Version:  1,
		Scenario: scenarioFileMeta{Name: "strath"},
		Sessions: []scenarioFileSession{
			{
				Name:     "ben",
				Repo:     "~/Code/croft",
				Includes: []string{"~/Code/bothy", "/tmp/glen"},
				Star:     true,
			},
			{
				Name: "canny",
				Repo: "/tmp/whin",
			},
		},
	}

	got, err := buildSessionInputs(sf)
	if err != nil {
		t.Fatalf("buildSessionInputs: %v", err)
	}

	s0 := got[0]
	if !s0.Star {
		t.Error("session[0].Star should be true")
	}

	if len(s0.Includes) != 2 {
		t.Fatalf("session[0].Includes = %v, want 2", s0.Includes)
	}

	if want := config.ExpandPath("~/Code/bothy"); s0.Includes[0] != want {
		t.Errorf("session[0].Includes[0] = %q, want expanded %q", s0.Includes[0], want)
	}

	if !strings.HasPrefix(s0.Includes[0], "/") {
		t.Errorf("expected include ~ expanded to absolute path, got %q", s0.Includes[0])
	}

	if s0.Includes[1] != "/tmp/glen" {
		t.Errorf("session[0].Includes[1] = %q, want /tmp/glen", s0.Includes[1])
	}

	// Defaults for a session with no includes/star.
	if got[1].Star {
		t.Error("session[1].Star should default false")
	}

	if len(got[1].Includes) != 0 {
		t.Errorf("session[1].Includes = %v, want none", got[1].Includes)
	}
}

func TestParseScenarioFileCompletionLifecycle(t *testing.T) {
	sf, err := parseScenarioFile([]byte(`
version = 1
[scenario]
name = "braw"
[scenario.lifecycle]
cleanup = "on_success"
delay = "10m"
[[sessions]]
name = "ben"
repo = "/tmp/croft"
[[trigger]]
name = "archive"
[trigger.completion]
session = "ben"
[trigger.action]
type = "command"
command = "true"
`))
	if err != nil {
		t.Fatal(err)
	}

	if sf.Scenario.Lifecycle.CleanupMode() != config.ScenarioCleanupOnSuccess ||
		sf.Scenario.Lifecycle.DelayDuration() != 10*time.Minute ||
		len(sf.Triggers) != 1 || !sf.Triggers[0].IsCompletion() {
		t.Fatalf("parsed scenario = %+v", sf)
	}
}

func TestBuildSessionInputsDependencies(t *testing.T) {
	sf := &scenarioFile{
		Version:  1,
		Scenario: scenarioFileMeta{Name: "strath"},
		Sessions: []scenarioFileSession{
			{Name: "braw", Repo: "/tmp/croft", Task: "build"},
			{Name: "canny", Repo: "/tmp/bothy", Task: "inspect", DependsOn: []string{"braw"}},
		},
	}

	inputs, err := buildSessionInputs(sf)
	if err != nil {
		t.Fatalf("buildSessionInputs: %v", err)
	}

	if len(inputs[1].DependsOn) != 1 || inputs[1].DependsOn[0] != "braw" {
		t.Fatalf("depends_on = %v", inputs[1].DependsOn)
	}
}

func TestBuildSessionInputsMirror(t *testing.T) {
	sf := &scenarioFile{Sessions: []scenarioFileSession{
		{Name: "subject", Repo: "/tmp/croft", Shared: true},
		{Name: "reader", Mirror: "subject", Agent: "codex"},
	}}

	got, err := buildSessionInputs(sf)
	if err != nil {
		t.Fatalf("buildSessionInputs: %v", err)
	}

	if got[1].Mirror != "subject" || got[1].Repo != "" {
		t.Errorf("mirrored input = %+v, want mirror subject and derived repo", got[1])
	}
}

func TestBuildSessionInputsMirrorRejectsConflictingRepo(t *testing.T) {
	sf := &scenarioFile{Sessions: []scenarioFileSession{
		{Name: "subject", Repo: "/tmp/croft"},
		{Name: "reader", Repo: "/tmp/other", Mirror: "subject"},
	}}

	_, err := buildSessionInputs(sf)
	if err == nil || !strings.Contains(err.Error(), "mirror and repo") {
		t.Fatalf("error = %v, want mirror/repo conflict", err)
	}
}

// TestBuildSessionInputsCov2MissingName verifies a session without a name is
// rejected with an index-qualified error.
func TestBuildSessionInputsCov2MissingName(t *testing.T) {
	sf := &scenarioFile{
		Sessions: []scenarioFileSession{{Repo: "/tmp/croft"}},
	}

	_, err := buildSessionInputs(sf)
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("expected missing-name error, got %v", err)
	}
}

// TestBuildSessionInputsCov2MissingRepo verifies a named session without a repo
// is rejected with a name-qualified error.
func TestBuildSessionInputsCov2MissingRepo(t *testing.T) {
	sf := &scenarioFile{
		Sessions: []scenarioFileSession{{Name: "thrawn"}},
	}

	_, err := buildSessionInputs(sf)
	if err == nil || !strings.Contains(err.Error(), "repo is required") {
		t.Errorf("expected missing-repo error, got %v", err)
	}

	if !strings.Contains(err.Error(), "thrawn") {
		t.Errorf("error should name the offending session, got %v", err)
	}
}

// TestScenariosDirCov2 verifies scenariosDir derives the scenarios subdir from
// the resolved config file's directory.
func TestScenariosDirCov2(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	dir := t.TempDir()
	paths.ConfigFile = filepath.Join(dir, "config.toml")

	if got, want := scenariosDir(), filepath.Join(dir, "scenarios"); got != want {
		t.Errorf("scenariosDir() = %q, want %q", got, want)
	}
}

// TestResolveScenarioSourceCov2 verifies the package-level resolveScenarioSource
// (which uses scenariosDir()) resolves a name to a file under the config dir's
// scenarios/ folder.
func TestResolveScenarioSourceCov2(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	base := t.TempDir()
	paths.ConfigFile = filepath.Join(base, "config.toml")

	scDir := filepath.Join(base, "scenarios")
	if err := os.MkdirAll(scDir, 0o750); err != nil {
		t.Fatal(err)
	}

	content := []byte("frae the clachan")
	if err := os.WriteFile(filepath.Join(scDir, "strath.toml"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := resolveScenarioSource("strath")
	if err != nil {
		t.Fatalf("resolveScenarioSource: %v", err)
	}

	if string(data) != "frae the clachan" {
		t.Errorf("data = %q", data)
	}
}

// TestResolveScenarioSourceFromCov2StorePrefix verifies the store: prefix is
// rejected with a not-yet-implemented error rather than silently mishandled.
func TestResolveScenarioSourceFromCov2StorePrefix(t *testing.T) {
	_, err := resolveScenarioSourceFrom("store:scenarios/strath.toml", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("expected store: prefix error, got %v", err)
	}
}

// TestResolveScenarioSourceFromCov2Stdin verifies the "-" source reads the whole
// scenario definition from stdin.
func TestResolveScenarioSourceFromCov2Stdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	orig := os.Stdin
	os.Stdin = r

	defer func() { os.Stdin = orig }()

	go func() {
		_, _ = w.WriteString("version = 1\n")
		_ = w.Close()
	}()

	data, err := resolveScenarioSourceFrom("-", t.TempDir())
	if err != nil {
		t.Fatalf("resolveScenarioSourceFrom(-): %v", err)
	}

	if string(data) != "version = 1\n" {
		t.Errorf("stdin data = %q", data)
	}

	_ = r.Close()
}

// TestListAvailableScenariosCov2 verifies the package-level wrapper reads the
// scenarios dir derived from paths.ConfigFile.
func TestListAvailableScenariosCov2(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	base := t.TempDir()
	paths.ConfigFile = filepath.Join(base, "config.toml")

	scDir := filepath.Join(base, "scenarios")
	if err := os.MkdirAll(scDir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(scDir, "clachan.toml"), []byte(`
version = 1

[scenario]
name = "clachan"
goal = "gather the glen"

[[sessions]]
name = "braw"
repo = "/tmp/croft"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := listAvailableScenarios()
	if len(got) != 1 {
		t.Fatalf("expected 1 available scenario, got %d (%+v)", len(got), got)
	}

	if got[0].Name != "clachan" || got[0].Goal != "gather the glen" {
		t.Errorf("unexpected scenario: %+v", got[0])
	}
}

type fakeScenarioResultClient struct {
	response protocol.Envelope
	sendErr  error
	readErr  error
	msgType  string
	payload  protocol.ScenarioResultPublishMsg
	closed   bool
}

func (f *fakeScenarioResultClient) SendControl(msgType string, payload any) error {
	f.msgType = msgType
	if publish, ok := payload.(protocol.ScenarioResultPublishMsg); ok {
		f.payload = publish
	}

	return f.sendErr
}

func (f *fakeScenarioResultClient) ReadControlResponse() (protocol.Envelope, error) {
	return f.response, f.readErr
}

func (f *fakeScenarioResultClient) Close() { f.closed = true }

func scenarioResultEnvelope(t *testing.T, msgType string, payload any) protocol.Envelope {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s response: %v", msgType, err)
	}

	return protocol.Envelope{Type: msgType, Payload: raw}
}

func withScenarioResultCommandTest(t *testing.T, fake *fakeScenarioResultClient, jsonMode bool) *bytes.Buffer {
	t.Helper()

	origConnect := scenarioResultConnect
	origOut := out
	origJSON := jsonOutput
	origFile := scenarioResultFile
	origScenario := scenarioResultScenario

	var buf bytes.Buffer

	scenarioResultConnect = func() (scenarioResultControlClient, error) { return fake, nil }
	jsonOutput = jsonMode
	out = output.NewWithWriter(jsonMode, &buf)
	scenarioResultFile = ""
	scenarioResultScenario = ""

	t.Cleanup(func() {
		scenarioResultConnect = origConnect
		out = origOut
		jsonOutput = origJSON
		scenarioResultFile = origFile
		scenarioResultScenario = origScenario
	})

	return &buf
}

func TestScenarioResultPutCommandPublishesAuthenticatedShape(t *testing.T) {
	fake := &fakeScenarioResultClient{response: scenarioResultEnvelope(t, "scenario_result_published", protocol.ScenarioResultPublishResponse{
		Scenario: "braw-fanout", Member: "canny",
		Result: protocol.ScenarioResultInfo{
			Name: "review", Format: "markdown", Destination: "scenarios/sc-braw/results/canny/review.md",
			Required: true, Status: "available", SizeBytes: 13,
		},
	})}
	buf := withScenarioResultCommandTest(t, fake, false)
	t.Setenv("GRAITH_SCENARIO_NAME", "braw-fanout")

	if err := scenarioResultPutCmd.RunE(scenarioResultPutCmd, []string{"review", "# Braw review"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if fake.msgType != "scenario_result_publish" {
		t.Fatalf("message type = %q", fake.msgType)
	}

	if fake.payload.Scenario != "braw-fanout" || fake.payload.Name != "review" || fake.payload.Body != "# Braw review" {
		t.Fatalf("payload = %+v", fake.payload)
	}

	if !fake.closed {
		t.Error("client was not closed")
	}

	if got := buf.String(); !strings.Contains(got, "Published markdown result") || !strings.Contains(got, fake.responsePayloadDestination(t)) {
		t.Fatalf("output = %q", got)
	}
}

func (f *fakeScenarioResultClient) responsePayloadDestination(t *testing.T) string {
	t.Helper()

	var response protocol.ScenarioResultPublishResponse
	if err := protocol.DecodePayload(f.response, &response); err != nil {
		t.Fatalf("decode fake response: %v", err)
	}

	return response.Result.Destination
}

func TestScenarioResultPutCommandErrors(t *testing.T) {
	t.Run("daemon rejection", func(t *testing.T) {
		fake := &fakeScenarioResultClient{response: scenarioResultEnvelope(t, "error", protocol.ErrorMsg{Message: "result is not valid JSON"})}
		withScenarioResultCommandTest(t, fake, false)

		err := scenarioResultPutCmd.RunE(scenarioResultPutCmd, []string{"facts", "{"})
		if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("send failure", func(t *testing.T) {
		fake := &fakeScenarioResultClient{sendErr: errors.New("dreich send")}
		withScenarioResultCommandTest(t, fake, false)

		err := scenarioResultPutCmd.RunE(scenarioResultPutCmd, []string{"review", "braw"})
		if err == nil || !strings.Contains(err.Error(), "dreich send") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		fake := &fakeScenarioResultClient{}
		withScenarioResultCommandTest(t, fake, false)

		err := scenarioResultPutCmd.RunE(scenarioResultPutCmd, []string{
			"review", strings.Repeat("x", protocol.MaxScenarioResultBodyBytes+1),
		})
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("error = %v", err)
		}

		if fake.msgType != "" {
			t.Fatal("oversized body should be rejected before connecting")
		}
	})
}

func TestFormatScenarioResultStatus(t *testing.T) {
	if got := formatScenarioResultStatus(nil); got != "—" {
		t.Fatalf("empty status = %q", got)
	}

	got := formatScenarioResultStatus([]protocol.ScenarioResultInfo{
		{Name: "notes", Status: "pending"},
		{Name: "review", Status: "available"},
		{Name: "facts", Status: "invalid"},
		{Name: "archive", Status: "failed"},
	})
	if got != "notes=pending,review=available,facts=invalid,archive=failed" {
		t.Fatalf("status = %q", got)
	}
}
