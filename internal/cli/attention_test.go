package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
)

func setupAttentionRunTest(t *testing.T, jsonMode bool, buf *bytes.Buffer, conn *scriptedConn) {
	t.Helper()

	origOut := out
	origJSON := jsonOutput
	origContext := attentionContext
	origClear := attentionClear
	origConnect := attentionConnectFn

	out = output.NewWithWriter(jsonMode, buf)
	jsonOutput = jsonMode
	attentionContext = ""
	attentionClear = false
	attentionConnectFn = func(*config.Config, config.Paths, string) (attentionConn, error) {
		return conn, nil
	}

	t.Cleanup(func() {
		out = origOut
		jsonOutput = origJSON
		attentionContext = origContext
		attentionClear = origClear
		attentionConnectFn = origConnect
	})
}

func TestRunAttentionSetsRequest(t *testing.T) {
	var buf bytes.Buffer

	conn := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("orchestrator_attention_response", protocol.OrchestratorAttentionResponse{
			Active: true,
			Text:   "Need release decision",
		})),
	}}
	setupAttentionRunTest(t, false, &buf, conn)

	attentionContext = "Use gr msg jail list"

	if err := runAttention([]string{"Need", "release", "decision"}); err != nil {
		t.Fatalf("runAttention() = %v", err)
	}

	if got := buf.String(); got != "Attention requested: Need release decision\n" {
		t.Fatalf("stdout = %q", got)
	}

	assertAttentionSend(t, conn, protocol.OrchestratorAttentionMsg{
		Text:    "Need release decision",
		Context: "Use gr msg jail list",
	})
}

func TestRunAttentionClearJSON(t *testing.T) {
	var buf bytes.Buffer

	conn := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("orchestrator_attention_response", protocol.OrchestratorAttentionResponse{})),
	}}
	setupAttentionRunTest(t, true, &buf, conn)

	attentionClear = true

	if err := runAttention(nil); err != nil {
		t.Fatalf("runAttention(clear) = %v", err)
	}

	var got attentionJSONResponse
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode attention JSON: %v\n%s", err, buf.String())
	}

	if got.Active {
		t.Fatalf("attention JSON active = true")
	}

	assertAttentionSend(t, conn, protocol.OrchestratorAttentionMsg{Clear: true})
}

func TestAttentionArgsRejectClearText(t *testing.T) {
	origClear := attentionClear
	attentionClear = true

	t.Cleanup(func() { attentionClear = origClear })

	err := attentionCmd.Args(attentionCmd, []string{"braw"})
	if err == nil || !strings.Contains(err.Error(), "--clear does not accept text") {
		t.Fatalf("Args(clear text) = %v", err)
	}
}

func TestAttentionArgsRejectClearContext(t *testing.T) {
	origClear := attentionClear
	origContext := attentionContext
	attentionClear = true
	attentionContext = "Use gr msg jail list"

	t.Cleanup(func() {
		attentionClear = origClear
		attentionContext = origContext
	})

	err := attentionCmd.Args(attentionCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--clear does not accept --context") {
		t.Fatalf("Args(clear context) = %v", err)
	}
}

func assertAttentionSend(t *testing.T, conn *scriptedConn, want protocol.OrchestratorAttentionMsg) {
	t.Helper()

	if len(conn.sends) != 1 || conn.sends[0].Type != "orchestrator_attention" {
		t.Fatalf("sends = %#v, want one orchestrator_attention", conn.sends)
	}

	got, ok := conn.sends[0].Payload.(protocol.OrchestratorAttentionMsg)
	if !ok {
		t.Fatalf("attention payload = %T, want protocol.OrchestratorAttentionMsg", conn.sends[0].Payload)
	}

	if got != want {
		t.Fatalf("attention payload = %+v, want %+v", got, want)
	}
}
