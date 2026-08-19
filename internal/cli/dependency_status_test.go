package cli

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestRunDependencyStatusJSONIsVersionedAndEmpty(t *testing.T) {
	var buf bytes.Buffer
	deps := commandDependencies{cfg: config.Default(), out: output.NewWithWriter(true, &buf), health: &fakeDependencyHealthUseCase{response: protocol.DependencyStatusResponseMsg{SchemaVersion: 1, Services: []protocol.DependencyStatusService{}}}}

	cmd := &cobra.Command{}
	cmd.SetContext(withCommandDependencies(context.Background(), deps))

	if err := runDependencyStatus(cmd, nil); err != nil {
		t.Fatal(err)
	}

	var got protocol.DependencyStatusResponseMsg
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, buf.String())
	}

	if got.SchemaVersion != 1 || got.Services == nil {
		t.Fatalf("response = %+v, want version 1 and non-nil services", got)
	}
}

func TestRenderDependencyStatusIncludesRoutingAndFailureDetails(t *testing.T) {
	var buf bytes.Buffer
	renderDependencyStatus(output.NewWithWriter(false, &buf), protocol.DependencyStatusResponseMsg{SchemaVersion: 1, Services: []protocol.DependencyStatusService{{
		Name: "github", Provider: "statuspage", SourceURL: "https://status.example", Global: true,
		ObservedState: "degraded", SourceHealth: "stale", LastFailureAt: "2026-08-19T19:00:00Z", IncidentIDs: []string{"inc-7"},
	}}})

	for _, want := range []string{"github", "all agents", "degraded", "stale", "2026-08-19T19:00:00Z", "inc-7"} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("output %q missing %q", buf.String(), want)
		}
	}
}
