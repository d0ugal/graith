package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

type fakeAgentUseCase struct {
	catalog      protocol.AgentCatalogResponseMsg
	catalogErr   error
	info         protocol.AgentInfoResponseMsg
	infoErr      error
	infoRequests []protocol.AgentInfoMsg
}

func (f *fakeAgentUseCase) AgentCatalog() (protocol.AgentCatalogResponseMsg, error) {
	if f.catalogErr != nil {
		return protocol.AgentCatalogResponseMsg{}, f.catalogErr
	}

	return f.catalog, nil
}

func (f *fakeAgentUseCase) AgentInfo(req protocol.AgentInfoMsg) (protocol.AgentInfoResponseMsg, error) {
	f.infoRequests = append(f.infoRequests, req)
	if f.infoErr != nil {
		return protocol.AgentInfoResponseMsg{}, f.infoErr
	}

	return f.info, nil
}

func agentTestCommand(t *testing.T, jsonMode bool, buf *bytes.Buffer, fake *fakeAgentUseCase) *cobra.Command {
	t.Helper()

	cmd := &cobra.Command{}
	cmd.SetContext(withCommandDependencies(context.Background(), commandDependencies{
		out:   output.NewWithWriter(jsonMode, buf),
		agent: fake,
	}))

	return cmd
}

func TestAgentCommandAliases(t *testing.T) {
	registerCommands()

	for _, args := range [][]string{{"agent", "list"}, {"agent", "ls"}} {
		cmd, _, err := rootCmd.Find(args)
		if err != nil {
			t.Fatalf("find %v: %v", args, err)
		}

		if cmd != agentListCmd {
			t.Fatalf("%v resolved to %q, want agent list", args, cmd.Name())
		}
	}
}

func TestRunAgentListHuman(t *testing.T) {
	fake := &fakeAgentUseCase{catalog: protocol.AgentCatalogResponseMsg{
		DefaultAgent: "cursor",
		Agents: []protocol.AgentCatalogEntry{
			{Name: "claude", Command: "claude"},
			{Name: "cursor", Command: "agent", InfoKeys: []string{"model", "version"}},
		},
	}}

	var buf bytes.Buffer

	cmd := agentTestCommand(t, false, &buf, fake)

	if err := runAgentList(cmd, nil); err != nil {
		t.Fatalf("runAgentList: %v", err)
	}

	got := buf.String()
	for _, want := range []string{"NAME", "DEFAULT", "COMMAND", "INFO", "cursor", "*", "agent", "model,version"} {
		if !strings.Contains(got, want) {
			t.Fatalf("agent list output = %q, want substring %q", got, want)
		}
	}
}

func TestRunAgentListJSON(t *testing.T) {
	fake := &fakeAgentUseCase{catalog: protocol.AgentCatalogResponseMsg{
		DefaultAgent: "claude",
		Agents:       []protocol.AgentCatalogEntry{{Name: "claude", Command: "claude"}},
	}}

	var buf bytes.Buffer

	cmd := agentTestCommand(t, true, &buf, fake)

	if err := runAgentList(cmd, nil); err != nil {
		t.Fatalf("runAgentList: %v", err)
	}

	var got protocol.AgentCatalogResponseMsg
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal list JSON: %v\n%s", err, buf.String())
	}

	if got.DefaultAgent != "claude" || len(got.Agents) != 1 || got.Agents[0].Name != "claude" {
		t.Fatalf("list JSON = %+v, want claude catalog", got)
	}
}

func TestRunAgentInfoRequestsKeyAndPrintsSingleOutput(t *testing.T) {
	fake := &fakeAgentUseCase{info: protocol.AgentInfoResponseMsg{
		Agent: "cursor",
		Results: []protocol.AgentInfoResult{{
			Key:    "model",
			Stdout: "model-a - Model A",
		}},
	}}

	var buf bytes.Buffer

	cmd := agentTestCommand(t, false, &buf, fake)

	if err := runAgentInfo(cmd, []string{"cursor", "model"}); err != nil {
		t.Fatalf("runAgentInfo: %v", err)
	}

	if !reflect.DeepEqual(fake.infoRequests, []protocol.AgentInfoMsg{{Agent: "cursor", Key: "model"}}) {
		t.Fatalf("requests = %+v, want cursor model", fake.infoRequests)
	}

	if got := buf.String(); got != "model-a - Model A\n" {
		t.Fatalf("info output = %q, want raw stdout plus newline", got)
	}
}

