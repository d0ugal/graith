package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

type fakeDependencyHealthUseCase struct {
	response protocol.DependencyStatusResponseMsg
}

func (fake *fakeDependencyHealthUseCase) Status() (protocol.DependencyStatusResponseMsg, error) {
	return fake.response, nil
}

func TestRunDependencyStatusJSONPreservesResponse(t *testing.T) {
	tests := map[string]struct {
		response protocol.DependencyStatusResponseMsg
		want     protocol.DependencyStatusResponseMsg
	}{
		"empty services": {
			response: protocol.DependencyStatusResponseMsg{SchemaVersion: 1, Services: []protocol.DependencyStatusService{}},
			want:     protocol.DependencyStatusResponseMsg{SchemaVersion: 1, Services: []protocol.DependencyStatusService{}},
		},
		"incident IDs": {
			response: protocol.DependencyStatusResponseMsg{SchemaVersion: 1, Services: []protocol.DependencyStatusService{{
				Name: "claude", SourceURL: "https://status.example", ObservedState: "operational", SourceHealth: "fresh", IncidentIDs: []string{"inc-1", "inc-2"},
			}}},
			want: protocol.DependencyStatusResponseMsg{SchemaVersion: 1, Services: []protocol.DependencyStatusService{{
				Name: "claude", SourceURL: "https://status.example", ObservedState: "operational", SourceHealth: "fresh", IncidentIDs: []string{"inc-1", "inc-2"},
			}}},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer

			deps := commandDependencies{cfg: config.Default(), out: output.NewWithWriter(true, &buf), health: &fakeDependencyHealthUseCase{response: test.response}}
			cmd := &cobra.Command{}
			cmd.SetContext(withCommandDependencies(context.Background(), deps))

			if err := runDependencyStatus(cmd, nil); err != nil {
				t.Fatal(err)
			}

			var got protocol.DependencyStatusResponseMsg
			if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
				t.Fatalf("decode JSON: %v\n%s", err, buf.String())
			}

			if !reflect.DeepEqual(test.want, got) {
				t.Errorf("JSON response = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestRenderDependencyStatusCompactTable(t *testing.T) {
	var buf bytes.Buffer
	renderDependencyStatus(output.NewWithWriter(false, &buf), protocol.DependencyStatusResponseMsg{SchemaVersion: 1, Services: []protocol.DependencyStatusService{
		{Name: "claude", Provider: "statuspage", SourceURL: "https://status.example", AgentTypes: []string{"claude", "codex"}, ObservedState: "operational", SourceHealth: "fresh", ObservedAt: "2026-08-19T19:00:00Z", IncidentIDs: []string{"inc-1", "inc-2"}},
		{Name: "github", Provider: "statuspage", SourceURL: "https://githubstatus.example", Global: true, ObservedState: "degraded", SourceHealth: "stale", ObservedAt: "2026-08-19T18:00:00Z", IncidentIDs: []string{"inc-7"}},
	}}, false)

	got := buf.String()
	for _, want := range []string{
		"SERVICE", "STATE", "SOURCE HEALTH", "OBSERVED", "ROUTING",
		"claude", "operational", "fresh", "2026-08-19T19:00:00Z", "claude, codex",
		"github", "degraded", "stale", "2026-08-19T18:00:00Z", "all agents",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q missing %q", got, want)
		}
	}

	for _, unwanted := range []string{"inc-1", "inc-2", "inc-7", "status.example", "provider:"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("output %q unexpectedly contains %q", got, unwanted)
		}
	}

	if strings.Contains(got, "\x1b[") {
		t.Errorf("plain output contains ANSI escape sequence: %q", got)
	}
}

func TestRenderDependencyStatusColorsStateAndSourceHealth(t *testing.T) {
	var buf bytes.Buffer
	renderDependencyStatus(output.NewWithWriter(false, &buf), protocol.DependencyStatusResponseMsg{SchemaVersion: 1, Services: []protocol.DependencyStatusService{
		{Name: "braw", ObservedState: "operational", SourceHealth: "fresh"},
		{Name: "canny", ObservedState: "degraded", SourceHealth: "stale"},
		{Name: "dreich", ObservedState: "down", SourceHealth: "failed"},
		{Name: "thrawn", ObservedState: "unknown", SourceHealth: "unknown"},
	}}, true)

	if got := strings.Count(buf.String(), "\x1b["); got != 16 {
		t.Errorf("ANSI colour sequence count = %d, want 16: %q", got, buf.String())
	}
}

func TestDependencyStatusColorEnabledHonoursNoColorAndNonTTY(t *testing.T) {
	original := dependencyStatusNoColor

	t.Cleanup(func() { dependencyStatusNoColor = original })

	dependencyStatusNoColor = true

	if dependencyStatusColorEnabled(output.NewWithWriter(false, &bytes.Buffer{})) {
		t.Error("dependency status colour enabled with --no-color")
	}

	dependencyStatusNoColor = false

	if dependencyStatusColorEnabled(output.NewWithWriter(false, &bytes.Buffer{})) {
		t.Error("dependency status colour enabled for a non-terminal writer")
	}
}