func TestRunAgentInfoJSON(t *testing.T) {
	fake := &fakeAgentUseCase{info: protocol.AgentInfoResponseMsg{
		Agent: "cursor",
		Results: []protocol.AgentInfoResult{{
			Key:      "version",
			Command:  "agent",
			Args:     []string{"-v"},
			Stdout:   "agent 1.2.3\n",
			ExitCode: 0,
		}},
	}}

	var buf bytes.Buffer

	cmd := agentTestCommand(t, true, &buf, fake)

	if err := runAgentInfo(cmd, []string{"cursor"}); err != nil {
		t.Fatalf("runAgentInfo: %v", err)
	}

	if !reflect.DeepEqual(fake.infoRequests, []protocol.AgentInfoMsg{{Agent: "cursor"}}) {
		t.Fatalf("requests = %+v, want cursor all-keys request", fake.infoRequests)
	}

	var got protocol.AgentInfoResponseMsg
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal info JSON: %v\n%s", err, buf.String())
	}

	if got.Agent != "cursor" || len(got.Results) != 1 || got.Results[0].Key != "version" {
		t.Fatalf("info JSON = %+v, want cursor version result", got)
	}
}

func TestRunAgentInfoReturnsFailureAfterRenderingHumanOutput(t *testing.T) {
	fake := &fakeAgentUseCase{info: protocol.AgentInfoResponseMsg{
		Agent: "cursor",
		Results: []protocol.AgentInfoResult{{
			Key:      "model",
			Command:  "agent",
			Args:     []string{"--list-models"},
			Stderr:   "dreich provider failure\n",
			ExitCode: 4,
			Error:    "agent info cursor.model failed with exit code 4: exit status 4",
		}},
	}}

	var buf bytes.Buffer

	cmd := agentTestCommand(t, false, &buf, fake)

	err := runAgentInfo(cmd, []string{"cursor", "model"})
	if err == nil || err.Error() != "agent info cursor.model failed" {
		t.Fatalf("runAgentInfo error = %v, want failed model summary", err)
	}

	got := buf.String()
	for _, want := range []string{
		"error: agent info cursor.model failed with exit code 4",
		"stderr:\ndreich provider failure",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("info output = %q, want substring %q", got, want)
		}
	}
}

func TestRunAgentInfoPrintsTruncationMarkers(t *testing.T) {
	fake := &fakeAgentUseCase{info: protocol.AgentInfoResponseMsg{
		Agent: "cursor",
		Results: []protocol.AgentInfoResult{{
			Key:             "model",
			Stdout:          "stdout",
			Stderr:          "stderr",
			StdoutTruncated: true,
			StderrTruncated: true,
		}},
	}}

	var buf bytes.Buffer

	cmd := agentTestCommand(t, false, &buf, fake)

	if err := runAgentInfo(cmd, []string{"cursor", "model"}); err != nil {
		t.Fatalf("runAgentInfo: %v", err)
	}

	got := buf.String()
	for _, want := range []string{"stdout\n[stdout truncated]", "stderr:\nstderr\n[stderr truncated]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("info output = %q, want substring %q", got, want)
		}
	}
}

func TestRunAgentInfoReturnsFailureAfterRenderingJSON(t *testing.T) {
	fake := &fakeAgentUseCase{info: protocol.AgentInfoResponseMsg{
		Agent: "cursor",
		Results: []protocol.AgentInfoResult{
			{Key: "model", Stdout: "model-a\n", ExitCode: 0},
			{Key: "version", ExitCode: 1, Error: "agent info cursor.version failed with exit code 1: exit status 1"},
		},
	}}

	var buf bytes.Buffer

	cmd := agentTestCommand(t, true, &buf, fake)

	err := runAgentInfo(cmd, []string{"cursor"})
	if err == nil || err.Error() != "agent info cursor.version failed" {
		t.Fatalf("runAgentInfo error = %v, want failed version summary", err)
	}

	var got protocol.AgentInfoResponseMsg
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal info JSON: %v\n%s", err, buf.String())
	}

	if len(got.Results) != 2 || got.Results[1].Error == "" {
		t.Fatalf("info JSON = %+v, want rendered failure result", got)
	}
}

func TestRunAgentInfoSurfacesUseCaseError(t *testing.T) {
	fake := &fakeAgentUseCase{infoErr: errors.New("unknown info key")}

	var buf bytes.Buffer

	cmd := agentTestCommand(t, false, &buf, fake)

	err := runAgentInfo(cmd, []string{"cursor", "bogus"})
	if err == nil || err.Error() != "unknown info key" {
		t.Fatalf("runAgentInfo error = %v, want unknown info key", err)
	}
}
